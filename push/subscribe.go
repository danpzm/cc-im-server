package push

import (
	"context"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/notify"
	"github.com/xd/quic-server/redis"
)

// Subscriber 绑定到某一 QUIC 节点，消费定向到本节点的 Redis 频道
type Subscriber struct {
	NodeID                string
	Handler               notify.Handler
	OnDelegatedRoomResend func(context.Context, *DelegatedRoomResend) error
}

// Run 阻塞监听直至 ctx 取消
func (s *Subscriber) Run(ctx context.Context) error {
	chName := channelName(s.NodeID)
	sub := redis.GetClient().Subscribe(ctx, chName)
	defer sub.Close()
	log.Infof("push 订阅已启动 channel=%s", chName)
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			env, err := UnmarshalEnvelope([]byte(msg.Payload))
			if err != nil {
				log.Errorf("push 解码失败: %v", err)
				continue
			}
			if env.Message != nil {
				if err := notify.Dispatch(ctx, s.Handler, env.Message); err != nil {
					log.Errorf("push 分发跨进程通知失败: %v", err)
				}
			}
			if env.DelegatedRoomResend != nil && s.OnDelegatedRoomResend != nil {
				if err := s.OnDelegatedRoomResend(ctx, env.DelegatedRoomResend); err != nil {
					log.Errorf("push 处理委托重发失败: %v", err)
				}
			}
		}
	}
}
