import { describe, expect, it } from 'vitest'
import { policyScopeOptions } from './policyScopes'
import type { Source, SubscriptionItem, Target } from './types'

describe('policy scope selector contract', () => {
  const source: Source = {
    id: 'src-durable', platform: 'bilibili', external_id: '401742377',
    display_name: '原神官方', status: 'active', subscription_count: 1,
    created_at: '', updated_at: '',
  }
  const target: Target = {
    id: 'tgt-durable', connector_id: 'conn-onebot', connector_kind: 'onebot',
    target_type: 'group', external_id: '123456', display_name: '原神交流群',
    status: 'resolved', source_count: 1, created_at: '', updated_at: '',
  }
  const subscription: SubscriptionItem = {
    subscription: {
      id: 'sub-durable', source_id: source.id, target_id: target.id,
      legacy_key: 'bilibili:401742377:dynamic:123456', enabled: true,
      projection_status: 'active', projected_at: '',
    },
    source,
    target,
  }

  it('submits durable source and target IDs, not external identities', () => {
    expect(policyScopeOptions('source', [source], [], [])[0]).toMatchObject({ id: 'src-durable' })
    expect(policyScopeOptions('target', [], [target], [])[0]).toMatchObject({ id: 'tgt-durable' })
    expect(policyScopeOptions('source', [source], [], [])[0].id).not.toBe(source.external_id)
    expect(policyScopeOptions('target', [], [target], [])[0].id).not.toBe(target.external_id)
  })

  it('uses the durable subscription projection ID', () => {
    const option = policyScopeOptions('subscription', [], [], [subscription])[0]
    expect(option.id).toBe('sub-durable')
    expect(option.detail).toContain('bilibili:401742377:dynamic:123456')
  })
})
