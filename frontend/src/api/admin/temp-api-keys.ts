/**
 * Admin Temp API Keys API endpoints
 * Handles temporary API key management for administrators
 */

import { apiClient } from '../client'
import type {
  TempApiKey,
  CreateTempApiKeyRequest,
  UpdateTempApiKeyRequest,
  BatchUpdateTempApiKeyRequest,
} from '@/types'

export interface TempApiKeyListResponse {
  data: TempApiKey[]
  pagination: {
    total: number
    page: number
    page_size: number
  }
}

/**
 * List all temp API keys with pagination
 */
export async function list(
  page: number = 1,
  pageSize: number = 20
): Promise<TempApiKeyListResponse> {
  const { data } = await apiClient.get('/admin/temp-api-keys', {
    params: { page, page_size: pageSize },
  })
  return data
}

/**
 * Get a single temp API key by ID
 */
export async function getById(id: number): Promise<TempApiKey> {
  const { data } = await apiClient.get(`/admin/temp-api-keys/${id}`)
  return data
}

/**
 * Create temp API keys in batch
 */
export async function create(
  req: CreateTempApiKeyRequest
): Promise<{ data: TempApiKey[]; created: number }> {
  const { data } = await apiClient.post('/admin/temp-api-keys', req)
  return data
}

/**
 * Update a temp API key
 */
export async function update(
  id: number,
  req: UpdateTempApiKeyRequest
): Promise<TempApiKey> {
  const { data } = await apiClient.put(`/admin/temp-api-keys/${id}`, req)
  return data
}

/**
 * Delete a temp API key
 */
export async function deleteKey(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete(`/admin/temp-api-keys/${id}`)
  return data
}

/**
 * Batch delete temp API keys
 */
export async function batchDelete(ids: number[]): Promise<{ deleted: number }> {
  const { data } = await apiClient.post('/admin/temp-api-keys/batch-delete', { ids })
  return data
}

/**
 * Batch update temp API keys
 */
export async function batchUpdate(
  req: BatchUpdateTempApiKeyRequest
): Promise<{ updated: number }> {
  const { data } = await apiClient.post('/admin/temp-api-keys/batch-update', req)
  return data
}

export const tempApiKeysAPI = {
  list,
  getById,
  create,
  update,
  delete: deleteKey,
  batchDelete,
  batchUpdate,
}
