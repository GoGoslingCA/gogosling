package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 插件子进程协议：每次调用启动一次 entry，向 stdin 写一行请求，从 stdout 读一行响应。
//
//	请求：{"tool": "<name>", "arguments": {...}}
//	响应：{"content": <string 或任意 JSON>, "is_error": <bool>}
//
// 子进程环境：白名单变量（config.ChildEnvAllowlist，全仓唯一定义）+ GOGOSLING_EXTENSION_ID /
// GOGOSLING_EXTENSION_DIR / GOGOSLING_PLUGIN_PROTOCOL。
const (
	// PluginProtocolVersion 是插件子进程协议的版本，通过环境变量告知插件。
	PluginProtocolVersion = "1"
	// DefaultPluginTimeout 是单次插件调用的默认超时。
	DefaultPluginTimeout = 60 * time.Second
	// DefaultMaxPluginOutput 是插件响应行的默认大小上限。
	DefaultMaxPluginOutput = 1 << 20

	envExtensionID    = "GOGOSLING_EXTENSION_ID"
	envExtensionDir   = "GOGOSLING_EXTENSION_DIR"
	envPluginProtocol = "GOGOSLING_PLUGIN_PROTOCOL"

	// maxPluginStderr 是保留用于日志与错误信息的 stderr 字节数。
	maxPluginStderr = 4096
	// pluginWaitDelay 是进程被结束后等待其输出管道关闭的宽限。
	pluginWaitDelay = 2 * time.Second
)

// pluginRequest 是写给插件 stdin 的一行。
type pluginRequest struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// pluginResponse 是插件 stdout 的一行。
type pluginResponse struct {
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

// ProcessSpec 描述一次插件子进程调用；ProcessRunner 的实现按它启动进程。
type ProcessSpec struct {
	ExtensionID string
	// Dir 是子进程的工作目录（插件安装目录）。
	Dir string
	// Command 是已解析的可执行文件路径或命令名；Args 是参数。
	Command string
	Args    []string
	// Env 是完整的环境（"K=V"），不继承父进程其他变量。
	Env []string
	// Stdin 是写入子进程标准输入的内容（一行请求）。
	Stdin []byte
	// Permissions 是插件声明的权限；沙箱实现据此约束进程。
	Permissions Permissions
	// Timeout 是整个调用的上限。
	Timeout time.Duration
	// MaxOutputBytes 是允许读取的 stdout 字节数；超出视为协议错误。
	MaxOutputBytes int
}

// ProcessRunner 启动插件子进程并返回它的标准输出。
//
// 默认实现 execRunner 直接 exec；集成阶段可换成 sandbox 包的实现，在 OS 级隔离内按 Permissions 执行。
// 实现必须：尊重 ctx 与 Timeout（超时返回 CodeTimeout）、把 Env 作为完整环境、限制输出大小。
type ProcessRunner interface {
	Run(ctx context.Context, spec ProcessSpec) ([]byte, error)
}

// execRunner 是不带沙箱的默认实现。
type execRunner struct {
	log *slog.Logger
}

// Run 实现 ProcessRunner。
func (r *execRunner) Run(ctx context.Context, spec ProcessSpec) ([]byte, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultPluginTimeout
	}
	maxOut := spec.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxPluginOutput
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdin = bytes.NewReader(spec.Stdin)
	stdout := &cappedBuffer{limit: maxOut}
	stderr := &cappedBuffer{limit: maxPluginStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = pluginWaitDelay
	configurePluginCmd(cmd)

	err := cmd.Run()
	if stderr.Len() > 0 {
		r.log.Debug("插件 stderr", "extension_id", spec.ExtensionID, "stderr", stderr.String())
	}
	switch {
	case ctx.Err() != nil:
		return nil, model.Wrap(model.CodeCancelled, "插件调用已取消", ctx.Err())
	case runCtx.Err() != nil:
		return nil, model.Errorf(model.CodeTimeout, "插件进程超过 %s 未返回", timeout)
	case err != nil:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, model.Errorf(model.CodeUnavailable, "插件进程退出码 %d: %s", exitErr.ExitCode(), stderr.String())
		}
		return nil, model.Wrap(model.CodeUnavailable, "启动插件进程失败", err)
	}
	if stdout.overflow {
		return nil, model.Errorf(model.CodeInvalid, "插件输出超过上限 %d 字节", maxOut)
	}
	return stdout.Bytes(), nil
}

// cappedBuffer 只保留前 limit 字节，并记录是否溢出；实现 io.Writer。
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if room <= 0 {
		b.overflow = true
		return len(p), nil
	}
	if len(p) > room {
		b.overflow = true
		b.buf.Write(p[:room])
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *cappedBuffer) Len() int       { return b.buf.Len() }
func (b *cappedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *cappedBuffer) String() string { return strings.TrimSpace(b.buf.String()) }

// resolveEntry 把清单里的 entry 解析为可执行路径：
//   - 绝对路径原样使用；
//   - 含路径分隔符或以 "." 开头的相对路径解析到插件目录内（不得逃逸）；
//   - 其余先看插件目录下是否有同名文件，否则在 PATH 中查找。
func resolveEntry(dir, entry string) (string, error) {
	if filepath.IsAbs(entry) {
		return entry, nil
	}
	relative := strings.ContainsAny(entry, `/\`) || strings.HasPrefix(entry, ".")
	if !relative {
		local := filepath.Join(dir, entry)
		if info, err := os.Stat(local); err == nil && !info.IsDir() {
			return local, nil
		}
		path, err := exec.LookPath(entry)
		if err != nil {
			return "", model.Errorf(model.CodeUnavailable, "插件入口 %q 不存在，也不在 PATH 中", entry)
		}
		return path, nil
	}
	cleaned := filepath.Clean(filepath.FromSlash(entry))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", model.Errorf(model.CodeInvalid, "插件入口 %q 不能指向插件目录之外", entry)
	}
	local := filepath.Join(dir, cleaned)
	info, err := os.Stat(local)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", model.Errorf(model.CodeUnavailable, "插件入口 %q 不存在", entry)
		}
		return "", model.Wrap(model.CodeUnavailable, "读取插件入口失败", err)
	}
	if info.IsDir() {
		return "", model.Errorf(model.CodeInvalid, "插件入口 %q 是目录", entry)
	}
	return local, nil
}

// pluginEnv 组装插件子进程的环境：config.ChildEnvAllowlist 中存在的变量 + 插件协议变量。
// lookup 通常是 os.LookupEnv。
func pluginEnv(ext *model.Extension, lookup func(string) (string, bool)) []string {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var out []string
	for _, k := range config.ChildEnvAllowlist() {
		if v, ok := lookup(k); ok {
			out = append(out, k+"="+v)
		}
	}
	out = append(out,
		envExtensionID+"="+ext.ID,
		envExtensionDir+"="+ext.InstallDir,
		envPluginProtocol+"="+PluginProtocolVersion,
	)
	return out
}

// callPlugin 执行一次插件工具调用。
func (m *Manager) callPlugin(ctx context.Context, ext *model.Extension, manifest PluginManifest, tool string, args json.RawMessage) (model.ToolResult, error) {
	command, err := resolveEntry(ext.InstallDir, manifest.Entry)
	if err != nil {
		return model.ToolResult{}, err
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return model.ToolResult{}, model.NewError(model.CodeInvalid, "工具参数不是合法的 JSON")
	}
	line, err := json.Marshal(pluginRequest{Tool: tool, Arguments: args})
	if err != nil {
		return model.ToolResult{}, model.Wrap(model.CodeInvalid, "编码插件请求失败", err)
	}
	line = append(line, '\n')

	out, err := m.runner.Run(ctx, ProcessSpec{
		ExtensionID:    ext.ID,
		Dir:            ext.InstallDir,
		Command:        command,
		Args:           append([]string(nil), manifest.Args...),
		Env:            pluginEnv(ext, m.lookupEnv),
		Stdin:          line,
		Permissions:    manifest.Permissions,
		Timeout:        m.pluginTimeout,
		MaxOutputBytes: m.maxPluginOutput,
	})
	if err != nil {
		return model.ToolResult{}, err
	}
	first := out
	if i := bytes.IndexByte(out, '\n'); i >= 0 {
		first = out[:i]
	}
	first = bytes.TrimSpace(first)
	if len(first) == 0 {
		return model.ToolResult{}, model.NewError(model.CodeUnavailable, "插件没有返回响应")
	}
	var resp pluginResponse
	if err := json.Unmarshal(first, &resp); err != nil {
		return model.ToolResult{}, model.Wrap(model.CodeInvalid, "插件响应不是合法的 JSON", err)
	}
	return model.ToolResult{Content: renderPluginContent(resp.Content), IsError: resp.IsError}, nil
}

// renderPluginContent 把响应里的 content 变成文本：字符串原样，其它 JSON 值紧凑序列化。
func renderPluginContent(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return string(trimmed)
	}
	return buf.String()
}
