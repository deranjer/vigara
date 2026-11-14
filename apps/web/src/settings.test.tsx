import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MantineProvider } from '@mantine/core'
import { Settings } from './settings'
import { ScheduledTasks } from './scheduled-tasks'
import { parseRoute, route } from './route'

const render = (role: string, id: string) => renderToStaticMarkup(<MantineProvider><Settings user={{ id: 'u1', username: 'sam', displayName: 'Sam', role }} route={route('settings', id)} onNavigate={() => undefined} onUserChanged={() => undefined} onSessionExpired={() => undefined} /></MantineProvider>)

describe('settings', () => {
  it('hides server sections from people who cannot change them', () => {
    const html = render('operator', 'tasks')
    expect(html).toContain('Account')
    expect(html).toContain('AI connections')
    for (const hidden of ['Users &amp; access', 'Git repository', 'Scheduled tasks', 'Agents &amp; updates']) expect(html).not.toContain(hidden)
    // A section the role cannot see falls back to Account.
    expect(html).toContain('Change password')
  })
  it('lists every section for admins under You and Server', () => {
    const html = render('admin', 'account')
    for (const label of ['You', 'Server', 'Users &amp; access', 'Git repository', 'Stack profiles', 'AI connections', 'Agents &amp; updates', 'Scheduled tasks']) expect(html).toContain(label)
  })
  it('shows scheduled task columns and keeps old admin links working', () => {
    const html = renderToStaticMarkup(<MantineProvider><ScheduledTasks /></MantineProvider>)
    expect(html).toContain('server starts')
    expect(parseRoute('#/admin')).toMatchObject({ section: 'settings', id: 'tasks' })
  })
})
