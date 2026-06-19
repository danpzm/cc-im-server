package user

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
)

type FriendChatRequest struct {
	FriendUid string `json:"friend_uid" binding:"required"`
}

type GroupPrivateChatRequest struct {
	TargetUid string `json:"target_uid" binding:"required"`
	FromRid   string `json:"from_rid" binding:"required"` // 当前群聊房间 rid，用于标识群私聊来源
}

// StartFriendChat 打开与目标用户的私聊：是好友则直接进房间；非好友则校验对方是否允许非好友私聊，允许则创建/复用房间并记录操作。
func StartFriendChat(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	var req FriendChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}

	if req.FriendUid == "" {
		response.BadRequest(c, "friend_uid 必填")
		return
	}
	// 向自己发消息：使用独立的自聊房间类型（type=3）
	if req.FriendUid == user.Uid {
		session, err := query.EnsureSelfChatSession(user.Uid)
		if err != nil {
			log.Errorf("StartFriendChat EnsureSelfChatSession 失败 uid=%s: %v", user.Uid, err)
			response.ServerError(c, "打开与自己的对话失败")
			return
		}
		if session == nil {
			response.ServerError(c, "未能创建或获取有效会话")
			return
		}
		response.Success(c, session)
		return
	}

	session, createdForNonFriend, err := query.OpenPrivateChat(user.Uid, req.FriendUid)
	if err != nil {
		if errors.Is(err, query.ErrNotFriend) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "对方已不是你的好友，请刷新列表后重试"})
			return
		}
		if errors.Is(err, query.ErrPrivateChatNotAllowed) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "对方未开启非好友私聊，请先添加好友"})
			return
		}
		log.Errorf("StartFriendChat OpenPrivateChat 失败 uid=%s target_uid=%s: %v", user.Uid, req.FriendUid, err)
		response.ServerError(c, "创建聊天会话失败")
		return
	}

	if session == nil {
		response.ServerError(c, "未能创建或获取有效会话")
		return
	}

	if createdForNonFriend {
		beforeData, _ := json.Marshal(map[string]any{})
		afterData, _ := json.Marshal(map[string]any{"rid": session.Rid, "target_uid": req.FriendUid})
		_ = db.GetDB().Create(&entity.RoomAdminOperation{
			Rid:         session.Rid,
			OpType:      entity.RoomAdminOpPrivateChatCreate,
			OperatorUid: user.Uid,
			Sid:         helper.GetSid(c),
			RelatedId:   req.FriendUid,
			BeforeData:  string(beforeData),
			AfterData:   string(afterData),
		}).Error
	}

	response.Success(c, session)
}

// StartGroupPrivateChat 从群内发起与某成员的群私聊：创建或复用 type=2 房间，并返回当前用户会话。
func StartGroupPrivateChat(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var req GroupPrivateChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}
	if req.TargetUid == "" || req.FromRid == "" {
		response.BadRequest(c, "target_uid 与 from_rid 必填")
		return
	}
	// 校验当前用户在该群内
	var count int64
	if db.GetDB().Table("room_user").Where("rid = ? AND uid = ? AND delete_time = 0", req.FromRid, user.Uid).Count(&count).Error != nil || count == 0 {
		response.BadRequest(c, "非群成员无法发起群私聊")
		return
	}
	// 目标为自己：不走群私聊，打开自聊房间（与 StartFriendChat 自聊一致）
	if req.TargetUid == user.Uid {
		session, err := query.EnsureSelfChatSession(user.Uid)
		if err != nil {
			log.Errorf("StartGroupPrivateChat EnsureSelfChatSession 失败 uid=%s: %v", user.Uid, err)
			response.ServerError(c, "打开与自己的对话失败")
			return
		}
		if session == nil {
			response.ServerError(c, "未能创建或获取有效会话")
			return
		}
		response.Success(c, session)
		return
	}
	var targetInRoom int64
	if db.GetDB().Table("room_user").Where("rid = ? AND uid = ? AND delete_time = 0", req.FromRid, req.TargetUid).Count(&targetInRoom).Error != nil || targetInRoom == 0 {
		response.BadRequest(c, "对方不在该群内")
		return
	}
	session, err := query.OpenGroupPrivateChat(user.Uid, req.TargetUid, req.FromRid)
	if err != nil {
		log.Errorf("StartGroupPrivateChat OpenGroupPrivateChat 失败 uid=%s target=%s from_rid=%s: %v", user.Uid, req.TargetUid, req.FromRid, err)
		response.ServerError(c, "创建群私聊会话失败")
		return
	}
	if session == nil {
		response.ServerError(c, "未能创建或获取有效会话")
		return
	}
	response.Success(c, session)
}
