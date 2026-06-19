package entity

import (
	"encoding/json"

	"github.com/xd/quic-server/db/entity"
)

type MessageType uint8
type MessageEntity struct {
	Version     uint32      `json:"version" msgpack:"version"`
	MessageType MessageType `json:"message_type" msgpack:"message_type"`
	Token       string      `json:"token,omitempty" msgpack:"token,omitempty"`
	Data        []byte      `json:"data" msgpack:"data"`
}

const (
	TypeRoomMessage                MessageType = 1
	TypeRoomMessageAck             MessageType = 2
	TypeRoomMessageWithdrawAck     MessageType = 3
	TypeRoomMessageWithdraw        MessageType = 4
	TypeRoomMessageBatch           MessageType = 5
	TypeFileStatusUpdate           MessageType = 6
	TypeFileStatusUpdateAck        MessageType = 7
	TypeForceOffline               MessageType = 8
	TypeRoomMessageWithdrawBatch   MessageType = 9
	TypeRoomMessageWithdrawRequest MessageType = 10
	// TypeUserStatusSync 用户状态同步（通知客户端刷新会话/用户状态）
	TypeUserStatusSync MessageType = 11
	// TypeRoomUserPresenceSync 房间成员在线态同步（只更新在线/离线，不包含自定义状态）
	TypeRoomUserPresenceSync MessageType = 20
	// TypeNotification 消息通知（好友请求等）
	TypeNotification MessageType = 12
	// TypeUserInfo 用户资料（不含在线态；在线态见 UserStatusSync）
	TypeUserInfo MessageType = 13
	// TypeRoomMessageSendError 房间消息发送失败（如私聊非好友）
	TypeRoomMessageSendError MessageType = 14
	// TypeRoomUserRoomNicknameUpdate 房间内用户群昵称变更（不落库，仅广播给房间用户静默更新 UI）
	TypeRoomUserRoomNicknameUpdate MessageType = 15
	// TypeRoomUserIdsUpdate 房间成员列表变更（不落库，仅广播给房间用户静默更新 UI）
	TypeRoomUserIdsUpdate MessageType = 21
	// TypeRoomDissolvedUpdate 房间已解散（不落库，仅广播给房间用户静默更新 UI）
	TypeRoomDissolvedUpdate MessageType = 22
	// TypeRoomAvatarUpdate 房间头像变更（不落库，仅广播给房间用户静默更新 UI）
	TypeRoomAvatarUpdate MessageType = 18
	// TypeUserProfileChange 用户资料变更（懒推送，客户端按 uid 去重）
	TypeUserProfileChange MessageType = 19
	// TypeTokenRefreshRequired token 即将过期，要求客户端刷新
	TypeTokenRefreshRequired MessageType = 16
	// TypeInvalidMessageNotice 无效消息通知（用于排查版本/鉴权等问题）
	TypeInvalidMessageNotice MessageType = 17
	// TypeStreamCallInvite 流媒体通话邀请通知
	TypeStreamCallInvite MessageType = 24
	// TypeStreamCallJoin 下发流媒体通话加入签名
	TypeStreamCallJoin MessageType = 25
	// TypeStreamCallEnd 流媒体通话结束/关闭邀请
	TypeStreamCallEnd MessageType = 26
	// TypeStreamCallSync 通话在线人数同步
	TypeStreamCallSync MessageType = 27
)

type ClientRoomMessageContent struct {
	ClientCid string                        `json:"client_cid" msgpack:"client_cid"`
	Type      entity.RoomMessageContentType `json:"type" msgpack:"type"`
	TypeId    string                        `json:"type_id" msgpack:"type_id"`
	Content   json.RawMessage               `json:"content" msgpack:"content"`
}
type ClientRoomMessage struct {
	Rid        string                     `json:"rid" msgpack:"rid"`
	CreateTime int64                      `json:"create_time" msgpack:"create_time"`
	ClientMid  string                     `json:"client_mid" msgpack:"client_mid"`
	Contents   []ClientRoomMessageContent `json:"contents" msgpack:"contents"`
}

// ClientFileStatusUpdate 客户端上传完成后发起的文件状态更新
type ClientFileStatusUpdate struct {
	Rid       string          `json:"rid" msgpack:"rid"`
	ClientMid string          `json:"client_mid" msgpack:"client_mid"`
	ClientCid string          `json:"client_cid" msgpack:"client_cid"`
	UfId      string          `json:"uf_id" msgpack:"uf_id"`
	Content   json.RawMessage `json:"content" msgpack:"content"`
}

// ClientRoomMessageAck 客户端发送给服务器的房间消息ACK
type ClientRoomMessageAck struct {
	Mid string `json:"mid" msgpack:"mid"`
	Rid string `json:"rid" msgpack:"rid"`
}

// ClientRoomMessageWithdrawAck 客户端发送给服务器的房间消息撤回ACK
type ClientRoomMessageWithdrawAck struct {
	Mid string `json:"mid" msgpack:"mid"`
	Rid string `json:"rid" msgpack:"rid"`
}

// ClientRoomMessageWithdrawRequest 客户端发送给服务器的房间消息撤回请求
type ClientRoomMessageWithdrawRequest struct {
	Rid   string `json:"rid" msgpack:"rid"`
	SeqId int64  `json:"seq_id" msgpack:"seq_id"`
}

// ClientFileStatusUpdateAck 客户端发送给服务器的文件状态更新ACK
type ClientFileStatusUpdateAck struct {
	Rid       string `json:"rid" msgpack:"rid"`
	ClientMid string `json:"client_mid" msgpack:"client_mid"`
	ClientCid string `json:"client_cid" msgpack:"client_cid"`
}

// ServerRoomMessageAck 服务器发送给客户端的房间消息ACK确认
type ServerRoomMessageAck struct {
	Mid       string `json:"mid" msgpack:"mid"`
	Rid       string `json:"rid" msgpack:"rid"`
	SeqId     int64  `json:"seq_id" msgpack:"seq_id"`
	ClientMid string `json:"client_mid" msgpack:"client_mid"`
}

// ServerTokenRefreshRequired token 即将过期提醒
type ServerTokenRefreshRequired struct {
	ExpiresAt        int64  `json:"expires_at" msgpack:"expires_at"`
	RemainingSeconds int64  `json:"remaining_seconds" msgpack:"remaining_seconds"`
	Message          string `json:"message" msgpack:"message"`
}

// ServerInvalidMessageNotice 无效消息通知
type ServerInvalidMessageNotice struct {
	Code            string `json:"code" msgpack:"code"`
	Message         string `json:"message" msgpack:"message"`
	ReceivedVersion uint32 `json:"received_version" msgpack:"received_version"`
	LastVersion     uint32 `json:"last_version" msgpack:"last_version"`
	MessageType     uint8  `json:"message_type" msgpack:"message_type"`
	ServerTime      int64  `json:"server_time" msgpack:"server_time"`
}
