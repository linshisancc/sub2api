#!/bin/bash
# deploy.sh - 拉取最新代码、重新构建镜像并重启 sub2api 容器

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "==> Pulling latest code..."
cd "${REPO_ROOT}"
git pull

echo "==> Building image..."
cd "${SCRIPT_DIR}"
docker compose build sub2api

echo "==> Restarting sub2api..."
docker compose up -d --no-deps sub2api

echo "==> Done. Watching logs (Ctrl+C to exit)..."
docker compose logs -f --tail=50 sub2api
