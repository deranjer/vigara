import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { StatusText, toneOf } from './status'

describe('status', () => {
  it('maps Mantine colour names to status tones', () => {
    expect(toneOf('green')).toBe('ok')
    expect(toneOf('teal')).toBe('ok')
    expect(toneOf('yellow')).toBe('warn')
    expect(toneOf('orange')).toBe('warn')
    expect(toneOf('red')).toBe('crit')
    expect(toneOf('blue')).toBe('info')
    expect(toneOf('gray')).toBe('idle')
    expect(toneOf('violet')).toBe('idle')
    expect(toneOf(undefined)).toBe('idle')
  })

  it('renders a hidden dot and the visible word', () => {
    const html = renderToStaticMarkup(<StatusText color="red" title="why">failed</StatusText>)
    expect(html).toContain('data-tone="crit"')
    expect(html).toContain('aria-hidden="true"')
    expect(html).toContain('title="why"')
    expect(html).toContain('>failed<')
  })

  it('prefers an explicit tone over a colour', () => {
    expect(renderToStaticMarkup(<StatusText color="red" tone="ok">done</StatusText>)).toContain('data-tone="ok"')
  })
})
