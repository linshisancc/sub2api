<template>
  <div class="space-y-6">
    <!-- 基础配置 -->
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h3 class="text-base font-medium text-gray-900 dark:text-white">飞书 Webhook 通知</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          通过飞书机器人 Webhook 推送告警消息，与邮件告警并行触发，互不影响。
        </p>
      </div>
      <div class="px-6 py-6 space-y-4">
        <div class="flex items-center justify-between">
          <label class="mb-0 block text-sm font-medium text-gray-700 dark:text-gray-300">启用飞书 Webhook</label>
          <Toggle v-model="form.feishu_webhook_enabled" />
        </div>
        <div v-if="form.feishu_webhook_enabled" class="space-y-4">
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">Webhook URL</label>
            <input
              v-model="form.feishu_webhook_url"
              type="url"
              class="input"
              placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..."
            />
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              在飞书群聊「设置 → 机器人 → 添加自定义机器人」获取 Webhook URL。
            </p>
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">冷却时间（分钟）</label>
            <input
              v-model.number="form.feishu_webhook_cooldown_minutes"
              type="number"
              min="1"
              max="1440"
              class="input w-32"
              placeholder="30"
            />
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">同类告警触发后，冷却期内不重复推送，默认 30 分钟，最大 1440 分钟（24 小时）。</p>
          </div>
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-gray-700 dark:text-gray-300">@所有人</p>
              <p class="text-xs text-gray-500 dark:text-gray-400">推送告警卡片时 @所有人。</p>
            </div>
            <Toggle v-model="form.feishu_webhook_at_all" />
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">@指定成员</label>
            <textarea
              v-model="form.feishu_webhook_at_user_ids"
              rows="3"
              class="input"
              placeholder="ou_xxxxxxxxxxxxxxxx&#10;ou_yyyyyyyyyyyyyyyy"
            ></textarea>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">推送时 @ 指定成员，填写飞书 open_id，每行一个（也支持逗号 / 分号分隔）。</p>
          </div>
        </div>
      </div>
    </div>

    <!-- 推送告警类型 -->
    <div v-if="form.feishu_webhook_enabled" class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h3 class="text-base font-medium text-gray-900 dark:text-white">推送告警类型</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          选择需要推送到飞书群的告警类型，各类告警独立冷却。
        </p>
      </div>
      <div class="px-6 py-6 space-y-4">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm font-medium text-gray-700 dark:text-gray-300">用户余额不足</p>
            <p class="text-xs text-gray-500 dark:text-gray-400">用户余额低于告警阈值时触发，需在「邮件设置」中启用余额低通知。</p>
          </div>
          <Toggle v-model="form.feishu_webhook_notify_balance" />
        </div>
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm font-medium text-gray-700 dark:text-gray-300">账号额度超限</p>
            <p class="text-xs text-gray-500 dark:text-gray-400">账号日 / 周 / 总额度触达告警阈值时触发，需在「邮件设置」中启用账号配额通知。</p>
          </div>
          <Toggle v-model="form.feishu_webhook_notify_account" />
        </div>
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm font-medium text-gray-700 dark:text-gray-300">监控告警</p>
            <p class="text-xs text-gray-500 dark:text-gray-400">监控页告警策略命中阈值时推送，复用监控模块自身的静默 / 去重机制；需先在监控页配置告警策略。</p>
          </div>
          <Toggle v-model="form.feishu_webhook_notify_ops" />
        </div>
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm font-medium text-gray-700 dark:text-gray-300">账号 Warmup 汇总</p>
            <p class="text-xs text-gray-500 dark:text-gray-400">每日定时账号 Warmup 任务执行完成后推送一张汇总卡片（成功 / 失败 / 跳过）。配置入口在「定时 Warmup」Tab。</p>
          </div>
          <Toggle v-model="form.feishu_webhook_notify_warmup" />
        </div>
      </div>
    </div>

    <!-- 登录安全 -->
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h3 class="text-base font-medium text-gray-900 dark:text-white">登录安全</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          同一 IP 在时间窗口内多次登录失败时自动封禁，并推送飞书告警（若已启用 Webhook）。独立于上方飞书总开关，即使未配置 Webhook，封禁仍会生效，只是不会推送通知。
        </p>
      </div>
      <div class="px-6 py-6 space-y-4">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm font-medium text-gray-700 dark:text-gray-300">启用登录爆破自动封禁</p>
            <p class="text-xs text-gray-500 dark:text-gray-400">默认开启，可在下方调整触发阈值与封禁时长。</p>
          </div>
          <Toggle v-model="form.feishu_login_bruteforce_autoban_enabled" />
        </div>
        <div v-if="form.feishu_login_bruteforce_autoban_enabled" class="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">最大失败次数</label>
            <input
              v-model.number="form.login_bruteforce_max_failures"
              type="number"
              min="1"
              max="1000"
              class="input"
              placeholder="10"
            />
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">统计窗口（分钟）</label>
            <input
              v-model.number="form.login_bruteforce_window_minutes"
              type="number"
              min="1"
              max="1440"
              class="input"
              placeholder="5"
            />
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">封禁时长（分钟）</label>
            <input
              v-model.number="form.login_bruteforce_ban_minutes"
              type="number"
              min="1"
              max="10080"
              class="input"
              placeholder="60"
            />
          </div>
        </div>
        <p v-if="form.feishu_login_bruteforce_autoban_enabled" class="text-xs text-gray-500 dark:text-gray-400">
          同一 IP 在统计窗口内登录失败达到最大失败次数即被封禁，封禁期间该 IP 无法访问整站接口。
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import Toggle from "@/components/common/Toggle.vue";

interface FeishuForm {
  feishu_webhook_enabled: boolean;
  feishu_webhook_url: string;
  feishu_webhook_cooldown_minutes: number;
  feishu_webhook_at_all: boolean;
  feishu_webhook_at_user_ids: string;
  feishu_webhook_notify_balance: boolean;
  feishu_webhook_notify_account: boolean;
  feishu_webhook_notify_ops: boolean;
  feishu_webhook_notify_warmup: boolean;
  feishu_login_bruteforce_autoban_enabled: boolean;
  login_bruteforce_max_failures: number;
  login_bruteforce_window_minutes: number;
  login_bruteforce_ban_minutes: number;
}

defineProps<{
  form: FeishuForm;
}>();
</script>
