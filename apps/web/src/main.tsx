import React from 'react'
import ReactDOM from 'react-dom/client'
import { useEffect, useState, type FormEvent } from 'react'
import { Alert, Button, MantineProvider, PasswordInput, Tabs, Text, TextInput, Title } from '@mantine/core'
import { ImageUpdates } from './image-updates'
import { Containers } from './containers'
import { Stacks } from './stacks'
import { Devices } from './devices'
import { Resources } from './resources'
import { Alerts } from './alerts'
import { Settings } from './settings'
import { AppFrame, PublicFrame, sectionLabels } from './app-shell'
import { PasswordWhitespaceWarning } from './password-warning'
import { containerTabs, defaultRoute, formatRoute, isLegacyHash, parseRoute, route as routeOf, sameRoute, type Route, type Section } from './route'
import { cssVariablesResolver, theme } from './theme'
import '@fontsource/inter/400.css'
import '@fontsource/inter/500.css'
import '@fontsource/inter/600.css'
import '@fontsource/inter/700.css'
import '@fontsource/jetbrains-mono/400.css'
import '@fontsource/jetbrains-mono/500.css'
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
  const [setupPassword, setSetupPassword] = useState('')
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash))
  const section = route.section
  const selectedId = route.id

  useEffect(() => { window.scrollTo({ top: 0, left: 0, behavior: 'instant' }) }, [section, selectedId])
  useEffect(() => {
    // Old addresses (#/monitoring, #/admin, #/account) are rewritten so the address bar shows the new page.
    const sync = () => {
      const next = parseRoute(window.location.hash)
      if (isLegacyHash(window.location.hash)) window.history.replaceState(null, '', formatRoute(next))
      setRoute(next)
    }
    sync()
    window.addEventListener('hashchange', sync)
    return () => window.removeEventListener('hashchange', sync)
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
      setError(cause instanceof Error ? cause.message : 'Could not connect to Vigara.')
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
      setSetupPassword('')
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

  const expireSession = () => { setUser(null); setView('login'); setError('Your session expired. Log in again.') }

  if (view === 'dashboard' && user) return <AppFrame user={user} route={route} onNavigate={go} onLogout={() => void logout()}>
    {error && <Alert color="red" mb="md" withCloseButton onClose={() => setError('')}>{error}</Alert>}
    <Page user={user} route={route} onNavigate={go} onUserChanged={setUser} onSessionExpired={expireSession} />
  </AppFrame>

  return <PublicFrame>
    {view === 'loading' && <section className="welcome"><Text c="dimmed">Loading Vigara…</Text></section>}
    {view === 'error' && <section className="auth-card"><Title order={2}>Vigara is unavailable</Title><Alert color="red" mt="md">{error}</Alert><Button mt="lg" onClick={() => { setView('loading'); setError(''); void loadView() }}>Try again</Button></section>}
    {view === 'setup' && <section className="auth-card">
      <Text c="dimmed" size="sm" fw={600} tt="uppercase">First-time setup</Text>
      <Title order={1} mt="xs">Create your local account</Title>
      <Text c="dimmed" mt="sm">This creates the first administrator account. Once setup is complete, this page will not appear again.</Text>
      <form className="auth-form" onSubmit={submitSetup}>
        <TextInput name="username" label="Username" required minLength={3} maxLength={64} autoComplete="username" />
        <TextInput name="displayName" label="Display name" maxLength={80} autoComplete="name" />
        <PasswordInput name="password" label="Password" required minLength={12} maxLength={1024} autoComplete="new-password" value={setupPassword} onChange={(event) => setSetupPassword(event.currentTarget.value)} />
        <PasswordWhitespaceWarning value={setupPassword} />
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
  </PublicFrame>
}

type PageProps = { user: User; route: Route; onNavigate: (route: Route) => void; onUserChanged: (user: User) => void; onSessionExpired: () => void }

function Page({ user, route, onNavigate, onUserChanged, onSessionExpired }: PageProps) {
  const role = user.role
  switch (route.section) {
    case 'overview': return <Devices key={user.id} role={role} section="monitoring" selectedId={route.id} onSelect={(id) => onNavigate(routeOf('overview', id))} />
    case 'alerts': return <Alerts key={user.id} />
    case 'devices': return <Devices key={user.id} role={role} section="devices" selectedId={route.id} onSelect={(id) => onNavigate(routeOf('devices', id))} />
    case 'backups': return <Devices key={user.id} role={role} section="backups" selectedId={route.id} onSelect={(id) => onNavigate(routeOf(id ? 'devices' : 'backups', id))} />
    case 'stacks': return <Stacks key={user.id} route={route} role={role} onNavigate={onNavigate} />
    case 'updates': return <ImageUpdates key={user.id} role={role} onNavigate={onNavigate} />
    case 'settings': return <Settings user={user} route={route} onNavigate={onNavigate} onUserChanged={onUserChanged} onSessionExpired={onSessionExpired} />
    case 'containers': case 'images': case 'networks': case 'volumes': {
      // A single container has its own detail page; the lists share one tab bar.
      const detail = route.section === 'containers' && route.id && route.sub
      return <>
        {!detail && <Tabs value={route.section} onChange={(value) => { if (value) onNavigate(routeOf(value as Section)) }} mb="md" className="vg-page-tabs">
          <Tabs.List>{containerTabs.map((tab) => <Tabs.Tab key={tab} value={tab}>{sectionLabels[tab]}</Tabs.Tab>)}</Tabs.List>
        </Tabs>}
        {route.section === 'containers'
          ? <Containers key={user.id} route={route} role={role} onNavigate={onNavigate} />
          : <Resources key={`${user.id}/${route.section}`} kind={route.section === 'images' ? 'image' : route.section === 'networks' ? 'network' : 'volume'} route={route} role={role} onNavigate={onNavigate} />}
      </>
    }
  }
}

ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><MantineProvider theme={theme} cssVariablesResolver={cssVariablesResolver} defaultColorScheme="light"><App /></MantineProvider></React.StrictMode>)
