<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-6">
      <div class="grid grid-cols-1 gap-6 xl:grid-cols-[440px_minmax(0,1fr)]">
        <div class="space-y-6">
          <div class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <div class="flex items-center gap-3">
                <div class="flex h-11 w-11 items-center justify-center rounded-2xl bg-primary-100 text-primary-600 dark:bg-primary-900/30 dark:text-primary-300">
                  <Icon :name="mode === 'generate' ? 'sparkles' : 'edit'" size="lg" />
                </div>
                <div>
                  <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                    {{ t('imagePreview.formTitle') }}
                  </h2>
                  <p class="text-sm text-gray-500 dark:text-dark-400">
                    {{ modeDescription }}
                  </p>
                </div>
              </div>
            </div>

            <div class="space-y-5 p-6">
              <div class="rounded-xl border border-amber-200 bg-amber-50 p-4 dark:border-amber-800/50 dark:bg-amber-900/20">
                <div class="flex items-start gap-3">
                  <Icon name="exclamationTriangle" size="md" class="mt-0.5 flex-shrink-0 text-amber-500" />
                  <div class="space-y-2 text-sm text-amber-800 dark:text-amber-200">
                    <p class="font-semibold">{{ t('imagePreview.billingWarningTitle') }}</p>
                    <p>{{ t('imagePreview.billingWarningText') }}</p>
                    <p>{{ t('imagePreview.ephemeralWarningText') }}</p>
                  </div>
                </div>
              </div>

              <div>
                <label class="input-label">{{ t('imagePreview.modeLabel') }}</label>
                <div class="mt-1 grid grid-cols-2 gap-2 rounded-2xl bg-gray-100 p-1 dark:bg-dark-800">
                  <button
                    type="button"
                    class="flex items-center justify-center rounded-xl px-4 py-2.5 text-sm font-medium transition-colors"
                    :class="mode === 'generate'
                      ? 'bg-white text-primary-600 shadow-sm dark:bg-dark-700 dark:text-primary-300'
                      : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'"
                    @click="setMode('generate')"
                  >
                    <Icon name="sparkles" size="sm" class="mr-2" />
                    {{ t('imagePreview.modeGenerate') }}
                  </button>
                  <button
                    type="button"
                    class="flex items-center justify-center rounded-xl px-4 py-2.5 text-sm font-medium transition-colors"
                    :disabled="!EDIT_MODE_ENABLED"
                    :class="!EDIT_MODE_ENABLED
                      ? 'cursor-not-allowed text-gray-400 opacity-60 dark:text-dark-500'
                      : mode === 'edit'
                        ? 'bg-white text-primary-600 shadow-sm dark:bg-dark-700 dark:text-primary-300'
                        : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'"
                    @click="setMode('edit')"
                  >
                    <Icon name="edit" size="sm" class="mr-2" />
                    {{ t('imagePreview.modeEdit') }}
                  </button>
                </div>
                <p class="input-hint mt-2">
                  {{ modeHintText }}
                </p>
                <p v-if="!EDIT_MODE_ENABLED" class="input-hint mt-1">
                  {{ t('imagePreview.modeEditDisabled') }}
                </p>
              </div>

              <div>
                <label class="input-label">{{ t('imagePreview.keyLabel') }}</label>
                <div v-if="loadingKeys" class="mt-2 flex items-center gap-2 text-sm text-gray-500 dark:text-dark-400">
                  <LoadingSpinner />
                  <span>{{ t('imagePreview.loadingKeys') }}</span>
                </div>
                <template v-else-if="openAIKeys.length > 0">
                  <select v-model="selectedApiKeyId" class="input mt-1">
                    <option
                      v-for="apiKey in openAIKeys"
                      :key="apiKey.id"
                      :value="apiKey.id"
                    >
                      {{ buildApiKeyOptionLabel(apiKey) }}
                    </option>
                  </select>
                  <p class="input-hint mt-2">
                    {{ t('imagePreview.keyHint') }}
                  </p>
                </template>
                <div
                  v-else
                  class="mt-2 rounded-xl border border-dashed border-gray-300 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800/60"
                >
                  <p class="text-sm font-medium text-gray-900 dark:text-white">
                    {{ t('imagePreview.noKeysTitle') }}
                  </p>
                  <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                    {{ t('imagePreview.noKeysDescription') }}
                  </p>
                  <router-link to="/keys" class="btn btn-secondary mt-4 inline-flex">
                    <Icon name="key" size="sm" class="mr-2" />
                    {{ t('imagePreview.goToKeys') }}
                  </router-link>
                </div>
              </div>

              <div>
                <label class="input-label">{{ t('imagePreview.modelLabel') }}</label>
                <select v-model="form.model" class="input mt-1">
                  <option
                    v-for="modelOption in modelOptions"
                    :key="modelOption.value"
                    :value="modelOption.value"
                  >
                    {{ modelOption.label }}
                  </option>
                </select>
                <p class="input-hint mt-2">
                  {{ modelHintText }}
                </p>
              </div>

              <div>
                <label class="input-label">{{ t('imagePreview.sizeLabel') }}</label>
                <select v-model="form.size" class="input mt-1">
                  <option
                    v-for="sizeOption in sizeOptions"
                    :key="sizeOption.value"
                    :value="sizeOption.value"
                  >
                    {{ sizeOption.label }}
                  </option>
                </select>
                <p class="input-hint mt-2">
                  {{ t('imagePreview.sizeHint') }}
                </p>
              </div>

              <div v-if="mode === 'edit'">
                <div class="flex items-center justify-between gap-3">
                  <label class="input-label">
                    {{ t('imagePreview.sourceImageLabel') }} ({{ sourceImageFiles.length }}/{{ MAX_SOURCE_IMAGES }})
                  </label>
                  <input
                    ref="sourceFileInput"
                    type="file"
                    accept="image/*"
                    multiple
                    class="hidden"
                    @change="handleSourceFileChange"
                  />
                  <button
                    v-if="sourceImageFiles.length > 0"
                    type="button"
                    class="inline-flex items-center gap-1.5 text-sm font-medium text-red-600 transition-colors hover:text-red-700 dark:text-red-400 dark:hover:text-red-300"
                    @click="clearSourceImages"
                  >
                    <Icon name="trash" size="sm" />
                    {{ t('imagePreview.clearSourceImages') }}
                  </button>
                </div>

                <div
                  v-if="sourceImageFiles.length > 0"
                  class="mt-2 grid grid-cols-2 gap-3 sm:grid-cols-3"
                >
                  <div
                    v-for="(url, index) in sourceImageUrls"
                    :key="url"
                    class="group relative overflow-hidden rounded-2xl border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-900/60"
                  >
                    <img
                      :src="url"
                      :alt="t('imagePreview.sourceImageAlt')"
                      class="h-32 w-full object-cover"
                    />
                    <button
                      type="button"
                      class="absolute right-2 top-2 flex h-7 w-7 items-center justify-center rounded-full bg-black/60 text-white opacity-0 shadow-sm transition-opacity hover:bg-black/75 group-hover:opacity-100"
                      :aria-label="t('imagePreview.removeSourceImage')"
                      @click.stop="removeSourceImage(index)"
                    >
                      <Icon name="trash" size="sm" />
                    </button>
                    <div class="absolute inset-x-0 bottom-0 bg-black/55 px-2 py-1.5 text-white">
                      <p class="truncate text-xs font-medium">
                        {{ sourceImageFiles[index]?.name }}
                      </p>
                      <p class="mt-0.5 truncate text-[11px] text-white/75">
                        {{ formatSourceFileSummary(sourceImageFiles[index]) }}
                      </p>
                    </div>
                  </div>
                </div>

                <div
                  v-if="sourceImageFiles.length < MAX_SOURCE_IMAGES"
                  role="button"
                  tabindex="0"
                  class="mt-2 cursor-pointer rounded-2xl border border-dashed p-5 text-center transition-colors"
                  :class="sourceDragActive
                    ? 'border-primary-500 bg-primary-50 text-primary-600 dark:border-primary-500 dark:bg-primary-900/20 dark:text-primary-300'
                    : 'border-gray-300 bg-gray-50 text-gray-500 hover:border-primary-400 hover:bg-primary-50/50 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400 dark:hover:border-primary-600 dark:hover:bg-primary-900/10'"
                  @click="triggerSourceFileSelect"
                  @keydown.enter.prevent="triggerSourceFileSelect"
                  @keydown.space.prevent="triggerSourceFileSelect"
                  @dragenter.prevent="handleSourceDragEnter"
                  @dragover.prevent="handleSourceDragOver"
                  @dragleave.prevent="handleSourceDragLeave"
                  @drop.prevent="handleSourceDrop"
                >
                  <div
                    class="mx-auto flex h-14 w-14 items-center justify-center rounded-2xl transition-colors"
                    :class="sourceDragActive
                      ? 'bg-primary-100 text-primary-600 dark:bg-primary-900/30 dark:text-primary-300'
                      : 'bg-gray-200 text-gray-500 dark:bg-dark-800 dark:text-dark-400'"
                  >
                    <Icon name="upload" size="xl" />
                  </div>
                  <p class="mt-4 text-sm font-semibold text-gray-900 dark:text-white">
                    {{ t('imagePreview.sourceImageEmptyTitle') }}
                  </p>
                  <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
                    {{ t('imagePreview.sourceImageEmptyDescription') }}
                  </p>
                </div>

                <p class="input-hint mt-2">
                  {{ t('imagePreview.sourceImageHint') }}
                </p>
              </div>

              <div>
                <label class="input-label">{{ promptLabelText }}</label>
                <textarea
                  v-model="form.prompt"
                  rows="8"
                  class="input mt-1 min-h-[180px] resize-y"
                  :placeholder="promptPlaceholderText"
                ></textarea>
                <div class="mt-2 flex items-center justify-between text-xs text-gray-500 dark:text-dark-400">
                  <span>{{ promptHintText }}</span>
                  <span>{{ promptLengthText }}</span>
                </div>
              </div>

              <div class="flex flex-wrap gap-3">
                <button
                  @click="handleSubmit"
                  :disabled="!canSubmit || generating"
                  class="btn btn-primary"
                >
                  <LoadingSpinner v-if="generating" class="mr-2" />
                  <Icon v-else :name="mode === 'generate' ? 'sparkles' : 'edit'" size="sm" class="mr-2" />
                  {{ generating ? submittingButtonText : submitButtonText }}
                </button>

                <button
                  @click="openCurrentRequestModal"
                  :disabled="!currentRequestPreview"
                  class="btn btn-secondary"
                >
                  <Icon name="document" size="sm" class="mr-2" />
                  {{ t('imagePreview.viewRequest') }}
                </button>
              </div>
            </div>
          </div>
        </div>

        <div class="space-y-6">
          <div class="card min-h-[540px] overflow-hidden">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <div class="flex items-center justify-between gap-4">
                <div>
                  <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                    {{ t('imagePreview.previewTitle') }}
                  </h2>
                  <p class="text-sm text-gray-500 dark:text-dark-400">
                    {{ t('imagePreview.previewDescription') }}
                  </p>
                </div>
                <div
                  v-if="result"
                  class="rounded-full bg-emerald-100 px-3 py-1 text-xs font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300"
                >
                  {{ result.mode === 'edit' ? t('imagePreview.previewEdited') : t('imagePreview.previewReady') }}
                </div>
              </div>
            </div>

            <div class="p-6">
              <div
                v-if="generating"
                class="flex min-h-[420px] flex-col items-center justify-center rounded-2xl border border-dashed border-primary-300 bg-primary-50/70 p-8 text-center dark:border-primary-700/50 dark:bg-primary-900/10"
              >
                <LoadingSpinner class="mb-4" />
                <p class="text-base font-semibold text-gray-900 dark:text-white">
                  {{ generatingTitle }}
                </p>
                <p class="mt-2 max-w-md text-sm text-gray-500 dark:text-dark-400">
                  {{ generatingDescription }}
                </p>
              </div>

              <template v-else-if="result">
                <div class="space-y-5">
                  <div v-if="result.mode === 'edit' && sourceImageUrls.length > 0" class="grid grid-cols-1 gap-4 xl:grid-cols-2">
                    <div class="overflow-hidden rounded-2xl border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-900/60">
                      <div class="border-b border-gray-200 px-4 py-3 text-sm font-medium text-gray-900 dark:border-dark-700 dark:text-white">
                        {{ t('imagePreview.editSourcePreviewTitle') }} ({{ sourceImageUrls.length }})
                      </div>
                      <div class="grid grid-cols-2 gap-2 p-3">
                        <img
                          v-for="url in sourceImageUrls"
                          :key="url"
                          :src="url"
                          :alt="t('imagePreview.sourceImageAlt')"
                          class="h-36 w-full rounded-xl object-cover"
                        />
                      </div>
                    </div>

                    <div class="overflow-hidden rounded-2xl border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-900/60">
                      <div class="border-b border-gray-200 px-4 py-3 text-sm font-medium text-gray-900 dark:border-dark-700 dark:text-white">
                        {{ t('imagePreview.editResultPreviewTitle') }}
                      </div>
                      <img
                        :src="result.imageUrl"
                        :alt="t('imagePreview.generatedImageAlt')"
                        class="h-auto max-h-[320px] w-full object-contain"
                      />
                    </div>
                  </div>

                  <div
                    v-else
                    class="overflow-hidden rounded-2xl border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-900/60"
                  >
                    <img
                      :src="result.imageUrl"
                      :alt="t('imagePreview.generatedImageAlt')"
                      class="h-auto max-h-[640px] w-full object-contain"
                    />
                  </div>

                  <div class="grid grid-cols-2 gap-4 lg:grid-cols-2">
                    <div class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
                      <p class="text-xs uppercase tracking-wide text-gray-500 dark:text-dark-400">
                        {{ t('imagePreview.meta.operation') }}
                      </p>
                      <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
                        {{ result.mode === 'edit' ? t('imagePreview.modeEdit') : t('imagePreview.modeGenerate') }}
                      </p>
                    </div>
                    <div class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
                      <p class="text-xs uppercase tracking-wide text-gray-500 dark:text-dark-400">
                        {{ t('imagePreview.meta.model') }}
                      </p>
                      <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
                        {{ result.model }}
                      </p>
                    </div>
                    <div class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
                      <p class="text-xs uppercase tracking-wide text-gray-500 dark:text-dark-400">
                        {{ t('imagePreview.meta.apiKey') }}
                      </p>
                      <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
                        {{ result.apiKeyName }}
                      </p>
                    </div>
                    <div class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
                      <p class="text-xs uppercase tracking-wide text-gray-500 dark:text-dark-400">
                        {{ t('imagePreview.meta.generatedAt') }}
                      </p>
                      <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
                        {{ formatDateTime(result.createdAt) }}
                      </p>
                    </div>
                  </div>

                  <div class="flex flex-wrap gap-3">
                    <button @click="downloadCurrentImage" class="btn btn-primary">
                      <Icon name="download" size="sm" class="mr-2" />
                      {{ t('imagePreview.downloadImage') }}
                    </button>
                    <button @click="retryLastRequest" :disabled="generating || !lastSubmitted" class="btn btn-secondary">
                      <Icon name="refresh" size="sm" class="mr-2" />
                      {{ t('imagePreview.retry') }}
                    </button>
                    <button @click="openLastRequestModal" :disabled="!lastSubmitted" class="btn btn-secondary">
                      <Icon name="document" size="sm" class="mr-2" />
                      {{ t('imagePreview.viewRequest') }}
                    </button>
                  </div>

                  <div class="rounded-xl border border-gray-200 bg-gray-50 p-4 text-sm text-gray-600 dark:border-dark-700 dark:bg-dark-800/70 dark:text-dark-300">
                    {{ t('imagePreview.previewPersistenceNotice') }}
                  </div>
                </div>
              </template>

              <div
                v-else
                class="flex min-h-[420px] flex-col items-center justify-center rounded-2xl border border-dashed border-gray-300 bg-gray-50 p-8 text-center dark:border-dark-700 dark:bg-dark-900/40"
              >
                <div class="flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-200 text-gray-500 dark:bg-dark-800 dark:text-dark-400">
                  <Icon :name="mode === 'generate' ? 'sparkles' : 'edit'" size="xl" />
                </div>
                <p class="mt-4 text-base font-semibold text-gray-900 dark:text-white">
                  {{ emptyTitle }}
                </p>
                <p class="mt-2 max-w-md text-sm text-gray-500 dark:text-dark-400">
                  {{ emptyDescription }}
                </p>
              </div>

              <div
                v-if="errorMessage"
                class="mt-5 rounded-xl border border-red-200 bg-red-50 p-4 dark:border-red-800/40 dark:bg-red-900/10"
              >
                <div class="flex items-start gap-3">
                  <Icon name="exclamationCircle" size="md" class="mt-0.5 flex-shrink-0 text-red-500" />
                  <div>
                    <p class="text-sm font-semibold text-red-800 dark:text-red-300">
                      {{ t('imagePreview.errorTitle') }}
                    </p>
                    <p class="mt-1 whitespace-pre-wrap text-sm text-red-700 dark:text-red-200">
                      {{ errorMessage }}
                    </p>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <ImageRequestPreviewModal
      v-if="requestModalState"
      :show="showRequestModal"
      :endpoint="requestModalState.endpoint"
      :body-title="requestModalState.bodyTitle"
      :body-content="requestModalState.bodyContent"
      :curl-command="requestModalState.curlCommand"
      @close="showRequestModal = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { keysAPI } from '@/api'
import { openAIImagesAPI, type OpenAIImageGenerationItem } from '@/api/openai-images'
import type { ApiKey } from '@/types'
import { useAppStore } from '@/stores'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import ImageRequestPreviewModal from '@/components/user/ImageRequestPreviewModal.vue'

type GptImageModel =
  | 'gpt-image-1'
  | 'gpt-image-1.5'
  | 'gpt-image-2'
  | 'gpt-image-2.5-sunburst'
  | 'gpt-image-2.5-flare'
type ImagePreviewMode = 'generate' | 'edit'
type ImageSize = 'auto' | '1024x1024' | '1536x1024' | '1024x1536' | '1792x1024' | '1024x1792' | '2048x2048'
type UpscaleMode = '' | '2k' | '4k'

const PREVIEW_DRAFT_STORAGE_KEY = 'image_preview_draft_v3'
const PREVIEW_RESULT_STORAGE_KEY = 'image_preview_result_v3'
const MAX_SOURCE_IMAGES = 4

interface PreviewResult {
  imageUrl: string
  mimeType: string
  model: GptImageModel
  size: ImageSize
  upscale: UpscaleMode
  apiKeyName: string
  createdAt: Date
  mode: ImagePreviewMode
}

interface SubmittedRequest {
  apiKeyId: number
  mode: ImagePreviewMode
  model: GptImageModel
  size: ImageSize
  upscale: UpscaleMode
  prompt: string
  payload: Record<string, unknown>
  sourceImageNames?: string[]
  sourceImageTypes?: string[]
}

interface PersistedDraftState {
  mode: ImagePreviewMode
  model: GptImageModel
  size: ImageSize
  upscale: UpscaleMode
  prompt: string
  selectedApiKeyId: number | null
  currentRequestId: string | null
  pending: boolean
  errorMessage: string
  lastSubmitted: {
    apiKeyId: number
    mode: ImagePreviewMode
    model: GptImageModel
    size: ImageSize
    upscale: UpscaleMode
    prompt: string
    sourceImageNames?: string[]
    sourceImageTypes?: string[]
  } | null
}

interface PersistedResultState {
  imageUrl: string
  mimeType: string
  model: GptImageModel
  size: ImageSize
  upscale: UpscaleMode
  apiKeyName: string
  createdAt: string
  mode: ImagePreviewMode
}

interface RequestPreviewState {
  endpoint: string
  bodyTitle: string
  bodyContent: string
  curlCommand: string
}

const DEFAULT_MODEL: GptImageModel = 'gpt-image-2'
const DEFAULT_SIZE: ImageSize = 'auto'
const EDIT_MODE_ENABLED = true

const { t } = useI18n()
const appStore = useAppStore()

const loadingKeys = ref(false)
const generating = ref(false)
const errorMessage = ref('')
const allKeys = ref<ApiKey[]>([])
const selectedApiKeyId = ref<number | null>(null)
const result = ref<PreviewResult | null>(null)
const lastSubmitted = ref<SubmittedRequest | null>(null)
const lastSubmittedSourceFiles = ref<File[]>([])
const currentRequestId = ref<string | null>(null)
const showRequestModal = ref(false)
const requestModalState = ref<RequestPreviewState | null>(null)
const mode = ref<ImagePreviewMode>('generate')
const sourceFileInput = ref<HTMLInputElement | null>(null)
const sourceImageFiles = ref<File[]>([])
const sourceImageUrls = ref<string[]>([])
const sourceDragActive = ref(false)

const form = reactive({
  model: DEFAULT_MODEL as GptImageModel,
  size: DEFAULT_SIZE as ImageSize,
  prompt: ''
})

const modelOptions: Array<{ value: GptImageModel; label: string }> = [
  { value: 'gpt-image-2.5-sunburst', label: 'GPT Image 2.5 Sunburst' },
  { value: 'gpt-image-2.5-flare', label: 'GPT Image 2.5 Flare' },
  { value: 'gpt-image-2', label: 'GPT Image 2' },
  { value: 'gpt-image-1.5', label: 'GPT Image 1.5' },
  { value: 'gpt-image-1', label: 'GPT Image 1' }
]

const sizeOptions = computed<Array<{ value: ImageSize; label: string }>>(() => [
  { value: 'auto', label: t('imagePreview.sizeAuto') },
  { value: '1024x1024', label: `1024x1024 (${t('imagePreview.sizeTier1K')})` },
  { value: '1536x1024', label: `1536x1024 (${t('imagePreview.sizeTier2K')})` },
  { value: '1024x1536', label: `1024x1536 (${t('imagePreview.sizeTier2K')})` },
  { value: '1792x1024', label: `1792x1024 (${t('imagePreview.sizeTier2K')})` },
  { value: '1024x1792', label: `1024x1792 (${t('imagePreview.sizeTier2K')})` },
  { value: '2048x2048', label: `2048x2048 (${t('imagePreview.sizeTier4K')})` }
])

let activeController: AbortController | null = null
let pendingSyncTimer: number | null = null
let hasWarnedStorageFailure = false

const openAIKeys = computed(() =>
  allKeys.value.filter((item) => item.status === 'active' && item.group?.platform === 'openai')
)

const selectedApiKey = computed(() =>
  openAIKeys.value.find((item) => item.id === selectedApiKeyId.value) || null
)

const requestBaseUrl = computed(() => window.location.origin.replace(/\/$/, ''))
const promptLengthText = computed(() =>
  t('imagePreview.promptCount', { count: form.prompt.trim().length })
)
const modeDescription = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.formDescriptionGenerate')
    : t('imagePreview.formDescriptionEdit')
)
const modeHintText = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.modeGenerateHint')
    : t('imagePreview.modeEditHint')
)
const modelHintText = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.modelHint')
    : t('imagePreview.editModelHint')
)
const promptLabelText = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.promptLabel')
    : t('imagePreview.editPromptLabel')
)
const promptPlaceholderText = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.promptPlaceholder')
    : t('imagePreview.editPromptPlaceholder')
)
const promptHintText = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.promptHint')
    : t('imagePreview.editPromptHint')
)
const submitButtonText = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.generate')
    : t('imagePreview.editSubmit')
)
const submittingButtonText = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.generating')
    : t('imagePreview.editing')
)
const generatingTitle = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.generatingTitle')
    : t('imagePreview.editingTitle')
)
const generatingDescription = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.generatingDescription')
    : t('imagePreview.editingDescription')
)
const emptyTitle = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.emptyTitle')
    : t('imagePreview.editEmptyTitle')
)
const emptyDescription = computed(() =>
  mode.value === 'generate'
    ? t('imagePreview.emptyDescription')
    : t('imagePreview.editEmptyDescription')
)
const canSubmit = computed(() =>
  Boolean(
    selectedApiKey.value &&
    form.prompt.trim().length > 0 &&
    (mode.value === 'generate' || sourceImageFiles.value.length > 0)
  )
)
const currentRequestPreview = computed<RequestPreviewState | null>(() => {
  if (!selectedApiKey.value || !form.prompt.trim()) {
    return null
  }
  const payload = buildPayload(form.model, form.prompt, form.size)
  if (mode.value === 'edit' && sourceImageFiles.value.length > 0) {
    const previewPayload = {
      ...payload,
      reference_images: sourceImageFiles.value.map((file) => `(base64 data from ${file.name})`)
    }
    return buildGenerateRequestPreview(requestBaseUrl.value, selectedApiKey.value.key, previewPayload)
  }
  return buildGenerateRequestPreview(requestBaseUrl.value, selectedApiKey.value.key, payload)
})

watch(openAIKeys, (keys) => {
  if (keys.length === 0) {
    selectedApiKeyId.value = null
    return
  }
  if (!keys.some((item) => item.id === selectedApiKeyId.value)) {
    selectedApiKeyId.value = keys[0].id
  }
}, { immediate: true })

watch(
  [mode, () => form.model, () => form.size, () => form.prompt, selectedApiKeyId, errorMessage],
  () => {
    persistDraftState()
  }
)

watch(result, () => {
  persistResultState()
}, { deep: true })

onMounted(() => {
  restorePreviewState()
  void loadKeys()
  if (generating.value) {
    startPendingSyncTimer()
  }
})

onBeforeUnmount(() => {
  activeController?.abort()
  stopPendingSyncTimer()
  revokeSourceImageUrls()
})

function setMode(nextMode: ImagePreviewMode) {
  if (nextMode === 'edit' && !EDIT_MODE_ENABLED) {
    return
  }
  mode.value = nextMode
}

function normalizeAvailableMode(nextMode?: ImagePreviewMode | null): ImagePreviewMode {
  if (nextMode === 'edit' && !EDIT_MODE_ENABLED) {
    return 'generate'
  }
  return nextMode === 'edit' ? 'edit' : 'generate'
}

function buildPayload(model: GptImageModel, prompt: string, size: ImageSize): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    model,
    prompt: prompt.trim(),
    response_format: 'b64_json'
  }
  if (size && size !== 'auto') {
    payload.size = size
  }
  return payload
}

function deriveUpscaleFromSize(size: ImageSize): UpscaleMode {
  switch (size) {
    case '1536x1024':
    case '1024x1536':
    case '1792x1024':
    case '1024x1792':
      return '2k'
    case '2048x2048':
      return '4k'
    default:
      return ''
  }
}

function buildGenerateRequestPreview(baseUrl: string, apiKey: string, payload: Record<string, unknown>): RequestPreviewState {
  const endpoint = `${baseUrl}/v1/images/generations`
  const bodyContent = JSON.stringify(payload, null, 2)
  const curlCommand = [
    `curl "${endpoint}" \\`,
    `  -H "Authorization: Bearer ${apiKey}" \\`,
    '  -H "Content-Type: application/json" \\',
    "  -d @- <<'JSON'",
    bodyContent,
    'JSON'
  ].join('\n')
  return {
    endpoint,
    bodyTitle: t('imagePreview.requestModal.jsonBody'),
    bodyContent,
    curlCommand
  }
}

async function loadKeys() {
  loadingKeys.value = true
  try {
    const response = await keysAPI.list(1, 200)
    allKeys.value = response.items || []
  } catch (error) {
    console.error('Failed to load OpenAI API keys:', error)
    appStore.showError(t('imagePreview.failedToLoadKeys'))
  } finally {
    loadingKeys.value = false
  }
}

function fileToBase64DataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as string)
    reader.onerror = () => reject(new Error('Failed to read file'))
    reader.readAsDataURL(file)
  })
}

function buildApiKeyOptionLabel(apiKey: ApiKey): string {
  const maskedKey = maskKey(apiKey.key)
  const groupName = apiKey.group?.name ? ` · ${apiKey.group.name}` : ''
  return `${apiKey.name} · ${maskedKey}${groupName}`
}

function maskKey(key: string): string {
  if (key.length <= 12) return key
  return `${key.slice(0, 8)}...${key.slice(-4)}`
}

function getMimeType(item: OpenAIImageGenerationItem): string {
  if (item.b64_json) {
    return 'image/png'
  }
  return 'image/png'
}

function buildImageUrl(item: OpenAIImageGenerationItem): string {
  if (item.b64_json) {
    return `data:${getMimeType(item)};base64,${item.b64_json}`
  }
  return item.url || ''
}

function readJSONStorage<T>(key: string): T | null {
  try {
    const raw = sessionStorage.getItem(key)
    if (!raw) return null
    return JSON.parse(raw) as T
  } catch {
    return null
  }
}

function writeJSONStorage(key: string, value: unknown) {
  try {
    sessionStorage.setItem(key, JSON.stringify(value))
  } catch (error) {
    if (!hasWarnedStorageFailure) {
      hasWarnedStorageFailure = true
      console.warn(`Failed to persist image preview state for ${key}:`, error)
      appStore.showWarning(t('imagePreview.storagePersistWarning'))
    }
  }
}

function removeStorageKey(key: string) {
  try {
    sessionStorage.removeItem(key)
  } catch {
    // ignore storage cleanup failures
  }
}

const IDB_NAME = 'image_preview_db'
const IDB_STORE = 'images'
const IDB_KEY = 'current_image'

function openIDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(IDB_NAME, 1)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(IDB_STORE)) {
        db.createObjectStore(IDB_STORE)
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

async function writeImageToIDB(imageUrl: string): Promise<void> {
  try {
    const db = await openIDB()
    const tx = db.transaction(IDB_STORE, 'readwrite')
    tx.objectStore(IDB_STORE).put(imageUrl, IDB_KEY)
    await new Promise<void>((resolve, reject) => {
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
    })
    db.close()
  } catch (e) {
    console.warn('Failed to write image to IndexedDB:', e)
  }
}

async function readImageFromIDB(): Promise<string | null> {
  try {
    const db = await openIDB()
    const tx = db.transaction(IDB_STORE, 'readonly')
    const req = tx.objectStore(IDB_STORE).get(IDB_KEY)
    const value = await new Promise<string | null>((resolve, reject) => {
      req.onsuccess = () => resolve(req.result as string | null)
      req.onerror = () => reject(req.error)
    })
    db.close()
    return value || null
  } catch {
    return null
  }
}

async function removeImageFromIDB(): Promise<void> {
  try {
    const db = await openIDB()
    const tx = db.transaction(IDB_STORE, 'readwrite')
    tx.objectStore(IDB_STORE).delete(IDB_KEY)
    await new Promise<void>((resolve, reject) => {
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
    })
    db.close()
  } catch {
    // ignore
  }
}

function persistDraftState() {
  const state: PersistedDraftState = {
    mode: mode.value,
    model: form.model,
    size: form.size,
    upscale: deriveUpscaleFromSize(form.size),
    prompt: form.prompt,
    selectedApiKeyId: selectedApiKeyId.value,
    currentRequestId: currentRequestId.value,
    pending: generating.value,
    errorMessage: errorMessage.value,
    lastSubmitted: lastSubmitted.value
      ? {
          apiKeyId: lastSubmitted.value.apiKeyId,
          mode: lastSubmitted.value.mode,
          model: lastSubmitted.value.model,
          size: lastSubmitted.value.size,
          upscale: lastSubmitted.value.upscale,
          prompt: lastSubmitted.value.prompt,
          sourceImageNames: lastSubmitted.value.sourceImageNames,
          sourceImageTypes: lastSubmitted.value.sourceImageTypes
        }
      : null
  }
  writeJSONStorage(PREVIEW_DRAFT_STORAGE_KEY, state)
}

function clearUnresumablePendingState(draft: PersistedDraftState): boolean {
  if (!draft.pending || activeController) {
    return false
  }
  draft.pending = false
  draft.currentRequestId = null
  if (!draft.errorMessage) {
    draft.errorMessage = t('imagePreview.previousRequestInterrupted')
  }
  writeJSONStorage(PREVIEW_DRAFT_STORAGE_KEY, draft)
  return true
}

function persistResultState() {
  if (!result.value) {
    removeStorageKey(PREVIEW_RESULT_STORAGE_KEY)
    void removeImageFromIDB()
    return
  }

  const state: PersistedResultState = {
    imageUrl: '',
    mimeType: result.value.mimeType,
    model: result.value.model,
    size: result.value.size,
    upscale: result.value.upscale,
    apiKeyName: result.value.apiKeyName,
    createdAt: result.value.createdAt.toISOString(),
    mode: result.value.mode
  }
  writeJSONStorage(PREVIEW_RESULT_STORAGE_KEY, state)
  void writeImageToIDB(result.value.imageUrl)
}

async function restorePreviewState() {
  const draft = readJSONStorage<PersistedDraftState>(PREVIEW_DRAFT_STORAGE_KEY)
  if (draft) {
    clearUnresumablePendingState(draft)
    mode.value = normalizeAvailableMode(draft.mode)
    form.model = draft.model || DEFAULT_MODEL
    form.size = draft.size || DEFAULT_SIZE
    form.prompt = draft.prompt || ''
    selectedApiKeyId.value = draft.selectedApiKeyId ?? null
    currentRequestId.value = draft.currentRequestId ?? null
    generating.value = Boolean(draft.pending)
    errorMessage.value = draft.errorMessage || ''
    lastSubmitted.value = draft.lastSubmitted && normalizeAvailableMode(draft.lastSubmitted.mode) === draft.lastSubmitted.mode
      ? {
          apiKeyId: draft.lastSubmitted.apiKeyId,
          mode: draft.lastSubmitted.mode,
          model: draft.lastSubmitted.model,
          size: draft.lastSubmitted.size || DEFAULT_SIZE,
          upscale: draft.lastSubmitted.upscale || deriveUpscaleFromSize((draft.lastSubmitted.size || DEFAULT_SIZE) as ImageSize),
          prompt: draft.lastSubmitted.prompt,
          payload: buildPayload(draft.lastSubmitted.model, draft.lastSubmitted.prompt, (draft.lastSubmitted.size || DEFAULT_SIZE) as ImageSize),
          sourceImageNames: draft.lastSubmitted.sourceImageNames,
          sourceImageTypes: draft.lastSubmitted.sourceImageTypes
        }
      : null
  }

  const persistedResult = readJSONStorage<PersistedResultState>(PREVIEW_RESULT_STORAGE_KEY)
  if (persistedResult) {
    const imageUrl = await readImageFromIDB() || persistedResult.imageUrl || ''
    if (imageUrl) {
      result.value = {
        imageUrl,
        mimeType: persistedResult.mimeType,
        model: persistedResult.model,
        size: persistedResult.size || DEFAULT_SIZE,
        upscale: persistedResult.upscale || deriveUpscaleFromSize((persistedResult.size || DEFAULT_SIZE) as ImageSize),
        apiKeyName: persistedResult.apiKeyName,
        createdAt: new Date(persistedResult.createdAt),
        mode: persistedResult.mode || 'generate'
      }
    }
  }
}

async function syncFromStorage() {
  const draft = readJSONStorage<PersistedDraftState>(PREVIEW_DRAFT_STORAGE_KEY)
  if (draft) {
    clearUnresumablePendingState(draft)
    mode.value = normalizeAvailableMode(draft.mode || mode.value)
    currentRequestId.value = draft.currentRequestId ?? null
    generating.value = Boolean(draft.pending)
    errorMessage.value = draft.errorMessage || ''
    lastSubmitted.value = draft.lastSubmitted && normalizeAvailableMode(draft.lastSubmitted.mode) === draft.lastSubmitted.mode
      ? {
          apiKeyId: draft.lastSubmitted.apiKeyId,
          mode: draft.lastSubmitted.mode,
          model: draft.lastSubmitted.model,
          size: draft.lastSubmitted.size || DEFAULT_SIZE,
          upscale: draft.lastSubmitted.upscale || deriveUpscaleFromSize((draft.lastSubmitted.size || DEFAULT_SIZE) as ImageSize),
          prompt: draft.lastSubmitted.prompt,
          payload: buildPayload(draft.lastSubmitted.model, draft.lastSubmitted.prompt, (draft.lastSubmitted.size || DEFAULT_SIZE) as ImageSize),
          sourceImageNames: draft.lastSubmitted.sourceImageNames,
          sourceImageTypes: draft.lastSubmitted.sourceImageTypes
        }
      : null
  }

  const persistedResult = readJSONStorage<PersistedResultState>(PREVIEW_RESULT_STORAGE_KEY)
  if (persistedResult) {
    const imageUrl = await readImageFromIDB() || persistedResult.imageUrl || ''
    if (imageUrl) {
      result.value = {
        imageUrl,
        mimeType: persistedResult.mimeType,
        model: persistedResult.model,
        size: persistedResult.size || DEFAULT_SIZE,
        upscale: persistedResult.upscale || deriveUpscaleFromSize((persistedResult.size || DEFAULT_SIZE) as ImageSize),
        apiKeyName: persistedResult.apiKeyName,
        createdAt: new Date(persistedResult.createdAt),
        mode: persistedResult.mode || 'generate'
      }
    }
  }

  if (!draft?.pending) {
    stopPendingSyncTimer()
  }
}

function isLatestRequest(requestId: string): boolean {
  const draft = readJSONStorage<PersistedDraftState>(PREVIEW_DRAFT_STORAGE_KEY)
  return draft?.currentRequestId === requestId
}

function startPendingSyncTimer() {
  if (pendingSyncTimer !== null) {
    return
  }
  pendingSyncTimer = window.setInterval(() => {
    syncFromStorage()
  }, 1000)
}

function stopPendingSyncTimer() {
  if (pendingSyncTimer !== null) {
    window.clearInterval(pendingSyncTimer)
    pendingSyncTimer = null
  }
}

function revokeSourceImageUrls(urls: string[] = sourceImageUrls.value) {
  urls.forEach((url) => {
    if (url.startsWith('blob:')) {
      URL.revokeObjectURL(url)
    }
  })
}

function setSourceImageFiles(files: File[]) {
  revokeSourceImageUrls()
  sourceImageFiles.value = files
  sourceImageUrls.value = files.map((file) => URL.createObjectURL(file))
  if (sourceFileInput.value) {
    sourceFileInput.value.value = ''
  }
}

function appendSourceImageFiles(files: File[]) {
  const imageFiles = files.filter((file) => file.type.startsWith('image/'))
  if (imageFiles.length === 0) {
    appStore.showError(t('imagePreview.sourceImageInvalid'))
    return
  }
  const remaining = MAX_SOURCE_IMAGES - sourceImageFiles.value.length
  if (remaining <= 0) {
    appStore.showWarning(t('imagePreview.sourceImageLimit', { count: MAX_SOURCE_IMAGES }))
    return
  }

  const filesToAdd = imageFiles.slice(0, remaining)
  sourceImageFiles.value = [...sourceImageFiles.value, ...filesToAdd]
  sourceImageUrls.value = [
    ...sourceImageUrls.value,
    ...filesToAdd.map((file) => URL.createObjectURL(file))
  ]
  if (imageFiles.length > filesToAdd.length) {
    appStore.showWarning(t('imagePreview.sourceImageLimit', { count: MAX_SOURCE_IMAGES }))
  }
}

function removeSourceImage(index: number) {
  const url = sourceImageUrls.value[index]
  if (url?.startsWith('blob:')) {
    URL.revokeObjectURL(url)
  }
  sourceImageFiles.value = sourceImageFiles.value.filter((_, itemIndex) => itemIndex !== index)
  sourceImageUrls.value = sourceImageUrls.value.filter((_, itemIndex) => itemIndex !== index)
  if (sourceFileInput.value) {
    sourceFileInput.value.value = ''
  }
}

function triggerSourceFileSelect() {
  if (sourceFileInput.value) {
    sourceFileInput.value.value = ''
    sourceFileInput.value.click()
  }
}

function handleSourceFileChange(event: Event) {
  const target = event.target as HTMLInputElement
  const files = Array.from(target.files || [])
  if (files.length === 0) {
    return
  }
  appendSourceImageFiles(files)
  target.value = ''
}

function handleSourceDragEnter() {
  sourceDragActive.value = true
}

function handleSourceDragOver() {
  sourceDragActive.value = true
}

function handleSourceDragLeave(event: DragEvent) {
  const currentTarget = event.currentTarget as HTMLElement | null
  const relatedTarget = event.relatedTarget as Node | null
  if (currentTarget && relatedTarget && currentTarget.contains(relatedTarget)) {
    return
  }
  sourceDragActive.value = false
}

function handleSourceDrop(event: DragEvent) {
  sourceDragActive.value = false
  const files = Array.from(event.dataTransfer?.files || [])
  if (files.length > 0) {
    appendSourceImageFiles(files)
  }
}

function clearSourceImages() {
  setSourceImageFiles([])
}

function formatSourceFileSummary(file?: File): string {
  if (!file) {
    return ''
  }
  const sizeKb = Math.max(1, Math.round(file.size / 1024))
  return t('imagePreview.sourceImageSummary', {
    type: file.type || 'image/*',
    sizeKb
  })
}

async function handleSubmit() {
  if (!selectedApiKey.value) {
    appStore.showError(t('imagePreview.selectKeyFirst'))
    return
  }
  const prompt = form.prompt.trim()
  if (!prompt) {
    appStore.showError(t('imagePreview.promptRequired'))
    return
  }
  if (mode.value === 'edit' && sourceImageFiles.value.length === 0) {
    appStore.showError(t('imagePreview.sourceImageRequired'))
    return
  }

  errorMessage.value = ''
  generating.value = true
  activeController?.abort()
  activeController = new AbortController()
  const requestId = `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
  currentRequestId.value = requestId

  const payload = buildPayload(form.model, prompt, form.size)
  const sourceFilesForRequest = mode.value === 'edit' ? [...sourceImageFiles.value] : []
  const previewPayload = sourceFilesForRequest.length > 0
    ? {
        ...payload,
        reference_images: sourceFilesForRequest.map((file) => `(base64 data from ${file.name})`)
      }
    : payload
  lastSubmitted.value = {
    apiKeyId: selectedApiKey.value.id,
    mode: mode.value,
    model: form.model,
    size: form.size,
    upscale: deriveUpscaleFromSize(form.size),
    prompt,
    payload: previewPayload,
    sourceImageNames: sourceFilesForRequest.map((file) => file.name),
    sourceImageTypes: sourceFilesForRequest.map((file) => file.type)
  }
  lastSubmittedSourceFiles.value = sourceFilesForRequest
  persistDraftState()
  startPendingSyncTimer()

  try {
    if (sourceFilesForRequest.length > 0) {
      payload.reference_images = await Promise.all(sourceFilesForRequest.map(fileToBase64DataURL))
    }

    const response = await openAIImagesAPI.generatePreview({
      apiKey: selectedApiKey.value.key,
      payload,
      signal: activeController.signal
    })

    const firstItem = response.data?.[0]
    const imageUrl = firstItem ? buildImageUrl(firstItem) : ''
    if (!firstItem || !imageUrl) {
      throw new Error(t('imagePreview.noImageReturned'))
    }
    if (!isLatestRequest(requestId)) {
      return
    }

    result.value = {
      imageUrl,
      mimeType: getMimeType(firstItem),
      model: form.model,
      size: form.size,
      upscale: deriveUpscaleFromSize(form.size),
      apiKeyName: selectedApiKey.value.name,
      createdAt: new Date(),
      mode: mode.value
    }
    persistResultState()
  } catch (error: any) {
    if (error?.name === 'AbortError') {
      return
    }
    if (!isLatestRequest(requestId)) {
      return
    }
    const message = error?.message || t('imagePreview.generateFailed')
    errorMessage.value = message
    persistDraftState()
    appStore.showError(message)
  } finally {
    if (isLatestRequest(requestId)) {
      generating.value = false
      currentRequestId.value = null
      persistDraftState()
      activeController = null
      stopPendingSyncTimer()
    }
  }
}

function openCurrentRequestModal() {
  if (!currentRequestPreview.value) {
    return
  }
  requestModalState.value = currentRequestPreview.value
  showRequestModal.value = true
}

function buildRequestPreviewForSubmittedRequest(request: SubmittedRequest, apiKey: string): RequestPreviewState {
  return buildGenerateRequestPreview(requestBaseUrl.value, apiKey, request.payload)
}

function openLastRequestModal() {
  if (!lastSubmitted.value) {
    return
  }
  const apiKey = allKeys.value.find((item) => item.id === lastSubmitted.value?.apiKeyId)
  if (!apiKey) {
    appStore.showError(t('imagePreview.lastKeyUnavailable'))
    return
  }
  requestModalState.value = buildRequestPreviewForSubmittedRequest(lastSubmitted.value, apiKey.key)
  showRequestModal.value = true
}

async function retryLastRequest() {
  if (!lastSubmitted.value) {
    return
  }
  if (lastSubmitted.value.mode === 'edit' && !EDIT_MODE_ENABLED) {
    appStore.showWarning(t('imagePreview.modeEditDisabled'))
    return
  }

  selectedApiKeyId.value = lastSubmitted.value.apiKeyId
  mode.value = lastSubmitted.value.mode
  form.model = lastSubmitted.value.model
  form.size = lastSubmitted.value.size || DEFAULT_SIZE
  form.prompt = lastSubmitted.value.prompt

  if (lastSubmitted.value.mode === 'edit') {
    if (lastSubmittedSourceFiles.value.length === 0) {
      appStore.showError(t('imagePreview.editRetrySourceUnavailable'))
      return
    }
    setSourceImageFiles(lastSubmittedSourceFiles.value)
  }

  await handleSubmit()
}

function formatDateTime(date: Date): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short'
  }).format(date)
}

function getTargetLongEdge(size: ImageSize): number {
  switch (size) {
    case '1536x1024':
    case '1024x1536':
      return 1536
    case '1792x1024':
    case '1024x1792':
      return 1792
    case '2048x2048':
      return 2048
    default:
      return 0
  }
}

async function upscaleViaCanvas(imageUrl: string, targetLongEdge: number): Promise<string> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => {
      const sw = img.naturalWidth
      const sh = img.naturalHeight
      const long = Math.max(sw, sh)
      if (long >= targetLongEdge) {
        resolve(imageUrl)
        return
      }
      let dw: number, dh: number
      if (sw >= sh) {
        dw = targetLongEdge
        dh = Math.round(sh * targetLongEdge / sw)
      } else {
        dh = targetLongEdge
        dw = Math.round(sw * targetLongEdge / sh)
      }
      const canvas = document.createElement('canvas')
      canvas.width = dw
      canvas.height = dh
      const ctx = canvas.getContext('2d')
      if (!ctx) {
        resolve(imageUrl)
        return
      }
      ctx.imageSmoothingEnabled = true
      ctx.imageSmoothingQuality = 'high'
      ctx.drawImage(img, 0, 0, dw, dh)
      resolve(canvas.toDataURL('image/png'))
    }
    img.onerror = () => reject(new Error('Failed to load image for upscale'))
    img.src = imageUrl
  })
}

async function downloadCurrentImage() {
  if (!result.value) {
    return
  }
  let downloadUrl = result.value.imageUrl
  const targetLongEdge = getTargetLongEdge(result.value.size)
  if (targetLongEdge > 0) {
    try {
      downloadUrl = await upscaleViaCanvas(downloadUrl, targetLongEdge)
    } catch {
      // fallback to original
    }
  }
  const link = document.createElement('a')
  link.href = downloadUrl
  link.download = buildDownloadFilename(result.value)
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
}

function buildDownloadFilename(preview: PreviewResult): string {
  const timestamp = preview.createdAt.toISOString().replace(/[:.]/g, '-')
  const extension = preview.mimeType.includes('png') ? 'png' : preview.mimeType.includes('jpeg') ? 'jpg' : 'png'
  const operation = preview.mode === 'edit' ? 'edit' : 'generate'
  return `${preview.model}-${operation}-${timestamp}.${extension}`
}
</script>
