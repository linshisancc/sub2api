# sub2api 核心架构分析

本文档全面描述 sub2api 的系统架构、核心功能模块及关键数据流程。

---

## 目录

- [系统定位](#系统定位)
- [整体架构](#整体架构)
- [请求代理完整流程](#请求代理完整流程)
- [账号与分组数据模型](#账号与分组数据模型)
- [计费链路](#计费链路)
- [账号调度策略](#账号调度策略)
- [订阅配额系统](#订阅配额系统)
- [通知告警系统](#通知告警系统)
- [功能全景总结](#功能全景总结)

---

## 系统定位

sub2api 是一个**订阅转 API 代理网关**。它将上游 AI 服务商（Anthropic、OpenAI、Gemini 等）的订阅账号额度汇集管理，通过统一的 API Key 体系对外分发，实现多租户计费、配额隔离与智能调度。

**技术栈**

| 层次 | 技术 |
|------|------|
| 后端框架 | Go 1.25 + Gin（含 h2c HTTP/2 支持） |
| 数据库 | PostgreSQL 15+（Ent ORM） |
| 缓存 | Redis 7+（鉴权缓存、速率限制、计费缓存） |
| 前端 | Vue 3.4 + TypeScript（构建产物内嵌至二进制） |
| 入口 | `backend/cmd/server/main.go` |

---

## 整体架构

```mermaid
graph TB
    subgraph 客户端
        U[用户 / 开发者]
    end

    subgraph 网关层
        GW[API Gateway<br/>/v1/* /v1beta/* /responses]
        AUTH[API Key 鉴权中间件]
    end

    subgraph 业务核心
        GS[GatewayService<br/>账号选择 & 请求转发]
        BS[BillingService<br/>费用计算]
        US[UsageService<br/>用量记录 & 扣费]
        SS[SubscriptionService<br/>订阅配额检查]
    end

    subgraph 上游平台
        ANT[Anthropic / Claude]
        OAI[OpenAI / GPT]
        GEM[Gemini]
        AG[Antigravity]
    end

    subgraph 数据层
        PG[(PostgreSQL)]
        RD[(Redis)]
    end

    subgraph 通知
        EMAIL[邮件告警]
        FEISHU[飞书 Webhook]
    end

    U -->|Bearer Token| GW
    GW --> AUTH
    AUTH --> SS
    SS --> GS
    GS -->|转发请求| ANT & OAI & GEM & AG
    ANT & OAI & GEM & AG -->|响应| GS
    GS --> BS --> US
    US --> PG
    US -->|余额/配额告警| EMAIL & FEISHU
    AUTH & SS -.->|缓存| RD
```

---

## 请求代理完整流程

```mermaid
sequenceDiagram
    participant C as 客户端
    participant MW as 鉴权中间件
    participant SS as SubscriptionService
    participant GS as GatewayService
    participant UP as 上游 AI
    participant BS as BillingService
    participant US as UsageService
    participant DB as PostgreSQL
    participant RD as Redis

    C->>MW: POST /v1/messages<br/>Authorization: Bearer <key>
    MW->>RD: 查 key 缓存
    alt 缓存命中
        RD-->>MW: APIKey 对象
    else 缓存未命中
        MW->>DB: SELECT api_keys WHERE key=?
        DB-->>MW: APIKey 记录
        MW->>RD: 写入缓存
    end
    MW->>MW: 校验状态 / IP / 配额 / 过期
    MW->>SS: 检查订阅配额（daily/weekly/monthly）
    SS-->>MW: 通过 or ErrLimitExceeded

    MW->>GS: SelectAccount(groupID, platform)
    GS->>RD: 查 sticky session
    alt 有粘性会话
        RD-->>GS: accountID
    else 重新选择
        GS->>DB: 查分组内活跃账号
        GS->>GS: 按 load_factor + concurrency 排序选最优
        GS->>RD: 写 sticky session（TTL 1h）
    end

    GS->>UP: 转发原始请求
    UP-->>GS: SSE 流式 / 普通响应

    GS->>BS: CalculateCostUnified(tokens, model)
    BS->>BS: 计算 input/output/cache 各维度费用
    BS-->>GS: CostBreakdown

    GS->>US: Create(usageLog, actualCost)
    US->>DB: BEGIN TRANSACTION
    US->>DB: INSERT usage_logs
    US->>DB: UPDATE users SET balance -= actualCost
    DB-->>US: COMMIT
    US->>US: 检查余额阈值告警
    US-->>C: 响应透传
```

**支持的代理端点**

| 路径 | 平台 |
|------|------|
| `POST /v1/messages` | Anthropic / Claude |
| `POST /v1/chat/completions` | OpenAI / GPT |
| `POST /v1beta/*` | Gemini |
| `POST /responses` | OpenAI Responses API |
| `POST /images/generations` | OpenAI 图像生成 |
| `POST /antigravity/v1/*` | Antigravity 专属路由 |

---

## 账号与分组数据模型

```mermaid
erDiagram
    USER ||--o{ API_KEY : 创建
    USER ||--o{ USER_SUBSCRIPTION : 订阅
    API_KEY }o--|| GROUP : 绑定
    GROUP ||--o{ ACCOUNT_GROUP : 包含
    ACCOUNT ||--o{ ACCOUNT_GROUP : 归属
    USER_SUBSCRIPTION }o--|| GROUP : 关联
    USAGE_LOG }o--|| USER : 归属
    USAGE_LOG }o--|| API_KEY : 来自
    USAGE_LOG }o--|| ACCOUNT : 使用
    USAGE_LOG }o--|| GROUP : 属于

    USER {
        int id
        string email
        decimal balance
        string status
    }
    API_KEY {
        int id
        string key
        decimal quota
        decimal quota_used
        decimal rate_limit_5h
        decimal rate_limit_1d
        decimal rate_limit_7d
        string[] ip_whitelist
    }
    GROUP {
        int id
        string name
        string platform
        float rate_multiplier
        string subscription_type
        decimal daily_limit_usd
        decimal weekly_limit_usd
    }
    ACCOUNT {
        int id
        string name
        string platform
        string type
        json credentials
        int concurrency
        float load_factor
    }
    USER_SUBSCRIPTION {
        int id
        decimal daily_quota
        decimal daily_used
        decimal weekly_quota
        decimal weekly_used
        date expires_at
    }
    USAGE_LOG {
        bigint id
        int input_tokens
        int output_tokens
        decimal total_cost
        decimal actual_cost
        string model
        int duration_ms
    }
```

**核心关系说明**

- **用户 → API Key**：一对多，每个 Key 可绑定一个分组
- **分组 → 账号**：多对多（通过 `account_groups` 连接表），支持优先级排序
- **用户 → 订阅**：一个用户可在多个分组中持有订阅，各自独立配额
- **请求路径**：`API Key → Group → Account → 上游平台`

**账号类型**

| platform | type | 说明 |
|----------|------|------|
| anthropic | oauth | Claude 订阅 OAuth 账号 |
| anthropic | setup-token | Claude 企业 Setup Token |
| openai | oauth | OpenAI 订阅 OAuth 账号 |
| gemini | oauth / apikey | Google Gemini 账号 |
| antigravity | oauth | Antigravity 账号 |

---

## 计费链路

```mermaid
flowchart LR
    subgraph 原始数据
        TOK[Token 计数<br/>input / output / cache]
        MDL[模型定价表<br/>PricingService]
    end

    subgraph BillingService
        CALC["InputCost  = input_tokens  × price_per_token
OutputCost = output_tokens × price_per_token
CacheCost  = cache_tokens  × cache_price
TotalCost  = Σ above"]
        RATE["ActualCost = TotalCost
× GroupRateMultiplier
× AccountRateMultiplier"]
    end

    subgraph UsageService
        LOG[写入 usage_logs]
        BAL[扣减 user.balance]
        CHK{余额穿越<br/>告警阈值?}
        NOTIFY[触发通知]
    end

    TOK --> CALC
    MDL --> CALC
    CALC --> RATE --> LOG
    LOG --> BAL --> CHK
    CHK -- 是 --> NOTIFY
    CHK -- 否 --> END([完成])
    NOTIFY --> END
```

**费用字段说明**

| 字段 | 含义 |
|------|------|
| `total_cost` | 按标准定价计算的原始费用 |
| `actual_cost` | 经 GroupRateMultiplier × AccountRateMultiplier 后的实际扣费 |
| `account_cost` | 账号侧成本（用于分组统计） |
| `user_cost` | 用户侧计费（含用户级乘数，供用户看板展示） |

---

## 账号调度策略

```mermaid
flowchart TD
    REQ[收到请求] --> STICKY{Redis 粘性会话<br/>存在?}
    STICKY -- 是 --> VALID{账号仍活跃?}
    VALID -- 是 --> USE[使用缓存账号]
    VALID -- 否 --> SELECT
    STICKY -- 否 --> SELECT

    SELECT[查分组内活跃账号列表]
    SELECT --> SORT[按 load_factor + current_concurrency 排序]
    SORT --> PICK[选最低负载账号]
    PICK --> CACHE[写 sticky session TTL=1h]
    CACHE --> USE

    USE --> FWD[转发请求]
    FWD --> ERR{上游出错?}
    ERR -- 否 --> OK[返回响应]
    ERR -- 是 --> RETRY{已切换次数<br/>< 最大限制?}
    RETRY -- 是 --> MARK[标记当前账号不可用]
    MARK --> SELECT
    RETRY -- 否 --> FAIL[返回错误给客户端]
```

**调度参数**

| 参数 | 值 |
|------|-----|
| 粘性会话 TTL | 1 小时 |
| Claude 最大故障转移次数 | 10 次 |
| Gemini 最大故障转移次数 | 3 次 |
| 并发控制字段 | `account.concurrency`（默认 3） |

---

## 订阅配额系统

```mermaid
flowchart LR
    subgraph 配额检查 subscription_service.go
        L1{L1 ristretto<br/>缓存命中?}
        L1 -- 是 --> CHK
        L1 -- 否 --> DB[(PostgreSQL)]
        DB --> L1W[写入缓存] --> CHK
        CHK{检查 daily/weekly/<br/>monthly 已用量}
        CHK -- 超限 --> DENY[返回 ErrLimitExceeded]
        CHK -- 通过 --> ALLOW[放行]
    end

    subgraph 后台维护
        MQ[维护队列]
        MQ --> RESET[重置过期窗口<br/>daily_window / weekly_window]
        MQ --> EXPIRE[标记过期订阅]
    end
```

**配额维度**

| 维度 | 说明 |
|------|------|
| `daily_quota` / `daily_used` | 当日滚动窗口 |
| `weekly_quota` / `weekly_used` | 当周滚动窗口 |
| `monthly_quota` / `monthly_used` | 当月滚动窗口 |

超出任意维度均会拒绝本次请求，返回对应错误（`ErrDailyLimitExceeded` 等）。

---

## 通知告警系统

```mermaid
flowchart TD
    US[UsageService 扣费后] --> B1{用户余额<br/>穿越阈值?}
    US --> B2{账号配额<br/>穿越告警线?}

    B1 -- 是 --> N1[BalanceNotifyService]
    B2 -- 是 --> N2[QuotaNotifyService]

    N1 --> EMAIL1[发邮件给用户<br/>+ 管理员抄送]
    N1 --> FW1{飞书 Webhook<br/>已启用?}
    FW1 -- 是 --> CD1{Redis 冷却期<br/>未到期?}
    CD1 -- 否 --> SEND1[推送飞书消息<br/>写 Redis TTL=cooldown]

    N2 --> EMAIL2[发邮件给管理员]
    N2 --> FW2{飞书 Webhook<br/>已启用?}
    FW2 -- 是 --> CD2{Redis 冷却期<br/>未到期?}
    CD2 -- 否 --> SEND2[推送飞书消息<br/>写 Redis TTL=cooldown]
```

**告警触发条件**

| 告警类型 | 触发条件 | 通知对象 |
|---------|---------|---------|
| 用户余额不足 | `oldBalance >= threshold && newBalance < threshold` | 用户本人 + 管理员 |
| 账号配额超限 | 日/周/总额度触达告警阈值 | 管理员 |

- 冷却以「告警类型 + 实体 ID」为 key，用户余额和账号各自独立计算
- 冷却状态存储于 Redis，服务重启不重置
- 默认冷却时间 30 分钟，可在管理后台「飞书 Webhook」Tab 调整（1–1440 分钟）

---

## 功能全景总结

| 模块 | 核心文件 | 关键能力 |
|------|---------|---------|
| **请求代理** | `handler/gateway_handler.go`<br/>`service/gateway_service.go` | 多平台统一接口、SSE 流式透传 |
| **账号调度** | `service/gateway_service.go:200+` | 粘性会话、负载均衡、自动故障转移 |
| **鉴权** | `middleware/api_key_auth.go` | IP 白/黑名单、配额、过期、Redis 缓存 |
| **订阅配额** | `service/subscription_service.go` | 日/周/月滚动窗口、L1 ristretto 缓存 |
| **计费** | `service/billing_service.go`<br/>`service/usage_service.go` | 多维度 Token 计费、多重乘数、原子扣减 |
| **用量分析** | `service/dashboard_service.go` | 趋势图、模型分布、分组统计 |
| **账号管理** | `handler/admin/account_handler.go` | OAuth/ApiKey 多类型、用量窗口监控 |
| **通知告警** | `service/balance_notify_service.go`<br/>`service/feishu_webhook_service.go` | 邮件 + 飞书双通道、冷却防刷 |
| **管理后台** | `frontend/src/views/admin/` | 全局配置、实时仪表盘、分组账号用量卡片 |
| **路由注册** | `server/routes/gateway.go`<br/>`server/routes/admin.go` | 网关路由、管理 API、用户 API |
