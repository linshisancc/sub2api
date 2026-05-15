# 监控告警（Ops Alerts）接入飞书 Webhook 推送 — 实施计划

## 背景

监控告警模块（Ops Alerts）目前只能通过**邮件**通知：告警策略 `OpsAlertRule` 仅有
`notify_email` 开关，评估命中后由 `ops_alert_evaluator_service.go` 的 `maybeSendAlertEmail`
发邮件。飞书 Webhook 是另一套独立模块，只服务余额/账号类告警，两者互不相通。

本次让监控告警也能推送到飞书。设计取舍：

- **全局一个开关**（不做 per-rule 粒度，不改 `alert_rules` 表）。
- **复用现有飞书 URL**（系统设置里的 `feishu_webhook_url`），不新增独立 URL 配置。
- 飞书推送沿用邮件告警已有的 `MinSeverity` 等级门槛与静默(silence)规则，仅额外多一个
  `feishu_enabled` 开关控制是否推送。

## 改动清单

### 1. `backend/internal/service/ops_settings_models.go`

`OpsEmailAlertConfig` 结构体新增字段：

```go
FeishuEnabled bool `json:"feishu_enabled"`
```

该配置以 JSON blob 存在 settings 表（key `SettingKeyOpsEmailNotificationConfig`），
**无需 DB migration**；默认零值 false。

### 2. `backend/internal/service/feishu_webhook_service.go`

新增方法 `SendOpsAlert(ctx, rule *OpsAlertRule, event *OpsAlertEvent)`：

- 自行加载 `SettingKeyFeishuWebhookEnabled` + `SettingKeyFeishuWebhookURL`，
  检查全局飞书开关与 URL 后直接调用现成的 `s.post(url, title, content)`。
- **不走 `Send()`**：因为 `Send()` 自带 30min Redis 冷却 + per-type 开关，而 Ops 模块
  已有自己的 cooldown/sustained/单活动事件去重，叠加飞书冷却会造成双重门槛、误吞告警。
- 消息格式（与现有 `[Sub2API] ...` 风格一致，前缀由 `post()` 添加）：

  ```
  运维告警（P1）
  规则：API 成功率过低
  指标：success_rate < 95.00（当前 88.30）
  状态：firing
  触发时间：2026-05-15 18:30:00
  说明：<event.Description>
  ```

### 3. `backend/internal/service/ops_alert_evaluator_service.go`

- `OpsAlertEvaluatorService` 结构体新增字段 `feishuWebhook *FeishuWebhookService`。
- `NewOpsAlertEvaluatorService` 新增同名参数并赋值。
- 新增 `maybeSendAlertFeishu(ctx, runtimeCfg, rule, event)`，与 `maybeSendAlertEmail`
  (:645) 平行，门槛复用现有 helper：
  - nil 保护；
  - `emailCfg, _ := s.opsService.GetEmailNotificationConfig(ctx)`，检查
    `emailCfg.Alert.FeishuEnabled`；
  - 复用 `shouldSendOpsAlertEmailByMinSeverity(emailCfg.Alert.MinSeverity, rule.Severity)`；
  - 复用静默判断 `isOpsAlertSilenced(time.Now().UTC(), rule, event, runtimeCfg.Silencing)`；
  - 调 `s.feishuWebhook.SendOpsAlert(ctx, rule, event)`。
- 在事件创建处（:291-295，`maybeSendAlertEmail` 调用旁）并行调用 `maybeSendAlertFeishu`。
- **不新增 `FeishuSent` 去重列**：每个告警事件只触发一次发送，无重试路径，
  `MinSeverity` + 静默已是充分门槛（保持最小改动）。

### 4. `backend/internal/service/wire.go` + `cmd/server/wire_gen.go`

- `ProvideOpsAlertEvaluatorService`（wire.go:262）新增参数 `feishuWebhook *FeishuWebhookService`，
  透传给 `NewOpsAlertEvaluatorService`。
- `wire_gen.go`：`feishuWebhookService` 已在 line ~137 构造，早于
  `opsAlertEvaluatorService`（line ~262），直接在该调用追加 `feishuWebhookService`
  实参即可，无需调整顺序。
- `go generate` 因 go.sum 缺条目无法运行，沿用手动同步 `wire_gen.go` 的方式。

### 5. 前端

- `frontend/src/views/admin/ops/types.ts`：告警通知配置类型新增可选
  `feishu_enabled?: boolean`。
- `frontend/src/views/admin/ops/components/OpsEmailNotificationCard.vue`：告警卡片
  新增「同时推送到飞书」勾选框，附说明文案（复用「系统设置 → 飞书 Webhook」中配置的
  URL；需先在那里启用飞书 Webhook 并填 URL）。
- 如 `frontend/src/api/admin/ops.ts` 有显式请求/响应类型，同步加字段。

## 验证

1. 系统设置 → 飞书 Webhook：`feishu_webhook_enabled=true` 且填好 `feishu_webhook_url`。
2. 监控 → 告警通知配置：`Alert.Enabled=true`、勾选「同时推送到飞书」
   (`feishu_enabled=true`)、设置 `MinSeverity`。
3. 建一条易触发的告警策略（如 `success_rate < 100`），等评估周期命中，
   预期飞书群收到 `[Sub2API] 运维告警（…）` 消息，且邮件照常发送（两者并行、互不影响）。
4. 等级门槛：把规则 severity 调到低于 `MinSeverity`，预期不推送。
5. 静默：对该规则建静默条目，预期不推送。
6. 关闭 `feishu_enabled`，预期只发邮件不推飞书。
7. `cd backend && go build ./...`（wire_gen.go 手动同步后）。
