export type ApiEnvelope<T> = {
  data?: T
  meta?: Record<string, unknown>
  error?: { code: string; message: string }
  request_id?: string
}

export type SetupStatus = {
  state: 'setup_required' | 'ready'
}

export type SessionState = {
  authenticated: boolean
  username?: string
  csrf_token?: string
  expires_at?: string
}

export type ComponentStatus = {
  status: 'available' | 'degraded' | 'recovery' | 'unavailable' | 'unknown'
  code?: string
}

export type Overview = {
  product: { name: string; version: string; commit: string }
  platform: {
    legacy_core: ComponentStatus
    admin_api: ComponentStatus
    sqlite: ComponentStatus
    secret_store: ComponentStatus
    auth: ComponentStatus
  }
}

export type About = {
  product_name: string
  version: string
  commit: string
  build_time: string
  source_repository: string
  commit_url?: string
  license: string
  license_name: string
}
