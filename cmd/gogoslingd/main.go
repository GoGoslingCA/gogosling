// Command gogoslingd 是 gogosling 的本地守护进程。
//
// 它承载全部业务状态：控制面数据、智能体与会话、IM 网关、定时任务、扩展宿主，
// 并监管 Python 工作进程。桌面壳、Web UI 与 CLI 都只是它的客户端，
// 因此关闭窗口不会中断定时任务和 IM 通道。
//
// 本文件只做参数解析、日志与信号处理，然后把控制权交给 internal/app。进程配置（数据目录、监听地址）
// 的解析规则只在 config.Load 一处，命令行参数只是其中优先级最高的覆盖。
//
// 退出码：0 正常退出；1 运行失败；2 参数错误；3 数据目录已被另一个守护进程占用。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/GoGoslingCA/gogosling/internal/app"
	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/observability"
	"github.com/GoGoslingCA/gogosling/internal/version"
)

// 退出码。
const (
	exitOK       = 0
	exitFailure  = 1
	exitUsage    = 2
	exitConflict = 3
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

// flags 是解析后的命令行参数。
type flags struct {
	listen      string
	home        string
	logLevel    string
	showVersion bool
}

// parseFlags 解析参数；-h 时返回 flag.ErrHelp。
func parseFlags(args []string, stderr io.Writer, locale string) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("gogoslingd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.listen, "listen", "", i18n.T(locale, "daemon.flag_listen", nil))
	fs.StringVar(&f.home, "home", "", i18n.T(locale, "daemon.flag_home", map[string]any{"env": config.EnvHome}))
	fs.StringVar(&f.logLevel, "log-level", "info", i18n.T(locale, "daemon.flag_log_level", nil))
	fs.BoolVar(&f.showVersion, "version", false, i18n.T(locale, "daemon.flag_version", nil))
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("%s", i18n.T(locale, "daemon.unexpected_args", map[string]any{"args": strings.Join(fs.Args(), " ")}))
	}
	return f, nil
}

// run 是可测试的主流程：返回退出码。
func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	locale := cliLocale(getenv)
	f, err := parseFlags(args, stderr, locale)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintln(stderr, "gogoslingd:", err)
		return exitUsage
	}
	if f.showVersion {
		fmt.Fprintln(stdout, version.String())
		return exitOK
	}
	level, err := parseLevel(f.logLevel)
	if err != nil {
		fmt.Fprintln(stderr, "gogoslingd:", i18n.T(locale, "daemon.invalid_log_level", map[string]any{"level": f.logLevel}))
		return exitUsage
	}

	cfg, err := loadConfig(f, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "gogoslingd:", err)
		return exitUsage
	}
	logger, closeLog, err := observability.Setup(observability.Config{DataDir: cfg.DataDir, Level: level, Compress: true, Stderr: stderr})
	if err != nil {
		fmt.Fprintln(stderr, "gogoslingd:", i18n.T(locale, "daemon.start_failed", map[string]any{"error": err.Error()}))
		return exitFailure
	}
	defer func() {
		if err := closeLog(); err != nil {
			fmt.Fprintln(stderr, "gogoslingd:", err)
		}
	}()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("gogoslingd 启动", "version", version.Version, "data_dir", cfg.DataDir, "listen", cfg.ListenAddr, "pid", os.Getpid())
	a, err := app.New(cfg, logger, app.WithGetenv(getenv))
	if err != nil {
		fmt.Fprintln(stderr, "gogoslingd:", i18n.T(locale, "daemon.start_failed", map[string]any{"error": err.Error()}))
		return exitUsage
	}
	if err := a.Run(ctx); err != nil {
		return reportRunError(stderr, locale, cfg.DataDir, err, logger)
	}
	logger.Info("gogoslingd 已退出")
	return exitOK
}

// reportRunError 把运行错误写到终端与日志，并选择退出码。
func reportRunError(stderr io.Writer, locale, dataDir string, err error, logger *slog.Logger) int {
	var me *model.Error
	if errors.As(err, &me) && me.Code == model.CodeConflict && me.Details["data_dir"] != nil {
		pid := fmt.Sprint(me.Details["pid"])
		if me.Details["pid"] == nil {
			pid = "?"
		}
		fmt.Fprintln(stderr, "gogoslingd:", i18n.T(locale, "daemon.already_running", map[string]any{"pid": pid, "data_dir": dataDir}))
		return exitConflict
	}
	logger.Error("gogoslingd 异常退出", "error", err)
	fmt.Fprintln(stderr, "gogoslingd:", i18n.T(locale, "daemon.start_failed", map[string]any{"error": err.Error()}))
	return exitFailure
}

// loadConfig 解析进程配置。唯一的规则在 config.Load（默认值 < <数据目录>/config.json < 环境变量
// < 命令行参数），命令行的 -home / -listen 只作为最高优先级的覆盖传入；非回环监听地址、
// 损坏的 config.json 在这里就以参数错误（退出码 2）拒绝，不必等到各模块启动之后。
func loadConfig(f flags, getenv func(string) string) (config.Config, error) {
	return config.Load(getenv, f.home, f.listen)
}

// parseLevel 把 debug / info / warn / error 映射为 slog.Level。
func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(strings.TrimSpace(s)))
	return l, err
}

// cliLocale 按 LC_ALL / LC_MESSAGES / LANG 选择终端提示语言（i18n.FromEnv，与 CLI、内核同一套解析）；
// 无法判断时用 i18n 默认语言。
func cliLocale(getenv func(string) string) string {
	if l, ok := i18n.FromEnv(getenv); ok {
		return l
	}
	return i18n.DefaultLocale
}
