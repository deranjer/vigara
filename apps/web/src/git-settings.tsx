import { useEffect, useState } from 'react'
import { Alert, Anchor, Button, Card, Group, Loader, Select, Stack, Text, TextInput } from '@mantine/core'

type GitHubStatus = { available: boolean; connected: boolean; pending: boolean; canManage: boolean; setupHelp?: string; slug?: string; remote?: string; branch?: string }
type Settings = { remote: string; branch: string; version: number; authMode: string; githubApp: GitHubStatus }

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(path, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'Could not update Git settings.')
  return response.json() as Promise<T>
}

export function GitSettings({ onSaved }: { onSaved: () => void }) {
  const [value, setValue] = useState<Settings | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [organization, setOrganization] = useState('')
  const [disconnecting, setDisconnecting] = useState(false)
  useEffect(() => {
    let active = true
    void request<Settings>('/api/stacks/settings').then((settings) => {
      if (!active) return
      const params = new URLSearchParams(window.location.search)
      if (params.get('github_app') === 'connected' && settings.githubApp.connected) {
        settings = { ...settings, authMode: 'github-app', remote: settings.githubApp.remote || settings.remote, branch: settings.githubApp.branch || settings.branch }
        setNotice('GitHub App connected. Test the branch and save these Git settings to start using it.')
        params.delete('github_app')
        window.history.replaceState(null, '', window.location.pathname + (params.size ? '?' + params.toString() : '') + window.location.hash)
      }
      setValue(settings)
    }).catch((cause: Error) => { if (active) setError(cause.message) })
    return () => { active = false }
  }, [])

  async function submit(save: boolean) {
    if (!value) return
    setBusy(true); setError(''); setNotice('')
    try {
      const result = await request<{ message: string }>(save ? '/api/stacks/settings' : '/api/stacks/settings/test', save ? 'PUT' : 'POST', { remote: value.remote.trim(), branch: value.branch.trim(), version: value.version, authMode: value.authMode })
      if (save) onSaved(); else setNotice(result.message)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not update Git settings.') } finally { setBusy(false) }
  }

  async function connect() {
    if (!value) return
    setBusy(true); setError('')
    try {
      const result = await request<{ action: string; manifest: unknown }>('/api/stacks/settings/github/start', 'POST', { remote: value.remote.trim(), branch: value.branch.trim(), organization: organization.trim() })
      const form = document.createElement('form')
      form.method = 'POST'; form.action = result.action
      const input = document.createElement('input')
      input.type = 'hidden'; input.name = 'manifest'; input.value = JSON.stringify(result.manifest)
      form.appendChild(input); document.body.appendChild(form); form.submit(); form.remove()
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not start GitHub setup.') } finally { setBusy(false) }
  }

  async function resume() {
    setBusy(true); setError('')
    try { const result = await request<{ url: string }>('/api/stacks/settings/github/install', 'POST'); window.location.assign(result.url) } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not resume installation.'); setBusy(false) }
  }

  async function disconnect() {
    setBusy(true); setError('')
    try {
      const result = await request<{ message: string }>('/api/stacks/settings/github', 'DELETE')
      setValue(await request<Settings>('/api/stacks/settings')); setNotice(result.message); setDisconnecting(false)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not disconnect the App.') } finally { setBusy(false) }
  }

  const github = value?.githubApp
  const appMode = value?.authMode === 'github-app'
  const ready = !!value?.remote.trim() && !!value?.branch.trim() && (!appMode || !!github?.connected)
  return <Stack>
    {error && <Alert color="red">{error}</Alert>}
    {notice && <Alert color="green">{notice}</Alert>}
    {!value && !error && <Loader />}
    {value && <>
      <Text size="sm">All stacks use this repository and branch. Each Save creates a commit and pushes it; deployment remains a separate action.</Text>
      <TextInput label="Repository URL" placeholder="https://github.com/owner/stacks.git" description={appMode ? 'Use the HTTPS address of a repository selected during App installation.' : 'SSH or HTTPS. Keep passwords and tokens out of this field.'} value={value.remote} disabled={busy} onChange={(event) => { setValue({ ...value, remote: event.currentTarget.value }); setNotice('') }} />
      <TextInput label="Branch" value={value.branch} disabled={busy} onChange={(event) => { setValue({ ...value, branch: event.currentTarget.value }); setNotice('') }} />
      <Select label="Authentication" value={value.authMode} disabled={busy} allowDeselect={false} data={[{ value: 'github-app', label: 'GitHub App (recommended for GitHub)' }, { value: 'server', label: 'Server credentials (SSH or HTTPS)' }]} onChange={(mode) => { if (mode) { setValue({ ...value, authMode: mode }); setNotice('') } }} />
      {appMode ? <Card withBorder padding="sm"><Stack gap="sm">
        <Text fw={500} size="sm">{github?.connected ? `Connected: ${github.slug}` : 'Connect a GitHub App'}</Text>
        <Text size="sm" c="dimmed">Vigara automatically renews short-lived credentials. GitHub will ask you to create an App and choose which repository it can read and write.</Text>
        {!github?.available && <Alert color="yellow">{github?.setupHelp}</Alert>}
        {!github?.canManage && <Text size="sm">An administrator must connect or disconnect the App.</Text>}
        {!github?.connected && github?.canManage && <>
          <TextInput label="App owner organization (optional)" description="Leave empty to create the App under your personal GitHub account. Use an organization name for an organization-owned repository." value={organization} onChange={(event) => setOrganization(event.currentTarget.value)} disabled={busy} />
          {github.pending && <Button variant="light" disabled={busy || !github.available} onClick={() => void resume()}>Resume App installation</Button>}
          <Button disabled={busy || !github.available || !value.remote.trim() || !value.branch.trim()} loading={busy} onClick={() => void connect()}>{github.pending ? 'Start a new App registration' : 'Connect with GitHub'}</Button>
        </>}
        {github?.connected && <Group><Anchor size="sm" href={`https://github.com/apps/${encodeURIComponent(github.slug || '')}/installations/new`} target="_blank" rel="noreferrer">Manage repository access on GitHub</Anchor>{github.canManage && <Button size="xs" variant="subtle" color="red" disabled={busy} onClick={() => setDisconnecting(true)}>Disconnect App</Button>}</Group>}
        {disconnecting && <Alert color="yellow" title="Disconnect this App?"><Stack gap="xs"><Text size="sm">GitHub App authentication will stop working in Vigara. The repository and branch stay configured. Revoke the installation on GitHub separately.</Text><Group><Button variant="default" disabled={busy} onClick={() => setDisconnecting(false)}>Keep connected</Button><Button color="red" loading={busy} onClick={() => void disconnect()}>Disconnect App</Button></Group></Stack></Alert>}
      </Stack></Card> : <Text size="sm" c="dimmed">Git uses the Vigara service account’s SSH keys or Git credential helper, configured on the server.</Text>}
      <Text size="sm" c="dimmed">The repository needs an existing branch and at least one commit. Saves push directly to this branch; branches requiring pull requests are unsupported. Finish or discard drafts and resolve active deployments before switching repository or branch.</Text>
      <Group justify="end"><Button variant="default" loading={busy} disabled={!ready} onClick={() => void submit(false)}>Test connection</Button><Button loading={busy} disabled={!ready} onClick={() => void submit(true)}>Save settings</Button></Group>
    </>}
  </Stack>
}
