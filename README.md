# cc-server 本地开发

当前项目已提供一键本地集群脚本，开发时默认只需要执行 `scripts/dev.ps1`。

## 快速开始

### 1) 准备环境

- Windows + PowerShell
- Go（需可用 `go` 命令）
- Air（需可用 `air` 命令）

安装 Air：

```powershell
go install github.com/cosmtrek/air@latest
```

### 2) 配置环境变量（开发 / 生产分离）

配置目录按环境拆分：

| 目录 | 用途 |
|------|------|
| `env/dev/` | 本地开发（默认） |
| `env/prod/` | 生产部署 / Linux 构建包 |

每个进程在对应目录下读取：

- `.env.shared`（共用：数据库、Redis、JWT、队列等；**不含 TLS / 上传 / 邮箱**）
- `.env.<服务名>`（专用：`http` / `quic` / `queue` / `oss` / `media`）

首次从示例生成（在项目根目录执行）：

```powershell
.\scripts\init-env.ps1              # 开发：env/dev/
.\scripts\init-env.ps1 -Env prod    # 生产：env/prod/
```

加载规则（`config.LoadFor`）：

- 未设置时默认 **`APP_ENV=dev`** → `env/dev/`
- **`APP_ENV=prod`** → `env/prod/`
- **`CONFIG_DIR`** 可显式覆盖上述目录（最高优先级）

`scripts/dev.ps1` 会自动设置 `APP_ENV=dev`。Linux 构建默认打包 `env/prod/`（`.\scripts\build-linux.ps1 -AppEnv prod`）。

最少建议确认：`env/dev/.env.shared` 中的 `DB_DNS`、`REDIS_*`、`JWT_*` 及集群 Redis 键；`env/dev/.env.http` 的 `MAIL_*`；`env/dev/.env.oss` 的 **`UPLOAD_*`**；`env/dev/.env.quic` 的 `TLS_*`、`QUIC_*`；`env/dev/.env.media` 的 `TLS_*`；多实例时 `dev.ps1` 会覆盖各节点的 `HTTP_CLIENT_BASE_URL` / `OSS_CLIENT_BASE_URL` / `MEDIA_CLIENT_DIAL_ADDR` / `QUIC_CLIENT_DIAL_ADDR`。桌面客户端发布时另见下文「桌面客户端发布」中的 `DESKTOP_*` 与 `data/desktop/web-manifests/`。

### 3) 启动开发集群

在 `cc-server` 根目录执行：

```powershell
.\scripts\dev.ps1
```

脚本会自动：

- 按参数启动多实例：**HTTP / OSS / Media / QUIC**（默认各 1，QUIC 3）；**Queue 默认 1 个**（见下文「队列最佳实践」）
- 各实例向 Redis SET 注册对外地址，供登录与媒体接口做**客户端侧负载均衡**（轮询 + 故障转移）
- 自动探测并跳过已占用的 TCP/UDP 端口
- 为每个实例设置独立 `SERVER_NODE_ID`（如 `local-http-1`）

## 常用启动参数

### 指定各服务节点数量

```powershell
.\scripts\dev.ps1 -HttpNodes 2 -OssNodes 2 -QuicNodes 5 -MediaNodes 2 -QueueNodes 5 -QueueConcurrencyTotal 20
```

（`QueueConcurrencyTotal` 会均分到每个 Queue 实例；QUIC 进程内的 quic 队列消费也会按 `QuicNodes` 分摊。）

### 指定起始端口

```powershell
.\scripts\dev.ps1 -HttpBasePort 6666 -OssBasePort 6667 -QuicBasePort 5000 -MediaBasePort 4434
```

### 自定义节点 ID 前缀

```powershell
.\scripts\dev.ps1 -NodeIdPrefix dev
```

### 仅启动 QUIC（跳过 HTTP/OSS/Queue/Media）

```powershell
.\scripts\dev.ps1 -SkipClusterServices
# 兼容旧名：-SkipSharedServices
```

### 启动前清理旧集群进程

```powershell
.\scripts\dev.ps1 -KillExistingCluster
# 兼容旧名：-KillExistingQuic
```

## 说明

- `scripts/dev.ps1` 会在新终端窗口中拉起各服务。
- 登录接口返回 `quic_addrs`、`http_base_urls`、`oss_base_urls`；媒体 join 从 `media:dial_addrs` 随机选取节点。
- 客户端 `dev.json` 的 `api_base_url` / `oss_base_url` 仍用于**首次登录**入口，登录成功后会切换到集群列表。
- 若提示缺少 `go` 或 `air`，先安装并重新打开终端后再执行脚本。

## 服务角色与集群发现（均可多实例）

各进程启动时向 Redis SET 注册对外地址（键名在 `env/<APP_ENV>/.env.shared` 配置）：

| 服务 | 注册变量 | Redis 键（默认） | 消费方 |
|------|----------|------------------|--------|
| `cmd/http` | `HTTP_CLIENT_BASE_URL`（必填） | `http:base_urls` | 登录下发 `http_base_urls` |
| `cmd/oss` | `OSS_CLIENT_BASE_URL`（必填） | `oss:base_urls` | 登录下发 `oss_base_urls` |
| `cmd/quic` | `QUIC_CLIENT_DIAL_ADDR`（必填） | `quic:dial_addrs` | 登录下发 `quic_addrs` |
| `cmd/media` | `MEDIA_CLIENT_DIAL_ADDR`（必填） | `media:dial_addrs` | 媒体 join 随机 `quic_addr` |
| `cmd/queue` | `SERVER_NODE_ID`（可选） | — | 消费**主队列 + 操作日志队列**（Redis DB 1、3）；本地通常 **1 实例即可** |

### 队列（asynq）最佳实践

本项目的队列按 **Redis DB 分工**，和「要不要多起 `cmd/queue`」不是一回事：

| Redis DB | 谁在生产任务 | 谁在生产消费 |
|----------|--------------|--------------|
| 1 主队列 | HTTP / QUIC 发布 ACK 检查、禁言定时等 | **`cmd/queue`（1 个即可）** |
| 2 QUIC 队列 | 主队列 worker 转发重发 | **`cmd/quic`（随 QUIC 节点数扩展）** |
| 3 操作日志 | HTTP / QUIC 发布 | **`cmd/queue`（1 个即可）** |

结论（和你们现状一致）：

1. **本地开发：`cmd/queue` 默认 1 个就够**。Redis「顶不住」时，更常见原因是 **QUIC 节点 × `QUEUE_CONCURRENCY` 过大**（每个 `cmd/quic` 里还有一个 asynq Server 在消费 DB 2），而不是少开了 Queue 进程。
2. **要扩「实时重发」消费能力 → 加 `cmd/quic` 副本**，不要靠堆 `cmd/queue`。
3. **只有**主队列/操作日志 backlog 长期很高、或单进程 CPU 打满时，才考虑 `QUEUE_REPLICAS>1` 或多个 `cmd/queue`（asynq 多 worker 竞争同一 DB，属合法模式，但不是默认选项）。
4. 调优优先顺序：**降低 `QUEUE_CONCURRENCY`** → 看 Redis 慢查询/内存 → 再考虑加 QUIC 或 Queue 副本。

需要多 Queue 时仍可显式指定：

```powershell
.\scripts\dev.ps1 -QueueNodes 3 -QueueConcurrencyTotal 12
```

Linux：`QUEUE_REPLICAS=3 QUEUE_CONCURRENCY_TOTAL=12 ./server.sh start -s queue`

### 推荐部署方式

- 开发：`.\scripts\dev.ps1`（QUIC 可多节点；Queue 保持 1）。
- 生产：HTTP/OSS/Media/QUIC 按需水平扩；**Queue 一般 1～2 实例**（HA 时 2）；前置 Nginx/SLB 与 Redis 服务发现可并存。

## 编译到 Linux 可运行

项目已新增一键脚本：`scripts/build-linux.ps1`，可在 Windows 下直接交叉编译 Linux 可执行文件。

### 默认构建（linux/amd64）

在 `cc-server` 根目录执行：

```powershell
.\scripts\build-linux.ps1
```

默认目录布局（`dist/linux-amd64/`，可直接整包上传 Linux）：

- **根目录（一键编排）**
  - `start-all.sh`：一键启动全部服务（默认先执行 `infra.sh up` 拉起 PostgreSQL/Redis 并等待健康，再启动业务；后台 + `logs/` + `run/*.pid`）
  - `stop-all.sh`：按 pid 文件停止全部服务
  - `infra.sh`、`docker-compose.infra.yml`：PostgreSQL + Redis
  - `infra.env`：与源码 `env/prod/.env.shared`（或 `-AppEnv` 指定环境）相同，供 `infra.sh` 读取（Redis/Postgres 等）
- **各服务子目录**（`http/`、`quic/`、`queue/`、`oss/`、`media/`），每个内含：
  - 对应 Linux 二进制（如 `http/http`）
  - `env/.env.shared` 与 `env/.env.<服务名>`：由构建脚本从 `env/<AppEnv>/` 复制（运行时 `CONFIG_DIR=./env`）
  - **`cert/` 出现在 `quic/` 与 `media/` 子目录**（与包内 `env/.env.quic`、`env/.env.media` 中 `TLS_*` 路径相对本服务工作目录一致）。
  - **`geodb/` 出现在 `http/` 与 `quic/` 子目录**（与 `GEOIP_DB_PATH=geodb/...` 相对路径一致；若源码根目录无 `geodb/`，构建脚本会跳过复制并提示自行放置 GeoLite2 mmdb）。
  - `start.sh`：进入本目录并 `export CONFIG_DIR=./env` 后启动该二进制

### 构建 linux/arm64

```powershell
.\scripts\build-linux.ps1 -GoArch arm64
```

默认输出目录：`dist/linux-arm64/`

### 自定义输出目录（整包根路径）

```powershell
.\scripts\build-linux.ps1 -OutputDir .\dist\my-linux-bundle
```

会在该目录下生成与各默认包相同的子目录结构（`http/`、`quic/`、…及根脚本）。

### 说明

- 每次执行 `build-linux.ps1` 都会**先清空**目标构建根目录（默认 `dist/linux-amd64/`，或 `-OutputDir` 指定路径），再重新生成全部内容。
- 脚本默认使用 `CGO_ENABLED=0`，减少跨平台构建依赖。
- 构建前请在源码仓库配置好 `env/prod/`（或 `-AppEnv` 对应目录）下的 `.env.shared` 与各 `.env.<服务名>`；构建产物中每个服务目录会**原样复制**到包内 `env/`。
- 容器与业务进程分离：`server.sh` 仅管理业务进程；`infra.sh` 仅管理 PostgreSQL/Redis 容器。

## Linux 部署与一键启动（构建产物）

构建产物根目录（如 `dist/linux-amd64/`）内提供统一服务脚本 `server.sh`（支持 `start/stop/restart/status` + `-all/-s`）；多机集群请只拷贝并启动需要的**单个服务目录**。

```bash
cd ./dist/linux-amd64
chmod +x server.sh start-all.sh stop-all.sh restart-all.sh infra.sh
for d in http quic queue oss media; do chmod +x "$d/start.sh" "$d/$d"; done
./infra.sh up                      # 先启动容器（Postgres + Redis）
./server.sh start -all             # 再启动全部业务服务
./server.sh start -s http,quic     # 仅启动指定服务
./server.sh stop -all              # 停止全部服务
./server.sh stop -s http,quic      # 仅停止指定服务
./server.sh restart -s quic        # 仅重启 quic
./server.sh status -all            # 查看全部服务状态
./server.sh status -s quic         # 查看指定服务状态
# 使用外部 Postgres/Redis 时，可不执行 infra.sh，直接 ./server.sh start -all

# 兼容入口（内部转调 server.sh）
./start-all.sh -all
./stop-all.sh -all
./restart-all.sh -all
```

### 说明

- 每个服务在**自己的目录**内运行，工作目录即该目录，`env/` 与（若适用）`cert/` 随目录走；**`quic/` 与 `media/` 均自带 `cert/`**。
- 根目录 `logs/<服务>.log` 为各服务标准输出日志；`run/<服务>.pid` 为 pid 文件。
- **Queue 多副本（可选）**：`QUEUE_REPLICAS=1`（默认）；大于 1 时启动 `queue-1`…`queue-N`，`QUEUE_CONCURRENCY_TOTAL` 均分到各副本。
- `server.sh` 只负责业务进程（start/stop/restart），不会自动拉起或停止 Docker 容器。
- `infra.sh` 仅使用同目录的 `docker-compose.infra.yml` 与 `infra.env`（构建时由 `env/<AppEnv>/.env.shared` 复制）；需要本机已安装 **Docker Compose V2**（`docker compose` 命令）。
- 默认端口下，防火墙至少放行：PostgreSQL `5432`、Redis `6379`、HTTP `6666`、OSS `6667`、QUIC `4433`、Media QUIC `4578`（最终以 `env/prod/.env.*` 或实际部署配置为准）。
- 为避免多进程并发迁移冲突（`pg_type_typname_nsp_index`），`server.sh start ...` 会仅给 `http` 进程注入 `DB_AUTO_MIGRATE=true`，其余服务为 `false`；手工启动单服务时也可显式设置 `DB_AUTO_MIGRATE=false` 跳过自动迁移。
- **自动迁移使用建议**：`DB_AUTO_MIGRATE=true` 仅建议用于开发/测试环境；生产环境建议关闭自动迁移（`DB_AUTO_MIGRATE=false`）并通过独立迁移流程（人工审核 SQL / 变更窗口）执行结构变更。

## 桌面客户端发布（与 cc-client 配合）

桌面 IM 的发布构建在 **cc-client** 仓库执行 `pnpm run release:updater`（详见 cc-client `README.md`）。构建完成后，cc-server 需提供 **动态更新检查** 与 **web 完整性 manifest** 两类 HTTP 能力（均由 `cmd/http` 提供，无需鉴权）。

### 与 cc-client 的衔接

cc-client 发布脚本会：

1. 在 `cc-client/release-artifacts/updater/<version>/` 生成安装包、`latest.json`、`server-desktop-update.env`、`UPLOAD_README.txt`
2. **自动同步** `web-manifest.json[.sig]` 到本仓库 `data/desktop/web-manifests/<version>/`（与 cc-client 同级目录）

### 1) 动态更新（Tauri updater）

客户端 `tauri.conf.json` 中 `plugins.updater.endpoints` 可指向本服务，例如：

```text
https://<api-host>/api/v1/public/desktop/update/{{target}}/{{arch}}/{{current_version}}
```

| 情况 | HTTP 状态 |
|------|-----------|
| 未配置更新 / 无可用构件 / 客户端已是最新 | `204 No Content` |
| 有新版本 | `200` + JSON（`version` / `url` / `signature` 必填） |

实现见 [`http/handler/desktop/update.go`](http/handler/desktop/update.go)，配置见 [`config/desktop_update.go`](config/desktop_update.go)。

在 **HTTP 服务** 的环境变量中配置（`env/<APP_ENV>/.env.http`）。可直接将 cc-client 产物中的 `server-desktop-update.env` 合并进去，例如：

```env
DESKTOP_UPDATE_LATEST_VERSION=0.1.0
DESKTOP_UPDATE_NOTES=
DESKTOP_UPDATE_PUB_DATE=2026-06-18T08:17:37.969Z

DESKTOP_UPDATE_ARTIFACT_WINDOWS_X86_64_URL=https://api.example.com/releases/desktop/0.1.0/IM_x64-setup.exe
DESKTOP_UPDATE_ARTIFACT_WINDOWS_X86_64_SIGNATURE=<.sig 文件全文，单行>
```

平台键与 Tauri 一致（`windows-x86_64`、`darwin-aarch64` 等），对应环境变量后缀为 `DESKTOP_UPDATE_ARTIFACT_<平台键大写且横线改下划线>_URL` / `_SIGNATURE`。未设置 `DESKTOP_UPDATE_LATEST_VERSION` 时，动态更新接口恒返回 `204`（等同关闭）。

合并 env 后需**重启 HTTP 服务**生效。安装包本身需单独上传到 `*_URL` 所指公网路径（与 cc-client `release.json` 中的公网基址一致）。

> 也可不走动态接口，将 `latest.json` 静态托管到 CDN；此时客户端 `endpoints` 指向该 JSON 即可。cc-server 动态更新适合由服务端统一管控版本与签名。

### 2) Web 完整性 manifest

客户端启动时会联网拉取 manifest，校验安装目录下的 `web/` 静态文件及核心二进制（`IM.exe`、`cc_im_lib.dll`、`cc-im-ocr.exe`）是否被篡改。

**目录结构**（相对 HTTP 进程工作目录）：

```text
data/desktop/web-manifests/<version>/web-manifest.json
data/desktop/web-manifests/<version>/web-manifest.json.sig
```

在 `env/<APP_ENV>/.env.http` 中配置（示例已写在 [`.env.http.example`](env/dev/.env.http.example)）：

```env
DESKTOP_WEB_MANIFEST_DIR=data/desktop/web-manifests
```

**公开接口**（实现见 [`http/handler/desktop/web_integrity.go`](http/handler/desktop/web_integrity.go)）：

| 方法 | 路径 |
|------|------|
| GET | `/api/v1/public/desktop/web-integrity/:version/web-manifest.json` |
| GET | `/api/v1/public/desktop/web-integrity/:version/web-manifest.json.sig` |

每次请求从磁盘读取；**上传或替换 manifest 文件后无需重启 HTTP**（响应头 `Cache-Control: no-store`）。

### 3) 发布检查清单

1. **cc-client**：`pnpm run release:updater`，确认 `release-artifacts/updater/<version>/` 产物完整
2. **安装包**：将 `IM_*-setup.exe` 及同名 `.sig` 上传到公网（路径与 `DESKTOP_UPDATE_ARTIFACT_*_URL` 一致）
3. **动态更新**：将 `server-desktop-update.env` 合并进 `env/prod/.env.http`（或 Linux 包内 `http/env/.env.http`），重启 HTTP
4. **Web 完整性**：确认 `data/desktop/web-manifests/<version>/` 下已有 `web-manifest.json[.sig]`（cc-client 脚本通常已同步）
5. **Linux 部署**：`build-linux.ps1` 默认**不**打包 `data/desktop/`。需在服务器 `http/` 服务目录下自行放置 `data/desktop/web-manifests/`（或将 `DESKTOP_WEB_MANIFEST_DIR` 改为服务器上的绝对路径）

## Docker 一键部署数据库和 Redis

已新增：

- `docker-compose.infra.yml`（PostgreSQL + Redis）
- `infra.sh`（一键启动/停止/查看日志）

### 1) 启动

```bash
cd ./dist/linux-amd64
chmod +x ./infra.sh
./infra.sh up          # 启动并等待 Postgres/Redis 健康
# ./infra.sh up-bg     # 仅后台启动，不等待健康（调试）
```

### 2) 停止

```bash
./infra.sh down
```

### 3) 查看状态与日志

```bash
./infra.sh ps
./infra.sh logs
```

### 说明

- 默认会读取同目录 `infra.env` 中的 `REDIS_PASSWORD`、`REDIS_PORT`、`POSTGRES_*` 等变量。
- PostgreSQL 默认端口 `5432`，Redis 默认端口 `6379`。
- 数据持久化目录：
  - `docker-data/postgres`
  - `docker-data/redis`
