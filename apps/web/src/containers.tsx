import { useCallback, useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Group, Loader, Table, Text, Title } from '@mantine/core'

type LiveAgent = { agentId: string; dockerTier: string; agentVersion: string; connectedAt: string }
type DeviceRef = { id: string; name: string; agentId?: string | null }
type PingResult = { state: 'running' } | { state: 'ok'; roundTripMillis: number } | { state: 'error'; message: string }

const tierLabels: Record<string, { label: string; color: string; hint: string }> = {
  read: { label: 'Read only', color: 'gray', hint: 'The host can be viewed but not changed.' },
  manage: { label: 'Manage', color: 'blue', hint: 'Containers, images, networks and volumes can be changed.' },
  exec: { label: 'Manage + terminal', color: 'orange', hint: 'Includes interactive terminals inside containers.' },
  deploy: { label: 'Full control', color: 'red', hint: 'Includes deploying Compose stacks.' },
}

async function getJSON<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin' })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 200) || 'The request failed.')
  return response.json() as Promise<T>
}

export function Containers({ selectedId, onSelect }: { selectedId: string | null; onSelect: (id: string | null) => void }) {
  const [agents, setAgents] = useState<LiveAgent[] | null>(null)
  const [names, setNames] = useState<Record<string, string>>({})
  const [error, setError] = useState('')
  const [pings, setPings] = useState<Record<string, PingResult>>({})

  const refresh = useCallback(async () => {
    try {
      const [live, devices] = await Promise.all([
        getJSON<LiveAgent[]>('/api/channel/agents'),
        getJSON<{ devices: DeviceRef[] }>('/api/inventory/devices?limit=100&offset=0').catch(() => ({ devices: [] as DeviceRef[] })),
      ])
      setAgents(live)
      setNames(Object.fromEntries(devices.devices.filter((device) => device.agentId).map((device) => [device.agentId as string, device.name])))
      setError('')
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not load live connections.') }
  }, [])

  useEffect(() => {
    void refresh()
    const timer = window.setInterval(() => { void refresh() }, 10_000)
    return () => window.clearInterval(timer)
  }, [refresh])

  async function ping(agentId: string) {
    setPings((current) => ({ ...current, [agentId]: { state: 'running' } }))
    try {
      const response = await fetch(`/api/channel/agents/${encodeURIComponent(agentId)}/ping`, { method: 'POST', credentials: 'same-origin' })
      if (!response.ok) throw new Error((await response.text()).trim().slice(0, 200) || 'The host did not answer.')
      const body = await response.json() as { roundTripMillis: number }
      setPings((current) => ({ ...current, [agentId]: { state: 'ok', roundTripMillis: body.roundTripMillis } }))
    } catch (cause) {
      setPings((current) => ({ ...current, [agentId]: { state: 'error', message: cause instanceof Error ? cause.message : 'The host did not answer.' } }))
    }
  }

  const visible = (agents ?? []).filter((agent) => !selectedId || agent.agentId === selectedId)
  return <>
    <Text size="xs" fw={700} c="dimmed" tt="uppercase">Home infrastructure</Text>
    <Title order={1} mt={4}>Containers</Title>
    <Text c="dimmed" mt={6}>Container management for your hosts. Live features need a host's agent to hold an open connection to this server.</Text>
    {error && <Alert color="red" mt="md" title="Live connections could not be loaded">{error}</Alert>}
    <Alert color="blue" mt="md" title="Getting started">
      Container lists, logs, actions and terminals are being added in stages. This page currently shows which hosts are connected and how much access each one allows.
      A host offers more than read-only access only when its operator enables it locally with <code>VIGIRA_DOCKER_MODE</code> and its enrollment allows it.
    </Alert>
    {selectedId && <Group mt="md"><Button variant="default" size="xs" onClick={() => onSelect(null)}>← All hosts</Button></Group>}
    <Card withBorder radius="md" padding="lg" mt="lg">
      <Group justify="space-between" mb="sm">
        <Title order={3}>Connected hosts</Title>
        <Button variant="default" size="xs" onClick={() => void refresh()}>Refresh</Button>
      </Group>
      {agents === null && !error ? <Group><Loader size="sm" /><Text c="dimmed">Loading…</Text></Group>
        : visible.length === 0 ? <Text c="dimmed">No hosts are connected. Update the agent on a host to a build with live-channel support and restart it.</Text>
        : <Table.ScrollContainer minWidth={640}><Table verticalSpacing="sm"><Table.Thead><Table.Tr><Table.Th>Host</Table.Th><Table.Th>Docker access</Table.Th><Table.Th>Agent</Table.Th><Table.Th>Connected since</Table.Th><Table.Th>Connection</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{visible.map((agent) => {
            const tier = tierLabels[agent.dockerTier] ?? tierLabels.read
            const result = pings[agent.agentId]
            return <Table.Tr key={agent.agentId}>
              <Table.Td><Text fw={600} size="sm">{names[agent.agentId] ?? agent.agentId.slice(0, 8)}</Text><Text size="xs" c="dimmed">{agent.agentId.slice(0, 8)}</Text></Table.Td>
              <Table.Td><Badge color={tier.color} variant="light" title={tier.hint}>{tier.label}</Badge></Table.Td>
              <Table.Td><Text size="sm">{agent.agentVersion || 'unknown'}</Text></Table.Td>
              <Table.Td><Text size="sm">{new Date(agent.connectedAt).toLocaleString()}</Text></Table.Td>
              <Table.Td>
                <Group gap="xs" wrap="nowrap">
                  <Button size="xs" variant="light" loading={result?.state === 'running'} onClick={() => void ping(agent.agentId)}>Test connection</Button>
                  {result?.state === 'ok' && <Text size="xs" c="green">Replied in {result.roundTripMillis} ms</Text>}
                  {result?.state === 'error' && <Text size="xs" c="red">{result.message}</Text>}
                </Group>
              </Table.Td>
            </Table.Tr>
          })}</Table.Tbody></Table></Table.ScrollContainer>}
    </Card>
  </>
}
