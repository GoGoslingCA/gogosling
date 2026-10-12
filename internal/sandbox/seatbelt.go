package sandbox

import (
	"context"
	_ "embed"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// seatbeltTemplate 是嵌入的 profile 模板；占位符见 BuildSeatbeltProfile。
//
//go:embed seatbelt.sb
var seatbeltTemplate string

const (
	placeholderWritable = "{{WRITABLE}}"
	placeholderNetwork  = "{{NETWORK}}"
	seatbeltBinary      = "sandbox-exec"
)

// SeatbeltProfile 是生成 profile 所需的参数。
type SeatbeltProfile struct {
	// WritableDirs 是允许写入的目录；必须是绝对路径。调用方应先解析符号链接：
	// Seatbelt 按真实路径匹配，macOS 上 /var、/tmp 都是指向 /private 的符号链接。
	WritableDirs []string
	AllowNetwork bool
}

// BuildSeatbeltProfile 用参数填充嵌入的模板，返回可直接交给 sandbox-exec -p 的 profile 文本。
func BuildSeatbeltProfile(p SeatbeltProfile) (string, error) {
	var writable strings.Builder
	if len(p.WritableDirs) > 0 {
		writable.WriteString("(allow file-write*")
		for _, d := range p.WritableDirs {
			if !filepath.IsAbs(d) {
				return "", model.Errorf(model.CodeInvalid, "可写目录 %q 必须是绝对路径", d)
			}
			writable.WriteString("\n  (subpath \"")
			writable.WriteString(schemeEscape(filepath.Clean(d)))
			writable.WriteString("\")")
		}
		writable.WriteString(")")
	} else {
		writable.WriteString(";; 没有额外的可写目录")
	}
	network := "(deny network*)"
	if p.AllowNetwork {
		network = "(allow network*)\n(allow mach-lookup (global-name \"com.apple.dnssd.service\") (global-name \"com.apple.mDNSResponder\") (global-name \"com.apple.SystemConfiguration.configd\"))"
	}
	out := strings.Replace(seatbeltTemplate, placeholderWritable, writable.String(), 1)
	out = strings.Replace(out, placeholderNetwork, network, 1)
	if strings.Contains(out, "{{") {
		return "", model.NewError(model.CodeInternal, "profile 模板存在未填充的占位符")
	}
	return out, nil
}

// schemeEscape 转义 profile 字符串字面量中的反斜杠与双引号。
func schemeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// Seatbelt 用 macOS 的 sandbox-exec 执行命令：
// 只能写入 Spec.WritableDirs 与一个每次运行私有的临时目录（通过 TMPDIR 告知子进程），
// AllowNetwork 为 false 时切断网络。读取不受限（由 workspace 与权限层约束）。
// 可以并发使用。
type Seatbelt struct {
	binary string
	logger *slog.Logger
	lookup func(string) (string, bool)
}

// NewSeatbelt 创建 Seatbelt Runner；非 darwin 或找不到 sandbox-exec 时返回 CodeUnavailable。
func NewSeatbelt(logger *slog.Logger) (*Seatbelt, error) {
	if runtime.GOOS != "darwin" {
		return nil, model.NewError(model.CodeUnavailable, "Seatbelt 只在 macOS 上可用")
	}
	path, err := exec.LookPath(seatbeltBinary)
	if err != nil {
		return nil, model.Wrap(model.CodeUnavailable, "找不到 sandbox-exec", err)
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Seatbelt{binary: path, logger: logger, lookup: os.LookupEnv}, nil
}

// Mechanism 实现 Isolator。
func (s *Seatbelt) Mechanism() Mechanism { return MechanismSeatbelt }

// Run 实现 Runner。
func (s *Seatbelt) Run(ctx context.Context, spec Spec) (Result, error) {
	spec, err := normalize(spec)
	if err != nil {
		return Result{}, err
	}
	// 每次运行一个私有临时目录：既让需要临时文件的程序能工作，又不把系统临时目录整个放开。
	tmp, err := os.MkdirTemp("", "gogosling-sb-")
	if err != nil {
		return Result{}, model.Wrap(model.CodeInternal, "创建沙箱临时目录失败", err)
	}
	defer os.RemoveAll(tmp)

	dirs := make([]string, 0, len(spec.WritableDirs)+1)
	for _, d := range append(append([]string(nil), spec.WritableDirs...), tmp) {
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			return Result{}, model.Wrap(model.CodeInvalid, "可写目录不可用: "+d, err)
		}
		dirs = append(dirs, real)
	}
	profile, err := BuildSeatbeltProfile(SeatbeltProfile{WritableDirs: dirs, AllowNetwork: spec.AllowNetwork})
	if err != nil {
		return Result{}, err
	}

	env := make(map[string]string, len(spec.Env)+1)
	for k, v := range spec.Env {
		env[k] = v
	}
	env["TMPDIR"] = tmp

	argv := make([]string, 0, len(spec.Command)+4)
	argv = append(argv, s.binary, "-p", profile, "--")
	argv = append(argv, spec.Command...)
	return runProcess(ctx, spec, argv, buildEnv(env, s.lookup), s.logger)
}
