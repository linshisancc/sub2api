#!/bin/bash
# deploy.sh - 拉取最新镜像并重启 sub2api 容器

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> Pulling latest image..."
cd "${SCRIPT_DIR}"
docker compose pull sub2api

echo "==> Restarting sub2api..."
docker compose up -d --no-deps sub2api

echo "==> Done. Watching logs (Ctrl+C to exit)..."
docker compose logs -f --tail=50 sub2api
