import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Code, Group, Loader, Modal, Select, Stack, Tabs, Text, Textarea, TextInput, Title } from '@mantine/core'
import { route as routeOf, type Route } from './route'

import { StackList } from './stack-management'
import { isComposeFile } from './compose-editor-utils'

const ComposeEditor = lazy(() => import('./compose-editor'))

type Files = Record<string, string>
type Snapshot = { id: string; commit: string; files: Files }
type Draft = { base: string; files: Files | null; version: number }
type Detail = { projectName: string; snapshot: Snapshot; draft: Draft; branch: string; assigned: boolean; agentId: string; digest: string }
type Revision = { commit: string; message: string; at: string }
type Deployment = { id: string; commit: string; digest: string; status: string; stage: string; message: string; createdAt: string }
type Plan = { token: string; projectName: string; services: string[] }

async function request<T>(url: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(url, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 400) || 'The stack request failed.')
  return response.json() as Promise<T>
}
const equal = (a: Files, b: Files) => JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort())
const short = (commit: string) => commit ? commit.slice(0, 8) : 'Not saved'
const base = (id: string) => `/api/stacks/${encodeURIComponent(id)}`

function starter(id: string, projectName: string): Files {
  return {
    'vigara-stack.yaml': `apiVersion: vigara.io/v1\nkind: Stack\nstackId: ${id}\ndisplayName: ${id}\nprojectName: ${projectName}\ncompose:\n  files:\n    - compose.yaml\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n`,
    'compose.yaml': 'services:\n  web:\n    image: nginx:stable\n    ports:\n      - "8080:80"\n',
  }
}

export function Stacks({ route, role, onNavigate }: { route: Route; role: string; onNavigate: (route: Route) => void }) {
 if (role !== 'admin') return <Alert title="Stack access requires an administrator">Sign in as an administrator to manage Compose stacks.</Alert>
 if (route.id) return <StackEditor key={route.id} id={route.id} projectName={route.id} onBack={() => onNavigate(routeOf('stacks'))} />
 return <StackList onOpen={(id) => onNavigate(routeOf('stacks',id))} />
}

function StackEditor({ id, projectName, onBack }: { id: string; projectName: string; onBack: () => void }) {
  const [detail, setDetail] = useState<Detail | null>(null)
  const [files, setFiles] = useState<Files>({})
  const [draft, setDraft] = useState<Draft>({ base: '', files: null, version: 0 })
  const [file, setFile] = useState<string | null>('compose.yaml')
  const [tab, setTab] = useState<string | null>('files')
  const [history, setHistory] = useState<Revision[]>([])
  const [jobs, setJobs] = useState<Deployment[]>([])
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [newPath, setNewPath] = useState('')
  const [environment, setEnvironment] = useState('')
  const [environmentPath, setEnvironmentPath] = useState('secrets/runtime.env.sops')
  const [plan, setPlan] = useState<Plan | null>(null)
  const [pull, setPull] = useState(false)
  const [compare, setCompare] = useState(false)
  const [discarding, setDiscarding] = useState(false)
  const [remote, setRemote] = useState<Snapshot | null>(null)
  const filesRef = useRef(files)
  filesRef.current = files
  const dirty = detail ? !equal(files, detail.snapshot.files) : false
  const unpersisted = draft.files ? !equal(files, draft.files) : dirty

  async function load(initial: boolean) {
    const value = await request<Detail>(base(id))
    if (initial) {
      setDetail(value); setDraft(value.draft)
      const next = value.draft.files ?? (Object.keys(value.snapshot.files).length ? value.snapshot.files : starter(id, value.projectName || projectName))
      setFiles(next); setFile(next['compose.yaml'] !== undefined ? 'compose.yaml' : Object.keys(next)[0] ?? null)
    } else {
      setRemote(value.snapshot)
      setNotice(value.snapshot.commit !== detail?.snapshot.commit ? 'Fetched the latest saved version. Your editor contents are preserved; compare the versions before replacing your draft.' : 'The saved version is up to date. Your editor contents are preserved.')
    }
  }
  useEffect(() => {
    let active = true
    void request<Detail>(base(id)).then((value) => {
      if (!active) return
      setDetail(value); setDraft(value.draft)
      const next = value.draft.files ?? (Object.keys(value.snapshot.files).length ? value.snapshot.files : starter(id, value.projectName || projectName))
      setFiles(next); setFile(next['compose.yaml'] !== undefined ? 'compose.yaml' : Object.keys(next)[0] ?? null)
      if (value.draft.files) setNotice('Restored your saved draft. Running services are unchanged.')
    }).catch((cause: Error) => { if (active) setError(cause.message) }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
    // The editor is keyed by stack ID; later fetches never overwrite its draft.
  }, [id])
  useEffect(() => {
    let active = true
    async function poll() { try { const value = await request<Deployment[]>(`${base(id)}/deployments`); if (active) setJobs(value) } catch (cause) { if (active) setError(cause instanceof Error ? cause.message : 'Could not load deployments.') } }
    void poll(); const timer = window.setInterval(() => void poll(), 3000)
    return () => { active = false; window.clearInterval(timer) }
  }, [id])
  useEffect(() => {
    if (!dirty && !unpersisted) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault() }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty, unpersisted])

  async function action(work: () => Promise<void>) { setBusy(true); setError(''); setNotice(''); try { await work() } catch (cause) { setError(cause instanceof Error ? cause.message : 'The operation failed.') } finally { setBusy(false) } }
  async function persistDraft() {
    const next = await request<Draft>(`${base(id)}/draft`, 'PUT', { base: draft.base || detail?.snapshot.commit || '', files: filesRef.current, version: draft.version })
    setDraft(next); return next
  }
  async function save() {
    const next = await persistDraft()
    const result = await request<{ state: string; commit: string }>(`${base(id)}/save`, 'POST', { version: next.version })
    if (result.state === 'pendingPush') { setNotice('Pending sync: your draft is stored, but Git has not confirmed the push. Retry Save to sync it. This version cannot be deployed yet.'); return }
    await load(true); setPlan(null); setNotice(`Saved revision ${short(result.commit)} to ${detail?.branch}. Running services are unchanged.`)
  }
  if (loading) return <Loader />
  if (!detail) return <Stack><Alert color="red">{error}</Alert><Button variant="default" onClick={onBack}>All stacks</Button></Stack>
  const activeJob = jobs.find((job) => ['queued', 'running', 'unknown'].includes(job.status))
  const deployed = jobs.find((job) => job.status === 'succeeded')
  const selectedContent = file ? files[file] ?? '' : ''
  const encrypted = file?.endsWith('.sops') ?? false
  return <section className="devices-page"><Stack gap="lg">
    <Group justify="space-between"><div><Text size="sm" c="dimmed" tt="uppercase">Compose stack</Text><Title order={1}>{id}</Title><Text c="dimmed">Branch {detail.branch} · Project {detail.projectName || projectName}</Text></div><Button variant="default" disabled={busy} onClick={() => { if (unpersisted) { void action(async () => { await persistDraft(); onBack() }) } else onBack() }}>← All stacks</Button></Group>
    {error && <Alert color="red" title="Stack operation could not complete" withCloseButton onClose={() => setError('')}>{error}</Alert>}
    {notice && <Alert color="blue" withCloseButton onClose={() => setNotice('')}>{notice}</Alert>}
    <Group gap="xs"><Badge variant="light">Saved: {short(detail.snapshot.commit)}</Badge><Badge color="gray" variant="light">Last deployed: {deployed ? short(deployed.commit) : 'Never'}</Badge>{dirty && <Badge color="yellow">Draft changes</Badge>}{deployed && deployed.digest !== detail.digest && <Badge color="orange">Saved changes waiting to deploy</Badge>}</Group>
    {!detail.assigned && <Alert color="yellow">Configure an approved device and project folder for this stack before saving or deploying.</Alert>}
    <Tabs value={tab} onChange={(value) => { setTab(value); if (value === 'history') void action(async () => setHistory(await request<Revision[]>(`${base(id)}/history`))) }}>
      <Tabs.List mb="lg"><Tabs.Tab value="files">Compose files</Tabs.Tab><Tabs.Tab value="environment">Environment</Tabs.Tab><Tabs.Tab value="history">Revision history</Tabs.Tab><Tabs.Tab value="deployments">Deployments</Tabs.Tab></Tabs.List>
      <Tabs.Panel value="files"><Stack>
        <Group justify="space-between"><Select aria-label="Stack file" value={file} data={Object.keys(files).sort()} onChange={setFile} w={320} /><Checkbox label="Compare with saved version" checked={compare} onChange={(event) => setCompare(event.currentTarget.checked)} /></Group>
        {encrypted ? <Alert>Encrypted environment files are preserved without displaying their values. Use the Environment tab to replace their contents.</Alert> : file && <Suspense fallback={<Group><Loader size="sm" /><Text size="sm">Loading editor…</Text></Group>}>
          <ComposeEditor stackId={id} path={file} value={selectedContent} original={(remote ?? detail.snapshot).files[file] ?? ''} compare={compare} composeFile={isComposeFile(file, files['vigara-stack.yaml'] ?? '')} disabled={busy} onChange={(value) => setFiles((current) => ({ ...current, [file]: value }))} onSave={() => { if (!busy && dirty && detail.assigned) void action(save) }} />
        </Suspense>}
        <Group align="end"><TextInput label="New file path" placeholder="config/app.yaml" value={newPath} onChange={(event) => setNewPath(event.currentTarget.value)} /><Button variant="default" disabled={busy || !newPath || files[newPath] !== undefined || newPath.endsWith('.env') || newPath === '.env'} onClick={() => { setFiles({ ...files, [newPath]: '' }); setFile(newPath); setNewPath('') }}>Add file</Button>{file && file !== 'vigara-stack.yaml' && <Button color="red" variant="subtle" disabled={busy} onClick={() => { const next = { ...files }; delete next[file]; setFiles(next); setFile(Object.keys(next)[0] ?? null) }}>Remove from draft</Button>}</Group>
        <Text size="xs" c="dimmed">Keep passwords and tokens in encrypted environment inputs. YAML is preserved as written. File removal changes Git; this first runner leaves old device files in place.</Text>
      </Stack></Tabs.Panel>
      <Tabs.Panel value="environment"><Card withBorder padding="lg"><Stack><Title order={3}>Replace encrypted environment</Title><Text size="sm" c="dimmed">Enter the complete dotenv contents. Vigara encrypts them before storage; existing values are never returned to this editor. The encrypted input and recipient references are added to your stack manifest automatically.</Text><TextInput label="Encrypted file path" value={environmentPath} onChange={(event) => setEnvironmentPath(event.currentTarget.value)} /><Textarea label="New dotenv contents" placeholder={'APP_SETTING=value\nAPP_PASSWORD=...'} value={environment} onChange={(event) => setEnvironment(event.currentTarget.value)} minRows={8} spellCheck={false} autoComplete="off" /><Button disabled={busy || !environment} onClick={() => void action(async () => { const value = await request<{ path: string; ciphertext: string; manifest: string }>(`${base(id)}/environment`, 'POST', { path: environmentPath, value: environment, manifest: files['vigara-stack.yaml'] ?? '' }); setFiles((current) => ({ ...current, [value.path]: value.ciphertext, 'vigara-stack.yaml': value.manifest })); setEnvironment(''); setNotice('Environment encrypted and added to your draft. Save the revision when ready.'); })}>Encrypt into draft</Button></Stack></Card></Tabs.Panel>
      <Tabs.Panel value="history"><Stack>{!history.length && <Text c="dimmed">No saved revisions yet.</Text>}{history.map((revision) => <Card key={revision.commit} withBorder><Group justify="space-between"><div><Text fw={600}>{revision.message}</Text><Text size="xs" c="dimmed">{short(revision.commit)} · {new Date(revision.at).toLocaleString()}</Text></div><Button variant="light" disabled={busy} onClick={() => void action(async () => { const value = await request<Detail>(`${base(id)}?commit=${revision.commit}`); setFiles(value.snapshot.files); setPlan(null); setTab('files'); setNotice(`Loaded revision ${short(revision.commit)} into your draft. Save creates a new commit before deployment.`) })}>Restore into draft</Button></Group></Card>)}</Stack></Tabs.Panel>
      <Tabs.Panel value="deployments"><Stack>{!jobs.length && <Text c="dimmed">No deployments yet.</Text>}{jobs.map((job) => <Card withBorder key={job.id}><Group justify="space-between"><Text fw={600}>Revision {short(job.commit)}</Text><Badge color={job.status === 'succeeded' ? 'green' : job.status === 'failed' ? 'red' : 'yellow'}>{job.status}</Badge></Group><Text size="sm" mt="xs">{job.stage} · {job.message || 'Waiting for the device.'}</Text><Text size="xs" c="dimmed" mt="xs">{new Date(job.createdAt).toLocaleString()}</Text>{job.status === 'unknown' && <Button mt="sm" variant="light" disabled={busy} onClick={() => void action(async () => { const value = await request<{ message: string }>(`${base(id)}/deployments/${job.id}/reconcile`, 'POST', {}); setNotice(value.message) })}>Check device result</Button>}</Card>)}</Stack></Tabs.Panel>
    </Tabs>
    <Group justify="space-between"><Group><Button variant="subtle" color="red" disabled={busy || (!dirty && !draft.files)} onClick={() => setDiscarding(true)}>Discard draft</Button><Button variant="default" disabled={busy || !unpersisted} onClick={() => void action(async () => { await persistDraft(); setNotice('Draft stored in Vigara. Git and running services are unchanged.') })}>Keep draft</Button><Button loading={busy} disabled={!dirty || !detail.assigned} onClick={() => void action(save)}>Save to {detail.branch}</Button><Button variant="subtle" disabled={busy} onClick={() => void action(async () => { await load(false); setCompare(true) })}>Fetch saved version</Button></Group><Button variant="light" disabled={busy || !detail.snapshot.commit || !!activeJob || !detail.assigned || dirty} onClick={() => void action(async () => setPlan(await request<Plan>(`${base(id)}/plan`, 'POST', { commit: detail.snapshot.commit })))}>Preview deployment</Button></Group>
    {dirty && <Text size="sm" c="dimmed">Save your draft before deploying. Saving does not restart services.</Text>}
    {remote && <Alert title="Compare fetched version"><Text size="sm">Fetched {short(remote.commit)}. Review it beside your draft above. Keeping your draft against this version may overwrite changes, so resolve differences before saving.</Text><Button mt="sm" variant="light" disabled={busy} onClick={() => { setDetail({ ...detail, snapshot: remote }); setDraft({ ...draft, base: remote.commit }); setRemote(null); setNotice('Draft now uses the fetched version as its base. Review your edits before saving.') }}>Use fetched version as draft base</Button></Alert>}
    <Modal opened={discarding} onClose={() => { if (!busy) setDiscarding(false) }} title="Discard draft?" centered><Stack><Text>Discard your edits and stored draft for this stack? Saved Git revisions and running services are retained.</Text><Button color="red" loading={busy} onClick={() => void action(async () => { if (draft.files) await request(base(id) + '/draft', 'DELETE', { version: draft.version }); const next = Object.keys(detail.snapshot.files).length ? detail.snapshot.files : starter(id, detail.projectName || projectName); setFiles(next); setDraft({ base: detail.snapshot.commit, files: null, version: 0 }); setFile(next['compose.yaml'] !== undefined ? 'compose.yaml' : Object.keys(next)[0] ?? null); setPlan(null); setDiscarding(false); setNotice('Stored draft discarded.'); })}>Discard draft</Button></Stack></Modal>
    <Modal opened={!!plan} onClose={() => { if (!busy) setPlan(null) }} title="Deploy saved revision" centered><Stack><Text>Apply revision <Code>{short(detail.snapshot.commit)}</Code> to project <Code>{plan?.projectName}</Code>?</Text><Text size="sm">Device: {detail.agentId}</Text><Text size="sm">Services: {plan?.services.join(', ')}</Text><Text size="sm" c="dimmed">Changed services may restart. Existing data volumes are retained. Deploying an older configuration does not undo application data migrations.</Text><Checkbox label="Download newer images before deployment" checked={pull} onChange={(event) => setPull(event.currentTarget.checked)} />{error && <Alert color="red">{error}</Alert>}<Button loading={busy} onClick={() => void action(async () => { await request(`${base(id)}/deploy`, 'POST', { commit: detail.snapshot.commit, token: plan?.token, pull }); setPlan(null); setTab('deployments'); setNotice('Deployment queued. You can leave this page and return to its status.'); })}>Deploy revision</Button></Stack></Modal>
  </Stack></section>
}
