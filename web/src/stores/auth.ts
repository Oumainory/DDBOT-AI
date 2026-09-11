import { defineStore } from 'pinia'
import { ApiError, get, post } from '../api/client'
import type { SessionState, SetupStatus } from '../api/types'

export type AuthStatus = 'unknown' | 'setup_required' | 'unauthenticated' | 'authenticated'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    status: 'unknown' as AuthStatus,
    username: '',
    csrfToken: '',
    expiresAt: '',
    lastError: '',
  }),
  actions: {
    async refreshSetupState() {
      try {
        const data = await get<SetupStatus>('/api/v2/setup/status')
        if (data.state === 'setup_required') this.status = 'setup_required'
        else if (this.status !== 'authenticated') this.status = 'unauthenticated'
        this.lastError = ''
      } catch (error) {
        this.lastError = error instanceof Error ? error.message : 'setup status unavailable'
      }
    },
    async refreshSession() {
      try {
        const data = await get<SessionState>('/api/v2/auth/session')
        if (data.authenticated) {
          this.status = 'authenticated'
          this.username = data.username ?? ''
          this.csrfToken = data.csrf_token ?? ''
          this.expiresAt = data.expires_at ?? ''
        } else if (this.status !== 'setup_required') {
          this.clear()
          this.status = 'unauthenticated'
        }
        this.lastError = ''
      } catch (error) {
        if (error instanceof ApiError && error.status === 503) this.lastError = error.message
        else this.clear()
      }
    },
    async bootstrap() {
      await this.refreshSetupState()
      await this.refreshSession()
    },
    async setup(setupToken: string, username: string, password: string) {
      await post('/api/v2/setup', { setup_token: setupToken, username, password })
      this.clear()
      this.status = 'unauthenticated'
    },
    async login(username: string, password: string) {
      const data = await post<SessionState>('/api/v2/auth/login', { username, password })
      this.status = 'authenticated'
      this.username = data.username ?? ''
      this.csrfToken = data.csrf_token ?? ''
      this.expiresAt = data.expires_at ?? ''
      this.lastError = ''
    },
    async logout() {
      try {
        await post('/api/v2/auth/logout', {}, this.csrfToken)
      } catch (error) {
        if (!(error instanceof ApiError) || (error.status !== 401 && error.status !== 503)) throw error
      } finally {
        this.clear()
        this.status = 'unauthenticated'
      }
    },
    clear() {
      this.username = ''
      this.csrfToken = ''
      this.expiresAt = ''
    },
  },
})
