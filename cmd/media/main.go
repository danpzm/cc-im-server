package main

import (
	"context"
	"crypto/tls"
	"os"
	"os/signal"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/media/streaming"
	"github.com/xd/quic-server/pkg/serviceregistry"
	"github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/utils"
)

func main() {
	utils.InitLogger()
	config.LoadFor("media")
	redis.InitClient()

	serverCfg := config.GetServerConfig()
	regCtx, regCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer regCancel()
	mediaDial, err := serviceregistry.ResolvedMediaDialAddr(serverCfg)
	if err != nil {
		log.Fatalf("解析媒体 QUIC 对外地址失败: %v", err)
	}
	if err := serviceregistry.Register(regCtx, serviceregistry.KindMedia, serverCfg.MediaDialAddrsRedisKey, mediaDial); err != nil {
		log.Fatalf("向 Redis 注册媒体 QUIC 地址失败: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := serviceregistry.Unregister(ctx, serviceregistry.KindMedia, serverCfg.MediaDialAddrsRedisKey, mediaDial); err != nil {
			log.Warnf("从 Redis 注销媒体 QUIC 地址失败: %v", err)
		}
	}()
	log.Infof("已注册媒体 QUIC 集群地址: %s", mediaDial)
	cert, err := tls.LoadX509KeyPair(serverCfg.CertPath, serverCfg.KeyPath)
	if err != nil {
		log.Fatalf("无法加载证书文件: %v", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"cc-media-v1"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := streaming.NewServer(serverCfg.MediaQuicAddr, tlsConfig)
	go func() {
		if err := srv.Run(ctx); err != nil {
			log.Fatalf("媒体 QUIC 服务异常退出: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("媒体 QUIC 服务关闭中...")
}
