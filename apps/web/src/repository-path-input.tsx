import { Stack, Text, TextInput } from '@mantine/core'

export function RepositoryPathInput({ value, onChange, disabled = false }: { value: string; onChange: (value: string) => void; disabled?: boolean }) {
  return <Stack gap="xs">
    <TextInput label="Repository path on the device" description="A dedicated local folder or network share for this device's backups." value={value} onChange={(e) => onChange(e.currentTarget.value)} disabled={disabled} required />
    {!disabled && <Text size="xs" c="dimmed">Type the full path as the device sees it. Folders on the device cannot be browsed from this page, and nothing is read or uploaded.</Text>}
  </Stack>
}
