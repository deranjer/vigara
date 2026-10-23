import { describe, expect, it } from 'vitest'
import { labelKey, monitoringSections, type Measurement } from './monitoring-utils'

const metric = (name: string, labels: Record<string, string> = {}, value = 10): Measurement => ({ name, labels, value, unit: name.includes('percent') ? '%' : name.includes('second') ? 'By/s' : 'By' })

describe('monitoring resource grouping', () => {
  it('combines free and total capacity and keeps matching Windows disk activity adjacent', () => {
    const sections = monitoringSections([
      metric('filesystem_total_bytes', { mount_path: 'c:\\', filesystem: 'NTFS' }, 100),
      metric('filesystem_free_bytes', { filesystem: 'NTFS', mount_path: 'c:\\' }, 25),
      metric('disk_read_bytes_per_second', { device: 'C:' }),
      metric('disk_write_bytes_per_second', { device: 'C:' }),
      metric('filesystem_total_bytes', { mount_path: 'D:\\', filesystem: 'NTFS' }, 200),
    ])
    const storage = sections.find(section => section.id === 'storage')!
    expect(storage.groups).toHaveLength(2)
    const drive = storage.groups[0]!
    expect(drive.title).toBe('C:')
    expect(drive.cards.map(card => card.title)).toEqual(['Disk space', 'Disk activity'])
    expect(drive.cards[0]!.capacity?.free?.value).toBe(25)
    expect(drive.cards[0]!.trends.map(item => item.name)).toEqual(['filesystem_free_bytes'])
    expect(drive.cards[1]!.trends).toHaveLength(2)
    expect(storage.groups[1]!.cards[0]!.capacity?.free).toBeUndefined()
    expect(storage.groups[1]!.cards[0]!.trends).toHaveLength(0)
  })

  it('does not guess a relationship between Linux mounts and physical disks or remote targets', () => {
    const sections = monitoringSections([
      metric('filesystem_free_bytes', { mount_path: '/', filesystem: 'ext4' }),
      metric('disk_read_bytes_per_second', { device: 'sda' }),
      metric('disk_read_bytes_per_second', { device: 'sda', nas_target: 'nas-a' }),
      metric('disk_read_bytes_per_second', { device: 'sda', nas_target: 'nas-b' }),
    ])
    expect(sections[0]!.groups).toHaveLength(4)
  })

  it('pairs traffic per interface and groups memory capacity with an available-space trend', () => {
    const sections = monitoringSections([
      metric('network_receive_bytes_per_second', { interface: 'eth0' }),
      metric('network_send_bytes_per_second', { interface: 'eth0' }),
      metric('network_send_bytes_per_second', { interface: 'eth1' }),
      metric('memory_total_bytes'), metric('memory_available_bytes'), metric('memory_used_percent'),
    ])
    const memory = sections[0]!.groups[0]!.cards[0]!
    expect(memory.metrics).toHaveLength(3)
    expect(memory.trends.map(item => item.name)).toEqual(['memory_available_bytes'])
    const network = sections.find(section => section.id === 'network')!
    expect(network.groups).toHaveLength(2)
    expect(network.groups[0]!.cards[0]!.trends).toHaveLength(2)
  })

  it('keeps container identities separate while grouping their interface variants', () => {
    const sections = monitoringSections([
      metric('docker_memory_usage_bytes', { container_id: 'a' }),
      metric('docker_memory_limit_bytes', { container_id: 'a' }),
      metric('docker_memory_used_percent', { container_id: 'a' }),
      metric('docker_network_receive_bytes_per_second', { container_id: 'a', interface: 'eth0' }),
      metric('docker_network_transmit_bytes_per_second', { interface: 'eth0', container_id: 'a' }),
      metric('docker_memory_usage_bytes', { container_id: 'b' }),
    ], [{ id: 'a', name: 'Web' }])
    const groups = sections[0]!.groups
    const web = groups.find(group => group.title === 'Web')!
    expect(groups).toHaveLength(2)
    expect(web.cards).toHaveLength(2)
    expect(web.cards[0]!.trends.map(item => item.name)).toEqual(['docker_memory_usage_bytes'])
    expect(web.cards[1]!.trends).toHaveLength(2)
  })

  it('retains every measurement including unknown metrics and zero values', () => {
    const inputs = [metric('custom_sensor', { sensor: 'one' }, 0), metric('synology_disk_status', { disk_index: '1', nas_target: 'nas' }), metric('network_check_success', { check_name: 'DNS' })]
    const sections = monitoringSections(inputs)
    expect(sections.flatMap(section => section.groups.flatMap(group => group.cards.flatMap(card => card.metrics)))).toHaveLength(inputs.length)
    expect(labelKey({ b: '2', a: '1' })).toBe(labelKey({ a: '1', b: '2' }))
    expect(monitoringSections([])).toEqual([])
  })
})
