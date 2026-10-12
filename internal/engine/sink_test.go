package engine

import (
	"context"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestBusSinkPublishesRunEvent(t *testing.T) {
	bus := event.NewBus(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx, 0, 4, nil)

	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	BusSink(bus)(model.RunEvent{Type: model.EventTextDelta, RunID: "r", SessionID: "s", AgentID: "a", At: at, Delta: "hi"})

	select {
	case ev := <-sub.C:
		if ev.Type != "text.delta" || ev.RunID != "r" || ev.SessionID != "s" || ev.AgentID != "a" || !ev.At.Equal(at) {
			t.Errorf("总线事件 = %+v，期望类型与标识来自 RunEvent", ev)
		}
		payload, ok := ev.Payload.(model.RunEvent)
		if !ok || payload.Delta != "hi" {
			t.Errorf("Payload = %#v，期望原始 RunEvent", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("总线未收到事件")
	}
}

func TestMultiSinkFansOutAndSkipsNil(t *testing.T) {
	var a, b int
	sink := MultiSink(func(model.RunEvent) { a++ }, nil, func(model.RunEvent) { b++ })
	sink(model.RunEvent{Type: model.EventRunStarted})
	sink(model.RunEvent{Type: model.EventRunCompleted})
	if a != 2 || b != 2 {
		t.Errorf("调用次数 = %d/%d，期望 2/2", a, b)
	}
}
