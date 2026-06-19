package auth

import (
	"context"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/pkg/quiccluster"
	"github.com/xd/quic-server/pkg/serviceregistry"
)

type loginClusterAddrs struct {
	Quic []string
	HTTP []string
	OSS  []string
}

func fetchLoginClusterAddrs(ctx context.Context) (loginClusterAddrs, error) {
	sc := config.GetServerConfig()
	out := loginClusterAddrs{}

	quicAddrs, err := quiccluster.FetchAddrs(ctx, sc)
	if err != nil {
		return out, err
	}
	out.Quic = quicAddrs

	httpAddrs, err := serviceregistry.Fetch(ctx, serviceregistry.KindHTTP, sc.HttpBaseURLsRedisKey)
	if err != nil {
		return out, err
	}
	out.HTTP = httpAddrs

	ossAddrs, err := serviceregistry.Fetch(ctx, serviceregistry.KindOSS, sc.OssBaseURLsRedisKey)
	if err != nil {
		return out, err
	}
	out.OSS = ossAddrs

	return out, nil
}

// fetchLoginClusterAddrsBestEffort 刷新 token 时使用：任一集群拉取失败仅告警，不阻断。
func fetchLoginClusterAddrsBestEffort(ctx context.Context) loginClusterAddrs {
	sc := config.GetServerConfig()
	out := loginClusterAddrs{}

	if addrs, err := quiccluster.FetchAddrs(ctx, sc); err != nil {
		log.Warnf("刷新令牌：拉取 QUIC 集群失败: %v", err)
	} else {
		out.Quic = addrs
	}
	if addrs, err := serviceregistry.Fetch(ctx, serviceregistry.KindHTTP, sc.HttpBaseURLsRedisKey); err != nil {
		log.Warnf("刷新令牌：拉取 HTTP 集群失败: %v", err)
	} else {
		out.HTTP = addrs
	}
	if addrs, err := serviceregistry.Fetch(ctx, serviceregistry.KindOSS, sc.OssBaseURLsRedisKey); err != nil {
		log.Warnf("刷新令牌：拉取 OSS 集群失败: %v", err)
	} else {
		out.OSS = addrs
	}
	return out
}
