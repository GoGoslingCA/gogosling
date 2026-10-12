package session

import (
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestMakeKeyUsesDefaultPeer(t *testing.T) {
	if got := MakeKey("a1", model.OriginDesktop, ""); got != "a1:desktop:local" {
		t.Fatalf("空 peer 应使用默认值 local，得到 %q", got)
	}
	if got := MakeKey("a1", model.OriginChannel, "tg:12345"); got != "a1:channel:tg:12345" {
		t.Fatalf("peer 中的冒号应原样保留，得到 %q", got)
	}
}

func TestParseKeyRoundTrip(t *testing.T) {
	k, err := ParseKey("a1:channel:tg:12345")
	if err != nil {
		t.Fatalf("合法路由键解析失败: %v", err)
	}
	if k.AgentID != "a1" || k.Origin != model.OriginChannel || k.Peer != "tg:12345" {
		t.Fatalf("解析结果错误: %+v", k)
	}
	if k.String() != "a1:channel:tg:12345" {
		t.Fatalf("String 应还原原始键，得到 %q", k.String())
	}
}

func TestParseKeyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "a1", "a1:desktop", "a1:desktop:", ":desktop:local", "a1:unknown:local", "a1::local"} {
		if _, err := ParseKey(bad); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%q 应返回 invalid 错误，得到 %v", bad, err)
		}
	}
}

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to model.RunStatus
		ok       bool
	}{
		{model.RunQueued, model.RunRunning, true},
		{model.RunQueued, model.RunCancelled, true},
		{model.RunQueued, model.RunAwaitingApproval, false},
		{model.RunRunning, model.RunAwaitingApproval, true},
		{model.RunAwaitingApproval, model.RunRunning, true},
		{model.RunRunning, model.RunCompleted, true},
		{model.RunCompleted, model.RunRunning, false},
		{model.RunFailed, model.RunCancelled, false},
		{model.RunRunning, model.RunRunning, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Errorf("%s → %s 应为 %v", c.from, c.to, c.ok)
		}
	}
}

func TestAutoTitle(t *testing.T) {
	if got := autoTitle("\n\n  帮我写个脚本  \n第二行"); got != "帮我写个脚本" {
		t.Fatalf("应取第一个非空行并去除空白，得到 %q", got)
	}
	long := ""
	for i := 0; i < 70; i++ {
		long += "字"
	}
	got := autoTitle(long)
	if len([]rune(got)) != maxAutoTitleRunes+1 || got[len(got)-3:] != "…" {
		t.Fatalf("超长标题应截断并加省略号，得到 %q", got)
	}
	if autoTitle("   \n ") != "" {
		t.Fatal("空白文本不应产生标题")
	}
}

// TestChannelKeyMatchesGatewayFormat 与 gateway 包的 TestSessionKeyMatchesSessionPackage 使用同一个样例键：
// gateway 不导入本包，靠两边各自的用例锁定 IM 会话键 "<agent_id>:channel:<kind>:<chat_id>" 的格式。
func TestChannelKeyMatchesGatewayFormat(t *testing.T) {
	const want = "agent-1:channel:telegram:-100123:7"
	if got := MakeKey("agent-1", model.OriginChannel, "telegram"+":"+"-100123:7"); got != want {
		t.Fatalf("IM 会话键应与 gateway.SessionKey 的格式一致，期望 %q，得到 %q", want, got)
	}
	k, err := ParseKey(want)
	if err != nil {
		t.Fatalf("gateway 生成的键应能被本包解析: %v", err)
	}
	if k.AgentID != "agent-1" || k.Origin != model.OriginChannel || k.Peer != "telegram:-100123:7" {
		t.Fatalf("peer 段应为 <kind>:<chat_id>，得到 %+v", k)
	}
}
