# 数据备份与服务器迁移指南

本文档适用于 `docker-compose.yml`（Docker named volumes）部署方式。本地目录版（`docker-compose.local.yml`）的迁移更简单，直接打包整个 deploy 目录即可，不在本文讨论范围。

---

## 目录

- [数据存储概览](#数据存储概览)
- [日常备份（cron 定时任务）](#日常备份cron-定时任务)
- [从备份恢复数据库](#从备份恢复数据库)
- [完整迁移到新服务器](#完整迁移到新服务器)
- [关键注意事项](#关键注意事项)

---

## 数据存储概览

部署目录下有三类数据，分别存储在不同的位置：

| 数据 | 存储位置 | 重要性 | 可否丢失 |
|------|---------|--------|---------|
| PostgreSQL | Docker volume `postgres_data` | 唯一事实来源 | 不可丢失 |
| 应用配置 | Docker volume `sub2api_data` | config.yaml 自动生成 | 可重建 |
| Redis 缓存 | Docker volume `redis_data` | 鉴权缓存、会话、限流 | 可重建 |

PostgreSQL 存储所有业务数据：用户、账号、分组、订阅、订单、用量日志等。这是**必须备份**的数据。Redis 是纯缓存，重启后自动重建。

查看你的 volume 名称：

```bash
cd ~/docker/sub2api
docker volume ls | grep sub2api
```

---

## 日常备份

不需要停机，pg_dump 是只读操作，不锁表、不中断业务。

### 手动备份

在服务器上执行单行命令即可，备份文件生成在当前目录：

```bash
docker compose exec -T postgres pg_dump -U sub2api --clean --if-exists --no-owner --no-acl sub2api | gzip > sub2api_backup_$(date +%Y%m%d_%H%M).sql.gz
```

执行后当前目录会生成 `sub2api_backup_20260101_030000.sql.gz` 这样的文件。拉取到本地：

```bash
scp user@your-server:~/docker/sub2api/sub2api_backup_*.sql.gz ~/Desktop/
```

### 定时备份（cron）

如果希望每天自动执行，使用以下脚本注册 crontab。

#### 备份脚本

```bash
#!/bin/bash
# /opt/scripts/sub2api-backup.sh

set -e

BACKUP_DIR="/backups/sub2api"
RETENTION_DAYS=7
DEPLOY_DIR="$HOME/docker/sub2api"

mkdir -p "$BACKUP_DIR"

cd "$DEPLOY_DIR"

# 备份数据库（--clean --if-exists 保证恢复时先删后建，幂等执行）
docker compose exec -T postgres \
  pg_dump -U sub2api --clean --if-exists --no-owner --no-acl sub2api \
  | gzip > "$BACKUP_DIR/sub2api_$(date +%Y%m%d_%H%M).sql.gz"

# 备份 .env（含 JWT_SECRET / TOTP_ENCRYPTION_KEY / DB 密码）
cp .env "$BACKUP_DIR/.env.$(date +%Y%m%d)"

# 清理过期备份
find "$BACKUP_DIR" -name "*.sql.gz" -mtime +$RETENTION_DAYS -delete
find "$BACKUP_DIR" -name ".env.*" -mtime +30 -delete

echo "[$(date)] Backup completed: $(ls -lh "$BACKUP_DIR" | tail -1)"
```

### 注册 crontab

```bash
chmod +x /opt/scripts/sub2api-backup.sh

# 每天凌晨 3 点执行
(crontab -l 2>/dev/null; echo "0 3 * * * /bin/bash /opt/scripts/sub2api-backup.sh >> /var/log/sub2api-backup.log 2>&1") | crontab -
```

### pg_dump 参数说明

| 参数 | 作用 |
|------|------|
| `--clean` | 生成 DROP 语句，恢复时先删表再建表 |
| `--if-exists` | DROP 时使用 IF EXISTS，首次导入不报错 |
| `--no-owner` | 不保留原数据库的角色信息，跨环境恢复时不报 owner 错误 |
| `--no-acl` | 不保留权限设置，避免权限冲突 |

---

## 从备份恢复数据库

### 完整恢复

```bash
cd ~/docker/sub2api

# 确认 postgres 正常运行
docker compose ps postgres

# 恢复（数据量大时可能需要几分钟）
zcat /backups/sub2api/sub2api_20260101_030000.sql.gz | \
  docker compose exec -T postgres psql -U sub2api -d sub2api
```

恢复过程会先 DROP 已有表再重新创建并插入数据，整个操作在一个连接中顺序执行。

### 恢复前备份当前库

```bash
# 先给当前库做个快照，万一恢复出问题还能回滚
docker compose exec -T postgres \
  pg_dump -U sub2api --clean --if-exists --no-owner --no-acl sub2api \
  | gzip > ~/pre_restore_backup.sql.gz
```

---

## 完整迁移到新服务器

### 方式一：Volume 目录打包（推荐）

直接打包 Docker volume 的物理文件，恢复后 PostgreSQL 直接可用，无需跑 SQL。

**源服务器——导出：**

```bash
cd ~/docker/sub2api

# 停机
docker compose down

# 导出 PostgreSQL 数据目录
docker run --rm \
  -v sub2api_postgres_data:/data \
  -v $(pwd):/backup \
  alpine tar czf /backup/postgres_data.tar.gz -C /data .

# 导出 Redis 数据目录
docker run --rm \
  -v sub2api_redis_data:/data \
  -v $(pwd):/backup \
  alpine tar czf /backup/redis_data.tar.gz -C /data .

# 导出应用数据目录
docker run --rm \
  -v sub2api_sub2api_data:/data \
  -v $(pwd):/backup \
  alpine tar czf /backup/sub2api_data.tar.gz -C /data .

# 复制 .env
cp .env .env.migration
```

**传输：**

```bash
scp postgres_data.tar.gz redis_data.tar.gz sub2api_data.tar.gz .env.migration \
  user@new-server:~/docker/sub2api/
```

**新服务器——导入：**

```bash
cd ~/docker/sub2api

cp .env.migration .env

# 创建 volumes（up 后立即 down，只保留刚创建的 volumes）
docker compose up -d --no-start
docker compose down

# 解压数据到对应 volume
docker run --rm \
  -v sub2api_postgres_data:/data \
  -v $(pwd):/backup \
  alpine tar xzf /backup/postgres_data.tar.gz -C /data

docker run --rm \
  -v sub2api_redis_data:/data \
  -v $(pwd):/backup \
  alpine tar xzf /backup/redis_data.tar.gz -C /data

docker run --rm \
  -v sub2api_sub2api_data:/data \
  -v $(pwd):/backup \
  alpine tar xzf /backup/sub2api_data.tar.gz -C /data

# 启动
docker compose up -d
docker compose logs -f --tail=50 sub2api
```

此方法的数据完整性最高——PostgreSQL 是数据库原生的文件级拷贝。停机时间取决于 tar 打包耗时和传输耗时。

### 方式二：pg_dump + Redis 文件（小数据量）

适合数据库在几 GB 以内、对停机时间不敏感的场景。

**源服务器——导出：**

```bash
cd ~/docker/sub2api

# 导出数据库
docker compose exec -T postgres \
  pg_dump -U sub2api --clean --if-exists --no-owner --no-acl sub2api \
  | gzip > sub2api_db.sql.gz

# 导出 Redis 持久化文件
docker compose exec redis redis-cli SAVE
docker compose cp redis:/data/dump.rdb ./redis_dump.rdb
docker compose cp redis:/data/appendonly.aof ./redis_appendonly.aof

# 收集传输文件
# sub2api_db.sql.gz + redis_dump.rdb + redis_appendonly.aof + .env
```

**新服务器——导入：**

```bash
cd ~/docker/sub2api

# 先启动 postgres 和 redis
docker compose up -d postgres redis

# 等待 postgres 就绪
until docker compose exec -T postgres pg_isready -U sub2api; do sleep 2; done

# 恢复数据库
zcat sub2api_db.sql.gz | docker compose exec -T postgres psql -U sub2api -d sub2api

# 恢复 Redis 文件
docker compose cp redis_dump.rdb redis:/data/dump.rdb
docker compose cp redis_appendonly.aof redis:/data/appendonly.aof
docker compose restart redis

# 启动应用
docker compose up -d
docker compose logs -f --tail=50 sub2api
```

---

## 关键注意事项

### .env 文件必须备份

`.env` 中包含三个关键密钥，迁移到新服务器后**必须保持不变**：

| 变量 | 作用 | 如果变了会怎样 |
|------|------|---------------|
| `JWT_SECRET` | 用户登录 session 签名密钥 | 所有用户被强制登出 |
| `TOTP_ENCRYPTION_KEY` | 二次验证 TOTP 加密密钥 | 所有用户的 2FA 失效，无法登录 |
| `POSTGRES_PASSWORD` | 数据库密码 | 应用无法连接数据库 |

### 跨环境恢复注意 owner

使用 `--no-owner --no-acl` 导出，避免源服务器的数据库用户名和新服务器不一致导致恢复失败。

### 迁移后验证

```bash
# 确认所有容器正常
docker compose ps

# 查看应用日志，确认无报错
docker compose logs --tail=100 sub2api | grep -i "error\|fatal\|panic"

# 登录管理后台，抽查核心数据是否完整
# - 用户列表
# - 账号列表
# - 分组配置
# - 订单记录
```

### Redis 的处理方式

两种迁移方式都会保留 Redis 数据，迁移后缓存完整可用。

日常 cron 备份脚本只备份 PostgreSQL，不备份 Redis——因为 Redis 是纯缓存，没有持久化的业务数据。从 cron 备份恢复时（或服务器异常导致 Redis 数据损坏时），Redis 是空的，影响如下：

- 用户鉴权缓存被清空：下次请求会查数据库重建，首请求稍慢
- sticky session 丢失：用户会话可能切换到不同账号
- 飞书告警冷却状态丢失：短期内可能重复收到相同告警

以上影响均在几分钟内自动恢复，不丢失业务数据。

### 大数据量场景

如果 `usage_logs` 表积累了海量数据，可以考虑：

- 备份时排除 usage_logs：`pg_dump --exclude-table=usage_logs ...`
- 单独备份 usage_logs 表结构（不含数据）：`pg_dump --schema-only --table=usage_logs ...`
- 使用 Volume 目录打包方式（方式一）不受数据量影响恢复速度
