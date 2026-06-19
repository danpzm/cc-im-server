package db

import (
	"os"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

var db *gorm.DB

func GetDB() *gorm.DB {
	if db == nil {
		log.Fatal("数据库未初始化")
	}
	return db
}

func InitDb() {
	var err error
	db, err = gorm.Open(postgres.Open(config.GetDBConfig().DB_DNS), &gorm.Config{
		SkipDefaultTransaction: false,
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true, // 使用单数表名
		},
		DisableForeignKeyConstraintWhenMigrating: true, // 禁用外键约束
		PrepareStmt:                              true, // 开启预编译语句缓存
		QueryFields:                              true, // 查询字段
	})
	if err != nil {
		log.Fatal(err)
	}

	sqldb, err := db.DB()
	if err != nil {
		log.Fatal(err)
	}
	err = sqldb.Ping()
	if err != nil {
		log.Fatal(err)
	}
	// 连接池配置（可通过环境变量覆盖）：
	// DB_MAX_OPEN_CONNS 默认 50，DB_MAX_IDLE_CONNS 默认 10，
	// DB_CONN_MAX_LIFETIME_MINUTES 默认 30，DB_CONN_MAX_IDLE_MINUTES 默认 10。
	maxOpenConns := getEnvInt("DB_MAX_OPEN_CONNS", 50)
	maxIdleConns := getEnvInt("DB_MAX_IDLE_CONNS", 10)
	connMaxLifetimeMinutes := getEnvInt("DB_CONN_MAX_LIFETIME_MINUTES", 30)
	connMaxIdleMinutes := getEnvInt("DB_CONN_MAX_IDLE_MINUTES", 10)

	if maxIdleConns > maxOpenConns {
		maxIdleConns = maxOpenConns
	}

	sqldb.SetMaxOpenConns(maxOpenConns)
	sqldb.SetMaxIdleConns(maxIdleConns)
	sqldb.SetConnMaxLifetime(time.Duration(connMaxLifetimeMinutes) * time.Minute)
	sqldb.SetConnMaxIdleTime(time.Duration(connMaxIdleMinutes) * time.Minute)

	log.Infof(
		"数据库连接池配置: max_open=%d max_idle=%d max_lifetime_min=%d max_idle_time_min=%d",
		maxOpenConns,
		maxIdleConns,
		connMaxLifetimeMinutes,
		connMaxIdleMinutes,
	)

	if !autoMigrateEnabled() {
		log.Info("跳过数据库自动迁移（DB_AUTO_MIGRATE=false）")
		return
	}
	log.Warn("已启用数据库自动迁移（DB_AUTO_MIGRATE=true）。生产环境不建议开启，建议仅在开发/测试环境使用。")
	withMigrationLock(func() {
		err = db.AutoMigrate(
			&entity.User{},
			&entity.UserRememberToken{},
			&entity.UserRoomSession{},
			&entity.UserRefreshToken{},
			&entity.UserFriendGroup{},
			&entity.UserFriend{},
			&entity.UserUploadFile{},
			&entity.UploadFile{},
			&entity.UploadFileChunk{},
			&entity.Room{},
			&entity.RoomInvite{},
			&entity.RoomInviteJoin{},
			&entity.RoomCategory{},
			&entity.RoomTag{},
			&entity.RoomTagRelation{},
			&entity.RoomUser{},
			&entity.RoomMessage{},
			&entity.RoomMessageContent{},
			&entity.RoomMessageAck{},
			&entity.RoomMessageWithdrawAck{},
			&entity.RoomMessageMention{},
			&entity.UserOnlineStat{},
			&entity.UserDeviceSession{},
			&entity.UserSession{},
			&entity.UserFriendRequest{},
			&entity.RoomJoinRequest{},
			&entity.UserMessageNotification{},
			&entity.UserOnlineHistory{},
			&entity.UserCurrentStatus{},
			&entity.RoomAnnouncement{},
			&entity.RoomPinnedMessage{},
			&entity.RoomMuteConfig{},
			&entity.RoomAdminOperation{},
			&entity.UserOperation{},
			&entity.UserRoomBlock{},
			&entity.RoomUserBlockUser{},
			&entity.MediaCallRecord{},
			&entity.UserTheme{},
			&entity.UserEmojiRecent{},
			&entity.UserEmojiFavorite{},
		)
		if err != nil {
			log.Fatal(err)
		}
	})
}

func autoMigrateEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("DB_AUTO_MIGRATE")))
	switch raw {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		log.Warnf("无法识别 DB_AUTO_MIGRATE=%q，按 true 处理", raw)
		return true
	}
}

func withMigrationLock(run func()) {
	const lockKey int64 = 8844226601 // 全局迁移锁，避免多进程并发 AutoMigrate 竞争
	log.Info("等待数据库迁移锁...")
	if e := db.Exec("SELECT pg_advisory_lock(?)", lockKey).Error; e != nil {
		log.Fatalf("获取数据库迁移锁失败: %v", e)
	}
	log.Info("已获取数据库迁移锁，开始执行 AutoMigrate")
	defer func() {
		if e := db.Exec("SELECT pg_advisory_unlock(?)", lockKey).Error; e != nil {
			log.Warnf("释放数据库迁移锁失败: %v", e)
			return
		}
		log.Info("已释放数据库迁移锁")
	}()
	run()
}

func getEnvInt(key string, defaultValue int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		log.Warnf("环境变量 %s=%q 无效，使用默认值 %d", key, raw, defaultValue)
		return defaultValue
	}
	return value
}
