import { describe, expect, it } from 'vitest'
import {
  applyHeaderOverrides,
  buildHeaderOverrides,
  splitHeaderOverrides,
  validateHeaderOverrideRows
} from '../openaiHeaderOverrides'

describe('OpenAI header overrides', () => {
  it('normalizes rows and preserves empty template values', () => {
    expect(
      buildHeaderOverrides([
        { name: ' User-Agent ', value: ' codex-custom ' },
        { name: 'OpenAI-Beta', value: '' }
      ])
    ).toEqual({
      'user-agent': 'codex-custom',
      'openai-beta': ''
    })
  })

  it('rejects blocked and duplicate names', () => {
    expect(validateHeaderOverrideRows([{ name: 'Authorization', value: 'secret' }])).toBe(
      'blockedName'
    )
    expect(validateHeaderOverrideRows([{ name: 'OpenAI-Organization', value: 'org_other' }])).toBe(
      'blockedName'
    )
    expect(validateHeaderOverrideRows([{ name: 'OpenAI-Project', value: 'proj_other' }])).toBe(
      'blockedName'
    )
    for (const name of [
      'Forwarded',
      'Via',
      'X-Forwarded-For',
      'X-Forwarded-Host',
      'X-Forwarded-Port',
      'X-Forwarded-Proto',
      'X-Real-IP',
      'CF-Connecting-IP',
      'True-Client-IP'
    ]) {
      expect(validateHeaderOverrideRows([{ name, value: 'spoofed' }])).toBe('blockedName')
    }
    expect(
      validateHeaderOverrideRows([
        { name: 'X-Custom', value: 'one' },
        { name: 'x-custom', value: 'two' }
      ])
    ).toBe('duplicateName')
  })

  it('applies and clears credential fields', () => {
    const credentials: Record<string, unknown> = { api_key: 'secret' }
    applyHeaderOverrides(credentials, true, [{ name: 'X-Custom', value: 'value' }])
    expect(credentials).toMatchObject({
      header_override_enabled: true,
      header_overrides: { 'x-custom': 'value' }
    })
    expect(splitHeaderOverrides(credentials.header_overrides)).toEqual([
      { name: 'x-custom', value: 'value' }
    ])

    applyHeaderOverrides(credentials, false, [])
    expect(credentials).toEqual({ api_key: 'secret' })
  })
})
