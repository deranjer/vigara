import { useRef, useState } from 'react'
import { Alert, Button, Stack } from '@mantine/core'
import { selectedFolderName } from './backup-path-utils'

type PickerWindow = Window & { showDirectoryPicker?: (options: { mode: 'read' }) => Promise<{ name: string }> }

export function LocalFolderBrowse({ onSelect, disabled = false, label }: { onSelect: (name: string) => void; disabled?: boolean; label: string }) {
  const [error, setError] = useState('')
  const [picking, setPicking] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  async function browse() {
    if (disabled || picking) return
    setError('')
    const picker = window as PickerWindow
    if (picker.isSecureContext && picker.showDirectoryPicker) {
      setPicking(true)
      try { const folder = await picker.showDirectoryPicker({ mode: 'read' }); onSelect(folder.name) }
      catch (e) { if (!(e instanceof DOMException && e.name === 'AbortError')) setError('The browser could not open this folder. Enter its full path instead.') }
      finally { setPicking(false) }
    } else if (input.current && 'webkitdirectory' in input.current) {
      input.current.click()
    } else setError('This browser does not support choosing folders. Enter the full path instead.')
  }
  return <Stack gap="xs" align="flex-start">
    <Button type="button" variant="light" aria-label={label} onClick={() => void browse()} disabled={disabled || picking} loading={picking}>Browse local folders</Button>
    <input ref={input} type="file" {...{ webkitdirectory: '', directory: '' }} hidden tabIndex={-1} aria-hidden="true" disabled={disabled} onChange={(e) => {
      const name = selectedFolderName(e.currentTarget.files?.[0]?.webkitRelativePath ?? '')
      if (name) onSelect(name)
      else setError('The browser did not provide a folder name. For empty folders, enter the full path instead.')
      e.currentTarget.value = ''
    }} />
    {error && <Alert color="yellow">{error}</Alert>}
  </Stack>
}
