package event

import "sync"

// Event 代表一个带有静态类型的事件标识，用于在编译期限定载荷类型。
type Event[T any] struct {
	Name string
}

// NewEvent 创建一个新的事件标识。
func NewEvent[T any](name string) Event[T] {
	return Event[T]{Name: name}
}

// NewEventKey 为兼容旧命名，等价于 NewEvent。
func NewEventKey[T any](name string) Event[T] {
	return NewEvent[T](name)
}

// Bus 是一个简单的发布订阅总线，利用 Go 泛型在订阅阶段校验事件载荷类型。
type Bus struct {
	mu          sync.RWMutex
	subscribers map[string][]func(any)
}

var (
	defaultBus   *Bus
	defaultBusMu sync.RWMutex
)

// NewBus 构造函数。
func NewBus() *Bus {
	return &Bus{
		subscribers: make(map[string][]func(any)),
	}
}

// subscribe 注册处理器（内部使用，避免在方法上使用泛型）。
func (b *Bus) subscribe(name string, handler func(any)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[name] = append(b.subscribers[name], handler)
}

// publish 发布事件（内部使用，避免在方法上使用泛型）。
func (b *Bus) publish(name string, payload any) {
	b.mu.RLock()
	handlers, ok := b.subscribers[name]
	b.mu.RUnlock()
	if !ok || len(handlers) == 0 {
		return
	}

	// 为避免持有锁执行用户代码，这里先复制到局部切片。
	for _, h := range handlers {
		h(payload)
	}
}

// Subscribe 订阅默认总线。
func Subscribe[T any](evt Event[T], handler func(T)) {
	bus := DefaultBus()
	bus.subscribe(evt.Name, func(v any) {
		if payload, ok := v.(T); ok {
			handler(payload)
		}
	})
}

// Publish 向默认总线发布事件。
func Publish[T any](evt Event[T], payload T) {
	bus := DefaultBus()
	bus.publish(evt.Name, payload)
}

// InitDefaultBus 创建并注册默认总线（幂等）。
func InitDefaultBus() *Bus {
	defaultBusMu.Lock()
	defer defaultBusMu.Unlock()
	if defaultBus == nil {
		defaultBus = NewBus()
	}
	return defaultBus
}

// SetDefaultBus 手动指定默认总线。
func SetDefaultBus(b *Bus) {
	defaultBusMu.Lock()
	defer defaultBusMu.Unlock()
	defaultBus = b
}

// DefaultBus 获取默认总线，未初始化会 panic。
func DefaultBus() *Bus {
	defaultBusMu.RLock()
	defer defaultBusMu.RUnlock()
	if defaultBus == nil {
		panic("event default bus is nil, call InitDefaultBus or SetDefaultBus first")
	}
	return defaultBus
}
