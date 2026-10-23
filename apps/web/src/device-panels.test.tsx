import { MantineProvider } from '@mantine/core'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { DeviceInfo } from './device-panels'
import type { Device } from './devices'

describe('Device info rendering', () => {
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
