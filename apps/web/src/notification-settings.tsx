import { useEffect, useMemo, useState } from 'react'
import { ActionIcon, Alert, Button, Checkbox, Code, Group, Loader, Menu, Modal, PasswordInput, SegmentedControl, Select, Stack, Switch, Table, Text, TextInput } from '@mantine/core'
import { IconDots, IconPlus } from '@tabler/icons-react'
import { blankForm, channelBody, channelStatus, errorText, formOf, formProblem, kindLabels, quietSummary, sameSettings, type Channel, type ChannelForm, type ChannelKind, type NotificationSettings, type QuietHours, type Route } from './notification-utils'
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
  // The channel being edited, 'new' while adding one, or null.
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
  const [removing, setRemoving] = useState<Channel | null>(null)
  const [removeBusy, setRemoveBusy] = useState(false)
  const [removeError, setRemoveError] = useState('')
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
  const remove = (c: Channel) => {
    setRemoveBusy(true); setRemoveError('')
    fetch(`/api/notifications/channels/${encodeURIComponent(c.id)}`, { method: 'DELETE', credentials: 'same-origin' })
      .then(async (r) => { if (!r.ok) throw new Error((await r.text()).trim() || 'The channel could not be removed.'); setRemoving(null); void load() })
      .catch((e: unknown) => setRemoveError(e instanceof Error ? e.message : 'The channel could not be removed.')).finally(() => setRemoveBusy(false))
  }
  const channels = settings.channels
  const external = channels.filter((c) => c.kind !== 'android')
  const fromServer = channels.filter((c) => c.source === 'server')
  const android = channels.find((c) => c.kind === 'android')
  const severities: { key: keyof Route; label: string; tone: 'crit' | 'warn' | 'ok' }[] = [{ key: 'critical', label: 'Critical', tone: 'crit' }, { key: 'warning', label: 'Warning', tone: 'warn' }, { key: 'resolved', label: 'Cleared', tone: 'ok' }]

  return <Stack gap="lg" maw={960}>
    <Text c="dimmed" size="sm">Alerts you mute from the inbox are never sent, whatever these settings say.</Text>
    {error && <Alert color="red" withCloseButton onClose={() => setError('')}>{error}</Alert>}
    {!settings.linksEnabled && channels.length > 0 && <Alert color="yellow" title="Notifications can’t link back to Vigara">Set <Code>VIGIRA_PUBLIC_URL</Code> on the server so tapping a notification opens Alerts, and ntfy notifications get Acknowledge and Mute buttons.</Alert>}
    {manage && !settings.canStore && <Alert color="yellow" title="Channels can’t be added here yet">Set <Code>VIGIRA_NOTIFICATION_MASTER_KEY</Code> on the server to a random 32-byte key in base64 (for example from <Code>openssl rand -base64 32</Code>) and restart it. Channel tokens are encrypted with it.{fromServer.length > 0 && ' Channels set by environment variables are then copied here once, and the variables are ignored from then on.'}</Alert>}
    <Panel title="Channels" count={channels.length} flush actions={manage && settings.canStore && <Button size="xs" variant="default" leftSection={<IconPlus size={14} />} onClick={() => setEditing('new')}>Add channel</Button>}>
      {channels.length > 0 && <div className="table-scroll"><Table verticalSpacing="sm">
        <Table.Thead><Table.Tr><Table.Th>Channel</Table.Th><Table.Th className="vg-wide">Target</Table.Th><Table.Th>Status</Table.Th><Table.Th /></Table.Tr></Table.Thead>
        <Table.Tbody>{channels.map((c) => { const s = channelStatus(c, (at) => relativeTime(at)); return <Table.Tr key={c.id}>
          <Table.Td><Text size="sm" fw={600}>{c.name}</Text>{channelSubtitle(c) && <Text size="xs" c="dimmed">{channelSubtitle(c)}</Text>}<Text size="xs" c="dimmed" ff="monospace" className="vg-narrow vg-break">{c.target}</Text></Table.Td>
          <Table.Td className="vg-wide"><Text size="sm" c="dimmed" ff="monospace">{c.target}</Text></Table.Td>
          <Table.Td><StatusText tone={s.tone}>{s.text}</StatusText>{tests[c.id] && <Text size="xs" c="dimmed" mt={2}>{tests[c.id]}</Text>}</Table.Td>
          <Table.Td className="vg-num"><Group gap="xs" justify="flex-end" wrap="nowrap">
            {manage && (c.kind !== 'android' || !!c.phoneCount) && !c.target.startsWith('Cannot send') && <Button size="xs" variant="default" onClick={() => test(c.id)} loading={tests[c.id] === 'Sending…'}>Send test</Button>}
            {manage && c.source === 'settings' && <Menu position="bottom-end" withinPortal>
              <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={`More for ${c.name}`}><IconDots size={16} /></ActionIcon></Menu.Target>
              <Menu.Dropdown>
                <Menu.Item disabled={!settings.canStore} onClick={() => setEditing(c)}>Edit</Menu.Item>
                <Menu.Item color="red" onClick={() => { setRemoveError(''); setRemoving(c) }}>Remove</Menu.Item>
              </Menu.Dropdown>
            </Menu>}
          </Group></Table.Td>
        </Table.Tr> })}</Table.Tbody>
      </Table></div>}
      {external.length === 0 && <Stack gap={6} p="md">
        <Text size="sm">{manage && settings.canStore ? 'Add ntfy, Gotify or Discord to get alerts there as well.' : 'No ntfy, Gotify or Discord channels yet.'}</Text>
      </Stack>}
    </Panel>
    {editing && <ChannelDialog channel={editing === 'new' ? null : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void load() }} />}
    <Modal opened={!!removing} onClose={() => !removeBusy && setRemoving(null)} title={`Remove ${removing?.name ?? 'channel'}?`} centered>
      <Stack>
        <Text size="sm">Alerts stop going to this channel, and its delivery history is deleted.{removing?.id && ['ntfy', 'gotify', 'discord'].includes(removing.id) && ' It was imported from environment variables, which stay ignored.'}</Text>
        {removeError && <Alert color="red">{removeError}</Alert>}
        <Group justify="flex-end"><Button variant="default" disabled={removeBusy} onClick={() => setRemoving(null)}>Cancel</Button><Button color="red" loading={removeBusy} onClick={() => removing && remove(removing)}>Remove</Button></Group>
      </Stack>
    </Modal>
    {android && <PhonesPanel channel={android} admin={manage} onChanged={() => { void load() }} />}
    {channels.length > 0 && <div className="vg-notify-grid">
      <Panel title="Which alerts each channel gets" flush>
        <Table verticalSpacing="sm">
          <Table.Thead><Table.Tr><Table.Th>Severity</Table.Th>{channels.map((c) => <Table.Th key={c.id} ta="center">{c.name}</Table.Th>)}</Table.Tr></Table.Thead>
          <Table.Tbody>{severities.map((sev) => <Table.Tr key={sev.key}>
            <Table.Td><StatusText tone={sev.tone}>{sev.label}</StatusText></Table.Td>
            {channels.map((c) => <Table.Td key={c.id}><Checkbox mx="auto" w="fit-content" disabled={!manage} aria-label={`${sev.label} alerts to ${c.name}`} checked={draft.routes[c.id]?.[sev.key] ?? true} onChange={(e) => setRoute(c.id, { [sev.key]: e.currentTarget.checked })} /></Table.Td>)}
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

/** The kind, when the name doesn't already say it, and where a channel is set. */
const channelSubtitle = (c: Channel) => [(kindLabels[c.kind] ?? c.kind) !== c.name && (kindLabels[c.kind] ?? c.kind), c.source === 'server' && 'set by server environment'].filter(Boolean).join(' · ')

const kindOptions: { value: ChannelKind; label: string }[] = [{ value: 'ntfy', label: 'ntfy' }, { value: 'gotify', label: 'Gotify' }, { value: 'discord', label: 'Discord' }]

/** Add or edit a channel. Saved secrets are never shown; leaving the field alone keeps them. */
function ChannelDialog({ channel, onClose, onSaved }: { channel: Channel | null; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState<ChannelForm>(() => channel ? formOf(channel) : blankForm())
  const [busy, setBusy] = useState<'' | 'save' | 'test'>('')
  const [error, setError] = useState('')
  const [tested, setTested] = useState('')
  const set = (patch: Partial<ChannelForm>) => { setForm((f) => ({ ...f, ...patch })); setTested('') }
  const changeKind = (kind: ChannelKind) => setForm((f) => ({ ...blankForm(kind), name: f.name && f.name !== kindLabels[f.kind] ? f.name : kindLabels[kind] }))
  const problem = formProblem(form, channel)
  const saved = !!channel?.config?.hasToken && !form.secretTouched
  const discord = form.kind === 'discord'
  const send = (kind: 'save' | 'test') => {
    setBusy(kind); setError('')
    const body = channelBody(form)
    const request = kind === 'test'
      ? call<{ ok: boolean; error?: string }>('POST', '/api/notifications/channels/check', channel ? { ...body, id: channel.id, kind: undefined } : body)
        .then((r) => setTested(r.ok ? 'Sent. Check that it arrived.' : `Failed: ${errorText(r.error ?? '')}`))
      : call<{ id: string }>(channel ? 'PUT' : 'POST', channel ? `/api/notifications/channels/${encodeURIComponent(channel.id)}` : '/api/notifications/channels', channel ? { ...body, kind: undefined } : body).then(onSaved)
    request.catch((e: unknown) => setError(e instanceof Error ? e.message : 'That did not work.')).finally(() => setBusy(''))
  }
  return <Modal opened onClose={busy ? () => undefined : onClose} title={channel ? `Edit ${channel.name}` : 'Add a channel'} centered>
    <Stack>
      {!channel && <SegmentedControl fullWidth data={kindOptions} value={form.kind} onChange={(v) => changeKind(v as ChannelKind)} />}
      <TextInput label="Name" description="Shown in these settings, for example “Phone” or “Ops room”." value={form.name} maxLength={80} onChange={(e) => set({ name: e.currentTarget.value })} data-autofocus />
      {!discord && <TextInput label="Server URL" placeholder={form.kind === 'ntfy' ? 'https://ntfy.sh' : 'https://gotify.example.com'} value={form.url} onChange={(e) => set({ url: e.currentTarget.value })} />}
      {form.kind === 'ntfy' && <TextInput label="Topic" description="Subscribe to it in the ntfy app. On ntfy.sh anyone who knows the name can read it, so pick something hard to guess." value={form.topic} onChange={(e) => set({ topic: e.currentTarget.value })} />}
      <Stack gap={4}>
        {discord
          ? <PasswordInput label="Webhook URL" description="Server settings › Integrations › Webhooks › Copy Webhook URL." placeholder={saved ? 'Saved. Paste a new URL to replace it.' : 'https://discord.com/api/webhooks/…'} value={form.secret} onChange={(e) => set({ secret: e.currentTarget.value, secretTouched: true })} />
          : <PasswordInput label={form.kind === 'gotify' ? 'Application token' : 'Access token (optional)'} description={form.kind === 'gotify' ? 'Create an application in Gotify and copy its token.' : 'Only needed when the topic is protected.'} placeholder={saved ? 'Saved. Type to replace it.' : ''} value={form.secret} onChange={(e) => set({ secret: e.currentTarget.value, secretTouched: true })} />}
        {saved && form.kind === 'ntfy' && <Button size="compact-xs" variant="subtle" color="red" w="fit-content" onClick={() => set({ secret: '', secretTouched: true })}>Remove saved token</Button>}
        {channel?.config?.hasToken && form.secretTouched && !form.secret && form.kind === 'ntfy' && <Text size="xs" c="dimmed">The saved token will be removed.</Text>}
      </Stack>
      {error && <Alert color="red">{error}</Alert>}
      {tested && <Text size="sm" c={tested.startsWith('Sent') ? undefined : 'red'}>{tested}</Text>}
      <Group justify="space-between">
        <Button variant="default" disabled={!!problem || !!busy} loading={busy === 'test'} onClick={() => send('test')}>Send test</Button>
        <Group gap="xs">
          <Button variant="subtle" disabled={!!busy} onClick={onClose}>Cancel</Button>
          <Button disabled={!!problem || !!busy} loading={busy === 'save'} onClick={() => send('save')}>{channel ? 'Save' : 'Add channel'}</Button>
        </Group>
      </Group>
      {problem && form.name && <Text size="xs" c="dimmed">{problem}</Text>}
    </Stack>
  </Modal>
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
