package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/channel"
	"github.com/GoGoslingCA/gogosling/internal/gateway"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/secret"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/store"
)

// recordingReloader 是假网关：记录重载并报告固定状态。
type recordingReloader struct {
	mu      sync.Mutex
	reloads []string
}

func (r *recordingReloader) Reload(_ context.Context, id string) error {
	r.mu.Lock()
	r.reloads = append(r.reloads, id)
	r.mu.Unlock()
	return nil
}

func (r *recordingReloader) ChannelStatus(string) (channel.RuntimeStatus, bool) {
	return channel.RuntimeStatus{}, false
}

// TestChannelServiceWithRealStoreAndFileSecrets 用真实 SQLite 仓储与文件凭据存储，经 server.ChannelService
// 适配器驱动通道服务：凭据只进 secret 存储，数据库与返回值只有引用；删除通道时凭据一并删除。
func TestChannelServiceWithRealStoreAndFileSecrets(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(home, DatabaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files, err := secret.OpenWith(ctx, home, nil, secret.Options{DisableKeyring: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	agent := &model.Agent{ID: model.NewID(), Name: "a", Autonomy: model.AutonomyAskRisky, WorkspaceDir: "a", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	reloader := &recordingReloader{}
	svc, err := channel.New(st, secretAdapter{s: files}, channel.Options{
		Gateway: reloader, Kinds: gateway.DefaultFactory(discardLogger()), ValidateConfig: validateChannelConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	var cs server.ChannelService = channelServer{svc: svc}

	const token = "123456:TELEGRAM-TOKEN-VALUE"
	enabled := true
	ch, err := cs.Create(ctx, server.ChannelInput{
		AgentID: agent.ID, Kind: model.ChannelTelegram, Name: "电报", Secret: token,
		Config: []byte(`{"allowed_peers":["42"],"group_mode":"mention"}`), Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if ch.SecretRef != channel.SecretRef(ch.ID) || !cs.HasSecret(ctx, ch) {
		t.Fatalf("记录应只保存凭据引用，得到 %+v", ch)
	}
	if v, err := files.Get(ctx, ch.SecretRef); err != nil || v != token {
		t.Fatalf("凭据应写入文件存储，得到 %q / %v", v, err)
	}
	stored, err := st.GetChannel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(stored); strings.Contains(string(raw), token) {
		t.Fatal("数据库记录不应包含凭据")
	}
	if db, _ := os.ReadFile(filepath.Join(home, DatabaseFileName)); strings.Contains(string(db), token) {
		t.Fatal("数据库文件不应包含凭据")
	}
	if len(reloader.reloads) != 1 {
		t.Fatalf("创建后应重载一次，得到 %v", reloader.reloads)
	}

	// 非法配置在写库前被网关的解析规则拒绝。
	if _, err := cs.Update(ctx, ch.ID, server.ChannelPatch{Config: []byte(`{"group_mode":"loud"}`)}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("非法 group_mode 应 invalid，得到 %v", err)
	}
	// 轮换凭据。
	rotated := "123456:ROTATED"
	if _, err := cs.Update(ctx, ch.ID, server.ChannelPatch{Secret: &rotated}); err != nil {
		t.Fatal(err)
	}
	if v, _ := files.Get(ctx, ch.SecretRef); v != rotated {
		t.Fatalf("凭据应已轮换，得到 %q", v)
	}
	if state, _ := cs.Status(ch.ID); state != channel.StateStopped {
		t.Fatalf("网关未报告运行时应为 stopped，得到 %s", state)
	}

	list, err := cs.List(ctx, agent.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("List 应返回一条，得到 %d / %v", len(list), err)
	}
	if err := cs.Delete(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Get(ctx, ch.SecretRef); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("删除通道应删除凭据，得到 %v", err)
	}
	if _, err := cs.Get(ctx, ch.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("删除后应 not_found，得到 %v", err)
	}
	// 外键：智能体不存在时为 not_found（而不是数据库的外键冲突）。
	if _, err := cs.Create(ctx, server.ChannelInput{AgentID: model.NewID(), Kind: model.ChannelConsole, Name: "x"}); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("智能体不存在应 not_found，得到 %v", err)
	}
}
