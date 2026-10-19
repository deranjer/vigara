import { describe, expect, it } from 'vitest'
import { defaultRoute, formatRoute, parseRoute, route, sameRoute } from './route'

describe('route', () => {
  it('parses sections with up to three further segments', () => {
    expect(parseRoute('#/devices')).toEqual(route('devices'))
    expect(parseRoute('#/containers')).toEqual(route('containers'))
    expect(parseRoute('#/monitoring/0b1f3c2e-aaaa-4bbb-8ccc-123456789abc')).toEqual(route('monitoring', '0b1f3c2e-aaaa-4bbb-8ccc-123456789abc'))
    expect(parseRoute('#/containers/host-1/my_app.web-1')).toEqual(route('containers', 'host-1', 'my_app.web-1'))
    expect(parseRoute('#/containers/host-1/web/logs')).toEqual(route('containers', 'host-1', 'web', 'logs'))
    expect(parseRoute('#backups')).toEqual(route('backups'))
  })

  it('falls back to the default for empty or unknown routes', () => {
    for (const hash of ['', '#', '#/', '#/nowhere', '#/Devices', '#/__proto__']) expect(parseRoute(hash)).toEqual(defaultRoute)
  })

  it('drops unsafe segments and anything that depends on them', () => {
    expect(parseRoute('#/containers/%3Cscript%3E')).toEqual(route('containers'))
    expect(parseRoute('#/containers/..%2Fadmin/web/logs')).toEqual(route('containers'))
    expect(parseRoute('#/containers/%E0%A4%A')).toEqual(route('containers'))
    expect(parseRoute(`#/containers/${'a'.repeat(129)}`)).toEqual(route('containers'))
    expect(parseRoute('#/containers/host/-flag/logs')).toEqual(route('containers', 'host'))
    expect(parseRoute('#/containers/host/web/LOGS')).toEqual(route('containers', 'host', 'web'))
    expect(parseRoute('#/containers/host/web/not-a-tab')).toEqual(route('containers', 'host', 'web'))
  })

  it('round-trips valid routes and omits invalid parts when formatting', () => {
    for (const value of [route('devices'), route('containers', 'abc-123'), route('backups', 'a.b_c'), route('containers', 'host', 'web'), route('containers', 'host', 'web', 'stats')]) {
      expect(parseRoute(formatRoute(value))).toEqual(value)
    }
    expect(formatRoute(route('containers', '<bad>'))).toBe('#/containers')
    expect(formatRoute(route('containers', null, 'web', 'logs'))).toBe('#/containers')
    expect(formatRoute(route('containers', 'host', '<bad>', 'logs'))).toBe('#/containers/host')
    expect(sameRoute(route('devices', 'x'), route('devices', 'x'))).toBe(true)
    expect(sameRoute(route('devices', 'x'), route('monitoring', 'x'))).toBe(false)
    expect(sameRoute(route('containers', 'h', 'a'), route('containers', 'h', 'b'))).toBe(false)
  })
})
