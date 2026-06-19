package server

import (
	"context"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/event"
	"github.com/xd/quic-server/notifycoord"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/push"
	"github.com/xd/quic-server/queue"
	quicEntity "github.com/xd/quic-server/quic/handler/entity"
	"github.com/xd/quic-server/notify"
)

func (s *Server) registerEventHandlers() {
	event.Subscribe(event.EventRoomMessage, s.HandlerRoomMessage)
	event.Subscribe(event.EventRoomMessageContentUpdate, s.HandlerRoomMessageContentUpdate)
	event.Subscribe(event.EventRoomMessageWithdraw, s.HandlerRoomMessageWithdraw)
}

func (s *Server) HandlerRoomMessageContentUpdate(payload event.RoomMessageContentUpdatePayload) {
	rm := query.GetRoomMessageByMid(payload.Mid)
	if rm == nil {
		log.Errorf("查询房间消息失败: %s", payload.Mid)
		return
	}
	roomUserIds, err := query.GetRoomUserIdsCache(rm.Rid)
	if err != nil {
		log.Error("查询房间内的用户失败:", err)
		return
	}
	groups := push.GroupUidsByNode(roomUserIds)
	self := config.GetServerConfig().NodeID
	for nodeID, targetUids := range groups {
		if len(targetUids) == 0 {
			continue
		}
		if nodeID == self {
			if err := s.deliverFileStatusUpdateToTargets(payload.Mid, payload.Cid, targetUids); err != nil {
				log.Errorf("本节点文件状态更新投递失败: %v", err)
			}
			continue
		}
		msg := notify.Message{
			Type: notify.MessageTypeFileStatusUpdateDeliver,
			Payload: notify.FileStatusUpdateDeliverPayload{
				Mid:        payload.Mid,
				Cid:        payload.Cid,
				TargetUids: targetUids,
			},
		}
		if err := push.PublishMessage(context.Background(), nodeID, msg); err != nil {
			log.Errorf("跨节点文件状态更新投递失败 node=%s mid=%s cid=%s: %v", nodeID, payload.Mid, payload.Cid, err)
		}
	}
}

func (s *Server) deliverFileStatusUpdateToTargets(mid string, cid string, targetUids []string) error {
	rmc := query.GetRoomMessageContentByMidAndCid(mid, cid)
	if rmc == nil {
		return nil
	}
	rm := query.GetRoomMessageByMid(mid)
	if rm == nil {
		return nil
	}
	serverFileStatusUpdate := &types.ServerFileStatusUpdate{
		ClientCid: rmc.ClientCid,
		UfId:      rmc.TypeId,
		Content:   rmc.Content,
	}
	for _, uid := range targetUids {
		client := s.GetClientByUid(uid)
		if client == nil {
			continue
		}
		data, err := client.EncodeServerMessageData(serverFileStatusUpdate)
		if err != nil {
			log.Error("编码房间消息内容更新失败:", err)
			continue
		}
		err = client.SendServerMessage(types.ServerMessageEntity{
			MessageType: quicEntity.TypeFileStatusUpdate,
			Data:        data,
		})
		if err != nil {
			log.Error("发送房间消息内容更新失败:", err)
			continue
		}
		if s.queueClient != nil {
			ackPayload := queue.FileStatusUpdateAckCheckPayload{
				Rid:        rm.Rid,
				Uid:        uid,
				ClientCid:  rmc.ClientCid,
				ClientMid:  rm.ClientMid,
				Mid:        rm.Mid,
				Cid:        rmc.Cid,
				RetryCount: 0,
			}
			err := queue.PublishTask(s.queueClient, queue.TaskFileStatusUpdateAckCheck, ackPayload, queue.AckCheckTimeout)
			if err != nil {
				log.Errorf("发布文件状态更新ACK检查任务失败: client_cid=%s, uid=%s, error=%v", rmc.ClientCid, uid, err)
			}
		}
	}
	return nil
}

func (s *Server) HandlerRoomMessage(payload event.RoomMessagePayload) {
	rec, err := notifycoord.PrepareRoomMessageNotify(payload.Mid, nil, nil)
	if err != nil {
		log.Error(err)
		return
	}
	if err := push.FanoutRoomMessageDelivery(context.Background(), payload.Mid, rec); err != nil {
		log.Errorf("FanoutRoomMessageDelivery: %v", err)
	}
}

// HandlerRoomMessageWithdraw 处理房间消息撤回
func (s *Server) HandlerRoomMessageWithdraw(payload event.RoomMessagePayload) {
	uids, err := notifycoord.PrepareRoomMessageWithdraw(payload.Mid)
	if err != nil {
		log.Error(err)
		return
	}
	if err := push.FanoutRoomWithdrawDelivery(context.Background(), payload.Mid, uids); err != nil {
		log.Errorf("FanoutRoomWithdrawDelivery: %v", err)
	}
}

// HandleRoomMessageResend 处理重发消息队列任务
func (s *Server) HandleRoomMessageResend(ctx context.Context, payload queue.RoomMessageResendPayload) error {
	client := s.GetClientByUid(payload.Uid)
	if client != nil {
		err := client.SendRoomMessage(payload.Message)
		if err != nil {
			log.Errorf("重发房间消息失败: mid=%s, uid=%s, error=%v", payload.Message.Mid, payload.Uid, err)
			return err
		}
		log.Infof("已重发消息: mid=%s, uid=%s", payload.Message.Mid, payload.Uid)
		return nil
	}
	node, err := push.NodeForUser(payload.Uid)
	if err != nil || node == "" {
		return nil
	}
	self := config.GetServerConfig().NodeID
	if node == self {
		return nil
	}
	return push.PublishDelegatedRoomResend(ctx, node, push.DelegatedRoomResend{
		Uid:     payload.Uid,
		Message: payload.Message,
	})
}

func (s *Server) handleDelegatedRoomResend(ctx context.Context, payload *push.DelegatedRoomResend) error {
	if payload == nil {
		return nil
	}
	client := s.GetClientByUid(payload.Uid)
	if client == nil {
		return nil
	}
	err := client.SendRoomMessage(payload.Message)
	if err != nil {
		log.Errorf("委托重发房间消息失败: mid=%s, uid=%s, error=%v", payload.Message.Mid, payload.Uid, err)
	}
	return err
}

// HandleFileStatusUpdateAckCheck 处理文件状态更新ACK检查任务（包括重发）
func (s *Server) HandleFileStatusUpdateAckCheck(ctx context.Context, payload queue.FileStatusUpdateAckCheckPayload) error {
	// 如果 retry_count > 0，说明这是重发任务
	if payload.RetryCount > 0 {
		return s.handleFileStatusUpdateResend(ctx, payload)
	}

	// 否则是 ACK 检查任务，由 queue handlers 处理
	// 这里不应该被调用，因为 ACK 检查任务应该在 queue handlers 中处理
	log.Warnf("HandleFileStatusUpdateAckCheck 被调用，但这不是重发任务: client_cid=%s, retry_count=%d", payload.ClientCid, payload.RetryCount)
	return nil
}

// HandleRoomMessageNotify 处理房间消息通知任务（队列版本，由HTTP服务通过队列发布）
// func (s *Server) HandleRoomMessageNotify(ctx context.Context, payload queue.RoomMessageNotifyPayload) error {
// 	return s.handleRoomMessageNotifyInternal(payload.Mid)
// }

// handleFileStatusUpdateResend 处理重发文件状态更新
func (s *Server) handleFileStatusUpdateResend(ctx context.Context, payload queue.FileStatusUpdateAckCheckPayload) error {
	client := s.GetClientByUid(payload.Uid)
	if client == nil {
		log.Warnf("用户不在线，无法重发文件状态更新: uid=%s, client_cid=%s", payload.Uid, payload.ClientCid)
		return nil // 用户不在线不算错误，直接返回
	}

	// 查询消息内容
	rmc := query.GetRoomMessageContentByMidAndCid(payload.Mid, payload.Cid)
	if rmc == nil {
		log.Errorf("查询房间消息内容失败: mid=%s, cid=%s", payload.Mid, payload.Cid)
		return nil
	}

	serverFileStatusUpdate := &types.ServerFileStatusUpdate{
		ClientCid: rmc.ClientCid,
		UfId:      rmc.TypeId,
		Content:   rmc.Content,
	}

	data, err := client.EncodeServerMessageData(serverFileStatusUpdate)
	if err != nil {
		log.Errorf("编码文件状态更新失败: %v", err)
		return err
	}

	err = client.SendServerMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeFileStatusUpdate,
		Data:        data,
	})
	if err != nil {
		log.Errorf("重发文件状态更新失败: client_cid=%s, uid=%s, error=%v", payload.ClientCid, payload.Uid, err)
		return err
	}
	log.Infof("已重发文件状态更新: client_cid=%s, uid=%s, retry_count=%d", payload.ClientCid, payload.Uid, payload.RetryCount)
	return nil
}

// handleRoomMessageWithdrawNotifyInternal 内部方法：处理用户撤回消息通知
func (s *Server) handleRoomMessageWithdrawNotifyInternal(mid string) error {
	if mid == "" {
		log.Warn("用户撤回消息通知的mid为空，忽略处理")
		return nil
	}
	return push.Send(context.Background(), notify.MessageTypeRoomMessageWithdrawNotify, notify.RoomMessageWithdrawNotifyPayload{Mid: mid})
}

// HandleRoomMessageWithdrawNotify 处理用户撤回消息通知任务（队列版本，由HTTP服务通过队列发布）
// func (s *Server) HandleRoomMessageWithdrawNotify(ctx context.Context, payload queue.RoomMessageWithdrawNotifyPayload) error {
// 	return s.handleRoomMessageWithdrawNotifyInternal(payload.Mid)
// }

// HandleRoomMessageWithdrawResend 处理重发撤回消息队列任务
func (s *Server) HandleRoomMessageWithdrawResend(ctx context.Context, payload queue.RoomMessageWithdrawResendPayload) error {
	client := s.GetClientByUid(payload.Uid)
	if client == nil {
		log.Warnf("用户不在线，无法重发撤回消息: uid=%s, mid=%s", payload.Uid, payload.Message.Mid)
		return nil // 用户不在线不算错误，直接返回
	}

	err := client.SendRoomMessageWithdraw(payload.Message)
	if err != nil {
		log.Errorf("重发撤回消息失败: mid=%s, uid=%s, error=%v", payload.Message.Mid, payload.Uid, err)
		return err
	}
	log.Infof("已重发撤回消息: mid=%s, uid=%s", payload.Message.Mid, payload.Uid)
	return nil
}

// HandleRoomMessageDeliver 集群：本节点向 target_uids 在线连接下发房间消息并挂 ACK 检查
func (s *Server) HandleRoomMessageDeliver(ctx context.Context, payload notify.RoomMessageDeliverPayload) error {
	if payload.Mid == "" {
		return nil
	}
	rcm := query.GetRoomMessageByMid(payload.Mid)
	if rcm == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, u := range payload.TargetUids {
		if u != "" {
			seen[u] = struct{}{}
		}
	}
	for uid := range seen {
		c := s.GetClientByUid(uid)
		if c == nil {
			continue
		}
		if err := c.SendRoomMessage(*rcm); err != nil {
			log.Errorf("发送房间消息失败: %v", err)
			continue
		}
		if s.queueClient != nil {
			ackPayload := queue.RoomMessageAckCheckPayload{
				Rid:        rcm.Rid,
				Uid:        uid,
				Mid:        rcm.Mid,
				SeqId:      rcm.SeqId,
				RetryCount: 0,
			}
			if err := queue.PublishTask(s.queueClient, queue.TaskRoomMessageAckCheck, ackPayload, queue.AckCheckTimeout); err != nil {
				log.Errorf("发布ACK检查任务失败: mid=%s, uid=%s, error=%v", rcm.Mid, uid, err)
			}
		}
	}
	return nil
}

// HandleFileStatusUpdateDeliver 集群：本节点向 target_uids 下发文件状态更新并挂 ACK 检查
func (s *Server) HandleFileStatusUpdateDeliver(ctx context.Context, payload notify.FileStatusUpdateDeliverPayload) error {
	if payload.Mid == "" || payload.Cid == "" || len(payload.TargetUids) == 0 {
		return nil
	}
	return s.deliverFileStatusUpdateToTargets(payload.Mid, payload.Cid, payload.TargetUids)
}

// HandleRoomMessageWithdrawDeliver 集群：本节点下发撤回并挂撤回 ACK 检查
func (s *Server) HandleRoomMessageWithdrawDeliver(ctx context.Context, payload notify.RoomMessageWithdrawDeliverPayload) error {
	if payload.Mid == "" {
		return nil
	}
	rcm := query.GetRoomMessageByMidIncludeWithdraw(payload.Mid)
	if rcm == nil {
		return nil
	}
	withdrawMsg := types.ServerRoomMessageWithdraw{
		SeqId:      rcm.SeqId,
		ClientMid:  rcm.ClientMid,
		Mid:        rcm.Mid,
		SenderUid:  rcm.SenderUid,
		Rid:        rcm.Rid,
		Contents:   rcm.Contents,
		CreateTime: rcm.CreateTime,
	}
	for _, uid := range payload.TargetUids {
		if uid == "" {
			continue
		}
		client := s.GetClientByUid(uid)
		if client == nil {
			continue
		}
		if err := client.SendRoomMessageWithdraw(withdrawMsg); err != nil {
			log.Errorf("发送房间消息撤回失败: %v", err)
			continue
		}
		if s.queueClient != nil {
			ackPayload := queue.RoomMessageWithdrawAckCheckPayload{
				Rid:        rcm.Rid,
				Uid:        uid,
				Mid:        rcm.Mid,
				SeqId:      rcm.SeqId,
				RetryCount: 0,
			}
			if err := queue.PublishTask(s.queueClient, queue.TaskRoomMessageWithdrawAckCheck, ackPayload, queue.AckCheckTimeout); err != nil {
				log.Errorf("发布消息撤回ACK检查任务失败: mid=%s, uid=%s, error=%v", rcm.Mid, uid, err)
			}
		}
	}
	return nil
}

// notify.Handler 实现（经由 Redis 定向投递到本节点）

// HandleRoomMessageWithdrawNotify 跨进程通知：处理用户撤回消息通知
func (s *Server) HandleRoomMessageWithdrawNotify(ctx context.Context, payload notify.RoomMessageWithdrawNotifyPayload) error {
	return s.handleRoomMessageWithdrawNotifyInternal(payload.Mid)
}

// HandleRoomMessageNotify 跨进程通知：处理房间消息通知
func (s *Server) HandleRoomMessageNotify(ctx context.Context, payload notify.RoomMessageNotifyPayload) error {
	return push.Send(ctx, notify.MessageTypeRoomMessageNotify, payload)
}

// HandleRoomMessageNotifyIncludeUids 跨进程通知：房间消息仅发送给指定 uid 列表
func (s *Server) HandleRoomMessageNotifyIncludeUids(ctx context.Context, payload notify.RoomMessageNotifyIncludeUidsPayload) error {
	return push.Send(ctx, notify.MessageTypeRoomMessageNotifyIncludeUids, payload)
}

// HandleRoomMessageNotifyExcludeUids 跨进程通知：房间消息发送给除指定 uid 列表外的成员
func (s *Server) HandleRoomMessageNotifyExcludeUids(ctx context.Context, payload notify.RoomMessageNotifyExcludeUidsPayload) error {
	return push.Send(ctx, notify.MessageTypeRoomMessageNotifyExcludeUids, payload)
}

// HandleNotificationNotify 跨进程通知：处理通知推送
func (s *Server) HandleNotificationNotify(ctx context.Context, payload notify.NotificationNotifyPayload) error {
	return s.handleNotificationNotifyInternal(payload.Nid)
}

// HandleRoomUserRoomNicknameNotify 跨进程通知：房间用户群昵称变更，广播给房间内在线用户静默更新 UI
func (s *Server) HandleRoomUserRoomNicknameNotify(ctx context.Context, payload notify.RoomUserRoomNicknameNotifyPayload) error {
	if len(payload.TargetUids) == 0 {
		return nil
	}
	for _, uid := range payload.TargetUids {
		client := s.GetClientByUid(uid)
		if client != nil {
			_ = client.SendRoomUserRoomNicknameUpdate(payload.Rid, payload.Uid, payload.RoomNickname)
		}
	}
	return nil
}

// HandleRoomUserIdsNotify 跨进程通知：房间成员列表变更，广播给房间内在线用户静默更新 UI
func (s *Server) HandleRoomUserIdsNotify(ctx context.Context, payload notify.RoomUserIdsNotifyPayload) error {
	if len(payload.TargetUids) == 0 || len(payload.UserIds) == 0 {
		return nil
	}
	for _, uid := range payload.TargetUids {
		client := s.GetClientByUid(uid)
		if client != nil {
			_ = client.SendRoomUserIdsUpdate(payload.Rid, payload.UserIds)
		}
	}
	return nil
}

// HandleRoomDissolvedNotify 跨进程通知：房间已解散，广播给房间内在线用户静默更新 UI
func (s *Server) HandleRoomDissolvedNotify(ctx context.Context, payload notify.RoomDissolvedNotifyPayload) error {
	if len(payload.TargetUids) == 0 || payload.Rid == "" {
		return nil
	}
	for _, uid := range payload.TargetUids {
		client := s.GetClientByUid(uid)
		if client != nil {
			_ = client.SendRoomDissolvedUpdate(payload.Rid, payload.RoomState)
		}
	}
	return nil
}

// HandleRoomAvatarNotify 跨进程通知：房间头像变更，广播给房间内在线用户静默更新 UI
func (s *Server) HandleRoomAvatarNotify(ctx context.Context, payload notify.RoomAvatarNotifyPayload) error {
	if len(payload.TargetUids) == 0 {
		return nil
	}
	for _, uid := range payload.TargetUids {
		client := s.GetClientByUid(uid)
		if client != nil {
			_ = client.SendRoomAvatarUpdate(payload.Rid, payload.AvatarUfId)
		}
	}
	return nil
}

// HandleUserStatusSyncNotify HTTP 修改用户展示状态后，广播 UserStatusSync（好友可见状态）
func (s *Server) HandleUserStatusSyncNotify(ctx context.Context, payload notify.UserStatusSyncNotifyPayload) error {
	if payload.Uid == "" || len(payload.TargetUids) == 0 {
		return nil
	}
	currentStatus, err := query.GetOrCreateUserCurrentStatus(payload.Uid)
	if err != nil {
		log.Errorf("HandleUserStatusSyncNotify 获取状态失败 uid=%s: %v", payload.Uid, err)
		return nil
	}
	friendPayload := buildFriendVisibleUserStatusSyncPayload(payload.Uid, currentStatus)
	for _, recv := range payload.TargetUids {
		rc := s.GetClientByUid(recv)
		if rc == nil {
			continue
		}
		s.deliverFriendUserStatusSyncPayload(rc, friendPayload)
	}
	return nil
}

// HandleUserProfileNotify 跨进程通知：用户资料变更，懒推送给与变更用户同房间的在线用户（已按 uid 去重，每人收一条）
func (s *Server) HandleUserProfileNotify(ctx context.Context, payload notify.UserProfileNotifyPayload) error {
	if len(payload.TargetUids) == 0 {
		return nil
	}
	userInfo := quicEntity.ServerUserInfo{
		Uid:          payload.User.Uid,
		Username:     payload.User.Username,
		Nickname:     payload.User.Nickname,
		Signature:    payload.User.Signature,
		Introduction: payload.User.Introduction,
		Email:        payload.User.Email,
		AvatarUfId:   payload.User.AvatarUfId,
		CreateTime:   payload.User.CreateTime,
	}
	for _, uid := range payload.TargetUids {
		client := s.GetClientByUid(uid)
		if client != nil {
			_ = client.SendUserProfileChange(userInfo)
		}
	}
	return nil
}

func (s *Server) HandleStreamCallInviteNotify(ctx context.Context, payload notify.StreamCallInviteNotifyPayload) error {
	if len(payload.InviteeUID) == 0 {
		return nil
	}
	msg := quicEntity.ServerStreamCallInvite{
		CallID:         payload.CallID,
		Rid:            payload.Rid,
		CallType:       payload.CallType,
		CallScene:      payload.CallScene,
		InviterUid:     payload.InviterUID,
		CreateTime:     payload.CreateTime,
		ExpireAt:       payload.ExpireAt,
		PublisherMedia: payload.PublisherMedia,
	}
	for _, uid := range payload.InviteeUID {
		client := s.GetClientByUid(uid)
		if client == nil {
			continue
		}
		data, err := client.EncodeServerMessageData(msg)
		if err != nil {
			continue
		}
		_ = client.SendServerMessage(types.ServerMessageEntity{
			MessageType: quicEntity.TypeStreamCallInvite,
			Data:        data,
		})
	}
	return nil
}

func (s *Server) HandleStreamCallJoinNotify(ctx context.Context, payload notify.StreamCallJoinNotifyPayload) error {
	if payload.TargetUID == "" {
		return nil
	}
	client := s.GetClientByUid(payload.TargetUID)
	if client == nil {
		return nil
	}
	msg := quicEntity.ServerStreamCallJoin{
		CallID:         payload.CallID,
		Rid:            payload.Rid,
		CallType:       payload.CallType,
		CallScene:      payload.CallScene,
		QuicAddr:       payload.QuicAddr,
		ALPN:           payload.ALPN,
		PublisherMedia: payload.PublisherMedia,
		JoinSign: quicEntity.ServerStreamCallJoinSign{
			UID:      payload.JoinSign.UID,
			SID:      payload.JoinSign.SID,
			RID:      payload.JoinSign.RID,
			Role:     payload.JoinSign.Role,
			Nonce:    payload.JoinSign.Nonce,
			ExpireAt: payload.JoinSign.ExpireAt,
			Sign:     payload.JoinSign.Sign,
		},
	}
	data, err := client.EncodeServerMessageData(msg)
	if err != nil {
		return nil
	}
	return client.SendServerMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeStreamCallJoin,
		Data:        data,
	})
}

func (s *Server) HandleStreamCallSyncNotify(ctx context.Context, payload notify.StreamCallSyncNotifyPayload) error {
	targets := payload.TargetUIDs
	if len(targets) == 0 {
		return nil
	}
	msg := quicEntity.ServerStreamCallSync{
		CallID:      payload.CallID,
		Rid:         payload.Rid,
		ActiveCount: int64(payload.ActiveCount),
	}
	for _, uid := range targets {
		client := s.GetClientByUid(uid)
		if client == nil {
			continue
		}
		data, err := client.EncodeServerMessageData(msg)
		if err != nil {
			continue
		}
		_ = client.SendServerMessage(types.ServerMessageEntity{
			MessageType: quicEntity.TypeStreamCallSync,
			Data:        data,
		})
	}
	return nil
}

func (s *Server) HandleStreamCallEndNotify(ctx context.Context, payload notify.StreamCallEndNotifyPayload) error {
	targets := payload.TargetUIDs
	if len(targets) == 0 {
		return nil
	}
	msg := quicEntity.ServerStreamCallEnd{
		CallID:      payload.CallID,
		Rid:         payload.Rid,
		Reason:      payload.Reason,
		OperatorUid: payload.OperatorUID,
		CallScene:   payload.CallScene,
		CallType:    payload.CallType,
		InviterUid:  payload.InviterUID,
		DurationSec: payload.DurationSec,
	}
	for _, uid := range targets {
		client := s.GetClientByUid(uid)
		if client == nil {
			continue
		}
		data, err := client.EncodeServerMessageData(msg)
		if err != nil {
			continue
		}
		_ = client.SendServerMessage(types.ServerMessageEntity{
			MessageType: quicEntity.TypeStreamCallEnd,
			Data:        data,
		})
	}
	return nil
}
