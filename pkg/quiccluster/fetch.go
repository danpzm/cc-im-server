package quiccluster

import (
	"context"

	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/pkg/serviceregistry"
)

// DialAddrsRedisKey 客户端可拨 QUIC 地址集合在 Redis 中的 SET 键（各 cmd/quic 启动时 SADD）。
func DialAddrsRedisKey(sc *config.ServerConfig) string {
	return serviceregistry.RedisKey(serviceregistry.KindQUIC, sc.QuicDialAddrsRedisKey)
}

// FetchAddrs 从 Redis SET 读取当前已注册的客户端拨号地址列表（由各 QUIC 进程写入）。
func FetchAddrs(ctx context.Context, sc *config.ServerConfig) ([]string, error) {
	return serviceregistry.Fetch(ctx, serviceregistry.KindQUIC, sc.QuicDialAddrsRedisKey)
}
