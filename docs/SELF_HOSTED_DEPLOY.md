# 自托管私有镜像部署指南

本文档适用于：在服务器上从源码构建镜像并部署，不依赖 Docker Hub 的场景。

---

## 目录

- [前提：docker-compose.yml 已配置本地构建](#前提docker-composeyml-已配置本地构建)
- [首次部署](#首次部署)
- [更新部署（新功能上线）](#更新部署新功能上线)
- [一键部署脚本](#一键部署脚本)
- [数据安全说明](#数据安全说明)

---

## 前提：docker-compose.yml 已配置本地构建

`deploy/docker-compose.yml` 已将 `sub2api` 服务配置为本地构建模式（`image: sub2api:local` + `build:` 声明），无需手动修改，直接使用即可。

如需确认，相关配置如下：

```yaml
sub2api:
  image: sub2api:local
  build:
    context: ..
    dockerfile: deploy/Dockerfile
    args:
      GOPROXY: https://goproxy.cn,direct
      GOSUMDB: sum.golang.google.cn
```

---

## 首次部署

```bash
# 1. 克隆代码并进入 deploy 目录
cd /path/to/sub2api/deploy

# 2. 复制并配置环境变量
cp .env.example .env
# 编辑 .env，至少设置 POSTGRES_PASSWORD、JWT_SECRET、TOTP_ENCRYPTION_KEY

# 3. 构建镜像并启动所有服务
docker compose up -d --build

# 4. 查看启动日志
docker compose logs -f sub2api
```

---

## 更新部署（新功能上线）

每次有新功能需要上线，流程如下：

```bash
# 1. 进入项目根目录，拉取最新代码
cd /path/to/sub2api
git pull

# 2. 重新构建镜像
cd deploy
docker compose build sub2api

# 3. 只重启 sub2api 容器（postgres 和 redis 保持运行）
docker compose up -d --no-deps sub2api

# 4. 观察启动日志确认正常
docker compose logs -f sub2api
```

> `--no-deps` 确保只重启 sub2api，postgres 和 redis 不受影响。

---

## 一键部署脚本

`deploy/deploy.sh` 将上述步骤（git pull → 构建 → 重启）串成一条命令，适合日常更新使用。

首次使用赋予执行权限：

```bash
chmod +x deploy/deploy.sh
```

之后每次更新执行：

```bash
bash deploy/deploy.sh
```

---

## 数据安全说明

重建镜像和重启容器**不会影响任何已有数据**。所有持久化数据存储在 Docker named volumes 中，与容器生命周期完全分离：

| Volume | 存储内容 | 重建容器后 |
|--------|---------|-----------|
| `postgres_data` | 数据库数据（用户、账号、账单等） | 完全保留 |
| `redis_data` | 缓存数据（含告警冷却状态等） | 完全保留 |
| `sub2api_data` | 应用配置文件（config.yaml 等） | 完全保留 |

只有容器内的可执行文件被替换，volumes 不受影响。

> **注意**：更新过程中有数秒停机窗口（旧容器停止 → 新容器启动），自托管场景下通常可以接受。
