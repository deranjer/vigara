import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'
import { imageWithTag, setServiceImage } from './stack-image-utils'

describe('stack image tag changes', () => {
  it('replaces tags and digest pins while preserving registry ports and repositories', () => {
    expect(imageWithTag('localhost:5000/team/web:latest@sha256:abc', 'v2')).toBe('localhost:5000/team/web:v2')
    expect(imageWithTag('nginx', 'stable')).toBe('nginx:stable')
    expect(imageWithTag('ghcr.io/team/app@sha256:abc', 'latest')).toBe('ghcr.io/team/app:latest')
    for (const tag of ['../bad', 'a:b', '', 'with space', '-bad']) expect(() => imageWithTag('nginx', tag)).toThrow()
    expect(() => imageWithTag('${REGISTRY}/web:latest', 'stable')).toThrow()
  })
  it('updates the last service override without changing storage or environment inputs', () => {
    const files = {
      'vigara-stack.yaml': 'compose:\n  files: [compose.yaml, override.yaml]\n',
      'compose.yaml': 'services:\n  web:\n    image: nginx:latest\n    volumes: [data:/data]\n',
      'override.yaml': '# keep this comment\nservices:\n  web:\n    environment:\n      MODE: production\n',
      'secret.age': 'encrypted input',
    }
    const result = setServiceImage(files, 'web', 'nginx:stable')
    expect(result['compose.yaml']).toBe(files['compose.yaml'])
    expect(result['secret.age']).toBe(files['secret.age'])
    expect(result['override.yaml']).toContain('# keep this comment')
    expect(parse(result['override.yaml']!).services.web).toEqual({ environment: { MODE: 'production' }, image: 'nginx:stable' })
    expect(files['override.yaml']).not.toContain('image:')
  })
  it('refuses shared anchors and aliases rather than changing another service', () => {
    for (const yaml of [
      'services:\n  web: &shared\n    image: nginx\n  other: *shared\n',
      'services:\n  other: &shared\n    image: nginx\n  web: *shared\n',
      'services:\n  web:\n    image: &image nginx\n  other:\n    image: *image\n',
    ]) expect(() => setServiceImage({ 'vigara-stack.yaml': 'compose:\n  files: [compose.yaml]\n', 'compose.yaml': yaml }, 'web', 'nginx:stable')).toThrow(/anchor or alias/)
  })
  it('rejects missing services and invalid YAML without replacing files', () => {
    expect(() => setServiceImage({ 'vigara-stack.yaml': 'compose:\n  files: [compose.yaml]\n', 'compose.yaml': 'services: {}' }, 'missing', 'nginx:stable')).toThrow(/no longer/)
    expect(() => setServiceImage({ 'vigara-stack.yaml': 'compose:\n  files: [compose.yaml]\n', 'compose.yaml': 'services: [' }, 'web', 'nginx:stable')).toThrow(/Fix Compose/)
  })
})
