package queue

import (
	"time"

	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
)

// ACK检查相关常量
const (
	// AckCheckTimeout ACK检查超时时间（默认30秒）
	AckCheckTimeout = 30 * time.Second
	// AckMaxRetries 最大重试次数
	AckMaxRetries = 3
	// AckRetryDelay 重试延迟时间（每次重试后延迟）
	AckRetryDelay = 10 * time.Second
)

// RoomMessageAckCheckPayload ACK检查任务载荷
type RoomMessageAckCheckPayload struct {
	Rid        string `json:"rid"`         // 房间ID
	Uid        string `json:"uid"`         // 用户ID
	Mid        string `json:"mid"`         // 消息ID
	SeqId      int64  `json:"seq_id"`      // 消息顺序号
	RetryCount int32  `json:"retry_count"` // 当前重试次数
}

// TaskRoomMessageAckCheck ACK检查任务定义
var TaskRoomMessageAckCheck = NewTask[RoomMessageAckCheckPayload]("room:message:ack:check")

// RoomMessageResendPayload 重发消息任务载荷
type RoomMessageResendPayload struct {
	Uid     string                  `json:"uid"`
	Message types.ServerRoomMessage `json:"message"`
}

// TaskRoomMessageResend 重发消息任务定义
var TaskRoomMessageResend = NewTask[RoomMessageResendPayload]("room:message:resend")

// RoomMessageNotifyPayload 房间消息通知任务载荷（通知 quic 广播房间消息）
type RoomMessageNotifyPayload struct {
	Mid string `json:"mid"`
}

// TaskRoomMessageNotify 房间消息通知任务定义
var TaskRoomMessageNotify = NewTask[RoomMessageNotifyPayload]("room:message:notify")

type RoomMessageWithdrawNotifyPayload struct {
	Mid string `json:"mid"`
}

var TaskRoomMessageWithdrawNotify = NewTask[RoomMessageWithdrawNotifyPayload]("room:message:withdraw:notify")

// RoomMessageWithdrawAckCheckPayload 消息撤回ACK检查任务载荷
type RoomMessageWithdrawAckCheckPayload struct {
	Rid        string `json:"rid"`         // 房间ID
	Uid        string `json:"uid"`         // 用户ID
	Mid        string `json:"mid"`         // 消息ID
	SeqId      int64  `json:"seq_id"`      // 消息顺序号
	RetryCount int32  `json:"retry_count"` // 当前重试次数
}

// TaskRoomMessageWithdrawAckCheck 消息撤回ACK检查任务定义
var TaskRoomMessageWithdrawAckCheck = NewTask[RoomMessageWithdrawAckCheckPayload]("room:message:withdraw:ack:check")

// RoomMessageWithdrawResendPayload 重发撤回消息任务载荷
type RoomMessageWithdrawResendPayload struct {
	Uid     string                          `json:"uid"`
	Message types.ServerRoomMessageWithdraw `json:"message"`
}

// TaskRoomMessageWithdrawResend 重发撤回消息任务定义
var TaskRoomMessageWithdrawResend = NewTask[RoomMessageWithdrawResendPayload]("room:message:withdraw:resend")

// FileStatusUpdateAckCheckPayload 文件状态更新ACK检查任务载荷
type FileStatusUpdateAckCheckPayload struct {
	Rid        string `json:"rid"`         // 房间ID
	Uid        string `json:"uid"`         // 用户ID
	ClientCid  string `json:"client_cid"`  // 客户端内容ID
	ClientMid  string `json:"client_mid"`  // 客户端消息ID
	Mid        string `json:"mid"`         // 消息ID
	Cid        string `json:"cid"`         // 内容ID
	RetryCount int32  `json:"retry_count"` // 当前重试次数
}

// TaskFileStatusUpdateAckCheck 文件状态更新ACK检查任务定义
var TaskFileStatusUpdateAckCheck = NewTask[FileStatusUpdateAckCheckPayload]("file:status:update:ack:check")

// NotificationNotifyPayload 消息通知推送任务载荷（通知 quic 推送通知给在线用户）
type NotificationNotifyPayload struct {
	Nid string `json:"nid"` // 通知ID
}

// TaskNotificationNotify 消息通知推送任务定义
var TaskNotificationNotify = NewTask[NotificationNotifyPayload]("notification:notify")

// RoomMuteStrategyTimePayload 策略禁言开始/结束定时任务载荷（按策略生效时间、过期时间到点发送房间消息，与频率无关）
type RoomMuteStrategyTimePayload struct {
	Rid     string `json:"rid"`
	RunAtMs int64  `json:"run_at_ms"`
	Kind    string `json:"kind"` // "start" 生效时间到发禁言开始，"end" 过期时间到发禁言结束
}

// TaskRoomMuteStrategyTime 策略禁言定时任务定义
var TaskRoomMuteStrategyTime = NewTask[RoomMuteStrategyTimePayload]("room:mute:strategy_time")

// RoomAdminOperationLogPayload 房间管理员操作日志任务载荷（由独立队列消费并写 DB）
type RoomAdminOperationLogPayload struct {
	Rid         string                       `json:"rid"`
	OpType      entity.RoomAdminOperationType `json:"op_type"`
	OperatorUid string                       `json:"operator_uid"`
	Sid         string                       `json:"sid"`
	RelatedId   string                       `json:"related_id"`
	BeforeData  map[string]any               `json:"before_data"`
	AfterData   map[string]any               `json:"after_data"`
}

// TaskRoomAdminOperationLog 房间管理员操作日志任务定义
var TaskRoomAdminOperationLog = NewTask[RoomAdminOperationLogPayload]("oplog:room_admin")

// UserOperationLogPayload 用户操作日志任务载荷（由独立队列消费并写 DB）
type UserOperationLogPayload struct {
	Uid        string                    `json:"uid"`
	OpType     entity.UserOperationType `json:"op_type"`
	Sid        string                   `json:"sid"`
	RelatedId  string                   `json:"related_id"`
	BeforeData map[string]any           `json:"before_data"`
	AfterData  map[string]any           `json:"after_data"`
}

// TaskUserOperationLog 用户操作日志任务定义
var TaskUserOperationLog = NewTask[UserOperationLogPayload]("oplog:user")
