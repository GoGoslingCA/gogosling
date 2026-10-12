package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// shell 返回当前平台执行一段 shell 文本的 argv。
func shell(script string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", script}
	}
	return []string{"/bin/sh", "-c", script}
}

// sleepCmd 返回大约休眠 secs 秒的 shell 文本。
func sleepCmd(secs int) string {
	if runtime.GOOS == "windows" {
		return "ping -n " + strconv.Itoa(secs+1) + " 127.0.0.1 >NUL"
	}
	return "sleep " + strconv.Itoa(secs)
}

func TestNormalizeRejectsBadSpec(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		spec Spec
	}{
		{"空命令", Spec{Dir: dir}},
		{"空工作目录", Spec{Command: []string{"x"}}},
		{"相对工作目录", Spec{Command: []string{"x"}, Dir: "rel"}},
		{"不存在的工作目录", Spec{Command: []string{"x"}, Dir: filepath.Join(dir, "missing")}},
		{"非法环境变量键", Spec{Command: []string{"x"}, Dir: dir, Env: map[string]string{"A=B": "1"}}},
		{"相对可写目录", Spec{Command: []string{"x"}, Dir: dir, WritableDirs: []string{"rel"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := normalize(c.spec); !model.IsCode(err, model.CodeInvalid) {
				t.Fatalf("期望 CodeInvalid，得到 %v", err)
			}
		})
	}
}

func TestNormalizeAppliesDefaultsAndCaps(t *testing.T) {
	dir := t.TempDir()
	got, err := normalize(Spec{Command: []string{"x"}, Dir: dir, Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got.Timeout != MaxTimeout {
		t.Errorf("超时应被截到 MaxTimeout，得到 %v", got.Timeout)
	}
	if got.MaxOutputBytes != DefaultMaxOutputBytes {
		t.Errorf("输出上限应为默认值，得到 %d", got.MaxOutputBytes)
	}
	got, _ = normalize(Spec{Command: []string{"x"}, Dir: dir})
	if got.Timeout != DefaultTimeout {
		t.Errorf("零超时应为默认值，得到 %v", got.Timeout)
	}
}

func TestBuildEnvOnlyPassesAllowlistAndExtras(t *testing.T) {
	lookup := func(k string) (string, bool) {
		switch k {
		case "PATH":
			return "/bin", true
		case "HOME":
			return "/home/u", true
		case "SECRET_TOKEN":
			return "leak", true
		}
		return "", false
	}
	env := buildEnv(map[string]string{"ZED": "1", "PATH": "/override"}, lookup)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "SECRET_TOKEN") {
		t.Fatal("白名单之外的变量不应透传")
	}
	if !strings.Contains(joined, "PATH=/override") {
		t.Fatal("Spec.Env 应覆盖白名单变量")
	}
	if !strings.Contains(joined, "HOME=/home/u") || !strings.Contains(joined, "ZED=1") {
		t.Fatalf("环境不完整: %q", joined)
	}
}

func TestLimitedBufferTruncates(t *testing.T) {
	b := newLimitedBuffer(5)
	if n, _ := b.Write([]byte("abc")); n != 3 {
		t.Fatalf("Write 应报告写入 3 字节，得到 %d", n)
	}
	if n, _ := b.Write([]byte("defgh")); n != 5 {
		t.Fatalf("Write 应报告写入 5 字节，得到 %d", n)
	}
	if b.String() != "abcde" || !b.truncated {
		t.Fatalf("得到 %q truncated=%v", b.String(), b.truncated)
	}
}

func TestDirectRunCapturesOutputAndExitCode(t *testing.T) {
	r := NewDirect(nil)
	dir := t.TempDir()
	res, err := r.Run(context.Background(), Spec{Command: shell("echo hello"), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Stdout) != "hello" || res.ExitCode != 0 || res.TimedOut || res.Truncated {
		t.Fatalf("意外结果: %+v", res)
	}
	res, err = r.Run(context.Background(), Spec{Command: shell("exit 3"), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("退出码应为 3，得到 %d", res.ExitCode)
	}
	if res.Duration <= 0 {
		t.Fatal("应记录耗时")
	}
}

func TestDirectRunUsesWorkingDirAndStdin(t *testing.T) {
	r := NewDirect(nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	list := "ls"
	if runtime.GOOS == "windows" {
		list = "dir /B"
	}
	res, err := r.Run(context.Background(), Spec{Command: shell(list), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "marker.txt") {
		t.Fatalf("应在工作目录中执行，输出: %q", res.Stdout)
	}

	cat := "cat"
	if runtime.GOOS == "windows" {
		// more 原样回显标准输入；findstr 的引号经 cmd /C 转义后在 CI 上读不到管道输入。
		cat = "more"
	}
	res, err = r.Run(context.Background(), Spec{Command: shell(cat), Dir: dir, Stdin: "from stdin\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "from stdin") {
		t.Fatalf("标准输入应被传入，输出: %q", res.Stdout)
	}
}

func TestDirectRunDoesNotLeakDaemonEnv(t *testing.T) {
	t.Setenv("GOGOSLING_TEST_SECRET", "must-not-leak")
	r := NewDirect(nil)
	dump := "env"
	if runtime.GOOS == "windows" {
		dump = "set"
	}
	res, err := r.Run(context.Background(), Spec{Command: shell(dump), Dir: t.TempDir(), Env: map[string]string{"GOGOSLING_EXTRA": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "GOGOSLING_TEST_SECRET") {
		t.Fatal("守护进程环境变量泄露到了子进程")
	}
	if !strings.Contains(res.Stdout, "GOGOSLING_EXTRA=yes") {
		t.Fatalf("Spec.Env 应传入子进程，输出: %q", res.Stdout)
	}
}

func TestDirectRunTruncatesOutput(t *testing.T) {
	r := NewDirect(nil)
	script := "i=0; while [ $i -lt 2000 ]; do echo 0123456789abcdef0123456789abcdef; i=$((i+1)); done"
	if runtime.GOOS == "windows" {
		script = "for /L %i in (1,1,2000) do @echo 0123456789abcdef0123456789abcdef"
	}
	res, err := r.Run(context.Background(), Spec{Command: shell(script), Dir: t.TempDir(), MaxOutputBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Stdout) != 1024 {
		t.Fatalf("输出应被截断到 1024 字节: truncated=%v len=%d", res.Truncated, len(res.Stdout))
	}
	if res.ExitCode != 0 {
		t.Fatalf("截断不应影响退出码，得到 %d", res.ExitCode)
	}
}

func TestDirectRunTimesOut(t *testing.T) {
	r := NewDirect(nil)
	start := time.Now()
	res, err := r.Run(context.Background(), Spec{Command: shell("echo before; " + sleepCmd(5) + "; echo after"), Dir: t.TempDir(), Timeout: 300 * time.Millisecond})
	if runtime.GOOS == "windows" {
		res, err = r.Run(context.Background(), Spec{Command: shell("echo before& " + sleepCmd(5) + "& echo after"), Dir: t.TempDir(), Timeout: 300 * time.Millisecond})
	}
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("应报告超时: %+v", res)
	}
	if !strings.Contains(res.Stdout, "before") || strings.Contains(res.Stdout, "after") {
		t.Fatalf("应保留超时前的输出且不含超时后的输出: %q", res.Stdout)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("超时后应尽快返回，耗时 %v", elapsed)
	}
}

func TestDirectRunKillsBackgroundChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("进程组语义只在 POSIX 上验证")
	}
	r := NewDirect(nil)
	start := time.Now()
	// 后台 sleep 持有 stdout 管道：若只杀直接子进程、不杀进程组，Wait 会等到后台进程结束。
	res, err := r.Run(context.Background(), Spec{Command: shell("sleep 20 & sleep 20"), Dir: t.TempDir(), Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("应超时: %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("进程组应被整体结束，耗时 %v", elapsed)
	}
}

func TestDirectRunHonoursCancellation(t *testing.T) {
	r := NewDirect(nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := r.Run(ctx, Spec{Command: shell(sleepCmd(5)), Dir: t.TempDir(), Timeout: 10 * time.Second})
	if !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("期望 CodeCancelled，得到 %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, Spec{Command: shell("echo x"), Dir: t.TempDir()}); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("已取消的 ctx 应直接返回 CodeCancelled，得到 %v", err)
	}
}

func TestDirectRunMissingBinary(t *testing.T) {
	r := NewDirect(nil)
	_, err := r.Run(context.Background(), Spec{Command: []string{"gogosling-definitely-missing-binary"}, Dir: t.TempDir()})
	if !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("期望 CodeNotFound，得到 %v", err)
	}
}

func TestDetectWith(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/helper", nil }
	missing := func(string) (string, error) { return "", errors.New("missing") }

	c := DetectWith("darwin", found)
	if c.Mechanism != MechanismSeatbelt || !c.Available || !c.Implemented || c.Path != "/usr/bin/helper" {
		t.Fatalf("darwin + sandbox-exec: %+v", c)
	}
	c = DetectWith("darwin", missing)
	if c.Mechanism != MechanismSeatbelt || c.Available {
		t.Fatalf("darwin 无 sandbox-exec: %+v", c)
	}
	c = DetectWith("linux", found)
	if c.Mechanism != MechanismBwrap || !c.Available || !c.Implemented {
		t.Fatalf("linux + bwrap 应报告辅助程序可用且已实现: %+v", c)
	}
	c = DetectWith("windows", found)
	if c.Mechanism != MechanismNone || c.Available {
		t.Fatalf("windows 应无隔离机制: %+v", c)
	}
}

func TestMechanismOf(t *testing.T) {
	if MechanismOf(NewDirect(nil)) != MechanismNone {
		t.Fatal("Direct 应报告无隔离")
	}
	if MechanismOf(runnerFunc(nil)) != MechanismNone {
		t.Fatal("未实现 Isolator 的 Runner 应视为无隔离")
	}
}

type runnerFunc func(context.Context, Spec) (Result, error)

func (f runnerFunc) Run(ctx context.Context, s Spec) (Result, error) { return f(ctx, s) }

func TestNewReturnsSomeRunner(t *testing.T) {
	r, caps := New(nil)
	if r == nil {
		t.Fatal("New 不应返回 nil")
	}
	if caps.Platform != runtime.GOOS {
		t.Fatalf("Capabilities.Platform 应为 %s，得到 %s", runtime.GOOS, caps.Platform)
	}
	if runtime.GOOS == "darwin" && caps.Available && MechanismOf(r) != MechanismSeatbelt {
		t.Fatal("darwin 且 sandbox-exec 可用时应选择 Seatbelt")
	}
}

func TestBuildSeatbeltProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Seatbelt profile 只在 POSIX 路径语义下有意义；Windows 上 /tmp 不是绝对路径")
	}
	p, err := BuildSeatbeltProfile(SeatbeltProfile{WritableDirs: []string{`/tmp/a "quoted"`, "/tmp/b"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`(subpath "/tmp/a \"quoted\"")`, `(subpath "/tmp/b")`, "(deny network*)", "(deny default)"} {
		if !strings.Contains(p, want) {
			t.Errorf("profile 缺少 %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "{{") {
		t.Error("占位符未全部替换")
	}
	p, err = BuildSeatbeltProfile(SeatbeltProfile{AllowNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "(allow network*)") || strings.Contains(p, "(allow file-write*\n  (subpath") {
		t.Errorf("允许网络且无可写目录的 profile 不正确:\n%s", p)
	}
	if _, err := BuildSeatbeltProfile(SeatbeltProfile{WritableDirs: []string{"relative"}}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("相对可写目录应为 CodeInvalid，得到 %v", err)
	}
}

func TestNewSeatbeltUnavailableOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("只验证非 darwin 的行为")
	}
	if _, err := NewSeatbelt(nil); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("期望 CodeUnavailable，得到 %v", err)
	}
}
