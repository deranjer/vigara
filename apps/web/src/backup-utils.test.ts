import { describe, expect, it } from 'vitest'
import { coveringPolicies, lastRun, untilTime, pathCovers, policyLabel, runDuration, runState, runStrip, scheduleText, uncoveredVolumes, type BackupPolicy, type BackupRun } from './backup-utils'

const policy = (over: Partial<BackupPolicy> = {}): BackupPolicy => ({ id: 'p1', deviceId: 'd1', agentId: 'a1', profileId: 'managed', name: 'Managed backup', destination: '/repo', includedPaths: ['/var/lib/docker/volumes/db/_data'], schedule: { weekdays: ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'], time: '03:00', timezone: 'UTC' }, enabled: true, nextRunAt: '', ...over })
const run = (over: Partial<BackupRun>): BackupRun => ({ id: 'r', policyId: 'p1', scheduledFor: '2026-11-12T03:00:00Z', status: 'completed', executionStatus: 'succeeded', ...over })

describe('backup coverage', () => {
  it('matches a folder and anything inside it', () => {
    expect(pathCovers('/srv/data', '/srv/data')).toBe(true)
    expect(pathCovers('/srv/data/', '/srv/data/app')).toBe(true)
    expect(pathCovers('/srv/data', '/srv/database')).toBe(false)
    expect(pathCovers('C:\\Users\\Alex', 'c:/users/alex/Documents')).toBe(true)
  })
  it('only counts enabled policies on the same device', () => {
    const policies = [policy(), policy({ id: 'p2', agentId: 'a2', includedPaths: ['/'] }), policy({ id: 'p3', enabled: false, includedPaths: ['/var'] })]
    expect(coveringPolicies(policies, 'a1', '/var/lib/docker/volumes/db/_data').map((p) => p.id)).toEqual(['p1'])
    const hosts = [{ agentId: 'a1', name: 'nas', volumes: [
      { name: 'db', mountpoint: '/var/lib/docker/volumes/db/_data', size: 10, anonymous: false },
      { name: 'media', mountpoint: '/var/lib/docker/volumes/media/_data', size: 20, anonymous: false, usedBy: [{ id: 'c', name: 'plex' }] },
      { name: '3f9a', mountpoint: '/var/lib/docker/volumes/3f9a/_data', size: 1, anonymous: true },
    ] }]
    expect(uncoveredVolumes(hosts, policies)).toEqual([{ agentId: 'a1', deviceName: 'nas', name: 'media', path: '/var/lib/docker/volumes/media/_data', size: 20, usedBy: ['plex'] }])
  })
})

describe('backup wording', () => {
  it('names managed backups after their device', () => {
    expect(policyLabel(policy(), 'nas-01')).toBe('nas-01 backup')
    expect(policyLabel(policy({ profileId: 'nightly', name: 'Nightly' }), 'nas-01')).toBe('Nightly')
  })
  it('describes schedules', () => {
    expect(scheduleText(policy())).toBe('Daily at 03:00')
    expect(scheduleText(policy({ schedule: { weekdays: ['Friday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday'], time: '01:30', timezone: 'UTC' } }))).toBe('Weekdays at 01:30')
    expect(scheduleText(policy({ schedule: { weekdays: ['Sunday'], time: '04:00', timezone: 'UTC' } }))).toBe('Sundays at 04:00')
    expect(scheduleText(policy({ schedule: { weekdays: ['Thursday', 'Monday'], time: '04:00', timezone: 'UTC' } }))).toBe('Mon, Thu at 04:00')
    expect(scheduleText(policy({ enabled: false }))).toBe('Paused')
  })
  it('describes runs and their length', () => {
    expect(runState(run({})).text).toBe('Succeeded')
    expect(runState(run({ executionStatus: 'failed', errorCode: 'repository_unavailable' })).kind).toBe('fail')
    expect(runState(run({ executionStatus: 'running', progress: { observedAt: '', phase: 'backup', percentDone: 41.6 } })).text).toBe('Running · 42%')
    expect(runDuration(run({ startedAt: '2026-11-12T03:00:00Z', finishedAt: '2026-11-12T03:03:12Z' }))).toBe('3m 12s')
    expect(runDuration(run({}))).toBe('')
    expect(untilTime('2026-11-12T07:00:00Z', Date.parse('2026-11-12T03:00:00Z'))).toBe('in 4 h')
    expect(untilTime('2026-11-12T02:00:00Z', Date.parse('2026-11-12T03:00:00Z'))).toBe('due now')
    expect(lastRun([run({ id: 'w', executionStatus: 'waiting' }), run({ id: 'ok' })])?.id).toBe('ok')
  })
})

describe('run strip', () => {
  it('has one cell per day, worst run wins', () => {
    const now = new Date(2026, 10, 12, 12)
    const at = (d: number) => new Date(2026, 10, d, 3).toISOString()
    const strip = runStrip([run({ scheduledFor: at(12) }), run({ scheduledFor: at(11), executionStatus: 'failed' }), run({ scheduledFor: at(11) }), run({ scheduledFor: new Date(2026, 9, 20, 3).toISOString() })], now)
    expect(strip).toHaveLength(14)
    expect(strip[13].kind).toBe('ok')
    expect(strip[12].kind).toBe('fail')
    expect(strip[11].kind).toBe('none')
    expect(strip.some((d) => d.day === new Date(2026, 9, 20).toDateString())).toBe(false)
  })
})
