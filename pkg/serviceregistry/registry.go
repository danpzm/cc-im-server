package serviceregistry

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/xd/quic-server/redis"
)

// Kind 集群服务类型：各进程启动时向 Redis SET 注册对外地址，HTTP 登录或业务接口读取并负载均衡。
type Kind string

const (
	KindHTTP  Kind = "http"
	KindOSS   Kind = "oss"
	KindQUIC  Kind = "quic"
	KindMedia Kind = "media"
)

func defaultRedisKey(kind Kind) string {
	switch kind {
	case KindHTTP:
		return "http:base_urls"
	case KindOSS:
		return "oss:base_urls"
	case KindQUIC:
		return "quic:dial_addrs"
	case KindMedia:
		return "media:dial_addrs"
	default:
		return string(kind) + ":endpoints"
	}
}

// RedisKey 返回该类型在 Redis 中的 SET 键；override 非空时优先使用。
func RedisKey(kind Kind, override string) string {
	if k := strings.TrimSpace(override); k != "" {
		return k
	}
	return defaultRedisKey(kind)
}

// Register 将本实例对外地址写入 Redis SET（仅写入当前进程的 CLIENT_BASE_URL / DIAL_ADDR）。
func Register(ctx context.Context, kind Kind, redisKeyOverride, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("%s 对外地址为空", kind)
	}
	if err := RejectLoopbackRegister(endpoint); err != nil {
		return err
	}
	key := RedisKey(kind, redisKeyOverride)
	client := redis.GetClient()
	if !IsDevCluster() {
		members, err := client.SMembers(ctx, key).Result()
		if err != nil {
			return fmt.Errorf("读取 Redis SET %q: %w", key, err)
		}
		for _, m := range members {
			if IsLoopbackEndpoint(m) {
				if err := client.SRem(ctx, key, m).Err(); err != nil {
					return fmt.Errorf("清理 Redis SET %q 回环地址: %w", key, err)
				}
			}
		}
	}
	return client.SAdd(ctx, key, endpoint).Err()
}

// Unregister 进程退出时从 SET 移除本实例地址。
func Unregister(ctx context.Context, kind Kind, redisKeyOverride, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	key := RedisKey(kind, redisKeyOverride)
	return redis.GetClient().SRem(ctx, key, endpoint).Err()
}

// Fetch 读取当前已注册的全部地址（去空白、去重保序）。
func Fetch(ctx context.Context, kind Kind, redisKeyOverride string) ([]string, error) {
	key := RedisKey(kind, redisKeyOverride)
	members, err := redis.GetClient().SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("读取 Redis SET %q: %w", key, err)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("Redis SET %q 为空，请确认已启动对应服务并成功注册", key)
	}
	filtered := FilterEndpoints(members)
	if len(filtered) == 0 {
		if IsDevCluster() {
			return nil, fmt.Errorf("Redis SET %q 无可用地址", key)
		}
		return nil, fmt.Errorf("Redis SET %q 仅含 127.0.0.1/localhost 地址（生产环境请配置公网 HTTP_CLIENT_BASE_URL 等，并清理 Redis 旧节点；本地开发请用 scripts/dev.ps1）", key)
	}
	return filtered, nil
}

// Pick 从 SET 中随机选取一个地址（用于媒体 QUIC 等单次下发场景）。
func Pick(ctx context.Context, kind Kind, redisKeyOverride string) (string, error) {
	addrs, err := Fetch(ctx, kind, redisKeyOverride)
	if err != nil {
		return "", err
	}
	if len(addrs) == 1 {
		return addrs[0], nil
	}
	return addrs[rand.IntN(len(addrs))], nil
}

// RegisterWithCleanup 注册并在返回的 cancel 中注销（供 main 使用）。
func RegisterWithCleanup(ctx context.Context, kind Kind, redisKeyOverride, endpoint string) (func(), error) {
	regCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := Register(regCtx, kind, redisKeyOverride, endpoint); err != nil {
		return nil, err
	}
	return func() {
		unregCtx, unregCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer unregCancel()
		if err := Unregister(unregCtx, kind, redisKeyOverride, endpoint); err != nil {
			// 调用方自行打日志
			_ = err
		}
	}, nil
}

func trimNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
