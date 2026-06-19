package push

import (
	"context"
	"time"

	"github.com/xd/quic-server/redis"
)

const presenceKeyPrefix = "quic:presence:"

// PresenceTTL 心跳续期与映射过期（应大于客户端心跳间隔）
const PresenceTTL = 2 * time.Minute

func presenceKey(uid string) string {
	return presenceKeyPrefix + uid
}

// Register 用户长连接建立后登记所在节点
func Register(uid, nodeID string) error {
	if uid == "" || nodeID == "" {
		return nil
	}
	return redis.SetString(presenceKey(uid), nodeID, PresenceTTL)
}

// Unregister 连接移除时删除
func Unregister(uid string) error {
	if uid == "" {
		return nil
	}
	return redis.Delete(presenceKey(uid))
}

// Refresh 心跳时续期
func Refresh(uid, nodeID string) error {
	return Register(uid, nodeID)
}

// NodeForUser 查询 uid 当前所在节点（无则空字符串）
func NodeForUser(uid string) (string, error) {
	if uid == "" {
		return "", nil
	}
	s, err := redis.GetString(presenceKey(uid))
	if err != nil {
		return "", err
	}
	return s, nil
}

// GroupUidsByNode 将 uid 按 Redis 中的节点分组（无 presence 的 uid 不会出现在结果中）
func GroupUidsByNode(uids []string) map[string][]string {
	seen := make(map[string]struct{})
	out := make(map[string][]string)
	for _, uid := range uids {
		if uid == "" {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		node, err := NodeForUser(uid)
		if err != nil || node == "" {
			continue
		}
		out[node] = append(out[node], uid)
	}
	return out
}

// MGetNodes 批量查询 uid -> node（需 Redis 已初始化）
func MGetNodes(ctx context.Context, uids []string) map[string]string {
	if len(uids) == 0 {
		return nil
	}
	keys := make([]string, 0, len(uids))
	uidOrder := make([]string, 0, len(uids))
	seen := make(map[string]struct{})
	for _, uid := range uids {
		if uid == "" {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		keys = append(keys, presenceKey(uid))
		uidOrder = append(uidOrder, uid)
	}
	if len(keys) == 0 {
		return nil
	}
	vals, err := redis.GetClient().MGet(ctx, keys...).Result()
	if err != nil {
		return nil
	}
	res := make(map[string]string)
	for i, v := range vals {
		if v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		res[uidOrder[i]] = s
	}
	return res
}
