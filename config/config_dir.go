package config

import (
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// ResolveConfigDir 解析环境文件目录：
// 1) CONFIG_DIR 非空时原样使用（最高优先级）；
// 2) 否则 APP_ENV=dev|prod（默认 dev）→ env/<APP_ENV>/；
// 3) 若 env/<APP_ENV>/ 不存在但存在旧版扁平 env/.env.shared，则回退 env/ 并打警告。
func ResolveConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CONFIG_DIR")); dir != "" {
		return dir
	}
	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	if appEnv == "" {
		appEnv = "dev"
	}
	if appEnv != "dev" && appEnv != "prod" {
		log.Fatalf("无效的 APP_ENV=%q，仅支持 dev、prod", os.Getenv("APP_ENV"))
	}
	dir := filepath.Join("env", appEnv)
	if _, err := os.Stat(filepath.Join(dir, ".env.shared")); err == nil {
		return dir
	}
	legacy := filepath.Join("env", ".env.shared")
	if _, err := os.Stat(legacy); err == nil {
		log.Warnf("未找到 %s，使用旧版扁平目录 env/；建议执行 scripts/init-env.ps1 迁移到 env/%s/", dir, appEnv)
		return "env"
	}
	return dir
}
