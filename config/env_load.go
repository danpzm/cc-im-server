package config

import (
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
)

// LoadFor 从 ResolveConfigDir() 目录加载环境文件：
// 1) .env.shared（共用）
// 2) .env.<service>（服务专用，覆盖前者）
// 进程级环境变量（os.Environ）始终优先生效。
// 目录：CONFIG_DIR，或 APP_ENV=dev|prod（默认 dev）→ env/<APP_ENV>/。
// service 取值：http | quic | queue | oss | media | server
func LoadFor(service string) {
	service = strings.TrimSpace(strings.ToLower(service))
	if service == "" {
		log.Fatal("config.LoadFor: service 不能为空，例如 http、quic、server")
	}
	dir := ResolveConfigDir()
	_ = os.Setenv("CONFIG_DIR", dir)
	sharedPath := filepath.Join(dir, ".env.shared")
	svcPath := filepath.Join(dir, ".env."+service)

	merged, err := readDotEnvFile(sharedPath, true)
	if err != nil {
		log.Fatalf("加载共用环境文件失败 %s: %v", sharedPath, err)
	}
	svcMap, err := readDotEnvFile(svcPath, true)
	if err != nil {
		log.Fatalf("加载服务环境文件失败 %s: %v", svcPath, err)
	}
	maps.Copy(merged, svcMap)
	applyFromEnvMap(merged, service)
}

func readDotEnvFile(path string, required bool) (map[string]string, error) {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if !required {
				return map[string]string{}, nil
			}
			return nil, err
		}
		return nil, err
	}
	m, err := godotenv.Read(path)
	if err != nil {
		return nil, err
	}
	return m, nil
}
