import { apiClient } from './client'
import type { PublicModelChannel } from '@/types'

export async function getPublicChannels(): Promise<PublicModelChannel[]> {
  const { data } = await apiClient.get<PublicModelChannel[]>('/model-plaza')
  return data || []
}

export async function listGroupModels(groupId: number): Promise<string[]> {
  const { data } = await apiClient.get<string[]>(`/admin/groups/${groupId}/models`)
  return data || []
}

export async function createGroupModel(groupId: number, model: string): Promise<void> {
  await apiClient.post(`/admin/groups/${groupId}/models`, { model })
}

export async function updateGroupModel(
  groupId: number,
  currentModel: string,
  model: string
): Promise<void> {
  await apiClient.put(`/admin/groups/${groupId}/models`, {
    current_model: currentModel,
    model
  })
}

export async function deleteGroupModel(groupId: number, model: string): Promise<void> {
  await apiClient.delete(`/admin/groups/${groupId}/models`, { params: { model } })
}

export const modelPlazaAPI = {
  getPublicChannels,
  listGroupModels,
  createGroupModel,
  updateGroupModel,
  deleteGroupModel
}

export default modelPlazaAPI
