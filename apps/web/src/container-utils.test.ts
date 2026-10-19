import { describe, expect, it } from 'vitest'
import { appendBounded, filterLogLines, filterRows, fleetRows, formatBytes, formatRate, groupRows, isStreamEnd, logsToText, paginate, parseRecords, portSummary, stateCounts, streamUrl, type HostEntry, type LogLine } from './container-utils'

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
