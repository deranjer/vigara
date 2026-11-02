import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Group, Loader, Select, Stack, Text, Textarea, TextInput } from '@mantine/core'

type Device = { agentId: string; name: string; managedRoot: string; message?: string }
type Candidate = { directory: string; projectName: string; token: string; state: string; containers: number; composeFiles: string[]; environmentFiles: string[]; issues: string[]; imported: boolean; storage?: string[] }
type Discovery = { root: string; candidates: Candidate[] }
type Preview = { candidate: Candidate; files: Record<string, string> }

async function request<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, { method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', cache: 'no-store', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'The existing stack request failed.')
  return response.json() as Promise<T>
}

export function StackImport({ onClose, onImported }: { onClose: () => void; onImported: (id: string) => void }) {
  const [devices, setDevices] = useState<Device[] | null>(null)
  const [agent, setAgent] = useState<string | null>(null)
  const [scan, setScan] = useState(0)
  const [discovery, setDiscovery] = useState<Discovery | null>(null)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [name, setName] = useState('')
  const [id, setID] = useState('')
  const [reviewed, setReviewed] = useState(false)
  const [recoverySaved, setRecoverySaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [pendingID, setPendingID] = useState('')
  useEffect(() => {
    let active = true
    void request<{ devices: Device[] }>('/api/stacks/setup').then((result) => { if (active) { setDevices(result.devices); setAgent(result.devices.find((device) => device.managedRoot)?.agentId || null) } }).catch((cause: Error) => { if (active) setError(cause.message) })
    return () => { active = false }
  }, [])
  useEffect(() => {
    let active = true
    setDiscovery(null); setPreview(null); setReviewed(false); setRecoverySaved(false)
    if (!agent) return () => { active = false }
    setBusy(true); setError('')
    void request<Discovery>('/api/stacks/imports/discover', { agentId: agent }).then((result) => { if (active) setDiscovery(result) }).catch((cause: Error) => { if (active) setError(cause.message) }).finally(() => { if (active) setBusy(false) })
    return () => { active = false }
  }, [agent, scan])
  async function review(candidate: Candidate) {
    setBusy(true); setError(''); setReviewed(false); setRecoverySaved(false)
    try {
      const result = await request<Preview>('/api/stacks/imports/preview', { agentId: agent, directory: candidate.directory, token: candidate.token })
      setPreview(result); setName(candidate.projectName); setID(candidate.directory.split('/').pop()?.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 63) || '')
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not review this stack.') } finally { setBusy(false) }
  }
  async function importStack() {
    if (!preview || !agent) return
    setBusy(true); setError('')
    try {
      const result = await request<{ id: string; state: string }>('/api/stacks/imports', { agentId: agent, directory: preview.candidate.directory, token: preview.candidate.token, id, name: name.trim(), reviewed, recoverySaved })
      if (result.state === 'saved') onImported(result.id)
      else setPendingID(result.id)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not import this stack.') } finally { setBusy(false) }
  }
  return <Stack>
    <Text size="sm">Import an existing Compose project into Git without restarting its containers. Its project name, original folder, networks, and data mounts stay in place. Deployment remains a separate action.</Text>
    {error && <Alert color="red">{error}</Alert>}
    {pendingID && <Alert color="yellow" title="Imported; Git save needs a retry"><Stack gap="xs"><Text size="sm">The existing deployment is registered and its encrypted draft is safe in Vigara. Open the draft and retry Save to Git after resolving the repository connection or branch rules. No containers were restarted.</Text><Button onClick={() => onImported(pendingID)}>Open imported draft</Button></Stack></Alert>}
    {!devices && !error && <Loader size="sm" />}
    {devices && <Select label="Device" value={agent} onChange={setAgent} disabled={busy} data={devices.map((device) => ({ value: device.agentId, label: device.name + (device.managedRoot ? '' : ' — update agent or enable deployment'), disabled: !device.managedRoot }))} />}
    {devices && !devices.some((device) => device.managedRoot) && <Alert color="yellow">Enroll the host with Docker deployment enabled, or reinstall its agent with the current version. Import scans only that host’s configured Compose root.</Alert>}
    {busy && !preview && <Loader size="sm" />}
    {discovery && !preview && <><Group justify="space-between"><Text size="sm">Compose root: {discovery.root}</Text><Button size="xs" variant="default" disabled={busy} onClick={() => setScan((value) => value + 1)}>Rescan</Button></Group>{!discovery.candidates.length && <Text c="dimmed" size="sm">No existing Compose projects matched Docker’s running or stopped containers in this root.</Text>}{discovery.candidates.map((candidate) => <Card withBorder key={candidate.directory}><Stack gap="xs"><Group justify="space-between"><Text fw={600}>{candidate.projectName}</Text><Badge>{candidate.imported ? 'Already registered' : candidate.state}</Badge></Group><Text size="sm">{candidate.directory} · {candidate.containers} containers</Text>{candidate.issues.map((issue, index) => <Text key={index} size="sm" c="orange">{issue}</Text>)}<Button variant="light" disabled={busy || candidate.imported || candidate.issues.length > 0} onClick={() => void review(candidate)}>Review import</Button></Stack></Card>)}</>}
    {preview && !pendingID && <>
      <Text fw={600}>Keep project name: {preview.candidate.projectName}</Text>
      <Text size="sm">Original folder: {discovery?.root}/{preview.candidate.directory}</Text>
      {preview.candidate.storage?.map((mount, index) => <Text size="xs" key={index}>{mount}</Text>)}
      <TextInput label="Stack name" value={name} onChange={(event) => setName(event.currentTarget.value)} disabled={busy} maxLength={120} />
      <TextInput label="Stack ID" description="A new ID for this existing deployment. Its Docker project name stays the same." value={id} onChange={(event) => setID(event.currentTarget.value)} disabled={busy} maxLength={63} />
      {Object.entries(preview.files).map(([file, content]) => <Textarea key={file} label={file} value={content} readOnly autosize minRows={8} maxRows={20} styles={{ input: { fontFamily: 'monospace' } }} />)}
      <Text size="sm">Environment files will be encrypted before saving to Git: {[...new Set(preview.candidate.environmentFiles)].join(', ') || 'none'}. Their contents are not shown here.</Text>
      <Checkbox checked={reviewed} disabled={busy} onChange={(event) => setReviewed(event.currentTarget.checked)} label="I reviewed the Compose files for inline secrets and verified the existing storage paths." />
      <Button component="a" href="/api/stacks/imports/recovery" download variant="default">Download recovery key</Button>
      <Checkbox checked={recoverySaved} disabled={busy} onChange={(event) => setRecoverySaved(event.currentTarget.checked)} label="I saved the recovery key securely outside this server." />
      <Group justify="space-between"><Button variant="default" disabled={busy} onClick={() => { setPreview(null); setReviewed(false); setRecoverySaved(false) }}>Back to projects</Button><Button loading={busy} disabled={!reviewed || !recoverySaved || !name.trim() || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(id)} onClick={() => void importStack()}>Import and save to Git</Button></Group>
    </>}
    <Button variant="subtle" disabled={busy} onClick={onClose}>Close</Button>
  </Stack>
}
