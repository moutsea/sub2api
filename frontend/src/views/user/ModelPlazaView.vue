<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <div class="mb-2 flex items-center gap-2 text-primary-600 dark:text-primary-400">
            <Icon name="cube" size="md" />
            <span class="text-xs font-semibold uppercase tracking-[0.2em]">AI Catalog</span>
          </div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('modelPlaza.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('modelPlaza.subtitle') }}</p>
        </div>
        <button class="btn btn-secondary" :disabled="loading" @click="loadChannels">
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
          <span class="ml-2">{{ t('modelPlaza.refresh') }}</span>
        </button>
      </div>

      <div class="card p-4">
        <div class="relative flex-1">
          <Icon name="search" size="sm" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input v-model="searchQuery" class="input pl-9" :placeholder="t('modelPlaza.searchPlaceholder')" />
        </div>
      </div>

      <div v-if="loading" class="grid gap-5 lg:grid-cols-2">
        <div v-for="index in 4" :key="index" class="card h-64 animate-pulse p-6">
          <div class="h-5 w-1/3 rounded bg-gray-200 dark:bg-dark-700"></div>
          <div class="mt-4 h-4 w-2/3 rounded bg-gray-200 dark:bg-dark-700"></div>
          <div class="mt-8 h-20 rounded bg-gray-100 dark:bg-dark-800"></div>
        </div>
      </div>

      <div v-else-if="filteredChannels.length === 0" class="card p-12 text-center">
        <Icon name="cube" size="xl" class="mx-auto mb-4 text-gray-300 dark:text-dark-600" />
        <p class="text-lg font-medium text-gray-900 dark:text-white">{{ t('modelPlaza.noChannels') }}</p>
      </div>

      <div v-else class="grid gap-5 lg:grid-cols-2">
        <article
          v-for="channel in filteredChannels"
          :key="channel.id"
          class="card overflow-hidden transition-shadow hover:shadow-lg"
        >
          <div class="border-b border-gray-100 bg-gradient-to-r from-gray-50 to-white p-5 dark:border-dark-700 dark:from-dark-800 dark:to-dark-900">
            <div class="flex items-start justify-between gap-4">
              <div class="flex min-w-0 items-center gap-3">
                <div class="flex h-11 w-11 flex-shrink-0 items-center justify-center rounded-2xl bg-primary-100 text-primary-600 dark:bg-primary-900/30 dark:text-primary-400">
                  <Icon name="cube" size="md" />
                </div>
                <div class="min-w-0">
                  <h2 class="truncate text-lg font-semibold text-gray-900 dark:text-white">{{ channel.name }}</h2>
                </div>
              </div>
              <div class="flex-shrink-0 rounded-xl bg-primary-50 px-3 py-2 text-right dark:bg-primary-900/20">
                <div class="text-[10px] font-semibold uppercase tracking-wide text-primary-600 dark:text-primary-400">{{ t('modelPlaza.multiplier') }}</div>
                <div class="text-lg font-bold text-primary-700 dark:text-primary-300">{{ formatMultiplier(channel.rate_multiplier) }}x</div>
              </div>
            </div>
            <p v-if="channel.description" class="mt-4 line-clamp-2 text-sm text-gray-500 dark:text-dark-400">{{ channel.description }}</p>
          </div>

          <div class="p-5">
            <div class="mb-3 flex items-center justify-between">
              <span class="text-sm font-medium text-gray-700 dark:text-dark-300">{{ t('modelPlaza.models') }}</span>
              <span class="text-xs text-gray-400 dark:text-dark-500">{{ t('modelPlaza.modelCount', { count: channel.supported_models.length }) }}</span>
            </div>
            <div v-if="channel.supported_models.length" class="flex flex-wrap gap-2">
              <div
                v-for="model in channel.supported_models"
                :key="model"
                class="rounded-lg border border-gray-200 bg-gray-50 px-2.5 py-1.5 text-xs text-gray-700 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200"
              >
                <div class="font-mono">{{ model }}</div>
                <div v-if="getModelPrice(channel, model)" class="mt-1 flex flex-wrap gap-x-2 gap-y-0.5 text-[10px] text-gray-500 dark:text-dark-400">
                  <span>{{ t('modelPlaza.inputPrice') }} {{ formatPrice(getModelPrice(channel, model)?.input_price_per_mtok) }} {{ t('modelPlaza.priceUnit') }}</span>
                  <span>{{ t('modelPlaza.outputPrice') }} {{ formatPrice(getModelPrice(channel, model)?.output_price_per_mtok) }} {{ t('modelPlaza.priceUnit') }}</span>
                </div>
                <div v-else class="mt-1 text-[10px] text-gray-400 dark:text-dark-500">{{ t('modelPlaza.priceUnavailable') }}</div>
              </div>
            </div>
            <p v-else class="rounded-lg bg-gray-50 px-3 py-4 text-center text-sm text-gray-400 dark:bg-dark-800 dark:text-dark-500">{{ t('modelPlaza.noModels') }}</p>
          </div>
        </article>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import modelPlazaAPI from '@/api/model-plaza'
import type { PublicModelChannel } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()
const channels = ref<PublicModelChannel[]>([])
const loading = ref(true)
const searchQuery = ref('')

const filteredChannels = computed(() => {
  const query = searchQuery.value.trim().toLowerCase()
  return channels.value.filter((channel) => {
    if (!query) return true
    return [channel.name, channel.description, ...channel.supported_models]
      .join(' ')
      .toLowerCase()
      .includes(query)
  })
})

function formatMultiplier(value: number): string {
  return Number(value || 0).toFixed(3).replace(/\.?(0+)$/, '')
}

function getModelPrice(channel: PublicModelChannel, model: string) {
  return channel.model_prices?.[model]
}

function formatPrice(value?: number): string {
  if (value === undefined || !Number.isFinite(value)) return '—'
  const decimals = value >= 1 ? 2 : value >= 0.01 ? 3 : 4
  return `$${value.toFixed(decimals).replace(/\.?0+$/, '')}`
}

async function loadChannels() {
  loading.value = true
  try {
    channels.value = await modelPlazaAPI.getPublicChannels()
  } catch (error) {
    const message = (error as { message?: string })?.message
    appStore.showError(message || t('modelPlaza.failedToLoad'))
  } finally {
    loading.value = false
  }
}

onMounted(loadChannels)
</script>
