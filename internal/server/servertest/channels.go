package servertest

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/GoGoslingCA/gogosling/internal/channel"
	"github.com/GoGoslingCA/gogosling/internal/gateway"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/server"
)

// 通道装配：记录、凭据、类型与配置校验全部走生产的 channel.Service（与组合根相同的 Options：
// 网关支持的类型来自 gateway.DefaultFactory，配置用 gateway.ParseChannelConfig 校验），
// 只有 IM 网关换成 FakeGateway——它不连接任何平台。这样 server / client / testdaemon 的测试
// 覆盖的是生产的通道规则（凭据引用名、长度上限、支持的类型、智能体归属），而不是另一套实现。

// FakeGateway 是 channel.Gateway 的测试替身：不启动任何适配器，只按记录的 enabled 模拟运行状态
// （启用 → running，停用或已删除 → 不在运行），并统计每个通道被重载的次数。并发安全。
type FakeGateway struct {
	repo model.ChannelRepo

	mu      sync.Mutex
	running map[string]bool
	reloads map[string]int
}

// NewFakeGateway 创建假网关；repo 用于在 Reload 时读取最新记录。
func NewFakeGateway(repo model.ChannelRepo) *FakeGateway {
	return &FakeGateway{repo: repo, running: map[string]bool{}, reloads: map[string]int{}}
}

var _ channel.Gateway = (*FakeGateway)(nil)

// Reload 实现 channel.Gateway：记录已删除时停止并返回 CodeNotFound（与生产网关一致）。
func (g *FakeGateway) Reload(ctx context.Context, channelID string) error {
	ch, err := g.repo.GetChannel(ctx, channelID)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reloads[channelID]++
	if err != nil {
		delete(g.running, channelID)
		return err
	}
	g.running[channelID] = ch.Enabled
	return nil
}

// ChannelStatus 实现 channel.Gateway。
func (g *FakeGateway) ChannelStatus(channelID string) (channel.RuntimeStatus, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.running[channelID] {
		return channel.RuntimeStatus{}, false
	}
	return channel.RuntimeStatus{Running: true, Connected: true}, true
}

// Reloads 返回某个通道被重载的次数（创建、修改与显式 reload 都会触发）。
func (g *FakeGateway) Reloads(channelID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reloads[channelID]
}

// newChannelService 用生产的 channel.Service 装配通道服务（只有网关是假的）。
func newChannelService(repo channel.Repo, secrets channel.Secrets, gw *FakeGateway, env *Env) (*channel.Service, error) {
	return channel.New(repo, secrets, channel.Options{
		Gateway: gw,
		Kinds:   gateway.DefaultFactory(nil),
		ValidateConfig: func(raw json.RawMessage) error {
			_, err := gateway.ParseChannelConfig(raw)
			return err
		},
		Logger: env.logger,
	})
}

// channelServer 让 *channel.Service 满足 server.ChannelService（字段逐一映射；
// 与组合根 internal/app 中的同名适配器等价——传输层不能导入组合根，只能在这里再写一份）。
type channelServer struct{ svc *channel.Service }

var _ server.ChannelService = channelServer{}

func (c channelServer) Create(ctx context.Context, in server.ChannelInput) (*model.Channel, error) {
	return c.svc.Create(ctx, channel.Input{
		AgentID: in.AgentID, Kind: in.Kind, Name: in.Name, Secret: in.Secret, Config: in.Config, Enabled: in.Enabled,
	})
}

func (c channelServer) Get(ctx context.Context, id string) (*model.Channel, error) {
	return c.svc.Get(ctx, id)
}

func (c channelServer) List(ctx context.Context, agentID string) ([]*model.Channel, error) {
	return c.svc.List(ctx, agentID)
}

func (c channelServer) Update(ctx context.Context, id string, in server.ChannelPatch) (*model.Channel, error) {
	return c.svc.Update(ctx, id, channel.Patch{Name: in.Name, Secret: in.Secret, Config: in.Config, Enabled: in.Enabled})
}

func (c channelServer) Delete(ctx context.Context, id string) error { return c.svc.Delete(ctx, id) }

func (c channelServer) Reload(ctx context.Context, id string) (*model.Channel, error) {
	return c.svc.Reload(ctx, id)
}

func (c channelServer) HasSecret(ctx context.Context, ch *model.Channel) bool {
	return c.svc.HasSecret(ctx, ch)
}

func (c channelServer) Status(id string) (string, string) { return c.svc.Status(id) }
