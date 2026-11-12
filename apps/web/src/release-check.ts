import { useCallback, useEffect, useState } from 'react'
import { compareSemver } from './semver'

export type ReleaseStatus = { version: string; text: string; latest?: string; updateAvailable: boolean; checking: boolean }

type Result = Omit<ReleaseStatus, 'checking'>
export type GitHubRelease = { tag_name?: string; draft?: boolean; prerelease?: boolean }

/** The newest published server release; app releases and drafts are skipped. */
export function newestServerRelease(releases: GitHubRelease[]): string | null {
  const tags = releases.filter((r) => !r.draft && !r.prerelease && r.tag_name && compareSemver(r.tag_name, r.tag_name) !== null).map((r) => r.tag_name!)
  return tags.sort((a, b) => compareSemver(b, a) ?? 0)[0] ?? null
}
const cacheTtl = 5 * 60_000
// The sidebar and Settings ask at the same time; they share one request.
let inflight: Promise<Result> | null = null

async function check(force: boolean): Promise<Result> {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), 4500)
  let version = '…'
  try {
    const serverResponse = await fetch('/api/version', { signal: controller.signal })
    if (!serverResponse.ok) throw new Error('Server version unavailable')
    const server = await serverResponse.json() as { version: string; revision?: string }
    const installed = server.version || 'dev'
    version = `Version ${installed}${server.revision ? ` · ${server.revision.slice(0, 8)}` : ''}`
    const key = `vigara-release-check-v4:${installed}`
    if (!force) {
      try {
        const cached = JSON.parse(sessionStorage.getItem(key) ?? 'null') as { at: number; result: Result } | null
        if (cached && Date.now() - cached.at < cacheTtl) return { ...cached.result, version }
      } catch { /* A malformed or blocked cache just means checking again. */ }
    }
    // The repository also publishes app releases (android-v…), so pick the newest server version tag.
    const response = await fetch('https://api.github.com/repos/deranjer/vigara/releases?per_page=30', { headers: { Accept: 'application/vnd.github+json' }, signal: controller.signal })
    if (!response.ok) throw new Error('Release check unavailable')
    const latest = newestServerRelease(await response.json() as GitHubRelease[])
    let result: Result
    if (!latest) result = { version, text: 'No published release yet', updateAvailable: false }
    else {
      const comparison = compareSemver(latest, installed)
      result = comparison === null ? { version, latest, text: `Latest release ${latest}; comparison unavailable`, updateAvailable: false }
        : comparison > 0 ? { version, latest, text: `Update available: ${latest}`, updateAvailable: true }
        : { version, latest, text: `Up to date · latest ${latest}`, updateAvailable: false }
    }
    try { sessionStorage.setItem(key, JSON.stringify({ at: Date.now(), result })) } catch { /* Caching is optional. */ }
    return result
  } catch {
    return { version, text: 'Release check unavailable', updateAvailable: false }
  } finally { window.clearTimeout(timer) }
}

/** This server's version and whether a newer release is published. `enabled` false skips the check. */
export function useReleaseStatus(enabled = true): ReleaseStatus & { recheck: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [status, setStatus] = useState<ReleaseStatus>({ version: '…', text: 'Checking for releases…', updateAvailable: false, checking: true })
  useEffect(() => {
    if (!enabled) return
    let active = true
    setStatus((s) => ({ ...s, text: 'Checking for releases…', checking: true }))
    const force = attempt > 0
    const request = !force && inflight ? inflight : check(force)
    if (!force) { inflight = request; void request.finally(() => { if (inflight === request) inflight = null }) }
    void request.then((result) => { if (active) setStatus({ ...result, checking: false }) })
    return () => { active = false }
  }, [attempt, enabled])
  const recheck = useCallback(() => setAttempt((n) => n + 1), [])
  return { ...status, recheck }
}
