import { lazy, Suspense, useState } from 'react'
import { Alert, Badge, Card, Checkbox, Group, Stack, Text } from '@mantine/core'
import { StackLoading } from './stack-loading'
import { stackFileChanges, type StackFiles } from './stack-diff-utils'

const ComposeEditor = lazy(() => import('./compose-editor'))
const noop = () => {}

export function StackDiff({ stackId, saved, draft, revision }: { stackId: string; saved: StackFiles; draft: StackFiles; revision: string }) {
  const [showUnchanged, setShowUnchanged] = useState(false)
  const entries = stackFileChanges(saved, draft)
  const changed = entries.filter((entry) => entry.status !== 'Unchanged')
  const visible = showUnchanged ? entries : changed
  return <Stack gap="lg">
    <Group justify="space-between"><div><Text fw={600}>Review draft changes</Text><Text size="sm" c="dimmed">{changed.length} of {entries.length} files changed · Comparing with {revision || 'an empty stack'}</Text></div><Checkbox label="Show unchanged files" checked={showUnchanged} onChange={(event) => setShowUnchanged(event.currentTarget.checked)} /></Group>
    {!changed.length && <Alert color="gray">Your draft matches the saved version.</Alert>}
    {visible.map((entry) => <Card component="section" key={entry.path} withBorder className="stack-diff-file">
      <Group justify="space-between" mb="sm"><Text component="h3" size="sm" fw={600} m={0} className="stack-diff-path">{entry.path}</Text><Badge color={entry.status === 'Added' ? 'green' : entry.status === 'Removed' ? 'red' : entry.status === 'Changed' ? 'orange' : 'gray'}>{entry.status}</Badge></Group>
      {entry.encrypted ? <Text size="sm" c="dimmed">Encrypted environment file · contents hidden. Use the Environment tab to review values.</Text> : <Suspense fallback={<StackLoading editor />}>
        <ComposeEditor stackId={stackId} path={entry.path} value={entry.value} original={entry.original} compare composeFile={false} disabled review onChange={noop} onSave={noop} />
      </Suspense>}
    </Card>)}
  </Stack>
}
