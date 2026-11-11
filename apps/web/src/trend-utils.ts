import type { MetricSeries } from './metric-utils'

// One response from GET /api/inventory/devices/{id}/trends: every current series
// of the device on a shared time grid, so a whole overview needs a single request.
export type TrendResponse = {
  range: string
  windowStart: string
  windowEnd: string
  stepSeconds: number
  bins: number
  series: { name: string; unit: string; labels: Record<string, string> | null; values: (number | null)[] }[]
}

export type TrendHistory = { series: MetricSeries[]; resolutionSeconds: number; windowStart: string; windowEnd: string }

/** Expands the aligned value arrays into the per-point shape the charts use. */
export function trendToHistory(response: TrendResponse): TrendHistory {
  const start = new Date(response.windowStart).getTime()
  const step = response.stepSeconds
  return {
    windowStart: response.windowStart,
    windowEnd: response.windowEnd,
    resolutionSeconds: step,
    series: response.series.map(item => ({
      name: item.name,
      unit: item.unit,
      labels: item.labels ?? {},
      points: item.values.map((value, index) => ({ at: new Date(start + index * step * 1000).toISOString(), value, resolutionSeconds: step })),
    })),
  }
}

// A trend only changes when a new point lands in its last bin, so long intervals
// need far fewer refreshes than short ones. The server reuses results for a
// fraction of these periods, so several open pages share one computation.
const refreshSeconds: Record<string, number> = { '1h': 30, '6h': 60, '24h': 120, '7d': 300, '30d': 600, '90d': 900, '365d': 1800 }

export function trendRefreshMs(range: string): number {
  return (refreshSeconds[range] ?? 300) * 1000
}
