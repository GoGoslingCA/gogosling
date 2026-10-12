package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/client"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/observability"
	"github.com/GoGoslingCA/gogosling/internal/version"
)

// 守护进程管理的时限。
const (
	daemonStartTimeout = 30 * time.Second
	// daemonGraceTimeout 是经 API 请求关闭后等待进程自行退出的时间；超时才发信号或强制结束。
	daemonGraceTimeout = 10 * time.Second
	// daemonStopTimeout 是发信号（或强制结束）之后等待进程退出的上限。
	daemonStopTimeout  = 30 * time.Second
	daemonPollInterval = 200 * time.Millisecond
	healthTimeout      = 2 * time.Second
	// identifyTimeout 是确认句柄身份（GET /api/v1/version）的期限。
	identifyTimeout = 5 * time.Second
)

// errHandleMismatch 表示 daemon.json 指向的端点在响应，但它不是写这个句柄的那个实例（started_at 不一致）。
var errHandleMismatch = errors.New("daemon.json does not belong to the running daemon")

// daemonController 封装与操作系统进程打交道的部分，测试中替换。
type daemonController interface {
	// binary 定位 gogoslingd：与 gogosling 同目录优先，其次 PATH。
	binary() (string, error)
	// spawn 以脱离当前终端的方式启动守护进程；exited 在进程退出时关闭，status 随后可读。
	spawn(bin string, args []string) (pid int, exited <-chan struct{}, status func() string, err error)
	// foreground 在前台运行守护进程直到它退出，返回其退出码。
	foreground(bin string, args []string) (int, error)
	// identify 确认句柄属于正在运行的守护进程：用句柄里的令牌访问 GET /api/v1/version，
	// 要求 200 且 started_at 与句柄一致。不一致返回 errHandleMismatch；连不上、令牌被拒等返回对应错误。
	identify(ctx context.Context, h identity.Handle) error
	// shutdown 经 POST /api/v1/system/shutdown 请求守护进程优雅关闭（返回 202 即成功，不等待退出）。
	shutdown(ctx context.Context, h identity.Handle) error
	// terminate 在优雅关闭不可用或超时后结束进程：POSIX 发 SIGTERM；Windows 没有可用的信号途径，
	// 直接 taskkill /T /F（不带 /F 的 taskkill 对控制台程序必然失败，不再白等）。
	terminate(pid int) error
	// alive 判断进程是否仍在运行。
	alive(pid int) bool
	// health 探测 <endpoint>/healthz。
	health(ctx context.Context, endpoint string) error
}

func (c *cli) cmdDaemon(ctx context.Context, args []string) error {
	sub, rest, err := c.subcommand("daemon", args)
	if err != nil {
		return err
	}
	switch sub {
	case "start":
		return c.daemonStart(ctx, rest)
	case "stop":
		return c.daemonStop(ctx, rest)
	case "status":
		return c.daemonStatus(ctx, rest)
	default:
		return c.unknownSub("daemon", sub)
	}
}

// readHandle 读取 daemon.json；不存在时返回 errDaemonNotRunning。
func (c *cli) readHandle(ctx context.Context) (identity.Handle, string, error) {
	home, err := c.homeDir()
	if err != nil {
		return identity.Handle{}, "", err
	}
	h, err := identity.NewHandleFile(home).Read(ctx)
	if err != nil {
		if model.IsCode(err, model.CodeNotFound) {
			return identity.Handle{}, home, errDaemonNotRunning
		}
		return identity.Handle{}, home, err
	}
	return h, home, nil
}

// runningHandle 返回正在运行且健康的守护进程句柄；否则返回 errDaemonNotRunning。
func (c *cli) runningHandle(ctx context.Context) (identity.Handle, error) {
	h, _, err := c.readHandle(ctx)
	if err != nil {
		return h, err
	}
	if !c.daemonCtl.alive(h.PID) {
		return h, errDaemonNotRunning
	}
	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	if err := c.daemonCtl.health(hctx, h.Endpoint); err != nil {
		return h, errDaemonNotRunning
	}
	return h, nil
}

func (c *cli) daemonStart(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon start", flag.ContinueOnError)
	foreground := fs.Bool("foreground", false, "")
	listen := fs.String("listen", "", "")
	logLevel := fs.String("log-level", "", "")
	pos, err := c.parse(fs, args)
	if err != nil {
		return err
	}
	if err := c.need(pos); err != nil {
		return err
	}
	if h, err := c.runningHandle(ctx); err == nil {
		c.println(c.t("daemon_already_running", map[string]any{"pid": h.PID, "endpoint": h.Endpoint}))
		return nil
	}
	home, err := c.homeDir()
	if err != nil {
		return err
	}
	bin, err := c.daemonCtl.binary()
	if err != nil {
		return errors.New(c.t("daemon_binary_not_found", nil))
	}
	dargs := []string{"-home", home}
	if *listen != "" {
		dargs = append(dargs, "-listen", *listen)
	}
	if *logLevel != "" {
		dargs = append(dargs, "-log-level", *logLevel)
	}
	if *foreground {
		code, err := c.daemonCtl.foreground(bin, dargs)
		if err != nil {
			return err
		}
		if code != 0 {
			return &silentError{code: code}
		}
		return nil
	}

	pid, exited, status, err := c.daemonCtl.spawn(bin, dargs)
	if err != nil {
		return err
	}
	logPath := observability.LogPath(home)
	deadline := time.NewTimer(daemonStartTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(daemonPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-exited:
			return errors.New(c.t("daemon_exited_early", map[string]any{"status": status(), "log": logPath}))
		case <-deadline.C:
			return errors.New(c.t("daemon_start_timeout", map[string]any{"seconds": int(daemonStartTimeout.Seconds()), "log": logPath}))
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			h, err := c.runningHandle(ctx)
			if err != nil || h.PID != pid {
				continue
			}
			if c.asJSON {
				return writeJSON(c.out, map[string]any{"running": true, "pid": h.PID, "endpoint": h.Endpoint})
			}
			c.println(c.t("daemon_started", map[string]any{"pid": h.PID, "endpoint": h.Endpoint}))
			return nil
		}
	}
}

// daemonStop 停止守护进程。
//
// 结束任何进程之前先确认身份（XP-06）：daemon.json 可能是残留文件（崩溃、强制结束后），其中的 pid
// 可能已被无关进程复用。只有句柄里的令牌能通过 GET /api/v1/version 且 started_at 一致时才继续；
// 端点连不上、令牌被拒或 started_at 不一致都说明句柄已失效——删除它、不发任何信号。
// 端点超时（进程挂住，或仍在启动）时无法判断，报告"没有响应"并保留句柄，同样不发信号。
//
// 确认之后优先经 POST /api/v1/system/shutdown 优雅关闭（XP-07，三个平台一致），等待最多 10 秒；
// 端点不可用或超时才发信号（POSIX SIGTERM）或强制结束（Windows taskkill /F）。强制结束时守护进程
// 来不及删除 daemon.json，由这里在确认进程退出后删除（令牌不留在磁盘上）。
func (c *cli) daemonStop(ctx context.Context, args []string) error {
	pos, err := c.parse(flag.NewFlagSet("daemon stop", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := c.need(pos); err != nil {
		return err
	}
	h, home, err := c.readHandle(ctx)
	if errors.Is(err, errDaemonNotRunning) {
		c.println(c.t("daemon_status_stopped", nil))
		return nil
	}
	if err != nil {
		return err
	}
	ictx, cancel := context.WithTimeout(ctx, identifyTimeout)
	idErr := c.daemonCtl.identify(ictx, h)
	cancel()
	if idErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if model.CodeOf(idErr) == model.CodeTimeout {
			return errors.New(c.t("daemon_status_unhealthy", map[string]any{"pid": h.PID, "error": describeError(idErr)}))
		}
		// 句柄已失效：只删除文件，绝不向句柄里的 pid 发信号。
		if err := c.removeHandleIfSame(ctx, home, h); err != nil {
			return err
		}
		c.println(c.t("daemon_status_stopped", nil))
		return nil
	}

	c.println(c.t("daemon_stopping", map[string]any{"pid": h.PID}))
	sctx, cancel := context.WithTimeout(ctx, healthTimeout)
	shutErr := c.daemonCtl.shutdown(sctx, h)
	cancel()
	if shutErr == nil {
		exited, err := c.waitExit(ctx, h.PID, c.graceTimeout())
		if err != nil {
			return err
		}
		if exited {
			return c.finishStop(ctx, home, h)
		}
	}
	if err := c.daemonCtl.terminate(h.PID); err != nil && c.daemonCtl.alive(h.PID) {
		return err
	}
	exited, err := c.waitExit(ctx, h.PID, c.stopTimeout())
	if err != nil {
		return err
	}
	if !exited {
		return errors.New(c.t("daemon_stop_timeout", map[string]any{"pid": h.PID, "seconds": int(c.stopTimeout().Seconds())}))
	}
	return c.finishStop(ctx, home, h)
}

// finishStop 在进程退出后收尾：守护进程被强制结束时 daemon.json 还在，删除它。
func (c *cli) finishStop(ctx context.Context, home string, h identity.Handle) error {
	if err := c.removeHandleIfSame(ctx, home, h); err != nil {
		return err
	}
	c.println(c.t("daemon_stopped", nil))
	return nil
}

// removeHandleIfSame 删除 daemon.json，但只在它仍是 h 时（令牌与 pid 相同）：
// 期间若有新的守护进程写入了自己的句柄，不能把它删掉。
func (c *cli) removeHandleIfSame(ctx context.Context, home string, h identity.Handle) error {
	hf := identity.NewHandleFile(home)
	cur, err := hf.Read(ctx)
	if err != nil {
		// 已经不存在或已损坏：不存在无需处理；损坏的文件与 h 无法比较，保留给下一次启动覆盖。
		return nil
	}
	if cur.Token != h.Token || cur.PID != h.PID {
		return nil
	}
	return hf.Remove(ctx)
}

// waitExit 轮询进程直到退出或超时；返回是否已退出。
func (c *cli) waitExit(ctx context.Context, pid int, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		if !c.daemonCtl.alive(pid) {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(c.pollInterval()):
		}
	}
}

// graceTimeout / stopTimeout / pollInterval 返回停止守护进程的各项时限（测试可在 cli 上调小）。
func (c *cli) graceTimeout() time.Duration {
	if c.stopGrace > 0 {
		return c.stopGrace
	}
	return daemonGraceTimeout
}

func (c *cli) stopTimeout() time.Duration {
	if c.stopWait > 0 {
		return c.stopWait
	}
	return daemonStopTimeout
}

func (c *cli) pollInterval() time.Duration {
	if c.stopPoll > 0 {
		return c.stopPoll
	}
	return daemonPollInterval
}

func (c *cli) daemonStatus(ctx context.Context, args []string) error {
	pos, err := c.parse(flag.NewFlagSet("daemon status", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := c.need(pos); err != nil {
		return err
	}
	h, _, err := c.readHandle(ctx)
	if err == nil && !c.daemonCtl.alive(h.PID) {
		err = errDaemonNotRunning
	}
	if errors.Is(err, errDaemonNotRunning) {
		if c.asJSON {
			_ = writeJSON(c.out, map[string]any{"running": false})
		} else {
			c.println(c.t("daemon_status_stopped", nil))
		}
		return &silentError{code: exitNoDaemon}
	}
	if err != nil {
		return err
	}
	cl, err := c.connect(ctx)
	if err != nil {
		return err
	}
	v, err := cl.Version(ctx)
	if err != nil {
		if c.asJSON {
			_ = writeJSON(c.out, map[string]any{"running": false, "pid": h.PID, "error": describeError(err)})
		} else {
			c.println(c.t("daemon_status_unhealthy", map[string]any{"pid": h.PID, "error": describeError(err)}))
		}
		return &silentError{code: exitFailure}
	}
	if c.asJSON {
		return writeJSON(c.out, map[string]any{"running": true, "pid": h.PID, "endpoint": h.Endpoint, "version": v})
	}
	c.println(c.t("daemon_status_running", map[string]any{
		"pid": h.PID, "endpoint": h.Endpoint, "version": v.Version, "started_at": v.StartedAt,
	}))
	c.println(c.t("version_worker", map[string]any{"status": v.Worker.Status, "version": v.Worker.Version, "python": v.Worker.Python}))
	return nil
}

func (c *cli) cmdVersion(ctx context.Context, args []string) error {
	pos, err := c.parse(flag.NewFlagSet("version", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := c.need(pos); err != nil {
		return err
	}
	cliVersion := version.String()
	var daemonInfo *versionInfo
	cl, err := c.connect(ctx)
	if err == nil {
		if v, verr := cl.Version(ctx); verr == nil {
			daemonInfo = &v
		} else {
			err = verr
		}
	}
	if c.asJSON {
		return writeJSON(c.out, map[string]any{"cli": map[string]string{"version": version.Version, "commit": version.Commit, "date": version.Date}, "daemon": daemonInfo})
	}
	c.println(c.t("version_cli", map[string]any{"version": cliVersion}))
	if daemonInfo == nil {
		if errors.Is(err, errDaemonNotRunning) {
			fmt.Fprintln(c.errOut, c.t("daemon_not_running", nil))
			return nil
		}
		return err
	}
	c.println(c.t("version_daemon", map[string]any{"version": daemonInfo.Version, "commit": daemonInfo.Commit, "started_at": daemonInfo.StartedAt}))
	c.println(c.t("version_worker", map[string]any{"status": daemonInfo.Worker.Status, "version": daemonInfo.Worker.Version, "python": daemonInfo.Worker.Python}))
	return nil
}

// systemDaemonController 是基于真实进程的实现。
type systemDaemonController struct{}

func (systemDaemonController) binary() (string, error) {
	name := "gogoslingd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidate := filepath.Join(filepath.Dir(exe), name)
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return exec.LookPath(name)
}

func (systemDaemonController) spawn(bin string, args []string) (int, <-chan struct{}, func() string, error) {
	cmd := exec.Command(bin, args...)
	// 守护进程脱离当前终端：标准输入输出接到空设备（日志写在数据目录的 logs/ 下）。
	// 不能接管道：CLI 退出后管道断开，守护进程写 stderr 时会因 SIGPIPE 退出。
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, nil, nil, err
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return 0, nil, nil, err
	}
	exited := make(chan struct{})
	var state string
	go func() {
		err := cmd.Wait()
		if err != nil {
			state = err.Error()
		} else {
			state = "exit status 0"
		}
		close(exited)
	}()
	return cmd.Process.Pid, exited, func() string { <-exited; return state }, nil
}

func (systemDaemonController) foreground(bin string, args []string) (int, error) {
	return runForeground(bin, args)
}

func (systemDaemonController) terminate(pid int) error { return terminateProcess(pid) }

// identify 用句柄里的端点与令牌确认身份（只走 Authorization 头；客户端不经代理）。
func (systemDaemonController) identify(ctx context.Context, h identity.Handle) error {
	v, err := client.New(h.Endpoint, h.Token, client.WithUserAgent("gogosling-cli/"+version.Version)).Version(ctx)
	if err != nil {
		return err
	}
	return matchStartedAt(v.StartedAt, h.StartedAt)
}

// matchStartedAt 比较 /version 的 started_at（RFC 3339，秒精度）与句柄的 StartedAt。
func matchStartedAt(reported string, handle time.Time) error {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(reported))
	if err != nil || handle.IsZero() || !t.UTC().Truncate(time.Second).Equal(handle.UTC().Truncate(time.Second)) {
		return errHandleMismatch
	}
	return nil
}

func (systemDaemonController) shutdown(ctx context.Context, h identity.Handle) error {
	return client.New(h.Endpoint, h.Token, client.WithUserAgent("gogosling-cli/"+version.Version)).Shutdown(ctx)
}

func (systemDaemonController) alive(pid int) bool { return pid > 0 && processAlive(pid) }

func (systemDaemonController) health(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: HTTP %d", resp.StatusCode)
	}
	return nil
}
