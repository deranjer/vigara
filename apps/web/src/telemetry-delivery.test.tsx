import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MantineProvider } from '@mantine/core'
import type { Device } from './devices'
import { deliverySummary, staleReason } from './telemetry-delivery-utils'
import { TelemetryDeliveryChip, TelemetryDeliveryPanel } from './telemetry-delivery'
const now = Date.now()
const fresh = new Date(now - 10_000).toISOString()
const old = new Date(now - 3_600_000).toISOString()
const device = (extra: Partial<Device> = {}): Device => ({ id: 'd', name: 'Docker host', status: 'stale', platform: 'linux', architecture: 'amd64', agentVersion: 'test', capabilities: [], lastSeenAt: fresh, telemetryObservedAt: old, ...extra })
const limited = () => device({ telemetryDelivery: { lastAttemptAt: fresh, lastStatusCode: 429, queueReportedAt: fresh, queuedSnapshots: 51, oldestQueuedAt: old, limitPerMinute: 10 } })
describe('telemetry delivery visibility', () => {
 it('distinguishes fresh check-ins from delayed measurements', () => {
  expect(staleReason(device(), now)).toBe('Monitoring data delayed')
  expect(staleReason(device({ lastSeenAt: old }), now)).toBe('Check-in overdue')
 })
 it('shows rate limiting with the exact reported backlog', () => {
  expect(deliverySummary(limited(), now)).toMatchObject({ title: 'Uploads rate limited', queued: 51, color: 'yellow' })
  const html = renderToStaticMarkup(<MantineProvider><TelemetryDeliveryPanel device={limited()} /><TelemetryDeliveryChip device={limited()} /></MantineProvider>)
  expect(html).toContain('51 snapshots')
  expect(html).toContain('51 queued')
  expect(html).toContain('HTTP 429')
  expect(html).toContain('10 snapshots per minute')
  expect(html).toContain('hours old')
 })
 it('does not label a recent check-in as stale and does not invent old-agent queue counts', () => {
  const html = renderToStaticMarkup(<MantineProvider><TelemetryDeliveryPanel device={device()} /></MantineProvider>)
  expect(html).toContain('Backlog not reported')
  expect(html).not.toContain('0 snapshots')
  expect(deliverySummary(device(), now).queued).toBeNull()
 })
 it('marks stale queue reports as last-known and never leaves them green', () => {
  const d = limited(); d.lastSeenAt = old; d.telemetryDelivery!.queueReportedAt = old
  expect(deliverySummary(d, now)).toMatchObject({ title: 'Delivery status out of date', color: 'gray', queued: null })
  const html = renderToStaticMarkup(<MantineProvider><TelemetryDeliveryPanel device={d} /></MantineProvider>)
  expect(html).toContain('51 snapshots (last known)')
 })
 it('clears throttling after acceptance, reports backlog progress, and then an empty queue', () => {
  const d = limited(); d.telemetryDelivery!.lastStatusCode = 202
  expect(deliverySummary(d, now).title).toBe('Catching up')
  d.telemetryDelivery!.queuedSnapshots = 0; d.telemetryObservedAt = fresh; d.status = 'healthy'
  expect(deliverySummary(d, now)).toMatchObject({ title: 'Monitoring uploads up to date', queued: 0, color: 'green' })
 })
 it('shows rejected uploads and ignores an old rate-limit response', () => {
  const d = limited(); d.telemetryDelivery!.lastStatusCode = 400
  expect(deliverySummary(d, now).title).toBe('Monitoring uploads rejected')
  d.telemetryDelivery!.lastStatusCode = 429; d.telemetryDelivery!.lastAttemptAt = old
  expect(deliverySummary(d, now).title).toBe('Catching up')
 })
})
