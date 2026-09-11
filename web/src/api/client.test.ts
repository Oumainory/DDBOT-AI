import { describe, expect, it, vi } from 'vitest'
import { ApiError, displayError, get } from './client'

describe('API client', () => {
  it('uses same-origin credentials and maps the error envelope', async () => {
    vi.stubGlobal('fetch', vi.fn(async (_path: string, init: RequestInit) => new Response(
      JSON.stringify({ error: { code: 'rate_limited', message: 'internal' }, request_id: 'req-1' }),
      { status: 429, headers: { 'content-type': 'application/json' } },
    )))
    await expect(get('/api/v2/auth/session')).rejects.toEqual(new ApiError(429, 'rate_limited', 'internal', 'req-1'))
    expect(vi.mocked(fetch)).toHaveBeenCalledWith('/api/v2/auth/session', expect.objectContaining({ credentials: 'same-origin' }))
    expect(displayError(new ApiError(429, 'rate_limited', 'internal'))).toContain('频繁')
    vi.unstubAllGlobals()
  })

  it('keeps observation diagnostics explicit without exposing backend details', () => {
    expect(displayError(new ApiError(503, 'observation_unavailable', 'internal sqlite error'))).toContain('Observation')
    expect(displayError(new ApiError(404, 'observation_not_found', 'row missing'))).toContain('不存在')
    expect(displayError(new ApiError(400, 'invalid_argument', 'sql query'))).toContain('筛选参数')
  })
})
