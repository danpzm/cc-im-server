package netutil

import "net"

// ExtractIP 从 "host:port" 或 "[ipv6]:port" 地址中提取 IP，不含端口。
func ExtractIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
