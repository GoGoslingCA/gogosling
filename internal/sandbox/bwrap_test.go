package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func bwrapLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap 策略仅适用于 Linux")
	}
}

func containsBwrapArgs(args, sequence []string) bool {
	for i := 0; i+len(sequence) <= len(args); i++ {
		if slices.Equal(args[i:i+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

func TestBwrapArgsEnforceReadOnlyRootAndNamespaces(t *testing.T) {
	bwrapLinux(t)
	dir := t.TempDir()
	args, err := BuildBwrapArgs(BwrapProfile{Dir: dir, WritableDirs: []string{dir, dir + string(filepath.Separator) + "."}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{
		{"--die-with-parent"}, {"--new-session"}, {"--unshare-user"}, {"--unshare-pid"},
		{"--unshare-ipc"}, {"--unshare-uts"}, {"--unshare-net"}, {"--cap-drop", "ALL"},
		{"--ro-bind", "/", "/"}, {"--proc", "/proc"}, {"--dev", "/dev"},
		{"--tmpfs", "/tmp"}, {"--bind", dir, dir}, {"--chdir", dir}, {"--clearenv"},
	} {
		if !containsBwrapArgs(args, want) {
			t.Errorf("隔离策略缺少参数 %q，得到 %q", want, args)
		}
	}
	if n := strings.Count(strings.Join(args, "\n"), "--bind\n"); n != 1 {
		t.Fatalf("相同可写目录应只绑定一次，得到 %d", n)
	}
	args, err = BuildBwrapArgs(BwrapProfile{Dir: dir, AllowNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "--unshare-net") || slices.Contains(args, "--share-net") || slices.Contains(args, "--bind") {
		t.Fatalf("允许网络只保留宿主网络，不扩大可写边界：%q", args)
	}
}

func TestBwrapRejectsUnsafeWritableBoundaries(t *testing.T) {
	bwrapLinux(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", "/", "/tmp", "/tmp/..", "/proc", "/dev", "/sys", file, alias, filepath.Join(alias, "."), filepath.Join(dir, "missing")} {
		t.Run(path, func(t *testing.T) {
			if _, err := BuildBwrapArgs(BwrapProfile{Dir: dir, WritableDirs: []string{path}}); !model.IsCode(err, model.CodeInvalid) {
				t.Fatalf("不安全可写目录应返回 CodeInvalid，得到 %v", err)
			}
		})
	}
}

// fakeBwrap 创建不做隔离的测试辅助程序；只供验证包装参数和失败路径，不用于运行用户命令。
func fakeBwrap(t *testing.T, script string) string {
	t.Helper()
	bwrapLinux(t)
	path := filepath.Join(t.TempDir(), bwrapBinary)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func bwrapTestRunner(t *testing.T, path string, lookup func(string) (string, bool)) *Bwrap {
	t.Helper()
	file, info, digest, err := openBwrapHelper(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	return &Bwrap{binary: path, helperInfo: info, helperHash: digest, logger: slog.New(slog.DiscardHandler), lookup: lookup}
}

func TestBwrapRunPinsWritableFDsAndIsolatesHelperEnvironment(t *testing.T) {
	bwrapLinux(t)
	path := fakeBwrap(t, `printf 'argv:'
printf '%s\0' "$@"
printf '\nhelper:%s,%s\npolicy:' "${GOGOSLING_HOST_PRIVATE-unset}" "${LD_PRELOAD-unset}"
test -d /proc/self/fd/6 || exit 41
/bin/cat <&4
`)
	dir := t.TempDir()
	runner := bwrapTestRunner(t, path, func(key string) (string, bool) {
		switch key {
		case "PATH":
			return "/usr/bin:/bin", true
		case "GOGOSLING_HOST_PRIVATE":
			return "not-inherited", true
		}
		return "", false
	})
	res, err := runner.Run(context.Background(), Spec{
		Command: []string{"/bin/sh", "-c", "printf '%s' --literal", "--host-option"}, Dir: dir, WritableDirs: []string{dir},
		Env: map[string]string{"LD_PRELOAD": "/only/inside/sandbox.so", "CUSTOM": "value with spaces", "TMPDIR": "/host/tmp"},
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("参数包装应成功：%+v，%v", res, err)
	}
	visibleArgs, policy, ok := strings.Cut(res.Stdout, "\nhelper:unset,unset\npolicy:")
	if !ok {
		t.Fatalf("环境值不能进入 helper 环境或可见参数列表：%q", res.Stdout)
	}
	actualArgs := strings.Split(strings.TrimSuffix(strings.TrimPrefix(visibleArgs, "argv:"), "\x00"), "\x00")
	if !slices.Equal(actualArgs, []string{"--args", "4", "--", "/bin/sh", "-c", "printf '%s' --literal", "--host-option"}) {
		t.Fatalf("命令必须位于策略选项之后并保持参数边界：%q", actualArgs)
	}
	args := strings.Split(policy, "\x00")
	for _, want := range [][]string{
		{"--bind-fd", "6", dir}, {"--ro-bind-fd", "3", path}, {"--seccomp", "5"}, {"--setenv", "LD_PRELOAD", "/only/inside/sandbox.so"},
		{"--setenv", "CUSTOM", "value with spaces"}, {"--setenv", "TMPDIR", "/tmp"},
	} {
		if !containsBwrapArgs(args, want) {
			t.Errorf("包装参数缺少 %q", want)
		}
	}
	if slices.Contains(args, "--bind") || strings.Contains(res.Stderr, "preloaded") || strings.Contains(res.Stdout, "not-inherited") {
		t.Fatalf("应固定目录 FD，且不得把显式动态加载变量应用到宿主 helper：%+v", res)
	}
}

func TestBwrapRunNeverFallsBackAfterExecutionFailure(t *testing.T) {
	bwrapLinux(t)
	path := fakeBwrap(t, "/bin/cat <&4 >/dev/null\nprintf 'sandbox setup failed' >&2\nexit 37\n")
	runner := bwrapTestRunner(t, path, os.LookupEnv)
	dir := t.TempDir()
	marker := filepath.Join(dir, "must-not-exist")
	res, err := runner.Run(context.Background(), Spec{Command: []string{"/bin/sh", "-c", `echo escaped > "$1"`, "test", marker}, Dir: dir, WritableDirs: []string{dir}})
	if err != nil || res.ExitCode != 37 || res.Stderr != "sandbox setup failed" {
		t.Fatalf("隔离执行失败应原样返回：%+v，%v", res, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("失败后不得无隔离重试用户命令，文件状态：%v", err)
	}
	runner.binary = filepath.Join(dir, "missing-helper")
	if _, err := runner.Run(context.Background(), Spec{Command: []string{"/bin/true"}, Dir: dir}); err == nil {
		t.Fatal("辅助程序无法启动应报错，不得回退")
	}
}

func TestBwrapRunUsesTimeoutAndHonoursCancellation(t *testing.T) {
	bwrapLinux(t)
	path := fakeBwrap(t, "/bin/cat <&4 >/dev/null\nsleep 10 &\nwait\n")
	runner := bwrapTestRunner(t, path, os.LookupEnv)
	spec := Spec{Command: []string{"/bin/true"}, Dir: t.TempDir(), Timeout: 50 * time.Millisecond}
	start := time.Now()
	res, err := runner.Run(context.Background(), spec)
	if err != nil || !res.TimedOut || res.ExitCode != -1 || time.Since(start) > 3*time.Second {
		t.Fatalf("helper 及子进程应随超时结束：%+v，%v", res, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Run(ctx, spec); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("取消上下文应立即拒绝执行，得到 %v", err)
	}
}

func TestBwrapRejectsHelperReplacementAndWritableHelperParent(t *testing.T) {
	bwrapLinux(t)
	path := fakeBwrap(t, "/bin/cat <&4 >/dev/null\nexit 0\n")
	runner, err := newBwrap(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := runner.Run(context.Background(), Spec{Command: []string{"/bin/true"}, Dir: dir, WritableDirs: []string{filepath.Dir(path)}}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("helper 所在目录不能授权可写，得到 %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 19\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Spec{Command: []string{"/bin/true"}, Dir: dir}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("helper 被替换或修改后必须拒绝执行，得到 %v", err)
	}
}

func TestBwrapLargePolicyWriterIsBoundedWhenHelperLeavesReaderAlive(t *testing.T) {
	bwrapLinux(t)
	path := fakeBwrap(t, "sleep 20 </dev/null >/dev/null 2>&1 &\necho $!\nexit 37\n")
	runner := bwrapTestRunner(t, path, os.LookupEnv)
	start := time.Now()
	res, err := runner.Run(context.Background(), Spec{Command: []string{"/bin/true"}, Dir: t.TempDir(), Env: map[string]string{"LARGE_POLICY": strings.Repeat("x", 2<<20)}, Timeout: time.Second})
	if pid, parseErr := strconv.Atoi(strings.TrimSpace(res.Stdout)); parseErr == nil {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			t.Cleanup(func() { _ = process.Kill() })
		}
	}
	if err != nil || res.ExitCode != 37 || time.Since(start) > 2*time.Second {
		t.Fatalf("reader 被孙进程持有时也必须关闭 writer 并及时返回：%+v，%v", res, err)
	}
}

func TestNewBwrapProbesAndDeclaresDirectFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		if _, err := NewBwrap(nil); !model.IsCode(err, model.CodeUnavailable) {
			t.Fatalf("非 Linux 应拒绝 Bwrap，得到 %v", err)
		}
		return
	}
	path := fakeBwrap(t, "/bin/cat <&4 >/dev/null\nprintf 'bwrap: Creating new namespace failed: Operation not permitted' >&2\nexit 1\n")
	t.Setenv("PATH", filepath.Dir(path))
	if _, err := NewBwrap(nil); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("实际探测失败应返回 CodeUnavailable，得到 %v", err)
	}
	var log bytes.Buffer
	runner, caps := New(slog.New(slog.NewTextHandler(&log, nil)))
	if MechanismOf(runner) != MechanismNone || caps.Mechanism != MechanismBwrap || !caps.Available || !caps.Implemented {
		t.Fatalf("存在 helper 但隔离受限时应明确选择 Direct 并保留发现信息：%T，%+v", runner, caps)
	}
	if !strings.Contains(log.String(), "Operation not permitted") || !strings.Contains(log.String(), "无隔离") {
		t.Fatalf("回退日志必须说明隔离探测失败原因：%q", log.String())
	}
}

// newBwrapOrSkip 只在平台、辅助程序或宿主明确拒绝命名空间时跳过；其他探测失败视为缺陷。
func newBwrapOrSkip(t *testing.T) *Bwrap {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap 隔离仅适用于 Linux")
	}
	if _, err := exec.LookPath(bwrapBinary); err != nil {
		t.Skipf("宿主未安装 bwrap，无法进行真实隔离验证：%v", err)
	}
	runner, err := NewBwrap(nil)
	if err == nil {
		return runner
	}
	var probe *bwrapProbeError
	if errors.As(err, &probe) && !probe.timedOut {
		reason := strings.ToLower(probe.stderr)
		namespace := strings.Contains(reason, "namespace") || strings.Contains(reason, "uid map") || strings.Contains(reason, "gid map") || strings.Contains(reason, "mount")
		denied := strings.Contains(reason, "operation not permitted") || strings.Contains(reason, "permission denied") || strings.Contains(reason, "read-only file system") || strings.Contains(reason, "no permissions")
		if namespace && denied {
			t.Skipf("实际探测证明宿主限制了隔离能力：%v", err)
		}
	}
	t.Fatalf("bwrap 非预期探测失败，不能按平台限制跳过：%v", err)
	return nil
}

func TestBwrapActualWritableBoundaryAndSymlinkEscape(t *testing.T) {
	runner := newBwrapOrSkip(t)
	allowed, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(allowed, "escape")); err != nil {
		t.Fatal(err)
	}
	res, err := runner.Run(context.Background(), Spec{
		Command: []string{"/bin/sh", "-c", `echo good > allowed.txt && cat allowed.txt; echo bad > escape/escaped.txt`},
		Dir:     allowed, WritableDirs: []string{allowed},
	})
	if err != nil || res.ExitCode == 0 || strings.TrimSpace(res.Stdout) != "good" {
		t.Fatalf("可写目录应可写，符号链接逃逸应失败：%+v，%v", res, err)
	}
	if content, err := os.ReadFile(filepath.Join(allowed, "allowed.txt")); err != nil || string(content) != "good\n" {
		t.Fatalf("允许的文件应被写入：%q，%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("不允许的宿主目录不得被修改：%v", err)
	}
}

func TestBwrapActualPrivateTmpAndPIDNamespace(t *testing.T) {
	runner := newBwrapOrSkip(t)
	dir := t.TempDir()
	marker := "gogosling-private-" + filepath.Base(dir)
	res, err := runner.Run(context.Background(), Spec{
		Command: []string{"/bin/sh", "-c", `echo private > "$TMPDIR/$1" && cat "$TMPDIR/$1" && echo "$$"`, "test", marker},
		Dir:     dir, WritableDirs: []string{dir},
	})
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "private\n2" {
		t.Fatalf("/tmp 应可写且 shell 位于私有 PID 空间：%+v，%v", res, err)
	}
	if _, err := os.Stat(filepath.Join("/tmp", marker)); !os.IsNotExist(err) {
		t.Fatalf("私有临时文件不得出现在宿主 /tmp：%v", err)
	}
	res, err = runner.Run(context.Background(), Spec{Command: []string{"/bin/sh", "-c", `test ! -e "$TMPDIR/$1"`, "test", marker}, Dir: dir, WritableDirs: []string{dir}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("下一次运行必须使用新的临时空间：%+v，%v", res, err)
	}
}

func TestBwrapNetworkHelper(t *testing.T) {
	if os.Getenv("GOGOSLING_BWRAP_TEST_MODE") != "network" {
		return
	}
	conn, err := net.DialTimeout("tcp", os.Getenv("GOGOSLING_BWRAP_TEST_TARGET"), time.Second)
	if err != nil {
		os.Exit(3)
	}
	_ = conn.Close()
	os.Exit(0)
}

func TestBwrapActualNetworkPolicy(t *testing.T) {
	runner := newBwrapOrSkip(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dir := t.TempDir()
	// 测试二进制通常在 /tmp；复制到明确绑定的目录，使其在私有 /tmp 中仍可访问。
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	binary := filepath.Join(dir, "network-probe")
	out, err := os.OpenFile(binary, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("创建本地网络探测 helper：%v，%v", copyErr, closeErr)
	}
	spec := Spec{
		Command: []string{binary, "-test.run=^TestBwrapNetworkHelper$"}, Dir: dir, WritableDirs: []string{dir}, Timeout: 5 * time.Second,
		Env: map[string]string{"GOGOSLING_BWRAP_TEST_MODE": "network", "GOGOSLING_BWRAP_TEST_TARGET": listener.Addr().String()},
	}
	res, err := runner.Run(context.Background(), spec)
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("默认网络空间不得连接宿主回环监听：%+v，%v", res, err)
	}
	spec.AllowNetwork = true
	res, err = runner.Run(context.Background(), spec)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("明确允许网络时应能连接回环监听：%+v，%v", res, err)
	}
}

func TestBwrapActualTimeout(t *testing.T) {
	runner := newBwrapOrSkip(t)
	dir := t.TempDir()
	start := time.Now()
	res, err := runner.Run(context.Background(), Spec{Command: []string{"/bin/sh", "-c", "echo before; sleep " + strconv.Itoa(20) + " & wait"}, Dir: dir, WritableDirs: []string{dir}, Timeout: 100 * time.Millisecond})
	if err != nil || !res.TimedOut || res.ExitCode != -1 || !strings.Contains(res.Stdout, "before") || time.Since(start) > 3*time.Second {
		t.Fatalf("私有进程树应随超时被结束并保留已有输出：%+v，%v", res, err)
	}
}
