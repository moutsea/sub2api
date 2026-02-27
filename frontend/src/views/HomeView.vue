<template>
  <!-- Custom Home Content: Full Page Mode -->
  <div v-if="homeContent" class="min-h-screen">
    <!-- iframe mode -->
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    ></iframe>
    <!-- HTML mode - SECURITY: homeContent is admin-only setting, XSS risk is acceptable -->
    <div v-else v-html="homeContent"></div>
  </div>

  <!-- Default Home Page -->
  <div
    v-else
    class="relative flex min-h-screen flex-col overflow-hidden bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
  >
    <!-- Background Decorations -->
    <div class="pointer-events-none absolute inset-0 overflow-hidden">
      <div class="absolute -right-32 -top-32 h-[500px] w-[500px] animate-[float_20s_ease-in-out_infinite] rounded-full bg-primary-400/20 blur-[100px]"></div>
      <div class="absolute -bottom-32 -left-32 h-[450px] w-[450px] animate-[float_25s_ease-in-out_infinite_reverse] rounded-full bg-blue-400/15 blur-[100px]"></div>
      <div class="absolute left-1/2 top-1/3 h-[350px] w-[350px] animate-[float_18s_ease-in-out_infinite_2s] rounded-full bg-purple-400/10 blur-[100px]"></div>
      <div class="absolute inset-0 bg-[linear-gradient(rgba(20,184,166,0.03)_1px,transparent_1px),linear-gradient(90deg,rgba(20,184,166,0.03)_1px,transparent_1px)] bg-[size:64px_64px]"></div>
      <div class="absolute inset-0 bg-[radial-gradient(ellipse_at_top,rgba(20,184,166,0.08),transparent_50%)]"></div>
    </div>

    <!-- Announcement Bar -->
    <div class="relative z-20 border-b border-primary-200/50 bg-gradient-to-r from-primary-500/10 via-primary-400/10 to-primary-500/10 backdrop-blur-sm dark:border-primary-800/30 dark:from-primary-500/5 dark:via-primary-400/5 dark:to-primary-500/5">
      <div class="mx-auto flex max-w-6xl items-center justify-center gap-2 px-6 py-2.5">
        <span class="relative flex h-2 w-2">
          <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary-400 opacity-75"></span>
          <span class="relative inline-flex h-2 w-2 rounded-full bg-primary-500"></span>
        </span>
        <p class="text-sm font-medium text-gray-700 dark:text-dark-200">
          <span class="text-primary-600 dark:text-primary-400">内部系统</span>，需要测试联系：<span class="font-semibold text-gray-900 dark:text-white">liangtangjhz</span>
        </p>
      </div>
    </div>

    <!-- Header -->
    <header class="relative z-20 px-6 py-4">
      <nav class="mx-auto flex max-w-6xl items-center justify-between rounded-2xl border border-white/20 bg-white/60 px-5 py-3 shadow-lg shadow-gray-900/5 backdrop-blur-xl dark:border-dark-700/30 dark:bg-dark-900/60 dark:shadow-black/10">
        <!-- Logo -->
        <div class="flex items-center gap-3">
          <div class="h-9 w-9 overflow-hidden rounded-xl bg-gradient-to-br from-primary-400 to-primary-600 p-0.5 shadow-md shadow-primary-500/20">
            <img :src="siteLogo || '/logo.png'" alt="Logo" class="h-full w-full rounded-[10px] object-contain" />
          </div>
          <span class="hidden text-sm font-bold text-gray-800 dark:text-white sm:inline">{{ siteName }}</span>
        </div>

        <!-- Nav Actions -->
        <div class="flex items-center gap-1.5">
          <!-- Key Query Link -->
          <router-link
            to="/key-query"
            class="inline-flex items-center gap-1.5 rounded-xl px-3 py-2 text-sm text-gray-600 transition-all hover:bg-gray-100/80 hover:text-gray-900 dark:text-dark-400 dark:hover:bg-dark-800/80 dark:hover:text-white"
            :title="t('home.keyQuery')"
          >
            <svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z" />
            </svg>
            <span class="hidden sm:inline">{{ t('home.keyQuery') }}</span>
          </router-link>

          <!-- Language Switcher -->
          <LocaleSwitcher />

          <!-- Doc Link -->
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="rounded-xl p-2 text-gray-500 transition-all hover:bg-gray-100/80 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800/80 dark:hover:text-white"
            :title="t('home.viewDocs')"
          >
            <Icon name="book" size="md" />
          </a>

          <!-- Theme Toggle -->
          <button
            @click="toggleTheme"
            class="rounded-xl p-2 text-gray-500 transition-all hover:bg-gray-100/80 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800/80 dark:hover:text-white"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
          >
            <Icon v-if="isDark" name="sun" size="md" />
            <Icon v-else name="moon" size="md" />
          </button>

          <div class="mx-1 h-5 w-px bg-gray-200 dark:bg-dark-700"></div>

          <!-- Login / Dashboard Button -->
          <router-link
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="inline-flex items-center gap-2 rounded-xl bg-gradient-to-r from-primary-500 to-primary-600 px-4 py-2 text-sm font-medium text-white shadow-md shadow-primary-500/25 transition-all hover:from-primary-600 hover:to-primary-700 hover:shadow-lg hover:shadow-primary-500/30"
          >
            <span
              class="flex h-5 w-5 items-center justify-center rounded-full bg-white/20 text-[10px] font-bold"
            >
              {{ userInitial }}
            </span>
            {{ t('home.dashboard') }}
            <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5">
              <path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5L21 12m0 0l-7.5 7.5M21 12H3" />
            </svg>
          </router-link>
          <router-link
            v-else
            to="/login"
            class="inline-flex items-center gap-1.5 rounded-xl bg-gradient-to-r from-primary-500 to-primary-600 px-4 py-2 text-sm font-medium text-white shadow-md shadow-primary-500/25 transition-all hover:from-primary-600 hover:to-primary-700 hover:shadow-lg hover:shadow-primary-500/30"
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
    <main class="relative z-10 flex-1 px-6 py-20 lg:py-28">
      <div class="mx-auto max-w-6xl">
        <!-- Hero Section - Left/Right Layout -->
        <div class="mb-20 flex flex-col items-center justify-between gap-12 lg:flex-row lg:gap-20">
          <!-- Left: Text Content -->
          <div class="flex-1 text-center lg:text-left">
            <div class="mb-6 inline-flex items-center gap-2 rounded-full border border-primary-200/60 bg-primary-50/80 px-4 py-1.5 dark:border-primary-800/40 dark:bg-primary-900/20">
              <span class="relative flex h-2 w-2">
                <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary-400 opacity-75"></span>
                <span class="relative inline-flex h-2 w-2 rounded-full bg-primary-500"></span>
              </span>
              <span class="text-xs font-semibold text-primary-700 dark:text-primary-300">AI API Gateway</span>
            </div>
            <h1
              class="mb-6 bg-gradient-to-br from-gray-900 via-gray-800 to-gray-600 bg-clip-text text-5xl font-extrabold leading-tight text-transparent dark:from-white dark:via-gray-100 dark:to-gray-400 md:text-6xl lg:text-7xl"
            >
              {{ siteName }}
            </h1>
            <p class="mb-10 max-w-lg text-lg leading-relaxed text-gray-500 dark:text-dark-400 md:text-xl">
              {{ siteSubtitle }}
            </p>

            <!-- CTA Buttons -->
            <div class="flex flex-col items-center gap-4 sm:flex-row lg:justify-start">
              <router-link
                :to="isAuthenticated ? dashboardPath : '/login'"
                class="group inline-flex items-center gap-2 rounded-2xl bg-gradient-to-r from-primary-500 to-primary-600 px-8 py-3.5 text-base font-semibold text-white shadow-xl shadow-primary-500/25 transition-all duration-300 hover:from-primary-600 hover:to-primary-700 hover:shadow-2xl hover:shadow-primary-500/30"
              >
                {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
                <Icon name="arrowRight" size="md" class="transition-transform group-hover:translate-x-0.5" :stroke-width="2.5" />
              </router-link>
              <a
                v-if="docUrl"
                :href="docUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="inline-flex items-center gap-2 rounded-2xl border border-gray-200 bg-white/80 px-6 py-3.5 text-base font-medium text-gray-700 shadow-sm backdrop-blur-sm transition-all duration-300 hover:border-gray-300 hover:bg-white hover:shadow-md dark:border-dark-600 dark:bg-dark-800/80 dark:text-dark-200 dark:hover:border-dark-500 dark:hover:bg-dark-800"
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
                <!-- Window header -->
                <div class="terminal-header">
                  <div class="terminal-buttons">
                    <span class="btn-close"></span>
                    <span class="btn-minimize"></span>
                    <span class="btn-maximize"></span>
                  </div>
                  <span class="terminal-title">terminal</span>
                </div>
                <!-- Terminal content -->
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

        <!-- Feature Tags - Centered -->
        <div class="mb-16 flex flex-wrap items-center justify-center gap-3 md:gap-4">
          <div
            class="group inline-flex items-center gap-2.5 rounded-2xl border border-gray-200/60 bg-white/70 px-5 py-3 shadow-sm backdrop-blur-sm transition-all duration-300 hover:-translate-y-0.5 hover:shadow-md dark:border-dark-700/40 dark:bg-dark-800/70"
          >
            <div class="flex h-8 w-8 items-center justify-center rounded-lg bg-primary-100 dark:bg-primary-900/30">
              <Icon name="swap" size="sm" class="text-primary-600 dark:text-primary-400" />
            </div>
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{
              t('home.tags.subscriptionToApi')
            }}</span>
          </div>
          <div
            class="group inline-flex items-center gap-2.5 rounded-2xl border border-gray-200/60 bg-white/70 px-5 py-3 shadow-sm backdrop-blur-sm transition-all duration-300 hover:-translate-y-0.5 hover:shadow-md dark:border-dark-700/40 dark:bg-dark-800/70"
          >
            <div class="flex h-8 w-8 items-center justify-center rounded-lg bg-blue-100 dark:bg-blue-900/30">
              <Icon name="shield" size="sm" class="text-blue-600 dark:text-blue-400" />
            </div>
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{
              t('home.tags.stickySession')
            }}</span>
          </div>
          <div
            class="group inline-flex items-center gap-2.5 rounded-2xl border border-gray-200/60 bg-white/70 px-5 py-3 shadow-sm backdrop-blur-sm transition-all duration-300 hover:-translate-y-0.5 hover:shadow-md dark:border-dark-700/40 dark:bg-dark-800/70"
          >
            <div class="flex h-8 w-8 items-center justify-center rounded-lg bg-purple-100 dark:bg-purple-900/30">
              <Icon name="chart" size="sm" class="text-purple-600 dark:text-purple-400" />
            </div>
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{
              t('home.tags.realtimeBilling')
            }}</span>
          </div>
        </div>

        <!-- Features Grid -->
        <div class="mb-20 grid gap-6 md:grid-cols-3">
          <!-- Feature 1: Unified Gateway -->
          <div
            class="group relative overflow-hidden rounded-3xl border border-gray-200/60 bg-white/70 p-8 backdrop-blur-sm transition-all duration-500 hover:-translate-y-1 hover:shadow-2xl hover:shadow-blue-500/10 dark:border-dark-700/40 dark:bg-dark-800/70"
          >
            <div class="absolute -right-8 -top-8 h-32 w-32 rounded-full bg-blue-500/5 transition-transform duration-500 group-hover:scale-150"></div>
            <div
              class="relative mb-5 flex h-14 w-14 items-center justify-center rounded-2xl bg-gradient-to-br from-blue-500 to-blue-600 shadow-lg shadow-blue-500/25 transition-transform duration-300 group-hover:scale-110 group-hover:rotate-3"
            >
              <Icon name="server" size="lg" class="text-white" />
            </div>
            <h3 class="relative mb-3 text-lg font-bold text-gray-900 dark:text-white">
              {{ t('home.features.unifiedGateway') }}
            </h3>
            <p class="relative text-sm leading-relaxed text-gray-500 dark:text-dark-400">
              {{ t('home.features.unifiedGatewayDesc') }}
            </p>
          </div>

          <!-- Feature 2: Account Pool -->
          <div
            class="group relative overflow-hidden rounded-3xl border border-gray-200/60 bg-white/70 p-8 backdrop-blur-sm transition-all duration-500 hover:-translate-y-1 hover:shadow-2xl hover:shadow-primary-500/10 dark:border-dark-700/40 dark:bg-dark-800/70"
          >
            <div class="absolute -right-8 -top-8 h-32 w-32 rounded-full bg-primary-500/5 transition-transform duration-500 group-hover:scale-150"></div>
            <div
              class="relative mb-5 flex h-14 w-14 items-center justify-center rounded-2xl bg-gradient-to-br from-primary-500 to-primary-600 shadow-lg shadow-primary-500/25 transition-transform duration-300 group-hover:scale-110 group-hover:rotate-3"
            >
              <svg class="h-6 w-6 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M18 18.72a9.094 9.094 0 003.741-.479 3 3 0 00-4.682-2.72m.94 3.198l.001.031c0 .225-.012.447-.037.666A11.944 11.944 0 0112 21c-2.17 0-4.207-.576-5.963-1.584A6.062 6.062 0 016 18.719m12 0a5.971 5.971 0 00-.941-3.197m0 0A5.995 5.995 0 0012 12.75a5.995 5.995 0 00-5.058 2.772m0 0a3 3 0 00-4.681 2.72 8.986 8.986 0 003.74.477m.94-3.197a5.971 5.971 0 00-.94 3.197M15 6.75a3 3 0 11-6 0 3 3 0 016 0zm6 3a2.25 2.25 0 11-4.5 0 2.25 2.25 0 014.5 0zm-13.5 0a2.25 2.25 0 11-4.5 0 2.25 2.25 0 014.5 0z" />
              </svg>
            </div>
            <h3 class="relative mb-3 text-lg font-bold text-gray-900 dark:text-white">
              {{ t('home.features.multiAccount') }}
            </h3>
            <p class="relative text-sm leading-relaxed text-gray-500 dark:text-dark-400">
              {{ t('home.features.multiAccountDesc') }}
            </p>
          </div>

          <!-- Feature 3: Billing & Quota -->
          <div
            class="group relative overflow-hidden rounded-3xl border border-gray-200/60 bg-white/70 p-8 backdrop-blur-sm transition-all duration-500 hover:-translate-y-1 hover:shadow-2xl hover:shadow-purple-500/10 dark:border-dark-700/40 dark:bg-dark-800/70"
          >
            <div class="absolute -right-8 -top-8 h-32 w-32 rounded-full bg-purple-500/5 transition-transform duration-500 group-hover:scale-150"></div>
            <div
              class="relative mb-5 flex h-14 w-14 items-center justify-center rounded-2xl bg-gradient-to-br from-purple-500 to-purple-600 shadow-lg shadow-purple-500/25 transition-transform duration-300 group-hover:scale-110 group-hover:rotate-3"
            >
              <svg class="h-6 w-6 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M2.25 18.75a60.07 60.07 0 0115.797 2.101c.727.198 1.453-.342 1.453-1.096V18.75M3.75 4.5v.75A.75.75 0 013 6h-.75m0 0v-.375c0-.621.504-1.125 1.125-1.125H20.25M2.25 6v9m18-10.5v.75c0 .414.336.75.75.75h.75m-1.5-1.5h.375c.621 0 1.125.504 1.125 1.125v9.75c0 .621-.504 1.125-1.125 1.125h-.375m1.5-1.5H21a.75.75 0 00-.75.75v.75m0 0H3.75m0 0h-.375a1.125 1.125 0 01-1.125-1.125V15m1.5 1.5v-.75A.75.75 0 003 15h-.75M15 10.5a3 3 0 11-6 0 3 3 0 016 0zm3 0h.008v.008H18V10.5zm-12 0h.008v.008H6V10.5z" />
              </svg>
            </div>
            <h3 class="relative mb-3 text-lg font-bold text-gray-900 dark:text-white">
              {{ t('home.features.balanceQuota') }}
            </h3>
            <p class="relative text-sm leading-relaxed text-gray-500 dark:text-dark-400">
              {{ t('home.features.balanceQuotaDesc') }}
            </p>
          </div>
        </div>

        <!-- Supported Providers -->
        <div class="mb-10 text-center">
          <h2 class="mb-2 text-sm font-semibold uppercase tracking-widest text-primary-600 dark:text-primary-400">
            {{ t('home.providers.title') }}
          </h2>
          <p class="text-sm text-gray-500 dark:text-dark-400">
            {{ t('home.providers.description') }}
          </p>
        </div>

        <div class="mb-16 flex flex-wrap items-center justify-center gap-3">
          <!-- Claude -->
          <div class="group flex items-center gap-2.5 rounded-2xl border border-gray-200/60 bg-white/70 px-5 py-3 backdrop-blur-sm transition-all duration-300 hover:-translate-y-0.5 hover:border-orange-300 hover:shadow-lg hover:shadow-orange-500/10 dark:border-dark-700/40 dark:bg-dark-800/70 dark:hover:border-orange-700">
            <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-gradient-to-br from-orange-400 to-orange-500 shadow-sm shadow-orange-500/20">
              <span class="text-sm font-bold text-white">C</span>
            </div>
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('home.providers.claude') }}</span>
            <span class="rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-400">{{ t('home.providers.supported') }}</span>
          </div>
          <!-- GPT -->
          <div class="group flex items-center gap-2.5 rounded-2xl border border-gray-200/60 bg-white/70 px-5 py-3 backdrop-blur-sm transition-all duration-300 hover:-translate-y-0.5 hover:border-green-300 hover:shadow-lg hover:shadow-green-500/10 dark:border-dark-700/40 dark:bg-dark-800/70 dark:hover:border-green-700">
            <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-gradient-to-br from-green-500 to-green-600 shadow-sm shadow-green-500/20">
              <span class="text-sm font-bold text-white">G</span>
            </div>
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">GPT</span>
            <span class="rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-400">{{ t('home.providers.supported') }}</span>
          </div>
          <!-- Gemini -->
          <div class="group flex items-center gap-2.5 rounded-2xl border border-gray-200/60 bg-white/70 px-5 py-3 backdrop-blur-sm transition-all duration-300 hover:-translate-y-0.5 hover:border-blue-300 hover:shadow-lg hover:shadow-blue-500/10 dark:border-dark-700/40 dark:bg-dark-800/70 dark:hover:border-blue-700">
            <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-gradient-to-br from-blue-500 to-blue-600 shadow-sm shadow-blue-500/20">
              <span class="text-sm font-bold text-white">G</span>
            </div>
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('home.providers.gemini') }}</span>
            <span class="rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-400">{{ t('home.providers.supported') }}</span>
          </div>
          <!-- Antigravity -->
          <div class="group flex items-center gap-2.5 rounded-2xl border border-gray-200/60 bg-white/70 px-5 py-3 backdrop-blur-sm transition-all duration-300 hover:-translate-y-0.5 hover:border-rose-300 hover:shadow-lg hover:shadow-rose-500/10 dark:border-dark-700/40 dark:bg-dark-800/70 dark:hover:border-rose-700">
            <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-gradient-to-br from-rose-500 to-pink-600 shadow-sm shadow-rose-500/20">
              <span class="text-sm font-bold text-white">A</span>
            </div>
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('home.providers.antigravity') }}</span>
            <span class="rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-400">{{ t('home.providers.supported') }}</span>
          </div>
          <!-- More -->
          <div class="flex items-center gap-2.5 rounded-2xl border border-dashed border-gray-300/60 bg-white/40 px-5 py-3 backdrop-blur-sm dark:border-dark-600/40 dark:bg-dark-800/40">
            <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-gray-200 dark:bg-dark-700">
              <span class="text-sm font-bold text-gray-500 dark:text-dark-400">+</span>
            </div>
            <span class="text-sm font-medium text-gray-400 dark:text-dark-500">{{ t('home.providers.more') }}</span>
            <span class="rounded-full bg-gray-100 px-2 py-0.5 text-[10px] font-semibold text-gray-400 dark:bg-dark-700 dark:text-dark-500">{{ t('home.providers.soon') }}</span>
          </div>
        </div>
      </div>
    </main>

    <!-- Footer -->
    <footer class="relative z-10 border-t border-gray-200/30 px-6 py-8 dark:border-dark-800/30">
      <div
        class="mx-auto flex max-w-6xl flex-col items-center justify-between gap-4 sm:flex-row"
      >
        <p class="text-sm text-gray-400 dark:text-dark-500">
          &copy; {{ currentYear }} {{ siteName }}. {{ t('home.footer.allRightsReserved') }}
        </p>
        <div class="flex items-center gap-6">
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-sm text-gray-400 transition-colors hover:text-primary-500 dark:text-dark-500 dark:hover:text-primary-400"
          >
            {{ t('home.docs') }}
          </a>
          <a
            :href="githubUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-sm text-gray-400 transition-colors hover:text-primary-500 dark:text-dark-500 dark:hover:text-primary-400"
          >
            GitHub
          </a>
        </div>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()

const authStore = useAuthStore()
const appStore = useAppStore()

// Site settings - directly from appStore (already initialized from injected config)
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '')
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || 'AI API Gateway Platform')
const docUrl = computed(() => appStore.cachedPublicSettings?.doc_url || appStore.docUrl || '')
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')

// Check if homeContent is a URL (for iframe display)
const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})

// Theme
const isDark = ref(document.documentElement.classList.contains('dark'))

// GitHub URL
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

// Current year for footer
const currentYear = computed(() => new Date().getFullYear())

// Toggle theme
function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

// Initialize theme
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

  // Check auth state
  authStore.checkAuth()

  // Ensure public settings are loaded (will use cache if already loaded from injected config)
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

/* Terminal Container */
.terminal-container {
  position: relative;
  display: inline-block;
  filter: drop-shadow(0 32px 64px rgba(0, 0, 0, 0.15));
}

/* Terminal Window */
.terminal-window {
  width: 440px;
  background: linear-gradient(145deg, #1e293b 0%, #0f172a 100%);
  border-radius: 16px;
  box-shadow:
    0 0 0 1px rgba(255, 255, 255, 0.08),
    inset 0 1px 0 rgba(255, 255, 255, 0.1);
  overflow: hidden;
  transform: perspective(1200px) rotateX(2deg) rotateY(-3deg);
  transition: transform 0.4s cubic-bezier(0.4, 0, 0.2, 1);
}

.terminal-window:hover {
  transform: perspective(1200px) rotateX(0deg) rotateY(0deg) translateY(-6px) scale(1.02);
}

/* Terminal Header */
.terminal-header {
  display: flex;
  align-items: center;
  padding: 14px 18px;
  background: rgba(15, 23, 42, 0.6);
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}

.terminal-buttons {
  display: flex;
  gap: 8px;
}

.terminal-buttons span {
  width: 12px;
  height: 12px;
  border-radius: 50%;
  transition: opacity 0.2s;
}

.terminal-window:hover .terminal-buttons span {
  opacity: 0.9;
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
  animation: line-appear 0.6s cubic-bezier(0.4, 0, 0.2, 1) forwards;
}

.line-1 { animation-delay: 0.3s; }
.line-2 { animation-delay: 1s; }
.line-3 { animation-delay: 1.8s; }
.line-4 { animation-delay: 2.5s; }

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
  background: rgba(34, 197, 94, 0.12);
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

/* Dark mode terminal glow */
:deep(.dark) .terminal-container {
  filter: drop-shadow(0 32px 64px rgba(0, 0, 0, 0.4));
}

:deep(.dark) .terminal-window {
  box-shadow:
    0 0 0 1px rgba(20, 184, 166, 0.15),
    0 0 60px rgba(20, 184, 166, 0.08),
    inset 0 1px 0 rgba(255, 255, 255, 0.08);
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
