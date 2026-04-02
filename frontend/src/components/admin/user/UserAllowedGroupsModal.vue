<template>
  <BaseDialog :show="show" :title="t('admin.users.setAllowedGroups')" width="normal" @close="$emit('close')">
    <div v-if="user" class="space-y-4">
      <div class="flex items-center gap-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <div class="flex h-10 w-10 items-center justify-center rounded-full bg-primary-100">
          <span class="text-lg font-medium text-primary-700">{{ user.email.charAt(0).toUpperCase() }}</span>
        </div>
        <p class="font-medium text-gray-900 dark:text-white">{{ user.email }}</p>
      </div>
      <div v-if="loading" class="flex justify-center py-8">
        <svg class="h-8 w-8 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24">
          <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
          <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
        </svg>
      </div>
      <div v-else>
        <p class="mb-3 text-sm text-gray-600 dark:text-gray-400">{{ t('admin.users.allowedGroupsHint') }}</p>
        <div class="max-h-80 space-y-2 overflow-y-auto">
          <div
            v-for="group in groups"
            :key="group.id"
            class="rounded-lg border border-gray-200 p-3 transition-colors dark:border-dark-600"
            :class="{ 'border-primary-300 bg-primary-50 dark:border-primary-700 dark:bg-primary-900/20': selectedIds.includes(group.id) }"
          >
            <label class="flex cursor-pointer items-center gap-3">
              <input
                type="checkbox"
                :value="group.id"
                v-model="selectedIds"
                class="h-4 w-4 rounded border-gray-300 text-primary-600"
              />
              <div class="flex-1">
                <p class="font-medium text-gray-900 dark:text-white">{{ group.name }}</p>
                <p v-if="group.description" class="truncate text-sm text-gray-500 dark:text-gray-400">{{ group.description }}</p>
              </div>
              <div class="flex items-center gap-2">
                <span class="badge badge-gray text-xs">{{ group.platform }}</span>
                <span v-if="group.is_exclusive" class="badge badge-purple text-xs">{{ t('admin.groups.exclusive') }}</span>
              </div>
            </label>
            <!-- Custom rate multiplier input (shown when group is selected) -->
            <div
              v-if="selectedIds.includes(group.id)"
              class="mt-2 flex items-center gap-3 border-t border-gray-100 pt-2 pl-7 dark:border-dark-600"
            >
              <span class="text-xs text-gray-500 dark:text-gray-400 whitespace-nowrap">
                {{ t('admin.users.groupDefaultRate') }}: {{ group.rate_multiplier }}x
              </span>
              <div class="flex items-center gap-1.5">
                <span class="text-xs text-gray-600 dark:text-gray-300 whitespace-nowrap">{{ t('admin.users.customRate') }}:</span>
                <input
                  type="number"
                  step="0.01"
                  min="0"
                  :value="customRates[group.id] ?? ''"
                  @input="(e) => setCustomRate(group.id, (e.target as HTMLInputElement).value)"
                  :placeholder="t('admin.users.customRatePlaceholder')"
                  class="input w-24 px-2 py-1 text-xs"
                />
              </div>
            </div>
          </div>
        </div>
        <div class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-600">
          <label
            class="flex cursor-pointer items-center gap-3 rounded-lg border border-gray-200 p-3 hover:bg-gray-50 dark:border-dark-600 dark:hover:bg-dark-700"
            :class="{ 'border-green-300 bg-green-50 dark:border-green-700 dark:bg-green-900/20': selectedIds.length === 0 }"
          >
            <input
              type="radio"
              :checked="selectedIds.length === 0"
              @change="clearAllGroups"
              class="h-4 w-4 border-gray-300 text-green-600"
            />
            <div class="flex-1">
              <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.users.allowAllGroups') }}</p>
              <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.users.allowAllGroupsHint') }}</p>
            </div>
          </label>
        </div>
      </div>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" class="btn btn-secondary">{{ t('common.cancel') }}</button>
        <button @click="handleSave" :disabled="submitting" class="btn btn-primary">
          {{ submitting ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AdminUser, Group } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits(['close', 'success'])
const { t } = useI18n()
const appStore = useAppStore()

const groups = ref<Group[]>([])
const selectedIds = ref<number[]>([])
const customRates = reactive<Record<number, number | null>>({})
const loading = ref(false)
const submitting = ref(false)

const setCustomRate = (groupId: number, value: string) => {
  if (value === '' || value === null || value === undefined) {
    customRates[groupId] = null
  } else {
    const num = parseFloat(value)
    if (isNaN(num) || num < 0) {
      customRates[groupId] = null
    } else {
      customRates[groupId] = num
    }
  }
}

const clearAllGroups = () => {
  selectedIds.value = []
  Object.keys(customRates).forEach((k) => delete customRates[Number(k)])
}

watch(
  () => props.show,
  async (v) => {
    if (v && props.user) {
      selectedIds.value = props.user.allowed_groups || []
      // Clear previous custom rates
      Object.keys(customRates).forEach((k) => delete customRates[Number(k)])
      await load()
    }
  }
)

const load = async () => {
  if (!props.user) return
  loading.value = true
  try {
    // Load groups and custom rates in parallel
    const [groupsRes, ratesRes] = await Promise.all([
      adminAPI.groups.list(1, 1000),
      adminAPI.users.getUserGroupRates(props.user.id)
    ])
    groups.value = groupsRes.items.filter(
      (g) => g.subscription_type === 'standard' && g.status === 'active'
    )
    // Populate custom rates from API response
    for (const rate of ratesRes) {
      if (rate.custom_rate !== null && rate.custom_rate !== undefined) {
        customRates[rate.group_id] = rate.custom_rate
      }
    }
  } catch (error) {
    console.error('Failed to load groups:', error)
  } finally {
    loading.value = false
  }
}

const handleSave = async () => {
  if (!props.user) return
  submitting.value = true
  try {
    // 1. Update allowed groups
    await adminAPI.users.update(props.user.id, { allowed_groups: selectedIds.value })

    // 2. Update custom rates for selected groups
    if (selectedIds.value.length > 0) {
      const rates = selectedIds.value.map((groupId) => ({
        group_id: groupId,
        custom_rate: customRates[groupId] ?? null
      }))
      try {
        await adminAPI.users.updateUserGroupRates(props.user.id, rates)
      } catch (rateError) {
        console.error('Failed to update group rates:', rateError)
        appStore.showError(t('admin.users.failedToUpdateGroupRates'))
        // Groups were saved but rates failed — still refresh the list
        emit('success')
        emit('close')
        return
      }
    }

    appStore.showSuccess(t('admin.users.allowedGroupsUpdated'))
    emit('success')
    emit('close')
  } catch (error) {
    console.error('Failed to update allowed groups:', error)
    appStore.showError(t('admin.users.failedToUpdateAllowedGroups'))
  } finally {
    submitting.value = false
  }
}
</script>
