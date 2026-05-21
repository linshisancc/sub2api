# 渠道模型白名单配置

通过渠道（Channel）的 `restrict_models` + `model_pricing` 机制，实现模型白名单拦截：只允许指定的模型通过，未授权的模型在网关层直接拒绝，不转发到上游。

## 目录

- [背景](#背景)
- [原理](#原理)
- [配置流程（管理后台 UI）](#配置流程管理后台-ui)
- [配置流程（API）](#配置流程-api)
- [验证](#验证)
- [计费说明](#计费说明)
- [支持的模型列表](#支持的模型列表)
- [渠道平台功能开关](#渠道平台功能开关)
- [常见问题](#常见问题)

---

## 背景

sub2api 作为 API 代理网关，默认对所有模型名不做校验。如果客户端请求了一个上游平台不支持的模型（如向 Anthropic 发 `deepseek-v4-pro`），请求会被透传到上游，返回 404 错误。

这种请求应该在网关层被拦截，而不是浪费上游资源。

### 错误日志示例

```
gateway.forward_failed method=POST path=/v1/messages
  user=1 acc=1 model=deepseek-v4-pro
  error=upstream error: 404 message=model: deepseek-v4-pro
```

### 优化

模型被白名单拦截时，不再返回 503 `No available accounts`，而是返回 404：

```json
{
  "type": "error",
  "error": {
    "type": "not_found_error",
    "message": "Model not allowed by channel: deepseek-v4-pro"
  }
}
```

---

## 原理

```
请求 POST /v1/messages model=deepseek-v4-pro
  → 鉴权中间件
  → SubscriptionService（配额检查）
  → GatewayService.SelectAccountWithLoadAwareness
    → checkChannelPricingRestriction(groupID, "deepseek-v4-pro")
      → IsModelRestricted()
        → RestrictModels=true && 定价列表找不到该模型
        → RESTRICTED ❌ → 返回 404
```

关键点在 `checkChannelPricingRestriction`（`gateway_service.go`），它在账号调度之前执行：

1. 取分组关联的渠道
2. 如果 `RestrictModels=true`，检查模型是否在 `model_pricing` 列表中
3. 不在列表中 → 返回 `ErrModelRestrictedByChannel`
4. Handler 检测到该错误，返回 404 `not_found_error`

### 相关代码文件

| 文件 | 职责 |
|------|------|
| `internal/service/gateway_service.go` | `checkChannelPricingRestriction` 拦截逻辑、`ErrModelRestrictedByChannel` 错误定义 |
| `internal/service/channel_service.go` | `IsModelRestricted` / `checkRestricted` 白名单检查、缓存管理 |
| `internal/service/channel.go` | `Channel` / `ChannelModelPricing` 数据结构定义 |
| `internal/handler/gateway_handler.go` | `ErrModelRestrictedByChannel` 检测、404 错误响应 |
| `internal/handler/admin/channel_handler.go` | 渠道 CRUD API |
| `frontend/src/views/admin/ChannelsView.vue` | 管理后台 UI |

---

## 配置流程（管理后台 UI）

### 前置条件

已有一个或多个分组（Group），知道分组 ID。可在「设置 → 分组管理」查看。

### 步骤

**1. 进入渠道管理**

导航到：**设置 → 渠道管理**（`/admin/channels`）

**2. 创建渠道**

点击「创建渠道」按钮。

**3. 基础设置 Tab**（基础设置）

| 字段 | 操作 |
|------|------|
| **Name** | 输入渠道名称，如 `模型白名单` |
| **Restrict Models** | ✅ **勾选**（这是白名单的核心开关） |
| **Billing Basis** | 选择 `requested` |
| **平台配置** | 勾选需要启用白名单的平台（如 `anthropic`、`openai`） |

**4. 切换到平台 Tab（如 anthropic）**

**Model Mapping（模型映射）：** 不需要配置，留空即可。

**Model Pricing（模型定价）：** 点击「+ Add」添加一条定价规则：

| 字段 | 操作 |
|------|------|
| **Models** | 输入 `claude-*`（通配符匹配所有 Claude 模型） |
| **Billing Mode** | 选择 `token` |
| **Input Price / Output Price / Cache Write Price / Cache Read Price** | **全部留空不填** |

这样就会放行所有 `claude-` 开头的模型，且不影响计费。

**5. 切换到 openai Tab**

同样操作，添加定价规则：
- **Models**：`gpt-*`
- **Billing Mode**：`token`
- **所有价格留空**

**6. 保存**

点击保存按钮，渠道创建完成。

### 后续管理

| 操作 | 方式 |
|------|------|
| 编辑渠道 | 渠道列表点击编辑按钮，修改后保存 |
| 启用/停用 | 列表中的 Toggle 开关直接切换 |
| 删除渠道 | 编辑页面底部删除按钮 |
| 添加更多平台 | 编辑渠道，在基础设置 Tab 勾选新增平台后再切换到对应 Tab 配置 |

---

## 配置流程（API）

### 创建渠道

```http
POST /api/v1/admin/channels
Content-Type: application/json

{
  "name": "模型白名单",
  "group_ids": [1, 2],
  "restrict_models": true,
  "billing_model_source": "requested",
  "model_mapping": {},
  "model_pricing": [
    {
      "platform": "anthropic",
      "models": ["claude-*"],
      "billing_mode": "token"
    },
    {
      "platform": "openai",
      "models": ["gpt-*"],
      "billing_mode": "token"
    }
  ]
}
```

响应示例：

```json
{
  "id": 1
}
```

### 查看渠道列表

```http
GET /api/v1/admin/channels
```

### 修改渠道

```http
PUT /api/v1/admin/channels/1
Content-Type: application/json

{
  "model_pricing": [
    {
      "platform": "anthropic",
      "models": ["claude-*"],
      "billing_mode": "token"
    }
  ]
}
```

### 删除渠道

```http
DELETE /api/v1/admin/channels/1
```

---

## 验证

### 被拦截的请求

```http
POST /v1/messages
Authorization: Bearer <your-api-key>
Content-Type: application/json

{"model": "deepseek-v4-pro", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}]}
```

响应：

```json
{
  "type": "error",
  "error": {
    "type": "not_found_error",
    "message": "Model not allowed by channel: deepseek-v4-pro"
  }
}
```

状态码：**404 Not Found**

### 放行的请求

```http
POST /v1/messages
Authorization: Bearer <your-api-key>
Content-Type: application/json

{"model": "claude-sonnet-4-6", "max_tokens": 100, "messages": [{"role": "user", "content": "hi"}]}
```

响应：正常代理响应（2xx）。

### 日志

被拦截的请求会在日志中记录：

```
gateway.model_restricted_by_channel model=deepseek-v4-pro group_id=1 platform=anthropic
```

---

## 计费说明

由于 `model_pricing` 条目中没有填写任何价格字段（全部为 null），计费链路会跳过渠道覆盖，使用系统内置的默认定价：

```
渠道 model_pricing 中 input_price = nil
  → GetModelPricingWithChannel()
    → 先取默认定价（fallbackPrices 中的正确价格）
    → if channelPricing.InputPrice != nil → false → 不覆盖
    → 返回默认定价 ✅
```

所有价格字段在数据库和 Go 代码中均为 `*float64`（可空指针），不填即为 nil，不影响现有计费。

### 如果填了价格会怎样？

配了价格就会覆盖默认定价，影响实际扣费。如果目的是纯白名单，所有价格留空即可。

---

## 支持的模型列表

### Anthropic / Claude

| 请求可用名 | 上游最终模型 |
|---|---|
| `claude-sonnet-4-5` | `claude-sonnet-4-5-20250929` |
| `claude-sonnet-4-5-20250929` | `claude-sonnet-4-5-20250929` |
| `claude-sonnet-4-6` | `claude-sonnet-4-6` |
| `claude-opus-4-5` | `claude-opus-4-5-20251101` |
| `claude-opus-4-5-20251101` | `claude-opus-4-5-20251101` |
| `claude-opus-4-6` | `claude-opus-4-6` |
| `claude-opus-4-7` | `claude-opus-4-7` |
| `claude-haiku-4-5` | `claude-haiku-4-5-20251001` |
| `claude-haiku-4-5-20251001` | `claude-haiku-4-5-20251001` |

通配符：`claude-*` 匹配所有以上模型。

### OpenAI

| 模型 ID |
|---|
| `gpt-5.5` |
| `gpt-5.4` |
| `gpt-5.4-mini` |
| `gpt-5.3-codex` |
| `gpt-5.3-codex-spark` |
| `gpt-5.2` |
| `gpt-image-1` |
| `gpt-image-1.5` |
| `gpt-image-2` |

通配符：`gpt-*` 匹配所有以上模型（包括 `gpt-image-*` 系列）。

---

## 渠道平台功能开关

在渠道编辑页面的各平台 Tab 中，有三个独立于白名单的功能开关。它们不影响模型白名单逻辑，而是针对特定平台提供额外的请求处理能力。

### Web Search 模拟（Anthropic）

| 字段 | 值 |
|------|-----|
| 适用平台 | Anthropic |
| 前置条件 | 需先在 **设置 → 网关** 中开启全局 **Web Search 模拟** 开关 |

开启后，该渠道下所有 Anthropic 分组的请求中，如果客户端请求使用了 Claude 的 `web_search` 工具，网关会**拦截请求并在本地完成搜索**，而不是转发到上游 Anthropic API。

```
请求中包含 web_search 工具调用
  → GatewayService.shouldEmulateWebSearch()
    → 渠道启用了 web_search_emulation
    → 直接调用搜索 API 构造响应
    → 不转发到上游
```

**注意**：这是一个有风险的配置，开启后会对所有关联分组的 web_search 请求生效，请谨慎操作。

---

### Codex 图片生成桥接（OpenAI）

| 字段 | 值 |
|------|-----|
| 适用平台 | OpenAI |
| 代码文件 | `openai_codex_transform.go`、`codex_image_generation_bridge.go` |

开启后，当 OpenAI 分组的 Codex CLI 客户端发送 `/v1/responses` 请求时，网关会自动注入两样内容：

**1. 注入 `image_generation` 工具**

自动在请求的 `tools` 数组中添加：

```json
{
  "type": "image_generation",
  "output_format": "png"
}
```

**2. 注入桥接指令**

在 `instructions` 字段追加一段提示，告诉模型使用请求中原生的 `image_generation` 工具而非本地 `image_gen` 命名空间，避免 Codex CLI 因缺少本地 `image_gen` 工具而误报环境不支持图片生成。

**生效链路**

```
Codex CLI /v1/responses 请求
  → 识别 User-Agent 判定是 Codex CLI
  → 检查 isCodexImageGenerationBridgeEnabled()
    → 全局配置 → 渠道配置 → 账号配置（三级覆盖，任一启用了即生效）
  → 自动注入 image_generation tool
  → 自动注入桥接 instructions
  → 转发到上游 OpenAI
```

**三层级覆盖策略**

| 层级 | 配置位置 |
|------|---------|
| 全局 | `gateway.codex_image_generation_bridge_enabled`（config，默认 false） |
| 渠道（本开关） | 渠道表单 OpenAI Tab → Codex Image Generation Bridge |
| 账号 | 账号编辑 → Codex image-generation bridge（inherit / enabled / disabled） |

渠道开关开启后，关联该渠道的分组下的所有 Codex 请求都会自动注入桥接。留空则跟随全局或账号配置。

**注意**：仅在路由到支持图片生成的 OpenAI 账号时才应开启。如果账号不支持图片生成（如纯文本模型），开启后模型可能会尝试调用不存在的图片功能导致错误。

---

### Bedrock CC 兼容（Anthropic / Bedrock）

| 字段 | 值 |
|------|-----|
| 适用平台 | Anthropic（路由到 AWS Bedrock 的账号） |
| 代码文件 | `bedrock_request.go` |

开启后，当请求被路由到 Bedrock 账号时，网关会在转发前对请求体做兼容处理，解决 Claude Code 客户端通过 Bedrock 访问时的两个常见问题：

**1. Thinking 类型转换**

```
Opus 4.7+: thinking.type = "enabled" → "adaptive"，删除 budget_tokens
其他模型: thinking.type = "enabled" 缺少 budget_tokens → 补充默认值 (10000)
```

Bedrock 上 Opus 4.7+ 仅支持 `adaptive` 模式（`enabled` 模式不被 Bedrock 支持），同时**不支持** `budget_tokens` 参数；其他模型则要求 `enabled` 模式必须附带 `budget_tokens`。

**2. Tool Use ID 清理**

清理 `messages` 中 `tool_use.id` 和 `tool_result.tool_use_id` 的非法字符。Bedrock 要求 ID 只匹配 `^[a-zA-Z0-9_-]+$`，如果客户端生成了含有特殊字符的 ID，Bedrock 会拒绝请求。

**生效链路**

```
请求经过 Forward()
  → 已选定 Bedrock 账号
  → isBedrockCCCompatEnabled() 检查渠道配置
  → sanitizeBedrockThinking()   → 修复 thinking 类型
  → sanitizeBedrockToolUseIDs() → 清理 tool_use ID 字符
  → 继续进入 forwardBedrock() 转发到 AWS Bedrock
```

**注意**：这是**只影响请求体**的预处理，不会改变后续的透传/Bedrock 转发路径。

---

## 常见问题

### 渠道对一个分组生效？

一个渠道可以通过 `group_ids` 关联多个分组，一个分组只能关联一个渠道。

### 通配符如何匹配？

仅支持末尾 `*` 后缀通配。`claude-*` 匹配所有以 `claude-` 开头的模型名，按配置顺序先匹配先命中。

### 如果把所有模型都拦了怎么办？

在渠道编辑页面取消勾选 **Restrict Models**，保存后立即恢复。已配的定价数据不会丢失，重新勾上即可恢复。

### 新增模型时需要改配置吗？

如果用的是通配符（`claude-*` / `gpt-*`），新增模型自动匹配，不需要改配置。如果是逐条列出的精确模型名，需要手动添加。

### 渠道配置多久生效？

渠道创建或修改后，缓存会在几秒内自动重建，之后的新请求立即生效。

### 其他端点也受白名单控制吗？

白名单检查位于 `SelectAccountWithLoadAwareness` 和 `SelectAccountForModelWithExclusions` 中，所有通过这两个函数选择账号的端点都会受控：
- `POST /v1/messages`
- `POST /v1/messages/count_tokens`
- `POST /v1/chat/completions`（OpenAI 兼容路由）
- Gemini 相关路由
