package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 审计动作与结果的常量；与 model.AuditEntry 的约定一致。
const (
	// ActionToolCheck 是每次权限判定写入的审计动作。
	ActionToolCheck = "tool.check"
	// ActionApprovalDecide 是每次审批决定（含过期）写入的审计动作。
	ActionApprovalDecide = "approval.decide"
	// ActionRulesUpdate 是规则文件被替换时写入的审计动作。
	ActionRulesUpdate = "permission.rules.update"

	// OutcomeAllowed、OutcomeDenied、OutcomeAsked 是 tool.check 的三种结果。
	OutcomeAllowed = "allowed"
	OutcomeDenied  = "denied"
	OutcomeAsked   = "asked"
)

// 默认时间参数。
const (
	// DefaultApprovalTTL 是审批请求的默认有效期。
	DefaultApprovalTTL = 10 * time.Minute
	// DefaultExpireInterval 是 Run 中过期扫描的默认周期。
	DefaultExpireInterval = 30 * time.Second
	// orphanPageSize 是扫描仓储中孤儿审批时的分页大小。
	orphanPageSize = 200
)

// Source 说明一次判定的依据。
type Source string

const (
	// SourceGuard 是内置命令/路径防护。
	SourceGuard Source = "guard"
	// SourceRule 是规则文件中的一条规则。
	SourceRule Source = "rule"
	// SourceSession 是本会话内此前的审批授权。
	SourceSession Source = "session"
	// SourceAutonomy 是自治档位的默认行为。
	SourceAutonomy Source = "autonomy"
	// SourceUntrustedInput 表示因会话含不可信输入而被提升为询问。
	SourceUntrustedInput Source = "untrusted_input"
	// SourceApproval 表示经过了用户审批（结果见 Verdict.Allowed 与 Approval）。
	SourceApproval Source = "approval"
)

// Deps 是 Service 的依赖；全部由组合根注入。
type Deps struct {
	Approvals model.ApprovalRepo
	Audit     model.AuditRepo
	Bus       *event.Bus
	Logger    *slog.Logger
	// RulesPath 是规则文件路径；为空时用 DataDir 推导为 <DataDir>/security/rules.json。
	RulesPath string
	// DataDir 是数据根目录。用于保护内核自身的文件（数据库、令牌、密钥）不被工具触碰；可为空。
	DataDir string
	// Now 用于取当前时间；为空时用 time.Now。返回值会被转为 UTC。
	Now func() time.Time
	// ApprovalTTL 是审批请求的有效期；零值为 DefaultApprovalTTL。
	ApprovalTTL time.Duration
	// ExpireInterval 是 Run 中过期扫描的周期；零值为 DefaultExpireInterval。
	ExpireInterval time.Duration
}

// Request 是一次权限判定的输入。
type Request struct {
	Principal model.Principal
	AgentID   string
	Autonomy  model.AutonomyLevel
	SessionID string
	RunID     string
	// OperationID 与 RunID 互斥；人工文件操作只能获得一次审批。
	OperationID string
	Tool        model.ToolSpec
	Call        model.ToolCall
	// Workspace 是该智能体工作区的绝对路径，用于路径越界判断。
	// 为空时相对路径无法锚定（视为工作区内，由 workspace 包最终约束），绝对路径一律视为越界。
	Workspace string
	// Summary 是面向用户的一句话描述；为空时用工具名与路径/命令拼出。
	Summary string
	// Details 是调用方整理的结构化细节（path、paths、command、url、cwd 等）；写出前会脱敏。
	Details json.RawMessage
	// UntrustedInput 表示本会话包含不可信输入（IM 消息、网页、文件内容）。
	UntrustedInput bool
	// Isolated 表示这次调用的副作用发生在操作系统级隔离中（例如 macOS Seatbelt 下的 run_command）。
	// 由 tools 按 sandbox.MechanismOf(runner) != sandbox.MechanismNone 填写：sandbox.Direct（Linux 与
	// Windows 目前的实现）以及不经沙箱执行的工具（文件工具、fetch_url、扩展工具）都是 false。
	// auto_in_sandbox 档位只对已隔离的 execute / network 自动放行，未隔离时退回 ask_risky 的行为。
	Isolated bool
}

// Verdict 是判定结果。
type Verdict struct {
	Allowed bool
	// Source 说明判定依据。
	Source Source
	// RuleID 是命中的规则或内置防护标识（Source 为 guard / rule 时）。
	RuleID string
	// Reason 是面向模型的说明：拒绝原因，或询问原因；放行时为空。
	Reason string
	// Approval 是经过的审批请求（Source 为 approval 时）。
	Approval *model.ApprovalRequest
}

// Grant 是一条会话级授权：本会话内对同一工具的后续调用免审批。
type Grant struct {
	SessionID string
	Tool      string
	// OutsideDirs 是授权时涉及的工作区外目录；后续调用的越界路径必须都在这些目录下才算命中。
	OutsideDirs []string
	GrantedBy   string
	ApprovalID  string
	At          time.Time
}

// decision 是规则求值的内部结果。
type decision struct {
	effect Effect
	source Source
	ruleID string
	reason string
}

// waiter 是一个等待中的审批。
type waiter struct {
	req   *model.ApprovalRequest
	facts facts
	// done 在到达终态时关闭。
	done chan struct{}
	// registered 表示已登记在 Service.waiters 中（孤儿记录不会登记）。
	registered bool
	resolved   bool
	decision   model.Decision
	// systemReason 记录非用户决定的终态原因："expired" 或 "cancelled"。
	systemReason string
	// 人工操作须在审批和审计均持久化后唤醒；失败时拒绝执行。
	resolutionError error
}

// Service 是权限规则引擎、审批代理与审计的组合。它可以被多个 goroutine 并发使用。
type Service struct {
	deps      Deps
	logger    *slog.Logger
	now       func() time.Time
	ttl       time.Duration
	rulesPath string
	home      string
	// dataDir 是规范化后的数据目录（Windows 上已展开 8.3 短名）；为空表示不做数据目录防护。
	dataDir string
	// foldPaths 表示文件系统不区分大小写（caseInsensitivePaths）：路径类规则与工作区豁免据此不区分大小写。
	foldPaths bool
	// kernelPathRe 匹配命令中对数据目录（自定义位置）下敏感文件的引用。
	kernelPathRe *regexp.Regexp

	mu      sync.Mutex
	waiters map[string]*waiter

	grantMu sync.RWMutex
	grants  map[string]map[string]*Grant

	ruleMu sync.RWMutex
	rules  *ruleSet
}

// New 创建服务：校验依赖、确保规则文件存在（首次写入内置默认规则）并加载。
func New(deps Deps) (*Service, error) {
	if deps.Approvals == nil || deps.Audit == nil || deps.Bus == nil {
		return nil, model.NewError(model.CodeInvalid, "permission: Approvals、Audit 与 Bus 都不能为空")
	}
	if deps.RulesPath == "" {
		if deps.DataDir == "" {
			return nil, model.NewError(model.CodeInvalid, "permission: RulesPath 与 DataDir 至少提供一个")
		}
		deps.RulesPath = DefaultRulesPath(deps.DataDir)
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	ttl := deps.ApprovalTTL
	if ttl <= 0 {
		ttl = DefaultApprovalTTL
	}
	home, _ := os.UserHomeDir()

	s := &Service{
		deps:      deps,
		logger:    logger,
		now:       func() time.Time { return now().UTC() },
		ttl:       ttl,
		rulesPath: deps.RulesPath,
		home:      home,
		foldPaths: caseInsensitivePaths,
		waiters:   make(map[string]*waiter),
		grants:    make(map[string]map[string]*Grant),
	}
	if deps.DataDir != "" {
		s.dataDir = canonicalPath(filepath.Clean(deps.DataDir))
		names := make([]string, 0, len(kernelFileNames))
		for _, n := range kernelFileNames {
			names = append(names, regexp.QuoteMeta(n))
		}
		// 数据目录的几种写法（原样、长名、解析符号链接后）都要拦住。
		var dirs []string
		for _, d := range []string{filepath.Clean(deps.DataDir), s.dataDir, resolveExisting(s.dataDir)} {
			q := regexp.QuoteMeta(d)
			if !containsString(dirs, q) {
				dirs = append(dirs, q)
			}
		}
		s.kernelPathRe = regexp.MustCompile(`(?i)(` + strings.Join(dirs, "|") + `)[/\\](` + strings.Join(names, "|") + `)`)
	}

	created, err := ensureRuleFile(s.rulesPath)
	if err != nil {
		return nil, err
	}
	if created {
		logger.Info("已写入默认权限规则", "path", s.rulesPath)
	}
	if err := s.ReloadRules(); err != nil {
		return nil, err
	}
	return s, nil
}

// RulesPath 返回规则文件路径。
func (s *Service) RulesPath() string { return s.rulesPath }

// Rules 返回当前生效规则的副本。
func (s *Service) Rules() []Rule {
	s.ruleMu.RLock()
	defer s.ruleMu.RUnlock()
	out := make([]Rule, 0, len(s.rules.rules))
	for _, r := range s.rules.rules {
		out = append(out, r.Rule)
	}
	return out
}

// ReloadRules 重新读取并编译规则文件；文件无效时保留旧规则并返回 CodeInvalid。
func (s *Service) ReloadRules() error {
	rf, err := loadRuleFile(s.rulesPath)
	if err != nil {
		return err
	}
	rs, err := compileRules(rf.Rules, s.home)
	if err != nil {
		return err
	}
	s.ruleMu.Lock()
	s.rules = rs
	s.ruleMu.Unlock()
	return nil
}

// ReplaceRules 校验、写入并启用一组新规则，同时写审计。
func (s *Service) ReplaceRules(ctx context.Context, rules []Rule, by string) error {
	rs, err := compileRules(rules, s.home)
	if err != nil {
		return err
	}
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	if err := writeRuleFile(s.rulesPath, &RuleFile{Rules: rules}); err != nil {
		return err
	}
	s.rules = rs
	return s.appendAudit(ctx, &model.AuditEntry{
		Actor:    actorOr(by, "system"),
		Action:   ActionRulesUpdate,
		Resource: s.rulesPath,
		Outcome:  "ok",
		Details:  marshalDetails(map[string]any{"rule_count": len(rules)}),
	})
}

func (s *Service) currentRules() *ruleSet {
	s.ruleMu.RLock()
	defer s.ruleMu.RUnlock()
	return s.rules
}

// Check 判定一次工具调用是否允许。需要审批时会阻塞直到用户决定、请求过期或 ctx 结束。
//
// ctx 结束时：审批请求被标记为 expired、返回的 Verdict 为拒绝，同时返回带
// CodeCancelled（取消）或 CodeTimeout（超时）的错误；调用方可以只看错误，也可以读取 Verdict。
// 其他非 nil 错误表示基础设施失败（仓储、审计），此时不应执行工具。
func (s *Service) Check(ctx context.Context, req Request) (Verdict, error) {
	if err := s.normalizeRequest(&req); err != nil {
		return Verdict{}, err
	}
	f := extractFacts(req.Details, req.Call.Arguments, req.Workspace, s.home)
	d := s.evaluate(req, f)

	if d.effect != EffectAsk {
		outcome := OutcomeDenied
		if d.effect == EffectAllow {
			outcome = OutcomeAllowed
		}
		if err := s.auditCheck(ctx, req, f, d, outcome, ""); err != nil {
			return Verdict{}, err
		}
		return Verdict{Allowed: d.effect == EffectAllow, Source: d.source, RuleID: d.ruleID, Reason: d.reason}, nil
	}
	return s.ask(ctx, req, f, d)
}

// normalizeRequest 校验并补全请求字段。
func (s *Service) normalizeRequest(req *Request) error {
	if req.OperationID != "" {
		if req.RunID != "" || req.Principal.Kind != model.PrincipalLocalUser || req.Principal.ID == "" || req.SessionID == "" || req.AgentID == "" || req.Call.Name != model.OperationFileChangeTool || (req.Tool.Risk != model.RiskWrite && req.Tool.Risk != model.RiskDestructive) {
			return model.NewError(model.CodeInvalid, i18n.T("en", "operation.invalid_context", nil))
		}
		var args map[string]json.RawMessage
		if json.Unmarshal(req.Call.Arguments, &args) != nil || len(args) != 2 {
			return model.NewError(model.CodeInvalid, i18n.T("en", "operation.invalid_arguments", nil))
		}
		canonical := map[string]string{}
		for _, key := range []string{"change_id", "preview_digest"} {
			var value string
			if json.Unmarshal(args[key], &value) != nil || value == "" {
				return model.NewError(model.CodeInvalid, i18n.T("en", "operation.invalid_arguments", nil))
			}
			canonical[key] = value
		}
		req.Call.Arguments, _ = json.Marshal(canonical)
	}
	if req.Tool.Name == "" {
		req.Tool.Name = req.Call.Name
	}
	if req.Tool.Name == "" {
		return model.NewError(model.CodeInvalid, "permission: 工具名不能为空")
	}
	if req.Call.Name == "" {
		req.Call.Name = req.Tool.Name
	} else if req.Call.Name != req.Tool.Name {
		return model.Errorf(model.CodeInvalid, "permission: 调用的工具名 %q 与声明 %q 不一致", req.Call.Name, req.Tool.Name)
	}
	if !validRisks[req.Tool.Risk] {
		return model.Errorf(model.CodeInvalid, "permission: 工具 %q 的风险等级无效: %q", req.Tool.Name, req.Tool.Risk)
	}
	switch req.Autonomy {
	case model.AutonomyAskAll, model.AutonomyAskRisky, model.AutonomyAutoInSandbox:
	default:
		s.logger.Warn("未知的自治档位，按 ask_all 处理", "autonomy", string(req.Autonomy), "agent_id", req.AgentID)
		req.Autonomy = model.AutonomyAskAll
	}
	if req.Workspace != "" {
		req.Workspace = canonicalPath(filepath.Clean(req.Workspace))
	}
	return nil
}

// evaluate 按固定顺序求值：
// 内置防护 → 显式 deny → 显式 allow → 会话授权 → 显式 ask → 自治档位默认。
// 不可信输入会把 execute / network / destructive 的放行提升为询问（"三者取二"）。
func (s *Service) evaluate(req Request, f facts) decision {
	if hit := guardCommand(f.command, s.home); hit != nil {
		return decision{effect: EffectDeny, source: SourceGuard, ruleID: hit.ID, reason: "blocked by built-in guard: " + hit.Reason}
	}
	if s.kernelPathRe != nil && f.command != "" && s.kernelPathRe.MatchString(f.command) {
		return decision{effect: EffectDeny, source: SourceGuard, ruleID: "kernel-data-files", reason: "blocked by built-in guard: touching gogosling's own token, secret or database files"}
	}
	if hit := guardPaths(f, s.dataDir, req.Workspace, s.foldPaths); hit != nil {
		return decision{effect: EffectDeny, source: SourceGuard, ruleID: hit.ID, reason: "blocked by built-in guard: " + hit.Reason}
	}

	rs := s.currentRules()
	if r := rs.firstMatch(EffectDeny, req.Tool.Name, req.Tool.Risk, f, s.foldPaths); r != nil {
		return decision{effect: EffectDeny, source: SourceRule, ruleID: ruleLabel(r), reason: "denied by permission rule " + ruleLabel(r)}
	}
	// 人工预览确认不构成长期授权；即使命中 allow、旧会话授权或自动档位仍须本次审批。
	if req.OperationID != "" {
		return decision{effect: EffectAsk, source: SourceApproval, reason: "explicit user operation requires one-time approval"}
	}

	untrustedFloor := req.UntrustedInput &&
		(req.Tool.Risk == model.RiskExecute || req.Tool.Risk == model.RiskNetwork || req.Tool.Risk == model.RiskDestructive)
	floor := func(d decision) decision {
		if untrustedFloor {
			return decision{effect: EffectAsk, source: SourceUntrustedInput, ruleID: d.ruleID, reason: "session contains untrusted input"}
		}
		return d
	}

	if r := rs.firstMatch(EffectAllow, req.Tool.Name, req.Tool.Risk, f, s.foldPaths); r != nil {
		return floor(decision{effect: EffectAllow, source: SourceRule, ruleID: ruleLabel(r)})
	}
	if g := s.matchGrant(req.SessionID, req.Tool.Name, f); g != nil {
		return floor(decision{effect: EffectAllow, source: SourceSession, ruleID: g.ApprovalID})
	}
	if r := rs.firstMatch(EffectAsk, req.Tool.Name, req.Tool.Risk, f, s.foldPaths); r != nil {
		return decision{effect: EffectAsk, source: SourceRule, ruleID: ruleLabel(r), reason: "permission rule " + ruleLabel(r) + " requires approval"}
	}

	d := autonomyDefault(req.Autonomy, req.Tool.Risk, f, req.Isolated)
	if d.effect == EffectAllow {
		return floor(d)
	}
	return d
}

func ruleLabel(r *compiledRule) string {
	if r.ID != "" {
		return r.ID
	}
	return fmt.Sprintf("#%d", r.index)
}

// autonomyDefault 是没有任何规则与授权命中时的档位默认行为。
//
//   - ask_all：全部询问。
//   - ask_risky：工作区内的 read / write 放行；execute / network / destructive 询问。
//   - auto_in_sandbox：沙箱内放行，越界询问。
//     execute / network 只有在操作系统级隔离中（isolated，即 Request.Isolated）才放行；
//     没有隔离时（Linux / Windows 上的 sandbox.Direct、fetch_url、扩展工具）退回 ask_risky 的行为，即询问。
//     read / write 的"沙箱内"证据是路径都在工作区内；没有路径事实的 destructive 操作无法证明在沙箱内，仍然询问。
//
// 任何档位下，越出工作区的路径都会提升为询问（风险升级）。
func autonomyDefault(level model.AutonomyLevel, risk model.ToolRisk, f facts, isolated bool) decision {
	if outside := f.outsidePaths(); len(outside) > 0 {
		return decision{effect: EffectAsk, source: SourceAutonomy, reason: "path outside the agent workspace"}
	}
	switch level {
	case model.AutonomyAskRisky:
		if risk == model.RiskRead || risk == model.RiskWrite {
			return decision{effect: EffectAllow, source: SourceAutonomy}
		}
		return decision{effect: EffectAsk, source: SourceAutonomy, reason: "autonomy level ask_risky requires approval for " + string(risk)}
	case model.AutonomyAutoInSandbox:
		if !isolated && (risk == model.RiskExecute || risk == model.RiskNetwork) {
			return decision{effect: EffectAsk, source: SourceAutonomy, reason: "autonomy level auto_in_sandbox requires approval for " + string(risk) + " without OS-level isolation"}
		}
		if risk == model.RiskDestructive && !f.hasInsidePath() {
			return decision{effect: EffectAsk, source: SourceAutonomy, reason: "destructive operation without workspace evidence"}
		}
		return decision{effect: EffectAllow, source: SourceAutonomy}
	default:
		return decision{effect: EffectAsk, source: SourceAutonomy, reason: "autonomy level ask_all requires approval"}
	}
}

// ask 创建审批请求、发布事件、写审计并阻塞等待。
func (s *Service) ask(ctx context.Context, req Request, f facts, d decision) (Verdict, error) {
	now := s.now()
	ar := &model.ApprovalRequest{
		ID:          model.NewID(),
		RunID:       req.RunID,
		OperationID: req.OperationID,
		SessionID:   req.SessionID,
		AgentID:     req.AgentID,
		ToolCall:    req.Call,
		Risk:        req.Tool.Risk,
		Summary:     summaryFor(req, f),
		Details:     approvalDetails(req, f, d),
		Status:      model.ApprovalPending,
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.ttl),
	}
	w := &waiter{req: ar, facts: f, done: make(chan struct{}), registered: true}

	s.mu.Lock()
	s.waiters[ar.ID] = w
	if err := s.deps.Approvals.CreateApproval(ctx, ar); err != nil {
		delete(s.waiters, ar.ID)
		s.mu.Unlock()
		return Verdict{}, model.Wrap(model.CodeInternal, "permission: 创建审批请求失败", err)
	}
	// 解锁后 Decide 可能立刻改写 ar 的状态，因此事件载荷用锁内的副本。
	created := *ar
	s.mu.Unlock()

	s.publish(model.EventApprovalRequested, &created)
	if err := s.auditCheck(ctx, req, f, d, OutcomeAsked, ar.ID); err != nil {
		// 审计失败视为基础设施故障：不让这次调用继续等待，直接作废审批请求。
		_ = s.resolve(context.WithoutCancel(ctx), w, model.ApprovalExpired, model.Decision{}, "system", "cancelled")
		return Verdict{}, err
	}
	s.logger.Info("等待审批", "approval_id", ar.ID, "tool", req.Tool.Name, "risk", string(req.Tool.Risk), "session_id", req.SessionID)

	timer := time.NewTimer(ar.ExpiresAt.Sub(now))
	defer timer.Stop()
	select {
	case <-w.done:
	case <-ctx.Done():
		s.expireWaiter(context.WithoutCancel(ctx), w, "cancelled")
	case <-timer.C:
		s.expireWaiter(context.WithoutCancel(ctx), w, "expired")
	}

	final, dec, systemReason := w.snapshot()
	v := Verdict{Source: SourceApproval, Approval: final}
	switch final.Status {
	case model.ApprovalApproved:
		v.Allowed = true
	case model.ApprovalDenied:
		v.Reason = "denied by user"
		if dec.Reason != "" {
			v.Reason += ": " + truncateString(dec.Reason, maxCommandInAudit)
		}
	default:
		if systemReason == "cancelled" {
			v.Reason = "approval wait cancelled"
		} else {
			v.Reason = "approval request expired without a decision"
		}
	}
	if err := ctx.Err(); err != nil {
		return v, model.Wrap(codeForCtx(err), "permission: 审批等待被中止", err)
	}
	if req.OperationID != "" {
		if w.resolutionError != nil {
			return v, w.resolutionError
		}
		if final.Status == model.ApprovalExpired {
			return v, model.NewError(model.CodeTimeout, i18n.T("en", "operation.approval_expired", nil))
		}
	}
	return v, nil
}

// expireWaiter 由等待方自己触发的过期/取消：先迁移状态，再补一条审批决定审计。
func (s *Service) expireWaiter(ctx context.Context, w *waiter, reason string) {
	r := s.resolve(ctx, w, model.ApprovalExpired, model.Decision{}, "system", reason)
	if r.err != nil {
		s.logger.Error("写入审批过期状态失败", "approval_id", w.req.ID, "error", r.err)
	}
	if r.already {
		return
	}
	if !r.audited {
		if err := s.auditDecide(ctx, r.final, "system", reason); err != nil {
			s.logger.Error("写入审批审计失败", "approval_id", w.req.ID, "error", err)
		}
	}
}

// snapshot 在锁内复制等待者的终态。
func (w *waiter) snapshot() (*model.ApprovalRequest, model.Decision, string) {
	cp := *w.req
	return &cp, w.decision, w.systemReason
}

// resolution 是 resolve 的结果。
type resolution struct {
	// audited 表示人工操作的审批审计已在唤醒执行器之前写入。
	audited bool
	// final 是终态副本（already 时是别人迁移出的终态）。
	final *model.ApprovalRequest
	// already 表示等待者早已被别人迁移到终态；此时本次调用没有任何副作用（不写规则、不发事件）。
	already bool
	// err 是持久化审批状态失败；内存状态已迁移、等待者已唤醒。
	err error
	// rules 是 ScopeAlways 本次写入规则文件的规则。
	rules []Rule
	// ruleErr 是 ScopeAlways 写入规则失败；此时决定已降级为 ScopeOnce 生效。
	ruleErr error
}

// resolve 把等待者迁移到终态并持久化。
// 内存状态先于仓储更新，因此即使仓储失败，等待者也会被唤醒并得到确定的结果。
//
// ScopeAlways 的批准或拒绝在同一临界区内、确认本次迁移有效（等待者尚未到达终态）之后才写入规则文件：
// 并发的过期 / 取消要么先完成（本次 already，不写规则），要么在本次完成后才看到终态。
// 规则写入失败时决定降级为 ScopeOnce（只对这一次调用生效），失败原因放在 ruleErr。
// 等待者在规则写入（或降级）之后才被唤醒，approval.resolved 事件携带最终的 Scope。
func (s *Service) resolve(ctx context.Context, w *waiter, status model.ApprovalStatus, dec model.Decision, by, systemReason string) resolution {
	s.mu.Lock()
	if w.resolved {
		cp := *w.req
		s.mu.Unlock()
		return resolution{final: &cp, already: true}
	}
	w.resolved = true
	var res resolution
	userDecision := status == model.ApprovalApproved || status == model.ApprovalDenied
	if w.req.OperationID != "" && userDecision {
		dec.Scope = model.ScopeOnce
	}
	if userDecision && dec.Scope == model.ScopeAlways {
		rules, err := s.persistAlwaysRule(w, dec, by)
		if err != nil {
			s.logger.Error("写入永久规则失败，本次决定降级为 once", "approval_id", w.req.ID, "error", err)
			res.ruleErr = err
			dec.Scope = model.ScopeOnce
		} else {
			res.rules = rules
		}
	}
	now := s.now()
	w.req.Status = status
	w.req.DecidedBy = by
	w.req.ResolvedAt = &now
	w.decision = dec
	w.systemReason = systemReason
	if userDecision {
		w.req.Scope = dec.Scope
	}
	if status == model.ApprovalApproved && dec.Scope == model.ScopeSession {
		s.addGrant(w.req, w.facts, by)
	}
	if w.registered {
		delete(s.waiters, w.req.ID)
	}
	err := s.deps.Approvals.UpdateApproval(ctx, w.req)
	if w.req.OperationID != "" {
		if err == nil {
			err = s.auditDecide(ctx, w.req, by, systemReason)
			res.audited = err == nil
		}
		if err != nil {
			w.resolutionError = model.Wrap(model.CodeInternal, i18n.T("en", "operation.approval_persist_failed", nil), err)
			w.req.Status = model.ApprovalExpired
			w.decision.Approve = false
		}
	}
	close(w.done)
	cp := *w.req
	s.mu.Unlock()

	if err != nil {
		res.err = model.Wrap(model.CodeInternal, "permission: 更新审批状态失败", err)
	}
	s.publish(model.EventApprovalResolved, &cp)
	res.final = &cp
	return res
}

// Decide 写回用户对审批请求的决定并唤醒等待者。
//
// 幂等：对已是相同结果的请求重复提交返回当前状态；对结果不同或已过期的请求返回 CodeConflict
// （此时不写规则、不留下任何授权）。ScopeSession 的批准记入内存会话授权表；ScopeAlways 的决定
// （批准或拒绝）在确认审批仍待决之后写入规则文件并写一条 permission.rules.update 审计。
// 规则写入失败时决定降级为 once 生效（等待中的调用照常得到批准或拒绝），并返回 CodeInternal 错误与该终态。
func (s *Service) Decide(ctx context.Context, id string, dec model.Decision, by string) (*model.ApprovalRequest, error) {
	if id == "" {
		return nil, model.NewError(model.CodeInvalid, "permission: 审批 ID 不能为空")
	}
	if dec.Scope == "" {
		dec.Scope = model.ScopeOnce
	}
	switch dec.Scope {
	case model.ScopeOnce, model.ScopeSession, model.ScopeAlways:
	default:
		return nil, model.Errorf(model.CodeInvalid, "permission: 审批范围无效: %q", dec.Scope)
	}
	by = actorOr(by, "unknown")
	want := model.ApprovalDenied
	if dec.Approve {
		want = model.ApprovalApproved
	}

	s.mu.Lock()
	w, ok := s.waiters[id]
	s.mu.Unlock()
	if !ok {
		// 没有等待者：可能已是终态，也可能是上一个进程留下的孤儿记录。
		ar, err := s.deps.Approvals.GetApproval(ctx, id)
		if err != nil {
			return nil, err
		}
		if ar.OperationID != "" && dec.Scope != model.ScopeOnce {
			return nil, model.NewError(model.CodeInvalid, i18n.T("en", "operation.once_only", nil))
		}
		if ar.OperationID != "" && ar.Status == model.ApprovalPending {
			orphan := &waiter{req: ar, done: make(chan struct{})}
			s.expireWaiter(ctx, orphan, "expired")
			return nil, model.NewError(model.CodeConflict, i18n.T("en", "operation.approval_orphaned", nil))
		}
		if ar.Status != model.ApprovalPending {
			if ar.Status == want {
				return ar, nil
			}
			return nil, model.Errorf(model.CodeConflict, "permission: 审批 %s 已是终态 %s", id, ar.Status)
		}
		w = &waiter{req: ar, facts: extractFacts(ar.Details, ar.ToolCall.Arguments, "", s.home), done: make(chan struct{})}
	}
	if w.req.OperationID != "" && dec.Scope != model.ScopeOnce {
		return nil, model.NewError(model.CodeInvalid, i18n.T("en", "operation.once_only", nil))
	}

	r := s.resolve(ctx, w, want, dec, by, "")
	if r.already {
		if r.final.Status == want {
			return r.final, nil
		}
		return nil, model.Errorf(model.CodeConflict, "permission: 审批 %s 已是终态 %s", id, r.final.Status)
	}
	// 规则一旦写入就已生效，无论审批状态是否持久化成功都要留下审计。
	var rulesAuditErr error
	if len(r.rules) > 0 {
		rulesAuditErr = s.auditAlwaysRule(ctx, r.final, r.rules, by)
	}
	if r.err != nil {
		return r.final, r.err
	}
	if rulesAuditErr != nil {
		return r.final, rulesAuditErr
	}
	if !r.audited {
		if err := s.auditDecide(ctx, r.final, by, dec.Reason); err != nil {
			return r.final, err
		}
	}
	if r.ruleErr != nil {
		return r.final, model.Wrap(model.CodeInternal, "permission: 永久规则写入失败，本次决定已按 once 生效", r.ruleErr)
	}
	s.logger.Info("审批已决定", "approval_id", id, "status", string(r.final.Status), "scope", string(r.final.Scope))
	return r.final, nil
}

// Get 返回一个审批请求。
func (s *Service) Get(ctx context.Context, id string) (*model.ApprovalRequest, error) {
	return s.deps.Approvals.GetApproval(ctx, id)
}

// ListPending 返回全部待决定的审批请求（按创建时间升序）。
func (s *Service) ListPending(ctx context.Context) ([]*model.ApprovalRequest, error) {
	return s.listAll(ctx, model.ApprovalPending)
}

// List 按状态分页列出审批请求。
func (s *Service) List(ctx context.Context, status model.ApprovalStatus, p model.Page) ([]*model.ApprovalRequest, error) {
	return s.deps.Approvals.ListApprovals(ctx, status, p)
}

func (s *Service) listAll(ctx context.Context, status model.ApprovalStatus) ([]*model.ApprovalRequest, error) {
	var out []*model.ApprovalRequest
	for offset := 0; ; offset += orphanPageSize {
		page, err := s.deps.Approvals.ListApprovals(ctx, status, model.Page{Limit: orphanPageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < orphanPageSize {
			return out, nil
		}
	}
}

// Run 周期性地把到期的审批标记为过期，直到 ctx 结束。启动时先执行一次，以处理上一个进程留下的记录。
func (s *Service) Run(ctx context.Context) error {
	interval := s.deps.ExpireInterval
	if interval <= 0 {
		interval = DefaultExpireInterval
	}
	if _, err := s.ExpireDue(ctx, s.now()); err != nil {
		s.logger.Warn("过期扫描失败", "error", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := s.ExpireDue(ctx, s.now()); err != nil {
				s.logger.Warn("过期扫描失败", "error", err)
			}
		}
	}
}

// ExpireDue 把 ExpiresAt 不晚于 now 的等待中审批标记为过期并唤醒等待者；
// 同时把仓储中没有等待者的 pending 记录（上一个进程遗留）立即标记为过期，因为它们已无人可以送达。
// 返回处理的条数。
func (s *Service) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	now = now.UTC()
	s.mu.Lock()
	due := make([]*waiter, 0)
	for _, w := range s.waiters {
		if !w.req.ExpiresAt.After(now) {
			due = append(due, w)
		}
	}
	s.mu.Unlock()
	for _, w := range due {
		s.expireWaiter(ctx, w, "expired")
	}
	count := len(due)

	rows, err := s.listAll(ctx, model.ApprovalPending)
	if err != nil {
		return count, err
	}
	for _, ar := range rows {
		s.mu.Lock()
		_, live := s.waiters[ar.ID]
		s.mu.Unlock()
		if live {
			continue
		}
		// 在锁外重新读取，避免把刚刚完成的记录当成孤儿。
		fresh, err := s.deps.Approvals.GetApproval(ctx, ar.ID)
		if err != nil || fresh.Status != model.ApprovalPending {
			continue
		}
		w := &waiter{req: fresh, done: make(chan struct{})}
		s.expireWaiter(ctx, w, "expired")
		count++
	}
	return count, nil
}

// SessionGrants 返回某会话当前的授权（副本）。
func (s *Service) SessionGrants(sessionID string) []Grant {
	s.grantMu.RLock()
	defer s.grantMu.RUnlock()
	m := s.grants[sessionID]
	out := make([]Grant, 0, len(m))
	for _, g := range m {
		cp := *g
		cp.OutsideDirs = append([]string(nil), g.OutsideDirs...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

// RevokeSession 清除某会话的全部授权；会话归档或删除时调用。
func (s *Service) RevokeSession(sessionID string) {
	s.grantMu.Lock()
	delete(s.grants, sessionID)
	s.grantMu.Unlock()
}

// addGrant 登记会话授权；同一工具已有授权时合并越界目录。
func (s *Service) addGrant(ar *model.ApprovalRequest, f facts, by string) {
	if ar.SessionID == "" {
		return
	}
	dirs := outsideDirs(f)
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	m := s.grants[ar.SessionID]
	if m == nil {
		m = make(map[string]*Grant)
		s.grants[ar.SessionID] = m
	}
	if g, ok := m[ar.ToolCall.Name]; ok {
		g.OutsideDirs = mergeDirs(g.OutsideDirs, dirs)
		g.GrantedBy = by
		g.ApprovalID = ar.ID
		g.At = s.now()
		return
	}
	m[ar.ToolCall.Name] = &Grant{SessionID: ar.SessionID, Tool: ar.ToolCall.Name, OutsideDirs: dirs, GrantedBy: by, ApprovalID: ar.ID, At: s.now()}
}

// matchGrant 查找覆盖本次调用的会话授权：工具名相同，且越界路径都在已授权目录下。
func (s *Service) matchGrant(sessionID, tool string, f facts) *Grant {
	if sessionID == "" {
		return nil
	}
	s.grantMu.RLock()
	defer s.grantMu.RUnlock()
	g, ok := s.grants[sessionID][tool]
	if !ok {
		return nil
	}
	for _, p := range f.paths {
		if !p.outside {
			continue
		}
		if p.abs == "" {
			return nil
		}
		covered := false
		for _, d := range g.OutsideDirs {
			if pathWithin(d, p.abs) {
				covered = true
				break
			}
		}
		if !covered {
			return nil
		}
	}
	cp := *g
	return &cp
}

// outsideDirs 取越界路径所在的目录，作为授权与规则的范围。
func outsideDirs(f facts) []string {
	var dirs []string
	for _, p := range f.paths {
		if p.outside && p.abs != "" {
			dirs = mergeDirs(dirs, []string{filepath.Dir(p.abs)})
		}
	}
	return dirs
}

func mergeDirs(a, b []string) []string {
	for _, d := range b {
		dup := false
		for _, e := range a {
			if e == d {
				dup = true
				break
			}
		}
		if !dup {
			a = append(a, d)
		}
	}
	return a
}

// persistAlwaysRule 把 ScopeAlways 决定转成规则、追加到规则文件并启用，返回写入的规则。
// 只由 resolve 在确认迁移有效后调用；对应的 permission.rules.update 审计由 Decide 经 auditAlwaysRule 写入。
//
// 生成的规则尽量具体：工具名精确、风险等级固定；有路径时限定在对应目录（工作区内限定为工作区，
// 工作区外限定为该外部目录），有命令时精确匹配整条命令。这样"永久允许"不会变成对该工具的全面放行。
func (s *Service) persistAlwaysRule(w *waiter, dec model.Decision, by string) ([]Rule, error) {
	effect := EffectDeny
	if dec.Approve {
		effect = EffectAllow
	}
	ar := w.req
	base := Rule{
		ID:      "approval-" + strings.ToLower(ar.ID),
		Tool:    escapeWildcard(ar.ToolCall.Name),
		Risk:    ar.Risk,
		Effect:  effect,
		Origin:  "approval:" + ar.ID,
		Comment: "scope=always by=" + by,
	}
	if w.facts.command != "" {
		base.Command = "^" + regexp.QuoteMeta(w.facts.command) + "$"
	}
	var rules []Rule
	dirs := outsideDirs(w.facts)
	switch {
	case len(dirs) > 0:
		for i, d := range dirs {
			r := base
			if i > 0 {
				r.ID = fmt.Sprintf("%s-%d", base.ID, i)
			}
			r.Path = filepath.ToSlash(d) + "/**"
			rules = append(rules, r)
		}
	case w.facts.hasInsidePath():
		ws := workspaceOf(w.facts)
		if ws != "" {
			base.Path = filepath.ToSlash(ws) + "/**"
		}
		rules = append(rules, base)
	default:
		rules = append(rules, base)
	}

	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	// 以磁盘上的文件为准追加，避免覆盖用户在运行期间的手工修改。
	rf, err := loadRuleFile(s.rulesPath)
	if err != nil {
		return nil, err
	}
	rf.Rules = append(rf.Rules, rules...)
	rs, err := compileRules(rf.Rules, s.home)
	if err != nil {
		return nil, err
	}
	if err := writeRuleFile(s.rulesPath, rf); err != nil {
		return nil, err
	}
	s.rules = rs
	return rules, nil
}

// auditAlwaysRule 为 ScopeAlways 写入的规则补一条 permission.rules.update 审计（与 ReplaceRules 同一动作）。
func (s *Service) auditAlwaysRule(ctx context.Context, ar *model.ApprovalRequest, rules []Rule, by string) error {
	ids := make([]string, 0, len(rules))
	for _, r := range rules {
		ids = append(ids, r.ID)
	}
	effect := ""
	if len(rules) > 0 {
		effect = string(rules[0].Effect)
	}
	return s.appendAudit(ctx, &model.AuditEntry{
		Actor:     by,
		AgentID:   ar.AgentID,
		SessionID: ar.SessionID,
		RunID:     ar.RunID,
		Action:    ActionRulesUpdate,
		Resource:  s.rulesPath,
		Outcome:   "ok",
		Details: marshalDetails(map[string]any{
			"source":      "approval",
			"approval_id": ar.ID,
			"tool":        ar.ToolCall.Name,
			"effect":      effect,
			"added_rules": ids,
		}),
	})
}

// workspaceOf 由工作区内路径反推工作区根目录（abs 去掉 rel）。
func workspaceOf(f facts) string {
	for _, p := range f.paths {
		if !p.inside || p.abs == "" {
			continue
		}
		if p.rel == "." {
			return p.abs
		}
		rel := filepath.FromSlash(p.rel)
		if strings.HasSuffix(p.abs, rel) {
			return filepath.Clean(strings.TrimSuffix(p.abs, rel))
		}
	}
	return ""
}

// escapeWildcard 让工具名中的 *、? 与 \ 按字面匹配。
func escapeWildcard(name string) string {
	if !strings.ContainsAny(name, `*?\`) {
		return name
	}
	var b strings.Builder
	for _, c := range name {
		if c == '*' || c == '?' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// summaryFor 在调用方没有给出摘要时用工具名与关键事实拼一条数据性描述。
func summaryFor(req Request, f facts) string {
	if s := strings.TrimSpace(req.Summary); s != "" {
		return truncateString(s, maxDetailString)
	}
	parts := []string{req.Tool.Name}
	switch {
	case f.command != "":
		parts = append(parts, truncateString(f.command, maxCommandInAudit))
	case len(f.paths) > 0:
		parts = append(parts, truncateString(f.paths[0].raw, 256))
	case f.url != "":
		parts = append(parts, truncateString(f.url, 256))
	}
	return strings.Join(parts, ": ")
}

// approvalDetails 组合调用方 Details（脱敏后）与判定事实，供界面展示与持久化。
func approvalDetails(req Request, f facts, d decision) json.RawMessage {
	m := sanitizeDetails(req.Details)
	if _, ok := m["command"]; !ok && f.command != "" {
		m["command"] = truncateString(f.command, maxDetailString)
	}
	if _, ok := m["path"]; !ok {
		if _, ok := m["paths"]; !ok && len(f.paths) > 0 {
			m["paths"] = limitPaths(f.rawPaths())
		}
	}
	if _, ok := m["url"]; !ok && f.url != "" {
		m["url"] = truncateString(f.url, 256)
	}
	if outside := f.outsidePaths(); len(outside) > 0 {
		m["out_of_workspace"] = true
		m["outside_paths"] = limitPaths(outside)
	}
	if req.UntrustedInput {
		m["untrusted_input"] = true
	}
	m["ask_source"] = string(d.source)
	if d.ruleID != "" {
		m["rule_id"] = d.ruleID
	}
	if d.reason != "" {
		m["ask_reason"] = d.reason
	}
	m["autonomy"] = string(req.Autonomy)
	return marshalDetails(m)
}

// auditCheck 写一条 tool.check 审计。
func (s *Service) auditCheck(ctx context.Context, req Request, f facts, d decision, outcome, approvalID string) error {
	details := map[string]any{
		"risk":     string(req.Tool.Risk),
		"autonomy": string(req.Autonomy),
		"source":   string(d.source),
	}
	if d.ruleID != "" {
		details["rule_id"] = d.ruleID
	}
	if d.reason != "" {
		details["reason"] = d.reason
	}
	if req.UntrustedInput {
		details["untrusted_input"] = true
	}
	if req.Isolated {
		details["isolated"] = true
	}
	if outside := f.outsidePaths(); len(outside) > 0 {
		details["out_of_workspace"] = true
		details["outside_paths"] = limitPaths(outside)
	}
	if len(f.paths) > 0 {
		details["paths"] = limitPaths(f.rawPaths())
	}
	if f.command != "" {
		details["command"] = truncateString(f.command, maxCommandInAudit)
	}
	if f.url != "" {
		details["url"] = truncateString(f.url, 256)
	}
	if approvalID != "" {
		details["approval_id"] = approvalID
	}
	if req.OperationID != "" {
		details["operation_id"] = req.OperationID
	}
	actor := req.Principal.ID
	if actor == "" {
		actor = "agent:" + req.AgentID
	}
	return s.appendAudit(ctx, &model.AuditEntry{
		Actor:     actor,
		AgentID:   req.AgentID,
		SessionID: req.SessionID,
		RunID:     req.RunID,
		Action:    ActionToolCheck,
		Resource:  req.Tool.Name,
		Outcome:   outcome,
		Details:   marshalDetails(details),
	})
}

// auditDecide 写一条 approval.decide 审计；reason 是用户填写的理由或系统原因（expired / cancelled）。
func (s *Service) auditDecide(ctx context.Context, ar *model.ApprovalRequest, by, reason string) error {
	outcome := OutcomeDenied
	if ar.Status == model.ApprovalApproved {
		outcome = OutcomeAllowed
	}
	details := map[string]any{
		"tool":   ar.ToolCall.Name,
		"risk":   string(ar.Risk),
		"status": string(ar.Status),
	}
	if ar.OperationID != "" {
		details["operation_id"] = ar.OperationID
	}
	if ar.Scope != "" {
		details["scope"] = string(ar.Scope)
	}
	if reason != "" {
		details["reason"] = truncateString(reason, maxCommandInAudit)
	}
	return s.appendAudit(ctx, &model.AuditEntry{
		Actor:     by,
		AgentID:   ar.AgentID,
		SessionID: ar.SessionID,
		RunID:     ar.RunID,
		Action:    ActionApprovalDecide,
		Resource:  ar.ID,
		Outcome:   outcome,
		Details:   marshalDetails(details),
	})
}

func (s *Service) appendAudit(ctx context.Context, e *model.AuditEntry) error {
	e.ID = model.NewID()
	e.At = s.now()
	if err := s.deps.Audit.AppendAudit(ctx, e); err != nil {
		return model.Wrap(model.CodeInternal, "permission: 写入审计失败", err)
	}
	return nil
}

// publish 以 model.RunEvent 为载荷发布 approval.* 事件，与 OpenAPI 的 Event 约定一致。
func (s *Service) publish(t model.RunEventType, ar *model.ApprovalRequest) {
	cp := *ar
	if ar.OperationID != "" {
		s.deps.Bus.Publish(event.Event{Type: string(t), AgentID: ar.AgentID, SessionID: ar.SessionID, Payload: map[string]any{"type": t, "operation_id": ar.OperationID, "session_id": ar.SessionID, "agent_id": ar.AgentID, "at": s.now(), "approval": &cp}})
		return
	}
	s.deps.Bus.Publish(event.Event{
		Type:      string(t),
		AgentID:   ar.AgentID,
		SessionID: ar.SessionID,
		RunID:     ar.RunID,
		Payload: model.RunEvent{
			Type:      t,
			RunID:     ar.RunID,
			SessionID: ar.SessionID,
			AgentID:   ar.AgentID,
			At:        s.now(),
			Approval:  &cp,
		},
	})
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func actorOr(by, fallback string) string {
	if strings.TrimSpace(by) == "" {
		return fallback
	}
	return by
}

func codeForCtx(err error) model.Code {
	if errors.Is(err, context.DeadlineExceeded) {
		return model.CodeTimeout
	}
	return model.CodeCancelled
}
