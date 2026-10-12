package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

const stopTestPID = 4242

// writeTestHandle 在测试数据目录写入 daemon.json。
func writeTestHandle(t *testing.T, home string) identity.Handle {
	t.Helper()
	h := identity.Handle{
		PID: stopTestPID, Endpoint: "http://127.0.0.1:1", Token: "tok", Version: "v",
		StartedAt: time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC),
	}
	if err := identity.NewHandleFile(home).Write(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	return h
}

func handleExists(t *testing.T, home string) bool {
	t.Helper()
	_, err := identity.NewHandleFile(home).Read(context.Background())
	if err != nil && !model.IsCode(err, model.CodeNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func stopHarness(t *testing.T) (*harness, *fakeDaemon) {
	t.Helper()
	h := newHarness(t, "", nil)
	h.c.stopGrace, h.c.stopWait, h.c.stopPoll = 50*time.Millisecond, 200*time.Millisecond, 5*time.Millisecond
	fd := &fakeDaemon{alivePIDs: map[int]bool{stopTestPID: true}}
	h.c.daemonCtl = fd
	return h, fd
}

// XP-06：句柄残留且 pid 已被无关进程复用时，绝不发信号，只删除残留的句柄。
func TestDaemonStopStaleHandleNeverSignals(t *testing.T) {
	for name, idErr := range map[string]error{
		"端点连不上":          model.NewError(model.CodeUnavailable, "connection refused"),
		"令牌被拒":           model.NewError(model.CodeUnauthorized, "bad token"),
		"started_at 不一致": errHandleMismatch,
	} {
		t.Run(name, func(t *testing.T) {
			h, fd := stopHarness(t)
			writeTestHandle(t, h.c.home)
			fd.identifyErr = idErr
			if code := h.run("daemon", "stop"); code != exitOK || !strings.Contains(h.out.String(), "未运行") {
				t.Fatalf("残留句柄应视为未运行，得到 %d %q %q", code, h.out.String(), h.errOut.String())
			}
			if len(fd.terminated) != 0 || fd.shutdowns != 0 {
				t.Fatalf("身份未确认时不应发出任何信号或关闭请求：terminate=%v shutdown=%d", fd.terminated, fd.shutdowns)
			}
			if !fd.alive(stopTestPID) {
				t.Fatal("复用了该 pid 的无关进程不应被结束")
			}
			if handleExists(t, h.c.home) {
				t.Fatal("残留的 daemon.json 应被删除")
			}
		})
	}
}

// 端点超时：可能是挂住或仍在启动的守护进程，无法判断——报告没有响应，保留句柄，不发信号。
func TestDaemonStopUnresponsiveKeepsHandle(t *testing.T) {
	h, fd := stopHarness(t)
	writeTestHandle(t, h.c.home)
	fd.identifyErr = model.NewError(model.CodeTimeout, "timeout")
	if code := h.run("daemon", "stop"); code != exitFailure || !strings.Contains(h.errOut.String(), "没有响应") {
		t.Fatalf("超时应报告没有响应，得到 %d %q", code, h.errOut.String())
	}
	if len(fd.terminated) != 0 || fd.shutdowns != 0 || !handleExists(t, h.c.home) {
		t.Fatalf("超时时不应发信号、不应删除句柄：terminate=%v shutdown=%d", fd.terminated, fd.shutdowns)
	}
}

// XP-07：身份确认后优先经 API 优雅关闭，进程自行退出（并自己删除 daemon.json）时不发信号。
func TestDaemonStopPrefersShutdownAPI(t *testing.T) {
	h, fd := stopHarness(t)
	writeTestHandle(t, h.c.home)
	fd.exitOnShutdown = true
	if code := h.run("daemon", "stop"); code != exitOK || !strings.Contains(h.out.String(), "已停止") {
		t.Fatalf("应经 API 停止，得到 %d %q %q", code, h.out.String(), h.errOut.String())
	}
	if fd.identified != 1 || fd.shutdowns != 1 || len(fd.terminated) != 0 {
		t.Fatalf("应先确认身份再请求关闭且不发信号：identify=%d shutdown=%d terminate=%v", fd.identified, fd.shutdowns, fd.terminated)
	}
	if handleExists(t, h.c.home) {
		t.Fatal("停止后不应留下 daemon.json")
	}
}

// 关闭端点不可用（例如更早版本的守护进程）：立即退回到信号 / 强制结束，并清理句柄。
func TestDaemonStopFallsBackWhenShutdownUnavailable(t *testing.T) {
	h, fd := stopHarness(t)
	writeTestHandle(t, h.c.home)
	fd.shutdownErr = model.NewError(model.CodeNotFound, "no route")
	if code := h.run("daemon", "stop"); code != exitOK {
		t.Fatalf("应退回到结束进程，得到 %d %q", code, h.errOut.String())
	}
	if len(fd.terminated) != 1 || fd.terminated[0] != stopTestPID {
		t.Fatalf("应结束已确认身份的进程，terminate=%v", fd.terminated)
	}
	if handleExists(t, h.c.home) {
		t.Fatal("强制结束后应删除残留的 daemon.json（令牌不留在磁盘上）")
	}
}

// 关闭请求被接受但进程在宽限期内没有退出：超时后才结束进程。
func TestDaemonStopTerminatesAfterGraceTimeout(t *testing.T) {
	h, fd := stopHarness(t)
	writeTestHandle(t, h.c.home)
	start := time.Now()
	if code := h.run("daemon", "stop"); code != exitOK {
		t.Fatalf("宽限期后应结束进程，得到 %d %q", code, h.errOut.String())
	}
	if fd.shutdowns != 1 || len(fd.terminated) != 1 {
		t.Fatalf("应先请求关闭、超时后再结束：shutdown=%d terminate=%v", fd.shutdowns, fd.terminated)
	}
	if time.Since(start) < h.c.stopGrace {
		t.Fatal("应等满宽限期才结束进程")
	}
}

// 删除句柄前确认它仍是同一个：期间新启动的守护进程写入的句柄不能被删掉。
func TestRemoveHandleKeepsNewerHandle(t *testing.T) {
	h, _ := stopHarness(t)
	old := writeTestHandle(t, h.c.home)
	newer := old
	newer.Token, newer.PID = "other", 99
	if err := identity.NewHandleFile(h.c.home).Write(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if err := h.c.removeHandleIfSame(context.Background(), h.c.home, old); err != nil {
		t.Fatal(err)
	}
	if !handleExists(t, h.c.home) {
		t.Fatal("不属于被停止实例的句柄不应被删除")
	}
}

func TestMatchStartedAt(t *testing.T) {
	at := time.Date(2026, 10, 10, 8, 0, 0, 123456789, time.UTC)
	if err := matchStartedAt("2026-10-10T08:00:00Z", at); err != nil {
		t.Fatalf("秒精度相同应视为一致: %v", err)
	}
	if err := matchStartedAt("2026-10-10T16:00:00+08:00", at); err != nil {
		t.Fatalf("不同时区的同一时刻应视为一致: %v", err)
	}
	for _, bad := range []string{"2026-10-10T08:00:01Z", "", "garbage"} {
		if err := matchStartedAt(bad, at); !errors.Is(err, errHandleMismatch) {
			t.Errorf("started_at %q 应判定为不一致，实际 %v", bad, err)
		}
	}
	if err := matchStartedAt("2026-10-10T08:00:00Z", time.Time{}); !errors.Is(err, errHandleMismatch) {
		t.Error("句柄缺少 started_at 时无法确认身份")
	}
}
