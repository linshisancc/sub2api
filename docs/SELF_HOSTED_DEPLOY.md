# 自托管私有镜像部署指南

本文档适用于：在本地构建镜像并推送到 Docker Hub，服务器直接拉取镜像运行，不在服务器上编译代码的场景。

---

## 目录

- [前提条件](#前提条件)
- [首次部署](#首次部署)
- [更新部署（新功能上线）](#更新部署新功能上线)
- [一键部署脚本](#一键部署脚本)
- [数据安全说明](#数据安全说明)

---

## 前提条件

- 本地已安装 Docker，并已登录 Docker Hub（`docker login`）
- 服务器已安装 Docker，无需 Go 环境
- `deploy/docker-compose.yml` 中 `sub2api` 服务的 `image` 已改为 Docker Hub 镜像地址，并删除 `build:` 块：

```yaml
sub2api:
  image: linshisancc/sub2api:latest
```

---

## 首次部署

### 本地：构建并推送镜像

```bash
# 在项目根目录执行
docker build --platform linux/amd64 -t linshisancc/sub2api:latest -f deploy/Dockerfile .
docker push linshisancc/sub2api:latest
```

### 服务器：初始化并启动所有服务

```bash
# 1. 进入 deploy 目录
cd ~/docker/sub2api

# 2. 复制并配置环境变量
cp .env.example .env
# 编辑 .env，至少设置 POSTGRES_PASSWORD、JWT_SECRET、TOTP_ENCRYPTION_KEY

# 3. 拉取镜像并启动所有服务
docker compose up -d

# 4. 查看启动日志
docker compose logs -f sub2api
```

---

## 更新部署（新功能上线）

### 第一步：本地构建并推送新镜像

```bash
# 在项目根目录执行
docker build --platform linux/amd64 -t linshisancc/sub2api:latest -f deploy/Dockerfile .
docker push linshisancc/sub2api:latest
```

也可以同时打版本 tag，方便回滚：

```bash
docker build --platform linux/amd64 -t linshisancc/sub2api:v1.2.0 -t linshisancc/sub2api:latest -f deploy/Dockerfile .
docker push linshisancc/sub2api:v1.2.0
docker push linshisancc/sub2api:latest
```

### 第二步：服务器拉取并重启

```bash
cd ~/docker/sub2api
docker compose pull sub2api
docker compose up -d --no-deps sub2api
docker compose logs -f --tail=50 sub2api
```

> `--no-deps` 确保只重启 sub2api，postgres 和 redis 不受影响，无需提前 `docker compose down`。

---

## 一键部署脚本

`deploy/deploy.sh` 将服务器侧的步骤（拉取镜像 → 重启）串成一条命令，本地推送完镜像后在服务器执行即可：

```bash
bash ~/docker/sub2api/deploy.sh
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
