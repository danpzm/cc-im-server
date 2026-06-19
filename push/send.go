package push

import (
	"context"

	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/notifycoord"
	"github.com/xd/quic-server/notify"
)

// Send HTTP / 后台任务入口：按消息类型做协调与按节点定向发布到 Redis
func Send(ctx context.Context, msgType notify.MessageType, payload any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	switch msgType {
	case notify.MessageTypeRoomMessageNotify:
		p := payload.(notify.RoomMessageNotifyPayload)
		rec, err := notifycoord.PrepareRoomMessageNotify(p.Mid, nil, nil)
		if err != nil {
			return err
		}
		return FanoutRoomMessageDelivery(ctx, p.Mid, rec)

	case notify.MessageTypeRoomMessageNotifyIncludeUids:
		p := payload.(notify.RoomMessageNotifyIncludeUidsPayload)
		rec, err := notifycoord.PrepareRoomMessageNotify(p.Mid, p.UidList, nil)
		if err != nil {
			return err
		}
		return FanoutRoomMessageDelivery(ctx, p.Mid, rec)

	case notify.MessageTypeRoomMessageNotifyExcludeUids:
		p := payload.(notify.RoomMessageNotifyExcludeUidsPayload)
		rec, err := notifycoord.PrepareRoomMessageNotify(p.Mid, nil, p.ExcludeUidList)
		if err != nil {
			return err
		}
		return FanoutRoomMessageDelivery(ctx, p.Mid, rec)

	case notify.MessageTypeRoomMessageWithdrawNotify:
		p := payload.(notify.RoomMessageWithdrawNotifyPayload)
		uids, err := notifycoord.PrepareRoomMessageWithdraw(p.Mid)
		if err != nil {
			return err
		}
		return FanoutRoomWithdrawDelivery(ctx, p.Mid, uids)

	case notify.MessageTypeNotificationNotify:
		p := payload.(notify.NotificationNotifyPayload)
		n, err := query.GetMessageNotificationByNid(p.Nid)
		if err != nil || n == nil {
			return nil
		}
		node, err := NodeForUser(n.Uid)
		if err != nil || node == "" {
			return nil
		}
		return PublishMessage(ctx, node, notify.Message{Type: msgType, Payload: p})

	case notify.MessageTypeRoomUserRoomNicknameNotify:
		p := payload.(notify.RoomUserRoomNicknameNotifyPayload)
		uids, err := query.GetRoomUserIdsCache(p.Rid)
		if err != nil || len(uids) == 0 {
			return nil
		}
		groups := GroupUidsByNode(uids)
		for nodeID, subset := range groups {
			pp := p
			pp.TargetUids = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	case notify.MessageTypeRoomUserIdsNotify:
		p := payload.(notify.RoomUserIdsNotifyPayload)
		uids, err := query.GetRoomUserIdsCache(p.Rid)
		if err != nil || len(uids) == 0 {
			return nil
		}
		if len(p.UserIds) == 0 {
			p.UserIds = uids
		}
		groups := GroupUidsByNode(uids)
		for nodeID, subset := range groups {
			pp := p
			pp.TargetUids = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	case notify.MessageTypeRoomDissolvedNotify:
		p := payload.(notify.RoomDissolvedNotifyPayload)
		uids, err := query.GetRoomUserIdsCache(p.Rid)
		if err != nil || len(uids) == 0 {
			return nil
		}
		groups := GroupUidsByNode(uids)
		for nodeID, subset := range groups {
			pp := p
			pp.TargetUids = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	case notify.MessageTypeRoomAvatarNotify:
		p := payload.(notify.RoomAvatarNotifyPayload)
		uids, err := query.GetRoomUserIdsCache(p.Rid)
		if err != nil || len(uids) == 0 {
			return nil
		}
		groups := GroupUidsByNode(uids)
		for nodeID, subset := range groups {
			pp := p
			pp.TargetUids = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	case notify.MessageTypeUserProfileNotify:
		p := payload.(notify.UserProfileNotifyPayload)
		uids, err := query.GetUidsSharingRoomWith(p.User.Uid)
		if err != nil || len(uids) == 0 {
			return nil
		}
		groups := GroupUidsByNode(uids)
		for nodeID, subset := range groups {
			pp := p
			pp.TargetUids = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	case notify.MessageTypeUserStatusSyncNotify:
		p := payload.(notify.UserStatusSyncNotifyPayload)
		uids, err := notifycoord.FriendStatusSyncRecipientUids(p.Uid)
		if err != nil {
			return err
		}
		groups := GroupUidsByNode(uids)
		for nodeID, subset := range groups {
			pp := p
			pp.TargetUids = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	case notify.MessageTypeStreamCallInviteNotify:
		p := payload.(notify.StreamCallInviteNotifyPayload)
		groups := GroupUidsByNode(p.InviteeUID)
		for nodeID, subset := range groups {
			pp := p
			pp.InviteeUID = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	case notify.MessageTypeStreamCallJoinNotify:
		p := payload.(notify.StreamCallJoinNotifyPayload)
		node, err := NodeForUser(p.TargetUID)
		if err != nil || node == "" {
			return nil
		}
		return PublishMessage(ctx, node, notify.Message{Type: msgType, Payload: p})

	case notify.MessageTypeStreamCallEndNotify:
		p := payload.(notify.StreamCallEndNotifyPayload)
		groups := GroupUidsByNode(p.TargetUIDs)
		for nodeID, subset := range groups {
			pp := p
			pp.TargetUIDs = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	case notify.MessageTypeStreamCallSyncNotify:
		p := payload.(notify.StreamCallSyncNotifyPayload)
		groups := GroupUidsByNode(p.TargetUIDs)
		for nodeID, subset := range groups {
			pp := p
			pp.TargetUIDs = subset
			if err := PublishMessage(ctx, nodeID, notify.Message{Type: msgType, Payload: pp}); err != nil {
				return err
			}
		}
		return nil

	default:
		return nil
	}
}
