import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MantineProvider } from '@mantine/core'
import { Admin } from './admin'
import { parseRoute } from './route'

describe('scheduled task administration', () => {
  it('keeps task controls out of non-administrator pages', () => {
    const html = renderToStaticMarkup(<MantineProvider><Admin role="operator" /></MantineProvider>)
    expect(html).toContain('Administrator access required')
    expect(html).not.toContain('Scheduled tasks')
  })
  it('provides the admin tab and durable run/schedule columns', () => {
    const html = renderToStaticMarkup(<MantineProvider><Admin role="admin" /></MantineProvider>)
    expect(html).toContain('Scheduled tasks')
    expect(html).toContain('Last started')
    expect(html).toContain('Last finished')
    expect(html).toContain('Next run')
    expect(html).toContain('server startup')
    expect(parseRoute('#/admin').section).toBe('admin')
  })
})
