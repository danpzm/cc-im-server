package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type DBConfig struct {
	DB_DNS string
}

type UploadConfig struct {
	UploadChunkSize uint64
	UploadMaxSize   uint64
	UploadDir       string
}

type RedisConfig struct {
	RedisAddr     string
	RedisPassword string
	RedisConnect  string
	RedisUsername string
}
type MailConfig struct {
	MailHost     string
	MailPort     int
	MailUser     string
	MailPassword string
}

type GeoIPConfig struct {
	DBPath string // GeoIP2 数据库文件路径
}

type QueueConfig struct {
	MainQueueDB  int // 主队列Redis DB（用于ACK检查任务）
	QuicQueueDB  int // QUIC队列Redis DB（用于重发消息任务）
	OpLogQueueDB int // 操作日志队列Redis DB（独立队列写 DB，管理员/用户操作日志）
	Concurrency  int // 并发数
}

type ServerConfig struct {
	CertPath       string // TLS 证书路径（仅 quic / media 使用；其它服务为空）
	KeyPath        string // TLS 私钥路径（仅 quic / media 使用；其它服务为空）
	NodeID         string // 集群实例 ID（QUIC 定向推送必填；其它服务建议唯一便于排查）
	QuicAddr       string // QUIC 服务监听地址（格式：host:port）
	MediaQuicAddr  string // 媒体 QUIC 服务监听地址（格式：host:port）
	HttpAddr       string // HTTP 服务监听地址（格式：host:port）
	OssAddr        string // OSS 服务监听地址（格式：host:port）
	TrustedProxies []string
	QuicNextProtos []string
	// QuicDialAddrsRedisKey：SET，成员为客户端可拨 host:port；各 cmd/quic 启动时 SADD，HTTP 登录时 SMEMBERS。
	QuicDialAddrsRedisKey string
	// QuicClientDialAddr：仅 cmd/quic 使用，写入 SET 的对外拨号地址（可与 QUIC_LISTEN_ADDR 不同，如公网域名）。
	QuicClientDialAddr string
	// HttpClientBaseURL / OssClientBaseURL / MediaClientDialAddr：各进程注册到 Redis SET 的对外地址；空则按监听地址推导。
	HttpClientBaseURL       string
	OssClientBaseURL        string
	MediaClientDialAddr     string
	HttpBaseURLsRedisKey    string
	OssBaseURLsRedisKey     string
	MediaDialAddrsRedisKey  string
	// SessionOnlineCountCacheTTL：会话在线人数本地缓存时长（例如 2s）。
	SessionOnlineCountCacheTTL time.Duration
	// InviteWebBaseURL：邀请落地页公网地址（例如 https://chat-dev.example.com）
	InviteWebBaseURL string
}

type JWTConfig struct {
	JWTKey             string
	AccessTokenExpire  time.Duration
	ConnectTokenExpire time.Duration
	RefreshTokenExpire time.Duration
}

var redisConfig *RedisConfig
var dbConfig *DBConfig
var uploadConfig *UploadConfig
var mailConfig *MailConfig
var geoIPConfig *GeoIPConfig
var serverConfig *ServerConfig
var queueConfig *QueueConfig
var jwtConfig *JWTConfig

func GetRedisConfig() *RedisConfig {
	return redisConfig
}
func GetMailConfig() *MailConfig {
	return mailConfig
}
func GetDBConfig() *DBConfig {
	return dbConfig
}
func GetUploadConfig() *UploadConfig {
	return uploadConfig
}
func GetGeoIPConfig() *GeoIPConfig {
	return geoIPConfig
}
func GetServerConfig() *ServerConfig {
	return serverConfig
}
func GetQueueConfig() *QueueConfig {
	return queueConfig
}
func GetJWTConfig() *JWTConfig {
	return jwtConfig
}

// UploadStorageConfigured 表示当前进程已配置可用的本地上传目录（仅 cmd/oss 会加载；cmd/http 不配置上传）。
func UploadStorageConfigured() bool {
	if uploadConfig == nil {
		return false
	}
	return strings.TrimSpace(uploadConfig.UploadDir) != "" &&
		uploadConfig.UploadChunkSize > 0 &&
		uploadConfig.UploadMaxSize > 0
}

func parseUint64(str string) uint64 {
	num, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		log.Fatal("环境配置文件中UPLOAD_CHUNK_SIZE或UPLOAD_MAX_SIZE格式错误")
	}
	return num
}

func parseInt(str string) int {
	num, err := strconv.Atoi(str)
	if err != nil {
		log.Fatal("环境配置文件中MAIL_PORT格式错误")
	}
	return num
}
func parseIntWithDefault(str string, defaultValue int) int {
	if str == "" {
		return defaultValue
	}
	num, err := strconv.Atoi(str)
	if err != nil {
		log.Warnf("环境配置文件中 %s 格式错误，使用默认值 %d", str, defaultValue)
		return defaultValue
	}
	return num
}

func parseDurationWithDefault(raw string, defaultValue time.Duration) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		log.Warnf("环境配置文件中 duration=%q 格式错误，使用默认值 %s", raw, defaultValue)
		return defaultValue
	}
	return d
}

func parseCSVList(raw string) []string {
	if raw == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// applyFromEnvMap 由 LoadFor 调用：env 为已合并的键值（不含进程环境，进程环境在 get 内优先读取）。
func applyFromEnvMap(env map[string]string, service string) {
	get := func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return v
			}
		}
		if v, ok := env[key]; ok {
			return strings.TrimSpace(v)
		}
		return ""
	}

	redisConfig = &RedisConfig{
		RedisAddr:     get("REDIS_ADDR"),
		RedisPassword: get("REDIS_PASSWORD"),
		RedisConnect:  get("REDIS_CONNECT"),
		RedisUsername: get("REDIS_USERNAME"),
	}
	dbConfig = &DBConfig{
		DB_DNS: get("DB_DNS"),
	}
	// 上传：仅 OSS 从 env 加载；HTTP 不配置文件存储（与 OSS 分开展开时文件走 OSS 端口）
	switch service {
	case "http":
		uploadConfig = &UploadConfig{}
	case "oss":
		ch := strings.TrimSpace(get("UPLOAD_CHUNK_SIZE"))
		mx := strings.TrimSpace(get("UPLOAD_MAX_SIZE"))
		dir := strings.TrimSpace(get("UPLOAD_DIR"))
		if ch == "" || mx == "" || dir == "" {
			log.Fatal("OSS 须在 env/.env.oss 中配置 UPLOAD_CHUNK_SIZE、UPLOAD_MAX_SIZE、UPLOAD_DIR")
		}
		uploadConfig = &UploadConfig{
			UploadChunkSize: parseUint64(ch),
			UploadMaxSize:   parseUint64(mx),
			UploadDir:       dir,
		}
	default:
		uploadConfig = &UploadConfig{}
	}
	// 邮件：仅 HTTP 使用
	switch service {
	case "http":
		mailConfig = &MailConfig{
			MailHost:     get("MAIL_HOST"),
			MailPort:     parseIntWithDefault(get("MAIL_PORT"), 465),
			MailUser:     get("MAIL_USER"),
			MailPassword: get("MAIL_PASSWORD"),
		}
	default:
		mailConfig = &MailConfig{}
	}
	// GeoIP：HTTP / QUIC（相对工作目录；其它服务不加载）
	var geoipDBPath string
	switch service {
	case "http":
		geoipDBPath = strings.TrimSpace(get("GEOIP_DB_PATH"))
	case "quic":
		geoipDBPath = strings.TrimSpace(get("GEOIP_DB_PATH"))
		if geoipDBPath == "" {
			geoipDBPath = "geodb/GeoLite2-City.mmdb"
		}
	default:
		geoipDBPath = ""
	}
	geoIPConfig = &GeoIPConfig{
		DBPath: geoipDBPath,
	}
	certPath := strings.TrimSpace(get("TLS_CERT_PATH"))
	keyPath := strings.TrimSpace(get("TLS_KEY_PATH"))
	switch service {
	case "quic", "media":
		if certPath == "" {
			certPath = "cert/cert.pem"
		}
		if keyPath == "" {
			keyPath = filepath.Join(filepath.Dir(certPath), "key.pem")
		}
	default:
		certPath = ""
		keyPath = ""
	}

	httpAddr := get("HTTP_LISTEN_ADDR")
	ossAddr := get("OSS_LISTEN_ADDR")
	quicAddr := get("QUIC_LISTEN_ADDR")
	mediaQuicAddr := get("MEDIA_QUIC_LISTEN_ADDR")

	quicDialAddrsRedisKey := strings.TrimSpace(get("QUIC_DIAL_ADDRS_REDIS_KEY"))
	quicClientDialAddr := strings.TrimSpace(get("QUIC_CLIENT_DIAL_ADDR"))
	httpClientBaseURL := strings.TrimSpace(get("HTTP_CLIENT_BASE_URL"))
	ossClientBaseURL := strings.TrimSpace(get("OSS_CLIENT_BASE_URL"))
	mediaClientDialAddr := strings.TrimSpace(get("MEDIA_CLIENT_DIAL_ADDR"))
	httpBaseURLsRedisKey := strings.TrimSpace(get("HTTP_BASE_URLS_REDIS_KEY"))
	ossBaseURLsRedisKey := strings.TrimSpace(get("OSS_BASE_URLS_REDIS_KEY"))
	mediaDialAddrsRedisKey := strings.TrimSpace(get("MEDIA_DIAL_ADDRS_REDIS_KEY"))
	nodeID := strings.TrimSpace(get("SERVER_NODE_ID"))
	trustedProxies := parseCSVList(get("TRUSTED_PROXIES"))
	if len(trustedProxies) == 0 {
		trustedProxies = []string{"127.0.0.1"}
	}
	quicNextProtosStr := get("QUIC_NEXT_PROTOS")
	quicNextProtos := []string{"hq-29"}
	if quicNextProtosStr != "" {
		protos := strings.Split(quicNextProtosStr, ",")
		quicNextProtos = make([]string, 0, len(protos))
		for _, proto := range protos {
			proto = strings.TrimSpace(proto)
			if proto != "" {
				quicNextProtos = append(quicNextProtos, proto)
			}
		}
	}
	serverConfig = &ServerConfig{
		CertPath:              certPath,
		KeyPath:               keyPath,
		NodeID:                nodeID,
		QuicAddr:              quicAddr,
		MediaQuicAddr:         mediaQuicAddr,
		HttpAddr:              httpAddr,
		OssAddr:               ossAddr,
		TrustedProxies:        trustedProxies,
		QuicNextProtos:        quicNextProtos,
		QuicDialAddrsRedisKey: quicDialAddrsRedisKey,
		QuicClientDialAddr:    quicClientDialAddr,
		HttpClientBaseURL:     httpClientBaseURL,
		OssClientBaseURL:      ossClientBaseURL,
		MediaClientDialAddr:   mediaClientDialAddr,
		HttpBaseURLsRedisKey:  httpBaseURLsRedisKey,
		OssBaseURLsRedisKey:   ossBaseURLsRedisKey,
		MediaDialAddrsRedisKey: mediaDialAddrsRedisKey,
		SessionOnlineCountCacheTTL: parseDurationWithDefault(
			get("SESSION_ONLINE_COUNT_CACHE_TTL"),
			2*time.Second,
		),
		InviteWebBaseURL: strings.TrimRight(strings.TrimSpace(get("INVITE_WEB_BASE_URL")), "/"),
	}
	validateServiceEnv(service, get)
	mainQueueDB := parseIntWithDefault(get("QUEUE_MAIN_DB"), 1)
	quicQueueDB := parseIntWithDefault(get("QUEUE_QUIC_DB"), 2)
	opLogQueueDB := parseIntWithDefault(get("QUEUE_OPLOG_DB"), 3)
	concurrency := parseIntWithDefault(get("QUEUE_CONCURRENCY"), 10)
	queueConfig = &QueueConfig{
		MainQueueDB:  mainQueueDB,
		QuicQueueDB:  quicQueueDB,
		OpLogQueueDB: opLogQueueDB,
		Concurrency:  concurrency,
	}
	jwtKey := strings.TrimSpace(get("JWT_KEY"))
	if jwtKey == "" {
		jwtKey = "eW91cl9nZW5lcmF0ZWRfc2VjcmV0X2tleV9oZXJl"
	}
	jwtConfig = &JWTConfig{
		JWTKey:             jwtKey,
		AccessTokenExpire:  parseDurationWithDefault(get("ACCESS_TOKEN_EXPIRE"), 2*time.Hour),
		ConnectTokenExpire: parseDurationWithDefault(get("CONNECT_TOKEN_EXPIRE"), 5*time.Minute),
		RefreshTokenExpire: parseDurationWithDefault(get("REFRESH_TOKEN_EXPIRE"), 7*24*time.Hour),
	}
	if service == "http" {
		loadDesktopUpdateConfig(get)
		loadDesktopWebManifestDir(get)
	} else {
		desktopUpdateConfig = nil
		desktopWebManifestDir = ""
	}
	log.Infof("加载环境配置成功 service=%s CONFIG_DIR=%s APP_ENV=%s", service, os.Getenv("CONFIG_DIR"), os.Getenv("APP_ENV"))
}

func validateServiceEnv(service string, get func(string) string) {
	switch service {
	case "http":
		if get("HTTP_LISTEN_ADDR") == "" {
			log.Fatal("HTTP_LISTEN_ADDR 未配置（请在 env/.env.http 中设置）")
		}
		if strings.TrimSpace(get("HTTP_CLIENT_BASE_URL")) == "" {
			log.Fatal("HTTP_CLIENT_BASE_URL 未配置（请在 env/.env.http 中设置，将写入 Redis SET 供登录下发 http_base_urls）")
		}
		if strings.TrimSpace(get("INVITE_WEB_BASE_URL")) == "" {
			log.Fatal("INVITE_WEB_BASE_URL 未配置（请在 env/.env.http 中设置，例如 https://chat.example.com）")
		}
	case "quic":
		if get("QUIC_LISTEN_ADDR") == "" {
			log.Fatal("QUIC_LISTEN_ADDR 未配置（请在 env/.env.quic 中设置）")
		}
		if get("SERVER_NODE_ID") == "" {
			log.Fatal("SERVER_NODE_ID 未配置（请在 env/.env.quic 中设置）")
		}
		if strings.TrimSpace(get("QUIC_CLIENT_DIAL_ADDR")) == "" {
			log.Fatal("QUIC_CLIENT_DIAL_ADDR 未配置（请在 env/.env.quic 中设置，将写入 Redis SET 供 HTTP 下发 quic_addrs）")
		}
	case "oss":
		if get("OSS_LISTEN_ADDR") == "" {
			log.Fatal("OSS_LISTEN_ADDR 未配置（请在 env/.env.oss 中设置）")
		}
		if strings.TrimSpace(get("OSS_CLIENT_BASE_URL")) == "" {
			log.Fatal("OSS_CLIENT_BASE_URL 未配置（请在 env/.env.oss 中设置，将写入 Redis SET 供登录下发 oss_base_urls）")
		}
	case "media":
		if get("MEDIA_QUIC_LISTEN_ADDR") == "" {
			log.Fatal("MEDIA_QUIC_LISTEN_ADDR 未配置（请在 env/.env.media 中设置）")
		}
		if strings.TrimSpace(get("MEDIA_CLIENT_DIAL_ADDR")) == "" {
			log.Fatal("MEDIA_CLIENT_DIAL_ADDR 未配置（请在 env/.env.media 中设置，将写入 Redis SET 供媒体 join 下发 quic_addr）")
		}
	case "queue":
		return
	default:
		log.Fatalf("未知 service: %s", service)
	}
}
