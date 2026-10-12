package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/channel"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/server"
)

// 本文件覆盖与 OpenAPI 契约对齐的修复：system/shutdown、Settings.effective_*、POST /sessions 的 origin、
// PATCH /sessions 的空请求体、消息分页上限、事件流 hello 帧与通道服务的生产规则。

func TestShutdownEndpoint(t *testing.T) {
	// 没有提供 Options.Shutdown：unavailable。
	h := newHarness(t)
	mustError(t, h.do(http.MethodPost, "/api/v1/system/shutdown", nil), http.StatusServiceUnavailable, model.CodeUnavailable)

	var calls atomic.Int32
	h = newHarness(t, func(c *harnessConfig) { c.srv.Shutdown = func() { calls.Add(1) } })
	// 未鉴权：401，且不触发关闭。
	mustError(t, h.do(http.MethodPost, "/api/v1/system/shutdown", nil, withoutAuth()), http.StatusUnauthorized, model.CodeUnauthorized)
	if calls.Load() != 0 {
		t.Fatal("未鉴权的请求不应触发关闭")
	}
	// GET 不是关闭：405。
	mustStatus(t, h.do(http.MethodGet, "/api/v1/system/shutdown", nil), http.StatusMethodNotAllowed)

	for i := 0; i < 2; i++ {
		rec := h.do(http.MethodPost, "/api/v1/system/shutdown", nil)
		mustStatus(t, rec, http.StatusAccepted)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("重复的关闭请求只应触发一次 Options.Shutdown，实际 %d 次", got)
	}
}

func TestSettingsEffectiveValuesFollowOptions(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) {
		c.srv.DefaultLocale = "zh"
		c.srv.DefaultTimezone = "Asia/Shanghai"
	})
	type view struct {
		Timezone          *string `json:"timezone"`
		Locale            *string `json:"locale"`
		EffectiveLocale   string  `json:"effective_locale"`
		EffectiveTimezone string  `json:"effective_timezone"`
	}
	get := func() view {
		rec := h.do(http.MethodGet, "/api/v1/settings", nil)
		mustStatus(t, rec, http.StatusOK)
		return decodeBody[view](t, rec)
	}

	v := get()
	if v.Locale != nil || v.Timezone != nil {
		t.Fatalf("未保存的设置应省略 locale / timezone，实际 %+v", v)
	}
	if v.EffectiveLocale != "zh" || v.EffectiveTimezone != "Asia/Shanghai" {
		t.Fatalf("effective_* 应取 Options.DefaultLocale / DefaultTimezone，实际 %+v", v)
	}

	mustStatus(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"locale": "en", "timezone": "Europe/Berlin"}), http.StatusOK)
	v = get()
	if v.Locale == nil || *v.Locale != "en" || v.EffectiveLocale != "en" || v.EffectiveTimezone != "Europe/Berlin" {
		t.Fatalf("保存后 locale / effective_* 应取设置项，实际 %+v", v)
	}

	// 清除设置项：字段再次省略，effective_* 回到 Options。
	mustStatus(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"locale": "", "timezone": ""}), http.StatusOK)
	v = get()
	if v.Locale != nil || v.Timezone != nil || v.EffectiveLocale != "zh" || v.EffectiveTimezone != "Asia/Shanghai" {
		t.Fatalf("清除后应省略设置并回到 Options 的有效值，实际 %+v", v)
	}
}

func TestCreateSessionOrigin(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("来源")

	rec := h.do(http.MethodPost, "/api/v1/sessions", map[string]any{"agent_id": a.ID, "origin": "cli"})
	mustStatus(t, rec, http.StatusCreated)
	cli := decodeBody[sessionView](t, rec)
	if cli.Origin != model.OriginCLI || !strings.Contains(cli.Key, ":cli:") {
		t.Fatalf("origin=cli 应创建 CLI 会话，实际 %+v", cli.Session)
	}
	desk := h.seedSession(a.ID)
	if desk.Origin != model.OriginDesktop || desk.Key == cli.Key {
		t.Fatalf("缺省 origin 应为 desktop 且与 CLI 会话的键互不干扰，实际 %+v / %+v", desk.Session, cli.Session)
	}
	for _, bad := range []string{"channel", "job", "acp", "web"} {
		env := mustError(t, h.do(http.MethodPost, "/api/v1/sessions", map[string]any{"agent_id": a.ID, "origin": bad}),
			http.StatusBadRequest, model.CodeInvalid)
		if env.Error.Details["field"] != "origin" {
			t.Errorf("origin=%s 的错误应指出字段 origin，实际 %v", bad, env.Error.Details)
		}
	}
}

func TestPatchSessionWithoutBodyIsNoop(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("空体")
	sess := h.seedSession(a.ID)

	// 没有请求体（OpenAPI 中 requestBody 非必填）与 {} 都视为不修改，返回当前会话。
	for _, body := range []any{nil, "", "{}"} {
		rec := h.do(http.MethodPatch, "/api/v1/sessions/"+sess.ID, body)
		mustStatus(t, rec, http.StatusOK)
		got := decodeBody[sessionView](t, rec)
		if got.ID != sess.ID || got.Title != sess.Title || got.Status != sess.Status {
			t.Fatalf("空请求体 %q 应原样返回会话，实际 %+v", body, got.Session)
		}
	}
	mustError(t, h.do(http.MethodPatch, "/api/v1/sessions/01ARZ3NDEKTSV4RRFFQ69G5FAV", nil), http.StatusNotFound, model.CodeNotFound)
	// 非法 JSON 仍是 invalid。
	mustError(t, h.do(http.MethodPatch, "/api/v1/sessions/"+sess.ID, `{"title":`), http.StatusBadRequest, model.CodeInvalid)
}

func TestListMessagesHonoursContractLimit(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("分页")
	sess := h.seedSession(a.ID)
	ctx := context.Background()
	const total = 260
	for i := 0; i < total; i++ {
		m := model.TextMessage(sess.ID, model.RoleUser, "m")
		if err := h.env.Store.AppendMessage(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	// LAY-02：limit=500 时一次取完 260 条，不再被截到 200。
	got := decodeBody[items[model.Message]](t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/messages?limit=500", nil))
	if len(got.Items) != total {
		t.Fatalf("limit=500 应返回全部 %d 条，实际 %d 条", total, len(got.Items))
	}
	// 缺省 100；超过上限按 500。
	if d := decodeBody[items[model.Message]](t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/messages", nil)); len(d.Items) != 100 {
		t.Fatalf("缺省 limit 应为 100，实际 %d 条", len(d.Items))
	}
	if d := decodeBody[items[model.Message]](t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/messages?limit=9999", nil)); len(d.Items) != total {
		t.Fatalf("超过上限的 limit 按 500 处理，应返回 %d 条，实际 %d 条", total, len(d.Items))
	}
}

func TestEventsHelloFrameComesFirst(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.srv.StartedAt = "2026-10-10T08:00:00Z" })
	ts := newStreamServer(t, h)
	for i := 0; i < 3; i++ {
		publish(h.env.Bus, "test.event", "")
	}
	_, fr := openStream(t, ts, h.env.Token, "?since=1", nil)
	f := fr.nextFrame(t)
	if f.event != server.EventHello || f.id != "" {
		t.Fatalf("第一帧应是不带 id 的 hello，实际 %+v", f)
	}
	var hello struct {
		StartedAt string `json:"started_at"`
		LastSeq   uint64 `json:"last_seq"`
	}
	if err := json.Unmarshal([]byte(f.data), &hello); err != nil || hello.StartedAt != "2026-10-10T08:00:00Z" || hello.LastSeq != 3 {
		t.Fatalf("hello 的 data = %q (%v)", f.data, err)
	}
	if f := fr.nextEvent(t); f.id != "2" {
		t.Fatalf("hello 之后应从 since 续传，实际 %+v", f)
	}

	// since 大于当前 Seq（守护进程重启过）：先 hello，再 gap，然后结束响应。
	_, fr2 := openStream(t, ts, h.env.Token, "?since=99", nil)
	if f := fr2.nextFrame(t); f.event != server.EventHello {
		t.Fatalf("第一帧应是 hello，实际 %+v", f)
	}
	if f := fr2.nextEvent(t); f.event != server.EventGap || f.id != "3" {
		t.Fatalf("since 超前时应发送 id 为当前 Seq 的 gap，实际 %+v", f)
	}
	if _, err := fr2.next(); err != io.EOF {
		t.Fatalf("gap 之后应结束响应，实际 %v", err)
	}
}

// TestChannelsFollowProductionRules 覆盖 LAY-08：servertest 用生产的 channel.Service 装配，
// 测试看到的通道规则与生产一致（支持的类型、智能体归属、凭据引用名、凭据上限、删除幂等）。
func TestChannelsFollowProductionRules(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("通道规则")

	// 生产网关只支持 console 与 telegram。
	mustError(t, h.do(http.MethodPost, "/api/v1/channels", map[string]any{"agent_id": a.ID, "kind": "feishu", "name": "f"}),
		http.StatusBadRequest, model.CodeInvalid)
	// 智能体必须存在。
	mustError(t, h.do(http.MethodPost, "/api/v1/channels", map[string]any{
		"agent_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "kind": "console", "name": "c",
	}), http.StatusNotFound, model.CodeNotFound)
	// 凭据超过上限：invalid（不是 unavailable）。
	mustError(t, h.do(http.MethodPost, "/api/v1/channels", map[string]any{
		"agent_id": a.ID, "kind": "telegram", "name": "tg", "secret": strings.Repeat("x", channel.MaxSecretBytes+1),
	}), http.StatusBadRequest, model.CodeInvalid)
	// 配置按网关的解析规则校验。
	mustError(t, h.do(http.MethodPost, "/api/v1/channels", map[string]any{
		"agent_id": a.ID, "kind": "telegram", "name": "tg", "config": map[string]any{"group_mode": "sometimes"},
	}), http.StatusBadRequest, model.CodeInvalid)

	rec := h.do(http.MethodPost, "/api/v1/channels", map[string]any{"agent_id": a.ID, "kind": "telegram", "name": "tg", "secret": "tok"})
	mustStatus(t, rec, http.StatusCreated)
	var ch struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := h.env.Secrets.Has(ctx, channel.SecretRef(ch.ID)); err != nil || !ok {
		t.Fatalf("凭据应保存在生产的引用名 %s 下: %v / %v", channel.SecretRef(ch.ID), ok, err)
	}
	// PATCH 时 config 为 null 表示不修改，不会清空。
	mustStatus(t, h.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, map[string]any{"config": map[string]any{"allowed_peers": []string{"1"}}}), http.StatusOK)
	rec = h.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, map[string]any{"config": nil, "name": "tg2"})
	mustStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "allowed_peers") {
		t.Fatalf("config: null 不应清空配置: %s", rec.Body.String())
	}
	mustStatus(t, h.do(http.MethodDelete, "/api/v1/channels/"+ch.ID, nil), http.StatusNoContent)
	if ok, _ := h.env.Secrets.Has(ctx, channel.SecretRef(ch.ID)); ok {
		t.Fatal("删除通道后凭据应被删除")
	}
}
