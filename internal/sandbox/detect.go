package sandbox

import (
	"log/slog"
	"os/exec"
	"runtime"
)

// helperFor 列出每个平台的隔离机制及其辅助程序名。
var helperFor = map[string]struct {
	mechanism Mechanism
	binary    string
	// implemented 表示本版本已为该机制提供 Runner。
	implemented bool
}{
	"darwin": {MechanismSeatbelt, "sandbox-exec", true},
	"linux":  {MechanismBwrap, "bwrap", true},
}

// Detect 报告当前平台的隔离能力。
func Detect() Capabilities {
	return DetectWith(runtime.GOOS, exec.LookPath)
}

// DetectWith 是 Detect 的可注入版本：goos 是目标平台，lookPath 用于查找辅助程序（通常为 exec.LookPath）。
func DetectWith(goos string, lookPath func(string) (string, error)) Capabilities {
	caps := Capabilities{Platform: goos, Mechanism: MechanismNone}
	h, ok := helperFor[goos]
	if !ok {
		return caps
	}
	caps.Mechanism = h.mechanism
	caps.Implemented = h.implemented
	if path, err := lookPath(h.binary); err == nil && path != "" {
		caps.Available = true
		caps.Path = path
	}
	return caps
}

// New 返回当前平台能提供的最强 Runner：
// darwin 优先 Seatbelt；linux 的 bwrap 实际隔离探测通过才选择 Bwrap；否则为 Direct。
// 返回的 Capabilities 仍描述辅助程序发现结果，实际选择用 MechanismOf 返回的 Runner 确认。
func New(logger *slog.Logger) (Runner, Capabilities) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	caps := Detect()
	if caps.Mechanism == MechanismSeatbelt && caps.Available {
		if sb, err := NewSeatbelt(logger); err == nil {
			return sb, caps
		}
	}
	if caps.Mechanism == MechanismBwrap && caps.Available && caps.Implemented {
		if bw, err := newBwrap(caps.Path, logger); err == nil {
			return bw, caps
		} else {
			logger.Warn("bwrap 实际隔离探测失败，命令将在无隔离下执行",
				"platform", caps.Platform, "mechanism", string(caps.Mechanism), "error", err)
			return NewDirect(logger), caps
		}
	}
	if caps.Mechanism != MechanismNone {
		logger.Warn("当前平台没有可用的隔离机制，命令将在无隔离下执行",
			"platform", caps.Platform, "mechanism", string(caps.Mechanism), "available", caps.Available, "implemented", caps.Implemented)
	}
	return NewDirect(logger), caps
}
