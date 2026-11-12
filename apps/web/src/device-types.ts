import type { HardwareInventory } from './device-info-utils'
import type { TelemetryDelivery } from './telemetry-delivery-utils'

// Shapes of GET /api/inventory/devices. Only the fields the web app reads are listed.
export type Metric = { name: string; value: number; unit: string; labels?: Record<string, string> }
export type DockerContainer = { id: string; name?: string; image?: string; state: string }
export type Snapshot = { observedAt?: string; host?: { os?: string; osVersion?: string; architecture?: string; agentVersion?: string; cpuModel?: string; logicalCpus?: number; totalMemoryBytes?: number; uptimeSeconds?: number; hardwareInventory?: HardwareInventory }; metrics?: Metric[]; dockerContainers?: DockerContainer[]; errors?: { capability: string; message: string }[] }
export type DeviceStatus = 'healthy' | 'stale' | 'failed' | 'unsupported'
export type Device = { telemetryDelivery?: TelemetryDelivery | null; id: string; name: string; platformId?: string | null; status: DeviceStatus; agentId?: string | null; platform: string; architecture: string; agentVersion: string; capabilities: string[]; lastSeenAt?: string | null; latest?: Snapshot; telemetryObservedAt?: string | null; collectionErrors?: Snapshot['errors'] }
export type DevicePage = { devices: Device[]; total: number; offset: number; limit: number }

export const statusLabel: Record<DeviceStatus, string> = { healthy: 'Healthy', stale: 'Stale', failed: 'Failed', unsupported: 'Unsupported' }

export async function getJson<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin', signal })
  if (!response.ok) {
    const detail = (await response.text()).slice(0, 300).trim()
    throw new Error(response.status === 401 ? 'Your session expired. Log in again.' : detail || 'Could not load device information.')
  }
  return response.json() as Promise<T>
}

/** Every device, following pages of 100. */
export async function loadAllDevices(): Promise<Device[]> {
  const result: Device[] = []
  for (let offset = 0; ; offset += 100) {
    const page = await getJson<DevicePage>(`/api/inventory/devices?limit=100&offset=${offset}`)
    result.push(...page.devices)
    if (!page.devices.length || result.length >= page.total) break
  }
  return result
}
