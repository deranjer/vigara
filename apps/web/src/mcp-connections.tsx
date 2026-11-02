import { useEffect, useState } from 'react'
import { Alert, Anchor, Badge, Button, Card, Code, CopyButton, Group, Stack, Text, Title } from '@mantine/core'

export type McpStatus = { enabled: boolean; serverUrl?: string }

export function McpConnectionGuide({ status, error, admin }: { status?: McpStatus; error?: string; admin: boolean }) {
  const origin = status?.serverUrl?.replace(/\/mcp$/, '') || 'https://vigara.example.com'
  return <Card withBorder radius="md" padding="lg"><Stack gap="md">
    <Group justify="space-between">
      <Title order={3}>Claude and ChatGPT</Title>
      <Badge color={status?.enabled ? 'green' : 'gray'}>{status ? status.enabled ? 'MCP enabled' : 'MCP disabled' : error ? 'Status unavailable' : 'Checking MCP'}</Badge>
    </Group>
    <Text size="sm">Connect an AI assistant to read device health, containers, backup reports and alerts. Sign in with your Vigara account through OAuth; no OpenAI or Anthropic API key is needed.</Text>
    {!status && <Alert color={error ? 'yellow' : 'blue'} role="status">{error || 'Checking MCP configuration…'}</Alert>}
    {status && !status.enabled && <Alert color="blue" title="Enable MCP on your server">
      {admin ? 'Configure the API service below, then recreate its container.' : 'Ask your server administrator to enable MCP. The server configuration steps are below.'}
    </Alert>}
    {status?.enabled && status.serverUrl && <>
      <Text size="sm" fw={600}>Remote MCP server URL</Text>
      <Code block>{status.serverUrl}</Code>
      <CopyButton value={status.serverUrl}>{({ copied, copy }) => <Button variant="light" onClick={copy} style={{ alignSelf: 'flex-start' }}>{copied ? 'Copied' : 'Copy MCP URL'}</Button>}</CopyButton>
      <Text size="xs" c="dimmed">Enabled means configured on this server. Your AI provider must also be able to reach this HTTPS address.</Text>
    </>}
    <details open={status?.enabled === false}><summary>Server setup</summary><Stack gap="sm" mt="sm">
      <Text size="sm">For a new installation, run <Code>bash deploy/setup.sh</Code> and choose MCP when prompted. For an existing installation, keep its database, project name and credentials, and set this in the API server environment:</Text>
      <Code block>{`VIGIRA_MCP_URL=${origin}\nVIGIRA_COOKIE_SECURE=true`}</Code>
      <Text size="sm">Use your public HTTPS origin without /mcp. For Docker, ensure the app service forwards the setting; the current Caddy template includes:</Text>
      <Code block>{'services:\n  app:\n    environment:\n      VIGIRA_MCP_URL: ${VIGIRA_MCP_URL:-}'}</Code>
      <Text size="sm">Edit the existing deployment’s .env and Compose file as needed, then use its compose.sh helper with <Code>up -d --wait app</Code>, or your original Compose command. Reload this page afterward. An empty MCP URL disables MCP.</Text>
      <Text size="sm">Route /mcp, /oauth/* and /.well-known/oauth-* to Vigara, preserving Host. Avoid an extra proxy login on these routes. Private LAN or VPN access on your laptop alone is insufficient for cloud connectors.</Text>
    </Stack></details>
    <div><Title order={4}>Connect ChatGPT (OpenAI)</Title><ol>
      <li>On ChatGPT’s web app, open Plugins and add a custom MCP server. Account or workspace permissions may be required.</li>
      <li>Paste the remote MCP URL above. Choose OAuth and dynamic client registration (DCR); leave the client ID and secret empty for automatic registration.</li>
      <li>Sign in on Vigara’s consent page and approve access. Install the resulting plugin and select it with @ in a chat.</li>
    </ol><Anchor href="https://developers.openai.com/api/docs/guides/custom-mcp-server" target="_blank" rel="noreferrer">Official ChatGPT instructions</Anchor></div>
    <div><Title order={4}>Connect Claude</Title><ol>
      <li>Open Customize → Connectors, then Add → Add custom connector. Organization owners may need to add it first.</li>
      <li>Paste the remote MCP URL above. Choose sign-in with OAuth and Register automatically, rather than Claude’s published identity.</li>
      <li>Connect, sign in with Vigara and approve access. Enable the connector for your conversation.</li>
    </ol><Anchor href="https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp" target="_blank" rel="noreferrer">Official Claude instructions</Anchor></div>
    {!status?.enabled && <Text size="sm" c="dimmed">Enable MCP first to display the configured connection URL. Client menu names and availability can vary.</Text>}
    <Text size="sm">Access is read-only and limited by your account permissions and approved scopes. Try “Use Vigara to list my devices and any failing backups.”</Text>
    {status?.enabled && <Anchor href="/oauth/connections">Manage connected applications</Anchor>}
    <Text size="xs" c="dimmed">Disconnect applications to revoke access. Password changes also revoke their connections.</Text>
  </Stack></Card>
}

export function McpConnections({ admin }: { admin: boolean }) {
  const [status, setStatus] = useState<McpStatus>()
  const [error, setError] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    void fetch('/api/mcp/status', { credentials: 'same-origin', signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Could not check MCP status. Reload this page to try again.')
        const result = await response.json() as McpStatus
        if (typeof result.enabled !== 'boolean' || (result.enabled && typeof result.serverUrl !== 'string')) throw new Error('Could not read MCP connection details. Reload after updating the server.')
        if (!controller.signal.aborted) setStatus(result)
      })
      .catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'MCP status is unavailable.') })
    return () => controller.abort()
  }, [])
  return <McpConnectionGuide status={status} error={error} admin={admin} />
}
