import { describe, expect, it } from 'vitest'
import { clampTerminalSize, describeTerminalEnd, isValidExecUser, parseTerminalEnd, resizeMessage, splitInput, terminalBlocker, terminalPath } from './terminal-utils'

describe('terminal-utils', () => {
  it('builds the terminal path with encoded, bounded options', () => {
    expect(terminalPath('host-1', 'web', { shell: 'auto', user: '', cols: 120, rows: 40 })).toBe('/api/containers/host-1/web/exec?cols=120&rows=40')
    expect(terminalPath('host-1', 'web', { shell: 'bash', user: ' www-data ', cols: 5, rows: 9999 })).toBe('/api/containers/host-1/web/exec?shell=bash&user=www-data&cols=10&rows=500')
    expect(terminalPath('h/1', 'my app', { shell: 'sh', user: '1000:1000', cols: Number.NaN, rows: 0 })).toBe('/api/containers/h%2F1/my%20app/exec?shell=sh&user=1000%3A1000&cols=80&rows=24')
  })

  it('validates the run-as user like the server does', () => {
    for (const ok of ['', '  ', ' root ', 'root', '1000', '1000:1000', 'www-data:www.data', 'a_b-c.d']) expect(isValidExecUser(ok)).toBe(true)
    for (const bad of ['root;id', '$(id)', 'a:b:c', 'a b', 'x'.repeat(65), 'ro ot', 'root\nid']) expect(isValidExecUser(bad)).toBe(false)
  })

  it('clamps sizes into the supported range', () => {
    expect(clampTerminalSize(80, 24)).toBe(80)
    expect(clampTerminalSize(3, 24)).toBe(10)
    expect(clampTerminalSize(10_000, 24)).toBe(500)
    expect(clampTerminalSize(0, 24)).toBe(24)
    expect(clampTerminalSize(Number.NaN, 24)).toBe(24)
    expect(clampTerminalSize(80.6, 24)).toBe(81)
    expect(JSON.parse(resizeMessage(132, 43))).toEqual({ type: 'resize', cols: 132, rows: 43 })
    expect(JSON.parse(resizeMessage(1, 1))).toEqual({ type: 'resize', cols: 10, rows: 10 })
  })

  it('splits large input into bounded pieces without losing bytes', () => {
    const data = new Uint8Array(250_000).map((_, index) => index % 251)
    const pieces = splitInput(data, 100_000)
    expect(pieces.map((piece) => piece.length)).toEqual([100_000, 100_000, 50_000])
    const joined = new Uint8Array(250_000)
    let offset = 0
    for (const piece of pieces) { joined.set(piece, offset); offset += piece.length }
    expect(joined).toEqual(data)
    expect(splitInput(new Uint8Array(5), 100)).toHaveLength(1)
    expect(splitInput(new Uint8Array(0), 100)).toHaveLength(1)
  })

  it('recognises the final message and describes why a terminal ended', () => {
    expect(parseTerminalEnd('{"end":true,"code":"exec_exit","message":"The shell exited with status 2."}')).toEqual({ code: 'exec_exit', message: 'The shell exited with status 2.' })
    expect(parseTerminalEnd('{"end":true}')).toEqual({ code: '', message: '' })
    expect(parseTerminalEnd('{"end":false}')).toBeNull()
    expect(parseTerminalEnd('{"type":"resize"}')).toBeNull()
    expect(parseTerminalEnd('not json')).toBeNull()
    expect(describeTerminalEnd(null)).toBe('The terminal session ended.')
    expect(describeTerminalEnd({ code: '', message: '' })).toBe('The shell exited.')
    expect(describeTerminalEnd({ code: 'idle_timeout', message: 'Closed after 15m0s without input.' })).toBe('Closed after 15m0s without input.')
  })

  it('explains why a terminal cannot be opened', () => {
    expect(terminalBlocker('admin', 'exec', true)).toBeNull()
    expect(terminalBlocker('operator', 'deploy', null)).toBeNull()
    expect(terminalBlocker('viewer', 'exec', true)).toContain('view')
    expect(terminalBlocker('admin', 'manage', true)).toContain('VIGIRA_DOCKER_MODE=exec')
    expect(terminalBlocker('admin', 'read', true)).toContain('Terminals are not enabled')
    expect(terminalBlocker('admin', undefined, true)).toContain('Terminals are not enabled')
    expect(terminalBlocker('admin', 'exec', false)).toContain('not running')
  })
})
