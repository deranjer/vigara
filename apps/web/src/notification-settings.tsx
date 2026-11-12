import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Checkbox, Code, Group, Loader, Select, Stack, Switch, Table, Text, TextInput } from '@mantine/core'
import { channelStatus, errorText, kindLabels, quietSummary, sameSettings, type Channel, type NotificationSettings, type QuietHours, type Route } from './notification-utils'
import { relativeTime } from './overview-utils'
import { StatusText } from './status'
import { Panel } from './ui'

type Draft = { quietHours: QuietHours; routes: Record<string, Route> }
const draftOf = (s: NotificationSettings): Draft => ({ quietHours: s.quietHours, routes: Object.fromEntries(s.channels.map((c) => [c.id, c.route])) })
const viewerZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const zones = (() => { try { return Intl.supportedValuesOf('timeZone') } catch { return [viewerZone, 'UTC'] } })()

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim() || `Request failed (${response.status}).`)
  return await response.json() as T
}

/** Where alerts reach you outside Vigara: channels, which alerts each gets, and quiet hours. */
export function NotificationSettingsPanel() {
  const [settings, setSettings] = useState<NotificationSettings | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [tests, setTests] = useState<Record<string, string>>({})
  const load = () => call<NotificationSettings>('GET', '/api/notifications').then((s) => { setSettings(s); setDraft(draftOf(s)); setError('') }).catch((e: unknown) => setError(e instanceof Error ? e.message : 'Notification settings could not be loaded.'))
  useEffect(() => { void load() }, [])
  const dirty = useMemo(() => !!settings && !!draft && !sameSettings(draftOf(settings), draft), [settings, draft])
  if (!settings || !draft) return error ? <Alert color="red">{error}</Alert> : <Group justify="center" p="xl"><Loader size="sm" /></Group>
  const manage = settings.canManage
  const setQuiet = (patch: Partial<QuietHours>) => { setSaved(false); setDraft((d) => d && { ...d, quietHours: { ...d.quietHours, ...patch } }) }
  const setRoute = (id: string, patch: Partial<Route>) => { setSaved(false); setDraft((d) => d && { ...d, routes: { ...d.routes, [id]: { ...d.routes[id], ...patch } } }) }
  const save = () => {
    setBusy(true)
    call<NotificationSettings>('PUT', '/api/notifications/settings', draft).then((s) => {
      setSettings(s); setDraft(draftOf(s)); setSaved(true); setError('')
      // Turning quiet hours off releases held alerts; show where they ended up.
      if (s.channels.some((c) => c.pending)) window.setTimeout(() => { void load() }, 5000)
    })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : 'Settings could not be saved.')).finally(() => setBusy(false))
  }
  const test = (id: string) => {
    setTests((t) => ({ ...t, [id]: 'Sending…' }))
    call<{ ok: boolean; error?: string }>('POST', `/api/notifications/channels/${id}/test`)
      .then((r) => setTests((t) => ({ ...t, [id]: r.ok ? 'Sent. Check your device.' : `Failed: ${errorText(r.error ?? '')}` })))
      .catch((e: unknown) => setTests((t) => ({ ...t, [id]: e instanceof Error ? e.message : 'Failed' })))
  }
  const channels = settings.channels
  const external = channels.filter((c) => c.kind !== 'android')
  const android = channels.find((c) => c.kind === 'android')
  const severities: { key: keyof Route; label: string; tone: 'crit' | 'warn' | 'ok' }[] = [{ key: 'critical', label: 'Critical', tone: 'crit' }, { key: 'warning', label: 'Warning', tone: 'warn' }, { key: 'resolved', label: 'Cleared', tone: 'ok' }]

  return <Stack gap="lg" maw={960}>
    <Text c="dimmed" size="sm">Alerts you mute from the inbox are never sent, whatever these settings say.</Text>
    {error && <Alert color="red" withCloseButton onClose={() => setError('')}>{error}</Alert>}
    {!settings.linksEnabled && channels.length > 0 && <Alert color="yellow" title="Notifications can’t link back to Vigara">Set <Code>VIGIRA_PUBLIC_URL</Code> on the server so tapping a notification opens Alerts, and ntfy notifications get Acknowledge and Mute buttons.</Alert>}
    <Panel title="Channels" count={channels.length} flush>
      {channels.length > 0 && <div className="table-scroll"><Table verticalSpacing="sm">
        <Table.Thead><Table.Tr><Table.Th>Channel</Table.Th><Table.Th className="vg-wide">Target</Table.Th><Table.Th>Status</Table.Th><Table.Th /></Table.Tr></Table.Thead>
        <Table.Tbody>{channels.map((c) => { const s = channelStatus(c, (at) => relativeTime(at)); return <Table.Tr key={c.id}>
          <Table.Td><Text size="sm" fw={600}>{kindLabels[c.kind] ?? c.kind}</Text><Text size="xs" c="dimmed" ff="monospace" className="vg-narrow vg-break">{c.target}</Text></Table.Td>
          <Table.Td className="vg-wide"><Text size="sm" c="dimmed" ff="monospace">{c.target}</Text></Table.Td>
          <Table.Td><StatusText tone={s.tone}>{s.text}</StatusText>{tests[c.id] && <Text size="xs" c="dimmed" mt={2}>{tests[c.id]}</Text>}</Table.Td>
          <Table.Td className="vg-num">{manage && (c.kind !== 'android' || !!c.phoneCount) && <Button size="xs" variant="default" onClick={() => test(c.id)} loading={tests[c.id] === 'Sending…'}>Send test</Button>}</Table.Td>
        </Table.Tr> })}</Table.Tbody>
      </Table></div>}
      {external.length === 0 && <Stack gap={6} p="md">
        <Text size="sm">Other channels are configured on the server; restart it after setting them.</Text>
        <Text size="sm" c="dimmed">ntfy: <Code>VIGIRA_NTFY_URL</Code>, <Code>VIGIRA_NTFY_TOPIC</Code> and optionally <Code>VIGIRA_NTFY_TOKEN</Code>. Gotify: <Code>VIGIRA_GOTIFY_URL</Code> and <Code>VIGIRA_GOTIFY_TOKEN</Code>. Discord: <Code>VIGIRA_DISCORD_WEBHOOK_URL</Code>.</Text>
      </Stack>}
    </Panel>
    {android && <PhonesPanel channel={android} admin={manage} onChanged={() => { void load() }} />}
    {channels.length > 0 && <div className="vg-notify-grid">
      <Panel title="Which alerts each channel gets" flush>
        <Table verticalSpacing="sm">
          <Table.Thead><Table.Tr><Table.Th>Severity</Table.Th>{channels.map((c) => <Table.Th key={c.id} ta="center">{kindLabels[c.kind] ?? c.kind}</Table.Th>)}</Table.Tr></Table.Thead>
          <Table.Tbody>{severities.map((sev) => <Table.Tr key={sev.key}>
            <Table.Td><StatusText tone={sev.tone}>{sev.label}</StatusText></Table.Td>
            {channels.map((c) => <Table.Td key={c.id}><Checkbox mx="auto" w="fit-content" disabled={!manage} aria-label={`${sev.label} alerts to ${kindLabels[c.kind] ?? c.kind}`} checked={draft.routes[c.id]?.[sev.key] ?? true} onChange={(e) => setRoute(c.id, { [sev.key]: e.currentTarget.checked })} /></Table.Td>)}
          </Table.Tr>)}</Table.Tbody>
        </Table>
      </Panel>
      <Panel title="Quiet hours" actions={<Switch aria-label="Quiet hours" disabled={!manage} checked={draft.quietHours.enabled} onChange={(e) => setQuiet({ enabled: e.currentTarget.checked, ...(e.currentTarget.checked && draft.quietHours.timezone === 'UTC' && zones.includes(viewerZone) ? { timezone: viewerZone } : {}) })} />}>
        <Stack gap="sm">
          {draft.quietHours.enabled ? <Text size="sm" fw={500}>{quietSummary(draft.quietHours, viewerZone)}</Text> : <Text size="sm" c="dimmed">Off. Everything is sent as it happens.</Text>}
          <Group grow align="flex-start">
            <TextInput label="From" type="time" disabled={!manage} value={draft.quietHours.start} onChange={(e) => setQuiet({ start: e.currentTarget.value })} />
            <TextInput label="Until" type="time" disabled={!manage} value={draft.quietHours.end} onChange={(e) => setQuiet({ end: e.currentTarget.value })} />
          </Group>
          <Select label="Time zone" searchable disabled={!manage} data={zones.includes(draft.quietHours.timezone) ? zones : [draft.quietHours.timezone, ...zones]} value={draft.quietHours.timezone} onChange={(v) => v && setQuiet({ timezone: v })} allowDeselect={false} />
          <Text size="xs" c="dimmed">During quiet hours, warnings and cleared alerts wait and are sent when quiet hours end. Critical alerts always notify.</Text>
        </Stack>
      </Panel>
    </div>}
    {manage && channels.length > 0 && <Group>
      <Button onClick={save} disabled={!dirty} loading={busy}>Save changes</Button>
      {dirty && <Button variant="subtle" onClick={() => setDraft(draftOf(settings))}>Discard</Button>}
      {saved && !dirty && <Text size="sm" c="dimmed">Saved.</Text>}
    </Group>}
    {!manage && <Text size="sm" c="dimmed">Only administrators can change notification settings.</Text>}
  </Stack>
}

/** Phones running the Vigara app. Each person manages their own; admins see everyone's. */
function PhonesPanel({ channel, admin, onChanged }: { channel: Channel; admin: boolean; onChanged: () => void }) {
  const phones = channel.phones ?? []
  const [status, setStatus] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState('')
  const test = (id: string) => {
    setStatus((s) => ({ ...s, [id]: 'Sending…' }))
    call<{ ok: boolean; error?: string }>('POST', `/api/notifications/phones/${id}/test`)
      .then((r) => { setStatus((s) => ({ ...s, [id]: r.ok ? 'Sent. Check the phone.' : `Failed: ${errorText(r.error ?? '')}` })); if (!r.ok) onChanged() })
      .catch((e: unknown) => setStatus((s) => ({ ...s, [id]: e instanceof Error ? e.message : 'Failed' })))
  }
  const remove = (id: string) => {
    setBusy(id)
    fetch(`/api/notifications/phones/${id}`, { method: 'DELETE', credentials: 'same-origin' })
      .then(async (r) => { if (!r.ok) throw new Error((await r.text()).trim() || 'The phone could not be removed.'); onChanged() })
      .catch((e: unknown) => setStatus((s) => ({ ...s, [id]: e instanceof Error ? e.message : 'Failed' }))).finally(() => setBusy(''))
  }
  return <Panel title="Phones" count={phones.length} flush>
    {phones.length === 0 ? <Stack gap={6} p="md">
      <Text size="sm">{admin && channel.phoneCount ? 'None of the registered phones are yours.' : 'No phones yet.'} Install the Vigara Android app, sign in, and turn on notifications under More › Notifications.</Text>
      <Text size="sm" c="dimmed">Alerts reach the phone through UnifiedPush, so it also needs a push app such as ntfy. They are encrypted on this server, so the push app only relays them.</Text>
    </Stack> : <div className="table-scroll"><Table verticalSpacing="sm">
      <Table.Thead><Table.Tr><Table.Th>Phone</Table.Th><Table.Th>Status</Table.Th><Table.Th /></Table.Tr></Table.Thead>
      <Table.Tbody>{phones.map((p) => <Table.Tr key={p.id}>
        <Table.Td><Text size="sm" fw={600}>{p.name}</Text><Text size="xs" c="dimmed">{p.mine ? 'You' : p.user} · added {relativeTime(p.createdAt)}</Text></Table.Td>
        <Table.Td>{p.lastError ? <StatusText tone="crit">{errorText(p.lastError)}</StatusText>
          : p.lastSuccessAt ? <StatusText tone="ok">Last delivered {relativeTime(p.lastSuccessAt)}</StatusText>
          : <StatusText tone="idle">Nothing sent yet</StatusText>}
          {status[p.id] && <Text size="xs" c="dimmed" mt={2}>{status[p.id]}</Text>}</Table.Td>
        <Table.Td className="vg-num"><Group gap="xs" justify="flex-end">
          <Button size="xs" variant="default" onClick={() => test(p.id)} loading={status[p.id] === 'Sending…'}>Send test</Button>
          <Button size="xs" variant="subtle" color="red" onClick={() => remove(p.id)} loading={busy === p.id}>Remove</Button>
        </Group></Table.Td>
      </Table.Tr>)}</Table.Tbody>
    </Table></div>}
  </Panel>
}
