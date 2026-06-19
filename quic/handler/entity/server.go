package entity

import "github.com/xd/quic-server/pkg/publishermedia"

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/xd/quic-server/db/entity"
)

type ServerMessageContentFile struct {
	Filename     string `json:"filename" msgpack:"filename"`
	FileSize     uint64 `json:"file_size" msgpack:"file_size"`
	FileTypeMain string `json:"file_type_main" msgpack:"file_type_main"`
	FileTypeSub  string `json:"file_type_sub" msgpack:"file_type_sub"`
	FileHash     string `json:"file_hash" msgpack:"file_hash"`
	Height       uint64 `json:"height" msgpack:"height"`
	Width        uint64 `json:"width" msgpack:"width"`
	Duration     uint64 `json:"duration" msgpack:"duration"`
	UfId         string `json:"uf_id" msgpack:"uf_id"`
}
type ServerRoomMessageContent struct {
	Mid        string                        `json:"mid,omitempty" msgpack:"mid,omitempty"`
	ClientCid  string                        `json:"client_cid,omitempty" msgpack:"client_cid,omitempty"`
	Cid        string                        `json:"cid,omitempty" msgpack:"cid,omitempty"`
	Type       entity.RoomMessageContentType `json:"type" msgpack:"type"`
	TypeId     string                        `json:"type_id" msgpack:"type_id"`
	Content    json.RawMessage               `json:"content" msgpack:"content"`
	File       *ServerMessageContentFile     `json:"file" msgpack:"file"`
	CreateTime int64                         `json:"create_time" msgpack:"create_time"`
}
type ServerRoomMessageContentList []ServerRoomMessageContent

// Value/Scan 让 GORM 以 JSON 形式存取消息内容，避免解析内部字段为关联
func (m ServerRoomMessageContentList) Value() (driver.Value, error) {
	return json.Marshal(m)
}

func (m *ServerRoomMessageContentList) Scan(value any) error {
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("unexpected type for MessageContentList: %T", value)
	}
	return json.Unmarshal(bytes, m)
}

type ServerRoomMessage struct {
	SeqId              int64                        `json:"seq_id" msgpack:"seq_id"`
	ClientMid          string                       `json:"client_mid" msgpack:"client_mid"`
	Mid                string                       `json:"mid" msgpack:"mid"`
	SenderUid          string                       `json:"sender_uid" msgpack:"sender_uid"`
	Rid                string                       `json:"rid" msgpack:"rid"`
	Contents           ServerRoomMessageContentList `json:"contents" msgpack:"contents"`
	CreateTime         int64                        `json:"create_time" msgpack:"create_time"`
	SenderNickname     string                       `json:"sender_nickname,omitempty" msgpack:"sender_nickname,omitempty"`           // 会话列表等联表查询时带出，用于展示
	SenderAvatarUfId   string                       `json:"sender_avatar_uf_id,omitempty" msgpack:"sender_avatar_uf_id,omitempty"`   // 同上，发送者头像 uf_id
	SenderRoomNickname string                       `json:"sender_room_nickname,omitempty" msgpack:"sender_room_nickname,omitempty"` // 同上，群内昵称
}

// ServerRoomMessageBatch 批量消息下发
type ServerRoomMessageBatch struct {
	Messages []ServerRoomMessage `json:"messages" msgpack:"messages"`
}

type ServerFileStatusUpdate struct {
	ClientCid string          `json:"client_cid" msgpack:"client_cid"`
	UfId      string          `json:"uf_id" msgpack:"uf_id"`
	Content   json.RawMessage `json:"content" msgpack:"content"`
}

// ServerFileStatusUpdateAck 服务器发送给客户端的文件状态更新ACK确认
type ServerFileStatusUpdateAck struct {
	Rid       string `json:"rid" msgpack:"rid"`
	ClientMid string `json:"client_mid" msgpack:"client_mid"`
	ClientCid string `json:"client_cid" msgpack:"client_cid"`
	Mid       string `json:"mid" msgpack:"mid"`
	Cid       string `json:"cid" msgpack:"cid"`
}

// ServerRoomMessageWithdraw 服务器发送给客户端的房间消息撤回
type ServerRoomMessageWithdraw struct {
	SeqId      int64                        `json:"seq_id" msgpack:"seq_id"`
	ClientMid  string                       `json:"client_mid" msgpack:"client_mid"`
	Mid        string                       `json:"mid" msgpack:"mid"`
	SenderUid  string                       `json:"sender_uid" msgpack:"sender_uid"`
	Rid        string                       `json:"rid" msgpack:"rid"`
	Contents   ServerRoomMessageContentList `json:"contents" msgpack:"contents"`
	CreateTime int64                        `json:"create_time" msgpack:"create_time"`
}

// ServerRoomMessageWithdrawBatch 批量撤回消息下发
type ServerRoomMessageWithdrawBatch struct {
	Messages []ServerRoomMessageWithdraw `json:"messages" msgpack:"messages"`
}

// ServerForceOffline 服务器发送给客户端的强制掉线消息
type ServerForceOffline struct {
	Reason string `json:"reason" msgpack:"reason"`
}

// ServerUserStatusSync 服务器通知客户端进行用户/会话状态同步
// 目前只需要 uid，前端拿到事件后自行调用 API 刷新数据
type ServerUserStatusSync struct {
	Uid               string `json:"uid" msgpack:"uid"`
	IsOnline          bool   `json:"is_online" msgpack:"is_online"`
	CurrentStatus     string `json:"current_status" msgpack:"current_status"`
	LastOnline        int64  `json:"last_online" msgpack:"last_online"`
	LastLogin         int64  `json:"last_login" msgpack:"last_login"`
	CustomState       string `json:"custom_state" msgpack:"custom_state"`
	Platform          string `json:"platform" msgpack:"platform"`
	DeviceType        string `json:"device_type" msgpack:"device_type"`
	DeviceModel       string `json:"device_model" msgpack:"device_model"`
	OSVersion         string `json:"os_version" msgpack:"os_version"`
	AppVersion        string `json:"app_version" msgpack:"app_version"`
	ConcurrentDevices int    `json:"concurrent_devices" msgpack:"concurrent_devices"`
}

// ServerRoomUserPresenceSync 房间成员在线态同步（仅包含在线/离线与时间字段）
// 注意：不包含 custom_state/current_status 等展示字段，避免好友/房间权限混淆。
type ServerRoomUserPresenceSync struct {
	Uid        string `json:"uid" msgpack:"uid"`
	IsOnline   bool   `json:"is_online" msgpack:"is_online"`
	LastOnline int64  `json:"last_online" msgpack:"last_online"`
	LastLogin  int64  `json:"last_login" msgpack:"last_login"`
}

// ServerNotification 服务器发送给客户端的消息通知
type ServerNotification struct {
	Nid        string                   `json:"nid" msgpack:"nid"`
	Uid        string                   `json:"uid" msgpack:"uid"`
	State      entity.NotificationState `json:"state" msgpack:"state"` // 0-未读,11-好友请求已同意,12-好友请求已拒绝,21-房间邀请已同意,22-房间邀请已拒绝
	Type       entity.NotificationType  `json:"type" msgpack:"type"`   // 10-收到好友请求,11-发起好友请求,12-好友请求已同意,13-好友请求已拒绝,20-收到房间邀请,21-发起房间邀请,22-房间邀请已同意,23-房间邀请已拒绝
	RelatedId  string                   `json:"related_id" msgpack:"related_id"`
	Content    string                   `json:"content" msgpack:"content"` // JSON字符串
	Status     int8                     `json:"status" msgpack:"status"`   // 0-未读,1-已读
	ReadAt     int64                    `json:"read_at" msgpack:"read_at"`
	CreateTime int64                    `json:"create_time" msgpack:"create_time"`
}

// UserStatusPublic 用户公开状态信息（不包含敏感信息）
// 注意：此结构体与 query.UserStatusPublic 结构相同，但定义在此处以避免循环依赖
type UserStatusPublic struct {
	IsOnline          bool   `json:"is_online" msgpack:"is_online"`
	CurrentStatus     string `json:"current_status" msgpack:"current_status"`
	LastOnline        int64  `json:"last_online" msgpack:"last_online"`
	LastLogin         int64  `json:"last_login" msgpack:"last_login"`
	CustomState       string `json:"custom_state" msgpack:"custom_state"`
	Platform          string `json:"platform" msgpack:"platform"`
	DeviceType        string `json:"device_type" msgpack:"device_type"`
	DeviceModel       string `json:"device_model" msgpack:"device_model"`
	OSVersion         string `json:"os_version" msgpack:"os_version"`
	AppVersion        string `json:"app_version" msgpack:"app_version"`
	ConcurrentDevices int    `json:"concurrent_devices" msgpack:"concurrent_devices"`
}

// ServerRoomMessageSendError 服务器发送给客户端的房间消息发送失败（如私聊非好友）
type ServerRoomMessageSendError struct {
	Rid       string `json:"rid" msgpack:"rid"`
	ClientMid string `json:"client_mid" msgpack:"client_mid"`
	Code      string `json:"code" msgpack:"code"`
	Message   string `json:"message" msgpack:"message"`
}

// ServerRoomUserRoomNicknameUpdate 房间内用户群昵称变更（不落库，仅广播给房间用户静默更新 UI）
type ServerRoomUserRoomNicknameUpdate struct {
	Rid          string `json:"rid" msgpack:"rid"`
	Uid          string `json:"uid" msgpack:"uid"`
	RoomNickname string `json:"room_nickname" msgpack:"room_nickname"`
}

// ServerRoomUserIdsUpdate 房间成员列表变更（不落库，仅广播给房间用户静默更新 UI）
type ServerRoomUserIdsUpdate struct {
	Rid     string   `json:"rid" msgpack:"rid"`
	UserIds []string `json:"user_ids" msgpack:"user_ids"`
}

// ServerRoomDissolvedUpdate 房间已解散（不落库，仅广播给房间用户静默更新 UI）
type ServerRoomDissolvedUpdate struct {
	Rid       string `json:"rid" msgpack:"rid"`
	RoomState int8   `json:"room_state" msgpack:"room_state"`
}

// ServerRoomAvatarUpdate 房间头像变更（不落库，仅广播给房间用户静默更新 UI）
type ServerRoomAvatarUpdate struct {
	Rid        string `json:"rid" msgpack:"rid"`
	AvatarUfId string `json:"avatar_uf_id" msgpack:"avatar_uf_id"`
}

// ServerUserInfo 仅含用户资料字段（昵称、头像等），不含在线态。
// 在线态与设备展示字段由 UserStatusSync 单独推送，避免资料推送覆盖 presence。
type ServerUserInfo struct {
	Uid          string `json:"uid" msgpack:"uid"`
	Username     string `json:"username" msgpack:"username"`
	Nickname     string `json:"nickname" msgpack:"nickname"`
	Signature    string `json:"signature" msgpack:"signature"`
	Introduction string `json:"introduction" msgpack:"introduction"`
	Email        string `json:"email" msgpack:"email"`
	AvatarUfId   string `json:"avatar_uf_id" msgpack:"avatar_uf_id"`
	CreateTime   int64  `json:"create_time" msgpack:"create_time"`
}

type ServerStreamCallInvite struct {
	CallID         string         `json:"call_id" msgpack:"call_id"`
	Rid            string         `json:"rid" msgpack:"rid"`
	CallType       string         `json:"call_type" msgpack:"call_type"`
	CallScene      string         `json:"call_scene" msgpack:"call_scene"`
	InviterUid     string         `json:"inviter_uid" msgpack:"inviter_uid"`
	CreateTime     int64          `json:"create_time" msgpack:"create_time"`
	ExpireAt       int64          `json:"expire_at" msgpack:"expire_at"`
	PublisherMedia publishermedia.PublisherMedia `json:"publisher_media,omitempty" msgpack:"publisher_media,omitempty"`
}

type ServerStreamCallJoinSign struct {
	UID      string `json:"uid" msgpack:"uid"`
	SID      string `json:"sid" msgpack:"sid"`
	RID      string `json:"rid" msgpack:"rid"`
	Role     string `json:"role" msgpack:"role"`
	Nonce    string `json:"nonce" msgpack:"nonce"`
	ExpireAt int64  `json:"expire_at" msgpack:"expire_at"`
	Sign     string `json:"sign" msgpack:"sign"`
}

type ServerStreamCallJoin struct {
	CallID         string                   `json:"call_id" msgpack:"call_id"`
	Rid            string                   `json:"rid" msgpack:"rid"`
	CallType       string                   `json:"call_type" msgpack:"call_type"`
	CallScene      string                   `json:"call_scene" msgpack:"call_scene"`
	QuicAddr       string                   `json:"quic_addr" msgpack:"quic_addr"`
	ALPN           string                   `json:"alpn" msgpack:"alpn"`
	JoinSign       ServerStreamCallJoinSign `json:"join_sign" msgpack:"join_sign"`
	PublisherMedia publishermedia.PublisherMedia `json:"publisher_media,omitempty" msgpack:"publisher_media,omitempty"`
}

type ServerStreamCallEnd struct {
	CallID      string `json:"call_id" msgpack:"call_id"`
	Rid         string `json:"rid" msgpack:"rid"`
	Reason      string `json:"reason" msgpack:"reason"` // hangup / all_rejected / ended
	OperatorUid string `json:"operator_uid" msgpack:"operator_uid"`
	CallScene   string `json:"call_scene" msgpack:"call_scene"`
	CallType    string `json:"call_type" msgpack:"call_type"`
	InviterUid  string `json:"inviter_uid" msgpack:"inviter_uid"`
	DurationSec int64  `json:"duration_sec" msgpack:"duration_sec"`
}

// ServerStreamCallSync 通话在线人数同步（加入/离开/挂断）
type ServerStreamCallSync struct {
	CallID      string `json:"call_id" msgpack:"call_id"`
	Rid         string `json:"rid" msgpack:"rid"`
	ActiveCount int64  `json:"active_count" msgpack:"active_count"`
}
