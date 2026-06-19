package serviceregistry

import (
	"net"
	"net/url"
	"os"
	"strings"
)

// IsDevCluster 为 true 表示由 scripts/dev.ps1 启动（允许 localhost / 127.0.0.1 注册与下发）。
func IsDevCluster() bool {
	return strings.TrimSpace(os.Getenv("CC_DEV_CLUSTER")) == "1"
}

// IsLoopbackEndpoint 判断对外地址是否为回环（含 http://localhost、127.0.0.1、localhost:port）。
func IsLoopbackEndpoint(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return false
		}
		return isLoopbackHost(u.Hostname())
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return isLoopbackHost(endpoint)
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// FilterEndpoints 非开发模式下剔除回环地址；开发模式原样返回。
func FilterEndpoints(endpoints []string) []string {
	if IsDevCluster() {
		return trimNonEmpty(endpoints)
	}
	out := make([]string, 0, len(endpoints))
	for _, s := range trimNonEmpty(endpoints) {
		if IsLoopbackEndpoint(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// RejectLoopbackRegister 非开发集群禁止将回环地址写入 Redis。
func RejectLoopbackRegister(endpoint string) error {
	if IsDevCluster() || !IsLoopbackEndpoint(endpoint) {
		return nil
	}
	return errLoopbackRegister
}
