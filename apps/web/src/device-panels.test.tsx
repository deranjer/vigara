import { MantineProvider } from '@mantine/core'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { DeviceInfo } from './device-panels'
import type { Device } from './device-types'

describe('Device info rendering', () => {
  const renderVM = (reported = false) => {
    const device: Device = { id: 'vm', name: 'Guest', status: 'healthy', platform: 'linux', architecture: 'amd64', agentVersion: 'test', capabilities: [], latest: { host: {
      totalMemoryBytes: 8 * 1024 ** 3, logicalCpus: 4,
      hardwareInventory: { environment: { kind: 'virtual-machine', hypervisor: 'QEMU/KVM' }, system: { manufacturer: 'QEMU', model: 'Standard PC' }, firmware: { manufacturer: 'SeaBIOS' }, firmwareStatus: 'collected', gpus: [{ name: 'Passed-through GPU' }], gpuStatus: 'collected',
        ...(reported ? { motherboard: { model: 'Virtual board' }, memoryModules: [{ capacityBytes: 8 * 1024 ** 3 }], boardStatus: 'collected', memoryStatus: 'collected' } : {}) },
    } } }
    return renderToStaticMarkup(<MantineProvider><DeviceInfo device={device} onReinstall={() => {}} /></MantineProvider>)
  }

  it('hides empty physical inventory for confirmed guests and labels guest resources', () => {
    const html = renderVM()
    expect(html).toContain('Virtual machine · QEMU/KVM')
    expect(html).toContain('Virtual CPUs')
    expect(html).toContain('Guest-visible memory')
    expect(html).toContain('Standard PC')
    expect(html).toContain('motherboard and DIMM details aren’t exposed')
    expect(html).not.toContain('>Motherboard<')
    expect(html).not.toContain('>Memory modules<')
    expect(html).not.toContain('Proxmox')
    expect(html).toContain('SeaBIOS')
    expect(html).toContain('Passed-through GPU')
  })

  it('preserves reported virtual board and module entries', () => {
    const html = renderVM(true)
    expect(html).toContain('Motherboard (guest-reported)')
    expect(html).toContain('Memory modules (guest-reported)')
    expect(html).toContain('Virtual board')
    expect(html).not.toContain('details aren’t exposed')
  })

  it('renders accessible per-drive bars, an OS logo and reported hardware', () => {
    const device: Device = { id: 'test', name: 'Workstation', status: 'healthy', platform: 'windows', architecture: 'amd64', agentVersion: 'test', capabilities: [], latest: {
      metrics: [{ name: 'filesystem_total_bytes', value: 1000, unit: 'By', labels: { mount_path: 'C:' } }, { name: 'filesystem_free_bytes', value: 250, unit: 'By', labels: { mount_path: 'C:' } }],
      host: { hardwareInventory: { gpuStatus: 'collected', gpus: [{ name: 'Example GPU', version: '1.2.3' }], memoryStatus: 'unsupported' } },
    } }
    const html = renderToStaticMarkup(<MantineProvider><DeviceInfo device={device} onReinstall={() => {}} /></MantineProvider>)
    expect(html).toContain('aria-label="Windows"')
    expect(html).toContain('aria-label="C: used space"')
    expect(html).toContain('aria-valuenow="75"')
    expect(html).toContain('aria-label="C: free space"')
    expect(html).toContain('Example GPU')
    expect(html).toContain('Driver: 1.2.3')
    expect(html).toContain('Unsupported')
  })
})
