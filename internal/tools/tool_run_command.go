package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/sandbox"
)

const (
	// defaultCommandTimeout 是 run_command 的默认超时。
	defaultCommandTimeout = 60 * time.Second
	// maxCommandTimeoutSeconds 是允许请求的最长超时（与 sandbox.MaxTimeout 一致）。
	maxCommandTimeoutSeconds = 600
	// maxCommandBytes 是命令文本的长度上限。
	maxCommandBytes = 32 << 10
	// commandOutputBytes 是每条输出流保留的字节数；最终结果还会被 Executor 整体截断。
	commandOutputBytes = DefaultMaxResultBytes
	// summaryCommandChars 是摘要里展示的命令最大字符数。
	summaryCommandChars = 120
)

// 子进程可见的工作区标识变量。
const (
	envWorkspace = "GOGOSLING_WORKSPACE"
	envAgentID   = "GOGOSLING_AGENT_ID"
	envRunID     = "GOGOSLING_RUN_ID"
)

// runCommand 通过 sandbox.Runner 在工作区内执行 shell 命令。
type runCommand struct {
	runner sandbox.Runner
	shell  func(command string) shellCommand
}

// NewRunCommand 创建 run_command 工具。
func NewRunCommand(runner sandbox.Runner) Tool {
	return runCommand{runner: runner, shell: shellFor(runtime.GOOS)}
}

// shellCommand 是把一段命令文本交给平台 shell 的方式：argv 填入 sandbox.Spec.Command，
// raw 非空时填入 sandbox.Spec.RawCommandLine（只在 Windows 上使用）。
type shellCommand struct {
	argv []string
	raw  string
}

// shellFor 返回把一段命令文本交给平台 shell 执行的构造函数。
//
// Windows 上不能把命令文本作为参数交给 exec：Go 按 C 运行时规则把其中的 " 转义成 \"，而 cmd.exe 不认，
// `git commit -m "fix bug"` 会变成 git 收到 `"fix` 与 `bug"` 两个参数。因此整条命令行原样给出：
// cmd.exe /d /s /c "<command>"——/s 让 cmd 只去掉最外层的一对引号，其余引号原样保留；/d 跳过 AutoRun。
func shellFor(goos string) func(string) shellCommand {
	if goos == "windows" {
		return func(c string) shellCommand {
			return shellCommand{argv: []string{"cmd.exe"}, raw: `cmd.exe /d /s /c "` + c + `"`}
		}
	}
	return func(c string) shellCommand { return shellCommand{argv: []string{"/bin/sh", "-c", c}} }
}

type runCommandArgs struct {
	Command        string `json:"command"`
	Cwd            string `json:"cwd"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Stdin          string `json:"stdin"`
	AllowNetwork   bool   `json:"allow_network"`
}

func (runCommand) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: "run_command",
		Description: "在工作区目录中用系统 shell 执行一条命令，返回退出码、标准输出与标准错误。" +
			"默认 60 秒超时、禁止网络、只能写入工作区；输出过长会被截断。不要执行交互式或长期运行的程序。",
		InputSchema: objectSchema(map[string]any{
			"command":         stringProp("要执行的 shell 命令（Unix 为 sh -c，Windows 为 cmd /c）"),
			"cwd":             stringProp("工作目录（工作区相对路径）；省略表示工作区根目录"),
			"timeout_seconds": intProp("超时秒数；默认 60，最大 600", 1, maxCommandTimeoutSeconds),
			"stdin":           stringProp("喂给标准输入的文本；省略表示无输入"),
			"allow_network":   boolProp("是否允许命令访问网络；默认 false"),
		}, "command"),
		Source: SourceBuiltin,
		Risk:   model.RiskExecute,
	}
}

func (runCommand) parse(raw json.RawMessage) (runCommandArgs, error) {
	var a runCommandArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Command = strings.TrimSpace(a.Command)
	if a.Command == "" {
		return a, model.NewError(model.CodeInvalid, "command 不能为空")
	}
	if len(a.Command) > maxCommandBytes {
		return a, model.Errorf(model.CodeInvalid, "command 超过 %d 字节上限", maxCommandBytes)
	}
	if strings.ContainsRune(a.Command, 0) {
		return a, model.NewError(model.CodeInvalid, "command 包含非法字符")
	}
	a.Cwd = cleanRelPath(a.Cwd)
	if a.TimeoutSeconds < 0 {
		return a, model.NewError(model.CodeInvalid, "timeout_seconds 不能为负数")
	}
	if a.TimeoutSeconds == 0 {
		a.TimeoutSeconds = int(defaultCommandTimeout / time.Second)
	}
	if a.TimeoutSeconds > maxCommandTimeoutSeconds {
		a.TimeoutSeconds = maxCommandTimeoutSeconds
	}
	return a, nil
}

func (t runCommand) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	summary := "执行命令: " + firstLine(a.Command, summaryCommandChars)
	if a.Cwd != "." {
		summary += "（目录 " + a.Cwd + "）"
	}
	if a.AllowNetwork {
		summary += "，允许联网"
	}
	return Description{
		Summary: summary,
		Details: map[string]any{
			"command":         a.Command,
			"cwd":             a.Cwd,
			"timeout_seconds": a.TimeoutSeconds,
			"allow_network":   a.AllowNetwork,
			"sandbox":         string(sandbox.MechanismOf(t.runner)),
		},
	}, nil
}

func (t runCommand) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	info, err := rc.Workspace.Stat(ctx, a.Cwd)
	if err != nil {
		return model.ToolResult{}, err
	}
	if !info.IsDir {
		return model.ToolResult{}, model.Errorf(model.CodeInvalid, "cwd %s 不是目录", a.Cwd)
	}
	dir, err := rc.Workspace.Resolve(a.Cwd)
	if err != nil {
		return model.ToolResult{}, err
	}
	env := map[string]string{envWorkspace: rc.Workspace.Root()}
	if rc.Agent != nil {
		env[envAgentID] = rc.Agent.ID
	}
	if rc.RunID != "" {
		env[envRunID] = rc.RunID
	}
	timeout := time.Duration(a.TimeoutSeconds) * time.Second
	sh := t.shell(a.Command)
	res, err := t.runner.Run(ctx, sandbox.Spec{
		Command:        sh.argv,
		RawCommandLine: sh.raw,
		Dir:            dir,
		Env:            env,
		Stdin:          a.Stdin,
		Timeout:        timeout,
		WritableDirs:   []string{rc.Workspace.Root()},
		AllowNetwork:   a.AllowNetwork,
		MaxOutputBytes: commandOutputBytes,
	})
	if err != nil {
		return model.ToolResult{}, err
	}
	return formatCommandResult(res, timeout), nil
}

// Isolated 实现 Isolation：只有 Runner 提供操作系统级隔离（例如 macOS Seatbelt）时才算隔离；
// sandbox.Direct 视为未隔离。权限层的 auto_in_sandbox 档位据此决定是否自动放行。
func (t runCommand) Isolated() bool {
	return sandbox.MechanismOf(t.runner) != sandbox.MechanismNone
}

// formatCommandResult 把执行结果组织成给模型看的文本；超时或非零退出码视为失败（IsError）。
// 任一输出流被截断时 Truncated 为 true。
func formatCommandResult(res sandbox.Result, timeout time.Duration) model.ToolResult {
	var b strings.Builder
	switch {
	case res.TimedOut:
		fmt.Fprintf(&b, "命令超过 %s 超时，已被终止（耗时 %s）\n", timeout, res.Duration.Round(time.Millisecond))
	default:
		fmt.Fprintf(&b, "退出码: %d（耗时 %s）\n", res.ExitCode, res.Duration.Round(time.Millisecond))
	}
	if res.Truncated {
		fmt.Fprintf(&b, "[输出已截断：每条输出流最多保留 %d 字节]\n", commandOutputBytes)
	}
	b.WriteString("--- stdout ---\n")
	b.WriteString(res.Stdout)
	if res.Stdout != "" && !strings.HasSuffix(res.Stdout, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("--- stderr ---\n")
	b.WriteString(res.Stderr)
	if res.Stderr != "" && !strings.HasSuffix(res.Stderr, "\n") {
		b.WriteString("\n")
	}
	return model.ToolResult{Content: b.String(), IsError: res.TimedOut || res.ExitCode != 0, Truncated: res.Truncated}
}
