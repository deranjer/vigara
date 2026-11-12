import { useCallback, useEffect, useMemo, useState } from 'react'
import { ActionIcon, Alert, Button, Group, Loader, Menu, Modal, NumberInput, Select, Stack, Switch, Table, Tabs, Text, Textarea, TextInput, Title, Tooltip, UnstyledButton } from '@mantine/core'
import { IconBellOff, IconCheck, IconChevronDown, IconNote, IconPlus, IconTrash } from '@tabler/icons-react'
import {
  alertsChangedEvent, bucketOf, builtInRule, duration, inboxTabs, incidentDevice, incidentTarget, metricLabels, ruleCondition, ruleScope, severityTone, timelineText,
  type AlertRule, type Bucket, type FleetWindow, type Incident, type InboxTab, type RuleConfig, type Severity, type TimelineItem,
} from './alerts-utils'
import { relativeTime } from './overview-utils'
import { route, type Route } from './route'
import { StatusText } from './status'
import { Panel } from './ui'

type Device = { id: string; name: string }

async function call<T = void>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim() || `Request failed (${response.status}).`)
  return (response.status === 204 ? undefined : await response.json()) as T
}

type Data = { incidents: Incident[] | null; rules: AlertRule[] | null; timeline: TimelineItem[] | null; windows: FleetWindow[] | null; devices: Device[] }

function useAlertData(refresh: number) {
  const [data, setData] = useState<Data>({ incidents: null, rules: null, timeline: null, windows: null, devices: [] })
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    const set = <K extends keyof Data>(key: K) => (value: Data[K]) => { if (active) setData((current) => ({ ...current, [key]: value })) }
    const fail = (cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Alerts could not be loaded.') }
    const load = () => {
      void call<Incident[]>('GET', '/api/alerts/incidents').then((v) => { set('incidents')(v); if (active) setError('') }).catch(fail)
      void call<AlertRule[]>('GET', '/api/alerts/rules').then(set('rules')).catch(fail)
      void call<TimelineItem[]>('GET', '/api/alerts/timeline').then(set('timeline')).catch(fail)
      void call<FleetWindow[]>('GET', '/api/alerts/maintenance').then(set('windows')).catch(fail)
      void call<{ devices: { id: string; name: string }[] }>('GET', '/api/inventory/devices?limit=100&offset=0').then((v) => set('devices')(v.devices.map((d) => ({ id: d.id, name: d.name })))).catch(() => undefined)
    }
    load()
    const timer = window.setInterval(load, 30_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [refresh])
  return { data, error, setData }
}

const tabLabels: Record<InboxTab, string> = { open: 'Open', acknowledged: 'Acknowledged', muted: 'Muted', events: 'Events', rules: 'Rules' }
const emptyText: Record<Bucket, string> = { open: 'Nothing needs you right now.', acknowledged: 'No acknowledged alerts. Acknowledge an alert to say you have seen it; it stays here until the condition clears.', muted: 'No muted alerts.', resolved: '' }

/** The alert inbox: open, acknowledged and muted alerts, what happened, and the rules that raise them. */
export function Alerts({ route: current, onNavigate }: { route: Route; onNavigate: (route: Route) => void }) {
  const tab: InboxTab = inboxTabs.find((t) => t === current.id) ?? 'open'
  const [refresh, setRefresh] = useState(0)
  const reload = useCallback(() => setRefresh((n) => n + 1), [])
  const { data, error } = useAlertData(refresh)
  const [actionError, setActionError] = useState('')
  const now = Date.now()
  const buckets = useMemo(() => {
    const out: Record<Bucket, Incident[]> = { open: [], acknowledged: [], muted: [], resolved: [] }
    for (const incident of data.incidents ?? []) out[bucketOf(incident, now)].push(incident)
    // Critical first, then the longest-running.
    for (const list of Object.values(out)) list.sort((a, b) => (a.severity === b.severity ? 0 : a.severity === 'critical' ? -1 : 1) || new Date(a.startedAt).getTime() - new Date(b.startedAt).getTime())
    return out
  }, [data.incidents, now])
  const deviceName = useCallback((id: string) => data.devices.find((d) => d.id === id)?.name ?? 'Unknown device', [data.devices])
  const act = (work: Promise<unknown>) => { setActionError(''); void work.then(() => { reload(); window.dispatchEvent(new Event(alertsChangedEvent)) }).catch((cause: unknown) => setActionError(cause instanceof Error ? cause.message : 'That did not work.')) }
  const count: Partial<Record<InboxTab, number>> = { open: buckets.open.length, acknowledged: buckets.acknowledged.length, muted: buckets.muted.length, rules: data.rules?.length }
  const meta = tab === 'rules'
    ? `${data.rules?.filter((r) => r.enabled).length ?? '…'} of ${data.rules?.length ?? '…'} rules on · ${data.windows?.length ?? 0} maintenance window${data.windows?.length === 1 ? '' : 's'}`
    : `${buckets.open.length} open · evaluated every 30 s`

  return <Stack gap="lg" className="vg-page">
    <div className="vg-device-head">
      <div><Title order={1}>Alerts</Title><div className="vg-meta-row"><span>{data.incidents ? meta : 'Loading…'}</span></div></div>
    </div>
    {(error || actionError) && <Alert color="red" withCloseButton={!!actionError} onClose={() => setActionError('')}>{actionError || error}</Alert>}
    <Tabs value={tab} onChange={(value) => onNavigate(route('alerts', value === 'open' ? null : value))} className="vg-alert-tabs">
      <Tabs.List>{inboxTabs.map((t) => <Tabs.Tab key={t} value={t}>{tabLabels[t]}{count[t] ? <span className="vg-tab-count">{count[t]}</span> : null}</Tabs.Tab>)}</Tabs.List>
    </Tabs>
    {!data.incidents && !error ? <Group justify="center" p="xl"><Loader size="sm" /></Group>
      : tab === 'events' ? <EventList items={data.timeline} deviceName={deviceName} onNavigate={onNavigate} />
      : tab === 'rules' ? <RulesTab rules={data.rules} windows={data.windows} devices={data.devices} deviceName={deviceName} act={act} />
      : <Panel flush>
        {buckets[tab].length === 0 ? <Text c="dimmed" ta="center" p="xl" size="sm">{emptyText[tab]}</Text>
          : buckets[tab].map((incident) => <AlertRow key={incident.id} incident={incident} bucket={tab} now={now} deviceName={deviceName} act={act} onNavigate={onNavigate} />)}
      </Panel>}
  </Stack>
}

function AlertRow({ incident, bucket, now, deviceName, act, onNavigate }: { incident: Incident; bucket: Bucket; now: number; deviceName: (id: string) => string; act: (work: Promise<unknown>) => void; onNavigate: (route: Route) => void }) {
  const [noting, setNoting] = useState(false)
  const [note, setNote] = useState('')
  const base = `/api/alerts/incidents/${incident.id}`
  const device = incidentDevice(incident)
  const target = incidentTarget(incident)
  const name = device ? deviceName(device) : null
  // Newer summaries already name the device; the start time is on the age chip.
  const sub = [name && !incident.summary.includes(name) ? name : null, incident.ruleName,
    bucket === 'acknowledged' && incident.acknowledgedBy ? `acknowledged by ${incident.acknowledgedBy}` : null,
    bucket === 'muted' && incident.mutedUntil ? `muted until ${new Date(incident.mutedUntil).toLocaleString()}` : null].filter(Boolean).join(' · ')
  const saveNote = () => { if (!note.trim()) return; act(call('POST', `${base}/notes`, { body: note }).then(() => { setNote(''); setNoting(false) })) }
  return <div className="vg-attn vg-alert">
    <span className="vg-sev" data-tone={severityTone(incident.severity)} aria-label={incident.severity === 'critical' ? 'Critical' : 'Warning'} />
    <div className="vg-attn-what">
      <b>{incident.summary}</b>
      <span>{sub}</span>
      {incident.notes.map((n) => <div key={n.id} className="vg-alert-note"><IconNote size={13} aria-hidden="true" /> <b>{n.author}</b>, {relativeTime(n.createdAt, now)}: {n.body}</div>)}
      {noting && <Group gap="xs" mt={6} align="flex-end" wrap="nowrap">
        <Textarea aria-label="Note" placeholder="What you did or found" autosize minRows={1} maxRows={4} maxLength={2000} value={note} onChange={(e) => setNote(e.currentTarget.value)} style={{ flex: 1 }} autoFocus
          onKeyDown={(e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) saveNote(); if (e.key === 'Escape') setNoting(false) }} />
        <Button size="xs" onClick={saveNote} disabled={!note.trim()}>Save</Button><Button size="xs" variant="subtle" onClick={() => setNoting(false)}>Cancel</Button>
      </Group>}
    </div>
    <span className="vg-alert-age" title={`Started ${new Date(incident.startedAt).toLocaleString()}`}>{duration(incident.startedAt, now)}</span>
    <div className="vg-alert-actions">
      {bucket === 'open' && <Button size="xs" variant="default" leftSection={<IconCheck size={14} />} onClick={() => act(call('POST', `${base}/acknowledge`))}>Acknowledge</Button>}
      {bucket === 'acknowledged' && <Button size="xs" variant="default" onClick={() => act(call('DELETE', `${base}/acknowledge`))}>Move to open</Button>}
      {bucket === 'muted' ? <Button size="xs" variant="default" onClick={() => act(call('DELETE', `${base}/mute`))}>Unmute</Button>
        : <Menu position="bottom-end" withinPortal>
          <Menu.Target><Button size="xs" variant="default" leftSection={<IconBellOff size={14} />} rightSection={<IconChevronDown size={12} />}>Mute</Button></Menu.Target>
          <Menu.Dropdown>{[[60, '1 hour'], [1440, '24 hours'], [10080, '7 days']].map(([minutes, label]) => <Menu.Item key={minutes} onClick={() => act(call('PUT', `${base}/mute`, { minutes }))}>For {label}</Menu.Item>)}</Menu.Dropdown>
        </Menu>}
      <Tooltip label="Add a note"><ActionIcon variant="default" size={30} aria-label="Add a note" onClick={() => setNoting((v) => !v)}><IconNote size={15} /></ActionIcon></Tooltip>
      {target && <Button size="xs" variant="subtle" onClick={() => onNavigate(target.to)}>{target.label}</Button>}
    </div>
  </div>
}

function EventList({ items, deviceName, onNavigate }: { items: TimelineItem[] | null; deviceName: (id: string) => string; onNavigate: (route: Route) => void }) {
  if (!items) return <Group justify="center" p="xl"><Loader size="sm" /></Group>
  return <Panel title="What happened" count={items.length} flush>
    {items.length === 0 ? <Text c="dimmed" ta="center" p="xl" size="sm">Nothing recorded yet. Alerts firing and clearing, and newer images found by the update check, show up here.</Text>
      : <ul className="vg-feed">{items.map((item, index) => {
        const { tone, text: base } = timelineText(item)
        const device = item.kind !== 'image' && item.dimension ? incidentDevice({ dimension: item.dimension }) : null
        const name = device ? deviceName(device) : ''
        const text = name && !base.includes(name) ? `${base} · ${name}` : base
        const to = item.kind === 'image' ? route('updates') : route('alerts')
        return <li key={`${item.kind}:${item.incidentId ?? item.agentId}:${item.at}:${index}`}>
          <span className="vg-status-dot" data-tone={tone} aria-hidden="true" />
          {item.kind === 'image' ? <a href="#" onClick={(e) => { e.preventDefault(); onNavigate(to) }}>{text}</a> : <span>{text}</span>}
          <time dateTime={item.at} title={new Date(item.at).toLocaleString()}>{relativeTime(item.at)}</time>
        </li>
      })}</ul>}
  </Panel>
}

function RulesTab({ rules, windows, devices, deviceName, act }: { rules: AlertRule[] | null; windows: FleetWindow[] | null; devices: Device[]; deviceName: (id: string) => string; act: (work: Promise<unknown>) => void }) {
  const [editing, setEditing] = useState<AlertRule | 'new' | null>(null)
  const [addingWindow, setAddingWindow] = useState(false)
  return <Stack gap="lg">
    <Panel title="Rules" actions={<Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => setEditing('new')}>New rule</Button>} flush>
      {!rules ? <Group justify="center" p="xl"><Loader size="sm" /></Group> : <div className="table-scroll"><Table verticalSpacing="sm" highlightOnHover className="vg-rule-table">
        <Table.Thead><Table.Tr><Table.Th>Rule</Table.Th><Table.Th>Applies to</Table.Th><Table.Th>Condition</Table.Th><Table.Th>Severity</Table.Th><Table.Th className="vg-num">On</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{rules.map((rule) => <Table.Tr key={rule.id} className="vg-click-row" onClick={() => setEditing(rule)}>
          <Table.Td><UnstyledButton className="vg-row-link" onClick={(e) => { e.stopPropagation(); setEditing(rule) }}><b>{rule.name}</b></UnstyledButton></Table.Td>
          <Table.Td c="dimmed">{ruleScope(rule, deviceName)}</Table.Td>
          <Table.Td>{ruleCondition(rule)}</Table.Td>
          <Table.Td><StatusText tone={severityTone(rule.severity)}>{rule.severity === 'critical' ? 'Critical' : 'Warning'}</StatusText></Table.Td>
          <Table.Td className="vg-num" onClick={(e) => e.stopPropagation()}><Switch size="sm" aria-label={`${rule.enabled ? 'Switch off' : 'Switch on'} ${rule.name}`} checked={rule.enabled} onChange={() => act(call('POST', `/api/alerts/rules/${rule.id}/${rule.enabled ? 'disable' : 'enable'}`))} styles={{ body: { justifyContent: 'flex-end' } }} /></Table.Td>
        </Table.Tr>)}</Table.Tbody>
      </Table></div>}
    </Panel>
    <Panel title="Maintenance windows" actions={<Button size="xs" variant="default" leftSection={<IconPlus size={14} />} onClick={() => setAddingWindow(true)} disabled={!devices.length}>Add window</Button>} flush>
      {!windows?.length ? <Text c="dimmed" size="sm" p="md">No maintenance planned. During a window, a device raises no new alerts; alerts already open stay open.</Text>
        : <Table verticalSpacing="sm"><Table.Tbody>{windows.map((w) => <Table.Tr key={w.id}>
          <Table.Td fw={500}>{w.deviceName}</Table.Td>
          <Table.Td>{new Date(w.startsAt).toLocaleString()} – {new Date(w.endsAt).toLocaleString()}</Table.Td>
          <Table.Td>{w.active ? <StatusText tone="info">In progress</StatusText> : <Text size="sm" c="dimmed">Starts in {duration(new Date().toISOString(), new Date(w.startsAt).getTime())}</Text>}</Table.Td>
          <Table.Td className="vg-num"><ActionIcon variant="subtle" color="red" aria-label={`Remove maintenance window on ${w.deviceName}`} onClick={() => act(call('DELETE', `/api/devices/${w.deviceId}/maintenance/${w.id}`))}><IconTrash size={15} /></ActionIcon></Table.Td>
        </Table.Tr>)}</Table.Tbody></Table>}
    </Panel>
    {editing && <RuleEditor rule={editing === 'new' ? null : editing} devices={devices} onClose={() => setEditing(null)} act={act} />}
    {addingWindow && <WindowEditor devices={devices} onClose={() => setAddingWindow(false)} act={act} />}
  </Stack>
}

const kindOptions = [
  { value: 'device_unavailable', label: 'Device stops checking in' },
  { value: 'threshold', label: 'A metric crosses a threshold' },
  { value: 'backup_failure', label: 'A backup fails' },
  { value: 'backup_overdue', label: 'A backup is overdue' },
]

function RuleEditor({ rule, devices, onClose, act }: { rule: AlertRule | null; devices: Device[]; onClose: () => void; act: (work: Promise<unknown>) => void }) {
  const [kind, setKind] = useState(rule?.kind ?? 'device_unavailable')
  const [name, setName] = useState(rule?.name ?? '')
  const [severity, setSeverity] = useState<Severity>(rule?.severity ?? 'warning')
  const [config, setConfig] = useState<RuleConfig>(rule?.config ?? { staleAfterSeconds: 120 })
  const builtIn = builtInRule(kind)
  const set = (patch: RuleConfig) => setConfig((c) => ({ ...c, ...patch }))
  const percent = !config.metric?.endsWith('_bytes')
  const save = () => {
    const body = builtIn ? { name, severity } : { name, severity, config }
    act((rule ? call('PUT', `/api/alerts/rules/${rule.id}`, body) : call('POST', '/api/alerts/rules', { ...body, kind })).then(onClose))
  }
  const changeKind = (value: string | null) => {
    if (!value) return
    setKind(value)
    setConfig(value === 'device_unavailable' ? { staleAfterSeconds: 120 } : value === 'threshold' ? { metric: 'cpu_used_percent', operator: 'gte', threshold: 90, sustainSeconds: 600 } : value === 'backup_overdue' ? { intervalSeconds: 86400 } : {})
  }
  const backup = kind === 'backup_failure' || kind === 'backup_overdue'
  return <Modal opened onClose={onClose} title={rule ? `Edit ${rule.name}` : 'New alert rule'} size="md">
    <Stack gap="sm">
      {!rule && <Select label="Alert when" data={kindOptions} value={kind} onChange={changeKind} allowDeselect={false} />}
      <TextInput label="Name" value={name} onChange={(e) => setName(e.currentTarget.value)} maxLength={120} required />
      <Select label="Severity" data={[{ value: 'warning', label: 'Warning' }, { value: 'critical', label: 'Critical' }]} value={severity} onChange={(v) => v && setSeverity(v as Severity)} allowDeselect={false}
        description="Critical alerts are listed first." />
      {builtIn ? <Text size="sm" c="dimmed">{ruleCondition({ kind, config })}. This built-in check has no other settings.</Text> : <>
        <Select label="Device" data={[...(backup ? [] : [{ value: '', label: 'All devices' }]), ...devices.map((d) => ({ value: d.id, label: d.name }))]} value={config.deviceId ?? ''} onChange={(v) => set({ deviceId: v || undefined })} allowDeselect={false} />
        {kind === 'device_unavailable' && <NumberInput label="Alert after no check-in for (minutes)" min={1} max={10080} value={(config.staleAfterSeconds ?? 120) / 60} onChange={(v) => set({ staleAfterSeconds: Math.round(Number(v) * 60) })} />}
        {kind === 'threshold' && <>
          <Group grow align="flex-start">
            <Select label="Metric" data={Object.entries(metricLabels).map(([value, label]) => ({ value, label }))} value={config.metric ?? 'cpu_used_percent'} onChange={(v) => v && set({ metric: v })} allowDeselect={false} />
            <Select label="When it is" data={[{ value: 'gt', label: 'over' }, { value: 'gte', label: 'at or over' }, { value: 'lt', label: 'under' }, { value: 'lte', label: 'at or under' }]} value={config.operator ?? 'gte'} onChange={(v) => v && set({ operator: v })} allowDeselect={false} />
          </Group>
          <Group grow align="flex-start">
            <NumberInput label={percent ? 'Threshold (%)' : 'Threshold (GB)'} min={0} value={percent ? config.threshold ?? 0 : (config.threshold ?? 0) / 1024 ** 3} onChange={(v) => set({ threshold: percent ? Number(v) : Math.round(Number(v) * 1024 ** 3) })} />
            <NumberInput label="For at least (minutes)" min={0} max={1440} value={(config.sustainSeconds ?? 0) / 60} onChange={(v) => set({ sustainSeconds: Math.round(Number(v) * 60) })} />
          </Group>
        </>}
        {backup && <TextInput label="Backup policy key" value={config.policyKey ?? ''} onChange={(e) => set({ policyKey: e.currentTarget.value })} maxLength={128} required />}
        {kind === 'backup_overdue' && <NumberInput label="Overdue after (hours)" min={1} max={8760} value={(config.intervalSeconds ?? 86400) / 3600} onChange={(v) => set({ intervalSeconds: Math.round(Number(v) * 3600) })} />}
      </>}
      <Group justify="flex-end" mt="sm"><Button variant="default" onClick={onClose}>Cancel</Button><Button onClick={save} disabled={!name.trim()}>{rule ? 'Save rule' : 'Create rule'}</Button></Group>
    </Stack>
  </Modal>
}

function localInput(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function WindowEditor({ devices, onClose, act }: { devices: Device[]; onClose: () => void; act: (work: Promise<unknown>) => void }) {
  const [device, setDevice] = useState(devices[0]?.id ?? '')
  const [start, setStart] = useState(() => localInput(new Date()))
  const [hours, setHours] = useState<string | number>(2)
  const startsAt = new Date(start)
  const valid = !!device && !Number.isNaN(startsAt.getTime()) && Number(hours) > 0 && Number(hours) <= 720
  const save = () => {
    const endsAt = new Date(startsAt.getTime() + Number(hours) * 3600_000)
    act(call('POST', `/api/devices/${device}/maintenance`, { startsAt: startsAt.toISOString(), endsAt: endsAt.toISOString() }).then(onClose))
  }
  return <Modal opened onClose={onClose} title="Add a maintenance window" size="md">
    <Stack gap="sm">
      <Text size="sm" c="dimmed">The device raises no new alerts during the window. Alerts already open stay open.</Text>
      <Select label="Device" data={devices.map((d) => ({ value: d.id, label: d.name }))} value={device} onChange={(v) => setDevice(v ?? '')} allowDeselect={false} />
      <Group grow align="flex-start">
        <TextInput label="Starts" type="datetime-local" value={start} onChange={(e) => setStart(e.currentTarget.value)} />
        <NumberInput label="Lasts (hours)" min={0.25} max={720} step={0.5} value={hours} onChange={setHours} />
      </Group>
      <Group justify="flex-end" mt="sm"><Button variant="default" onClick={onClose}>Cancel</Button><Button onClick={save} disabled={!valid}>Add window</Button></Group>
    </Stack>
  </Modal>
}
