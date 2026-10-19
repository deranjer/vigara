export const shells = [
  { value: 'auto', label: 'Automatic (bash if available, otherwise sh)' },
  { value: 'sh', label: 'sh' },
  { value: 'bash', label: 'bash' },
] as const

export const minTerminalSize = 10
export const maxTerminalSize = 500
export const idleMinutes = 15

// The same rule the server applies, so a bad value is caught before connecting.
const userPattern = /^[A-Za-z0-9_.-]{1,64}(:[A-Za-z0-9_.-]{1,64})?$/

export function isValidExecUser(value: string): boolean {
  return value.trim() === '' || userPattern.test(value.trim())
}

export function clampTerminalSize(value: number, fallback: number): number {
  if (!Number.isFinite(value) || value <= 0) return fallback
  return Math.min(Math.max(Math.round(value), minTerminalSize), maxTerminalSize)
}

export type TerminalOptions = { shell: string; user: string; cols: number; rows: number }

/** The WebSocket path that opens a terminal for a container. */
export function terminalPath(hostId: string, reference: string, options: TerminalOptions): string {
  const query = new URLSearchParams()
  if (options.shell && options.shell !== 'auto') query.set('shell', options.shell)
  if (options.user.trim()) query.set('user', options.user.trim())
  query.set('cols', String(clampTerminalSize(options.cols, 80)))
  query.set('rows', String(clampTerminalSize(options.rows, 24)))
  return `/api/containers/${encodeURIComponent(hostId)}/${encodeURIComponent(reference)}/exec?${query.toString()}`
}

/** The control message that tells the host the terminal changed size. */
export function resizeMessage(cols: number, rows: number): string {
  return JSON.stringify({ type: 'resize', cols: clampTerminalSize(cols, 80), rows: clampTerminalSize(rows, 24) })
}

/** Splits input into pieces of at most `size` bytes so one huge paste cannot exceed a message limit. */
export function splitInput(data: Uint8Array, size: number): Uint8Array[] {
  if (data.length <= size) return [data]
  const pieces: Uint8Array[] = []
  for (let offset = 0; offset < data.length; offset += size) pieces.push(data.subarray(offset, offset + size))
  return pieces
}

export type TerminalEnd = { code: string; message: string }

/** Reads the terminal's final text message, or returns null when the text is not one. */
export function parseTerminalEnd(text: string): TerminalEnd | null {
  try {
    const value = JSON.parse(text) as { end?: unknown; code?: unknown; message?: unknown }
    if (value.end !== true) return null
    return { code: typeof value.code === 'string' ? value.code : '', message: typeof value.message === 'string' ? value.message : '' }
  } catch { return null }
}

/** What to show the user when a terminal ends. A clean exit has no message. */
export function describeTerminalEnd(end: TerminalEnd | null): string {
  if (!end) return 'The terminal session ended.'
  return end.message || 'The shell exited.'
}

const tierRank: Record<string, number> = { read: 0, manage: 1, exec: 2, deploy: 3 }

/** Why a terminal cannot be opened, or null when it can. */
export function terminalBlocker(role: string, hostTier: string | undefined, running: boolean | null): string | null {
  if (role !== 'operator' && role !== 'admin') return 'Your account can view containers but not open terminals in them.'
  if ((tierRank[hostTier ?? 'read'] ?? 0) < tierRank.exec) return 'Terminals are not enabled for this host. Set VIGIRA_DOCKER_MODE=exec on its agent and allow terminals when the device is enrolled.'
  if (running === false) return 'This container is not running, so there is nothing to open a terminal in.'
  return null
}
