package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GoGoslingCA/gogosling/internal/client"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/version"
)

// dialClient 通过 daemon.json 发现守护进程并返回基于 internal/client 的 api。
// 找不到或连不上守护进程时返回 errDaemonNotRunning。
func dialClient(ctx context.Context, home, locale string) (api, error) {
	c, err := client.Discover(ctx, home, client.WithLocale(locale), client.WithUserAgent("gogosling-cli/"+version.Version))
	if err != nil {
		var me *model.Error
		if errors.As(err, &me) && me.Code == model.CodeUnavailable {
			return nil, errDaemonNotRunning
		}
		return nil, err
	}
	return clientAPI{c: c}, nil
}

// clientAPI 把 *client.Client 适配为 CLI 的 api 接口（指针 → 值、视图类型转换）。
type clientAPI struct {
	c *client.Client
}

var _ api = clientAPI{}

func (a clientAPI) Version(ctx context.Context) (versionInfo, error) {
	v, err := a.c.Version(ctx)
	if err != nil {
		return versionInfo{}, err
	}
	out := versionInfo{Version: v.Version, Commit: v.Commit, Date: v.Date, StartedAt: v.StartedAt}
	out.Worker.Status, out.Worker.Version, out.Worker.Python = v.Worker.Status, v.Worker.Version, v.Worker.Python
	return out, nil
}

func (a clientAPI) ListAgents(ctx context.Context) ([]model.Agent, error) {
	list, err := a.c.ListAgents(ctx)
	return derefAll(list), err
}

func (a clientAPI) GetAgent(ctx context.Context, id string) (model.Agent, error) {
	return deref(a.c.GetAgent(ctx, id))
}

func (a clientAPI) CreateAgent(ctx context.Context, in agentInput) (model.Agent, error) {
	return deref(a.c.CreateAgent(ctx, client.AgentCreate{
		Name: in.Name, Description: in.Description, SystemPrompt: in.SystemPrompt,
		Model: in.Model, Autonomy: in.Autonomy, Template: in.Template,
	}))
}

func (a clientAPI) DeleteAgent(ctx context.Context, id string) error { return a.c.DeleteAgent(ctx, id) }

func (a clientAPI) ListProviders(ctx context.Context) ([]providerView, error) {
	list, err := a.c.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]providerView, len(list))
	for i, p := range list {
		out[i] = toProviderView(p)
	}
	return out, nil
}

func (a clientAPI) CreateProvider(ctx context.Context, in providerInput) (providerView, error) {
	p, err := a.c.CreateProvider(ctx, client.ProviderCreate{
		Name: in.Name, Kind: in.Kind, BaseURL: in.BaseURL, APIKey: in.APIKey, DefaultModel: in.DefaultModel,
	})
	if err != nil {
		return providerView{}, err
	}
	return toProviderView(*p), nil
}

func toProviderView(p client.ProviderView) providerView {
	return providerView{
		ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, HasSecret: p.HasSecret,
		DefaultModel: p.DefaultModel, Models: p.Models, Enabled: p.Enabled, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (a clientAPI) TestProvider(ctx context.Context, id string) (providerTest, error) {
	r, err := a.c.TestProvider(ctx, id)
	if err != nil {
		return providerTest{}, err
	}
	return providerTest{OK: r.OK, Models: r.Models, LatencyMS: r.LatencyMS, Error: r.Error}, nil
}

func (a clientAPI) DeleteProvider(ctx context.Context, id string) error {
	return a.c.DeleteProvider(ctx, id)
}

func (a clientAPI) ListSessions(ctx context.Context, f sessionFilter) ([]sessionView, error) {
	list, err := a.c.ListSessions(ctx, client.SessionQuery{AgentID: f.AgentID, Status: model.SessionStatus(f.Status), Limit: f.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]sessionView, len(list))
	for i, s := range list {
		out[i] = sessionView{Session: s.Session, ActiveRunID: s.ActiveRunID}
	}
	return out, nil
}

func (a clientAPI) GetSession(ctx context.Context, id string) (sessionView, error) {
	return toSessionView(a.c.GetSession(ctx, id))
}

// CreateSession 以 origin=cli 创建会话：CLI 的会话键是 "<agent>:cli:local"，不会接管桌面会话的键。
func (a clientAPI) CreateSession(ctx context.Context, agentID, title string) (sessionView, error) {
	return toSessionView(a.c.CreateSessionWith(ctx, client.SessionCreate{AgentID: agentID, Title: title, Origin: model.OriginCLI}))
}

func (a clientAPI) ArchiveSession(ctx context.Context, id string) (sessionView, error) {
	return toSessionView(a.c.UpdateSession(ctx, id, client.SessionPatch{Status: client.Ptr(model.SessionArchived)}))
}

func toSessionView(s *client.SessionView, err error) (sessionView, error) {
	if err != nil {
		return sessionView{}, err
	}
	return sessionView{Session: s.Session, ActiveRunID: s.ActiveRunID}, nil
}

func (a clientAPI) DeleteSession(ctx context.Context, id string) error {
	return a.c.DeleteSession(ctx, id)
}

func (a clientAPI) SendMessage(ctx context.Context, sessionID, text string) (model.Run, error) {
	return deref(a.c.SendMessage(ctx, sessionID, text, nil))
}

func (a clientAPI) CancelRun(ctx context.Context, runID string) (model.Run, error) {
	return deref(a.c.CancelRun(ctx, runID))
}

func (a clientAPI) GetRun(ctx context.Context, runID string) (model.Run, error) {
	return deref(a.c.GetRun(ctx, runID))
}

// Events 订阅事件流并把载荷保留为原始 JSON；不可恢复的错误让通道关闭。
func (a clientAPI) Events(ctx context.Context, sessionID string) (<-chan streamEvent, error) {
	evs, errs := a.c.Events(ctx, 0, sessionID)
	out := make(chan streamEvent, 64)
	go func() {
		defer close(out)
		for {
			select {
			case ev, ok := <-evs:
				if !ok {
					return
				}
				se := toStreamEvent(ev)
				select {
				case out <- se:
				case <-ctx.Done():
					return
				}
			case _, ok := <-errs:
				if !ok {
					errs = nil
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// toStreamEvent 把总线事件转成载荷为原始 JSON 的事件。
func toStreamEvent(ev event.Event) streamEvent {
	se := streamEvent{Seq: ev.Seq, Type: ev.Type, At: ev.At, AgentID: ev.AgentID, SessionID: ev.SessionID, RunID: ev.RunID}
	switch p := ev.Payload.(type) {
	case nil:
	case json.RawMessage:
		se.Payload = p
	case []byte:
		se.Payload = p
	default:
		if raw, err := json.Marshal(p); err == nil {
			se.Payload = raw
		}
	}
	return se
}

func (a clientAPI) ListApprovals(ctx context.Context, status string) ([]model.ApprovalRequest, error) {
	list, err := a.c.ListApprovals(ctx, model.ApprovalStatus(status), 0, 0)
	return derefAll(list), err
}

func (a clientAPI) DecideApproval(ctx context.Context, id string, d model.Decision) (model.ApprovalRequest, error) {
	return deref(a.c.DecideApproval(ctx, id, d))
}

func (a clientAPI) ListJobs(ctx context.Context, agentID string) ([]model.Job, error) {
	list, err := a.c.ListJobs(ctx, agentID)
	return derefAll(list), err
}

func (a clientAPI) CreateJob(ctx context.Context, in jobInput) (model.Job, error) {
	return deref(a.c.CreateJob(ctx, client.JobCreate{
		AgentID: in.AgentID, Name: in.Name, Schedule: in.Schedule, Timezone: in.Timezone, Kind: in.Kind,
		Input: in.Input, SessionKey: in.SessionKey, MissedPolicy: in.MissedPolicy, Enabled: in.Enabled,
	}))
}

func (a clientAPI) DeleteJob(ctx context.Context, id string) error { return a.c.DeleteJob(ctx, id) }

func (a clientAPI) RunJob(ctx context.Context, id string) error { return a.c.RunJob(ctx, id) }

func (a clientAPI) ListExtensions(ctx context.Context, kind string) ([]model.Extension, error) {
	list, err := a.c.ListExtensions(ctx, model.ExtensionKind(kind))
	return derefAll(list), err
}

func (a clientAPI) InstallExtension(ctx context.Context, path string) (model.Extension, error) {
	return deref(a.c.InstallExtension(ctx, path))
}

func (a clientAPI) RegisterMCP(ctx context.Context, name string, cfg model.MCPServerConfig) (model.Extension, error) {
	return deref(a.c.RegisterMCP(ctx, name, cfg))
}

func (a clientAPI) SetExtensionEnabled(ctx context.Context, id string, enabled bool) (model.Extension, error) {
	return deref(a.c.SetExtensionEnabled(ctx, id, enabled))
}

func (a clientAPI) DeleteExtension(ctx context.Context, id string) error {
	return a.c.UninstallExtension(ctx, id)
}

func (a clientAPI) ExtensionStatus(ctx context.Context, id string) (extensionStatus, error) {
	return toExtensionStatus(a.c.ExtensionStatus(ctx, id))
}

func (a clientAPI) AcceptExtensionTools(ctx context.Context, id string) (extensionStatus, error) {
	return toExtensionStatus(a.c.AcceptExtensionTools(ctx, id))
}

func toExtensionStatus(s *client.ExtensionStatus, err error) (extensionStatus, error) {
	if err != nil {
		return extensionStatus{}, err
	}
	return extensionStatus{ID: s.ID, Connected: s.Connected, ToolCount: s.ToolCount, ToolsChanged: s.ToolsChanged, LastError: s.LastError}, nil
}

func (a clientAPI) ListChannels(ctx context.Context, agentID string) ([]channelView, error) {
	list, err := a.c.ListChannels(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]channelView, len(list))
	for i, ch := range list {
		out[i] = toChannelView(ch)
	}
	return out, nil
}

func (a clientAPI) CreateChannel(ctx context.Context, in channelInput) (channelView, error) {
	ch, err := a.c.CreateChannel(ctx, client.ChannelCreate{
		AgentID: in.AgentID, Kind: in.Kind, Name: in.Name, Secret: in.Secret, Config: in.Config, Enabled: in.Enabled,
	})
	if err != nil {
		return channelView{}, err
	}
	return toChannelView(*ch), nil
}

func (a clientAPI) DeleteChannel(ctx context.Context, id string) error {
	return a.c.DeleteChannel(ctx, id)
}

func (a clientAPI) ReloadChannel(ctx context.Context, id string) (channelView, error) {
	ch, err := a.c.ReloadChannel(ctx, id)
	if err != nil {
		return channelView{}, err
	}
	return toChannelView(*ch), nil
}

func toChannelView(ch client.ChannelView) channelView {
	return channelView{
		Channel: model.Channel{
			ID: ch.ID, AgentID: ch.AgentID, Kind: ch.Kind, Name: ch.Name, Config: ch.Config,
			Enabled: ch.Enabled, LastError: ch.LastError, CreatedAt: ch.CreatedAt, UpdatedAt: ch.UpdatedAt,
		},
		HasSecret: ch.HasSecret,
		Status:    ch.Status,
	}
}

func (a clientAPI) GetSettings(ctx context.Context) (map[string]any, error) {
	s, err := a.c.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	return settingsMap(*s), nil
}

// UpdateSettings 支持 timezone、locale、allowed_origins 三个键；其他键返回 invalid。
func (a clientAPI) UpdateSettings(ctx context.Context, patch map[string]any) (map[string]any, error) {
	var in client.SettingsPatch
	for k, v := range patch {
		switch k {
		case "timezone", "locale":
			s, ok := v.(string)
			if !ok {
				return nil, model.Errorf(model.CodeInvalid, "%s 必须是字符串", k)
			}
			if k == "timezone" {
				in.Timezone = &s
			} else {
				in.Locale = &s
			}
		case "allowed_origins":
			list, err := toStrings(v)
			if err != nil {
				return nil, model.Errorf(model.CodeInvalid, "allowed_origins 必须是字符串列表")
			}
			in.AllowedOrigins = &list
		default:
			return nil, model.Errorf(model.CodeInvalid, "未知的设置项 %s", k)
		}
	}
	s, err := a.c.UpdateSettings(ctx, in)
	if err != nil {
		return nil, err
	}
	return settingsMap(*s), nil
}

func settingsMap(s client.Settings) map[string]any {
	origins := make([]any, len(s.AllowedOrigins))
	for i, o := range s.AllowedOrigins {
		origins[i] = o
	}
	return map[string]any{"timezone": s.Timezone, "locale": s.Locale, "allowed_origins": origins}
}

func toStrings(v any) ([]string, error) {
	switch x := v.(type) {
	case []string:
		return x, nil
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, errors.New("not a string")
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, errors.New("not a list")
	}
}

// deref 把 (*T, error) 转成 (T, error)。
func deref[T any](p *T, err error) (T, error) {
	var zero T
	if err != nil || p == nil {
		return zero, err
	}
	return *p, nil
}

// derefAll 把 []*T 转成 []T。
func derefAll[T any](list []*T) []T {
	out := make([]T, 0, len(list))
	for _, p := range list {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}
