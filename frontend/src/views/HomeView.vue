<template>
  <!-- Custom Home Content: Full Page Mode -->
  <div v-if="homeContent" class="min-h-screen">
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    ></iframe>
    <div v-else v-html="homeContent"></div>
  </div>

  <!-- Default Home Page -->
  <div
    v-else
    class="relative flex min-h-screen flex-col overflow-hidden bg-gradient-to-b from-gray-50 via-white to-gray-50 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
  >
    <!-- Background Decorations -->
    <div class="pointer-events-none absolute inset-0 overflow-hidden">
      <div class="absolute -right-40 -top-40 h-[600px] w-[600px] animate-[float_20s_ease-in-out_infinite] rounded-full bg-primary-400/15 blur-[120px]"></div>
      <div class="absolute -bottom-40 -left-40 h-[500px] w-[500px] animate-[float_25s_ease-in-out_infinite_reverse] rounded-full bg-blue-400/10 blur-[120px]"></div>
      <div class="absolute left-1/2 top-1/4 h-[400px] w-[400px] animate-[float_18s_ease-in-out_infinite_2s] rounded-full bg-violet-400/8 blur-[120px]"></div>
      <div class="absolute inset-0 bg-[linear-gradient(rgba(20,184,166,0.02)_1px,transparent_1px),linear-gradient(90deg,rgba(20,184,166,0.02)_1px,transparent_1px)] bg-[size:72px_72px]"></div>
      <div class="absolute inset-0 bg-[radial-gradient(ellipse_at_top,rgba(20,184,166,0.06),transparent_60%)]"></div>
    </div>

    <!-- Announcement Bar -->
    <div class="relative z-20 border-b border-primary-200/40 bg-gradient-to-r from-primary-500/8 via-primary-400/8 to-primary-500/8 backdrop-blur-md dark:border-primary-800/20 dark:from-primary-500/5 dark:via-primary-400/5 dark:to-primary-500/5">
      <div class="mx-auto flex max-w-6xl items-center justify-center gap-2.5 px-6 py-2">
        <span class="relative flex h-1.5 w-1.5">
          <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary-400 opacity-75"></span>
          <span class="relative inline-flex h-1.5 w-1.5 rounded-full bg-primary-500"></span>
        </span>
        <p class="text-xs font-medium text-gray-600 dark:text-dark-300">
          <span class="text-primary-600 dark:text-primary-400">内部系统</span>，测试申请联系：<span class="font-semibold text-gray-800 dark:text-white">liangtangjhz</span>
        </p>
      </div>
    </div>

    <!-- Header -->
    <header class="relative z-20 px-6 py-5">
      <nav class="mx-auto flex max-w-6xl items-center justify-between rounded-2xl border border-white/30 bg-white/70 px-6 py-3.5 shadow-lg shadow-gray-900/[0.03] backdrop-blur-xl dark:border-dark-700/20 dark:bg-dark-900/70 dark:shadow-black/10">
        <!-- Logo -->
        <div class="flex items-center gap-3">
          <div class="h-9 w-9 overflow-hidden rounded-xl bg-gradient-to-br from-primary-400 to-primary-600 p-0.5 shadow-md shadow-primary-500/20 transition-transform duration-300 hover:scale-105">
            <img :src="siteLogo || '/logo.png'" alt="Logo" class="h-full w-full rounded-[10px] object-contain" />
          </div>
          <span class="hidden text-sm font-bold tracking-tight text-gray-800 dark:text-white sm:inline">{{ siteName }}</span>
        </div>

        <!-- Nav Actions -->
        <div class="flex items-center gap-1">
          <router-link
            to="/key-query"
            class="inline-flex items-center gap-1.5 rounded-xl px-3 py-2 text-sm text-gray-500 transition-all duration-200 hover:bg-gray-100/80 hover:text-gray-800 dark:text-dark-400 dark:hover:bg-dark-800/80 dark:hover:text-white"
            :title="t('home.keyQuery')"
          >
            <svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z" />
            </svg>
            <span class="hidden sm:inline">{{ t('home.keyQuery') }}</span>
          </router-link>

          <router-link
            to="/model-plaza"
            class="inline-flex items-center gap-1.5 rounded-xl px-3 py-2 text-sm text-gray-500 transition-all duration-200 hover:bg-gray-100/80 hover:text-gray-800 dark:text-dark-400 dark:hover:bg-dark-800/80 dark:hover:text-white"
            :title="t('modelPlaza.title')"
          >
            <Icon name="cube" size="md" />
            <span class="hidden sm:inline">{{ t('modelPlaza.title') }}</span>
          </router-link>

          <LocaleSwitcher />

          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="rounded-xl p-2 text-gray-400 transition-all duration-200 hover:bg-gray-100/80 hover:text-gray-600 dark:text-dark-500 dark:hover:bg-dark-800/80 dark:hover:text-white"
            :title="t('home.viewDocs')"
          >
            <Icon name="book" size="md" />
          </a>

          <button
            @click="toggleTheme"
            class="rounded-xl p-2 text-gray-400 transition-all duration-200 hover:bg-gray-100/80 hover:text-gray-600 dark:text-dark-500 dark:hover:bg-dark-800/80 dark:hover:text-white"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
          >
            <Icon v-if="isDark" name="sun" size="md" />
            <Icon v-else name="moon" size="md" />
          </button>

          <div class="mx-1.5 h-5 w-px bg-gray-200/60 dark:bg-dark-700/60"></div>

          <router-link
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="inline-flex items-center gap-2 rounded-xl bg-gradient-to-r from-primary-500 to-primary-600 px-4 py-2 text-sm font-medium text-white shadow-md shadow-primary-500/20 transition-all duration-300 hover:shadow-lg hover:shadow-primary-500/30 hover:brightness-110"
          >
            <span class="flex h-5 w-5 items-center justify-center rounded-full bg-white/20 text-[10px] font-bold">{{ userInitial }}</span>
            {{ t('home.dashboard') }}
            <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5">
              <path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5L21 12m0 0l-7.5 7.5M21 12H3" />
            </svg>
          </router-link>
          <router-link
            v-else
            to="/login"
            class="inline-flex items-center gap-1.5 rounded-xl bg-gradient-to-r from-primary-500 to-primary-600 px-4 py-2 text-sm font-medium text-white shadow-md shadow-primary-500/20 transition-all duration-300 hover:shadow-lg hover:shadow-primary-500/30 hover:brightness-110"
          >
            {{ t('home.login') }}
            <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5">
              <path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5L21 12m0 0l-7.5 7.5M21 12H3" />
            </svg>
          </router-link>
        </div>
      </nav>
    </header>

    <!-- Main Content -->
    <main class="relative z-10 flex-1 px-6 py-16 lg:py-24">
      <div class="mx-auto max-w-6xl">

        <!-- Hero Section -->
        <div class="mb-24 flex flex-col items-center justify-between gap-16 lg:flex-row lg:gap-20">
          <!-- Left: Text Content -->
          <div class="flex-1 text-center lg:text-left">
            <div class="hero-badge mb-8 inline-flex items-center gap-2 rounded-full border border-primary-200/50 bg-primary-50/60 px-4 py-1.5 dark:border-primary-800/30 dark:bg-primary-900/15">
              <span class="relative flex h-2 w-2">
                <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary-400 opacity-75"></span>
                <span class="relative inline-flex h-2 w-2 rounded-full bg-primary-500"></span>
              </span>
              <span class="text-xs font-semibold tracking-wide text-primary-600 dark:text-primary-300">AI API Gateway</span>
            </div>

            <h1 class="hero-title mb-6 text-5xl font-extrabold leading-[1.1] tracking-tight md:text-6xl lg:text-7xl">
              <span class="bg-gradient-to-r from-gray-900 via-gray-800 to-gray-700 bg-clip-text text-transparent dark:from-white dark:via-gray-100 dark:to-gray-300">{{ siteName }}</span>
            </h1>

            <p class="hero-subtitle mb-10 max-w-lg text-lg leading-relaxed text-gray-500 dark:text-dark-400 md:text-xl lg:mx-0 mx-auto">
              {{ siteSubtitle }}
            </p>

            <!-- CTA Buttons -->
            <div class="hero-cta flex flex-col items-center gap-4 sm:flex-row lg:justify-start">
              <router-link
                :to="isAuthenticated ? dashboardPath : '/login'"
                class="group inline-flex items-center gap-2.5 rounded-2xl bg-gradient-to-r from-primary-500 to-primary-600 px-8 py-4 text-base font-semibold text-white shadow-xl shadow-primary-500/20 transition-all duration-300 hover:shadow-2xl hover:shadow-primary-500/30 hover:brightness-110"
              >
                {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
                <Icon name="arrowRight" size="md" class="transition-transform duration-300 group-hover:translate-x-1" :stroke-width="2.5" />
              </router-link>
              <a
                v-if="docUrl"
                :href="docUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="inline-flex items-center gap-2 rounded-2xl border border-gray-200/80 bg-white/80 px-6 py-4 text-base font-medium text-gray-600 shadow-sm backdrop-blur-sm transition-all duration-300 hover:border-gray-300 hover:bg-white hover:text-gray-800 hover:shadow-md dark:border-dark-600/60 dark:bg-dark-800/80 dark:text-dark-300 dark:hover:border-dark-500 dark:hover:bg-dark-800 dark:hover:text-white"
              >
                <Icon name="book" size="md" />
                {{ t('home.docs') }}
              </a>
            </div>
          </div>

          <!-- Right: Terminal Animation -->
          <div class="flex flex-1 justify-center lg:justify-end">
            <div class="terminal-container">
              <div class="terminal-window">
                <div class="terminal-header">
                  <div class="terminal-buttons">
                    <span class="btn-close"></span>
                    <span class="btn-minimize"></span>
                    <span class="btn-maximize"></span>
                  </div>
                  <span class="terminal-title">terminal</span>
                </div>
                <div class="terminal-body">
                  <div class="code-line line-1">
                    <span class="code-prompt">$</span>
                    <span class="code-cmd">curl</span>
                    <span class="code-flag">-X POST</span>
                    <span class="code-url">/v1/messages</span>
                  </div>
                  <div class="code-line line-2">
                    <span class="code-comment"># Routing to upstream...</span>
                  </div>
                  <div class="code-line line-3">
                    <span class="code-success">200 OK</span>
                    <span class="code-response">{ "content": "Hello!" }</span>
                  </div>
                  <div class="code-line line-4">
                    <span class="code-prompt">$</span>
                    <span class="cursor"></span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Feature Tags -->
        <div class="mb-20 flex flex-wrap items-center justify-center gap-3 md:gap-4">
          <div
            v-for="(tag, index) in featureTags"
            :key="tag.key"
            class="feature-tag group inline-flex items-center gap-2.5 rounded-2xl border border-gray-200/50 bg-white/60 px-5 py-3 shadow-sm backdrop-blur-sm transition-all duration-300 hover:-translate-y-1 hover:shadow-lg dark:border-dark-700/30 dark:bg-dark-800/60"
            :style="{ animationDelay: `${index * 100}ms` }"
          >
            <div class="flex h-8 w-8 items-center justify-center rounded-xl transition-transform duration-300 group-hover:scale-110" :class="tag.bgClass">
              <Icon :name="tag.icon" size="sm" :class="tag.iconClass" />
            </div>
            <span class="text-sm font-medium text-gray-600 dark:text-dark-300">{{ t(tag.key) }}</span>
          </div>
        </div>

        <!-- Features Grid -->
        <div class="mb-24 grid gap-8 md:grid-cols-3">
          <div
            v-for="(feature, index) in features"
            :key="feature.titleKey"
            class="feature-card group relative overflow-hidden rounded-3xl border border-gray-200/50 bg-white/60 p-8 backdrop-blur-sm transition-all duration-500 hover:-translate-y-2 hover:shadow-2xl dark:border-dark-700/30 dark:bg-dark-800/60"
            :class="feature.hoverShadow"
            :style="{ animationDelay: `${index * 150}ms` }"
          >
            <!-- Decorative orb -->
            <div class="absolute -right-10 -top-10 h-36 w-36 rounded-full transition-all duration-700 group-hover:scale-[2] group-hover:opacity-100 opacity-50" :class="feature.orbClass"></div>
            <!-- Decorative line -->
            <div class="absolute bottom-0 left-8 right-8 h-px bg-gradient-to-r from-transparent via-current to-transparent opacity-0 transition-opacity duration-500 group-hover:opacity-20" :class="feature.lineClass"></div>

            <div
              class="relative mb-6 flex h-14 w-14 items-center justify-center rounded-2xl shadow-lg transition-all duration-500 group-hover:scale-110 group-hover:rotate-3 group-hover:shadow-xl"
              :class="feature.iconBgClass"
            >
              <component :is="feature.iconComponent" v-if="feature.iconComponent" />
              <Icon v-else :name="feature.icon" size="lg" class="text-white" />
            </div>
            <h3 class="relative mb-3 text-lg font-bold text-gray-900 dark:text-white">
              {{ t(feature.titleKey) }}
            </h3>
            <p class="relative text-sm leading-relaxed text-gray-500 dark:text-dark-400">
              {{ t(feature.descKey) }}
            </p>
          </div>
        </div>

        <!-- CTA Section -->
        <div class="cta-section relative mb-16 overflow-hidden rounded-3xl border border-primary-200/30 p-12 text-center md:p-16">
          <!-- CTA Background -->
          <div class="absolute inset-0 bg-gradient-to-br from-primary-500/[0.04] via-blue-500/[0.04] to-violet-500/[0.04] dark:from-primary-500/[0.08] dark:via-blue-500/[0.08] dark:to-violet-500/[0.08]"></div>
          <div class="absolute -left-20 -top-20 h-60 w-60 rounded-full bg-primary-400/10 blur-[80px]"></div>
          <div class="absolute -bottom-20 -right-20 h-60 w-60 rounded-full bg-violet-400/10 blur-[80px]"></div>

          <h2 class="relative mb-4 text-2xl font-bold text-gray-900 dark:text-white md:text-3xl">
            {{ t('home.cta.title') }}
          </h2>
          <p class="relative mx-auto mb-10 max-w-xl text-base leading-relaxed text-gray-500 dark:text-dark-400">
            {{ t('home.cta.description') }}
          </p>
          <router-link
            :to="isAuthenticated ? dashboardPath : '/login'"
            class="group relative inline-flex items-center gap-2.5 rounded-2xl bg-gradient-to-r from-primary-500 to-primary-600 px-10 py-4 text-base font-semibold text-white shadow-xl shadow-primary-500/20 transition-all duration-300 hover:shadow-2xl hover:shadow-primary-500/30 hover:brightness-110"
          >
            {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
            <Icon name="arrowRight" size="md" class="transition-transform duration-300 group-hover:translate-x-1" :stroke-width="2.5" />
          </router-link>
        </div>
      </div>
    </main>

    <!-- Footer -->
    <footer class="relative z-10 border-t border-gray-200/20 px-6 py-8 dark:border-dark-800/20">
      <div class="mx-auto flex max-w-6xl flex-col items-center justify-between gap-4 sm:flex-row">
        <p class="text-sm text-gray-400 dark:text-dark-500">
          &copy; {{ currentYear }} {{ siteName }}. {{ t('home.footer.allRightsReserved') }}
        </p>
        <div class="flex items-center gap-6">
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-sm text-gray-400 transition-colors duration-200 hover:text-primary-500 dark:text-dark-500 dark:hover:text-primary-400"
          >
            {{ t('home.docs') }}
          </a>
          <a
            :href="githubUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-sm text-gray-400 transition-colors duration-200 hover:text-primary-500 dark:text-dark-500 dark:hover:text-primary-400"
          >
            GitHub
          </a>
        </div>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()

const authStore = useAuthStore()
const appStore = useAppStore()

// Site settings
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '')
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || 'AI API Gateway Platform')
const docUrl = computed(() => appStore.cachedPublicSettings?.doc_url || appStore.docUrl || '')
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')

const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})

// Theme
const isDark = ref(document.documentElement.classList.contains('dark'))
const githubUrl = 'https://github.com/Wei-Shaw/sub2api'

// Auth state
const isAuthenticated = computed(() => authStore.isAuthenticated)
const isAdmin = computed(() => authStore.isAdmin)
const dashboardPath = computed(() => isAdmin.value ? '/admin/dashboard' : '/dashboard')
const userInitial = computed(() => {
  const user = authStore.user
  if (!user || !user.email) return ''
  return user.email.charAt(0).toUpperCase()
})

const currentYear = computed(() => new Date().getFullYear())

// Feature Tags data
const featureTags = [
  { key: 'home.tags.subscriptionToApi', icon: 'swap' as const, bgClass: 'bg-primary-100 dark:bg-primary-900/30', iconClass: 'text-primary-600 dark:text-primary-400' },
  { key: 'home.tags.stickySession', icon: 'shield' as const, bgClass: 'bg-blue-100 dark:bg-blue-900/30', iconClass: 'text-blue-600 dark:text-blue-400' },
  { key: 'home.tags.realtimeBilling', icon: 'chart' as const, bgClass: 'bg-violet-100 dark:bg-violet-900/30', iconClass: 'text-violet-600 dark:text-violet-400' },
]

// SVG icon components for features
const UsersIcon = () => h('svg', { class: 'h-6 w-6 text-white', fill: 'none', viewBox: '0 0 24 24', stroke: 'currentColor', 'stroke-width': '1.5' }, [
  h('path', { 'stroke-linecap': 'round', 'stroke-linejoin': 'round', d: 'M18 18.72a9.094 9.094 0 003.741-.479 3 3 0 00-4.682-2.72m.94 3.198l.001.031c0 .225-.012.447-.037.666A11.944 11.944 0 0112 21c-2.17 0-4.207-.576-5.963-1.584A6.062 6.062 0 016 18.719m12 0a5.971 5.971 0 00-.941-3.197m0 0A5.995 5.995 0 0012 12.75a5.995 5.995 0 00-5.058 2.772m0 0a3 3 0 00-4.681 2.72 8.986 8.986 0 003.74.477m.94-3.197a5.971 5.971 0 00-.94 3.197M15 6.75a3 3 0 11-6 0 3 3 0 016 0zm6 3a2.25 2.25 0 11-4.5 0 2.25 2.25 0 014.5 0zm-13.5 0a2.25 2.25 0 11-4.5 0 2.25 2.25 0 014.5 0z' })
])

const WalletIcon = () => h('svg', { class: 'h-6 w-6 text-white', fill: 'none', viewBox: '0 0 24 24', stroke: 'currentColor', 'stroke-width': '1.5' }, [
  h('path', { 'stroke-linecap': 'round', 'stroke-linejoin': 'round', d: 'M2.25 18.75a60.07 60.07 0 0115.797 2.101c.727.198 1.453-.342 1.453-1.096V18.75M3.75 4.5v.75A.75.75 0 013 6h-.75m0 0v-.375c0-.621.504-1.125 1.125-1.125H20.25M2.25 6v9m18-10.5v.75c0 .414.336.75.75.75h.75m-1.5-1.5h.375c.621 0 1.125.504 1.125 1.125v9.75c0 .621-.504 1.125-1.125 1.125h-.375m1.5-1.5H21a.75.75 0 00-.75.75v.75m0 0H3.75m0 0h-.375a1.125 1.125 0 01-1.125-1.125V15m1.5 1.5v-.75A.75.75 0 003 15h-.75M15 10.5a3 3 0 11-6 0 3 3 0 016 0zm3 0h.008v.008H18V10.5zm-12 0h.008v.008H6V10.5z' })
])

// Features data
const features = [
  {
    titleKey: 'home.features.unifiedGateway',
    descKey: 'home.features.unifiedGatewayDesc',
    icon: 'server' as const,
    iconComponent: null,
    iconBgClass: 'bg-gradient-to-br from-blue-500 to-blue-600 shadow-blue-500/25',
    hoverShadow: 'hover:shadow-blue-500/10',
    orbClass: 'bg-blue-500/5',
    lineClass: 'text-blue-500',
  },
  {
    titleKey: 'home.features.multiAccount',
    descKey: 'home.features.multiAccountDesc',
    icon: 'users' as const,
    iconComponent: UsersIcon,
    iconBgClass: 'bg-gradient-to-br from-primary-500 to-primary-600 shadow-primary-500/25',
    hoverShadow: 'hover:shadow-primary-500/10',
    orbClass: 'bg-primary-500/5',
    lineClass: 'text-primary-500',
  },
  {
    titleKey: 'home.features.balanceQuota',
    descKey: 'home.features.balanceQuotaDesc',
    icon: 'dollar' as const,
    iconComponent: WalletIcon,
    iconBgClass: 'bg-gradient-to-br from-violet-500 to-violet-600 shadow-violet-500/25',
    hoverShadow: 'hover:shadow-violet-500/10',
    orbClass: 'bg-violet-500/5',
    lineClass: 'text-violet-500',
  },
]

function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

function initTheme() {
  const savedTheme = localStorage.getItem('theme')
  if (
    savedTheme === 'dark' ||
    (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)
  ) {
    isDark.value = true
    document.documentElement.classList.add('dark')
  }
}

onMounted(() => {
  initTheme()
  authStore.checkAuth()
  if (!appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }
})
</script>

<style scoped>
/* Float animation for background blobs */
@keyframes float {
  0%, 100% { transform: translate(0, 0) scale(1); }
  33% { transform: translate(30px, -20px) scale(1.05); }
  66% { transform: translate(-20px, 15px) scale(0.95); }
}

/* Hero entrance animations */
.hero-badge {
  animation: fadeSlideUp 0.6s cubic-bezier(0.16, 1, 0.3, 1) both;
}
.hero-title {
  animation: fadeSlideUp 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.1s both;
}
.hero-subtitle {
  animation: fadeSlideUp 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.2s both;
}
.hero-cta {
  animation: fadeSlideUp 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.3s both;
}

@keyframes fadeSlideUp {
  from { opacity: 0; transform: translateY(24px); }
  to { opacity: 1; transform: translateY(0); }
}

/* Feature tag entrance */
.feature-tag {
  animation: fadeSlideUp 0.5s cubic-bezier(0.16, 1, 0.3, 1) both;
}

/* Feature card entrance */
.feature-card {
  animation: fadeSlideUp 0.6s cubic-bezier(0.16, 1, 0.3, 1) both;
}

/* Terminal Container */
.terminal-container {
  position: relative;
  display: inline-block;
  filter: drop-shadow(0 32px 64px rgba(0, 0, 0, 0.12));
  animation: fadeSlideUp 0.8s cubic-bezier(0.16, 1, 0.3, 1) 0.2s both;
}

/* Terminal Window */
.terminal-window {
  width: 440px;
  background: linear-gradient(145deg, #1e293b 0%, #0f172a 100%);
  border-radius: 16px;
  box-shadow:
    0 0 0 1px rgba(255, 255, 255, 0.06),
    inset 0 1px 0 rgba(255, 255, 255, 0.08);
  overflow: hidden;
  transform: perspective(1200px) rotateX(2deg) rotateY(-3deg);
  transition: transform 0.5s cubic-bezier(0.16, 1, 0.3, 1);
}

.terminal-window:hover {
  transform: perspective(1200px) rotateX(0deg) rotateY(0deg) translateY(-8px) scale(1.02);
}

/* Terminal Header */
.terminal-header {
  display: flex;
  align-items: center;
  padding: 14px 18px;
  background: rgba(15, 23, 42, 0.5);
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
}

.terminal-buttons {
  display: flex;
  gap: 8px;
}

.terminal-buttons span {
  width: 12px;
  height: 12px;
  border-radius: 50%;
  opacity: 0.8;
  transition: opacity 0.2s;
}

.terminal-window:hover .terminal-buttons span {
  opacity: 1;
}

.btn-close { background: #ef4444; }
.btn-minimize { background: #eab308; }
.btn-maximize { background: #22c55e; }

.terminal-title {
  flex: 1;
  text-align: center;
  font-size: 12px;
  font-family: ui-monospace, monospace;
  color: #475569;
  margin-right: 52px;
  letter-spacing: 0.05em;
}

/* Terminal Body */
.terminal-body {
  padding: 22px 26px;
  font-family: ui-monospace, 'Fira Code', 'JetBrains Mono', monospace;
  font-size: 13.5px;
  line-height: 2.2;
}

.code-line {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  opacity: 0;
  animation: line-appear 0.6s cubic-bezier(0.16, 1, 0.3, 1) forwards;
}

.line-1 { animation-delay: 0.5s; }
.line-2 { animation-delay: 1.2s; }
.line-3 { animation-delay: 2s; }
.line-4 { animation-delay: 2.8s; }

@keyframes line-appear {
  from { opacity: 0; transform: translateY(8px); }
  to { opacity: 1; transform: translateY(0); }
}

.code-prompt { color: #22c55e; font-weight: bold; }
.code-cmd { color: #38bdf8; }
.code-flag { color: #a78bfa; }
.code-url { color: #14b8a6; }
.code-comment { color: #475569; font-style: italic; }
.code-success {
  color: #22c55e;
  background: rgba(34, 197, 94, 0.1);
  padding: 2px 10px;
  border-radius: 6px;
  font-weight: 600;
}
.code-response { color: #fbbf24; }

/* Blinking Cursor */
.cursor {
  display: inline-block;
  width: 8px;
  height: 17px;
  background: #22c55e;
  border-radius: 1px;
  animation: blink 1s step-end infinite;
}

@keyframes blink {
  0%, 50% { opacity: 1; }
  51%, 100% { opacity: 0; }
}

/* CTA Section */
.cta-section {
  animation: fadeSlideUp 0.6s cubic-bezier(0.16, 1, 0.3, 1) both;
  backdrop-filter: blur(8px);
}

/* Dark mode terminal glow */
:deep(.dark) .terminal-container {
  filter: drop-shadow(0 32px 64px rgba(0, 0, 0, 0.35));
}

:deep(.dark) .terminal-window {
  box-shadow:
    0 0 0 1px rgba(20, 184, 166, 0.12),
    0 0 80px rgba(20, 184, 166, 0.06),
    inset 0 1px 0 rgba(255, 255, 255, 0.06);
}

/* Responsive terminal */
@media (max-width: 640px) {
  .terminal-window {
    width: 340px;
  }
  .terminal-body {
    font-size: 12px;
    padding: 16px 18px;
  }
}
</style>
