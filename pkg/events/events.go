package events

// 事件类型常量
const (
	// EventHeartbeatUpdate 心跳更新事件
	EventHeartbeatUpdate = "heartbeat.update"
	// EventClientClose 客户端关闭事件
	EventClientClose = "client.close"
	// EventClientTimeout 客户端超时事件
	EventClientTimeout = "client.timeout"
	// EventClientOnline 客户端上线事件
	EventClientOnline = "client.online"
)

// HeartbeatUpdateEvent 心跳更新事件数据
type HeartbeatUpdateEvent struct {
	Uid    string
	Sid    string
	ConnID uintptr
}

// ClientCloseEvent 客户端关闭事件数据
type ClientCloseEvent struct {
	Uid    string
	Sid    string
	Reason string
	// ConnID 标识 QUIC 连接实例（通常为 conn 指针地址），用于同 uid+sid 重连时忽略陈旧 close。
	ConnID uintptr
}

// ClientTimeoutEvent 客户端超时事件数据
type ClientTimeoutEvent struct {
	Uid    string
	Sid    string
	ConnID uintptr
}
