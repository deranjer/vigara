import { useEffect, useState } from 'react'
import { Alert, Button, Group, Select, Stack, Text, TextInput } from '@mantine/core'
import { StackLoading } from './stack-loading'

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(path, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'The stack request failed.')
  return response.json() as Promise<T>
}
export type ApprovedTarget = { id: string; projectName: string; agentId: string; deviceName: string }
type Device = { agentId: string; name: string; managedRoot: string; message?: string }

/** The New stack form: a draft with a name, project and device. Creating it starts nothing. */
export function NewStack({ approved, onOpen }: { approved: ApprovedTarget[]; onOpen: (id: string) => void }) {
  const [devices, setDevices] = useState<Device[] | null>(null)
  const [name, setName] = useState('')
  const [id, setId] = useState('')
  const [project, setProject] = useState('')
  const [agent, setAgent] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [approvedId, setApprovedId] = useState<string | null>(null)
  useEffect(() => { let active = true; void request<{ devices: Device[] }>('/api/stacks/setup').then((value) => { if (active) setDevices(value.devices) }).catch((cause: Error) => { if (active) setError(cause.message) }); return () => { active = false } }, [])
  const selected = devices?.find((device) => device.agentId === agent)
  const valid = /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(id) && id.length <= 63 && /^[a-z0-9][a-z0-9_-]{0,62}$/.test(project) && !!name.trim() && !!selected?.managedRoot
  return <Stack>{error && <Alert color="red">{error}</Alert>}<Text size="sm" c="dimmed">Create a draft, choose its device, then edit Compose before saving or deploying. Creating a stack starts no containers.</Text><TextInput label="Stack name" placeholder="Home media" maxLength={120} value={name} disabled={busy} onChange={(event) => { const next = event.currentTarget.value; const slug = next.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 63); const previousSlug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 63); if (!id || id === previousSlug) setId(slug); if (!project || project === previousSlug) setProject(slug); setName(next) }} /><Group grow><TextInput label="Stack ID" description="Lowercase letters, numbers and hyphens" value={id} disabled={busy} onChange={(event) => setId(event.currentTarget.value)} /><TextInput label="Compose project name" description="Must be unused on the selected device" value={project} disabled={busy} onChange={(event) => setProject(event.currentTarget.value)} /></Group>{!devices && !error && <StackLoading editor />}<Select label="Device" placeholder="Choose a device" searchable value={agent} disabled={busy} onChange={setAgent} data={(devices ?? []).map((device) => ({ value: device.agentId, label: device.name + (device.managedRoot ? '' : ' — unavailable'), disabled: !device.managedRoot }))} />{devices?.filter((device) => device.message).map((device) => <Text size="xs" c="dimmed" key={device.agentId}>{device.name}: {device.message}</Text>)}<TextInput label="Device folder" description="Vigara uses a new folder inside this device’s managed stack directory." readOnly value={selected?.managedRoot ? selected.managedRoot.replace(/\/$/, '') + '/' + id : ''} /><Button loading={busy} disabled={!valid} onClick={() => { setBusy(true); setError(''); void request<{ id: string }>('/api/stacks', 'POST', { id, name, projectName: project, agentId: agent }).then((result) => onOpen(result.id)).catch((cause: Error) => setError(cause.message)).finally(() => setBusy(false)) }}>Create draft</Button>{!!approved.length && <><Text size="sm" fw={500} mt="sm">Or use an existing approved target</Text><Select label="Approved target" value={approvedId} onChange={setApprovedId} data={approved.map((target) => ({ value: target.id, label: target.id + ' · ' + (target.deviceName || target.agentId) }))} /><Button variant="default" disabled={!approvedId || busy} onClick={() => { if (approvedId) onOpen(approvedId) }}>Open approved target</Button></>}</Stack>
}
