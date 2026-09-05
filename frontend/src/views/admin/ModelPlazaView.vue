<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <div class="mb-2 flex items-center gap-2 text-primary-600 dark:text-primary-400">
            <Icon name="cube" size="md" />
            <span class="text-xs font-semibold uppercase tracking-[0.2em]">AI Catalog</span>
          </div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('admin.modelPlaza.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.modelPlaza.description') }}</p>
        </div>
        <button class="btn btn-primary" :disabled="!selectedGroup" @click="openCreate">
          <Icon name="plus" size="sm" class="mr-2" />{{ t('admin.modelPlaza.addModel') }}
        </button>
      </div>

      <div v-if="loading" class="card flex items-center justify-center p-12">
        <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></div>
      </div>

      <div v-else-if="groups.length === 0" class="card p-12 text-center">
        <Icon name="cube" size="xl" class="mx-auto mb-4 text-gray-300 dark:text-dark-600" />
        <p class="text-lg font-medium text-gray-900 dark:text-white">{{ t('admin.modelPlaza.noChannels') }}</p>
      </div>

      <template v-else>
        <div class="card p-4">
          <label class="input-label">{{ t('admin.modelPlaza.channel') }}</label>
          <select v-model="selectedGroupId" class="input mt-2 max-w-xl">
            <option :value="null">{{ t('admin.modelPlaza.selectChannel') }}</option>
            <option v-for="group in groups" :key="group.id" :value="group.id">
              {{ group.name }} · {{ formatMultiplier(group.rate_multiplier) }}x
            </option>
          </select>
        </div>

        <div v-if="selectedGroup" class="card overflow-hidden">
          <div class="flex flex-col gap-3 border-b border-gray-100 p-5 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ selectedGroup.name }}</h2>
              <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('modelPlaza.modelCount', { count: models.length }) }}</p>
            </div>
            <span class="rounded-lg bg-primary-50 px-3 py-1.5 text-sm font-semibold text-primary-700 dark:bg-primary-900/20 dark:text-primary-300">{{ formatMultiplier(selectedGroup.rate_multiplier) }}x</span>
          </div>

          <div v-if="models.length === 0" class="p-12 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.modelPlaza.empty') }}</div>
          <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
            <div v-for="model in models" :key="model" class="flex items-center justify-between gap-4 px-5 py-4">
              <code class="min-w-0 truncate text-sm text-gray-800 dark:text-dark-100">{{ model }}</code>
              <div v-if="isManagedModel(model)" class="flex flex-shrink-0 items-center gap-1">
                <button class="btn btn-ghost btn-sm" :title="t('common.edit')" @click="openEdit(model)"><Icon name="edit" size="sm" /></button>
                <button class="btn btn-ghost btn-sm text-red-500 hover:bg-red-50 dark:hover:bg-red-900/20" :title="t('common.delete')" @click="deleteModel(model)"><Icon name="trash" size="sm" /></button>
              </div>
              <span v-else class="flex-shrink-0 text-xs text-gray-400 dark:text-dark-500">{{ t('admin.modelPlaza.autoModel') }}</span>
            </div>
          </div>
        </div>
        <div v-else class="card p-12 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.modelPlaza.chooseChannel') }}</div>
      </template>
    </div>

    <BaseDialog :show="dialogOpen" :title="editingModel ? t('admin.modelPlaza.editModel') : t('admin.modelPlaza.addModel')" @close="closeDialog">
      <form class="space-y-4" @submit.prevent="saveModel">
        <div>
          <label class="input-label">{{ t('admin.modelPlaza.modelName') }}</label>
          <input v-model="modelInput" class="input mt-2 font-mono" :placeholder="t('admin.modelPlaza.modelPlaceholder')" autofocus />
          <p class="input-hint">{{ t('admin.modelPlaza.modelHint') }}</p>
        </div>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" @click="closeDialog">{{ t('common.cancel') }}</button>
          <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? t('common.loading') : t('admin.modelPlaza.saveModel') }}</button>
        </div>
      </form>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import adminAPI from '@/api/admin'
import modelPlazaAPI from '@/api/model-plaza'
import type { AdminGroup } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()
const groups = ref<AdminGroup[]>([])
const selectedGroupId = ref<number | null>(null)
const loading = ref(true)
const saving = ref(false)
const dialogOpen = ref(false)
const editingModel = ref<string | null>(null)
const modelInput = ref('')
const models = ref<string[]>([])
let modelsRequestId = 0

const selectedGroup = computed(() => groups.value.find((group) => group.id === selectedGroupId.value) || null)

function formatMultiplier(value: number): string {
  return Number(value || 0).toFixed(3).replace(/\.?(0+)$/, '')
}

function openCreate() {
  editingModel.value = null
  modelInput.value = ''
  dialogOpen.value = true
}

function openEdit(model: string) {
  editingModel.value = model
  modelInput.value = model
  dialogOpen.value = true
}

function closeDialog() {
  if (!saving.value) dialogOpen.value = false
}

function replaceSelectedModels(nextModels: string[]) {
  models.value = nextModels
  if (selectedGroup.value) {
    selectedGroup.value.supported_models = nextModels
  }
}

function isManagedModel(model: string): boolean {
  return selectedGroup.value?.supported_models?.includes(model) ?? false
}

async function saveModel() {
  if (!selectedGroup.value) return
  const model = modelInput.value.trim()
  if (!model) {
    appStore.showError(t('admin.modelPlaza.modelRequired'))
    return
  }
  saving.value = true
  try {
    const configured = selectedGroup.value.supported_models || []
    if (editingModel.value) {
      await modelPlazaAPI.updateGroupModel(selectedGroup.value.id, editingModel.value, model)
      replaceSelectedModels(configured.map((item) => (item === editingModel.value ? model : item)))
      appStore.showSuccess(t('admin.modelPlaza.modelUpdated'))
    } else {
      await modelPlazaAPI.createGroupModel(selectedGroup.value.id, model)
      replaceSelectedModels([...configured, model])
      appStore.showSuccess(t('admin.modelPlaza.modelAdded'))
    }
    dialogOpen.value = false
  } catch (error) {
    const message = (error as { message?: string })?.message
    appStore.showError(message || t('admin.modelPlaza.failedToSave'))
  } finally {
    saving.value = false
  }
}

async function deleteModel(model: string) {
  if (!selectedGroup.value || !window.confirm(t('admin.modelPlaza.deleteConfirm', { model }))) return
  try {
    await modelPlazaAPI.deleteGroupModel(selectedGroup.value.id, model)
    replaceSelectedModels((selectedGroup.value.supported_models || []).filter((item) => item !== model))
    appStore.showSuccess(t('admin.modelPlaza.modelDeleted'))
  } catch (error) {
    const message = (error as { message?: string })?.message
    appStore.showError(message || t('admin.modelPlaza.failedToDelete'))
  }
}

async function loadGroups() {
  loading.value = true
  try {
    const result = await adminAPI.groups.getAll()
    groups.value = result.filter((group) => !group.is_exclusive)
    selectedGroupId.value = groups.value[0]?.id || null
  } catch (error) {
    const message = (error as { message?: string })?.message
    appStore.showError(message || t('admin.modelPlaza.failedToLoad'))
  } finally {
    loading.value = false
  }
}

async function loadModels(groupId: number | null) {
  const requestId = ++modelsRequestId
  models.value = []
  if (!groupId) return

  try {
    const nextModels = await modelPlazaAPI.listGroupModels(groupId)
    if (requestId === modelsRequestId && selectedGroupId.value === groupId) {
      models.value = nextModels
    }
  } catch (error) {
    if (requestId !== modelsRequestId || selectedGroupId.value !== groupId) return
    const message = (error as { message?: string })?.message
    appStore.showError(message || t('admin.modelPlaza.failedToLoad'))
  }
}

watch(selectedGroupId, loadModels)
onMounted(loadGroups)
</script>
