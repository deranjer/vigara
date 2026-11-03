import { Loader, Text } from '@mantine/core'

export function StackLoading({ editor = false }: { editor?: boolean }) {
  return <div className={editor ? 'stack-loading stack-loading-editor' : 'stack-loading'} role="status" aria-live="polite"><Loader aria-hidden="true" /><Text size="sm" c="dimmed">{editor ? 'Loading editor…' : 'Loading stacks…'}</Text></div>
}
