import { describe, expect, it } from 'vitest'
import { actionsFor, appendBounded, canManage, drainRecords, filterLogLines, filterRows, fleetRows, formatBytes, formatRate, groupRows, isStreamEnd, logsToText, manageBlocker, needsConfirmation, paginate, parseRecords, portSummary, runLimited, shortImageId, stateCounts, streamUrl, type ActionName, type HostEntry, type LogLine } from './container-utils'

const hosts: HostEntry[] = [
  { agentId: 'a1', name: 'nas', state: 'ok', containers: [
    { id: 'c1', name: 'web', image: 'nginx:1.27', state: 'running', status: 'Up', created: 1, project: 'site', service: 'web', ports: [{ privatePort: 80, publicPort: 8080, type: 'tcp' }, { privatePort: 80, publicPort: 8080, type: 'tcp', ip: '::' }] },
    { id: 'c2', name: 'db', image: 'postgres:17', state: 'running', status: 'Up', created: 2, project: 'site', service: 'db' },
    { id: 'c3', name: 'tool', image: 'busybox', state: 'exited', status: 'Exited', created: 3 },
  ] },
  { agentId: 'a2', name: 'attic', state: 'ok', containers: [{ id: 'c4', name: 'web', image: 'caddy', state: 'paused', status: 'Paused', created: 4, project: 'site' }] },
  { agentId: 'a3', name: 'down', state: 'offline', containers: [] },
]

describe('container-utils', () => {
  it('formats sizes and rates', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes(1536)).toBe('1.5 KiB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB')
    expect(formatBytes(250 * 1024 * 1024)).toBe('250 MiB')
    expect(formatBytes(3 * 1024 ** 4)).toBe('3.0 TiB')
    expect(formatBytes(-1)).toBe('–')
    expect(formatBytes(Number.NaN)).toBe('–')
    expect(formatRate(2048)).toBe('2.0 KiB/s')
    expect(formatRate(undefined)).toBe('–')
  })

  it('flattens, filters and counts the fleet', () => {
    const rows = fleetRows(hosts)
    expect(rows).toHaveLength(4)
    expect(rows[0]).toMatchObject({ hostId: 'a1', hostName: 'nas', name: 'web' })
    expect(filterRows(rows, { search: '', hostId: null, state: null })).toHaveLength(4)
    expect(filterRows(rows, { search: 'NGINX', hostId: null, state: null }).map((row) => row.id)).toEqual(['c1'])
    expect(filterRows(rows, { search: 'attic', hostId: null, state: null }).map((row) => row.id)).toEqual(['c4'])
    expect(filterRows(rows, { search: 'web', hostId: 'a2', state: null }).map((row) => row.id)).toEqual(['c4'])
    expect(filterRows(rows, { search: '', hostId: null, state: 'exited' }).map((row) => row.id)).toEqual(['c3'])
    expect(filterRows(rows, { search: 'nothing-matches', hostId: null, state: null })).toEqual([])
    expect(stateCounts(rows)).toEqual({ running: 2, exited: 1, paused: 1 })
  })

  it('groups by host and Compose project with standalone containers last', () => {
    const groups = groupRows(fleetRows(hosts))
    expect(groups.map((group) => `${group.subtitle}/${group.title}`)).toEqual(['attic/site', 'nas/site', 'nas/Standalone containers'])
    expect(groups[1].rows.map((row) => row.id)).toEqual(['c1', 'c2'])
  })

  it('paginates within bounds', () => {
    const items = Array.from({ length: 53 }, (_, index) => index)
    expect(paginate(items, 1, 25)).toMatchObject({ pages: 3, page: 1 })
    expect(paginate(items, 3, 25).items).toHaveLength(3)
    expect(paginate(items, 99, 25).page).toBe(3)
    expect(paginate(items, -4, 25).page).toBe(1)
    expect(paginate([], 1, 25)).toEqual({ items: [], pages: 1, page: 1 })
  })

  it('summarises ports without duplicates', () => {
    expect(portSummary(undefined)).toBe('')
    expect(portSummary(hosts[0].containers[0].ports)).toBe('8080→80/tcp')
    expect(portSummary([1, 2, 3, 4, 5].map((n) => ({ privatePort: n, type: 'tcp' })), 2)).toBe('1/tcp, 2/tcp +3')
  })

  it('builds same-origin stream addresses', () => {
    expect(streamUrl('/api/x', { protocol: 'http:', host: '192.168.1.97:8080' })).toBe('ws://192.168.1.97:8080/api/x')
    expect(streamUrl('/api/x', { protocol: 'https:', host: 'vigara.example' })).toBe('wss://vigara.example/api/x')
  })

  it('parses newline-delimited records and detects the end marker', () => {
    const records = parseRecords<LogLine>('{"s":"o","m":"one"}\n\nnot json\n{"s":"e","m":"two"}\n{"end":true,"code":"not_found","message":"gone"}')
    expect(records).toHaveLength(3)
    expect(isStreamEnd(records[0])).toBe(false)
    expect(isStreamEnd(records[2])).toBe(true)
    expect(parseRecords('')).toEqual([])
    expect(isStreamEnd(null)).toBe(false)
    expect(isStreamEnd({ end: 'true' })).toBe(false)
  })

  it('bounds log buffers, filters and exports them', () => {
    expect(appendBounded([1, 2, 3], [4, 5], 4)).toEqual([2, 3, 4, 5])
    expect(appendBounded([1], [2], 10)).toEqual([1, 2])
    const lines: LogLine[] = [{ s: 'o', t: '2026-10-18T10:00:00Z', m: 'Started server' }, { s: 'e', m: 'Disk WARNING' }]
    expect(filterLogLines(lines, 'warning')).toEqual([lines[1]])
    expect(filterLogLines(lines, '  ')).toEqual(lines)
    expect(logsToText(lines)).toBe('2026-10-18T10:00:00Z Started server\n[stderr] Disk WARNING')
  })
})

describe('container actions', () => {
  it('offers sensible actions for each state', () => {
    expect(actionsFor('running')).toEqual(['stop', 'restart', 'pause', 'kill', 'remove'])
    expect(actionsFor('paused')).toEqual(['unpause', 'stop', 'kill', 'remove'])
    expect(actionsFor('restarting')).toEqual(['stop', 'kill', 'remove'])
    expect(actionsFor('exited')).toEqual(['start', 'remove'])
    expect(actionsFor('created')).toEqual(['start', 'remove'])
    expect(actionsFor('something-new')).toEqual(['start', 'remove'])
  })

  it('requires both a role and a host tier before allowing changes', () => {
    expect(canManage('admin', 'manage')).toBe(true)
    expect(canManage('operator', 'exec')).toBe(true)
    expect(canManage('operator', 'deploy')).toBe(true)
    expect(canManage('viewer', 'manage')).toBe(false)
    expect(canManage('admin', 'read')).toBe(false)
    expect(canManage('admin', undefined)).toBe(false)
    expect(canManage('', 'manage')).toBe(false)
    expect(manageBlocker('admin', 'manage')).toBeNull()
    expect(manageBlocker('viewer', 'manage')).toContain('view')
    expect(manageBlocker('operator', 'read')).toContain('VIGIRA_DOCKER_MODE')
  })

  it('asks for confirmation only on destructive actions', () => {
    expect(['start', 'stop', 'restart', 'pause', 'unpause'].some((action) => needsConfirmation(action as ActionName))).toBe(false)
    expect(needsConfirmation('kill')).toBe(true)
    expect(needsConfirmation('remove')).toBe(true)
    expect(needsConfirmation('recreate')).toBe(true)
  })

  it('drains complete NDJSON records and keeps the partial tail', () => {
    const first = drainRecords<{ step: string }>('{"step":"pull"}\n{"step":"cre')
    expect(first.records).toEqual([{ step: 'pull' }])
    expect(first.rest).toBe('{"step":"cre')
    const second = drainRecords<{ step: string }>(first.rest + 'ate"}\nnot json\n\n{"step":"done"}\n')
    expect(second.records).toEqual([{ step: 'create' }, { step: 'done' }])
    expect(second.rest).toBe('')
    expect(drainRecords('')).toEqual({ records: [], rest: '' })
  })

  it('runs tasks with a concurrency limit and keeps result order', async () => {
    let running = 0
    let peak = 0
    const results = await runLimited([30, 5, 20, 1, 10, 2], 2, async (ms) => {
      running += 1
      peak = Math.max(peak, running)
      await new Promise((resolve) => setTimeout(resolve, ms))
      running -= 1
      return ms * 2
    })
    expect(results).toEqual([60, 10, 40, 2, 20, 4])
    expect(peak).toBe(2)
    expect(await runLimited([], 3, async (value: number) => value)).toEqual([])
  })

  it('formats image ids and carries the host tier on rows', () => {
    expect(shortImageId('sha256:0123456789abcdef0123')).toBe('0123456789ab')
    expect(shortImageId(undefined)).toBe('–')
    const rows = fleetRows([{ agentId: 'a', name: 'nas', state: 'ok', dockerTier: 'manage', containers: [{ id: 'c', name: 'web', image: 'x', state: 'running', status: 'Up', created: 1 }] }])
    expect(rows[0].hostTier).toBe('manage')
  })
})
