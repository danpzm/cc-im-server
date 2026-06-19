package heartbeat

import (
	"container/heap"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// heartbeatEntry 心跳条目
type heartbeatEntry struct {
	clientID   string
	lastHbTime time.Time
	index      int // 堆索引
}

// heartbeatHeap 心跳时间堆（最小堆）
type heartbeatHeap []*heartbeatEntry

func (h heartbeatHeap) Len() int           { return len(h) }
func (h heartbeatHeap) Less(i, j int) bool { return h[i].lastHbTime.Before(h[j].lastHbTime) }
func (h heartbeatHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *heartbeatHeap) Push(x any) {
	n := len(*h)
	item := x.(*heartbeatEntry)
	item.index = n
	*h = append(*h, item)
}

func (h *heartbeatHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[0 : n-1]
	return item
}

// Manager 心跳管理器
type Manager struct {
	mu        sync.RWMutex
	entries   map[string]*heartbeatEntry // 客户端ID -> 心跳条目
	heap      *heartbeatHeap             // 按心跳时间排序的最小堆
	timeout   time.Duration              // 心跳超时时间
	interval  time.Duration              // 心跳检测间隔
	stopChan  chan struct{}
	wg        sync.WaitGroup
	batchSize int // 每次检查的超时客户端数量
}

// NewManager 创建心跳管理器
func NewManager(timeout time.Duration, interval time.Duration) *Manager {
	h := make(heartbeatHeap, 0)
	return &Manager{
		entries:   make(map[string]*heartbeatEntry),
		heap:      &h,
		timeout:   timeout,
		interval:  interval,
		stopChan:  make(chan struct{}),
		batchSize: 100, // 每次最多处理100个超时客户端
	}
}

// UpdateHeartbeat 更新客户端心跳时间
func (m *Manager) UpdateHeartbeat(clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	entry, exists := m.entries[clientID]

	if exists {
		// 更新现有条目
		entry.lastHbTime = now
		heap.Fix(m.heap, entry.index)
	} else {
		// 创建新条目
		entry = &heartbeatEntry{
			clientID:   clientID,
			lastHbTime: now,
		}
		m.entries[clientID] = entry
		heap.Push(m.heap, entry)
		log.Debugf("客户端 %s 已添加到心跳管理器", clientID)
	}
}

// RemoveClient 移除客户端
func (m *Manager) RemoveClient(clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.entries[clientID]
	if !exists {
		return
	}

	// 从堆中移除
	heap.Remove(m.heap, entry.index)
	delete(m.entries, clientID)
}

// GetLastHeartbeat 获取客户端最后心跳时间
func (m *Manager) GetLastHeartbeat(clientID string) (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.entries[clientID]
	if !exists {
		return time.Time{}, false
	}
	return entry.lastHbTime, true
}

// Start 启动心跳检测循环
func (m *Manager) Start(checkTimeout func(clientID string)) {
	m.wg.Go(func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		for {
			select {
			case <-m.stopChan:
				log.Info("心跳管理器已停止")
				return
			case <-ticker.C:
				m.checkTimeouts(checkTimeout)
			}
		}
	})
	log.Info("心跳管理器已启动")
}

// Stop 停止心跳管理器
func (m *Manager) Stop() {
	close(m.stopChan)
	m.wg.Wait()
	log.Info("心跳管理器已停止")
}

// checkTimeouts 检查超时的客户端
func (m *Manager) checkTimeouts(checkTimeout func(clientID string)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.heap.Len() == 0 {
		return
	}

	now := time.Now()
	timeoutThreshold := now.Add(-m.timeout)
	timeoutClients := make([]string, 0, m.batchSize)

	// 从堆顶开始检查，只检查可能超时的客户端（堆顶是最早的心跳时间）
	for m.heap.Len() > 0 && len(timeoutClients) < m.batchSize {
		entry := (*m.heap)[0]

		// 如果最早的心跳时间都还没超时，说明后面的也不会超时
		if entry.lastHbTime.After(timeoutThreshold) {
			break
		}

		// 再次确认是否真的超时（可能在被检查期间更新了心跳）
		if now.Sub(entry.lastHbTime) > m.timeout {
			timeoutClients = append(timeoutClients, entry.clientID)
			// 从堆中移除
			heap.Pop(m.heap)
			delete(m.entries, entry.clientID)
		} else {
			// 如果没超时，说明堆结构可能有问题，重新调整
			heap.Fix(m.heap, 0)
			break
		}
	}

	// 在锁外调用回调，避免死锁
	if len(timeoutClients) > 0 {
		// 批量记录日志，减少日志开销
		log.Warnf("检测到 %d 个客户端心跳超时", len(timeoutClients))

		go func() {
			for _, clientID := range timeoutClients {
				checkTimeout(clientID)
			}
		}()
	}
}

// GetStats 获取统计信息（用于监控）
func (m *Manager) GetStats() (total int, oldestHbTime time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total = len(m.entries)
	if m.heap.Len() > 0 {
		oldestHbTime = (*m.heap)[0].lastHbTime
	}
	return
}

// GetTimeoutCount 获取即将超时的客户端数量（用于监控）
func (m *Manager) GetTimeoutCount(warningThreshold time.Duration) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	warningThresholdTime := now.Add(-warningThreshold)
	count := 0

	// 遍历堆，统计即将超时的客户端数量
	for i := 0; i < m.heap.Len(); i++ {
		entry := (*m.heap)[i]
		if entry.lastHbTime.Before(warningThresholdTime) {
			count++
		} else {
			// 堆是有序的，后面的不会超时
			break
		}
	}

	return count
}
