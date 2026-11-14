import { describe, expect, it } from 'vitest'
import { emptyGrant, resourceChoices, targetKey } from './resource-access'

describe('resource selections', () => {
  it('keeps Compose access stable across replacement IDs and hosts separate', () => {
    const hosts = [{ agentId: 'host-a', name: 'Docker host', state: 'ok' as const, containers: [{ id: 'old-id', name: 'game', image: 'game:1', state: 'running', status: 'Up', created: 1, project: 'dragonwilds', service: 'game' }] }]
    const first = resourceChoices(hosts)
    hosts[0].containers[0].id = 'new-id'
    expect(resourceChoices(hosts)).toEqual(first)
    expect(first.map((choice) => choice.target.kind).sort()).toEqual(['service', 'stack'])
    expect(targetKey({ ...first[0].target, agentId: 'host-b' })).not.toEqual(first[0].value)
  })
  it('preserves offline selections and defaults to view only', () => {
    const grant = emptyGrant({ agentId: 'host-a', kind: 'service', resource: 'dragonwilds', service: 'game' })
    expect(resourceChoices([], [grant])[0].target).toEqual(grant)
    expect(grant).toMatchObject({ logs: false, restart: false, recreate: false, pullRecreate: false })
  })
  it('ties standalone grants to an immutable container ID', () => {
    const choices = resourceChoices([{ agentId: 'host-a', name: 'Docker host', state: 'ok', containers: [{ id: 'immutable-id', name: 'game', image: 'game:1', state: 'running', status: 'Up', created: 1 }] }])
    expect(choices[0].target).toEqual({ agentId: 'host-a', kind: 'container', resource: 'immutable-id' })
  })
})
