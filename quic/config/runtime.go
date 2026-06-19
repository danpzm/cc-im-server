package config

import (
	"time"

	"github.com/xd/quic-server/jwt"
)

const (
	// HeartbeatTimeout 心跳超时时间（秒）
	HeartbeatTimeout = 14 * time.Second
	// HeartbeatCheckInterval 心跳检测间隔（秒）
	HeartbeatCheckInterval = 5 * time.Second
)

func AccessTokenTtl() time.Duration {
	return jwt.AccessTokenExpire()
}

// TokenMinRemaining token 最小可用剩余时间（随 access token ttl 自适应）
func TokenMinRemaining() time.Duration {
	ttl := AccessTokenTtl()
	if ttl <= 0 {
		return time.Second
	}
	// 目标取 ttl 的 20%，并限制在 [1s, 30s]
	v := ttl / 5
	if v < time.Second {
		return time.Second
	}
	if v > 30*time.Second {
		return 30 * time.Second
	}
	return v
}

// TokenRefreshNoticeAhead token 过期前提醒窗口（随 access token ttl 自适应）
func TokenRefreshNoticeAhead() time.Duration {
	ttl := AccessTokenTtl()
	if ttl <= 0 {
		return 2 * time.Second
	}
	// 目标取 ttl 的 50%，并限制在 [2s, 5m]
	v := ttl / 2
	if v < 2*time.Second {
		return 2 * time.Second
	}
	if v > 5*time.Minute {
		return 5 * time.Minute
	}
	return v
}

// TokenTtlSafety token 缓存安全余量（随 access token ttl 自适应）
func TokenTtlSafety() time.Duration {
	ttl := AccessTokenTtl()
	if ttl <= 0 {
		return time.Second
	}
	// 目标取 ttl 的 10%，并限制在 [1s, 5s]
	v := ttl / 10
	if v < time.Second {
		return time.Second
	}
	if v > 5*time.Second {
		return 5 * time.Second
	}
	return v
}
