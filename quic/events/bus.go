package events

import "github.com/asaskevich/EventBus"

type EventEngine struct {
	bus EventBus.Bus
}

func NewEventEngine() *EventEngine {
	return &EventEngine{
		bus: EventBus.New(),
	}
}

func (e *EventEngine) Publish(event string, args ...any) {
	e.bus.Publish(event, args...)
}

func (e *EventEngine) Subscribe(event string, handler any) {
	e.bus.Subscribe(event, handler)
}

// 前缀定义事件类型
func (e *EventEngine) GroupSubscribe(prefix string, event string, handler any) {
	e.Subscribe(prefix+"."+event, handler)
}

func (e *EventEngine) GroupUnsubscribe(prefix string, event string, handler any) {
	e.Unsubscribe(prefix+"."+event, handler)
}

func (e *EventEngine) PrefixPublish(prefix string, event string, args ...any) {
	e.Publish(prefix+"."+event, args...)
}

func (e *EventEngine) Unsubscribe(event string, handler any) {
	e.bus.Unsubscribe(event, handler)
}
