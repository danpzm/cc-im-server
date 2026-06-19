package queue

import "github.com/xd/quic-server/config"

// Config 队列配置
type Config struct {
	// MainQueueDB 主队列Redis DB（用于ACK检查任务）
	MainQueueDB int
	// QuicQueueDB quic队列Redis DB（用于重发消息任务）
	QuicQueueDB int
	// OpLogQueueDB 操作日志队列Redis DB（独立队列写 DB）
	OpLogQueueDB int
	// Concurrency 并发数
	Concurrency int
}

// DefaultConfig 返回默认配置（从配置文件读取，如果配置不存在则使用默认值）
func DefaultConfig() *Config {
	cfg := config.GetQueueConfig()
	if cfg != nil {
		return &Config{
			MainQueueDB:  cfg.MainQueueDB,
			QuicQueueDB:  cfg.QuicQueueDB,
			OpLogQueueDB: cfg.OpLogQueueDB,
			Concurrency:  cfg.Concurrency,
		}
	}
	return &Config{
		MainQueueDB:  1,
		QuicQueueDB:  2,
		OpLogQueueDB: 3,
		Concurrency:  10,
	}
}
