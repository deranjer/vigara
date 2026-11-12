import { describe, expect, it } from 'vitest'
import { bucketOf, duration, incidentTarget, openCount, ruleCondition, ruleScope, timelineText } from './alerts-utils'
import { formatRoute } from './route'

const device = '7113dd5d-0000-4000-8000-000000000001'
const now = new Date('2026-11-12T12:00:00Z').getTime()

describe('alert inbox', () => {
  it('sorts alerts into open, acknowledged and muted, and counts only open ones', () => {
    const firing = { status: 'firing' }
    expect(bucketOf(firing, now)).toBe('open')
    expect(bucketOf({ ...firing, acknowledgedAt: '2026-11-12T11:00:00Z' }, now)).toBe('acknowledged')
    expect(bucketOf({ ...firing, acknowledgedAt: '2026-11-12T11:00:00Z', mutedUntil: '2026-11-12T13:00:00Z' }, now)).toBe('muted')
    expect(bucketOf({ ...firing, mutedUntil: '2026-11-12T11:59:00Z' }, now)).toBe('open')
    expect(bucketOf({ status: 'resolved' }, now)).toBe('resolved')
    expect(openCount([firing, firing, { ...firing, acknowledgedAt: 'x' }, { status: 'resolved' }], now)).toBe(2)
  })

  it('opens the cause of an alert on the right page', () => {
    const open = (kind: string, dimension: string) => { const t = incidentTarget({ kind, dimension }); return t && `${t.label} ${formatRoute(t.to)}` }
    expect(open('device_unavailable', device)).toBe(`Open device #/devices/${device}`)
    expect(open('threshold', `${device}/{"mount":"/"}`)).toBe(`Open metrics #/devices/${device}/metrics`)
    expect(open('backup_failure', `${device}/nightly`)).toBe('Open backups #/backups')
    expect(open('image_update', `${device}/image-update-failure/holyarrs`)).toBe('Open stack #/stacks/holyarrs')
    expect(open('pinned_image', `${device}/image/abc`)).toBe('Open updates #/updates')
  })

  it('words rules and durations', () => {
    expect(ruleCondition({ kind: 'device_unavailable', config: { staleAfterSeconds: 120 } })).toBe('No check-in for 2 min')
    expect(ruleCondition({ kind: 'threshold', config: { metric: 'cpu_used_percent', operator: 'gte', threshold: 90, sustainSeconds: 600 } })).toBe('CPU at or over 90% for 10 min')
    expect(ruleCondition({ kind: 'threshold', config: { metric: 'filesystem_free_bytes', operator: 'lt', threshold: 10 * 1024 ** 3 } })).toBe('Free disk under 10 GB')
    expect(ruleCondition({ kind: 'backup_overdue', config: { policyKey: 'nightly', intervalSeconds: 86400 } })).toBe('No successful “nightly” backup for 1 d')
    expect(ruleScope({ kind: 'threshold', config: { deviceId: device } }, () => 'docker-host-02')).toBe('docker-host-02')
    expect(ruleScope({ kind: 'threshold', config: {} }, () => 'x')).toBe('All devices')
    expect(duration('2026-11-12T11:15:00Z', now)).toBe('45 min')
    expect(duration('2026-11-12T10:29:00Z', now)).toBe('1 h 31 m')
    expect(duration('2026-11-10T08:00:00Z', now)).toBe('2 d')
  })

  it('describes timeline events, keeping image changes quiet', () => {
    expect(timelineText({ kind: 'image', at: '', summary: '', container: 'plex', image: 'lscr.io/linuxserver/plex:latest', deviceName: 'derajnet' }))
      .toEqual({ tone: 'info', text: 'plex has a newer image · lscr.io/linuxserver/plex:latest · derajnet' })
    expect(timelineText({ kind: 'resolved', at: '', summary: 'condition recovered', rule: 'Disk usage' })).toEqual({ tone: 'ok', text: 'Disk usage cleared' })
    expect(timelineText({ kind: 'firing', at: '', summary: 'cpu high', rule: 'CPU', severity: 'critical' })).toEqual({ tone: 'crit', text: 'CPU: cpu high' })
  })
})
