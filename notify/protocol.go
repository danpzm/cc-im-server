package notify

import (
	"github.com/xd/quic-server/pkg/publishermedia"
)

// MessageType 跨进程通知类型（HTTP/队列经 Redis 投递，由 QUIC 节点消费）
type MessageType string

const (
	MessageTypeRoomMessageWithdrawNotify MessageType = "room_message_withdraw_notify"
	MessageTypeRoomMessageNotify         MessageType = "room_message_notify"
	MessageTypeRoomMessageNotifyIncludeUids MessageType = "room_message_notify_include_uids"
	MessageTypeRoomMessageNotifyExcludeUids MessageType = "room_message_notify_exclude_uids"
	MessageTypeNotificationNotify        MessageType = "notification_notify"
	MessageTypeRoomUserRoomNicknameNotify MessageType = "room_user_room_nickname_notify"
	MessageTypeRoomUserIdsNotify         MessageType = "room_user_ids_notify"
	MessageTypeRoomDissolvedNotify       MessageType = "room_dissolved_notify"
	MessageTypeRoomAvatarNotify          MessageType = "room_avatar_notify"
	MessageTypeUserProfileNotify         MessageType = "user_profile_notify"
	MessageTypeUserStatusSyncNotify      MessageType = "user_status_sync_notify"
	MessageTypeStreamCallInviteNotify    MessageType = "stream_call_invite_notify"
	MessageTypeStreamCallJoinNotify      MessageType = "stream_call_join_notify"
	MessageTypeStreamCallEndNotify       MessageType = "stream_call_end_notify"
	MessageTypeStreamCallSyncNotify      MessageType = "stream_call_sync_notify"
	MessageTypeRoomMessageDeliver        MessageType = "room_message_deliver"
	MessageTypeRoomMessageWithdrawDeliver MessageType = "room_message_withdraw_deliver"
	MessageTypeFileStatusUpdateDeliver   MessageType = "file_status_update_deliver"
)

// Message 一条通知（序列化进 push.Envelope）
type Message struct {
	Type    MessageType `json:"type"`
	Payload any         `json:"payload"`
}

type RoomMessageWithdrawNotifyPayload struct {
	Mid string `json:"mid"`
}

type RoomMessageNotifyPayload struct {
	Mid string `json:"mid"`
}

type RoomMessageDeliverPayload struct {
	Mid        string   `json:"mid"`
	TargetUids []string `json:"target_uids"`
}

type RoomMessageWithdrawDeliverPayload struct {
	Mid        string   `json:"mid"`
	TargetUids []string `json:"target_uids"`
}

type FileStatusUpdateDeliverPayload struct {
	Mid        string   `json:"mid"`
	Cid        string   `json:"cid"`
	TargetUids []string `json:"target_uids"`
}

type RoomMessageNotifyIncludeUidsPayload struct {
	Mid     string   `json:"mid"`
	UidList []string `json:"uid_list"`
}

type RoomMessageNotifyExcludeUidsPayload struct {
	Mid            string   `json:"mid"`
	ExcludeUidList []string `json:"exclude_uid_list"`
}

type NotificationNotifyPayload struct {
	Nid string `json:"nid"`
}

type RoomUserRoomNicknameNotifyPayload struct {
	Rid          string   `json:"rid"`
	Uid          string   `json:"uid"`
	RoomNickname string   `json:"room_nickname"`
	TargetUids   []string `json:"target_uids"`
}

type RoomUserIdsNotifyPayload struct {
	Rid        string   `json:"rid"`
	UserIds    []string `json:"user_ids"`
	TargetUids []string `json:"target_uids"`
}

type RoomDissolvedNotifyPayload struct {
	Rid        string `json:"rid"`
	RoomState  int8   `json:"room_state"`
	TargetUids []string `json:"target_uids"`
}

type RoomAvatarNotifyPayload struct {
	Rid        string   `json:"rid"`
	AvatarUfId string   `json:"avatar_uf_id"`
	TargetUids []string `json:"target_uids"`
}

type UserProfileNotifyUser struct {
	Uid          string `json:"uid" msgpack:"uid"`
	Username     string `json:"username" msgpack:"username"`
	Nickname     string `json:"nickname" msgpack:"nickname"`
	Signature    string `json:"signature" msgpack:"signature"`
	Introduction string `json:"introduction" msgpack:"introduction"`
	Email        string `json:"email" msgpack:"email"`
	AvatarUfId   string `json:"avatar_uf_id" msgpack:"avatar_uf_id"`
	CreateTime   int64  `json:"create_time" msgpack:"create_time"`
}

type UserProfileNotifyPayload struct {
	User       UserProfileNotifyUser `json:"user"`
	TargetUids []string               `json:"target_uids"`
}

type UserStatusSyncNotifyPayload struct {
	Uid        string   `json:"uid"`
	TargetUids []string `json:"target_uids"`
}

type StreamCallInviteNotifyPayload struct {
	CallID         string                        `json:"call_id"`
	Rid            string                        `json:"rid"`
	CallType       string                        `json:"call_type"`
	CallScene      string                        `json:"call_scene"`
	InviterUID     string                        `json:"inviter_uid"`
	InviteeUID     []string                      `json:"invitee_uids"`
	CreateTime     int64                         `json:"create_time"`
	ExpireAt       int64                         `json:"expire_at"`
	PublisherMedia publishermedia.PublisherMedia `json:"publisher_media,omitempty"`
}

type StreamCallJoinSign struct {
	UID      string `json:"uid"`
	SID      string `json:"sid"`
	RID      string `json:"rid"`
	Role     string `json:"role"`
	Nonce    string `json:"nonce"`
	ExpireAt int64  `json:"expire_at"`
	Sign     string `json:"sign"`
}

type StreamCallJoinNotifyPayload struct {
	CallID         string                        `json:"call_id"`
	Rid            string                        `json:"rid"`
	CallType       string                        `json:"call_type"`
	CallScene      string                        `json:"call_scene"`
	TargetUID      string                        `json:"target_uid"`
	QuicAddr       string                        `json:"quic_addr"`
	ALPN           string                        `json:"alpn"`
	JoinSign       StreamCallJoinSign            `json:"join_sign"`
	PublisherMedia publishermedia.PublisherMedia `json:"publisher_media,omitempty"`
}

type StreamCallEndNotifyPayload struct {
	CallID      string   `json:"call_id"`
	Rid         string   `json:"rid"`
	Reason      string   `json:"reason"`
	OperatorUID string   `json:"operator_uid"`
	CallScene   string   `json:"call_scene"`
	CallType    string   `json:"call_type"`
	InviterUID  string   `json:"inviter_uid"`
	TargetUIDs  []string `json:"target_uids"`
	DurationSec int64    `json:"duration_sec"`
}

type StreamCallSyncNotifyPayload struct {
	CallID      string   `json:"call_id"`
	Rid         string   `json:"rid"`
	ActiveCount int      `json:"active_count"`
	TargetUIDs  []string `json:"target_uids"`
}
