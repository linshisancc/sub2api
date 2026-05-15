<template>
  <div class="rounded-xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
    <!-- Header -->
    <div class="flex items-center justify-between border-b border-gray-100 px-4 py-3 dark:border-dark-700">
      <h3 class="text-sm font-semibold text-gray-700 dark:text-dark-200">{{ t('dashboard.groupAccounts.title') }}</h3>
      <span v-if="!loading && groups.length" class="text-xs text-gray-400 dark:text-dark-500">
        {{ groups.length }} {{ t('dashboard.groupAccounts.groups') }}
      </span>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="flex items-center justify-center py-8">
      <div class="h-5 w-5 animate-spin rounded-full border-2 border-gray-300 border-t-blue-500"></div>
    </div>

    <!-- Empty -->
    <div v-else-if="!groups.length" class="px-4 py-6 text-center text-xs text-gray-400 dark:text-dark-500">
      {{ t('dashboard.groupAccounts.empty') }}
    </div>

    <!-- Groups list -->
    <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
      <div v-for="group in groups" :key="group.id">
        <!-- Group header row -->
        <button
          type="button"
          class="flex w-full items-center gap-2 px-4 py-2.5 text-left hover:bg-gray-50 dark:hover:bg-dark-700 transition-colors"
          @click="toggleGroup(group.id)"
        >
          <svg
            class="h-3.5 w-3.5 shrink-0 text-gray-400 transition-transform duration-150"
            :class="expandedGroups.has(group.id) ? 'rotate-90' : ''"
            fill="none" stroke="currentColor" viewBox="0 0 24 24"
          >
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7" />
          </svg>
          <span class="flex-1 truncate text-sm font-medium text-gray-800 dark:text-dark-100">{{ group.name }}</span>
          <span class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
            :class="platformBadgeClass(group.platform)">
            {{ group.platform }}
          </span>
          <span v-if="groupBadgeCount(group) != null" class="shrink-0 text-xs text-gray-400 dark:text-dark-500">
            {{ groupBadgeCount(group) }}
          </span>
        </button>

        <!-- Accounts panel -->
        <div v-if="expandedGroups.has(group.id)" class="px-4 pb-3 pt-1">
          <!-- Loading accounts -->
          <div v-if="groupLoadingState[group.id]" class="flex items-center gap-2 py-2 text-xs text-gray-400">
            <div class="h-3.5 w-3.5 animate-spin rounded-full border-2 border-gray-300 border-t-blue-400"></div>
            <span>{{ t('common.loading') }}</span>
          </div>
          <!-- No accounts -->
          <div v-else-if="!groupAccounts[group.id]?.length" class="py-2 text-xs text-gray-400 dark:text-dark-500">
            {{ t('dashboard.groupAccounts.noAccounts') }}
          </div>
          <!-- Account rows -->
          <div v-else class="space-y-2.5">
            <div
              v-for="account in groupAccounts[group.id]"
              :key="account.id"
              class="flex items-start gap-3 rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700"
            >
              <div class="flex-1 min-w-0">
                <p class="truncate text-xs font-medium text-gray-700 dark:text-dark-200">
                  {{ account.name || `#${account.id}` }}
                </p>
                <p class="text-[10px] text-gray-400 dark:text-dark-500">{{ account.type }}</p>
              </div>
              <div class="shrink-0">
                <AccountUsageCell :account="account" :usage-fetcher="usageFetcher" />
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { userGroupsAPI } from '@/api/groups'
import type { AdminGroup, Account, Group, AccountUsageInfo } from '@/types'
import AccountUsageCell from '@/components/account/AccountUsageCell.vue'

type WidgetMode = 'admin' | 'user'

const props = withDefaults(
  defineProps<{
    mode?: WidgetMode
  }>(),
  { mode: 'admin' }
)

const { t } = useI18n()

// Loose group shape that covers both AdminGroup and user-side Group.
// Only id/name/platform are required by the widget; active_account_count is admin-only.
type WidgetGroup = (AdminGroup | Group) & { active_account_count?: number }
// User-mode endpoints return a minimal summary; admin endpoint returns full Account.
// AccountUsageCell only reads id/name/type/platform, so we cast user summaries through
// Account at assembly time rather than threading a union type everywhere.
type WidgetAccount = Account

const loading = ref(false)
const groups = ref<WidgetGroup[]>([])
const expandedGroups = ref(new Set<number>())
const groupAccounts = ref<Record<number, WidgetAccount[]>>({})
const groupLoadingState = ref<Record<number, boolean>>({})

const fetchGroups = async (): Promise<WidgetGroup[]> => {
  if (props.mode === 'user') {
    return (await userGroupsAPI.getAvailable()) as WidgetGroup[]
  }
  return (await adminAPI.groups.getAll()) as WidgetGroup[]
}

const fetchGroupAccounts = async (groupId: number): Promise<WidgetAccount[]> => {
  if (props.mode === 'user') {
    const summaries = await userGroupsAPI.getGroupAccounts(groupId)
    return summaries as unknown as WidgetAccount[]
  }
  const res = await adminAPI.accounts.list(1, 50, { group: String(groupId), status: 'active' })
  return (res.items || []) as WidgetAccount[]
}

// Fetcher passed into AccountUsageCell — only override in user mode so admin
// callers keep the default adminAPI.accounts.getUsage behavior.
const usageFetcher =
  props.mode === 'user'
    ? (id: number, source?: 'passive' | 'active'): Promise<AccountUsageInfo> =>
        userGroupsAPI.getAccountUsage(id, source)
    : undefined

const platformBadgeClass = (platform: string) => {
  const map: Record<string, string> = {
    anthropic: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300',
    openai: 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300',
    gemini: 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300',
    antigravity: 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300',
  }
  return map[platform] ?? 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
}

const groupBadgeCount = (group: WidgetGroup): number | null => {
  // Prefer admin-provided count when available; otherwise fall back to loaded accounts length.
  if (typeof group.active_account_count === 'number') return group.active_account_count
  const loaded = groupAccounts.value[group.id]
  return loaded ? loaded.length : null
}

const loadAccounts = async (groupId: number) => {
  if (groupAccounts.value[groupId] !== undefined || groupLoadingState.value[groupId]) return
  groupLoadingState.value[groupId] = true
  try {
    groupAccounts.value[groupId] = await fetchGroupAccounts(groupId)
  } catch {
    groupAccounts.value[groupId] = []
  } finally {
    groupLoadingState.value[groupId] = false
  }
}

const toggleGroup = (groupId: number) => {
  if (expandedGroups.value.has(groupId)) {
    expandedGroups.value.delete(groupId)
  } else {
    expandedGroups.value.add(groupId)
    loadAccounts(groupId)
  }
}

onMounted(async () => {
  loading.value = true
  try {
    groups.value = await fetchGroups()
    // Auto-expand every group and trigger lazy loads in parallel.
    // loadAccounts() caches per-group, so concurrent calls won't duplicate requests.
    for (const g of groups.value) {
      expandedGroups.value.add(g.id)
    }
    await Promise.all(groups.value.map((g) => loadAccounts(g.id)))
  } catch {
    groups.value = []
  } finally {
    loading.value = false
  }
})
</script>
