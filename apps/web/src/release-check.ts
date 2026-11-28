import { useEffect, useState } from 'react'
import { compareSemver } from './semver'

export type ReleaseStatus = { version: string; revision?: string; text: string; latest?: string; updateAvailable: boolean; checking: boolean }

type Result = Omit<ReleaseStatus, 'checking'>
export type GitHubRelease = { tag_name?: string; draft?: boolean; prerelease?: boolean }

/** The newest published server release; app releases and drafts are skipped. */
export function newestServerRelease(releases: GitHubRelease[]): string | null {
  const tags = releases.filter((r) => !r.draft && !r.prerelease && r.tag_name && compareSemver(r.tag_name, r.tag_name) !== null).map((r) => r.tag_name!)
  return tags.sort((a, b) => compareSemver(b, a) ?? 0)[0] ?? null
}
async function check(): Promise<Result> {
  const response = await fetch('/api/version')
  if (!response.ok) throw new Error('Server version unavailable')
  const server = await response.json() as { version: string; revision?: string; staging?: boolean; releases?: GitHubRelease[]; stagingUpdate?: string; checkedAt?: string; failed?: boolean }
  const version = `Vigara ${server.version}`
  const staging = server.staging ?? server.version === 'staging'
  const latest = server.stagingUpdate?.slice(0, 8) || newestServerRelease(server.releases ?? []) || undefined
  const comparison = latest && !staging ? compareSemver(latest, server.version) : null
  const updateAvailable = !!server.stagingUpdate || (comparison ?? 0) > 0
  const text = server.failed ? 'Last release check failed' : !server.checkedAt ? 'Awaiting release check' : updateAvailable ? `Update available: ${latest}` : staging || comparison !== null ? 'Up to date' : latest ? `Latest release: ${latest}` : 'No published server release'
  return { version, revision: server.revision, latest, updateAvailable, text }
}

export function useReleaseStatus() {
  const [status, setStatus] = useState<ReleaseStatus>({ version: 'Vigara', text: 'Loading version', updateAvailable: false, checking: true })
  useEffect(() => {
    let active = true
    const load = () => check().then(result => { if (active) setStatus({ ...result, checking: false }) })
      .catch(() => { if (active) setStatus(previous => ({ ...previous, text: 'Server version unavailable', checking: false })) })
    void load()
    const timer = window.setInterval(load, 60000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])
  return status
}
