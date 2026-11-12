import type { StatusTone } from './status'

export type BackupSchedule = { weekdays: string[]; time: string; timezone: string }
export type BackupPolicy = {
  id: string; deviceId: string; agentId: string; profileId: string; name: string; destination: string
  includedPaths: string[]; schedule: BackupSchedule; enabled: boolean; nextRunAt: string
}
export type BackupProgress = { observedAt: string; phase: string; filesDone?: number; filesTotal?: number; bytesDone?: number; bytesTotal?: number; percentDone?: number }
export type BackupRun = {
  id: string; policyId: string; scheduledFor: string; status: string; executionStatus: string; outcome?: string
  snapshotIds?: string[]; errorCode?: string; errorMessage?: string; progress?: BackupProgress; startedAt?: string; finishedAt?: string
}
export type VolumeInfo = { name: string; mountpoint?: string; size: number; anonymous: boolean; usedBy?: { id: string; name: string }[] }
export type UncoveredVolume = { agentId: string; deviceName: string; name: string; path: string; size: number; usedBy: string[] }

const weekdayOrder = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday']
const failed = new Set(['failed', 'interrupted', 'canceled', 'timed_out', 'stale', 'unknown'])

/** The name people see: managed backups all share one internal name, so they go by their device. */
export const policyLabel = (p: BackupPolicy, deviceName: string) => p.profileId === 'managed' ? `${deviceName} backup` : p.name

function pathKey(path: string): string {
  const windows = /^[a-z]:[\\/]/i.test(path) || path.startsWith('\\\\')
  const p = windows ? path.replaceAll('\\', '/').toLowerCase() : path
  return p.replace(/\/+$/, '') || '/'
}

/** Whether backing up `source` includes `path` (the same folder or one inside it). */
export function pathCovers(source: string, path: string): boolean {
  const s = pathKey(source), p = pathKey(path)
  return s === '/' || p === s || p.startsWith(`${s}/`)
}

export const coveringPolicies = (policies: BackupPolicy[], agentId: string, path: string) =>
  policies.filter((p) => p.enabled && p.agentId === agentId && p.includedPaths.some((source) => pathCovers(source, path)))

/** Named volumes on each device that no enabled policy on that device backs up. */
export function uncoveredVolumes(hosts: { agentId: string; name: string; volumes: VolumeInfo[] }[], policies: BackupPolicy[]): UncoveredVolume[] {
  return hosts.flatMap((host) => host.volumes
    .filter((v) => !v.anonymous && v.mountpoint && coveringPolicies(policies, host.agentId, v.mountpoint).length === 0)
    .map((v) => ({ agentId: host.agentId, deviceName: host.name, name: v.name, path: v.mountpoint!, size: v.size, usedBy: (v.usedBy ?? []).map((u) => u.name) })))
    .sort((a, b) => a.deviceName.localeCompare(b.deviceName) || a.name.localeCompare(b.name))
}

/** "Daily at 02:00", "Weekdays at 02:00", "Mon, Thu at 02:00", or "Paused". */
export function scheduleText(p: Pick<BackupPolicy, 'schedule' | 'enabled'>): string {
  if (!p.enabled) return 'Paused'
  const days = weekdayOrder.filter((d) => p.schedule.weekdays.includes(d))
  const when = days.length === 7 ? 'Daily' : days.length === 5 && !days.includes('Saturday') && !days.includes('Sunday') ? 'Weekdays'
    : days.length === 1 ? `${days[0]}s` : days.map((d) => d.slice(0, 3)).join(', ')
  return `${when} at ${p.schedule.time}`
}

export type RunState = { tone: StatusTone; text: string; kind: 'ok' | 'fail' | 'warn' | 'running' | 'waiting' | 'skipped' }

/** How a run went, in words. */
export function runState(run: BackupRun): RunState {
  const s = run.executionStatus
  if (s === 'succeeded' || (s === 'completed' && !run.errorCode)) return { tone: 'ok', text: 'Succeeded', kind: 'ok' }
  if (s === 'partial') return { tone: 'warn', text: 'Partly backed up', kind: 'warn' }
  if (s === 'running') return { tone: 'info', text: run.progress?.percentDone ? `Running · ${Math.round(run.progress.percentDone)}%` : 'Running', kind: 'running' }
  if (s === 'pending' || s === 'waiting') return { tone: 'idle', text: s === 'waiting' ? 'Waiting for the device' : 'Queued', kind: 'waiting' }
  if (s === 'skipped') return { tone: 'idle', text: 'Skipped', kind: 'skipped' }
  if (s === 'stale') return { tone: 'crit', text: 'Stopped reporting', kind: 'fail' }
  if (failed.has(s) || run.errorCode) return { tone: 'crit', text: s === 'timed_out' ? 'Timed out' : s === 'canceled' ? 'Canceled' : 'Failed', kind: 'fail' }
  return { tone: 'idle', text: s.replace(/_/g, ' '), kind: 'waiting' }
}

/** "3m 12s", from start to finish; empty while unknown. */
export function runDuration(run: BackupRun): string {
  if (!run.startedAt || !run.finishedAt) return ''
  const seconds = Math.max(0, Math.round((Date.parse(run.finishedAt) - Date.parse(run.startedAt)) / 1000))
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  return `${Math.floor(seconds / 3600)}h ${Math.floor(seconds % 3600 / 60)}m`
}

export type StripDay = { day: string; kind: 'ok' | 'fail' | 'warn' | 'running' | 'none' }

/** One cell per day for the last `days` days, oldest first; a failed run outranks a good one the same day. */
export function runStrip(runs: BackupRun[], now: Date, days = 14): StripDay[] {
  const key = (d: Date) => `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
  const byDay = new Map<string, StripDay['kind']>()
  const rank: Record<StripDay['kind'], number> = { none: 0, ok: 1, running: 2, warn: 3, fail: 4 }
  for (const run of runs) {
    const k = runState(run).kind
    const cell = k === 'ok' ? 'ok' : k === 'fail' ? 'fail' : k === 'warn' ? 'warn' : k === 'running' ? 'running' : null
    if (!cell) continue
    const day = key(new Date(run.scheduledFor))
    if (rank[cell] > rank[byDay.get(day) ?? 'none']) byDay.set(day, cell)
  }
  return Array.from({ length: days }, (_, i) => {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (days - 1 - i))
    return { day: d.toDateString(), kind: byDay.get(key(d)) ?? 'none' }
  })
}

/** The newest run that has finished or is running, skipping queued and skipped ones. */
export const lastRun = (runs: BackupRun[]) => runs.find((r) => !['waiting', 'skipped'].includes(runState(r).kind))

/** "in 12 min", "in 4 h", "in 5 d"; "due now" once the time has passed. */
export function untilTime(at: string | null | undefined, now = Date.now()): string {
  if (!at) return '—'
  const seconds = Math.round((Date.parse(at) - now) / 1000)
  if (Number.isNaN(seconds)) return '—'
  if (seconds < 60) return 'due now'
  if (seconds < 3600) return `in ${Math.round(seconds / 60)} min`
  if (seconds < 86400) return `in ${Math.round(seconds / 3600)} h`
  return `in ${Math.round(seconds / 86400)} d`
}

/** "/home/me/docs" as clickable steps: "/", "/home", "/home/me", "/home/me/docs". */
export function pathSteps(directory: string): { name: string; path: string }[] {
  const parts = directory.split('/').filter(Boolean)
  return [{ name: '/', path: '/' }, ...parts.map((name, i) => ({ name, path: `/${parts.slice(0, i + 1).join('/')}` }))]
}

/** Text files up to this size open in the viewer; larger ones are download-only. */
export const previewLimit = 1024 * 1024

/** A file with a NUL byte near the start is treated as binary rather than shown as text. */
export function looksBinary(bytes: Uint8Array): boolean {
  const n = Math.min(bytes.length, 8192)
  for (let i = 0; i < n; i++) if (bytes[i] === 0) return true
  return false
}
