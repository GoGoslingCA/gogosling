package extension

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func enableIdleMCP(t *testing.T, env *testEnv) *model.Extension {
	t.Helper()
	ext, err := env.m.RegisterMCP(context.Background(), "local", model.MCPServerConfig{Transport: model.MCPStdio, Command: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.m.Enable(context.Background(), ext.ID); err != nil {
		t.Fatal(err)
	}
	return ext
}

func awaitMCPClosed(t *testing.T, client *fakeMCP) {
	t.Helper()
	select {
	case <-client.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("闲置连接应在有限时间内关闭")
	}
}

func TestMCP闲置释放后按需重连并重新固定声明检查(t *testing.T) {
	env := newTestEnv(t, WithMCPIdleTimeout(20*time.Millisecond))
	ext := enableIdleMCP(t, env)
	first := env.lastClient()
	awaitMCPClosed(t, first)
	if env.m.Status(ext.ID).Connected {
		t.Fatal("闲置连接应显示已断开")
	}
	agent := testAgent(ext.ID)
	res, err := env.m.CallTool(context.Background(), agent, model.ToolCall{ID: "c1", Name: "search", Arguments: json.RawMessage(`{}`)})
	if err != nil || res.IsError || env.dialCount() != 2 {
		t.Fatalf("下一次调用应重新连接而不更改权限：%+v / %v / %d", res, err, env.dialCount())
	}
	awaitMCPClosed(t, env.lastClient())
	env.m.dial = func(context.Context, *model.Extension) (MCPClient, error) {
		return newFakeMCP([]model.ToolSpec{{Name: "search", Description: "changed", InputSchema: json.RawMessage(`{"type":"object"}`)}}), nil
	}
	_, err = env.m.CallTool(context.Background(), agent, model.ToolCall{ID: "c2", Name: "search", Arguments: json.RawMessage(`{}`)})
	if !model.IsCode(err, model.CodeConflict) || !env.m.Status(ext.ID).ToolsChanged {
		t.Fatalf("重新连接不能绕过工具声明变化确认：%v / %+v", err, env.m.Status(ext.ID))
	}
}

func TestMCP活动调用和并发使用不被闲置期限中断(t *testing.T) {
	env := newTestEnv(t, WithMCPIdleTimeout(time.Hour))
	ext := enableIdleMCP(t, env)
	client := env.lastClient()
	started := make(chan struct{}, 2)
	finish := make(chan struct{})
	client.mu.Lock()
	client.callFn = func(string, json.RawMessage) (model.ToolResult, error) {
		started <- struct{}{}
		<-finish
		if client.isClosed() {
			return model.ToolResult{}, model.NewError(model.CodeUnavailable, "连接已关闭")
		}
		return model.ToolResult{Content: "done"}, nil
	}
	client.mu.Unlock()
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := env.m.CallTool(context.Background(), testAgent(ext.ID), model.ToolCall{Name: "search", Arguments: json.RawMessage(`{}`)})
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("两个调用都应进入连接")
		}
	}
	slot := env.m.slot(ext.ID)
	slot.mu.Lock()
	slot.idle = 15 * time.Millisecond
	slot.mu.Unlock()
	select {
	case <-client.Done():
		t.Fatal("活动调用不得因闲置期限中断")
	case <-time.After(60 * time.Millisecond):
	}
	close(finish)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	awaitMCPClosed(t, client)
}

func TestMCP关闭停止计时器且禁止后续重连(t *testing.T) {
	env := newTestEnv(t, WithMCPIdleTimeout(time.Hour))
	ext := enableIdleMCP(t, env)
	slot := env.m.slot(ext.ID)
	client := env.lastClient()
	if err := env.m.Close(); err != nil {
		t.Fatal(err)
	}
	if !client.isClosed() || env.m.slot(ext.ID) != nil {
		t.Fatal("关闭必须释放连接并禁止创建新槽")
	}
	slot.mu.Lock()
	timer, retired := slot.timer, slot.retired
	slot.mu.Unlock()
	if timer != nil || !retired {
		t.Fatal("关闭后不得留下有效的闲置回收计时器")
	}
	if _, err := env.m.CallTool(context.Background(), testAgent(ext.ID), model.ToolCall{Name: "search"}); err == nil || env.dialCount() != 1 {
		t.Fatalf("关闭后不得重新启动服务：%v / %d", err, env.dialCount())
	}
}

func TestMCP旧租约回收回调不能关闭重新使用的连接(t *testing.T) {
	env := newTestEnv(t, WithMCPIdleTimeout(time.Hour))
	ext := enableIdleMCP(t, env)
	slot := env.m.slot(ext.ID)
	// 在截止点前续租并持有多倍闲置时限，模拟旧计时器已经排队等待锁。
	slot.mu.Lock()
	slot.idle = 20 * time.Millisecond
	oldEpoch := slot.epoch
	slot.pinLocked()
	if slot.epoch == oldEpoch || slot.timer != nil {
		t.Fatal("续租必须作废旧计时器")
	}
	slot.mu.Unlock()
	select {
	case <-env.lastClient().Done():
		t.Fatal("旧计时器不能回收已续租连接")
	case <-time.After(60 * time.Millisecond):
	}
	slot.release()
	awaitMCPClosed(t, env.lastClient())
}

func TestMCP固定声明保存失败不允许首次启用或接受变化(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	ext, err := env.m.RegisterMCP(ctx, "local", model.MCPServerConfig{Transport: model.MCPStdio, Command: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	env.repo.fail["update"] = errors.New("fixture database failure")
	if _, err := env.m.Enable(ctx, ext.ID); err == nil || !env.lastClient().isClosed() {
		t.Fatalf("首次摘要保存失败时必须关闭连接并拒绝启用：%v", err)
	}
	delete(env.repo.fail, "update")
	enabled, err := env.m.Enable(ctx, ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := enabled.Digest
	env.lastClient().setSpecs([]model.ToolSpec{{Name: "search", Description: "new declaration", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	env.repo.fail["update"] = errors.New("fixture database failure")
	if _, err := env.m.AcceptTools(ctx, ext.ID); err == nil || !env.m.Status(ext.ID).ToolsChanged {
		t.Fatalf("保存失败时新工具必须保持未确认：%v / %+v", err, env.m.Status(ext.ID))
	}
	stored, _ := env.m.Get(ctx, ext.ID)
	if stored.Digest != oldDigest {
		t.Fatal("保存失败不得改变持久化摘要")
	}
	if specs, err := env.m.ToolsFor(ctx, testAgent(ext.ID)); err != nil || len(specs) != 0 {
		t.Fatalf("失败的确认不得放行模型工具：%+v / %v", specs, err)
	}
	delete(env.repo.fail, "update")
	if _, err := env.m.AcceptTools(ctx, ext.ID); err != nil || env.m.Status(ext.ID).ToolsChanged {
		t.Fatalf("手动重试保存成功后应恢复：%v", err)
	}
}
