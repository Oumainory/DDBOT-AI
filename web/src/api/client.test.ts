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
})
