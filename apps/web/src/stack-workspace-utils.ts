import { stackFileChanges, type StackFiles } from './stack-diff-utils'
import type { StatusTone } from './status'

export type Revision = { commit: string; message: string; at: string }
export type Deployment = { id: string; commit: string; digest: string; status: string; stage: string; message: string; createdAt: string }
export type ActivityItem = { kind: 'revision'; at: string; revision: Revision } | { kind: 'deployment'; at: string; deployment: Deployment }

/** Paths that differ between the saved revision and the editor, for the file list's dots. */
export function changedPaths(saved: StackFiles, draft: StackFiles): Map<string, 'Added' | 'Removed' | 'Changed'> {
  const changes = new Map<string, 'Added' | 'Removed' | 'Changed'>()
  for (const entry of stackFileChanges(saved, draft)) if (entry.status !== 'Unchanged') changes.set(entry.path, entry.status)
  return changes
}

/** "compose.yaml changed", "compose.yaml added", or "3 files changed" for the draft bar. */
export function draftSummary(changes: Map<string, string>): string {
  if (!changes.size) return 'No changes'
  if (changes.size === 1) {
    const [[path, status]] = [...changes]
    return `${path} ${status.toLowerCase()}`
  }
  return `${changes.size} files changed`
}

/** Saved revisions and deployments as one timeline, newest first. */
export function activityItems(history: Revision[], jobs: Deployment[]): ActivityItem[] {
  const items: ActivityItem[] = [
    ...history.map((revision): ActivityItem => ({ kind: 'revision', at: revision.at, revision })),
    ...jobs.map((deployment): ActivityItem => ({ kind: 'deployment', at: deployment.createdAt, deployment })),
  ]
  return items.sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime())
}

const deploymentWords: Record<string, string> = { succeeded: 'Deployed', failed: 'Deploy failed', queued: 'Deploy queued', running: 'Deploying', unknown: 'Deploy result unknown' }
export function deploymentTone(status: string): StatusTone {
  return status === 'succeeded' ? 'ok' : status === 'failed' ? 'crit' : status === 'unknown' ? 'warn' : 'info'
}
export function deploymentTitle(job: Deployment): string {
  return `${deploymentWords[job.status] ?? job.status} ${job.commit.slice(0, 7)}`
}

/** The tag part of an image reference, for the narrow services rail: "postgres:17-alpine" → "17-alpine". */
export function imageTag(image: string): string {
  const withoutDigest = image.split('@')[0]
  const slash = withoutDigest.lastIndexOf('/')
  const colon = withoutDigest.lastIndexOf(':')
  return colon > slash ? withoutDigest.slice(colon + 1) : 'latest'
}

export type ReviewStep = 'changes' | 'plan' | 'deploy'
/** Where the review sheet opens: at the changes when there is something to commit, otherwise at the plan. */
export function firstReviewStep(dirty: boolean): ReviewStep {
  return dirty ? 'changes' : 'plan'
}
