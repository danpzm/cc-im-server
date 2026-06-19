package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/xid"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/event"
	"github.com/xd/quic-server/jwt"
	pkgEvents "github.com/xd/quic-server/pkg/events"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/queue"
	quicConfig "github.com/xd/quic-server/quic/config"
	"github.com/xd/quic-server/redis"

	quicEntity "github.com/xd/quic-server/quic/handler/entity"
	"github.com/xd/quic-server/quic/handler/pack"
	"github.com/xd/quic-server/quic/handler/protocol"
	"gorm.io/gorm"
)

type MessageHandler struct {
	BaseHandler
	protocol protocol.MessageProtocol
	pack     pack.MessagePack
	// token 过期提醒：按配置窗口单次定时发送（无轮询）
	tokenRefreshNoticeSent bool
	tokenRefreshNoticeMu   sync.Mutex
	tokenRefreshTimer      *time.Timer
	tokenRefreshExpiresAt  int64
	runDone                chan struct{}
	lastIncomingVersion    uint32
	outgoingVersion        uint32
	versionMu              sync.Mutex
}

func (h *MessageHandler) Handle() error {
	h.protocol = protocol.NewFourByteProtocol(h.sessionKey)
	h.pack = pack.NewRmpPack()
	h.runDone = make(chan struct{})
	// 连接建立即按认证 token 调度过期前提醒，无需等待首条上行消息（与客户端本地 proactive 互补）。
	if h.ConnectAccessClaims != nil {
		h.scheduleTokenRefreshNotice(h.ConnectAccessClaims)
	}
	go h.run()
	if h.eventBus != nil {
		// 上线后消息流就绪，再触发未读消息推送
		h.eventBus.Publish(pkgEvents.EventClientOnline, h.user.Uid)
	}
	return nil
}
func (h *MessageHandler) DataEncode(data any) ([]byte, error) {
	return h.pack.Encode(data)
}
func (h *MessageHandler) SendMessage(message types.ServerMessageEntity) error {
	message.Version = h.nextOutgoingVersion()
	data, err := h.pack.Encode(message)
	if err != nil {
		log.Errorf("信息打包失败: %v", err)
		return err
	}
	buffer, err := h.protocol.Encode(data)
	if err != nil {
		log.Errorf("协议打包失败: %v", err)
		return err
	}
	_, err = h.stream.Write(buffer)
	if err != nil {
		log.Errorf("发送失败: %v", err)
		return err
	}
	return nil
}

func (h *MessageHandler) nextOutgoingVersion() uint32 {
	h.versionMu.Lock()
	defer h.versionMu.Unlock()
	if h.outgoingVersion == ^uint32(0) {
		h.outgoingVersion = 1
	} else {
		h.outgoingVersion++
	}
	return h.outgoingVersion
}
func (h *MessageHandler) run() {
	defer func() {
		close(h.runDone)
		h.stopTokenRefreshTimer()
	}()
	buf := make([]byte, 1024)
	for {
		n, err := h.stream.Read(buf)
		if err != nil {
			break
		}
		if n == 0 {
			break // 连接关闭
		}
		h.protocol.AddData(buf[:n])
		for {
			packet := h.protocol.TryParse()
			if packet == nil {
				break
			}
			log.Infof("收到消息: %v", packet)
			var message quicEntity.MessageEntity
			if err := h.pack.Decode(packet, &message); err != nil {
				log.Errorf("解码失败: %v", err)
				continue
			}
			claims, err := h.validateMessageToken(message.Token)
			if err != nil {
				log.Warnf("消息 token 校验失败 uid=%s err=%v", h.user.Uid, err)
				h.sendForceOffline("长连接消息认证失败，请重新登录")
				_ = h.stream.Close()
				return
			}
			if h.isInvalidIncomingVersion(message.Version) {
				log.Warnf(
					"收到失效消息(版本过期) uid=%s version=%d last=%d type=%d",
					h.user.Uid,
					message.Version,
					h.lastIncomingVersion,
					message.MessageType,
				)
				h.sendInvalidMessageNotice(message.Version, h.lastIncomingVersion, message.MessageType)
				continue
			}
			h.scheduleTokenRefreshNotice(claims)
			go h.handlerMessage(message.MessageType, message.Data)
		}
	}
}

func (h *MessageHandler) isInvalidIncomingVersion(version uint32) bool {
	if version <= h.lastIncomingVersion {
		return true
	}
	h.lastIncomingVersion = version
	return false
}

func (h *MessageHandler) sendInvalidMessageNotice(
	receivedVersion uint32,
	lastVersion uint32,
	messageType quicEntity.MessageType,
) {
	data, err := h.pack.Encode(quicEntity.ServerInvalidMessageNotice{
		Code:            "INVALID_VERSION",
		Message:         "消息版本失效：version 必须严格递增",
		ReceivedVersion: receivedVersion,
		LastVersion:     lastVersion,
		MessageType:     uint8(messageType),
		ServerTime:      time.Now().UnixMilli(),
	})
	if err != nil {
		log.Errorf("编码无效消息通知失败: %v", err)
		return
	}
	if err := h.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeInvalidMessageNotice,
		Data:        data,
	}); err != nil {
		log.Errorf("发送无效消息通知失败: %v", err)
	}
}

func (h *MessageHandler) validateMessageToken(token string) (*jwt.CustomClaims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("消息缺少token")
	}
	claims, err := jwt.ValidateJWT(token)
	if err != nil {
		return nil, err
	}
	if claims.Subject != h.user.Uid {
		return nil, fmt.Errorf("token用户不匹配: token_uid=%s conn_uid=%s", claims.Subject, h.user.Uid)
	}
	if claims.Sid == "" {
		return nil, errors.New("消息令牌缺少会话")
	}
	ok, err := query.ValidateActiveUserSession(h.user.Uid, claims.Sid)
	if err != nil || !ok {
		return nil, &jwt.JWTExpiresError{Message: "身份令牌已失效"}
	}
	return claims, nil
}

func (h *MessageHandler) scheduleTokenRefreshNotice(claims *jwt.CustomClaims) {
	if claims == nil {
		return
	}

	h.tokenRefreshNoticeMu.Lock()
	defer h.tokenRefreshNoticeMu.Unlock()

	// 相同过期时间不重复调度
	if h.tokenRefreshExpiresAt == claims.ExpiresAt {
		return
	}
	h.tokenRefreshExpiresAt = claims.ExpiresAt
	h.tokenRefreshNoticeSent = false

	if h.tokenRefreshTimer != nil {
		if !h.tokenRefreshTimer.Stop() {
			select {
			case <-h.tokenRefreshTimer.C:
			default:
			}
		}
		h.tokenRefreshTimer = nil
	}

	remaining := claims.ExpiresAt - time.Now().Unix()
	if remaining <= 0 {
		return
	}

	delaySeconds := remaining - int64(quicConfig.TokenRefreshNoticeAhead()/time.Second)
	if delaySeconds <= 0 {
		// 已进入提醒窗口，立即提醒一次
		go h.sendTokenRefreshRequired(claims.ExpiresAt)
		return
	}

	h.tokenRefreshTimer = time.NewTimer(time.Duration(delaySeconds) * time.Second)
	timer := h.tokenRefreshTimer
	exp := claims.ExpiresAt
	go func() {
		select {
		case <-h.runDone:
			return
		case <-timer.C:
			h.sendTokenRefreshRequired(exp)
		}
	}()
}

func (h *MessageHandler) sendTokenRefreshRequired(expiresAt int64) {
	h.tokenRefreshNoticeMu.Lock()
	if h.tokenRefreshNoticeSent || h.tokenRefreshExpiresAt != expiresAt {
		h.tokenRefreshNoticeMu.Unlock()
		return
	}
	// 先占位，避免并发场景重复发送；发送失败时再回滚
	h.tokenRefreshNoticeSent = true
	h.tokenRefreshNoticeMu.Unlock()

	remaining := expiresAt - time.Now().Unix()
	if remaining < 0 {
		remaining = 0
	}
	data, err := h.pack.Encode(quicEntity.ServerTokenRefreshRequired{
		ExpiresAt:        expiresAt * 1000,
		RemainingSeconds: remaining,
		Message:          "token即将过期，请立即刷新",
	})
	if err != nil {
		log.Errorf("编码token刷新提醒失败: %v", err)
		h.resetTokenRefreshNoticeSent(expiresAt)
		return
	}
	if err := h.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeTokenRefreshRequired,
		Data:        data,
	}); err != nil {
		log.Errorf("发送token刷新提醒失败: %v", err)
		h.resetTokenRefreshNoticeSent(expiresAt)
		return
	}
	log.Infof("已发送token刷新提醒 uid=%s 过期时间=%d", h.user.Uid, expiresAt)
}

func (h *MessageHandler) resetTokenRefreshNoticeSent(expiresAt int64) {
	h.tokenRefreshNoticeMu.Lock()
	defer h.tokenRefreshNoticeMu.Unlock()
	if h.tokenRefreshExpiresAt == expiresAt {
		h.tokenRefreshNoticeSent = false
	}
}

func (h *MessageHandler) stopTokenRefreshTimer() {
	h.tokenRefreshNoticeMu.Lock()
	defer h.tokenRefreshNoticeMu.Unlock()
	if h.tokenRefreshTimer == nil {
		return
	}
	if !h.tokenRefreshTimer.Stop() {
		select {
		case <-h.tokenRefreshTimer.C:
		default:
		}
	}
	h.tokenRefreshTimer = nil
}

func (h *MessageHandler) sendForceOffline(reason string) {
	data, err := h.pack.Encode(quicEntity.ServerForceOffline{Reason: reason})
	if err != nil {
		log.Errorf("编码强制下线消息失败: %v", err)
		return
	}
	if err := h.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeForceOffline,
		Data:        data,
	}); err != nil {
		log.Errorf("发送强制下线消息失败: %v", err)
	}
}

// getFileStatusUpdateRedisKey 获取文件状态更新的 Redis key
func getFileStatusUpdateRedisKey(clientCid string) string {
	return fmt.Sprintf("file_status_update:pending:%s", clientCid)
}

// getFileStatusUpdateAckRedisKey 获取文件状态更新 ACK 的 Redis key
func getFileStatusUpdateAckRedisKey(clientCid string) string {
	return fmt.Sprintf("file_status_update:ack:%s", clientCid)
}

// getRoomMessageDedupeRedisKey 获取房间消息去重锁 Redis key
func getRoomMessageDedupeRedisKey(uid, rid, clientMid string) string {
	return fmt.Sprintf("room_message:dedupe:%s:%s:%s", uid, rid, clientMid)
}

// handlerFileStatusUpdate 处理客户端上传完成后的文件状态更新，并广播给房间
func (h *MessageHandler) handlerFileStatusUpdate(data []byte) {
	var payload quicEntity.ClientFileStatusUpdate
	if err := json.Unmarshal(data, &payload); err != nil {
		log.Errorf("解码文件状态更新失败: %v", err)
		return
	}
	log.Infof("收到文件状态更新: rid=%s client_mid=%s client_cid=%s uf_id=%s", payload.Rid, payload.ClientMid, payload.ClientCid, payload.UfId)

	// 尝试查询房间消息，如果不存在则缓存到 Redis
	var rm types.RoomMessage
	err := db.GetDB().
		Where("rid = ? AND client_mid = ? AND sender_uid = ? AND delete_time = 0", payload.Rid, payload.ClientMid, h.user.Uid).
		First(&rm).Error
	if err != nil {
		// 消息还未到达，缓存到 Redis 等待回调处理
		log.Warnf("房间消息尚未到达，缓存文件状态更新到 Redis: rid=%s client_mid=%s client_cid=%s", payload.Rid, payload.ClientMid, payload.ClientCid)
		redisKey := getFileStatusUpdateRedisKey(payload.ClientCid)
		if err := redis.Set(redisKey, payload, 24*time.Hour); err != nil {
			log.Errorf("缓存文件状态更新到 Redis 失败: %v", err)
		}
		// 发送ACK确认，但标记为待处理
		h.sendFileStatusUpdateAck(quicEntity.ServerFileStatusUpdateAck{
			Rid:       payload.Rid,
			ClientMid: payload.ClientMid,
			ClientCid: payload.ClientCid,
		})
		return
	}

	// 消息已存在，直接处理
	h.processFileStatusUpdate(payload, &rm)
}

// processFileStatusUpdate 处理文件状态更新的核心逻辑
func (h *MessageHandler) processFileStatusUpdate(payload quicEntity.ClientFileStatusUpdate, rm *types.RoomMessage) {
	// 根据 client_cid 定位对应内容
	var rmc types.RoomMessageContent
	if err := db.GetDB().
		Where("mid = ? AND client_cid = ? AND delete_time = 0", rm.Mid, payload.ClientCid).
		First(&rmc).Error; err != nil {
		log.Errorf("文件状态更新: 查询内容失败 client_cid=%s err=%v", payload.ClientCid, err)
		// 发送ACK确认
		h.sendFileStatusUpdateAck(quicEntity.ServerFileStatusUpdateAck{
			Rid:       payload.Rid,
			ClientMid: payload.ClientMid,
			ClientCid: payload.ClientCid,
		})
		return
	}

	now := time.Now().UnixMilli()
	if err := db.GetDB().
		Model(&types.RoomMessageContent{}).
		Where("mid = ? AND cid = ?", rm.Mid, rmc.Cid).
		Updates(map[string]any{
			"content":     payload.Content,
			"type_id":     payload.UfId, // 回填 type_id 为 uf_id
			"update_time": now,
		}).Error; err != nil {
		log.Errorf("文件状态更新: 更新 content 失败 cid=%s err=%v", rmc.Cid, err)
		// 发送ACK确认
		h.sendFileStatusUpdateAck(quicEntity.ServerFileStatusUpdateAck{
			Rid:       payload.Rid,
			ClientMid: payload.ClientMid,
			ClientCid: payload.ClientCid,
		})
		return
	}

	// 广播给房间内用户
	event.Publish(event.EventRoomMessageContentUpdate, event.RoomMessageContentUpdatePayload{
		Mid: rmc.Mid,
		Cid: rmc.Cid,
	})
	log.Infof("文件状态更新已广播: mid=%s cid=%s uf_id=%s", rmc.Mid, rmc.Cid, payload.UfId)

	// 发送ACK确认并记录到 Redis，用于 ACK 重发机制
	ack := quicEntity.ServerFileStatusUpdateAck{
		Rid:       payload.Rid,
		ClientMid: payload.ClientMid,
		ClientCid: payload.ClientCid,
		Mid:       rm.Mid,
		Cid:       rmc.Cid,
	}
	h.sendFileStatusUpdateAck(ack)

	// 记录 ACK 信息到 Redis，用于重发机制（30秒超时）
	ackKey := getFileStatusUpdateAckRedisKey(payload.ClientCid)
	ackData := map[string]any{
		"rid":         payload.Rid,
		"client_mid":  payload.ClientMid,
		"client_cid":  payload.ClientCid,
		"mid":         rm.Mid,
		"cid":         rmc.Cid,
		"uid":         h.user.Uid,
		"create_time": time.Now().UnixMilli(),
	}
	if err := redis.Set(ackKey, ackData, 30*time.Second); err != nil {
		log.Errorf("记录文件状态更新 ACK 到 Redis 失败: %v", err)
	}
}
func (h *MessageHandler) handlerRoomMessage(data []byte) {
	var message types.ClientRoomMessage
	if err := json.Unmarshal(data, &message); err != nil {
		log.Errorf("解码失败: %v", err)
		return
	}

	log.Infof("收到房间消息: %v", message)
	if message.Rid == "" || message.ClientMid == "" {
		h.sendRoomMessageSendError(quicEntity.ServerRoomMessageSendError{
			Rid:       message.Rid,
			ClientMid: message.ClientMid,
			Code:      "INVALID_MESSAGE",
			Message:   "消息参数不完整（rid/client_mid不能为空）",
		})
		return
	}

	// 第一层防护：请求级去重锁，降低并发重复提交
	dedupeKey := getRoomMessageDedupeRedisKey(h.user.Uid, message.Rid, message.ClientMid)
	locked, lockErr := redis.SetNX(dedupeKey, "1", 15*time.Second)
	if lockErr != nil {
		log.Warnf("房间消息去重锁获取失败，降级走DB幂等: rid=%s client_mid=%s err=%v", message.Rid, message.ClientMid, lockErr)
	}
	if lockErr == nil && locked {
		defer func() {
			if err := redis.Delete(dedupeKey); err != nil {
				log.Warnf("释放房间消息去重锁失败: key=%s err=%v", dedupeKey, err)
			}
		}()
	}

	// 第二层防护：DB幂等检查（无论是否拿到锁都执行）
	existedRm, err := h.findExistingRoomMessage(message.Rid, message.ClientMid)
	if err != nil {
		log.Errorf("幂等检查失败 rid=%s client_mid=%s: %v", message.Rid, message.ClientMid, err)
		h.failRoomMessageSend(message.Rid, message.ClientMid, "SEND_FAILED", "消息处理失败，请重试")
		return
	}
	if existedRm != nil {
		log.Warnf("重复房间消息，忽略创建 rid=%s client_mid=%s mid=%s", message.Rid, message.ClientMid, existedRm.Mid)
		h.processCachedFileStatusUpdates(existedRm, message.Contents)
		h.deliverAcceptedRoomMessageToSender(existedRm.Mid)
		return
	}

	roomAny, err := query.GetRoomByRidAny(message.Rid)
	if err != nil || roomAny == nil {
		log.Errorf("获取房间失败 rid=%s: %v", message.Rid, err)
		h.failRoomMessageSend(message.Rid, message.ClientMid, "INVALID_ROOM", "房间不存在或不可用")
		return
	}
	if roomAny.State == entity.RoomStateDissolved {
		h.sendRoomMessageSendError(quicEntity.ServerRoomMessageSendError{
			Rid:       message.Rid,
			ClientMid: message.ClientMid,
			Code:      "ROOM_DISSOLVED",
			Message:   "该群已被群主解散或被删除",
		})
		return
	}
	room := roomAny
	// 屏蔽语义：屏蔽方可以发，但不接收该会话消息。发送不做限制，推送时排除屏蔽方（见 handleRoomMessageNotifyWithFilter）。
	// 私聊房间：若非“允许非好友私聊”房间，则校验发送方与对方是否为好友
	if room.Type == entity.RoomTypePrivate && !room.AllowNonFriendChat {
		userIds, err := query.GetRoomUserIdsCache(message.Rid)
		if err != nil {
			log.Errorf("获取房间成员失败 rid=%s: %v", message.Rid, err)
			h.failRoomMessageSend(message.Rid, message.ClientMid, "SEND_FAILED", "消息处理失败，请重试")
			return
		}
		var otherUid string
		for _, uid := range userIds {
			if uid != h.user.Uid {
				otherUid = uid
				break
			}
		}
		if otherUid != "" {
			areFriends, _, _ := query.CheckFriendRelation(h.user.Uid, otherUid)
			if !areFriends {
				log.Warnf("私聊房间发送失败：双方不是好友 uid=%s other=%s rid=%s", h.user.Uid, otherUid, message.Rid)
				h.sendRoomMessageSendError(quicEntity.ServerRoomMessageSendError{
					Rid:       message.Rid,
					ClientMid: message.ClientMid,
					Code:      "NOT_FRIEND",
					Message:   "您与对方不是好友，无法发送消息",
				})
				return
			}
		}
	}
	// 禁言校验：个人禁言、全体禁言或策略/频率；返回错误码供前端固定文案（MUTED / MUTED_TIME_RANGE / MUTED_FREQUENCY）
	if muted, code := query.IsUserMutedInRoom(message.Rid, h.user.Uid); muted {
		h.sendRoomMessageSendError(quicEntity.ServerRoomMessageSendError{
			Rid:       message.Rid,
			ClientMid: message.ClientMid,
			Code:      code,
			Message:   "",
		})
		return
	}

	seqId, err := query.GetRoomSeqId(message.Rid)
	if err != nil {
		log.Errorf("获取房间序列号失败: %v", err)
		h.failRoomMessageSend(message.Rid, message.ClientMid, "SEND_FAILED", "消息处理失败，请重试")
		return
	}
	rm := &types.RoomMessage{
		Rid:              message.Rid,
		ClientCreateTime: message.CreateTime,
		SeqId:            seqId,
		SenderUid:        h.user.Uid,
		IP:               h.ip,
		ClientMid:        message.ClientMid,
	}
	tx := db.GetDB().Begin()
	// 第三层防护：事务内再次幂等检查，防止并发穿透
	var txExisted types.RoomMessage
	if err := tx.Where("rid = ? AND client_mid = ? AND sender_uid = ? AND delete_time = 0", message.Rid, message.ClientMid, h.user.Uid).
		First(&txExisted).Error; err == nil {
		tx.Rollback()
		log.Warnf("事务内命中重复房间消息，忽略创建 rid=%s client_mid=%s mid=%s", message.Rid, message.ClientMid, txExisted.Mid)
		h.processCachedFileStatusUpdates(&txExisted, message.Contents)
		h.deliverAcceptedRoomMessageToSender(txExisted.Mid)
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		tx.Rollback()
		log.Errorf("事务内幂等检查失败 rid=%s client_mid=%s: %v", message.Rid, message.ClientMid, err)
		h.failRoomMessageSend(message.Rid, message.ClientMid, "SEND_FAILED", "消息处理失败，请重试")
		return
	}
	if err := tx.Create(rm).Error; err != nil {
		log.Errorf("创建房间消息失败: %v", err)
		tx.Rollback()
		h.failRoomMessageSend(message.Rid, message.ClientMid, "SEND_FAILED", "消息处理失败，请重试")
		return
	}
	contents := make([]*types.RoomMessageContent, 0, len(message.Contents))
	for _, content := range message.Contents {
		clientCid := content.ClientCid
		if clientCid == "" {
			log.Errorf("房间消息内容client_cid为空: %v", content)
			continue
		}
		if len(bytes.TrimSpace(content.Content)) == 0 {
			log.Errorf("房间消息内容为空 client_cid=%s", clientCid)
			continue
		}
		contents = append(contents, &types.RoomMessageContent{
			Type:             content.Type,
			TypeId:           content.TypeId,
			Mid:              rm.Mid,
			ClientCid:        clientCid,
			Content:          content.Content,
			ClientCreateTime: message.CreateTime,
		})
	}
	if len(contents) == 0 {
		tx.Rollback()
		h.sendRoomMessageSendError(quicEntity.ServerRoomMessageSendError{
			Rid:       message.Rid,
			ClientMid: message.ClientMid,
			Code:      "EMPTY_CONTENT",
			Message:   "消息内容为空或格式非法",
		})
		return
	}
	err = tx.Model(&types.RoomMessageContent{}).Create(contents).Error
	if err != nil {
		log.Errorf("创建房间消息内容失败: %v", err)
		tx.Rollback()
		h.failRoomMessageSend(message.Rid, message.ClientMid, "SEND_FAILED", "消息处理失败，请重试")
		return
	}
	if tx.Commit().Error != nil {
		log.Errorf("提交事务失败: %v", err)
		h.failRoomMessageSend(message.Rid, message.ClientMid, "SEND_FAILED", "消息处理失败，请重试")
		return
	}

	// 如果是私聊房间，确保接收方有 session 并置顶（须在 Bump 之前，以便新行也被更新）
	if err := query.EnsureReceiverSessionForPrivateRoom(message.Rid, h.user.Uid); err != nil {
		log.Errorf("确保接收方session失败: %v", err)
		// 不影响消息发送流程，只记录日志
	}
	if err := query.BumpUserRoomSessionLastMessageTime(message.Rid, rm.CreateTime); err != nil {
		log.Errorf("更新会话最后消息时间失败 rid=%s: %v", message.Rid, err)
	}
	if err := query.TouchRoomUserLastSpeak(message.Rid, h.user.Uid, rm.CreateTime, h.ip); err != nil {
		log.Errorf("更新成员最后发言信息失败 rid=%s uid=%s: %v", message.Rid, h.user.Uid, err)
	}
	// 已读游标仅由客户端 session/update-last-seq-id 上报；发送消息不等于已读对方消息，勿在此抬高 last_seq_id。
	// 频率限制：发信成功后增加 Redis 计数（仅当该房间启用频率规则时生效）
	query.IncrRoomMessageFrequencyCount(message.Rid, h.user.Uid)

	event.Publish(event.EventRoomMessage, event.RoomMessagePayload{
		Mid: rm.Mid,
	})

	// 发送方在同 QUIC 连接收到完整 ServerRoomMessage（state 0→1）；其他成员走 EventRoomMessage fanout
	h.deliverAcceptedRoomMessageToSender(rm.Mid)

	// 检查 Redis 缓存，处理之前缓存的文件状态更新
	h.processCachedFileStatusUpdates(rm, message.Contents)
}

func (h *MessageHandler) findExistingRoomMessage(rid, clientMid string) (*types.RoomMessage, error) {
	var rm types.RoomMessage
	err := db.GetDB().
		Where("rid = ? AND client_mid = ? AND sender_uid = ? AND delete_time = 0", rid, clientMid, h.user.Uid).
		First(&rm).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &rm, nil
}

// processCachedFileStatusUpdates 处理缓存的文件状态更新
func (h *MessageHandler) processCachedFileStatusUpdates(rm *types.RoomMessage, contents []quicEntity.ClientRoomMessageContent) {
	for _, content := range contents {
		if content.ClientCid == "" {
			continue
		}
		redisKey := getFileStatusUpdateRedisKey(content.ClientCid)
		cachedPayload, err := redis.Get[quicEntity.ClientFileStatusUpdate](redisKey)
		if err != nil {
			// 没有缓存或已过期，跳过
			continue
		}
		log.Infof("发现缓存的文件状态更新，开始处理: client_cid=%s", content.ClientCid)
		// 处理缓存的文件状态更新
		h.processFileStatusUpdate(cachedPayload, rm)
		// 删除 Redis 缓存
		if err := redis.Delete(redisKey); err != nil {
			log.Errorf("删除文件状态更新缓存失败: %v", err)
		}
	}
}

// handlerRoomMessageAck 处理客户端发送的房间消息ACK
func (h *MessageHandler) handlerRoomMessageAck(data []byte) {
	var ack quicEntity.ClientRoomMessageAck
	if err := json.Unmarshal(data, &ack); err != nil {
		log.Errorf("解码ACK失败: %v", err)
		return
	}

	log.Infof("收到房间消息ACK: mid=%s, rid=%s, uid=%s", ack.Mid, ack.Rid, h.user.Uid)

	// 更新RoomMessageAck状态为用户已接收
	now := time.Now().UnixMilli()
	result := db.GetDB().Model(&types.RoomMessageAck{}).
		Where("mid = ? AND uid = ? AND state = ?", ack.Mid, h.user.Uid, 0).
		Updates(map[string]any{
			"state":          1, // 1-用户已接收
			"confirmed_time": now,
			"update_time":    now,
		})

	if result.Error != nil {
		log.Errorf("更新房间消息ACK状态失败: %v", result.Error)
		return
	}

	if result.RowsAffected == 0 {
		log.Warnf("未找到对应的房间消息ACK记录: mid=%s, uid=%s", ack.Mid, h.user.Uid)
		return
	}

	log.Infof("已更新房间消息ACK状态: mid=%s, uid=%s, rows=%d", ack.Mid, h.user.Uid, result.RowsAffected)

	// 查询消息信息以获取seq_id和client_mid
	rm := query.GetRoomMessageByMid(ack.Mid)
	if rm == nil {
		log.Errorf("查询房间消息失败: mid=%s", ack.Mid)
		return
	}

	// 向接收方确认 ClientRoomMessageAck 已入库（与发送方发信回执无关）
	serverAck := quicEntity.ServerRoomMessageAck{
		Mid:       ack.Mid,
		Rid:       ack.Rid,
		SeqId:     rm.SeqId,
		ClientMid: rm.ClientMid,
	}
	h.sendRoomMessageAck(serverAck)
}

func (h *MessageHandler) failRoomMessageSend(rid, clientMid, code, msg string) {
	h.sendRoomMessageSendError(quicEntity.ServerRoomMessageSendError{
		Rid:       rid,
		ClientMid: clientMid,
		Code:      code,
		Message:   msg,
	})
}

// deliverAcceptedRoomMessageToSender 向当前连接（发送方）下发已落库的完整 ServerRoomMessage
func (h *MessageHandler) deliverAcceptedRoomMessageToSender(mid string) {
	if mid == "" {
		return
	}
	rcm := query.GetRoomMessageByMid(mid)
	if rcm == nil {
		log.Errorf("发送方回执失败，消息不存在: mid=%s", mid)
		return
	}
	data, err := h.DataEncode(*rcm)
	if err != nil {
		log.Errorf("编码发送方回执失败: mid=%s err=%v", mid, err)
		return
	}
	if err := h.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomMessage,
		Data:        data,
	}); err != nil {
		log.Errorf("发送方回执下发失败: mid=%s rid=%s err=%v", mid, rcm.Rid, err)
		return
	}
	log.Infof("已向发送方下发消息回执: mid=%s rid=%s client_mid=%s", mid, rcm.Rid, rcm.ClientMid)
}

// sendRoomMessageAck 向当前连接发送 ServerRoomMessageAck（仅用于 ClientRoomMessageAck 已读上报后的确认）
func (h *MessageHandler) sendRoomMessageAck(ack quicEntity.ServerRoomMessageAck) {
	data, err := h.pack.Encode(ack)
	if err != nil {
		log.Errorf("编码ACK确认失败: %v", err)
		return
	}
	err = h.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomMessageAck,
		Data:        data,
	})
	if err != nil {
		log.Errorf("发送ACK确认失败: %v", err)
		return
	}
	log.Infof("已发送房间消息ACK确认: mid=%s, rid=%s", ack.Mid, ack.Rid)
}

// sendRoomMessageSendError 发送房间消息发送失败（如私聊非好友）给客户端
func (h *MessageHandler) sendRoomMessageSendError(errPayload quicEntity.ServerRoomMessageSendError) {
	data, err := h.pack.Encode(errPayload)
	if err != nil {
		log.Errorf("编码房间消息发送失败: %v", err)
		return
	}
	err = h.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomMessageSendError,
		Data:        data,
	})
	if err != nil {
		log.Errorf("发送房间消息发送失败: %v", err)
		return
	}
	log.Infof("已发送房间消息发送失败: rid=%s client_mid=%s code=%s", errPayload.Rid, errPayload.ClientMid, errPayload.Code)
}

// handlerRoomMessageWithdrawAck 处理客户端发送的房间消息撤回ACK
func (h *MessageHandler) handlerRoomMessageWithdrawAck(data []byte) {
	var ack quicEntity.ClientRoomMessageWithdrawAck
	if err := json.Unmarshal(data, &ack); err != nil {
		log.Errorf("解码消息撤回ACK失败: %v", err)
		return
	}

	log.Infof("收到房间消息撤回ACK: mid=%s, rid=%s, uid=%s", ack.Mid, ack.Rid, h.user.Uid)

	// 更新RoomMessageWithdrawAck状态为用户已接收
	now := time.Now().UnixMilli()
	result := db.GetDB().Model(&types.RoomMessageWithdrawAck{}).
		Where("mid = ? AND uid = ? AND state = ?", ack.Mid, h.user.Uid, 0).
		Updates(map[string]any{
			"state":          1, // 1-撤回成功
			"confirmed_time": now,
			"update_time":    now,
		})

	if result.Error != nil {
		log.Errorf("更新房间消息撤回ACK状态失败: %v", result.Error)
		return
	}

	if result.RowsAffected == 0 {
		log.Warnf("未找到对应的房间消息撤回ACK记录: mid=%s, uid=%s", ack.Mid, h.user.Uid)
		return
	}

	log.Infof("已更新房间消息撤回ACK状态: mid=%s, uid=%s, rows=%d", ack.Mid, h.user.Uid, result.RowsAffected)
}

// sendFileStatusUpdateAck 发送文件状态更新ACK确认给客户端
func (h *MessageHandler) sendFileStatusUpdateAck(ack quicEntity.ServerFileStatusUpdateAck) {
	data, err := h.pack.Encode(ack)
	if err != nil {
		log.Errorf("编码文件状态更新ACK确认失败: %v", err)
		return
	}
	err = h.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeFileStatusUpdateAck,
		Data:        data,
	})
	if err != nil {
		log.Errorf("发送文件状态更新ACK确认失败: %v", err)
		return
	}
	log.Infof("已发送文件状态更新ACK确认: rid=%s, client_mid=%s, client_cid=%s", ack.Rid, ack.ClientMid, ack.ClientCid)
}

// handlerFileStatusUpdateAck 处理客户端发送的文件状态更新ACK
func (h *MessageHandler) handlerFileStatusUpdateAck(data []byte) {
	var ack quicEntity.ClientFileStatusUpdateAck
	if err := json.Unmarshal(data, &ack); err != nil {
		log.Errorf("解码文件状态更新ACK失败: %v", err)
		return
	}

	log.Infof("收到文件状态更新ACK: rid=%s, client_mid=%s, client_cid=%s, uid=%s", ack.Rid, ack.ClientMid, ack.ClientCid, h.user.Uid)

	// 删除 Redis 中的 ACK 记录，表示已收到客户端确认
	ackKey := getFileStatusUpdateAckRedisKey(ack.ClientCid)
	if err := redis.Delete(ackKey); err != nil {
		log.Errorf("删除文件状态更新ACK记录失败: %v", err)
	} else {
		log.Infof("已删除文件状态更新ACK记录: client_cid=%s", ack.ClientCid)
	}
}

// handlerRoomMessageWithdrawRequest 处理客户端发送的房间消息撤回请求
func (h *MessageHandler) handlerRoomMessageWithdrawRequest(data []byte) {
	var request quicEntity.ClientRoomMessageWithdrawRequest
	if err := json.Unmarshal(data, &request); err != nil {
		log.Errorf("解码撤回请求失败: %v", err)
		return
	}

	log.Infof("收到房间消息撤回请求: rid=%s, seq_id=%d, uid=%s", request.Rid, request.SeqId, h.user.Uid)

	// 查询消息
	roomMessage := query.GetRoomMessageByRidSeqId(request.Rid, request.SeqId)
	if roomMessage == nil {
		log.Errorf("消息不存在: rid=%s, seq_id=%d", request.Rid, request.SeqId)
		return
	}

	// 权限：消息发送者可撤回；管理员/房主可撤回他人消息
	isSelfWithdraw := roomMessage.SenderUid == h.user.Uid
	if !isSelfWithdraw {
		ru, err := query.GetRoomUser(request.Rid, h.user.Uid)
		if err != nil || ru == nil || (ru.Role != entity.RoomUserRoleAdmin && ru.Role != entity.RoomUserRoleOwner) {
			log.Errorf("用户无法撤回该消息: uid=%s, sender_uid=%s", h.user.Uid, roomMessage.SenderUid)
			return
		}
	}

	// 检查是否已撤回
	if roomMessage.WithdrawTime > 0 {
		log.Warnf("消息已撤回: mid=%s", roomMessage.Mid)
		return
	}

	// 检查时间限制（仅本人撤回时限制 5 分钟，管理员撤回他人不受此限制）
	if isSelfWithdraw && roomMessage.CreateTime < time.Now().UnixMilli()-1000*60*5 {
		log.Errorf("消息已超过5分钟无法撤回: mid=%s", roomMessage.Mid)
		return
	}

	// 执行撤回
	tx := db.GetDB().Begin()
	err := tx.Model(&types.RoomMessage{}).
		Where("rid = ?", request.Rid).
		Where("seq_id = ?", request.SeqId).
		Update("withdraw_time", time.Now().UnixMilli()).Error
	if err != nil {
		tx.Rollback()
		log.Errorf("撤回失败: %v", err)
		return
	}

	// 删除原本的消息内容
	err = tx.Model(&types.RoomMessageContent{}).
		Where("mid = ?", roomMessage.Mid).
		Update("delete_time", time.Now().UnixMilli()).Error
	if err != nil {
		tx.Rollback()
		log.Errorf("撤回失败: %v", err)
		return
	}

	// 添加撤回消息内容（携带 action_uid/target_uid 供客户端文案区分）
	withdrawContent, _ := json.Marshal(map[string]any{
		"action_uid": h.user.Uid,
		"target_uid": roomMessage.SenderUid,
		"by_admin":   !isSelfWithdraw,
	})
	rmc := &types.RoomMessageContent{
		ClientCid: xid.New().String(),
		Type:      types.RoomMessageContentTypeUserWithdraw,
		TypeId:    h.user.Uid,
		Mid:       roomMessage.Mid,
		Content:   withdrawContent,
	}
	err = tx.Create(rmc).Error
	if err != nil {
		tx.Rollback()
		log.Errorf("撤回失败: %v", err)
		return
	}

	if err := tx.Commit().Error; err != nil {
		log.Errorf("提交事务失败: %v", err)
		return
	}

	query.InvalidateRoomLastMessageCache(request.Rid)

	beforeData := map[string]any{"mid": roomMessage.Mid, "seq_id": roomMessage.SeqId, "sender_uid": roomMessage.SenderUid, "withdraw_time": roomMessage.WithdrawTime}
	afterData := map[string]any{"mid": roomMessage.Mid, "seq_id": roomMessage.SeqId, "sender_uid": roomMessage.SenderUid, "withdraw_time": time.Now().UnixMilli()}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: request.Rid, OpType: entity.RoomAdminOpMessageWithdraw, OperatorUid: h.user.Uid, Sid: h.sid, RelatedId: roomMessage.Mid,
		BeforeData: beforeData, AfterData: afterData,
	}, 0)

	// 通过事件系统通知 quic 广播一条用户撤回消息的系统消息
	event.Publish(event.EventRoomMessageWithdraw, event.RoomMessagePayload{Mid: roomMessage.Mid})
	log.Infof("已发布用户撤回消息事件: mid=%s", roomMessage.Mid)
}

func (h *MessageHandler) handlerMessage(messageType quicEntity.MessageType, data []byte) {
	switch messageType {
	case quicEntity.TypeRoomMessage:
		h.handlerRoomMessage(data)
	case quicEntity.TypeRoomMessageAck:
		h.handlerRoomMessageAck(data)
	case quicEntity.TypeFileStatusUpdate:
		h.handlerFileStatusUpdate(data)
	case quicEntity.TypeFileStatusUpdateAck:
		h.handlerFileStatusUpdateAck(data)
	case quicEntity.TypeRoomMessageWithdrawAck:
		h.handlerRoomMessageWithdrawAck(data)
	case quicEntity.TypeRoomMessageWithdrawRequest:
		h.handlerRoomMessageWithdrawRequest(data)
	default:
		log.Errorf("未知消息类型: %v", messageType)
	}
	log.Infof("处理消息完成: %v", messageType)
}
