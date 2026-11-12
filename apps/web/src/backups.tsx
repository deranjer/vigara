import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Checkbox, Drawer, Group, Loader, Modal, Select, Stack, Table, Text, Title, UnstyledButton } from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { BackupFields, newBackupConfig, setupErrors, SnapshotBrowser, type BackupConfig, type ManagedView } from './backup-config'
import { isAbsoluteBackupPath } from './backup-path-utils'
import { lastRun, pathCovers, policyLabel, runDuration, runState, runStrip, scheduleText, uncoveredVolumes, untilTime, type BackupPolicy, type BackupRun, type UncoveredVolume, type VolumeInfo } from './backup-utils'
import { formatBytes } from './container-utils'
import { formatRoute, route } from './route'
import { StatusText } from './status'
import { AttentionRow, Panel } from './ui'

type DeviceRef = { id: string; name: string; agentId?: string | null }
type Host = { agentId: string; name: string; state: string; containers: unknown[] }
type VolumeHost = { agentId: string; name: string; volumes: VolumeInfo[] }

async function read<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { credentials: 'same-origin', ...init })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 300) || `Request failed (${response.status}).`)
  return await response.json() as T
}

/** Every live backup policy, across pages. */
export async function loadPolicies(): Promise<BackupPolicy[]> {
  const all: BackupPolicy[] = []
  for (let offset = 0; ; offset += 100) {
    const response = await fetch(`/api/backups/policies?limit=100&offset=${offset}`, { credentials: 'same-origin' })
    if (!response.ok) throw new Error('Backup policies could not be loaded.')
    all.push(...await response.json() as BackupPolicy[])
    if (response.headers.get('X-Has-More') !== 'true') return all
  }
}

export const loadRuns = (policyId: string, limit = 60) => read<BackupRun[]>(`/api/backups/policies/${encodeURIComponent(policyId)}/history?limit=${limit}`)

const deviceBase = (deviceId: string) => `/api/backups/devices/${encodeURIComponent(deviceId)}`

/** The 14-day strip: one square per day, coloured by how that day's runs went. */
export function RunStrip({ runs }: { runs: BackupRun[] }) {
  const days = runStrip(runs, new Date())
  const label = { ok: 'backed up', fail: 'failed', warn: 'partly backed up', running: 'running', none: 'no run' }
  return <span className="vg-runs" role="img" aria-label={`Last 14 days: ${days.filter((d) => d.kind === 'fail').length} failed, ${days.filter((d) => d.kind === 'ok').length} backed up`}>
    {days.map((d) => <i key={d.day} data-kind={d.kind} title={`${d.day}: ${label[d.kind]}`} />)}
  </span>
}

export function Backups({ role }: { role: string }) {
  const admin = role === 'admin'
  const [policies, setPolicies] = useState<BackupPolicy[] | null>(null)
  const [runs, setRuns] = useState<Record<string, BackupRun[]>>({})
  const [devices, setDevices] = useState<DeviceRef[]>([])
  const [hosts, setHosts] = useState<Host[] | null>(null)
  const [volumes, setVolumes] = useState<VolumeHost[] | null>(null)
  const [unchecked, setUnchecked] = useState(0)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<string | null>(null)
  const [sheet, setSheet] = useState<{ deviceId?: string; add?: string[] } | null>(null)
  const [refresh, setRefresh] = useState(0)

  useEffect(() => {
    let active = true
    const load = async () => {
      try {
        const [list, inventory] = await Promise.all([loadPolicies(), read<{ devices: DeviceRef[] }>('/api/inventory/devices?limit=100&offset=0')])
        const entries = await Promise.all(list.map(async (p) => [p.id, await loadRuns(p.id).catch(() => [] as BackupRun[])] as const))
        if (!active) return
        setPolicies(list); setDevices(inventory.devices); setRuns(Object.fromEntries(entries)); setError('')
      } catch (e) { if (active) setError(e instanceof Error ? e.message : 'Backups could not be loaded.') }
    }
    void load()
    const timer = window.setInterval(() => { void load() }, 30_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [refresh])

  // Volumes come live from each online Docker host, so they load once rather than on every refresh.
  useEffect(() => {
    let active = true
    void read<{ hosts: Host[] }>('/api/containers').then(async ({ hosts: list }) => {
      if (!active) return
      setHosts(list)
      const online = list.filter((h) => h.state === 'ok')
      const results = await Promise.all(online.map((h) => read<{ volumes?: VolumeInfo[] | null }>(`/api/docker/${encodeURIComponent(h.agentId)}/volumes`)
        .then((v) => ({ agentId: h.agentId, name: h.name, volumes: v.volumes ?? [] })).catch(() => null)))
      if (!active) return
      setVolumes(results.filter((r): r is VolumeHost => r !== null))
      setUnchecked(list.length - results.filter(Boolean).length)
    }).catch(() => { if (active) { setVolumes([]); setHosts([]) } })
    return () => { active = false }
  }, [refresh])

  const deviceName = useCallback((id: string) => devices.find((d) => d.id === id)?.name ?? 'Unknown device', [devices])
  const uncovered = useMemo(() => policies && volumes ? uncoveredVolumes(volumes, policies) : [], [policies, volumes])
  const byDevice = useMemo(() => {
    const groups = new Map<string, UncoveredVolume[]>()
    for (const v of uncovered) groups.set(v.agentId, [...groups.get(v.agentId) ?? [], v])
    return [...groups.values()]
  }, [uncovered])
  const current = policies?.find((p) => p.id === selected) ?? policies?.[0] ?? null
  const deviceOfAgent = (agentId: string) => devices.find((d) => d.agentId === agentId)?.id
  const volumeCount = volumes?.reduce((n, h) => n + h.volumes.filter((v) => !v.anonymous).length, 0) ?? 0
  const containerCount = hosts?.reduce((n, h) => n + h.containers.length, 0) ?? 0

  if (error && !policies) return <Alert color="red" title="Backups could not be loaded">{error}</Alert>
  if (!policies) return <Group justify="center" p="xl"><Loader size="sm" /></Group>

  return <Stack gap="lg">
    <Group justify="space-between" align="flex-end">
      <Text c="dimmed" size="sm">Read-only snapshots of folders and volumes. Vigara never changes or restores anything on its own.</Text>
      {admin && policies.length > 0 && <Button leftSection={<IconPlus size={16} />} onClick={() => setSheet({})}>New policy</Button>}
    </Group>
    {error && <Alert color="red" withCloseButton onClose={() => setError('')}>{error}</Alert>}

    {byDevice.length > 0 && <Panel flush>
      {byDevice.map((group) => {
        const deviceId = deviceOfAgent(group[0].agentId)
        const names = group.map((v) => v.name)
        return <AttentionRow key={group[0].agentId} tone="warn"
          title={`${group.length} ${group.length === 1 ? 'volume isn’t' : 'volumes aren’t'} backed up on ${group[0].deviceName}`}
          detail={`${names.slice(0, 8).join(', ')}${names.length > 8 ? ` and ${names.length - 8} more` : ''}`}
          action={admin && deviceId ? 'Add to a policy' : 'View volumes'}
          onAction={() => { if (admin && deviceId) setSheet({ deviceId, add: group.map((v) => v.path) }); else window.location.hash = formatRoute(route('volumes', group[0].agentId)) }} />
      })}
    </Panel>}
    {unchecked > 0 && <Text size="xs" c="dimmed">Volumes on {unchecked} {unchecked === 1 ? 'device' : 'devices'} weren’t checked: no Docker connection right now.</Text>}

    {policies.length === 0 ? <Panel>
      <Stack align="center" gap="xs" py="xl" ta="center">
        <Title order={3}>No backups set up yet</Title>
        <Text c="dimmed" maw={520}>Vigara backs up folders and Docker volumes as encrypted, read-only snapshots with restic. You can browse and download any snapshot here; it never restores anything on its own.</Text>
        {admin ? <Button mt="xs" onClick={() => setSheet({})}>Set up your first policy</Button> : <Text size="sm">Ask an administrator to set up backups.</Text>}
        {volumes && volumes.length > 0 && <Text size="xs" c="dimmed">{containerCount} containers · {volumeCount} named volumes found</Text>}
      </Stack>
    </Panel> : <>
      <Panel title="Policies" count={policies.length} flush>
        <div className="table-scroll"><Table className="vg-table" highlightOnHover verticalSpacing="sm">
          <Table.Thead><Table.Tr><Table.Th>Policy</Table.Th><Table.Th>Schedule</Table.Th><Table.Th>Last 14 days</Table.Th><Table.Th>Last run</Table.Th><Table.Th ta="right">Next</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{policies.map((p) => {
            const list = runs[p.id] ?? []
            const last = lastRun(list)
            const state = last && runState(last)
            return <Table.Tr key={p.id} aria-selected={current?.id === p.id} className={current?.id === p.id ? 'vg-row-selected' : undefined} onClick={() => setSelected(p.id)} style={{ cursor: 'pointer' }}>
              <Table.Td><UnstyledButton onClick={() => setSelected(p.id)} fw={600} fz="sm">{policyLabel(p, deviceName(p.deviceId))}</UnstyledButton>
                <Text size="xs" c="dimmed" truncate maw={280}>{p.includedPaths.length === 1 ? p.includedPaths[0] : `${p.includedPaths.length} folders`}</Text></Table.Td>
              <Table.Td>{scheduleText(p)}</Table.Td>
              <Table.Td><RunStrip runs={list} /></Table.Td>
              <Table.Td>{state ? <StatusText tone={state.tone}>{state.text}{runDuration(last) ? ` · ${runDuration(last)}` : ''}</StatusText> : <Text size="sm" c="dimmed">No runs yet</Text>}</Table.Td>
              <Table.Td ta="right"><Text size="sm" c="dimmed">{p.enabled ? untilTime(p.nextRunAt) : '—'}</Text></Table.Td>
            </Table.Tr>
          })}</Table.Tbody>
        </Table></div>
      </Panel>
      {current && <RunsPanel key={current.id} policy={current} label={policyLabel(current, deviceName(current.deviceId))} runs={runs[current.id] ?? []} admin={admin} onEdit={() => setSheet({ deviceId: current.deviceId })} />}
    </>}

    <PolicySheet opened={!!sheet} initial={sheet ?? {}} devices={devices} policies={policies} volumes={volumes ?? []} onClose={() => setSheet(null)} onSaved={() => { setSheet(null); setRefresh((n) => n + 1) }} />
  </Stack>
}

/** Runs of one policy, newest first, with Browse files for snapshots the server can read. */
function RunsPanel({ policy, label, runs, admin, onEdit }: { policy: BackupPolicy; label: string; runs: BackupRun[]; admin: boolean; onEdit: () => void }) {
  const managed = policy.profileId === 'managed'
  const [view, setView] = useState<ManagedView | null>(null)
  const [browse, setBrowse] = useState<{ snapshot: string; when: string } | null>(null)
  const [open, setOpen] = useState<string | null>(null)
  useEffect(() => {
    if (!managed) return
    let active = true
    void read<ManagedView>(`${deviceBase(policy.deviceId)}/managed`).then((v) => { if (active) setView(v) }).catch(() => undefined)
    return () => { active = false }
  }, [managed, policy.deviceId])
  const browsable = managed && !!view?.config?.serverRepository
  const shown = runs.filter((r) => runState(r).kind !== 'skipped').slice(0, 20)
  return <Panel title={`Runs · ${label}`} flush actions={<Group gap="xs">
    {managed && view?.errorCode && <StatusText tone="crit">{setupErrors[view.errorCode] ?? 'Setup needs attention'}</StatusText>}
    {managed && view?.configured && view.revision !== view.appliedRevision && !view.errorCode && <StatusText tone="info">Waiting for the device to apply changes</StatusText>}
    {admin && managed && <Button size="xs" variant="default" onClick={onEdit}>Edit policy</Button>}
  </Group>}>
    {shown.length === 0 ? <Text size="sm" c="dimmed" p="md">No runs yet. The first one starts {untilTime(policy.nextRunAt) === 'due now' ? 'shortly' : untilTime(policy.nextRunAt)}.</Text>
      : <div className="table-scroll"><Table className="vg-table" verticalSpacing="xs">
        <Table.Tbody>{shown.map((r) => {
          const s = runState(r)
          const size = r.progress?.bytesTotal
          return [<Table.Tr key={r.id}>
            <Table.Td><StatusText tone={s.tone}>{new Date(r.scheduledFor).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })}</StatusText></Table.Td>
            <Table.Td><Text size="sm" c={s.kind === 'fail' ? undefined : 'dimmed'}>{s.text}{runDuration(r) ? ` · ${runDuration(r)}` : ''}</Text></Table.Td>
            <Table.Td className="vg-num"><Text size="sm" c="dimmed">{size ? formatBytes(size) : ''}</Text></Table.Td>
            <Table.Td className="vg-num">
              {s.kind === 'fail' && (r.errorMessage || r.errorCode) && <Button size="xs" variant="subtle" onClick={() => setOpen(open === r.id ? null : r.id)}>{open === r.id ? 'Hide error' : 'Show error'}</Button>}
              {browsable && r.snapshotIds?.length ? <Button size="xs" variant="subtle" onClick={() => setBrowse({ snapshot: r.snapshotIds![0], when: new Date(r.scheduledFor).toLocaleString() })}>Browse files</Button> : null}
            </Table.Td>
          </Table.Tr>, open === r.id && <Table.Tr key={`${r.id}-error`}><Table.Td colSpan={4}><Text size="sm" c="red">{r.errorMessage || r.errorCode}{r.errorMessage && r.errorCode ? ` (${r.errorCode})` : ''}</Text></Table.Td></Table.Tr>]
        })}</Table.Tbody>
      </Table></div>}
    {managed && view?.configured && !view.config?.serverRepository && <Text size="xs" c="dimmed" p="md" pt={0}>To browse snapshots here, set the repository path on the server in this policy.</Text>}
    <Modal opened={!!browse} onClose={() => setBrowse(null)} size="xl" title={browse ? `${label} › ${browse.when}` : ''}>
      {browse && <Stack gap="xs"><SnapshotBrowser base={deviceBase(policy.deviceId)} snapshotId={browse.snapshot} /><Text size="xs" c="dimmed">Read-only. Download a file or a folder as a ZIP; nothing is restored.</Text></Stack>}
    </Modal>
  </Panel>
}

/** New or edited backup policy for one device, in a side sheet. */
function PolicySheet({ opened, initial, devices, policies, volumes, onClose, onSaved }: { opened: boolean; initial: { deviceId?: string; add?: string[] }; devices: DeviceRef[]; policies: BackupPolicy[]; volumes: VolumeHost[]; onClose: () => void; onSaved: () => void }) {
  const [deviceId, setDeviceId] = useState<string | null>(null)
  const [view, setView] = useState<ManagedView | null>(null)
  const [draft, setDraft] = useState<BackupConfig | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [pending, setPending] = useState(false)
  // Paths to pre-select are read once per opening, so a re-render never reloads over edits in progress.
  const addRef = useRef(initial.add)
  useEffect(() => { if (opened) { addRef.current = initial.add; setDeviceId(initial.deviceId ?? null); setError('') } }, [opened, initial])
  useEffect(() => {
    setView(null); setDraft(null)
    if (!opened || !deviceId) return
    let active = true
    void read<ManagedView>(`${deviceBase(deviceId)}/managed`).then((v) => {
      if (!active) return
      const base = v.config ? { ...v.config, expectedRevision: v.revision } : newBackupConfig()
      const add = (addRef.current ?? []).filter((p) => !base.sourcePaths.some((s) => pathCovers(s, p)))
      setView(v); setDraft({ ...base, sourcePaths: [...base.sourcePaths, ...add] })
    }).catch((e: unknown) => { if (active) setError(e instanceof Error ? e.message : 'Backup settings could not be loaded.') })
    return () => { active = false }
  }, [opened, deviceId])
  const device = devices.find((d) => d.id === deviceId)
  const deviceVolumes = volumes.find((h) => h.agentId === device?.agentId)?.volumes.filter((v) => !v.anonymous && v.mountpoint) ?? []
  const editing = !!view?.configured
  const used = new Set(policies.filter((p) => p.profileId === 'managed').map((p) => p.deviceId))
  const toggle = (path: string, on: boolean) => draft && setDraft({ ...draft, sourcePaths: on ? [...draft.sourcePaths, path] : draft.sourcePaths.filter((p) => p !== path) })
  const save = async () => {
    if (!draft || !deviceId) return
    setBusy(true); setError('')
    try {
      await read<ManagedView>(`${deviceBase(deviceId)}/managed`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...draft, sourcePaths: draft.sourcePaths.map((p) => p.trim()).filter(Boolean) }) })
      onSaved()
    } catch (e) { setError(e instanceof Error ? e.message : 'The policy could not be saved.') } finally { setBusy(false) }
  }
  const selectedSize = deviceVolumes.filter((v) => draft?.sourcePaths.includes(v.mountpoint!) && v.size > 0).reduce((n, v) => n + v.size, 0)
  return <Drawer opened={opened} onClose={() => { if (!busy) onClose() }} position="right" size="lg" title={<Title order={3}>{editing ? 'Edit backup policy' : 'New backup policy'}</Title>}>
    <Stack gap="md">
      {error && <Alert color="red">{error}</Alert>}
      <Select label="Device" placeholder="Choose a device" disabled={!!initial.deviceId || busy} value={deviceId} onChange={setDeviceId} allowDeselect={false}
        data={devices.filter((d) => d.agentId).map((d) => ({ value: d.id, label: used.has(d.id) && d.id !== initial.deviceId ? `${d.name} (has a policy; opens it)` : d.name }))} />
      {deviceId && !draft && !error && <Group><Loader size="xs" /><Text size="sm" c="dimmed">Loading…</Text></Group>}
      {view && !view.available && <Alert color="yellow">The server needs a backup master key before backups can be set up.</Alert>}
      {draft && <>
        {deviceVolumes.length > 0 && <Stack gap={6}>
          <Text size="sm" fw={500}>Docker volumes</Text>
          <div className="vg-check-list">{deviceVolumes.map((v) => <Checkbox key={v.name} aria-label={`Back up volume ${v.name}`} disabled={busy} checked={draft.sourcePaths.includes(v.mountpoint!)} onChange={(e) => toggle(v.mountpoint!, e.currentTarget.checked)}
            label={<Group gap="xs" wrap="nowrap" justify="space-between" w="100%"><Text size="sm" ff="monospace" truncate>{v.name}</Text><Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>{v.usedBy?.length ? v.usedBy.map((u) => u.name).join(', ') : 'unused'}{v.size > 0 ? ` · ${formatBytes(v.size)}` : ''}</Text></Group>} />)}</div>
          {selectedSize > 0 && <Text size="xs" c="dimmed">Selected volumes hold about {formatBytes(selectedSize)}.</Text>}
        </Stack>}
        <BackupFields value={draft} onChange={setDraft} existing={view?.initialized} disabled={busy} onPathsPendingChange={setPending} />
        <Text size="xs" c="dimmed">Changes apply when the device next connects. A run already in progress finishes first.</Text>
        <Group justify="flex-end"><Button variant="default" onClick={onClose} disabled={busy}>Cancel</Button>
          <Button loading={busy} disabled={pending || !draft.sourcePaths.length || !isAbsoluteBackupPath(draft.repository) || !view?.available} onClick={() => void save()}>{editing ? 'Save policy' : 'Create policy'}</Button></Group>
      </>}
    </Stack>
  </Drawer>
}

/** The device page's Backups tab: runs for each of this device's policies. */
export function DeviceRuns({ deviceId, deviceName }: { deviceId: string; deviceName: string }) {
  const [policies, setPolicies] = useState<BackupPolicy[] | null>(null)
  const [runs, setRuns] = useState<Record<string, BackupRun[]>>({})
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    const load = async () => {
      try {
        const list = (await loadPolicies()).filter((p) => p.deviceId === deviceId)
        const entries = await Promise.all(list.map(async (p) => [p.id, await loadRuns(p.id).catch(() => [] as BackupRun[])] as const))
        if (active) { setPolicies(list); setRuns(Object.fromEntries(entries)); setError('') }
      } catch (e) { if (active) setError(e instanceof Error ? e.message : 'Backup runs could not be loaded.') }
    }
    void load()
    const timer = window.setInterval(() => { void load() }, 30_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [deviceId])
  if (error) return <Alert color="red">{error}</Alert>
  if (!policies) return null
  return <>{policies.map((p) => <RunsPanel key={p.id} policy={p} label={policyLabel(p, deviceName)} runs={runs[p.id] ?? []} admin={false} onEdit={() => undefined} />)}</>
}
