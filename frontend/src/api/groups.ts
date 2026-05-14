/**
 * User Groups API endpoints (non-admin)
 * Handles group-related operations for regular users
 */

import { apiClient } from './client'
import type { Group } from '@/types'

/**
 * Get available groups that the current user can bind to API keys
 * This returns groups based on user's permissions:
 * - Standard groups: public (non-exclusive) or explicitly allowed
 * - Subscription groups: user has active subscription
 * @returns List of available groups
 */
export async function getAvailable(): Promise<Group[]> {
  const { data } = await apiClient.get<Group[]>('/groups/available')
  return data
}

/**
 * Get current user's custom group rate multipliers
 * @returns Map of group_id to custom rate_multiplier
 */
export async function getUserGroupRates(): Promise<Record<number, number>> {
  const { data } = await apiClient.get<Record<number, number> | null>('/groups/rates')
  return data || {}
}

export interface GroupWindowStats {
  group_id: number
  requests_5h: number
  actual_cost_5h: number
  requests_7d: number
  actual_cost_7d: number
}

/**
 * Get 5h/7d aggregated usage stats for a group the user has access to
 */
export async function getGroupStats(groupId: number): Promise<GroupWindowStats> {
  const { data } = await apiClient.get<GroupWindowStats>(`/groups/${groupId}/stats`)
  return data
}

export const userGroupsAPI = {
  getAvailable,
  getUserGroupRates,
  getGroupStats
}

export default userGroupsAPI
