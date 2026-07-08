<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.createAccount')"
    width="normal"
    @close="handleClose"
  >
    <!-- Step Indicator for OAuth accounts -->
    <div v-if="isOAuthFlow" class="mb-6 flex items-center justify-center">
      <div class="flex items-center space-x-4">
        <div class="flex items-center">
          <div
            :class="[
              'flex h-8 w-8 items-center justify-center rounded-full text-sm font-semibold',
              step >= 1 ? 'bg-primary-500 text-white' : 'bg-gray-200 text-gray-500 dark:bg-dark-600'
            ]"
          >
            1
          </div>
          <span class="ml-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{
            t('admin.accounts.oauth.authMethod')
          }}</span>
        </div>
        <div class="h-0.5 w-8 bg-gray-300 dark:bg-dark-600" />
        <div class="flex items-center">
          <div
            :class="[
              'flex h-8 w-8 items-center justify-center rounded-full text-sm font-semibold',
              step >= 2 ? 'bg-primary-500 text-white' : 'bg-gray-200 text-gray-500 dark:bg-dark-600'
            ]"
          >
            2
          </div>
          <span class="ml-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{
            oauthStepTitle
          }}</span>
        </div>
      </div>
    </div>

    <!-- Step 1: Basic Info -->
    <form
      v-if="step === 1"
      id="create-account-form"
      @submit.prevent="handleSubmit"
      class="space-y-5"
    >
      <div>
        <label class="input-label">{{
          form.platform === 'kiro' && kiroInputMode === 'batch'
            ? t('admin.accounts.kiro.batchNamePrefix')
            : form.platform === 'openai' && openaiInputMode === 'batch'
              ? t('admin.accounts.openaiImport.batchNamePrefix')
              : t('admin.accounts.accountName')
        }}</label>
        <input
          v-model="form.name"
          type="text"
          :required="!(form.platform === 'kiro' && kiroInputMode === 'batch') && !(form.platform === 'openai' && openaiInputMode === 'batch')"
          class="input"
          :placeholder="
            form.platform === 'kiro' && kiroInputMode === 'batch'
              ? t('admin.accounts.kiro.batchNamePrefixPlaceholder')
              : form.platform === 'openai' && openaiInputMode === 'batch'
                ? t('admin.accounts.openaiImport.batchNamePrefixPlaceholder')
                : t('admin.accounts.enterAccountName')
          "
          data-tour="account-form-name"
        />
        <p v-if="form.platform === 'kiro' && kiroInputMode === 'batch'" class="input-hint">
          {{ t('admin.accounts.kiro.batchNamePrefixHint') }}
        </p>
        <p v-if="form.platform === 'openai' && openaiInputMode === 'batch'" class="input-hint">
          {{ t('admin.accounts.openaiImport.batchNamePrefixHint') }}
        </p>
      </div>
      <div>
        <label class="input-label">{{ t('admin.accounts.notes') }}</label>
        <textarea
          v-model="form.notes"
          rows="3"
          class="input"
          :placeholder="t('admin.accounts.notesPlaceholder')"
        ></textarea>
        <p class="input-hint">{{ t('admin.accounts.notesHint') }}</p>
      </div>

      <!-- Platform Selection - Dropdown -->
      <div>
        <label class="input-label">{{ t('admin.accounts.platform') }}</label>
        <Select
          :model-value="form.platform"
          @update:model-value="form.platform = $event as AccountPlatform"
          :options="platformOptions"
          data-tour="account-form-platform"
        />
      </div>

      <!-- Account Type Selection (Anthropic) -->
      <div v-if="form.platform === 'anthropic'">
        <label class="input-label">{{ t('admin.accounts.accountType') }}</label>
        <div class="mt-2 grid grid-cols-2 gap-3" data-tour="account-form-type">
          <button
            type="button"
            @click="accountCategory = 'oauth-based'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              accountCategory === 'oauth-based'
                ? 'border-orange-500 bg-orange-50 dark:bg-orange-900/20'
                : 'border-gray-200 hover:border-orange-300 dark:border-dark-600 dark:hover:border-orange-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                accountCategory === 'oauth-based'
                  ? 'bg-orange-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <Icon name="sparkles" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">{{
                t('admin.accounts.claudeCode')
              }}</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{
                t('admin.accounts.oauthSetupToken')
              }}</span>
            </div>
          </button>

          <button
            type="button"
            @click="accountCategory = 'apikey'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              accountCategory === 'apikey'
                ? 'border-purple-500 bg-purple-50 dark:bg-purple-900/20'
                : 'border-gray-200 hover:border-purple-300 dark:border-dark-600 dark:hover:border-purple-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                accountCategory === 'apikey'
                  ? 'bg-purple-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <Icon name="key" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">{{
                t('admin.accounts.claudeConsole')
              }}</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{
                t('admin.accounts.apiKey')
              }}</span>
            </div>
          </button>
        </div>
      </div>

      <!-- Account Type Selection (OpenAI) -->
      <div v-if="form.platform === 'openai'">
        <label class="input-label">{{ t('admin.accounts.accountType') }}</label>
        <div class="mt-2 grid grid-cols-2 gap-3" data-tour="account-form-type">
          <button
            type="button"
            @click="accountCategory = 'oauth-based'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              accountCategory === 'oauth-based'
                ? 'border-green-500 bg-green-50 dark:bg-green-900/20'
                : 'border-gray-200 hover:border-green-300 dark:border-dark-600 dark:hover:border-green-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                accountCategory === 'oauth-based'
                  ? 'bg-green-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <Icon name="key" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">OAuth</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.types.chatgptOauth') }}</span>
            </div>
          </button>

          <button
            type="button"
            @click="accountCategory = 'apikey'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              accountCategory === 'apikey'
                ? 'border-purple-500 bg-purple-50 dark:bg-purple-900/20'
                : 'border-gray-200 hover:border-purple-300 dark:border-dark-600 dark:hover:border-purple-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                accountCategory === 'apikey'
                  ? 'bg-purple-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <Icon name="key" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">API Key</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.types.responsesApi') }}</span>
            </div>
          </button>
        </div>
      </div>

      <!-- OpenAI OAuth: Single/Batch Mode Selection -->
      <div v-if="form.platform === 'openai' && accountCategory === 'oauth-based'">
        <div class="mt-4">
          <div class="flex rounded-lg bg-gray-100 p-1 dark:bg-dark-700">
            <button
              type="button"
              @click.stop="openaiInputMode = 'single'"
              :class="[
                'flex-1 rounded-md px-4 py-2 text-sm font-medium transition-all',
                openaiInputMode === 'single'
                  ? 'bg-white text-green-600 shadow-sm dark:bg-dark-600 dark:text-green-400'
                  : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'
              ]"
            >
              {{ t('admin.accounts.openaiImport.singleAdd') }}
            </button>
            <button
              type="button"
              @click.stop="openaiInputMode = 'batch'"
              :class="[
                'flex-1 rounded-md px-4 py-2 text-sm font-medium transition-all',
                openaiInputMode === 'batch'
                  ? 'bg-white text-green-600 shadow-sm dark:bg-dark-600 dark:text-green-400'
                  : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'
              ]"
            >
              {{ t('admin.accounts.openaiImport.batchImport') }}
            </button>
          </div>
        </div>

        <!-- Batch Import UI -->
        <div v-if="openaiInputMode === 'batch'" class="mt-4 space-y-4">
          <!-- File Upload Area -->
          <div
            class="relative rounded-lg border-2 border-dashed p-6 text-center transition-colors"
            :class="[
              openaiIsDragging
                ? 'border-green-500 bg-green-50 dark:bg-green-900/20'
                : 'border-gray-300 hover:border-green-400 dark:border-dark-600'
            ]"
            @dragover.prevent="openaiIsDragging = true"
            @dragleave.prevent="openaiIsDragging = false"
            @drop.prevent="handleOpenaiFileDrop"
          >
            <Icon name="upload" size="lg" class="mx-auto mb-2 text-gray-400" />
            <p class="text-sm text-gray-600 dark:text-gray-400">
              {{ t('admin.accounts.openaiImport.dragDropJson') }}
            </p>
            <p class="text-xs text-gray-500 dark:text-gray-500">
              {{ t('admin.accounts.openaiImport.multipleFilesSupported') }}
            </p>
            <label class="mt-2 inline-flex cursor-pointer items-center gap-1.5 rounded-lg bg-green-500/10 px-3 py-1.5 text-xs font-medium text-green-600 transition-colors hover:bg-green-500/20 dark:text-green-400">
              <Icon name="document" size="xs" />
              {{ t('admin.accounts.openaiImport.selectFiles') }}
              <input type="file" accept=".json,.jsonl" multiple class="hidden" @change="handleOpenaiFileSelect" />
            </label>
          </div>

          <!-- Email filter status -->
          <div v-if="openaiEmailFilterLoading" class="rounded-lg bg-blue-50 p-3 dark:bg-blue-900/20" role="status">
            <div class="flex items-center gap-2 text-sm text-blue-700 dark:text-blue-400">
              <Icon name="refresh" size="sm" class="animate-spin" />
              <span>{{ t('admin.accounts.openaiImport.loadingExistingEmails') }}</span>
            </div>
          </div>
          <div v-else-if="existingOpenaiEmails.size > 0" class="rounded-lg bg-blue-50 p-3 dark:bg-blue-900/20">
            <div class="flex items-center gap-2 text-sm text-blue-700 dark:text-blue-400">
              <Icon name="infoCircle" size="sm" />
              <span>{{ t('admin.accounts.openaiImport.emailFilterEnabled', { count: existingOpenaiEmails.size }) }}</span>
            </div>
          </div>

          <!-- Uploaded Files List -->
          <div v-if="openaiUploadedFiles.length > 0" class="space-y-2">
            <label class="input-label">{{ t('admin.accounts.openaiImport.uploadedFiles') }}</label>
            <div class="max-h-32 overflow-y-auto rounded-lg border border-gray-200 dark:border-dark-600">
              <div
                v-for="(file, index) in openaiUploadedFiles"
                :key="index"
                class="flex items-center justify-between border-b border-gray-100 px-3 py-2 last:border-b-0 dark:border-dark-700"
              >
                <div class="flex items-center gap-2 min-w-0">
                  <Icon name="document" size="sm" class="flex-shrink-0 text-green-500" />
                  <span class="truncate text-sm text-gray-700 dark:text-gray-300">{{ file.name }}</span>
                  <span class="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400">({{ file.accountCount }} accounts)</span>
                </div>
                <button
                  type="button"
                  @click="removeOpenaiUploadedFile(index)"
                  class="flex-shrink-0 rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-red-500 dark:hover:bg-dark-600"
                >
                  <Icon name="x" size="sm" />
                </button>
              </div>
            </div>
          </div>

          <!-- Or paste JSON -->
          <div>
            <label class="input-label">{{ t('admin.accounts.openaiImport.orPasteJson') }}</label>
            <textarea
              v-model="openaiBatchJson"
              rows="4"
              class="input font-mono text-xs"
              :placeholder="t('admin.accounts.openaiImport.batchJsonPlaceholder')"
            ></textarea>
          </div>

          <!-- Parsed accounts count -->
          <div v-if="openaiParsedAccounts.length > 0" class="rounded-lg bg-green-50 p-3 dark:bg-green-900/20">
            <div class="flex items-center gap-2 text-sm text-green-700 dark:text-green-400">
              <Icon name="check" size="sm" />
              <span>{{ t('admin.accounts.openaiImport.parsedAccounts', { count: openaiParsedAccounts.length }) }}</span>
            </div>
          </div>

          <!-- Parse error -->
          <div v-if="openaiParseError" class="rounded-lg bg-red-50 p-3 dark:bg-red-900/20">
            <div class="flex items-center gap-2 text-sm text-red-700 dark:text-red-400">
              <Icon name="exclamationTriangle" size="sm" />
              <span>{{ openaiParseError }}</span>
            </div>
          </div>

          <!-- Parse button -->
          <button
            type="button"
            class="btn btn-secondary w-full"
            :disabled="openaiEmailFilterLoading || (!openaiBatchJson.trim() && openaiUploadedFiles.length === 0)"
            @click="parseOpenaiBatchJson"
          >
            {{ t('admin.accounts.openaiImport.parseJson') }}
          </button>
        </div>
      </div>

      <!-- Account Type Selection (Gemini) -->
      <div v-if="form.platform === 'gemini'">
        <div class="flex items-center justify-between">
          <label class="input-label">{{ t('admin.accounts.accountType') }}</label>
          <button
            type="button"
            @click="showGeminiHelpDialog = true"
            class="flex items-center gap-1 rounded px-2 py-1 text-xs text-blue-600 hover:bg-blue-50 dark:text-blue-400 dark:hover:bg-blue-900/20"
          >
            <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M9.879 7.519c1.171-1.025 3.071-1.025 4.242 0 1.172 1.025 1.172 2.687 0 3.712-.203.179-.43.326-.67.442-.745.361-1.45.999-1.45 1.827v.75M21 12a9 9 0 11-18 0 9 9 0 0118 0zm-9 5.25h.008v.008H12v-.008z" />
            </svg>
            {{ t('admin.accounts.gemini.helpButton') }}
          </button>
        </div>
        <div class="mt-2 grid grid-cols-2 gap-3" data-tour="account-form-type">
          <button
            type="button"
            @click="accountCategory = 'oauth-based'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              accountCategory === 'oauth-based'
                ? 'border-blue-500 bg-blue-50 dark:bg-blue-900/20'
                : 'border-gray-200 hover:border-blue-300 dark:border-dark-600 dark:hover:border-blue-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                accountCategory === 'oauth-based'
                  ? 'bg-blue-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <Icon name="key" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">
                {{ t('admin.accounts.gemini.accountType.oauthTitle') }}
              </span>
              <span class="text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.accounts.gemini.accountType.oauthDesc') }}
              </span>
            </div>
          </button>

          <button
            type="button"
            @click="accountCategory = 'apikey'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              accountCategory === 'apikey'
                ? 'border-purple-500 bg-purple-50 dark:bg-purple-900/20'
                : 'border-gray-200 hover:border-purple-300 dark:border-dark-600 dark:hover:border-purple-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                accountCategory === 'apikey'
                  ? 'bg-purple-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <svg
                class="h-4 w-4"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                stroke-width="1.5"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  d="M15.75 5.25a3 3 0 013 3m3 0a6 6 0 01-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1721.75 8.25z"
                />
              </svg>
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">
                {{ t('admin.accounts.gemini.accountType.apiKeyTitle') }}
              </span>
              <span class="text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.accounts.gemini.accountType.apiKeyDesc') }}
              </span>
            </div>
          </button>
        </div>

        <div
          v-if="accountCategory === 'apikey'"
          class="mt-3 rounded-lg border border-purple-200 bg-purple-50 px-3 py-2 text-xs text-purple-800 dark:border-purple-800/40 dark:bg-purple-900/20 dark:text-purple-200"
        >
          <p>{{ t('admin.accounts.gemini.accountType.apiKeyNote') }}</p>
          <div class="mt-2 flex flex-wrap gap-2">
            <a
              :href="geminiHelpLinks.apiKey"
              class="font-medium text-blue-600 hover:underline dark:text-blue-400"
              target="_blank"
              rel="noreferrer"
            >
              {{ t('admin.accounts.gemini.accountType.apiKeyLink') }}
            </a>
          </div>
        </div>

        <!-- OAuth Type Selection (only show when oauth-based is selected) -->
        <div v-if="accountCategory === 'oauth-based'" class="mt-4">
          <label class="input-label">{{ t('admin.accounts.oauth.gemini.oauthTypeLabel') }}</label>
          <div class="mt-2 grid grid-cols-2 gap-3">
            <!-- Google One OAuth -->
            <button
              type="button"
              @click="handleSelectGeminiOAuthType('google_one')"
              :class="[
                'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
                geminiOAuthType === 'google_one'
                  ? 'border-purple-500 bg-purple-50 dark:bg-purple-900/20'
                  : 'border-gray-200 hover:border-purple-300 dark:border-dark-600 dark:hover:border-purple-700'
              ]"
            >
              <div
                :class="[
                  'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                  geminiOAuthType === 'google_one'
                    ? 'bg-purple-500 text-white'
                    : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
                ]"
              >
                <Icon name="user" size="sm" />
              </div>
              <div class="min-w-0">
                <span class="block text-sm font-medium text-gray-900 dark:text-white">
                  Google One
                </span>
                <span class="text-xs text-gray-500 dark:text-gray-400">
                  个人账号，享受 Google One 订阅配额
                </span>
                <div class="mt-2 flex flex-wrap gap-1">
                  <span
                    class="rounded bg-purple-100 px-2 py-0.5 text-[10px] font-semibold text-purple-700 dark:bg-purple-900/40 dark:text-purple-300"
                  >
                    推荐个人用户
                  </span>
                  <span
                    class="rounded bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300"
                  >
                    无需 GCP
                  </span>
                </div>
              </div>
            </button>

            <!-- GCP Code Assist OAuth -->
            <button
              type="button"
              @click="handleSelectGeminiOAuthType('code_assist')"
              :class="[
                'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
                geminiOAuthType === 'code_assist'
                  ? 'border-blue-500 bg-blue-50 dark:bg-blue-900/20'
                  : 'border-gray-200 hover:border-blue-300 dark:border-dark-600 dark:hover:border-blue-700'
              ]"
            >
              <div
                :class="[
                  'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                  geminiOAuthType === 'code_assist'
                    ? 'bg-blue-500 text-white'
                    : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
                ]"
              >
                <Icon name="cloud" size="sm" />
              </div>
              <div class="min-w-0">
                <span class="block text-sm font-medium text-gray-900 dark:text-white">
                  GCP Code Assist
                </span>
                <span class="text-xs text-gray-500 dark:text-gray-400">
                  企业级，需要 GCP 项目
                </span>
                <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  需要激活 GCP 项目并绑定信用卡
                  <a
                    :href="geminiHelpLinks.gcpProject"
                    class="ml-1 text-blue-600 hover:underline dark:text-blue-400"
                    target="_blank"
                    rel="noreferrer"
                  >
                    {{ t('admin.accounts.gemini.oauthType.gcpProjectLink') }}
                  </a>
                </div>
                <div class="mt-2 flex flex-wrap gap-1">
                  <span
                    class="rounded bg-blue-100 px-2 py-0.5 text-[10px] font-semibold text-blue-700 dark:bg-blue-900/40 dark:text-blue-300"
                  >
                    企业用户
                  </span>
                  <span
                    class="rounded bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300"
                  >
                    高并发
                  </span>
                </div>
              </div>
            </button>
          </div>

          <!-- Advanced Options Toggle -->
          <div class="mt-3">
            <button
              type="button"
              @click="showAdvancedOAuth = !showAdvancedOAuth"
              class="flex items-center gap-2 text-sm text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200"
            >
              <svg
                :class="['h-4 w-4 transition-transform', showAdvancedOAuth ? 'rotate-90' : '']"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                stroke-width="2"
              >
                <path stroke-linecap="round" stroke-linejoin="round" d="M9 5l7 7-7 7" />
              </svg>
              <span>{{ showAdvancedOAuth ? '隐藏' : '显示' }}高级选项（自建 OAuth Client）</span>
            </button>
          </div>

          <!-- Custom OAuth Client (Advanced) -->
          <div v-if="showAdvancedOAuth" class="mt-3 group relative">
            <button
              type="button"
              :disabled="!geminiAIStudioOAuthEnabled"
              @click="handleSelectGeminiOAuthType('ai_studio')"
              :class="[
                'flex w-full items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
                !geminiAIStudioOAuthEnabled ? 'cursor-not-allowed opacity-60' : '',
                geminiOAuthType === 'ai_studio'
                  ? 'border-amber-500 bg-amber-50 dark:bg-amber-900/20'
                  : 'border-gray-200 hover:border-amber-300 dark:border-dark-600 dark:hover:border-amber-700'
              ]"
            >
              <div
                :class="[
                  'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                  geminiOAuthType === 'ai_studio'
                    ? 'bg-amber-500 text-white'
                    : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
                ]"
              >
                <svg
                  class="h-4 w-4"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  stroke-width="1.5"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    d="M9.813 15.904L9 18.75l-.813-2.846a4.5 4.5 0 00-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 003.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 003.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 00-3.09 3.09z"
                  />
                </svg>
              </div>
              <div class="min-w-0">
                <span class="block text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('admin.accounts.gemini.oauthType.customTitle') }}
                </span>
                <span class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.oauthType.customDesc') }}
                </span>
                <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.oauthType.customRequirement') }}
                </div>
                <div class="mt-2 flex flex-wrap gap-1">
                  <span
                    class="rounded bg-amber-100 px-2 py-0.5 text-[10px] font-semibold text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
                  >
                    {{ t('admin.accounts.gemini.oauthType.badges.orgManaged') }}
                  </span>
                  <span
                    class="rounded bg-amber-100 px-2 py-0.5 text-[10px] font-semibold text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
                  >
                    {{ t('admin.accounts.gemini.oauthType.badges.adminRequired') }}
                  </span>
                </div>
              </div>
              <span
                v-if="!geminiAIStudioOAuthEnabled"
                class="ml-auto shrink-0 rounded bg-amber-100 px-2 py-0.5 text-xs text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
              >
                {{ t('admin.accounts.oauth.gemini.aiStudioNotConfiguredShort') }}
              </span>
            </button>

            <div
              v-if="!geminiAIStudioOAuthEnabled"
              class="pointer-events-none absolute right-0 top-full z-50 mt-2 w-80 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 opacity-0 shadow-lg transition-opacity group-hover:opacity-100 dark:border-amber-700 dark:bg-amber-900/40 dark:text-amber-200"
            >
              {{ t('admin.accounts.oauth.gemini.aiStudioNotConfiguredTip') }}
            </div>
          </div>
        </div>

        <!-- Tier selection (used as fallback when auto-detection is unavailable/fails) -->
        <div class="mt-4">
          <label class="input-label">{{ t('admin.accounts.gemini.tier.label') }}</label>
          <div class="mt-2">
            <select
              v-if="geminiOAuthType === 'google_one'"
              v-model="geminiTierGoogleOne"
              class="input"
            >
              <option value="google_one_free">{{ t('admin.accounts.gemini.tier.googleOne.free') }}</option>
              <option value="google_ai_pro">{{ t('admin.accounts.gemini.tier.googleOne.pro') }}</option>
              <option value="google_ai_ultra">{{ t('admin.accounts.gemini.tier.googleOne.ultra') }}</option>
            </select>

            <select
              v-else-if="geminiOAuthType === 'code_assist'"
              v-model="geminiTierGcp"
              class="input"
            >
              <option value="gcp_standard">{{ t('admin.accounts.gemini.tier.gcp.standard') }}</option>
              <option value="gcp_enterprise">{{ t('admin.accounts.gemini.tier.gcp.enterprise') }}</option>
            </select>

            <select
              v-else
              v-model="geminiTierAIStudio"
              class="input"
            >
              <option value="aistudio_free">{{ t('admin.accounts.gemini.tier.aiStudio.free') }}</option>
              <option value="aistudio_paid">{{ t('admin.accounts.gemini.tier.aiStudio.paid') }}</option>
            </select>
          </div>
          <p class="input-hint">{{ t('admin.accounts.gemini.tier.hint') }}</p>
        </div>
      </div>

      <!-- Account Type Selection (Antigravity - OAuth only) -->
      <div v-if="form.platform === 'antigravity'">
        <label class="input-label">{{ t('admin.accounts.accountType') }}</label>
        <div class="mt-2">
          <div
            class="flex items-center gap-3 rounded-lg border-2 border-purple-500 bg-purple-50 p-3 dark:bg-purple-900/20"
          >
            <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-purple-500 text-white">
              <Icon name="key" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">OAuth</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.types.antigravityOauth') }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Account Type Selection (Kiro) -->
      <div v-if="form.platform === 'kiro'">
        <label class="input-label">{{ t('admin.accounts.accountType') }}</label>
        <div class="mt-2 grid grid-cols-3 gap-3">
          <button
            type="button"
            @click="kiroAuthType = 'social'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              kiroAuthType === 'social'
                ? 'border-cyan-500 bg-cyan-50 dark:bg-cyan-900/20'
                : 'border-gray-200 hover:border-cyan-300 dark:border-dark-600 dark:hover:border-cyan-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                kiroAuthType === 'social'
                  ? 'bg-cyan-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <Icon name="user" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">Social</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.kiro.socialDesc') }}</span>
            </div>
          </button>

          <button
            type="button"
            @click="kiroAuthType = 'idc'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              kiroAuthType === 'idc'
                ? 'border-cyan-500 bg-cyan-50 dark:bg-cyan-900/20'
                : 'border-gray-200 hover:border-cyan-300 dark:border-dark-600 dark:hover:border-cyan-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                kiroAuthType === 'idc'
                  ? 'bg-cyan-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <Icon name="shield" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">IdC</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.kiro.idcDesc') }}</span>
            </div>
          </button>

          <button
            type="button"
            @click="kiroAuthType = 'apikey'"
            :class="[
              'flex items-center gap-3 rounded-lg border-2 p-3 text-left transition-all',
              kiroAuthType === 'apikey'
                ? 'border-cyan-500 bg-cyan-50 dark:bg-cyan-900/20'
                : 'border-gray-200 hover:border-cyan-300 dark:border-dark-600 dark:hover:border-cyan-700'
            ]"
          >
            <div
              :class="[
                'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                kiroAuthType === 'apikey'
                  ? 'bg-cyan-500 text-white'
                  : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
              ]"
            >
              <Icon name="key" size="sm" />
            </div>
            <div>
              <span class="block text-sm font-medium text-gray-900 dark:text-white">API Key</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.kiro.apikeyDesc') }}</span>
            </div>
          </button>
        </div>

        <!-- API Key Input (apikey auth type) -->
        <div v-if="kiroAuthType === 'apikey'" class="mt-4 space-y-4">
          <div>
            <label class="input-label">Base URL *</label>
            <input
              v-model="kiroBaseUrl"
              type="text"
              class="input font-mono text-sm"
              placeholder="https://my-proxy.example.com"
            />
            <p class="input-hint">{{ t('admin.accounts.kiro.baseUrlHint') }}</p>
          </div>
          <div>
            <label class="input-label">API Key *</label>
            <input
              v-model="kiroApiKeyValue"
              type="password"
              class="input font-mono text-sm"
              placeholder="sk-..."
            />
          </div>

          <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
            <label class="input-label">{{ t('admin.accounts.modelRestriction') }}</label>

            <div class="mb-4 flex gap-2">
              <button
                type="button"
                @click="modelRestrictionMode = 'whitelist'"
                :class="[
                  'flex-1 rounded-lg px-4 py-2 text-sm font-medium transition-all',
                  modelRestrictionMode === 'whitelist'
                    ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400'
                    : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
                ]"
              >
                {{ t('admin.accounts.modelWhitelist') }}
              </button>
              <button
                type="button"
                @click="modelRestrictionMode = 'mapping'"
                :class="[
                  'flex-1 rounded-lg px-4 py-2 text-sm font-medium transition-all',
                  modelRestrictionMode === 'mapping'
                    ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'
                    : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
                ]"
              >
                {{ t('admin.accounts.modelMapping') }}
              </button>
            </div>

            <div v-if="modelRestrictionMode === 'whitelist'">
              <ModelWhitelistSelector v-model="allowedModels" :platform="modelSelectionPlatform" />
              <p class="text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.accounts.selectedModels', { count: allowedModels.length }) }}
                <span v-if="allowedModels.length === 0">{{ t('admin.accounts.supportsAllModels') }}</span>
              </p>
            </div>

            <div v-else>
              <div v-if="modelMappings.length > 0" class="mb-3 space-y-2">
                <div
                  v-for="(mapping, index) in modelMappings"
                  :key="index"
                  class="flex items-center gap-2"
                >
                  <input
                    v-model="mapping.from"
                    type="text"
                    class="input flex-1"
                    :placeholder="t('admin.accounts.requestModel')"
                  />
                  <span class="text-gray-400">→</span>
                  <input
                    v-model="mapping.to"
                    type="text"
                    class="input flex-1"
                    :placeholder="t('admin.accounts.actualModel')"
                  />
                  <button
                    type="button"
                    @click="removeModelMapping(index)"
                    class="rounded-lg p-2 text-red-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
                  >
                    ×
                  </button>
                </div>
              </div>

              <button
                type="button"
                @click="addModelMapping"
                class="mb-3 w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
              >
                {{ t('admin.accounts.addMapping') }}
              </button>

              <div class="flex flex-wrap gap-2">
                <button
                  v-for="preset in presetMappings"
                  :key="preset.label"
                  type="button"
                  @click="addPresetMapping(preset.from, preset.to)"
                  :class="['rounded-lg px-3 py-1 text-xs transition-colors', preset.color]"
                >
                  + {{ preset.label }}
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- Kiro Input Mode Selection (social/idc only) -->
        <div v-if="kiroAuthType !== 'apikey'" class="mt-4">
          <div class="flex rounded-lg bg-gray-100 p-1 dark:bg-dark-700">
            <button
              type="button"
              @click.stop="kiroInputMode = 'single'"
              :class="[
                'flex-1 rounded-md px-4 py-2 text-sm font-medium transition-all',
                kiroInputMode === 'single'
                  ? 'bg-white text-cyan-600 shadow-sm dark:bg-dark-600 dark:text-cyan-400'
                  : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'
              ]"
            >
              {{ t('admin.accounts.kiro.singleAdd') }}
            </button>
            <button
              type="button"
              @click.stop="kiroInputMode = 'batch'"
              :class="[
                'flex-1 rounded-md px-4 py-2 text-sm font-medium transition-all',
                kiroInputMode === 'batch'
                  ? 'bg-white text-cyan-600 shadow-sm dark:bg-dark-600 dark:text-cyan-400'
                  : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'
              ]"
            >
              {{ t('admin.accounts.kiro.batchImport') }}
            </button>
          </div>
        </div>

        <!-- Single Token Input -->
        <div v-if="kiroAuthType !== 'apikey' && kiroInputMode === 'single'" class="mt-4 space-y-4">
          <div>
            <label class="input-label">{{ t('admin.accounts.kiro.refreshToken') }}</label>
            <textarea
              v-model="kiroRefreshToken"
              rows="3"
              class="input font-mono text-sm"
              :placeholder="t('admin.accounts.kiro.refreshTokenPlaceholder')"
            ></textarea>
            <p class="input-hint">{{ t('admin.accounts.kiro.refreshTokenHint') }}</p>
          </div>

          <!-- IdC specific fields -->
          <div v-if="kiroAuthType === 'idc'" class="grid grid-cols-2 gap-4">
            <div>
              <label class="input-label">Client ID *</label>
              <input
                v-model="kiroClientId"
                type="text"
                class="input font-mono text-sm"
                placeholder="BuilderId Client ID"
              />
            </div>
            <div>
              <label class="input-label">Client Secret *</label>
              <input
                v-model="kiroClientSecret"
                type="password"
                class="input font-mono text-sm"
                placeholder="BuilderId Client Secret"
              />
            </div>
          </div>
          <div v-if="kiroAuthType === 'idc'">
            <label class="input-label">{{ t('admin.accounts.kiro.profileArn') }}</label>
            <input
              v-model="kiroProfileArn"
              type="text"
              class="input font-mono text-sm"
              :placeholder="t('admin.accounts.kiro.profileArnPlaceholder')"
            />
            <p class="input-hint">{{ t('admin.accounts.kiro.profileArnHint') }}</p>
          </div>
        </div>

        <!-- Batch Import -->
        <div v-if="kiroAuthType !== 'apikey' && kiroInputMode === 'batch'" class="mt-4 space-y-4">
          <!-- File Upload Area -->
          <div
            class="relative rounded-lg border-2 border-dashed p-6 text-center transition-colors"
            :class="[
              kiroIsDragging
                ? 'border-cyan-500 bg-cyan-50 dark:bg-cyan-900/20'
                : 'border-gray-300 hover:border-cyan-400 dark:border-dark-600'
            ]"
            @dragover.prevent="kiroIsDragging = true"
            @dragleave.prevent="kiroIsDragging = false"
            @drop.prevent="handleKiroFileDrop"
          >
            <Icon name="upload" size="lg" class="mx-auto mb-2 text-gray-400" />
            <p class="text-sm text-gray-600 dark:text-gray-400">
              {{ t('admin.accounts.kiro.dragDropJson') }}
            </p>
            <p class="text-xs text-gray-500 dark:text-gray-500">
              {{ t('admin.accounts.kiro.multipleFilesSupported') }}
            </p>
            <label class="mt-2 inline-flex cursor-pointer items-center gap-1.5 rounded-lg bg-cyan-500/10 px-3 py-1.5 text-xs font-medium text-cyan-600 transition-colors hover:bg-cyan-500/20 dark:text-cyan-400">
              <Icon name="document" size="xs" />
              {{ t('admin.accounts.kiro.selectFiles') }}
              <input type="file" accept=".json" multiple class="hidden" @change="handleKiroFileSelect" />
            </label>
          </div>

          <!-- Uploaded Files List -->
          <div v-if="kiroUploadedFiles.length > 0" class="space-y-2">
            <label class="input-label">{{ t('admin.accounts.kiro.uploadedFiles') }}</label>
            <div class="max-h-32 overflow-y-auto rounded-lg border border-gray-200 dark:border-dark-600">
              <div
                v-for="(file, index) in kiroUploadedFiles"
                :key="index"
                class="flex items-center justify-between border-b border-gray-100 px-3 py-2 last:border-b-0 dark:border-dark-700"
              >
                <div class="flex items-center gap-2 min-w-0">
                  <Icon name="document" size="sm" class="flex-shrink-0 text-cyan-500" />
                  <span class="truncate text-sm text-gray-700 dark:text-gray-300">{{ file.name }}</span>
                  <span class="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400">({{ file.tokenCount }} tokens)</span>
                </div>
                <button
                  type="button"
                  @click="removeKiroUploadedFile(index)"
                  class="flex-shrink-0 rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-red-500 dark:hover:bg-dark-600"
                >
                  <Icon name="x" size="sm" />
                </button>
              </div>
            </div>
          </div>

          <!-- Or paste JSON / Refresh Tokens -->
          <div>
            <label class="input-label">
              {{ kiroAuthType === 'social' ? t('admin.accounts.kiro.orPasteJsonOrTokens') : t('admin.accounts.kiro.orPasteJson') }}
            </label>
            <textarea
              v-model="kiroBatchJson"
              rows="4"
              class="input font-mono text-xs"
              :placeholder="kiroAuthType === 'idc' ? t('admin.accounts.kiro.batchJsonPlaceholderIdc') : t('admin.accounts.kiro.batchJsonPlaceholderSocial')"
            ></textarea>
          </div>

          <!-- IdC default credentials for batch -->
          <div v-if="kiroAuthType === 'idc'" class="grid grid-cols-2 gap-4">
            <div>
              <label class="input-label">{{ t('admin.accounts.kiro.defaultClientId') }}</label>
              <input
                v-model="kiroClientId"
                type="text"
                class="input font-mono text-sm"
                :placeholder="t('admin.accounts.kiro.defaultClientIdHint')"
              />
            </div>
            <div>
              <label class="input-label">{{ t('admin.accounts.kiro.defaultClientSecret') }}</label>
              <input
                v-model="kiroClientSecret"
                type="password"
                class="input font-mono text-sm"
                :placeholder="t('admin.accounts.kiro.defaultClientSecretHint')"
              />
            </div>
          </div>
          <div v-if="kiroAuthType === 'idc'">
            <label class="input-label">{{ t('admin.accounts.kiro.defaultProfileArn') }}</label>
            <input
              v-model="kiroProfileArn"
              type="text"
              class="input font-mono text-sm"
              :placeholder="t('admin.accounts.kiro.defaultProfileArnHint')"
            />
          </div>

          <!-- Parsed tokens count -->
          <div v-if="kiroParsedTokens.length > 0" class="rounded-lg bg-green-50 p-3 dark:bg-green-900/20">
            <div class="flex items-center gap-2 text-sm text-green-700 dark:text-green-400">
              <Icon name="check" size="sm" />
              <span>{{ t('admin.accounts.kiro.parsedTokens', { count: kiroParsedTokens.length }) }}</span>
            </div>
          </div>

          <!-- Parse error -->
          <div v-if="kiroParseError" class="rounded-lg bg-red-50 p-3 dark:bg-red-900/20">
            <div class="flex items-center gap-2 text-sm text-red-700 dark:text-red-400">
              <Icon name="exclamationTriangle" size="sm" />
              <span>{{ kiroParseError }}</span>
            </div>
          </div>

          <!-- Parse button -->
          <button
            type="button"
            class="btn btn-secondary w-full"
            :disabled="!kiroBatchJson.trim()"
            @click="parseKiroBatchJson"
          >
            {{ kiroAuthType === 'social' ? t('admin.accounts.kiro.parseJsonOrTokens') : t('admin.accounts.kiro.parseJson') }}
          </button>
        </div>

        <!-- Help section -->
        <div class="mt-4 rounded-lg bg-cyan-50 p-4 dark:bg-cyan-900/20">
          <div class="flex items-start gap-3">
            <Icon name="infoCircle" size="md" class="flex-shrink-0 text-cyan-600 dark:text-cyan-400" />
            <div class="text-sm text-cyan-700 dark:text-cyan-300">
              <p class="font-medium">{{ t('admin.accounts.kiro.howToGetToken') }}</p>
              <ol class="mt-2 list-inside list-decimal space-y-1 text-xs">
                <li>{{ t('admin.accounts.kiro.step1') }}</li>
                <li>{{ t('admin.accounts.kiro.step2') }}</li>
                <li>{{ t('admin.accounts.kiro.step3') }}</li>
              </ol>
            </div>
          </div>
        </div>
      </div>

      <!-- Add Method (only for Anthropic OAuth-based type) -->
      <div v-if="form.platform === 'anthropic' && isOAuthFlow">
        <label class="input-label">{{ t('admin.accounts.addMethod') }}</label>
        <div class="mt-2 flex gap-4">
          <label class="flex cursor-pointer items-center">
            <input
              v-model="addMethod"
              type="radio"
              value="oauth"
              class="mr-2 text-primary-600 focus:ring-primary-500"
            />
            <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.accounts.types.oauth') }}</span>
          </label>
          <label class="flex cursor-pointer items-center">
            <input
              v-model="addMethod"
              type="radio"
              value="setup-token"
              class="mr-2 text-primary-600 focus:ring-primary-500"
            />
            <span class="text-sm text-gray-700 dark:text-gray-300">{{
              t('admin.accounts.setupTokenLongLived')
            }}</span>
          </label>
        </div>
      </div>

      <!-- API Key input (only for apikey type) -->
      <div v-if="form.type === 'apikey'" class="space-y-4">
        <div>
          <label class="input-label">{{ t('admin.accounts.baseUrl') }}</label>
          <input
            v-model="apiKeyBaseUrl"
            type="text"
            class="input"
            :placeholder="
              form.platform === 'openai'
                ? 'https://api.openai.com'
                : form.platform === 'gemini'
                  ? 'https://generativelanguage.googleapis.com'
                  : 'https://api.anthropic.com'
            "
          />
          <p class="input-hint">{{ baseUrlHint }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('admin.accounts.apiKeyRequired') }}</label>
          <input
            v-model="apiKeyValue"
            type="password"
            required
            class="input font-mono"
            :placeholder="
              form.platform === 'openai'
                ? 'sk-proj-...'
                : form.platform === 'gemini'
                  ? 'AIza...'
                  : 'sk-ant-...'
            "
          />
          <p class="input-hint">{{ apiKeyHint }}</p>
        </div>

        <!-- Gemini API Key tier selection -->
        <div v-if="form.platform === 'gemini'">
          <label class="input-label">{{ t('admin.accounts.gemini.tier.label') }}</label>
          <select v-model="geminiTierAIStudio" class="input">
            <option value="aistudio_free">{{ t('admin.accounts.gemini.tier.aiStudio.free') }}</option>
            <option value="aistudio_paid">{{ t('admin.accounts.gemini.tier.aiStudio.paid') }}</option>
          </select>
          <p class="input-hint">{{ t('admin.accounts.gemini.tier.aiStudioHint') }}</p>
        </div>

        <!-- Model Restriction Section -->
        <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
          <label class="input-label">{{ t('admin.accounts.modelRestriction') }}</label>

          <!-- Mode Toggle -->
          <div class="mb-4 flex gap-2">
            <button
              type="button"
              @click="modelRestrictionMode = 'whitelist'"
              :class="[
                'flex-1 rounded-lg px-4 py-2 text-sm font-medium transition-all',
                modelRestrictionMode === 'whitelist'
                  ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400'
                  : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
              ]"
            >
              <svg
                class="mr-1.5 inline h-4 w-4"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"
                />
              </svg>
              {{ t('admin.accounts.modelWhitelist') }}
            </button>
            <button
              type="button"
              @click="modelRestrictionMode = 'mapping'"
              :class="[
                'flex-1 rounded-lg px-4 py-2 text-sm font-medium transition-all',
                modelRestrictionMode === 'mapping'
                  ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'
                  : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
              ]"
            >
              <svg
                class="mr-1.5 inline h-4 w-4"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M8 7h12m0 0l-4-4m4 4l-4 4m0 6H4m0 0l4 4m-4-4l4-4"
                />
              </svg>
              {{ t('admin.accounts.modelMapping') }}
            </button>
          </div>

          <!-- Whitelist Mode -->
          <div v-if="modelRestrictionMode === 'whitelist'">
            <ModelWhitelistSelector v-model="allowedModels" :platform="modelSelectionPlatform" />
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.selectedModels', { count: allowedModels.length }) }}
              <span v-if="allowedModels.length === 0">{{
                t('admin.accounts.supportsAllModels')
              }}</span>
            </p>
          </div>

          <!-- Mapping Mode -->
          <div v-else>
            <div class="mb-3 rounded-lg bg-purple-50 p-3 dark:bg-purple-900/20">
              <p class="text-xs text-purple-700 dark:text-purple-400">
                <svg
                  class="mr-1 inline h-4 w-4"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                {{ t('admin.accounts.mapRequestModels') }}
              </p>
            </div>

            <!-- Model Mapping List -->
            <div v-if="modelMappings.length > 0" class="mb-3 space-y-2">
              <div
                v-for="(mapping, index) in modelMappings"
                :key="index"
                class="flex items-center gap-2"
              >
                <input
                  v-model="mapping.from"
                  type="text"
                  class="input flex-1"
                  :placeholder="t('admin.accounts.requestModel')"
                />
                <svg
                  class="h-4 w-4 flex-shrink-0 text-gray-400"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M14 5l7 7m0 0l-7 7m7-7H3"
                  />
                </svg>
                <input
                  v-model="mapping.to"
                  type="text"
                  class="input flex-1"
                  :placeholder="t('admin.accounts.actualModel')"
                />
                <button
                  type="button"
                  @click="removeModelMapping(index)"
                  class="rounded-lg p-2 text-red-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
                >
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      stroke-width="2"
                      d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                    />
                  </svg>
                </button>
              </div>
            </div>

            <button
              type="button"
              @click="addModelMapping"
              class="mb-3 w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
            >
              <svg
                class="mr-1 inline h-4 w-4"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M12 4v16m8-8H4"
                />
              </svg>
              {{ t('admin.accounts.addMapping') }}
            </button>

            <!-- Quick Add Buttons -->
            <div class="flex flex-wrap gap-2">
              <button
                v-for="preset in presetMappings"
                :key="preset.label"
                type="button"
                @click="addPresetMapping(preset.from, preset.to)"
                :class="['rounded-lg px-3 py-1 text-xs transition-colors', preset.color]"
              >
                + {{ preset.label }}
              </button>
            </div>
          </div>
        </div>

        <!-- Custom Error Codes Section -->
        <div
          v-if="form.platform !== 'gemini'"
          class="border-t border-gray-200 pt-4 dark:border-dark-600"
        >
          <div class="mb-3 flex items-center justify-between">
            <div>
              <label class="input-label mb-0">{{ t('admin.accounts.customErrorCodes') }}</label>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.accounts.customErrorCodesHint') }}
              </p>
            </div>
            <button
              type="button"
              @click="customErrorCodesEnabled = !customErrorCodesEnabled"
              :class="[
                'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
                customErrorCodesEnabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
              ]"
            >
              <span
                :class="[
                  'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                  customErrorCodesEnabled ? 'translate-x-5' : 'translate-x-0'
                ]"
              />
            </button>
          </div>

          <div v-if="customErrorCodesEnabled" class="space-y-3">
            <div class="rounded-lg bg-amber-50 p-3 dark:bg-amber-900/20">
              <p class="text-xs text-amber-700 dark:text-amber-400">
                <Icon name="exclamationTriangle" size="sm" class="mr-1 inline" :stroke-width="2" />
                {{ t('admin.accounts.customErrorCodesWarning') }}
              </p>
            </div>

            <!-- Error Code Buttons -->
            <div class="flex flex-wrap gap-2">
              <button
                v-for="code in commonErrorCodes"
                :key="code.value"
                type="button"
                @click="toggleErrorCode(code.value)"
                :class="[
                  'rounded-lg px-3 py-1.5 text-sm font-medium transition-colors',
                  selectedErrorCodes.includes(code.value)
                    ? 'bg-red-100 text-red-700 ring-1 ring-red-500 dark:bg-red-900/30 dark:text-red-400'
                    : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
                ]"
              >
                {{ code.value }} {{ code.label }}
              </button>
            </div>

            <!-- Manual input -->
            <div class="flex items-center gap-2">
              <input
                v-model.number="customErrorCodeInput"
                type="number"
                min="100"
                max="599"
                class="input flex-1"
                :placeholder="t('admin.accounts.enterErrorCode')"
                @keyup.enter="addCustomErrorCode"
              />
              <button type="button" @click="addCustomErrorCode" class="btn btn-secondary px-3">
                <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M12 4v16m8-8H4"
                  />
                </svg>
              </button>
            </div>

            <!-- Selected codes summary -->
            <div class="flex flex-wrap gap-1.5">
              <span
                v-for="code in selectedErrorCodes.sort((a, b) => a - b)"
                :key="code"
                class="inline-flex items-center gap-1 rounded-full bg-red-100 px-2.5 py-0.5 text-sm font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
              >
                {{ code }}
                <button
                  type="button"
                  @click="removeErrorCode(code)"
                  class="hover:text-red-900 dark:hover:text-red-300"
                >
                  <Icon name="x" size="sm" :stroke-width="2" />
                </button>
              </span>
              <span v-if="selectedErrorCodes.length === 0" class="text-xs text-gray-400">
                {{ t('admin.accounts.noneSelectedUsesDefault') }}
              </span>
            </div>
          </div>
        </div>

        <!-- Gemini 模型说明 -->
        <div v-if="form.platform === 'gemini'" class="border-t border-gray-200 pt-4 dark:border-dark-600">
          <div class="rounded-lg bg-blue-50 p-4 dark:bg-blue-900/20">
            <div class="flex items-start gap-3">
              <svg
                class="h-5 w-5 flex-shrink-0 text-blue-600 dark:text-blue-400"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                />
              </svg>
              <div>
                <p class="text-sm font-medium text-blue-800 dark:text-blue-300">
                  {{ t('admin.accounts.gemini.modelPassthrough') }}
                </p>
                <p class="mt-1 text-xs text-blue-700 dark:text-blue-400">
                  {{ t('admin.accounts.gemini.modelPassthroughDesc') }}
                </p>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Temp Unschedulable Rules -->
      <div class="border-t border-gray-200 pt-4 dark:border-dark-600 space-y-4">
        <div class="mb-3 flex items-center justify-between">
          <div>
            <label class="input-label mb-0">{{ t('admin.accounts.tempUnschedulable.title') }}</label>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.tempUnschedulable.hint') }}
            </p>
          </div>
          <button
            type="button"
            @click="tempUnschedEnabled = !tempUnschedEnabled"
            :class="[
              'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
              tempUnschedEnabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
            ]"
          >
            <span
              :class="[
                'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                tempUnschedEnabled ? 'translate-x-5' : 'translate-x-0'
              ]"
            />
          </button>
        </div>

        <div v-if="tempUnschedEnabled" class="space-y-3">
          <div class="rounded-lg bg-blue-50 p-3 dark:bg-blue-900/20">
              <p class="text-xs text-blue-700 dark:text-blue-400">
                <Icon name="exclamationTriangle" size="sm" class="mr-1 inline" :stroke-width="2" />
                {{ t('admin.accounts.tempUnschedulable.notice') }}
              </p>
            </div>

          <div class="flex flex-wrap gap-2">
            <button
              v-for="preset in tempUnschedPresets"
              :key="preset.label"
              type="button"
              @click="addTempUnschedRule(preset.rule)"
              class="rounded-lg bg-gray-100 px-3 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-300 dark:hover:bg-dark-500"
            >
              + {{ preset.label }}
            </button>
          </div>

          <div v-if="tempUnschedRules.length > 0" class="space-y-3">
            <div
              v-for="(rule, index) in tempUnschedRules"
              :key="index"
              class="rounded-lg border border-gray-200 p-3 dark:border-dark-600"
            >
              <div class="mb-2 flex items-center justify-between">
                <span class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.accounts.tempUnschedulable.ruleIndex', { index: index + 1 }) }}
                </span>
                <div class="flex items-center gap-2">
                  <button
                    type="button"
                    :disabled="index === 0"
                    @click="moveTempUnschedRule(index, -1)"
                    class="rounded p-1 text-gray-400 transition-colors hover:text-gray-600 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:text-gray-200"
                  >
                    <Icon name="chevronUp" size="sm" :stroke-width="2" />
                  </button>
                  <button
                    type="button"
                    :disabled="index === tempUnschedRules.length - 1"
                    @click="moveTempUnschedRule(index, 1)"
                    class="rounded p-1 text-gray-400 transition-colors hover:text-gray-600 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:text-gray-200"
                  >
                    <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
                    </svg>
                  </button>
                  <button
                    type="button"
                    @click="removeTempUnschedRule(index)"
                    class="rounded p-1 text-red-500 transition-colors hover:text-red-600"
                  >
                    <Icon name="x" size="sm" :stroke-width="2" />
                  </button>
                </div>
              </div>

              <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div>
                  <label class="input-label">{{ t('admin.accounts.tempUnschedulable.errorCode') }}</label>
                  <input
                    v-model.number="rule.error_code"
                    type="number"
                    min="100"
                    max="599"
                    class="input"
                    :placeholder="t('admin.accounts.tempUnschedulable.errorCodePlaceholder')"
                  />
                </div>
                <div>
                  <label class="input-label">{{ t('admin.accounts.tempUnschedulable.durationMinutes') }}</label>
                  <input
                    v-model.number="rule.duration_minutes"
                    type="number"
                    min="1"
                    class="input"
                    :placeholder="t('admin.accounts.tempUnschedulable.durationPlaceholder')"
                  />
                </div>
                <div class="sm:col-span-2">
                  <label class="input-label">{{ t('admin.accounts.tempUnschedulable.keywords') }}</label>
                  <input
                    v-model="rule.keywords"
                    type="text"
                    class="input"
                    :placeholder="t('admin.accounts.tempUnschedulable.keywordsPlaceholder')"
                  />
                  <p class="input-hint">{{ t('admin.accounts.tempUnschedulable.keywordsHint') }}</p>
                </div>
                <div class="sm:col-span-2">
                  <label class="input-label">{{ t('admin.accounts.tempUnschedulable.description') }}</label>
                  <input
                    v-model="rule.description"
                    type="text"
                    class="input"
                    :placeholder="t('admin.accounts.tempUnschedulable.descriptionPlaceholder')"
                  />
                </div>
              </div>
            </div>
          </div>

          <button
            type="button"
            @click="addTempUnschedRule()"
            class="w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-sm text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
          >
            <svg
              class="mr-1 inline h-4 w-4"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
            </svg>
            {{ t('admin.accounts.tempUnschedulable.addRule') }}
          </button>
        </div>
      </div>

      <!-- Intercept Warmup Requests (Anthropic only) -->
      <div
        v-if="form.platform === 'anthropic'"
        class="border-t border-gray-200 pt-4 dark:border-dark-600"
      >
        <div class="flex items-center justify-between">
          <div>
            <label class="input-label mb-0">{{
              t('admin.accounts.interceptWarmupRequests')
            }}</label>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.interceptWarmupRequestsDesc') }}
            </p>
          </div>
          <button
            type="button"
            @click="interceptWarmupRequests = !interceptWarmupRequests"
            :class="[
              'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
              interceptWarmupRequests ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
            ]"
          >
            <span
              :class="[
                'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                interceptWarmupRequests ? 'translate-x-5' : 'translate-x-0'
              ]"
            />
          </button>
        </div>
      </div>

      <div>
        <label class="input-label">{{ t('admin.accounts.proxy') }}</label>
        <ProxySelector v-model="form.proxy_id" :proxies="proxies" />
      </div>

      <div class="grid grid-cols-2 gap-4 lg:grid-cols-3">
        <div>
          <label class="input-label">{{ t('admin.accounts.concurrency') }}</label>
          <input v-model.number="form.concurrency" type="number" min="1" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.accounts.priority') }}</label>
          <input
            v-model.number="form.priority"
            type="number"
            min="1"
            class="input"
            data-tour="account-form-priority"
          />
          <p class="input-hint">{{ t('admin.accounts.priorityHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('admin.accounts.billingRateMultiplier') }}</label>
          <input v-model.number="form.rate_multiplier" type="number" min="0" step="0.01" class="input" />
          <p class="input-hint">{{ t('admin.accounts.billingRateMultiplierHint') }}</p>
        </div>
      </div>
      <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <label class="input-label">{{ t('admin.accounts.expiresAt') }}</label>
        <input v-model="expiresAtInput" type="datetime-local" class="input" />
        <p class="input-hint">{{ t('admin.accounts.expiresAtHint') }}</p>
      </div>

      <div>
        <div class="flex items-center justify-between">
          <div>
            <label class="input-label mb-0">{{
              t('admin.accounts.autoPauseOnExpired')
            }}</label>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.autoPauseOnExpiredDesc') }}
            </p>
          </div>
          <button
            type="button"
            @click="autoPauseOnExpired = !autoPauseOnExpired"
            :class="[
              'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
              autoPauseOnExpired ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
            ]"
          >
            <span
              :class="[
                'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                autoPauseOnExpired ? 'translate-x-5' : 'translate-x-0'
              ]"
            />
          </button>
        </div>
      </div>

      <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <!-- Mixed Scheduling (only for antigravity accounts) -->
        <div v-if="form.platform === 'antigravity'" class="flex items-center gap-2">
          <label class="flex cursor-pointer items-center gap-2">
            <input
              type="checkbox"
              v-model="mixedScheduling"
              class="h-4 w-4 rounded border-gray-300 text-primary-500 focus:ring-primary-500 dark:border-dark-500"
            />
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.accounts.mixedScheduling') }}
            </span>
          </label>
          <div class="group relative">
            <span
              class="inline-flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-gray-200 text-xs text-gray-500 hover:bg-gray-300 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500"
            >
              ?
            </span>
            <!-- Tooltip（向下显示避免被弹窗裁剪） -->
            <div
              class="pointer-events-none absolute left-0 top-full z-[100] mt-1.5 w-72 rounded bg-gray-900 px-3 py-2 text-xs text-white opacity-0 transition-opacity group-hover:opacity-100 dark:bg-gray-700"
            >
              {{ t('admin.accounts.mixedSchedulingTooltip') }}
              <div
                class="absolute bottom-full left-3 border-4 border-transparent border-b-gray-900 dark:border-b-gray-700"
              ></div>
            </div>
          </div>
        </div>

        <!-- Group Selection - 仅标准模式显示 -->
        <GroupSelector
          v-if="!authStore.isSimpleMode"
          v-model="form.group_ids"
          :groups="groups"
          :platform="form.platform"
          :mixed-scheduling="mixedScheduling"
          data-tour="account-form-groups"
        />
      </div>

    </form>

    <!-- Step 2: OAuth Authorization -->
    <div v-else class="space-y-5">
      <OAuthAuthorizationFlow
        ref="oauthFlowRef"
        :add-method="form.platform === 'anthropic' ? addMethod : 'oauth'"
        :auth-url="currentAuthUrl"
        :session-id="currentSessionId"
        :loading="currentOAuthLoading"
        :error="currentOAuthError"
        :show-help="form.platform === 'anthropic'"
        :show-proxy-warning="form.platform !== 'openai' && !!form.proxy_id"
        :allow-multiple="form.platform === 'anthropic'"
        :show-cookie-option="form.platform === 'anthropic'"
        :platform="form.platform"
        :show-project-id="geminiOAuthType === 'code_assist'"
        @generate-url="handleGenerateUrl"
        @cookie-auth="handleCookieAuth"
      />

    </div>

    <template #footer>
      <div v-if="step === 1" class="flex justify-end gap-3">
        <button @click="handleClose" type="button" class="btn btn-secondary">
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="create-account-form"
          :disabled="submitting"
          class="btn btn-primary"
          data-tour="account-form-submit"
        >
          <svg
            v-if="submitting"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
            fill="none"
            viewBox="0 0 24 24"
          >
            <circle
              class="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              stroke-width="4"
            ></circle>
            <path
              class="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            ></path>
          </svg>
          {{
            isOAuthFlow
              ? t('common.next')
              : submitting
                ? t('admin.accounts.creating')
                : t('common.create')
          }}
        </button>
      </div>
      <div v-else class="flex justify-between gap-3">
        <button type="button" class="btn btn-secondary" @click="goBackToBasicInfo">
          {{ t('common.back') }}
        </button>
        <button
          v-if="isManualInputMethod"
          type="button"
          :disabled="!canExchangeCode"
          class="btn btn-primary"
          @click="handleExchangeCode"
        >
          <svg
            v-if="currentOAuthLoading"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
            fill="none"
            viewBox="0 0 24 24"
          >
            <circle
              class="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              stroke-width="4"
            ></circle>
            <path
              class="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            ></path>
          </svg>
          {{
            currentOAuthLoading
              ? t('admin.accounts.oauth.verifying')
              : t('admin.accounts.oauth.completeAuth')
          }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <!-- Gemini Help Dialog -->
  <BaseDialog
    :show="showGeminiHelpDialog"
    :title="t('admin.accounts.gemini.helpDialog.title')"
    @close="showGeminiHelpDialog = false"
    max-width="max-w-3xl"
  >
    <div class="space-y-6">
      <!-- Setup Guide Section -->
      <div>
        <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.accounts.gemini.setupGuide.title') }}
        </h3>
        <div class="space-y-4">
          <div>
            <p class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.accounts.gemini.setupGuide.checklistTitle') }}
            </p>
            <ul class="list-inside list-disc space-y-1 text-sm text-gray-600 dark:text-gray-400">
              <li>{{ t('admin.accounts.gemini.setupGuide.checklistItems.usIp') }}</li>
              <li>{{ t('admin.accounts.gemini.setupGuide.checklistItems.age') }}</li>
            </ul>
          </div>
          <div>
            <p class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.accounts.gemini.setupGuide.activationTitle') }}
            </p>
            <ul class="list-inside list-disc space-y-1 text-sm text-gray-600 dark:text-gray-400">
              <li>{{ t('admin.accounts.gemini.setupGuide.activationItems.geminiWeb') }}</li>
              <li>{{ t('admin.accounts.gemini.setupGuide.activationItems.gcpProject') }}</li>
            </ul>
            <div class="mt-2 flex flex-wrap gap-2">
              <a
                href="https://policies.google.com/terms"
                target="_blank"
                rel="noreferrer"
                class="text-sm text-blue-600 hover:underline dark:text-blue-400"
              >
                {{ t('admin.accounts.gemini.setupGuide.links.countryCheck') }}
              </a>
              <span class="text-gray-400">·</span>
              <a
                href="https://policies.google.com/country-association-form"
                target="_blank"
                rel="noreferrer"
                class="text-sm text-blue-600 hover:underline dark:text-blue-400"
              >
                修改归属地
              </a>
              <span class="text-gray-400">·</span>
              <a
                href="https://gemini.google.com/gems/create?hl=en-US&pli=1"
                target="_blank"
                rel="noreferrer"
                class="text-sm text-blue-600 hover:underline dark:text-blue-400"
              >
                {{ t('admin.accounts.gemini.setupGuide.links.geminiWebActivation') }}
              </a>
              <span class="text-gray-400">·</span>
              <a
                href="https://console.cloud.google.com"
                target="_blank"
                rel="noreferrer"
                class="text-sm text-blue-600 hover:underline dark:text-blue-400"
              >
                {{ t('admin.accounts.gemini.setupGuide.links.gcpProject') }}
              </a>
            </div>
          </div>
        </div>
      </div>

      <!-- Quota Policy Section -->
      <div class="border-t border-gray-200 pt-6 dark:border-dark-600">
        <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.accounts.gemini.quotaPolicy.title') }}
        </h3>
        <p class="mb-4 text-xs text-amber-600 dark:text-amber-400">
          {{ t('admin.accounts.gemini.quotaPolicy.note') }}
        </p>
        <div class="overflow-x-auto">
          <table class="w-full text-xs">
            <thead class="bg-gray-50 dark:bg-dark-800/90">
              <tr>
                <th class="px-3 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
                  {{ t('admin.accounts.gemini.quotaPolicy.columns.channel') }}
                </th>
                <th class="px-3 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
                  {{ t('admin.accounts.gemini.quotaPolicy.columns.account') }}
                </th>
                <th class="px-3 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
                  {{ t('admin.accounts.gemini.quotaPolicy.columns.limits') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-200 dark:divide-dark-700/60">
              <tr>
                <td class="px-3 py-2 text-gray-900 dark:text-white">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.googleOne.channel') }}
                </td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">Free</td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsFree') }}
                </td>
              </tr>
              <tr>
                <td class="px-3 py-2 text-gray-900 dark:text-white"></td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">Pro</td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsPro') }}
                </td>
              </tr>
              <tr>
                <td class="px-3 py-2 text-gray-900 dark:text-white"></td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">Ultra</td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsUltra') }}
                </td>
              </tr>
              <tr>
                <td class="px-3 py-2 text-gray-900 dark:text-white">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.gcp.channel') }}
                </td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">Standard</td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.gcp.limitsStandard') }}
                </td>
              </tr>
              <tr>
                <td class="px-3 py-2 text-gray-900 dark:text-white"></td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">Enterprise</td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.gcp.limitsEnterprise') }}
                </td>
              </tr>
              <tr>
                <td class="px-3 py-2 text-gray-900 dark:text-white">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.channel') }}
                </td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">Free</td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.limitsFree') }}
                </td>
              </tr>
              <tr>
                <td class="px-3 py-2 text-gray-900 dark:text-white"></td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">Paid</td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-400">
                  {{ t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.limitsPaid') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="mt-4 flex flex-wrap gap-3">
          <a
            :href="geminiQuotaDocs.codeAssist"
            target="_blank"
            rel="noreferrer"
            class="text-sm text-blue-600 hover:underline dark:text-blue-400"
          >
            {{ t('admin.accounts.gemini.quotaPolicy.docs.codeAssist') }}
          </a>
          <a
            :href="geminiQuotaDocs.aiStudio"
            target="_blank"
            rel="noreferrer"
            class="text-sm text-blue-600 hover:underline dark:text-blue-400"
          >
            {{ t('admin.accounts.gemini.quotaPolicy.docs.aiStudio') }}
          </a>
          <a
            :href="geminiQuotaDocs.vertex"
            target="_blank"
            rel="noreferrer"
            class="text-sm text-blue-600 hover:underline dark:text-blue-400"
          >
            {{ t('admin.accounts.gemini.quotaPolicy.docs.vertex') }}
          </a>
        </div>
      </div>

      <!-- API Key Links Section -->
      <div class="border-t border-gray-200 pt-6 dark:border-dark-600">
        <h3 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.accounts.gemini.helpDialog.apiKeySection') }}
        </h3>
        <div class="flex flex-wrap gap-3">
          <a
            :href="geminiHelpLinks.apiKey"
            target="_blank"
            rel="noreferrer"
            class="text-sm text-blue-600 hover:underline dark:text-blue-400"
          >
            {{ t('admin.accounts.gemini.accountType.apiKeyLink') }}
          </a>
          <a
            :href="geminiHelpLinks.aiStudioPricing"
            target="_blank"
            rel="noreferrer"
            class="text-sm text-blue-600 hover:underline dark:text-blue-400"
          >
            {{ t('admin.accounts.gemini.accountType.quotaLink') }}
          </a>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button @click="showGeminiHelpDialog = false" type="button" class="btn btn-primary">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { claudeModels, getPresetMappingsByPlatform, getModelsByPlatform, commonErrorCodes, buildModelMappingObject } from '@/composables/useModelWhitelist'
import { useAuthStore } from '@/stores/auth'
import { adminAPI } from '@/api/admin'
import {
  useAccountOAuth,
  type AddMethod,
  type AuthInputMethod
} from '@/composables/useAccountOAuth'
import { useOpenAIOAuth } from '@/composables/useOpenAIOAuth'
import { useGeminiOAuth } from '@/composables/useGeminiOAuth'
import { useAntigravityOAuth } from '@/composables/useAntigravityOAuth'
import type { Proxy, AdminGroup, AccountPlatform, AccountType } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import ProxySelector from '@/components/common/ProxySelector.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import { formatDateTimeLocalInput, parseDateTimeLocalInput } from '@/utils/format'
import OAuthAuthorizationFlow from './OAuthAuthorizationFlow.vue'

// Type for exposed OAuthAuthorizationFlow component
// Note: defineExpose automatically unwraps refs, so we use the unwrapped types
interface OAuthFlowExposed {
  authCode: string
  oauthState: string
  projectId: string
  sessionKey: string
  inputMethod: AuthInputMethod
  reset: () => void
}

const { t } = useI18n()
const authStore = useAuthStore()

const oauthStepTitle = computed(() => {
  if (form.platform === 'openai') return t('admin.accounts.oauth.openai.title')
  if (form.platform === 'gemini') return t('admin.accounts.oauth.gemini.title')
  if (form.platform === 'antigravity') return t('admin.accounts.oauth.antigravity.title')
  return t('admin.accounts.oauth.title')
})

// Platform-specific hints for API Key type
const baseUrlHint = computed(() => {
  if (form.platform === 'openai') return t('admin.accounts.openai.baseUrlHint')
  if (form.platform === 'gemini') return t('admin.accounts.gemini.baseUrlHint')
  return t('admin.accounts.baseUrlHint')
})

const apiKeyHint = computed(() => {
  if (form.platform === 'openai') return t('admin.accounts.openai.apiKeyHint')
  if (form.platform === 'gemini') return t('admin.accounts.gemini.apiKeyHint')
  return t('admin.accounts.apiKeyHint')
})

// Platform options for dropdown
const platformOptions = computed(() => [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'kiro', label: 'Kiro' }
])

interface Props {
  show: boolean
  proxies: Proxy[]
  groups: AdminGroup[]
  defaultPlatform?: AccountPlatform | ''
}

const props = defineProps<Props>()
const emit = defineEmits<{
  close: []
  created: []
}>()

const appStore = useAppStore()

// OAuth composables
const oauth = useAccountOAuth() // For Anthropic OAuth
const openaiOAuth = useOpenAIOAuth() // For OpenAI OAuth
const geminiOAuth = useGeminiOAuth() // For Gemini OAuth
const antigravityOAuth = useAntigravityOAuth() // For Antigravity OAuth

// Computed: current OAuth state for template binding
const currentAuthUrl = computed(() => {
  if (form.platform === 'openai') return openaiOAuth.authUrl.value
  if (form.platform === 'gemini') return geminiOAuth.authUrl.value
  if (form.platform === 'antigravity') return antigravityOAuth.authUrl.value
  return oauth.authUrl.value
})

const currentSessionId = computed(() => {
  if (form.platform === 'openai') return openaiOAuth.sessionId.value
  if (form.platform === 'gemini') return geminiOAuth.sessionId.value
  if (form.platform === 'antigravity') return antigravityOAuth.sessionId.value
  return oauth.sessionId.value
})

const currentOAuthLoading = computed(() => {
  if (form.platform === 'openai') return openaiOAuth.loading.value
  if (form.platform === 'gemini') return geminiOAuth.loading.value
  if (form.platform === 'antigravity') return antigravityOAuth.loading.value
  return oauth.loading.value
})

const currentOAuthError = computed(() => {
  if (form.platform === 'openai') return openaiOAuth.error.value
  if (form.platform === 'gemini') return geminiOAuth.error.value
  if (form.platform === 'antigravity') return antigravityOAuth.error.value
  return oauth.error.value
})

// Refs
const oauthFlowRef = ref<OAuthFlowExposed | null>(null)

// Model mapping type
interface ModelMapping {
  from: string
  to: string
}

interface TempUnschedRuleForm {
  error_code: number | null
  keywords: string
  duration_minutes: number | null
  description: string
}

// State
const step = ref(1)
const submitting = ref(false)
const accountCategory = ref<'oauth-based' | 'apikey'>('oauth-based') // UI selection for account category
const addMethod = ref<AddMethod>('oauth') // For oauth-based: 'oauth' or 'setup-token'
const apiKeyBaseUrl = ref('https://api.anthropic.com')
const apiKeyValue = ref('')
const modelMappings = ref<ModelMapping[]>([])
const modelRestrictionMode = ref<'whitelist' | 'mapping'>('whitelist')
const allowedModels = ref<string[]>([])
const customErrorCodesEnabled = ref(false)
const selectedErrorCodes = ref<number[]>([])
const customErrorCodeInput = ref<number | null>(null)
const interceptWarmupRequests = ref(false)
const autoPauseOnExpired = ref(true)
const mixedScheduling = ref(false) // For antigravity accounts: enable mixed scheduling
const kiroRefreshToken = ref('') // For kiro accounts: refresh token
const kiroAuthType = ref<'social' | 'idc' | 'apikey'>('social') // Kiro auth type
const kiroInputMode = ref<'single' | 'batch'>('single') // Kiro input mode
const kiroClientId = ref('') // For IdC auth
const kiroClientSecret = ref('') // For IdC auth
const kiroProfileArn = ref('') // For IdC auth
const kiroApiKeyValue = ref('') // For apikey auth
const kiroBaseUrl = ref('') // For apikey auth
const kiroBatchJson = ref('') // For batch import
const kiroIsDragging = ref(false) // For drag-drop
type KiroParsedToken = { refreshToken: string; clientId?: string; clientSecret?: string; profileArn?: string; provider?: string; name?: string; region?: string }
const kiroParsedTokens = ref<KiroParsedToken[]>([])
const kiroParseError = ref('')
const kiroUploadedFiles = ref<Array<{ name: string; tokenCount: number; tokens: KiroParsedToken[] }>>([])
const tempUnschedEnabled = ref(false)
// OpenAI batch import
const openaiInputMode = ref<'single' | 'batch'>('single')
const openaiBatchJson = ref('')
const openaiIsDragging = ref(false)
const openaiParsedAccounts = ref<Array<{ email?: string; password?: string; accessToken: string; refreshToken: string }>>([])
const openaiParseError = ref('')
const openaiUploadedFiles = ref<Array<{ name: string; accountCount: number; accounts: Array<{ email?: string; password?: string; accessToken: string; refreshToken: string }> }>>([])
const existingOpenaiEmails = ref<Set<string>>(new Set())
const openaiEmailFilterLoading = ref(false)
const tempUnschedRules = ref<TempUnschedRuleForm[]>([])
const geminiOAuthType = ref<'code_assist' | 'google_one' | 'ai_studio'>('google_one')
const geminiAIStudioOAuthEnabled = ref(false)
const showAdvancedOAuth = ref(false)
const showGeminiHelpDialog = ref(false)

// Gemini tier selection (used as fallback when auto-detection is unavailable/fails)
const geminiTierGoogleOne = ref<'google_one_free' | 'google_ai_pro' | 'google_ai_ultra'>('google_one_free')
const geminiTierGcp = ref<'gcp_standard' | 'gcp_enterprise'>('gcp_standard')
const geminiTierAIStudio = ref<'aistudio_free' | 'aistudio_paid'>('aistudio_free')

const geminiSelectedTier = computed(() => {
  if (form.platform !== 'gemini') return ''
  if (accountCategory.value === 'apikey') return geminiTierAIStudio.value
  switch (geminiOAuthType.value) {
    case 'google_one':
      return geminiTierGoogleOne.value
    case 'code_assist':
      return geminiTierGcp.value
    default:
      return geminiTierAIStudio.value
  }
})

const geminiQuotaDocs = {
  codeAssist: 'https://developers.google.com/gemini-code-assist/resources/quotas',
  aiStudio: 'https://ai.google.dev/pricing',
  vertex: 'https://cloud.google.com/vertex-ai/generative-ai/docs/quotas'
}

const geminiHelpLinks = {
  apiKey: 'https://aistudio.google.com/app/apikey',
  aiStudioPricing: 'https://ai.google.dev/pricing',
  gcpProject: 'https://console.cloud.google.com/welcome/new',
  geminiWebActivation: 'https://gemini.google.com/gems/create?hl=en-US&pli=1',
  countryCheck: 'https://policies.google.com/terms',
  countryChange: 'https://policies.google.com/country-association-form'
}

// Computed: current preset mappings based on platform
const modelSelectionPlatform = computed(() =>
  form.platform === 'kiro' && kiroAuthType.value === 'apikey' ? 'kiro-apikey' : form.platform
)
const presetMappings = computed(() => getPresetMappingsByPlatform(modelSelectionPlatform.value))
const tempUnschedPresets = computed(() => [
  {
    label: t('admin.accounts.tempUnschedulable.presets.overloadLabel'),
    rule: {
      error_code: 529,
      keywords: 'overloaded, too many',
      duration_minutes: 60,
      description: t('admin.accounts.tempUnschedulable.presets.overloadDesc')
    }
  },
  {
    label: t('admin.accounts.tempUnschedulable.presets.rateLimitLabel'),
    rule: {
      error_code: 429,
      keywords: 'rate limit, too many requests',
      duration_minutes: 10,
      description: t('admin.accounts.tempUnschedulable.presets.rateLimitDesc')
    }
  },
  {
    label: t('admin.accounts.tempUnschedulable.presets.unavailableLabel'),
    rule: {
      error_code: 503,
      keywords: 'unavailable, maintenance',
      duration_minutes: 30,
      description: t('admin.accounts.tempUnschedulable.presets.unavailableDesc')
    }
  }
])

const form = reactive({
  name: '',
  notes: '',
  platform: 'anthropic' as AccountPlatform,
  type: 'oauth' as AccountType, // Will be 'oauth', 'setup-token', or 'apikey'
  credentials: {} as Record<string, unknown>,
  proxy_id: null as number | null,
  concurrency: 10,
  priority: 1,
  rate_multiplier: 1,
  group_ids: [] as number[],
  expires_at: null as number | null
})

// Helper to check if current type needs OAuth flow (Kiro uses direct refresh token, not OAuth flow; OpenAI batch mode skips OAuth)
const isOAuthFlow = computed(() => accountCategory.value === 'oauth-based' && form.platform !== 'kiro' && !(form.platform === 'openai' && openaiInputMode.value === 'batch'))

const isManualInputMethod = computed(() => {
  return oauthFlowRef.value?.inputMethod === 'manual'
})

const expiresAtInput = computed({
  get: () => formatDateTimeLocal(form.expires_at),
  set: (value: string) => {
    form.expires_at = parseDateTimeLocal(value)
  }
})

const canExchangeCode = computed(() => {
  const authCode = oauthFlowRef.value?.authCode || ''
  if (form.platform === 'openai') {
    return authCode.trim() && openaiOAuth.sessionId.value && !openaiOAuth.loading.value
  }
  if (form.platform === 'gemini') {
    return authCode.trim() && geminiOAuth.sessionId.value && !geminiOAuth.loading.value
  }
  if (form.platform === 'antigravity') {
    return authCode.trim() && antigravityOAuth.sessionId.value && !antigravityOAuth.loading.value
  }
  return authCode.trim() && oauth.sessionId.value && !oauth.loading.value
})

// Watchers
watch(
  () => props.show,
  (newVal) => {
    if (newVal) {
      // Modal opened - set default platform if provided
      if (props.defaultPlatform) {
        form.platform = props.defaultPlatform
        // Reset account category based on platform
        if (props.defaultPlatform === 'antigravity' || props.defaultPlatform === 'kiro') {
          accountCategory.value = 'oauth-based'
        }
      }
      // Fill related models
      allowedModels.value = [...getModelsByPlatform(form.platform)]
    } else {
      resetForm()
    }
  }
)

// Sync form.type based on accountCategory and addMethod
watch(
  [accountCategory, addMethod],
  ([category, method]) => {
    if (category === 'oauth-based') {
      form.type = method as AccountType // 'oauth' or 'setup-token'
    } else {
      form.type = 'apikey'
    }
  },
  { immediate: true }
)

// Reset platform-specific settings when platform changes
watch(
  () => form.platform,
  (newPlatform) => {
    // Reset base URL based on platform
    apiKeyBaseUrl.value =
      newPlatform === 'openai'
        ? 'https://api.openai.com'
        : newPlatform === 'gemini'
          ? 'https://generativelanguage.googleapis.com'
          : 'https://api.anthropic.com'
    // Clear model-related settings
    allowedModels.value = []
    modelMappings.value = []
    // Reset Anthropic-specific settings when switching to other platforms
    if (newPlatform !== 'anthropic') {
      interceptWarmupRequests.value = false
    }
    // Antigravity and Kiro only support OAuth-based
    if (newPlatform === 'antigravity' || newPlatform === 'kiro') {
      accountCategory.value = 'oauth-based'
    }
    // Reset Kiro refresh token when switching platforms
    if (newPlatform !== 'kiro') {
      kiroRefreshToken.value = ''
      kiroAuthType.value = 'social'
      kiroInputMode.value = 'single'
      kiroClientId.value = ''
      kiroClientSecret.value = ''
      kiroApiKeyValue.value = ''
      kiroBaseUrl.value = ''
      kiroBatchJson.value = ''
      kiroParsedTokens.value = []
      kiroParseError.value = ''
      kiroUploadedFiles.value = []
    }
    // Reset OpenAI batch import when switching platforms
    if (newPlatform !== 'openai') {
      openaiInputMode.value = 'single'
      openaiBatchJson.value = ''
      openaiParsedAccounts.value = []
      openaiParseError.value = ''
      openaiUploadedFiles.value = []
      existingOpenaiEmails.value = new Set()
      openaiEmailFilterLoading.value = false
    }
    // Reset OAuth states
    oauth.resetState()
    openaiOAuth.resetState()
    geminiOAuth.resetState()
    antigravityOAuth.resetState()
  }
)

// Gemini AI Studio OAuth availability (requires operator-configured OAuth client)
watch(
  [() => props.show, () => form.platform, accountCategory],
  async ([show, platform, category]) => {
    if (!show || platform !== 'gemini' || category !== 'oauth-based') {
      geminiAIStudioOAuthEnabled.value = false
      return
    }
    const caps = await geminiOAuth.getCapabilities()
    geminiAIStudioOAuthEnabled.value = !!caps?.ai_studio_oauth_enabled
    if (!geminiAIStudioOAuthEnabled.value && geminiOAuthType.value === 'ai_studio') {
      geminiOAuthType.value = 'code_assist'
    }
  },
  { immediate: true }
)

// Load existing OpenAI emails when switching to batch mode
watch(openaiInputMode, (mode) => {
  if (mode === 'batch') {
    loadExistingOpenaiEmails()
  }
})

const handleSelectGeminiOAuthType = (oauthType: 'code_assist' | 'google_one' | 'ai_studio') => {
  if (oauthType === 'ai_studio' && !geminiAIStudioOAuthEnabled.value) {
    appStore.showError(t('admin.accounts.oauth.gemini.aiStudioNotConfigured'))
    return
  }
  geminiOAuthType.value = oauthType
}

// Auto-fill related models when switching to whitelist mode or changing platform
watch(
  [modelRestrictionMode, modelSelectionPlatform],
  ([newMode]) => {
    if (newMode === 'whitelist') {
      allowedModels.value = [...getModelsByPlatform(modelSelectionPlatform.value)]
    }
  }
)

// Model mapping helpers
const addModelMapping = () => {
  modelMappings.value.push({ from: '', to: '' })
}

const removeModelMapping = (index: number) => {
  modelMappings.value.splice(index, 1)
}

const addPresetMapping = (from: string, to: string) => {
  if (modelMappings.value.some((m) => m.from === from)) {
    appStore.showInfo(t('admin.accounts.mappingExists', { model: from }))
    return
  }
  modelMappings.value.push({ from, to })
}

// Error code toggle helper
const toggleErrorCode = (code: number) => {
  const index = selectedErrorCodes.value.indexOf(code)
  if (index === -1) {
    // Adding code - check for 429/529 warning
    if (code === 429) {
      if (!confirm(t('admin.accounts.customErrorCodes429Warning'))) {
        return
      }
    } else if (code === 529) {
      if (!confirm(t('admin.accounts.customErrorCodes529Warning'))) {
        return
      }
    }
    selectedErrorCodes.value.push(code)
  } else {
    selectedErrorCodes.value.splice(index, 1)
  }
}

// Add custom error code from input
const addCustomErrorCode = () => {
  const code = customErrorCodeInput.value
  if (code === null || code < 100 || code > 599) {
    appStore.showError(t('admin.accounts.invalidErrorCode'))
    return
  }
  if (selectedErrorCodes.value.includes(code)) {
    appStore.showInfo(t('admin.accounts.errorCodeExists'))
    return
  }
  // Check for 429/529 warning
  if (code === 429) {
    if (!confirm(t('admin.accounts.customErrorCodes429Warning'))) {
      return
    }
  } else if (code === 529) {
    if (!confirm(t('admin.accounts.customErrorCodes529Warning'))) {
      return
    }
  }
  selectedErrorCodes.value.push(code)
  customErrorCodeInput.value = null
}

// Remove error code
const removeErrorCode = (code: number) => {
  const index = selectedErrorCodes.value.indexOf(code)
  if (index !== -1) {
    selectedErrorCodes.value.splice(index, 1)
  }
}

const addTempUnschedRule = (preset?: TempUnschedRuleForm) => {
  if (preset) {
    tempUnschedRules.value.push({ ...preset })
    return
  }
  tempUnschedRules.value.push({
    error_code: null,
    keywords: '',
    duration_minutes: 30,
    description: ''
  })
}

const removeTempUnschedRule = (index: number) => {
  tempUnschedRules.value.splice(index, 1)
}

const moveTempUnschedRule = (index: number, direction: number) => {
  const target = index + direction
  if (target < 0 || target >= tempUnschedRules.value.length) return
  const rules = tempUnschedRules.value
  const current = rules[index]
  rules[index] = rules[target]
  rules[target] = current
}

const buildTempUnschedRules = (rules: TempUnschedRuleForm[]) => {
  const out: Array<{
    error_code: number
    keywords: string[]
    duration_minutes: number
    description: string
  }> = []

  for (const rule of rules) {
    const errorCode = Number(rule.error_code)
    const duration = Number(rule.duration_minutes)
    const keywords = splitTempUnschedKeywords(rule.keywords)
    if (!Number.isFinite(errorCode) || errorCode < 100 || errorCode > 599) {
      continue
    }
    if (!Number.isFinite(duration) || duration <= 0) {
      continue
    }
    if (keywords.length === 0) {
      continue
    }
    out.push({
      error_code: Math.trunc(errorCode),
      keywords,
      duration_minutes: Math.trunc(duration),
      description: rule.description.trim()
    })
  }

  return out
}

const applyTempUnschedConfig = (credentials: Record<string, unknown>) => {
  if (!tempUnschedEnabled.value) {
    delete credentials.temp_unschedulable_enabled
    delete credentials.temp_unschedulable_rules
    return true
  }

  const rules = buildTempUnschedRules(tempUnschedRules.value)
  if (rules.length === 0) {
    appStore.showError(t('admin.accounts.tempUnschedulable.rulesInvalid'))
    return false
  }

  credentials.temp_unschedulable_enabled = true
  credentials.temp_unschedulable_rules = rules
  return true
}

const splitTempUnschedKeywords = (value: string) => {
  return value
    .split(/[,;]/)
    .map((item) => item.trim())
    .filter((item) => item.length > 0)
}

// Methods
const resetForm = () => {
  step.value = 1
  form.name = ''
  form.notes = ''
  form.platform = 'anthropic'
  form.type = 'oauth'
  form.credentials = {}
  form.proxy_id = null
  form.concurrency = 10
  form.priority = 1
  form.rate_multiplier = 1
  form.group_ids = []
  form.expires_at = null
  accountCategory.value = 'oauth-based'
  addMethod.value = 'oauth'
  apiKeyBaseUrl.value = 'https://api.anthropic.com'
  apiKeyValue.value = ''
  modelMappings.value = []
  modelRestrictionMode.value = 'whitelist'
  allowedModels.value = [...claudeModels] // Default fill related models
  customErrorCodesEnabled.value = false
  selectedErrorCodes.value = []
  customErrorCodeInput.value = null
  interceptWarmupRequests.value = false
  autoPauseOnExpired.value = true
  kiroRefreshToken.value = ''
  kiroAuthType.value = 'social'
  kiroInputMode.value = 'single'
  kiroClientId.value = ''
  kiroClientSecret.value = ''
  kiroProfileArn.value = ''
  kiroApiKeyValue.value = ''
  kiroBaseUrl.value = ''
  kiroBatchJson.value = ''
  kiroParsedTokens.value = []
  kiroParseError.value = ''
  kiroUploadedFiles.value = []
  openaiInputMode.value = 'single'
  openaiBatchJson.value = ''
  openaiParsedAccounts.value = []
  openaiParseError.value = ''
  openaiUploadedFiles.value = []
  existingOpenaiEmails.value = new Set()
  openaiEmailFilterLoading.value = false
  tempUnschedEnabled.value = false
  tempUnschedRules.value = []
  geminiOAuthType.value = 'code_assist'
  geminiTierGoogleOne.value = 'google_one_free'
  geminiTierGcp.value = 'gcp_standard'
  geminiTierAIStudio.value = 'aistudio_free'
  oauth.resetState()
  openaiOAuth.resetState()
  geminiOAuth.resetState()
  antigravityOAuth.resetState()
  oauthFlowRef.value?.reset()
}

const handleClose = () => {
  emit('close')
}

// Kiro file handling methods
const handleKiroFileDrop = (e: DragEvent) => {
  kiroIsDragging.value = false
  const files = e.dataTransfer?.files
  if (files && files.length > 0) {
    for (let i = 0; i < files.length; i++) {
      readKiroJsonFile(files[i])
    }
  }
}

const handleKiroFileSelect = (e: Event) => {
  const input = e.target as HTMLInputElement
  const files = input.files
  if (files && files.length > 0) {
    for (let i = 0; i < files.length; i++) {
      readKiroJsonFile(files[i])
    }
  }
  input.value = ''
}

const KIRO_BUILDER_ID_PROFILE_ARN = 'arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX'

const isBuilderIdProvider = (provider?: string): boolean => {
  if (typeof provider !== 'string') return false
  return provider.trim().toLowerCase() === 'builderid'
}

// resolveKiroIdcProfileArn picks the profile_arn used when an account is added
// through the IdC entrypoint. Builder ID accounts always map to the fixed Kiro
// hardcoded ARN; otherwise we honor the per-token value, then the user-supplied
// default, finally falling back to the Builder ID ARN so the request never goes
// out empty (downstream APIs reject empty profile_arn for IdC accounts).
const resolveKiroIdcProfileArn = (
  tokenProfileArn: string | undefined,
  tokenProvider: string | undefined,
  defaultProfileArn: string | undefined
): string => {
  if (isBuilderIdProvider(tokenProvider)) return KIRO_BUILDER_ID_PROFILE_ARN
  if (tokenProfileArn) return tokenProfileArn
  if (defaultProfileArn) return defaultProfileArn
  return KIRO_BUILDER_ID_PROFILE_ARN
}

const normalizeKiroProfileArn = (value: unknown): string | undefined => {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  if (!trimmed.startsWith('arn:aws:codewhisperer:')) return undefined
  return trimmed
}

const normalizeKiroRefreshTokenLine = (value: string): string => {
  const trimmed = value.trim()
  const withoutComma = trimmed.endsWith(',') ? trimmed.slice(0, -1).trim() : trimmed
  if (
    (withoutComma.startsWith('"') && withoutComma.endsWith('"')) ||
    (withoutComma.startsWith("'") && withoutComma.endsWith("'"))
  ) {
    return withoutComma.slice(1, -1).trim()
  }
  return withoutComma
}

const appendUniqueKiroToken = (
  tokens: KiroParsedToken[],
  existingTokens: Set<string>,
  refreshToken: unknown,
  item?: Record<string, any>
): boolean => {
  if (typeof refreshToken !== 'string') return false
  const rt = refreshToken.trim()
  if (!rt || existingTokens.has(rt)) return false

  existingTokens.add(rt)
  tokens.push({
    refreshToken: rt,
    clientId: item?.clientId || item?.client_id,
    clientSecret: item?.clientSecret || item?.client_secret,
    profileArn: normalizeKiroProfileArn(item?.profileArn || item?.profile_arn || item?.arn),
    provider: typeof item?.provider === 'string'
      ? item.provider
      : typeof item?.Provider === 'string'
        ? item.Provider
        : undefined,
    name: item?.name,
    region: item?.region
  })
  return true
}

const parseKiroJsonTokens = (jsonText: string, existingTokens: Set<string>): { tokens: KiroParsedToken[]; skippedCount: number } => {
  const data = JSON.parse(jsonText)
  const items = Array.isArray(data) ? data : Array.isArray(data?.tokens) ? data.tokens : [data]
  const tokens: KiroParsedToken[] = []
  let skippedCount = 0

  for (const item of items) {
    const rt = typeof item === 'string'
      ? item
      : item?.refreshToken || item?.refresh_token || item?.RefreshToken
    const added = appendUniqueKiroToken(
      tokens,
      existingTokens,
      rt,
      typeof item === 'object' && item !== null && !Array.isArray(item) ? item : undefined
    )
    if (!added && typeof rt === 'string' && rt.trim()) {
      skippedCount++
    }
  }

  return { tokens, skippedCount }
}

const parseKiroRefreshTokenLines = (text: string, existingTokens: Set<string>): { tokens: KiroParsedToken[]; skippedCount: number } => {
  const tokens: KiroParsedToken[] = []
  let skippedCount = 0

  const lines = text
    .split(/\r?\n/)
    .map(normalizeKiroRefreshTokenLine)
    .filter(Boolean)

  for (const line of lines) {
    if (!appendUniqueKiroToken(tokens, existingTokens, line)) {
      skippedCount++
    }
  }

  return { tokens, skippedCount }
}

const readKiroJsonFile = (file: File) => {
  if (!file.name.endsWith('.json')) {
    kiroParseError.value = t('admin.accounts.kiro.pleaseSelectJson')
    return
  }
  const reader = new FileReader()
  reader.onload = (e) => {
    const jsonText = (e.target?.result as string) || ''
    parseKiroJsonFileContent(file.name, jsonText)
  }
  reader.onerror = () => {
    kiroParseError.value = t('admin.accounts.kiro.fileReadError')
  }
  reader.readAsText(file)
}

const parseKiroJsonFileContent = (fileName: string, jsonText: string) => {
  kiroParseError.value = ''
  if (!jsonText.trim()) {
    kiroParseError.value = t('admin.accounts.kiro.jsonParseError')
    return
  }

  try {
    // Get existing refresh tokens for deduplication
    const existingTokens = new Set(kiroParsedTokens.value.map(t => t.refreshToken))
    const { tokens, skippedCount } = parseKiroJsonTokens(jsonText, existingTokens)

    if (tokens.length === 0 && skippedCount === 0) {
      kiroParseError.value = t('admin.accounts.kiro.noValidTokens')
      return
    }

    if (tokens.length === 0 && skippedCount > 0) {
      kiroParseError.value = t('admin.accounts.kiro.allTokensDuplicate', { count: skippedCount })
      return
    }

    kiroUploadedFiles.value.push({
      name: fileName,
      tokenCount: tokens.length,
      tokens
    })
    aggregateKiroParsedTokens()

    if (skippedCount > 0) {
      kiroParseError.value = t('admin.accounts.kiro.someTokensDuplicate', { added: tokens.length, skipped: skippedCount })
    }
  } catch {
    kiroParseError.value = t('admin.accounts.kiro.jsonParseError')
  }
}

const removeKiroUploadedFile = (index: number) => {
  kiroUploadedFiles.value.splice(index, 1)
  aggregateKiroParsedTokens()
}

const aggregateKiroParsedTokens = () => {
  kiroParsedTokens.value = kiroUploadedFiles.value.flatMap(file => file.tokens)
}

const parseKiroBatchJson = () => {
  kiroParseError.value = ''

  const jsonText = kiroBatchJson.value.trim()
  if (!jsonText) {
    kiroParseError.value = t('admin.accounts.kiro.jsonParseError')
    return
  }

  try {
    // Get existing refresh tokens from uploaded files for deduplication
    const fileTokens = kiroUploadedFiles.value.flatMap(file => file.tokens)
    const existingTokens = new Set(fileTokens.map(t => t.refreshToken))
    const looksLikeJson = jsonText.startsWith('{') || jsonText.startsWith('[')
    const { tokens: newTokens, skippedCount } = looksLikeJson
      ? parseKiroJsonTokens(jsonText, existingTokens)
      : kiroAuthType.value === 'social'
        ? parseKiroRefreshTokenLines(jsonText, existingTokens)
        : parseKiroJsonTokens(jsonText, existingTokens)

    if (newTokens.length === 0 && skippedCount === 0) {
      kiroParseError.value = t('admin.accounts.kiro.noValidTokens')
      return
    }

    if (newTokens.length === 0 && skippedCount > 0) {
      kiroParseError.value = t('admin.accounts.kiro.allTokensDuplicate', { count: skippedCount })
      return
    }

    // Merge with tokens from uploaded files
    kiroParsedTokens.value = [...fileTokens, ...newTokens]

    if (skippedCount > 0) {
      kiroParseError.value = t('admin.accounts.kiro.someTokensDuplicate', { added: newTokens.length, skipped: skippedCount })
    }
  } catch {
    kiroParseError.value = t('admin.accounts.kiro.jsonParseError')
  }
}

// ===== OpenAI Batch Import Functions =====

const handleOpenaiFileDrop = (e: DragEvent) => {
  openaiIsDragging.value = false
  if (openaiEmailFilterLoading.value) return
  const files = e.dataTransfer?.files
  if (files) {
    for (const file of Array.from(files)) {
      if (file.name.endsWith('.json') || file.name.endsWith('.jsonl')) {
        readOpenaiJsonFile(file)
      }
    }
  }
}

const handleOpenaiFileSelect = (e: Event) => {
  if (openaiEmailFilterLoading.value) return
  const input = e.target as HTMLInputElement
  if (input.files) {
    for (const file of Array.from(input.files)) {
      readOpenaiJsonFile(file)
    }
    input.value = '' // Reset for re-selection
  }
}

const readOpenaiJsonFile = (file: File) => {
  const reader = new FileReader()
  reader.onload = (e) => {
    const content = e.target?.result as string
    parseOpenaiJsonFileContent(file.name, content)
  }
  reader.onerror = () => {
    openaiParseError.value = t('admin.accounts.openaiImport.fileReadError')
  }
  reader.readAsText(file)
}

const loadExistingOpenaiEmails = async () => {
  existingOpenaiEmails.value = new Set()
  openaiEmailFilterLoading.value = true
  try {
    // Fetch all openai accounts (use large page size to get all)
    const res = await adminAPI.accounts.list(1, 9999, { platform: 'openai' })
    const emails = new Set<string>()
    for (const account of res.items) {
      const email = (account.credentials as Record<string, unknown>)?.email
      if (email && typeof email === 'string') {
        emails.add(email.toLowerCase())
      }
    }
    existingOpenaiEmails.value = emails
  } catch {
    // Silently fail - filter just won't work
  } finally {
    openaiEmailFilterLoading.value = false
  }
}

const parseOpenaiJsonFileContent = (fileName: string, jsonText: string) => {
  openaiParseError.value = ''
  if (!jsonText.trim()) {
    openaiParseError.value = t('admin.accounts.openaiImport.jsonParseError')
    return
  }

  // Try JSON array first, then JSONL (one JSON object per line)
  let items: any[]
  try {
    const data = JSON.parse(jsonText)
    items = Array.isArray(data) ? data : [data]
  } catch {
    // Try JSONL: each line is a separate JSON object
    items = []
    for (const line of jsonText.split('\n')) {
      const trimmed = line.trim()
      if (!trimmed) continue
      try {
        items.push(JSON.parse(trimmed))
      } catch {
        // skip invalid lines
      }
    }
    if (items.length === 0) {
      openaiParseError.value = t('admin.accounts.openaiImport.jsonParseError')
      return
    }
  }

  const newAccounts: Array<{ email?: string; password?: string; accessToken: string; refreshToken: string }> = []

  // Dedup against existing parsed accounts and uploaded files
  const fileAccounts = openaiUploadedFiles.value.flatMap(f => f.accounts)
  const existingTokens = new Set([
    ...fileAccounts.map(a => a.refreshToken),
    ...openaiParsedAccounts.value.map(a => a.refreshToken)
  ])
  let skippedCount = 0
  let emailFilteredCount = 0

  for (const item of items) {
    const at = item?.access_token || item?.accessToken
    const rt = item?.refresh_token || item?.refreshToken
    if (at && rt && typeof at === 'string' && typeof rt === 'string') {
      if (existingTokens.has(rt)) {
        skippedCount++
        continue
      }
      // Filter by email: skip accounts whose email already exists in the database
      const email = item?.email as string | undefined
      if (email && existingOpenaiEmails.value.has(email.toLowerCase())) {
        emailFilteredCount++
        continue
      }
      existingTokens.add(rt)
      newAccounts.push({
        email,
        password: item?.password,
        accessToken: at,
        refreshToken: rt,
      })
    }
  }

  const totalSkipped = skippedCount + emailFilteredCount

  if (newAccounts.length === 0 && totalSkipped === 0) {
    openaiParseError.value = t('admin.accounts.openaiImport.noValidAccounts')
    return
  }

  if (newAccounts.length > 0) {
    openaiUploadedFiles.value.push({
      name: fileName,
      accountCount: newAccounts.length,
      accounts: newAccounts,
    })
  }

  // Build feedback message
  const messages: string[] = []
  if (skippedCount > 0) {
    messages.push(t('admin.accounts.openaiImport.someAccountsDuplicate', { added: newAccounts.length, skipped: skippedCount }))
  }
  if (emailFilteredCount > 0) {
    messages.push(t('admin.accounts.openaiImport.emailFilteredCount', { count: emailFilteredCount }))
  }

  if (newAccounts.length === 0 && totalSkipped > 0) {
    openaiParseError.value = messages.length > 0 ? messages.join('；') : t('admin.accounts.openaiImport.allAccountsDuplicate', { count: totalSkipped })
  } else if (messages.length > 0) {
    openaiParseError.value = messages.join('；')
  }

  // Update combined parsed accounts
  refreshOpenaiParsedAccounts()
}

const removeOpenaiUploadedFile = (index: number) => {
  openaiUploadedFiles.value.splice(index, 1)
  refreshOpenaiParsedAccounts()
}

const refreshOpenaiParsedAccounts = () => {
  const all = openaiUploadedFiles.value.flatMap(f => f.accounts)
  // Dedup by refresh_token
  const seen = new Set<string>()
  openaiParsedAccounts.value = all.filter(a => {
    if (seen.has(a.refreshToken)) return false
    seen.add(a.refreshToken)
    return true
  })
}

const parseOpenaiBatchJson = () => {
  if (openaiEmailFilterLoading.value) return
  openaiParseError.value = ''

  const jsonText = openaiBatchJson.value.trim()
  if (!jsonText && openaiUploadedFiles.value.length > 0) {
    // Already have uploaded files, just refresh
    refreshOpenaiParsedAccounts()
    return
  }
  if (!jsonText) {
    openaiParseError.value = t('admin.accounts.openaiImport.jsonParseError')
    return
  }

  parseOpenaiJsonFileContent('pasted-json', jsonText)
}

const handleSubmit = async () => {
  // For OAuth-based type, handle OAuth flow (goes to step 2)
  if (isOAuthFlow.value) {
    if (!form.name.trim()) {
      appStore.showError(t('admin.accounts.pleaseEnterAccountName'))
      return
    }
    step.value = 2
    return
  }

  // For OpenAI platform batch import
  if (form.platform === 'openai' && accountCategory.value === 'oauth-based' && openaiInputMode.value === 'batch') {
    if (openaiParsedAccounts.value.length === 0) {
      appStore.showError(t('admin.accounts.openaiImport.pleaseParseFirst'))
      return
    }

    const getDefaultBatchPrefix = () => {
      const now = new Date()
      const year = now.getFullYear()
      const month = String(now.getMonth() + 1).padStart(2, '0')
      const day = String(now.getDate()).padStart(2, '0')
      return `openai_${year}${month}${day}`
    }
    const batchPrefix = form.name.trim() || getDefaultBatchPrefix()
    const totalCount = openaiParsedAccounts.value.length

    submitting.value = true
    let successCount = 0
    let failCount = 0

    try {
      for (let i = 0; i < totalCount; i++) {
        const acct = openaiParsedAccounts.value[i]
        const credentials: Record<string, unknown> = {
          access_token: acct.accessToken,
          refresh_token: acct.refreshToken,
        }
        if (acct.email) {
          credentials.email = acct.email
        }
        if (acct.password) {
          credentials.password = acct.password
        }

        const accountName = acct.email
          ? `${batchPrefix}_${acct.email}`
          : `${batchPrefix}_${i + 1}`

        try {
          await adminAPI.accounts.create({
            name: accountName,
            notes: form.notes,
            platform: 'openai',
            type: 'oauth',
            credentials,
            proxy_id: form.proxy_id,
            concurrency: form.concurrency,
            priority: form.priority,
            rate_multiplier: form.rate_multiplier,
            group_ids: form.group_ids,
            expires_at: form.expires_at,
            auto_pause_on_expired: autoPauseOnExpired.value
          })
          successCount++
        } catch {
          failCount++
        }
      }

      if (failCount === 0) {
        appStore.showSuccess(t('admin.accounts.openaiImport.batchImportSuccess', { count: successCount }))
      } else {
        appStore.showWarning(t('admin.accounts.openaiImport.batchImportPartial', { success: successCount, fail: failCount }))
      }
      emit('created')
      handleClose()
    } finally {
      submitting.value = false
    }
    return
  }

  // For Kiro platform, create account with refresh token directly
  if (form.platform === 'kiro') {
    if (!form.name.trim() && kiroInputMode.value === 'single') {
      appStore.showError(t('admin.accounts.pleaseEnterAccountName'))
      return
    }

    // API Key mode
    if (kiroAuthType.value === 'apikey') {
      if (!form.name.trim()) {
        appStore.showError(t('admin.accounts.pleaseEnterAccountName'))
        return
      }
      if (!kiroBaseUrl.value.trim() || !kiroApiKeyValue.value.trim()) {
        appStore.showError(t('admin.accounts.kiro.pleaseEnterApikeyCredentials'))
        return
      }

      const credentials: Record<string, unknown> = {
        auth_type: 'apikey',
        base_url: kiroBaseUrl.value.trim(),
        api_key: kiroApiKeyValue.value.trim()
      }
      const modelMapping = buildModelMappingObject(modelRestrictionMode.value, allowedModels.value, modelMappings.value)
      if (modelMapping) {
        credentials.model_mapping = modelMapping
      }

      submitting.value = true
      try {
        await adminAPI.accounts.create({
          name: form.name,
          notes: form.notes,
          platform: 'kiro',
          type: 'oauth',
          credentials,
          proxy_id: form.proxy_id,
          concurrency: form.concurrency,
          priority: form.priority,
          rate_multiplier: form.rate_multiplier,
          group_ids: form.group_ids,
          expires_at: form.expires_at,
          auto_pause_on_expired: autoPauseOnExpired.value
        })
        appStore.showSuccess(t('admin.accounts.accountCreated'))
        emit('created')
        handleClose()
      } catch (error: any) {
        appStore.showError(error.response?.data?.detail || t('admin.accounts.failedToCreate'))
      } finally {
        submitting.value = false
      }
      return
    }

    // Batch import mode
    if (kiroInputMode.value === 'batch') {
      if (kiroParsedTokens.value.length === 0) {
        appStore.showError(t('admin.accounts.kiro.pleaseParseFirst'))
        return
      }
      // IdC profile_arn is no longer required up-front: resolveKiroIdcProfileArn
      // falls back to the Builder ID default ARN when the token JSON has no
      // valid profileArn and the user did not supply a default. Builder ID /
      // Github / Google providers always map to the hardcoded official ARN.
      const defaultProfileArn = normalizeKiroProfileArn(kiroProfileArn.value)

      // Batch name prefix (default to 'kiro_{date}' if not provided)
      const getDefaultBatchPrefix = () => {
        const now = new Date()
        const year = now.getFullYear()
        const month = String(now.getMonth() + 1).padStart(2, '0')
        const day = String(now.getDate()).padStart(2, '0')
        return `kiro_${year}${month}${day}`
      }
      const batchPrefix = form.name.trim() || getDefaultBatchPrefix()
      const totalCount = kiroParsedTokens.value.length
      const now = new Date()
      const dateStr = `${now.getFullYear()}${String(now.getMonth() + 1).padStart(2, '0')}${String(now.getDate()).padStart(2, '0')}`

      submitting.value = true
      let successCount = 0
      let failCount = 0

      try {
        for (let i = 0; i < totalCount; i++) {
          const token = kiroParsedTokens.value[i]
          const credentials: Record<string, unknown> = {
            auth_type: kiroAuthType.value,
            refresh_token: token.refreshToken
          }

          // Add IdC credentials if applicable
          if (kiroAuthType.value === 'idc') {
            credentials.client_id = token.clientId || kiroClientId.value.trim()
            credentials.client_secret = token.clientSecret || kiroClientSecret.value.trim()
            credentials.profile_arn = resolveKiroIdcProfileArn(token.profileArn, token.provider, defaultProfileArn)
          }

          // Add region if specified in token data
          if (token.region) {
            credentials.region = token.region
          }

          // Generate account name: use token.name_date if name exists, otherwise batchPrefix_index
          const accountName = token.name
            ? `${token.name}_${dateStr}`
            : `${batchPrefix}_${i + 1}`

          try {
            await adminAPI.accounts.create({
              name: accountName,
              notes: form.notes,
              platform: 'kiro',
              type: 'oauth',
              credentials,
              proxy_id: form.proxy_id,
              concurrency: form.concurrency,
              priority: form.priority,
              rate_multiplier: form.rate_multiplier,
              group_ids: form.group_ids,
              expires_at: form.expires_at,
              auto_pause_on_expired: autoPauseOnExpired.value
            })
            successCount++
          } catch {
            failCount++
          }
        }

        if (failCount === 0) {
          appStore.showSuccess(t('admin.accounts.kiro.batchImportSuccess', { count: successCount }))
        } else {
          appStore.showWarning(t('admin.accounts.kiro.batchImportPartial', { success: successCount, fail: failCount }))
        }
        emit('created')
        handleClose()
      } finally {
        submitting.value = false
      }
      return
    }

    // Single token mode
    if (!kiroRefreshToken.value.trim()) {
      appStore.showError(t('admin.accounts.kiro.pleaseEnterRefreshToken'))
      return
    }

    // Validate IdC fields
    if (kiroAuthType.value === 'idc') {
      if (!kiroClientId.value.trim() || !kiroClientSecret.value.trim()) {
        appStore.showError(t('admin.accounts.kiro.pleaseEnterIdcCredentials'))
        return
      }
      // profile_arn is optional now: an empty value falls back to the Builder ID
      // default ARN inside resolveKiroIdcProfileArn (and the backend re-applies
      // the same fallback). Only client_id/client_secret remain mandatory.
    }

    const credentials: Record<string, unknown> = {
      auth_type: kiroAuthType.value,
      refresh_token: kiroRefreshToken.value.trim()
    }

    // Add IdC credentials if applicable
    if (kiroAuthType.value === 'idc') {
      credentials.client_id = kiroClientId.value.trim()
      credentials.client_secret = kiroClientSecret.value.trim()
      credentials.profile_arn = resolveKiroIdcProfileArn(undefined, undefined, normalizeKiroProfileArn(kiroProfileArn.value))
    }

    submitting.value = true
    try {
      await adminAPI.accounts.create({
        name: form.name,
        notes: form.notes,
        platform: 'kiro',
        type: 'oauth',
        credentials,
        proxy_id: form.proxy_id,
        concurrency: form.concurrency,
        priority: form.priority,
        rate_multiplier: form.rate_multiplier,
        group_ids: form.group_ids,
        expires_at: form.expires_at,
        auto_pause_on_expired: autoPauseOnExpired.value
      })
      appStore.showSuccess(t('admin.accounts.accountCreated'))
      emit('created')
      handleClose()
    } catch (error: any) {
      appStore.showError(error.response?.data?.detail || t('admin.accounts.failedToCreate'))
    } finally {
      submitting.value = false
    }
    return
  }

  // For apikey type, create directly
  if (!apiKeyValue.value.trim()) {
    appStore.showError(t('admin.accounts.pleaseEnterApiKey'))
    return
  }

  // Determine default base URL based on platform
  const defaultBaseUrl =
    form.platform === 'openai'
      ? 'https://api.openai.com'
      : form.platform === 'gemini'
        ? 'https://generativelanguage.googleapis.com'
        : 'https://api.anthropic.com'

  // Build credentials with optional model mapping
  const credentials: Record<string, unknown> = {
    base_url: apiKeyBaseUrl.value.trim() || defaultBaseUrl,
    api_key: apiKeyValue.value.trim()
  }
  if (form.platform === 'gemini') {
    credentials.tier_id = geminiTierAIStudio.value
  }

  // Add model mapping if configured
  const modelMapping = buildModelMappingObject(modelRestrictionMode.value, allowedModels.value, modelMappings.value)
  if (modelMapping) {
    credentials.model_mapping = modelMapping
  }

  // Add custom error codes if enabled
  if (customErrorCodesEnabled.value) {
    credentials.custom_error_codes_enabled = true
    credentials.custom_error_codes = [...selectedErrorCodes.value]
  }

  // Add intercept warmup requests setting
  if (interceptWarmupRequests.value) {
    credentials.intercept_warmup_requests = true
  }
  if (!applyTempUnschedConfig(credentials)) {
    return
  }

  form.credentials = credentials

  submitting.value = true
  try {
    await adminAPI.accounts.create({
      ...form,
      group_ids: form.group_ids,
      auto_pause_on_expired: autoPauseOnExpired.value
    })
    appStore.showSuccess(t('admin.accounts.accountCreated'))
    emit('created')
    handleClose()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.accounts.failedToCreate'))
  } finally {
    submitting.value = false
  }
}

const goBackToBasicInfo = () => {
  step.value = 1
  oauth.resetState()
  openaiOAuth.resetState()
  geminiOAuth.resetState()
  antigravityOAuth.resetState()
  oauthFlowRef.value?.reset()
}

const handleGenerateUrl = async () => {
  if (form.platform === 'openai') {
    await openaiOAuth.generateAuthUrl(form.proxy_id)
  } else if (form.platform === 'gemini') {
    await geminiOAuth.generateAuthUrl(
      form.proxy_id,
      oauthFlowRef.value?.projectId,
      geminiOAuthType.value,
      geminiSelectedTier.value
    )
  } else if (form.platform === 'antigravity') {
    await antigravityOAuth.generateAuthUrl(form.proxy_id)
  } else {
    await oauth.generateAuthUrl(addMethod.value, form.proxy_id)
  }
}

const formatDateTimeLocal = formatDateTimeLocalInput
const parseDateTimeLocal = parseDateTimeLocalInput

// Create account and handle success/failure
const createAccountAndFinish = async (
  platform: AccountPlatform,
  type: AccountType,
  credentials: Record<string, unknown>,
  extra?: Record<string, unknown>
) => {
  if (!applyTempUnschedConfig(credentials)) {
    return
  }
  await adminAPI.accounts.create({
    name: form.name,
    notes: form.notes,
    platform,
    type,
    credentials,
    extra,
    proxy_id: form.proxy_id,
    concurrency: form.concurrency,
    priority: form.priority,
    rate_multiplier: form.rate_multiplier,
    group_ids: form.group_ids,
    expires_at: form.expires_at,
    auto_pause_on_expired: autoPauseOnExpired.value
  })
  appStore.showSuccess(t('admin.accounts.accountCreated'))
  emit('created')
  handleClose()
}

// OpenAI OAuth 授权码兑换
const handleOpenAIExchange = async (authCode: string) => {
  if (!authCode.trim() || !openaiOAuth.sessionId.value) return

  openaiOAuth.loading.value = true
  openaiOAuth.error.value = ''

  try {
    const tokenInfo = await openaiOAuth.exchangeAuthCode(
      authCode.trim(),
      openaiOAuth.sessionId.value,
      form.proxy_id,
      oauthFlowRef.value?.oauthState || openaiOAuth.oauthState.value
    )
    if (!tokenInfo) return

    const credentials = openaiOAuth.buildCredentials(tokenInfo)
    const extra = openaiOAuth.buildExtraInfo(tokenInfo)
    await createAccountAndFinish('openai', 'oauth', credentials, extra)
  } catch (error: any) {
    openaiOAuth.error.value = error.response?.data?.detail || t('admin.accounts.oauth.authFailed')
    appStore.showError(openaiOAuth.error.value)
  } finally {
    openaiOAuth.loading.value = false
  }
}

// Gemini OAuth 授权码兑换
const handleGeminiExchange = async (authCode: string) => {
  if (!authCode.trim() || !geminiOAuth.sessionId.value) return

  geminiOAuth.loading.value = true
  geminiOAuth.error.value = ''

  try {
    const stateFromInput = oauthFlowRef.value?.oauthState || ''
    const stateToUse = stateFromInput || geminiOAuth.state.value
    if (!stateToUse) {
      geminiOAuth.error.value = t('admin.accounts.oauth.authFailed')
      appStore.showError(geminiOAuth.error.value)
      return
    }

    const tokenInfo = await geminiOAuth.exchangeAuthCode({
      code: authCode.trim(),
      sessionId: geminiOAuth.sessionId.value,
      state: stateToUse,
      proxyId: form.proxy_id,
      oauthType: geminiOAuthType.value,
      tierId: geminiSelectedTier.value
    })
    if (!tokenInfo) return

    const credentials = geminiOAuth.buildCredentials(tokenInfo)
    const extra = geminiOAuth.buildExtraInfo(tokenInfo)
    await createAccountAndFinish('gemini', 'oauth', credentials, extra)
  } catch (error: any) {
    geminiOAuth.error.value = error.response?.data?.detail || t('admin.accounts.oauth.authFailed')
    appStore.showError(geminiOAuth.error.value)
  } finally {
    geminiOAuth.loading.value = false
  }
}

// Antigravity OAuth 授权码兑换
const handleAntigravityExchange = async (authCode: string) => {
  if (!authCode.trim() || !antigravityOAuth.sessionId.value) return

  antigravityOAuth.loading.value = true
  antigravityOAuth.error.value = ''

  try {
    const stateFromInput = oauthFlowRef.value?.oauthState || ''
    const stateToUse = stateFromInput || antigravityOAuth.state.value
    if (!stateToUse) {
      antigravityOAuth.error.value = t('admin.accounts.oauth.authFailed')
      appStore.showError(antigravityOAuth.error.value)
      return
    }

    const tokenInfo = await antigravityOAuth.exchangeAuthCode({
      code: authCode.trim(),
      sessionId: antigravityOAuth.sessionId.value,
      state: stateToUse,
      proxyId: form.proxy_id
    })
    if (!tokenInfo) return

    const credentials = antigravityOAuth.buildCredentials(tokenInfo)
    const extra = mixedScheduling.value ? { mixed_scheduling: true } : undefined
    await createAccountAndFinish('antigravity', 'oauth', credentials, extra)
  } catch (error: any) {
    antigravityOAuth.error.value = error.response?.data?.detail || t('admin.accounts.oauth.authFailed')
    appStore.showError(antigravityOAuth.error.value)
  } finally {
    antigravityOAuth.loading.value = false
  }
}

// Anthropic OAuth 授权码兑换
const handleAnthropicExchange = async (authCode: string) => {
  if (!authCode.trim() || !oauth.sessionId.value) return

  oauth.loading.value = true
  oauth.error.value = ''

  try {
    const proxyConfig = form.proxy_id ? { proxy_id: form.proxy_id } : {}
    const endpoint =
      addMethod.value === 'oauth'
        ? '/admin/accounts/exchange-code'
        : '/admin/accounts/exchange-setup-token-code'

    const tokenInfo = await adminAPI.accounts.exchangeCode(endpoint, {
      session_id: oauth.sessionId.value,
      code: authCode.trim(),
      ...proxyConfig
    })

    const extra = oauth.buildExtraInfo(tokenInfo)
    const credentials = {
      ...tokenInfo,
      ...(interceptWarmupRequests.value ? { intercept_warmup_requests: true } : {})
    }
    await createAccountAndFinish(form.platform, addMethod.value as AccountType, credentials, extra)
  } catch (error: any) {
    oauth.error.value = error.response?.data?.detail || t('admin.accounts.oauth.authFailed')
    appStore.showError(oauth.error.value)
  } finally {
    oauth.loading.value = false
  }
}

// 主入口：根据平台路由到对应处理函数
const handleExchangeCode = async () => {
  const authCode = oauthFlowRef.value?.authCode || ''

  switch (form.platform) {
    case 'openai':
      return handleOpenAIExchange(authCode)
    case 'gemini':
      return handleGeminiExchange(authCode)
    case 'antigravity':
      return handleAntigravityExchange(authCode)
    default:
      return handleAnthropicExchange(authCode)
  }
}

const handleCookieAuth = async (sessionKey: string) => {
  oauth.loading.value = true
  oauth.error.value = ''

  try {
    const proxyConfig = form.proxy_id ? { proxy_id: form.proxy_id } : {}
    const keys = oauth.parseSessionKeys(sessionKey)

    if (keys.length === 0) {
      oauth.error.value = t('admin.accounts.oauth.pleaseEnterSessionKey')
      return
    }

    const tempUnschedPayload = tempUnschedEnabled.value
      ? buildTempUnschedRules(tempUnschedRules.value)
      : []
    if (tempUnschedEnabled.value && tempUnschedPayload.length === 0) {
      appStore.showError(t('admin.accounts.tempUnschedulable.rulesInvalid'))
      return
    }

    const endpoint =
      addMethod.value === 'oauth'
        ? '/admin/accounts/cookie-auth'
        : '/admin/accounts/setup-token-cookie-auth'

    let successCount = 0
    let failedCount = 0
    const errors: string[] = []

    for (let i = 0; i < keys.length; i++) {
      try {
        const tokenInfo = await adminAPI.accounts.exchangeCode(endpoint, {
          session_id: '',
          code: keys[i],
          ...proxyConfig
        })

        const extra = oauth.buildExtraInfo(tokenInfo)
        const accountName = keys.length > 1 ? `${form.name} #${i + 1}` : form.name

        // Merge interceptWarmupRequests into credentials
        const credentials: Record<string, unknown> = {
          ...tokenInfo,
          ...(interceptWarmupRequests.value ? { intercept_warmup_requests: true } : {})
        }
        if (tempUnschedEnabled.value) {
          credentials.temp_unschedulable_enabled = true
          credentials.temp_unschedulable_rules = tempUnschedPayload
        }

        await adminAPI.accounts.create({
          name: accountName,
          notes: form.notes,
          platform: form.platform,
          type: addMethod.value, // Use addMethod as type: 'oauth' or 'setup-token'
          credentials,
          extra,
          proxy_id: form.proxy_id,
          concurrency: form.concurrency,
          priority: form.priority,
          rate_multiplier: form.rate_multiplier,
          group_ids: form.group_ids,
          expires_at: form.expires_at,
          auto_pause_on_expired: autoPauseOnExpired.value
        })

        successCount++
      } catch (error: any) {
        failedCount++
        errors.push(
          t('admin.accounts.oauth.keyAuthFailed', {
            index: i + 1,
            error: error.response?.data?.detail || t('admin.accounts.oauth.authFailed')
          })
        )
      }
    }

    if (successCount > 0) {
      appStore.showSuccess(t('admin.accounts.oauth.successCreated', { count: successCount }))
      if (failedCount === 0) {
        emit('created')
        handleClose()
      } else {
        emit('created')
      }
    }

    if (failedCount > 0) {
      oauth.error.value = errors.join('\n')
    }
  } catch (error: any) {
    oauth.error.value = error.response?.data?.detail || t('admin.accounts.oauth.cookieAuthFailed')
  } finally {
    oauth.loading.value = false
  }
}
</script>
