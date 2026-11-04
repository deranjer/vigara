import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Group, Stack, Text, Title } from '@mantine/core'

type Update = {
  automatic: boolean
  canManage: boolean
  currentVersion: string
  available: { version: string; sha256: string } | null
  status: { state: string; version?: string; message?: string }
  reportedAt: string | null
}

async function request(path: string, body?: object): Promise<Update> {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
  })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 300) || 'Could not load client updates.')
  return response.json() as Promise<Update>
}

const labels: Record<string, string> = {
  not_installed: 'Awaiting updater', idle: 'Ready', downloading: 'Downloading', waiting: 'Waiting for active work',
  installing: 'Installing', checking: 'Checking agent', updated: 'Up to date', rolled_back: 'Previous version restored', failed: 'Update failed',
}

export function AgentUpdate({ deviceId }: { deviceId: string }) {
  const [value, setValue] = useState<Update | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [queued, setQueued] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const path = `/api/devices/${encodeURIComponent(deviceId)}/agent-update`
  useEffect(() => {
    let active = true
    let timer: ReturnType<typeof setTimeout>
    setValue(null); setQueued(false)
    const load = async () => {
      try {
        const next = await request(path)
        if (active) { setValue(next); setError('') }
      } catch (cause) { if (active) setError(cause instanceof Error ? cause.message : 'Could not load client updates.') }
      if (active) timer = setTimeout(() => { void load() }, 10000)
    }
    void load()
    return () => { active = false; clearTimeout(timer) }
  }, [path, attempt])
  const save = async (body: object) => {
    setBusy(true); setError('')
    try { setValue(await request(path, body)); setQueued('updateNow' in body) }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not save client updates.') }
    finally { setBusy(false) }
  }
  const state = value?.status.state || 'not_installed'
  const installing = ['downloading', 'waiting', 'installing', 'checking'].includes(state)
  return <Card withBorder padding="lg"><Stack gap="sm">
    <Group justify="space-between"><Title order={3}>Client updates</Title><Badge color={['failed', 'rolled_back'].includes(state) ? 'orange' : 'blue'}>{labels[state] || state}</Badge></Group>
    {error && <Alert color="red">{error}<Button variant="subtle" size="xs" onClick={() => setAttempt(v => v + 1)}>Retry</Button></Alert>}
    {value && <>
      <Text size="sm">Installed: {value.currentVersion || 'Unknown'} · Available: {value.available?.version || 'No compatible package'}</Text>
      <Checkbox label="Automatically install compatible client updates" checked={value.automatic} disabled={busy || !value.canManage} onChange={event => { void save({ automatic: event.currentTarget.checked }) }} />
      <Text size="sm" c="dimmed">The client checks about once a minute, waits for backups, deployments, and open live sessions to finish, then restarts its background service. Enrollment and settings are preserved.</Text>
      {state === 'not_installed' && <Alert color="blue">Run a new Reinstall agent bundle once to install the local updater. Self updates require an HTTPS server address.</Alert>}
      {value.status.message && <Text size="sm">{value.status.message}</Text>}
      {queued && <Text size="sm" role="status">Update requested. The client will pick it up on its next check.</Text>}
      <Group><Button variant="light" disabled={busy || installing || !value.available || !value.canManage} loading={busy} onClick={() => { void save({ updateNow: true }) }}>{['failed', 'rolled_back'].includes(state) ? 'Retry update' : 'Update now'}</Button>
      {value.reportedAt && <Text size="xs" c="dimmed">Updater checked {new Date(value.reportedAt).toLocaleString()}</Text>}</Group>
    </>}
  </Stack></Card>
}
