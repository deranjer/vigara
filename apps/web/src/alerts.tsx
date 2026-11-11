import { useEffect, useState } from 'react'
import { Alert, Button, Card, Group, Loader, SegmentedControl, Stack, Table, Text, Title } from '@mantine/core'
import { StatusText } from './status'

type Incident = { id: string; ruleId: string; dimension: string; status: string; summary: string; startedAt: string; lastObservedAt?: string; resolvedAt?: string }

const when = (value?: string) => value ? new Date(value).toLocaleString() : '—'

/** Alert incidents, newest first. Acknowledge, mute and rule editing arrive with the alert inbox. */
export function Alerts() {
  const [incidents, setIncidents] = useState<Incident[] | null>(null)
  const [error, setError] = useState('')
  const [filter, setFilter] = useState<'firing' | 'all'>('firing')
  const [refresh, setRefresh] = useState(0)
  useEffect(() => {
    let active = true
    const load = () => fetch('/api/alerts/incidents', { credentials: 'same-origin' })
      .then(async (response) => { if (!response.ok) throw new Error('Alerts could not be loaded.'); return await response.json() as Incident[] })
      .then((items) => { if (active) { setIncidents(items); setError('') } })
      .catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Alerts could not be loaded.') })
    void load()
    const timer = window.setInterval(() => void load(), 30_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [refresh])
  const firing = incidents?.filter((incident) => incident.status === 'firing') ?? []
  const shown = filter === 'firing' ? firing : incidents ?? []
  return <Stack gap="lg" className="vg-page">
    <Group justify="space-between" align="flex-end">
      <div><Title order={1}>Alerts</Title><Text c="dimmed" mt={6}>Incidents raised by your alert rules. Firing incidents clear on their own once the condition recovers.</Text></div>
      <Button variant="default" onClick={() => setRefresh((value) => value + 1)}>Refresh</Button>
    </Group>
    {error && <Alert color="red">{error}</Alert>}
    <Card withBorder padding={0}>
      <Group justify="space-between" p="md"><SegmentedControl size="xs" value={filter} onChange={(value) => setFilter(value as 'firing' | 'all')} data={[{ value: 'firing', label: `Firing (${firing.length})` }, { value: 'all', label: `All (${incidents?.length ?? 0})` }]} /></Group>
      {!incidents && !error ? <Group justify="center" p="xl"><Loader size="sm" /></Group>
        : shown.length === 0 ? <Text c="dimmed" ta="center" p="xl">{filter === 'firing' ? 'Nothing is firing right now.' : 'No incidents recorded yet.'}</Text>
        : <div className="table-scroll"><Table verticalSpacing="sm" highlightOnHover>
          <Table.Thead><Table.Tr><Table.Th>Status</Table.Th><Table.Th>Summary</Table.Th><Table.Th>Started</Table.Th><Table.Th>Last seen / resolved</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{shown.map((incident) => <Table.Tr key={incident.id}>
            <Table.Td><StatusText tone={incident.status === 'firing' ? 'crit' : 'ok'}>{incident.status}</StatusText></Table.Td>
            <Table.Td><Text size="sm" fw={500}>{incident.summary}</Text><Text size="xs" c="dimmed" ff="monospace">{incident.dimension}</Text></Table.Td>
            <Table.Td><Text size="sm">{when(incident.startedAt)}</Text></Table.Td>
            <Table.Td><Text size="sm">{incident.resolvedAt ? when(incident.resolvedAt) : when(incident.lastObservedAt)}</Text></Table.Td>
          </Table.Tr>)}</Table.Tbody>
        </Table></div>}
    </Card>
  </Stack>
}
