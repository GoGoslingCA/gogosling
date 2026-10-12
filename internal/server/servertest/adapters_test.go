package servertest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/secret"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// TestAdaptersMirrorComposition 守住测试替身与组合根适配器的关键一致点（LAY-08）：
// 凭据错误映射、删除不存在的凭据视为成功、channel 起源的会话标记为不可信输入。
func TestAdaptersMirrorComposition(t *testing.T) {
	cases := []struct {
		err  error
		code model.Code
	}{
		{secret.ErrNotFound, model.CodeNotFound},
		{secret.ErrInvalidName, model.CodeInvalid},
		{secret.ErrTooLarge, model.CodeInvalid},
		{secret.ErrCorrupt, model.CodeUnavailable},
		{secret.ErrBackendUnavailable, model.CodeUnavailable},
		{context.Canceled, model.CodeCancelled},
		{context.DeadlineExceeded, model.CodeTimeout},
		{errors.New("disk on fire"), model.CodeUnavailable},
	}
	for _, c := range cases {
		err := mapSecretErr("channel:x:secret", c.err)
		if !model.IsCode(err, c.code) {
			t.Errorf("%v 应映射为 %s，实际 %v", c.err, c.code, err)
		}
		if !errors.Is(err, c.err) {
			t.Errorf("映射后的错误应保留原哨兵 %v", c.err)
		}
	}
	if mapSecretErr("x", nil) != nil {
		t.Error("nil 应映射为 nil")
	}

	ctx := context.Background()
	env, err := NewEnv(ctx, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Close() })
	adapter := secretAdapter{env.Secrets}
	if err := adapter.Delete(ctx, "never-written"); err != nil {
		t.Fatalf("删除不存在的凭据应视为成功（与生产一致），实际 %v", err)
	}
	if err := adapter.Set(ctx, "big", strings.Repeat("x", 1<<20)); err != nil && !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("超大凭据应返回 invalid，实际 %v", err)
	}

	a, err := env.SeedAgent(ctx, "可信度")
	if err != nil {
		t.Fatal(err)
	}
	resolver := runResolver{store: env.Store, workspaces: workspace.New(env.Dir)}
	now := time.Now().UTC()
	for _, origin := range []model.Origin{model.OriginDesktop, model.OriginChannel} {
		sess := &model.Session{
			ID: model.NewID(), AgentID: a.ID, Origin: origin, Key: a.ID + ":" + string(origin) + ":" + model.NewID(),
			Status: model.SessionActive, CreatedAt: now, UpdatedAt: now,
		}
		if err := env.Store.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		run := &model.Run{ID: model.NewID(), SessionID: sess.ID, AgentID: a.ID, Status: model.RunRunning, Trigger: model.TriggerUser, CreatedAt: now}
		if err := env.Store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		rc, err := resolver.ResolveRunContext(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if want := origin == model.OriginChannel; rc.Untrusted != want {
			t.Errorf("origin=%s 的运行 Untrusted = %v，期望 %v", origin, rc.Untrusted, want)
		}
	}
}
