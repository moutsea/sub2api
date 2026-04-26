<template>
  <BaseDialog
    :show="show"
    :title="t('imagePreview.requestModal.title')"
    width="wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <div class="rounded-xl border border-blue-200 bg-blue-50 p-4 dark:border-blue-800/50 dark:bg-blue-900/20">
        <div class="flex items-start gap-3">
          <Icon name="infoCircle" size="md" class="mt-0.5 flex-shrink-0 text-blue-500" />
          <div class="space-y-2 text-sm text-blue-700 dark:text-blue-300">
            <p>{{ t('imagePreview.requestModal.description') }}</p>
          </div>
        </div>
      </div>

      <div class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800/80">
        <div class="flex items-center justify-between gap-3">
          <div>
            <p class="text-xs uppercase tracking-wide text-gray-500 dark:text-dark-400">
              {{ t('imagePreview.requestModal.endpointLabel') }}
            </p>
            <p class="mt-1 break-all font-mono text-sm text-gray-900 dark:text-white">
              {{ props.endpoint }}
            </p>
          </div>
          <button @click="copyContent(props.endpoint)" class="btn btn-secondary whitespace-nowrap">
            <Icon name="copy" size="sm" class="mr-2" />
            {{ t('imagePreview.requestModal.copyEndpoint') }}
          </button>
        </div>
      </div>

      <div class="space-y-3">
        <div class="overflow-hidden rounded-xl bg-gray-900 shadow-sm dark:bg-dark-900">
          <div class="flex items-center justify-between border-b border-gray-800 bg-gray-800 px-4 py-2.5 dark:border-dark-700 dark:bg-dark-800">
            <span class="text-xs font-medium uppercase tracking-wide text-gray-300">
              {{ props.bodyTitle }}
            </span>
            <button
              @click="copyContent(props.bodyContent)"
              class="rounded-lg bg-gray-700 px-2.5 py-1 text-xs font-medium text-gray-200 transition-colors hover:bg-gray-600 hover:text-white"
            >
              {{ t('imagePreview.requestModal.copyBody') }}
            </button>
          </div>
          <pre class="overflow-x-auto p-4 text-sm text-gray-100"><code>{{ props.bodyContent }}</code></pre>
        </div>

        <div class="overflow-hidden rounded-xl bg-gray-900 shadow-sm dark:bg-dark-900">
          <div class="flex items-center justify-between border-b border-gray-800 bg-gray-800 px-4 py-2.5 dark:border-dark-700 dark:bg-dark-800">
            <span class="text-xs font-medium uppercase tracking-wide text-gray-300">
              {{ t('imagePreview.requestModal.curlExample') }}
            </span>
            <button
              @click="copyContent(props.curlCommand)"
              class="rounded-lg bg-gray-700 px-2.5 py-1 text-xs font-medium text-gray-200 transition-colors hover:bg-gray-600 hover:text-white"
            >
              {{ t('imagePreview.requestModal.copyCurl') }}
            </button>
          </div>
          <pre class="overflow-x-auto p-4 text-sm text-gray-100"><code>{{ props.curlCommand }}</code></pre>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button @click="emit('close')" class="btn btn-secondary">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'

interface Props {
  show: boolean
  endpoint: string
  bodyTitle: string
  bodyContent: string
  curlCommand: string
}

interface Emits {
  (e: 'close'): void
}

const props = defineProps<Props>()

const emit = defineEmits<Emits>()

const { t } = useI18n()
const { copyToClipboard } = useClipboard()

async function copyContent(text: string) {
  await copyToClipboard(text, t('common.copiedToClipboard'))
}
</script>
