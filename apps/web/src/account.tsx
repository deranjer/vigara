import { useEffect, useState, type FormEvent } from 'react'
import { Alert, Badge, Button, Card, Group, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core'
import { PasswordWhitespaceWarning } from './password-warning'

type AccountUser = { id: string; username: string; displayName: string; role: string }

export function Account({ user, onChanged, onSessionExpired }: { user: AccountUser; onChanged: (user: AccountUser) => void; onSessionExpired: () => void }) {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)

  const [mcpEnabled, setMcpEnabled] = useState(false)
  useEffect(() => {
    let ignore = false
    void fetch('/api/mcp/status', { credentials: 'same-origin' })
      .then(async (response) => response.ok ? response.json() as Promise<{ enabled?: boolean }> : null)
      .then((status) => { if (!ignore) setMcpEnabled(status?.enabled === true) })
      .catch(() => { /* The account form remains usable if status is unavailable. */ })
    return () => { ignore = true }
  }, [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError(''); setSuccess(false)
    if (newPassword !== confirmation) { setError('New passwords do not match.'); return }
    setBusy(true)
    try {
      const response = await fetch('/api/auth/password', {
        method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ currentPassword, newPassword, confirmPassword: confirmation }),
      })
      if (response.status === 401) { onSessionExpired(); return }
      const result = await response.json().catch(() => ({})) as { user?: AccountUser; detail?: string }
      if (!response.ok || !result.user) throw new Error(result.detail || 'Could not change your password. Please try again.')
      onChanged(result.user)
      setCurrentPassword(''); setNewPassword(''); setConfirmation(''); setSuccess(true)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not change your password.')
    } finally { setBusy(false) }
  }

  return <section className="account-page"><Stack gap="lg">
    <div><Text size="sm" c="dimmed" tt="uppercase">Your account</Text><Title order={1}>Account settings</Title><Text c="dimmed" mt="xs">Your profile and sign-in details.</Text></div>
    <Card withBorder radius="md" padding="lg"><Stack gap="md">
      <Group justify="space-between"><Title order={3}>{user.displayName || user.username}</Title><Badge variant="light">{user.role}</Badge></Group>
      <TextInput label="Username" value={user.username} readOnly autoComplete="username" />
    </Stack></Card>
    <Card withBorder radius="md" padding="lg"><Stack>
      <div><Title order={3}>Change password</Title><Text size="sm" c="dimmed" mt="xs">Confirm your current password, then enter your new password twice.</Text></div>
      {success && <Alert color="green" title="Password changed" role="status">You’re still signed in here. Your other sessions have been signed out.</Alert>}
      {error && <Alert color="red" title="Password could not be changed" role="alert">{error}</Alert>}
      <form onSubmit={(event) => void submit(event)}><Stack gap="md">
        <PasswordInput label="Current password" name="currentPassword" autoComplete="current-password" value={currentPassword} onChange={(event) => { setCurrentPassword(event.currentTarget.value); setSuccess(false) }} required maxLength={1024} disabled={busy} />
        <PasswordInput label="New password" name="newPassword" autoComplete="new-password" description="At least 12 characters. Spaces are preserved exactly." value={newPassword} onChange={(event) => { setNewPassword(event.currentTarget.value); setSuccess(false) }} required minLength={12} maxLength={1024} disabled={busy} />
        <PasswordWhitespaceWarning value={newPassword} />
        <PasswordInput label="Confirm new password" name="confirmPassword" autoComplete="new-password" value={confirmation} onChange={(event) => { setConfirmation(event.currentTarget.value); setSuccess(false) }} required minLength={12} maxLength={1024} disabled={busy} error={confirmation && confirmation !== newPassword ? 'New passwords do not match.' : undefined} />
        <Text size="xs" c="dimmed">Changing your password signs out your other sessions.</Text>
        <Button type="submit" loading={busy} disabled={!currentPassword || newPassword.length < 12 || newPassword !== confirmation} style={{ alignSelf: 'flex-start' }}>Change password</Button>
      </Stack></form>
    </Stack></Card>
    {mcpEnabled && <Card withBorder radius="md" padding="lg"><Stack gap="sm">
      <Title order={3}>Connected applications</Title>
      <Text size="sm" c="dimmed">Manage access granted to Claude, ChatGPT, and other applications.</Text>
      <a href="/oauth/connections">Manage connected applications</a>
    </Stack></Card>}
  </Stack></section>
}
