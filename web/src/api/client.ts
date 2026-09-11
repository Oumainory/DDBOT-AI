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

function commandKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  return `${Date.now()}-${Math.random().toString(36).slice(2)}-ddbot`
}

export function command<T>(path: string, method: 'POST' | 'PATCH' | 'DELETE', body: unknown, csrfToken: string): Promise<T> {
  const headers = new Headers({ 'Content-Type': 'application/json', 'Idempotency-Key': commandKey() })
  if (csrfToken) headers.set('X-CSRF-Token', csrfToken)
  return request<T>(path, { method, headers, body: JSON.stringify(body ?? {}) })
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
    case 'observation_unavailable': return 'Observation 当前不可用或尚未启用'
    case 'observation_not_found': return 'Observation 已不存在（可能已被保留策略清理）'
    case 'migration_required': return '该 Connector 存在有效订阅，需要迁移后才能切换类型'
    case 'projection_degraded': return 'Legacy 已更新，但 SQLite projection 当前降级，请稍后重建'
    case 'source_in_use': return 'Source 仍有订阅，请先解除订阅'
    case 'target_in_use': return 'Target 仍有订阅，请先解除订阅'
    case 'discovery_unavailable': return '搜索暂不可用，但仍可使用 UID / 官方 profile 直接添加'
    case 'idempotency_conflict': return '请求 Key 已用于另一种请求，请重新生成后重试'
    case 'idempotency_in_progress': return '相同请求正在处理中，请稍后刷新'
    case 'invalid_argument': return '筛选参数无效，请检查时间和分页条件'
    default: return error.message || '请求失败'
  }
}

export function isUnauthorized(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 401
}
