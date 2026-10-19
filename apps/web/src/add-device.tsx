import { useEffect, useState } from 'react'
import { Alert, Button, Checkbox, Group, Modal, Select, Stack, Text, TextInput } from '@mantine/core'

type ServerOption = { url: string; secure: boolean }
type Availability = { serverUrl: string; serverUrls: ServerOption[]; allowInsecureHttp: boolean; targets: string[] }
type Props = { opened: boolean; onClose: () => void }

export function AddDevice({ opened, onClose }: Props) {
  const [availability, setAvailability] = useState<Availability>({ serverUrl: '', serverUrls: [], allowInsecureHttp: false, targets: [] })
  const [target, setTarget] = useState<string | null>(null)
  const [serverUrl, setServerUrl] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [session, setSession] = useState('')
  const [status, setStatus] = useState('')
  const [backupFeature, setBackupFeature] = useState(false)

  const selectedOption = availability.serverUrls.find((option) => option.url === serverUrl)

  useEffect(() => {
    if (!opened) return
    let active = true
    void fetch('/api/agent-downloads/availability', { credentials: 'same-origin' }).then(async (response) => {
      if (!response.ok) throw new Error((await response.text()).slice(0, 200) || 'Could not load available agent bundles.')
      return response.json() as Promise<Availability>
    }).then((value) => { if (active) { setAvailability(value); setServerUrl((current) => current && value.serverUrls.some((option) => option.url === current) ? current : value.serverUrl || null); setTarget((current) => current && value.targets.includes(current) ? current : value.targets[0] ?? null) } }).catch((cause: unknown) => { if (active) setError(cause instanceof Error ? cause.message : 'Could not load available agent bundles.') })
    return () => { active = false }
  }, [opened])

  useEffect(() => {
    if (!opened || !session || status === 'enrolled' || status === 'canceled' || status === 'expired') return
    let active = true
    const poll = async () => {
      try {
        const response = await fetch(`/api/agent-downloads/${encodeURIComponent(session)}`, { credentials: 'same-origin' })
        if (response.ok && active) setStatus((await response.json() as { status: string }).status)
      } catch { /* The next poll retries. */ }
    }
    void poll()
    const timer = window.setInterval(() => { void poll() }, 3000)
    return () => { active = false; window.clearInterval(timer) }
  }, [opened, session, status])

  async function createBundle() {
    setBusy(true); setError('')
    try {
      const response = await fetch('/api/agent-downloads', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ target, serverUrl, name, features: backupFeature ? ['restic.backup'] : [] }) })
      if (!response.ok) throw new Error((await response.text()).slice(0, 300) || 'Could not prepare the bundle.')
      const sessionId = response.headers.get('X-Vigira-Session-ID') ?? ''
      const blob = await response.blob()
      const href = URL.createObjectURL(blob)
      const link = document.createElement('a'); link.href = href; link.download = `vigira-agent-${target}.zip`; link.click()
      window.setTimeout(() => URL.revokeObjectURL(href), 60_000)
      setSession(sessionId); setStatus('waiting')
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not prepare the bundle.') }
    finally { setBusy(false) }
  }

  async function cancel() {
    if (!session) { onClose(); return }
    const response = await fetch(`/api/agent-downloads/${encodeURIComponent(session)}/cancel`, { method: 'POST', credentials: 'same-origin' })
    if (response.ok) setStatus('canceled')
    else setError('This enrollment has already finished or expired.')
  }

  function close() {
    if (['enrolled', 'canceled', 'expired'].includes(status)) { setSession(''); setStatus(''); setName(''); setBackupFeature(false) }
    onClose()
  }

  return <Modal opened={opened} onClose={close} title="Add device" size="md">
    <Stack>
      {error && <Alert color="red" title="Could not prepare device">{error}</Alert>}
      {availability.serverUrls.length === 0 && <Alert color="yellow" title="No server URL configured">Set <code>VIGIRA_PUBLIC_URL</code> to the HTTPS address agents can reach. On a trusted private network or Tailscale, you can instead set <code>VIGIRA_ALLOW_INSECURE_AGENT_HTTP=true</code> and use an <code>http://</code> address on a private (LAN or Tailscale) IP or name. Add more addresses with <code>VIGIRA_PUBLIC_URLS</code>.</Alert>}
      {availability.serverUrls.length === 1 && <Text size="sm">Agents will connect to <b>{availability.serverUrls[0].url}</b>.</Text>}
      {availability.serverUrls.length > 1 && <Select label="Server address the agent will connect to" description="Pick an address the new device can reach, such as the LAN or Tailscale address." value={serverUrl} onChange={setServerUrl} allowDeselect={false} data={availability.serverUrls.map((option) => ({ value: option.url, label: option.secure ? option.url : `${option.url} (unencrypted HTTP)` }))} />}
      {selectedOption && !selectedOption.secure && <Alert color="yellow" title="This connection is not encrypted">The agent will use plain HTTP, so its credential, telemetry, and inventory can be read or altered by anyone who can see traffic on the network between the device and this server. Use this only on a trusted private network or over a VPN such as Tailscale, never across the internet. Prefer an HTTPS address when you have one.</Alert>}
      <Select label="Operating system and architecture" value={target} onChange={setTarget} data={[
        { value: 'windows-amd64', label: 'Windows · x64' },
        { value: 'linux-amd64', label: 'Linux · x64' },
        { value: 'linux-arm64', label: 'Linux · ARM64' },
      ].map((item) => ({ ...item, disabled: !availability.targets.includes(item.value) }))} placeholder="Choose a supported target" />
      {availability.targets.length === 0 && <Alert color="yellow">Agent bundles are unavailable. The server operator must provision verified release artifacts.</Alert>}
      <TextInput label="Device name" value={name} onChange={(event) => setName(event.currentTarget.value)} maxLength={120} required />
      <Checkbox checked={backupFeature} onChange={(event) => setBackupFeature(event.currentTarget.checked)} label="Allow Restic backup jobs" description="This sets the server-side capability limit only. The Restic module and repository credentials must be configured locally on the device." />
      <Text size="sm" c="dimmed">Telemetry and inventory are included by the standard agent. Bundle selection never installs or enables privileged local modules.</Text>
      {status && <Alert color={status === 'enrolled' ? 'green' : status === 'waiting' ? 'blue' : 'gray'} title={`Enrollment ${status}`}>{status === 'waiting' ? <>Extract the bundle on the device and run <code>{target?.startsWith('linux-') ? 'sh install.sh' : 'install.ps1'}</code> as an administrator. This one-use enrollment expires after 30 minutes.</> : status === 'enrolled' ? 'The device has enrolled and will appear in the inventory.' : status === 'canceled' ? 'The enrollment was canceled.' : 'The enrollment expired.'}</Alert>}
      <Group justify="space-between">
        {session && status === 'waiting' ? <Button variant="default" onClick={() => void cancel()}>Cancel enrollment</Button> : <Button variant="default" onClick={close}>Close</Button>}
        {!session && <Button loading={busy} disabled={!selectedOption || !target || !name.trim() || !availability.targets.includes(target)} onClick={() => void createBundle()}>Download bundle</Button>}
      </Group>
    </Stack>
  </Modal>
}
