package sandbox

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// Direct 直接在当前用户下执行命令，没有操作系统级隔离。
//
// 它仍然提供：工作目录、环境变量白名单、超时、输出截断、整棵进程树的终止。
// WritableDirs 与 AllowNetwork 在这里无法强制执行；调用方应通过 Detect 或 MechanismOf 知晓这一点。
// 可以并发使用。
type Direct struct {
	logger *slog.Logger
	lookup func(string) (string, bool)
}

// NewDirect 创建无隔离的 Runner；logger 为 nil 时不输出日志。
func NewDirect(logger *slog.Logger) *Direct {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Direct{logger: logger, lookup: os.LookupEnv}
}

// Mechanism 实现 Isolator：无隔离。
func (d *Direct) Mechanism() Mechanism { return MechanismNone }

// Run 实现 Runner。
func (d *Direct) Run(ctx context.Context, spec Spec) (Result, error) {
	spec, err := normalize(spec)
	if err != nil {
		return Result{}, err
	}
	return runProcess(ctx, spec, spec.Command, buildEnv(spec.Env, d.lookup), d.logger)
}

// runProcess 是 Direct 与隔离实现共用的进程管理核心：
// 超时、取消、输出截断、进程树终止、退出码归一化。argv 是最终要启动的命令（可能已包裹沙箱前缀）。
func runProcess(ctx context.Context, spec Spec, argv, env []string, logger *slog.Logger, extraFiles ...*os.File) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, ctxError(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = env
	cmd.ExtraFiles = extraFiles
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}
	stdout := newLimitedBuffer(spec.MaxOutputBytes)
	stderr := newLimitedBuffer(spec.MaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	configureCmd(cmd, spec.RawCommandLine)
	// ctx 结束时结束整棵进程树，而不只是直接子进程；否则 sh -c "x | y" 里的孙进程会存活。
	cmd.Cancel = func() error { return killTree(cmd.Process) }
	cmd.WaitDelay = waitDelay

	start := time.Now()
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Result{}, model.Wrap(model.CodeNotFound, "命令不存在: "+argv[0], err)
		}
		return Result{}, model.Wrap(model.CodeInternal, "命令启动失败", err)
	}
	waitErr := cmd.Wait()
	res := Result{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.truncated || stderr.truncated,
		Duration:  time.Since(start),
	}

	// 调用方取消优先于超时：两者都会结束 runCtx，但语义不同。
	if err := ctx.Err(); err != nil {
		return res, ctxError(err)
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		logger.Debug("命令超时", "command", argv[0], "timeout", spec.Timeout.String())
		return res, nil
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		// 输出管道在 WaitDelay 后被强制关闭等情况：进程本身已经结束，退出码仍可用。
		if cmd.ProcessState != nil {
			res.ExitCode = cmd.ProcessState.ExitCode()
			return res, nil
		}
		return res, model.Wrap(model.CodeInternal, "等待命令结束失败", waitErr)
	}
	res.ExitCode = cmd.ProcessState.ExitCode()
	return res, nil
}

// ctxError 把 ctx 的结束原因转成带错误码的错误。
func ctxError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return model.Wrap(model.CodeTimeout, "命令未在期限内完成", err)
	}
	return model.Wrap(model.CodeCancelled, "命令被取消", err)
}
