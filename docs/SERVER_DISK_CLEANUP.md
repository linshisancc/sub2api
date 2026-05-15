# 服务器磁盘空间排查与清理指南

本文档记录如何排查服务器磁盘占用情况，以及清理 Docker 无用资源的方法。

---

## 查看整体磁盘使用

```bash
df -h
```

输出示例：

```
Filesystem      Size  Used Avail Use% Mounted on
/dev/vda1        58G   11G   47G  18% /
```

重点关注主分区（通常挂载在 `/`）的 `Use%`，超过 80% 需要开始清理。

---

## 定位占用大户

**查看用户目录各子目录大小：**

```bash
sudo du -h --max-depth=1 ~ | sort -rh
```

**查看 Docker 整体占用：**

```bash
docker system df
```

输出说明：

| 类型 | 说明 |
|------|------|
| Images | 所有镜像占用 |
| Containers | 容器可写层占用 |
| Local Volumes | 本地 volume 占用 |
| Build Cache | 构建缓存（最常见的空间浪费来源） |

---

## 清理 Docker 无用资源

### 清理构建缓存（最有效）

如果服务器已改为拉取远程镜像运行，本地构建缓存完全没用，可以全部清除：

```bash
docker builder prune -af
```

> 注意：`-a` 表示清除所有缓存（包括未悬空的），`-f` 跳过确认。构建缓存积累后通常会达到数 GB。

### 清理悬空镜像、停止的容器、未使用的网络

```bash
docker system prune
```

### 查看各镜像大小

```bash
docker images --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}"
```

### 清理未使用的 volume（谨慎）

```bash
# 先确认哪些 volume 未被使用
docker volume ls

# 确认无误后清理
docker system prune --volumes
```

> 警告：此操作会删除未挂载到任何容器的 volume，执行前务必确认数据已备份。

---

## 典型清理效果

从服务器本地构建改为拉取 Docker Hub 镜像后，清理构建缓存的效果：

| 清理前 | 清理后 |
|--------|--------|
| Build Cache: 6.8GB | Build Cache: 0B |
| 镜像: 7.5GB | 镜像: 1.0GB |
| 主分区可用: 47GB | 主分区可用: ~54GB |

---

## 日常维护建议

- 定期执行 `docker system df` 观察各项占用趋势
- 改为远程镜像拉取部署后，服务器上不会再产生 Build Cache
- 若磁盘告急，优先清理 Build Cache，其次清理悬空镜像，最后再考虑 volume
