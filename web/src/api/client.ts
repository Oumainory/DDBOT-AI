import type { ApiEnvelope } from './types'

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId?: string

  constructor(status: number, code: string, message: string, requestId?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
  }
}

async function parsePayload(response: Response): Promise<ApiEnvelope<unknown>> {
  const text = await response.text()
  if (!text) return {}
  try {
    return JSON.parse(text) as ApiEnvelope<unknown>
  } catch {
    throw new ApiError(response.status, 'invalid_json', '服务器返回了无效响应')
  }
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  const payload = await parsePayload(response)
  if (!response.ok || payload.error) {
    const error = payload.error ?? { code: `http_${response.status}`, message: '请求失败' }
    throw new ApiError(response.status, error.code, error.message, payload.request_id)
  }
  return payload.data as T
}

export function get<T>(path: string): Promise<T> {
  return request<T>(path, { method: 'GET' })
}

export function post<T>(path: string, body: unknown, csrfToken?: string): Promise<T> {
  const headers = new Headers({ 'Content-Type': 'application/json' })
  if (csrfToken) headers.set('X-CSRF-Token', csrfToken)
  return request<T>(path, { method: 'POST', headers, body: JSON.stringify(body) })
}

export function displayError(error: unknown): string {
  if (!(error instanceof ApiError)) return '网络连接失败，请稍后重试'
  switch (error.code) {
    case 'invalid_credentials': return '用户名或密码不正确'
    case 'rate_limited': return '登录尝试过于频繁，请稍后再试'
    case 'invalid_setup_token': return 'Setup Token 无效或已失效'
    case 'setup_complete': return '初始化已经完成，请直接登录'
    case 'csrf_rejected': return '安全校验失败，请刷新页面后重试'
    case 'auth_unavailable': return '认证服务暂不可用'
    case 'platform_unavailable': return '平台服务暂不可用'
    default: return error.message || '请求失败'
  }
}

export function isUnauthorized(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 401
}
