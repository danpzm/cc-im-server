// dbbulkseed 向 PostgreSQL 各业务表批量灌入测试数据（默认每表百万行）。
// 仅用于本地/压测库；会显著撑大库体积，且与真实业务数据无关。
//
// 用法：
//
//	go run ./cmd/dbbulkseed -confirm
//	# 默认会加载 CONFIG_DIR（未设置则 APP_ENV=dev → env/dev）下 .env.shared 与 .env.http，再读 DB_DNS。
//	# 也可用 -dsn 显式指定连接串（优先级高于 DB_DNS）。
//
// 可选：-rows=10000 降低行数；-tables=user,room 只灌部分表（逗号分隔，见 -help）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/xd/quic-server/config"
)

func main() {
	var (
		dsnFlag     = flag.String("dsn", "", "PostgreSQL DSN（默认读取环境变量 DB_DNS；高于 dotenv 中的 DB_DNS）")
		noDotenv    = flag.Bool("no-dotenv", false, "不加载 CONFIG_DIR 下的 .env.shared / .env.http（仅用当前进程环境）")
		envFile     = flag.String("envfile", "", "可选：在 .env.shared/.env.http 之后再合并该文件（后者覆盖前者；仍不覆盖进程已有变量）")
		rowsFlag    = flag.Int64("rows", 1_000_000, "每张表插入行数（所有表使用相同行数）")
		confirm     = flag.Bool("confirm", false, "必须显式传入，否则拒绝执行")
		tablesFlag  = flag.String("tables", "all", "all 或逗号分隔表名（与 PostgreSQL 表名一致，如 user,room,room_message）")
		batchReport = flag.Int("batch-log", 100_000, "每插入多少行打印一次进度（按表内累计）")
	)
	flag.Parse()

	if !*confirm {
		log.Fatal("拒绝执行：请加 -confirm。该工具会向数据库大量写入数据。")
	}
	if *rowsFlag <= 0 {
		log.Fatal("-rows 必须为正整数")
	}

	extra := strings.TrimSpace(*envFile)
	if !*noDotenv {
		dir := configDir()
		paths := []string{
			filepath.Join(dir, ".env.shared"),
			filepath.Join(dir, ".env.http"),
		}
		if extra != "" {
			paths = append(paths, extra)
		}
		mergeDotenvRespectingOS(paths...)
	} else if extra != "" {
		mergeDotenvRespectingOS(extra)
	}

	dsn := strings.TrimSpace(*dsnFlag)
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("DB_DNS"))
	}
	if dsn == "" {
		log.Fatal("未提供 DSN：请配置 DB_DNS（默认已从 CONFIG_DIR 加载 dotenv），或使用 -dsn")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("解析 DSN 失败: %v", err)
	}
	cfg.MaxConns = 4

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Ping 失败: %v", err)
	}

	tset := parseTables(*tablesFlag)
	log.Printf("开始灌数: rows=%d tables=%v", *rowsFlag, tset)

	runPrefix := newRunPrefix()
	opts := seedOpts{
		Rows:        *rowsFlag,
		BatchLog:    int64(*batchReport),
		NowMs:       time.Now().UnixMilli(),
		RunPrefix:   runPrefix,
		JSONEmpty:   []byte("{}"),
		JSONArray:   []byte("[]"),
		InviteeJSON: nil,
	}
	opts.InviteeJSON = []byte(fmt.Sprintf(`["%s"]`, opts.Col("uu", 1)))
	log.Printf("本 run 随机前缀 run_prefix=%s（char(20)/用户名/邮箱/jti 等均带此前缀，可重复执行）", runPrefix)

	for _, job := range allSeedJobs() {
		if !tset.all {
			if _, ok := tset.set[job.Table]; !ok {
				continue
			}
		}
		start := time.Now()
		n, err := job.Fn(ctx, pool, opts)
		if err != nil {
			log.Fatalf("表 %s: %v", job.Table, err)
		}
		log.Printf("表 %s: 完成 %d 行, 耗时 %s", job.Table, n, time.Since(start).Truncate(time.Millisecond))
	}

	log.Println("全部完成")
}

type tableSet struct {
	all bool
	set map[string]struct{}
}

func parseTables(raw string) tableSet {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || raw == "all" {
		return tableSet{all: true, set: nil}
	}
	parts := strings.Split(raw, ",")
	m := make(map[string]struct{})
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		m[p] = struct{}{}
	}
	return tableSet{all: false, set: m}
}

type seedJob struct {
	Table string
	Fn    func(context.Context, *pgxpool.Pool, seedOpts) (int64, error)
}

func configDir() string {
	return config.ResolveConfigDir()
}

// mergeDotenvRespectingOS 按路径顺序合并变量（后者覆盖前者），且绝不覆盖进程里已存在的环境变量（与 config.LoadFor 的 get 语义一致）。
func mergeDotenvRespectingOS(paths ...string) {
	merged := make(map[string]string)
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.Printf("警告: 检查 %s: %v", path, err)
			continue
		}
		m, err := godotenv.Read(path)
		if err != nil {
			log.Printf("警告: 读取 %s 失败: %v", path, err)
			continue
		}
		maps.Copy(merged, m)
		log.Printf("已合并环境文件: %s (%d 项)", path, len(m))
	}
	for k, v := range merged {
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			log.Printf("警告: Setenv %s: %v", k, err)
		}
	}
}
