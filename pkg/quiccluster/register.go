package quiccluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/pkg/serviceregistry"
)

// RegisterDialAddr 将本 QUIC 实例的客户端拨号地址写入 Redis SET，供 HTTP 登录下发 quic_addrs。
func RegisterDialAddr(ctx context.Context, sc *config.ServerConfig) error {
	addr := strings.TrimSpace(sc.QuicClientDialAddr)
	if addr == "" {
		return fmt.Errorf("QUIC_CLIENT_DIAL_ADDR 为空")
	}
	return serviceregistry.Register(ctx, serviceregistry.KindQUIC, sc.QuicDialAddrsRedisKey, addr)
}

// UnregisterDialAddr 进程退出时从 SET 移除本实例地址。
func UnregisterDialAddr(ctx context.Context, sc *config.ServerConfig) error {
	addr := strings.TrimSpace(sc.QuicClientDialAddr)
	if addr == "" {
		return nil
	}
	return serviceregistry.Unregister(ctx, serviceregistry.KindQUIC, sc.QuicDialAddrsRedisKey, addr)
}
