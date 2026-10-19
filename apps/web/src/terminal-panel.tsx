import { useCallback, useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { Alert, Badge, Button, Group, Select, Stack, Text, TextInput } from '@mantine/core'
import { streamUrl } from './container-utils'
import { describeTerminalEnd, idleMinutes, isValidExecUser, parseTerminalEnd, resizeMessage, shells, splitInput, terminalBlocker, terminalPath } from './terminal-utils'

type Status = 'idle' | 'connecting' | 'live' | 'ended' | 'failed'
type Props = { hostId: string; reference: string; name: string; running: boolean | null; role: string; hostTier?: string }

const badges: Record<Status, { color: string; label: string }> = {
  idle: { color: 'gray', label: 'Not connected' }, connecting: { color: 'blue', label: 'Connecting…' }, live: { color: 'green', label: 'Connected' },
  ended: { color: 'gray', label: 'Ended' }, failed: { color: 'red', label: 'Disconnected' },
}

// A paste larger than this is sent as several messages.
const inputPiece = 256 * 1024

type Live = { socket: WebSocket; terminal: Terminal; observer: ResizeObserver }

/** An interactive shell inside the container, drawn with xterm.js over the server's terminal WebSocket. */
export function TerminalPanel({ hostId, reference, name, running, role, hostTier }: Props) {
  const [shell, setShell] = useState<string | null>('auto')
  const [user, setUser] = useState('')
  const [status, setStatus] = useState<Status>('idle')
  const [message, setMessage] = useState('')
  const holder = useRef<HTMLDivElement>(null)
  const live = useRef<Live | null>(null)

  const close = useCallback(() => {
    const current = live.current
    live.current = null
    if (!current) return
    current.observer.disconnect()
    current.socket.onclose = null
    current.socket.close()
    current.terminal.dispose()
  }, [])

  // Leaving the tab or the page closes the socket, which tells the server to hang up the shell.
  useEffect(() => close, [close])

  const connect = useCallback(() => {
    const element = holder.current
    if (!element) return
    close()
    setMessage('')
    setStatus('connecting')

    const terminal = new Terminal({
      cursorBlink: true, scrollback: 5000, fontSize: 14, allowProposedApi: false,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      theme: { background: '#0b0d10', foreground: '#d8dee9' },
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.open(element)
    fit.fit()

    const socket = new WebSocket(streamUrl(terminalPath(hostId, reference, { shell: shell ?? 'auto', user, cols: terminal.cols, rows: terminal.rows }), window.location))
    socket.binaryType = 'arraybuffer'
    const encoder = new TextEncoder()
    let ended = false
    const send = (bytes: Uint8Array) => { if (socket.readyState === WebSocket.OPEN) for (const piece of splitInput(bytes, inputPiece)) socket.send(piece as Uint8Array<ArrayBuffer>) }

    socket.onopen = () => { setStatus('live'); terminal.focus() }
    socket.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) { terminal.write(new Uint8Array(event.data)); return }
      if (typeof event.data !== 'string') return
      ended = true
      const end = parseTerminalEnd(event.data)
      const text = describeTerminalEnd(end)
      terminal.write(`\r\n\x1b[2m[${text}]\x1b[0m\r\n`)
      setMessage(text)
      setStatus('ended')
    }
    socket.onclose = () => {
      if (ended) return
      setMessage('The connection to the server was lost.')
      setStatus('failed')
    }
    terminal.onData((data) => send(encoder.encode(data)))
    terminal.onBinary((data) => send(Uint8Array.from(data, (character) => character.charCodeAt(0))))
    terminal.onResize(({ cols, rows }) => { if (socket.readyState === WebSocket.OPEN) socket.send(resizeMessage(cols, rows)) })
    const observer = new ResizeObserver(() => { try { fit.fit() } catch { /* The element may be hidden while the tab changes. */ } })
    observer.observe(element)
    live.current = { socket, terminal, observer }
  }, [close, hostId, reference, shell, user])

  const blocker = terminalBlocker(role, hostTier, running)
  if (blocker) return <Alert color="gray" title="Terminal unavailable">{blocker}</Alert>

  const badge = badges[status]
  const active = status === 'connecting' || status === 'live'
  const userError = isValidExecUser(user) ? undefined : 'Use a user name or number, optionally followed by :group.'
  return <Stack gap="sm">
    <Alert color="yellow" title="This opens a shell inside the container">
      Anything you type runs with that container's privileges, and a shell in a container is often root. Opening and closing a terminal is recorded in the audit log (never what you type or see).
      The session closes after {idleMinutes} minutes without typing, and when you leave this tab.
    </Alert>
    <Group gap="sm" align="flex-end">
      <Select label="Shell" value={shell} onChange={setShell} data={[...shells]} allowDeselect={false} disabled={active} w={300} />
      <TextInput label="Run as" placeholder="Container default" value={user} onChange={(event) => setUser(event.currentTarget.value)} disabled={active} error={userError} w={180} />
      {active ? <Button color="red" variant="light" onClick={() => { close(); setStatus('idle'); setMessage('') }}>Disconnect</Button>
        : <Button disabled={Boolean(userError)} onClick={connect}>{status === 'idle' ? 'Open terminal' : 'Reconnect'}</Button>}
      <Badge color={badge.color} variant="light" mb={6}>{badge.label}</Badge>
    </Group>
    {status === 'failed' && <Alert color="red" title="The terminal disconnected">{message}</Alert>}
    <div ref={holder} role="application" aria-label={`Terminal for ${name}`} style={{ height: 460, background: '#0b0d10', borderRadius: 8, padding: 8, display: status === 'idle' ? 'none' : 'block' }} />
    {status === 'idle' && <Text size="sm" c="dimmed">Choose a shell and press Open terminal.</Text>}
  </Stack>
}
