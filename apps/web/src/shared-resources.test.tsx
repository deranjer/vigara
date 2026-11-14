import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MantineProvider } from '@mantine/core'
import { Settings } from './settings'
import { AppFrame } from './app-shell'
import { route } from './route'
import { HistoricalLogSearch, LogValues } from './log-values'
import { SharedResources } from './shared-resources'

const render = (element: React.ReactNode) => renderToStaticMarkup(<MantineProvider>{element}</MantineProvider>)
const settings = (role: string, page: string) => <Settings user={{ id: 'friend', username: 'friend', displayName: 'Friend', role }} route={route('settings', page)} onNavigate={() => {}} onUserChanged={() => {}} onSessionExpired={() => {}} />
describe('shared resource screens', () => {
  it('offers resource access management only in admin settings', () => {
    const admin = render(settings('admin', 'users'))
    expect(admin).toContain('Users &amp; access')
    expect(admin).toContain('Add user')
    expect(render(settings('restricted', 'users'))).not.toContain('Add user')
  })
  it('keeps global connection controls out of restricted account settings', () => {
    const html = render(settings('restricted', 'ai'))
    expect(html).toContain('Change password')
    expect(html).not.toContain('MCP connections')
    expect(html).not.toContain('Connected apps')
    expect(html).not.toContain('AI connections')
    expect(html).not.toContain('Notifications')
  })
  it('keeps global navigation out of the restricted shell', () => {
    const html = render(<AppFrame user={{ id: 'friend', username: 'friend', displayName: 'Friend', role: 'restricted' }} route={route('containers')} onNavigate={() => {}} onLogout={() => {}}><div>Shared dashboard</div></AppFrame>)
    expect(html).toContain('Shared resources')
    expect(html).not.toContain('>Devices<')
    expect(html).not.toContain('>Backups<')
    expect(html).not.toContain('Search devices, containers')
  })
  it('provides retained search and administrator-only capture creation', () => {
    expect(render(<HistoricalLogSearch hostId="host" reference="game" />)).toContain('Search retained logs')
    expect(render(<LogValues hostId="host" reference="game" admin />)).toContain('Add log value')
    expect(render(<LogValues hostId="host" reference="game" />)).not.toContain('Add log value')
    expect(render(<SharedResources />)).toContain('Shared resources')
  })
})
