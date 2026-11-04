import { MantineProvider } from '@mantine/core'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { BackupPathInput } from './backup-path-input'

describe('backup path input', () => {
  it('shows individually removable paths, a separate add action, and a directory-only picker', () => {
    const html = renderToStaticMarkup(<MantineProvider><BackupPathInput paths={['/home/alex/Documents', '/srv/photos']} onChange={() => {}} /></MantineProvider>)
    expect(html).toContain('aria-label="Backup folders"')
    expect(html).toContain('aria-label="Remove /home/alex/Documents"')
    expect(html).toContain('aria-label="Remove /srv/photos"')
    expect(html).toContain('Add path')
    expect(html).toContain('Browse local folders')
    expect(html).toContain('webkitdirectory=""')
    expect(html).not.toContain('<textarea')
    expect(html).toContain('Browsers hide full paths')
  })
})
