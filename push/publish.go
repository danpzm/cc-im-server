package push

import (
	"context"
	"fmt"

	"github.com/xd/quic-server/notify"
	"github.com/xd/quic-server/redis"
)

func channelName(nodeID string) string {
	return fmt.Sprintf("quic:push:%s", nodeID)
}

// PublishEnvelope 向指定节点发布一条 Envelope（仅该节点订阅者收到）
func PublishEnvelope(ctx context.Context, nodeID string, env Envelope) error {
	if nodeID == "" {
		return nil
	}
	b, err := MarshalEnvelope(env)
	if err != nil {
		return err
	}
	return redis.GetClient().Publish(ctx, channelName(nodeID), b).Err()
}

// PublishMessage 将一条 notify.Message 发布到指定 QUIC 节点
func PublishMessage(ctx context.Context, nodeID string, msg notify.Message) error {
	return PublishEnvelope(ctx, nodeID, Envelope{Message: &msg})
}

// PublishDelegatedRoomResend 将重发任务转交给持有用户连接的节点
func PublishDelegatedRoomResend(ctx context.Context, targetNode string, p DelegatedRoomResend) error {
	if targetNode == "" || p.Uid == "" {
		return nil
	}
	pp := p
	return PublishEnvelope(ctx, targetNode, Envelope{DelegatedRoomResend: &pp})
}
