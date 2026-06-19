#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$APP_DIR/docker-compose.infra.yml"
ENV_FILE="$APP_DIR/infra.env"

usage() {
  cat <<EOF
用法:
  $(basename "$0") up        # 启动 PostgreSQL + Redis，并等待健康检查通过
  $(basename "$0") up-bg     # 仅后台启动，不等待健康（调试用）
  $(basename "$0") down      # 停止并删除容器
  $(basename "$0") restart   # 重启基础设施
  $(basename "$0") ps        # 查看容器状态
  $(basename "$0") logs      # 查看容器日志

要求（与构建产物根目录一致）:
  - $COMPOSE_FILE
  - $ENV_FILE
  - 本机已安装 Docker Compose V2: docker compose
EOF
}

compose() {
  if [[ ! -f "$COMPOSE_FILE" ]]; then
    echo "错误: 缺少 $COMPOSE_FILE"
    exit 1
  fi
  if [[ ! -f "$ENV_FILE" ]]; then
    echo "错误: 缺少 $ENV_FILE"
    exit 1
  fi
  if ! command -v docker >/dev/null 2>&1; then
    echo "错误: 未找到 docker"
    exit 1
  fi
  if ! docker compose version >/dev/null 2>&1; then
    echo "错误: 需要 Docker Compose V2（docker compose）"
    exit 1
  fi
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

wait_infra_healthy() {
  local start=$SECONDS
  local limit=300
  echo "等待 PostgreSQL / Redis 就绪（最多 ${limit}s）..."
  while true; do
    local pg_status rd_status
    pg_status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' cc-postgres 2>/dev/null || echo none)"
    rd_status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' cc-redis 2>/dev/null || echo none)"
    if [[ "$pg_status" == "healthy" && "$rd_status" == "healthy" ]]; then
      echo "基础设施已就绪。"
      return 0
    fi
    if (( SECONDS - start >= limit )); then
      echo "错误: 等待超时（postgres=$pg_status redis=$rd_status）。请检查: docker compose --env-file \"$ENV_FILE\" -f \"$COMPOSE_FILE\" logs"
      return 1
    fi
    echo "  postgres=$pg_status redis=$rd_status ..."
    sleep 2
  done
}

ACTION="${1:-up}"

case "$ACTION" in
  up)
    compose up -d
    wait_infra_healthy
    compose ps
    ;;
  up-bg)
    compose up -d
    compose ps
    ;;
  down)
    compose down
    ;;
  restart)
    compose down
    compose up -d
    wait_infra_healthy
    compose ps
    ;;
  ps)
    compose ps
    ;;
  logs)
    compose logs -f --tail=200
    ;;
  *)
    usage
    exit 1
    ;;
esac
