package permission

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func ruleFromApproval(rules []Rule, approvalID string) *Rule {
	for i := range rules {
		if rules[i].Origin == "approval:"+approvalID {
			return &rules[i]
		}
	}
	return nil
}

// CONC-2：用户点"永久允许"的同时审批到期。无论谁先完成，结果都必须一致：
// Decide 成功 ⇔ 规则已写入且审批为 approved；Decide 冲突 ⇔ 规则未写入且审批为 expired。
func TestAlwaysDecisionRacingExpiryIsConsistent(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)

	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "git push --force"})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)

	// 卡住规则写入，让 Decide 停在"已取到等待者、正要写规则"的位置，同时触发过期扫描。
	env.svc.ruleMu.Lock()
	type decideOut struct {
		ar  *model.ApprovalRequest
		err error
	}
	decided := make(chan decideOut, 1)
	go func() {
		a, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true, Scope: model.ScopeAlways}, "user-1")
		decided <- decideOut{a, err}
	}()
	time.Sleep(50 * time.Millisecond)
	env.clock.Advance(DefaultApprovalTTL + time.Second)
	expired := make(chan struct{})
	go func() {
		_, _ = env.svc.ExpireDue(ctx, env.clock.Now())
		close(expired)
	}()
	time.Sleep(50 * time.Millisecond)
	env.svc.ruleMu.Unlock()

	var out decideOut
	select {
	case out = <-decided:
	case <-time.After(5 * time.Second):
		t.Fatal("Decide 未返回")
	}
	<-expired
	check := waitResult(t, res)
	stored, err := env.store.GetApproval(ctx, ar.ID)
	if err != nil {
		t.Fatal(err)
	}
	rule := ruleFromApproval(env.svc.Rules(), ar.ID)

	switch {
	case out.err == nil:
		if rule == nil || stored.Status != model.ApprovalApproved || stored.Scope != model.ScopeAlways || !check.v.Allowed {
			t.Fatalf("Decide 成功时应写入规则并批准: rule=%v status=%s scope=%s allowed=%v", rule, stored.Status, stored.Scope, check.v.Allowed)
		}
	case model.IsCode(out.err, model.CodeConflict):
		if rule != nil {
			t.Fatalf("Decide 冲突时不应留下永久规则: %+v", rule)
		}
		if stored.Status != model.ApprovalExpired || check.v.Allowed {
			t.Fatalf("Decide 冲突时审批应已过期: status=%s allowed=%v", stored.Status, check.v.Allowed)
		}
	default:
		t.Fatalf("意外错误: %v", out.err)
	}
	// 与之对应：重启后的新进程看到的规则文件与 Decide 的结果一致。
	rf, err := loadRuleFile(env.svc.RulesPath())
	if err != nil {
		t.Fatal(err)
	}
	if (ruleFromApproval(rf.Rules, ar.ID) != nil) != (out.err == nil) {
		t.Fatalf("规则文件与 Decide 结果不一致: decide err=%v", out.err)
	}
}

// CONC-2：规则写入失败时决定降级为 once 生效，并返回错误；不写 rules.update 审计。
func TestAlwaysRuleWriteFailureDowngradesToOnce(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)
	before := len(env.svc.Rules())

	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "make deploy"})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)
	// 规则文件在运行期间被改坏：追加规则时读取失败。
	if err := os.WriteFile(env.svc.RulesPath(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	final, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true, Scope: model.ScopeAlways}, "user-1")
	if !model.IsCode(err, model.CodeInternal) {
		t.Fatalf("规则写入失败应返回 internal，得到 %v", err)
	}
	if final == nil || final.Status != model.ApprovalApproved || final.Scope != model.ScopeOnce {
		t.Fatalf("决定应降级为 once 生效: %+v", final)
	}
	if out := waitResult(t, res); !out.v.Allowed || out.v.Approval.Scope != model.ScopeOnce {
		t.Fatalf("等待中的调用应按 once 得到批准: %+v", out.v)
	}
	resolved := waitEvent(t, sub, model.EventApprovalResolved)
	if resolved.Scope != model.ScopeOnce {
		t.Errorf("approval.resolved 事件应携带降级后的范围，得到 %s", resolved.Scope)
	}
	stored, _ := env.store.GetApproval(ctx, ar.ID)
	if stored.Scope != model.ScopeOnce {
		t.Errorf("持久化的范围应为 once，得到 %s", stored.Scope)
	}
	if len(env.svc.Rules()) != before {
		t.Error("规则写入失败时不应改变生效规则")
	}
	if n := len(env.store.auditsOf(ActionRulesUpdate)); n != 0 {
		t.Errorf("规则没有写入时不应有 rules.update 审计，得到 %d 条", n)
	}
	decides := env.store.auditsOf(ActionApprovalDecide)
	if len(decides) != 1 || detailsOf(t, decides[0].Details)["scope"] != string(model.ScopeOnce) {
		t.Errorf("approval.decide 审计应记录降级后的范围: %+v", decides)
	}
}

// CONC-2：ScopeAlways 写入的规则与 ReplaceRules 一样留下 permission.rules.update 审计。
func TestAlwaysRuleWritesRulesUpdateAudit(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)

	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "go test ./..."})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)
	if _, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true, Scope: model.ScopeAlways}, "user-1"); err != nil {
		t.Fatal(err)
	}
	waitResult(t, res)
	audits := env.store.auditsOf(ActionRulesUpdate)
	if len(audits) != 1 {
		t.Fatalf("应有一条 rules.update 审计，得到 %d", len(audits))
	}
	a := audits[0]
	d := detailsOf(t, a.Details)
	rule := ruleFromApproval(env.svc.Rules(), ar.ID)
	ids, _ := d["added_rules"].([]any)
	if a.Actor != "user-1" || a.Resource != env.svc.RulesPath() || d["approval_id"] != ar.ID || d["effect"] != string(EffectAllow) ||
		rule == nil || len(ids) != 1 || ids[0] != rule.ID {
		t.Fatalf("rules.update 审计内容不正确: %+v %v", a, d)
	}
}

// DOC-01：Isolated 进入审计，便于事后区分"沙箱内放行"。
func TestIsolatedIsAudited(t *testing.T) {
	env := newEnv(t, nil)
	r := env.req("run_command", model.RiskExecute, model.AutonomyAutoInSandbox, map[string]any{"command": "ls"})
	r.Isolated = true
	v, err := env.svc.Check(context.Background(), r)
	if err != nil || !v.Allowed || v.Source != SourceAutonomy {
		t.Fatalf("隔离中的执行在 auto_in_sandbox 下应放行: %+v %v", v, err)
	}
	checks := env.store.auditsOf(ActionToolCheck)
	if len(checks) != 1 || detailsOf(t, checks[0].Details)["isolated"] != true {
		t.Fatalf("审计应记录 isolated: %+v", checks)
	}
}
