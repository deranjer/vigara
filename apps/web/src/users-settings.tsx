import { useEffect, useState, type FormEvent } from 'react'
import { Alert, Button, Drawer, Group, PasswordInput, Select, Stack, Table, Text, TextInput } from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { PasswordWhitespaceWarning } from './password-warning'
import { relativeTime } from './overview-utils'
import { Panel } from './ui'
import { GrantEditor } from './users'
import type { HostEntry } from './container-utils'
import { resourceRequest } from './resource-access'

type ManagedUser = { id: string; username: string; displayName: string; role: string; createdAt: string }

const roles = [
  { value: 'restricted', label: 'Selected resources', detail: 'Only the stacks or containers you share, with optional logs, restart, recreate, and pull & recreate.' },
  { value: 'viewer', label: 'Viewer', detail: 'Sees devices, containers, backups and alerts.' },
  { value: 'operator', label: 'Operator', detail: 'Also starts, stops and runs commands in containers and on devices.' },
  { value: 'admin', label: 'Admin', detail: 'Everything, including stacks, secrets, users and server settings.' },
]

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(path, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 300) || `Request failed (${response.status}).`)
  return await response.json() as T
}

/** Who can sign in, and what each person may do. */
export function UsersSettings({ currentUserId }: { currentUserId: string }) {
  const [users, setUsers] = useState<ManagedUser[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<ManagedUser | null>(null)
  const [hosts, setHosts] = useState<HostEntry[]>([])
  const load = () => request<ManagedUser[]>('/api/users').then((list) => { setUsers(list); setError('') })
  useEffect(() => { void load().catch((e: unknown) => setError(e instanceof Error ? e.message : 'Users could not be loaded.')) }, [])
  useEffect(() => {
    const controller = new AbortController()
    void resourceRequest<{ hosts: HostEntry[] }>('/api/containers', 'GET', undefined, controller.signal).then((fleet) => setHosts(fleet.hosts)).catch((cause: unknown) => { if (!controller.signal.aborted) setError(String(cause)) })
    return () => controller.abort()
  }, [])

  async function setRole(user: ManagedUser, role: string) {
    if (role === user.role) return
    setBusy(user.id); setError('')
    try { const updated = await request<ManagedUser>(`/api/users/${encodeURIComponent(user.id)}`, 'PATCH', { role }); await load(); if (updated.role === 'restricted') setEditing(updated) }
    catch (e) { setError(e instanceof Error ? e.message : 'The role could not be changed.') }
    finally { setBusy('') }
  }

  return <Stack gap="md">
    <Group justify="space-between" align="flex-end">
      <Text c="dimmed" size="sm" maw={560}>Share selected stacks and containers, or assign a global role. Selected-resources accounts start with no access; there must always be at least one admin.</Text>
      <Button leftSection={<IconPlus size={14} />} onClick={() => setAdding(true)}>Add user</Button>
    </Group>
    {error && <Alert color="red" withCloseButton onClose={() => setError('')}>{error}</Alert>}
    <Panel flush>
      {!users ? <Text c="dimmed" size="sm" p="md">Loading…</Text> : <div className="table-scroll"><Table className="vg-table" verticalSpacing={8}>
        <Table.Thead><Table.Tr><Table.Th>User</Table.Th><Table.Th>Access type</Table.Th><Table.Th>Added</Table.Th><Table.Th /></Table.Tr></Table.Thead>
        <Table.Tbody>{users.map((user) => <Table.Tr key={user.id}>
          <Table.Td><b>{user.displayName || user.username}</b>{user.id === currentUserId && <Text span size="xs" c="dimmed"> · you</Text>}<Text size="xs" c="dimmed" className="vg-mono">{user.username}</Text></Table.Td>
          <Table.Td w={180}><Select size="xs" aria-label={`Role for ${user.username}`} data={roles.map(({ value, label }) => ({ value, label }))} value={user.role} allowDeselect={false} disabled={busy === user.id} onChange={(v) => { if (v) void setRole(user, v) }} /></Table.Td>
          <Table.Td className="vg-dim" title={new Date(user.createdAt).toLocaleString()}>{relativeTime(user.createdAt)}</Table.Td>
          <Table.Td>{user.role === 'restricted' && <Button size="xs" variant="light" onClick={() => setEditing(user)}>Edit resource access</Button>}</Table.Td>
        </Table.Tr>)}</Table.Tbody>
      </Table></div>}
    </Panel>
    <Panel title="Roles">
      <dl className="vg-kv">{roles.map((role) => <div key={role.value}><dt>{role.label}</dt><dd>{role.detail}</dd></div>)}</dl>
    </Panel>
    <Text size="xs" c="dimmed">Saving resource access signs the affected user out. Changing account type clears previous resource selections.</Text>
    <AddUser opened={adding} onClose={() => setAdding(false)} onAdded={(created) => { setAdding(false); if (created.role === 'restricted') setEditing(created); void load().catch(() => undefined) }} />
    {editing && <GrantEditor key={editing.id} user={editing} hosts={hosts} close={() => setEditing(null)} />}
  </Stack>
}

function AddUser({ opened, onClose, onAdded }: { opened: boolean; onClose: () => void; onAdded: (user: ManagedUser) => void }) {
  const [username, setUsername] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('restricted')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => { if (opened) { setUsername(''); setDisplayName(''); setPassword(''); setRole('restricted'); setError('') } }, [opened])
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true); setError('')
    try { const created = await request<ManagedUser>('/api/users', 'POST', { username: username.trim(), displayName: displayName.trim(), password, role }); onAdded(created) }
    catch (e) { setError(e instanceof Error ? e.message : 'The user could not be added.') }
    finally { setBusy(false) }
  }
  return <Drawer opened={opened} onClose={() => { if (!busy) onClose() }} position="right" size="md" title="Add user">
    <form onSubmit={(e) => void submit(e)}><Stack gap="md">
      {error && <Alert color="red">{error}</Alert>}
      <TextInput label="Username" value={username} onChange={(e) => setUsername(e.currentTarget.value)} required autoComplete="off" disabled={busy} />
      <TextInput label="Display name" value={displayName} onChange={(e) => setDisplayName(e.currentTarget.value)} autoComplete="off" disabled={busy} />
      <PasswordInput label="Password" description="At least 12 characters. Share it with them; they can change it in Settings › Account." value={password} onChange={(e) => setPassword(e.currentTarget.value)} required minLength={12} maxLength={1024} autoComplete="new-password" disabled={busy} />
      <PasswordWhitespaceWarning value={password} />
      <Select label="Role" data={roles.map(({ value, label }) => ({ value, label }))} value={role} allowDeselect={false} onChange={(v) => v && setRole(v)} description={roles.find((r) => r.value === role)?.detail} disabled={busy} />
      <Group justify="flex-end"><Button variant="default" onClick={onClose} disabled={busy}>Cancel</Button><Button type="submit" loading={busy} disabled={!username.trim() || password.length < 12}>Add user</Button></Group>
    </Stack></form>
  </Drawer>
}
