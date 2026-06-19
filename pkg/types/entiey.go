package types

import (
	"github.com/xd/quic-server/db/entity"
	quicEntity "github.com/xd/quic-server/quic/handler/entity"
)

type User = entity.User
type UserRememberToken = entity.UserRememberToken
type UserRefreshToken = entity.UserRefreshToken
type UserFriendGroup = entity.UserFriendGroup
type UserRoomSession = entity.UserRoomSession
type Room = entity.Room
type RoomUser = entity.RoomUser
type RoomMessage = entity.RoomMessage
type RoomMessageContent = entity.RoomMessageContent
type RoomMessageAck = entity.RoomMessageAck
type RoomMessageWithdrawAck = entity.RoomMessageWithdrawAck
type RoomMessageMention = entity.RoomMessageMention
type UploadFile = entity.UploadFile
type UploadFileChunk = entity.UploadFileChunk
type UserUploadFile = entity.UserUploadFile

type ServerRoomMessage = quicEntity.ServerRoomMessage
type ServerRoomMessageContent = quicEntity.ServerRoomMessageContent
type ServerRoomMessageBatch = quicEntity.ServerRoomMessageBatch
type ServerRoomMessageWithdraw = quicEntity.ServerRoomMessageWithdraw
type ServerRoomMessageWithdrawBatch = quicEntity.ServerRoomMessageWithdrawBatch
type ServerMessageType = quicEntity.MessageType
type ServerMessageEntity = quicEntity.MessageEntity
type ServerFileStatusUpdate = quicEntity.ServerFileStatusUpdate
type ServerForceOffline = quicEntity.ServerForceOffline
type ServerUserStatusSync = quicEntity.ServerUserStatusSync
type ServerRoomUserPresenceSync = quicEntity.ServerRoomUserPresenceSync
type ServerNotification = quicEntity.ServerNotification
type ClientRoomMessage = quicEntity.ClientRoomMessage
type ClientRoomMessageContent = quicEntity.ClientRoomMessageContent

const (
	PrivateRoom      entity.RoomType = 0
	PublicRoom       entity.RoomType = 1
	GroupPrivateRoom entity.RoomType = 2 // 群私聊（从群内发起的临时私聊，互加好友后改为私聊）
	SelfChatRoom     entity.RoomType = 3 // 自聊（与自己对话/笔记）
)
const (
	RoomMessageContentTypeEmoji        entity.RoomMessageContentType = "emoji"
	RoomMessageContentTypeVideo        entity.RoomMessageContentType = "video"
	RoomMessageContentTypeAudio        entity.RoomMessageContentType = "audio"
	RoomMessageContentTypeText         entity.RoomMessageContentType = "text"
	RoomMessageContentTypeImage        entity.RoomMessageContentType = "image"
	RoomMessageContentTypeFile         entity.RoomMessageContentType = "file"
	RoomMessageContentTypeUserJoin     entity.RoomMessageContentType = "user:join"
	RoomMessageContentTypeUserLeave    entity.RoomMessageContentType = "user:leave"
	RoomMessageContentTypeRoomCreate   entity.RoomMessageContentType = "room:create"
	RoomMessageContentTypeFriendVerify entity.RoomMessageContentType = "friend:verify"
	// 用户撤回消息
	RoomMessageContentTypeUserWithdraw entity.RoomMessageContentType = "user:withdraw"
	// 用户引用消息
	RoomMessageContentTypeUserQuoteMessage entity.RoomMessageContentType = "user:quote:message"
	// 用户邀请加入群聊
	RoomMessageContentTypeUserInvite entity.RoomMessageContentType = "user:invite:join"
	// 用户修改群聊名称
	RoomMessageContentTypeUserUpdateRoomName entity.RoomMessageContentType = "user:update:room:name"
	// 房间成员角色变更（设为/取消管理员）
	RoomMessageContentTypeRoomMemberRoleUpdate entity.RoomMessageContentType = "room:member:role:update"
	// 转让群主
	RoomMessageContentTypeRoomOwnerTransfer entity.RoomMessageContentType = "room:owner:transfer"
	// 开启全体禁言
	RoomMessageContentTypeRoomMuteAllOn entity.RoomMessageContentType = "room:mute:all:on"
	// 关闭全体禁言
	RoomMessageContentTypeRoomMuteAllOff entity.RoomMessageContentType = "room:mute:all:off"
	// 指定用户禁言
	RoomMessageContentTypeRoomMuteUser entity.RoomMessageContentType = "room:mute:user"
	// 指定用户解除禁言
	RoomMessageContentTypeRoomUnmuteUser entity.RoomMessageContentType = "room:unmute:user"
	// 开启策略禁言
	RoomMessageContentTypeRoomMuteStrategyEnable entity.RoomMessageContentType = "room:mute:strategy:enable"
	// 关闭策略禁言
	RoomMessageContentTypeRoomMuteStrategyDisable entity.RoomMessageContentType = "room:mute:strategy:disable"
	// 修改策略禁言
	RoomMessageContentTypeRoomMuteStrategyUpdate entity.RoomMessageContentType = "room:mute:strategy:update"
	// 策略禁言开始（策略生效时间到点时发送，与频率无关）
	RoomMessageContentTypeRoomMuteStrategyStart entity.RoomMessageContentType = "room:mute:strategy_start"
	// 策略禁言结束（策略过期时间到点时发送，与频率无关）
	RoomMessageContentTypeRoomMuteStrategyEnd entity.RoomMessageContentType = "room:mute:strategy_end"
	// 用户@用户
	RoomMessageContentTypeUserAtUser entity.RoomMessageContentType = "user:at:user"
	// 用户@all
	RoomMessageContentTypeUserAtAll entity.RoomMessageContentType = "user:at:all"
	// 链接
	RoomMessageContentTypeLink entity.RoomMessageContentType = "link"
	// 房间邀请卡片（成员分享，含邀请 token 与展示信息）
	RoomMessageContentTypeRoomInviteLink entity.RoomMessageContentType = "room:invite:link"
)
