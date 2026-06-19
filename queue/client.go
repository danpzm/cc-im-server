package queue

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
)

type Client struct {
	client *asynq.Client
}

var (
	defaultClient   *Client
	defaultClientMu sync.RWMutex
	opLogClient     *Client
	opLogClientMu   sync.RWMutex
)

func NewClient(db int) *Client {
	redisConfig := config.GetRedisConfig()
	opt := asynq.RedisClientOpt{
		Addr:     redisConfig.RedisConnect,
		Username: redisConfig.RedisUsername,
		Password: redisConfig.RedisPassword,
		DB:       db,
	}
	client := asynq.NewClient(opt)
	return &Client{client: client}
}

func (c *Client) Publish(task *asynq.Task) error {
	_, err := c.client.Enqueue(task)
	if err != nil {
		log.Errorf("任务发布失败: %v", err)
		return err
	}
	return nil
}
func (c *Client) PublishWithDelay(task *asynq.Task, delay time.Duration) error {
	_, err := c.client.Enqueue(task, asynq.ProcessIn(delay))
	if err != nil {
		log.Errorf("任务发布失败: %v", err)
		return err
	}
	return nil
}

// PublishWithProcessAt 在指定时间执行任务
func (c *Client) PublishWithProcessAt(task *asynq.Task, processAt time.Time) error {
	_, err := c.client.Enqueue(task, asynq.ProcessAt(processAt))
	if err != nil {
		log.Errorf("任务发布失败: %v", err)
		return err
	}
	return nil
}

// PublishTask 以类型安全方式发布任务，编译期约束载荷类型。
func PublishTask[T any](c *Client, task Task[T], body T, delay time.Duration) error {
	payload, err := json.Marshal(body)
	if err != nil {
		log.Errorf("编码任务消息失败: %v", err)
		return err
	}
	asynqTask := asynq.NewTask(task.Name, payload)
	if delay == 0 {
		return c.Publish(asynqTask)
	}
	return c.PublishWithDelay(asynqTask, delay)
}

// InitDefaultClient 创建并注册默认客户端（幂等）。
func InitDefaultClient(db int) *Client {
	defaultClientMu.Lock()
	defer defaultClientMu.Unlock()
	if defaultClient == nil {
		defaultClient = NewClient(db)
	}
	return defaultClient
}

// SetDefaultClient 手动指定默认客户端。
func SetDefaultClient(c *Client) {
	defaultClientMu.Lock()
	defer defaultClientMu.Unlock()
	defaultClient = c
}

// DefaultClient 获取默认客户端，未初始化会 panic。
func DefaultClient() *Client {
	defaultClientMu.RLock()
	defer defaultClientMu.RUnlock()
	if defaultClient == nil {
		panic("queue default client is nil, call InitDefaultClient or SetDefaultClient first")
	}
	return defaultClient
}

// PublishTaskDefault 使用默认客户端发布任务。
func PublishTaskDefault[T any](task Task[T], body T, delay time.Duration) error {
	return PublishTask(DefaultClient(), task, body, delay)
}

// PublishTaskAt 在指定时间发布任务
func PublishTaskAt[T any](c *Client, task Task[T], body T, processAt time.Time) error {
	payload, err := json.Marshal(body)
	if err != nil {
		log.Errorf("编码任务消息失败: %v", err)
		return err
	}
	asynqTask := asynq.NewTask(task.Name, payload)
	return c.PublishWithProcessAt(asynqTask, processAt)
}

// PublishTaskAtDefault 使用默认客户端在指定时间发布任务
func PublishTaskAtDefault[T any](task Task[T], body T, processAt time.Time) error {
	return PublishTaskAt(DefaultClient(), task, body, processAt)
}

// InitOpLogClient 创建并设置操作日志队列客户端（独立 DB，幂等）。
func InitOpLogClient(db int) *Client {
	opLogClientMu.Lock()
	defer opLogClientMu.Unlock()
	if opLogClient == nil {
		opLogClient = NewClient(db)
	}
	return opLogClient
}

// SetOpLogClient 设置操作日志队列客户端（用于注入或测试）。
func SetOpLogClient(c *Client) {
	opLogClientMu.Lock()
	defer opLogClientMu.Unlock()
	opLogClient = c
}

// OpLogClient 获取操作日志队列客户端，未初始化返回 nil（发布时跳过，不 panic）。
func OpLogClient() *Client {
	opLogClientMu.RLock()
	defer opLogClientMu.RUnlock()
	return opLogClient
}

// PublishOpLogTaskDefault 使用操作日志队列客户端发布任务；若未初始化则按 DefaultConfig 的 OpLogQueueDB 懒加载一次。
func PublishOpLogTaskDefault[T any](task Task[T], body T, delay time.Duration) error {
	c := OpLogClient()
	if c == nil {
		cfg := DefaultConfig()
		InitOpLogClient(cfg.OpLogQueueDB)
		c = OpLogClient()
	}
	if c == nil {
		log.Warn("操作日志队列客户端未初始化，跳过写入")
		return nil
	}
	return PublishTask(c, task, body, delay)
}

func (c *Client) Close() error {
	return c.client.Close()
}
