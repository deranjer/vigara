import { useEffect, useState } from 'react'
import { Anchor, Text } from '@mantine/core'
import { loadPolicies, loadRuns } from './backups'
import { coveringPolicies, lastRun, runState, type BackupPolicy, type BackupRun } from './backup-utils'
import { relativeTime } from './overview-utils'
import { formatRoute, route } from './route'
import { StatusText } from './status'

type BackupStatus = { policies: BackupPolicy[]; last: Record<string, BackupRun | undefined>; lastGood: Record<string, BackupRun | undefined> }

// Several status lines can show at once (one per mount), so they share one load.
let cached: { at: number; value: Promise<BackupStatus> } | null = null
function load(): Promise<BackupStatus> {
  if (cached && Date.now() - cached.at < 30_000) return cached.value
  const value = loadPolicies().then(async (policies) => {
    const runs = await Promise.all(policies.map((p) => loadRuns(p.id, 30).catch(() => [] as BackupRun[])))
    return {
      policies,
      last: Object.fromEntries(policies.map((p, i) => [p.id, lastRun(runs[i])])),
      lastGood: Object.fromEntries(policies.map((p, i) => [p.id, runs[i].find((r) => runState(r).kind === 'ok')])),
    }
  })
  cached = { at: Date.now(), value }
  value.catch(() => { cached = null })
  return value
}

function useBackupStatus(): BackupStatus | null {
  const [status, setStatus] = useState<BackupStatus | null>(null)
  useEffect(() => {
    let active = true
    void load().then((s) => { if (active) setStatus(s) }).catch(() => undefined)
    return () => { active = false }
  }, [])
  return status
}

const when = (at: string) => {
  const d = new Date(at)
  return d.toDateString() === new Date().toDateString() ? `${d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} today` : relativeTime(at)
}

/** "2 policies · last run 03:00 today" for the device About panel. */
export function DeviceBackupLine({ deviceId, onOpen }: { deviceId: string; onOpen: () => void }) {
  const status = useBackupStatus()
  if (!status) return <Text size="sm" c="dimmed">…</Text>
  const policies = status.policies.filter((p) => p.deviceId === deviceId)
  if (policies.length === 0) return <span><StatusText tone="idle">Not set up</StatusText> <Anchor size="sm" href="#" onClick={(e) => { e.preventDefault(); onOpen() }}>Set up</Anchor></span>
  const runs = policies.map((p) => status.last[p.id]).filter((r): r is BackupRun => !!r).sort((a, b) => b.scheduledFor.localeCompare(a.scheduledFor))
  const failing = runs.find((r) => runState(r).kind === 'fail')
  const count = `${policies.length} ${policies.length === 1 ? 'policy' : 'policies'}`
  return <Anchor size="sm" href="#" underline="never" onClick={(e) => { e.preventDefault(); onOpen() }}>
    <StatusText tone={failing ? 'crit' : runs.length ? 'ok' : 'idle'}>{failing ? `${count} · last run failed ${when(failing.scheduledFor)}` : runs.length ? `${count} · last run ${when(runs[0].scheduledFor)}` : `${count} · no runs yet`}</StatusText>
  </Anchor>
}

/** Whether one container mount is backed up, for the container's Mounts table. */
export function MountBackup({ agentId, source }: { agentId: string; source?: string }) {
  const status = useBackupStatus()
  if (!source || (!source.startsWith('/') && !/^[a-z]:[\\/]/i.test(source))) return <Text size="sm" c="dimmed">–</Text>
  if (!status) return <Text size="sm" c="dimmed">…</Text>
  const policies = coveringPolicies(status.policies, agentId, source)
  if (policies.length === 0) return <span><StatusText tone="warn">Not backed up</StatusText> <Anchor size="xs" href={formatRoute(route('backups'))}>Add to a policy</Anchor></span>
  const failed = policies.map((p) => status.last[p.id]).find((r) => r && runState(r).kind === 'fail')
  const good = policies.map((p) => status.lastGood[p.id]).filter((r): r is BackupRun => !!r).sort((a, b) => b.scheduledFor.localeCompare(a.scheduledFor))[0]
  if (failed) return <StatusText tone="crit">Last backup failed{good ? ` · good copy ${when(good.scheduledFor)}` : ''}</StatusText>
  return <StatusText tone={good ? 'ok' : 'idle'}>{good ? `Backed up ${when(good.scheduledFor)}` : 'In a policy · not run yet'}</StatusText>
}
