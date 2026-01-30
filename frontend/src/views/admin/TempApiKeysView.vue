<template>
  <AppLayout>
    <TablePageLayout>
      <template #actions>
        <div class="flex justify-end gap-3">
          <button
            @click="loadKeys"
            :disabled="loading"
            class="btn btn-secondary"
            :title="t('common.refresh')"
          >
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
          <button @click="showCreateDialog = true" class="btn btn-primary">
            {{ t('admin.tempApiKeys.create') }}
          </button>
        </div>
      </template>

      <template #filters>
        <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          <div class="flex items-center gap-2" v-if="selectedIds.length > 0">
            <span class="text-sm text-gray-600 dark:text-gray-400">
              {{ t('common.selected', { count: selectedIds.length }) }}
            </span>
            <button @click="copySelectedKeys" class="btn btn-secondary btn-sm">
              <Icon name="copy" size="sm" class="mr-1" />
              {{ t('admin.tempApiKeys.copyKeys') }}
            </button>
            <button @click="showBatchDialog = true" class="btn btn-secondary btn-sm">
              {{ t('admin.tempApiKeys.batchUpdate') }}
            </button>
            <button @click="handleBatchDelete" class="btn btn-danger btn-sm">
              {{ t('common.delete') }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="keys" :loading="loading">
          <template #header-select>
            <input
              type="checkbox"
              :checked="isAllSelected"
              :indeterminate="selectedIds.length > 0 && selectedIds.length < keys.length"
              @change="toggleSelectAll"
              class="checkbox"
            />
          </template>
          <template #cell-select="{ row }">
            <input
              type="checkbox"
              :checked="selectedIds.includes(row.id)"
              @change="toggleSelect(row.id)"
              class="checkbox"
            />
          </template>

          <template #cell-name="{ value, row }">
            <div class="flex flex-col">
              <span class="font-medium text-gray-900 dark:text-white">{{ value }}</span>
              <div class="flex items-center gap-1 mt-1">
                <code class="text-xs bg-gray-100 dark:bg-gray-800 px-1.5 py-0.5 rounded font-mono truncate max-w-[150px]">
                  {{ row.key }}
                </code>
                <button
                  @click="copyKey(row.key)"
                  class="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
                  :title="t('keys.copyToClipboard')"
                >
                  <Icon name="copy" size="sm" />
                </button>
              </div>
            </div>
          </template>

          <template #cell-group_name="{ value, row }">
            <span class="badge badge-primary">{{ value || `Group #${row.group_id}` }}</span>
          </template>

          <template #cell-status="{ row }">
            <span
              :class="[
                'badge',
                row.is_exhausted || row.status === 'exhausted' ? 'badge-warning' :
                row.is_expired ? 'badge-danger' :
                row.status === 'inactive' ? 'badge-secondary' : 'badge-success'
              ]"
            >
              {{ row.is_exhausted || row.status === 'exhausted' ? t('admin.tempApiKeys.exhausted') :
                 row.is_expired ? t('admin.tempApiKeys.expired') :
                 row.status === 'inactive' ? t('admin.tempApiKeys.inactive') : t('admin.tempApiKeys.active') }}
            </span>
          </template>

          <template #cell-validity="{ row }">
            <div class="text-sm">
              <template v-if="row.key_type === 'quota_only'">
                <span class="badge badge-info">{{ t('admin.tempApiKeys.quotaOnly') }}</span>
                <div class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.tempApiKeys.quota') }}: {{ row.total_requests }} / {{ row.total_quota }}
                </div>
              </template>
              <template v-else-if="row.is_activated">
                <span>{{ row.valid_days }}{{ t('admin.tempApiKeys.days') }}</span>
                <div class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.tempApiKeys.expiresAt') }}: {{ formatDate(row.expires_at) }}
                </div>
              </template>
              <template v-else>
                <span class="text-gray-500 dark:text-gray-400">
                  {{ row.valid_days }}{{ t('admin.tempApiKeys.days') }} ({{ t('admin.tempApiKeys.notActivated') }})
                </span>
              </template>
            </div>
          </template>

          <template #cell-usage="{ row }">
            <template v-if="row.key_type === 'quota_only'">
              <span :class="row.remaining_requests === 0 ? 'text-red-500' : ''">
                {{ row.remaining_requests === -1 ? '∞' : row.remaining_requests }}
              </span>
            </template>
            <template v-else>
              <span :class="row.remaining_requests === 0 ? 'text-red-500' : ''">
                {{ row.current_period_count }} / {{ row.daily_limit }}
              </span>
            </template>
          </template>

          <template #cell-total_requests="{ value }">
            {{ value }}
          </template>

          <template #cell-created_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-gray-400">{{ formatDate(value) }}</span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-1">
              <button
                @click="handleEdit(row)"
                class="btn btn-ghost btn-sm"
                :title="t('common.edit')"
              >
                <Icon name="edit" size="sm" />
              </button>
              <button
                @click="handleDelete(row.id)"
                class="btn btn-ghost btn-sm text-red-500 hover:text-red-600"
                :title="t('common.delete')"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="!loading"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:page-size="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <!-- Create Dialog -->
    <BaseDialog
      :show="showCreateDialog"
      :title="t('admin.tempApiKeys.createTitle')"
      @close="showCreateDialog = false"
    >
      <form @submit.prevent="handleCreate" class="space-y-4">
        <div>
          <label class="label">{{ t('admin.tempApiKeys.namePrefix') }} *</label>
          <input
            v-model="createForm.name_prefix"
            type="text"
            class="input"
            :placeholder="t('admin.tempApiKeys.namePrefixPlaceholder')"
            required
          />
        </div>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.count') }} *</label>
          <input
            v-model.number="createForm.count"
            type="number"
            min="1"
            max="100"
            class="input"
            required
          />
        </div>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.group') }} *</label>
          <Select
            v-model="createForm.group_id"
            :options="groupOptions"
            :placeholder="t('admin.tempApiKeys.selectGroup')"
          />
        </div>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.keyType') }}</label>
          <Select
            v-model="createForm.key_type"
            :options="keyTypeOptions"
          />
        </div>
        <template v-if="createForm.key_type === 'time_limited'">
          <div>
            <label class="label">{{ t('admin.tempApiKeys.validDays') }} *</label>
            <input
              v-model.number="createForm.valid_days"
              type="number"
              min="1"
              class="input"
            />
          </div>
          <div>
            <label class="label">{{ t('admin.tempApiKeys.dailyLimit') }}</label>
            <input
              v-model.number="createForm.daily_limit"
              type="number"
              min="1"
              class="input"
            />
          </div>
        </template>
        <template v-else>
          <div>
            <label class="label">{{ t('admin.tempApiKeys.totalQuota') }} *</label>
            <input
              v-model.number="createForm.total_quota"
              type="number"
              min="1"
              class="input"
              :placeholder="t('admin.tempApiKeys.totalQuotaPlaceholder')"
            />
          </div>
        </template>
      </form>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" @click="showCreateDialog = false" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button @click="handleCreate" class="btn btn-primary" :disabled="creating">
            {{ creating ? t('common.creating') : t('common.create') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Edit Dialog -->
    <BaseDialog
      :show="showEditDialog"
      :title="t('admin.tempApiKeys.editTitle')"
      @close="showEditDialog = false"
    >
      <form @submit.prevent="handleUpdate" class="space-y-4">
        <div>
          <label class="label">{{ t('admin.tempApiKeys.name') }}</label>
          <input v-model="editForm.name" type="text" class="input" />
        </div>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.status') }}</label>
          <Select
            v-model="editForm.status"
            :options="statusOptions"
          />
        </div>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.validDays') }}</label>
          <input v-model.number="editForm.valid_days" type="number" min="1" class="input" />
        </div>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.dailyLimit') }}</label>
          <input v-model.number="editForm.daily_limit" type="number" min="1" class="input" />
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" @click="showEditDialog = false" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button @click="handleUpdate" class="btn btn-primary" :disabled="updating">
            {{ updating ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Batch Update Dialog -->
    <BaseDialog
      :show="showBatchDialog"
      :title="t('admin.tempApiKeys.batchUpdateTitle', { count: selectedIds.length })"
      @close="showBatchDialog = false"
    >
      <form @submit.prevent="handleBatchUpdate" class="space-y-4">
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.tempApiKeys.batchUpdateHint') }}
        </p>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.status') }}</label>
          <Select
            v-model="batchForm.status"
            :options="batchStatusOptions"
          />
        </div>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.validDays') }}</label>
          <input
            v-model.number="batchForm.valid_days"
            type="number"
            min="1"
            class="input"
            :placeholder="t('admin.tempApiKeys.noChange')"
          />
        </div>
        <div>
          <label class="label">{{ t('admin.tempApiKeys.dailyLimit') }}</label>
          <input
            v-model.number="batchForm.daily_limit"
            type="number"
            min="1"
            class="input"
            :placeholder="t('admin.tempApiKeys.noChange')"
          />
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" @click="showBatchDialog = false" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button @click="handleBatchUpdate" class="btn btn-primary" :disabled="updating">
            {{ updating ? t('common.saving') : t('admin.tempApiKeys.batchUpdate') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { tempApiKeysAPI } from '@/api/admin/temp-api-keys'
import { groupsAPI } from '@/api/admin/groups'
import type { TempApiKey, AdminGroup } from '@/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const appStore = useAppStore()

const keys = ref<TempApiKey[]>([])
const groups = ref<AdminGroup[]>([])
const loading = ref(false)
const creating = ref(false)
const updating = ref(false)
const pagination = ref({ total: 0, page: 1, page_size: 20 })

// Dialogs
const showCreateDialog = ref(false)
const showEditDialog = ref(false)
const showBatchDialog = ref(false)

// Forms
const createForm = ref({
  count: 1,
  name_prefix: '',
  group_id: 0,
  key_type: 'time_limited' as 'time_limited' | 'quota_only',
  valid_days: 7,
  daily_limit: 1000,
  total_quota: 1000,
})

const editingKey = ref<TempApiKey | null>(null)
const editForm = ref({
  name: '',
  status: 'active' as 'active' | 'inactive' | 'exhausted',
  valid_days: 7,
  daily_limit: 1000,
  total_quota: 0,
})

const batchForm = ref({
  status: '' as '' | 'active' | 'inactive',
  valid_days: null as number | null,
  daily_limit: null as number | null,
})

// Selection
const selectedIds = ref<number[]>([])

// Options
const groupOptions = computed(() =>
  groups.value.map((g) => ({ value: g.id, label: g.name }))
)

const keyTypeOptions = [
  { value: 'time_limited', label: 'Time Limited' },
  { value: 'quota_only', label: 'Quota Only' },
]

const statusOptions = [
  { value: 'active', label: 'Active' },
  { value: 'inactive', label: 'Inactive' },
]

const batchStatusOptions = computed(() => [
  { value: '', label: t('admin.tempApiKeys.noChange') },
  { value: 'active', label: 'Active' },
  { value: 'inactive', label: 'Inactive' },
])

// Table columns
const columns = computed(() => [
  { key: 'select', label: '', width: '40px' },
  { key: 'name', label: t('admin.tempApiKeys.name'), sortable: true },
  { key: 'group_name', label: t('admin.tempApiKeys.group') },
  { key: 'status', label: t('admin.tempApiKeys.status') },
  { key: 'validity', label: t('admin.tempApiKeys.validity') },
  { key: 'usage', label: t('admin.tempApiKeys.todayUsage') },
  { key: 'total_requests', label: t('admin.tempApiKeys.totalRequests') },
  { key: 'created_at', label: t('common.createdAt') },
  { key: 'actions', label: t('common.actions'), width: '100px' },
])

// Methods
const loadKeys = async () => {
  loading.value = true
  try {
    const res = await tempApiKeysAPI.list(pagination.value.page, pagination.value.page_size)
    keys.value = res.data || []
    pagination.value = res.pagination || { total: 0, page: 1, page_size: 20 }
  } catch (e: unknown) {
    appStore.showError((e as Error).message || t('admin.tempApiKeys.loadFailed'))
  } finally {
    loading.value = false
  }
}

const loadGroups = async () => {
  try {
    groups.value = await groupsAPI.getAll()
  } catch (e) {
    console.error('Failed to load groups', e)
  }
}

const handlePageChange = (page: number) => {
  pagination.value.page = page
  loadKeys()
}

const handlePageSizeChange = (pageSize: number) => {
  pagination.value.page_size = pageSize
  pagination.value.page = 1
  loadKeys()
}

const handleCreate = async () => {
  if (!createForm.value.name_prefix || !createForm.value.group_id) {
    appStore.showError(t('admin.tempApiKeys.fillRequired'))
    return
  }
  creating.value = true
  try {
    const res = await tempApiKeysAPI.create(createForm.value)
    // 自动复制所有新建 key 到剪贴板
    if (res.data && res.data.length > 0) {
      const keysText = res.data.map((k: TempApiKey) => k.key).join('\n')
      await navigator.clipboard.writeText(keysText)
      appStore.showSuccess(t('admin.tempApiKeys.createSuccessAndCopied', { count: res.created }))
    } else {
      appStore.showSuccess(t('admin.tempApiKeys.createSuccess', { count: res.created }))
    }
    showCreateDialog.value = false
    createForm.value = { count: 1, name_prefix: '', group_id: 0, valid_days: 7, daily_limit: 1000 }
    loadKeys()
  } catch (e: unknown) {
    appStore.showError((e as Error).message || t('admin.tempApiKeys.createFailed'))
  } finally {
    creating.value = false
  }
}

const handleEdit = (key: TempApiKey) => {
  editingKey.value = key
  editForm.value = {
    name: key.name,
    status: key.status === 'expired' ? 'inactive' : key.status,
    valid_days: key.valid_days,
    daily_limit: key.daily_limit,
  }
  showEditDialog.value = true
}

const handleUpdate = async () => {
  if (!editingKey.value) return
  updating.value = true
  try {
    await tempApiKeysAPI.update(editingKey.value.id, editForm.value)
    appStore.showSuccess(t('admin.tempApiKeys.updateSuccess'))
    showEditDialog.value = false
    loadKeys()
  } catch (e: unknown) {
    appStore.showError((e as Error).message || t('admin.tempApiKeys.updateFailed'))
  } finally {
    updating.value = false
  }
}

const handleDelete = async (id: number) => {
  if (!confirm(t('admin.tempApiKeys.confirmDelete'))) return
  try {
    await tempApiKeysAPI.delete(id)
    appStore.showSuccess(t('admin.tempApiKeys.deleteSuccess'))
    loadKeys()
  } catch (e: unknown) {
    appStore.showError((e as Error).message || t('admin.tempApiKeys.deleteFailed'))
  }
}

const handleBatchDelete = async () => {
  if (selectedIds.value.length === 0) return
  if (!confirm(t('admin.tempApiKeys.confirmBatchDelete', { count: selectedIds.value.length }))) return
  try {
    const res = await tempApiKeysAPI.batchDelete(selectedIds.value)
    appStore.showSuccess(t('admin.tempApiKeys.batchDeleteSuccess', { count: res.deleted }))
    selectedIds.value = []
    loadKeys()
  } catch (e: unknown) {
    appStore.showError((e as Error).message || t('admin.tempApiKeys.deleteFailed'))
  }
}

const handleBatchUpdate = async () => {
  if (selectedIds.value.length === 0) return
  const data: { ids: number[]; status?: 'active' | 'inactive'; valid_days?: number; daily_limit?: number } = {
    ids: selectedIds.value,
  }
  if (batchForm.value.status === 'active' || batchForm.value.status === 'inactive') {
    data.status = batchForm.value.status
  }
  if (batchForm.value.valid_days !== null) data.valid_days = batchForm.value.valid_days
  if (batchForm.value.daily_limit !== null) data.daily_limit = batchForm.value.daily_limit

  updating.value = true
  try {
    const res = await tempApiKeysAPI.batchUpdate(data)
    appStore.showSuccess(t('admin.tempApiKeys.batchUpdateSuccess', { count: res.updated }))
    showBatchDialog.value = false
    batchForm.value = { status: '', valid_days: null, daily_limit: null }
    selectedIds.value = []
    loadKeys()
  } catch (e: unknown) {
    appStore.showError((e as Error).message || t('admin.tempApiKeys.updateFailed'))
  } finally {
    updating.value = false
  }
}

const toggleSelect = (id: number) => {
  const idx = selectedIds.value.indexOf(id)
  if (idx === -1) {
    selectedIds.value.push(id)
  } else {
    selectedIds.value.splice(idx, 1)
  }
}

const isAllSelected = computed(() => {
  return keys.value.length > 0 && selectedIds.value.length === keys.value.length
})

const toggleSelectAll = () => {
  if (isAllSelected.value) {
    selectedIds.value = []
  } else {
    selectedIds.value = keys.value.map(k => k.id)
  }
}

const copyKey = (key: string) => {
  navigator.clipboard.writeText(key)
  appStore.showSuccess(t('keys.copiedToClipboard'))
}

const copySelectedKeys = async () => {
  if (selectedIds.value.length === 0) return
  const selectedKeys = keys.value
    .filter(k => selectedIds.value.includes(k.id))
    .map(k => k.key)
  await navigator.clipboard.writeText(selectedKeys.join('\n'))
  appStore.showSuccess(t('admin.tempApiKeys.keysCopied', { count: selectedKeys.length }))
}

const formatDate = (date: string | null) => {
  if (!date) return '-'
  return new Date(date).toLocaleString()
}

onMounted(() => {
  loadKeys()
  loadGroups()
})
</script>
