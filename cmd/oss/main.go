package main

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/oss/router"
	"github.com/xd/quic-server/pkg/serviceregistry"
	"github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/utils"
)

func main() {
	// 初始化日志
	utils.InitLogger()
	// 加载配置
	config.LoadFor("oss")
	// 初始化Redis
	redis.InitClient()
	serverCfg := config.GetServerConfig()
	regCtx, regCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer regCancel()
	ossBase, err := serviceregistry.ResolvedOSSBaseURL(serverCfg)
	if err != nil {
		log.Fatalf("解析 OSS 对外基地址失败: %v", err)
	}
	if err := serviceregistry.Register(regCtx, serviceregistry.KindOSS, serverCfg.OssBaseURLsRedisKey, ossBase); err != nil {
		log.Fatalf("向 Redis 注册 OSS 基地址失败: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := serviceregistry.Unregister(ctx, serviceregistry.KindOSS, serverCfg.OssBaseURLsRedisKey, ossBase); err != nil {
			log.Warnf("从 Redis 注销 OSS 基地址失败: %v", err)
		}
	}()
	log.Infof("已注册 OSS 集群地址: %s", ossBase)
	// 初始化数据库
	db.InitDb()

	// 创建HTTP服务器
	r := gin.New()
	r.SetTrustedProxies(serverCfg.TrustedProxies)

	// 注册OSS服务路由
	router.Register(r)

	log.Infof("Starting OSS service on %s", serverCfg.OssAddr)
	if err := r.Run(serverCfg.OssAddr); err != nil {
		log.Fatalf("Failed to run OSS service: %v", err)
	}
}
