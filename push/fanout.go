package push

import (
	"context"

	"github.com/xd/quic-server/notify"
)

// FanoutRoomMessageDelivery 按节点拆分在线用户并投递 room_message_deliver
func FanoutRoomMessageDelivery(ctx context.Context, mid string, recipientUids []string) error {
	if mid == "" {
		return nil
	}
	groups := GroupUidsByNode(recipientUids)
	for nodeID, uids := range groups {
		if len(uids) == 0 {
			continue
		}
		err := PublishMessage(ctx, nodeID, notify.Message{
			Type: notify.MessageTypeRoomMessageDeliver,
			Payload: notify.RoomMessageDeliverPayload{
				Mid:        mid,
				TargetUids: uids,
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// FanoutRoomWithdrawDelivery 按节点投递撤回
func FanoutRoomWithdrawDelivery(ctx context.Context, mid string, roomUserUids []string) error {
	if mid == "" {
		return nil
	}
	groups := GroupUidsByNode(roomUserUids)
	for nodeID, uids := range groups {
		if len(uids) == 0 {
			continue
		}
		err := PublishMessage(ctx, nodeID, notify.Message{
			Type: notify.MessageTypeRoomMessageWithdrawDeliver,
			Payload: notify.RoomMessageWithdrawDeliverPayload{
				Mid:        mid,
				TargetUids: uids,
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}
