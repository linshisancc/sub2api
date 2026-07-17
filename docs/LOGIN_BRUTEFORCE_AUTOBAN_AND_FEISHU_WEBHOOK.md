# 登录爆破自动封禁与飞书 Webhook 告警

> 实现状态：代码已在本地完成并适配上游 `v0.1.159`，尚未提交、尚未部署生产。
> 适用范围：`sub2api` 认证登录接口与全站 HTTP 请求。
> 生产前置状态：源站入口隔离、Caddy HTTPS 回源、Cloudflare Full (strict) 和 VPN 8443 验证已完成；生产镜像版本与真实客户端 IP仍待验证。

## 1. 功能概述

该功能用于发现同一客户端 IP 在短时间内反复登录失败的行为，并自动对该 IP 实施临时封禁。封禁触发后，系统可通过已有的飞书 Webhook 告警能力向管理员推送通知。

核心能力包括：

- 按客户端 IP 统计 `POST /api/v1/auth/login` 的登录失败次数
- 在配置的统计窗口内达到阈值时自动封禁 IP
- 封禁期间拦截该 IP 对整站所有接口的访问
- 封禁到期后由 Redis TTL 自动解封
- 通过管理接口查看当前封禁列表和手动解封
- 在启用并配置飞书 Webhook 时异步发送封禁告警
- Redis 或飞书异常时不改变原登录响应，避免依赖故障扩大为全站不可用

该功能不能代替 Cloudflare WAF、Turnstile、源站端口隔离或防火墙。它属于应用层的补充防护。

## 2. 工作流程

```text
客户端请求
    |
    v
统一解析安全客户端 IP
    |
    v
全局 IP 封禁检查
    |-- 已封禁 --> HTTP 403 IP_BANNED
    |
    `-- 未封禁 --> 进入正常路由
                         |
                         v
              POST /api/v1/auth/login
                         |
             +-----------+-----------+
             |                       |
          非 401                    HTTP 401
             |                       |
          不计数                     v
                              Redis 原子计数 + TTL
                                      |
                         +------------+------------+
                         |                         |
                      未达阈值                  达到阈值
                         |                         |
                       放行                       v
                                          写入临时封禁键
                                          清除失败计数
                                          异步飞书告警
```

### 2.1 登录失败计数

登录失败追踪中间件只挂载在：

```http
POST /api/v1/auth/login
```

请求处理完成后，只有响应状态码为 `401 Unauthorized` 时才会计数。注册、OAuth、`/auth/login/2fa`、忘记密码及其他接口不会进入该计数逻辑；`429 Too Many Requests` 等非 401 响应也不会计数。

失败记录保留请求中的安全上下文值，但脱离客户端连接的取消信号，并设置 3 秒超时。因此攻击者在收到 401 前后主动断开连接不能跳过 Redis 计数，数据库或 Redis 异常也不会无限阻塞中间件。

失败计数使用 Redis Lua 脚本原子执行 `INCR` 和首次 TTL 设置，避免并发登录时发生计数竞争。当前窗口是从该 IP 第一次失败开始计算的固定窗口，不会在每次失败时延长。

### 2.2 自动封禁

失败次数达到阈值时，系统写入带 TTL 的 Redis 封禁记录。封禁立即生效，失败计数键同时被删除，使封禁到期后的下一轮统计从零开始。

封禁检查是全局中间件，位于业务路由之前。命中封禁时返回：

```json
{
  "error": {
    "code": "IP_BANNED",
    "message": "该 IP 因异常登录行为已被临时封禁"
  }
}
```

HTTP 状态码为 `403 Forbidden`。封禁范围是整站接口，不仅限于登录接口，也包括网关转发、管理接口和健康检查。

### 2.3 飞书告警

封禁成功后，系统使用 goroutine 异步调用已有的 `FeishuWebhookService`，不会等待飞书返回后再结束登录请求。告警内容包括：

- 被封禁的 IP
- 登录接口路径
- 窗口内失败次数及阈值
- 统计窗口
- 封禁时长
- 触发时间

告警类型为 `login_bruteforce_autoban`，使用红色告警卡片。冷却范围按“告警类型 + IP”区分，避免同一 IP 在冷却期内重复推送。

自动封禁不依赖飞书是否可用：未启用 Webhook、URL 未配置、通知处于冷却期或飞书请求失败时，IP 仍会正常封禁。飞书发送失败只记录服务端日志。

## 3. 配置项

配置位于管理后台的“飞书通知 > 登录安全”区域，并通过系统设置表持久化。

| 配置键 | 默认值 | 含义 |
| --- | ---: | --- |
| `feishu_login_bruteforce_autoban_enabled` | `true` | 自动封禁总开关；虽然键名带 `feishu`，但同时控制封禁本身 |
| `login_bruteforce_max_failures` | `10` | 统计窗口内触发封禁的失败次数 |
| `login_bruteforce_window_minutes` | `5` | 失败计数固定窗口，单位为分钟 |
| `login_bruteforce_ban_minutes` | `60` | 自动封禁时长，单位为分钟 |

> 生产首次部署前必须显式保存 `feishu_login_bruteforce_autoban_enabled=false`。该功能在设置缺失时默认开启，不能在真实客户端 IP链尚未验证时依赖“未配置”等同于关闭。

飞书通知还需满足已有 Webhook 配置：

| 配置键 | 要求 |
| --- | --- |
| `feishu_webhook_enabled` | 必须为 `true` 才发送通知 |
| `feishu_webhook_url` | 必须配置有效的飞书机器人 Webhook URL |
| `feishu_webhook_cooldown_minutes` | 控制相同告警类型和 IP 的通知冷却时间，默认 30 分钟 |
| `feishu_webhook_at_all` | 可选，沿用已有的 @所有人设置 |
| `feishu_webhook_at_user_ids` | 可选，沿用已有的指定成员 open_id 设置 |

后端对缺失、非数字或非正数的阈值配置使用默认值。前端输入框给出了范围提示，但当前后端没有设置相同的最大值约束，运维时应使用合理的正整数。

## 4. Redis 数据

该功能不新增数据库表，失败计数和当前封禁状态均保存在 Redis。

### 4.1 失败计数

```text
Key:   login_bruteforce:fail:<IP>
Value: 窗口内累计失败次数
TTL:   login_bruteforce_window_minutes
```

TTL 仅在第一次失败时设置；窗口到期后 Redis 自动删除计数。

### 4.2 封禁记录

```text
Key: banned_ip:<IP>
TTL: login_bruteforce_ban_minutes
```

Value 为 JSON：

```json
{
  "reason": "login_bruteforce",
  "failures": 10,
  "banned_at": "2026-07-17T16:00:00+08:00"
}
```

当前实现只保存“正在生效的封禁”，TTL 到期后记录自动消失，不保留历史封禁审计。

## 5. 管理接口

以下接口位于管理员路由组，需要管理员身份认证。

### 5.1 查看当前封禁 IP

```http
GET /api/v1/admin/security/banned-ips
```

返回项包含：

```json
{
  "ip": "111.229.153.119",
  "reason": "login_bruteforce",
  "failures": 10,
  "banned_at": "2026-07-17T16:00:00+08:00",
  "expires_in_seconds": 3599
}
```

接口通过 Redis `SCAN` 枚举 `banned_ip:*`，不会阻塞 Redis 执行全量 `KEYS` 查询。当前没有对应的封禁列表管理页面，只提供后端接口。

### 5.2 手动解封

```http
DELETE /api/v1/admin/security/banned-ips/:ip
```

成功响应中返回被解封的 IP。当前处理器只检查路径参数非空，没有执行 `net.ParseIP` 格式校验。

由于封禁检查位于全局路由最前面，如果管理员与被封禁用户共用同一个出口 IP，管理员也无法从该 IP 调用解封接口。此时需从服务器直接操作 Redis：

```bash
docker compose exec redis redis-cli DEL 'banned_ip:111.229.153.119'
```

如 Redis 配置了密码，应按部署配置补充认证参数。执行前应先确认目标 IP，避免删除错误记录。

## 6. 真实客户端 IP 依赖

上游 `v0.1.159` 的提交 `7c48f9a85` 统一了会话绑定、审计日志和 API Key ACL 的安全客户端 IP来源。本功能在合并后也改为使用 `middleware.SecurityClientIP()`，因此四条安全链路读取同一个 IP。

`SecurityClientIP()` 优先读取全局 `SessionBindingContext` 注入的结果，其行为受系统设置控制：

| 配置键 | 默认值 | 行为 |
| --- | ---: | --- |
| `api_key_acl_trust_forwarded_ip` | `false` | `false` 时使用 Gin `trusted_proxies`；`true` 时信任 `CF-Connecting-IP`、`X-Real-IP`、`X-Forwarded-For` |

Cloudflare 场景下应同时满足：

1. sub2api 只绑定 `127.0.0.1:8080`，不能保留 Docker 公网发布端口。
2. 宿主机 Caddy 的 80/443 只允许 Cloudflare 官方网段访问。
3. Caddy 保留或规范化 Cloudflare 提供的真实客户端 IP头。
4. 完成源站隔离后，才可启用 `api_key_acl_trust_forwarded_ip`。
5. 审计日志、会话绑定和自动封禁观察到的客户端 IP必须一致。

如果未正确处理代理链，多个用户可能被统计到 Cloudflare/Caddy 的出口 IP 上，造成批量误封。如果在源站仍可直连时开启转发 IP信任，攻击者则可能伪造请求头绕过计数或嫁祸其他 IP。

因此，在完成源站隔离和安全客户端 IP验证前，不应在生产环境启用该功能。完整的入口切换顺序见 [登录爆破绕过封禁与会话绑定异常处置文档](INCIDENT_2026-07-17_LOGIN_BRUTEFORCE_AND_SESSION_BINDING.md)。

生产入口改造已完成：sub2api 仅绑定 `127.0.0.1:8080`，Caddy 使用 Origin Certificate 接管 80/443，UFW 仅允许 Cloudflare 网段访问 80/443，Cloudflare 已切换 Full (strict)。从外部直连裸 IP和灰云域名的 80/443 均超时，灰云 VPN 8443 正常。自动封禁仍需等待生产镜像版本和真实客户端 IP链路验证后再启用。

### 6.1 与灰云 VPN 的关系

生产环境的 `proxy.linshisan.cc` 保持 DNS-only 灰云，但 VPN 客户端使用独立的 `8443` 端口和独立容器。本功能只运行在 sub2api HTTP 服务中，不会修改 UFW、Cloudflare 或 VPN 容器，也不会封禁 `proxy.linshisan.cc:8443` 的 VPN 连接。

自动封禁命中后会阻止该 IP访问 sub2api 的全站 HTTP 接口，但不会影响独立的 VPN 8443 传输。

## 7. 故障策略与边界

- Redis 客户端为空时，计数、封禁检查和解封均按无操作处理。
- Redis 计数失败时不改变登录响应，当前请求按原结果返回。
- 全局封禁查询失败时 fail-open，请求继续进入业务路由。
- 系统设置读取失败时记录警告，并使用默认阈值和默认开启状态。
- 飞书告警异步、best-effort，并带 panic recovery；失败不回滚封禁。
- 飞书冷却占位在实际 HTTP 发送前获取，因此一次发送失败后，后续同类告警仍可能在冷却期内被抑制。
- 计数维度只有 IP，不区分邮箱、账号或 User-Agent。NAT、公司网络和移动运营商出口可能有多人共用同一 IP。
- 当前不记录成功登录，也不会在登录成功后主动清除该 IP 已累计的失败次数；计数只依靠窗口 TTL 到期或触发封禁后清除。
- 当前不提供永久黑名单、白名单、分级封禁、跨 Redis 的持久化历史或自动写入 UFW/Cloudflare WAF。

## 8. 验证方法

### 8.1 自动化测试

服务层测试覆盖以下场景：

- 恰好达到阈值时封禁
- 功能关闭时不计数、不封禁
- 设置缺失时应用默认值
- 统计窗口到期后计数重置
- 手动解封
- 枚举多个当前封禁 IP
- Redis 客户端为空时安全降级
- 上游统一安全客户端 IP上下文与封禁中间件兼容
- 客户端请求 context 已取消时，401 失败仍会被计数并触发封禁

运行测试：

```bash
cd backend
go test ./internal/service -run LoginBruteforce
go test ./internal/server/middleware
go test ./internal/server/routes -run 'Auth|Security'
```

### 8.2 生产前验证

建议使用受控测试 IP，并临时把阈值调低：

当前进度：第 1 项的入口隔离已完成；其余项目需要在部署自动封禁版本后执行。

1. 确认 sub2api 只绑定 `127.0.0.1:8080`，裸 IP的 80/443 无法访问。
2. 确认审计日志中的测试请求 IP 是真实客户端 IP。
3. 确认会话绑定、审计日志和自动封禁使用相同的安全客户端 IP来源。
4. 连续提交错误密码，确认阈值前仍返回原有 401。
5. 达到阈值后再次访问任意 sub2api 接口，确认返回 403 `IP_BANNED`。
6. 确认 Redis 中存在 `banned_ip:<测试IP>` 且 TTL 正确。
7. 确认飞书收到一次“登录爆破自动封禁”卡片。
8. 确认同一台客户端的 `proxy.linshisan.cc:8443` VPN 连接不受影响。
9. 调用管理员解封接口，确认该 IP 恢复访问。
10. 模拟飞书不可用，确认封禁仍然生效。
11. 模拟 Redis 不可用，确认请求 fail-open 且服务没有全站误封。

## 9. 紧急停用与回滚

在管理后台关闭“启用登录爆破自动封禁”，或把以下设置更新为 `false`：

```text
feishu_login_bruteforce_autoban_enabled=false
```

关闭开关后不会再累计新的登录失败或产生新的自动封禁，但已经存在的 `banned_ip:*` 记录仍会持续到 TTL 到期。需要立即解除全部现有封禁时，应先通过管理接口逐条确认和解封；紧急情况下可在 Redis 中扫描后精确删除对应键。

关闭飞书总开关只会停止通知，不会停止自动封禁。

## 10. 主要实现文件

| 文件 | 职责 |
| --- | --- |
| `backend/internal/server/middleware/login_bruteforce.go` | 登录失败追踪与全局封禁检查中间件 |
| `backend/internal/server/middleware/session_binding.go` | 统一安全客户端 IP上下文与 `SecurityClientIP()` |
| `backend/internal/service/login_bruteforce_service.go` | 设置加载、Redis 计数、封禁、解封、列表和告警触发 |
| `backend/internal/handler/admin/security_handler.go` | 当前封禁列表与手动解封接口 |
| `backend/internal/service/feishu_webhook_service.go` | 飞书告警开关、冷却和卡片发送 |
| `backend/internal/server/router.go` | 全局封禁中间件注册 |
| `backend/internal/server/routes/auth.go` | 登录失败追踪中间件注册 |
| `backend/internal/server/routes/admin.go` | 安全管理接口注册 |
| `backend/internal/service/domain_constants.go` | 设置键定义 |
| `frontend/src/views/admin/components/SettingTabFeishu.vue` | 登录安全配置界面 |
| `backend/internal/service/login_bruteforce_service_test.go` | 服务层自动化测试 |
