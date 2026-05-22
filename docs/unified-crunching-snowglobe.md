# 修复 Ops 告警恢复通知丢失 & 低流量误报

## 背景

通过分析一次 15:58:58 触发、16:00 恢复的 P0 `error_rate` 告警，发现两个独立问题：

1. **恢复通知静默丢失**：`postCard()` 无重试、不读飞书响应 body（无法检测 HTTP 200 中的业务错误），`maybeSendAlertResolvedFeishu` 在 nil 检查失败时静默跳过。
2. **低流量误报**：1 分钟窗口内只有 1 个 429 请求，`error_rate = 100%` 触发 P0。评估器缺少最小请求数保护。

## 变更内容

### Part A：恢复通知可靠性（P1）

**A1. `feishu_webhook_service.go` — `postCard` 重试 + 读取响应 body**

新增常量和 `feishuErrorResponse` 结构体，将 `postCard`（原第 485 行）重写为重试循环：
- 最多 3 次，指数退避（基础 1s，每次翻倍，上限 8s），加 0~+20% 随机抖动
- 读取完整响应 body，解析为 `{"code": int, "msg": string}`
- `code != 0` 视为飞书业务错误，触发重试
- 重试时打 WARN 日志，最终失败返回包装后的 error
- 函数签名不变，所有调用方自动受益

**A2. `ops_alert_evaluator_service.go` — `maybeSendAlertResolvedFeishu` nil 检查日志（第 755 行）**

nil 检查失败时补充 `logger.LegacyPrintf("[OpsAlertEvaluator] feishu resolved skipped: nil dependency")`，与 `maybeSendAlertFeishu`（第 737 行）的模式保持一致。

**A3. `ops_alert_evaluator_service.go` — 无数据路径的错误日志（第 234 行）**

将 `if ae, aeErr := ...; aeErr == nil && ae != nil` 拆分为独立错误检查并补充日志，与第 249 行未触发路径的处理模式保持一致。

### Part B：最小请求数保护（P2）

**B1. `ops_alert_models.go` — 添加 `MinRequests` 字段**

```go
MinRequests int `json:"min_requests"`
```
插入 `OpsAlertRule` 结构体 `CooldownMinutes` 之后（第 30 行）。默认值 0 表示不设下限。

**B2. 数据库迁移 — `140_ops_alert_rules_min_requests.sql`**

```sql
ALTER TABLE ops_alert_rules ADD COLUMN IF NOT EXISTS min_requests INT NOT NULL DEFAULT 0;
```

**B3. `ops_repo_alerts.go` — 更新 3 个 CRUD 函数**

- `ListAlertRules`（第 19 行）：SELECT 和 Scan 中加入 `min_requests`
- `CreateAlertRule`（第 89 行）：INSERT 列、VALUES、RETURNING、QueryRowContext 参数、Scan 中均加入 `min_requests`
- `UpdateAlertRule`（第 192 行）：SET 子句、RETURNING、QueryRowContext 参数、Scan 中均加入 `min_requests`
- 三处均插在 `cooldown_minutes` 之后、`notify_email` 之前

**B4. `ops_alert_evaluator_service.go` — `computeRuleMetric` 中添加保护（第 582-596 行）**

针对 `success_rate`、`error_rate`、`upstream_error_rate` 三个 case：
```go
if rule.MinRequests > 0 && overview.RequestCountSLA < int64(rule.MinRequests) {
    return 0, false  // 视为"数据不足"
}
```
放在已有的 `RequestCountSLA <= 0` 检查之后。`MinRequests == 0` 时行为与原来完全相同。

**B5. `ops_alerts_handler.go` — Handler 校验与字段绑定**

- `opsAlertRuleValidatedInput` 结构体（第 63 行）：新增 `MinRequests` 和 `MinRequestsProvided` 字段
- `validateOpsAlertRulePayload`（第 102 行）：解析 `min_requests`（整数，>= 0）
- `CreateAlertRule`（第 296 行）：赋值 `rule.MinRequests = validated.MinRequests`
- `UpdateAlertRule`（第 350 行）：赋值 `rule.MinRequests = validated.MinRequests`

**B6. 前端 — 类型、表单、国际化**

- `ops.ts`（第 691 行）：`AlertRule` 接口新增 `min_requests: number`
- `OpsAlertRulesCard.vue`（第 275 行）：`newRuleDraft()` 新增 `min_requests: 0`；在 cooldown 区块后新增输入框及国际化 key
- `zh.ts` / `en.ts`：新增 `form.minRequests` 和 `hints.minRequests` 翻译

## 审查发现的问题

### BUG-1（已修复，严重）：`CreateAlertRule` VALUES 参数编号错误

**文件：** `backend/internal/repository/ops_repo_alerts.go:122`

添加 `min_requests` 时，VALUES 从 `$1..$12,NOW(),NOW()` 改成了 `$1..$12,$13,$14,NOW(),NOW()`，
多出了一个 `$14` 占位符，但 QueryRowContext 只传入了 13 个参数。
PostgreSQL 会报 "there is no parameter $14"，导致 `CreateAlertRule` 接口调用即失败。

> `UpdateAlertRule` 的 `$14` 是正确的（id 额外占 $1，总参数 = 1 + 13 = 14 个）。

**修复：** 将 VALUES 改为 `$1,$2,...,$13,NOW(),NOW()`（已完成）

### BUG-2（已记录，轻微）：postCard jitter 描述与实现不一致

原计划描述 "+/-20% jitter"，实际实现为 `+rand.Int63n(delay/5)`（0~+20%，只加不减）。
不影响功能，保持现有实现。

## 验证方案

1. **postCard 重试**：mock 飞书端点，第一次返回 HTTP 200 + `code: 19021`，第二次返回成功——验证重试和退避时机。
2. **MinRequests 保护**：配置 `min_requests: 5` 的 error_rate 规则，发送 1 个 429——验证告警不触发；再发送 5 个错误——验证告警触发。
3. **向后兼容**：数据库中已有规则（无 `min_requests` 列值）默认为 0——验证行为与修改前相同。
4. **nil 检查日志**：强制 `feishuWebhook = nil`——验证日志中出现 `"feishu resolved skipped: nil dependency"`。
5. **无数据路径错误日志**：模拟 `GetActiveAlertEvent` 返回 DB 错误——验证无数据路径中错误被记录。

## 修改文件汇总

| 文件 | 变更内容 |
|------|---------|
| `backend/internal/service/feishu_webhook_service.go` | postCard 重试 + 读取响应 body |
| `backend/internal/service/ops_alert_evaluator_service.go` | nil 检查日志、无数据路径日志、MinRequests 保护 |
| `backend/internal/service/ops_alert_models.go` | MinRequests 字段 |
| `backend/migrations/140_ops_alert_rules_min_requests.sql` | 新建：ALTER TABLE |
| `backend/internal/repository/ops_repo_alerts.go` | CRUD + min_requests 列（**BUG-1 修复**） |
| `backend/internal/handler/admin/ops_alerts_handler.go` | 校验 + 字段绑定 |
| `frontend/src/api/admin/ops.ts` | AlertRule 类型 |
| `frontend/src/views/admin/ops/components/OpsAlertRulesCard.vue` | 表单字段 |
| `frontend/src/i18n/locales/zh.ts` | 国际化 |
| `frontend/src/i18n/locales/en.ts` | 国际化 |
