import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Checkbox, Group, NativeSelect, Stack, Text, Textarea, TextInput, Title } from '@mantine/core'

type Source = { version: number; digest: string }
type Encrypted = { path: string; ciphertext: string; manifest: string }
type Props = {
  id: string
  files: Record<string, string>
  busy: boolean
  persistDraft: () => Promise<{ version: number; digest?: string }>
  onBusy: (busy: boolean) => void
  onEncrypted: (value: Encrypted) => void
}

// Plaintext lives only in this short-lived form. It never enters the stack
// draft, query cache, Monaco model, local/session storage or URL.
export function SecretEditor({ id, files, busy, persistDraft, onBusy, onEncrypted }: Props) {
  const [path, setPath] = useState(() => Object.keys(files).find((file) => file.endsWith('.age') || file.endsWith('.sops')) || 'secrets/runtime.env.age')
  const [value, setValue] = useState('')
  const [editing, setEditing] = useState(false)
  const [source, setSource] = useState<Source | null>(null)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  const [genName, setGenName] = useState('')
  const [genKind, setGenKind] = useState('password')
  const [genLength, setGenLength] = useState('')
  const [genOverwrite, setGenOverwrite] = useState(false)
  const [notice, setNotice] = useState('')
  const generation = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const onBusyRef = useRef(onBusy)
  onBusyRef.current = onBusy

  function hide() {
    generation.current++
    controller.current?.abort()
    controller.current = null
    setValue(''); setEditing(false); setSource(null); setError(''); setPending(false)
    onBusyRef.current(false)
  }

  useEffect(() => {
    const clear = () => {
      generation.current++
      controller.current?.abort()
      setValue(''); setEditing(false); setSource(null); setPending(false)
      onBusyRef.current(false)
    }
    clear()
    const visibility = () => { if (document.visibilityState !== 'visible') clear() }
    window.addEventListener('pagehide', clear)
    document.addEventListener('visibilitychange', visibility)
    return () => {
      generation.current++
      controller.current?.abort()
      onBusyRef.current(false)
      window.removeEventListener('pagehide', clear)
      document.removeEventListener('visibilitychange', visibility)
    }
  }, [id, files])

  async function post<T>(suffix: string, body: unknown, signal: AbortSignal): Promise<T> {
    const response = await fetch(`/api/stacks/${encodeURIComponent(id)}/environment${suffix}`, {
      method: 'POST', credentials: 'same-origin', cache: 'no-store', signal,
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    })
    if (!response.ok) throw new Error((await response.text()).trim().slice(0, 400) || 'The secret operation failed.')
    return response.json() as Promise<T>
  }

  async function reveal() {
    hide()
    const current = ++generation.current
    const abort = new AbortController()
    controller.current = abort
    setPending(true)
    onBusy(true)
    try {
      const draft = await persistDraft()
      if (generation.current !== current) return
      if (!draft.digest) throw new Error('Reload before revealing environment values.')
      const selected = { version: draft.version, digest: draft.digest }
      const result = await post<{ value: string }>('/reveal', { path, source: selected }, abort.signal)
      if (generation.current !== current) return
      setSource(selected); setValue(result.value); setEditing(true)
    } catch (cause) {
      if (generation.current === current) setError(cause instanceof Error ? cause.message : 'Environment reveal failed.')
    } finally { if (generation.current === current) { setPending(false); onBusy(false) } }
  }

  async function encrypt() {
    const plaintext = value
    const selected = source
    // Clear the visible form as soon as encryption begins, including on failure.
    setValue(''); setEditing(false); setSource(null); setError('')
    const current = ++generation.current
    const abort = new AbortController()
    controller.current = abort
    setPending(true)
    onBusy(true)
    try {
      let exact = selected
      if (!exact) {
        const draft = await persistDraft()
        if (!draft.digest) throw new Error('Reload before editing environment values.')
        exact = { version: draft.version, digest: draft.digest }
      }
      if (generation.current !== current) return
      const result = await post<Encrypted>('', { path, source: exact, value: plaintext }, abort.signal)
      if (generation.current !== current) return
      setPath(result.path)
      onEncrypted(result)
    } catch (cause) {
      if (generation.current === current) setError(cause instanceof Error ? cause.message : 'Environment encryption failed.')
    } finally { if (generation.current === current) { setPending(false); onBusy(false) } }
  }

  // Values are generated, merged and encrypted on the server; the browser only
  // learns the variable name and whether it was created, replaced or kept.
  async function generate() {
    const name = genName.trim()
    setNotice(''); setError('')
    const current = ++generation.current
    const abort = new AbortController()
    controller.current = abort
    setPending(true)
    onBusy(true)
    try {
      const draft = await persistDraft()
      if (!draft.digest) throw new Error('Reload before generating environment values.')
      if (generation.current !== current) return
      const variable: Record<string, unknown> = { name, kind: genKind, overwrite: genOverwrite }
      if (genLength.trim()) variable.length = Number(genLength)
      const result = await post<Encrypted & { generated: { name: string; status: string }[] }>('/generate', { path, source: { version: draft.version, digest: draft.digest }, variables: [variable] }, abort.signal)
      if (generation.current !== current) return
      setPath(result.path)
      onEncrypted(result)
      setNotice(result.generated.map((item) => `${item.name}: ${item.status}`).join(', ') + '. Save the revision to keep it.')
      setGenName('')
    } catch (cause) {
      if (generation.current === current) setError(cause instanceof Error ? cause.message : 'Secret generation failed.')
    } finally { if (generation.current === current) { setPending(false); onBusy(false) } }
  }

  return <Card withBorder padding="lg"><Stack>
    <Title order={3}>Encrypted environment</Title>
    <Text size="sm" c="dimmed">Values are hidden. Reveal and edit explicitly decrypts the exact saved draft on the server and records an audit event. This works while the device is offline. Encrypting adds ciphertext to your draft; save the revision when ready.</Text>
    <Text size="sm" c="dimmed">New stacks set up age encryption automatically on first use; their device must be online and updated for setup. They reuse this installation’s recovery key. After first encryption, download a copy and save it securely outside this server.</Text>
    <Button component="a" href="/api/stacks/imports/recovery" download variant="subtle" disabled={busy || !Object.keys(files).some((file) => file.endsWith('.age'))}>Download recovery key</Button>
    <TextInput label="Encrypted file path" value={path} disabled={busy} onChange={(event) => { hide(); setPath(event.currentTarget.value) }} autoComplete="off" />
    <Group gap="xs">{Object.keys(files).filter((file) => file.endsWith('.age') || file.endsWith('.sops')).map((file) => <Button key={file} size="compact-xs" variant={file === path ? 'light' : 'subtle'} disabled={busy} onClick={() => { hide(); setPath(file) }}>{file}</Button>)}</Group>
    {error && <Alert color="red">{error}</Alert>}
    {notice && <Alert color="green">{notice}</Alert>}
    <Group align="flex-end" gap="xs">
      <TextInput label="Generate variable" placeholder="DB_PASSWORD" value={genName} disabled={busy} onChange={(event) => setGenName(event.currentTarget.value)} autoComplete="off" />
      <NativeSelect label="Type" value={genKind} disabled={busy} onChange={(event) => setGenKind(event.currentTarget.value)} data={[
        { value: 'password', label: 'Password' }, { value: 'alphanumeric', label: 'Alphanumeric' }, { value: 'pin', label: 'PIN' },
        { value: 'hex', label: 'Hex key' }, { value: 'base64', label: 'Base64 key' }, { value: 'jwt_secret', label: 'JWT secret' }, { value: 'uuid', label: 'UUID' }]} />
      <TextInput label="Length (optional)" w={130} value={genLength} disabled={busy} onChange={(event) => setGenLength(event.currentTarget.value.replace(/\D/g, ''))} autoComplete="off" />
      <Checkbox label="Replace existing" checked={genOverwrite} disabled={busy} onChange={(event) => setGenOverwrite(event.currentTarget.checked)} />
      <Button disabled={busy || !/^[A-Za-z_][A-Za-z0-9_]*$/.test(genName.trim())} onClick={() => void generate()}>Generate into environment</Button>
    </Group>
    <Text size="xs" c="dimmed">The value is created and encrypted on the server and is not shown here. Use Reveal and edit to view it later.</Text>
    {editing ? <>
      <Textarea label={source ? 'Revealed dotenv contents' : 'Replacement dotenv contents'} value={value} onChange={(event) => setValue(event.currentTarget.value)} rows={20} resize="vertical" styles={{ input: { height: 'clamp(320px, 50vh, 640px)', minHeight: 240, fontFamily: 'var(--mantine-font-family-monospace)', lineHeight: 1.5 } }} spellCheck={false} autoComplete="off" autoCorrect="off" autoCapitalize="off" data-1p-ignore data-lpignore="true" />
      <Text size="xs" c="dimmed">Hide or leave this tab to clear the form. Encryption clears it even if the request fails.</Text>
      <Group><Button disabled={busy} onClick={() => void encrypt()}>Encrypt into draft</Button><Button variant="default" onClick={hide}>Hide and clear</Button></Group>
    </> : <Group>
      <Text size="sm" c="dimmed">•••••••• (hidden)</Text>
      <Button variant="light" disabled={busy || files[path] === undefined} onClick={() => void reveal()}>Reveal and edit</Button>
      <Button variant="default" disabled={busy} onClick={() => { hide(); setEditing(true) }}>Enter replacement</Button>
      {pending && <Button variant="subtle" onClick={hide}>Cancel secret operation</Button>}
    </Group>}
  </Stack></Card>
}
