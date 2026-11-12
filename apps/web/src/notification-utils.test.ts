import { describe, expect, it } from 'vitest'
import { channelStatus, errorText, quietSummary, sameSettings } from './notification-utils'

const channel = { id: 'ntfy', kind: 'ntfy', target: 'http://ntfy.local · topic vigara', route: { critical: true, warning: true, resolved: true }, pending: 0, held: 0 }
const rel = () => '5 min ago'

describe('notification settings', () => {
  it('words channel health', () => {
    expect(channelStatus(channel, rel)).toEqual({ tone: 'idle', text: 'Nothing sent yet' })
    expect(channelStatus({ ...channel, lastDeliveredAt: 'x', held: 2 }, rel)).toEqual({ tone: 'ok', text: 'Last delivered 5 min ago · 2 held for quiet hours' })
    expect(channelStatus({ ...channel, lastError: 'rejected', pending: 1 }, rel)).toEqual({ tone: 'crit', text: 'The server refused the message; check the topic and token · 1 waiting to retry' })
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
})
