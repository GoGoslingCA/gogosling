package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestConsoleAdapterLifecycle(t *testing.T) {
	c := NewConsole()
	if c.Kind() != model.ChannelConsole {
		t.Fatalf("类型错误: %s", c.Kind())
	}
	if err := c.Inject(Inbound{Text: "x"}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("未启动时 Inject 应返回 unavailable，得到 %v", err)
	}
	if err := c.Start(context.Background(), nil, "", nil); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("缺少参数应返回 invalid，得到 %v", err)
	}

	var got []Inbound
	ch := &model.Channel{ID: "c1", Kind: model.ChannelConsole}
	if err := c.Start(context.Background(), ch, "ignored", func(in Inbound) { got = append(got, in) }); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if err := c.Start(context.Background(), ch, "", func(Inbound) {}); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("重复启动应返回 conflict，得到 %v", err)
	}
	if !c.Status().Connected {
		t.Fatal("启动后应报告已连接")
	}
	if err := c.Inject(Inbound{ChatID: "a", PeerID: "a", Text: "hi"}); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	if len(got) != 1 || got[0].ChannelID != "c1" || got[0].Kind != model.ChannelConsole || got[0].At.IsZero() {
		t.Fatalf("注入应补齐通道、类型与时间，得到 %+v", got)
	}

	var hooked []Outbound
	c.OnSend(func(o Outbound) { hooked = append(hooked, o) })
	if err := c.Send(context.Background(), Outbound{ChatID: "a", Text: "回复"}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if len(c.Sent()) != 1 || len(hooked) != 1 || hooked[0].Text != "回复" {
		t.Fatalf("Send 应记录并触发钩子，得到 %v / %v", c.Sent(), hooked)
	}
	c.Sent()[0].Text = "改副本"
	if c.Sent()[0].Text != "回复" {
		t.Fatal("Sent 应返回副本")
	}
	c.FailSends(errors.New("boom"))
	if err := c.Send(context.Background(), Outbound{Text: "x"}); err == nil {
		t.Fatal("FailSends 后 Send 应失败")
	}
	c.FailSends(nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Send(cancelled, Outbound{Text: "x"}); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("ctx 取消应返回 cancelled，得到 %v", err)
	}
	c.Reset()
	if len(c.Sent()) != 0 {
		t.Fatal("Reset 应清空记录")
	}

	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if c.Status().Connected {
		t.Fatal("停止后不应报告已连接")
	}
	if err := c.Inject(Inbound{Text: "late"}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("停止后 Inject 应失败，得到 %v", err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("重复停止应成功，得到 %v", err)
	}
}
