import type { HostEntry } from './container-utils'

export type ResourceGrant = { agentId: string; kind: 'stack' | 'service' | 'container'; resource: string; service?: string; logs: boolean; restart: boolean; recreate: boolean; pullRecreate: boolean }
export type ResourceTarget = Pick<ResourceGrant, 'agentId' | 'kind' | 'resource' | 'service'>
export const targetKey = (target: ResourceTarget) => JSON.stringify([target.agentId, target.kind, target.resource, target.service || ''])
export const emptyGrant = (target: ResourceTarget): ResourceGrant => ({ ...target, logs: false, restart: false, recreate: false, pullRecreate: false })

export function resourceChoices(hosts: HostEntry[], grants: ResourceGrant[] = []) {
  const choices = new Map<string, { value: string; label: string; target: ResourceTarget }>()
  const add = (target: ResourceTarget, label: string) => choices.set(targetKey(target), { value: targetKey(target), label, target })
  for (const host of hosts) for (const container of host.containers) {
    if (container.project) add({ agentId: host.agentId, kind: 'stack', resource: container.project }, `${host.name} · Stack: ${container.project}`)
    if (container.project && container.service) add({ agentId: host.agentId, kind: 'service', resource: container.project, service: container.service }, `${host.name} · ${container.project} / ${container.service}`)
    else add({ agentId: host.agentId, kind: 'container', resource: container.id }, `${host.name} · Container: ${container.name}`)
  }
  for (const grant of grants) if (!choices.has(targetKey(grant))) add(grant, `${grant.kind}: ${grant.resource}${grant.service ? ` / ${grant.service}` : ''} (not currently listed)`)
  return [...choices.values()].sort((a, b) => a.label.localeCompare(b.label))
}

export async function resourceRequest<T>(url: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, { method, signal, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).slice(0, 350) || 'The request failed.')
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
