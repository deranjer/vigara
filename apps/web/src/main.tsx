import React from 'react'
import ReactDOM from 'react-dom/client'
import { useEffect, useState, type FormEvent } from 'react'
import { Alert, AppShell, Badge, Button, MantineProvider, PasswordInput, Text, TextInput, Title } from '@mantine/core'
import '@mantine/core/styles.css'
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
    await request('/api/auth/logout', undefined, 'POST')
    setUser(null)
    setView('login')
  }

  return <MantineProvider>
    <AppShell header={{ height: 64 }} padding="xl">
      <AppShell.Header className="topbar"><Text fw={700} size="lg">Vigira</Text>{view === 'dashboard' ? <Button variant="subtle" onClick={() => void logout()}>Log out</Button> : <Badge color="gray" variant="light">Private home monitoring</Badge>}</AppShell.Header>
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
        {view === 'dashboard' && <section className="welcome">
          <Text c="dimmed" size="sm" fw={600} tt="uppercase">Home infrastructure</Text>
          <Title order={1} mt="xs">Know what’s healthy. Know what’s safe.</Title>
          <Text c="dimmed" mt="md" maw={600}>Signed in as {user?.displayName}. Device inventory, monitoring, alerts, and recoverable backups will appear here.</Text>
          <div className="empty-state"><Text fw={600}>No devices yet</Text><Text c="dimmed" size="sm" mt={6}>When you enroll an agent, it will appear here with its latest health and backup status.</Text></div>
        </section>}
      </AppShell.Main>
    </AppShell>
  </MantineProvider>
}

ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><App /></React.StrictMode>)
