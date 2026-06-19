package serviceregistry

import (
	"fmt"
	"strings"

	"github.com/xd/quic-server/config"
)

// ResolvedHTTPBaseURL 本 HTTP 实例写入 Redis 的对外基地址（须显式配置 HTTP_CLIENT_BASE_URL）。
func ResolvedHTTPBaseURL(sc *config.ServerConfig) (string, error) {
	u := strings.TrimSpace(sc.HttpClientBaseURL)
	if u == "" {
		return "", fmt.Errorf("HTTP_CLIENT_BASE_URL 为空")
	}
	return strings.TrimRight(u, "/"), nil
}

// ResolvedOSSBaseURL 本 OSS 实例写入 Redis 的对外基地址（须显式配置 OSS_CLIENT_BASE_URL）。
func ResolvedOSSBaseURL(sc *config.ServerConfig) (string, error) {
	u := strings.TrimSpace(sc.OssClientBaseURL)
	if u == "" {
		return "", fmt.Errorf("OSS_CLIENT_BASE_URL 为空")
	}
	return strings.TrimRight(u, "/"), nil
}

// ResolvedMediaDialAddr 本媒体实例写入 Redis 的对外 host:port（须显式配置 MEDIA_CLIENT_DIAL_ADDR）。
func ResolvedMediaDialAddr(sc *config.ServerConfig) (string, error) {
	a := strings.TrimSpace(sc.MediaClientDialAddr)
	if a == "" {
		return "", fmt.Errorf("MEDIA_CLIENT_DIAL_ADDR 为空")
	}
	return a, nil
}
