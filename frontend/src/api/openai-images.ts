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

export type ImagePreviewJobStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled'

export interface ImagePreviewJobResponse {
  id: string
  status: ImagePreviewJobStatus
  status_code?: number
  content_type?: string
  result?: OpenAIImageGenerationResponse
  error?: string
  created_at: string
  updated_at: string
  completed_at?: string
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

async function parseImagePreviewJobResponse(response: Response): Promise<ImagePreviewJobResponse> {
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

  return parsed as ImagePreviewJobResponse
}

function waitForJobPoll(intervalMs: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('Request aborted', 'AbortError'))
      return
    }
    const timer = window.setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, intervalMs)
    const onAbort = () => {
      window.clearTimeout(timer)
      signal?.removeEventListener('abort', onAbort)
      reject(new DOMException('Request aborted', 'AbortError'))
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
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

export async function createPreviewJob(options: GenerateOpenAIImageOptions): Promise<ImagePreviewJobResponse> {
  const response = await fetch('/v1/image-preview/jobs', {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${options.apiKey}`,
      'Content-Type': 'application/json'
    },
    body: JSON.stringify(options.payload),
    signal: options.signal
  })

  return parseImagePreviewJobResponse(response)
}

export async function getPreviewJob(
  apiKey: string,
  jobId: string,
  signal?: AbortSignal
): Promise<ImagePreviewJobResponse> {
  const response = await fetch(`/v1/image-preview/jobs/${encodeURIComponent(jobId)}`, {
    method: 'GET',
    headers: {
      Authorization: `Bearer ${apiKey}`
    },
    signal
  })

  return parseImagePreviewJobResponse(response)
}

export async function cancelPreviewJob(apiKey: string, jobId: string): Promise<ImagePreviewJobResponse | null> {
  try {
    const response = await fetch(`/v1/image-preview/jobs/${encodeURIComponent(jobId)}`, {
      method: 'DELETE',
      headers: {
        Authorization: `Bearer ${apiKey}`
      }
    })

    return parseImagePreviewJobResponse(response)
  } catch {
    return null
  }
}

export async function generatePreview(options: GenerateOpenAIImageOptions): Promise<OpenAIImageGenerationResponse> {
  let jobId = ''
  let cancelRequested = false
  const cancelJob = () => {
    cancelRequested = true
    if (jobId) {
      void cancelPreviewJob(options.apiKey, jobId)
    }
  }
  options.signal?.addEventListener('abort', cancelJob, { once: true })

  try {
    let job = await createPreviewJob(options)
    jobId = job.id

    while (true) {
      if (job.status === 'succeeded') {
        return job.result || { data: [] }
      }
      if (job.status === 'failed' || job.status === 'canceled') {
        throw new Error(job.error || 'Image generation failed')
      }
      await waitForJobPoll(3000, options.signal)
      job = await getPreviewJob(options.apiKey, jobId, options.signal)
    }
  } finally {
    options.signal?.removeEventListener('abort', cancelJob)
    if (cancelRequested && jobId) {
      void cancelPreviewJob(options.apiKey, jobId)
    }
  }
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
  generatePreview,
  createPreviewJob,
  getPreviewJob,
  cancelPreviewJob,
  edit
}

export default openAIImagesAPI
