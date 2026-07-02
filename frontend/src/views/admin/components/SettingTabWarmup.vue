<template>
  <div class="space-y-6">
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h3 class="text-base font-medium text-gray-900 dark:text-white">定时账号 Warmup</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          每日早上自动对所有可调度账号发起一次极小请求，触发上游 5h 限流窗口起点对齐到工作开始时刻，确保白天能用满 2 个完整的 5h 窗口。
        </p>
      </div>
      <div class="px-6 py-6 space-y-4">
        <div class="flex items-center justify-between">
          <label class="mb-0 block text-sm font-medium text-gray-700 dark:text-gray-300">启用</label>
          <Toggle v-model="form.scheduled_warmup_enabled" />
        </div>
        <div v-if="form.scheduled_warmup_enabled" class="space-y-4">
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">触发时间（5 段 cron）</label>
            <input
              v-model="form.scheduled_warmup_cron"
              type="text"
              class="input w-64"
              placeholder="0 8 * * *"
            />
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">默认 <code>0 8 * * *</code>（每天 08:00）。时区跟随服务部署时区，工作日判定见下方设置。</p>
          </div>
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium text-gray-700 dark:text-gray-300">仅工作日触发</p>
              <p class="text-xs text-gray-500 dark:text-gray-400">默认开启。周末不触发，但下面的"补班日"会覆盖周末；节假日则会跳过。</p>
            </div>
            <Toggle v-model="form.scheduled_warmup_workday_only" />
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">参与平台</label>
            <div class="flex flex-wrap gap-3">
              <label
                v-for="p in warmupPlatformOptions"
                :key="p"
                class="inline-flex cursor-pointer items-center gap-1.5 rounded-md border px-3 py-1.5 text-sm transition-colors"
                :class="form.scheduled_warmup_platforms?.includes(p)
                  ? 'bg-primary-50 border-primary-300 dark:bg-primary-900/20 dark:border-primary-700'
                  : 'border-gray-200 hover:bg-gray-50 dark:border-dark-600 dark:hover:bg-dark-700'"
              >
                <input
                  type="checkbox"
                  :checked="form.scheduled_warmup_platforms?.includes(p)"
                  class="h-3.5 w-3.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                  @change="toggleWarmupPlatform(p)"
                />
                <span>{{ warmupPlatformLabel(p) }}</span>
              </label>
            </div>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">未勾选的平台不会被 Warmup。</p>
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">节假日（每行一个 YYYY-MM-DD）</label>
            <textarea
              :value="(form.scheduled_warmup_holidays || []).join('\n')"
              rows="4"
              class="input"
              placeholder="2026-05-01&#10;2026-05-02&#10;2026-05-03"
              @input="onWarmupHolidaysInput($event)"
            ></textarea>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">这些日期即使是工作日也会跳过 Warmup。</p>
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">补班日（每行一个 YYYY-MM-DD）</label>
            <textarea
              :value="(form.scheduled_warmup_extra_workdays || []).join('\n')"
              rows="4"
              class="input"
              placeholder="2026-04-26&#10;2026-05-09"
              @input="onWarmupExtraWorkdaysInput($event)"
            ></textarea>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">这些日期即使是周末也会触发 Warmup（覆盖节假日）。国务院节假日公告发布后手动维护即可。</p>
          </div>
          <div class="border-t border-gray-100 pt-4 dark:border-dark-700">
            <div class="flex items-center justify-between">
              <div>
                <p class="text-sm font-medium text-gray-700 dark:text-gray-300">立即触发一次</p>
                <p class="text-xs text-gray-500 dark:text-gray-400">用于联调或手动补救。当天若已运行过会拒绝，可勾选"强制"绕过。</p>
              </div>
              <div class="flex items-center gap-3">
                <label class="inline-flex cursor-pointer items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                  <input type="checkbox" v-model="warmupRunForce" />
                  强制
                </label>
                <button
                  type="button"
                  class="btn btn-primary btn-sm"
                  :disabled="warmupRunLoading"
                  @click="onWarmupRunNow"
                >
                  {{ warmupRunLoading ? "执行中…" : "立即触发" }}
                </button>
              </div>
            </div>
            <div v-if="warmupLastRunResult" class="mt-4 rounded-md border border-gray-200 bg-gray-50 p-3 text-xs text-gray-700 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300">
              <p>
                执行于
                <code>{{ warmupLastRunResult.executed_at }}</code> · 来源
                <code>{{ warmupLastRunResult.source }}</code> · 共
                {{ warmupLastRunResult.total }} 个账号 · 成功
                <span class="font-semibold text-emerald-600">{{ warmupLastRunResult.success }}</span> · 失败
                <span class="font-semibold" :class="warmupLastRunResult.failed > 0 ? 'text-red-600' : 'text-gray-500'">{{ warmupLastRunResult.failed }}</span> · 耗时
                {{ warmupLastRunResult.duration_ms }} ms
              </p>
              <ul
                v-if="warmupLastRunResult.failures && warmupLastRunResult.failures.length > 0"
                class="mt-2 list-disc space-y-1 pl-5"
              >
                <li v-for="(f, i) in warmupLastRunResult.failures" :key="i">
                  {{ f.Name }} ({{ f.Platform }}) — {{ f.Error }}
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { adminAPI } from "@/api";
import type { ScheduledWarmupRunResult } from "@/api/admin/settings";
import Toggle from "@/components/common/Toggle.vue";
import { extractApiErrorMessage } from "@/utils/apiError";
import { useAppStore } from "@/stores";

interface WarmupForm {
  scheduled_warmup_enabled: boolean;
  scheduled_warmup_cron: string;
  scheduled_warmup_workday_only: boolean;
  scheduled_warmup_holidays: string[];
  scheduled_warmup_extra_workdays: string[];
  scheduled_warmup_platforms: string[];
}

const props = defineProps<{
  form: WarmupForm;
}>();

const appStore = useAppStore();

const warmupPlatformOptions = [
  "anthropic",
  "openai",
  "gemini",
  "antigravity",
  "grok",
];

const warmupPlatformLabel = (platform: string): string => {
  switch (platform) {
    case "anthropic":
      return "Anthropic";
    case "openai":
      return "OpenAI";
    case "gemini":
      return "Gemini";
    case "antigravity":
      return "Antigravity";
    case "grok":
      return "Grok";
    default:
      return platform;
  }
};

const warmupRunForce = ref(false);
const warmupRunLoading = ref(false);
const warmupLastRunResult = ref<ScheduledWarmupRunResult | null>(null);

const parseDateLines = (raw: string): string[] =>
  raw
    .split(/[\r\n,，;；]+/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0);

const toggleWarmupPlatform = (platform: string) => {
  const list = props.form.scheduled_warmup_platforms || [];
  const idx = list.indexOf(platform);
  if (idx >= 0) {
    list.splice(idx, 1);
  } else {
    list.push(platform);
  }
  props.form.scheduled_warmup_platforms = [...list];
};

const onWarmupHolidaysInput = (event: Event) => {
  const target = event.target as HTMLTextAreaElement;
  props.form.scheduled_warmup_holidays = parseDateLines(target.value);
};

const onWarmupExtraWorkdaysInput = (event: Event) => {
  const target = event.target as HTMLTextAreaElement;
  props.form.scheduled_warmup_extra_workdays = parseDateLines(target.value);
};

const onWarmupRunNow = async () => {
  warmupRunLoading.value = true;
  try {
    warmupLastRunResult.value =
      await adminAPI.settings.runScheduledWarmupNow(warmupRunForce.value);
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, "执行失败"));
  } finally {
    warmupRunLoading.value = false;
  }
};
</script>
