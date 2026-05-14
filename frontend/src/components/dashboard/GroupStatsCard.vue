<template>
  <div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800">
    <h3 class="mb-3 text-sm font-semibold text-gray-700 dark:text-dark-200">{{ t('dashboard.groupStats.title') }}</h3>
    <div class="flex flex-wrap gap-4">
      <div
        v-for="gs in items"
        :key="gs.group_id"
        class="flex min-w-[180px] flex-col gap-1 rounded-lg border border-gray-100 bg-gray-50 px-4 py-3 dark:border-dark-700 dark:bg-dark-700"
      >
        <span class="text-xs font-medium text-gray-500 dark:text-dark-400">{{ gs.group_name }}</span>
        <div class="flex gap-4 text-sm">
          <span class="text-gray-600 dark:text-dark-300">
            5h: <span class="font-semibold text-gray-900 dark:text-white">${{ gs.actual_cost_5h.toFixed(4) }}</span>
          </span>
          <span class="text-gray-600 dark:text-dark-300">
            7d: <span class="font-semibold text-gray-900 dark:text-white">${{ gs.actual_cost_7d.toFixed(4) }}</span>
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

export interface GroupStatItem {
  group_id: number
  group_name: string
  actual_cost_5h: number
  actual_cost_7d: number
}

defineProps<{ items: GroupStatItem[] }>()
</script>
