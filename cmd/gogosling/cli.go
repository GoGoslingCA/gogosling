package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 退出码。
const (
	exitOK         = 0
	exitFailure    = 1
	exitUsage      = 2
	exitNoDaemon   = 3
	exitInterrupts = 130
)

// errDaemonNotRunning 表示找不到正在运行的守护进程（没有 daemon.json 或连不上）。
var errDaemonNotRunning = errors.New("daemon not running")

// usageError 是参数错误；消息已本地化。
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// silentError 表示错误已经输出过，只需要退出码（例如 chat --once 的运行失败）。
type silentError struct{ code int }

func (e *silentError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// cli 是一次命令行调用的上下文。字段在测试中可以替换；不使用任何全局可变状态。
type cli struct {
	stdin  io.Reader
	out    io.Writer
	errOut io.Writer
	getenv func(string) string

	// 全局参数。
	home   string
	asJSON bool
	locale string

	// color 为 true 时输出 ANSI 暗色等样式（标准输出是终端且没有设置 NO_COLOR）。
	color bool
	// stdinTTY 为 true 时可以关闭回显读取密钥。
	stdinTTY bool

	// dial 连接守护进程；默认用 internal/client 从 daemon.json 发现。
	dial func(ctx context.Context, home, locale string) (api, error)
	// daemonCtl 管理守护进程进程（daemon start/stop/status）；测试替换。
	daemonCtl daemonController
	// interrupts 投递 Ctrl-C；默认来自 os/signal。
	interrupts func() (<-chan os.Signal, func())

	// stopGrace / stopWait / stopPoll 覆盖 daemon stop 的时限（零值使用 daemonGraceTimeout 等缺省值；测试调小）。
	stopGrace time.Duration
	stopWait  time.Duration
	stopPoll  time.Duration

	lines *lineReader
}

// newCLI 用真实的进程环境创建 cli。颜色只在标准输出是终端、没有 NO_COLOR，且终端能解释 ANSI 转义时启用
// （Windows 传统控制台需要先开启 VT 处理，开启失败就不输出 ANSI 转义）。
func newCLI() *cli {
	return &cli{
		stdin:      os.Stdin,
		out:        os.Stdout,
		errOut:     os.Stderr,
		getenv:     os.Getenv,
		color:      isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == "" && enableVT(os.Stdout),
		stdinTTY:   isTerminal(os.Stdin),
		dial:       dialClient,
		daemonCtl:  systemDaemonController{},
		interrupts: notifyInterrupts,
	}
}

// t 返回 cli.* 文案。
func (c *cli) t(key string, args map[string]any) string {
	return i18n.T(c.locale, "cli."+key, args)
}

// printf 写到标准输出。
func (c *cli) printf(format string, args ...any) { fmt.Fprintf(c.out, format, args...) }

// println 写一行到标准输出。
func (c *cli) println(s string) { fmt.Fprintln(c.out, s) }

// command 是一个顶层命令。
type command func(c *cli, ctx context.Context, args []string) error

// commands 返回顶层命令表（函数而不是包级变量，避免全局可变状态）。
func commands() map[string]command {
	return map[string]command{
		"daemon":    (*cli).cmdDaemon,
		"version":   (*cli).cmdVersion,
		"agent":     (*cli).cmdAgent,
		"provider":  (*cli).cmdProvider,
		"session":   (*cli).cmdSession,
		"chat":      (*cli).cmdChat,
		"approvals": (*cli).cmdApprovals,
		"job":       (*cli).cmdJob,
		"extension": (*cli).cmdExtension,
		"channel":   (*cli).cmdChannel,
		"settings":  (*cli).cmdSettings,
	}
}

// run 解析全局参数并分发到子命令，返回退出码。
func (c *cli) run(ctx context.Context, args []string) int {
	c.locale = envLocale(c.getenv)
	c.asJSON = false
	fs := flag.NewFlagSet("gogosling", flag.ContinueOnError)
	c.globalFlags(fs)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.usage()
			return exitOK
		}
		return c.exitCode(&usageError{msg: err.Error()})
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "help" {
		c.usage()
		return exitOK
	}
	cmd, ok := commands()[rest[0]]
	if !ok {
		return c.exitCode(&usageError{msg: c.t("unknown_command", map[string]any{"cmd": rest[0]})})
	}
	return c.exitCode(cmd(c, ctx, rest[1:]))
}

// usage 打印帮助。
func (c *cli) usage() { fmt.Fprint(c.out, c.t("usage", nil)) }

// exitCode 输出错误并给出退出码。
func (c *cli) exitCode(err error) int {
	if err == nil {
		return exitOK
	}
	var ue *usageError
	var se *silentError
	switch {
	case errors.As(err, &se):
		return se.code
	case errors.As(err, &ue):
		fmt.Fprintln(c.errOut, c.t("error", map[string]any{"message": ue.msg}))
		return exitUsage
	case errors.Is(err, errDaemonNotRunning):
		fmt.Fprintln(c.errOut, c.t("daemon_not_running", nil))
		return exitNoDaemon
	default:
		fmt.Fprintln(c.errOut, c.t("error", map[string]any{"message": describeError(err)}))
		return exitFailure
	}
}

// describeError 返回面向用户的错误描述：带错误码的错误用服务端（已本地化）的消息，附上错误码。
func describeError(err error) string {
	var me *model.Error
	if errors.As(err, &me) {
		msg := me.Message
		if msg == "" {
			msg = string(me.Code)
		}
		return msg + " (" + string(me.Code) + ")"
	}
	return err.Error()
}

// globalFlags 在 fs 上登记全局参数：顶层与每个子命令都接受它们，位置不限。
func (c *cli) globalFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.home, "home", c.home, "")
	fs.BoolVar(&c.asJSON, "json", c.asJSON, "")
	fs.Func("lang", "", func(v string) error {
		l, ok := i18n.Normalize(v)
		if !ok {
			return fmt.Errorf("unsupported language %q", v)
		}
		c.locale = l
		return nil
	})
}

// parse 解析子命令参数，允许参数与位置参数交错（例如 session new <agent> --title T），
// 同时接受全局参数。返回位置参数。
func (c *cli) parse(fs *flag.FlagSet, args []string) ([]string, error) {
	c.globalFlags(fs)
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				c.usage()
				return nil, &silentError{code: exitOK}
			}
			return nil, &usageError{msg: err.Error()}
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// need 校验位置参数个数恰好为 n，否则返回参数错误。
func (c *cli) need(pos []string, names ...string) error {
	if len(pos) < len(names) {
		return &usageError{msg: c.t("missing_arg", map[string]any{"arg": names[len(pos)]})}
	}
	if len(pos) > len(names) {
		return &usageError{msg: c.t("invalid_arg", map[string]any{"arg": pos[len(names)], "reason": "unexpected"})}
	}
	return nil
}

// subcommand 取出子命令名；缺失时返回参数错误。
func (c *cli) subcommand(cmd string, args []string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, &usageError{msg: c.t("missing_arg", map[string]any{"arg": cmd + " <subcommand>"})}
	}
	return args[0], args[1:], nil
}

// unknownSub 构造"未知子命令"错误。
func (c *cli) unknownSub(cmd, sub string) error {
	return &usageError{msg: c.t("unknown_subcommand", map[string]any{"cmd": cmd, "sub": sub})}
}

// homeDir 返回数据目录：--home > GOGOSLING_HOME > ~/.gogosling。规则只有 config.DataDir 一份，
// 与 gogoslingd 一致（两边找到的是同一个 daemon.json）。CLI 只用它找 daemon.json。
func (c *cli) homeDir() (string, error) {
	return config.DataDir(c.getenv, c.home)
}

// connect 连接守护进程。
func (c *cli) connect(ctx context.Context) (api, error) {
	home, err := c.homeDir()
	if err != nil {
		return nil, err
	}
	return c.dial(ctx, home, c.locale)
}

// envLocale 按 LC_ALL / LC_MESSAGES / LANG 选择语言（i18n.FromEnv，全仓唯一的解析）；无法判断时用 i18n 默认语言。
func envLocale(getenv func(string) string) string {
	if l, ok := i18n.FromEnv(getenv); ok {
		return l
	}
	return i18n.DefaultLocale
}

// isTerminal 粗略判断文件是否是终端（字符设备）。
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
