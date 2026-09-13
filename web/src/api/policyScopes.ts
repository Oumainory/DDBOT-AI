import type { Source, SubscriptionItem, Target } from './types'

export type PolicyScopeKind = 'source' | 'target' | 'subscription'

export type PolicyScopeOption = {
  id: string
  label: string
  detail: string
}

/**
 * Build policy selector options from the same durable IDs consumed by the
 * policy API. External identities remain display-only so they cannot be
 * accidentally submitted as a scope ID.
 */
export function policyScopeOptions(
  kind: PolicyScopeKind,
  sources: Source[],
  targets: Target[],
  subscriptions: SubscriptionItem[],
): PolicyScopeOption[] {
  switch (kind) {
    case 'source':
      return sources.map((source) => ({
        id: source.id,
        label: `${source.display_name || source.external_id} · ${source.platform}`,
        detail: `${source.external_id} · ${source.id}`,
      }))
    case 'target':
      return targets.map((target) => ({
        id: target.id,
        label: `${target.display_name || target.external_id} · ${target.target_type}`,
        detail: `${target.connector_kind || 'connector'} · ${target.external_id} · ${target.id}`,
      }))
    case 'subscription':
      return subscriptions.map((item) => {
        const subscription = item.subscription
        const sourceLabel = item.source?.display_name || item.source?.external_id || subscription.source_id
        const targetLabel = item.target?.display_name || item.target?.external_id || subscription.target_id
        return {
          id: subscription.id,
          label: `${sourceLabel} → ${targetLabel}`,
          detail: `${subscription.legacy_key} · ${subscription.id}`,
        }
      })
  }
}
