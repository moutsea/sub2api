<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import axios from 'axios'

const { t, locale } = useI18n()
const appStore = useAppStore()

interface KeyInfo {
  name: string
  group_name?: string
  status: string
  valid_days: number
  activated_at: string | null
  expires_at: string | null
  daily_limit: number
  current_period_count: number
  total_requests: number
  remaining_requests: number
  is_expired: boolean
  is_activated: boolean
}

const keyInput = ref('')
const loading = ref(false)
const keyInfo = ref<KeyInfo | null>(null)
const error = ref('')

const queryKey = async () => {
  if (!keyInput.value.trim()) {
    appStore.showError(t('keyQuery.enterKey'))
    return
  }

  loading.value = true
  error.value = ''
  keyInfo.value = null

  try {
    const resp = await axios.get('/api/v1/temp-api-keys/query', {
      params: { key: keyInput.value.trim() }
    })
    if (resp.data.code === 0) {
      keyInfo.value = resp.data.data
    } else {
      error.value = resp.data.message || t('keyQuery.notFound')
    }
  } catch (e: unknown) {
    const err = e as { response?: { data?: { message?: string } }; message?: string }
    error.value = err.response?.data?.message || err.message || t('keyQuery.queryFailed')
  } finally {
    loading.value = false
  }
}

const formatDate = (dateStr: string | null) => {
  if (!dateStr) return '-'
  const date = new Date(dateStr)
  const options: Intl.DateTimeFormatOptions = {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    timeZoneName: 'short'
  }
  return date.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', options)
}

const getStatusText = (info: KeyInfo) => {
  if (info.status === 'disabled') return t('keyQuery.status.disabled')
  if (info.is_expired) return t('keyQuery.status.expired')
  if (info.is_activated) return t('keyQuery.status.active')
  return t('keyQuery.status.pending')
}

const getStatusBadge = (info: KeyInfo) => {
  if (info.status === 'disabled') return 'badge-error'
  if (info.is_expired) return 'badge-warning'
  if (info.is_activated) return 'badge-success'
  return 'badge-info'
}

const usagePercent = computed(() => {
  if (!keyInfo.value) return 0
  return Math.min(100, (keyInfo.value.current_period_count / keyInfo.value.daily_limit) * 100)
})

const progressColor = computed(() => {
  if (usagePercent.value >= 90) return 'progress-error'
  if (usagePercent.value >= 70) return 'progress-warning'
  return 'progress-success'
})
</script>

<template>
  <div class="min-h-screen bg-gradient-to-br from-base-200 via-base-100 to-base-200 flex items-center justify-center p-4">
    <div class="w-full max-w-md">
      <div class="text-center mb-8">
        <div class="inline-flex items-center justify-center w-16 h-16 rounded-full bg-primary/10 mb-4">
          <svg xmlns="http://www.w3.org/2000/svg" class="h-8 w-8 text-primary" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z" />
          </svg>
        </div>
        <h1 class="text-2xl font-bold text-base-content">{{ t('keyQuery.title') }}</h1>
        <p class="text-base-content/60 mt-2 text-sm">{{ t('keyQuery.inputLabel') }}</p>
      </div>

      <div class="card bg-base-100 shadow-2xl border border-base-300">
        <div class="card-body p-6">
          <div class="form-control">
            <div class="relative">
              <input
                type="text"
                class="input input-bordered w-full pr-24 focus:input-primary transition-all"
                v-model="keyInput"
                :placeholder="t('keyQuery.placeholder')"
                @keyup.enter="queryKey"
              />
              <button
                class="btn btn-primary absolute right-0 top-0 rounded-l-none"
                :disabled="loading"
                @click="queryKey"
              >
                <span v-if="loading" class="loading loading-spinner loading-sm"></span>
                <svg v-else xmlns="http://www.w3.org/2000/svg" class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                </svg>
              </button>
            </div>
          </div>

          <div v-if="error" class="alert alert-error mt-4 text-sm">
            <svg xmlns="http://www.w3.org/2000/svg" class="stroke-current shrink-0 h-5 w-5" fill="none" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z" />
            </svg>
            <span>{{ error }}</span>
          </div>

          <div v-if="keyInfo" class="mt-6 space-y-6 animate-in fade-in duration-300">
            <div class="flex items-center justify-between p-4 bg-base-200/50 rounded-xl">
              <div>
                <p class="text-xs text-base-content/50 uppercase tracking-wider">{{ t('keyQuery.name') }}</p>
                <p class="font-semibold text-lg">{{ keyInfo.name }}</p>
                <p v-if="keyInfo.group_name" class="text-sm text-base-content/60">{{ keyInfo.group_name }}</p>
              </div>
              <div class="badge gap-1 py-3" :class="getStatusBadge(keyInfo)">
                <span class="w-2 h-2 rounded-full bg-current opacity-75"></span>
                {{ getStatusText(keyInfo) }}
              </div>
            </div>

            <div class="grid grid-cols-2 gap-4">
              <div class="stat bg-base-200/30 rounded-xl p-4">
                <div class="stat-title text-xs">{{ t('keyQuery.validDays') }}</div>
                <div class="stat-value text-xl text-primary">{{ keyInfo.valid_days }}</div>
                <div class="stat-desc">{{ t('keyQuery.days') }}</div>
              </div>
              <div class="stat bg-base-200/30 rounded-xl p-4">
                <div class="stat-title text-xs">{{ t('keyQuery.totalRequests') }}</div>
                <div class="stat-value text-xl">{{ keyInfo.total_requests.toLocaleString() }}</div>
              </div>
            </div>

            <div class="space-y-3">
              <div class="flex items-center justify-between text-sm">
                <span class="text-base-content/60">{{ t('keyQuery.activatedAt') }}</span>
                <span class="font-mono">{{ formatDate(keyInfo.activated_at) }}</span>
              </div>
              <div class="flex items-center justify-between text-sm">
                <span class="text-base-content/60">{{ t('keyQuery.expiresAt') }}</span>
                <span class="font-mono" :class="keyInfo.is_expired ? 'text-error' : ''">{{ formatDate(keyInfo.expires_at) }}</span>
              </div>
            </div>

            <div class="divider text-xs text-base-content/40">{{ t('keyQuery.usageInfo') }}</div>

            <div class="space-y-3">
              <div class="flex items-center justify-between text-sm">
                <span class="text-base-content/60">{{ t('keyQuery.todayUsed') }} / {{ t('keyQuery.dailyLimit') }}</span>
                <span class="font-semibold">{{ keyInfo.current_period_count }} / {{ keyInfo.daily_limit }}</span>
              </div>
              <progress
                class="progress w-full h-3"
                :class="progressColor"
                :value="keyInfo.current_period_count"
                :max="keyInfo.daily_limit"
              ></progress>
              <div class="flex items-center justify-between">
                <span class="text-base-content/60 text-sm">{{ t('keyQuery.remaining') }}</span>
                <span
                  class="text-2xl font-bold"
                  :class="keyInfo.remaining_requests > 0 ? 'text-success' : 'text-error'"
                >
                  {{ keyInfo.remaining_requests }}
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <p class="text-center text-xs text-base-content/40 mt-6">
        © {{ new Date().getFullYear() }} Sub2API
      </p>
    </div>
  </div>
</template>

<style scoped>
.animate-in {
  animation: fadeIn 0.3s ease-out;
}

@keyframes fadeIn {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
