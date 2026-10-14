type Version = { major: number; minor: number; patch: number; prerelease: (string | number)[] }

function parse(value: string): Version | null {
  const match = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(value)
  if (!match) return null
  if (match[4]?.split('.').some((part) => !part || /^0\d+$/.test(part))) return null
  const metadata = value.split('+')[1]
  if (metadata?.split('.').some((part) => !part)) return null
  const prerelease = match[4] ? match[4].split('.').map((part) => /^\d+$/.test(part) ? Number(part) : part) : []
  if ([Number(match[1]), Number(match[2]), Number(match[3])].some((part) => !Number.isSafeInteger(part)) || prerelease.some((part) => typeof part === 'number' && !Number.isSafeInteger(part))) return null
  return { major: Number(match[1]), minor: Number(match[2]), patch: Number(match[3]), prerelease }
}

export function compareSemver(left: string, right: string): number | null {
  const a = parse(left); const b = parse(right)
  if (!a || !b) return null
  for (const key of ['major', 'minor', 'patch'] as const) if (a[key] !== b[key]) return a[key] > b[key] ? 1 : -1
  if (!a.prerelease.length || !b.prerelease.length) return a.prerelease.length === b.prerelease.length ? 0 : a.prerelease.length ? -1 : 1
  for (let index = 0; index < Math.max(a.prerelease.length, b.prerelease.length); index++) {
    const av = a.prerelease[index]; const bv = b.prerelease[index]
    if (av === undefined || bv === undefined) return av === undefined ? -1 : 1
    if (av === bv) continue
    if (typeof av === 'number' && typeof bv === 'number') return av > bv ? 1 : -1
    if (typeof av === 'number') return -1
    if (typeof bv === 'number') return 1
    return av > bv ? 1 : -1
  }
  return 0
}
