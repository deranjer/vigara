import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MantineProvider } from '@mantine/core'
import { McpConnectionGuide, type McpStatus } from './mcp-connections'

function render(status?: McpStatus, error?: string) {
  return renderToStaticMarkup(<MantineProvider><McpConnectionGuide status={status} error={error} admin={false} /></MantineProvider>)
}

describe('MCP connection instructions', () => {
  it('shows disabled setup guidance and both clients without a dead connection link', () => {
    const html = render({ enabled: false })
    expect(html).toContain('MCP disabled')
    expect(html).toContain('Ask your server administrator')
    expect(html).toContain('VIGIRA_MCP_URL=')
    expect(html).toContain('Connect ChatGPT')
    expect(html).toContain('Connect Claude')
    expect(html).toContain('dynamic client registration')
    expect(html).toContain('Register automatically')
    expect(html).not.toContain('href="/oauth/connections"')
    expect(html).not.toContain('Copy MCP URL')
  })

  it('uses the configured endpoint and exposes revocation only when enabled', () => {
    const html = render({ enabled: true, serverUrl: 'https://monitor.example.net:8443/mcp' })
    expect(html).toContain('https://monitor.example.net:8443/mcp')
    expect(html).toContain('Copy MCP URL')
    expect(html).toContain('href="/oauth/connections"')
    expect(html).toContain('MCP enabled')
    expect(html).not.toContain('https://vigara.example.com/mcp')
  })

  it('keeps unavailable status distinct from disabled', () => {
    const html = render(undefined, 'Could not check MCP status.')
    expect(html).toContain('Status unavailable')
    expect(html).toContain('Could not check MCP status.')
    expect(html).not.toContain('MCP disabled')
    expect(html).not.toContain('href="/oauth/connections"')
  })
})
