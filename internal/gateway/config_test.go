package gateway

import (
	"encoding/json"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestParseChannelConfigDefaults(t *testing.T) {
	for _, raw := range []string{"", "null", "  ", "{}"} {
		cfg, err := ParseChannelConfig(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%q 应解析为默认值，得到 %v", raw, err)
		}
		if cfg.GroupMode != GroupModeMention || cfg.RatePerMinute != DefaultRatePerMinute ||
			cfg.RateBurst != DefaultRateBurst || cfg.MaxChunk != DefaultMaxChunk || len(cfg.AllowedPeers) != 0 {
			t.Fatalf("默认值错误: %+v", cfg)
		}
		if !cfg.PeerAllowed("anyone") {
			t.Fatal("空白名单应允许所有人")
		}
	}
}

func TestParseChannelConfigValuesAndUnknownKeys(t *testing.T) {
	cfg, err := ParseChannelConfig(json.RawMessage(`{
		"allowed_peers": [" a ", "", "b"], "group_mode": "all", "trigger_prefix": " ! ",
		"rate_per_minute": 5, "rate_burst": 1, "max_chunk": 2000, "api_base": "http://x"}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(cfg.AllowedPeers) != 2 || cfg.AllowedPeers[0] != "a" || cfg.AllowedPeers[1] != "b" {
		t.Fatalf("白名单应去空白与空项，得到 %v", cfg.AllowedPeers)
	}
	if cfg.GroupMode != GroupModeAll || cfg.TriggerPrefix != "!" || cfg.RatePerMinute != 5 || cfg.RateBurst != 1 || cfg.MaxChunk != 2000 {
		t.Fatalf("字段解析错误: %+v", cfg)
	}
	if cfg.PeerAllowed("c") || !cfg.PeerAllowed("b") {
		t.Fatal("白名单判断错误")
	}
}

func TestParseChannelConfigRejectsBadValues(t *testing.T) {
	bad := []string{`{"group_mode":"everyone"}`, `{"rate_per_minute":-1}`, `{"rate_burst":-2}`, `{"max_chunk":-5}`, `[1,2]`, `{"allowed_peers":"x"}`, `{bad json`}
	for _, raw := range bad {
		if _, err := ParseChannelConfig(json.RawMessage(raw)); !model.IsCode(err, model.CodeInvalid) {
			t.Fatalf("%s 应返回 invalid，得到 %v", raw, err)
		}
	}
}

// TestSessionKeyMatchesSessionPackage 与 session 包的 TestChannelKeyMatchesGatewayFormat 使用同一个样例键：
// 本包不导入 session，靠两边各自的用例锁定 IM 会话键 "<agent_id>:channel:<kind>:<chat_id>" 的格式。
func TestSessionKeyMatchesSessionPackage(t *testing.T) {
	const want = "agent-1:channel:telegram:-100123:7"
	if got := SessionKey("agent-1", model.ChannelTelegram, "-100123:7"); got != want {
		t.Fatalf("会话键应与 session.MakeKey(agent, channel, kind+\":\"+chat) 一致，期望 %q，得到 %q", want, got)
	}
	agentID, kind, chatID, ok := ParseSessionKey(want)
	if !ok || agentID != "agent-1" || kind != model.ChannelTelegram || chatID != "-100123:7" {
		t.Fatalf("ParseSessionKey 应还原各段（chat_id 可含冒号），得到 %q %q %q %v", agentID, kind, chatID, ok)
	}
	for _, bad := range []string{"", "agent-1:desktop:local", "agent-1:cli:local", "agent-1:channel:telegram",
		"agent-1:channel::42", ":channel:telegram:42", "agent-1:channel:telegram:", "agent-1:job:telegram:42"} {
		if _, _, _, ok := ParseSessionKey(bad); ok {
			t.Errorf("%q 不是 IM 会话键，应返回 ok=false", bad)
		}
	}
}

func TestSessionKeyFormat(t *testing.T) {
	got := SessionKey("A1", model.ChannelTelegram, "-100123")
	if got != "A1:channel:telegram:-100123" {
		t.Fatalf("会话键格式错误: %q", got)
	}
}
