<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading" class="flex items-center justify-center py-12"><LoadingSpinner /></div>
      <template v-else-if="stats">
        <UserDashboardStats :stats="stats" :balance="user?.balance || 0" :is-simple="authStore.isSimpleMode" />

        <!-- 分组消耗额度卡片 -->
        <div v-if="groupStatsList.length > 0" class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <h3 class="mb-3 text-sm font-semibold text-gray-700 dark:text-dark-200">分组消耗额度</h3>
          <div class="flex flex-wrap gap-4">
            <div
              v-for="gs in groupStatsList"
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

        <UserDashboardCharts v-model:startDate="startDate" v-model:endDate="endDate" v-model:granularity="granularity" :loading="loadingCharts" :trend="trendData" :models="modelStats" @dateRangeChange="loadCharts" @granularityChange="loadCharts" @refresh="refreshAll" />
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div class="lg:col-span-2"><UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage" /></div>
          <div class="lg:col-span-1"><UserDashboardQuickActions /></div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { usageAPI, type UserDashboardStats as UserStatsType } from '@/api/usage'
import { keysAPI } from '@/api'
import { userGroupsAPI, type GroupWindowStats } from '@/api/groups'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'
import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'
import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'
import type { UsageLog, TrendDataPoint, ModelStat } from '@/types'

const authStore = useAuthStore()
const user = computed(() => authStore.user)
const stats = ref<UserStatsType | null>(null)
const loading = ref(false)
const loadingUsage = ref(false)
const loadingCharts = ref(false)
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const recentUsage = ref<UsageLog[]>([])

// Group stats for dashboard
interface GroupStatDisplay extends GroupWindowStats { group_name: string }
const groupStatsList = ref<GroupStatDisplay[]>([])

const formatLD = (d: Date) => d.toISOString().split('T')[0]
const startDate = ref(formatLD(new Date(Date.now() - 6 * 86400000)))
const endDate = ref(formatLD(new Date()))
const granularity = ref('day')

const loadStats = async () => {
  loading.value = true
  try {
    await authStore.refreshUser()
    stats.value = await usageAPI.getDashboardStats()
  } catch (error) {
    console.error('Failed to load dashboard stats:', error)
  } finally {
    loading.value = false
  }
}

const loadGroupStats = async () => {
  try {
    const resp = await keysAPI.list(1, 200, {})
    const keys = resp.items
    const seen = new Map<number, string>()
    keys.forEach(k => { if (k.group?.id && !seen.has(k.group.id)) seen.set(k.group.id, k.group.name) })
    if (seen.size === 0) return
    const results = await Promise.allSettled([...seen.keys()].map(id => userGroupsAPI.getGroupStats(id)))
    const list: GroupStatDisplay[] = []
    let i = 0
    for (const [_, name] of seen) {
      const r = results[i++]
      if (r.status === 'fulfilled') list.push({ ...r.value, group_name: name })
    }
    groupStatsList.value = list
  } catch (e) {
    console.error('Failed to load group stats:', e)
  }
}

const loadCharts = async () => {
  loadingCharts.value = true
  try {
    const res = await Promise.all([
      usageAPI.getDashboardTrend({ start_date: startDate.value, end_date: endDate.value, granularity: granularity.value as any }),
      usageAPI.getDashboardModels({ start_date: startDate.value, end_date: endDate.value })
    ])
    trendData.value = res[0].trend || []
    modelStats.value = res[1].models || []
  } catch (error) {
    console.error('Failed to load charts:', error)
  } finally {
    loadingCharts.value = false
  }
}

const loadRecent = async () => {
  loadingUsage.value = true
  try {
    const res = await usageAPI.getByDateRange(startDate.value, endDate.value)
    recentUsage.value = res.items.slice(0, 5)
  } catch (error) {
    console.error('Failed to load recent usage:', error)
  } finally {
    loadingUsage.value = false
  }
}

const refreshAll = () => { loadStats(); loadCharts(); loadRecent(); loadGroupStats() }

onMounted(() => { refreshAll() })
</script>
