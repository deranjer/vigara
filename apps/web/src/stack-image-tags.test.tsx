import { MantineProvider } from '@mantine/core'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchImageTags, StackImageTags } from './stack-image-tags'

afterEach(() => vi.unstubAllGlobals())

describe('stack image tag browser', () => {
  it('looks up tags on the selected stack and forwards the pagination cursor', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ repository: 'registry:5000/team/web', tags: ['latest', 'v2'], next: 'v2' })))
    vi.stubGlobal('fetch', fetch)
    const tags = await fetchImageTags('my-stack', 'registry:5000/team/web:latest', 'v1')
    expect(tags.next).toBe('v2')
    expect(fetch).toHaveBeenCalledWith('/api/stacks/my-stack/image-tags', expect.objectContaining({ method: 'POST', credentials: 'same-origin', body: JSON.stringify({ image: 'registry:5000/team/web:latest', last: 'v1' }) }))
  })
  it('surfaces lookup failures so a manual tag remains available', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('Device is offline.', { status: 409 })))
    await expect(fetchImageTags('my-stack', 'nginx')).rejects.toThrow('Device is offline.')
    const html = renderToStaticMarkup(<MantineProvider><StackImageTags id="my-stack" images={{ web: 'nginx:latest' }} busy={false} onSelect={() => {}} /></MantineProvider>)
    expect(html).toContain('Selected tag')
    expect(html).toContain('Save tag and preview deployment')
    expect(html).toContain('nginx:latest')
  })
})
