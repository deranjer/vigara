import { MantineProvider } from '@mantine/core'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { PlanImages } from './stack-workspace'

describe('deployment plan images', () => {
  it('distinguishes an image change from recreation and missing inventory', () => {
    const html = renderToStaticMarkup(<MantineProvider><PlanImages recreate plan={{
      projectName: 'media', services: ['app', 'worker', 'db'],
      images: { app: 'app@sha256:new', worker: 'worker@sha256:new', db: 'db@sha256:same' },
      tracking: { app: 'app:stable' },
      imageReview: {
        app: { createdAt: '2026-11-27T05:22:00Z', version: '1.2.3', revision: 'a1b2c3d', containers: [
          { name: 'app-1', state: 'running', currentImage: 'app@sha256:new', change: 'same' },
          { name: 'app-2', state: 'running', currentImage: 'app@sha256:old', change: 'changed' },
        ] },
        worker: { containers: [] },
      },
    }} /></MantineProvider>)
    for (const detail of ['app-1', 'app-2', 'Same image', 'Image will change', 'No existing container', 'Current image unknown', 'will be recreated', 'app:stable', 'Image created']) {
      expect(html).toContain(detail)
    }
    expect(html).toMatch(/Publisher version.*1\.2\.3/)
    expect(html).toMatch(/Source revision.*a1b2c3d/)
    expect(html.match(/Publisher version/g)).toHaveLength(1)
    expect(html.match(/Source revision/g)).toHaveLength(1)
  })
})
