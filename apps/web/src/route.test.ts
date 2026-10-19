import { describe, expect, it } from 'vitest'
import { defaultRoute, formatRoute, parseRoute, sameRoute } from './route'

describe('route', () => {
  it('parses sections with and without an id', () => {
    expect(parseRoute('#/devices')).toEqual({ section: 'devices', id: null })
    expect(parseRoute('#/containers')).toEqual({ section: 'containers', id: null })
    expect(parseRoute('#/monitoring/0b1f3c2e-aaaa-4bbb-8ccc-123456789abc')).toEqual({ section: 'monitoring', id: '0b1f3c2e-aaaa-4bbb-8ccc-123456789abc' })
    expect(parseRoute('#backups')).toEqual({ section: 'backups', id: null })
  })

  it('falls back to the default for empty or unknown routes', () => {
    for (const hash of ['', '#', '#/', '#/nowhere', '#/Devices', '#/__proto__']) expect(parseRoute(hash)).toEqual(defaultRoute)
  })

  it('drops ids that are unsafe or malformed but keeps the section', () => {
    expect(parseRoute('#/containers/%3Cscript%3E')).toEqual({ section: 'containers', id: null })
    expect(parseRoute('#/containers/..%2Fadmin')).toEqual({ section: 'containers', id: null })
    expect(parseRoute('#/containers/%E0%A4%A')).toEqual({ section: 'containers', id: null })
    expect(parseRoute(`#/containers/${'a'.repeat(65)}`)).toEqual({ section: 'containers', id: null })
  })

  it('round-trips valid routes and omits invalid ids when formatting', () => {
    for (const route of [{ section: 'devices', id: null }, { section: 'containers', id: 'abc-123' }, { section: 'backups', id: 'a.b_c' }] as const) {
      expect(parseRoute(formatRoute(route))).toEqual(route)
    }
    expect(formatRoute({ section: 'containers', id: '<bad>' })).toBe('#/containers')
    expect(sameRoute({ section: 'devices', id: 'x' }, { section: 'devices', id: 'x' })).toBe(true)
    expect(sameRoute({ section: 'devices', id: 'x' }, { section: 'monitoring', id: 'x' })).toBe(false)
  })
})
