# 定时账号 Warmup（窗口预热）

本文档说明「定时账号 Warmup」功能的使用方式、配置方法、判定规则与实现细节。

---

## 目录

- [背景与目标](#背景与目标)
- [功能概览](#功能概览)
- [配置入口](#配置入口)
- [工作日 / 节假日判定](#工作日--节假日判定)
- [Runner 执行流程](#runner-执行流程)
- [手动触发](#手动触发)
- [飞书汇总卡片](#飞书汇总卡片)
- [多副本部署与分布式锁](#多副本部署与分布式锁)
- [与其他模块的边界](#与其他模块的边界)
- [设置键参考](#设置键参考)
- [关键文件](#关键文件)
- [常见问题](#常见问题)

---

## 背景与目标

Anthropic / OpenAI / Gemini / Antigravity / Grok 等上游服务商对每个账号采用「**首次请求时刻 + N
小时**」的滚动窗口限流（Anthropic 5h / 7d 最典型）。如果工作日早上 8 点之前没有任何
请求打到账号，那么当天的 5h 窗口起点会被「第一个用户请求」决定 —— 比如 10:30 的第一
个请求会让窗口锁定到 10:30–15:30，一天里只能用满一个完整窗口外加一段截断的下午窗口。

**定时账号 Warmup** 解决这个问题：在工作日 08:00（可配置）由系统主动给每个可调度账号
发起一条极小的请求，把第一个 5h 窗口的起点钉在 08:00；之后系统会在 8 小时覆盖范围内
按 5 小时间隔自动续触发后续窗口，无需等待客户端真实请求。这样工作日就能用满

- **08:00 – 13:00**（早上窗口）
- **13:00 – 18:00**（下午窗口）

两个完整 5h 窗口，覆盖正常工作时间。

> 探针请求复用「账号管理 → 测试连接」的同一条 SSE 链路（`AccountTestService.RunTestBackground`），
> 每次仅一次真实模型请求，prompt 极短（`"hi"`），实际 token 消耗取决于上游平台与测试链路，通常较小但不承诺严格固定；请求成功后足以让上游开启 5h 窗口。

---

## 功能概览

| 维度 | 行为 |
|------|------|
| **触发时间** | 默认每天 08:00 起，在 8 小时覆盖范围内每 5 小时自动触发一次后续窗口；通过 5 段 cron 表达式可配置首个窗口锚点（如 `15 7 * * *` = 每天 07:15） |
| **触发条件** | `enabled=true` + 窗口边界命中 + （可选）当日为工作日 + 当前窗口未执行过 |
| **覆盖账号** | 所有 `schedulable=true` 且未被限流 / 过期 / 临时不可调度的账号，按平台白名单过滤 |
| **并发度** | 最多 10 个账号并行探针（信号量） |
| **幂等** | 同一个 5h 窗口只执行一次；08:00 已执行不会阻挡 13:00 后续窗口；8 小时覆盖范围外的 18:00 / 23:00 不会触发 |
| **失败处理** | 单账号失败不影响其他账号；全部失败 / 部分失败都进入同一张飞书汇总卡片 |
| **通知** | 整次任务执行结束推送 **一张** 飞书卡片；不对单账号单独推送 |
| **多副本** | Redis 分布式锁（`scheduled_warmup:leader`，TTL 15min）确保只有一个副本执行 |

---

## 配置入口

管理员后台 → **系统设置** → **定时 Warmup** Tab：

### 主配置卡片

| 配置项 | 类型 | 默认 | 说明 |
|-------|------|------|------|
| 启用 | 开关 | 关闭 | 总开关 |
| 触发时间（5 段 cron） | 字符串 | `0 8 * * *` | 标准 cron，时区跟随服务部署时区 |
| 仅工作日触发 | 开关 | 开启 | 关闭后周六周日也会触发；节假日 / 补班日按下方设置 |
| 参与平台 | 多选 | 全选 | Anthropic / OpenAI / Gemini / Antigravity / Grok，未勾选的平台不会被 Warmup |
| 节假日 | 文本域 | — | 每行一个 `YYYY-MM-DD`，这些日期跳过 Warmup |
| 补班日 | 文本域 | — | 每行一个 `YYYY-MM-DD`，这些日期即使周末也触发 Warmup |
| 立即触发一次 | 按钮 | — | 调试 / 补救用；可勾选「强制」绕过当前窗口幂等 |

### 飞书 Tab 内的镜像开关

为方便和其他飞书告警一起管理，「**系统设置 → 飞书 Webhook → 推送告警类型**」卡片里
追加了一个「**账号 Warmup 汇总**」开关，对应设置键 `feishu_webhook_notify_warmup`。

> 关闭这个开关时，Warmup 任务仍然会执行并写日志，**只是不推送飞书卡片**。

### 后端校验

保存设置时：

- `scheduled_warmup_platforms` 不可为空数组，至少选择一个平台，否则 400。
- `scheduled_warmup_cron` 走 `cron.NewParser(Minute|Hour|Dom|Month|Dow).Parse()`，
  非法表达式直接 400；空字符串视为「使用默认值 `0 8 * * *`」，保存时自动填入。
- `scheduled_warmup_holidays` / `scheduled_warmup_extra_workdays` 中的每一项都会用
  `time.Parse("2006-01-02", ...)` 校验，含非法日期直接 400 并返回首个非法值。
- 空字符串 / 仅空白的条目会被自动剔除。
- 节假日 / 补班日文本框支持「换行、逗号、分号」混合分隔，前端与后端均做规范化。

---

## 工作日 / 节假日判定

中国大陆的法定节假日伴随调休 / 补班，并非简单的「周六周日 = 休」。本功能采用 **本地
年度配置表** 方案：

```go
func (c *WorkdayCalendar) IsWorkday(t time.Time) bool {
    d := t.Format("2006-01-02")
    if c.extraWorkdays[d] { return true }   // 补班日：覆盖周末判定
    if c.holidays[d]      { return false }  // 节假日：覆盖工作日判定
    wd := t.Weekday()
    return wd != time.Saturday && wd != time.Sunday
}
```

### 优先级

**补班日 > 节假日 > 周末默认**

举例（假设 2026 年五一调休安排）：

| 日期 | 周几 | 节假日 | 补班日 | `IsWorkday` |
|------|------|--------|--------|-------------|
| 2026-05-01 (Fri) | 周五 | ✅ | — | `false`（节假日跳过） |
| 2026-05-02 (Sat) | 周六 | ✅ | — | `false`（节假日 + 周末） |
| 2026-04-26 (Sun) | 周日 | — | ✅ | `true`（补班覆盖周末） |
| 2026-05-09 (Sat) | 周六 | — | ✅ | `true`（补班覆盖周末） |
| 2026-05-11 (Mon) | 周一 | — | — | `true`（普通工作日） |
| 2026-05-16 (Sat) | 周六 | — | — | `false`（普通周末） |

### 维护频率

国务院通常 12 月前后发布次年节假日 / 调休安排：

1. 运维每年初打开「定时 Warmup」Tab；
2. 复制官方公告里的节假日清单到「节假日」文本域；
3. 复制调休补班日到「补班日」文本域；
4. 保存即可生效，零外部依赖、离线环境可用。

> 跨年遗忘续期时，由于 `holidays` 为空、`workday_only=true`，会退化为「纯周一至周五」
> 行为 —— 这是最安全的降级（不会误触发，最多漏触发节假日补班那种相对少见的边界）。

---

## Runner 执行流程

每分钟 ticker → 一轮判定：

1. **读取 `scheduled_warmup_enabled`**，关闭则返回。
2. **解析 `scheduled_warmup_cron`**，把 cron 命中的当日首个时间作为窗口锚点，并在 8 小时
   覆盖范围内按 5 小时推导后续窗口；判断 `now` 是否处于「上次 tick → now」区间的某个窗口边界。若否，返回。
   这套半开区间判定能容忍 ticker 的微小漂移（避免 :00 漂到 :00.5 时错过当分钟）。
3. **工作日判定**：若 `workday_only && !calendar.IsWorkday(now)`，返回。
4. **窗口幂等**：读取 `scheduled_warmup_last_run_date`，若等于当前窗口键（如
   `2026-05-21T13:00`），返回。旧版遗留的 `YYYY-MM-DD` 值仅视为当天首个窗口已执行，
   不会阻挡 13:00 之后的续窗。
5. **申请分布式锁**：Redis `SETNX scheduled_warmup:leader <instance_id> EX 300`；未拿到
   说明其他副本在跑，返回。
6. **锁内二次校验**：再读一次 `last_run_date`，防止两个副本在状态写入间隙各跑一遍。
7. **拉取账号**：`accountRepo.ListSchedulableByPlatforms(platforms)` —— 已在 SQL 层排
   除限流中、temp_unschedulable、expired、overloaded 的账号。
8. **并发探针**：信号量 = 10，对每个账号 `AccountTestService.RunTestBackground(accID, "")`，
   单账号超时 60s。每条结果归入 `successes` 或 `failures`。
9. **写入幂等键**：`scheduled_warmup_last_run_date = 当前窗口键`，例如
   `2026-05-21T13:00`。**先写后通知**，确保即使飞书 webhook 挂了，当前窗口也不会被重复触发。
   **例外**：若第 7 步拉取账号列表失败（`list_error` 非空），则跳过写入，让下次窗口边界有机会自动重试，也可由运维手动 `force=true` 补跑。
10. **推送飞书**：若 `feishu_webhook_notify_warmup=true`，调用 `SendScheduledWarmupSummary`
    发卡片；卡片头部颜色根据是否有失败动态切换（全成功蓝 / 含失败橙 / 拉取账号失败红）。
11. **释放分布式锁**：Lua 脚本 `if GET == self then DEL`，避免误删别人的锁。

---

## 手动触发

```
POST /api/v1/admin/settings/scheduled-warmup/run-now
Content-Type: application/json

{ "force": false }
```

- 不带 `force` 或 `force=false`：当前窗口已执行过会返回 400
  `already executed for warmup window: 2026-05-21T13:00`，避免重复浪费配额。
- `force=true`：绕过当前窗口幂等，但仍走分布式锁与「hi」探针逻辑。
- 响应示例：

```json
{
  "code": 0,
  "data": {
    "executed_at": "2026-05-21T08:00:12+08:00",
    "source": "manual",
    "platforms": ["anthropic", "openai", "gemini", "antigravity", "grok"],
    "total": 42,
    "success": 39,
    "failed": 3,
    "failures": [
      { "AccountID": 17, "Name": "acct-claude-pool-3", "Platform": "anthropic", "Error": "upstream 5xx" },
      { "AccountID": 22, "Name": "acct-openai-vip",     "Platform": "openai",    "Error": "401 invalid_api_key" },
      { "AccountID": 31, "Name": "acct-gemini-prod",    "Platform": "gemini",    "Error": "context deadline exceeded" }
    ],
    "duration_ms": 1234,
    "list_error": ""
  }
}
```

前端「立即触发」按钮调用的就是这个端点，结果直接回显在按钮下方面板里。

---

## 飞书汇总卡片

### 触发条件

`feishu_webhook_enabled=true` + `feishu_webhook_url` 非空 + `feishu_webhook_notify_warmup=true`。

### 卡片样例

```
🌅 [Sub2API] 账号 Warmup 完成
时间：2026-05-21 08:00:12
触发来源：schedule
覆盖平台：anthropic, openai, gemini, antigravity
共处理：42 个账号
耗时：1234 ms
✅ 成功：39
  anthropic: 20, gemini: 6, openai: 13
❌ 失败：3
  • acct-claude-pool-3 (anthropic) — upstream 5xx
  • acct-openai-vip (openai) — 401 invalid_api_key
  • acct-gemini-prod (gemini) — context deadline exceeded
```

### 卡片头部颜色

| 情况 | Emoji | 模板色 | 标题 |
|------|-------|-------|------|
| 全部成功 | 🌅 | `blue` | 账号 Warmup 完成 |
| 含失败 | 🌅 | `orange` | 账号 Warmup 完成（含失败） |
| 拉取账号列表失败（极少见） | 🌅 | `red` | 账号 Warmup 失败 |

### 失败列表折叠

每张卡片最多展示 **8 条** 失败明细，超出部分折叠为 `+ N more …`，完整失败列表可通过
日志或手动调用 run-now 端点查看。

### 不复用 30 分钟冷却

飞书业务告警有「同类告警 30 分钟内不重复」的冷却机制；Warmup 卡片**不使用**这套冷却 ——
任务自身已有「当日只执行一次」的幂等保证，重复冷却反而会让 force-run 调试时的卡片
被吞掉。

---

## 多副本部署与分布式锁

| 部署形态 | 行为 |
|---------|------|
| 单副本 | 直接执行；Redis 不可用时会打一条 `redis not configured; running without distributed lock` 警告，但不阻塞功能 |
| 多副本（共享 PG + Redis） | 每分钟所有副本都会评估一次 cron；首先 `SETNX scheduled_warmup:leader` 成功的那个副本负责执行，其余副本立即返回；执行结束后通过 Lua 释放锁 |
| 多副本（共享 PG，无 Redis） | 退化为「无锁」。由于 `last_run_date` 在任务**完成后**才写入，若两个实例同时通过「今天未跑」检查，两者都可能进入执行流程，存在重复预热同一批账号的风险。**多副本部署强烈建议配置 Redis。** |

锁的 key 与 OpsScheduledReportService 完全独立，多个定时任务不会互相阻塞。

> 实现位于 `tryAcquireLeaderLock` 与 `accountWarmupReleaseScript`，与
> `ops_scheduled_report_service.go` 同款 SETNX + Lua 释放套路。

---

## 与其他模块的边界

| 场景 | 处理方 | 与 Warmup 的关系 |
|------|--------|-----------------|
| 上游 5h / 7d 限流触发 / 恢复 | `RateLimitService` + 飞书「账号被限流 / 限流恢复」 | 完全独立；Warmup 的探针请求若恰好打到已限流账号，repo 层已过滤，不会发出 |
| 监控页告警策略命中 | `OpsAlertEvaluator` + 飞书监控告警 | 完全独立；监控告警有自己的静默 / 沉降机制 |
| 每个账号自定义的「定时连通性测试」 | `ScheduledTestRunnerService` | 共用同一条 `RunTestBackground` 路径，但调度策略、持久化、cron、锁全独立 |
| 余额低 / 账号额度超限告警 | `BalanceNotifyService` 等 | 完全独立 |

Warmup 的 cron / 幂等键 / 分布式锁 key / 飞书 gating 开关与上述模块均不冲突，可安全
并存。

---

## 设置键参考

存储在 `settings` 表，键值对形式：

| Key | 类型 | 默认 | 说明 |
|-----|------|------|------|
| `scheduled_warmup_enabled` | bool | `false` | 总开关 |
| `scheduled_warmup_cron` | string | `0 8 * * *` | 5 段 cron 表达式 |
| `scheduled_warmup_workday_only` | bool | `true` | 仅工作日触发 |
| `scheduled_warmup_holidays` | JSON 数组 | `[]` | 节假日清单，`["2026-05-01", ...]` |
| `scheduled_warmup_extra_workdays` | JSON 数组 | `[]` | 补班日清单 |
| `scheduled_warmup_platforms` | JSON 数组 | `["anthropic","openai","gemini","antigravity","grok"]` | 参与平台白名单 |
| `scheduled_warmup_last_run_date` | string | `""` | 最近一次执行的本地窗口键 `YYYY-MM-DDTHH:mm`（兼容旧值 `YYYY-MM-DD`；运行时自动写入，**勿手动改**） |
| `feishu_webhook_notify_warmup` | bool | `false` | 飞书汇总卡片开关 |

> 节假日 / 补班日 / 平台白名单字段也支持「换行 / 逗号 / 分号分隔的纯文本」回退解析，
> 方便直接从公告里复制粘贴；保存时统一规范化为 JSON 数组。

---

## 关键文件

**后端**

- `backend/internal/service/account_warmup_service.go` — 核心服务（Calendar / Runner / runOnce / 分布式锁 / 配置解析）
- `backend/internal/service/account_warmup_service_test.go` — 单测（日历、cron 跨界、JSON 数组解析、卡片渲染）
- `backend/internal/service/feishu_webhook_service.go` — `SendScheduledWarmupSummary` + 卡片构建
- `backend/internal/service/domain_constants.go` — `SettingKeyScheduledWarmup*` 常量
- `backend/internal/service/setting_service.go` / `settings_view.go` — 配置读写
- `backend/internal/handler/admin/setting_handler.go` — 校验 + run-now 端点
- `backend/internal/server/routes/admin.go` — 路由注册
- `backend/cmd/server/wire_gen.go` — 依赖注入 + 启停接入

**前端**

- `frontend/src/views/admin/SettingsView.vue` — 系统设置页（Tab 导航 + 配置读写逻辑）
- `frontend/src/views/admin/components/SettingTabWarmup.vue` — 「定时 Warmup」Tab 内容组件
- `frontend/src/views/admin/components/SettingTabFeishu.vue` — 「飞书 Webhook」Tab 内容组件
- `frontend/src/api/admin/settings.ts` — 类型扩展 + `runScheduledWarmupNow`
- `frontend/src/i18n/locales/{zh,en}.ts` — Tab 标题文案

**相关文档**

- `docs/GROUP_STATS_AND_FEISHU_WEBHOOK.md` — 飞书 Webhook 总览（含 Warmup 章节摘要）

---

## 常见问题

**Q：探针请求会消耗用户配额吗？**

A：会。每次为一次真实模型请求，prompt 是 `"hi"`，token 消耗取决于上游平台与测试链路
的 system prompt 规模，通常较小但不承诺严格固定。对账号当日配额影响轻微，但足以让上游
开启 5h 窗口。若请求失败（401 / 429 / 超时），则不能认为该账号窗口已成功开启。

**Q：如果想 07:30 触发怎么办？**

A：把 `scheduled_warmup_cron` 改成 `30 7 * * *` 即可。Cron 解析与 ticker 漂移容忍同款。

**Q：周末加班，想周日 08:00 也触发一次？**

A：把周日日期加进「补班日」文本框；或直接关掉「仅工作日触发」开关（会让所有周末都
触发）。

**Q：当前窗口 Warmup 失败了，想再跑一次。**

A：管理员后台「立即触发」按钮 + 勾选「强制」；或直接 `POST .../scheduled-warmup/run-now`
带 `{"force": true}`。

**Q：多个副本部署，会不会重复打上游？**

A：配置了 Redis 时：Redis 锁 + PG 幂等键双保险，通常不会重复。**若 Redis 不可用**，
两个副本可能同时通过「今天未跑」检查并进入执行，存在重复预热风险（因幂等键在执行完成
后才写入）。多副本部署强烈建议配置 Redis。

**Q：能否针对单个分组做 Warmup？**

A：当前版本按「平台白名单 + 全局 schedulable 过滤」批量处理。如需分组粒度，后续可
扩展 `scheduled_warmup_groups` 设置键 + `ListSchedulableByGroupIDAndPlatforms` 拉取，
属于不破坏现有接口的增量改动。

**Q：Warmup 触发了，但是飞书没收到卡片。**

A：依次检查：
1. 「系统设置 → 飞书 Webhook → 启用飞书 Webhook」是否开启；
2. Webhook URL 是否正确（手动 curl 一次 `{"msg_type":"text","content":{"text":"ping"}}` 验证）；
3. 「推送告警类型 → 账号 Warmup 汇总」开关是否开启；
4. 后端日志关键字 `scheduled_warmup:` / `feishu_webhook:`，会写明跳过原因。

---

## 审查与修复记录

本功能在 2026-05-21 经过专项审查（见 [`docs/SCHEDULED_ACCOUNT_WARMUP_REVIEW.md`](SCHEDULED_ACCOUNT_WARMUP_REVIEW.md)），并完成以下修复：

| 问题 | 修复内容 |
|------|---------|
| 平台全部取消后回退为全平台（高风险） | 后端新增校验：`platforms` 为空数组直接 400 |
| 节假日文本框只支持换行分隔，与文档不符 | 前端 `parseDateLines` 改为支持换行 / 逗号 / 分号混合分隔 |
| `RunNow(force=false)` 缺少锁内二次幂等检查，并发调用可能重复执行 | 拿锁后补一次 `last_run_date` 再读，与定时任务 `tick()` 行为对齐 |
| 清空 cron 字段不会恢复默认值，旧值被保留 | 写入时空 cron 规范化为 `0 8 * * *` |
| 拉取账号失败也写入幂等键，导致当天无法自动重试 | 仅 `list_error == ""` 时才写 `last_run_date` |
| Redis 锁 TTL（5min）< 任务上下文超时（10min），多副本下可能锁过期后被抢占 | TTL 调整为 15min |
| 文档夸大「无 Redis 多副本不会重复」及「个位数 token」的承诺 | 相关描述已修正为更准确的表述 |
