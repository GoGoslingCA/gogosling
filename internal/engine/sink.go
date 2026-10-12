package engine

import (
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// BusSink 返回把运行事件发布到事件总线的 EventSink：总线事件的 Type 即 RunEvent.Type，
// Payload 为整个 RunEvent，AgentID / SessionID / RunID 同步填入以便订阅过滤。
func BusSink(bus *event.Bus) EventSink {
	return func(ev model.RunEvent) {
		bus.Publish(event.Event{
			Type:      string(ev.Type),
			At:        ev.At,
			AgentID:   ev.AgentID,
			SessionID: ev.SessionID,
			RunID:     ev.RunID,
			Payload:   ev,
		})
	}
}

// MultiSink 把同一事件依次交给多个 sink（例如先持久化再发布）；nil 的 sink 会被跳过。
func MultiSink(sinks ...EventSink) EventSink {
	return func(ev model.RunEvent) {
		for _, s := range sinks {
			if s != nil {
				s(ev)
			}
		}
	}
}
