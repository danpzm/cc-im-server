package queue

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/hibiken/asynq"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
)

// Task 表示一个携带静态类型信息的任务标识。
type Task[T any] struct {
	Name string
}

// NewTask 创建任务标识。
func NewTask[T any](name string) Task[T] {
	return Task[T]{Name: name}
}

// Server 包装 asynq.Server，提供泛型注册以在编译期校验载荷类型。
type Server struct {
	srv *asynq.Server
	mux *asynq.ServeMux
}

var (
	defaultServer   *Server
	defaultServerMu sync.RWMutex
)

// NewServer 创建队列消费端。
func NewServer(db int, concurrency int) *Server {
	if concurrency <= 0 {
		concurrency = 10
	}
	redisConfig := config.GetRedisConfig()
	opt := asynq.RedisClientOpt{
		Addr:     redisConfig.RedisConnect,
		Username: redisConfig.RedisUsername,
		Password: redisConfig.RedisPassword,
		DB:       db,
	}
	srv := asynq.NewServer(opt, asynq.Config{
		Concurrency: concurrency,
	})
	return &Server{
		srv: srv,
		mux: asynq.NewServeMux(),
	}
}

// Handle 以类型安全方式注册任务处理函数（避免在方法上使用泛型）。
func Handle[T any](s *Server, task Task[T], handler func(context.Context, T) error) {
	s.mux.HandleFunc(task.Name, func(ctx context.Context, t *asynq.Task) error {
		var payload T
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			log.Errorf("解码任务 %s 失败: %v", task.Name, err)
			return err
		}
		return handler(ctx, payload)
	})
}

// Run 启动任务消费循环（阻塞）。
func (s *Server) Run() error {
	return s.srv.Run(s.mux)
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown() {
	s.srv.Shutdown()
}

// InitDefaultServer 创建并注册默认 Server（幂等）。
func InitDefaultServer(db int, concurrency int) *Server {
	defaultServerMu.Lock()
	defer defaultServerMu.Unlock()
	if defaultServer == nil {
		defaultServer = NewServer(db, concurrency)
	}
	return defaultServer
}

// SetDefaultServer 手动指定默认 Server。
func SetDefaultServer(s *Server) {
	defaultServerMu.Lock()
	defer defaultServerMu.Unlock()
	defaultServer = s
}

// DefaultServer 获取默认 Server，未初始化会 panic。
func DefaultServer() *Server {
	defaultServerMu.RLock()
	defer defaultServerMu.RUnlock()
	if defaultServer == nil {
		panic("queue default server is nil, call InitDefaultServer or SetDefaultServer first")
	}
	return defaultServer
}

// HandleDefault 在默认 Server 上注册任务处理器。
func HandleDefault[T any](task Task[T], handler func(context.Context, T) error) {
	Handle(DefaultServer(), task, handler)
}

// RunDefault 启动默认 Server。
func RunDefault() error {
	return DefaultServer().Run()
}

// ShutdownDefault 关闭默认 Server。
func ShutdownDefault() {
	DefaultServer().Shutdown()
}
