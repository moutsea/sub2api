import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { GrokTokenInfo } from '@/api/admin/grok'

export function useGrokOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()
  const authUrl = ref('')
  const sessionId = ref('')
  const state = ref('')
  const loading = ref(false)
  const error = ref('')

  const resetState = () => {
    authUrl.value = ''
    sessionId.value = ''
    state.value = ''
    loading.value = false
    error.value = ''
  }

  const generateAuthUrl = async (proxyId?: number | null): Promise<boolean> => {
    loading.value = true
    error.value = ''
    try {
      const payload = proxyId ? { proxy_id: proxyId } : {}
      const response = await adminAPI.grok.generateAuthUrl(payload)
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      state.value = response.state
      return true
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.grok.failedToGenerateUrl')
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  const exchangeAuthCode = async (params: { code: string; sessionId: string; state: string; proxyId?: number | null }): Promise<GrokTokenInfo | null> => {
    if (!params.code?.trim() || !params.sessionId || !params.state) {
      error.value = t('admin.accounts.oauth.grok.missingExchangeParams')
      return null
    }
    loading.value = true
    error.value = ''
    try {
      return await adminAPI.grok.exchangeCode({
        session_id: params.sessionId,
        state: params.state,
        code: params.code.trim(),
        ...(params.proxyId ? { proxy_id: params.proxyId } : {})
      })
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.grok.failedToExchangeCode')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const validateRefreshToken = async (refreshToken: string, proxyId?: number | null): Promise<GrokTokenInfo | null> => {
    if (!refreshToken.trim()) {
      error.value = t('admin.accounts.oauth.grok.pleaseEnterRefreshToken')
      return null
    }
    loading.value = true
    error.value = ''
    try {
      return await adminAPI.grok.refreshGrokToken(refreshToken.trim(), proxyId)
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.grok.failedToValidateRT')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const buildCredentials = (tokenInfo: GrokTokenInfo): Record<string, unknown> => {
    const credentials: Record<string, unknown> = {
      access_token: tokenInfo.access_token,
      refresh_token: tokenInfo.refresh_token,
      token_type: tokenInfo.token_type,
      expires_at: tokenInfo.expires_at,
      client_id: tokenInfo.client_id,
      scope: tokenInfo.scope,
      email: tokenInfo.email,
      subscription_tier: tokenInfo.subscription_tier,
      entitlement_status: tokenInfo.entitlement_status
    }
    if (tokenInfo.id_token) credentials.id_token = tokenInfo.id_token
    return Object.fromEntries(Object.entries(credentials).filter(([, value]) => value !== undefined && value !== ''))
  }

  const buildExtraInfo = (tokenInfo: GrokTokenInfo): Record<string, unknown> => Object.fromEntries(
    Object.entries({ email: tokenInfo.email, subscription_tier: tokenInfo.subscription_tier, entitlement_status: tokenInfo.entitlement_status })
      .filter(([, value]) => value !== undefined && value !== '')
  )

  return { authUrl, sessionId, state, loading, error, resetState, generateAuthUrl, exchangeAuthCode, validateRefreshToken, buildCredentials, buildExtraInfo }
}
