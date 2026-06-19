package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/xid"
)

// DistributedLock 封装了分布式锁的 Lock 和 Unlock 方法
type DistributedLock struct {
	rdb      *redis.Client
	key      string
	clientID string
}

// NewDistributedLock 创建并返回一个新的 DistributedLock 实例
func NewDistributedLock(key string) *DistributedLock {
	return &DistributedLock{
		rdb:      GetClient(),
		key:      key,
		clientID: xid.New().String(), // 生成唯一的 clientID
	}
}

// Lock 尝试获取锁，如果成功则返回 nil，否则返回错误
func (dl *DistributedLock) Lock(expiration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := dl.rdb.SetArgs(ctx, dl.key, dl.clientID, redis.SetArgs{
		Mode: "NX",
		TTL:  expiration,
	}).Result()
	if err == redis.Nil {
		return fmt.Errorf("锁被其他客户端持有")
	}
	if err != nil {
		return fmt.Errorf("获取锁失败: %w", err)
	}
	if res != "OK" {
		return fmt.Errorf("锁被其他客户端持有")
	}
	return nil
}

// Unlock 尝试释放锁，如果成功则返回 nil，否则返回错误
func (dl *DistributedLock) Unlock() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	script := `
        if redis.call("GET", KEYS[1]) == ARGV[1] then
            return redis.call("DEL", KEYS[1])
        else
            return 0
        end
    `
	result, err := dl.rdb.Eval(ctx, script, []string{dl.key}, dl.clientID).Result()
	if err != nil {
		return fmt.Errorf("释放锁失败: %w", err)
	}

	// 忽略锁已被其他客户端持有的情况
	if result.(int64) == 0 {
		return nil
	}

	return nil
}

// LockWithRetry 尝试多次获取锁，直到成功或达到最大重试次数
func (dl *DistributedLock) LockWithRetry(expiration time.Duration, maxRetries int) error {
	var err error
	backoff := time.Second // 初始间隔
	for range maxRetries {
		err = dl.Lock(expiration)
		if err == nil {
			return nil
		}
		time.Sleep(backoff)
		backoff *= 2 // 指数增长
		if backoff > 30*time.Second {
			backoff = 30 * time.Second // 上限
		}
	}
	return fmt.Errorf("达到最大重试次数: %w", err)
}
