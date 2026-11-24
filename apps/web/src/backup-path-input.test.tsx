import { MantineProvider } from '@mantine/core'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { BackupPathInput } from './backup-path-input'

describe('backup path input', () => {
  it('shows individually removable paths, and a separate add action, with no folder picker', () => {
    const html = renderToStaticMarkup(<MantineProvider><BackupPathInput paths={['/home/alex/Documents', '/srv/photos']} onChange={() => {}} /></MantineProvider>)
    expect(html).toContain('aria-label="Backup folders"')
    expect(html).toContain('aria-label="Remove /home/alex/Documents"')
    expect(html).toContain('aria-label="Remove /srv/photos"')
    expect(html).toContain('Add path')
    expect(html).not.toContain('Browse local folders')
    expect(html).not.toContain('webkitdirectory')
    expect(html).not.toContain('<textarea')
    expect(html).toContain('cannot be browsed from this page')
  })
})
