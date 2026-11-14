import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Group, Modal, Select, Stack, Text } from '@mantine/core'
import type { HostEntry } from './container-utils'
import { emptyGrant, resourceChoices, resourceRequest, targetKey, type ResourceGrant } from './resource-access'

type User = { id: string; username: string; displayName: string; role: string }

export function GrantEditor({ user, hosts, close }: { user: User; hosts: HostEntry[]; close: () => void }) {
  const [grants, setGrants] = useState<ResourceGrant[]>([])
  const [error, setError] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const choices = resourceChoices(hosts, grants)
  useEffect(() => {
    const controller = new AbortController()
    void resourceRequest<ResourceGrant[]>(`/api/users/${user.id}/resources`, 'GET', undefined, controller.signal).then((result) => { setGrants(result); setLoaded(true) }).catch((cause: unknown) => { if (!controller.signal.aborted) setError(String(cause)) })
    return () => controller.abort()
  }, [user.id])
  function update(index: number, patch: Partial<ResourceGrant>) { setGrants((current) => current.map((grant, i) => i === index ? { ...grant, ...patch } : grant)) }
  async function save() {
    setBusy(true); setError('')
    try { await resourceRequest(`/api/users/${user.id}/resources`, 'PUT', { grants }); close() } catch (cause) { setError(String(cause)) } finally { setBusy(false) }
  }
  return <Modal title={`Resource access · ${user.displayName || user.username}`} opened onClose={() => { if (!busy) close() }} size="lg"><Stack>
    <Text size="sm">Select an entire stack, one Compose service, or a standalone container. Stack and service access follows replacements. Standalone access is tied to its current container ID.</Text>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    <Group align="end"><Select label="Resource" searchable data={choices.filter((choice) => !grants.some((grant) => targetKey(grant) === choice.value)).map(({ value, label }) => ({ value, label }))} value={selected} onChange={setSelected} style={{ flex: 1 }} disabled={!loaded || busy} />
      <Button disabled={!loaded || !selected || busy} onClick={() => { const choice = choices.find((entry) => entry.value === selected); if (choice) setGrants([...grants, emptyGrant(choice.target)]); setSelected(null) }}>Add</Button></Group>
    {loaded && !grants.length && <Alert color="gray">This account has no resource access.</Alert>}
    {grants.map((grant, index) => <Card key={targetKey(grant)} withBorder><Stack gap="sm">
      <Group justify="space-between"><Text fw={600} style={{ overflowWrap: 'anywhere' }}>{choices.find((choice) => choice.value === targetKey(grant))?.label}</Text><Button size="xs" variant="subtle" color="red" disabled={busy} onClick={() => setGrants(grants.filter((_, i) => i !== index))}>Remove</Button></Group>
      <Group><Badge variant="light">View included</Badge><Button size="xs" variant="default" disabled={busy} onClick={() => update(index, { logs: false, restart: false, recreate: false, pullRecreate: false })}>Read only</Button><Button size="xs" variant="default" disabled={busy} onClick={() => update(index, { restart: true, recreate: true, pullRecreate: true })}>Operator preset</Button></Group>
      <Group>{(['logs', 'restart', 'recreate', 'pullRecreate'] as const).map((permission) => <Checkbox key={permission} label={{ logs: 'Read raw logs', restart: 'Restart', recreate: 'Recreate', pullRecreate: 'Pull & recreate' }[permission]} checked={grant[permission]} disabled={busy} onChange={(event) => update(index, { [permission]: event.currentTarget.checked })} />)}</Group>
    </Stack></Card>)}
    <Text size="xs" c="dimmed">Raw logs can contain private values. Shared cards can be viewed without granting raw-log access. Shells, files, Compose source, secrets, and deletion remain unavailable.</Text>
    <Group justify="flex-end"><Button variant="default" disabled={busy} onClick={close}>Cancel</Button><Button loading={busy} disabled={!loaded} onClick={() => void save()}>Save access</Button></Group>
  </Stack></Modal>
}
