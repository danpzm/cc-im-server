package server

import (
	"sync"

	"github.com/xd/quic-server/quic/client"
)

// clientRegistry 管理 uid -> 当前活跃 QUIC 连接（每用户仅一条），所有读写均经 mutex 保护。
type clientRegistry struct {
	mu      sync.RWMutex
	clients map[string]*client.Client
}

func newClientRegistry() *clientRegistry {
	return &clientRegistry{
		clients: make(map[string]*client.Client),
	}
}

func (r *clientRegistry) get(uid string) (*client.Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[uid]
	return c, ok && c != nil
}

func (r *clientRegistry) register(c *client.Client) {
	if c == nil || c.User() == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[c.User().Uid] = c
}

// isCurrentConn 判断 map 中该 uid 的活跃连接是否仍为给定 sid+connID。
// connID 必须为具体连接实例；为 0 时一律视为无法确认（避免旧格式心跳/事件误伤新连接）。
func (r *clientRegistry) isCurrentConn(uid, sid string, connID uintptr) bool {
	if connID == 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[uid]
	if !ok || c == nil || c.Sid() != sid {
		return false
	}
	return c.ConnID() == connID
}

// removeIfMatch 仅当 map 中连接与 sid、connID 一致时移除，避免误删已替换的新连接。
func (r *clientRegistry) removeIfMatch(uid, sid string, connID uintptr) (*client.Client, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[uid]
	if !ok || c == nil {
		return nil, false
	}
	if c.Sid() != sid || connID == 0 || c.ConnID() != connID {
		return nil, false
	}
	delete(r.clients, uid)
	return c, true
}

// snapshotForReplace 在注册新连接前取出旧连接（调用方须在锁外关闭旧 conn）。
func (r *clientRegistry) snapshotForReplace(uid string) *client.Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.clients[uid]
}

func (r *clientRegistry) len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.clients)
}

func (r *clientRegistry) snapshotAll() []*client.Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*client.Client, 0, len(r.clients))
	for _, c := range r.clients {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

func (r *clientRegistry) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients = make(map[string]*client.Client)
}
