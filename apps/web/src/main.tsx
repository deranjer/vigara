import React from 'react'
import ReactDOM from 'react-dom/client'
import { useEffect, useState, type FormEvent } from 'react'
import { Alert, AppShell, Badge, Burger, Avatar, Button, Group, MantineProvider, NavLink, PasswordInput, ScrollArea, Text, TextInput, Title, ActionIcon, useMantineColorScheme } from '@mantine/core'
import { useDisclosure, useMediaQuery } from '@mantine/hooks'
import { IconBox, IconChartLine, IconDeviceDesktop, IconHistory, IconLogout, IconMoon, IconSun } from '@tabler/icons-react'
import { Containers } from './containers'
import { Devices } from './devices'
import { defaultRoute, formatRoute, parseRoute, route as routeOf, sameRoute, type Route, type Section } from './route'
import { compareSemver } from './semver'
import '@mantine/core/styles.css'
import '@mantine/charts/styles.css'
import './styles.css'

type User = { id: string; username: string; displayName: string; role: string }
type View = 'loading' | 'error' | 'setup' | 'login' | 'dashboard'

class RequestError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request<T>(path: string, body?: unknown, method?: string): Promise<T> {
  const response = await fetch(path, {
    method: method ?? (body === undefined ? 'GET' : 'POST'),
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })
  const payload = await response.json().catch(() => ({})) as { detail?: string }
  if (!response.ok) throw new RequestError(response.status, payload.detail ?? 'The request could not be completed.')
  return payload as T
}

function App() {
  const [view, setView] = useState<View>('loading')
  const [user, setUser] = useState<User | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const mobile = useMediaQuery('(max-width: 47.99em)')
  const [navOpen, nav] = useDisclosure(true)
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash))
  const section = route.section
  const selectedId = route.id
  const { colorScheme, toggleColorScheme } = useMantineColorScheme()

  useEffect(() => { document.documentElement.dataset.mantineColorScheme = colorScheme }, [colorScheme])
  useEffect(() => { window.scrollTo({ top: 0, left: 0, behavior: 'instant' }) }, [section, selectedId])
  useEffect(() => {
    const onHashChange = () => setRoute(parseRoute(window.location.hash))
    window.addEventListener('hashchange', onHashChange)
    return () => window.removeEventListener('hashchange', onHashChange)
  }, [])

  // The address bar is the source of truth, so every page and selection can be deep-linked and the
  // browser's back button works. Assigning the hash fires hashchange, which updates the route.
  function go(next: Route) {
    if (sameRoute(next, route) && window.location.hash === formatRoute(next)) return
    window.location.hash = formatRoute(next)
    setRoute(next)
  }

  async function loadView() {
    try {
      const status = await request<{ setupRequired: boolean }>('/api/setup/status')
      if (status.setupRequired) {
        setView('setup')
        return
      }
      try {
        const current = await request<{ user: User }>('/api/auth/me')
        setUser(current.user)
        setView('dashboard')
      } catch {
        setView('login')
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not connect to Vigira.')
      setView('error')
    }
  }

  useEffect(() => { void loadView() }, [])

  async function submitSetup(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError('')
    const values = new FormData(event.currentTarget)
    try {
      const result = await request<{ user: User }>('/api/setup/first-user', {
        username: values.get('username'),
        displayName: values.get('displayName'),
        password: values.get('password'),
        confirmPassword: values.get('confirmPassword'),
      })
      setUser(result.user)
      setView('dashboard')
    } catch (cause) {
      if (cause instanceof RequestError && cause.status === 409) {
        setView('loading')
        await loadView()
        return
      }
      setError(cause instanceof Error ? cause.message : 'Could not finish setup.')
    } finally {
      setBusy(false)
    }
  }

  async function submitLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError('')
    const values = new FormData(event.currentTarget)
    try {
      const result = await request<{ user: User }>('/api/auth/login', {
        username: values.get('username'),
        password: values.get('password'),
      })
      setUser(result.user)
      setView('dashboard')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not log in.')
    } finally {
      setBusy(false)
    }
  }

  async function logout() {
    setBusy(true)
    setError('')
    try {
      await request('/api/auth/logout', undefined, 'POST')
      setUser(null)
      go(defaultRoute)
      setView('login')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not log out.')
    } finally { setBusy(false) }
  }

  function navigate(next: Section) {
    go(routeOf(next))
    if (mobile) nav.close()
  }

  return <>
    <AppShell header={{ height: 64 }} navbar={{ width: 258, breakpoint: 'sm', collapsed: { desktop: view !== 'dashboard' || !navOpen, mobile: view !== 'dashboard' || !navOpen } }} padding="lg">
      <AppShell.Header className="topbar"><Group><Burger opened={navOpen} onClick={nav.toggle} size="sm" aria-label="Toggle navigation" /><Text fw={700} size="lg">Vigara</Text><Badge color="gray" variant="light">Private home monitoring</Badge></Group><Group><ActionIcon variant="subtle" size="lg" aria-label={colorScheme === 'dark' ? 'Use light theme' : 'Use dark theme'} onClick={() => toggleColorScheme()}>{colorScheme === 'dark' ? <IconSun size={18}/> : <IconMoon size={18}/>}</ActionIcon></Group></AppShell.Header>
      {view === 'dashboard' && <AppShell.Navbar p="sm" className="sidebar">
        <ScrollArea>
          <Text size="xs" fw={700} c="dimmed" tt="uppercase" px="sm" py="xs">Workspace</Text>
          <NavLink component="button" label="Devices" leftSection={<IconDeviceDesktop size={18}/>} active={section === 'devices'} onClick={() => navigate('devices')} />
          <NavLink component="button" label="Monitoring" leftSection={<IconChartLine size={18}/>} active={section === 'monitoring'} onClick={() => navigate('monitoring')} />
          <NavLink component="button" label="Containers" leftSection={<IconBox size={18}/>} active={section === 'containers'} onClick={() => navigate('containers')} />
          <NavLink component="button" label="Backup history" leftSection={<IconHistory size={18}/>} active={section === 'backups'} onClick={() => navigate('backups')} />
        </ScrollArea>
        <div className="sidebar-footer">
          <Group wrap="nowrap" gap="sm" mb="sm">
            <Avatar name={user?.displayName || user?.username || 'Administrator'} color="blue" radius="xl" size={36} />
            <div className="sidebar-user"><Text size="sm" fw={600} truncate>{user?.displayName || user?.username}</Text><Text size="xs" c="dimmed" truncate>{user?.username} · {user?.role}</Text></div>
          </Group>
          <Button variant="light" color="red" fullWidth size="sm" mb="md" leftSection={<IconLogout size={16}/>} loading={busy} onClick={() => void logout()}>Log out</Button>
          <Text size="xs" c="dimmed">Vigara server</Text><ReleaseStatus />
        </div>
      </AppShell.Navbar>}
      <AppShell.Main>
        {view === 'loading' && <section className="welcome"><Text c="dimmed">Loading Vigira…</Text></section>}
        {view === 'error' && <section className="auth-card"><Title order={2}>Vigira is unavailable</Title><Alert color="red" mt="md">{error}</Alert><Button mt="lg" onClick={() => { setView('loading'); setError(''); void loadView() }}>Try again</Button></section>}
        {view === 'setup' && <section className="auth-card">
          <Text c="dimmed" size="sm" fw={600} tt="uppercase">First-time setup</Text>
          <Title order={1} mt="xs">Create your local account</Title>
          <Text c="dimmed" mt="sm">This creates the first administrator account. Once setup is complete, this page will not appear again.</Text>
          <form className="auth-form" onSubmit={submitSetup}>
            <TextInput name="username" label="Username" required minLength={3} maxLength={64} autoComplete="username" />
            <TextInput name="displayName" label="Display name" maxLength={80} autoComplete="name" />
            <PasswordInput name="password" label="Password" required minLength={12} maxLength={1024} autoComplete="new-password" />
            <PasswordInput name="confirmPassword" label="Confirm password" required minLength={12} maxLength={1024} autoComplete="new-password" />
            {error && <Alert color="red">{error}</Alert>}
            <Button type="submit" loading={busy}>Finish setup</Button>
          </form>
        </section>}
        {view === 'login' && <section className="auth-card">
          <Text c="dimmed" size="sm" fw={600} tt="uppercase">Local account</Text>
          <Title order={1} mt="xs">Welcome back</Title>
          <Text c="dimmed" mt="sm">Sign in to manage your devices and backups.</Text>
          <form className="auth-form" onSubmit={submitLogin}>
            <TextInput name="username" label="Username" required autoComplete="username" />
            <PasswordInput name="password" label="Password" required autoComplete="current-password" />
            {error && <Alert color="red">{error}</Alert>}
            <Button type="submit" loading={busy}>Log in</Button>
          </form>
        </section>}
        {view === 'dashboard' && <>{error && <Alert color="red" mb="md" withCloseButton onClose={() => setError('')}>{error}</Alert>}{section === 'containers' ? <Containers key={user?.id} route={route} role={user?.role ?? ''} onNavigate={go} /> : <Devices key={user?.id} section={section} selectedId={selectedId} onSelect={(id) => go(routeOf(id ? 'devices' : section, id))} />}</>}
      </AppShell.Main>
    </AppShell>
  </>
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
      const displayedVersion = `Version ${installed}${server.revision ? ` · ${server.revision.slice(0, 8)}` : ''}`
      setServerVersion(displayedVersion)
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
  return <><Text size="sm" fw={600}>{serverVersion}</Text><Text size="xs" c="dimmed" mt={4}>{status}</Text><Button variant="subtle" size="compact-xs" mt="xs" disabled={status.startsWith('Checking')} onClick={() => setCheckAttempt((value) => value + 1)}>Check for updates</Button></>
}

ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><MantineProvider defaultColorScheme="light"><App /></MantineProvider></React.StrictMode>)
