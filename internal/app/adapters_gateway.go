package app

import (
	"context"
	"encoding/json"

	"github.com/GoGoslingCA/gogosling/internal/channel"
	"github.com/GoGoslingCA/gogosling/internal/gateway"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

// gatewaySessions 是网关需要的会话能力（*session.Service 满足）。
type gatewaySessions interface {
	EnsureByKey(ctx context.Context, agentID, key string, origin model.Origin) (*model.Session, error)
	Send(ctx context.Context, req session.SendRequest) (*model.Run, error)
}

// senderAdapter 实现 gateway.Sender：两个包各自定义了同形的 SendRequest，这里逐字段转换。
type senderAdapter struct {
	s gatewaySessions
}

func (a senderAdapter) EnsureByKey(ctx context.Context, agentID, key string, origin model.Origin) (*model.Session, error) {
	return a.s.EnsureByKey(ctx, agentID, key, origin)
}

func (a senderAdapter) Send(ctx context.Context, r gateway.SendRequest) (*model.Run, error) {
	return a.s.Send(ctx, toSessionSend(r))
}

// toSessionSend 是 gateway.SendRequest → session.SendRequest 的字段映射。
func toSessionSend(r gateway.SendRequest) session.SendRequest {
	return session.SendRequest{
		SessionID:   r.SessionID,
		Text:        r.Text,
		Attachments: r.Attachments,
		Trigger:     r.Trigger,
		Principal:   r.Principal,
		Untrusted:   r.Untrusted,
	}
}

// gatewayTexts 按语言填充网关发回 IM 的提示文案（键 gateway.*），以及运行失败原因的本地化映射
// （RunErrorMap，来自 i18n 的 run.error.*，覆盖 session 与 engine 写入 Run.Error 的全部机器常量）。
// 占位符 {error} 等由网关自己替换，这里不传参数，i18n 会原样保留占位符。
func gatewayTexts(locale string) gateway.Texts {
	return gateway.Texts{
		RunErrorMap:      i18n.RunErrorTexts(locale),
		Busy:             i18n.T(locale, "gateway.busy", nil),
		Unavailable:      i18n.T(locale, "gateway.unavailable", nil),
		ApprovalRequired: i18n.T(locale, "gateway.approval_required", nil),
		RunFailed:        i18n.T(locale, "gateway.run_failed", nil),
		RunCancelled:     i18n.T(locale, "gateway.run_cancelled", nil),
		EmptyReply:       i18n.T(locale, "gateway.empty_reply", nil),
	}
}

// gatewayRuntime 是 *gateway.Gateway 中通道服务用到的部分。
type gatewayRuntime interface {
	Reload(ctx context.Context, channelID string) error
	Status(channelID string) (gateway.ChannelStatus, bool)
}

// channelGateway 实现 channel.Gateway。
type channelGateway struct {
	g gatewayRuntime
}

func (c channelGateway) Reload(ctx context.Context, channelID string) error {
	return c.g.Reload(ctx, channelID)
}

func (c channelGateway) ChannelStatus(channelID string) (channel.RuntimeStatus, bool) {
	st, ok := c.g.Status(channelID)
	if !ok {
		return channel.RuntimeStatus{}, false
	}
	return channel.RuntimeStatus{Running: st.Running, Connected: st.Connected, LastError: st.LastError}, true
}

// validateChannelConfig 在保存前用网关的解析规则校验通道配置。
func validateChannelConfig(raw json.RawMessage) error {
	if _, err := gateway.ParseChannelConfig(raw); err != nil {
		return err
	}
	return nil
}

// channelServer 让 *channel.Service 满足 server.ChannelService。
//
// channel 包按依赖规则不能导入 server（领域包不得依赖传输层），所以契约里的
// ChannelInput / ChannelPatch 在这里转换成 channel 包自己的同形类型。
type channelServer struct {
	svc *channel.Service
}

var _ server.ChannelService = channelServer{}

func (c channelServer) Create(ctx context.Context, in server.ChannelInput) (*model.Channel, error) {
	return c.svc.Create(ctx, toChannelInput(in))
}

func (c channelServer) Get(ctx context.Context, id string) (*model.Channel, error) {
	return c.svc.Get(ctx, id)
}

func (c channelServer) List(ctx context.Context, agentID string) ([]*model.Channel, error) {
	return c.svc.List(ctx, agentID)
}

func (c channelServer) Update(ctx context.Context, id string, in server.ChannelPatch) (*model.Channel, error) {
	return c.svc.Update(ctx, id, toChannelPatch(in))
}

func (c channelServer) Delete(ctx context.Context, id string) error { return c.svc.Delete(ctx, id) }

func (c channelServer) Reload(ctx context.Context, id string) (*model.Channel, error) {
	return c.svc.Reload(ctx, id)
}

func (c channelServer) HasSecret(ctx context.Context, ch *model.Channel) bool {
	return c.svc.HasSecret(ctx, ch)
}

func (c channelServer) Status(id string) (string, string) { return c.svc.Status(id) }

// toChannelInput 是 server.ChannelInput → channel.Input 的字段映射。
func toChannelInput(in server.ChannelInput) channel.Input {
	return channel.Input{
		AgentID: in.AgentID,
		Kind:    in.Kind,
		Name:    in.Name,
		Secret:  in.Secret,
		Config:  in.Config,
		Enabled: in.Enabled,
	}
}

// toChannelPatch 是 server.ChannelPatch → channel.Patch 的字段映射。
func toChannelPatch(in server.ChannelPatch) channel.Patch {
	return channel.Patch{Name: in.Name, Secret: in.Secret, Config: in.Config, Enabled: in.Enabled}
}
