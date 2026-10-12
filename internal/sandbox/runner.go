package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// DefaultTimeout 是 Spec.Timeout 为零时使用的超时。
	DefaultTimeout = 60 * time.Second
	// MaxTimeout 是允许的最长超时；更长的请求会被截到这个值。
	MaxTimeout = 10 * time.Minute
	// DefaultMaxOutputBytes 是 Spec.MaxOutputBytes 为零时每条输出流保留的最大字节数。
	DefaultMaxOutputBytes = 1 << 20
	// waitDelay 是进程结束后等待输出管道关闭的宽限期：子进程遗留的后台进程若仍持有管道，
	// 超过这个时间就强制关闭管道，避免 Wait 永久阻塞。
	waitDelay = 2 * time.Second
)

// Spec 描述一次受限执行。
type Spec struct {
	// Command 是 argv；Command[0] 是可执行文件（相对名在 PATH 中查找）。
	Command []string
	// RawCommandLine 只在 Windows 上生效：非空时作为完整命令行原样交给 CreateProcess
	// （SysProcAttr.CmdLine），Command[1:] 不再按 C 运行时规则转义拼接；Command[0] 仍用于定位可执行文件。
	// cmd.exe 不遵循 C 运行时的引号规则（\" 对它不是转义），带引号的命令文本只能这样传递。
	// 其他平台忽略该字段。
	RawCommandLine string
	// Dir 是工作目录，必须是已存在的目录（绝对路径）。
	Dir string
	// Env 是显式传入的环境变量，会覆盖白名单中的同名变量；键不得为空、含 "=" 或 NUL。
	Env map[string]string
	// Stdin 是喂给标准输入的内容；为空时标准输入连接到空设备，命令不会因等待输入而挂起。
	Stdin string
	// Timeout 是总运行时限；零值为 DefaultTimeout，上限 MaxTimeout。
	Timeout time.Duration
	// WritableDirs 是允许写入的目录（绝对路径）；Direct 无法强制执行，只有隔离实现才生效。
	WritableDirs []string
	// AllowNetwork 为 false 时隔离实现会切断网络；Direct 无法强制执行。
	AllowNetwork bool
	// MaxOutputBytes 是 stdout 与 stderr 各自保留的最大字节数；超出部分丢弃并标记 Truncated。
	MaxOutputBytes int
}

// Result 是一次执行的结果。
//
// 非零退出码与超时都是"正常结束"而不是错误：Run 只在命令无法启动、Spec 非法或
// 调用方的 ctx 被取消时返回 error。超时时 TimedOut 为 true、ExitCode 为 -1，已捕获的输出保留。
type Result struct {
	Stdout string
	Stderr string
	// ExitCode 是进程退出码；被信号结束或超时时为 -1。
	ExitCode int
	// Truncated 表示至少一条输出流超过了 MaxOutputBytes 而被截断。
	Truncated bool
	// TimedOut 表示进程因超过 Timeout 而被结束。
	TimedOut bool
	Duration time.Duration
}

// Runner 按给定策略执行命令。实现必须是并发安全的。
type Runner interface {
	Run(ctx context.Context, spec Spec) (Result, error)
}

// Mechanism 是隔离机制的名称。
type Mechanism string

const (
	// MechanismNone 表示没有操作系统级隔离。
	MechanismNone Mechanism = "none"
	// MechanismSeatbelt 是 macOS 的 sandbox-exec。
	MechanismSeatbelt Mechanism = "seatbelt"
	// MechanismBwrap 是 Linux 的 bubblewrap。
	MechanismBwrap Mechanism = "bwrap"
)

// Isolator 是 Runner 可选实现的接口，报告它使用的隔离机制；调用方据此向权限层说明执行环境。
type Isolator interface {
	Mechanism() Mechanism
}

// MechanismOf 返回 Runner 的隔离机制；未实现 Isolator 的 Runner 视为无隔离。
func MechanismOf(r Runner) Mechanism {
	if iso, ok := r.(Isolator); ok {
		return iso.Mechanism()
	}
	return MechanismNone
}

// Capabilities 描述当前平台的隔离能力。
type Capabilities struct {
	// Platform 是 runtime.GOOS。
	Platform string `json:"platform"`
	// Mechanism 是该平台对应的隔离机制；没有时为 MechanismNone。
	Mechanism Mechanism `json:"mechanism"`
	// Available 表示该机制的辅助程序在本机存在。
	Available bool `json:"available"`
	// Path 是辅助程序的路径（Available 时）。
	Path string `json:"path,omitempty"`
	// Implemented 表示本版本已为该机制提供 Runner；Available 且 Implemented 时才能真正隔离。
	Implemented bool `json:"implemented"`
}

// shellEnvExtras 是在全仓子进程白名单（config.ChildEnvAllowlist）之外，执行系统 shell 命令额外需要的变量：
// 区域设置覆盖（LC_ALL）与 Windows 上 cmd.exe 及常见命令行程序定位系统目录、临时目录与可执行扩展名所需的变量。
// 它们都不是凭据。基础白名单只在 config 包定义一份，本包不再复制。
var shellEnvExtras = []string{"LC_ALL", "SystemDrive", "TEMP", "TMP", "ComSpec", "PATHEXT"}

// envKeys 是从守护进程环境透传给子进程的变量白名单：config.ChildEnvAllowlist() + shellEnvExtras；其余变量一律不传。
var envKeys = composeEnvKeys(config.ChildEnvAllowlist(), shellEnvExtras)

// composeEnvKeys 按顺序合并并去重。
func composeEnvKeys(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, k := range l {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// BaseEnvKeys 返回环境变量白名单的副本（config.ChildEnvAllowlist() 加上执行 shell 所需的增量）。
func BaseEnvKeys() []string {
	return append([]string(nil), envKeys...)
}

// buildEnv 组装子进程环境：白名单变量 → Spec.Env（按键名排序，可覆盖白名单）。
// lookup 通常是 os.LookupEnv，抽出来便于测试。
func buildEnv(extra map[string]string, lookup func(string) (string, bool)) []string {
	var order []string
	vars := make(map[string]string, len(envKeys)+len(extra))
	set := func(k, v string) {
		if _, seen := vars[k]; !seen {
			order = append(order, k)
		}
		vars[k] = v
	}
	for _, k := range envKeys {
		if v, ok := lookup(k); ok {
			set(k, v)
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		set(k, extra[k])
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out
}

// normalize 校验 Spec 并填充默认值；非法时返回 CodeInvalid。
func normalize(spec Spec) (Spec, error) {
	if len(spec.Command) == 0 || strings.TrimSpace(spec.Command[0]) == "" {
		return Spec{}, model.NewError(model.CodeInvalid, "命令不能为空")
	}
	for _, a := range spec.Command {
		if strings.ContainsRune(a, 0) {
			return Spec{}, model.NewError(model.CodeInvalid, "命令参数包含非法字符")
		}
	}
	if strings.ContainsRune(spec.RawCommandLine, 0) {
		return Spec{}, model.NewError(model.CodeInvalid, "命令行包含非法字符")
	}
	for k, v := range spec.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return Spec{}, model.Errorf(model.CodeInvalid, "环境变量 %q 不合法", k)
		}
	}
	if spec.Dir == "" {
		return Spec{}, model.NewError(model.CodeInvalid, "工作目录不能为空")
	}
	if !filepath.IsAbs(spec.Dir) {
		return Spec{}, model.Errorf(model.CodeInvalid, "工作目录 %q 必须是绝对路径", spec.Dir)
	}
	info, err := os.Stat(spec.Dir)
	if err != nil {
		return Spec{}, model.Wrap(model.CodeInvalid, "工作目录不可用", err)
	}
	if !info.IsDir() {
		return Spec{}, model.Errorf(model.CodeInvalid, "工作目录 %q 不是目录", spec.Dir)
	}
	for _, d := range spec.WritableDirs {
		if !filepath.IsAbs(d) {
			return Spec{}, model.Errorf(model.CodeInvalid, "可写目录 %q 必须是绝对路径", d)
		}
	}
	if spec.Timeout <= 0 {
		spec.Timeout = DefaultTimeout
	}
	if spec.Timeout > MaxTimeout {
		spec.Timeout = MaxTimeout
	}
	if spec.MaxOutputBytes <= 0 {
		spec.MaxOutputBytes = DefaultMaxOutputBytes
	}
	return spec, nil
}

// limitedBuffer 保留前 max 字节、丢弃其余并记录是否截断。单个 goroutine 写入，无需加锁。
type limitedBuffer struct {
	max       int
	buf       []byte
	total     int64
	truncated bool
}

func newLimitedBuffer(max int) *limitedBuffer {
	return &limitedBuffer{max: max}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	if room := b.max - len(b.buf); room > 0 {
		if len(p) > room {
			p = p[:room]
			b.truncated = true
		}
		b.buf = append(b.buf, p...)
	} else if n > 0 {
		b.truncated = true
	}
	// 始终报告全部写入成功：丢弃是策略而不是错误，否则 exec 会把管道当作已损坏。
	return n, nil
}

func (b *limitedBuffer) String() string { return string(b.buf) }
