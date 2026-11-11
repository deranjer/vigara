import { atomWithStorage, createJSONStorage } from 'jotai/utils'

// The device switcher in the top bar. It holds an agent ID, or null for all devices, and pages that
// list things per host (Containers, Updates) filter by it. Kept per browser until server-side
// preferences arrive; a stale ID simply matches nothing until the user picks again.
const storage = createJSONStorage<string | null>(() => {
  try { return window.localStorage } catch { return sessionStorageFallback }
})
const memory = new Map<string, string>()
const sessionStorageFallback = {
  getItem: (key: string) => memory.get(key) ?? null,
  setItem: (key: string, value: string) => { memory.set(key, value) },
  removeItem: (key: string) => { memory.delete(key) },
} as Storage

export const deviceScopeAtom = atomWithStorage<string | null>('vigara.deviceScope', null, storage, { getOnInit: true })
