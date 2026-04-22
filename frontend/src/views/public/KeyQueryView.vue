<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { useAppStore } from '@/stores/app'
import axios from 'axios'

const { t, locale } = useI18n()
const appStore = useAppStore()
const route = useRoute()

interface UsageLogEntry {
  id: number
  model: string
  input_tokens: number
  output_tokens: number
  cache_creation_tokens: number
  cache_read_tokens: number
  total_tokens: number
  actual_cost: number
  stream: boolean
  duration_ms?: number
  created_at: string
}

interface PaginationInfo {
  page: number
  page_size: number
  total: number
}

interface KeyInfo {
  name: string
  group_name?: string
  status: string
  key_type: string
  valid_days: number
  activated_at: string | null
  expires_at: string | null
  daily_limit: number
  current_period_count: number
  total_requests: number
  remaining_requests: number
  is_expired: boolean
  is_activated: boolean
  // quota_only 类型专用字段
  total_quota_usd?: number
  total_cost_usd?: number
  remaining_quota_usd?: number
  is_exhausted?: boolean
  // time_quota 类型专用字段
  daily_quota_usd?: number
  current_period_cost_usd?: number
  remaining_daily_quota_usd?: number
  // standard 类型：时间段消费统计
  cost_today?: number
  cost_30d?: number
  usage_logs?: UsageLogEntry[]
  pagination?: PaginationInfo
}

const keyInput = ref('')
const loading = ref(false)
const keyInfo = ref<KeyInfo | null>(null)
const error = ref('')
const currentPage = ref(1)
const pageSize = ref(10)

const queryKey = async (page = 1) => {
  if (!keyInput.value.trim()) {
    appStore.showError(t('keyQuery.enterKey'))
    return
  }

  loading.value = true
  error.value = ''
  if (page === 1) {
    keyInfo.value = null
  }

  try {
    const resp = await axios.get('/api/v1/keys/query', {
      params: {
        key: keyInput.value.trim(),
        page: page,
        page_size: pageSize.value
      }
    })
    if (resp.data.code === 0) {
      keyInfo.value = resp.data.data
      currentPage.value = page
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

const formatLogDate = (dateStr: string) => {
  const date = new Date(dateStr)
  return date.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false
  })
}

const getStatusText = (info: KeyInfo) => {
  if (info.status === 'disabled') return t('keyQuery.status.disabled')
  if (info.status === 'exhausted' || info.is_exhausted) return t('keyQuery.status.exhausted')
  if (info.is_expired) return t('keyQuery.status.expired')
  if (info.key_type === 'standard') return info.status === 'active' ? t('keyQuery.status.active') : info.status
  if (info.is_activated) return t('keyQuery.status.active')
  return t('keyQuery.status.pending')
}

const getStatusBadge = (info: KeyInfo) => {
  if (info.status === 'disabled') return 'badge-error'
  if (info.status === 'exhausted' || info.is_exhausted) return 'badge-error'
  if (info.is_expired) return 'badge-warning'
  if (info.key_type === 'standard') return info.status === 'active' ? 'badge-success' : 'badge-warning'
  if (info.is_activated) return 'badge-success'
  return 'badge-info'
}

// 判断是否为 standard 类型（普通 API Key）
const isStandard = computed(() => {
  return keyInfo.value?.key_type === 'standard'
})

// 判断是否为 quota_only 类型
const isQuotaOnly = computed(() => {
  return keyInfo.value?.key_type === 'quota_only'
})

// 判断是否为 time_quota 类型
const isTimeQuota = computed(() => {
  return keyInfo.value?.key_type === 'time_quota'
})

const usagePercent = computed(() => {
  if (!keyInfo.value) return 0
  // standard 类型：基于 quota USD 计算
  if (keyInfo.value.key_type === 'standard') {
    if (!keyInfo.value.total_quota_usd || keyInfo.value.total_quota_usd <= 0) return 0
    return Math.min(100, ((keyInfo.value.total_cost_usd || 0) / keyInfo.value.total_quota_usd) * 100)
  }
  // quota_only 类型：基于美元消费计算
  if (keyInfo.value.key_type === 'quota_only') {
    if (!keyInfo.value.total_quota_usd || keyInfo.value.total_quota_usd <= 0) return 0
    return Math.min(100, ((keyInfo.value.total_cost_usd || 0) / keyInfo.value.total_quota_usd) * 100)
  }
  // time_quota 类型：基于每日 USD 消费计算
  if (keyInfo.value.key_type === 'time_quota') {
    if (!keyInfo.value.daily_quota_usd || keyInfo.value.daily_quota_usd <= 0) return 0
    return Math.min(100, ((keyInfo.value.current_period_cost_usd || 0) / keyInfo.value.daily_quota_usd) * 100)
  }
  // time_limited 类型：基于请求次数计算
  return Math.min(100, (keyInfo.value.current_period_count / keyInfo.value.daily_limit) * 100)
})

const progressColor = computed(() => {
  if (usagePercent.value >= 90) return 'progress-error'
  if (usagePercent.value >= 70) return 'progress-warning'
  return 'progress-success'
})

const totalPages = computed(() => {
  if (!keyInfo.value?.pagination) return 1
  return Math.ceil(keyInfo.value.pagination.total / keyInfo.value.pagination.page_size)
})

const goToPage = (page: number) => {
  if (page >= 1 && page <= totalPages.value) {
    queryKey(page)
  }
}

// Auto-query if key parameter is present in URL
onMounted(() => {
  const keyParam = route.query.key as string
  if (keyParam) {
    keyInput.value = keyParam
    queryKey()
  }
})
</script>

<template>
  <div class="min-h-screen bg-gradient-to-br from-base-200 via-base-100 to-base-200 flex items-center justify-center p-4">
    <div class="w-full max-w-2xl">
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
                @keyup.enter="queryKey(1)"
              />
              <button
                class="btn btn-primary absolute right-0 top-0 rounded-l-none"
                :disabled="loading"
                @click="queryKey(1)"
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
              <!-- standard 类型：显示总额度 -->
              <div v-if="isStandard && keyInfo.total_quota_usd" class="stat bg-base-200/30 rounded-xl p-4">
                <div class="stat-title text-xs">{{ t('keyQuery.totalQuota') }}</div>
                <div class="stat-value text-xl text-primary">${{ keyInfo.total_quota_usd?.toFixed(2) || '0.00' }}</div>
                <div class="stat-desc">USD</div>
              </div>
              <div v-else-if="isStandard" class="stat bg-base-200/30 rounded-xl p-4">
                <div class="stat-title text-xs">{{ t('keyQuery.totalQuota') }}</div>
                <div class="stat-value text-xl text-primary">{{ t('keyQuery.unlimited') || 'Unlimited' }}</div>
              </div>
              <!-- quota_only 类型：显示总额度 -->
              <div v-else-if="isQuotaOnly" class="stat bg-base-200/30 rounded-xl p-4">
                <div class="stat-title text-xs">{{ t('keyQuery.totalQuota') }}</div>
                <div class="stat-value text-xl text-primary">${{ keyInfo.total_quota_usd?.toFixed(2) || '0.00' }}</div>
                <div class="stat-desc">USD</div>
              </div>
              <!-- time_quota 类型：显示每日额度 + 有效天数 -->
              <div v-else-if="isTimeQuota" class="stat bg-base-200/30 rounded-xl p-4">
                <div class="stat-title text-xs">{{ t('keyQuery.validDays') }}</div>
                <div class="stat-value text-xl text-primary">{{ keyInfo.valid_days }}</div>
                <div class="stat-desc">{{ t('keyQuery.days') }}</div>
              </div>
              <!-- time_limited 类型：显示有效天数 -->
              <div v-else class="stat bg-base-200/30 rounded-xl p-4">
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
              <div v-if="!isStandard" class="flex items-center justify-between text-sm">
                <span class="text-base-content/60">{{ t('keyQuery.activatedAt') }}</span>
                <span class="font-mono">{{ formatDate(keyInfo.activated_at) }}</span>
              </div>
              <div v-if="isStandard" class="flex items-center justify-between text-sm">
                <span class="text-base-content/60">{{ t('keyQuery.createdAt') || '创建时间' }}</span>
                <span class="font-mono">{{ formatDate(keyInfo.activated_at) }}</span>
              </div>
              <!-- time_limited / time_quota 类型才显示过期时间 -->
              <div v-if="!isQuotaOnly && !isStandard" class="flex items-center justify-between text-sm">
                <span class="text-base-content/60">{{ t('keyQuery.expiresAt') }}</span>
                <span class="font-mono" :class="keyInfo.is_expired ? 'text-error' : ''">{{ formatDate(keyInfo.expires_at) }}</span>
              </div>
            </div>

            <div class="divider text-xs text-base-content/40">{{ t('keyQuery.usageInfo') }}</div>

            <div class="space-y-3">
              <!-- standard 类型：显示 quota 消费 -->
              <template v-if="isStandard && keyInfo.total_quota_usd">
                <div class="flex items-center justify-between text-sm">
                  <span class="text-base-content/60">{{ t('keyQuery.costUsed') }} / {{ t('keyQuery.totalQuota') }}</span>
                  <span class="font-semibold">${{ (keyInfo.total_cost_usd || 0).toFixed(4) }} / ${{ (keyInfo.total_quota_usd || 0).toFixed(2) }}</span>
                </div>
                <progress
                  class="progress w-full h-3"
                  :class="progressColor"
                  :value="keyInfo.total_cost_usd || 0"
                  :max="keyInfo.total_quota_usd || 1"
                ></progress>
                <div class="flex items-center justify-between">
                  <span class="text-base-content/60 text-sm">{{ t('keyQuery.remainingQuota') }}</span>
                  <span
                    class="text-2xl font-bold"
                    :class="(keyInfo.remaining_quota_usd || 0) > 0 ? 'text-success' : 'text-error'"
                  >
                    ${{ (keyInfo.remaining_quota_usd || 0).toFixed(4) }}
                  </span>
                </div>
                <div class="grid grid-cols-2 gap-4 mt-2">
                  <div class="flex items-center justify-between text-sm">
                    <span class="text-base-content/60">{{ t('keyQuery.usageToday') }}</span>
                    <span class="font-mono text-warning">${{ (keyInfo.cost_today || 0).toFixed(4) }}</span>
                  </div>
                  <div class="flex items-center justify-between text-sm">
                    <span class="text-base-content/60">{{ t('keyQuery.usage30d') }}</span>
                    <span class="font-mono text-warning">${{ (keyInfo.cost_30d || 0).toFixed(4) }}</span>
                  </div>
                </div>
              </template>
              <!-- standard 类型无 quota：显示消费统计 -->
              <template v-else-if="isStandard">
                <div class="grid grid-cols-2 gap-4">
                  <div class="stat bg-base-200/30 rounded-xl p-4">
                    <div class="stat-title text-xs">{{ t('keyQuery.usageToday') }}</div>
                    <div class="stat-value text-lg text-warning">${{ (keyInfo.cost_today || 0).toFixed(4) }}</div>
                  </div>
                  <div class="stat bg-base-200/30 rounded-xl p-4">
                    <div class="stat-title text-xs">{{ t('keyQuery.usage30d') }}</div>
                    <div class="stat-value text-lg text-warning">${{ (keyInfo.cost_30d || 0).toFixed(4) }}</div>
                  </div>
                </div>
              </template>
              <!-- quota_only 类型：显示美元消费 -->
              <template v-else-if="isQuotaOnly">
                <div class="flex items-center justify-between text-sm">
                  <span class="text-base-content/60">{{ t('keyQuery.costUsed') }} / {{ t('keyQuery.totalQuota') }}</span>
                  <span class="font-semibold">${{ (keyInfo.total_cost_usd || 0).toFixed(4) }} / ${{ (keyInfo.total_quota_usd || 0).toFixed(2) }}</span>
                </div>
                <progress
                  class="progress w-full h-3"
                  :class="progressColor"
                  :value="keyInfo.total_cost_usd || 0"
                  :max="keyInfo.total_quota_usd || 1"
                ></progress>
                <div class="flex items-center justify-between">
                  <span class="text-base-content/60 text-sm">{{ t('keyQuery.remainingQuota') }}</span>
                  <span
                    class="text-2xl font-bold"
                    :class="(keyInfo.remaining_quota_usd || 0) > 0 ? 'text-success' : 'text-error'"
                  >
                    ${{ (keyInfo.remaining_quota_usd || 0).toFixed(4) }}
                  </span>
                </div>
              </template>
              <!-- time_quota 类型：显示每日 USD 消费 -->
              <template v-else-if="isTimeQuota">
                <div class="flex items-center justify-between text-sm">
                  <span class="text-base-content/60">{{ t('keyQuery.costUsed') }} / {{ t('keyQuery.dailyLimit') }}</span>
                  <span class="font-semibold">${{ (keyInfo.current_period_cost_usd || 0).toFixed(4) }} / ${{ (keyInfo.daily_quota_usd || 0).toFixed(2) }}</span>
                </div>
                <progress
                  class="progress w-full h-3"
                  :class="progressColor"
                  :value="keyInfo.current_period_cost_usd || 0"
                  :max="keyInfo.daily_quota_usd || 1"
                ></progress>
                <div class="flex items-center justify-between">
                  <span class="text-base-content/60 text-sm">{{ t('keyQuery.remaining') }}</span>
                  <span
                    class="text-2xl font-bold"
                    :class="(keyInfo.remaining_daily_quota_usd || 0) > 0 ? 'text-success' : 'text-error'"
                  >
                    ${{ (keyInfo.remaining_daily_quota_usd || 0).toFixed(4) }}
                  </span>
                </div>
              </template>
              <!-- time_limited 类型：显示请求次数 -->
              <template v-else>
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
              </template>
            </div>

            <!-- Usage Logs Section -->
            <div v-if="keyInfo.usage_logs && keyInfo.usage_logs.length > 0">
              <div class="divider text-xs text-base-content/40">{{ t('keyQuery.usageLogs') || '使用日志' }}</div>

              <div class="overflow-x-auto">
                <table class="table table-sm">
                  <thead>
                    <tr>
                      <th class="text-xs">{{ t('keyQuery.logTime') || '时间' }}</th>
                      <th class="text-xs">{{ t('keyQuery.logModel') || '模型' }}</th>
                      <th class="text-xs text-right">{{ t('keyQuery.logTokens') || 'Tokens' }}</th>
                      <th class="text-xs text-right">{{ t('keyQuery.logCost') || '消耗' }}</th>
                      <th class="text-xs text-right">{{ t('keyQuery.logDuration') || '耗时' }}</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="log in keyInfo.usage_logs" :key="log.id" class="hover">
                      <td class="font-mono text-xs">{{ formatLogDate(log.created_at) }}</td>
                      <td>
                        <span class="badge badge-ghost badge-sm">{{ log.model }}</span>
                      </td>
                      <td class="text-right">
                        <div>
                          <span class="text-xs text-base-content/60">{{ log.input_tokens.toLocaleString() }}</span>
                          <span class="text-xs text-base-content/40 mx-1">/</span>
                          <span class="text-xs text-base-content/60">{{ log.output_tokens.toLocaleString() }}</span>
                          <span class="text-xs text-base-content/40 mx-1">=</span>
                          <span class="font-semibold text-sm">{{ log.total_tokens.toLocaleString() }}</span>
                        </div>
                        <div v-if="log.cache_creation_tokens > 0 || log.cache_read_tokens > 0" class="text-xs text-base-content/50">
                          <span>{{ t('keyQuery.cache') || '缓存' }}: </span>
                          <span v-if="log.cache_creation_tokens > 0">+{{ log.cache_creation_tokens.toLocaleString() }}</span>
                          <span v-if="log.cache_creation_tokens > 0 && log.cache_read_tokens > 0"> / </span>
                          <span v-if="log.cache_read_tokens > 0">{{ log.cache_read_tokens.toLocaleString() }}</span>
                        </div>
                      </td>
                      <td class="text-right">
                        <span class="text-xs font-mono text-warning">${{ log.actual_cost.toFixed(4) }}</span>
                      </td>
                      <td class="text-right text-xs">
                        <span v-if="log.duration_ms" class="font-mono">{{ (log.duration_ms / 1000).toFixed(1) }}s</span>
                        <span v-else class="text-base-content/40">-</span>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>

              <!-- Pagination -->
              <div v-if="keyInfo.pagination && totalPages > 1" class="flex justify-center mt-4">
                <div class="join">
                  <button
                    class="join-item btn btn-sm"
                    :disabled="currentPage <= 1"
                    @click="goToPage(currentPage - 1)"
                  >
                    «
                  </button>
                  <button class="join-item btn btn-sm">
                    {{ currentPage }} / {{ totalPages }}
                  </button>
                  <button
                    class="join-item btn btn-sm"
                    :disabled="currentPage >= totalPages"
                    @click="goToPage(currentPage + 1)"
                  >
                    »
                  </button>
                </div>
              </div>
            </div>

            <!-- No logs message -->
            <div v-else-if="keyInfo.total_requests === 0" class="text-center py-4">
              <p class="text-base-content/40 text-sm">{{ t('keyQuery.noLogs') || '暂无使用记录' }}</p>
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
