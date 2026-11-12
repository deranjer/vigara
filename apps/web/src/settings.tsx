import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Button, Group, Select, Stack, Text, Title, UnstyledButton } from '@mantine/core'
import { Account } from './account'
import type { ShellUser } from './app-shell'
import { GitSettings } from './git-settings'
import { McpConnections } from './mcp-connections'
import { NotificationSettingsPanel } from './notification-settings'
import { useReleaseStatus } from './release-check'
import { route as routeOf, type Route, type SettingsTab } from './route'
import { ScheduledTasks } from './scheduled-tasks'
import { StackProfiles } from './stack-profiles'
import { Panel } from './ui'
import { UsersSettings } from './users-settings'

type Props = { user: ShellUser; route: Route; onNavigate: (route: Route) => void; onUserChanged: (user: ShellUser) => void; onSessionExpired: () => void }
type Section = { id: SettingsTab; label: string; group: 'You' | 'Server'; admin: boolean; title: string; detail: string }

export const settingsSections: Section[] = [
  { id: 'account', label: 'Account', group: 'You', admin: false, title: 'Account', detail: 'Your sign-in details and password.' },
  { id: 'notifications', label: 'Notifications', group: 'You', admin: false, title: 'Notifications', detail: 'Where alerts reach you outside Vigara, and when.' },
  { id: 'users', label: 'Users & roles', group: 'Server', admin: true, title: 'Users & roles', detail: 'Who can sign in and what they can change.' },
  { id: 'git', label: 'Git repository', group: 'Server', admin: true, title: 'Git repository', detail: 'Where stack Compose files are kept and versioned.' },
  { id: 'profiles', label: 'Stack profiles', group: 'Server', admin: true, title: 'Stack profiles', detail: 'Server roles and which stacks each one runs.' },
  { id: 'ai', label: 'AI connections', group: 'Server', admin: false, title: 'AI connections', detail: 'Let Claude or ChatGPT read this server through MCP.' },
  { id: 'agents', label: 'Agents & updates', group: 'Server', admin: true, title: 'Agents & updates', detail: 'This server’s version and whether a newer release is out.' },
  { id: 'tasks', label: 'Scheduled tasks', group: 'Server', admin: true, title: 'Scheduled tasks', detail: 'Maintenance the server runs on its own.' },
]

/** Everything configured once, under one left-hand menu. Sections a role cannot change are left out. */
export function Settings({ user, route, onNavigate, onUserChanged, onSessionExpired }: Props) {
  const isAdmin = user.role === 'admin'
  const sections = settingsSections.filter((s) => isAdmin || !s.admin)
  const current = sections.find((s) => s.id === route.id) ?? sections[0]
  const go = (id: string) => onNavigate(routeOf('settings', id))
  const groups = (['You', 'Server'] as const).map((group) => ({ group, items: sections.filter((s) => s.group === group) })).filter((g) => g.items.length)

  return <div className="vg-page vg-settings">
    <nav className="vg-snav" aria-label="Settings sections">
      {groups.map(({ group, items }) => <div key={group} className="vg-snav-group">
        <span className="vg-snav-label">{group}</span>
        {items.map((s) => <UnstyledButton key={s.id} className="vg-snav-item" data-active={s.id === current.id || undefined} aria-current={s.id === current.id ? 'page' : undefined} onClick={() => go(s.id)}>{s.label}</UnstyledButton>)}
      </div>)}
    </nav>
    <Select className="vg-snav-select" aria-label="Settings section" value={current.id} allowDeselect={false} onChange={(v) => { if (v) go(v) }}
      data={groups.map(({ group, items }) => ({ group, items: items.map((s) => ({ value: s.id, label: s.label })) }))} />
    <Stack gap="md" className="vg-settings-body">
      <div><Title order={1}>{current.title}</Title><Text c="dimmed" size="sm" mt={4}>{current.detail}</Text></div>
      <SectionBody id={current.id} user={user} onNavigate={onNavigate} onUserChanged={onUserChanged} onSessionExpired={onSessionExpired} />
    </Stack>
  </div>
}

function SectionBody({ id, user, onNavigate, onUserChanged, onSessionExpired }: { id: SettingsTab } & Omit<Props, 'route'>): ReactNode {
  switch (id) {
    case 'account': return <Account key={user.id} user={user} onChanged={onUserChanged} onSessionExpired={onSessionExpired} />
    case 'notifications': return <NotificationSettingsPanel />
    case 'users': return <UsersSettings currentUserId={user.id} />
    case 'git': return <GitSection onNavigate={onNavigate} />
    case 'profiles': return <ProfilesSection onNavigate={onNavigate} />
    case 'ai': return <McpConnections admin={user.role === 'admin'} />
    case 'agents': return <AgentsSection onNavigate={onNavigate} />
    case 'tasks': return <ScheduledTasks />
  }
}

function GitSection({ onNavigate }: { onNavigate: (route: Route) => void }) {
  const [saved, setSaved] = useState(0)
  return <Stack gap="md" maw={760}>
    {saved > 0 && <Alert color="green" withCloseButton onClose={() => setSaved(0)}><Group justify="space-between" gap="sm">
      <Text size="sm">Git settings saved. Existing Compose projects on your devices can be imported from Stacks.</Text>
      <Button size="xs" variant="light" onClick={() => onNavigate(routeOf('stacks'))}>Open Stacks</Button>
    </Group></Alert>}
    <GitSettings key={saved} onSaved={() => setSaved((n) => n + 1)} />
  </Stack>
}

type StackListing = { configured: boolean; stacks: { id: string; name: string; commit?: string }[] }

function ProfilesSection({ onNavigate }: { onNavigate: (route: Route) => void }) {
  const [listing, setListing] = useState<StackListing | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    void fetch('/api/stacks', { credentials: 'same-origin' }).then(async (response) => {
      if (!response.ok) throw new Error((await response.text()).trim().slice(0, 300) || 'Stacks could not be loaded.')
      return await response.json() as StackListing
    }).then((value) => { if (active) setListing(value) }).catch((e: unknown) => { if (active) setError(e instanceof Error ? e.message : 'Stacks could not be loaded.') })
    return () => { active = false }
  }, [])
  if (error) return <Alert color="red">{error}</Alert>
  if (!listing) return <Text c="dimmed" size="sm">Loading…</Text>
  if (!listing.configured) return <Panel><Group justify="space-between"><Text size="sm">Profiles are stored in the stack repository. Connect one first.</Text><Button size="xs" variant="light" onClick={() => onNavigate(routeOf('settings', 'git'))}>Git repository</Button></Group></Panel>
  return <StackProfiles stacks={listing.stacks} />
}

function AgentsSection({ onNavigate }: { onNavigate: (route: Route) => void }) {
  const release = useReleaseStatus()
  return <Stack gap="md" maw={760}>
    <Panel title="Vigara server">
      <Group justify="space-between" align="flex-start" wrap="wrap">
        <div><Text size="lg" fw={600}>{release.version}</Text><Text size="sm" c={release.updateAvailable ? 'orange' : 'dimmed'} mt={4}>{release.text}</Text></div>
        <Group gap="xs">
          {release.updateAvailable && release.latest && <Button size="xs" variant="light" component="a" href={`https://github.com/deranjer/vigara/releases/tag/${encodeURIComponent(release.latest)}`} target="_blank" rel="noreferrer">Release notes</Button>}
          <Button size="xs" variant="default" disabled={release.checking} onClick={release.recheck}>Check for updates</Button>
        </Group>
      </Group>
    </Panel>
    <Panel title="Device agents">
      <Text size="sm">Each device’s agent is updated from that device’s Settings tab. Agent bundles are downloaded and pruned by the <a href="#" onClick={(e) => { e.preventDefault(); onNavigate(routeOf('settings', 'tasks')) }}>agent bundle task</a>.</Text>
      <Button mt="sm" size="xs" variant="subtle" onClick={() => onNavigate(routeOf('devices'))}>Open Devices</Button>
    </Panel>
  </Stack>
}
