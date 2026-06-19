package user

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/xid"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/notify"
	"github.com/xd/quic-server/utils"
	"gorm.io/gorm"
)

type FriendRequestCreate struct {
	ReceiverUid string `json:"receiver_uid" binding:"required"`
	Gid         string `json:"gid" binding:"required"`
	Remark      string `json:"remark"`
	Message     string `json:"message"`
}

type FriendRequestHandle struct {
	FrId   string `json:"fr_id" binding:"required"`
	Gid    string `json:"gid"`    // 可选：接收者的分组ID（同意时使用，如果不提供则使用请求中的）
	Remark string `json:"remark"` // 可选：接收者给发送者的备注（同意时使用）
	Rid    string `json:"rid"`    // 可选：若为群私聊且仅含双方，则升级为私聊并复用；否则创建新私聊房间
}

// FriendDelete 删除好友请求
type FriendDelete struct {
	FriendUid string `json:"friend_uid" binding:"required"`
}

// CreateFriendRequest 创建好友请求
func CreateFriendRequest(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	req, err := utils.BodyToObj[FriendRequestCreate](c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}

	// 验证不能添加自己为好友
	if req.ReceiverUid == user.Uid {
		response.BadRequest(c, "不能添加自己为好友")
		return
	}

	// 验证接收者是否存在
	receiver, err := query.GetUserByUid(req.ReceiverUid)
	if err != nil || receiver == nil {
		response.BadRequest(c, "用户不存在")
		return
	}

	// 检查是否已经是好友（单方面删除允许走流程，同意时再做恢复）
	areFriends, _, err := query.CheckFriendRelation(user.Uid, req.ReceiverUid)
	if err != nil {
		response.ServerError(c, "校验好友关系失败")
		return
	}
	if areFriends {
		response.BadRequest(c, "你们已经是好友")
		return
	}

	// 创建好友请求
	now := time.Now()
	friendRequest := &entity.UserFriendRequest{
		SenderUid:   user.Uid,
		ReceiverUid: req.ReceiverUid,
		Gid:         req.Gid,
		Remark:      req.Remark,
		Message:     req.Message,
		State:       0,                                // 等待验证
		ExpiresAt:   now.AddDate(0, 0, 7).UnixMilli(), // 7天过期
	}

	// 创建或更新好友请求（重复添加以最后一次请求为准）
	err = query.CreateOrUpdateFriendRequest(friendRequest)
	if err != nil {
		response.ServerError(c, "创建好友请求失败")
		return
	}

	// 为双方创建或重置好友请求通知（同一 fr_id 每人只保留一条）
	// 接收者：类型 10-好友发起请求通知；发送者：类型 11-好友添加请求通知
	receiverContent, _ := json.Marshal(map[string]any{
		"sender_uid":   user.Uid,
		"sender_name":  user.Nickname,
		"receiver_uid": req.ReceiverUid,
		"message":      req.Message,
		"remark":       req.Remark,
	})
	senderContent, _ := json.Marshal(map[string]any{
		"sender_uid":    user.Uid,
		"receiver_uid":  req.ReceiverUid,
		"receiver_name": receiver.Nickname,
		"message":       req.Message,
		"remark":        req.Remark,
	})

	// 接收者通知（类型 10）
	existingRecv, err := query.GetMessageNotificationByRelatedIdAndType(
		req.ReceiverUid,
		friendRequest.FrId,
		entity.NotificationTypeFriendNotification,
	)
	if err != nil {
		log.Errorf("查询接收者好友请求消息通知失败: %v", err)
	} else {
		var recvNotif *entity.UserMessageNotification
		if existingRecv != nil {
			existingRecv.Content = string(receiverContent)
			existingRecv.Status = 1
			existingRecv.ReadAt = 0
			existingRecv.State = entity.NotificationStatePending
			recvNotif = existingRecv
			if tx := db.GetDB().Save(recvNotif); tx.Error != nil {
				log.Errorf("重置接收者好友请求消息通知失败: %v", tx.Error)
				recvNotif = nil
			}
		} else {
			recvNotif = &entity.UserMessageNotification{
				Uid:       req.ReceiverUid,
				Type:      entity.NotificationTypeFriendNotification,
				RelatedId: friendRequest.FrId,
				Content:   string(receiverContent),
			}
			if err = query.CreateMessageNotification(recvNotif); err != nil {
				log.Errorf("创建接收者好友请求消息通知失败: %v", err)
				recvNotif = nil
			}
		}
		if recvNotif != nil {
			if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: recvNotif.Nid}); err != nil {
				log.Errorf("发送接收者通知推送失败: nid=%s err=%v", recvNotif.Nid, err)
			} else {
				log.Infof("已发送接收者通知推送: nid=%s", recvNotif.Nid)
			}
		}
	}

	// 发送者通知（类型 11-好友添加请求）
	existingSend, err := query.GetMessageNotificationByRelatedIdAndType(
		user.Uid,
		friendRequest.FrId,
		entity.NotificationTypeFriendAddRequest,
	)
	if err != nil {
		log.Errorf("查询发送者好友请求消息通知失败: %v", err)
	} else {
		var sendNotif *entity.UserMessageNotification
		if existingSend != nil {
			existingSend.Content = string(senderContent)
			existingSend.Status = 1
			existingSend.ReadAt = 0
			existingSend.State = entity.NotificationStatePending
			sendNotif = existingSend
			if tx := db.GetDB().Save(sendNotif); tx.Error != nil {
				log.Errorf("重置发送者好友请求消息通知失败: %v", tx.Error)
				sendNotif = nil
			}
		} else {
			sendNotif = &entity.UserMessageNotification{
				Uid:       user.Uid,
				Type:      entity.NotificationTypeFriendAddRequest,
				RelatedId: friendRequest.FrId,
				Content:   string(senderContent),
			}
			if err = query.CreateMessageNotification(sendNotif); err != nil {
				log.Errorf("创建发送者好友请求消息通知失败: %v", err)
				sendNotif = nil
			}
		}
		if sendNotif != nil {
			if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: sendNotif.Nid}); err != nil {
				log.Errorf("发送发送者通知推送失败: nid=%s err=%v", sendNotif.Nid, err)
			} else {
				log.Infof("已发送发送者通知推送: nid=%s", sendNotif.Nid)
			}
		}
	}

	response.Success(c, friendRequest)
}

// FriendRequestList 获取好友请求列表
func FriendRequestList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	asReceiver := c.DefaultQuery("as_receiver", "true")
	isReceiver := asReceiver == "true"

	requests, err := query.GetFriendRequestList(user.Uid, isReceiver)
	if err != nil {
		response.ServerError(c, "获取好友请求列表失败")
		return
	}
	response.Success(c, requests)
}

// AcceptFriendRequest 同意好友请求
func AcceptFriendRequest(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	req, err := utils.BodyToObj[FriendRequestHandle](c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}

	// 获取好友请求
	friendRequest, err := query.GetFriendRequestByFrId(req.FrId)
	if err != nil {
		response.ServerError(c, "获取好友请求失败")
		return
	}
	if friendRequest == nil {
		response.BadRequest(c, "好友请求不存在")
		return
	}

	// 验证是否是接收者
	if friendRequest.ReceiverUid != user.Uid {
		response.BadRequest(c, "无权处理此请求")
		return
	}

	// 验证请求状态
	if friendRequest.State != 0 {
		response.BadRequest(c, "该请求已被处理")
		return
	}

	// 验证是否过期
	if friendRequest.ExpiresAt > 0 && friendRequest.ExpiresAt < time.Now().UnixMilli() {
		response.BadRequest(c, "该请求已过期")
		return
	}

	tx := db.GetDB().Begin()

	// 更新好友请求状态为同意（2）
	const state int8 = 2
	err = query.UpdateFriendRequestStateWithTx(tx, req.FrId, state)
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "处理好友请求失败")
		return
	}

	// 同步更新双方的好友请求通知状态（接收者类型10，发送者类型11）
	if err = query.UpdateMessageNotificationStateWithTx(tx, user.Uid, req.FrId, entity.NotificationTypeFriendNotification, entity.NotificationFriendStateAccepted); err != nil {
		tx.Rollback()
		log.Errorf("更新接收方好友请求通知状态失败: %v", err)
		response.ServerError(c, "处理好友请求失败")
		return
	}
	if err = query.UpdateMessageNotificationStateWithTx(tx, friendRequest.SenderUid, req.FrId, entity.NotificationTypeFriendAddRequest, entity.NotificationFriendStateAccepted); err != nil {
		tx.Rollback()
		log.Errorf("更新发送方好友请求通知状态失败: %v", err)
		response.ServerError(c, "处理好友请求失败")
		return
	}

	// 用于保存好友验证消息ID和房间ID（如果同意）
	var friendVerifyMessageMid string
	var friendVerifyRoomRid string
	var friendVerifyMsgCreateTime int64
	var room *types.Room
	var rm *types.RoomMessage

	if state == 2 {
		// 如果同意，创建双向好友关系（使用事务）
		// receiverUid: 接收者（当前用户，同意请求的人）
		// senderUid: 发送者（发起请求的人）
		// receiverGid: 接收者的分组ID（优先使用请求中的gid，如果没有则使用默认分组）
		// receiverRemark: 接收者给发送者的备注（优先使用请求中的remark，如果没有则使用好友请求中的）
		receiverGid := req.Gid
		receiverRemark := req.Remark

		// 如果没有提供分组ID，获取默认分组
		if receiverGid == "" {
			defaultGroup, err := query.GetDefaultFriendGroup(user.Uid)
			if err != nil {
				tx.Rollback()
				response.ServerError(c, "获取默认分组失败")
				return
			}
			if defaultGroup == nil {
				tx.Rollback()
				response.BadRequest(c, "未找到默认分组，请先创建分组")
				return
			}
			receiverGid = defaultGroup.Gid
		}

		// 如果没有提供备注，使用好友请求中的备注
		if receiverRemark == "" {
			receiverRemark = friendRequest.Remark
		}

		// 验证分组ID是否存在且属于当前用户
		if receiverGid != "" {
			group, err := query.GetFriendGroupByGid(user.Uid, receiverGid)
			if err != nil {
				tx.Rollback()
				response.ServerError(c, "验证分组失败")
				return
			}
			if group == nil {
				tx.Rollback()
				response.BadRequest(c, "分组不存在或不属于当前用户")
				return
			}
		}

		// 若已是好友（并发等导致）直接返回；若存在单方面删除则恢复，否则新建
		areFriends, oneSideDeleted, err := query.CheckFriendRelation(friendRequest.ReceiverUid, friendRequest.SenderUid)
		if err != nil {
			tx.Rollback()
			response.ServerError(c, "校验好友关系失败")
			return
		}
		if areFriends {
			// 已是好友，不再重复创建
		} else if oneSideDeleted {
			err = query.RestoreFriendRelationWithTx(
				tx,
				friendRequest.ReceiverUid,
				friendRequest.SenderUid,
				receiverGid,
				receiverRemark,
			)
		} else {
			err = query.CreateFriendRelationWithTx(
				tx,
				friendRequest.ReceiverUid,
				friendRequest.SenderUid,
				receiverGid,
				receiverRemark,
			)
		}
		if err != nil {
			tx.Rollback()
			log.Errorf("创建/恢复好友关系失败: %v", err)
			response.ServerError(c, "创建好友关系失败")
			return
		}

		// 确定使用的私聊房间：若请求带了 rid 且该房间是仅含双方的群私聊，则升级并复用；否则先升级所有群私聊再查找或创建
		if req.Rid != "" {
			var upgraded bool
			room, upgraded, err = query.TryUpgradeGroupPrivateRoomToPrivateTx(tx, req.Rid, friendRequest.ReceiverUid, friendRequest.SenderUid)
			if err != nil {
				tx.Rollback()
				log.Errorf("尝试升级指定房间失败: %v", err)
				response.ServerError(c, "处理房间失败")
				return
			}
			if !upgraded {
				room = nil
			}
		}
		if room == nil {
			_ = query.UpgradeGroupPrivateToPrivate(friendRequest.ReceiverUid, friendRequest.SenderUid)
			room, err = query.FindPrivateRoomBetweenUsers(friendRequest.ReceiverUid, friendRequest.SenderUid)
			if err != nil {
				tx.Rollback()
				log.Errorf("查找私聊房间失败: %v", err)
				response.ServerError(c, "创建私聊房间失败")
				return
			}
			if room == nil {
				room, err = query.CreatePrivateRoomWithConfigTx(tx, []string{friendRequest.ReceiverUid, friendRequest.SenderUid}, false)
				if err != nil {
					tx.Rollback()
					log.Errorf("创建私聊房间失败: %v", err)
					response.ServerError(c, "创建私聊房间失败")
					return
				}
			}
		}

		// 为双方置顶会话：先取消双方的旧会话置顶
		if err := tx.Model(&types.UserRoomSession{}).
			Where("uid IN (?) AND delete_time = 0", []string{friendRequest.ReceiverUid, friendRequest.SenderUid}).
			Update("is_top", false).Error; err != nil {
			tx.Rollback()
			log.Errorf("取消旧会话置顶失败: %v", err)
			response.ServerError(c, "创建会话失败")
			return
		}

		// 为接收者创建/更新会话并置顶（若曾软删除则恢复，避免 (uid,rid) 唯一约束冲突导致无法再次写入验证消息）
		var receiverSession *types.UserRoomSession
		if err := tx.Where("uid = ? AND rid = ? AND delete_time = 0", friendRequest.ReceiverUid, room.Rid).
			First(&receiverSession).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				tx.Rollback()
				log.Errorf("查询接收者会话失败: %v", err)
				response.ServerError(c, "创建会话失败")
				return
			}
			var existing types.UserRoomSession
			if errRestore := tx.Where("uid = ? AND rid = ?", friendRequest.ReceiverUid, room.Rid).
				First(&existing).Error; errRestore == nil {
				if err := tx.Model(&types.UserRoomSession{}).Where("id = ?", existing.Id).
					Updates(map[string]any{"delete_time": 0, "state": 1}).Error; err != nil {
					tx.Rollback()
					log.Errorf("恢复接收者会话失败: %v", err)
					response.ServerError(c, "创建会话失败")
					return
				}
				receiverSession = &existing
				receiverSession.DeleteTime = 0
			} else {
				receiverSession = &types.UserRoomSession{
					Uid:   friendRequest.ReceiverUid,
					Rid:   room.Rid,
					State: 1,
				}
				if err := tx.Create(receiverSession).Error; err != nil {
					tx.Rollback()
					log.Errorf("创建接收者会话失败: %v", err)
					response.ServerError(c, "创建会话失败")
					return
				}
			}
		} else {
			if err := tx.Model(&types.UserRoomSession{}).
				Where("id = ?", receiverSession.Id).
				Updates(map[string]any{
					"state": 1,
				}).Error; err != nil {
				tx.Rollback()
				log.Errorf("更新接收者会话失败: %v", err)
				response.ServerError(c, "创建会话失败")
				return
			}
		}

		// 为发送者创建/更新会话并置顶（若曾软删除则恢复，避免 (uid,rid) 唯一约束冲突导致无法再次写入验证消息）
		var senderSession *types.UserRoomSession
		if err := tx.Where("uid = ? AND rid = ? AND delete_time = 0", friendRequest.SenderUid, room.Rid).
			First(&senderSession).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				tx.Rollback()
				log.Errorf("查询发送者会话失败: %v", err)
				response.ServerError(c, "创建会话失败")
				return
			}
			var existing types.UserRoomSession
			if errRestore := tx.Where("uid = ? AND rid = ?", friendRequest.SenderUid, room.Rid).
				First(&existing).Error; errRestore == nil {
				if err := tx.Model(&types.UserRoomSession{}).Where("id = ?", existing.Id).
					Updates(map[string]any{"delete_time": 0, "state": 1}).Error; err != nil {
					tx.Rollback()
					log.Errorf("恢复发送者会话失败: %v", err)
					response.ServerError(c, "创建会话失败")
					return
				}
				senderSession = &existing
				senderSession.DeleteTime = 0
			} else {
				senderSession = &types.UserRoomSession{
					Uid:   friendRequest.SenderUid,
					Rid:   room.Rid,
					IsTop: false,
					State: 1,
				}
				if err := tx.Create(senderSession).Error; err != nil {
					tx.Rollback()
					log.Errorf("创建发送者会话失败: %v", err)
					response.ServerError(c, "创建会话失败")
					return
				}
			}
		} else {
			if err := tx.Model(&types.UserRoomSession{}).
				Where("id = ?", senderSession.Id).
				Updates(map[string]any{
					"state": 1,
				}).Error; err != nil {
				tx.Rollback()
				log.Errorf("更新发送者会话失败: %v", err)
				response.ServerError(c, "创建会话失败")
				return
			}
		}

		// 获取房间序列号
		seqId, err := query.GetRoomSeqId(room.Rid)
		if err != nil {
			tx.Rollback()
			log.Errorf("获取房间序列号失败: %v", err)
			response.ServerError(c, "创建消息失败")
			return
		}

		// 创建好友验证消息（类似QQ打招呼）
		// 消息发送者为发起好友请求的人（senderUid），内容为好友请求的消息
		rm = &types.RoomMessage{
			Rid:       room.Rid,
			ClientMid: xid.New().String(),
			SenderUid: friendRequest.SenderUid,
			SeqId:     seqId,
			IP:        c.ClientIP(),
		}
		if err := tx.Create(rm).Error; err != nil {
			tx.Rollback()
			log.Errorf("创建房间消息失败: %v", err)
			response.ServerError(c, "创建消息失败")
			return
		}

		// 创建消息内容（好友验证类型，内容为好友请求的消息）
		verifyContent, _ := json.Marshal(map[string]any{
			"message": friendRequest.Message,
			"remark":  friendRequest.Remark,
		})
		rc := &types.RoomMessageContent{
			Type:      types.RoomMessageContentTypeFriendVerify,
			TypeId:    friendRequest.SenderUid,
			ClientCid: xid.New().String(),
			Mid:       rm.Mid,
			Content:   json.RawMessage(verifyContent),
		}
		if err := tx.Create(rc).Error; err != nil {
			tx.Rollback()
			log.Errorf("创建消息内容失败: %v", err)
			response.ServerError(c, "创建消息失败")
			return
		}

		// 保存消息ID和房间ID，用于事务提交后通知QUIC服务器和更新缓存
		friendVerifyMessageMid = rm.Mid
		friendVerifyRoomRid = room.Rid
		friendVerifyMsgCreateTime = rm.CreateTime
		// 注意：房间成员缓存需要在事务提交后更新，因为此时 room_user 数据才真正写入数据库
	}

	tx.Commit()
	if tx.Error != nil {
		response.ServerError(c, "处理好友请求失败")
		return
	}

	// 事务提交后推送双方通知更新（接收者与发送者）
	if recvNotif, _ := query.GetMessageNotificationByRelatedIdAndType(user.Uid, req.FrId, entity.NotificationTypeFriendNotification); recvNotif != nil {
		if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: recvNotif.Nid}); err != nil {
			log.Errorf("发送接收方通知推送失败: nid=%s err=%v", recvNotif.Nid, err)
		} else {
			log.Infof("已发送接收方通知推送: nid=%s", recvNotif.Nid)
		}
	}
	if sendNotif, _ := query.GetMessageNotificationByRelatedIdAndType(friendRequest.SenderUid, req.FrId, entity.NotificationTypeFriendAddRequest); sendNotif != nil {
		if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: sendNotif.Nid}); err != nil {
			log.Errorf("发送发送方通知推送失败: nid=%s err=%v", sendNotif.Nid, err)
		} else {
			log.Infof("已发送发送方通知推送: nid=%s", sendNotif.Nid)
		}
	}

	// 如果同意了好友请求，更新房间成员缓存并通知QUIC服务器广播好友验证消息
	if state == 2 {
		// 事务提交后，更新房间成员缓存（此时 room_user 数据已真正写入数据库）
		if friendVerifyRoomRid != "" {
			query.SetRoomUserIdsCache(friendVerifyRoomRid)
			if friendVerifyMsgCreateTime > 0 {
				if err := query.BumpUserRoomSessionLastMessageTime(friendVerifyRoomRid, friendVerifyMsgCreateTime); err != nil {
					log.Errorf("更新会话最后消息时间失败 rid=%s: %v", friendVerifyRoomRid, err)
				}
			}
		}

		if friendVerifyMessageMid != "" {
			payload := notify.RoomMessageNotifyIncludeUidsPayload{
				Mid: friendVerifyMessageMid,
				UidList: []string{
					friendRequest.SenderUid,
					friendRequest.ReceiverUid,
				},
			}
			if err := helper.NotifyQuic(notify.MessageTypeRoomMessageNotifyIncludeUids, payload); err != nil {
				log.Errorf("发送好友验证消息通知失败: mid=%s err=%v", friendVerifyMessageMid, err)
			} else {
				log.Infof("已发送好友验证消息通知: mid=%s", friendVerifyMessageMid)
			}
		}
	}

	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpFriendRequestAccept, req.FrId, nil, map[string]any{
		"fr_id": req.FrId, "sender_uid": friendRequest.SenderUid,
	})
	response.Success(c, friendRequest)
}

// RejectFriendRequest 拒绝好友请求
func RejectFriendRequest(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	req, err := utils.BodyToObj[FriendRequestHandle](c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}

	// 获取好友请求
	friendRequest, err := query.GetFriendRequestByFrId(req.FrId)
	if err != nil {
		response.ServerError(c, "获取好友请求失败")
		return
	}
	if friendRequest == nil {
		response.BadRequest(c, "好友请求不存在")
		return
	}

	// 验证是否是接收者
	if friendRequest.ReceiverUid != user.Uid {
		response.BadRequest(c, "无权处理此请求")
		return
	}

	// 验证请求状态
	if friendRequest.State != 0 {
		response.BadRequest(c, "该请求已被处理")
		return
	}

	// 验证是否过期
	if friendRequest.ExpiresAt > 0 && friendRequest.ExpiresAt < time.Now().UnixMilli() {
		response.BadRequest(c, "该请求已过期")
		return
	}

	tx := db.GetDB().Begin()

	// 更新好友请求状态为拒绝（1）
	const state int8 = 1
	err = query.UpdateFriendRequestStateWithTx(tx, req.FrId, state)
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "处理好友请求失败")
		return
	}

	// 同步更新双方的好友请求通知状态为「已拒绝」（接收者类型10，发送者类型11）
	if err = query.UpdateMessageNotificationStateWithTx(
		tx,
		user.Uid,
		req.FrId,
		entity.NotificationTypeFriendNotification,
		entity.NotificationFriendStateRejected,
	); err != nil {
		tx.Rollback()
		log.Errorf("更新接收方好友请求通知状态失败: %v", err)
		response.ServerError(c, "处理好友请求失败")
		return
	}
	if err = query.UpdateMessageNotificationStateWithTx(
		tx,
		friendRequest.SenderUid,
		req.FrId,
		entity.NotificationTypeFriendAddRequest,
		entity.NotificationFriendStateRejected,
	); err != nil {
		tx.Rollback()
		log.Errorf("更新发送方好友请求通知状态失败: %v", err)
		response.ServerError(c, "处理好友请求失败")
		return
	}

	tx.Commit()
	if tx.Error != nil {
		response.ServerError(c, "处理好友请求失败")
		return
	}

	if recvNotif, _ := query.GetMessageNotificationByRelatedIdAndType(user.Uid, req.FrId, entity.NotificationTypeFriendNotification); recvNotif != nil {
		if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: recvNotif.Nid}); err != nil {
			log.Errorf("发送接收方拒绝通知失败: nid=%s err=%v", recvNotif.Nid, err)
		} else {
			log.Infof("已发送接收方拒绝通知: nid=%s", recvNotif.Nid)
		}
	}
	if sendNotif, _ := query.GetMessageNotificationByRelatedIdAndType(friendRequest.SenderUid, req.FrId, entity.NotificationTypeFriendAddRequest); sendNotif != nil {
		if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: sendNotif.Nid}); err != nil {
			log.Errorf("发送发送方拒绝通知失败: nid=%s err=%v", sendNotif.Nid, err)
		} else {
			log.Infof("已发送发送方拒绝通知: nid=%s", sendNotif.Nid)
		}
	}

	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpFriendRequestReject, req.FrId, nil, map[string]any{
		"fr_id": req.FrId, "sender_uid": friendRequest.SenderUid,
	})
	response.Success(c, friendRequest)
}

// DeleteFriend 删除好友（单向删除）
// 当前登录用户只删除自己这侧的好友关系，对方保留；之后如需重新添加，将走“单方面/双方删除”的加好友流程。
func DeleteFriend(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	var req FriendDelete
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}

	if req.FriendUid == "" {
		response.BadRequest(c, "friend_uid 必填")
		return
	}
	if req.FriendUid == user.Uid {
		response.BadRequest(c, "不能删除自己")
		return
	}

	// 执行单向删除
	if err := query.DeleteFriend(user.Uid, req.FriendUid); err != nil {
		if errors.Is(err, query.ErrNotFriend) {
			response.BadRequest(c, "对方不是你的好友或已删除")
			return
		}
		log.Errorf("DeleteFriend 删除好友失败 uid=%s friend_uid=%s: %v", user.Uid, req.FriendUid, err)
		response.ServerError(c, "删除好友失败")
		return
	}

	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpFriendDelete, req.FriendUid, nil, map[string]any{
		"friend_uid": req.FriendUid,
	})
	response.Success(c, gin.H{"friend_uid": req.FriendUid})
}
