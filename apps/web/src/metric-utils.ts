export type MetricSeries = { name: string; unit: string; labels: Record<string, string>; points: { at: string; value: number | null; resolutionSeconds?: number }[] }
export type RateUnit = 'B/s' | 'kB/s' | 'MB/s' | 'GB/s' | 'kb/s' | 'Mb/s' | 'Gb/s'

export function convertRate(value: number, displayUnit: RateUnit | 'bytes' | 'bits'): number {
  if (displayUnit === 'bits') return value * 8
  if (displayUnit === 'bytes' || displayUnit === 'B/s') return value
  const factor: Record<Exclude<RateUnit, 'B/s'>, number> = { 'kB/s': 1e3, 'MB/s': 1e6, 'GB/s': 1e9, 'kb/s': 1e3, 'Mb/s': 1e6, 'Gb/s': 1e9 }
  return displayUnit.endsWith('b/s') ? (value * 8) / factor[displayUnit] : value / factor[displayUnit]
}

export function formatMetric(value: number, unit: string, rateUnit: RateUnit | 'bytes' | 'bits' = 'bytes'): string {
  if (unit === 'By/s' || unit === 'bytes/s') {
    if (rateUnit !== 'bytes' && rateUnit !== 'bits') return `${new Intl.NumberFormat(undefined, { maximumSignificantDigits: 4 }).format(convertRate(value, rateUnit))} ${rateUnit}`
    const amount = convertRate(value, rateUnit)
    const steps = rateUnit === 'bits' ? ['b/s', 'kb/s', 'Mb/s', 'Gb/s'] : ['B/s', 'kB/s', 'MB/s', 'GB/s']
    let scaled = amount; let index = 0
    while (scaled >= 1000 && index < steps.length - 1) { scaled /= 1000; index++ }
    return `${Number(scaled.toFixed(scaled < 10 ? 1 : 0))} ${steps[index]}`
  }
  if (unit === 'By' || unit === 'bytes') { const steps = ['B', 'kB', 'MB', 'GB', 'TB']; let scaled = value; let index = 0; while (scaled >= 1000 && index < steps.length - 1) { scaled /= 1000; index++ }; return `${Number(scaled.toFixed(scaled < 10 ? 1 : 0))} ${steps[index]}` }
  return `${Number(value.toFixed(2))}${unit ? ` ${unit}` : ''}`
}

export function seriesDisplayName(series: MetricSeries): string {
  const labels = Object.entries(series.labels).map(([key, value]) => `${key}=${value}`).join(', ')
  return `${series.name}${labels ? ` (${labels})` : ''}`
}

export function normalizeSeries(series: MetricSeries): MetricSeries {
  const values = series.points.flatMap((point) => point.value === null ? [] : [point.value])
  const low = Math.min(...values); const high = Math.max(...values)
  return { ...series, points: series.points.map((point) => ({ ...point, value: point.value === null ? null : high === low ? 50 : ((point.value - low) / (high - low)) * 100 })) }
}

export function chartRows(series: MetricSeries[], stepSeconds: number, normalized: boolean, rateUnit: RateUnit | 'bytes' | 'bits' = 'bytes') {
  const converted = series.map((item) => item.unit === 'By/s' || item.unit === 'bytes/s' ? { ...item, points: item.points.map((point) => ({ ...point, value: point.value === null ? null : convertRate(point.value, rateUnit) })) } : item)
  const display = normalized ? converted.map(normalizeSeries) : converted
  const keys = display.map((_item, index) => `series_${index}`)
  const rows = new Map<number, Record<string, number | null>>()
  display.forEach((item, index) => item.points.forEach((point, pointIndex) => {
    const at = new Date(point.at).getTime(); const row = rows.get(at) ?? { timestamp: at }
    row.resolution = Math.max(Number(row.resolution ?? 0), point.resolutionSeconds ?? stepSeconds); row[keys[index]!] = point.value; row[`actual_${index}`] = series[index]!.points[pointIndex]!.value; rows.set(at, row)
  }))
  const sorted = [...rows.entries()].sort(([a], [b]) => a - b)
  for (const [, row] of sorted) for (const key of keys) if (!(key in row)) row[key] = null
  // Insert explicit null points so missing buckets remain visible gaps rather than connected lines.
  const expanded: Record<string, number | null>[] = []
  for (let index = 0; index < sorted.length; index++) {
    const [at, row] = sorted[index]!
    if (index > 0) {
      const previous = sorted[index - 1]![0]
      const gapStep = Math.max(Number(sorted[index - 1]![1].resolution ?? stepSeconds), Number(row.resolution ?? stepSeconds))
      const missing = Math.min(120, Math.floor((at - previous) / (gapStep * 1000)) - 1)
      for (let gap = 1; gap <= missing; gap++) expanded.push({ timestamp: previous + gap * gapStep * 1000, ...Object.fromEntries(keys.flatMap((_key, index) => [[keys[index]!, null], [`actual_${index}`, null]])) })
    }
    expanded.push(row)
  }
  return { data: expanded, chartSeries: display.map((item, index) => ({ name: keys[index]!, label: seriesDisplayName(item), color: ['cyan.7', 'indigo.6', 'teal.7', 'orange.6', 'grape.6', 'pink.6', 'lime.7'][index % 7]!, unit: item.unit })) }
}
