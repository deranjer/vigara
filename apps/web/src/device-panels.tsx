import { useEffect, useState, type FormEvent } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Group, Loader, MultiSelect, Modal, Progress, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core'
import { IconBrandWindowsFilled, IconBrandAppleFilled, IconDeviceDesktopQuestion } from '@tabler/icons-react'
import { type Device } from './devices'
import { driveUsage, operatingSystem, type HardwareComponent } from './device-info-utils'
import { formatMetric } from './metric-utils'

type Awake = { weekdays: string[]; start: string; end: string; timezone: string }
type Availability = { awake: Awake | null; expectedAwake: boolean; maintenanceActive: boolean; maintenance: { id: string; startsAt: string; endsAt: string }[] }
type Incident = { id: string; dimension: string; status: string; summary: string; startedAt: string }
const weekdays = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday']
async function read<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin' })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 300) || 'Could not load information.')
  return response.json() as Promise<T>
}
const date = (value?: string | null) => value ? new Date(value).toLocaleString() : 'Not reported'

function OSLogo({ os }: { os: string }) {
  const kind = operatingSystem(os)
  if (kind === 'windows') return <IconBrandWindowsFilled size={28} color="#0078d4" aria-label="Windows" />
  if (kind === 'linux') return <svg width="28" height="32" viewBox="0 0 28 32" role="img" aria-label="Linux"><ellipse cx="14" cy="17" rx="10" ry="13" fill="#242424" /><ellipse cx="14" cy="21" rx="7" ry="9" fill="#fff" /><ellipse cx="10" cy="9" rx="3" ry="4" fill="#fff" /><ellipse cx="18" cy="9" rx="3" ry="4" fill="#fff" /><circle cx="11" cy="10" r="1.4" /><circle cx="17" cy="10" r="1.4" /><path d="M9 14 14 11 19 14 14 17Z" fill="#f6bf26" /><ellipse cx="7" cy="29" rx="6" ry="2.5" fill="#f6bf26" /><ellipse cx="21" cy="29" rx="6" ry="2.5" fill="#f6bf26" /></svg>
  if (kind === 'macos') return <IconBrandAppleFilled size={28} aria-label="macOS" />
  return <IconDeviceDesktopQuestion size={28} aria-label="Unknown operating system" />
}

function HardwareDetails({ title, status, items }: { title: string; status?: string; items?: HardwareComponent[] }) {
  return <Card withBorder padding="lg"><Group justify="space-between" mb="md"><Title order={4}>{title}</Title><Badge variant="light" color={status === 'collected' ? 'teal' : 'gray'}>{status === 'collected' ? 'Reported' : status === 'unsupported' ? 'Unsupported' : 'Not available'}</Badge></Group>
    {items?.length ? <Stack gap="md">{items.map((item, index) => <div key={index}>
      <Text fw={500}>{[item.manufacturer, item.model || item.name].filter(Boolean).join(' · ') || `${title} ${index + 1}`}</Text>
      {item.slot && <Text size="sm" c="dimmed">Slot: {item.slot}</Text>}
      {!!item.capacityBytes && <Text size="sm">{formatMetric(item.capacityBytes, 'By')}{item.speedMTs ? ` · ${item.speedMTs} MT/s` : ''}</Text>}
      {item.version && <Text size="sm" c="dimmed">{title === 'Graphics' ? 'Driver' : 'Version'}: {item.version}</Text>}
    </div>)}</Stack> : <Text size="sm" c="dimmed">{status === 'unsupported' ? 'Collection is not supported on this platform yet.' : 'The agent has not reported these details. Hardware access or system tools may be unavailable.'}</Text>}
  </Card>
}

export function DeviceInfo({ device, onReinstall }: { device: Device; onReinstall: () => void }) {
  const host = device.latest?.host
  const drives = driveUsage(device.latest?.metrics)
  const hardware = host?.hardwareInventory
  const os = host?.os || device.platform || 'Unknown'
  const fields = [
    ['Processor', host?.cpuModel || 'Not reported'],
    ['Logical CPUs', host?.logicalCpus ? String(host.logicalCpus) : 'Not reported'],
    ['Installed memory', host?.totalMemoryBytes ? formatMetric(host.totalMemoryBytes, 'By') : 'Not reported'],
    ['Architecture', host?.architecture || device.architecture || 'Not reported'],
    ['Agent version', host?.agentVersion || device.agentVersion || 'Not reported'],
    ['Uptime at observation', host?.uptimeSeconds === undefined ? 'Not reported' : `${Math.floor(host.uptimeSeconds / 86400)} days, ${Math.floor(host.uptimeSeconds / 3600) % 24} hours`],
  ]
  return <Stack>
    <Card withBorder padding="lg"><Title order={3} mb="xs">Hardware and system</Title><Text size="sm" c="dimmed" mb="lg">{device.telemetryObservedAt ? `Reported ${date(device.telemetryObservedAt)}. These details describe the latest agent snapshot.` : 'No hardware snapshot has been reported for this device.'}</Text><SimpleGrid cols={{base: 1, sm: 2, lg: 3}}><Group gap="sm" wrap="nowrap"><OSLogo os={os} /><div><Text size="xs" c="dimmed">Operating system</Text><Text fw={500}>{[os, host?.osVersion].filter(Boolean).join(' ')}</Text></div></Group>{fields.map(([label, value]) => <div key={label}><Text size="xs" c="dimmed">{label}</Text><Text fw={500}>{value}</Text></div>)}</SimpleGrid></Card>
    <Card withBorder padding="lg"><Group justify="space-between" mb="xs"><Title order={3}>Disk space</Title><Badge variant="light">{drives.length} {drives.length === 1 ? 'drive' : 'drives'}</Badge></Group><Text size="sm" c="dimmed" mb="lg">Used and free space for each reported filesystem. Each bar represents that drive’s total capacity.</Text>
      {drives.length ? <Stack gap="lg">{drives.map(drive => <div key={drive.key}>
        <Group justify="space-between" mb={6}><Group gap="xs"><Text fw={600}>{drive.mount}</Text>{drive.filesystem && <Badge color="gray" variant="light" size="sm">{drive.filesystem}</Badge>}</Group><Text size="sm">{drive.used === null ? 'Usage unavailable' : `${formatMetric(drive.used, 'By')} used`} / {formatMetric(drive.total, 'By')} total</Text></Group>
        {drive.used === null ? <Text size="sm" c="dimmed">Free space was not reported for this drive.</Text> : <><Progress.Root size={22} radius="sm"><Progress.Section value={drive.used / drive.total * 100} color={drive.used / drive.total >= .9 ? 'red' : drive.used / drive.total >= .8 ? 'orange' : 'blue'} aria-label={`${drive.mount} used space`} /><Progress.Section value={(drive.free ?? 0) / drive.total * 100} color="gray.3" aria-label={`${drive.mount} free space`} /></Progress.Root><Group justify="space-between" mt={5}><Text size="xs" c="dimmed">{(drive.used / drive.total * 100).toFixed(1)}% used</Text><Text size="xs" c="dimmed">{formatMetric(drive.free ?? 0, 'By')} free</Text></Group></>}
      </div>)}</Stack> : <Text size="sm" c="dimmed">No drive capacity has been reported yet.</Text>}
    </Card>
    <div><Title order={3} mb="xs">Hardware inventory</Title><Text size="sm" c="dimmed" mb="md">{hardware?.observedAt ? `Collected ${date(hardware.observedAt)}. Refreshed hourly by the agent.` : 'Details will appear after an updated agent reports hardware inventory.'}</Text><SimpleGrid cols={{base: 1, md: 2}}><HardwareDetails title="Graphics" status={hardware?.gpuStatus} items={hardware?.gpus} /><HardwareDetails title="Memory modules" status={hardware?.memoryStatus} items={hardware?.memoryModules} /><HardwareDetails title="Motherboard" status={hardware?.boardStatus} items={hardware?.motherboard ? [hardware.motherboard] : []} /><HardwareDetails title="Firmware / BIOS" status={hardware?.firmwareStatus} items={hardware?.firmware ? [hardware.firmware] : []} /></SimpleGrid></div>
    <Card withBorder padding="lg"><Title order={3} mb="md">Device identity and capabilities</Title><Text size="sm">Device ID: {device.id}</Text><Text size="sm">Agent ID: {device.agentId || 'No enrolled agent'}</Text><Text size="sm" mb="md">Last check-in: {date(device.lastSeenAt)}</Text><Group gap="xs">{device.capabilities.length ? device.capabilities.map((capability) => <Badge key={capability} variant="light">{capability}</Badge>) : <Text size="sm" c="dimmed">No agent capabilities reported.</Text>}</Group></Card>
    {device.agentId && <Card withBorder padding="lg"><Title order={3} mb="xs">Agent installation</Title><Text c="dimmed" size="sm" mb="md">Repair or reinstall the agent on this device while keeping its information and history. The Windows agent runs as a background service and has no tray icon.</Text><Button variant="light" onClick={onReinstall}>Reinstall agent</Button></Card>}
    {!!device.collectionErrors?.length && <Alert color="red" title="Collection issues">{device.collectionErrors.map((item, index) => <Text size="sm" key={index}>{item.capability}: {item.message}</Text>)}</Alert>}
  </Stack>
}

export function DeviceSettings({ device, onSaved, onDeleted }: { device: Device; onSaved: () => void; onDeleted: () => void }) {
  const [value, setValue] = useState<Availability | null>(null)
  const [enabled, setEnabled] = useState(false)
  const [days, setDays] = useState<string[]>(weekdays)
  const [start, setStart] = useState('08:00')
  const [end, setEnd] = useState('22:00')
  const [timezone, setTimezone] = useState(Intl.DateTimeFormat().resolvedOptions().timeZone)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    let active = true
    void read<Availability>(`/api/devices/${encodeURIComponent(device.id)}/availability`).then((next) => {
      if (!active) return
      setValue(next); setError(''); setEnabled(!!next.awake)
      if (next.awake) { setDays(next.awake.weekdays); setStart(next.awake.start); setEnd(next.awake.end); setTimezone(next.awake.timezone) }
    }).catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Could not read settings.') })
    return () => { active = false }
  }, [device.id, attempt])
  async function save(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(''); setSaved(false)
    try {
      if (enabled && (!days.length || !start || !end || start === end || !timezone.trim())) throw new Error('Choose weekdays, different start/end times and an IANA timezone.')
      const response = await fetch(`/api/devices/${encodeURIComponent(device.id)}/availability`, { method: 'PUT', credentials: 'same-origin', headers: {'Content-Type':'application/json'}, body: JSON.stringify({awake: enabled ? {weekdays: days, start, end, timezone: timezone.trim()} : null}) })
      if (!response.ok) throw new Error((await response.text()).slice(0, 300) || 'Could not save settings.')
      setSaved(true); onSaved()
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not save settings.') }
    finally { setBusy(false) }
  }
  return <Stack><Card withBorder padding="lg"><Title order={3} mb="xs">Availability settings</Title><Text c="dimmed" size="sm" mb="lg">Set when this device is expected to be awake. Outside this schedule, stale check-ins do not create new availability alerts. Overnight intervals are supported.</Text>
    {error && <Alert color="red" mb="md">{error}{!value && <Button size="xs" ml="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button>}</Alert>}
    {saved && <Alert color="green" mb="md">Availability settings saved.</Alert>}
    {!value && !error ? <Loader size="sm" /> : <form onSubmit={(event) => void save(event)}><fieldset className="settings-fields" disabled={!value || busy}><Stack>
      <Checkbox label="Use an expected awake schedule" checked={enabled} onChange={(event) => { setEnabled(event.currentTarget.checked); setSaved(false) }} />
      {enabled && <><MultiSelect label="Weekdays" data={weekdays} value={days} onChange={(next) => {setDays(next); setSaved(false)}} /><Group align="start"><TextInput type="time" label="Awake from" value={start} onChange={(event) => {setStart(event.currentTarget.value); setSaved(false)}} required /><TextInput type="time" label="Awake until" value={end} onChange={(event) => {setEnd(event.currentTarget.value); setSaved(false)}} required /></Group><TextInput label="Timezone" description="IANA timezone, for example America/New_York" value={timezone} onChange={(event) => {setTimezone(event.currentTarget.value); setSaved(false)}} required /></>}
      <Text size="xs" c="dimmed">Disabling the schedule means the device is expected awake at all times.</Text><Button type="submit" loading={busy} style={{alignSelf:'flex-start'}}>Save availability</Button>
    </Stack></fieldset></form>}
    <Title order={4} mt="xl" mb="xs">Maintenance windows</Title>{value?.maintenance?.length ? value.maintenance.map((window) => <Text size="sm" key={window.id}>{date(window.startsAt)} – {date(window.endsAt)}</Text>) : <Text size="sm" c="dimmed">{value ? 'No current or upcoming maintenance windows.' : 'Unavailable until settings load.'}</Text>}
  </Card><DeleteDevice key={device.id} device={device} onDeleted={onDeleted} /></Stack>
}


function DeleteDevice({ device, onDeleted }: { device: Device; onDeleted: () => void }) {
  const [opened, setOpened] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const close = () => { if (!busy) { setOpened(false); setConfirmation(''); setError('') } }
  async function remove(event: FormEvent) {
    event.preventDefault()
    if (confirmation !== device.name || busy) return
    setBusy(true); setError('')
    try {
      const response = await fetch(`/api/devices/${encodeURIComponent(device.id)}`, { method: 'DELETE', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: confirmation }) })
      if (!response.ok) throw new Error((await response.text()).trim().slice(0, 400) || 'Could not delete device.')
      onDeleted()
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not delete device.'); setBusy(false) }
  }
  return <Card withBorder padding="lg">
    <Title order={3} c="red" mb="xs">Delete device</Title>
    <Text size="sm" c="dimmed" mb="md">Permanently remove this device and its saved information and history. This cannot be undone.</Text>
    <Button color="red" variant="light" onClick={() => setOpened(true)}>Delete device</Button>
    <Modal opened={opened} onClose={close} title={`Delete ${device.name}?`} closeOnClickOutside={!busy} closeOnEscape={!busy} withCloseButton={!busy}>
      <form onSubmit={(event) => void remove(event)}><Stack>
        <Alert color="red" title="This cannot be undone">The device information, monitoring history, backup policies and job history, and stack deployment records will be permanently deleted. You cannot get this information back.</Alert>
        <Text size="sm">Its agent credentials will stop working. This does not uninstall the agent, stop containers, delete Git repositories, or remove backup files from their storage. Adding the device again starts a new record.</Text>
        <TextInput data-autofocus label="Type the device name to confirm" description={device.name} value={confirmation} onChange={(event) => setConfirmation(event.currentTarget.value)} disabled={busy} autoComplete="off" spellCheck={false} />
        {error && <Alert color="red">{error}</Alert>}
        <Group justify="flex-end"><Button variant="default" onClick={close} disabled={busy}>Cancel</Button><Button type="submit" color="red" loading={busy} disabled={confirmation !== device.name}>Permanently delete device</Button></Group>
      </Stack></form>
    </Modal>
  </Card>
}

export function MonitoringOverview({ devices, refresh, onOpen }: { devices: Device[]; refresh: number; onOpen: (id: string) => void }) {
  const [incidents, setIncidents] = useState<Incident[] | null>(null)
  const [error, setError] = useState('')
  const [expanded, setExpanded] = useState(false)
  useEffect(() => {
    let active = true
    void read<Incident[]>('/api/alerts/incidents').then((items) => { if (active) {setIncidents(items); setError('')} }).catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Could not load alerts.') })
    return () => { active = false }
  }, [refresh])
  const firing = incidents?.filter((incident) => incident.status === 'firing') ?? []
  const issues = devices.filter((device) => device.status === 'failed' || device.status === 'stale' || !!device.collectionErrors?.length)
  return <Stack mb="xl">
    <SimpleGrid cols={{base: 2, lg: 4}}>{[
      ['Devices', devices.length], ['Healthy', devices.filter((device) => device.status === 'healthy').length], ['Devices with issues', issues.length], ['Firing alerts', error ? 'Unavailable' : incidents ? firing.length : '…'],
    ].map(([label, count]) => <Card key={label} withBorder padding="lg"><Text c="dimmed" size="sm">{label}</Text><Text size="xl" fw={700}>{count}</Text></Card>)}</SimpleGrid>
    <Card withBorder padding="lg"><Group justify="space-between" mb="md"><Title order={3}>Monitoring alerts</Title><Badge variant="light">Refreshes every 30 seconds</Badge></Group>
      {error ? <Alert color="red">{error}</Alert> : incidents === null ? <Loader size="sm" /> : !firing.length ? <Text c="dimmed" size="sm">No firing incidents in the latest alert history.</Text> : <Stack>{(expanded ? firing : firing.slice(0, 10)).map((incident) => {
        const device = devices.find((item) => item.id === incident.dimension.split('/')[0])
        return <Alert color="red" key={incident.id} title={device?.name || 'Monitoring incident'}><Text size="sm">{incident.summary}</Text><Text size="xs" mt={4}>Since {date(incident.startedAt)}</Text>{device && <Button size="xs" variant="light" mt="sm" onClick={() => onOpen(device.id)}>Open device monitoring</Button>}</Alert>
      })}{firing.length > 10 && <Button variant="subtle" onClick={() => setExpanded(!expanded)}>{expanded ? 'Show fewer alerts' : `Show all ${firing.length} alerts`}</Button>}</Stack>}
      {incidents?.length === 500 && <Text size="xs" c="dimmed" mt="sm">Showing incidents from the latest 500 records returned by the server.</Text>}
    </Card>
  </Stack>
}
