package permission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestNewSeedsRulesFileAndValidatesDeps(t *testing.T) {
	env := newEnv(t, nil)
	p := env.svc.RulesPath()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("规则文件应已落盘: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("规则文件权限 = %o，期望 0600", info.Mode().Perm())
	}
	defaults, _ := DefaultRules()
	if len(env.svc.Rules()) != len(defaults) {
		t.Errorf("加载的规则数 %d 与内置 %d 不一致", len(env.svc.Rules()), len(defaults))
	}

	if _, err := New(Deps{}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少依赖应返回 invalid: %v", err)
	}
	if _, err := New(Deps{Approvals: env.store, Audit: env.store, Bus: env.bus}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少 RulesPath 与 DataDir 应返回 invalid: %v", err)
	}
}

func TestNewRejectsInvalidRulesFile(t *testing.T) {
	dir := t.TempDir()
	p := DefaultRulesPath(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"version":1,"rules":[{"tool":"*","effect":"nope"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	_, err := New(Deps{Approvals: store, Audit: store, Bus: newEnv(t, nil).bus, RulesPath: p})
	if !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("无效规则文件应让 New 失败并返回 invalid: %v", err)
	}
}

func TestCheckRejectsInvalidRequests(t *testing.T) {
	env := newEnv(t, nil)
	ctx := context.Background()
	r := env.req("", model.RiskRead, model.AutonomyAskRisky, nil)
	r.Call.Name = ""
	if _, err := env.svc.Check(ctx, r); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少工具名应返回 invalid: %v", err)
	}
	r = env.req("read_file", "", model.AutonomyAskRisky, nil)
	if _, err := env.svc.Check(ctx, r); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("风险等级为空应返回 invalid: %v", err)
	}
	r = env.req("read_file", model.RiskRead, model.AutonomyAskRisky, nil)
	r.Call.Name = "other"
	if _, err := env.svc.Check(ctx, r); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("工具名不一致应返回 invalid: %v", err)
	}
}

func TestGuardDeniesDangerousCommandWithoutApproval(t *testing.T) {
	env := newEnv(t, nil)
	r := env.req("run_command", model.RiskExecute, model.AutonomyAutoInSandbox, map[string]any{"command": "rm -rf /"})
	v, err := env.svc.Check(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if v.Allowed || v.Source != SourceGuard || v.RuleID != "rm-critical-path" || v.Reason == "" {
		t.Fatalf("危险命令应被内置防护拒绝: %+v", v)
	}
	if pending, _ := env.svc.ListPending(context.Background()); len(pending) != 0 {
		t.Error("硬拒绝不应创建审批请求")
	}
	audits := env.store.auditsOf(ActionToolCheck)
	if len(audits) != 1 || audits[0].Outcome != OutcomeDenied || audits[0].Resource != "run_command" {
		t.Fatalf("应写一条 denied 的 tool.check 审计: %+v", audits)
	}
	d := detailsOf(t, audits[0].Details)
	if d["rule_id"] != "rm-critical-path" || d["source"] != string(SourceGuard) {
		t.Errorf("审计 Details 应记录命中的防护: %v", d)
	}

	// 用户的 allow 规则不能绕过内置防护。
	rules := append(env.svc.Rules(), Rule{ID: "allow-all", Tool: "*", Effect: EffectAllow, Origin: "user"})
	if err := env.svc.ReplaceRules(context.Background(), rules, "user-1"); err != nil {
		t.Fatal(err)
	}
	v, _ = env.svc.Check(context.Background(), r)
	if v.Allowed {
		t.Error("allow 规则不应绕过内置防护")
	}
}

func TestEvaluateRulePriority(t *testing.T) {
	env := newEnv(t, nil)
	rules := []Rule{
		{ID: "allow-danger", Tool: "danger", Effect: EffectAllow},
		{ID: "deny-danger", Tool: "danger", Effect: EffectDeny},
		{ID: "allow-net", Tool: "fetch", Effect: EffectAllow},
		{ID: "ask-read", Tool: "read_file", Effect: EffectAsk},
	}
	if err := env.svc.ReplaceRules(context.Background(), rules, "user-1"); err != nil {
		t.Fatal(err)
	}
	eval := func(tool string, risk model.ToolRisk, level model.AutonomyLevel) decision {
		r := env.req(tool, risk, level, nil)
		return env.svc.evaluate(r, extractFacts(nil, nil, env.workspace, ""))
	}
	if d := eval("danger", model.RiskRead, model.AutonomyAutoInSandbox); d.effect != EffectDeny || d.ruleID != "deny-danger" {
		t.Errorf("deny 应优先于 allow: %+v", d)
	}
	if d := eval("fetch", model.RiskNetwork, model.AutonomyAskAll); d.effect != EffectAllow || d.source != SourceRule {
		t.Errorf("allow 规则应优先于档位默认: %+v", d)
	}
	if d := eval("read_file", model.RiskRead, model.AutonomyAutoInSandbox); d.effect != EffectAsk || d.ruleID != "ask-read" {
		t.Errorf("ask 规则应优先于档位默认放行: %+v", d)
	}
	if d := eval("write_file", model.RiskWrite, model.AutonomyAskRisky); d.effect != EffectAllow || d.source != SourceAutonomy {
		t.Errorf("无规则命中时应用档位默认: %+v", d)
	}
}

func TestAutonomyDefaults(t *testing.T) {
	env := newEnv(t, nil)
	if err := env.svc.ReplaceRules(context.Background(), nil, "user-1"); err != nil {
		t.Fatal(err)
	}
	inside := map[string]any{"path": "notes.md"}
	outside := map[string]any{"path": filepath.Join(env.outside, "x")}
	cases := []struct {
		name     string
		level    model.AutonomyLevel
		risk     model.ToolRisk
		details  map[string]any
		want     Effect
		isolated bool
	}{
		{"ask_all 读取也询问", model.AutonomyAskAll, model.RiskRead, inside, EffectAsk, false},
		{"ask_risky 工作区内读取放行", model.AutonomyAskRisky, model.RiskRead, inside, EffectAllow, false},
		{"ask_risky 工作区内写入放行", model.AutonomyAskRisky, model.RiskWrite, inside, EffectAllow, false},
		{"ask_risky 无路径读取放行", model.AutonomyAskRisky, model.RiskRead, nil, EffectAllow, false},
		{"ask_risky 执行询问", model.AutonomyAskRisky, model.RiskExecute, nil, EffectAsk, false},
		{"ask_risky 网络询问", model.AutonomyAskRisky, model.RiskNetwork, nil, EffectAsk, false},
		{"ask_risky 破坏性询问", model.AutonomyAskRisky, model.RiskDestructive, inside, EffectAsk, false},
		{"ask_risky 越界读取询问", model.AutonomyAskRisky, model.RiskRead, outside, EffectAsk, false},
		{"auto 隔离中的工作区内执行放行", model.AutonomyAutoInSandbox, model.RiskExecute, inside, EffectAllow, true},
		{"auto 隔离中的网络放行", model.AutonomyAutoInSandbox, model.RiskNetwork, nil, EffectAllow, true},
		{"auto 无隔离的执行询问（Linux/Windows 的 Direct）", model.AutonomyAutoInSandbox, model.RiskExecute, inside, EffectAsk, false},
		{"auto 无隔离的无路径执行询问", model.AutonomyAutoInSandbox, model.RiskExecute, nil, EffectAsk, false},
		{"auto 无隔离的网络询问", model.AutonomyAutoInSandbox, model.RiskNetwork, nil, EffectAsk, false},
		{"auto 无隔离的工作区内读取仍放行", model.AutonomyAutoInSandbox, model.RiskRead, inside, EffectAllow, false},
		{"auto 无隔离的工作区内写入仍放行", model.AutonomyAutoInSandbox, model.RiskWrite, inside, EffectAllow, false},
		{"auto 工作区内破坏性放行", model.AutonomyAutoInSandbox, model.RiskDestructive, inside, EffectAllow, false},
		{"auto 无证据破坏性询问", model.AutonomyAutoInSandbox, model.RiskDestructive, nil, EffectAsk, false},
		{"auto 越界写入询问", model.AutonomyAutoInSandbox, model.RiskWrite, outside, EffectAsk, false},
		{"auto 隔离中的越界执行仍询问", model.AutonomyAutoInSandbox, model.RiskExecute, outside, EffectAsk, true},
		{"ask_risky 隔离中的执行仍询问", model.AutonomyAskRisky, model.RiskExecute, inside, EffectAsk, true},
	}
	for _, c := range cases {
		r := env.req("tool", c.risk, c.level, c.details)
		r.Isolated = c.isolated
		d := env.svc.evaluate(r, extractFacts(r.Details, nil, env.workspace, ""))
		if d.effect != c.want {
			t.Errorf("%s: 得到 %s，期望 %s（%s）", c.name, d.effect, c.want, d.reason)
		}
	}
	// 未知档位按 ask_all 处理。
	r := env.req("tool", model.RiskRead, "weird", inside)
	v, err := env.svc.Check(contextWithCancelNow(), r)
	if err == nil || v.Allowed {
		t.Errorf("未知档位应回退为询问: v=%+v err=%v", v, err)
	}
}

func contextWithCancelNow() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestUntrustedInputFloor(t *testing.T) {
	env := newEnv(t, nil)
	rules := []Rule{{ID: "allow-send", Tool: "send_mail", Effect: EffectAllow}}
	if err := env.svc.ReplaceRules(context.Background(), rules, "user-1"); err != nil {
		t.Fatal(err)
	}
	eval := func(tool string, risk model.ToolRisk, level model.AutonomyLevel, untrusted bool) decision {
		r := env.req(tool, risk, level, map[string]any{"path": "notes.md"})
		r.UntrustedInput = untrusted
		// 隔离中：auto_in_sandbox 会放行 execute / network，这里验证的是不可信输入的下限。
		r.Isolated = true
		return env.svc.evaluate(r, extractFacts(r.Details, nil, env.workspace, ""))
	}
	if d := eval("fetch", model.RiskNetwork, model.AutonomyAutoInSandbox, true); d.effect != EffectAsk || d.source != SourceUntrustedInput {
		t.Errorf("不可信输入 + 网络应询问: %+v", d)
	}
	if d := eval("run_command", model.RiskExecute, model.AutonomyAutoInSandbox, true); d.effect != EffectAsk || d.source != SourceUntrustedInput {
		t.Errorf("不可信输入 + 执行应询问（即使在沙箱内）: %+v", d)
	}
	if d := eval("run_command", model.RiskExecute, model.AutonomyAutoInSandbox, false); d.effect != EffectAllow {
		t.Errorf("没有不可信输入时沙箱内执行应放行: %+v", d)
	}
	if d := eval("send_mail", model.RiskDestructive, model.AutonomyAutoInSandbox, true); d.effect != EffectAsk || d.source != SourceUntrustedInput {
		t.Errorf("显式 allow 也不能绕过不可信输入的下限: %+v", d)
	}
	if d := eval("send_mail", model.RiskDestructive, model.AutonomyAutoInSandbox, false); d.effect != EffectAllow {
		t.Errorf("没有不可信输入时 allow 规则应生效: %+v", d)
	}
	if d := eval("read_file", model.RiskRead, model.AutonomyAskRisky, true); d.effect != EffectAllow {
		t.Errorf("不可信输入不影响工作区内读取: %+v", d)
	}
	if d := eval("rm", model.RiskDestructive, model.AutonomyAskRisky, true); d.effect != EffectAsk {
		t.Errorf("不可信输入 + 破坏性应询问: %+v", d)
	}
}

func TestCheckAskApproveOnce(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)

	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "go test ./..."})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)
	if ar.Status != model.ApprovalPending || ar.ToolCall.Name != "run_command" || ar.Risk != model.RiskExecute {
		t.Fatalf("approval.requested 载荷不正确: %+v", ar)
	}
	if ar.ExpiresAt.Sub(ar.CreatedAt) != DefaultApprovalTTL {
		t.Errorf("默认有效期应为 10 分钟: %v", ar.ExpiresAt.Sub(ar.CreatedAt))
	}
	if !strings.Contains(ar.Summary, "go test") {
		t.Errorf("没有给 Summary 时应由工具名与命令拼出: %q", ar.Summary)
	}
	pending, err := env.svc.ListPending(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != ar.ID {
		t.Fatalf("ListPending 应返回等待中的请求: %v %v", pending, err)
	}

	got, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true}, "user-1")
	if err != nil {
		t.Fatalf("Decide 失败: %v", err)
	}
	if got.Status != model.ApprovalApproved || got.DecidedBy != "user-1" || got.Scope != model.ScopeOnce || got.ResolvedAt == nil {
		t.Fatalf("Decide 返回的状态不正确: %+v", got)
	}
	resolved := waitEvent(t, sub, model.EventApprovalResolved)
	if resolved.ID != ar.ID || resolved.Status != model.ApprovalApproved {
		t.Errorf("approval.resolved 载荷不正确: %+v", resolved)
	}

	out := waitResult(t, res)
	if out.err != nil || !out.v.Allowed || out.v.Source != SourceApproval || out.v.Approval == nil || out.v.Approval.Status != model.ApprovalApproved {
		t.Fatalf("批准后 Check 应放行: %+v %v", out.v, out.err)
	}
	stored, _ := env.store.GetApproval(ctx, ar.ID)
	if stored.Status != model.ApprovalApproved {
		t.Errorf("仓储中的状态应为 approved: %s", stored.Status)
	}
	if pending, _ := env.svc.ListPending(ctx); len(pending) != 0 {
		t.Error("决定后不应再有待处理请求")
	}

	checks := env.store.auditsOf(ActionToolCheck)
	decides := env.store.auditsOf(ActionApprovalDecide)
	if len(checks) != 1 || checks[0].Outcome != OutcomeAsked || detailsOf(t, checks[0].Details)["approval_id"] != ar.ID {
		t.Errorf("应写一条 asked 的 tool.check 审计: %+v", checks)
	}
	if len(decides) != 1 || decides[0].Outcome != OutcomeAllowed || decides[0].Actor != "user-1" || decides[0].Resource != ar.ID {
		t.Errorf("应写一条 allowed 的 approval.decide 审计: %+v", decides)
	}

	// ScopeOnce 只对这一次有效：再次调用仍然询问。
	res2 := env.checkAsync(ctx, r)
	ar2 := waitEvent(t, sub, model.EventApprovalRequested)
	if ar2.ID == ar.ID {
		t.Fatal("第二次调用应创建新的审批请求")
	}
	if _, err := env.svc.Decide(ctx, ar2.ID, model.Decision{Approve: false, Reason: "not now"}, "user-1"); err != nil {
		t.Fatal(err)
	}
	out2 := waitResult(t, res2)
	if out2.err != nil || out2.v.Allowed || !strings.Contains(out2.v.Reason, "not now") {
		t.Fatalf("拒绝后应返回带理由的拒绝: %+v %v", out2.v, out2.err)
	}
}

func TestSessionScopeGrant(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)

	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "make build"})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)
	if _, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true, Scope: model.ScopeSession}, "user-1"); err != nil {
		t.Fatal(err)
	}
	if out := waitResult(t, res); !out.v.Allowed {
		t.Fatalf("批准后应放行: %+v", out.v)
	}
	grants := env.svc.SessionGrants("sess-1")
	if len(grants) != 1 || grants[0].Tool != "run_command" || grants[0].ApprovalID != ar.ID {
		t.Fatalf("应记录会话授权: %+v", grants)
	}

	// 同会话同工具（不同命令）直接放行。
	r2 := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "make test"})
	v, err := env.svc.Check(ctx, r2)
	if err != nil || !v.Allowed || v.Source != SourceSession {
		t.Fatalf("会话授权应放行后续调用: %+v %v", v, err)
	}
	// 会话授权不能绕过内置防护。
	r3 := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "rm -rf /"})
	if v, _ := env.svc.Check(ctx, r3); v.Allowed {
		t.Error("会话授权不应绕过内置防护")
	}
	// 其他会话仍然询问。
	r4 := r2
	r4.SessionID = "sess-2"
	cctx, ccancel := context.WithCancel(ctx)
	res4 := env.checkAsync(cctx, r4)
	waitEvent(t, sub, model.EventApprovalRequested)
	ccancel()
	if out := waitResult(t, res4); out.v.Allowed {
		t.Error("其他会话不应继承授权")
	}
	// 撤销后再次询问。
	env.svc.RevokeSession("sess-1")
	if len(env.svc.SessionGrants("sess-1")) != 0 {
		t.Error("撤销后授权应清空")
	}
	cctx2, ccancel2 := context.WithCancel(ctx)
	res5 := env.checkAsync(cctx2, r2)
	waitEvent(t, sub, model.EventApprovalRequested)
	ccancel2()
	if out := waitResult(t, res5); out.v.Allowed {
		t.Error("撤销后不应再放行")
	}
}

func TestSessionGrantScopedToOutsideDirs(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)

	dirA := filepath.Join(env.outside, "a")
	dirB := filepath.Join(env.outside, "b")
	r := env.req("read_file", model.RiskRead, model.AutonomyAskRisky, map[string]any{"path": filepath.Join(dirA, "x.txt")})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)
	d := detailsOf(t, ar.Details)
	if d["out_of_workspace"] != true {
		t.Errorf("越界路径应在 Details 中标记: %v", d)
	}
	if _, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true, Scope: model.ScopeSession}, "user-1"); err != nil {
		t.Fatal(err)
	}
	if out := waitResult(t, res); !out.v.Allowed {
		t.Fatal("批准后应放行")
	}

	same := env.req("read_file", model.RiskRead, model.AutonomyAskRisky, map[string]any{"path": filepath.Join(dirA, "y.txt")})
	if v, _ := env.svc.Check(ctx, same); !v.Allowed || v.Source != SourceSession {
		t.Errorf("同一外部目录应被会话授权覆盖: %+v", v)
	}
	inside := env.req("read_file", model.RiskRead, model.AutonomyAskRisky, map[string]any{"path": "notes.md"})
	if v, _ := env.svc.Check(ctx, inside); !v.Allowed {
		t.Errorf("工作区内读取应放行: %+v", v)
	}
	other := env.req("read_file", model.RiskRead, model.AutonomyAskRisky, map[string]any{"path": filepath.Join(dirB, "z.txt")})
	cctx, ccancel := context.WithCancel(ctx)
	res2 := env.checkAsync(cctx, other)
	waitEvent(t, sub, model.EventApprovalRequested)
	ccancel()
	if out := waitResult(t, res2); out.v.Allowed {
		t.Error("其他外部目录不应被会话授权覆盖")
	}
}

func TestAlwaysScopeWritesRuleFile(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)

	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "git status"})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)
	if _, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true, Scope: model.ScopeAlways}, "user-1"); err != nil {
		t.Fatal(err)
	}
	if out := waitResult(t, res); !out.v.Allowed {
		t.Fatal("批准后应放行")
	}

	rf, err := loadRuleFile(env.svc.RulesPath())
	if err != nil {
		t.Fatal(err)
	}
	var written *Rule
	for i := range rf.Rules {
		if rf.Rules[i].Origin == "approval:"+ar.ID {
			written = &rf.Rules[i]
		}
	}
	if written == nil {
		t.Fatal("ScopeAlways 应把规则写入文件")
	}
	if written.Tool != "run_command" || written.Risk != model.RiskExecute || written.Effect != EffectAllow || written.Command != `^git status$` {
		t.Errorf("生成的规则应精确到工具、风险与命令: %+v", written)
	}

	// 新进程加载同一文件后，同一命令直接放行；不同命令仍然询问。
	store2 := newMemStore()
	svc2, err := New(Deps{Approvals: store2, Audit: store2, Bus: env.bus, RulesPath: env.svc.RulesPath(), DataDir: env.dataDir, Now: env.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	v, err := svc2.Check(ctx, r)
	if err != nil || !v.Allowed || v.Source != SourceRule || v.RuleID != written.ID {
		t.Fatalf("写入的规则应在重启后生效: %+v %v", v, err)
	}
	other := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "git push"})
	if d := svc2.evaluate(other, extractFacts(other.Details, nil, env.workspace, "")); d.effect != EffectAsk {
		t.Errorf("不同命令不应被精确规则放行: %+v", d)
	}

	// 永久拒绝写 deny 规则。
	w := env.req("write_file", model.RiskWrite, model.AutonomyAskAll, map[string]any{"path": "secret.txt"})
	res2 := env.checkAsync(ctx, w)
	ar2 := waitEvent(t, sub, model.EventApprovalRequested)
	if _, err := env.svc.Decide(ctx, ar2.ID, model.Decision{Approve: false, Scope: model.ScopeAlways}, "user-1"); err != nil {
		t.Fatal(err)
	}
	if out := waitResult(t, res2); out.v.Allowed {
		t.Fatal("拒绝后不应放行")
	}
	v, err = env.svc.Check(ctx, w)
	if err != nil || v.Allowed || v.Source != SourceRule {
		t.Fatalf("永久拒绝应直接命中 deny 规则: %+v %v", v, err)
	}
	var denyRule *Rule
	for _, rr := range env.svc.Rules() {
		if rr.Origin == "approval:"+ar2.ID {
			rr := rr
			denyRule = &rr
		}
	}
	// 规则里的路径是规范化后的形式（Windows 上为长名）；工作区也按同样方式规范化后再比较，
	// 不区分大小写的平台上忽略大小写差异。
	wsPrefix := filepath.ToSlash(canonicalPath(env.workspace))
	hasPrefix := func(p string) bool {
		if caseInsensitivePaths {
			return strings.HasPrefix(strings.ToLower(p), strings.ToLower(wsPrefix))
		}
		return strings.HasPrefix(p, wsPrefix)
	}
	if denyRule == nil || denyRule.Effect != EffectDeny || !strings.HasSuffix(denyRule.Path, "/**") || !hasPrefix(denyRule.Path) {
		t.Errorf("工作区内的永久规则应限定在工作区目录下: %+v", denyRule)
	}
}

func TestDecideIdempotentAndConflict(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)

	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "ls"})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)

	if _, err := env.svc.Decide(ctx, ar.ID, model.Decision{Scope: "weird"}, "user-1"); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("非法范围应返回 invalid: %v", err)
	}
	if _, err := env.svc.Decide(ctx, "", model.Decision{}, "user-1"); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("空 ID 应返回 invalid: %v", err)
	}
	if _, err := env.svc.Decide(ctx, "01NOPE", model.Decision{Approve: true}, "user-1"); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("未知 ID 应返回 not_found: %v", err)
	}

	first, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true}, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	waitResult(t, res)
	again, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: true}, "user-2")
	if err != nil || again.Status != first.Status || again.DecidedBy != "user-1" {
		t.Errorf("相同结果的重复提交应幂等返回原状态: %+v %v", again, err)
	}
	if _, err := env.svc.Decide(ctx, ar.ID, model.Decision{Approve: false}, "user-1"); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("相反结果应返回 conflict: %v", err)
	}
	if n := len(env.store.auditsOf(ActionApprovalDecide)); n != 1 {
		t.Errorf("幂等重放不应重复写审计，得到 %d 条", n)
	}
}

func TestContextCancelExpiresApproval(t *testing.T) {
	env := newEnv(t, nil)
	bg, stop := context.WithCancel(context.Background())
	defer stop()
	sub := env.subscribe(bg, 16)

	ctx, cancel := context.WithCancel(bg)
	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "ls"})
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)
	cancel()

	out := waitResult(t, res)
	if !model.IsCode(out.err, model.CodeCancelled) {
		t.Fatalf("ctx 取消应返回 cancelled: %v", out.err)
	}
	if out.v.Allowed || out.v.Approval == nil || out.v.Approval.Status != model.ApprovalExpired || out.v.Reason == "" {
		t.Fatalf("取消后 Verdict 应为拒绝且状态 expired: %+v", out.v)
	}
	resolved := waitEvent(t, sub, model.EventApprovalResolved)
	if resolved.Status != model.ApprovalExpired {
		t.Errorf("应发布 expired 的 approval.resolved: %+v", resolved)
	}
	stored, _ := env.store.GetApproval(bg, ar.ID)
	if stored.Status != model.ApprovalExpired || stored.DecidedBy != "system" {
		t.Errorf("仓储状态应为 expired: %+v", stored)
	}
	if _, err := env.svc.Decide(bg, ar.ID, model.Decision{Approve: true}, "user-1"); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("过期后决定应返回 conflict: %v", err)
	}
	decides := env.store.auditsOf(ActionApprovalDecide)
	if len(decides) != 1 || decides[0].Outcome != OutcomeDenied || detailsOf(t, decides[0].Details)["reason"] != "cancelled" {
		t.Errorf("取消应写一条 denied/cancelled 的审计: %+v", decides)
	}

	// 截止时间超时返回 timeout。
	tctx, tcancel := context.WithTimeout(bg, 50*time.Millisecond)
	defer tcancel()
	out2 := waitResult(t, env.checkAsync(tctx, r))
	if !model.IsCode(out2.err, model.CodeTimeout) {
		t.Errorf("ctx 超时应返回 timeout: %v", out2.err)
	}
}

func TestTimerExpiryWithoutLoop(t *testing.T) {
	env := newEnv(t, func(d *Deps) {
		d.Now = time.Now
		d.ApprovalTTL = 40 * time.Millisecond
	})
	ctx := context.Background()
	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "ls"})
	start := time.Now()
	v, err := env.svc.Check(ctx, r)
	if err != nil {
		t.Fatalf("自然过期不应返回错误: %v", err)
	}
	if v.Allowed || v.Approval == nil || v.Approval.Status != model.ApprovalExpired || !strings.Contains(v.Reason, "expired") {
		t.Fatalf("到期后应返回 expired 的拒绝: %+v", v)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("等待者应由自身定时器唤醒，而不是依赖扫描循环")
	}
}

func TestExpireDueWakesMultipleWaiters(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 64)

	const n = 5
	results := make([]<-chan checkResult, 0, n)
	for i := 0; i < n; i++ {
		r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "ls"})
		r.RunID = "run-" + string(rune('a'+i))
		results = append(results, env.checkAsync(ctx, r))
		waitEvent(t, sub, model.EventApprovalRequested)
	}
	if n0, err := env.svc.ExpireDue(ctx, env.clock.Now()); err != nil || n0 != 0 {
		t.Fatalf("未到期时不应过期任何请求: %d %v", n0, err)
	}
	env.clock.Advance(DefaultApprovalTTL + time.Second)
	n1, err := env.svc.ExpireDue(ctx, env.clock.Now())
	if err != nil || n1 != n {
		t.Fatalf("应过期 %d 条，得到 %d %v", n, n1, err)
	}
	for _, ch := range results {
		out := waitResult(t, ch)
		if out.err != nil || out.v.Allowed || out.v.Approval.Status != model.ApprovalExpired {
			t.Errorf("过期后等待者应得到 expired 拒绝: %+v %v", out.v, out.err)
		}
	}
	if pending, _ := env.svc.ListPending(ctx); len(pending) != 0 {
		t.Errorf("过期后不应有待处理请求: %d", len(pending))
	}
	if got := len(env.store.auditsOf(ActionApprovalDecide)); got != n {
		t.Errorf("每条过期都应写审计: %d", got)
	}
}

func TestExpireDueHandlesOrphans(t *testing.T) {
	env := newEnv(t, nil)
	ctx := context.Background()
	orphan := &model.ApprovalRequest{
		ID: model.NewID(), RunID: "old-run", SessionID: "old-sess", AgentID: "agent-1",
		ToolCall: model.ToolCall{ID: "c", Name: "run_command"}, Risk: model.RiskExecute,
		Summary: "leftover", Status: model.ApprovalPending,
		CreatedAt: env.clock.Now().Add(-time.Minute), ExpiresAt: env.clock.Now().Add(9 * time.Minute),
	}
	if err := env.store.CreateApproval(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	n, err := env.svc.ExpireDue(ctx, env.clock.Now())
	if err != nil || n != 1 {
		t.Fatalf("孤儿记录应被立即过期: %d %v", n, err)
	}
	stored, _ := env.store.GetApproval(ctx, orphan.ID)
	if stored.Status != model.ApprovalExpired {
		t.Errorf("孤儿记录状态应为 expired: %s", stored.Status)
	}
	if _, err := env.svc.Decide(ctx, orphan.ID, model.Decision{Approve: true}, "user-1"); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("已过期的孤儿记录不能再决定: %v", err)
	}
}

func TestDecideOrphanWithoutWaiterStillRecordsDecision(t *testing.T) {
	env := newEnv(t, nil)
	ctx := context.Background()
	orphan := &model.ApprovalRequest{
		ID: model.NewID(), RunID: "old-run", SessionID: "sess-1", AgentID: "agent-1",
		ToolCall: model.ToolCall{ID: "c", Name: "run_command"}, Risk: model.RiskExecute,
		Summary: "leftover", Status: model.ApprovalPending,
		CreatedAt: env.clock.Now(), ExpiresAt: env.clock.Now().Add(9 * time.Minute),
	}
	if err := env.store.CreateApproval(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	got, err := env.svc.Decide(ctx, orphan.ID, model.Decision{Approve: true, Scope: model.ScopeSession}, "user-1")
	if err != nil || got.Status != model.ApprovalApproved {
		t.Fatalf("孤儿记录的决定应被写回: %+v %v", got, err)
	}
	if g := env.svc.SessionGrants("sess-1"); len(g) != 1 {
		t.Errorf("会话授权仍应登记，供该会话后续运行使用: %+v", g)
	}
}

func TestConcurrentWaitersGetTheirOwnDecision(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 256)

	const n = 24
	results := make(map[string]<-chan checkResult, n)
	for i := 0; i < n; i++ {
		r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "ls"})
		r.RunID = "run-" + string(rune('A'+i))
		r.SessionID = "sess-" + string(rune('A'+i))
		results[r.RunID] = env.checkAsync(ctx, r)
	}
	approvals := make(map[string]*model.ApprovalRequest, n)
	for len(approvals) < n {
		ar := waitEvent(t, sub, model.EventApprovalRequested)
		approvals[ar.RunID] = ar
	}
	if pending, _ := env.svc.ListPending(ctx); len(pending) != n {
		t.Fatalf("应有 %d 条待处理请求，得到 %d", n, len(pending))
	}

	var wg sync.WaitGroup
	approve := make(map[string]bool, n)
	i := 0
	for runID, ar := range approvals {
		approve[runID] = i%2 == 0
		i++
		wg.Add(1)
		go func(id string, ok bool) {
			defer wg.Done()
			if _, err := env.svc.Decide(ctx, id, model.Decision{Approve: ok}, "user-1"); err != nil {
				t.Errorf("Decide %s 失败: %v", id, err)
			}
		}(ar.ID, approve[runID])
	}
	wg.Wait()

	for runID, ch := range results {
		out := waitResult(t, ch)
		if out.err != nil {
			t.Errorf("%s: 不应出错: %v", runID, out.err)
			continue
		}
		if out.v.Approval == nil || out.v.Approval.RunID != runID {
			t.Errorf("%s: 等待者拿到了别人的审批: %+v", runID, out.v.Approval)
			continue
		}
		if out.v.Allowed != approve[runID] {
			t.Errorf("%s: 判定 %v 与决定 %v 不一致", runID, out.v.Allowed, approve[runID])
		}
	}
	if pending, _ := env.svc.ListPending(ctx); len(pending) != 0 {
		t.Errorf("全部决定后不应有待处理请求: %d", len(pending))
	}
	if got := len(env.store.auditsOf(ActionApprovalDecide)); got != n {
		t.Errorf("每个决定都应写审计: %d", got)
	}
}

func TestDetailsAreSanitized(t *testing.T) {
	env := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := env.subscribe(ctx, 16)

	long := strings.Repeat("x", 1000)
	r := env.req("write_file", model.RiskWrite, model.AutonomyAskAll, map[string]any{
		"path":      "notes.md",
		"content":   "SECRET-BODY",
		"api_token": "tok-123",
		"nested":    map[string]any{"password": "PASS-VALUE", "keep": "v"},
		"command":   long,
	})
	r.Call.Arguments = json.RawMessage(`{"path":"notes.md","content":"SECRET-BODY"}`)
	res := env.checkAsync(ctx, r)
	ar := waitEvent(t, sub, model.EventApprovalRequested)
	raw := string(ar.Details)
	if strings.Contains(raw, "SECRET-BODY") || strings.Contains(raw, "tok-123") || strings.Contains(raw, "PASS-VALUE") {
		t.Errorf("审批 Details 不应包含内容或凭据: %s", raw)
	}
	d := detailsOf(t, ar.Details)
	if _, ok := d["redacted_keys"]; !ok {
		t.Errorf("应记录被移除的键: %v", d)
	}
	nested, _ := d["nested"].(map[string]any)
	if nested["keep"] != "v" {
		t.Errorf("无害的嵌套字段应保留: %v", d["nested"])
	}
	if _, leaked := nested["password"]; leaked {
		t.Errorf("嵌套的凭据键应被移除: %v", nested)
	}
	if cmd, _ := d["command"].(string); len(cmd) >= 1000 {
		t.Errorf("过长字符串应截断: %d", len(cmd))
	}
	cancel()
	waitResult(t, res)

	checks := env.store.auditsOf(ActionToolCheck)
	if len(checks) != 1 {
		t.Fatalf("应有一条 tool.check 审计: %d", len(checks))
	}
	audit := string(checks[0].Details)
	if strings.Contains(audit, "SECRET-BODY") || strings.Contains(audit, "tok-123") {
		t.Errorf("审计 Details 不应包含内容或凭据: %s", audit)
	}
	ad := detailsOf(t, checks[0].Details)
	if cmd, _ := ad["command"].(string); len([]rune(cmd)) > maxCommandInAudit+len("…(truncated)") {
		t.Errorf("审计中的命令应截断到 %d 字符: %d", maxCommandInAudit, len(cmd))
	}
	if ad["approval_id"] != ar.ID || ad["autonomy"] != string(model.AutonomyAskAll) {
		t.Errorf("审计应记录审批 ID 与档位: %v", ad)
	}
}

func TestKernelDataDirIsProtected(t *testing.T) {
	env := newEnv(t, nil)
	ctx := context.Background()
	r := env.req("read_file", model.RiskRead, model.AutonomyAutoInSandbox, map[string]any{"path": filepath.Join(env.dataDir, "daemon.json")})
	v, err := env.svc.Check(ctx, r)
	if err != nil || v.Allowed || v.Source != SourceGuard || v.RuleID != "kernel-data-dir" {
		t.Fatalf("读取内核数据文件应被拒绝: %+v %v", v, err)
	}
	r = env.req("run_command", model.RiskExecute, model.AutonomyAutoInSandbox, map[string]any{"command": "cat " + filepath.Join(env.dataDir, "secrets.enc")})
	v, err = env.svc.Check(ctx, r)
	if err != nil || v.Allowed || v.RuleID != "kernel-data-files" {
		t.Fatalf("命令中引用内核数据文件应被拒绝: %+v %v", v, err)
	}
	r = env.req("read_file", model.RiskRead, model.AutonomyAutoInSandbox, map[string]any{"path": "notes.md"})
	if v, err = env.svc.Check(ctx, r); err != nil || !v.Allowed {
		t.Fatalf("工作区内读取不应受影响: %+v %v", v, err)
	}
}

func TestDefaultRulesAskForEnvAndDenySSH(t *testing.T) {
	env := newEnv(t, nil)
	ctx := context.Background()
	r := env.req("read_file", model.RiskRead, model.AutonomyAutoInSandbox, map[string]any{"path": ".ssh/id_rsa"})
	if v, err := env.svc.Check(ctx, r); err != nil || v.Allowed || v.RuleID != "deny-ssh-keys" {
		t.Fatalf("内置规则应拒绝读取 .ssh: %+v %v", v, err)
	}
	r = env.req("read_file", model.RiskRead, model.AutonomyAutoInSandbox, map[string]any{"path": ".env"})
	if d := env.svc.evaluate(r, extractFacts(r.Details, nil, env.workspace, "")); d.effect != EffectAsk || d.ruleID != "ask-env-files" {
		t.Errorf("内置规则应对 .env 询问: %+v", d)
	}
	r = env.req("run_command", model.RiskExecute, model.AutonomyAutoInSandbox, map[string]any{"command": "sudo apt install x"})
	if d := env.svc.evaluate(r, extractFacts(r.Details, nil, env.workspace, "")); d.effect != EffectAsk || d.ruleID != "ask-sudo" {
		t.Errorf("内置规则应对 sudo 询问: %+v", d)
	}
}

func TestAuditFailureFailsClosed(t *testing.T) {
	env := newEnv(t, nil)
	ctx := context.Background()
	env.store.setFailAudit(errors.New("disk full"))

	r := env.req("read_file", model.RiskRead, model.AutonomyAskRisky, map[string]any{"path": "notes.md"})
	if _, err := env.svc.Check(ctx, r); !model.IsCode(err, model.CodeInternal) {
		t.Errorf("审计失败时放行路径也应返回 internal: %v", err)
	}
	r = env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "ls"})
	if _, err := env.svc.Check(ctx, r); !model.IsCode(err, model.CodeInternal) {
		t.Errorf("审计失败时询问路径应返回 internal 而不是挂起: %v", err)
	}
	if pending, _ := env.svc.ListPending(ctx); len(pending) != 0 {
		t.Errorf("审计失败后不应留下待处理请求: %d", len(pending))
	}
}

func TestRunLoopExpiresAndStops(t *testing.T) {
	env := newEnv(t, func(d *Deps) { d.ExpireInterval = 5 * time.Millisecond })
	bg, stop := context.WithCancel(context.Background())
	defer stop()
	sub := env.subscribe(bg, 16)

	r := env.req("run_command", model.RiskExecute, model.AutonomyAskRisky, map[string]any{"command": "ls"})
	res := env.checkAsync(bg, r)
	waitEvent(t, sub, model.EventApprovalRequested)
	env.clock.Advance(DefaultApprovalTTL + time.Second)

	runCtx, runCancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- env.svc.Run(runCtx) }()
	out := waitResult(t, res)
	if out.v.Allowed || out.v.Approval.Status != model.ApprovalExpired {
		t.Errorf("Run 循环应让到期请求过期: %+v", out.v)
	}
	runCancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run 退出不应返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消 ctx 后 Run 未退出")
	}
}
