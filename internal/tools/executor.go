package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// Deps 是 Executor 的依赖，由组合根注入。
type Deps struct {
	Registry *Registry
	Resolver RunContextResolver
	// OperationResolver 和 UserOperationTool 仅一起启用，不进入模型工具清单。
	OperationResolver OperationContextResolver
	UserOperationTool Tool
	Permissions       PermissionChecker
	// External 提供扩展工具；可为 nil。
	External ExternalTools
	Logger   *slog.Logger
	// MaxResultBytes 是回填给模型的结果上限；零值为 DefaultMaxResultBytes。
	MaxResultBytes int
}

// Executor 执行工作进程发来的工具调用（host.tool.call）。可以并发使用。
type Executor struct {
	registry          *Registry
	resolver          RunContextResolver
	operationResolver OperationContextResolver
	userOperationTool Tool
	permissions       PermissionChecker
	external          ExternalTools
	logger            *slog.Logger
	maxBytes          int
}

// NewExecutor 创建执行器；Registry、Resolver 与 Permissions 不能为空。
func NewExecutor(d Deps) (*Executor, error) {
	if d.Registry == nil || d.Resolver == nil || d.Permissions == nil {
		return nil, model.NewError(model.CodeInvalid, "tools: Registry、Resolver 与 Permissions 都不能为空")
	}
	if (d.OperationResolver == nil) != (d.UserOperationTool == nil) {
		return nil, operationError(model.CodeInvalid, "invalid_context")
	}
	if d.UserOperationTool != nil {
		spec := d.UserOperationTool.Spec()
		if spec.Name != UserFileChangeTool || spec.Source != SourceBuiltin || (spec.Risk != model.RiskWrite && spec.Risk != model.RiskDestructive) {
			return nil, operationError(model.CodeInvalid, "unsupported_tool")
		}
		if err := ValidateInputSchema(spec.InputSchema); err != nil {
			return nil, err
		}
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	max := d.MaxResultBytes
	if max <= 0 {
		max = DefaultMaxResultBytes
	}
	return &Executor{
		registry:          d.Registry,
		resolver:          d.Resolver,
		operationResolver: d.OperationResolver,
		userOperationTool: d.UserOperationTool,
		permissions:       d.Permissions,
		external:          d.External,
		logger:            logger,
		maxBytes:          max,
	}, nil
}

// HasBuiltinTool 只查询宿主登记的内置工具及静态依赖，不发现扩展、连接服务或执行工具。
// 该能力不代表某个智能体获准使用工具；执行时仍按智能体工具列表及权限规则检查。
func (e *Executor) HasBuiltinTool(name string) bool {
	if e == nil || e.registry == nil || name == UserFileChangeTool {
		return false
	}
	tool, ok := e.registry.Get(name)
	if !ok {
		return false
	}
	if availability, ok := tool.(interface{ staticAvailable() bool }); ok {
		return availability.staticAvailable()
	}
	return true
}

// ToolsFor 返回该智能体当前可用的工具声明：智能体启用的内置工具 + 扩展工具。
//
// 这是工具清单的唯一出处：交给工作进程的清单与 GET /agents/{id}/tools 都用它，因此两者一致。
//   - 实现了 Conditional 的工具（load_skill：智能体没有技能时）在不可用时不声明；
//   - 参数 Schema 不合格（ValidateInputSchema）的扩展工具被跳过；
//   - 与内置工具重名的扩展工具被跳过（内置优先）。
func (e *Executor) ToolsFor(ctx context.Context, agent *model.Agent) ([]model.ToolSpec, error) {
	builtins := e.registry.Enabled(agent)
	specs := make([]model.ToolSpec, 0, len(builtins))
	for _, t := range builtins {
		if t.Spec().Name == UserFileChangeTool {
			continue
		}
		ok, err := e.availableFor(ctx, t, agent)
		if err != nil {
			return nil, err
		}
		if ok {
			specs = append(specs, t.Spec())
		}
	}
	if e.external == nil {
		return specs, nil
	}
	ext, err := e.external.ToolsFor(ctx, agent)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(builtins)+len(ext))
	for _, t := range builtins {
		// 按启用的内置工具去重（含暂不可用的 load_skill）：执行时 Registry.Lookup 优先，同名扩展工具调不到。
		seen[t.Spec().Name] = true
	}
	for _, s := range ext {
		if s.Name == UserFileChangeTool {
			continue
		}
		if seen[s.Name] {
			e.logger.Warn("扩展工具与内置工具重名，已跳过", "tool", s.Name, "source", string(s.Source))
			continue
		}
		if !e.validExternal(s) {
			continue
		}
		seen[s.Name] = true
		specs = append(specs, s)
	}
	return specs, nil
}

// availableFor 询问实现了 Conditional 的工具对该智能体是否可用；查询失败时不声明该工具
// （ctx 已结束时返回错误）。
func (e *Executor) availableFor(ctx context.Context, t Tool, agent *model.Agent) (bool, error) {
	c, ok := t.(Conditional)
	if !ok {
		return true, nil
	}
	avail, err := c.AvailableFor(ctx, agent)
	if err != nil {
		if cerr := ctxErr(ctx); cerr != nil {
			return false, cerr
		}
		agentID := ""
		if agent != nil {
			agentID = agent.ID
		}
		e.logger.Warn("判断工具是否可用失败，本次不提供该工具", "tool", t.Spec().Name, "agent_id", agentID, "error", err)
		return false, nil
	}
	return avail, nil
}

// validExternal 用与内置工具相同的规则校验扩展工具的参数 Schema；不合格的记录日志并跳过。
func (e *Executor) validExternal(s model.ToolSpec) bool {
	if err := ValidateInputSchema(s.InputSchema); err != nil {
		e.logger.Warn("扩展工具的参数 Schema 不合格，已跳过", "tool", s.Name, "source", string(s.Source), "error", err)
		return false
	}
	return true
}

// Execute 执行一次工具调用并返回回填给模型的结果。
//
// 工具不存在（CodeNotFound）、参数非法（CodeInvalid）、权限拒绝（CodeDenied）以及工具自身的失败
// 都以 IsError 的结果返回，内容是给模型看的说明；只有 ctx 结束或无法解析运行上下文时返回 error。
func (e *Executor) Execute(ctx context.Context, runID string, call model.ToolCall) (model.ToolResult, error) {
	if err := ctxErr(ctx); err != nil {
		return model.ToolResult{}, err
	}
	if strings.TrimSpace(call.ID) == "" {
		return model.ToolResult{}, model.NewError(model.CodeInvalid, "工具调用缺少 ID")
	}
	rc, err := e.resolver.ResolveRunContext(ctx, runID)
	if err != nil {
		return model.ToolResult{}, err
	}
	if rc.OperationID != "" {
		return model.ToolResult{}, operationError(model.CodeInvalid, "invalid_context")
	}
	if rc.Agent == nil || rc.Workspace == nil {
		return model.ToolResult{}, model.NewError(model.CodeInvalid, "运行上下文缺少智能体或工作区")
	}
	if rc.RunID == "" {
		rc.RunID = runID
	}

	start := time.Now()
	res, err := e.execute(ctx, rc, call)
	if err != nil {
		e.logger.Warn("工具调用中止", "run_id", runID, "tool", call.Name, "call_id", call.ID, "error", err)
		return model.ToolResult{}, err
	}
	res.CallID = call.ID
	content, clipped := clipResult(res.Content, e.maxBytes)
	res.Content = content
	// 工具自身截断（run_command 的输出流、fetch_url 的正文）或这里的整体截断，都要让界面知道。
	res.Truncated = res.Truncated || clipped
	e.logger.Info("工具调用完成", "run_id", runID, "tool", call.Name, "call_id", call.ID,
		"is_error", res.IsError, "bytes", len(res.Content), "duration_ms", time.Since(start).Milliseconds())
	return res, nil
}

// execute 是 Execute 去掉外围日志与截断后的主体。
func (e *Executor) execute(ctx context.Context, rc RunContext, call model.ToolCall) (model.ToolResult, error) {
	name := strings.TrimSpace(call.Name)
	args := bytes.TrimSpace(call.Arguments)
	if len(args) == 0 || bytes.Equal(args, []byte("null")) {
		args = []byte("{}")
	}
	if !json.Valid(args) || args[0] != '{' {
		return executionError(rc, model.NewError(model.CodeInvalid, "参数必须是 JSON 对象"))
	}
	call.Arguments = args

	tool, spec, found, err := e.find(ctx, rc, name)
	if err != nil {
		return model.ToolResult{}, err
	}
	if !found {
		return executionError(rc, model.Errorf(model.CodeNotFound, "工具 %q 不存在或未对本智能体启用", name))
	}

	var desc Description
	if rc.OperationID != "" {
		desc = Description{Summary: rc.OperationSummary}
		err = json.Unmarshal(rc.OperationDetails, &desc.Details)
	} else {
		desc, err = describe(tool, name, args)
	}
	if err != nil {
		return executionError(rc, err)
	}
	var details json.RawMessage
	if len(desc.Details) > 0 {
		details, _ = json.Marshal(desc.Details)
	}

	verdict, err := e.permissions.Check(ctx, CheckRequest{
		Principal:      rc.Principal,
		AgentID:        rc.Agent.ID,
		Autonomy:       rc.Agent.Autonomy,
		SessionID:      rc.SessionID,
		RunID:          rc.RunID,
		OperationID:    rc.OperationID,
		Tool:           spec,
		Call:           call,
		Workspace:      rc.Workspace.Root(),
		Summary:        desc.Summary,
		Details:        details,
		UntrustedInput: rc.Untrusted,
		Isolated:       isIsolated(tool),
	})
	if err != nil {
		if rc.OperationID != "" {
			return model.ToolResult{}, err
		}
		if ctxErr(ctx) != nil || model.IsCode(err, model.CodeCancelled) {
			return model.ToolResult{}, err
		}
		if model.IsCode(err, model.CodeDenied) {
			return deniedResult(messageOf(err)), nil
		}
		e.logger.Error("权限判定失败", "run_id", rc.RunID, "tool", name, "error", err)
		return errorResult(model.NewError(model.CodeInternal, "权限判定失败，本次调用未执行")), nil
	}
	if !verdict.Allowed {
		if rc.OperationID != "" {
			return model.ToolResult{}, operationError(model.CodeDenied, "denied")
		}
		return deniedResult(verdict.Reason), nil
	}

	var res model.ToolResult
	if tool != nil {
		res, err = tool.Execute(ctx, rc, args)
	} else {
		res, err = e.external.CallTool(ctx, rc.Agent, call)
	}
	if err != nil {
		if rc.OperationID != "" {
			return model.ToolResult{}, err
		}
		if ctxErr(ctx) != nil || model.IsCode(err, model.CodeCancelled) {
			return model.ToolResult{}, err
		}
		e.logger.Warn("工具执行失败", "run_id", rc.RunID, "tool", name, "error", err)
		return errorResult(err), nil
	}
	return res, nil
}

// find 先在内置工具里找，再问扩展；返回 (内置工具或 nil, 声明, 是否找到, 基础设施错误)。
func (e *Executor) find(ctx context.Context, rc RunContext, name string) (Tool, model.ToolSpec, bool, error) {
	if rc.OperationID != "" {
		if name != UserFileChangeTool || e.userOperationTool == nil {
			return nil, model.ToolSpec{}, false, operationError(model.CodeDenied, "unsupported_tool")
		}
		spec := e.userOperationTool.Spec()
		spec.Risk = rc.OperationRisk
		return e.userOperationTool, spec, true, nil
	}
	if name == UserFileChangeTool {
		return nil, model.ToolSpec{}, false, nil
	}
	if t, ok := e.registry.Lookup(rc.Agent, name); ok {
		return t, t.Spec(), true, nil
	}
	if e.external == nil {
		return nil, model.ToolSpec{}, false, nil
	}
	specs, err := e.external.ToolsFor(ctx, rc.Agent)
	if err != nil {
		if ctxErr(ctx) != nil {
			return nil, model.ToolSpec{}, false, err
		}
		e.logger.Warn("获取扩展工具列表失败", "run_id", rc.RunID, "error", err)
		return nil, model.ToolSpec{}, false, nil
	}
	for _, s := range specs {
		if s.Name == name {
			if !e.validExternal(s) {
				// 与 ToolsFor 一致：Schema 不合格的扩展工具没有声明给模型，也不可调用。
				return nil, model.ToolSpec{}, false, nil
			}
			return nil, s, true, nil
		}
	}
	return nil, model.ToolSpec{}, false, nil
}

// describe 生成摘要与细节：工具实现了 Describer 就用它，否则给出通用摘要。
func describe(tool Tool, name string, args json.RawMessage) (Description, error) {
	if d, ok := tool.(Describer); ok && tool != nil {
		desc, err := d.Describe(args)
		if err != nil {
			return Description{}, err
		}
		if desc.Summary == "" {
			desc.Summary = "调用工具 " + name
		}
		return desc, nil
	}
	return Description{Summary: "调用工具 " + name}, nil
}

// errorResult 把错误转成给模型看的失败结果；只使用错误码与消息，不暴露底层错误链。
func errorResult(err error) model.ToolResult {
	return model.ToolResult{Content: "错误（" + string(model.CodeOf(err)) + "）：" + messageOf(err), IsError: true}
}

// deniedResult 把权限拒绝转成结果。
func deniedResult(reason string) model.ToolResult {
	msg := "操作被拒绝"
	if strings.TrimSpace(reason) != "" {
		msg += "：" + strings.TrimSpace(reason)
	}
	msg += "。请不要重复尝试同一操作；可以换一种不需要该权限的方式，或向用户说明情况。"
	return model.ToolResult{Content: "错误（" + string(model.CodeDenied) + "）：" + msg, IsError: true}
}

// messageOf 返回 *model.Error 的 Message；其他错误给出通用说明，避免把内部细节交给模型。
func messageOf(err error) string {
	var me *model.Error
	if errors.As(err, &me) {
		return me.Message
	}
	return "工具执行失败"
}

// ctxErr 把已经结束的 ctx 转成带错误码的错误。
func ctxErr(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return model.Wrap(model.CodeTimeout, "操作未完成", err)
	}
	return model.Wrap(model.CodeCancelled, "操作被取消", err)
}
