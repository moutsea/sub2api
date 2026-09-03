<template>
  <div v-if="showUsageWindows">
    <!-- Anthropic OAuth and Setup Token accounts: fetch real usage data -->
    <template
      v-if="
        account.platform === 'anthropic' &&
        (account.type === 'oauth' || account.type === 'setup-token')
      "
    >
      <!-- Loading state -->
      <div v-if="loading" class="space-y-1.5">
        <!-- OAuth: 3 rows, Setup Token: 1 row -->
        <div class="flex items-center gap-1">
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        </div>
        <template v-if="account.type === 'oauth'">
          <div class="flex items-center gap-1">
            <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
            <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
            <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          </div>
          <div class="flex items-center gap-1">
            <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
            <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
            <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          </div>
        </template>
      </div>

      <!-- Error state -->
      <div v-else-if="error" class="text-xs text-red-500">
        {{ error }}
      </div>

      <!-- Usage data -->
      <div v-else-if="usageInfo" class="space-y-1">
        <!-- 5h Window -->
        <UsageProgressBar
          v-if="usageInfo.five_hour"
          label="5h"
          :utilization="usageInfo.five_hour.utilization"
          :resets-at="usageInfo.five_hour.resets_at"
          :window-stats="usageInfo.five_hour.window_stats"
          color="indigo"
        />

        <!-- 7d Window (OAuth only) -->
        <UsageProgressBar
          v-if="usageInfo.seven_day"
          label="7d"
          :utilization="usageInfo.seven_day.utilization"
          :resets-at="usageInfo.seven_day.resets_at"
          color="emerald"
        />

        <!-- 7d Sonnet Window (OAuth only) -->
        <UsageProgressBar
          v-if="usageInfo.seven_day_sonnet"
          label="7d S"
          :utilization="usageInfo.seven_day_sonnet.utilization"
          :resets-at="usageInfo.seven_day_sonnet.resets_at"
          color="purple"
        />
      </div>

      <!-- No data yet -->
      <div v-else class="text-xs text-gray-400">-</div>
    </template>

    <!-- OpenAI OAuth accounts: show Codex usage from extra field -->
    <template v-else-if="account.platform === 'openai' && account.type === 'oauth'">
      <div v-if="hasCodexUsage" class="space-y-1">
        <!-- 5h Window -->
        <UsageProgressBar
          v-if="codex5hUsedPercent !== null"
          label="5h"
          :utilization="codex5hUsedPercent"
          :resets-at="codex5hResetAt"
          color="indigo"
        />

        <!-- 7d Window -->
        <UsageProgressBar
          v-if="codex7dUsedPercent !== null"
          label="7d"
          :utilization="codex7dUsedPercent"
          :resets-at="codex7dResetAt"
          color="emerald"
        />
      </div>
      <div v-else class="text-xs text-gray-400">-</div>
    </template>

    <!-- Antigravity OAuth accounts: fetch usage from API -->
    <template v-else-if="account.platform === 'antigravity' && account.type === 'oauth'">
      <!-- 账户类型徽章 -->
      <div v-if="antigravityTierLabel" class="mb-1 flex items-center gap-1">
        <span
          :class="[
            'inline-block rounded px-1.5 py-0.5 text-[10px] font-medium',
            antigravityTierClass
          ]"
        >
          {{ antigravityTierLabel }}
        </span>
        <!-- 不合格账户警告图标 -->
        <span
          v-if="hasIneligibleTiers"
          class="group relative cursor-help"
        >
          <svg
            class="h-3.5 w-3.5 text-red-500"
            fill="currentColor"
            viewBox="0 0 20 20"
          >
            <path
              fill-rule="evenodd"
              d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 102 0V6a1 1 0 00-1-1z"
              clip-rule="evenodd"
            />
          </svg>
          <span
            class="pointer-events-none absolute left-0 top-full z-50 mt-1 w-80 whitespace-normal break-words rounded bg-gray-900 px-3 py-2 text-xs leading-relaxed text-white opacity-0 shadow-lg transition-opacity group-hover:opacity-100 dark:bg-gray-700"
          >
            {{ t('admin.accounts.ineligibleWarning') }}
          </span>
        </span>
      </div>

      <!-- Loading state -->
      <div v-if="loading" class="space-y-1.5">
        <div class="flex items-center gap-1">
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        </div>
      </div>

      <!-- Error state -->
      <div v-else-if="error" class="text-xs text-red-500">
        {{ error }}
      </div>

      <!-- Usage data from API -->
      <div v-else-if="hasAntigravityQuotaFromAPI" class="space-y-1">
        <!-- Gemini 3 Pro -->
        <UsageProgressBar
          v-if="antigravity3ProUsageFromAPI !== null"
          :label="t('admin.accounts.usageWindow.gemini3Pro')"
          :utilization="antigravity3ProUsageFromAPI.utilization"
          :resets-at="antigravity3ProUsageFromAPI.resetTime"
          color="indigo"
        />

        <!-- Gemini 3 Flash -->
        <UsageProgressBar
          v-if="antigravity3FlashUsageFromAPI !== null"
          :label="t('admin.accounts.usageWindow.gemini3Flash')"
          :utilization="antigravity3FlashUsageFromAPI.utilization"
          :resets-at="antigravity3FlashUsageFromAPI.resetTime"
          color="emerald"
        />

        <!-- Gemini 3 Image -->
        <UsageProgressBar
          v-if="antigravity3ImageUsageFromAPI !== null"
          :label="t('admin.accounts.usageWindow.gemini3Image')"
          :utilization="antigravity3ImageUsageFromAPI.utilization"
          :resets-at="antigravity3ImageUsageFromAPI.resetTime"
          color="purple"
        />

        <!-- Claude 4.5 -->
        <UsageProgressBar
          v-if="antigravityClaude45UsageFromAPI !== null"
          :label="t('admin.accounts.usageWindow.claude45')"
          :utilization="antigravityClaude45UsageFromAPI.utilization"
          :resets-at="antigravityClaude45UsageFromAPI.resetTime"
          color="amber"
        />
      </div>
      <div v-else class="text-xs text-gray-400">-</div>
    </template>

    <!-- Gemini platform: show quota + local usage window -->
    <template v-else-if="account.platform === 'gemini'">
      <!-- Auth Type + Tier Badge (first line) -->
      <div v-if="geminiAuthTypeLabel" class="mb-1 flex items-center gap-1">
        <span
          :class="[
            'inline-block rounded px-1.5 py-0.5 text-[10px] font-medium',
            geminiTierClass
          ]"
        >
          {{ geminiAuthTypeLabel }}
        </span>
        <!-- Help icon -->
        <span
          class="group relative cursor-help"
        >
          <svg
            class="h-3.5 w-3.5 text-gray-400 hover:text-gray-600 dark:text-gray-500 dark:hover:text-gray-300"
            fill="currentColor"
            viewBox="0 0 20 20"
          >
            <path
              fill-rule="evenodd"
              d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-8-3a1 1 0 00-.867.5 1 1 0 11-1.731-1A3 3 0 0113 8a3.001 3.001 0 01-2 2.83V11a1 1 0 11-2 0v-1a1 1 0 011-1 1 1 0 100-2zm0 8a1 1 0 100-2 1 1 0 000 2z"
              clip-rule="evenodd"
            />
          </svg>
          <span
            class="pointer-events-none absolute left-0 top-full z-50 mt-1 w-80 whitespace-normal break-words rounded bg-gray-900 px-3 py-2 text-xs leading-relaxed text-white opacity-0 shadow-lg transition-opacity group-hover:opacity-100 dark:bg-gray-700"
          >
            <div class="font-semibold mb-1">{{ t('admin.accounts.gemini.quotaPolicy.title') }}</div>
            <div class="mb-2 text-gray-300">{{ t('admin.accounts.gemini.quotaPolicy.note') }}</div>
            <div class="space-y-1">
              <div><strong>{{ geminiQuotaPolicyChannel }}:</strong></div>
              <div class="pl-2">• {{ geminiQuotaPolicyLimits }}</div>
              <div class="mt-2">
                <a :href="geminiQuotaPolicyDocsUrl" target="_blank" rel="noopener noreferrer" class="text-blue-400 hover:text-blue-300 underline">
                  {{ t('admin.accounts.gemini.quotaPolicy.columns.docs') }} →
                </a>
              </div>
            </div>
          </span>
        </span>
      </div>

      <!-- Usage data or unlimited flow -->
      <div class="space-y-1">
        <div v-if="loading" class="space-y-1">
          <div class="flex items-center gap-1">
            <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
            <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
            <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          </div>
        </div>
        <div v-else-if="error" class="text-xs text-red-500">
          {{ error }}
        </div>
        <!-- Gemini: show daily usage bars when available -->
        <div v-else-if="geminiUsageAvailable" class="space-y-1">
          <UsageProgressBar
            v-for="bar in geminiUsageBars"
            :key="bar.key"
            :label="bar.label"
            :utilization="bar.utilization"
            :resets-at="bar.resetsAt"
            :window-stats="bar.windowStats"
            :color="bar.color"
          />
          <p class="mt-1 text-[9px] leading-tight text-gray-400 dark:text-gray-500 italic">
            * {{ t('admin.accounts.gemini.quotaPolicy.simulatedNote') || 'Simulated quota' }}
          </p>
        </div>
        <!-- AI Studio Client OAuth: show unlimited flow (no usage tracking) -->
        <div v-else class="text-xs text-gray-400">
          {{ t('admin.accounts.gemini.rateLimit.unlimited') }}
        </div>
      </div>
    </template>

    <!-- Kiro accounts: show credits balance -->
    <template v-else-if="account.platform === 'kiro'">
      <!-- Loading state -->
      <div v-if="loading" class="space-y-1.5">
        <div class="flex items-center gap-1">
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-1.5 w-16 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
        </div>
      </div>

      <!-- Error state -->
      <div v-else-if="error" class="text-xs text-red-500">
        {{ error }}
      </div>

      <!-- Credits data -->
      <div v-else-if="kiroCreditsInfo" class="space-y-1">
        <!-- Subscription type badge -->
        <div v-if="kiroSubscriptionLabel" class="mb-1">
          <span
            :class="[
              'inline-block rounded px-1.5 py-0.5 text-[10px] font-medium',
              kiroSubscriptionClass
            ]"
          >
            {{ kiroSubscriptionLabel }}
          </span>
        </div>

        <!-- Credits display -->
        <div class="flex items-center gap-2">
          <span class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.kiro.credits') }}
          </span>
          <span class="text-sm font-semibold" :class="kiroCreditsColorClass">
            {{ formatCredits(kiroCreditsInfo.available_credits) }}
          </span>
          <span class="text-xs text-gray-400">/</span>
          <span class="text-xs text-gray-500 dark:text-gray-400">
            {{ formatCredits(kiroCreditsInfo.total_credits) }}
          </span>
        </div>

        <!-- Reset info -->
        <div v-if="kiroCreditsInfo.days_until_reset > 0" class="text-[10px] text-gray-400 dark:text-gray-500">
          {{ t('admin.accounts.kiro.resetIn', { days: kiroCreditsInfo.days_until_reset }) }}
        </div>

        <!-- User email (optional) -->
        <div v-if="kiroCreditsInfo.user_email" class="text-[10px] text-gray-400 dark:text-gray-500 truncate" :title="kiroCreditsInfo.user_email">
          {{ kiroCreditsInfo.user_email }}
        </div>
      </div>

      <div v-else class="text-xs text-gray-400">-</div>
    </template>

    <!-- Grok accounts: show passive xAI quota headers and local usage -->
    <template v-else-if="account.platform === 'grok'">
      <div v-if="loading" class="space-y-1.5">
        <div class="flex items-center gap-1">
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        </div>
      </div>

      <div v-else-if="error" class="text-xs text-red-500">
        {{ error }}
      </div>

      <div v-else-if="usageInfo" class="space-y-1">
        <div v-if="usageInfo.needs_reauth" class="text-[10px] text-orange-600 dark:text-orange-400">
          {{ t('admin.accounts.needsReauth') }}
        </div>
        <div v-if="usageInfo.is_forbidden" class="text-[10px] text-red-600 dark:text-red-400">
          {{ usageInfo.grok_entitlement_status || t('common.forbidden') }}
        </div>
        <div v-if="grokEntitlementLabel" class="text-[10px] text-gray-500 dark:text-gray-400">
          {{ grokEntitlementLabel }}
        </div>
        <div v-if="grokLocalUsage" class="flex items-center gap-1.5 text-[9px] text-gray-500 dark:text-gray-400">
          <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
            {{ formatGrokRequests(grokLocalUsage.requests) }} req
          </span>
          <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
            {{ formatGrokTokens(grokLocalUsage.tokens) }}
          </span>
          <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800" :title="t('usage.accountBilled')">
            A ${{ grokLocalUsage.cost.toFixed(2) }}
          </span>
        </div>
        <UsageProgressBar
          v-if="grokRequestQuotaBar"
          :label="t('admin.accounts.usageWindow.grokRequests')"
          :utilization="grokRequestQuotaBar.utilization"
          :resets-at="grokRequestQuotaBar.resetsAt"
          color="indigo"
        />
        <UsageProgressBar
          v-if="grokTokenQuotaBar"
          :label="t('admin.accounts.usageWindow.grokTokens')"
          :utilization="grokTokenQuotaBar.utilization"
          :resets-at="grokTokenQuotaBar.resetsAt"
          color="emerald"
        />
        <div v-if="grokRetryAfterLabel" class="text-[10px] text-amber-600 dark:text-amber-400">
          {{ t('admin.accounts.usageWindow.grokRetryAfter', { time: grokRetryAfterLabel }) }}
        </div>
        <div v-if="grokQuotaUnknown" class="text-[10px] text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.usageWindow.grokUnknown') }}
        </div>
        <div v-else-if="usageInfo.error" class="truncate text-[10px] text-amber-600 dark:text-amber-400" :title="usageInfo.error">
          {{ usageInfo.error }}
        </div>
      </div>

      <div v-else class="text-xs text-gray-400">-</div>
    </template>

    <!-- Other accounts: no usage window -->
    <template v-else>
      <div class="text-xs text-gray-400">-</div>
    </template>
  </div>

  <!-- Non-OAuth/Setup-Token accounts -->
  <div v-else>
    <!-- Gemini API Key accounts: show quota info -->
    <AccountQuotaInfo v-if="account.platform === 'gemini'" :account="account" />
    <div v-else class="text-xs text-gray-400">-</div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { Account, AccountUsageInfo, GeminiCredentials, GrokQuotaWindow, WindowStats } from '@/types'
import UsageProgressBar from './UsageProgressBar.vue'
import AccountQuotaInfo from './AccountQuotaInfo.vue'

const props = defineProps<{
  account: Account
}>()

const { t } = useI18n()

const loading = ref(false)
const error = ref<string | null>(null)
const usageInfo = ref<AccountUsageInfo | null>(null)

// Show usage windows for OAuth and Setup Token accounts
const showUsageWindows = computed(() => {
  // Gemini: we can always compute local usage windows from DB logs (simulated quotas).
  if (props.account.platform === 'gemini') return true
  // Kiro: always show credits if available
  if (props.account.platform === 'kiro') return true
  // Grok: quota snapshots are populated from upstream response headers.
  if (props.account.platform === 'grok') return true
  return props.account.type === 'oauth' || props.account.type === 'setup-token'
})

const shouldFetchUsage = computed(() => {
  if (props.account.platform === 'anthropic') {
    return props.account.type === 'oauth' || props.account.type === 'setup-token'
  }
  if (props.account.platform === 'gemini') {
    return true
  }
  if (props.account.platform === 'antigravity') {
    return props.account.type === 'oauth'
  }
  if (props.account.platform === 'kiro') {
    return true
  }
  if (props.account.platform === 'grok') {
    return true
  }
  return false
})

interface GrokQuotaBarInfo {
  utilization: number
  resetsAt: string | null
}

const makeGrokQuotaBar = (quota?: GrokQuotaWindow | null): GrokQuotaBarInfo | null => {
  if (!quota || quota.limit == null || quota.remaining == null || quota.limit <= 0) return null
  const used = Math.max(0, quota.limit - quota.remaining)
  let resetsAt = quota.reset_at || null
  if (!resetsAt && quota.reset_unix != null) {
    resetsAt = new Date(quota.reset_unix * 1000).toISOString()
  }
  return {
    utilization: Math.min(100, (used / quota.limit) * 100),
    resetsAt
  }
}

const grokRequestQuotaBar = computed(() => makeGrokQuotaBar(usageInfo.value?.grok_request_quota))
const grokTokenQuotaBar = computed(() => makeGrokQuotaBar(usageInfo.value?.grok_token_quota))
const grokQuotaUnknown = computed(() => {
  if (props.account.platform !== 'grok') return false
  if (grokRequestQuotaBar.value || grokTokenQuotaBar.value) return false
  return usageInfo.value?.grok_quota_snapshot_state !== 'observed'
})
const grokLocalUsage = computed(() => usageInfo.value?.grok_local_usage || null)
const grokEntitlementLabel = computed(() => {
  const status = (usageInfo.value?.grok_entitlement_status || '').trim()
  return status || null
})
const grokRetryAfterLabel = computed(() => {
  const seconds = usageInfo.value?.grok_retry_after_seconds
  if (seconds == null || seconds <= 0) return null
  if (seconds < 60) return `${seconds}s`
  return `${Math.ceil(seconds / 60)}m`
})

const formatGrokRequests = (value: number): string => {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return value.toString()
}

const formatGrokTokens = (value: number): string => {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return value.toString()
}

const geminiUsageAvailable = computed(() => {
  return (
    !!usageInfo.value?.gemini_shared_daily ||
    !!usageInfo.value?.gemini_pro_daily ||
    !!usageInfo.value?.gemini_flash_daily ||
    !!usageInfo.value?.gemini_shared_minute ||
    !!usageInfo.value?.gemini_pro_minute ||
    !!usageInfo.value?.gemini_flash_minute
  )
})

// OpenAI Codex usage computed properties
const hasCodexUsage = computed(() => {
  const extra = props.account.extra
  return (
    extra &&
    // Check for new canonical fields first
    (extra.codex_5h_used_percent !== undefined ||
      extra.codex_7d_used_percent !== undefined ||
      // Fallback to legacy fields
      extra.codex_primary_used_percent !== undefined ||
      extra.codex_secondary_used_percent !== undefined)
  )
})

// 5h window usage (prefer canonical field)
const codex5hUsedPercent = computed(() => {
  const extra = props.account.extra
  if (!extra) return null

  // Prefer canonical field
  if (extra.codex_5h_used_percent !== undefined) {
    return extra.codex_5h_used_percent
  }

  // Fallback: detect from legacy fields using window_minutes
  if (
    extra.codex_primary_window_minutes !== undefined &&
    extra.codex_primary_window_minutes <= 360
  ) {
    return extra.codex_primary_used_percent ?? null
  }
  if (
    extra.codex_secondary_window_minutes !== undefined &&
    extra.codex_secondary_window_minutes <= 360
  ) {
    return extra.codex_secondary_used_percent ?? null
  }

  // Legacy assumption: secondary = 5h (may be incorrect)
  return extra.codex_secondary_used_percent ?? null
})

const codex5hResetAt = computed(() => {
  const extra = props.account.extra
  if (!extra) return null

  // Prefer canonical field
  if (extra.codex_5h_reset_after_seconds !== undefined) {
    const resetTime = new Date(Date.now() + extra.codex_5h_reset_after_seconds * 1000)
    return resetTime.toISOString()
  }

  // Fallback: detect from legacy fields using window_minutes
  if (
    extra.codex_primary_window_minutes !== undefined &&
    extra.codex_primary_window_minutes <= 360
  ) {
    if (extra.codex_primary_reset_after_seconds !== undefined) {
      const resetTime = new Date(Date.now() + extra.codex_primary_reset_after_seconds * 1000)
      return resetTime.toISOString()
    }
  }
  if (
    extra.codex_secondary_window_minutes !== undefined &&
    extra.codex_secondary_window_minutes <= 360
  ) {
    if (extra.codex_secondary_reset_after_seconds !== undefined) {
      const resetTime = new Date(Date.now() + extra.codex_secondary_reset_after_seconds * 1000)
      return resetTime.toISOString()
    }
  }

  // Legacy assumption: secondary = 5h
  if (extra.codex_secondary_reset_after_seconds !== undefined) {
    const resetTime = new Date(Date.now() + extra.codex_secondary_reset_after_seconds * 1000)
    return resetTime.toISOString()
  }

  return null
})

// 7d window usage (prefer canonical field)
const codex7dUsedPercent = computed(() => {
  const extra = props.account.extra
  if (!extra) return null

  // Prefer canonical field
  if (extra.codex_7d_used_percent !== undefined) {
    return extra.codex_7d_used_percent
  }

  // Fallback: detect from legacy fields using window_minutes
  if (
    extra.codex_primary_window_minutes !== undefined &&
    extra.codex_primary_window_minutes >= 10000
  ) {
    return extra.codex_primary_used_percent ?? null
  }
  if (
    extra.codex_secondary_window_minutes !== undefined &&
    extra.codex_secondary_window_minutes >= 10000
  ) {
    return extra.codex_secondary_used_percent ?? null
  }

  // Legacy assumption: primary = 7d (may be incorrect)
  return extra.codex_primary_used_percent ?? null
})

const codex7dResetAt = computed(() => {
  const extra = props.account.extra
  if (!extra) return null

  // Prefer canonical field
  if (extra.codex_7d_reset_after_seconds !== undefined) {
    const resetTime = new Date(Date.now() + extra.codex_7d_reset_after_seconds * 1000)
    return resetTime.toISOString()
  }

  // Fallback: detect from legacy fields using window_minutes
  if (
    extra.codex_primary_window_minutes !== undefined &&
    extra.codex_primary_window_minutes >= 10000
  ) {
    if (extra.codex_primary_reset_after_seconds !== undefined) {
      const resetTime = new Date(Date.now() + extra.codex_primary_reset_after_seconds * 1000)
      return resetTime.toISOString()
    }
  }
  if (
    extra.codex_secondary_window_minutes !== undefined &&
    extra.codex_secondary_window_minutes >= 10000
  ) {
    if (extra.codex_secondary_reset_after_seconds !== undefined) {
      const resetTime = new Date(Date.now() + extra.codex_secondary_reset_after_seconds * 1000)
      return resetTime.toISOString()
    }
  }

  // Legacy assumption: primary = 7d
  if (extra.codex_primary_reset_after_seconds !== undefined) {
    const resetTime = new Date(Date.now() + extra.codex_primary_reset_after_seconds * 1000)
    return resetTime.toISOString()
  }

  return null
})

// Antigravity quota types (用于 API 返回的数据)
interface AntigravityUsageResult {
  utilization: number
  resetTime: string | null
}

// ===== Antigravity quota from API (usageInfo.antigravity_quota) =====

// 检查是否有从 API 获取的配额数据
const hasAntigravityQuotaFromAPI = computed(() => {
  return usageInfo.value?.antigravity_quota && Object.keys(usageInfo.value.antigravity_quota).length > 0
})

// 从 API 配额数据中获取使用率（多模型取最高使用率）
const getAntigravityUsageFromAPI = (
  modelNames: string[]
): AntigravityUsageResult | null => {
  const quota = usageInfo.value?.antigravity_quota
  if (!quota) return null

  let maxUtilization = 0
  let earliestReset: string | null = null

  for (const model of modelNames) {
    const modelQuota = quota[model]
    if (!modelQuota) continue

    if (modelQuota.utilization > maxUtilization) {
      maxUtilization = modelQuota.utilization
    }
    if (modelQuota.reset_time) {
      if (!earliestReset || modelQuota.reset_time < earliestReset) {
        earliestReset = modelQuota.reset_time
      }
    }
  }

  // 如果没有找到任何匹配的模型
  if (maxUtilization === 0 && earliestReset === null) {
    const hasAnyData = modelNames.some((m) => quota[m])
    if (!hasAnyData) return null
  }

  return {
    utilization: maxUtilization,
    resetTime: earliestReset
  }
}

// Gemini 3 Pro from API
const antigravity3ProUsageFromAPI = computed(() =>
  getAntigravityUsageFromAPI(['gemini-3-pro-low', 'gemini-3-pro-high', 'gemini-3-pro-preview'])
)

// Gemini 3 Flash from API
const antigravity3FlashUsageFromAPI = computed(() => getAntigravityUsageFromAPI(['gemini-3-flash']))

// Gemini 3 Image from API
const antigravity3ImageUsageFromAPI = computed(() => getAntigravityUsageFromAPI(['gemini-3-pro-image']))

// Claude 4.5 from API
const antigravityClaude45UsageFromAPI = computed(() =>
  getAntigravityUsageFromAPI(['claude-sonnet-4-5', 'claude-opus-4-5-thinking'])
)

// Antigravity 账户类型（从 load_code_assist 响应中提取）
const antigravityTier = computed(() => {
  const extra = props.account.extra as Record<string, unknown> | undefined
  if (!extra) return null

  const loadCodeAssist = extra.load_code_assist as Record<string, unknown> | undefined
  if (!loadCodeAssist) return null

  // 优先取 paidTier，否则取 currentTier
  const paidTier = loadCodeAssist.paidTier as Record<string, unknown> | undefined
  if (paidTier && typeof paidTier.id === 'string') {
    return paidTier.id
  }

  const currentTier = loadCodeAssist.currentTier as Record<string, unknown> | undefined
  if (currentTier && typeof currentTier.id === 'string') {
    return currentTier.id
  }

  return null
})

// Gemini 账户类型（从 credentials 中提取）
const geminiTier = computed(() => {
  if (props.account.platform !== 'gemini') return null
  const creds = props.account.credentials as GeminiCredentials | undefined
  return creds?.tier_id || null
})

const geminiOAuthType = computed(() => {
  if (props.account.platform !== 'gemini') return null
  const creds = props.account.credentials as GeminiCredentials | undefined
  return (creds?.oauth_type || '').trim() || null
})

// Gemini 是否为 Code Assist OAuth
const isGeminiCodeAssist = computed(() => {
  if (props.account.platform !== 'gemini') return false
  const creds = props.account.credentials as GeminiCredentials | undefined
  return creds?.oauth_type === 'code_assist' || (!creds?.oauth_type && !!creds?.project_id)
})

const geminiChannelShort = computed((): 'ai studio' | 'gcp' | 'google one' | 'client' | null => {
  if (props.account.platform !== 'gemini') return null

  // API Key accounts are AI Studio.
  if (props.account.type === 'apikey') return 'ai studio'

  if (geminiOAuthType.value === 'google_one') return 'google one'
  if (isGeminiCodeAssist.value) return 'gcp'
  if (geminiOAuthType.value === 'ai_studio') return 'client'

  // Fallback (unknown legacy data): treat as AI Studio.
  return 'ai studio'
})

const geminiUserLevel = computed((): string | null => {
  if (props.account.platform !== 'gemini') return null

  const tier = (geminiTier.value || '').toString().trim()
  const tierLower = tier.toLowerCase()
  const tierUpper = tier.toUpperCase()

  // Google One: free / pro / ultra
  if (geminiOAuthType.value === 'google_one') {
    if (tierLower === 'google_one_free') return 'free'
    if (tierLower === 'google_ai_pro') return 'pro'
    if (tierLower === 'google_ai_ultra') return 'ultra'

    // Backward compatibility (legacy tier markers)
    if (tierUpper === 'AI_PREMIUM' || tierUpper === 'GOOGLE_ONE_STANDARD') return 'pro'
    if (tierUpper === 'GOOGLE_ONE_UNLIMITED') return 'ultra'
    if (tierUpper === 'FREE' || tierUpper === 'GOOGLE_ONE_BASIC' || tierUpper === 'GOOGLE_ONE_UNKNOWN' || tierUpper === '') return 'free'

    return null
  }

  // GCP Code Assist: standard / enterprise
  if (isGeminiCodeAssist.value) {
    if (tierLower === 'gcp_enterprise') return 'enterprise'
    if (tierLower === 'gcp_standard') return 'standard'

    // Backward compatibility
    if (tierUpper.includes('ULTRA') || tierUpper.includes('ENTERPRISE')) return 'enterprise'
    return 'standard'
  }

  // AI Studio (API Key) and Client OAuth: free / paid
  if (props.account.type === 'apikey' || geminiOAuthType.value === 'ai_studio') {
    if (tierLower === 'aistudio_paid') return 'paid'
    if (tierLower === 'aistudio_free') return 'free'

    // Backward compatibility
    if (tierUpper.includes('PAID') || tierUpper.includes('PAYG') || tierUpper.includes('PAY')) return 'paid'
    if (tierUpper.includes('FREE')) return 'free'
    if (props.account.type === 'apikey') return 'free'
    return null
  }

  return null
})

// Gemini 认证类型（按要求：授权方式简称 + 用户等级）
const geminiAuthTypeLabel = computed(() => {
  if (props.account.platform !== 'gemini') return null
  if (!geminiChannelShort.value) return null
  return geminiUserLevel.value ? `${geminiChannelShort.value} ${geminiUserLevel.value}` : geminiChannelShort.value
})

// Gemini 账户类型徽章样式（统一样式）
const geminiTierClass = computed(() => {
  // Use channel+level to choose a stable color without depending on raw tier_id variants.
  const channel = geminiChannelShort.value
  const level = geminiUserLevel.value

  if (channel === 'client' || channel === 'ai studio') {
    return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
  }

  if (channel === 'google one') {
    if (level === 'ultra') return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    if (level === 'pro') return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
  }

  if (channel === 'gcp') {
    if (level === 'enterprise') return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
  }

  return ''
})

// Gemini 配额政策信息
const geminiQuotaPolicyChannel = computed(() => {
  if (geminiOAuthType.value === 'google_one') {
    return t('admin.accounts.gemini.quotaPolicy.rows.googleOne.channel')
  }
  if (isGeminiCodeAssist.value) {
    return t('admin.accounts.gemini.quotaPolicy.rows.gcp.channel')
  }
  return t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.channel')
})

const geminiQuotaPolicyLimits = computed(() => {
  const tierLower = (geminiTier.value || '').toString().trim().toLowerCase()

  if (geminiOAuthType.value === 'google_one') {
    if (tierLower === 'google_ai_ultra' || geminiUserLevel.value === 'ultra') {
      return t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsUltra')
    }
    if (tierLower === 'google_ai_pro' || geminiUserLevel.value === 'pro') {
      return t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsPro')
    }
    return t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsFree')
  }

  if (isGeminiCodeAssist.value) {
    if (tierLower === 'gcp_enterprise' || geminiUserLevel.value === 'enterprise') {
      return t('admin.accounts.gemini.quotaPolicy.rows.gcp.limitsEnterprise')
    }
    return t('admin.accounts.gemini.quotaPolicy.rows.gcp.limitsStandard')
  }

  // AI Studio (API Key / custom OAuth)
  if (tierLower === 'aistudio_paid' || geminiUserLevel.value === 'paid') {
    return t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.limitsPaid')
  }
  return t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.limitsFree')
})

const geminiQuotaPolicyDocsUrl = computed(() => {
  if (geminiOAuthType.value === 'google_one' || isGeminiCodeAssist.value) {
    return 'https://developers.google.com/gemini-code-assist/resources/quotas'
  }
  return 'https://ai.google.dev/pricing'
})

const geminiUsesSharedDaily = computed(() => {
  if (props.account.platform !== 'gemini') return false
  // Per requirement: Google One & GCP are shared RPD pools (no per-model breakdown).
  return (
    !!usageInfo.value?.gemini_shared_daily ||
    !!usageInfo.value?.gemini_shared_minute ||
    geminiOAuthType.value === 'google_one' ||
    isGeminiCodeAssist.value
  )
})

const geminiUsageBars = computed(() => {
  if (props.account.platform !== 'gemini') return []
  if (!usageInfo.value) return []

  const bars: Array<{
    key: string
    label: string
    utilization: number
    resetsAt: string | null
    windowStats?: WindowStats | null
    color: 'indigo' | 'emerald'
  }> = []

  if (geminiUsesSharedDaily.value) {
    const sharedDaily = usageInfo.value.gemini_shared_daily
    if (sharedDaily) {
      bars.push({
        key: 'shared_daily',
        label: '1d',
        utilization: sharedDaily.utilization,
        resetsAt: sharedDaily.resets_at,
        windowStats: sharedDaily.window_stats,
        color: 'indigo'
      })
    }
    return bars
  }

  const pro = usageInfo.value.gemini_pro_daily
  if (pro) {
    bars.push({
      key: 'pro_daily',
      label: 'pro',
      utilization: pro.utilization,
      resetsAt: pro.resets_at,
      windowStats: pro.window_stats,
      color: 'indigo'
      })
  }

  const flash = usageInfo.value.gemini_flash_daily
  if (flash) {
    bars.push({
      key: 'flash_daily',
      label: 'flash',
      utilization: flash.utilization,
      resetsAt: flash.resets_at,
      windowStats: flash.window_stats,
      color: 'emerald'
    })
  }

  return bars
})

// 账户类型显示标签
const antigravityTierLabel = computed(() => {
  switch (antigravityTier.value) {
    case 'free-tier':
      return t('admin.accounts.tier.free')
    case 'g1-pro-tier':
      return t('admin.accounts.tier.pro')
    case 'g1-ultra-tier':
      return t('admin.accounts.tier.ultra')
    default:
      return null
  }
})

// 账户类型徽章样式
const antigravityTierClass = computed(() => {
  switch (antigravityTier.value) {
    case 'free-tier':
      return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
    case 'g1-pro-tier':
      return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    case 'g1-ultra-tier':
      return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    default:
      return ''
  }
})

// 检测账户是否有不合格状态（ineligibleTiers）
const hasIneligibleTiers = computed(() => {
  const extra = props.account.extra as Record<string, unknown> | undefined
  if (!extra) return false

  const loadCodeAssist = extra.load_code_assist as Record<string, unknown> | undefined
  if (!loadCodeAssist) return false

  const ineligibleTiers = loadCodeAssist.ineligibleTiers as unknown[] | undefined
  return Array.isArray(ineligibleTiers) && ineligibleTiers.length > 0
})

// ===== Kiro credits from API (usageInfo.kiro_credits) =====

// Kiro credits info from API
const kiroCreditsInfo = computed(() => {
  return usageInfo.value?.kiro_credits || null
})

// Kiro subscription type label
const kiroSubscriptionLabel = computed(() => {
  if (!kiroCreditsInfo.value?.subscription_type) return null
  const subType = kiroCreditsInfo.value.subscription_type.toLowerCase()
  if (subType.includes('pro')) return 'Pro'
  if (subType.includes('free')) return 'Free'
  if (subType.includes('builder')) return 'Builder'
  return kiroCreditsInfo.value.subscription_type
})

// Kiro subscription class
const kiroSubscriptionClass = computed(() => {
  if (!kiroCreditsInfo.value?.subscription_type) return ''
  const subType = kiroCreditsInfo.value.subscription_type.toLowerCase()
  if (subType.includes('pro') || subType.includes('builder')) {
    return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
  }
  return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
})

// Kiro credits color class based on available credits
const kiroCreditsColorClass = computed(() => {
  if (!kiroCreditsInfo.value) return 'text-gray-900 dark:text-white'
  const available = kiroCreditsInfo.value.available_credits
  const total = kiroCreditsInfo.value.total_credits
  if (total <= 0) return 'text-gray-900 dark:text-white'

  const ratio = available / total
  if (ratio <= 0.1) return 'text-red-500 dark:text-red-400'
  if (ratio <= 0.3) return 'text-orange-500 dark:text-orange-400'
  return 'text-green-600 dark:text-green-400'
})

// Format credits value
const formatCredits = (value: number | null | undefined): string => {
  if (value === null || value === undefined) return '0'
  // Round to 2 decimal places
  return value.toFixed(2)
}

const loadUsage = async () => {
  if (!shouldFetchUsage.value) return

  loading.value = true
  error.value = null

  try {
    usageInfo.value = await adminAPI.accounts.getUsage(props.account.id)
  } catch (e: any) {
    error.value = t('common.error')
    console.error('Failed to load usage:', e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadUsage()
})
</script>
