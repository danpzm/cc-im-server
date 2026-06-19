package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
)

var rdb *redis.Client
var REDIS_TIMEOUT = 5 * time.Second

func InitClient() {
	redisConfig := config.GetRedisConfig()
	rdb = redis.NewClient(&redis.Options{
		Addr:           redisConfig.RedisConnect,
		Username:       redisConfig.RedisUsername,
		Password:       redisConfig.RedisPassword,
		DB:             0,
		PoolSize:       50,  // 增加连接池大小
		MinIdleConns:   10,  // 保持最小空闲连接
		MaxActiveConns: 100, // 连接最大存活时间
	})

	ctx := context.Background()
	if err := GetClient().Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis 初始化失败: %v", err)
	}
}

func GetClient() *redis.Client {
	if rdb == nil {
		log.Error("redis 未初始化")
		panic("Redis client not initialized") // 生产环境建议使用更优雅的错误处理
	}
	return rdb
}

// 原生字符串类型操作（保留旧接口）
func SetString(key string, value string, expiration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Set(ctx, key, value, expiration).Err()
}

func GetString(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Get(ctx, key).Result()
}
func GetInt64(key string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Get(ctx, key).Int64()
}
func SetInt64(key string, value int64, expiration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Set(ctx, key, value, expiration).Err()
}
func GetInt(key string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Get(ctx, key).Int()
}
func SetInt(key string, value int, expiration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Set(ctx, key, value, expiration).Err()
}
func ExistsString(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	b, err := GetClient().Exists(ctx, key).Result()
	if err != nil {
		log.Errorf("Redis Exists 失败: key=%s, error=%v", key, err)
		return false
	}
	return b > 0
}

// 泛型操作（推荐新接口）
func Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Del(ctx, key).Err()
}

func Set[T any](key string, value T, expiration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	data, err := json.Marshal(value)
	if err != nil {
		log.Errorf("Redis Set 失败: key=%s, value=%v, error=%v", key, value, err)
		return err
	}
	return GetClient().Set(ctx, key, data, expiration).Err()
}

func Get[T any](key string) (T, error) {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	data, err := GetClient().Get(ctx, key).Result()
	if err == redis.Nil {
		return *new(T), err
	}
	if err != nil {
		log.Errorf("Redis Get 失败: key=%s, error=%v", key, err)
		return *new(T), err
	}
	var result T
	err = json.Unmarshal([]byte(data), &result)
	if err != nil {
		log.Errorf("Redis Get 反序列化失败: key=%s, data=%s, error=%v", key, data, err)
		return *new(T), fmt.Errorf("failed to unmarshal data for key %s", key)
	}
	return result, nil
}

func Exists(key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	_, err := GetClient().Get(ctx, key).Result()
	if err == redis.Nil {
		log.Errorf("Redis Exists 失败: key=%s, 键不存在", key)
		return false, fmt.Errorf("key %s not found", key)
	}
	if err != nil {
		log.Errorf("Redis Exists 失败: key=%s, error=%v", key, err)
		return false, err
	}
	// 只要存在值就认为是存在的，无需关心具体类型
	return true, nil
}

// 增加计数器
func Incr(key string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Incr(ctx, key).Result()
}

// 减少计数器
func Decr(key string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Decr(ctx, key).Result()
}

// Expire 设置 key 的过期时间
func Expire(key string, expiration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Expire(ctx, key, expiration).Err()
}

func SetNX(key string, value any, expiration time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().SetNX(ctx, key, value, expiration).Result()
}
func Eval(script string, keys []string, args ...any) *redis.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	return GetClient().Eval(ctx, script, keys, args...)
}

// MGet 批量获取多个 key 的值（泛型版本）
func MGet[T any](keys []string) (map[string]T, error) {
	if len(keys) == 0 {
		return make(map[string]T), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()

	// 使用 MGet 批量获取
	values, err := GetClient().MGet(ctx, keys...).Result()
	if err != nil {
		log.Errorf("Redis MGet 失败: keys=%v, error=%v", keys, err)
		return nil, err
	}

	result := make(map[string]T)
	for i, val := range values {
		if val == nil {
			// key 不存在，跳过
			continue
		}

		// 将 interface{} 转换为字符串（Redis 返回的是字符串）
		strVal, ok := val.(string)
		if !ok {
			log.Warnf("Redis MGet 返回值类型错误: key=%s, type=%T", keys[i], val)
			continue
		}

		// 反序列化 JSON
		var item T
		if err := json.Unmarshal([]byte(strVal), &item); err != nil {
			log.Errorf("Redis MGet 反序列化失败: key=%s, data=%s, error=%v", keys[i], strVal, err)
			continue
		}

		result[keys[i]] = item
	}

	return result, nil
}

// MSetJSON 批量写入 JSON 值，使用 pipeline 减少网络往返。
func MSetJSON[T any](items map[string]T, expiration time.Duration) error {
	if len(items) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), REDIS_TIMEOUT)
	defer cancel()
	_, err := GetClient().Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for key, value := range items {
			data, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				log.Errorf("Redis MSetJSON 序列化失败: key=%s, error=%v", key, marshalErr)
				continue
			}
			pipe.Set(ctx, key, data, expiration)
		}
		return nil
	})
	if err != nil {
		log.Errorf("Redis MSetJSON 失败: keys=%d, error=%v", len(items), err)
		return err
	}
	return nil
}
