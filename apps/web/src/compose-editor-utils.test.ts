import { describe, expect, it } from 'vitest'
import { isComposeFile } from './compose-editor-utils'

describe('Compose schema association', () => {
  it('recognizes custom compose paths from the stack manifest', () => {
    expect(isComposeFile('deploy/home.yaml', 'compose:\n  files: [deploy/home.yaml]')).toBe(true)
  })
  it('does not apply Compose validation to the stack manifest or app config', () => {
    expect(isComposeFile('vigara-stack.yaml', 'compose:\n  files: [compose.yaml]')).toBe(false)
    expect(isComposeFile('config/app.yaml', '')).toBe(false)
  })
  it('keeps conventional filenames working when the manifest is incomplete', () => {
    for (const path of ['compose.yaml', 'docker-compose.yml', 'deploy/compose.override.yaml']) {
      expect(isComposeFile(path, 'compose: [')).toBe(true)
    }
  })
})
