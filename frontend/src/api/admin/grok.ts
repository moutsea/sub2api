import { apiClient } from '../client'

export interface GrokAuthUrlResponse {
  auth_url: string
  session_id: string
  state: string
}

export interface GrokAuthUrlRequest {
  proxy_id?: number
  redirect_uri?: string
}

export interface GrokExchangeCodeRequest {
  session_id: string
  state: string
  code: string
  proxy_id?: number
  redirect_uri?: string
}

export interface GrokTokenInfo {
  access_token?: string
  refresh_token?: string
  token_type?: string
  id_token?: string
  expires_at?: number | string
  expires_in?: number
  scope?: string
  client_id?: string
  email?: string
  subscription_tier?: string
  entitlement_status?: string
  [key: string]: unknown
}

const generateAuthUrl = async (payload: GrokAuthUrlRequest = {}): Promise<GrokAuthUrlResponse> => {
  const { data } = await apiClient.post<GrokAuthUrlResponse>('/admin/grok/oauth/auth-url', payload)
  return data
}

const exchangeCode = async (payload: GrokExchangeCodeRequest): Promise<GrokTokenInfo> => {
  const { data } = await apiClient.post<GrokTokenInfo>('/admin/grok/oauth/exchange-code', payload)
  return data
}

const refreshGrokToken = async (refreshToken: string, proxyId?: number | null): Promise<GrokTokenInfo> => {
  const payload: Record<string, unknown> = { refresh_token: refreshToken }
  if (proxyId) payload.proxy_id = proxyId
  const { data } = await apiClient.post<GrokTokenInfo>('/admin/grok/oauth/refresh-token', payload)
  return data
}

export default { generateAuthUrl, exchangeCode, refreshGrokToken }
