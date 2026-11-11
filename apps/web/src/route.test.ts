import { describe, expect, it } from 'vitest'
import { defaultRoute, formatRoute, isLegacyHash, navSection, parseRoute, route, sameRoute } from './route'

describe('route', () => {
  it('parses sections with up to three further segments', () => {
    expect(parseRoute('#/overview')).toEqual(route('overview'))
    expect(parseRoute('#/alerts')).toEqual(route('alerts'))
    expect(parseRoute('#/devices')).toEqual(route('devices'))
    expect(parseRoute('#/settings/account')).toEqual(route('settings', 'account'))
    expect(parseRoute('#/containers')).toEqual(route('containers'))
    expect(parseRoute('#/containers/host-1/my_app.web-1')).toEqual(route('containers', 'host-1', 'my_app.web-1'))
    expect(parseRoute('#/containers/host-1/web/logs')).toEqual(route('containers', 'host-1', 'web', 'logs'))
    expect(parseRoute('#backups')).toEqual(route('backups'))
    expect(parseRoute('#/images')).toEqual(route('images'))
    expect(parseRoute('#/networks/host-1')).toEqual(route('networks', 'host-1'))
    expect(parseRoute('#/volumes/host-1/ignored')).toEqual(route('volumes', 'host-1', 'ignored'))
  })

  it('lands on Overview for empty or unknown routes', () => {
    expect(defaultRoute).toEqual(route('overview'))
    for (const hash of ['', '#', '#/', '#/nowhere', '#/Devices', '#/__proto__']) expect(parseRoute(hash)).toEqual(defaultRoute)
  })

  it('sends old links to the pages that replaced them', () => {
    expect(parseRoute('#/monitoring')).toEqual(route('overview'))
    expect(parseRoute('#/monitoring/0b1f3c2e-aaaa-4bbb-8ccc-123456789abc')).toEqual(route('devices', '0b1f3c2e-aaaa-4bbb-8ccc-123456789abc'))
    expect(parseRoute('#/account')).toEqual(route('settings', 'account'))
    expect(parseRoute('#/admin')).toEqual(route('settings', 'server'))
    for (const hash of ['#/monitoring', '#/account', '#admin']) expect(isLegacyHash(hash)).toBe(true)
    for (const hash of ['#/overview', '#/containers', '']) expect(isLegacyHash(hash)).toBe(false)
  })

  it('keeps Docker resource pages under the Containers nav item', () => {
    for (const section of ['containers', 'images', 'networks', 'volumes'] as const) expect(navSection(section)).toBe('containers')
    expect(navSection('stacks')).toBe('stacks')
  })

  it('drops unsafe segments and anything that depends on them', () => {
    expect(parseRoute('#/containers/%3Cscript%3E')).toEqual(route('containers'))
    expect(parseRoute('#/containers/..%2Fadmin/web/logs')).toEqual(route('containers'))
    expect(parseRoute('#/containers/%E0%A4%A')).toEqual(route('containers'))
    expect(parseRoute(`#/containers/${'a'.repeat(129)}`)).toEqual(route('containers'))
    expect(parseRoute('#/containers/host/-flag/logs')).toEqual(route('containers', 'host'))
    expect(parseRoute('#/containers/host/web/LOGS')).toEqual(route('containers', 'host', 'web'))
    expect(parseRoute('#/containers/host/web/not-a-tab')).toEqual(route('containers', 'host', 'web'))
    expect(parseRoute('#/monitoring/%3Cx%3E')).toEqual(route('overview'))
  })

  it('round-trips valid routes and omits invalid parts when formatting', () => {
    for (const value of [route('settings', 'server'), route('overview'), route('devices'), route('containers', 'abc-123'), route('backups', 'a.b_c'), route('containers', 'host', 'web'), route('containers', 'host', 'web', 'stats')]) {
      expect(parseRoute(formatRoute(value))).toEqual(value)
    }
    expect(formatRoute(route('containers', '<bad>'))).toBe('#/containers')
    expect(formatRoute(route('containers', null, 'web', 'logs'))).toBe('#/containers')
    expect(formatRoute(route('containers', 'host', '<bad>', 'logs'))).toBe('#/containers/host')
    expect(sameRoute(route('devices', 'x'), route('devices', 'x'))).toBe(true)
    expect(sameRoute(route('devices', 'x'), route('overview', 'x'))).toBe(false)
    expect(sameRoute(route('containers', 'h', 'a'), route('containers', 'h', 'b'))).toBe(false)
  })
})
