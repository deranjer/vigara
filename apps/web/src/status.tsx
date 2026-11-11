import type { ReactNode } from 'react'

export type StatusTone = 'ok' | 'warn' | 'crit' | 'info' | 'idle'

// Pages already pick Mantine colour names for states (green, red, …); map them onto the status tones.
const toneByColor: Record<string, StatusTone> = {
  green: 'ok', teal: 'ok', yellow: 'warn', orange: 'warn', red: 'crit', blue: 'info', gray: 'idle',
}

export function toneOf(color: string | undefined): StatusTone {
  return toneByColor[color ?? ''] ?? 'idle'
}

/** A coloured dot plus a word: the default way to show state in tables and lists. */
export function StatusText({ color, tone, children, title }: { color?: string; tone?: StatusTone; children: ReactNode; title?: string }) {
  return <span className="vg-status" data-tone={tone ?? toneOf(color)} title={title}><span className="vg-status-dot" aria-hidden="true" /><span className="vg-status-label">{children}</span></span>
}
