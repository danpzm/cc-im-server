package main

import (
	"os"
	"os/signal"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/queue"
	"github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/utils"
)

func main() {
	// 初始化日志
	utils.InitLogger()
	// 加载配置
	config.LoadFor("queue")
	// 初始化Redis
	redis.InitClient()
	// 初始化数据库
	db.InitDb()

	// 创建队列配置
	queueCfg := queue.DefaultConfig()

	// 创建主队列服务器（用于ACK检查任务）
	mainQueueServer := queue.NewServer(queueCfg.MainQueueDB, queueCfg.Concurrency)

	// 创建quic队列客户端（用于发布重发消息任务）
	quicQueueClient := queue.NewClient(queueCfg.QuicQueueDB)

	// 注册ACK检查任务处理器（传入quic队列客户端）
	queue.Handle(mainQueueServer, queue.TaskRoomMessageAckCheck, queue.HandleRoomMessageAckCheck(quicQueueClient))
	queue.Handle(mainQueueServer, queue.TaskRoomMessageWithdrawAckCheck, queue.HandleRoomMessageWithdrawAckCheck(quicQueueClient))
	queue.Handle(mainQueueServer, queue.TaskRoomMuteStrategyTime, queue.HandleRoomMuteStrategyTime)

	// 操作日志队列服务器（独立 DB，消费并写管理员/用户操作日志）
	opLogQueueServer := queue.NewServer(queueCfg.OpLogQueueDB, queueCfg.Concurrency)
	queue.Handle(opLogQueueServer, queue.TaskRoomAdminOperationLog, queue.HandleRoomAdminOperationLog)
	queue.Handle(opLogQueueServer, queue.TaskUserOperationLog, queue.HandleUserOperationLog)

	// 启动主队列服务器
	go func() {
		if err := mainQueueServer.Run(); err != nil {
			log.Fatalf("主队列服务器运行失败: %v", err)
		}
	}()

	// 启动操作日志队列服务器
	go func() {
		if err := opLogQueueServer.Run(); err != nil {
			log.Fatalf("操作日志队列服务器运行失败: %v", err)
		}
	}()

	nodeID := config.GetServerConfig().NodeID
	if nodeID == "" {
		nodeID = "(未设置 SERVER_NODE_ID)"
	}
	log.Infof("队列服务已启动 node=%s concurrency=%d - 主队列DB: %d, 操作日志队列DB: %d",
		nodeID, queueCfg.Concurrency, queueCfg.MainQueueDB, queueCfg.OpLogQueueDB)

	// 注册退出时的清理动作
	defer mainQueueServer.Shutdown()
	defer opLogQueueServer.Shutdown()
	defer quicQueueClient.Close()

	// 监听退出信号，方便优雅关闭
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("Queue server shutting down...")
}
