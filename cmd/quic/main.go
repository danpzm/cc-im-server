package main

import (
	"context"
	"crypto/tls"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/event"
	"github.com/xd/quic-server/pkg/geoip"
	"github.com/xd/quic-server/pkg/quiccluster"
	"github.com/xd/quic-server/queue"
	"github.com/xd/quic-server/quic/server"
	"github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/utils"
)

func main() {
	// 初始化日志
	utils.InitLogger()
	// 加载配置
	config.LoadFor("quic")
	// 初始化Redis
	redis.InitClient()
	serverCfg := config.GetServerConfig()
	regCtx, regCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer regCancel()
	if err := quiccluster.RegisterDialAddr(regCtx, serverCfg); err != nil {
		log.Fatalf("向 Redis 注册 QUIC 客户端拨号地址失败: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := quiccluster.UnregisterDialAddr(ctx, config.GetServerConfig()); err != nil {
			log.Warnf("从 Redis 注销 QUIC 客户端拨号地址失败: %v", err)
		}
	}()
	// 初始化事件总线
	event.InitDefaultBus()
	// 初始化数据库
	db.InitDb()
	geoip.Init(config.GetGeoIPConfig().DBPath)

	// 创建队列配置
	queueCfg := queue.DefaultConfig()

	// 创建quic队列服务器（用于监听重发消息任务）
	quicQueueServer := queue.NewServer(queueCfg.QuicQueueDB, queueCfg.Concurrency)

	// 创建主队列客户端（用于发布ACK检查任务）
	mainQueueClient := queue.NewClient(queueCfg.MainQueueDB)

	// 注意：重发消息任务处理器将在quic服务器创建后注册

	// 启动quic队列服务器（异步运行）
	go func() {
		if err := quicQueueServer.Run(); err != nil {
			log.Fatalf("Quic队列服务器运行失败: %v", err)
		}
	}()

	if strings.TrimSpace(serverCfg.NodeID) == "" {
		log.Fatal("SERVER_NODE_ID 未设置：每个 QUIC 实例在集群内需唯一 ID")
	}
	// 加载证书
	cert, err := tls.LoadX509KeyPair(serverCfg.CertPath, serverCfg.KeyPath)
	if err != nil {
		log.Fatalf("无法加载证书文件: %v (证书路径: %s, 私钥路径: %s)", err, serverCfg.CertPath, serverCfg.KeyPath)
	}

	// 配置 TLS
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   serverCfg.QuicNextProtos,
	}

	// 创建并启动quic服务器
	srv := server.NewServer(tlsConfig,
		serverCfg.QuicAddr,
		quicQueueServer,
		mainQueueClient,
		serverCfg.NodeID,
	)
	if err := srv.Run(); err != nil {
		log.Fatalf("Failed to run Quic server: %v", err)
	}

	log.Infof("Starting Quic server on %s", serverCfg.QuicAddr)
	defer func() {
		srv.Shutdown()
		quicQueueServer.Shutdown()
		mainQueueClient.Close()
	}()

	// 监听退出信号，方便优雅关闭
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("Quic server shutting down...")
}
