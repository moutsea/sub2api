/**
 * API Keys management endpoints
 * Handles CRUD operations for user API keys
 */

import { apiClient } from './client'
import type { ApiKey, CreateApiKeyRequest, UpdateApiKeyRequest, PaginatedResponse } from '@/types'

/**
 * List all API keys for current user
 * @param page - Page number (default: 1)
 * @param pageSize - Items per page (default: 10)
 * @param options - Optional request options
 * @returns Paginated list of API keys
 */
export async function list(
  page: number = 1,
  pageSize: number = 10,
  options?: {
    signal?: AbortSignal
    groupId?: number | null
  }
): Promise<PaginatedResponse<ApiKey>> {
  const params: Record<string, any> = { page, page_size: pageSize }
  if (options?.groupId !== undefined && options.groupId !== null) {
    params.group_id = options.groupId
  }
  const { data } = await apiClient.get<PaginatedResponse<ApiKey>>('/keys', {
    params,
    signal: options?.signal
  })
  return data
}

/**
 * Get API key by ID
 * @param id - API key ID
 * @returns API key details
 */
export async function getById(id: number): Promise<ApiKey> {
  const { data } = await apiClient.get<ApiKey>(`/keys/${id}`)
  return data
}

/**
 * Create new API key
 * @param name - Key name
 * @param groupId - Optional group ID
 * @param customKey - Optional custom key value
 * @param ipWhitelist - Optional IP whitelist
 * @param ipBlacklist - Optional IP blacklist
 * @returns Created API key
 */
export async function create(
  name: string,
  groupId?: number | null,
  customKey?: string,
  ipWhitelist?: string[],
  ipBlacklist?: string[]
): Promise<ApiKey> {
  const payload: CreateApiKeyRequest = { name }
  if (groupId !== undefined) {
    payload.group_id = groupId
  }
  if (customKey) {
    payload.custom_key = customKey
  }
  if (ipWhitelist && ipWhitelist.length > 0) {
    payload.ip_whitelist = ipWhitelist
  }
  if (ipBlacklist && ipBlacklist.length > 0) {
    payload.ip_blacklist = ipBlacklist
  }

  const { data } = await apiClient.post<ApiKey>('/keys', payload)
  return data
}

/**
 * Update API key
 * @param id - API key ID
 * @param updates - Fields to update
 * @returns Updated API key
 */
export async function update(id: number, updates: UpdateApiKeyRequest): Promise<ApiKey> {
  const { data } = await apiClient.put<ApiKey>(`/keys/${id}`, updates)
  return data
}

/**
 * Delete API key
 * @param id - API key ID
 * @returns Success confirmation
 */
export async function deleteKey(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/keys/${id}`)
  return data
}

export async function resetQuota(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>(`/keys/${id}/reset-quota`)
  return data
}

/**
 * Bulk update group for multiple API keys
 * @param keyIds - Array of API key IDs
 * @param groupId - Group ID to assign, or null to remove group
 * @returns Success and failed counts
 */
export async function bulkUpdateGroup(
  keyIds: number[],
  groupId: number | null
): Promise<{ success: number; failed: number }> {
  const { data } = await apiClient.put<{ success: number; failed: number }>(
    '/keys/bulk-update-group',
    { key_ids: keyIds, group_id: groupId }
  )
  return data
}

/**
 * Toggle API key status (active/inactive)
 * @param id - API key ID
 * @param status - New status
 * @returns Updated API key
 */
export async function toggleStatus(id: number, status: 'active' | 'inactive'): Promise<ApiKey> {
  return update(id, { status })
}

export const keysAPI = {
  list,
  getById,
  create,
  update,
  delete: deleteKey,
  toggleStatus,
  resetQuota,
  bulkUpdateGroup
}

export default keysAPI
