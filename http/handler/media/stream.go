package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/jwt"
	"github.com/xd/quic-server/pkg/publishermedia"
	"github.com/xd/quic-server/pkg/serviceregistry"
	"github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/notify"
)

const (
	mediaJoinSignTTLDefault     = 45 * time.Second
	mediaJoinSignTTLMin         = 10 * time.Second
	mediaJoinSignTTLMax         = 2 * time.Minute
	mediaJoinSignRedisKey       = "media:join:sign:%s"
	mediaALPN                   = "cc-media-v1"
	mediaCallInviteRedisKey     = "media:call:invite:%s"
	mediaCallStateRedisKey      = "media:call:state:%s"
	mediaCallRoomActiveRedisKey = "media:call:room:active:%s"
	// mediaCallUserActiveRedisKey：uid -> call_id，用于主 QUIC 下线时服务端兜底离房（与 MarkCallMemberJoined 成对维护）
	mediaCallUserActiveRedisKey = "media:call:user:active:%s"
)

type leaveCallStatus int

const (
	leaveCallExpired leaveCallStatus = iota
	leaveCallUnauthorized
	leaveCallOK
)

type mediaJoinSignPayload struct {
	Rid      string `json:"rid"`
	Role     string `json:"role"`
	TTL      int64  `json:"ttl_seconds"`
	ClientTs int64  `json:"client_ts"`
}

type mediaJoinSignData struct {
	UID      string `json:"uid"`
	SID      string `json:"sid"`
	RID      string `json:"rid"`
	Role     string `json:"role"`
	Nonce    string `json:"nonce"`
	ExpireAt int64  `json:"expire_at"`
	Sign     string `json:"sign"`
}

type mediaCallInviteCreatePayload struct {
	Rid            string                        `json:"rid"`
	CallType       string                        `json:"call_type"` // audio / video
	TTL            int64                         `json:"ttl_seconds"`
	PublisherMedia publishermedia.PublisherMedia `json:"publisher_media"`
}

type mediaCallAcceptPayload struct {
	CallID string `json:"call_id"`
}

type mediaCallInviteData struct {
	CallID         string                        `json:"call_id"`
	Rid            string                        `json:"rid"`
	CallType       string                        `json:"call_type"`
	CallScene      string                        `json:"call_scene"` // friend / room
	InviterUID     string                        `json:"inviter_uid"`
	InviteeUIDs    []string                      `json:"invitee_uids"`
	CreateTime     int64                         `json:"create_time"`
	ExpireAt       int64                         `json:"expire_at"`
	PublisherMedia publishermedia.PublisherMedia `json:"publisher_media,omitempty"`
}

type mediaCallStateData struct {
	CallID         string                        `json:"call_id"`
	Rid            string                        `json:"rid"`
	CallType       string                        `json:"call_type"`
	CallScene      string                        `json:"call_scene"` // friend / room
	InviterUID     string                        `json:"inviter_uid"`
	InviteeUIDs    []string                      `json:"invitee_uids"`
	ActiveUIDs     []string                      `json:"active_uids"`
	RejectedUIDs   []string                      `json:"rejected_uids"`
	Ended          bool                          `json:"ended"`
	CreateTime     int64                         `json:"create_time"`
	ExpireAt       int64                         `json:"expire_at"`
	PublisherMedia publishermedia.PublisherMedia `json:"publisher_media,omitempty"`
}

type mediaCallMemberStatePayload struct {
	CallID string `json:"call_id"`
}

type mediaCallCurrentPayload struct {
	Rid string `form:"rid" json:"rid"`
}

func setUserActiveCallMapping(uid, callID string, expireAt int64) {
	uid = strings.TrimSpace(uid)
	callID = strings.TrimSpace(callID)
	if uid == "" || callID == "" {
		return
	}
	_ = redis.SetString(fmt.Sprintf(mediaCallUserActiveRedisKey, uid), callID, callStateTTL(expireAt))
}

func clearUserActiveCallMapping(uid string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	_ = redis.Delete(fmt.Sprintf(mediaCallUserActiveRedisKey, uid))
}

func clearAllUserActiveCallMappingsForCall(callData mediaCallInviteData, state *mediaCallStateData) {
	uids := []string{callData.InviterUID}
	uids = append(uids, callData.InviteeUIDs...)
	if state != nil && state.CallID != "" {
		uids = append(uids, state.ActiveUIDs...)
		uids = append(uids, state.RejectedUIDs...)
	}
	for _, u := range uniqueUids(uids) {
		clearUserActiveCallMapping(u)
	}
}

// destroyMediaCallSessionKeys 销毁一次通话在 Redis 中的会话数据（邀请 / 状态 / 房间当前通话指针）
func destroyMediaCallSessionKeys(callID, rid string) {
	callID = strings.TrimSpace(callID)
	rid = strings.TrimSpace(rid)
	if callID != "" {
		_ = redis.Delete(fmt.Sprintf(mediaCallInviteRedisKey, callID))
		_ = redis.Delete(fmt.Sprintf(mediaCallStateRedisKey, callID))
	}
	if rid != "" {
		_ = redis.Delete(fmt.Sprintf(mediaCallRoomActiveRedisKey, rid))
	}
}

// finalizeMediaCallSessionNoActive 已无任何在线成员（ActiveUIDs 为空）时结束通话：广播、下发结束、销毁 Redis 会话。
func finalizeMediaCallSessionNoActive(callData mediaCallInviteData, state *mediaCallStateData, operatorUID string) {
	if state == nil || state.Ended {
		return
	}
	state.Ended = true
	stateKey := fmt.Sprintf(mediaCallStateRedisKey, callData.CallID)
	_ = redis.Set(stateKey, state, callStateTTL(callData.ExpireAt))
	clearAllUserActiveCallMappingsForCall(callData, state)
	broadcastStreamCallMemberSync(callData.CallID)
	targets := make([]string, 0, 1+len(callData.InviteeUIDs))
	targets = append(targets, callData.InviterUID)
	targets = append(targets, callData.InviteeUIDs...)
	targets = uniqueUids(targets)
	_ = helper.NotifyQuic(notify.MessageTypeStreamCallEndNotify, notify.StreamCallEndNotifyPayload{
		CallID:      callData.CallID,
		Rid:         callData.Rid,
		Reason:      "ended",
		OperatorUID: operatorUID,
		CallScene:   callData.CallScene,
		CallType:    callData.CallType,
		InviterUID:  callData.InviterUID,
		TargetUIDs:  targets,
		DurationSec: callDurationSeconds(callData),
	})
	saveMediaCallRecord(callData, state, "ended", operatorUID)
	destroyMediaCallSessionKeys(callData.CallID, callData.Rid)
}

// saveMediaCallRecord 将一次媒体通话持久化到数据库，用于历史记录和审计。
func saveMediaCallRecord(callData mediaCallInviteData, state *mediaCallStateData, reason, operatorUID string) {
	inviteesJSON, err := json.Marshal(callData.InviteeUIDs)
	if err != nil {
		log.Errorf("saveMediaCallRecord: marshal invitees failed call_id=%s err=%v", callData.CallID, err)
		return
	}
	endedAt := time.Now().UnixMilli()
	durationSec := callDurationSeconds(callData)
	record := entity.MediaCallRecord{
		CallID:      callData.CallID,
		Rid:         callData.Rid,
		CallType:    callData.CallType,
		CallScene:   callData.CallScene,
		InviterUID:  callData.InviterUID,
		InviteeUIDs: string(inviteesJSON),
		StartedAt:   callData.CreateTime,
		EndedAt:     endedAt,
		DurationSec: durationSec,
		EndReason:   reason,
		OperatorUID: operatorUID,
	}
	if err := db.GetDB().Create(&record).Error; err != nil {
		log.Errorf("saveMediaCallRecord: db create failed call_id=%s err=%v", callData.CallID, err)
	}
}

// leaveCallByUID 从 ActiveUIDs 移除 uid 并广播 sync。
// 返回值 recall 恒为 false：服务端不再自动回呼下发邀请，避免结束/离开时重复触发新来电。
func leaveCallByUID(uid, callID string, skipRecall bool) (recall bool, st leaveCallStatus) {
	uid = strings.TrimSpace(uid)
	callID = strings.TrimSpace(callID)
	if uid == "" || callID == "" {
		return false, leaveCallExpired
	}
	callKey := fmt.Sprintf(mediaCallInviteRedisKey, callID)
	callData, err := redis.Get[mediaCallInviteData](callKey)
	if err != nil || callData.CallID == "" || isCallExpired(callData.ExpireAt) {
		clearUserActiveCallMapping(uid)
		return false, leaveCallExpired
	}
	if !allowCallMember(callData, uid) {
		return false, leaveCallUnauthorized
	}
	stateKey := fmt.Sprintf(mediaCallStateRedisKey, callID)
	state, _ := redis.Get[mediaCallStateData](stateKey)
	if state.CallID == "" {
		state = mediaCallStateData{
			CallID:         callData.CallID,
			Rid:            callData.Rid,
			CallType:       callData.CallType,
			CallScene:      callData.CallScene,
			InviterUID:     callData.InviterUID,
			InviteeUIDs:    callData.InviteeUIDs,
			ActiveUIDs:     []string{},
			RejectedUIDs:   []string{},
			Ended:          false,
			CreateTime:     callData.CreateTime,
			ExpireAt:       callData.ExpireAt,
			PublisherMedia: callData.PublisherMedia,
		}
	}
	state.ActiveUIDs = removeUid(state.ActiveUIDs, uid)
	_ = redis.Set(stateKey, state, callStateTTL(callData.ExpireAt))
	clearUserActiveCallMapping(uid)
	if len(state.ActiveUIDs) == 0 && !state.Ended {
		finalizeMediaCallSessionNoActive(callData, &state, uid)
		return false, leaveCallOK
	}
	broadcastStreamCallMemberSync(callData.CallID)
	_ = skipRecall
	return false, leaveCallOK
}

// OnUserMessageQuicOffline 主 QUIC 正常关闭或心跳超时触发用户下线时调用：若该用户仍在媒体通话中，则按 leave 逻辑更新 Redis 并广播人数，且不触发回呼邀请。
func OnUserMessageQuicOffline(uid string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	callID, err := redis.GetString(fmt.Sprintf(mediaCallUserActiveRedisKey, uid))
	if err != nil || strings.TrimSpace(callID) == "" {
		return
	}
	recall, st := leaveCallByUID(uid, callID, true)
	if st == leaveCallOK {
		log.Infof("主 QUIC 下线兜底媒体离房 uid=%s call_id=%s recall_would_be=%v (skipped)", uid, callID, recall)
	}
}

// OnMediaRoomEmpty 当媒体 QUIC 房间内所有客户端均断开时触发兜底清理：
// 解决“媒体房间人数为 0 但通话状态/房间指针未结束”的阻塞问题（尤其是异常下线 + 快速重登）。
//
// 触发条件优先依赖：in-memory 房间确实已为空；
// 进一步在 Redis 侧仅当 state.ActiveUIDs 为空（认为没有任何人已成功进入媒体通话）时，才会结束通话并清理会话键。
func OnMediaRoomEmpty(rid, operatorUID string) {
	rid = strings.TrimSpace(rid)
	operatorUID = strings.TrimSpace(operatorUID)
	if rid == "" {
		return
	}

	activeKey := fmt.Sprintf(mediaCallRoomActiveRedisKey, rid)
	callID, err := redis.GetString(activeKey)
	if err != nil || strings.TrimSpace(callID) == "" {
		return
	}

	callKey := fmt.Sprintf(mediaCallInviteRedisKey, callID)
	callData, err := redis.Get[mediaCallInviteData](callKey)
	if err != nil || callData.CallID == "" {
		// Redis 状态残留：至少清理 room 指针及邀请/状态键。
		_ = redis.Delete(activeKey)
		destroyMediaCallSessionKeys(callID, rid)
		return
	}

	if isCallExpired(callData.ExpireAt) {
		_ = redis.Delete(activeKey)
		destroyMediaCallSessionKeys(callData.CallID, callData.Rid)
		return
	}

	stateKey := fmt.Sprintf(mediaCallStateRedisKey, callID)
	state, _ := redis.Get[mediaCallStateData](stateKey)
	if state.CallID == "" {
		state = mediaCallStateData{
			CallID:         callData.CallID,
			Rid:            callData.Rid,
			CallType:       callData.CallType,
			CallScene:      callData.CallScene,
			InviterUID:     callData.InviterUID,
			InviteeUIDs:    callData.InviteeUIDs,
			ActiveUIDs:     []string{},
			RejectedUIDs:   []string{},
			Ended:          false,
			CreateTime:     callData.CreateTime,
			ExpireAt:       callData.ExpireAt,
			PublisherMedia: callData.PublisherMedia,
		}
	}

	if state.Ended {
		_ = redis.Delete(activeKey)
		return
	}
	if len(state.ActiveUIDs) > 0 {
		// Redis 侧认为仍有媒体参与者：做一次更强的兜底校正。
		// 逐个检查这些 uid 是否仍然将本通话标记为 active；若否，则视为脏数据并清理。
		activeAlive := make([]string, 0, len(state.ActiveUIDs))
		for _, uid := range state.ActiveUIDs {
			uid = strings.TrimSpace(uid)
			if uid == "" {
				continue
			}
			userKey := fmt.Sprintf(mediaCallUserActiveRedisKey, uid)
			mappedCallID, _ := redis.GetString(userKey)
			if strings.TrimSpace(mappedCallID) == callData.CallID {
				activeAlive = append(activeAlive, uid)
			} else {
				// 已不再真正处于本通话中的 uid：清理可能残留的映射，防止后续创建新通话被阻塞。
				clearUserActiveCallMapping(uid)
			}
		}
		state.ActiveUIDs = activeAlive
		stateKey := fmt.Sprintf(mediaCallStateRedisKey, callData.CallID)
		_ = redis.Set(stateKey, state, callStateTTL(callData.ExpireAt))
		if len(activeAlive) > 0 {
			// 兜底校正后仍有「真正活跃」的媒体成员：避免误结束。
			return
		}
	}

	finalizeMediaCallSessionNoActive(callData, &state, operatorUID)
}

func IssueJoinSign(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	sid := helper.GetSid(c)
	if sid == "" {
		response.Unauthorized(c, "会话无效，请重新登录")
		return
	}

	var req mediaJoinSignPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数错误")
		return
	}
	req.Rid = strings.TrimSpace(req.Rid)
	req.Role = strings.TrimSpace(req.Role)
	if req.Rid == "" {
		response.BadRequest(c, "rid 不能为空")
		return
	}
	if req.Role == "" {
		response.BadRequest(c, "role 不能为空")
		return
	}
	if req.Role != "publisher" && req.Role != "subscriber" {
		response.BadRequest(c, "role 仅支持 publisher/subscriber")
		return
	}
	if req.TTL <= 0 {
		response.BadRequest(c, "ttl_seconds 必须大于 0")
		return
	}
	ttl := time.Duration(req.TTL) * time.Second
	if ttl < mediaJoinSignTTLMin || ttl > mediaJoinSignTTLMax {
		response.BadRequest(c, fmt.Sprintf("ttl_seconds 超出允许范围 [%d, %d] 秒", int64(mediaJoinSignTTLMin/time.Second), int64(mediaJoinSignTTLMax/time.Second)))
		return
	}

	now := time.Now()
	nonce := uuid.NewString()
	expireAt := now.Add(ttl).UnixMilli()
	sign := makeMediaJoinSign(user.Uid, sid, req.Rid, req.Role, nonce, expireAt)

	signData := mediaJoinSignData{
		UID:      user.Uid,
		SID:      sid,
		RID:      req.Rid,
		Role:     req.Role,
		Nonce:    nonce,
		ExpireAt: expireAt,
		Sign:     sign,
	}
	if err := redis.Set(fmt.Sprintf(mediaJoinSignRedisKey, nonce), signData, ttl); err != nil {
		response.ServerError(c, "签名生成失败")
		return
	}

	serverCfg := config.GetServerConfig()
	publicMediaQuicAddr := pickClusterMediaQuicAddr(c.Request.Context(), serverCfg)
	response.Success(c, gin.H{
		"join_sign": signData,
		"quic_addr": publicMediaQuicAddr,
		"alpn":      mediaALPN,
		"ttl_ms":    ttl.Milliseconds(),
	})
}

func CreateCallInvite(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	sid := helper.GetSid(c)
	if sid == "" {
		response.Unauthorized(c, "会话无效，请重新登录")
		return
	}
	var req mediaCallInviteCreatePayload
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数错误")
		return
	}
	req.Rid = strings.TrimSpace(req.Rid)
	req.CallType = strings.TrimSpace(req.CallType)
	if req.Rid == "" {
		response.BadRequest(c, "rid 不能为空")
		return
	}
	if req.CallType != "audio" && req.CallType != "video" {
		response.BadRequest(c, "call_type 仅支持 audio/video")
		return
	}
	if req.TTL <= 0 {
		response.BadRequest(c, "ttl_seconds 必须大于 0")
		return
	}
	if !req.PublisherMedia.Valid() {
		response.BadRequest(c, "publisher_media 无效（需包含 width/height/fps/bitrate 且在允许范围内）")
		return
	}
	room, err := query.GetRoomByRid(req.Rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	roomUserIds, err := query.GetRoomUserIdsCache(req.Rid)
	if err != nil || len(roomUserIds) == 0 {
		response.BadRequest(c, "房间不存在或无成员")
		return
	}
	inviteeUIDs := make([]string, 0, len(roomUserIds))
	for _, uid := range roomUserIds {
		if uid != "" && uid != user.Uid {
			inviteeUIDs = append(inviteeUIDs, uid)
		}
	}
	if len(inviteeUIDs) == 0 {
		response.BadRequest(c, "无可邀请成员")
		return
	}
	// 仅允许两类场景：
	// 1) 公开群聊（type=1）
	// 2) 好友私聊（type=0，且双方互为好友）
	switch room.Type {
	case entity.RoomTypeGroup:
		// allow
	case entity.RoomTypePrivate:
		if len(inviteeUIDs) != 1 {
			response.BadRequest(c, "私聊媒体通话仅支持双方通话")
			return
		}
		areFriends, _, ferr := query.CheckFriendRelation(user.Uid, inviteeUIDs[0])
		if ferr != nil {
			response.ServerError(c, "校验好友关系失败")
			return
		}
		if !areFriends {
			response.BadRequest(c, "仅好友私聊支持媒体通话")
			return
		}
	default:
		response.BadRequest(c, "仅公开群聊和好友私聊支持媒体通话")
		return
	}
	callScene := "room"
	switch room.Type {
	case entity.RoomTypeGroup:
		callScene = "room"
	case entity.RoomTypePrivate:
		callScene = "friend"
	}
	// 同一个房间仅允许一个未销毁通话
	roomActiveKey := fmt.Sprintf(mediaCallRoomActiveRedisKey, req.Rid)
	if activeCallID, err := redis.GetString(roomActiveKey); err == nil && strings.TrimSpace(activeCallID) != "" {
		// 已存在通话：根据状态决定是拒绝还是强制结束旧通话后重建。
		stateKey := fmt.Sprintf(mediaCallStateRedisKey, activeCallID)
		activeState, aerr := redis.Get[mediaCallStateData](stateKey)
		if aerr == nil && activeState.CallID != "" && !activeState.Ended && len(activeState.ActiveUIDs) > 0 {
			// Redis 认为仍有媒体参与者：拒绝在同一房间启动新通话。
			response.BadRequest(c, "当前房间已有进行中的通话")
			return
		}
		// 无在线成员或状态已结束：强制结束旧通话并清理 Redis，再创建新的通话。
		callKey := fmt.Sprintf(mediaCallInviteRedisKey, activeCallID)
		oldCallData, cerr := redis.Get[mediaCallInviteData](callKey)
		if cerr == nil && oldCallData.CallID != "" {
			finalizeMediaCallSessionNoActive(oldCallData, &activeState, user.Uid)
		} else {
			// 状态不完整时至少清理房间指针与会话键，避免阻塞新通话。
			destroyMediaCallSessionKeys(activeCallID, req.Rid)
		}
	}

	now := time.Now()
	expireAt := int64(0) // 0 表示通话邀请永不过期，直到显式挂断/结束
	callID := uuid.NewString()
	callData := mediaCallInviteData{
		CallID:         callID,
		Rid:            req.Rid,
		CallType:       req.CallType,
		CallScene:      callScene,
		InviterUID:     user.Uid,
		InviteeUIDs:    inviteeUIDs,
		CreateTime:     now.UnixMilli(),
		ExpireAt:       expireAt,
		PublisherMedia: req.PublisherMedia,
	}
	if err := redis.Set(fmt.Sprintf(mediaCallInviteRedisKey, callID), callData, 0); err != nil {
		response.ServerError(c, "创建通话邀请失败")
		return
	}
	_ = redis.Set(fmt.Sprintf(mediaCallStateRedisKey, callID), mediaCallStateData{
		CallID:         callData.CallID,
		Rid:            callData.Rid,
		CallType:       callData.CallType,
		CallScene:      callData.CallScene,
		InviterUID:     callData.InviterUID,
		InviteeUIDs:    callData.InviteeUIDs,
		ActiveUIDs:     []string{},
		RejectedUIDs:   []string{},
		Ended:          false,
		CreateTime:     callData.CreateTime,
		ExpireAt:       callData.ExpireAt,
		PublisherMedia: callData.PublisherMedia,
	}, 0)
	_ = redis.SetString(roomActiveKey, callID, 0)
	// 发起者 publisher 端 join_sign（仅通过 QUIC 下发，不出现在 HTTP 响应中）
	signData, err := issueJoinSignData(user.Uid, sid, req.Rid, "publisher", mediaJoinSignTTLDefault)
	if err != nil {
		response.ServerError(c, "创建发起者签名失败")
		return
	}
	// 不在 HTTP 返回签名：通过 QUIC(聊天通道)下发给发起者，让其再去创建媒体 QUIC
	serverCfg := config.GetServerConfig()
	publicMediaQuicAddr := pickClusterMediaQuicAddr(c.Request.Context(), serverCfg)
	// 通过 Redis 定向推送 -> QUIC
	_ = helper.NotifyQuic(notify.MessageTypeStreamCallJoinNotify, notify.StreamCallJoinNotifyPayload{
		CallID:         callID,
		Rid:            req.Rid,
		CallType:       req.CallType,
		CallScene:      callScene,
		TargetUID:      user.Uid,
		QuicAddr:       publicMediaQuicAddr,
		ALPN:           mediaALPN,
		PublisherMedia: callData.PublisherMedia,
		JoinSign: notify.StreamCallJoinSign{
			UID:      signData.UID,
			SID:      signData.SID,
			RID:      signData.RID,
			Role:     signData.Role,
			Nonce:    signData.Nonce,
			ExpireAt: signData.ExpireAt,
			Sign:     signData.Sign,
		},
	})
	_ = helper.NotifyQuic(notify.MessageTypeStreamCallInviteNotify, notify.StreamCallInviteNotifyPayload{
		CallID:         callID,
		Rid:            req.Rid,
		CallType:       req.CallType,
		CallScene:      callScene,
		InviterUID:     user.Uid,
		InviteeUID:     inviteeUIDs,
		CreateTime:     now.UnixMilli(),
		ExpireAt:       expireAt,
		PublisherMedia: callData.PublisherMedia,
	})
	response.Success(c, gin.H{
		"call_id": callID,
	})
}

func MarkCallMemberJoined(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var req mediaCallMemberStatePayload
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.CallID) == "" {
		response.BadRequest(c, "call_id 不能为空")
		return
	}
	callKey := fmt.Sprintf(mediaCallInviteRedisKey, req.CallID)
	callData, err := redis.Get[mediaCallInviteData](callKey)
	if err != nil || callData.CallID == "" || isCallExpired(callData.ExpireAt) {
		response.BadRequest(c, "通话已失效")
		return
	}
	if !allowCallMember(callData, user.Uid) {
		response.Unauthorized(c, "无权操作该通话")
		return
	}
	stateKey := fmt.Sprintf(mediaCallStateRedisKey, req.CallID)
	state, _ := redis.Get[mediaCallStateData](stateKey)
	if state.CallID == "" {
		state = mediaCallStateData{
			CallID:         callData.CallID,
			Rid:            callData.Rid,
			CallType:       callData.CallType,
			CallScene:      callData.CallScene,
			InviterUID:     callData.InviterUID,
			InviteeUIDs:    callData.InviteeUIDs,
			ActiveUIDs:     []string{},
			RejectedUIDs:   []string{},
			Ended:          false,
			CreateTime:     callData.CreateTime,
			ExpireAt:       callData.ExpireAt,
			PublisherMedia: callData.PublisherMedia,
		}
	}
	state.ActiveUIDs = appendUniqueUid(state.ActiveUIDs, user.Uid)
	_ = redis.Set(stateKey, state, callStateTTL(callData.ExpireAt))
	setUserActiveCallMapping(user.Uid, callData.CallID, callData.ExpireAt)
	broadcastStreamCallMemberSync(callData.CallID)
	response.Success(c, gin.H{"joined": true, "call_id": callData.CallID})
}

func LeaveCall(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var req mediaCallMemberStatePayload
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.CallID) == "" {
		response.BadRequest(c, "call_id 不能为空")
		return
	}
	recall, st := leaveCallByUID(user.Uid, req.CallID, false)
	switch st {
	case leaveCallExpired:
		response.Success(c, gin.H{"left": true, "recall_invite": false})
	case leaveCallUnauthorized:
		response.Unauthorized(c, "无权操作该通话")
	case leaveCallOK:
		response.Success(c, gin.H{"left": true, "recall_invite": recall, "call_id": req.CallID})
	}
}

func RejectCallInvite(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var req mediaCallMemberStatePayload
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.CallID) == "" {
		response.BadRequest(c, "call_id 不能为空")
		return
	}
	callKey := fmt.Sprintf(mediaCallInviteRedisKey, req.CallID)
	callData, err := redis.Get[mediaCallInviteData](callKey)
	if err != nil || callData.CallID == "" || isCallExpired(callData.ExpireAt) {
		response.BadRequest(c, "通话已失效")
		return
	}
	inInvitees := false
	for _, uid := range callData.InviteeUIDs {
		if uid == user.Uid {
			inInvitees = true
			break
		}
	}
	if !inInvitees {
		response.Unauthorized(c, "仅被邀请者可拒绝")
		return
	}
	stateKey := fmt.Sprintf(mediaCallStateRedisKey, req.CallID)
	state, _ := redis.Get[mediaCallStateData](stateKey)
	if state.CallID == "" {
		state = mediaCallStateData{
			CallID:         callData.CallID,
			Rid:            callData.Rid,
			CallType:       callData.CallType,
			CallScene:      callData.CallScene,
			InviterUID:     callData.InviterUID,
			InviteeUIDs:    callData.InviteeUIDs,
			ActiveUIDs:     []string{},
			RejectedUIDs:   []string{},
			Ended:          false,
			CreateTime:     callData.CreateTime,
			ExpireAt:       callData.ExpireAt,
			PublisherMedia: callData.PublisherMedia,
		}
	}
	if state.Ended {
		response.Success(c, gin.H{"rejected": true, "call_id": callData.CallID, "ended": true})
		return
	}
	state.RejectedUIDs = appendUniqueUid(state.RejectedUIDs, user.Uid)
	allRejected := len(state.RejectedUIDs) >= len(callData.InviteeUIDs)
	if allRejected {
		state.Ended = true
	}
	_ = redis.Set(stateKey, state, callStateTTL(callData.ExpireAt))
	if allRejected {
		clearAllUserActiveCallMappingsForCall(callData, &state)
		_ = helper.NotifyQuic(notify.MessageTypeStreamCallEndNotify, notify.StreamCallEndNotifyPayload{
			CallID:      callData.CallID,
			Rid:         callData.Rid,
			Reason:      "all_rejected",
			OperatorUID: user.Uid,
			CallScene:   callData.CallScene,
			CallType:    callData.CallType,
			InviterUID:  callData.InviterUID,
			TargetUIDs:  []string{callData.InviterUID},
			DurationSec: callDurationSeconds(callData),
		})
		saveMediaCallRecord(callData, &state, "all_rejected", user.Uid)
		destroyMediaCallSessionKeys(callData.CallID, callData.Rid)
	}
	response.Success(c, gin.H{"rejected": true, "call_id": callData.CallID, "all_rejected": allRejected})
}

func HangupCall(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var req mediaCallMemberStatePayload
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.CallID) == "" {
		response.BadRequest(c, "call_id 不能为空")
		return
	}
	callKey := fmt.Sprintf(mediaCallInviteRedisKey, req.CallID)
	callData, err := redis.Get[mediaCallInviteData](callKey)
	if err != nil || callData.CallID == "" {
		response.Success(c, gin.H{"hungup": true, "call_id": req.CallID})
		return
	}
	if !allowCallMember(callData, user.Uid) {
		response.Unauthorized(c, "无权操作该通话")
		return
	}
	stateKey := fmt.Sprintf(mediaCallStateRedisKey, req.CallID)
	state, _ := redis.Get[mediaCallStateData](stateKey)
	if state.CallID == "" {
		state = mediaCallStateData{
			CallID:         callData.CallID,
			Rid:            callData.Rid,
			CallType:       callData.CallType,
			CallScene:      callData.CallScene,
			InviterUID:     callData.InviterUID,
			InviteeUIDs:    callData.InviteeUIDs,
			ActiveUIDs:     []string{},
			RejectedUIDs:   []string{},
			Ended:          false,
			CreateTime:     callData.CreateTime,
			ExpireAt:       callData.ExpireAt,
			PublisherMedia: callData.PublisherMedia,
		}
	}
	if state.Ended {
		response.Success(c, gin.H{"hungup": true, "call_id": callData.CallID, "already_ended": true})
		return
	}
	// 好友通话：任一方挂断都强制结束
	if callData.CallScene == "friend" {
		state.Ended = true
		_ = redis.Set(stateKey, state, callStateTTL(callData.ExpireAt))
		broadcastStreamCallMemberSync(callData.CallID)
		clearAllUserActiveCallMappingsForCall(callData, &state)
		targets := make([]string, 0, 1+len(callData.InviteeUIDs))
		targets = append(targets, callData.InviterUID)
		targets = append(targets, callData.InviteeUIDs...)
		targets = uniqueUids(targets)
		_ = helper.NotifyQuic(notify.MessageTypeStreamCallEndNotify, notify.StreamCallEndNotifyPayload{
			CallID:      callData.CallID,
			Rid:         callData.Rid,
			Reason:      "hangup",
			OperatorUID: user.Uid,
			CallScene:   callData.CallScene,
			CallType:    callData.CallType,
			InviterUID:  callData.InviterUID,
			TargetUIDs:  targets,
			DurationSec: callDurationSeconds(callData),
		})
		saveMediaCallRecord(callData, &state, "hangup", user.Uid)
		destroyMediaCallSessionKeys(callData.CallID, callData.Rid)
		response.Success(c, gin.H{"hungup": true, "call_id": callData.CallID, "ended": true, "scene": "friend"})
		return
	}

	// 房间通话：
	// - 创建者挂断：强制结束全员（等同旧 EndCall 语义）
	// - 普通成员挂断：仅对个人生效；所有成员都挂断后才结束
	if user.Uid == callData.InviterUID {
		state.Ended = true
		_ = redis.Set(stateKey, state, callStateTTL(callData.ExpireAt))
		broadcastStreamCallMemberSync(callData.CallID)
		clearAllUserActiveCallMappingsForCall(callData, &state)

		targets := make([]string, 0, 1+len(callData.InviteeUIDs))
		targets = append(targets, callData.InviterUID)
		targets = append(targets, callData.InviteeUIDs...)
		targets = uniqueUids(targets)

		_ = helper.NotifyQuic(notify.MessageTypeStreamCallEndNotify, notify.StreamCallEndNotifyPayload{
			CallID:      callData.CallID,
			Rid:         callData.Rid,
			Reason:      "ended",
			OperatorUID: user.Uid,
			CallScene:   callData.CallScene,
			CallType:    callData.CallType,
			InviterUID:  callData.InviterUID,
			TargetUIDs:  targets,
			DurationSec: callDurationSeconds(callData),
		})
		saveMediaCallRecord(callData, &state, "ended", user.Uid)
		destroyMediaCallSessionKeys(callData.CallID, callData.Rid)
		response.Success(c, gin.H{"hungup": true, "call_id": callData.CallID, "ended": true, "scene": "room"})
		return
	}

	// 普通成员挂断：挂断仅对个人生效；所有成员都挂断后才结束。
	state.ActiveUIDs = removeUid(state.ActiveUIDs, user.Uid)
	state.RejectedUIDs = appendUniqueUid(state.RejectedUIDs, user.Uid)
	clearUserActiveCallMapping(user.Uid)
	allMembers := make([]string, 0, 1+len(callData.InviteeUIDs))
	allMembers = append(allMembers, callData.InviterUID)
	allMembers = append(allMembers, callData.InviteeUIDs...)
	allMembers = uniqueUids(allMembers)
	allHungup := len(state.RejectedUIDs) >= len(allMembers)
	if allHungup {
		state.Ended = true
	}
	_ = redis.Set(stateKey, state, callStateTTL(callData.ExpireAt))
	broadcastStreamCallMemberSync(callData.CallID)
	if allHungup {
		clearAllUserActiveCallMappingsForCall(callData, &state)
		_ = redis.Delete(fmt.Sprintf(mediaCallRoomActiveRedisKey, callData.Rid))
		_ = helper.NotifyQuic(notify.MessageTypeStreamCallEndNotify, notify.StreamCallEndNotifyPayload{
			CallID:      callData.CallID,
			Rid:         callData.Rid,
			Reason:      "ended",
			OperatorUID: user.Uid,
			CallScene:   callData.CallScene,
			CallType:    callData.CallType,
			InviterUID:  callData.InviterUID,
			TargetUIDs:  allMembers,
			DurationSec: callDurationSeconds(callData),
		})
		saveMediaCallRecord(callData, &state, "ended", user.Uid)
	}
	response.Success(c, gin.H{"hungup": true, "call_id": callData.CallID, "ended": allHungup, "scene": "room"})
}

func CurrentCall(c *gin.Context) {
	var req mediaCallCurrentPayload
	if err := c.ShouldBindQuery(&req); err != nil || strings.TrimSpace(req.Rid) == "" {
		response.BadRequest(c, "rid 不能为空")
		return
	}
	activeKey := fmt.Sprintf(mediaCallRoomActiveRedisKey, req.Rid)
	callID, err := redis.GetString(activeKey)
	if err != nil || strings.TrimSpace(callID) == "" {
		response.Success(c, gin.H{"exists": false})
		return
	}
	callData, err := redis.Get[mediaCallInviteData](fmt.Sprintf(mediaCallInviteRedisKey, callID))
	if err != nil || callData.CallID == "" || isCallExpired(callData.ExpireAt) {
		_ = redis.Delete(activeKey)
		response.Success(c, gin.H{"exists": false})
		return
	}
	state, _ := redis.Get[mediaCallStateData](fmt.Sprintf(mediaCallStateRedisKey, callID))
	if state.CallID != "" && state.Ended {
		_ = redis.Delete(activeKey)
		response.Success(c, gin.H{"exists": false})
		return
	}
	now := time.Now().UnixMilli()
	durationSec := int64(0)
	if callData.CreateTime > 0 && now >= callData.CreateTime {
		durationSec = (now - callData.CreateTime) / 1000
	}
	activeCount := 0
	if state.CallID != "" {
		activeCount = len(state.ActiveUIDs)
	}
	response.Success(c, gin.H{
		"exists":          true,
		"call_id":         callData.CallID,
		"rid":             callData.Rid,
		"call_type":       callData.CallType,
		"call_scene":      callData.CallScene,
		"inviter_uid":     callData.InviterUID,
		"create_time":     callData.CreateTime,
		"duration_sec":    durationSec,
		"active_count":    activeCount,
		"publisher_media": callData.PublisherMedia,
	})
}

// broadcastStreamCallMemberSync 在 ActiveUIDs 变更后向通话相关成员推送当前在线人数
func broadcastStreamCallMemberSync(callID string) {
	callKey := fmt.Sprintf(mediaCallInviteRedisKey, callID)
	callData, err := redis.Get[mediaCallInviteData](callKey)
	if err != nil || callData.CallID == "" {
		return
	}
	stateKey := fmt.Sprintf(mediaCallStateRedisKey, callID)
	state, _ := redis.Get[mediaCallStateData](stateKey)
	activeCount := 0
	if state.CallID != "" {
		activeCount = len(state.ActiveUIDs)
	}
	targets := make([]string, 0, 1+len(callData.InviteeUIDs)+len(state.ActiveUIDs))
	targets = append(targets, callData.InviterUID)
	targets = append(targets, callData.InviteeUIDs...)
	if state.CallID != "" {
		targets = append(targets, state.ActiveUIDs...)
	}
	targets = uniqueUids(targets)
	_ = helper.NotifyQuic(notify.MessageTypeStreamCallSyncNotify, notify.StreamCallSyncNotifyPayload{
		CallID:      callData.CallID,
		Rid:         callData.Rid,
		ActiveCount: activeCount,
		TargetUIDs:  targets,
	})
}

func allowCallMember(callData mediaCallInviteData, uid string) bool {
	if uid == callData.InviterUID {
		return true
	}
	for _, v := range callData.InviteeUIDs {
		if v == uid {
			return true
		}
	}
	return false
}

func appendUniqueUid(list []string, uid string) []string {
	for _, v := range list {
		if v == uid {
			return list
		}
	}
	return append(list, uid)
}

func removeUid(list []string, uid string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != uid {
			out = append(out, v)
		}
	}
	return out
}

func uniqueUids(list []string) []string {
	m := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, uid := range list {
		if uid == "" {
			continue
		}
		if _, ok := m[uid]; ok {
			continue
		}
		m[uid] = struct{}{}
		out = append(out, uid)
	}
	return out
}

func containsUid(list []string, uid string) bool {
	for _, v := range list {
		if v == uid {
			return true
		}
	}
	return false
}

func AcceptCallInvite(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	sid := helper.GetSid(c)
	if sid == "" {
		response.Unauthorized(c, "会话无效，请重新登录")
		return
	}
	var req mediaCallAcceptPayload
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.CallID) == "" {
		response.BadRequest(c, "call_id 不能为空")
		return
	}
	key := fmt.Sprintf(mediaCallInviteRedisKey, req.CallID)
	callData, err := redis.Get[mediaCallInviteData](key)
	if err != nil || callData.CallID == "" {
		response.BadRequest(c, "通话邀请不存在或已失效")
		return
	}
	if isCallExpired(callData.ExpireAt) {
		response.BadRequest(c, "通话邀请已过期")
		return
	}
	allowed := false
	for _, uid := range callData.InviteeUIDs {
		if uid == user.Uid {
			allowed = true
			break
		}
	}
	if !allowed {
		response.Unauthorized(c, "无权加入该通话")
		return
	}
	// 严格拦截：已结束通话禁止再次签发 join_sign，避免客户端收到“结束后又加入”。
	stateKey := fmt.Sprintf(mediaCallStateRedisKey, req.CallID)
	state, _ := redis.Get[mediaCallStateData](stateKey)
	if state.CallID != "" && state.Ended {
		response.BadRequest(c, "通话已结束")
		return
	}
	// 房间活跃指针必须仍指向当前 call_id；否则视为旧邀请/重放请求。
	roomActiveKey := fmt.Sprintf(mediaCallRoomActiveRedisKey, callData.Rid)
	activeCallID, _ := redis.GetString(roomActiveKey)
	if strings.TrimSpace(activeCallID) != callData.CallID {
		response.BadRequest(c, "通话已失效")
		return
	}
	signData, err := issueJoinSignData(user.Uid, sid, callData.Rid, "subscriber", mediaJoinSignTTLDefault)
	if err != nil {
		response.ServerError(c, "生成加入签名失败")
		return
	}
	serverCfg := config.GetServerConfig()
	publicMediaQuicAddr := pickClusterMediaQuicAddr(c.Request.Context(), serverCfg)
	_ = helper.NotifyQuic(notify.MessageTypeStreamCallJoinNotify, notify.StreamCallJoinNotifyPayload{
		CallID:         callData.CallID,
		Rid:            callData.Rid,
		CallType:       callData.CallType,
		CallScene:      callData.CallScene,
		TargetUID:      user.Uid,
		QuicAddr:       publicMediaQuicAddr,
		ALPN:           mediaALPN,
		PublisherMedia: callData.PublisherMedia,
		JoinSign: notify.StreamCallJoinSign{
			UID:      signData.UID,
			SID:      signData.SID,
			RID:      signData.RID,
			Role:     signData.Role,
			Nonce:    signData.Nonce,
			ExpireAt: signData.ExpireAt,
			Sign:     signData.Sign,
		},
	})
	response.Success(c, gin.H{"accepted": true, "call_id": callData.CallID})
}

func issueJoinSignData(uid, sid, rid, role string, ttl time.Duration) (mediaJoinSignData, error) {
	nonce := uuid.NewString()
	expireAt := time.Now().Add(ttl).UnixMilli()
	sign := makeMediaJoinSign(uid, sid, rid, role, nonce, expireAt)
	signData := mediaJoinSignData{
		UID:      uid,
		SID:      sid,
		RID:      rid,
		Role:     role,
		Nonce:    nonce,
		ExpireAt: expireAt,
		Sign:     sign,
	}
	if err := redis.Set(fmt.Sprintf(mediaJoinSignRedisKey, nonce), signData, ttl); err != nil {
		return mediaJoinSignData{}, err
	}
	return signData, nil
}

func makeMediaJoinSign(uid, sid, rid, role, nonce string, expireAt int64) string {
	raw := uid + "|" + sid + "|" + rid + "|" + role + "|" + nonce + "|" + strconv.FormatInt(expireAt, 10)
	mac := hmac.New(sha256.New, []byte(jwt.JWTKey()))
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func isCallExpired(expireAt int64) bool {
	if expireAt <= 0 {
		return false
	}
	return time.Now().UnixMilli() > expireAt
}

func callStateTTL(expireAt int64) time.Duration {
	if expireAt <= 0 {
		return 0
	}
	ttl := time.Until(time.UnixMilli(expireAt))
	if ttl <= 0 {
		return 0
	}
	return ttl
}

// callDurationSeconds 从通话创建时间到当前的秒数（用于结束系统消息）
func callDurationSeconds(callData mediaCallInviteData) int64 {
	if callData.CreateTime <= 0 {
		return 0
	}
	now := time.Now().UnixMilli()
	if now < callData.CreateTime {
		return 0
	}
	return (now - callData.CreateTime) / 1000
}

// pickClusterMediaQuicAddr 从 Redis 媒体集群 SET 随机选取节点；无注册时回退本机配置。
func pickClusterMediaQuicAddr(ctx context.Context, serverCfg *config.ServerConfig) string {
	if addr, err := serviceregistry.Pick(ctx, serviceregistry.KindMedia, serverCfg.MediaDialAddrsRedisKey); err == nil {
		return addr
	}
	return serviceregistry.AdvertiseHostPort(serverCfg.MediaQuicAddr)
}
