package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/sandbox"
)

// recordingRunner 记录收到的 Spec 并返回固定结果。
type recordingRunner struct {
	spec sandbox.Spec
	res  sandbox.Result
	err  error
}

func (r *recordingRunner) Run(_ context.Context, spec sandbox.Spec) (sandbox.Result, error) {
	r.spec = spec
	return r.res, r.err
}

func TestRunCommandWithDirectRunner(t *testing.T) {
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	mustWrite(t, ws, "sub/marker.txt", "x")
	tool := NewRunCommand(sandbox.NewDirect(nil))

	res, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": "echo hello"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "退出码: 0") || !strings.Contains(res.Content, "--- stdout ---\nhello") {
		t.Fatalf("输出格式不正确: %+v", res)
	}

	res, err = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": "exit 2"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "退出码: 2") {
		t.Fatalf("非零退出码应标记为错误: %+v", res)
	}

	list := "ls"
	if runtime.GOOS == "windows" {
		list = "dir /B"
	}
	res, err = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": list, "cwd": "sub"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "marker.txt") {
		t.Fatalf("cwd 应生效: %+v", res)
	}

	dump := "env"
	if runtime.GOOS == "windows" {
		dump = "set"
	}
	t.Setenv("GOGOSLING_DAEMON_SECRET", "leak")
	res, err = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": dump}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "GOGOSLING_DAEMON_SECRET") {
		t.Fatal("守护进程环境不应泄露给命令")
	}
	if !strings.Contains(res.Content, "GOGOSLING_WORKSPACE="+ws.Root()) || !strings.Contains(res.Content, "GOGOSLING_AGENT_ID=01AGENT") {
		t.Fatalf("应传入工作区标识: %s", res.Content)
	}

	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": "echo x", "cwd": "missing"})); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的 cwd 应为 CodeNotFound，得到 %v", err)
	}
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": "echo x", "cwd": "sub/marker.txt"})); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("cwd 是文件应为 CodeInvalid，得到 %v", err)
	}
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": "echo x", "cwd": "../"})); !model.IsCode(err, model.CodeForbidden) {
		t.Fatalf("越界 cwd 应为 CodeForbidden，得到 %v", err)
	}
	for name, args := range map[string]map[string]any{
		"空命令":  {"command": "  "},
		"负超时":  {"command": "echo", "timeout_seconds": -1},
		"类型错误": {"command": 42},
	} {
		if _, err := tool.Execute(context.Background(), rc, rawArgs(t, args)); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%s 应为 CodeInvalid，得到 %v", name, err)
		}
	}
}

func TestRunCommandWritesInsideWorkspace(t *testing.T) {
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	tool := NewRunCommand(sandbox.NewDirect(nil))
	cmd := "echo created > out.txt"
	res, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": cmd}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("写入工作区应成功: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "out.txt")); err != nil {
		t.Fatalf("文件应写在工作区内: %v", err)
	}
}

func TestRunCommandSpecAndFormatting(t *testing.T) {
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	rr := &recordingRunner{res: sandbox.Result{Stdout: "partial", TimedOut: true, ExitCode: -1, Duration: 1500 * time.Millisecond}}
	tool := NewRunCommand(rr)

	res, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{
		"command": "make build", "timeout_seconds": 5, "stdin": "in", "allow_network": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "超过 5s 超时") || !strings.Contains(res.Content, "partial") {
		t.Fatalf("超时结果格式不正确: %+v", res)
	}
	if rr.spec.Dir != ws.Root() || rr.spec.Timeout != 5*time.Second || rr.spec.Stdin != "in" || !rr.spec.AllowNetwork {
		t.Fatalf("Spec 未按参数填写: %+v", rr.spec)
	}
	if len(rr.spec.WritableDirs) != 1 || rr.spec.WritableDirs[0] != ws.Root() {
		t.Fatalf("可写目录应只有工作区: %v", rr.spec.WritableDirs)
	}
	want := shellFor(runtime.GOOS)("make build")
	if rr.spec.MaxOutputBytes != commandOutputBytes || strings.Join(rr.spec.Command, "\x00") != strings.Join(want.argv, "\x00") ||
		rr.spec.RawCommandLine != want.raw {
		t.Fatalf("命令与输出上限不正确: %+v", rr.spec)
	}
	if rr.spec.Env[envRunID] != "01RUN" {
		t.Fatalf("应传入运行 ID: %v", rr.spec.Env)
	}

	rr.res = sandbox.Result{Stdout: "x", Stderr: "warn", Truncated: true, ExitCode: 0}
	res, _ = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": "noisy", "timeout_seconds": 9999}))
	if res.IsError || !strings.Contains(res.Content, "[输出已截断") || !strings.Contains(res.Content, "--- stderr ---\nwarn\n") {
		t.Fatalf("截断提示或 stderr 格式不正确: %+v", res)
	}
	if !res.Truncated {
		t.Fatal("输出流被截断时 ToolResult.Truncated 应为 true")
	}
	if rr.spec.Timeout != time.Duration(maxCommandTimeoutSeconds)*time.Second {
		t.Fatalf("超时应被截到上限，得到 %v", rr.spec.Timeout)
	}

	rr.err = model.Wrap(model.CodeCancelled, "取消", context.Canceled)
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": "x"})); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("Runner 的取消应原样返回，得到 %v", err)
	}

	d, err := tool.(Describer).Describe(rawArgs(t, map[string]any{"command": "rm -rf build\nls", "cwd": "sub", "allow_network": true}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Summary, "执行命令: rm -rf build …") || !strings.Contains(d.Summary, "目录 sub") || !strings.Contains(d.Summary, "允许联网") {
		t.Fatalf("摘要不正确: %q", d.Summary)
	}
	if d.Details["command"] != "rm -rf build\nls" || d.Details["cwd"] != "sub" || d.Details["sandbox"] != "none" || d.Details["allow_network"] != true {
		t.Fatalf("细节不正确: %+v", d.Details)
	}
}

func TestShellFor(t *testing.T) {
	// XP-02：Windows 上命令文本不经 exec 转义，整条命令行原样交给 cmd.exe，引号保持原样。
	got := shellFor("windows")(`git commit -m "fix bug" && dir "C:\Program Files"`)
	if len(got.argv) != 1 || got.argv[0] != "cmd.exe" {
		t.Fatalf("windows argv 应只有 cmd.exe: %v", got.argv)
	}
	if want := `cmd.exe /d /s /c "git commit -m "fix bug" && dir "C:\Program Files""`; got.raw != want {
		t.Fatalf("windows 原样命令行不正确:\n得到 %s\n期望 %s", got.raw, want)
	}
	if strings.Contains(got.raw, `\"`) {
		t.Fatal(`命令行里不应出现 \" 转义`)
	}
	unix := shellFor("linux")("ls")
	if len(unix.argv) != 3 || unix.argv[0] != "/bin/sh" || unix.argv[1] != "-c" || unix.argv[2] != "ls" || unix.raw != "" {
		t.Fatalf("unix shell 不正确: %+v", unix)
	}
}

// isolatedRunner 是报告 Seatbelt 隔离的 Runner。
type isolatedRunner struct{ recordingRunner }

func (*isolatedRunner) Mechanism() sandbox.Mechanism { return sandbox.MechanismSeatbelt }

// DOC-01：run_command 按 Runner 的隔离机制报告 Isolated；Direct 视为未隔离。
func TestRunCommandReportsIsolation(t *testing.T) {
	if isIsolated(NewRunCommand(sandbox.NewDirect(nil))) {
		t.Fatal("Direct 应视为未隔离")
	}
	if !isIsolated(NewRunCommand(&isolatedRunner{})) {
		t.Fatal("Seatbelt 等隔离机制应报告已隔离")
	}
	if isIsolated(NewReadFile()) || isIsolated(nil) {
		t.Fatal("不经沙箱的工具与扩展工具应视为未隔离")
	}
}
