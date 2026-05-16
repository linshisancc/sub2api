# SSE 流式输出排查指南

本文档用于排查「客户端（Claude Code CLI / SDK 等）收到的回复是一块一块的，
而不是逐字流式输出」这类问题。

---

## 症状

- 通过中转站请求 AI 接口时，开启了 `stream: true`，但响应不是平滑的逐 token 输出，
  而是积攒一批后一次性涌出，呈「一块一块」的节奏。
- 直连后端（不经过反向代理 / CDN）时正常，接入域名 / CDN 后才出现。

## 根因总述

SSE（`text/event-stream`）依赖**每个事件产生后立即送达客户端**。链路上任何一层做了
**压缩**或**缓冲**，都会把多个事件攒成一块再下发。请求链路：

```
Claude Code CLI  →  Cloudflare(可选)  →  Caddy 反向代理  →  sub2api 后端  →  上游 API
```

缓冲可能发生在其中任意一层。**逐层排查、逐层排除**是定位的关键。

---

## 分层排查

### 第 1 层：sub2api 后端 —— 已确认正常

后端流式实现（`backend/internal/service/gateway_service.go` 的 `handleStreamingResponse`）：

- 用 `bufio.Scanner` 逐行读取上游，按空行切分出完整 SSE 事件；
- **每写完一个事件立即 `flusher.Flush()`**；
- 设置了 `X-Accel-Buffering: no`、`Cache-Control: no-cache`、`Connection: keep-alive`。

结论：后端按事件粒度 flush，不是缓冲源，无需改动。

> `X-Accel-Buffering: no` 是 nginx 系反向代理识别的约定头；Caddy 和 Cloudflare **不认**这个头，
> 因此它只对 nginx 链路有效。

### 第 2 层：Caddy 反向代理 —— 压缩块的常见元凶

Caddy 的 `encode` 指令会对响应做 zstd/gzip 压缩。压缩器必须**攒够输入**才能产出一个压缩块，
于是多个 SSE 事件被压成一块再下发 —— 这正是「一块一块」。

**关键陷阱**：`encode` 的 `match` 块若使用 `text/*` 通配，会连 `text/event-stream` 一起匹配，
导致 SSE 响应被压缩。

**正确写法**（见 `deploy/Caddyfile`）—— 不使用 `text/*` 通配，只列明确子类型：

```caddy
encode {
    zstd
    gzip 6
    minimum_length 256
    # 刻意不使用 text/* 通配，否则会匹配 text/event-stream，
    # 导致 SSE 流式响应被压缩器缓冲。
    match {
        header Content-Type text/html*
        header Content-Type text/css*
        header Content-Type text/plain*
        header Content-Type text/javascript*
        header Content-Type text/xml*
        header Content-Type text/markdown*
        header Content-Type application/json*
        header Content-Type application/javascript*
        header Content-Type application/xml*
        header Content-Type application/rss+xml*
        header Content-Type image/svg+xml*
    }
}
```

> 注意：曾尝试用 `not header Content-Type text/event-stream*` 排除 SSE，但 `encode` 的
> `match` 块在部分 Caddy 版本中**不支持 `not`**，`caddy validate` 会直接报错。
> 改用「明确列举子类型」的写法更稳健，所有 Caddy 版本通用。

**改完务必让配置生效**（仓库里的 `deploy/Caddyfile` 只是模板，改它不影响线上）：

```bash
# 把新配置同步到服务器实际使用的 Caddyfile（如 /etc/caddy/Caddyfile）后：
caddy validate --config /etc/caddy/Caddyfile   # 校验语法
caddy reload   --config /etc/caddy/Caddyfile   # 热重载
# 或：systemctl reload caddy
```

### 第 3 层：Cloudflare —— 接入域名后才出问题，多半是它

如果「没接 Cloudflare 时正常、接了之后变一块一块」，基本可锁定 Cloudflare。
Cloudflare 边缘会对响应做自己的压缩（gzip/brotli），压缩同样需要缓冲 → SSE 被攒块。

按以下顺序处理：

1. **加 Compression Rule（推荐，零改码）**
   Cloudflare 控制台 → Rules → Compression Rules，新增规则：
   - 匹配：`Hostname equals api.你的域名` 或 `URI Path starts with /v1/`
   - 动作：Compression 设为 **off**

2. **源站发送 `Cache-Control: no-transform`**
   Cloudflare 遵守 `no-transform`，收到后不会对该响应做压缩 / 改写。
   可在 Caddy 层为流式响应补这个头，或在后端流式响应里把 `no-cache` 改为
   `no-cache, no-transform`。

3. **给 API 单独留一个不过 Cloudflare 代理的入口**
   把 API 子域名的 DNS 记录设为 **DNS only（灰云）**，或新增一个灰云子域名
   （如 `api-direct.你的域名`）专供 CLI 使用。代价是失去 Cloudflare 的防护 / 缓存。

> Cloudflare 不识别 `X-Accel-Buffering: no`，靠它无效。

### 第 4 层：上游 API

极少见。若上游本身返回非流式或缓冲，超出中转站可控范围，一般无需考虑。

---

## 逐层定位法

最快的定位手段是**绕过某一层做对比**：

```bash
# A. 经完整链路（CLI → Cloudflare → Caddy → 后端）
curl -N -sS -D - https://api.你的域名/v1/messages \
  -H 'content-type: application/json' \
  -H 'x-api-key: <你的key>' \
  -d '{"model":"claude-opus-4-7","max_tokens":64,"stream":true,
       "messages":[{"role":"user","content":"数到20"}]}'

# B. 绕过 Cloudflare，直连源站 IP（仍经过 Caddy）
curl -N -sS -D - https://api.你的域名/v1/messages \
  --resolve api.你的域名:443:<源站公网IP> \
  -H 'content-type: application/json' \
  -H 'x-api-key: <你的key>' \
  -d '{"model":"claude-opus-4-7","max_tokens":64,"stream":true,
       "messages":[{"role":"user","content":"数到20"}]}'
```

判断：

| A 表现 | B 表现 | 结论 |
|--------|--------|------|
| 块状 | 流式 | 问题在 **Cloudflare** |
| 块状 | 块状 | 问题在 **Caddy 或后端** |
| 流式 | 流式 | 已正常 |

---

## 验证修复

`curl -N` 会关闭客户端缓冲。修复后重新执行上面的命令，检查：

- **响应头不应出现** `Content-Encoding: gzip` / `Content-Encoding: zstd` / `Content-Encoding: br`
  —— 一旦出现，说明该层仍在压缩 SSE。
- 响应头应有 `Content-Type: text/event-stream`。
- 终端里 `data:` 事件应**逐条陆续到达**，而不是末尾一次性涌出。

---

## 速查清单

- [ ] 后端：逐事件 `Flush()`（已实现，无需改动）。
- [ ] Caddy：`encode` 的 `match` 不含 `text/*` 通配；不依赖 `not`。
- [ ] Caddy：改完已 `caddy validate` + `caddy reload`。
- [ ] Cloudflare：对 API 路径关闭 Compression，或源站发 `Cache-Control: no-transform`。
- [ ] 验证：`curl -N` 看不到 `Content-Encoding`，事件逐条到达。
