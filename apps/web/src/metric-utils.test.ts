import { describe, expect, it } from 'vitest'
import { chartRows, convertRate, formatMetric, normalizeSeries, type MetricSeries } from './metric-utils'
import { compareSemver } from './semver'

describe('metric transformations', () => {
  it('converts bytes per second to decimal byte and bit units', () => {
    expect(convertRate(1_000_000, 'MB/s')).toBe(1)
    expect(convertRate(125_000, 'Mb/s')).toBe(1)
    expect(formatMetric(125_000, 'By/s', 'Mb/s')).toBe('1 Mb/s')
    expect(formatMetric(1_000_000, 'By/s', 'MB/s')).toBe('1 MB/s')
  })

  it('normalizes mixed scale lines while preserving their source values in chart rows', () => {
    const inputs: MetricSeries[] = [
      { name: 'cpu.used_percent', unit: '%', labels: { core: 'all' }, points: [{ at: '2026-10-11T12:00:00Z', value: 10 }, { at: '2026-10-11T12:02:00Z', value: 30 }] },
      { name: 'network_receive_bytes_per_second', unit: 'By/s', labels: { interface: 'wan[0].uplink' }, points: [{ at: '2026-10-11T12:00:00Z', value: 1_000_000 }, { at: '2026-10-11T12:02:00Z', value: 2_000_000 }] },
    ]
    const normalized = normalizeSeries(inputs[0]!)
    expect(normalized.points.map((point) => point.value)).toEqual([0, 100])
    const chart = chartRows(inputs, 60, true, 'Mb/s')
    expect(chart.chartSeries.map((item) => item.name)).toEqual(['series_0', 'series_1'])
    expect(chart.chartSeries[1]?.label).toContain('wan[0].uplink')
    expect(chart.data[0]?.series_0).toBe(0)
    expect(chart.data[0]?.actual_1).toBe(1_000_000)
    expect(chart.data).toHaveLength(3)
    expect(chart.data[1]?.series_0).toBeNull()
  })
})

describe('release version comparison', () => {
  it('compares semantic versions including prerelease precedence and build metadata', () => {
    expect(compareSemver('1.2.3-alpha', '1.2.3-beta')).toBe(-1)
    expect(compareSemver('1.2.3-rc.2', '1.2.3-rc.10')).toBe(-1)
    expect(compareSemver('1.2.3+build.1', 'v1.2.3+build.2')).toBe(0)
    expect(compareSemver('dev', '1.2.3')).toBeNull()
    expect(compareSemver('1.2.3-rc.01', '1.2.3')).toBeNull()
    expect(compareSemver('1.2.3-alpha..1', '1.2.3')).toBeNull()
    expect(compareSemver('1.2.3+build..1', '1.2.3')).toBeNull()
  })
})



it('retains hourly source spacing and does not round tiny converted rates to zero', () => {
  const hourly: MetricSeries[] = [{name:'cpu_used_percent',unit:'%',labels:{},points:[
    {at:'2026-10-01T10:00:00Z',value:20,resolutionSeconds:3600},
    {at:'2026-10-01T11:00:00Z',value:30,resolutionSeconds:3600},
  ]}]
  const chart=chartRows(hourly,60,false)
  expect(chart.data).toHaveLength(2)
  expect(formatMetric(1000,'By/s','GB/s')).not.toBe('0 GB/s')
})
