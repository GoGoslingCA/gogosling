package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/secret"
)

// daemon 是测试中运行的一个进程内守护进程。
type daemon struct {
	app    *App
	home   string
	cancel context.CancelFunc
	done   chan error
	handle identity.Handle
}

// fakeWorkerCommand 返回让引擎把测试二进制自身当作假工作进程启动的命令行。
func fakeWorkerCommand(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return strconv.Quote(exe) + " " + fakeWorkerArg
}

// startDaemon 在临时数据目录里启动 App，等到 daemon.json 写好后返回；测试结束时优雅关闭。
func startDaemon(t *testing.T, home string) *daemon {
	t.Helper()
	return startDaemonWith(t, home)
}

// startDaemonWith 与 startDaemon 相同，但可以追加 Option（后者覆盖默认测试选项）。
func startDaemonWith(t *testing.T, home string, extra ...Option) *daemon {
	t.Helper()
	t.Setenv(engine.EnvWorkerCmd, fakeWorkerCommand(t))
	if home == "" {
		home = t.TempDir()
	}
	secrets, err := secret.OpenWith(context.Background(), home, nil, secret.Options{DisableKeyring: true})
	if err != nil {
		t.Fatal(err)
	}
	opts := append([]Option{WithSecretStore(secrets), WithWorkerStatusInterval(50 * time.Millisecond), WithShutdownTimeout(10 * time.Second)}, extra...)
	a, err := New(config.Config{DataDir: home, ListenAddr: "127.0.0.1:0"}, discardLogger(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{app: a, home: home, cancel: cancel, done: make(chan error, 1)}
	go func() { d.done <- a.Run(ctx) }()
	select {
	case <-a.Ready():
	case err := <-d.done:
		cancel()
		t.Fatalf("守护进程启动失败: %v", err)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("等待守护进程就绪超时")
	}
	h, err := identity.NewHandleFile(home).Read(context.Background())
	if err != nil {
		t.Fatalf("读取 daemon.json 失败: %v", err)
	}
	d.handle = h
	t.Cleanup(func() { d.stop(t) })
	return d
}

// do 带令牌请求本地 API，返回状态码与响应体。
func (d *daemon) do(t *testing.T, method, path string, body io.Reader) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, d.handle.Endpoint+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+d.handle.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// getJSON 带令牌 GET 并把 200 响应解码到 out。
func (d *daemon) getJSON(t *testing.T, path string, out any) {
	t.Helper()
	code, raw := d.do(t, http.MethodGet, path, nil)
	if code != http.StatusOK {
		t.Fatalf("GET %s 返回 %d: %s", path, code, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("解码 %s 失败: %v（%s）", path, err, raw)
	}
}

// stop 优雅关闭并等待 Run 返回；可重复调用。
func (d *daemon) stop(t *testing.T) error {
	t.Helper()
	d.cancel()
	select {
	case err, ok := <-d.done:
		if ok {
			close(d.done)
		}
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("等待守护进程退出超时")
		return nil
	}
}

func TestDaemonLifecycleLockAndHandleFile(t *testing.T) {
	d := startDaemon(t, "")
	if d.handle.PID != os.Getpid() || d.handle.Endpoint != "http://"+d.app.Addr() || d.handle.Token == "" {
		t.Fatalf("daemon.json 内容不对: %+v（addr %s）", d.handle, d.app.Addr())
	}
	if fi, err := os.Stat(filepath.Join(d.home, identity.HandleFileName)); err != nil {
		t.Fatal(err)
	} else if mode := fi.Mode().Perm(); mode&0o077 != 0 && !isWindows() {
		t.Fatalf("daemon.json 权限应为 0600，得到 %v", mode)
	}

	resp, err := http.Get(d.handle.Endpoint + "/healthz")
	if err != nil {
		t.Fatalf("healthz 请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz 状态码 %d", resp.StatusCode)
	}
	// 未带令牌访问 API 应被拒绝。
	resp, err = http.Get(d.handle.Endpoint + "/api/v1/version")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未带令牌应 401，得到 %d", resp.StatusCode)
	}

	// 同一数据目录的第二个守护进程：锁冲突，带 pid。
	second, err := New(config.Config{DataDir: d.home, ListenAddr: "127.0.0.1:0"}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	err = second.Run(context.Background())
	var me *model.Error
	if !model.IsCode(err, model.CodeConflict) || !errors.As(err, &me) || me.Details["pid"] != os.Getpid() {
		t.Fatalf("重复启动应返回带 pid 的 conflict，得到 %v", err)
	}
	// 冲突的那次启动不能动到正在运行的实例的句柄文件。
	if _, err := os.Stat(filepath.Join(d.home, identity.HandleFileName)); err != nil {
		t.Fatalf("冲突的启动不应删除 daemon.json: %v", err)
	}

	if err := d.stop(t); err != nil {
		t.Fatalf("优雅关闭返回错误: %v", err)
	}
	for _, name := range []string{identity.HandleFileName, LockFileName} {
		if _, err := os.Stat(filepath.Join(d.home, name)); !os.IsNotExist(err) {
			t.Errorf("关闭后 %s 应被删除，得到 %v", name, err)
		}
	}
	// 关闭后同一目录可以再次启动（锁已释放、数据库已关闭）。
	again := startDaemon(t, d.home)
	if again.handle.Token == d.handle.Token {
		t.Fatal("每次启动都应生成新的访问令牌")
	}
}

func TestRunCanOnlyBeCalledOnce(t *testing.T) {
	a, err := New(config.Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.ran = true
	a.mu.Unlock()
	if err := a.Run(context.Background()); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("第二次 Run 应 conflict，得到 %v", err)
	}
}

func TestStartupFailureReleasesEverything(t *testing.T) {
	home := t.TempDir()
	t.Setenv(engine.EnvWorkerCmd, fakeWorkerCommand(t))
	// 占住监听端口，让 Listen 失败：此时数据库、引擎、网关都已启动，必须全部逆序关闭。
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	secrets, _ := secret.OpenWith(context.Background(), home, nil, secret.Options{DisableKeyring: true})
	a, err := New(config.Config{DataDir: home, ListenAddr: blocker.Addr().String()}, discardLogger(), WithSecretStore(secrets))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("端口被占用时启动应失败")
	}
	for _, name := range []string{identity.HandleFileName, LockFileName} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Errorf("启动失败后 %s 不应残留，得到 %v", name, err)
		}
	}
	// 资源已释放：同一目录可以立即正常启动。
	startDaemon(t, home)
}

// POST /api/v1/system/shutdown 经组合根提供的 Options.Shutdown 触发优雅关闭：
// Run 正常返回（nil），daemon.json 与 daemon.lock 被删除（Windows 上桌面壳与 CLI 依赖这条途径）。
func TestShutdownEndpointStopsDaemonGracefully(t *testing.T) {
	d := startDaemon(t, "")
	code, raw := d.do(t, http.MethodPost, "/api/v1/system/shutdown", nil)
	if code != http.StatusAccepted {
		t.Fatalf("关闭请求应返回 202，得到 %d: %s", code, raw)
	}
	select {
	case err := <-d.done:
		close(d.done)
		if err != nil {
			t.Fatalf("经 API 关闭后 Run 应返回 nil，得到 %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("经 API 请求关闭后守护进程没有退出")
	}
	for _, name := range []string{identity.HandleFileName, LockFileName} {
		if _, err := os.Stat(filepath.Join(d.home, name)); !os.IsNotExist(err) {
			t.Errorf("关闭后 %s 应被删除，得到 %v", name, err)
		}
	}
}

// requestShutdown 在 Run 之前或重复调用都无害。
func TestRequestShutdownIsSafeBeforeRunAndRepeated(t *testing.T) {
	a, err := New(config.Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	a.requestShutdown()
	a.requestShutdown()
}

func isWindows() bool { return os.PathSeparator == '\\' }
