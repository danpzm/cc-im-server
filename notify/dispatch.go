package notify

import (
	"context"
	"encoding/json"

	log "github.com/sirupsen/logrus"
)

// Handler QUIC 节点对 Redis 投递的 Message 的处理接口
type Handler interface {
	HandleRoomMessageWithdrawNotify(ctx context.Context, payload RoomMessageWithdrawNotifyPayload) error
	HandleRoomMessageNotify(ctx context.Context, payload RoomMessageNotifyPayload) error
	HandleRoomMessageNotifyIncludeUids(ctx context.Context, payload RoomMessageNotifyIncludeUidsPayload) error
	HandleRoomMessageNotifyExcludeUids(ctx context.Context, payload RoomMessageNotifyExcludeUidsPayload) error
	HandleNotificationNotify(ctx context.Context, payload NotificationNotifyPayload) error
	HandleRoomUserRoomNicknameNotify(ctx context.Context, payload RoomUserRoomNicknameNotifyPayload) error
	HandleRoomUserIdsNotify(ctx context.Context, payload RoomUserIdsNotifyPayload) error
	HandleRoomDissolvedNotify(ctx context.Context, payload RoomDissolvedNotifyPayload) error
	HandleRoomAvatarNotify(ctx context.Context, payload RoomAvatarNotifyPayload) error
	HandleUserProfileNotify(ctx context.Context, payload UserProfileNotifyPayload) error
	HandleUserStatusSyncNotify(ctx context.Context, payload UserStatusSyncNotifyPayload) error
	HandleStreamCallInviteNotify(ctx context.Context, payload StreamCallInviteNotifyPayload) error
	HandleStreamCallJoinNotify(ctx context.Context, payload StreamCallJoinNotifyPayload) error
	HandleStreamCallEndNotify(ctx context.Context, payload StreamCallEndNotifyPayload) error
	HandleStreamCallSyncNotify(ctx context.Context, payload StreamCallSyncNotifyPayload) error
	HandleRoomMessageDeliver(ctx context.Context, payload RoomMessageDeliverPayload) error
	HandleRoomMessageWithdrawDeliver(ctx context.Context, payload RoomMessageWithdrawDeliverPayload) error
	HandleFileStatusUpdateDeliver(ctx context.Context, payload FileStatusUpdateDeliverPayload) error
}

func unmarshalPayload(payload any, target any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// Dispatch 将一条 Message 分发给 Handler
func Dispatch(ctx context.Context, h Handler, msg *Message) error {
	switch msg.Type {
	case MessageTypeRoomMessageWithdrawNotify:
		var payload RoomMessageWithdrawNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomMessageWithdrawNotify(ctx, payload)

	case MessageTypeRoomMessageNotify:
		var payload RoomMessageNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomMessageNotify(ctx, payload)

	case MessageTypeRoomMessageNotifyIncludeUids:
		var payload RoomMessageNotifyIncludeUidsPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomMessageNotifyIncludeUids(ctx, payload)

	case MessageTypeRoomMessageNotifyExcludeUids:
		var payload RoomMessageNotifyExcludeUidsPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomMessageNotifyExcludeUids(ctx, payload)

	case MessageTypeNotificationNotify:
		var payload NotificationNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleNotificationNotify(ctx, payload)

	case MessageTypeRoomUserRoomNicknameNotify:
		var payload RoomUserRoomNicknameNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomUserRoomNicknameNotify(ctx, payload)

	case MessageTypeRoomUserIdsNotify:
		var payload RoomUserIdsNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomUserIdsNotify(ctx, payload)

	case MessageTypeRoomDissolvedNotify:
		var payload RoomDissolvedNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomDissolvedNotify(ctx, payload)

	case MessageTypeRoomAvatarNotify:
		var payload RoomAvatarNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomAvatarNotify(ctx, payload)

	case MessageTypeUserProfileNotify:
		var payload UserProfileNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleUserProfileNotify(ctx, payload)

	case MessageTypeUserStatusSyncNotify:
		var payload UserStatusSyncNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleUserStatusSyncNotify(ctx, payload)

	case MessageTypeStreamCallInviteNotify:
		var payload StreamCallInviteNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleStreamCallInviteNotify(ctx, payload)

	case MessageTypeStreamCallJoinNotify:
		var payload StreamCallJoinNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleStreamCallJoinNotify(ctx, payload)

	case MessageTypeStreamCallEndNotify:
		var payload StreamCallEndNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleStreamCallEndNotify(ctx, payload)

	case MessageTypeStreamCallSyncNotify:
		var payload StreamCallSyncNotifyPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleStreamCallSyncNotify(ctx, payload)

	case MessageTypeRoomMessageDeliver:
		var payload RoomMessageDeliverPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomMessageDeliver(ctx, payload)

	case MessageTypeRoomMessageWithdrawDeliver:
		var payload RoomMessageWithdrawDeliverPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleRoomMessageWithdrawDeliver(ctx, payload)

	case MessageTypeFileStatusUpdateDeliver:
		var payload FileStatusUpdateDeliverPayload
		if err := unmarshalPayload(msg.Payload, &payload); err != nil {
			return err
		}
		return h.HandleFileStatusUpdateDeliver(ctx, payload)

	default:
		log.Warnf("未识别的跨进程通知类型: %s", msg.Type)
		return nil
	}
}
