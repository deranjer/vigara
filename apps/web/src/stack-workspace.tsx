import { useState, type ReactNode } from 'react'
import { ActionIcon, Alert, Button, Checkbox, Code, Group, Loader, Modal, Stack, Tabs, Text, TextInput, UnstyledButton } from '@mantine/core'
import { IconChevronLeft, IconChevronRight, IconFile, IconLock, IconPlus, IconRocket } from '@tabler/icons-react'
import { relativeTime } from './overview-utils'
import { route as routeOf, type Route } from './route'
import { StackDiff } from './stack-diff'
import type { RuntimeData } from './stack-runtime'
import { activityItems, deploymentTitle, deploymentTone, imageTag, type Deployment, type ReviewStep, type Revision } from './stack-workspace-utils'
import { StatusText, type StatusTone } from './status'

type Files = Record<string, string>

/** The stack's files, with a dot on each one the draft changes. */
export function FileList({ files, changes, selected, readOnly, onSelect, onAdd, onRemove }: {
  files: Files; changes: Map<string, string>; selected: string | null; readOnly: boolean
  onSelect: (path: string) => void; onAdd: (path: string) => void; onRemove: (path: string) => void
}) {
  const [adding, setAdding] = useState<string | null>(null)
  const paths = [...new Set([...Object.keys(files), ...[...changes].filter(([, status]) => status === 'Removed').map(([path]) => path)])].sort()
  const valid = adding !== null && !!adding.trim() && files[adding] === undefined && !adding.endsWith('.env') && adding !== '.env'
  return <nav className="vg-ws-files" aria-label="Stack files">
    <span className="vg-label">Files</span>
    {paths.map((path) => {
      const status = changes.get(path)
      const removed = status === 'Removed'
      return <UnstyledButton key={path} className="vg-ws-file" data-active={path === selected || undefined} data-removed={removed || undefined} disabled={removed} onClick={() => onSelect(path)} title={status ? `${path} · ${status.toLowerCase()} in the draft` : path}>
        {path.endsWith('.sops') ? <IconLock size={14} aria-hidden="true" /> : <IconFile size={14} aria-hidden="true" />}
        <span className="vg-ellipsis">{path}</span>
        {status && <span className="vg-ws-mark" data-status={status}>{status === 'Changed' ? 'M' : status[0]}</span>}
      </UnstyledButton>
    })}
    {!readOnly && (adding === null
      ? <Button size="compact-xs" variant="subtle" leftSection={<IconPlus size={12} />} onClick={() => setAdding('')} className="vg-ws-add">Add file</Button>
      : <form onSubmit={(event) => { event.preventDefault(); if (valid) { onAdd(adding.trim()); setAdding(null) } }}>
        <TextInput size="xs" autoFocus placeholder="config/app.yaml" value={adding} onChange={(event) => setAdding(event.currentTarget.value)} onKeyDown={(event) => { if (event.key === 'Escape') setAdding(null) }} aria-label="New file path" />
        <Group gap={4} mt={4}><Button size="compact-xs" type="submit" disabled={!valid}>Add</Button><Button size="compact-xs" variant="subtle" onClick={() => setAdding(null)}>Cancel</Button></Group>
      </form>)}
    {!readOnly && selected && selected !== 'vigara-stack.yaml' && files[selected] !== undefined && <Button size="compact-xs" variant="subtle" color="red" onClick={() => onRemove(selected)} className="vg-ws-add">Remove {selected.split('/').pop()}</Button>}
  </nav>
}

function serviceTone(state: string, exitCode: number | undefined, health: string | undefined): StatusTone {
  if (health === 'unhealthy' || state === 'restarting') return 'warn'
  if (state === 'running') return 'ok'
  if (state === 'exited' && exitCode) return 'crit'
  return 'idle'
}

/** Live services beside the editor: state, image tag and published port, each opening its container. */
export function ServicesRail({ runtime, jobs, collapsed, onCollapse, onActivity, onNavigate }: {
  runtime: { data: RuntimeData | null; error: string; loading: boolean }; jobs: Deployment[]; collapsed: boolean
  onCollapse: (collapsed: boolean) => void; onActivity: () => void; onNavigate: (route: Route) => void
}) {
  if (collapsed) return <aside className="vg-ws-rail" data-collapsed>
    <ActionIcon variant="subtle" size="sm" onClick={() => onCollapse(false)} aria-label="Show services"><IconChevronLeft size={14} /></ActionIcon>
  </aside>
  const { data, error, loading } = runtime
  const host = data?.hosts.find((item) => item.agentId === data.stack.agentId)
  const services = new Set(data?.containers.map(({ container }) => container.service || container.id))
  const missing = data && host?.state === 'ok' && !host.truncated ? data.stack.serviceCount - services.size : 0
  return <aside className="vg-ws-rail" aria-label="Services">
    <Group justify="space-between" wrap="nowrap"><span className="vg-label">Services</span><ActionIcon variant="subtle" size="sm" onClick={() => onCollapse(true)} aria-label="Hide services"><IconChevronRight size={14} /></ActionIcon></Group>
    {loading && !data && <Loader size="xs" />}
    {error && <Text size="xs" c="red">{error}</Text>}
    {data && (!host || host.state !== 'ok') && <Text size="xs" c="dimmed">{host?.message || 'The device is not connected, so live services are unavailable.'}</Text>}
    {data && host?.state === 'ok' && !data.containers.length && <Text size="xs" c="dimmed">No containers exist for this stack yet. Deploy it to create them.</Text>}
    {data?.containers.map(({ container, inspect }) => {
      const state = inspect?.state.status || container.state
      const published = (inspect?.ports ?? container.ports ?? []).find((port) => port.publicPort)?.publicPort
      return <UnstyledButton key={container.id} className="vg-ws-svc" onClick={() => onNavigate(routeOf('containers', data.stack.agentId, container.name || container.id))} title={`${container.name || container.id} · ${container.status}`}>
        <span className="vg-status-dot" data-tone={serviceTone(state, inspect?.state.exitCode, inspect?.state.health)} aria-hidden="true" />
        <span className="vg-ws-svc-name"><b className="vg-ellipsis">{container.service || container.name}</b><span className="vg-mono vg-dim vg-ellipsis">{imageTag(inspect?.image || container.image)}</span></span>
        {published && <span className="vg-pill" title={`Published on port ${published}`}>{published}</span>}
      </UnstyledButton>
    })}
    {missing > 0 && <Text size="xs" c="orange">{missing} saved service{missing === 1 ? ' has' : 's have'} no container. Deploy to create {missing === 1 ? 'it' : 'them'}.</Text>}
    <span className="vg-label" style={{ marginTop: 8 }}>Last deploys</span>
    {!jobs.length && <Text size="xs" c="dimmed">Not deployed from Vigara yet.</Text>}
    {jobs.slice(0, 3).map((job) => <div key={job.id} className="vg-ws-feed"><StatusText tone={deploymentTone(job.status)}>{deploymentTitle(job)}</StatusText><time className="vg-dim">{relativeTime(job.createdAt)}</time></div>)}
    <Button size="compact-xs" variant="subtle" onClick={onActivity} className="vg-ws-add" rightSection={<IconChevronRight size={12} />}>Full activity</Button>
  </aside>
}

/** One bar for the draft or an undeployed revision, with the way forward. */
export function DraftBar({ tone, children, actions }: { tone: StatusTone; children: ReactNode; actions: ReactNode }) {
  return <div className="vg-draftbar" role="status"><span className="vg-status-dot" data-tone={tone} aria-hidden="true" /><span className="vg-grow">{children}</span><Group gap="xs" wrap="nowrap">{actions}</Group></div>
}

/** Saved revisions and deployments on one timeline, with the draft's changes on top. */
export function ActivityTimeline({ history, jobs, loading, busy, draftPanel, onRestore, onReconcile }: {
  history: Revision[]; jobs: Deployment[]; loading: boolean; busy: boolean; draftPanel: ReactNode
  onRestore: (commit: string) => void; onReconcile: (job: string) => void
}) {
  const items = activityItems(history, jobs)
  return <Stack gap="md">
    {draftPanel}
    {loading && <Loader size="sm" />}
    {!loading && !items.length && <Text c="dimmed" size="sm">No saved revisions or deployments yet.</Text>}
    <ol className="vg-timeline">{items.map((item) => item.kind === 'revision'
      ? <li key={`r${item.revision.commit}`}><span className="vg-status-dot" data-tone="idle" aria-hidden="true" />
        <div className="vg-grow"><b>{item.revision.message || 'Saved revision'}</b><div className="vg-dim"><span className="vg-mono">{item.revision.commit.slice(0, 7)}</span> · saved {relativeTime(item.revision.at)}</div></div>
        <Button size="compact-xs" variant="subtle" disabled={busy} onClick={() => onRestore(item.revision.commit)}>Restore into draft</Button></li>
      : <li key={`d${item.deployment.id}`}><span className="vg-status-dot" data-tone={deploymentTone(item.deployment.status)} aria-hidden="true" />
        <div className="vg-grow"><b>{deploymentTitle(item.deployment)}</b><div className="vg-dim">{[item.deployment.stage, item.deployment.message].filter(Boolean).join(' · ') || 'Waiting for the device.'} · {relativeTime(item.deployment.createdAt)}</div></div>
        {item.deployment.status === 'unknown' && <Button size="compact-xs" variant="light" disabled={busy} onClick={() => onReconcile(item.deployment.id)}>Check device result</Button>}</li>)}
    </ol>
  </Stack>
}

export type PlanView = { images: Record<string, string>; projectName: string; services: string[] }

/**
 * Review & deploy: the draft's changes, then the plan for the saved revision, then the deployment.
 * Commit & deploy saves and moves to the plan; nothing deploys until Deploy on the plan step.
 */
export function ReviewSheet(props: {
  opened: boolean; step: ReviewStep; stackId: string; dirty: boolean; saved: Files; draft: Files; revision: string
  plan: PlanView | null; pull: boolean; recreate: boolean; deviceName: string; branch: string; busy: boolean; error: string
  job: Deployment | null; canDeploy: boolean
  onStep: (step: ReviewStep) => void; onPull: (value: boolean) => void; onRecreate: (value: boolean) => void
  onCommitOnly: () => void; onCommitAndPlan: () => void; onPlan: () => void; onDeploy: () => void; onClose: () => void
}) {
  const { opened, step, dirty, plan, job, busy } = props
  const title = props.recreate ? (props.pull ? 'Pull & recreate' : 'Recreate') : 'Review & deploy'
  return <Modal opened={opened} onClose={() => { if (!busy) props.onClose() }} closeOnEscape={!busy} closeOnClickOutside={!busy} title={`${title} ${props.stackId}`} size="xl" centered>
    <Tabs value={step} onChange={(value) => value && props.onStep(value as ReviewStep)} keepMounted={false}>
      <Tabs.List mb="md">
        <Tabs.Tab value="changes" disabled={busy}>1 · Changes</Tabs.Tab>
        <Tabs.Tab value="plan" disabled={busy || dirty}>2 · Plan</Tabs.Tab>
        <Tabs.Tab value="deploy" disabled={!job && step !== 'deploy'}>3 · Deploy</Tabs.Tab>
      </Tabs.List>
      <Tabs.Panel value="changes"><Stack>
        {dirty ? <StackDiff stackId={props.stackId} saved={props.saved} draft={props.draft} revision={props.revision} /> : <Alert color="gray">Nothing to commit: the editor matches saved revision <Code>{props.revision || 'none'}</Code>.</Alert>}
        {props.error && <Alert color="red">{props.error}</Alert>}
        <Group justify="flex-end" gap="xs">
          {dirty ? <>
            <Button variant="default" loading={busy} onClick={props.onCommitOnly}>Commit only</Button>
            <Button loading={busy} disabled={!props.canDeploy} leftSection={<IconRocket size={14} />} onClick={props.onCommitAndPlan}>Commit &amp; deploy</Button>
          </> : <Button onClick={() => props.onStep('plan')}>Next: plan</Button>}
        </Group>
        {dirty && <Text size="xs" c="dimmed" ta="right">Commit saves to {props.branch}; running services change only when you deploy on the next step.</Text>}
      </Stack></Tabs.Panel>
      <Tabs.Panel value="plan"><Stack>
        {!plan && <Group><Loader size="sm" /><Text size="sm" c="dimmed">Preparing the plan…</Text></Group>}
        {plan && <>
          <Text>Apply revision <Code>{props.revision}</Code> to project <Code>{plan.projectName}</Code> on {props.deviceName}.</Text>
          <div className="vg-panel"><table className="vg-kv-table"><tbody>{plan.services.map((service) => <tr key={service}><th>{service}</th><td className="vg-mono">{plan.images[service] ?? '—'}</td></tr>)}</tbody></table></div>
          <Checkbox label="Recreate every container in the stack" checked={props.recreate} onChange={(event) => props.onRecreate(event.currentTarget.checked)} />
          <Checkbox label="Download newer images for the configured tags first" checked={props.pull} onChange={(event) => props.onPull(event.currentTarget.checked)} />
          <Text size="sm" c="dimmed">Changed services restart. Data volumes are kept. Deploying an older revision does not undo application data migrations.</Text>
        </>}
        {props.error && <Alert color="red">{props.error}</Alert>}
        <Group justify="flex-end" gap="xs">
          {!plan && !busy && <Button variant="default" onClick={props.onPlan}>Try again</Button>}
          <Button loading={busy} disabled={!plan || !props.canDeploy} leftSection={<IconRocket size={14} />} onClick={props.onDeploy}>{props.recreate ? (props.pull ? 'Pull & recreate' : 'Recreate') : 'Deploy'}</Button>
        </Group>
      </Stack></Tabs.Panel>
      <Tabs.Panel value="deploy"><Stack>
        {!job && <Group gap="xs"><Loader size="xs" /><Text size="sm" c="dimmed">Deployment queued. Waiting for the device…</Text></Group>}
        {job && <>
          <StatusText tone={deploymentTone(job.status)}>{deploymentTitle(job)}</StatusText>
          <Text size="sm">{[job.stage, job.message].filter(Boolean).join(' · ') || 'Waiting for the device to pick up the job.'}</Text>
          {['queued', 'running'].includes(job.status) && <Group gap="xs"><Loader size="xs" /><Text size="sm" c="dimmed">You can close this; the deployment keeps going and shows under Activity.</Text></Group>}
          {job.status === 'failed' && <Alert color="red" title="The deployment failed">Check the device's Compose output under Activity, fix the stack, and deploy again.</Alert>}
        </>}
        <Group justify="flex-end"><Button variant={job && ['queued', 'running'].includes(job.status) ? 'default' : 'filled'} onClick={props.onClose}>Close</Button></Group>
      </Stack></Tabs.Panel>
    </Tabs>
  </Modal>
}
