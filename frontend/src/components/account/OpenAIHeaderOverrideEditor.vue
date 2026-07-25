<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <label class="flex cursor-pointer items-start gap-3">
      <input
        :checked="enabled"
        type="checkbox"
        class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
        @change="emitEnabled"
      />
      <span>
        <span class="block text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.accounts.openai.headerOverride.title') }}
        </span>
        <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.openai.headerOverride.hint') }}
        </span>
      </span>
    </label>

    <div v-if="enabled" class="mt-4 space-y-3">
      <div
        v-for="(row, index) in rows"
        :key="index"
        class="grid grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)_auto] gap-2"
      >
        <input
          :value="row.name"
          type="text"
          class="input font-mono text-sm"
          :placeholder="t('admin.accounts.openai.headerOverride.namePlaceholder')"
          @input="updateRow(index, 'name', $event)"
        />
        <input
          :value="row.value"
          type="text"
          class="input font-mono text-sm"
          :placeholder="t('admin.accounts.openai.headerOverride.valuePlaceholder')"
          @input="updateRow(index, 'value', $event)"
        />
        <button
          type="button"
          class="rounded-lg px-3 text-gray-400 hover:bg-gray-100 hover:text-red-500 dark:hover:bg-dark-600"
          @click="removeRow(index)"
        >
          ×
        </button>
      </div>

      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary text-xs" @click="addRow">
          {{ t('admin.accounts.openai.headerOverride.addRow') }}
        </button>
        <button type="button" class="btn btn-secondary text-xs" @click="fillTemplate">
          {{ t('admin.accounts.openai.headerOverride.fillTemplate') }}
        </button>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.openai.headerOverride.emptyValueHint') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import {
  openAIHeaderOverrideTemplate,
  type HeaderOverrideRow
} from './openaiHeaderOverrides'

const props = defineProps<{
  enabled: boolean
  rows: HeaderOverrideRow[]
}>()

const emit = defineEmits<{
  'update:enabled': [value: boolean]
  'update:rows': [value: HeaderOverrideRow[]]
}>()

const { t } = useI18n()

function emitEnabled(event: Event) {
  emit('update:enabled', (event.target as HTMLInputElement).checked)
}

function updateRow(index: number, field: keyof HeaderOverrideRow, event: Event) {
  const next = props.rows.map((row) => ({ ...row }))
  next[index][field] = (event.target as HTMLInputElement).value
  emit('update:rows', next)
}

function addRow() {
  emit('update:rows', [...props.rows, { name: '', value: '' }])
}

function removeRow(index: number) {
  emit('update:rows', props.rows.filter((_, rowIndex) => rowIndex !== index))
}

function fillTemplate() {
  const existing = new Set(props.rows.map((row) => row.name.trim().toLowerCase()).filter(Boolean))
  const additions = openAIHeaderOverrideTemplate.filter((row) => !existing.has(row.name))
  emit('update:rows', [...props.rows, ...additions.map((row) => ({ ...row }))])
}
</script>
