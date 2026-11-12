import { useEffect, useState } from 'react'
import { Button, Card, Stack, Tabs, Text, Title } from '@mantine/core'
import { Account } from './account'
import { Admin } from './admin'
import { NotificationSettingsPanel } from './notification-settings'
import { route as routeOf, settingsTabs, type Route, type SettingsTab } from './route'
import { compareSemver } from './semver'
import type { ShellUser } from './app-shell'

type Props = { user: ShellUser; route: Route; onNavigate: (route: Route) => void; onUserChanged: (user: ShellUser) => void; onSessionExpired: () => void }

/** One home for account, server administration and version details. */
export function Settings({ user, route, onNavigate, onUserChanged, onSessionExpired }: Props) {
  const isAdmin = user.role === 'admin'
  const available = settingsTabs.filter((tab) => tab !== 'server' || isAdmin)
  const requested = settingsTabs.find((tab) => tab === route.id)
  const tab: SettingsTab = requested && available.includes(requested) ? requested : isAdmin ? 'server' : 'account'
  return <Stack gap="lg" className="vg-page">
    <div><Title order={1}>Settings</Title><Text c="dimmed" mt={6}>Your account, notifications, server administration and version details.</Text></div>
    <Tabs value={tab} onChange={(value) => { if (value) onNavigate(routeOf('settings', value)) }} keepMounted={false}>
      <Tabs.List mb="lg">
        <Tabs.Tab value="account">Account</Tabs.Tab>
        <Tabs.Tab value="notifications">Notifications</Tabs.Tab>
        {isAdmin && <Tabs.Tab value="server">Server</Tabs.Tab>}
        <Tabs.Tab value="about">About</Tabs.Tab>
      </Tabs.List>
      <Tabs.Panel value="account"><Account key={user.id} user={user} onChanged={onUserChanged} onSessionExpired={onSessionExpired} /></Tabs.Panel>
      <Tabs.Panel value="notifications"><NotificationSettingsPanel /></Tabs.Panel>
      {isAdmin && <Tabs.Panel value="server"><Admin role={user.role} /></Tabs.Panel>}
      <Tabs.Panel value="about"><Card withBorder padding="lg" maw={560}><Text size="sm" c="dimmed">Vigara server</Text><ReleaseStatus /></Card></Tabs.Panel>
    </Tabs>
  </Stack>
}

function ReleaseStatus() {
  const [checkAttempt, setCheckAttempt] = useState(0)
  const [serverVersion, setServerVersion] = useState('…')
  const [status, setStatus] = useState('Checking for releases…')
  useEffect(() => {
    setStatus('Checking for releases…')
    const controller = new AbortController()
    const timer = window.setTimeout(() => controller.abort(), 4500)
    void fetch('/api/version', { signal: controller.signal }).then(async (serverResponse) => {
      if (!serverResponse.ok) throw new Error('Server version unavailable')
      const server = await serverResponse.json() as { version: string; revision?: string }
      const installed = server.version || 'dev'
      setServerVersion(`Version ${installed}${server.revision ? ` · ${server.revision.slice(0, 8)}` : ''}`)
      const key = `vigara-release-check-v2:${installed}`
      const cache = sessionStorage.getItem(key)
      if (cache && checkAttempt === 0) { try { const value = JSON.parse(cache) as { at: number; status: string }; if (Date.now() - value.at < 5 * 60_000) { setStatus(value.status); return } } catch { /* Ignore malformed local cache. */ } }
      return fetch('https://api.github.com/repos/deranjer/vigara/releases/latest', { headers: { Accept: 'application/vnd.github+json' }, signal: controller.signal }).then(async (response) => {
        let next: string
        if (response.status === 404) next = 'No published release yet'
        else if (!response.ok) throw new Error('Release check unavailable')
        else {
          const release = await response.json() as { tag_name?: string }
          const latest = release.tag_name ?? ''
          const comparison = compareSemver(latest, installed)
          next = comparison === null ? `Latest release ${latest}; comparison unavailable` : comparison > 0 ? `Update available: ${latest}` : `Up to date · latest ${latest}`
        }
        setStatus(next); sessionStorage.setItem(key, JSON.stringify({ at: Date.now(), status: next }))
      })
    }).catch(() => setStatus('Release check unavailable')).finally(() => window.clearTimeout(timer))
    return () => { controller.abort(); window.clearTimeout(timer) }
  }, [checkAttempt])
  return <><Text size="lg" fw={600} mt={4}>{serverVersion}</Text><Text size="sm" c="dimmed" mt={4}>{status}</Text><Button variant="light" size="xs" mt="md" disabled={status.startsWith('Checking')} onClick={() => setCheckAttempt((value) => value + 1)}>Check for updates</Button></>
}
