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

export type ObservationPublicSummary = {
  text?: string
  url?: string
  media_urls?: string[]
  author_id?: string
  author_name?: string
}

export type ObservationEvent = {
  id: string
  platform: string
  source_kind: string
  source_external_id: string
  upstream_event_id: string
  event_type: string
  observed_at: string
  source_event_at?: string
  content_fingerprint: string
  public_summary: ObservationPublicSummary
  route_count?: number
  delivery_count?: number
  final_delivery_status?: string
}

export type ObservationPublicSnapshot = ObservationPublicSummary & {
  platform?: string
  source_kind?: string
  source_external_id?: string
  upstream_event_id?: string
  event_type?: string
  source_event_at?: number
}

export type ObservationRoute = {
  id: string
  event_id: string
  route_ordinal: number
  destination_kind: string
  destination_external_id: string
  outcome: 'pass' | 'filtered' | 'skipped' | 'unknown'
  reason_code: string
  observed_at: string
}

export type ObservationDelivery = {
  id: string
  event_id: string
  route_observation_id: string
  connector_kind: string
  destination_external_id: string
  status: 'sent' | 'queued' | 'not_sent' | 'unknown' | 'rejected'
  result_code: string
  observed_at: string
}

export type ObservationEventDetail = {
  event: ObservationEvent & { public_snapshot: ObservationPublicSnapshot }
  routes: ObservationRoute[]
  deliveries: ObservationDelivery[]
}

export type ObservationPage = {
  items: ObservationEvent[]
  next_cursor?: string
}

export type ObservationSummary = {
  window: { retention_days: number }
  runtime: {
    status: 'available' | 'degraded' | 'disabled' | 'unknown'
    queue_capacity: number
    queue_depth: number
    events_accepted: number
    routes_accepted: number
    deliveries_accepted: number
    queue_dropped: number
    persistence_errors: number
    worker_panics: number
    prune_errors: number
  }
  recent: { events_24h: number; routes_24h: number; deliveries_24h: number }
}

export type Source = {
  id: string
  platform: string
  external_id: string
  handle?: string
  display_name?: string
  canonical_url?: string
  status: string
  subscription_count: number
  metadata?: Record<string, unknown>
  created_at: string
  updated_at: string
}

export type Target = {
  id: string
  connector_id: string
  connector_kind?: string
  target_type: 'group' | 'channel'
  external_id: string
  display_name?: string
  status: string
  source_count: number
  metadata?: Record<string, unknown>
  created_at: string
  updated_at: string
}

export type Connector = {
  id: string
  kind: string
  name: string
  role: string
  enabled: boolean
  status: string
  endpoint?: string
  credential_configured: boolean
  credential_masked?: string
  config?: Record<string, unknown>
  metadata?: Record<string, unknown>
  created_at: string
  updated_at: string
}

export type SubscriptionProjection = {
  id: string
  source_id: string
  target_id: string
  legacy_key: string
  enabled: boolean
  projection_status: string
  projected_at: string
}

export type SubscriptionItem = {
  subscription: SubscriptionProjection
  source?: Source
  target?: Target
}
