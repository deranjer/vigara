import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { Alert, Button, Checkbox, Code, Group, Menu, Modal, Stack, Tabs, Text, TextInput, Title } from '@mantine/core'
import { useMediaQuery } from '@mantine/hooks'
import { IconDots, IconLock, IconRefresh, IconRocket } from '@tabler/icons-react'
import { StackUpdatePolicy } from './image-updates'
import { relativeTime } from './overview-utils'
import { route as routeOf, type Route } from './route'
import { isComposeFile } from './compose-editor-utils'
import { SecretEditor } from './secret-editor'
import { runStackActions } from './stack-actions'
import { StackDiff } from './stack-diff'
import { StackImageTags } from './stack-image-tags'
import { setServiceImage } from './stack-image-utils'
import { StackList } from './stack-list'
import { isReverseProxy, stackActionBlocker, stackActionTargets, stackRuntimeBadge, type StackAction, type StackHost, type StackSummary } from './stack-list-utils'
import { StackLoading } from './stack-loading'
import { useStackRuntime } from './stack-runtime'
import { ActivityTimeline, DraftBar, FileList, ReviewSheet, ServicesRail } from './stack-workspace'
import { changedPaths, deploymentTitle, draftSummary, firstReviewStep, type Deployment, type Revision, type ReviewStep } from './stack-workspace-utils'
import { StatusText } from './status'
import { Panel } from './ui'

const ComposeEditor = lazy(() => import('./compose-editor'))

type Files = Record<string, string>
type Snapshot = { id: string; commit: string; files: Files }
type Draft = { digest?: string; base: string; files: Files | null; version: number }
type Detail = { projectName: string; snapshot: Snapshot; draft: Draft; branch: string; assigned: boolean; agentId: string; digest: string }
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
 if (route.id) return <StackEditor key={route.id} id={route.id} projectName={route.id} role={role} onNavigate={onNavigate} onBack={() => onNavigate(routeOf('stacks'))} pullOnOpen={route.sub === 'pull'} />
 return <StackList role={role} onOpen={(id) => onNavigate(routeOf('stacks', id))} onPull={(id) => onNavigate(routeOf('stacks', id, 'pull'))} />
}

function readCollapsed() { try { return localStorage.getItem('vigara.stack.rail') === 'collapsed' } catch { return false } }

/**
 * A stack as a workspace: files, the editor and live services side by side, one bar for the draft,
 * and one Review & deploy sheet. Phones get a read-only view that can still deploy, pull and restart.
 */
function StackEditor({ id, projectName, onBack, role, onNavigate, pullOnOpen }: { id: string; projectName: string; onBack: () => void; role: string; onNavigate: (route: Route) => void; pullOnOpen?: boolean }) {
  const [detail, setDetail] = useState<Detail | null>(null)
  const [files, setFiles] = useState<Files>({})
  const [draft, setDraft] = useState<Draft>({ base: '', files: null, version: 0 })
  const [file, setFile] = useState<string | null>('compose.yaml')
  const [tab, setTab] = useState<string | null>('files')
  const [history, setHistory] = useState<Revision[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const [jobs, setJobs] = useState<Deployment[]>([])
  const [actionBusy, setBusy] = useState(false)
  const [secretBusy, setSecretBusy] = useState(false)
  const [secretEpoch, setSecretEpoch] = useState(0)
  const busy = actionBusy || secretBusy
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [plan, setPlan] = useState<Plan | null>(null)
  const [planCommit, setPlanCommit] = useState('')
  const [pull, setPull] = useState(false)
  const [recreate, setRecreate] = useState(false)
  const [review, setReview] = useState<ReviewStep | null>(null)
  const [deployCommit, setDeployCommit] = useState('')
  const [tagImages, setTagImages] = useState<Record<string, string> | null>(null)
  const [discarding, setDiscarding] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState('')
  const [skipDevice, setSkipDevice] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [outcomes, setOutcomes] = useState<{ name: string; ok: boolean; message: string }[]>([])
  const [remote, setRemote] = useState<Snapshot | null>(null)
  const [railCollapsed, setRailCollapsed] = useState(readCollapsed)
  const phone = useMediaQuery('(max-width: 47.99em)') ?? false
  const filesRef = useRef(files)
  filesRef.current = files
  const dirty = detail ? !equal(files, detail.snapshot.files) : false
  const unpersisted = draft.files ? !equal(files, draft.files) : dirty
  const runtime = useStackRuntime(id, jobs[0] ? `${jobs[0].id}:${jobs[0].status}` : '')

  async function load(initial: boolean) {
    const value = await request<Detail>(base(id))
    if (initial) {
      setDetail(value); setDraft(value.draft); setRemote(null)
      const next = value.draft.files ?? (Object.keys(value.snapshot.files).length ? value.snapshot.files : starter(id, value.projectName || projectName))
      setFiles(next); setFile(next['compose.yaml'] !== undefined ? 'compose.yaml' : Object.keys(next)[0] ?? null)
    } else {
      setRemote(value.snapshot)
      setNotice(value.snapshot.commit !== detail?.snapshot.commit ? 'Fetched the latest saved version. Your editor contents are preserved; compare the versions under Activity before replacing your draft.' : 'The saved version is up to date. Your editor contents are preserved.')
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
  // Leaving through the breadcrumb, the sidebar or a link stores unsaved edits as the draft first.
  const leaving = useRef({ unpersisted, persistDraft: () => Promise.resolve(draft) })
  leaving.current = { unpersisted, persistDraft: () => persistDraft() }
  useEffect(() => () => { if (leaving.current.unpersisted) void leaving.current.persistDraft().catch(() => undefined) }, [])
  // "Pull & recreate" in the stack list lands here (#/stacks/<id>/pull) and opens the same review.
  const pullStarted = useRef(false)
  useEffect(() => {
    if (!pullOnOpen || !detail || pullStarted.current) return
    pullStarted.current = true
    onNavigate(routeOf('stacks', id))
    if (detail.snapshot.commit && detail.assigned && !detail.draft.files) openPull()
    // Runs once, when the stack has loaded; the ref keeps later reloads from reopening it.
  }, [pullOnOpen, detail])

  async function action(work: () => Promise<void>) { setSecretEpoch((epoch) => epoch + 1); setBusy(true); setError(''); setNotice(''); try { await work() } catch (cause) { setError(cause instanceof Error ? cause.message : 'The operation failed.') } finally { setBusy(false) } }
  async function persistDraft(nextFiles = filesRef.current) {
    const next = await request<Draft>(`${base(id)}/draft`, 'PUT', { base: draft.base || detail?.snapshot.commit || '', files: nextFiles, version: draft.version })
    setDraft(next); return next
  }
  /** Commits the draft to Git. Returns the new commit, or null when Git has not confirmed the push. */
  async function save(): Promise<string | null> {
    const next = await persistDraft()
    const result = await request<{ state: string; commit: string }>(`${base(id)}/save`, 'POST', { version: next.version })
    if (result.state === 'pendingPush') { setNotice('Pending sync: your draft is stored, but Git has not confirmed the push. Commit again to sync it. This version cannot be deployed yet.'); return null }
    await load(true); setPlan(null); setNotice(`Committed ${short(result.commit)} to ${detail?.branch}. Running services are unchanged.`)
    return result.commit
  }
  async function preparePlan(recreateStack: boolean, commit = detail?.snapshot.commit ?? '') {
    setPull(recreateStack); setRecreate(recreateStack); setPlan(null); setPlanCommit(commit)
    setPlan(await request<Plan>(`${base(id)}/plan`, 'POST', { commit }))
  }
  function openReview() {
    const step = firstReviewStep(dirty)
    setReview(step); setError('')
    if (step === 'plan') void action(() => preparePlan(false))
  }
  function openPull() { setReview('plan'); void action(() => preparePlan(true)) }
  async function selectTag(service: string, image: string) {
    const updated = setServiceImage(filesRef.current, service, image)
    setFiles(updated); filesRef.current = updated; setPlan(null)
    const next = await persistDraft(updated)
    const saved = await request<{ state: string; commit: string }>(`${base(id)}/save`, 'POST', { version: next.version })
    setTagImages(null)
    if (saved.state !== 'saved') {
      setNotice('The selected tag is stored in your draft. Commit again to finish syncing Git before deployment.'); return
    }
    await load(true)
    setNotice(`Committed ${service}: ${image}. Review the deployment before applying it.`)
    setReview('plan')
    await preparePlan(true, saved.commit)
  }
  async function runAction(kind: StackAction) {
    setOutcomes([])
    // Choose current container IDs immediately before the action, as in the stack list.
    const [listing, inventory] = await Promise.all([request<{ stacks: StackSummary[] }>('/api/stacks'), request<{ hosts: StackHost[] }>('/api/containers')])
    runtime.lock.current = true
    try { setOutcomes(await runStackActions(listing.stacks, inventory.hosts, role, kind, id)) } finally { runtime.lock.current = false; runtime.reload() }
  }

  if (loading) return <StackLoading />
  if (!detail) return <Stack><Alert color="red">{error}</Alert><Button variant="default" onClick={onBack}>All stacks</Button></Stack>
  const activeJob = jobs.find((job) => ['queued', 'running', 'unknown'].includes(job.status))
  const deployed = jobs.find((job) => job.status === 'succeeded')
  const deployJob = deployCommit ? jobs.find((job) => job.commit === deployCommit) ?? null : null
  const selectedContent = file ? files[file] ?? '' : ''
  const encrypted = file?.endsWith('.sops') ?? false
  const changes = changedPaths(detail.snapshot.files, files)
  const live = runtime.data
  const stackName = live?.stack.name || id
  const deviceName = live?.stack.deviceName || live?.hosts.find((host) => host.agentId === detail.agentId)?.name || (detail.agentId ? 'Unknown device' : 'No device')
  const badge = live ? stackRuntimeBadge(live.stack, live.hosts) : null
  const blocker = live ? stackActionBlocker(live.stack, live.hosts, role) : 'Live container information is unavailable.'
  const canAct = (kind: StackAction) => !busy && !blocker && !!live && stackActionTargets(live.stack, live.hosts, kind).length > 0
  const canDeploy = detail.assigned && !activeJob
  const savedUndeployed = !!detail.snapshot.commit && (!deployed || deployed.digest !== detail.digest)
  const lastFailed = jobs[0]?.status === 'failed' ? jobs[0] : null
  const openActivity = () => { setTab('activity'); setHistoryLoading(true); void request<Revision[]>(`${base(id)}/history`).then(setHistory).catch((cause: Error) => setError(cause.message)).finally(() => setHistoryLoading(false)) }

  return <div className="vg-page vg-stack-page">
    <header className="vg-device-head">
      <div className="vg-device-title">
        <Title order={1}>{stackName}</Title>
        <Text size="sm" c="dimmed" mt={4} className="vg-meta-row">
          {badge ? <StatusText color={badge.color} title={badge.label}>{badge.label === 'Unavailable' ? 'Services unavailable' : `${badge.count} running`}</StatusText> : <span>Loading services…</span>}
          <span>{deviceName}</span>
          <span className="vg-mono">{detail.branch}@{detail.snapshot.commit ? detail.snapshot.commit.slice(0, 7) : 'unsaved'}</span>
          <span>{deployed ? `deployed ${relativeTime(deployed.createdAt)}` : 'never deployed'}</span>
        </Text>
      </div>
      <Group gap="xs">
        <Button size="xs" variant="default" leftSection={<IconRefresh size={14} />} disabled={busy || !detail.snapshot.commit || !canDeploy || dirty} title={dirty ? 'Commit or discard the draft first.' : 'Download newer images for the configured tags and recreate every service'} onClick={openPull}>Pull &amp; recreate</Button>
        <Menu position="bottom-end" withinPortal>
          <Menu.Target><Button size="xs" variant="default" px={8} aria-label="More stack actions"><IconDots size={16} /></Button></Menu.Target>
          <Menu.Dropdown>
            <Menu.Item disabled={!canAct('restart')} onClick={() => void action(() => runAction('restart'))}>Restart</Menu.Item>
            <Menu.Item disabled={!canAct('stop')} onClick={() => setStopping(true)}>Stop…</Menu.Item>
            <Menu.Item disabled={!canAct('start')} onClick={() => void action(() => runAction('start'))}>Start</Menu.Item>
            {blocker && <Menu.Label>{blocker}</Menu.Label>}
            <Menu.Divider />
            <Menu.Item disabled={busy || !detail.snapshot.commit || !canDeploy || dirty || phone} onClick={() => void action(async () => { const source = await request<Plan>(`${base(id)}/plan`, 'POST', { commit: detail.snapshot.commit, sourcesOnly: true }); setTagImages(source.tracking ?? source.images) })}>Choose image tags…</Menu.Item>
            <Menu.Item disabled={busy} onClick={() => void action(async () => { await load(false); openActivity() })}>Fetch saved version</Menu.Item>
            <Menu.Divider />
            <Menu.Item color="red" disabled={busy || !!activeJob} onClick={() => { setDeleteConfirm(''); setSkipDevice(false); setDeleting(true) }}>Delete stack…</Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </Group>
    </header>

    {error && !review && <Alert mb="sm" color="red" title="Stack operation could not complete" withCloseButton onClose={() => setError('')}>{error}</Alert>}
    {notice && <Alert mb="sm" color="blue" withCloseButton onClose={() => setNotice('')}>{notice}</Alert>}
    {!!outcomes.length && <Alert mb="sm" color={outcomes.some((outcome) => !outcome.ok) ? 'red' : 'green'} withCloseButton onClose={() => setOutcomes([])}>{outcomes.map((outcome, index) => <Text key={index} size="sm">{outcome.message}</Text>)}</Alert>}
    {!detail.assigned && <Alert mb="sm" color="yellow">Assign this stack to a device under Profiles and assignments on the Stacks page before committing or deploying.</Alert>}
    {remote && <Alert mb="sm" title="Compare the fetched version"><Text size="sm">Fetched {short(remote.commit)}. Compare it with your draft under Activity. Keeping your draft against this version may overwrite changes, so resolve differences before committing.</Text><Button mt="sm" size="xs" variant="light" disabled={busy} onClick={() => { setDetail({ ...detail, snapshot: remote }); setDraft({ ...draft, base: remote.commit }); setRemote(null); setNotice('Your draft now uses the fetched version as its base. Review your edits before committing.') }}>Use fetched version as draft base</Button></Alert>}

    {dirty ? <DraftBar tone="warn" actions={<>
      <Button size="xs" variant="subtle" color="gray" disabled={busy} onClick={() => setDiscarding(true)}>Discard</Button>
      {unpersisted && !phone && <Button size="xs" variant="default" disabled={busy} onClick={() => void action(async () => { await persistDraft(); setNotice('Draft stored in Vigara. Git and running services are unchanged.') })}>Store draft</Button>}
      <Button size="xs" leftSection={<IconRocket size={14} />} disabled={busy || !detail.assigned} onClick={openReview}>Review &amp; deploy</Button>
    </>}><b>Draft</b> · {draftSummary(changes)} · {unpersisted ? 'not stored yet' : 'stored in Vigara'}</DraftBar>
      : activeJob ? <DraftBar tone={activeJob.status === 'unknown' ? 'warn' : 'info'} actions={activeJob.status === 'unknown'
        ? <Button size="xs" variant="default" onClick={openActivity}>Check in Activity</Button>
        : <Button size="xs" variant="default" onClick={() => { setDeployCommit(activeJob.commit); setReview('deploy') }}>Show progress</Button>}>
        <b>{deploymentTitle(activeJob)}</b>{activeJob.stage ? ` · ${activeJob.stage}` : ''}{activeJob.status === 'unknown' ? ' · the device has not confirmed the result' : ''}</DraftBar>
      : lastFailed ? <DraftBar tone="crit" actions={<><Button size="xs" variant="default" onClick={openActivity}>Activity</Button><Button size="xs" leftSection={<IconRocket size={14} />} disabled={busy || !canDeploy} onClick={openReview}>Review &amp; deploy</Button></>}>
        <b>Last deploy failed</b> · {[lastFailed.stage, lastFailed.message].filter(Boolean).join(' · ') || 'see Activity for the device output'}</DraftBar>
      : savedUndeployed ? <DraftBar tone="info" actions={<Button size="xs" leftSection={<IconRocket size={14} />} disabled={busy || !canDeploy} onClick={openReview}>Review &amp; deploy</Button>}>
        Revision <span className="vg-mono">{detail.snapshot.commit.slice(0, 7)}</span> is committed but not deployed</DraftBar>
      : null}

    <Tabs value={tab} onChange={(value) => { if (value === 'activity') openActivity(); else setTab(value) }} mt="xs" keepMounted={false} className="vg-page-tabs">
      <Tabs.List><Tabs.Tab value="files">Files</Tabs.Tab><Tabs.Tab value="environment" rightSection={<IconLock size={12} aria-hidden="true" />}>Environment</Tabs.Tab><Tabs.Tab value="activity">Activity</Tabs.Tab><Tabs.Tab value="settings">Settings</Tabs.Tab></Tabs.List>
      <Tabs.Panel value="files" pt="md">
        {phone && <Text size="xs" c="dimmed" mb="xs">Files are read-only on a phone. Deploy, pull and restart still work.</Text>}
        <div className="vg-ws" data-rail={railCollapsed ? 'collapsed' : undefined}>
          <FileList files={files} changes={changes} selected={file} readOnly={phone || busy} onSelect={setFile}
            onAdd={(path) => { setFiles({ ...files, [path]: '' }); setFile(path) }}
            onRemove={(path) => { const next = { ...files }; delete next[path]; setFiles(next); setFile(next['compose.yaml'] !== undefined ? 'compose.yaml' : Object.keys(next)[0] ?? null) }} />
          <div className="vg-ws-editor">
            {encrypted ? <Alert>Encrypted environment files keep their values hidden. Use the Environment tab to replace their contents.</Alert> : file && files[file] !== undefined ? <Suspense fallback={<StackLoading editor />}>
              <ComposeEditor stackId={id} path={file} value={selectedContent} original={(remote ?? detail.snapshot).files[file] ?? ''} compare={false} composeFile={isComposeFile(file, files['vigara-stack.yaml'] ?? '')} disabled={busy || phone} onChange={(value) => setFiles((current) => ({ ...current, [file]: value }))} onSave={() => { if (!busy && dirty && detail.assigned) openReview() }} />
            </Suspense> : <Text c="dimmed" size="sm">Choose a file.</Text>}
            <Text size="xs" c="dimmed" mt="xs">Keep passwords and tokens in the Environment tab, where they are encrypted. YAML is kept as written. Removing a file changes Git; files already on the device stay in place.</Text>
          </div>
          <ServicesRail runtime={runtime} jobs={jobs} collapsed={railCollapsed && !phone} onCollapse={(value) => { setRailCollapsed(value); try { localStorage.setItem('vigara.stack.rail', value ? 'collapsed' : 'open') } catch { /* optional */ } }} onActivity={openActivity}
            onNavigate={(route) => { if (unpersisted) { void action(async () => { await persistDraft(); onNavigate(route) }) } else onNavigate(route) }} />
        </div>
      </Tabs.Panel>
      <Tabs.Panel value="environment" pt="md">{phone ? <Text size="sm" c="dimmed">Environment values can be changed on a larger screen.</Text> : <SecretEditor key={`${detail.snapshot.commit}:${secretEpoch}`} id={id} files={files} busy={busy} persistDraft={persistDraft} onBusy={setSecretBusy} onEncrypted={(value) => { setFiles((current) => ({ ...current, [value.path]: value.ciphertext, 'vigara-stack.yaml': value.manifest })); setNotice('Environment encrypted and added to your draft. Commit it with Review & deploy when ready.') }} />}</Tabs.Panel>
      <Tabs.Panel value="activity" pt="md">
        <ActivityTimeline history={history} jobs={jobs} loading={historyLoading} busy={busy || phone}
          draftPanel={(dirty || remote) ? <Panel title={remote ? 'Fetched version ↔ your editor' : 'Unsaved draft'}><StackDiff stackId={id} saved={(remote ?? detail.snapshot).files} draft={files} revision={(remote ?? detail.snapshot).commit.slice(0, 8)} /></Panel> : null}
          onRestore={(commit) => void action(async () => { const value = await request<Detail>(`${base(id)}?commit=${commit}`); setFiles(value.snapshot.files); setPlan(null); setTab('files'); setNotice(`Loaded revision ${short(commit)} into your draft. Commit it before deploying.`) })}
          onReconcile={(job) => void action(async () => { const value = await request<{ message: string }>(`${base(id)}/deployments/${job}/reconcile`, 'POST', {}); setNotice(value.message) })} />
      </Tabs.Panel>
      <Tabs.Panel value="settings" pt="md"><Stack gap="md">
        <Panel title="Stack"><dl className="vg-kv">
          <div><dt>Stack ID</dt><dd className="vg-mono">{id}</dd></div>
          <div><dt>Compose project</dt><dd className="vg-mono">{detail.projectName || projectName}</dd></div>
          <div><dt>Device</dt><dd>{deviceName}</dd></div>
          {live?.stack.directory && <div><dt>Folder on the device</dt><dd className="vg-mono">{live.stack.directory}</dd></div>}
          <div><dt>Branch</dt><dd className="vg-mono">{detail.branch}</dd></div>
        </dl></Panel>
        <Panel title="Image updates"><StackUpdatePolicy id={id} /></Panel>
        <Panel title="Delete"><Group justify="space-between" align="center"><Text size="sm" c="dimmed">Stops and removes the stack’s containers and deletes it from Git. Volumes and data on the device are kept.</Text><Button size="xs" color="red" variant="outline" disabled={busy || !!activeJob} onClick={() => { setDeleteConfirm(''); setSkipDevice(false); setDeleting(true) }}>Delete stack…</Button></Group></Panel>
      </Stack></Tabs.Panel>
    </Tabs>

    <ReviewSheet opened={review !== null} step={review ?? 'changes'} stackId={id} dirty={dirty} saved={(remote ?? detail.snapshot).files} draft={files} revision={short(planCommit || detail.snapshot.commit)}
      plan={plan} pull={pull} recreate={recreate} deviceName={deviceName} branch={detail.branch} busy={busy} error={error} job={deployJob} canDeploy={canDeploy}
      onStep={(step) => { setReview(step); if (step === 'plan' && !plan) void action(() => preparePlan(recreate)) }} onPull={setPull} onRecreate={setRecreate}
      onCommitOnly={() => void action(async () => { if (await save()) setReview(null) })}
      onCommitAndPlan={() => void action(async () => { const commit = await save(); if (!commit) return; setReview('plan'); await preparePlan(false, commit) })}
      onPlan={() => void action(() => preparePlan(recreate))}
      onDeploy={() => void action(async () => { await request(`${base(id)}/deploy`, 'POST', { commit: planCommit, token: plan?.token, images: plan?.images, pull, recreate }); setDeployCommit(planCommit); setPlan(null); setReview('deploy') })}
      onClose={() => { setReview(null); setError('') }} />
    <Modal opened={stopping} onClose={() => setStopping(false)} title={`Stop ${stackName}?`} centered size="sm"><Stack>
      <Text size="sm">{live ? stackActionTargets(live.stack, live.hosts, 'stop').length : 0} running container{live && stackActionTargets(live.stack, live.hosts, 'stop').length === 1 ? '' : 's'} on {deviceName} stop. Nothing is removed; Start brings them back.</Text>
      {live && isReverseProxy(live.stack) && <Alert color="yellow" p="xs">This looks like a reverse proxy. Sites behind it go offline while it is stopped.</Alert>}
      <Group justify="flex-end" gap="xs"><Button variant="default" onClick={() => setStopping(false)}>Cancel</Button><Button color="red" onClick={() => { setStopping(false); void action(() => runAction('stop')) }}>Stop stack</Button></Group>
    </Stack></Modal>
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
    <Modal opened={discarding} onClose={() => { if (!busy) setDiscarding(false) }} title="Discard draft?" centered><Stack><Text>Discard your edits and stored draft for this stack? Committed revisions and running services are kept.</Text><Button color="red" loading={busy} onClick={() => void action(async () => { if (draft.files) await request(base(id) + '/draft', 'DELETE', { version: draft.version }); const next = Object.keys(detail.snapshot.files).length ? detail.snapshot.files : starter(id, detail.projectName || projectName); setFiles(next); setDraft({ base: detail.snapshot.commit, files: null, version: 0 }); setFile(next['compose.yaml'] !== undefined ? 'compose.yaml' : Object.keys(next)[0] ?? null); setPlan(null); setDiscarding(false); setNotice('Draft discarded.'); })}>Discard draft</Button></Stack></Modal>
  </div>
}
