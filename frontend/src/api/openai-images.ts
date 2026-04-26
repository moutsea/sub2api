/**
 * OpenAI image generation helpers for frontend preview/testing.
 * Uses the user's own gateway API key against the OpenAI-compatible images endpoint.
 */

export interface OpenAIImageGenerationItem {
  b64_json?: string
  url?: string
  revised_prompt?: string
}

export interface OpenAIImageGenerationResponse {
  created?: number
  data: OpenAIImageGenerationItem[]
}

export interface GenerateOpenAIImageOptions {
  apiKey: string
  payload: Record<string, unknown>
  signal?: AbortSignal
}

export interface EditOpenAIImageOptions {
  apiKey: string
  payload: Record<string, unknown>
  image: File
  signal?: AbortSignal
}

function extractImageErrorMessage(body: any): string {
  if (!body) {
    return 'Image generation failed'
  }
  if (typeof body === 'string') {
    return body
  }
  if (typeof body?.error?.message === 'string' && body.error.message.trim()) {
    return body.error.message.trim()
  }
  if (typeof body?.detail === 'string' && body.detail.trim()) {
    return body.detail.trim()
  }
  return 'Image generation failed'
}

async function parseImageResponse(response: Response): Promise<OpenAIImageGenerationResponse> {
  const rawText = await response.text()
  let parsed: any = null

  if (rawText) {
    try {
      parsed = JSON.parse(rawText)
    } catch {
      parsed = rawText
    }
  }

  if (!response.ok) {
    throw new Error(extractImageErrorMessage(parsed))
  }

  return (parsed || { data: [] }) as OpenAIImageGenerationResponse
}

export async function generate(options: GenerateOpenAIImageOptions): Promise<OpenAIImageGenerationResponse> {
  const response = await fetch('/v1/images/generations', {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${options.apiKey}`,
      'Content-Type': 'application/json'
    },
    body: JSON.stringify(options.payload),
    signal: options.signal
  })

  return parseImageResponse(response)
}

export async function edit(options: EditOpenAIImageOptions): Promise<OpenAIImageGenerationResponse> {
  const formData = new FormData()
  Object.entries(options.payload).forEach(([key, value]) => {
    if (value === undefined || value === null) {
      return
    }
    formData.append(key, String(value))
  })
  formData.append('image', options.image)

  const response = await fetch('/v1/images/edits', {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${options.apiKey}`
    },
    body: formData,
    signal: options.signal
  })

  return parseImageResponse(response)
}

export const openAIImagesAPI = {
  generate,
  edit
}

export default openAIImagesAPI
