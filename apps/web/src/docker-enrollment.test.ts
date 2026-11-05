import { describe, expect, it } from 'vitest'
import { dockerEnrollment, supportsMountFiles } from './docker-enrollment'

describe('Docker enrollment choices', () => {
  it('includes explicit mounted file permissions and omits the preserve choice', () => {
    for (const files of ['disabled', 'read', 'write']) {
      expect(dockerEnrollment(true, 'exec', 'linux-amd64', '', '', '', files).docker?.files).toBe(files)
    }
    expect(dockerEnrollment(true, 'deploy', 'linux-amd64', '', '', '', 'preserve').docker).not.toHaveProperty('files')
  })
  it('rejects mounted file access on unsupported platforms, endpoints and tiers', () => {
    for (const [mode, target, host] of [
      ['read', 'linux-amd64', ''], ['manage', 'linux-amd64', ''],
      ['exec', 'windows-amd64', ''], ['exec', 'linux-amd64', 'http://127.0.0.1:2375'],
    ]) {
      expect(supportsMountFiles(mode, target, host)).toBe(false)
      expect(() => dockerEnrollment(true, mode, target, host, '', '', 'write')).toThrow('Mounted file access')
    }
    expect(() => dockerEnrollment(true, 'exec', 'linux-amd64', '', '', '', 'admin')).toThrow('mounted file access level')
  })
  it('passes an explicit stack owner only for deployment', () => {
    expect(dockerEnrollment(true, 'deploy', 'linux-amd64', '', '/srv/stacks', ' deranjer:docker ').docker?.stackOwner).toBe('deranjer:docker')
    expect(dockerEnrollment(true, 'deploy', 'linux-amd64', '', '', '').docker).not.toHaveProperty('stackOwner')
    expect(dockerEnrollment(true, 'read', 'linux-amd64', '', '', 'deranjer').docker).not.toHaveProperty('stackOwner')
    expect(() => dockerEnrollment(true, 'deploy', 'linux-amd64', '', '', 'root\nUser=root')).toThrow('owner')
  })
  it('leaves an omitted Compose root for the installer to preserve on reinstall', () => {
    expect(dockerEnrollment(true, 'deploy', 'linux-amd64', '', '').docker?.managedRoot).toBe('')
  })
  it('omits host changes and Docker grants unless explicitly selected', () => {
    expect(dockerEnrollment(false, 'deploy', 'linux-amd64', '', '/srv/stacks')).toEqual({ docker: undefined, features: [] })
  })
  it('enables viewing through the default Linux socket without mutation grants', () => {
    expect(dockerEnrollment(true, 'read', 'linux-amd64', '', '')).toEqual({ docker: { mode: 'read', host: 'unix:///var/run/docker.sock', managedRoot: undefined }, features: [] })
  })
  it('matches each access level to its full enrollment ceiling', () => {
    for (const [mode, features] of Object.entries({ manage: ['docker.manage'], exec: ['docker.manage', 'docker.exec'], deploy: ['docker.manage', 'docker.exec', 'docker.deploy'] })) {
      const choice = dockerEnrollment(true, mode, 'linux-arm64', ' unix:///run/docker.sock ', ' /mnt/stacks ')
      expect(choice.features).toEqual(features)
      expect(choice.docker?.host).toBe('unix:///run/docker.sock')
      expect(choice.docker?.managedRoot).toBe(mode === 'deploy' ? '/mnt/stacks' : undefined)
    }
  })
  it('defaults Windows to its supported local HTTP endpoint and rejects stack deployment', () => {
    expect(dockerEnrollment(true, 'read', 'windows-amd64', '', '').docker?.host).toBe('http://127.0.0.1:2375')
    expect(() => dockerEnrollment(true, 'deploy', 'windows-amd64', '', '')).toThrow('Linux')
    expect(() => dockerEnrollment(true, 'root', 'linux-amd64', '', '')).toThrow('access level')
  })
})
