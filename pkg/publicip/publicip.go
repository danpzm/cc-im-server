package publicip

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const cacheTTL = time.Hour

var (
	cacheMu sync.RWMutex
	cache   string
	cacheAt time.Time
)

// IsLocal 判断是否为本地或私有地址（127.x、10.x、192.168.x、172.16-31.x、::1 等）。
func IsLocal(ipAddr netip.Addr) bool {
	if ipAddr == netip.MustParseAddr("::1") {
		return true
	}
	if ipAddr.Is4() {
		ip := ipAddr.As4()
		if ip[0] == 127 {
			return true
		}
		if ip[0] == 10 {
			return true
		}
		if ip[0] == 192 && ip[1] == 168 {
			return true
		}
		if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
			return true
		}
		if ip[0] == 169 && ip[1] == 254 {
			return true
		}
	}
	if ipAddr.Is6() {
		ip := ipAddr.As16()
		if ip[0] == 0xfc || ip[0] == 0xfd {
			return true
		}
		if ip[0] == 0xfe && (ip[1]&0xc0) == 0x80 {
			return true
		}
	}
	return false
}

// ConvertIPv6ToIPv4 将 ::1 或 IPv4-mapped IPv6 转为 IPv4。
func ConvertIPv6ToIPv4(ipv6 netip.Addr) netip.Addr {
	if ipv6 == netip.MustParseAddr("::1") {
		return netip.MustParseAddr("127.0.0.1")
	}
	if ipv6.Is4In6() {
		ipv6Bytes := ipv6.As16()
		ipv4Bytes := [4]byte{ipv6Bytes[12], ipv6Bytes[13], ipv6Bytes[14], ipv6Bytes[15]}
		if ipv4Addr, ok := netip.AddrFromSlice(ipv4Bytes[:]); ok {
			return ipv4Addr
		}
	}
	return netip.Addr{}
}

// Get 通过多个 HTTP 端点获取本机公网 IP（带缓存）。
func Get() (string, error) {
	cacheMu.RLock()
	if cache != "" && time.Since(cacheAt) < cacheTTL {
		ip := cache
		cacheMu.RUnlock()
		log.Debugf("使用缓存的公网IP: %s", ip)
		return ip, nil
	}
	cacheMu.RUnlock()

	endpoints := []struct {
		url    string
		format string
	}{
		{"https://api.ipify.org?format=json", "json"},
		{"https://api.ipify.org", "text"},
		{"https://api64.ipify.org?format=json", "json"},
		{"https://ifconfig.me/ip", "text"},
		{"https://icanhazip.com", "text"},
		{"https://ident.me", "text"},
	}

	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error

	for _, endpoint := range endpoints {
		for retry := range 2 {
			if retry > 0 {
				time.Sleep(time.Duration(retry) * 500 * time.Millisecond)
			}
			req, err := http.NewRequest("GET", endpoint.url, nil)
			if err != nil {
				lastErr = fmt.Errorf("创建请求失败: %v", err)
				continue
			}
			req.Header.Set("User-Agent", "cc-server/1.0")

			resp, err := client.Do(req)
			if err != nil {
				lastErr = fmt.Errorf("请求失败: %v", err)
				continue
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = fmt.Errorf("读取响应失败: %v", err)
				continue
			}
			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("API返回错误状态码: %d", resp.StatusCode)
				continue
			}

			var ip string
			if endpoint.format == "json" {
				var result struct {
					IP string `json:"ip"`
				}
				if err := json.Unmarshal(body, &result); err != nil {
					ip = strings.TrimSpace(string(body))
				} else {
					ip = result.IP
				}
			} else {
				ip = strings.TrimSpace(string(body))
			}

			ipAddr, err := netip.ParseAddr(ip)
			if err != nil || !ipAddr.IsValid() || IsLocal(ipAddr) {
				lastErr = fmt.Errorf("无效的IP地址: %s", ip)
				continue
			}

			cacheMu.Lock()
			cache = ip
			cacheAt = time.Now()
			cacheMu.Unlock()
			log.Debugf("成功获取公网IP: %s (来源: %s)", ip, endpoint.url)
			return ip, nil
		}
	}
	return "", fmt.Errorf("所有API都失败，最后一个错误: %v", lastErr)
}
