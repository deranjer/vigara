import { useEffect, useState } from 'react'
import { Alert, Button, Checkbox, Group, Modal, Select, Stack, Text, TextInput } from '@mantine/core'

import { BackupFields, newBackupConfig } from './backup-config'
import { isAbsoluteBackupPath } from './backup-path-utils'
import { dockerEnrollment, supportsMountFiles } from './docker-enrollment'

type ServerOption = { url: string; secure: boolean }
type Availability = { serverUrl: string; serverUrls: ServerOption[]; allowInsecureHttp: boolean; targets: string[]; managedBackupsAvailable?: boolean; backupTargets?: string[]; releaseSync?: { enabled: boolean; state: string; tag?: string; error?: string } }
type ReinstallDevice = { id: string; name: string; platform: string; architecture: string }
type Props = { opened: boolean; onClose: () => void; device?: ReinstallDevice }

export function AddDevice({ opened, onClose, device }: Props) {
  const [availability, setAvailability] = useState<Availability>({ serverUrl: '', serverUrls: [], allowInsecureHttp: false, targets: [] })
  const [target, setTarget] = useState<string | null>(device ? `${device.platform}-${device.architecture}` : null)
  const [serverUrl, setServerUrl] = useState<string | null>(null)
  const [name, setName] = useState(device?.name ?? '')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [session, setSession] = useState('')
  const [status, setStatus] = useState('')
  const [backupFeature, setBackupFeature] = useState(false)
  const [backup, setBackup] = useState(newBackupConfig)
  const [pathsPending, setPathsPending] = useState(false)
  const [dockerCeiling, setDockerCeiling] = useState<string | null>('read')
  const [shellAccess, setShellAccess] = useState<string | null>(device ? 'preserve' : 'disabled')
  const [dockerEnabled, setDockerEnabled] = useState(false)
  const [dockerHost, setDockerHost] = useState('')
  const [composeRoot, setComposeRoot] = useState('')
  const [stackOwner, setStackOwner] = useState('')
  const [dockerFiles, setDockerFiles] = useState<string | null>(device ? 'preserve' : 'disabled')
  const mountFilesAvailable = supportsMountFiles(dockerCeiling, target, dockerHost)
  const effectiveDockerHost = dockerHost || (target?.startsWith('linux-') ? 'unix:///var/run/docker.sock' : 'http://127.0.0.1:2375')

  const selectedOption = availability.serverUrls.find((option) => option.url === serverUrl)

  useEffect(() => {
    if (!opened) return
    let active = true
    let timer: number | undefined
    const load = async () => {
      try {
        const response = await fetch('/api/agent-downloads/availability', { credentials: 'same-origin' })
        if (!response.ok) throw new Error((await response.text()).slice(0, 200) || 'Could not load available agent bundles.')
        const value = await response.json() as Availability
        if (!active) return
        setError(''); setAvailability(value)
        setServerUrl((current) => current && value.serverUrls.some((option) => option.url === current) ? current : value.serverUrl || null)
        setTarget((current) => current && value.targets.includes(current) ? current : value.targets[0] ?? null)
        if (value.releaseSync?.enabled && value.releaseSync.state !== 'ready') timer = window.setTimeout(() => void load(), 5000)
      } catch (cause) {
        if (active) { setError(cause instanceof Error ? cause.message : 'Could not load available agent bundles.'); timer = window.setTimeout(() => void load(), 10000) }
      }
    }
    void load()
    return () => { active = false; window.clearTimeout(timer) }
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
      const dockerChoice = dockerEnrollment(dockerEnabled, dockerCeiling ?? 'read', target, dockerHost, composeRoot, stackOwner, mountFilesAvailable ? dockerFiles ?? 'disabled' : 'disabled')
      const response = await fetch('/api/agent-downloads', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ shell: shellAccess === 'preserve' ? undefined : shellAccess === 'enabled', deviceId: device?.id, target, serverUrl, name, backup: backupFeature ? { ...backup, sourcePaths: backup.sourcePaths.map((p) => p.trim()).filter(Boolean) } : undefined, docker: dockerChoice.docker, features: [...(backupFeature ? ['restic.backup'] : []), ...dockerChoice.features] }) })
      if (!response.ok) throw new Error((await response.text()).slice(0, 300) || 'Could not prepare the bundle.')
      const sessionId = response.headers.get('X-Vigara-Session-ID') ?? response.headers.get('X-Vigira-Session-ID') ?? ''
      const blob = await response.blob()
      const href = URL.createObjectURL(blob)
      const link = document.createElement('a'); link.href = href; link.download = `vigara-agent-${target}.zip`; link.click()
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
    if (['enrolled', 'canceled', 'expired'].includes(status)) { setSession(''); setStatus(''); setName(device?.name ?? ''); setBackupFeature(false); setShellAccess(device ? 'preserve' : 'disabled'); setDockerCeiling('read'); setDockerEnabled(false); setDockerHost(''); setComposeRoot(''); setDockerFiles(device ? 'preserve' : 'disabled'); setStackOwner('') }
    onClose()
  }

  return <Modal opened={opened} onClose={close} title={device ? `Reinstall agent · ${device.name}` : 'Add device'} size="md">
    <Stack>
      {device && <Alert color="blue" title="Reinstall on this device">This creates a fresh one-use installer for {device.name}, keeping its device record and history. Existing permissions are preserved unless you explicitly select Docker settings below. Running it replaces the previous agent credential. Use it only on this device.</Alert>}
      {error && <Alert color="red" title="Could not prepare device">{error}</Alert>}
      {availability.serverUrls.length === 0 && <Alert color="yellow" title="No server URL configured">Set <code>VIGIRA_PUBLIC_URL</code> to the HTTPS address agents can reach. On a trusted private network or Tailscale, you can instead set <code>VIGIRA_ALLOW_INSECURE_AGENT_HTTP=true</code> and use an <code>http://</code> address on a private (LAN or Tailscale) IP or name. Add more addresses with <code>VIGIRA_PUBLIC_URLS</code>.</Alert>}
      {availability.serverUrls.length === 1 && <Text size="sm">Agents will connect to <b>{availability.serverUrls[0].url}</b>.</Text>}
      {availability.serverUrls.length > 1 && <Select label="Server address the agent will connect to" description="Pick an address the new device can reach, such as the LAN or Tailscale address." value={serverUrl} onChange={setServerUrl} allowDeselect={false} data={availability.serverUrls.map((option) => ({ value: option.url, label: option.secure ? option.url : `${option.url} (unencrypted HTTP)` }))} />}
      {selectedOption && !selectedOption.secure && <Alert color="yellow" title="This connection is not encrypted">The agent will use plain HTTP, so its credential, telemetry, and inventory can be read or altered by anyone who can see traffic on the network between the device and this server. Use this only on a trusted private network or over a VPN such as Tailscale, never across the internet. Prefer an HTTPS address when you have one.</Alert>}
      <Select disabled={!!device || !!session} label="Operating system and architecture" value={target} onChange={setTarget} data={[
        { value: 'windows-amd64', label: 'Windows · x64' },
        { value: 'linux-amd64', label: 'Linux · x64' },
        { value: 'linux-arm64', label: 'Linux · ARM64' },
      ].map((item) => ({ ...item, disabled: !availability.targets.includes(item.value) }))} placeholder="Choose a supported target" />
      {availability.targets.length === 0 && <Alert color={availability.releaseSync?.state === 'error' ? 'yellow' : 'blue'} title="Agent bundles">
        {availability.releaseSync?.enabled ? availability.releaseSync.state === 'error'
          ? `${availability.releaseSync.error} Check Admin → Scheduled tasks for the next retry or run the scan manually.`
          : availability.releaseSync.state === 'pending'
            ? 'The bundle cache is empty. Check the Scan agent bundles task in Admin → Scheduled tasks, or run it now.'
            : 'The server is downloading and verifying compatible agent releases from GitHub. This list will refresh when they are ready.'
          : 'Agent bundles are unavailable. Enable automatic release downloads or provision verified release artifacts on the server.'}
      </Alert>}
      <TextInput readOnly={!!device} disabled={!!session} description={device ? 'Existing device name' : 'Use a unique name. To reinstall an existing device, open its Device info tab.'} label="Device name" value={name} onChange={(event) => setName(event.currentTarget.value)} maxLength={120} required />
      {!device && <><Checkbox disabled={!!session || !availability.managedBackupsAvailable || !availability.backupTargets?.includes(target ?? '')} checked={backupFeature} onChange={(event) => setBackupFeature(event.currentTarget.checked)} label="Enable managed backups" description="Bundles restic and lets administrators manage backup paths and schedules. On Linux, installs the backup-enabled service with root access." />
      {availability.managedBackupsAvailable && !availability.backupTargets?.includes(target ?? '') && <Text size="xs" c="dimmed">A backup-enabled bundle is unavailable for this platform. Rebuild server release artifacts to include restic.</Text>}
      {backupFeature && <BackupFields value={backup} onChange={setBackup} disabled={!!session} onPathsPendingChange={setPathsPending} />}
      {!availability.managedBackupsAvailable && <Text size="xs" c="dimmed">Managed backups require a backup master key on the server.</Text>}
      </>}
      <Select disabled={!!session} label="Device shell access" value={shellAccess} onChange={setShellAccess} allowDeselect={false} description="Linux shell or Windows PowerShell, running as the agent service account. Independent of Docker access." data={[...(device ? [{ value: 'preserve', label: 'Keep existing shell settings' }] : []), { value: 'disabled', label: 'Disabled' }, { value: 'enabled', label: 'Enabled' }]} />
      <Checkbox disabled={!!session} label={device ? 'Configure Docker access during reinstall' : 'Enable Docker access'} checked={dockerEnabled} onChange={(event) => setDockerEnabled(event.currentTarget.checked)} description={device ? 'Unchecked preserves existing settings. Selecting Docker updates its enrollment permissions when this bundle is installed.' : 'The installer configures the agent and checks its access to Docker on this device.'} />
      {dockerEnabled && <><Select disabled={!!session} label="Docker access level" value={dockerCeiling} onChange={setDockerCeiling} allowDeselect={false}
        description="Sets the local agent mode and, for new devices, matching enrollment permissions."
        data={[
          { value: 'read', label: 'View only' },
          { value: 'manage', label: 'Manage containers, images, networks and volumes' },
          { value: 'exec', label: 'Manage and open terminals in containers' },
          { value: 'deploy', label: 'Manage, terminals and deploy stacks', disabled: !target?.startsWith('linux-') },
        ]} /></>}
      {dockerEnabled && <><TextInput disabled={!!session} label="Docker endpoint on this device" value={dockerHost} placeholder={effectiveDockerHost} onChange={(event) => setDockerHost(event.currentTarget.value)} description={target?.startsWith('linux-') ? 'Default: local Docker socket. A loopback HTTP IP and port is also supported.' : 'Requires an existing loopback HTTP Docker endpoint. Windows named pipes are not supported; the installer does not expose the Docker daemon.'} />
        {dockerCeiling === 'deploy' && <TextInput disabled={!!session} label="Managed Compose directory on this device" value={composeRoot} placeholder={device ? 'Keep the host’s configured directory' : '/var/lib/vigara-agent/compose-stacks'} onChange={(event) => setComposeRoot(event.currentTarget.value)} description={device ? 'Leave blank to preserve the directory configured on this host. Enter a path only to change it.' : 'Choose the directory containing your Compose stacks. Blank uses the default shown above.'} />}
        {dockerCeiling === 'deploy' && <TextInput disabled={!!session} label="Stack folder and configuration owner" value={stackOwner} placeholder="e.g. deranjer or 1000:1000" onChange={(event) => setStackOwner(event.currentTarget.value)} description="An existing Linux user, optionally followed by :group. Blank keeps the host setting; fresh installs default to root. Applies to new stack folders and configuration on the next deployment. Application data ownership is preserved." />}
        <Select disabled={!!session || !mountFilesAvailable} label="Mounted file access" value={mountFilesAvailable ? dockerFiles : 'disabled'} onChange={setDockerFiles} allowDeselect={false}
          description={mountFilesAvailable ? 'Administrators can browse mounted files and Docker volumes. Requires an agent installed directly on the native Linux Docker host.' : 'Requires Linux, a local Docker socket, and Docker access with container terminals or stack deployment.'}
          data={[...(device ? [{ value: 'preserve', label: 'Keep existing mounted file access' }] : []), { value: 'disabled', label: 'Disabled' }, { value: 'read', label: 'Browse only' }, { value: 'write', label: 'Browse and edit' }]} />
        {mountFilesAvailable && (dockerFiles === 'read' || dockerFiles === 'write') && <Alert color="yellow" title="Mounted file permissions">The agent service runs as root to access mounted files, including files in home and temporary directories. {dockerFiles === 'write' ? 'Editing enables host filesystem writes, with a backup before each save. It can also change mounts marked read-only inside a container.' : 'Browsing keeps file editing disabled.'}</Alert>}
        <Alert color="yellow" title="Docker host access">Docker socket or unrestricted API access can control the host, including when Vigara is set to view-only. Only install this bundle on a host where you approve that access. Linux stack deployment runs the agent as root with write access to the selected Compose directory.</Alert></>}
      <Text size="sm" c="dimmed">Telemetry and inventory are included. Selected Docker and backup settings are applied when you run the installer on the device.</Text>
      {status && <Alert color={status === 'enrolled' ? 'green' : status === 'waiting' ? 'blue' : 'gray'} title={`Enrollment ${status}`}>{status === 'waiting' ? <>Extract the bundle on the device and run <code>{target?.startsWith('linux-') ? 'sh install.sh' : 'install.cmd'}</code> as an administrator. This one-use enrollment expires after 30 minutes.</> : status === 'enrolled' ? 'Enrollment was accepted. Installation is complete only when the installer confirms the service is running and the device reports a successful check-in.' : status === 'canceled' ? 'The enrollment was canceled.' : 'The enrollment expired.'}</Alert>}
      <Group justify="space-between">
        {session && status === 'waiting' ? <Button variant="default" onClick={() => void cancel()}>Cancel enrollment</Button> : <Button variant="default" onClick={close}>Close</Button>}
        {!session && <Button loading={busy} disabled={!selectedOption || !target || !name.trim() || !availability.targets.includes(target) || (backupFeature && (!availability.backupTargets?.includes(target) || pathsPending || !backup.sourcePaths.length || !isAbsoluteBackupPath(backup.repository)))} onClick={() => void createBundle()}>{device ? 'Download reinstall bundle' : 'Download bundle'}</Button>}
      </Group>
    </Stack>
  </Modal>
}
