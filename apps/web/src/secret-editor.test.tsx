import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MantineProvider } from '@mantine/core'
import { SecretEditor } from './secret-editor'

describe('secret editor', () => {
  it('starts masked with no plaintext textarea or source contents in the markup', () => {
    const html = renderToStaticMarkup(<MantineProvider><SecretEditor id="demo" files={{ 'secrets/runtime.env.sops': 'source-content-must-stay-hidden' }} busy={false} persistDraft={async () => ({ version: 1, digest: 'sha256:source' })} onBusy={() => {}} onEncrypted={() => {}} /></MantineProvider>)
    expect(html).toContain('hidden')
    expect(html).toContain('Reveal and edit')
    expect(html).toContain('Enter replacement')
    expect(html).not.toContain('<textarea')
    expect(html).not.toContain('source-content-must-stay-hidden')
  })
})
