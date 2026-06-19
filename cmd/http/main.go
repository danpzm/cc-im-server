package main

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/http/router"
	"github.com/xd/quic-server/pkg/geoip"
	"github.com/xd/quic-server/pkg/serviceregistry"
	"github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/utils"
)

func main() {
	// 初始化日志
	utils.InitLogger()
	// 加载配置
	config.LoadFor("http")
	// 初始化Redis
	redis.InitClient()
	serverCfg := config.GetServerConfig()
	regCtx, regCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer regCancel()
	httpBase, err := serviceregistry.ResolvedHTTPBaseURL(serverCfg)
	if err != nil {
		log.Fatalf("解析 HTTP 对外基地址失败: %v", err)
	}
	if err := serviceregistry.Register(regCtx, serviceregistry.KindHTTP, serverCfg.HttpBaseURLsRedisKey, httpBase); err != nil {
		log.Fatalf("向 Redis 注册 HTTP 基地址失败: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := serviceregistry.Unregister(ctx, serviceregistry.KindHTTP, serverCfg.HttpBaseURLsRedisKey, httpBase); err != nil {
			log.Warnf("从 Redis 注销 HTTP 基地址失败: %v", err)
		}
	}()
	log.Infof("已注册 HTTP 集群地址: %s", httpBase)
	// 初始化数据库
	db.InitDb()
	geoip.Init(config.GetGeoIPConfig().DBPath)

	// 创建HTTP服务器
	r := gin.New()
	r.SetTrustedProxies(serverCfg.TrustedProxies)
	router.Register(r)

	log.Infof("Starting HTTP server on %s", serverCfg.HttpAddr)
	if err := r.Run(serverCfg.HttpAddr); err != nil {
		log.Fatalf("Failed to run HTTP server: %v", err)
	}
}
