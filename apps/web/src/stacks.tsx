import { StackUpdatePolicy } from './image-updates'
import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Code, Group, Modal, Select, Stack, Tabs, Text, TextInput, Title } from '@mantine/core'
import { route as routeOf, type Route } from './route'

import { StackList } from './stack-management'
import { isComposeFile } from './compose-editor-utils'
import { SecretEditor } from './secret-editor'
import { StackDiff } from './stack-diff'
import { StackLoading } from './stack-loading'
import { StackContainersTab, StackRuntime } from './stack-runtime'
import type { StackRuntimeStatus } from './stack-list-utils'
import { StackImageTags } from './stack-image-tags'
import { setServiceImage } from './stack-image-utils'

const ComposeEditor = lazy(() => import('./compose-editor'))

type Files = Record<string, string>
type Snapshot = { id: string; commit: string; files: Files }
type Draft = { digest?: string; base: string; files: Files | null; version: number }
type Detail = { projectName: string; snapshot: Snapshot; draft: Draft; branch: string; assigned: boolean; agentId: string; digest: string }
type Revision = { commit: string; message: string; at: string }
type Deployment = { id: string; commit: string; digest: string; status: string; stage: string; message: string; createdAt: string }
type Plan = { images: Record<string, string>; tracking?: Record<string, string>; token: string; projectName: string; services: string[] }

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
 if (route.id) return <StackEditor key={route.id} id={route.id} projectName={route.id} role={role} onNavigate={onNavigate} onBack={() => onNavigate(routeOf('stacks'))} />
 return <StackList role={role} onOpen={(id) => onNavigate(routeOf('stacks',id))} />
}

function StackEditor({ id, projectName, onBack, role, onNavigate }: { id: string; projectName: string; onBack: () => void; role: string; onNavigate: (route: Route) => void }) {
  const [detail, setDetail] = useState<Detail | null>(null)
  const [files, setFiles] = useState<Files>({})
  const [draft, setDraft] = useState<Draft>({ base: '', files: null, version: 0 })
  const [file, setFile] = useState<string | null>('compose.yaml')
  const [tab, setTab] = useState<string | null>('files')
  const [containerStatus, setContainerStatus] = useState<StackRuntimeStatus>({ color: 'gray', label: 'Loading containers', count: '…' })
  const [history, setHistory] = useState<Revision[]>([])
  const [jobs, setJobs] = useState<Deployment[]>([])
  const [actionBusy, setBusy] = useState(false)
  const [secretBusy, setSecretBusy] = useState(false)
  const [secretEpoch, setSecretEpoch] = useState(0)
  const busy = actionBusy || secretBusy
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [newPath, setNewPath] = useState('')
  const [plan, setPlan] = useState<Plan | null>(null)
  const [pull, setPull] = useState(false)
  const [recreate, setRecreate] = useState(false)
  const [tagImages, setTagImages] = useState<Record<string, string> | null>(null)
  const [discarding, setDiscarding] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState('')
  const [skipDevice, setSkipDevice] = useState(false)
  const [remote, setRemote] = useState<Snapshot | null>(null)
  const filesRef = useRef(files)
  filesRef.current = files
  const dirty = detail ? !equal(files, detail.snapshot.files) : false
  const unpersisted = draft.files ? !equal(files, draft.files) : dirty

  async function load(initial: boolean) {
    const value = await request<Detail>(base(id))
    if (initial) {
      setDetail(value); setDraft(value.draft); setRemote(null)
      const next = value.draft.files ?? (Object.keys(value.snapshot.files).length ? value.snapshot.files : starter(id, value.projectName || projectName))
      setFiles(next); setFile(next['compose.yaml'] !== undefined ? 'compose.yaml' : Object.keys(next)[0] ?? null)
    } else {
      setRemote(value.snapshot)
      setNotice(value.snapshot.commit !== detail?.snapshot.commit ? 'Fetched the latest saved version. Your editor contents are preserved; compare the versions before replacing your draft.' : 'The saved version is up to date. Your editor contents are preserved.')
    }
    return value
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

  async function action(work: () => Promise<void>) { setSecretEpoch((epoch) => epoch + 1); setBusy(true); setError(''); setNotice(''); try { await work() } catch (cause) { setError(cause instanceof Error ? cause.message : 'The operation failed.') } finally { setBusy(false) } }
  async function persistDraft(nextFiles = filesRef.current) {
    const next = await request<Draft>(`${base(id)}/draft`, 'PUT', { base: draft.base || detail?.snapshot.commit || '', files: nextFiles, version: draft.version })
    setDraft(next); return next
  }
  async function save() {
    const next = await persistDraft()
    const result = await request<{ state: string; commit: string }>(`${base(id)}/save`, 'POST', { version: next.version })
    if (result.state === 'pendingPush') { setNotice('Pending sync: your draft is stored, but Git has not confirmed the push. Retry Save to sync it. This version cannot be deployed yet.'); return }
    await load(true); setPlan(null); setNotice(`Saved revision ${short(result.commit)} to ${detail?.branch}. Running services are unchanged.`)
  }
  async function preview(recreateStack: boolean) {
    setPull(recreateStack); setRecreate(recreateStack)
    setPlan(await request<Plan>(`${base(id)}/plan`, 'POST', { commit: detail?.snapshot.commit }))
  }
  async function selectTag(service: string, image: string) {
    const updated = setServiceImage(filesRef.current, service, image)
    setFiles(updated); filesRef.current = updated; setPlan(null)
    const next = await persistDraft(updated)
    const saved = await request<{ state: string; commit: string }>(`${base(id)}/save`, 'POST', { version: next.version })
    setTagImages(null)
    if (saved.state !== 'saved') {
      setNotice('The selected tag is stored in your draft. Retry Save to finish syncing Git before deployment.'); return
    }
    await load(true)
    setNotice(`Saved ${service}: ${image}. Review the deployment before applying it.`)
    setPull(true); setRecreate(true)
    setPlan(await request<Plan>(`${base(id)}/plan`, 'POST', { commit: saved.commit }))
  }
  if (loading) return <StackLoading />
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
    <Group gap="xs">
      <Button variant="light" loading={actionBusy} disabled={busy || !detail.snapshot.commit || !!activeJob || !detail.assigned || dirty} onClick={() => void action(() => preview(true))}>Pull and recreate stack</Button>
      <Button variant="default" disabled={busy || !detail.snapshot.commit || !!activeJob || !detail.assigned || dirty} onClick={() => void action(async () => {
        const source = await request<Plan>(`${base(id)}/plan`, 'POST', { commit: detail.snapshot.commit, sourcesOnly: true })
        setTagImages(source.tracking ?? source.images)
      })}>Browse image tags</Button>
      <Text size="xs" c="dimmed">Pull refreshes configured tags, including latest, and recreates all services. Data volumes are retained.</Text>
    </Group>
    {!detail.assigned && <Alert color="yellow">Configure an approved device and project folder for this stack before saving or deploying.</Alert>}
    {activeJob?.status === 'unknown' && <Alert color="yellow" title="Check deployment">The last deployment result could not be confirmed, so pulling and recreating is disabled. {activeJob.message || 'Open Deployments to check the device result.'}</Alert>}
    {jobs[0]?.status === 'failed' &&<Alert color="red" title="Last deployment failed">{jobs[0].stage} · {jobs[0].message || 'Open Deployments for the device result.'}</Alert>}
    <Tabs value={tab} onChange={(value) => { setTab(value); if (value === 'history') void action(async () => setHistory(await request<Revision[]>(`${base(id)}/history`))) }}>
      <Tabs.List mb="lg"><Tabs.Tab value="files">Compose files</Tabs.Tab><StackContainersTab status={containerStatus} /><Tabs.Tab value="diff">Draft diff</Tabs.Tab><Tabs.Tab value="environment">Environment</Tabs.Tab><Tabs.Tab value="history">Revision history</Tabs.Tab><Tabs.Tab value="deployments">Deployments</Tabs.Tab><Tabs.Tab value="updates">Image updates</Tabs.Tab></Tabs.List>
      <Tabs.Panel value="containers" keepMounted><StackRuntime id={id} role={role} deploymentStatus={jobs[0] ? `${jobs[0].id}:${jobs[0].status}` : ''} onStatusChange={setContainerStatus} onNavigate={(route) => { if (unpersisted) { void action(async () => { await persistDraft(); onNavigate(route) }) } else onNavigate(route) }} /></Tabs.Panel>
      <Tabs.Panel value="files"><Stack>
        <Group justify="space-between"><Select aria-label="Stack file" value={file} data={Object.keys(files).sort()} onChange={setFile} w={320} /><Button variant="light" onClick={() => setTab('diff')}>Review draft changes</Button></Group>
        {encrypted ? <Alert>Encrypted environment files are preserved without displaying their values. Use the Environment tab to replace their contents.</Alert> : file && <Suspense fallback={<StackLoading editor />}>
          <ComposeEditor stackId={id} path={file} value={selectedContent} original={(remote ?? detail.snapshot).files[file] ?? ''} compare={false} composeFile={isComposeFile(file, files['vigara-stack.yaml'] ?? '')} disabled={busy} onChange={(value) => setFiles((current) => ({ ...current, [file]: value }))} onSave={() => { if (!busy && dirty && detail.assigned) void action(save) }} />
        </Suspense>}
        <Group align="end"><TextInput label="New file path" placeholder="config/app.yaml" value={newPath} onChange={(event) => setNewPath(event.currentTarget.value)} /><Button variant="default" disabled={busy || !newPath || files[newPath] !== undefined || newPath.endsWith('.env') || newPath === '.env'} onClick={() => { setFiles({ ...files, [newPath]: '' }); setFile(newPath); setNewPath('') }}>Add file</Button>{file && file !== 'vigara-stack.yaml' && <Button color="red" variant="subtle" disabled={busy} onClick={() => { const next = { ...files }; delete next[file]; setFiles(next); setFile(Object.keys(next)[0] ?? null) }}>Remove from draft</Button>}</Group>
        <Text size="xs" c="dimmed">Keep passwords and tokens in encrypted environment inputs. YAML is preserved as written. File removal changes Git; this first runner leaves old device files in place.</Text>
      </Stack></Tabs.Panel>
      <Tabs.Panel value="diff">{tab === 'diff' && <StackDiff stackId={id} saved={(remote ?? detail.snapshot).files} draft={files} revision={(remote ?? detail.snapshot).commit.slice(0, 8)} />}</Tabs.Panel>
      <Tabs.Panel value="environment">{tab === 'environment' && <SecretEditor key={`${detail.snapshot.commit}:${secretEpoch}`} id={id} files={files} busy={busy} persistDraft={persistDraft} onBusy={setSecretBusy} onEncrypted={(value) => { setFiles((current) => ({ ...current, [value.path]: value.ciphertext, 'vigara-stack.yaml': value.manifest })); setNotice('Environment encrypted and added to your draft. Save the revision when ready.'); }} />}</Tabs.Panel>
      <Tabs.Panel value="history"><Stack>{!history.length && <Text c="dimmed">No saved revisions yet.</Text>}{history.map((revision) => <Card key={revision.commit} withBorder><Group justify="space-between"><div><Text fw={600}>{revision.message}</Text><Text size="xs" c="dimmed">{short(revision.commit)} · {new Date(revision.at).toLocaleString()}</Text></div><Button variant="light" disabled={busy} onClick={() => void action(async () => { const value = await request<Detail>(`${base(id)}?commit=${revision.commit}`); setFiles(value.snapshot.files); setPlan(null); setTab('files'); setNotice(`Loaded revision ${short(revision.commit)} into your draft. Save creates a new commit before deployment.`) })}>Restore into draft</Button></Group></Card>)}</Stack></Tabs.Panel>
      <Tabs.Panel value="updates">{tab === 'updates' && <StackUpdatePolicy id={id} />}</Tabs.Panel>
      <Tabs.Panel value="deployments"><Stack>{!jobs.length && <Text c="dimmed">No deployments yet.</Text>}{jobs.map((job) => <Card withBorder key={job.id}><Group justify="space-between"><Text fw={600}>Revision {short(job.commit)}</Text><Badge color={job.status === 'succeeded' ? 'green' : job.status === 'failed' ? 'red' : 'yellow'}>{job.status}</Badge></Group><Text size="sm" mt="xs">{job.stage} · {job.message || 'Waiting for the device.'}</Text><Text size="xs" c="dimmed" mt="xs">{new Date(job.createdAt).toLocaleString()}</Text>{job.status === 'unknown' && <Button mt="sm" variant="light" disabled={busy} onClick={() => void action(async () => { const value = await request<{ message: string }>(`${base(id)}/deployments/${job.id}/reconcile`, 'POST', {}); setNotice(value.message) })}>Check device result</Button>}</Card>)}</Stack></Tabs.Panel>
    </Tabs>
    <Group justify="space-between"><Group><Button variant="subtle" color="red" disabled={busy || (!dirty && !draft.files)} onClick={() => setDiscarding(true)}>Discard draft</Button><Button variant="default" disabled={busy || !unpersisted} onClick={() => void action(async () => { await persistDraft(); setNotice('Draft stored in Vigara. Git and running services are unchanged.') })}>Keep draft</Button><Button loading={busy} disabled={!dirty || !detail.assigned} onClick={() => void action(save)}>Save to {detail.branch}</Button><Button variant="subtle" disabled={busy} onClick={() => void action(async () => { await load(false); setTab('diff') })}>Fetch saved version</Button></Group><Button variant="light" disabled={busy || !detail.snapshot.commit || !!activeJob || !detail.assigned || dirty} onClick={() => void action(() => preview(false))}>Preview deployment</Button></Group>
    {dirty && <Text size="sm" c="dimmed">Save your draft before deploying. Saving does not restart services.</Text>}
    <Group><Button variant="outline" color="red" disabled={busy || !!activeJob} onClick={() => { setDeleteConfirm(''); setSkipDevice(false); setDeleting(true) }}>Delete stack</Button>{activeJob && <Text size="xs" c="dimmed">Wait for the active deployment to finish before deleting.</Text>}</Group>
    <Modal opened={deleting} onClose={() => { if (!busy) setDeleting(false) }} title="Delete stack entirely?" centered><Stack>
      <Text>This stops and removes the containers and networks of project <Code>{detail.projectName || projectName}</Code>, deletes <Code>{id}</Code> from {detail.branch} in Git, and removes its drafts and deployment history from Vigara.</Text>
      <Text size="sm" c="dimmed">Named volumes, bind-mounted data and the project folder on the device are kept. Earlier Git revisions remain in repository history.</Text>
      <Checkbox label="The device is unavailable: forget the stack without stopping its containers" checked={skipDevice} onChange={(event) => setSkipDevice(event.currentTarget.checked)} />
      <TextInput label={<>Type <Code>{id}</Code> to confirm</>} value={deleteConfirm} onChange={(event) => setDeleteConfirm(event.currentTarget.value)} />
      {error && <Alert color="red">{error}</Alert>}
      <Button color="red" loading={actionBusy} disabled={deleteConfirm !== id} onClick={() => void action(async () => { await request(base(id), 'DELETE', { confirm: deleteConfirm, skipDevice }); setDeleting(false); onBack() })}>Delete stack</Button>
    </Stack></Modal>
    <Modal opened={tagImages !== null} onClose={() => { if (!busy) setTagImages(null) }} closeOnEscape={!busy} closeOnClickOutside={!busy} title="Choose an image tag" size="lg" centered>
      {error && <Alert color="red" mb="sm">{error}</Alert>}
      {tagImages && <StackImageTags id={id} images={tagImages} busy={busy} onSelect={(service, image) => void action(() => selectTag(service, image))} />}
    </Modal>
    {remote && <Alert title="Compare fetched version"><Text size="sm">Fetched {short(remote.commit)}. Review all files in the Draft diff tab. Keeping your draft against this version may overwrite changes, so resolve differences before saving.</Text><Button mt="sm" variant="light" disabled={busy} onClick={() => { setDetail({ ...detail, snapshot: remote }); setDraft({ ...draft, base: remote.commit }); setRemote(null); setNotice('Draft now uses the fetched version as its base. Review your edits before saving.') }}>Use fetched version as draft base</Button></Alert>}
    <Modal opened={discarding} onClose={() => { if (!busy) setDiscarding(false) }} title="Discard draft?" centered><Stack><Text>Discard your edits and stored draft for this stack? Saved Git revisions and running services are retained.</Text><Button color="red" loading={busy} onClick={() => void action(async () => { if (draft.files) await request(base(id) + '/draft', 'DELETE', { version: draft.version }); const next = Object.keys(detail.snapshot.files).length ? detail.snapshot.files : starter(id, detail.projectName || projectName); setFiles(next); setDraft({ base: detail.snapshot.commit, files: null, version: 0 }); setFile(next['compose.yaml'] !== undefined ? 'compose.yaml' : Object.keys(next)[0] ?? null); setPlan(null); setDiscarding(false); setNotice('Stored draft discarded.'); })}>Discard draft</Button></Stack></Modal>
    <Modal opened={!!plan} onClose={() => { if (!busy) setPlan(null) }} title={recreate ? (pull ? "Pull and recreate stack" : "Recreate stack") : "Deploy saved revision"} size="lg" classNames={{ body: 'stack-deploy-preview' }} centered><Stack><Text>Apply revision <Code>{short(detail.snapshot.commit)}</Code> to project <Code>{plan?.projectName}</Code>?</Text><Text size="sm">Device: {detail.agentId}</Text><Text size="sm">Services: {plan?.services.join(', ')}</Text><Stack gap="xs">{Object.entries(plan?.images ?? {}).map(([service, image]) => <Text size="xs" key={service}><Code>{service}</Code>: <Code>{image}</Code></Text>)}</Stack><Text size="sm" c="dimmed">Changed services may restart. Existing data volumes are retained. Deploying an older configuration does not undo application data migrations.</Text><Checkbox label="Recreate all stack containers" checked={recreate} onChange={(event) => setRecreate(event.currentTarget.checked)} /><Checkbox label="Download the reviewed images before deployment" checked={pull} onChange={(event) => setPull(event.currentTarget.checked)} />{error && <Alert color="red">{error}</Alert>}<Button loading={busy} onClick={() => void action(async () => { await request(`${base(id)}/deploy`, 'POST', { commit: detail.snapshot.commit, token: plan?.token, images: plan?.images, pull, recreate }); setPlan(null); setTab('deployments'); setNotice('Deployment queued. You can leave this page and return to its status.'); })}>{recreate ? (pull ? "Pull and recreate stack" : "Recreate stack") : "Deploy revision"}</Button></Stack></Modal>
  </Stack></section>
}
