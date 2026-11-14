import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Card, Checkbox, Code, Group, Modal, Select, Stack, Text, TextInput, Title } from '@mantine/core'
import type { LogLine } from './container-utils'
import { resourceRequest } from './resource-access'

type LogValue = { id: string; label: string; value: string; state: string; capturedAt?: string; checkedAt?: string; match: string; prefix: string; suffix: string; pattern: string; shared: boolean; version: number }
type Rule = Pick<LogValue, 'label' | 'match' | 'prefix' | 'suffix' | 'pattern' | 'shared' | 'version'>
type Preview = { value: string; line: LogLine; truncated: boolean }
const initialRule: Rule = { label: 'Join code', match: 'JoinCode', prefix: 'value[', suffix: ']', pattern: '', shared: false, version: 0 }
const base = (host: string, id: string) => `/api/containers/${encodeURIComponent(host)}/${encodeURIComponent(id)}`
const states: Record<string, string> = { current: 'Current server run', waiting: 'Waiting for a value from this run', offline: 'Host connection lost', stale: 'Capture status is out of date', scanning: 'Searching retained history…', 'scan-limit': 'Search limit reached — narrow the source logs', ambiguous: 'Multiple service instances — select a single source', 'agent-update-required': 'Update the host agent to enable capture' }

export function LogValues({ hostId, reference, admin = false, suspended = false }: { hostId: string; reference: string; admin?: boolean; suspended?: boolean }) {
  const [values, setValues] = useState<LogValue[]>([])
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<LogValue | 'new' | null>(null)
  const [revision, setRevision] = useState(0)
  const [copied, setCopied] = useState('')
  const [copying, setCopying] = useState('')
  useEffect(() => {
    const controller = new AbortController(); let timer: number | undefined
    const load = async () => {
      try { const result = await resourceRequest<LogValue[]>(`${base(hostId, reference)}/values`, 'GET', undefined, controller.signal); if (!controller.signal.aborted) { setValues(result); setError('') } }
      catch (cause) { if (!controller.signal.aborted) setError(String(cause)) }
      finally { if (!controller.signal.aborted) timer = window.setTimeout(() => void load(), 10000) }
    }
    void load(); return () => { controller.abort(); window.clearTimeout(timer) }
  }, [hostId, reference, revision])
  async function remove(value: LogValue) {
    if (!window.confirm(`Delete the saved value “${value.label}”?`)) return
    try { await resourceRequest(`${base(hostId, reference)}/values/${value.id}`, 'DELETE'); setRevision((current) => current + 1) } catch (cause) { setError(String(cause)) }
  }
  async function copy(value: LogValue) {
    setCopying(value.id); setCopied('')
    try {
      // Revalidate the run at copy time, including after a sleeping browser tab.
      const fresh = await resourceRequest<LogValue[]>(`${base(hostId, reference)}/values`)
      setValues(fresh)
      const current = fresh.find((entry) => entry.id === value.id)
      if (current?.state !== 'current' || !current.value) return
      await navigator.clipboard.writeText(current.value); setCopied(value.id)
    } catch (cause) { setError(String(cause)) } finally { setCopying('') }
  }
  return <Stack gap="sm">
    {admin && <Group justify="space-between"><div><Title order={4}>Saved log values</Title><Text size="sm" c="dimmed">Capture a value in the background and keep it after surrounding logs rotate.</Text></div><Button size="xs" variant="light" onClick={() => setEditing('new')}>Add log value</Button></Group>}
    {error && <Alert color="orange">Saved values are unavailable. Copying is disabled until the host and capture status are verified.{admin && <Text size="xs">{error}</Text>}</Alert>}
    {values.map((value) => <Card withBorder key={value.id} radius="md"><Stack gap="xs">
      <Group justify="space-between"><Text fw={600}>{value.label}</Text><Badge color={!error && value.state === 'current' ? 'green' : 'gray'}>{error ? 'Unavailable' : states[value.state] || value.state}</Badge></Group>
      <Group justify="space-between"><Text size="xl" fw={700} style={{ overflowWrap: 'anywhere' }}>{!suspended && !error && value.state === 'current' && value.value ? value.value : 'Waiting for a verified value'}</Text>
        <Button size="xs" loading={copying === value.id} disabled={suspended || !!error || value.state !== 'current' || !value.value} onClick={() => void copy(value)}>{copied === value.id ? 'Copied' : 'Copy'}</Button></Group>
      {value.capturedAt && <Text size="xs" c="dimmed">Captured {new Date(value.capturedAt).toLocaleString()}</Text>}
      {admin && <Group><Badge variant="outline" color={value.shared ? 'blue' : 'gray'}>{value.shared ? 'Shared with viewers' : 'Admin only'}</Badge><Button size="xs" variant="subtle" onClick={() => setEditing(value)}>Edit</Button><Button size="xs" variant="subtle" color="red" onClick={() => void remove(value)}>Delete</Button></Group>}
    </Stack></Card>)}
    {admin && !values.length && !error && <Text size="sm" c="dimmed">No saved values yet. Add a filter and extraction rule to show a join code here.</Text>}
    {editing && <ValueEditor key={editing === 'new' ? 'new' : editing.id} hostId={hostId} reference={reference} value={editing === 'new' ? null : editing} close={() => setEditing(null)} saved={() => { setEditing(null); setRevision((current) => current + 1) }} />}
  </Stack>
}

function ValueEditor({ hostId, reference, value, close, saved }: { hostId: string; reference: string; value: LogValue | null; close: () => void; saved: () => void }) {
  const [rule, setRule] = useState<Rule>(value ? { label: value.label, match: value.match, prefix: value.prefix, suffix: value.suffix, pattern: value.pattern, shared: value.shared, version: value.version } : initialRule)
  const [mode, setMode] = useState<string | null>(value?.pattern ? 'regex' : 'delimiters')
  const [preview, setPreview] = useState<Preview | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const update = (patch: Partial<Rule>) => { setRule((current) => ({ ...current, ...patch })); setPreview(null) }
  async function test() {
    setBusy(true); setError(''); setPreview(null)
    try { setPreview(await resourceRequest<Preview>(`${base(hostId, reference)}/values/preview`, 'POST', rule)) } catch (cause) { setError(String(cause)) } finally { setBusy(false) }
  }
  async function save() {
    setBusy(true); setError('')
    try { await resourceRequest(`${base(hostId, reference)}/values${value ? `/${value.id}` : ''}`, value ? 'PUT' : 'POST', rule); saved() } catch (cause) { setError(String(cause)) } finally { setBusy(false) }
  }
  return <Modal title={value ? 'Edit saved log value' : 'Add saved log value'} opened onClose={() => { if (!busy) close() }} size="lg"><Stack>
    <Text size="sm">Compose sources follow the service across replacements. Capture runs while the page is closed. A restart invalidates the previous run’s value.</Text>
    <TextInput label="Label" value={rule.label} maxLength={80} onChange={(event) => update({ label: event.currentTarget.value })} disabled={busy} />
    <TextInput label="Log line contains" value={rule.match} maxLength={256} onChange={(event) => update({ match: event.currentTarget.value })} disabled={busy} />
    <Select label="Extract value using" data={[{ value: 'delimiters', label: 'Text between delimiters' }, { value: 'regex', label: 'Regular expression (advanced)' }]} value={mode} allowDeselect={false} disabled={busy} onChange={(next) => { setMode(next); update(next === 'regex' ? { prefix: '', suffix: '', pattern: 'value\\[([^\\]]+)\\]' } : { prefix: 'value[', suffix: ']', pattern: '' }) }} />
    {mode === 'regex' ? <TextInput label="Regular expression" description="Use exactly one capture group. Extraction runs on matching lines only." value={rule.pattern} maxLength={512} disabled={busy} onChange={(event) => update({ pattern: event.currentTarget.value })} /> : <Group grow><TextInput label="After this text" value={rule.prefix} maxLength={256} disabled={busy} onChange={(event) => update({ prefix: event.currentTarget.value })} /><TextInput label="Before this text" value={rule.suffix} maxLength={256} disabled={busy} onChange={(event) => update({ suffix: event.currentTarget.value })} /></Group>}
    <Checkbox label="Share extracted value with users who can view this resource" description="Raw logs and the extraction rule stay private unless separately permitted." checked={rule.shared} disabled={busy} onChange={(event) => update({ shared: event.currentTarget.checked })} />
    {error && <Alert color="red" role="alert">{error}</Alert>}
    <Button variant="light" loading={busy} onClick={() => void test()}>Preview from current run</Button>
    {preview && <Alert color={preview.value ? 'green' : 'gray'} title={preview.value ? `Extracted: ${preview.value}` : 'No complete value found yet'}>{preview.line.m && <Code block style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{preview.line.m}</Code>}{preview.truncated && <Text size="sm">This preview reached its scan limit. Background capture will continue through retained history.</Text>}<Text size="sm">You can save now to capture future matches.</Text></Alert>}
    <Group justify="flex-end"><Button variant="default" disabled={busy} onClick={close}>Cancel</Button><Button loading={busy} disabled={!preview} onClick={() => void save()}>Save log value</Button></Group>
  </Stack></Modal>
}

type SearchResult = { lines: LogLine[]; truncated: boolean; moreMatches: boolean; nextSince?: string; scannedBytes: number }
export function HistoricalLogSearch({ hostId, reference }: { hostId: string; reference: string }) {
  const [match, setMatch] = useState('JoinCode')
  const [since, setSince] = useState('')
  const [until, setUntil] = useState('')
  const [result, setResult] = useState<SearchResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function search(cursor?: string) {
    setBusy(true); setError(''); setResult(null)
    try {
      const query = new URLSearchParams({ match })
      if (cursor || since) query.set('since', cursor || new Date(since).toISOString())
      if (until) query.set('until', new Date(until).toISOString())
      setResult(await resourceRequest<SearchResult>(`${base(hostId, reference)}/logs/search?${query}`))
    } catch (cause) { setError(String(cause)) } finally { setBusy(false) }
  }
  return <Card withBorder><Stack gap="sm"><Title order={4}>Search retained logs</Title><Text size="sm" c="dimmed">Search runs on the host before limiting results. Rotated or deleted logs cannot be recovered. Times below use your local timezone.</Text>
    <TextInput label="Log line contains" value={match} maxLength={256} disabled={busy} onChange={(event) => { setMatch(event.currentTarget.value); setResult(null) }} />
    <Group grow><TextInput type="datetime-local" label="From (optional)" value={since} disabled={busy} onChange={(event) => { setSince(event.currentTarget.value); setResult(null) }} /><TextInput type="datetime-local" label="Until (optional)" value={until} disabled={busy} onChange={(event) => { setUntil(event.currentTarget.value); setResult(null) }} /></Group>
    <Button loading={busy} disabled={!match.trim()} onClick={() => void search()}>Search retained logs</Button>
    {error && <Alert color="red" role="alert">{error}</Alert>}
    {result && <><Text size="sm">{result.lines.length} matching lines · {(result.scannedBytes / 1048576).toFixed(1)} MiB scanned</Text>
      {(result.truncated || result.moreMatches) && <Alert color="orange">{result.truncated ? 'Scan limit reached. Narrow the time range or continue from the last scanned timestamp.' : 'Showing the last 200 matches. Narrow the time range for earlier matches.'}</Alert>}
      {result.truncated && result.nextSince && <Button variant="light" onClick={() => void search(result.nextSince)}>Continue search</Button>}
      {result.lines.length ? <Code block style={{ maxHeight: 420, overflow: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{result.lines.map((line) => `${line.t || ''} ${line.m}${line.x ? ' [line shortened]' : ''}`).join('\n')}</Code> : <Text size="sm" c="dimmed">No matching lines in the scanned retained logs.</Text>}
    </>}
  </Stack></Card>
}
