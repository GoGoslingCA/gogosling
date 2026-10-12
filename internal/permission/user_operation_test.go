package permission

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func manualRequest(env *testEnv) Request {
	req := env.req(model.OperationFileChangeTool, model.RiskWrite, model.AutonomyAutoInSandbox, map[string]any{"path": "notes.md", "change_id": "change-1", "preview_digest": "digest"})
	req.RunID = ""
	req.OperationID = "operation-1"
	req.Call.Arguments = json.RawMessage(`{"change_id":"change-1","preview_digest":"digest"}`)
	return req
}
func waitOperationApproval(t *testing.T, sub *event.Subscription) *model.ApprovalRequest {
	t.Helper()
	select {
	case ev := <-sub.C:
		if ev.RunID != "" {
			t.Fatal("人工审批事件不能携带伪造Run")
		}
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("人工事件必须按Operation表示: %T", ev.Payload)
		}
		if _, ok := payload["run_id"]; ok {
			t.Fatal("人工事件不应伪装运行")
		}
		approval, ok := payload["approval"].(*model.ApprovalRequest)
		if !ok || approval.OperationID == "" || approval.RunID != "" {
			t.Fatal("审批应绑定真实操作标识")
		}
		return approval
	case <-time.After(3 * time.Second):
		t.Fatal("未收到人工审批")
		return nil
	}
}

func waitOperationAskedAudit(t *testing.T, env *testEnv) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		env.store.mu.Lock()
		found := false
		for _, entry := range env.store.audits {
			if entry.Action == ActionToolCheck && entry.Outcome == OutcomeAsked {
				found = true
				break
			}
		}
		env.store.mu.Unlock()
		if found {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("审批等待必须先写入询问审计")
}

func Test人工变更即使命中allow仍须once审批且不产生会话授权(t *testing.T) {
	env := newEnv(t, nil)
	if err := env.svc.ReplaceRules(context.Background(), []Rule{{ID: "allow-files", Tool: "*", Effect: EffectAllow}}, "user-1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)
	defer sub.Cancel()
	result := env.checkAsync(ctx, manualRequest(env))
	approval := waitOperationApproval(t, sub)
	for _, scope := range []model.ApprovalScope{model.ScopeSession, model.ScopeAlways} {
		if _, err := env.svc.Decide(ctx, approval.ID, model.Decision{Approve: true, Scope: scope}, "user-1"); !model.IsCode(err, model.CodeInvalid) {
			t.Fatalf("人工变更必须拒绝扩大批准范围: %s %v", scope, err)
		}
	}
	select {
	case <-result:
		t.Fatal("拒绝长期范围后仍应等待一次批准")
	default:
	}
	if _, err := env.svc.Decide(ctx, approval.ID, model.Decision{Approve: true, Scope: model.ScopeOnce}, "user-1"); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil || !got.v.Allowed {
		t.Fatalf("一次审批后才能允许: %+v", got)
	}
	if len(env.svc.SessionGrants("sess-1")) != 0 || len(env.svc.Rules()) != 1 {
		t.Fatal("人工审批不得留下会话或永久授权")
	}
}

func Test人工审批仍先执行保护路径和deny规则(t *testing.T) {
	env := newEnv(t, nil)
	req := manualRequest(env)
	if err := env.svc.ReplaceRules(context.Background(), []Rule{{ID: "deny-file", Tool: model.OperationFileChangeTool, Effect: EffectDeny}, {ID: "allow-all", Tool: "*", Effect: EffectAllow}}, "user-1"); err != nil {
		t.Fatal(err)
	}
	v, err := env.svc.Check(context.Background(), req)
	if err != nil || v.Allowed || v.Source != SourceRule {
		t.Fatalf("显式deny必须先于人工审批: %+v %v", v, err)
	}
	if err = env.svc.ReplaceRules(context.Background(), []Rule{{ID: "allow-all", Tool: "*", Effect: EffectAllow}}, "user-1"); err != nil {
		t.Fatal(err)
	}
	req.Details, _ = json.Marshal(map[string]any{"path": filepath.Join(env.dataDir, "gogosling.db")})
	v, err = env.svc.Check(context.Background(), req)
	if err != nil || v.Allowed || v.Source != SourceGuard {
		t.Fatalf("保护目录不能被用户操作绕过: %+v %v", v, err)
	}
	items, _ := env.svc.ListPending(context.Background())
	if len(items) != 0 {
		t.Fatal("硬拒绝不能继续创建可批准请求")
	}
}

func Test人工审批取消到期及重启孤儿不能继续获准(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	sub := env.subscribe(ctx, 16)
	result := env.checkAsync(ctx, manualRequest(env))
	approval := waitOperationApproval(t, sub)
	cancel()
	got := <-result
	if !model.IsCode(got.err, model.CodeCancelled) || got.v.Allowed {
		t.Fatalf("取消必须阻止后续执行: %+v", got)
	}
	sub.Cancel()
	stored, err := env.svc.Get(context.Background(), approval.ID)
	if err != nil || stored.Status != model.ApprovalExpired {
		t.Fatalf("取消审批须落盘过期: %+v %v", stored, err)
	}
	orphan := *stored
	orphan.ID = "orphan-operation-approval"
	orphan.Status = model.ApprovalPending
	orphan.Scope = ""
	orphan.ResolvedAt = nil
	orphan.ExpiresAt = env.clock.Now().Add(time.Hour)
	if err = env.store.CreateApproval(context.Background(), &orphan); err != nil {
		t.Fatal(err)
	}
	if _, err = env.svc.Decide(context.Background(), orphan.ID, model.Decision{Approve: true, Scope: model.ScopeOnce}, "user-1"); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("无等待者的人工审批必须拒绝: %v", err)
	}
	stored, _ = env.svc.Get(context.Background(), orphan.ID)
	if stored.Status != model.ApprovalExpired {
		t.Fatal("孤儿人工审批应立即过期")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	sub = env.bus.Subscribe(ctx, env.bus.LastSeq(), 16, nil)
	defer sub.Cancel()
	result = env.checkAsync(ctx, manualRequest(env))
	_ = waitOperationApproval(t, sub)
	env.clock.Advance(11 * time.Minute)
	if _, err = env.svc.ExpireDue(ctx, env.clock.Now()); err != nil {
		t.Fatal(err)
	}
	got = <-result
	if !model.IsCode(got.err, model.CodeTimeout) || got.v.Allowed {
		t.Fatalf("审批过期须返回可判定的timeout: %+v", got)
	}
}

type failingOperationApprovals struct {
	*memStore
	fail bool
}

func (s *failingOperationApprovals) UpdateApproval(ctx context.Context, a *model.ApprovalRequest) error {
	if s.fail {
		return errors.New("simulated storage failure")
	}
	return s.memStore.UpdateApproval(ctx, a)
}

func Test人工审批持久化或审计失败时不得唤醒为允许(t *testing.T) {
	for _, failure := range []string{"approval", "audit"} {
		t.Run(failure, func(t *testing.T) {
			env := newEnv(t, nil)
			repo := &failingOperationApprovals{memStore: env.store}
			env.svc.deps.Approvals = repo
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sub := env.subscribe(ctx, 16)
			defer sub.Cancel()
			result := env.checkAsync(ctx, manualRequest(env))
			approval := waitOperationApproval(t, sub)
			waitOperationAskedAudit(t, env)
			if failure == "approval" {
				repo.fail = true
			} else {
				env.store.mu.Lock()
				env.store.failAudit = errors.New("simulated audit failure")
				env.store.mu.Unlock()
			}
			_, err := env.svc.Decide(ctx, approval.ID, model.Decision{Approve: true, Scope: model.ScopeOnce}, "user-1")
			if !model.IsCode(err, model.CodeInternal) {
				t.Fatalf("存储故障应返回内部错误: %v", err)
			}
			got := <-result
			if got.v.Allowed || !model.IsCode(got.err, model.CodeInternal) {
				t.Fatalf("未持久化批准不能执行文件操作: %+v", got)
			}
		})
	}
}
