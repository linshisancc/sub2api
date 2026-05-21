# 定时账号 Warmup 审查记录

本文档记录对 `docs/SCHEDULED_ACCOUNT_WARMUP.md` 及其对应代码实现的审查结果。

审查目标不是否定这个功能，而是把文档承诺、代码实际行为、部署风险之间的差距讲清楚，方便后续决定哪些要修、哪些只需要调整文档。

---

## 审查范围

主要查看了以下文件：

- `docs/SCHEDULED_ACCOUNT_WARMUP.md`
- `backend/internal/service/account_warmup_service.go`
- `backend/internal/service/feishu_webhook_service.go`
- `backend/internal/handler/admin/setting_handler.go`
- `backend/internal/repository/account_repo.go`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/api/admin/settings.ts`

相关测试：

- `backend/internal/service/account_warmup_service_test.go`
- `backend/internal/handler/admin` 包测试

---

## 总体结论

功能主流程是成立的：

1. 后台每分钟检查 cron。
2. 命中后读取配置。
3. 按平台筛选可调度账号。
4. 并发调用账号测试链路。
5. 记录当天已执行。
6. 推送飞书汇总卡片。

但文档里有几处说得比代码更稳，尤其是多副本无 Redis 场景、平台空选择、手动触发并发、失败后的幂等语义、请求成本说明。若当前部署确定是单机单进程，多副本相关风险会降低；但仍有一些单机下也会发生的问题需要处理。

---

## 问题 1：多副本无 Redis 时，不能保证不重复请求上游

### 文档说法

文档在“多副本部署与分布式锁”里说：

> 多副本（共享 PG，无 Redis）会退化为无锁，但由于 `last_run_date` 是 PG 持久化的，第二个副本拉取到 `last_run_date == today` 后会立即退出，不会多发上游请求。

### 代码实际行为

当前代码是在执行完成后才写入：

```go
scheduled_warmup_last_run_date = today
```

也就是说，如果两个实例同时通过了“今天还没跑”的检查，它们都可能进入账号 warmup 流程。只有跑完以后才会写入幂等键。

### 风险

如果是多实例部署，并且 Redis 不可用或未配置，可能重复预热同一批账号。

如果确定是单机单进程部署，这个问题通常不会触发。但如果出现以下情况，仍可能接近多实例状态：

- systemd / supervisor 配错，启动了两个进程。
- Docker / Compose 重启时旧进程未完全退出，新进程已启动。
- 以后做水平扩容，但忘了重新审视这里。

### 建议

如果未来支持多实例，建议使用数据库原子 claim，而不是只靠任务结束后的 `last_run_date`。

推荐模型：

```sql
CREATE TABLE scheduled_job_runs (
  job_name text NOT NULL,
  run_date date NOT NULL,
  status text NOT NULL,
  started_at timestamptz NOT NULL,
  finished_at timestamptz,
  instance_id text,
  PRIMARY KEY (job_name, run_date)
);
```

任务开始前：

```sql
INSERT INTO scheduled_job_runs(job_name, run_date, status, started_at, instance_id)
VALUES ('account_warmup', CURRENT_DATE, 'running', now(), $instance_id)
ON CONFLICT DO NOTHING;
```

插入成功才执行。插入失败说明今天已经有人跑过或正在跑。

如果当前只支持单机部署，可以先不做这张表，但需要把文档里“多副本无 Redis 不会重复请求”的承诺改掉。

---

## 问题 2：Redis 锁 TTL 5 分钟，可能短于任务执行时间

### 代码实际行为

Redis 锁 TTL 是 5 分钟：

```go
accountWarmupLeaderLockTTL = 5 * time.Minute
```

但 warmup 整体执行上下文是 10 分钟：

```go
context.WithTimeout(context.Background(), 10*time.Minute)
```

单账号超时是 60 秒，并发度是 10。账号数量较多或上游响应较慢时，任务可能超过 5 分钟。

### 风险

在多实例部署下，锁过期后另一个实例有机会拿到锁并进入执行流程。

在单机单进程部署下，这个问题通常不会造成重复执行，因为没有第二个实例来抢锁。

### 建议

如果保持 Redis 锁方案，至少做一个：

- 将 TTL 调整为大于任务最大执行时间，例如 15 分钟。
- 执行期间定期续约锁。
- 用数据库原子 claim 作为最终幂等保证，Redis 锁只作为减少竞争的优化。

单机部署下，可以把这个问题标记为低优先级，但文档不要把 Redis 锁描述成绝对可靠的多副本保证。

---

## 问题 3：平台全部取消勾选后，实际会回退为全平台

### 文档说法

文档说“未勾选的平台不会被 Warmup”。

### 代码实际行为

后端加载配置时：

```go
platforms := parseStringJSONArray(values[SettingKeyScheduledWarmupPlatforms])
if len(platforms) == 0 {
    platforms = []string{"anthropic", "openai", "gemini", "antigravity"}
}
```

这意味着平台列表为空时，会回退成默认全平台。

### 风险

这是单机部署下也会发生的问题。

用户可能以为自己取消了所有平台，实际结果却是所有平台都被 warmup。这个行为很反直觉，也比较危险。

### 建议

优先修。

最简单的方式：前端和后端都禁止保存空平台。

后端校验：

```go
if req.ScheduledWarmupPlatforms != nil && len(*req.ScheduledWarmupPlatforms) == 0 {
    return fmt.Errorf("scheduled_warmup_platforms 至少选择一个平台")
}
```

前端也应在取消最后一个平台时提示，或者禁用保存按钮。

如果确实想支持“不 warmup 任何平台”，则需要区分：

- 老数据 / 未配置：使用默认全平台。
- 用户显式保存空数组：表示不执行任何平台。

这个方案更复杂，不如直接禁止空选择。

---

## 问题 4：探针能触发窗口，但“个位数 token”描述偏乐观

### 文档说法

文档说探针请求每次仅消耗个位数 token。

### 代码实际行为

Warmup 复用：

```go
AccountTestService.RunTestBackground(accID, "")
```

这条链路本质是“账号测试连接”，会发起真实的上游模型推理请求，不是 HTTP ping，也不是本地模拟。

因此，从“是否足以触发上游 5h 窗口倒计时”的角度看，这个实现思路是成立的：只要请求成功并被上游计入 usage，它就应该足以开启对应账号的滚动窗口。

各平台大致情况：

- Claude / Anthropic：真实 `/v1/messages` 请求，用户内容是 `"hi"`，同时带 Claude Code system prompt，`max_tokens` 为 `1024`。
- OpenAI：真实 Responses 请求，用户内容是 `"hi"`，同时带 `openai.DefaultInstructions`。
- Gemini：真实 `streamGenerateContent` 请求，用户内容是 `"hi"`，带一句较短 system instruction。
- Antigravity：测试请求最轻，输入 `"."`，输出通常限制为 1 token。

也就是说，它“能触发窗口”的可信度比较高；需要修正的是文档里“个位数 token”的成本描述。

### 风险

功能有效性风险不高。真正的风险是成本和预期表达不准确：

- Claude / OpenAI 路径不是严格的个位数 token。
- `max_tokens=1024` 不代表一定输出 1024 token，但它也不是最小化 warmup 设计。
- 如果请求失败，例如 401、429、网络错误、模型不可用，就不能认为该账号窗口已成功开启。

### 建议

建议把文档表述从“个位数 token”改成更稳的说法：

> 探针复用测试连接链路，会发起一次真实模型请求；请求成功后通常足以触发上游窗口。实际 token 消耗取决于平台与测试链路，通常较小，但不承诺严格个位数。

如果后续希望进一步降低成本，可以新增专门的 warmup probe，但这属于优化，不是当前功能有效性的前置条件：

```go
RunWarmupProbe(ctx, accountID)
```

专门用于 warmup：

- prompt 使用 `"hi"`。
- `max_tokens` 设置为 8 或 16。
- 不带大型 system prompt。
- 不做图片模型测试。
- 不做额外能力探测。

这样可以把成本控制得更精确，也能让文档里的“极小请求”更扎实。

---

## 问题 5：节假日输入格式的文档和前端不一致

### 文档说法

文档说节假日 / 补班日字段支持：

> 换行 / 逗号 / 分号分隔的纯文本。

### 代码实际行为

后端底层解析函数确实支持换行、逗号、分号。

但前端输入时只按换行拆分：

```ts
raw.split(/\r?\n/)
```

如果用户在前端输入：

```text
2026-05-01,2026-05-02
```

前端会把它当成一个字符串提交给后端。后端保存校验会把这个整体当成一个日期校验，最终失败。

### 风险

这是单机部署下也会发生的问题。

用户照着文档粘贴逗号分隔日期，可能保存失败。

### 建议

前端改成和文档一致：

```ts
raw
  .split(/[\r\n,，;；]+/)
  .map((item) => item.trim())
  .filter(Boolean)
```

或者删掉文档里“逗号 / 分号”的说明，只保留“每行一个日期”。

更推荐修前端，体验更好。

---

## 问题 6：非法 cron 在运行期会静默跳过

### 代码实际行为

保存设置时有 cron 校验，这是好的。

但如果数据库里被手工写入非法 cron，运行时：

```go
sched, err := accountWarmupCronParser.Parse(spec)
if err != nil {
    return false
}
```

任务会直接跳过，没有明显日志。

### 风险

单机部署下也会发生。

结果是页面看起来配置开启了，但任务永远不跑，排查时不直观。

### 建议

在 `loadConfig` 或 `tick` 阶段增加 warning 日志：

```go
if err := ValidateWarmupCron(cronSpec); err != nil {
    slog.Warn("scheduled_warmup: invalid cron; skipping",
        "cron", cronSpec,
        "error", err,
    )
    return nil, false
}
```

---

## 问题 7：手动触发缺少锁内二次幂等检查

### 文档说法

文档说：

> 不带 `force` 或 `force=false`：当日已执行过会返回 400，避免重复浪费配额。

### 代码实际行为

`RunNow` 会在加载配置后检查一次 `lastRunDate`：

```go
if !force && cfg.lastRunDate == today {
    return nil, fmt.Errorf("already executed today: %s", cfg.lastRunDate)
}
```

然后申请 Redis 锁，拿到锁后直接执行：

```go
return s.executeAndReport(cfg, now, "manual"), nil
```

定时任务 `tick` 在拿到锁后会再读一次 `scheduled_warmup_last_run_date`，但 `RunNow` 没有这一步。

### 风险

这是单机部署下也可能发生的问题。

普通页面按钮有 `warmupRunLoading` 禁用，能挡住常规双击。但如果出现以下情况，仍可能重复执行：

- 两个管理员同时点手动触发。
- 浏览器或代理重试了 POST 请求。
- 直接用 API 并发调用 `/scheduled-warmup/run-now`。
- `force=false` 的请求 A 执行完成并写入 `last_run_date` 后，请求 B 才拿到锁，但 B 使用的是旧配置里的空 `lastRunDate`，仍会执行。

### 建议

在 `RunNow` 拿到锁后补一次和定时任务相同的二次检查：

```go
if !force {
    if latest, err := s.settingRepo.GetValue(ctx, SettingKeyScheduledWarmupLastRunDate); err == nil {
        if strings.TrimSpace(latest) == now.Format("2006-01-02") {
            return nil, fmt.Errorf("already executed today: %s", latest)
        }
    }
}
```

这样 `force=false` 才能真正保证“当天已执行就拒绝”。

---

## 问题 8：拉取账号失败也会写入当天已执行

### 代码实际行为

`runOnce` 拉取账号失败时会返回 `summary.ListError`：

```go
accounts, err := s.accountRepo.ListSchedulableByPlatforms(ctx, cfg.platforms)
if err != nil {
    summary.ListError = err.Error()
    return summary
}
```

但 `executeAndReport` 不区分成功、部分失败、拉取账号失败，都会写：

```go
scheduled_warmup_last_run_date = today
```

### 风险

这是单机部署下也会发生的问题。

如果只是一次短暂 DB 抖动或 repository 错误，系统会发送失败卡片，但当天后续定时任务不会再自动重试。管理员必须手动 `force=true` 才能补跑。

这不一定是 bug，因为“失败也只通知一次，避免反复打扰”也说得通。但文档没有明确交代这个语义。

### 建议

二选一：

1. 保持当前实现，但文档明确写清：只要任务被触发，无论拉取账号是否失败，都会写入当日幂等；失败后需要手动 force 补跑。
2. 调整实现：只有账号列表拉取成功后才写 `last_run_date`；拉取失败不写幂等，让下一次 cron 有机会自动重试。

如果目标是“每天只打一张汇总卡片，不自动重试”，选第 1 种。  
如果目标是“尽量保证当天 warmup 成功”，选第 2 种。

---

## 问题 9：清空 cron 并不会恢复默认值

### 文档说法

文档说默认 cron 是：

```text
0 8 * * *
```

代码里的 `ValidateWarmupCron` 也把空字符串视为“使用默认值”。

### 代码实际行为

更新设置时，如果请求里传入空字符串，`settings.ScheduledWarmupCron` 会变成空。

但持久化时：

```go
if cron := strings.TrimSpace(settings.ScheduledWarmupCron); cron != "" {
    updates[SettingKeyScheduledWarmupCron] = cron
}
```

也就是说，空字符串不会写入 settings 表。结果是旧的 cron 值会保留，而不是清空并回退默认。

### 风险

这是单机部署下也会发生的问题。

如果管理员以为“清空 cron = 恢复默认 08:00”，实际可能仍沿用之前保存过的 cron，比如 `30 7 * * *`。

### 建议

二选一：

1. 前端不允许空 cron，明确要求始终填写 cron。
2. 后端把空 cron 规范化成默认值并写入：

```go
cron := strings.TrimSpace(settings.ScheduledWarmupCron)
if cron == "" {
    cron = accountWarmupDefaultCron
}
updates[SettingKeyScheduledWarmupCron] = cron
```

更推荐第 2 种，和“空字符串使用默认值”的校验语义保持一致。

---

## 单机部署下的风险排序

如果当前明确是：

- 单机部署
- 单进程运行
- 不做水平扩容
- 不会滚动发布双进程共存

那么多副本相关问题可以先降级。

单机下仍建议优先处理：

1. 平台全取消会变成全平台预热。
2. 手动触发 `force=false` 缺少锁内二次幂等检查。
3. 拉取账号失败也写入当天已执行，这个语义需要明确取舍。
4. 清空 cron 不会恢复默认值。
5. “个位数 token”文档描述需要改得更准确。
6. 日期输入支持格式和前端不一致。
7. 非法 cron 静默失效。

多实例相关问题建议至少修改文档：

1. 删除或弱化“无 Redis 多副本不会重复请求上游”的承诺。
2. 明确说明多副本部署建议配置 Redis。
3. 如果未来正式支持多副本，补数据库原子幂等。

---

## 建议修复顺序

### 第一批：低成本、高收益

1. 后端禁止保存空平台列表。
2. 前端禁止取消最后一个平台，或保存时提示。
3. `RunNow(force=false)` 拿到锁后再读一次 `last_run_date`。
4. 明确“拉取账号失败是否写入幂等”的产品语义，并同步文档或代码。
5. 清空 cron 时写入默认值，或前端禁止空 cron。
6. 前端日期解析支持逗号 / 分号。
7. 非法 cron 运行期增加 warning 日志。
8. 修改文档里“个位数 token”和“无 Redis 多副本不会重复”的绝对表述。

### 第二批：增强正确性

1. Redis 锁 TTL 调整为大于任务最大执行时间，或加入续约。
2. 可选：为 warmup 增加 dedicated probe，进一步减少 token 消耗和测试链路副作用。
3. 增加关键单测。

### 第三批：多实例硬保证

1. 增加 `scheduled_job_runs` 之类的数据库执行记录表。
2. 用唯一键实现每日任务原子 claim。
3. Redis 锁降级为优化，不作为最终正确性保证。

---

## 建议补充测试

建议至少补这些测试：

- 平台列表为空时保存失败。
- 前端日期输入 `2026-05-01,2026-05-02;2026-05-03` 能拆成 3 个日期。
- 非法 cron 会在运行期产生 warning 或阻止执行。
- 清空 cron 后会恢复默认值，或保存接口明确拒绝空 cron。
- 单次 warmup 执行完成后写入 `scheduled_warmup_last_run_date`。
- `force=false` 时当天已执行会拒绝。
- 两个 `RunNow(force=false)` 串行或并发进入时，第二个在锁内二次检查后拒绝。
- `force=true` 时允许手动重复执行。
- 拉取账号失败时是否写入 `last_run_date`，按最终语义补测试。

如果未来支持多实例：

- 两个 runner 同时触发，只有一个能 claim 成功。
- Redis 不可用时，DB claim 仍然阻止重复执行。
- Redis 锁过期后，第二个实例仍然不能重复执行同一天任务。

---

## 验证记录

已运行 warmup 相关 service 单测：

```bash
go test ./internal/service -run 'TestWorkdayCalendar|TestParseStringJSONArray|TestCronCrossed|TestBuildWarmupCardBody'
```

结果通过。

也尝试运行：

```bash
go test ./internal/service ./internal/handler/admin
```

其中 `internal/handler/admin` 通过；`internal/service` 被当前沙箱环境限制拦在一个与 warmup 无关的 `httptest.NewServer` 端口监听用例上，不代表 warmup 单测失败。

---

## 一句话版

如果项目只按单机单进程部署，多副本重复执行问题可以先不急。当前探针是一次真实模型请求，成功时足以触发窗口；这里需要调整的是 token 成本表述，而不是否定 warmup 有效性。平台空选择、手动触发二次幂等、失败是否写入当天已执行、清空 cron 语义、日期输入不一致、cron 静默失效这些问题仍建议优先处理。
