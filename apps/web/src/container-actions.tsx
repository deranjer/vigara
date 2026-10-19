import { useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Badge, Button, Checkbox, Code, Group, List, Loader, Modal, Progress, Select, Stack, Switch, Table, Text, TextInput } from '@mantine/core'
import { actionLabels, actionPast, actionsFor, canManage, drainRecords, manageBlocker, needsConfirmation, runLimited, shortImageId, type ActionName, type BulkOutcome, type FleetRow, type RecreateResult, type RecreateStep } from './container-utils'

export type ActionResult = { id: string; name: string; action: string; changed: boolean; message?: string }
export type ActionOptions = { force?: boolean; removeVolumes?: boolean; signal?: string; timeoutSeconds?: number }

const base = (hostId: string, reference: string) => `/api/containers/${encodeURIComponent(hostId)}/${encodeURIComponent(reference)}`

async function failureMessage(response: Response, fallback: string): Promise<string> {
  const text = (await response.text()).trim().slice(0, 400)
  return text || fallback
}

/** Runs one lifecycle action. Throws an Error whose message is safe to show the user. */
export async function postAction(hostId: string, reference: string, action: ActionName, options: ActionOptions = {}): Promise<ActionResult> {
  const response = await fetch(`${base(hostId, reference)}/actions`, {
    method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action, ...options }),
  })
  if (!response.ok) throw new Error(await failureMessage(response, `The ${action} request failed.`))
  return response.json() as Promise<ActionResult>
}

/**
 * Recreates a container and reports each step as it arrives. Resolves with the result once the
 * host confirms the replacement is running; rejects with the host's explanation otherwise.
 */
export async function streamRecreate(hostId: string, reference: string, pull: boolean, onStep: (step: RecreateStep) => void, signal?: AbortSignal): Promise<RecreateResult> {
  const response = await fetch(`${base(hostId, reference)}/recreate`, {
    method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ pull }), signal,
  })
  if (!response.ok) throw new Error(await failureMessage(response, 'The recreate request failed.'))
  if (!response.body) throw new Error('This browser cannot show progress for a recreate.')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let result: RecreateResult | undefined
  for (;;) {
    const { value, done } = await reader.read()
    if (value) buffer += decoder.decode(value, { stream: !done })
    const drained = drainRecords<RecreateStep & { code?: string }>(buffer)
    buffer = drained.rest
    for (const record of drained.records) {
      if (record.step === 'error') throw new Error(record.message || 'The recreate failed.')
      if (record.step === 'done' && record.result) result = record.result
      onStep(record)
    }
    if (done) break
  }
  if (!result) throw new Error('The host stopped reporting before the recreate finished. Check the container before trying again.')
  return result
}

type ConfirmProps = {
  opened: boolean; title: string; confirmLabel: string; color?: string; busy: boolean; error: string
  requireText?: string; onConfirm: () => void; onClose: () => void; children?: React.ReactNode
}

function ConfirmDialog({ opened, title, confirmLabel, color = 'red', busy, error, requireText, onConfirm, onClose, children }: ConfirmProps) {
  const [typed, setTyped] = useState('')
  const [seen, setSeen] = useState(opened)
  if (seen !== opened) { setSeen(opened); setTyped('') }
  const allowed = !requireText || typed === requireText
  return <Modal opened={opened} onClose={busy ? () => undefined : onClose} title={title} centered closeOnClickOutside={!busy}>
    <Stack>
      {children}
      {requireText && <TextInput label={<>Type <Code>{requireText}</Code> to confirm</>} value={typed} onChange={(event) => setTyped(event.currentTarget.value)} autoComplete="off" data-autofocus />}
      {error && <Alert color="red" title="That did not work">{error}</Alert>}
      <Group justify="flex-end"><Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button><Button color={color} loading={busy} disabled={!allowed} onClick={onConfirm}>{confirmLabel}</Button></Group>
    </Stack>
  </Modal>
}

const signals = [{ value: 'KILL', label: 'KILL (immediate, cannot be handled)' }, { value: 'TERM', label: 'TERM (ask politely)' }, { value: 'HUP', label: 'HUP (reload)' }, { value: 'INT', label: 'INT' }, { value: 'QUIT', label: 'QUIT' }, { value: 'USR1', label: 'USR1' }, { value: 'USR2', label: 'USR2' }]

type BarProps = {
  hostId: string; reference: string; name: string; state: string; project?: string
  role: string; hostTier?: string
  onChanged: () => void; onRemoved: () => void; onRecreated: (name: string) => void
}

/** The buttons for changing one container, or the reason they are unavailable. */
export function ActionBar({ hostId, reference, name, state, project, role, hostTier, onChanged, onRemoved, onRecreated }: BarProps) {
  const [busy, setBusy] = useState<ActionName | null>(null)
  const [notice, setNotice] = useState<{ color: string; text: string } | null>(null)
  const [confirm, setConfirm] = useState<ActionName | null>(null)
  const [error, setError] = useState('')
  const [signal, setSignal] = useState<string | null>('KILL')
  const [force, setForce] = useState(true)
  const [volumes, setVolumes] = useState(false)
  const [recreateOpen, setRecreateOpen] = useState(false)

  const blocker = manageBlocker(role, hostTier)
  if (blocker) return <Text size="sm" c="dimmed" mt="sm">{blocker}</Text>
  const available = actionsFor(state)

  async function run(action: ActionName, options: ActionOptions = {}) {
    setBusy(action); setError(''); setNotice(null)
    try {
      const result = await postAction(hostId, reference, action, options)
      if (action === 'remove') { setConfirm(null); onRemoved(); return }
      setConfirm(null)
      setNotice({ color: 'green', text: result.message || (result.changed ? `${name} was ${actionPast[action]}.` : `${name} was already in that state.`) })
      onChanged()
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : 'The action failed.'
      if (confirm) setError(message); else setNotice({ color: 'red', text: message })
    } finally { setBusy(null) }
  }

  function choose(action: ActionName) {
    setError('')
    if (needsConfirmation(action)) { setForce(state === 'running' || state === 'paused' || state === 'restarting'); setVolumes(false); setSignal('KILL'); setConfirm(action) } else void run(action)
  }

  return <Stack gap="xs" mt="sm">
    <Group gap="xs">
      {available.filter((action) => action !== 'remove').map((action) => <Button key={action} size="xs" variant={action === 'kill' ? 'light' : 'default'} color={action === 'kill' ? 'red' : undefined} loading={busy === action} disabled={busy !== null} onClick={() => choose(action)}>{actionLabels[action]}</Button>)}
      <Button size="xs" variant="default" disabled={busy !== null} onClick={() => setRecreateOpen(true)}>Recreate…</Button>
      <Button size="xs" variant="light" color="red" disabled={busy !== null} onClick={() => choose('remove')}>Remove…</Button>
    </Group>
    {notice && <Alert color={notice.color} withCloseButton onClose={() => setNotice(null)}>{notice.text}</Alert>}

    <ConfirmDialog opened={confirm === 'kill'} title={`Kill ${name}?`} confirmLabel="Send signal" busy={busy === 'kill'} error={error} onClose={() => setConfirm(null)} onConfirm={() => void run('kill', { signal: signal ?? 'KILL' })}>
      <Text size="sm">This sends a signal straight to the container's main process. Work in progress can be lost, and with KILL nothing gets a chance to clean up.</Text>
      <Select label="Signal" value={signal} onChange={setSignal} data={signals} allowDeselect={false} />
    </ConfirmDialog>

    <ConfirmDialog opened={confirm === 'remove'} title={`Remove ${name}?`} confirmLabel="Remove container" busy={busy === 'remove'} error={error} requireText={name} onClose={() => setConfirm(null)} onConfirm={() => void run('remove', { force, removeVolumes: volumes })}>
      <Text size="sm">The container is deleted. Its image and named volumes stay; changes made inside the container's own filesystem are lost.</Text>
      {(state === 'running' || state === 'paused' || state === 'restarting') && <Checkbox checked={force} onChange={(event) => setForce(event.currentTarget.checked)} label="Force removal (stops the container first)" />}
      <Checkbox checked={volumes} onChange={(event) => setVolumes(event.currentTarget.checked)} label="Also delete its anonymous volumes" description="Named volumes and bind mounts are never deleted." />
      {project && <Alert color="yellow" title="Managed by Docker Compose">This container belongs to the Compose project {project}. Compose may create it again the next time that project is started.</Alert>}
    </ConfirmDialog>

    <RecreateDialog opened={recreateOpen} hostId={hostId} reference={reference} name={name} project={project} onClose={() => setRecreateOpen(false)} onDone={(result) => { onChanged(); onRecreated(result.name) }} />
  </Stack>
}

const stepLabels: Record<string, string> = {
  pull: 'Pull the image', create: 'Create the replacement', stop_old: 'Stop the original', swap: 'Swap names', start: 'Start the replacement', verify: 'Check it stays up', cleanup: 'Remove the original', rollback: 'Restore the original',
}

function RecreateDialog({ opened, hostId, reference, name, project, onClose, onDone }: { opened: boolean; hostId: string; reference: string; name: string; project?: string; onClose: () => void; onDone: (result: RecreateResult) => void }) {
  const [pull, setPull] = useState(true)
  const [phase, setPhase] = useState<'ready' | 'running' | 'done' | 'failed'>('ready')
  const [steps, setSteps] = useState<RecreateStep[]>([])
  const [error, setError] = useState('')
  const [result, setResult] = useState<RecreateResult | null>(null)
  const abort = useRef<AbortController | null>(null)
  const [seen, setSeen] = useState(opened)
  if (seen !== opened) { setSeen(opened); if (opened) { setPhase('ready'); setSteps([]); setError(''); setResult(null) } }
  useEffect(() => () => abort.current?.abort(), [])

  const layer = useMemo(() => [...steps].reverse().find((step) => step.step === 'pull_layer'), [steps])
  const reached = useMemo(() => Object.keys(stepLabels).filter((key) => steps.some((step) => step.step === key)), [steps])
  const notes = steps.filter((step) => step.step === 'skipped' && step.message)

  async function start() {
    setPhase('running'); setSteps([]); setError('')
    const controller = new AbortController()
    abort.current = controller
    try {
      const outcome = await streamRecreate(hostId, reference, pull, (step) => setSteps((current) => [...current, step]), controller.signal)
      setResult(outcome); setPhase('done'); onDone(outcome)
    } catch (cause) {
      if (controller.signal.aborted) return
      setError(cause instanceof Error ? cause.message : 'The recreate failed.'); setPhase('failed')
    }
  }

  const running = phase === 'running'
  return <Modal opened={opened} onClose={running ? () => undefined : onClose} title={`Recreate ${name}`} centered closeOnClickOutside={!running} closeOnEscape={!running}>
    <Stack>
      {phase === 'ready' && <>
        <Text size="sm">A new container is built from this one's settings (image, environment, mounts, ports, networks, and limits). It replaces the original only after it is created, and the original is restored if the replacement does not stay up.</Text>
        <Switch checked={pull} onChange={(event) => setPull(event.currentTarget.checked)} label="Pull the image again first" description="Picks up a newer image when the tag, such as latest, has moved. Public registries only for now." />
        {project && <Alert color="yellow" title="Managed by Docker Compose">This container belongs to the Compose project {project}. The replacement keeps its labels, so Compose still recognises it, but changes you make to the Compose file are not applied by recreating here.</Alert>}
        <Group justify="flex-end"><Button variant="default" onClick={onClose}>Cancel</Button><Button onClick={() => void start()}>{pull ? 'Pull and recreate' : 'Recreate'}</Button></Group>
      </>}
      {phase !== 'ready' && <>
        <List spacing={6} size="sm" center icon={<Badge size="xs" color="green" variant="filled" circle> </Badge>}>
          {reached.map((key, index) => <List.Item key={key} icon={running && index === reached.length - 1 ? <Loader size={14} /> : <Badge size="xs" color={key === 'rollback' ? 'orange' : 'green'} variant="filled" circle> </Badge>}>
            {stepLabels[key]}{key === 'pull' && layer && running ? <Text span size="xs" c="dimmed"> — {layer.message} {layer.total ? `${Math.min(100, Math.round(((layer.current ?? 0) / layer.total) * 100))}%` : ''}</Text> : null}</List.Item>)}
        </List>
        {running && layer?.total ? <Progress value={Math.min(100, ((layer.current ?? 0) / layer.total) * 100)} size="sm" animated /> : null}
        {notes.map((note, index) => <Alert key={index} color="gray">{note.message}</Alert>)}
        {phase === 'failed' && <Alert color="red" title="The recreate did not complete">{error}</Alert>}
        {phase === 'done' && result && <Alert color="green" title="Recreated">
          <Text size="sm">{result.started ? `${result.name} is running again.` : `${result.name} was recreated and left stopped, as the original was.`}</Text>
          <Text size="sm" mt={4}>{result.imageChanged ? <>The image changed from <Code>{shortImageId(result.previousImageId)}</Code> to <Code>{shortImageId(result.imageId)}</Code>.</> : <>The image is unchanged (<Code>{shortImageId(result.imageId)}</Code>).</>}</Text></Alert>}
        <Group justify="flex-end">{!running && <Button onClick={onClose}>Close</Button>}{running && <Text size="xs" c="dimmed">Keep this window open until it finishes.</Text>}</Group>
      </>}
    </Stack>
  </Modal>
}

type BulkProps = { rows: FleetRow[]; role: string; onFinished: () => void; onClear: () => void }

/** Acts on every selected container, a few at a time, and reports each result. */
export function BulkBar({ rows, role, onFinished, onClear }: BulkProps) {
  const [action, setAction] = useState<ActionName | null>(null)
  const [force, setForce] = useState(true)
  const [volumes, setVolumes] = useState(false)
  const [typed, setTyped] = useState('')
  const [outcomes, setOutcomes] = useState<BulkOutcome[] | null>(null)
  const [busy, setBusy] = useState(false)
  const manageable = rows.filter((row) => canManage(role, row.hostTier))
  if (manageable.length === 0) return null
  const actions: ActionName[] = ['start', 'stop', 'restart', 'pause', 'unpause', 'kill', 'remove']

  async function execute() {
    if (!action) return
    setBusy(true)
    const results = await runLimited(manageable, 3, async (row): Promise<BulkOutcome> => {
      try {
        const result = await postAction(row.hostId, row.name || row.id, action, action === 'remove' ? { force, removeVolumes: volumes } : action === 'kill' ? { signal: 'KILL' } : {})
        return { row, ok: true, message: result.message || (result.changed ? `${actionPast[action]}` : 'already in that state') }
      } catch (cause) { return { row, ok: false, message: cause instanceof Error ? cause.message : 'failed' } }
    })
    setOutcomes(results); setBusy(false); onFinished()
  }
  function close() { setAction(null); setOutcomes(null); setTyped(''); if (outcomes) onClear() }

  const confirmWord = action === 'remove' ? `remove ${manageable.length}` : ''
  return <>
    <Group gap="xs" mb="md" p="xs" style={{ background: 'var(--mantine-color-default-hover)', borderRadius: 8 }}>
      <Text size="sm" fw={600}>{manageable.length} selected</Text>
      {actions.map((name) => <Button key={name} size="xs" variant={name === 'remove' || name === 'kill' ? 'light' : 'default'} color={name === 'remove' || name === 'kill' ? 'red' : undefined} onClick={() => { setAction(name); setForce(true); setVolumes(false); setTyped('') }}>{actionLabels[name]}</Button>)}
      <Button size="xs" variant="subtle" onClick={onClear}>Clear selection</Button>
    </Group>
    <Modal opened={action !== null} onClose={busy ? () => undefined : close} title={action ? `${actionLabels[action]} ${manageable.length} container${manageable.length === 1 ? '' : 's'}?` : ''} centered size="lg" closeOnClickOutside={!busy}>
      {action && !outcomes && <Stack>
        <Text size="sm">This applies to:</Text>
        <List size="sm" spacing={2}>{manageable.slice(0, 8).map((row) => <List.Item key={`${row.hostId}/${row.id}`}>{row.name} <Text span size="xs" c="dimmed">on {row.hostName}</Text></List.Item>)}{manageable.length > 8 && <List.Item>and {manageable.length - 8} more</List.Item>}</List>
        {action === 'remove' && <>
          <Checkbox checked={force} onChange={(event) => setForce(event.currentTarget.checked)} label="Force removal of running containers" />
          <Checkbox checked={volumes} onChange={(event) => setVolumes(event.currentTarget.checked)} label="Also delete their anonymous volumes" />
          <TextInput label={<>Type <Code>{confirmWord}</Code> to confirm</>} value={typed} onChange={(event) => setTyped(event.currentTarget.value)} autoComplete="off" />
        </>}
        {action === 'kill' && <Alert color="orange">Each container's main process receives KILL and gets no chance to clean up.</Alert>}
        <Group justify="flex-end"><Button variant="default" disabled={busy} onClick={close}>Cancel</Button><Button color={action === 'remove' || action === 'kill' ? 'red' : undefined} loading={busy} disabled={action === 'remove' && typed !== confirmWord} onClick={() => void execute()}>{actionLabels[action]}</Button></Group>
      </Stack>}
      {outcomes && <Stack>
        <Text size="sm">{outcomes.filter((outcome) => outcome.ok).length} of {outcomes.length} succeeded.</Text>
        <Table verticalSpacing="xs"><Table.Tbody>{outcomes.map((outcome) => <Table.Tr key={`${outcome.row.hostId}/${outcome.row.id}`}><Table.Td>{outcome.row.name}</Table.Td><Table.Td><Text size="sm" c={outcome.ok ? 'green' : 'red'}>{outcome.message}</Text></Table.Td></Table.Tr>)}</Table.Tbody></Table>
        <Group justify="flex-end"><Button onClick={close}>Close</Button></Group>
      </Stack>}
    </Modal>
  </>
}
