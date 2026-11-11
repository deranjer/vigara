import { describe, expect, it } from 'vitest'
import { trendRefreshMs, trendToHistory, type TrendResponse } from './trend-utils'

const response: TrendResponse = {
  range: '7d',
  windowStart: '2026-11-01T00:00:00.000Z',
  windowEnd: '2026-11-01T04:00:00.000Z',
  stepSeconds: 3600,
  bins: 4,
  series: [
    { name: 'cpu_used_percent', unit: '%', labels: {}, values: [10, null, 30, 40] },
    { name: 'docker_memory_usage_bytes', unit: 'By', labels: null, values: [null, null, null, null] },
  ],
}

describe('trendToHistory', () => {
  it('places each value on its bin start and keeps gaps as nulls', () => {
    const history = trendToHistory(response)
    expect(history.resolutionSeconds).toBe(3600)
    expect(history.series[0]!.points.map(point => point.at)).toEqual([
      '2026-11-01T00:00:00.000Z', '2026-11-01T01:00:00.000Z', '2026-11-01T02:00:00.000Z', '2026-11-01T03:00:00.000Z',
    ])
    expect(history.series[0]!.points.map(point => point.value)).toEqual([10, null, 30, 40])
  })

  it('treats missing labels as an empty label set so cards can match series', () => {
    expect(trendToHistory(response).series[1]!.labels).toEqual({})
  })

  it('never turns a missing value into zero', () => {
    const values = trendToHistory(response).series.flatMap(item => item.points.map(point => point.value))
    expect(values.filter(value => value === 0)).toHaveLength(0)
  })
})

describe('trendRefreshMs', () => {
  it('refreshes long intervals less often than short ones', () => {
    expect(trendRefreshMs('1h')).toBeLessThan(trendRefreshMs('24h'))
    expect(trendRefreshMs('24h')).toBeLessThan(trendRefreshMs('7d'))
    expect(trendRefreshMs('7d')).toBeLessThan(trendRefreshMs('365d'))
  })

  it('falls back to a moderate period for an unknown interval', () => {
    expect(trendRefreshMs('custom')).toBe(300_000)
  })
})
