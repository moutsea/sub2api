<template>
  <AppLayout>
    <div class="mx-auto max-w-3xl space-y-6">
      <div class="card overflow-hidden">
        <div class="bg-gradient-to-br from-primary-500 to-primary-600 px-6 py-8 text-center">
          <div class="mb-4 inline-flex h-16 w-16 items-center justify-center rounded-2xl bg-white/20 backdrop-blur-sm">
            <Icon name="creditCard" size="xl" class="text-white" />
          </div>
          <p class="text-sm font-medium text-primary-100">{{ t('recharge.currentBalance') }}</p>
          <p class="mt-2 text-4xl font-bold text-white">
            ¥{{ user?.balance?.toFixed(2) || '0.00' }}
          </p>
        </div>
      </div>

      <transition name="fade">
        <div
          v-if="statusMessage"
          :class="[
            'card',
            statusType === 'success'
              ? 'border-emerald-200 bg-emerald-50 dark:border-emerald-800/50 dark:bg-emerald-900/20'
              : 'border-amber-200 bg-amber-50 dark:border-amber-800/50 dark:bg-amber-900/20'
          ]"
        >
          <div class="flex items-start gap-3 p-5">
            <Icon
              :name="statusType === 'success' ? 'checkCircle' : 'exclamationTriangle'"
              size="md"
              :class="
                statusType === 'success'
                  ? 'text-emerald-600 dark:text-emerald-400'
                  : 'text-amber-600 dark:text-amber-400'
              "
            />
            <p
              :class="
                statusType === 'success'
                  ? 'text-sm text-emerald-800 dark:text-emerald-200'
                  : 'text-sm text-amber-800 dark:text-amber-200'
              "
            >
              {{ statusMessage }}
            </p>
          </div>
        </div>
      </transition>

      <div class="card">
        <div class="border-b border-gray-100 px-6 py-5 dark:border-dark-700">
          <h1 class="text-xl font-bold text-gray-900 dark:text-white">{{ t('recharge.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('recharge.description') }}</p>
        </div>

        <form class="space-y-6 p-6" @submit.prevent="handleSubmit">
          <div>
            <label class="input-label">{{ t('recharge.amountLabel') }}</label>
            <div class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-3">
              <button
                v-for="amount in presetAmounts"
                :key="amount"
                type="button"
                :class="[
                  'rounded-xl border px-4 py-4 text-center text-lg font-bold transition-all',
                  selectedAmount === amount && !String(customAmount).trim()
                    ? 'border-primary-500 bg-primary-50 text-primary-700 shadow-sm dark:border-primary-400 dark:bg-primary-900/30 dark:text-primary-200'
                    : 'border-gray-200 bg-white text-gray-900 hover:border-primary-300 hover:bg-primary-50/60 dark:border-dark-700 dark:bg-dark-800 dark:text-white dark:hover:border-primary-500/60 dark:hover:bg-primary-900/20'
                ]"
                @click="selectAmount(amount)"
              >
                ¥{{ amount.toFixed(2) }}
              </button>
            </div>
          </div>

          <div>
            <label for="custom-amount" class="input-label">{{ t('recharge.customAmount') }}</label>
            <div class="relative mt-2">
              <div class="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-4">
                <span class="text-sm font-semibold text-gray-400 dark:text-dark-400">¥</span>
              </div>
              <input
                id="custom-amount"
                v-model="customAmount"
                type="number"
                inputmode="decimal"
                :min="minRechargeAmount"
                step="0.01"
                class="input py-3 pl-9 text-lg"
                :placeholder="t('recharge.customPlaceholder')"
                :disabled="submitting"
              />
            </div>
            <p class="input-hint">{{ t('recharge.minimumHint') }}</p>
          </div>

          <div>
            <label class="input-label">{{ t('recharge.paymentMethod') }}</label>
            <div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
              <button
                v-for="method in paymentMethods"
                :key="method.value"
                type="button"
                :disabled="method.disabled"
                :class="[
                  'flex items-center justify-between rounded-xl border p-4 text-left transition-all',
                  method.disabled
                    ? 'cursor-not-allowed border-gray-200 bg-gray-50 opacity-50 dark:border-dark-700 dark:bg-dark-900'
                    : paymentMethod === method.value
                      ? 'border-primary-500 bg-primary-50 shadow-sm dark:border-primary-400 dark:bg-primary-900/30'
                      : 'border-gray-200 bg-white hover:border-primary-300 hover:bg-primary-50/60 dark:border-dark-700 dark:bg-dark-800 dark:hover:border-primary-500/60 dark:hover:bg-primary-900/20'
                ]"
                @click="!method.disabled && (paymentMethod = method.value)"
              >
                <span>
                  <span class="block text-sm font-semibold text-gray-900 dark:text-white">{{ method.label }}</span>
                  <span class="mt-1 block text-xs text-gray-500 dark:text-dark-400">{{ method.description }}</span>
                </span>
                <span
                  :class="[
                    'flex h-5 w-5 items-center justify-center rounded-full border',
                    paymentMethod === method.value
                      ? 'border-primary-500 bg-primary-500'
                      : 'border-gray-300 dark:border-dark-600'
                  ]"
                >
                  <span v-if="paymentMethod === method.value" class="h-2 w-2 rounded-full bg-white"></span>
                </span>
              </button>
            </div>
          </div>

          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-800">
            <div class="flex items-center justify-between text-sm">
              <span class="text-gray-500 dark:text-dark-400">{{ t('recharge.payAmount') }}</span>
              <span class="text-xl font-bold text-gray-900 dark:text-white">¥{{ effectiveAmountText }}</span>
            </div>
            <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('recharge.stripeHint') }}</p>
          </div>

          <button
            type="submit"
            :disabled="submitting || !canSubmit"
            class="btn btn-primary w-full py-3"
          >
            <svg
              v-if="submitting"
              class="-ml-1 mr-2 h-5 w-5 animate-spin"
              fill="none"
              viewBox="0 0 24 24"
            >
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
              <path
                class="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
              ></path>
            </svg>
            <Icon v-else name="externalLink" size="md" class="mr-2" />
            {{ submitting ? t('recharge.creating') : t('recharge.confirm') }}
          </button>
        </form>
      </div>

      <!-- Payment History -->
      <div class="card">
        <div class="border-b border-gray-100 px-6 py-5 dark:border-dark-700">
          <h2 class="text-lg font-bold text-gray-900 dark:text-white">{{ t('recharge.orderHistory') }}</h2>
        </div>
        <div class="p-6">
          <DataTable :columns="orderColumns" :data="orders" :loading="ordersLoading" row-key="id">
            <template #cell-amount="{ row }">
              <span class="font-semibold">¥{{ row.amount.toFixed(2) }}</span>
            </template>
            <template #cell-payment_method="{ row }">
              {{ row.payment_method === 'wechat_pay' ? t('recharge.wechat') : t('recharge.alipay') }}
            </template>
            <template #cell-status="{ row }">
              <span
                :class="[
                  'inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium',
                  statusBadgeClass(row.status)
                ]"
              >
                {{ statusLabel(row.status) }}
              </span>
            </template>
            <template #cell-created_at="{ row }">
              {{ formatTime(row.created_at) }}
            </template>
            <template #cell-paid_at="{ row }">
              {{ row.paid_at ? formatTime(row.paid_at) : '-' }}
            </template>
            <template #cell-actions="{ row }">
              <a
                v-if="canContinuePayment(row)"
                :href="row.checkout_url"
                target="_blank"
                rel="noopener noreferrer"
                class="text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
              >
                {{ t('recharge.continuePay') }}
              </a>
            </template>
            <template #empty>
              <div class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">
                {{ t('recharge.noOrders') }}
              </div>
            </template>
          </DataTable>
          <Pagination
            v-if="orderPagination.total > 0"
            class="mt-4"
            :page="orderPagination.page"
            :total="orderPagination.total"
            :page-size="orderPagination.page_size"
            @update:page="handleOrderPageChange"
            @update:page-size="handleOrderPageSizeChange"
          />
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { paymentsAPI, type PaymentMethod } from '@/api'
import type { PaymentOrder } from '@/api/payments'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()

const minRechargeAmount = 5
const checkoutExpiryGraceMs = 5 * 60 * 1000
const presetAmounts = [50, 100, 200, 500, 1000, 2000]
const selectedAmount = ref(50)
const customAmount = ref('')
const paymentMethod = ref<PaymentMethod>('wechat_pay')
const submitting = ref(false)
const statusMessage = ref('')
const statusType = ref<'success' | 'warning'>('success')
const nowMs = ref(Date.now())
let nowTimer: number | undefined

const user = computed(() => authStore.user)

const paymentMethods = computed(() => [
  {
    value: 'wechat_pay' as PaymentMethod,
    label: t('recharge.wechat'),
    description: t('recharge.wechatDesc'),
    disabled: false
  },
  {
    value: 'alipay' as PaymentMethod,
    label: t('recharge.alipay'),
    description: t('recharge.alipayComingSoon'),
    disabled: true
  }
])

const effectiveAmount = computed(() => {
  const custom = String(customAmount.value).trim()
  if (custom) {
    const parsed = Number(custom)
    return Number.isFinite(parsed) ? parsed : 0
  }
  return selectedAmount.value
})

const effectiveAmountText = computed(() => {
  return Math.max(effectiveAmount.value, 0).toFixed(2)
})

const canSubmit = computed(() => effectiveAmount.value >= minRechargeAmount && Number.isFinite(effectiveAmount.value))

function selectAmount(amount: number) {
  selectedAmount.value = amount
  customAmount.value = ''
}

async function handleSubmit() {
  if (!canSubmit.value) {
    appStore.showError(t('recharge.invalidAmount'))
    return
  }

  submitting.value = true
  try {
    const result = await paymentsAPI.createCheckoutSession({
      amount: effectiveAmount.value,
      payment_method: paymentMethod.value
    })
    window.open(result.checkout_url, '_blank')
  } catch (error: any) {
    appStore.showError(error?.message || t('recharge.createFailed'))
  } finally {
    submitting.value = false
  }
}

// --- Order History ---
const orders = ref<PaymentOrder[]>([])
const ordersLoading = ref(false)
const orderPagination = reactive({ page: 1, page_size: 10, total: 0, pages: 0 })

const orderColumns = computed(() => [
  { key: 'amount', label: t('recharge.orderAmount'), sortable: false },
  { key: 'payment_method', label: t('recharge.orderMethod'), sortable: false },
  { key: 'status', label: t('recharge.orderStatus'), sortable: false },
  { key: 'created_at', label: t('recharge.orderTime'), sortable: false },
  { key: 'paid_at', label: t('recharge.orderPaidAt'), sortable: false },
  { key: 'actions', label: '', sortable: false }
])

const statusMap: Record<string, string> = {
  paid: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300',
  pending: 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300',
  failed: 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300',
  created: 'bg-gray-100 text-gray-800 dark:bg-gray-800 dark:text-gray-300',
  amount_mismatch: 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'
}

function statusBadgeClass(status: string) {
  return statusMap[status] || statusMap.created
}

function statusLabel(status: string) {
  const map: Record<string, string> = {
    paid: t('recharge.statusPaid'),
    pending: t('recharge.statusPending'),
    failed: t('recharge.statusFailed'),
    created: t('recharge.statusCreated'),
    amount_mismatch: t('recharge.statusMismatch')
  }
  return map[status] || status
}

function formatTime(iso: string) {
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}

function canContinuePayment(row: PaymentOrder) {
  if (!row.checkout_url) {
    return false
  }
  if (!row.expires_at) {
    return true
  }
  const expiresAt = new Date(row.expires_at).getTime()
  return Number.isFinite(expiresAt) && expiresAt - nowMs.value > checkoutExpiryGraceMs
}

async function loadOrders() {
  ordersLoading.value = true
  try {
    const res = await paymentsAPI.getOrders({
      page: orderPagination.page,
      page_size: orderPagination.page_size
    })
    orders.value = res.items || []
    orderPagination.total = res.total
    orderPagination.pages = res.pages
  } catch {
    appStore.showError(t('recharge.loadFailed'))
  } finally {
    ordersLoading.value = false
  }
}

function handleOrderPageChange(page: number) {
  orderPagination.page = page
  loadOrders()
}

function handleOrderPageSizeChange(size: number) {
  orderPagination.page_size = size
  orderPagination.page = 1
  loadOrders()
}

onMounted(async () => {
  nowTimer = window.setInterval(() => {
    nowMs.value = Date.now()
  }, 60_000)

  const status = String(route.query.status || '')
  if (status === 'success') {
    statusType.value = 'success'
    statusMessage.value = t('recharge.successPending')
    try {
      await authStore.refreshUser()
    } catch (error) {
      console.error('Failed to refresh user after payment:', error)
    }
  } else if (status === 'cancelled') {
    statusType.value = 'warning'
    statusMessage.value = t('recharge.cancelled')
  }
  loadOrders()
})

onBeforeUnmount(() => {
  if (nowTimer !== undefined) {
    window.clearInterval(nowTimer)
  }
})
</script>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: all 0.3s ease;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
