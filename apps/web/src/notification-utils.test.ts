import { describe, expect, it } from 'vitest'
import { blankForm, channelBody, channelStatus, errorText, formOf, formProblem, quietSummary, sameSettings, type Channel } from './notification-utils'

const channel: Channel = { id: 'ntfy', kind: 'ntfy', name: 'ntfy', source: 'settings', target: 'http://ntfy.local · topic vigara', route: { critical: true, warning: true, resolved: true }, pending: 0, held: 0 }
const rel = () => '5 min ago'

describe('notification settings', () => {
  it('words channel health', () => {
    expect(channelStatus(channel, rel)).toEqual({ tone: 'idle', text: 'Nothing sent yet' })
    expect(channelStatus({ ...channel, lastDeliveredAt: 'x', held: 2 }, rel)).toEqual({ tone: 'ok', text: 'Last delivered 5 min ago · 2 held for quiet hours' })
    expect(channelStatus({ ...channel, lastError: 'rejected', pending: 1 }, rel)).toEqual({ tone: 'crit', text: 'The server refused the message; check the topic and token · 1 waiting to retry' })
    expect(channelStatus({ ...channel, target: 'Cannot send: secrets unreadable' }, rel)).toEqual({ tone: 'crit', text: 'Saved with a different server key; edit it and enter the token again' })
    expect(errorText('some_new_code')).toBe('some new code')
  })
  it('summarises quiet hours and spots unsaved changes', () => {
    const q = { enabled: true, start: '23:00', end: '07:00', timezone: 'Europe/Berlin' }
    expect(quietSummary(q, 'Europe/Berlin')).toBe('23:00 – 07:00, every day')
    expect(quietSummary(q, 'UTC')).toBe('23:00 – 07:00, every day (Europe/Berlin)')
    const a = { quietHours: q, routes: { ntfy: channel.route } }
    expect(sameSettings(a, { ...a })).toBe(true)
    expect(sameSettings(a, { ...a, routes: { ntfy: { ...channel.route, resolved: false } } })).toBe(false)
  })
  it('builds channel requests that keep saved secrets unless changed', () => {
    const form = { ...blankForm('ntfy'), name: ' Phone ', topic: 'alerts' }
    expect(channelBody(form)).toEqual({ kind: 'ntfy', name: 'Phone', url: 'https://ntfy.sh', topic: 'alerts' })
    expect(channelBody({ ...form, secret: 'tk_1', secretTouched: true })).toMatchObject({ token: 'tk_1' })
    // Touched and emptied: the saved token is removed.
    expect(channelBody({ ...form, secretTouched: true })).toMatchObject({ token: '' })
    expect(channelBody({ ...blankForm('discord'), secret: 'https://discord.com/api/webhooks/1/x', secretTouched: true })).toEqual({ kind: 'discord', name: 'Discord', webhookUrl: 'https://discord.com/api/webhooks/1/x' })
    const saved: Channel = { ...channel, config: { url: 'https://ntfy.sh', topic: 'alerts', hasToken: true } }
    expect(formOf(saved)).toMatchObject({ kind: 'ntfy', url: 'https://ntfy.sh', topic: 'alerts', secretTouched: false })
  })
  it('says what is missing before saving', () => {
    expect(formProblem({ ...blankForm('ntfy'), topic: 'alerts' }, null)).toBe('')
    expect(formProblem({ ...blankForm('ntfy'), topic: 'bad topic' }, null)).toMatch(/Topics/)
    expect(formProblem({ ...blankForm('gotify'), url: 'https://gotify.lan' }, null)).toMatch(/token/)
    const gotify: Channel = { ...channel, kind: 'gotify', config: { url: 'https://gotify.lan', hasToken: true } }
    expect(formProblem(formOf(gotify), gotify)).toBe('')
    expect(formProblem(blankForm('discord'), null)).toMatch(/webhook/)
  })
})
