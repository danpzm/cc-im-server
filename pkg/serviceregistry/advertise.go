package serviceregistry

import (
	"net"
)

// AdvertiseHostPort 将监听地址中的 0.0.0.0/:: 替换为客户端可达的回环地址。
func AdvertiseHostPort(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
