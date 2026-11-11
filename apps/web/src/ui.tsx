import type { ReactNode } from 'react'
import { Button } from '@mantine/core'
import type { StatusTone } from './status'

/** A bordered panel with an optional header row: title, an optional count, and right-aligned actions. */
export function Panel({ title, count, actions, children, flush }: { title?: ReactNode; count?: number; actions?: ReactNode; children: ReactNode; flush?: boolean }) {
  return <section className="vg-panel">
    {(title || actions) && <header className="vg-panel-h">{title && <h3>{title}</h3>}{count !== undefined && <span className="vg-pill">{count}</span>}{actions && <div className="vg-panel-actions">{actions}</div>}</header>}
    <div className={flush ? undefined : 'vg-panel-body'}>{children}</div>
  </section>
}

/**
 * A small line chart for a trend in a table cell. Gaps (null) break the line rather than
 * dropping to zero. The scale is fixed to 0–max so small wobbles do not look like spikes.
 */
export function Sparkline({ values, max = 100, width = 100, height = 22, label }: { values: (number | null)[]; max?: number; width?: number; height?: number; label: string }) {
  const known = values.filter((v): v is number => v !== null)
  if (known.length < 2) return <span className="vg-spark-empty" style={{ width, height }} aria-label={`${label}: not enough data`} />
  const top = Math.max(max, ...known)
  const step = width / Math.max(1, values.length - 1)
  const y = (v: number) => (height - 2 - (v / top) * (height - 4)).toFixed(1)
  let path = ''
  let pen = false
  values.forEach((v, i) => {
    if (v === null) { pen = false; return }
    path += `${pen ? 'L' : 'M'}${(i * step).toFixed(1)} ${y(v)} `
    pen = true
  })
  return <svg className="vg-spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label}><path d={path.trim()} /></svg>
}

/** A thin horizontal bar for a percentage. It turns amber at 80% and red at 90% by default. */
export function MetricBar({ value, width = 80, warnAt = 80, critAt = 90, label }: { value: number | null; width?: number; warnAt?: number; critAt?: number; label: string }) {
  if (value === null) return <span className="vg-dim">—</span>
  const tone = value >= critAt ? 'crit' : value >= warnAt ? 'warn' : 'ok'
  return <span className="vg-metric"><span className="vg-bar" style={{ width }} role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(value)}><i data-tone={tone} style={{ width: `${Math.min(100, Math.max(0, value))}%` }} /></span><span className="vg-num">{value.toFixed(value < 10 ? 1 : 0)}%</span></span>
}

/** One row in a "needs attention" list: a severity bar, what is wrong, and the next step. */
export function AttentionRow({ tone, title, detail, action, onAction }: { tone: StatusTone; title: ReactNode; detail?: ReactNode; action: string; onAction: () => void }) {
  return <div className="vg-attn">
    <span className="vg-sev" data-tone={tone} aria-hidden="true" />
    <div className="vg-attn-what"><b>{title}</b>{detail && <span>{detail}</span>}</div>
    <Button size="xs" variant={tone === 'crit' || tone === 'warn' ? 'light' : 'default'} onClick={onAction}>{action}</Button>
  </div>
}

/** A labelled figure for a summary strip. */
export function Stat({ label, value, unit, detail, tone }: { label: string; value: ReactNode; unit?: string; detail?: ReactNode; tone?: StatusTone }) {
  return <div className="vg-stat">
    <span className="vg-stat-label">{label}</span>
    <span className="vg-stat-value">{value}{unit && <small> {unit}</small>}</span>
    {detail && <span className="vg-stat-detail">{tone && <span className="vg-status-dot" data-tone={tone} aria-hidden="true" />}{detail}</span>}
  </div>
}
