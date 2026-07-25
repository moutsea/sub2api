export interface HeaderOverrideRow {
  name: string
  value: string
}

export const HEADER_OVERRIDE_ENABLED_KEY = 'header_override_enabled'
export const HEADER_OVERRIDES_KEY = 'header_overrides'

const blockedNames = new Set([
  'host',
  'content-length',
  'content-type',
  'transfer-encoding',
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'proxy-connection',
  'te',
  'trailer',
  'upgrade',
  'authorization',
  'x-api-key',
  'x-goog-api-key',
  'cookie',
  'accept-encoding',
  'sec-websocket-key',
  'sec-websocket-version',
  'sec-websocket-extensions',
  'sec-websocket-protocol',
  'sec-websocket-accept',
  'session_id',
  'conversation_id',
  'x-codex-turn-state',
  'x-codex-turn-metadata',
  'chatgpt-account-id',
  'x-client-request-id',
  'openai-organization',
  'openai-project',
  'forwarded',
  'via',
  'x-forwarded-for',
  'x-forwarded-host',
  'x-forwarded-port',
  'x-forwarded-proto',
  'x-real-ip',
  'cf-connecting-ip',
  'true-client-ip'
])

const headerNamePattern = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/
const textEncoder = new TextEncoder()

function hasInvalidHeaderValue(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code <= 8 || (code >= 10 && code <= 31) || code === 127) return true
  }
  return false
}

export const openAIHeaderOverrideTemplate: HeaderOverrideRow[] = [
  'user-agent',
  'originator',
  'openai-beta',
  'version',
  'accept',
  'accept-language'
].map((name) => ({ name, value: '' }))

export type HeaderOverrideValidationError =
  | 'invalidName'
  | 'blockedName'
  | 'duplicateName'
  | 'invalidValue'
  | 'tooManyEntries'

export function validateHeaderOverrideRows(
  rows: HeaderOverrideRow[]
): HeaderOverrideValidationError | null {
  const seen = new Set<string>()
  for (const row of rows) {
    const name = row.name.trim()
    const value = row.value.trim()
    if (!name) {
      if (value) return 'invalidName'
      continue
    }
    if (!headerNamePattern.test(name) || textEncoder.encode(name).length > 200) {
      return 'invalidName'
    }
    const lowerName = name.toLowerCase()
    if (blockedNames.has(lowerName)) return 'blockedName'
    if (seen.has(lowerName)) return 'duplicateName'
    if (hasInvalidHeaderValue(value) || textEncoder.encode(value).length > 8192) {
      return 'invalidValue'
    }
    seen.add(lowerName)
  }
  return seen.size > 64 ? 'tooManyEntries' : null
}

export function buildHeaderOverrides(rows: HeaderOverrideRow[]): Record<string, string> {
  const result: Record<string, string> = {}
  for (const row of rows) {
    const name = row.name.trim().toLowerCase()
    if (name) result[name] = row.value.trim()
  }
  return result
}

export function splitHeaderOverrides(value: unknown): HeaderOverrideRow[] {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return []
  return Object.entries(value as Record<string, unknown>)
    .filter(([, headerValue]) => typeof headerValue === 'string')
    .map(([name, headerValue]) => ({ name, value: headerValue as string }))
    .sort((left, right) => left.name.localeCompare(right.name))
}

export function applyHeaderOverrides(
  credentials: Record<string, unknown>,
  enabled: boolean,
  rows: HeaderOverrideRow[]
): void {
  if (enabled) {
    credentials[HEADER_OVERRIDE_ENABLED_KEY] = true
    credentials[HEADER_OVERRIDES_KEY] = buildHeaderOverrides(rows)
    return
  }
  delete credentials[HEADER_OVERRIDE_ENABLED_KEY]
  delete credentials[HEADER_OVERRIDES_KEY]
}
