import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Group, Select, Stack, Text, TextInput } from '@mantine/core'
import { imageWithTag } from './stack-image-utils'

type Tags = { repository: string; tags: string[]; next?: string }

export async function fetchImageTags(id: string, image: string, last?: string): Promise<Tags> {
  const response = await fetch(`/api/stacks/${encodeURIComponent(id)}/image-tags`, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ image, last }) })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'Could not load image tags.')
  return response.json() as Promise<Tags>
}

export function StackImageTags({ id, images, busy, onSelect }: { id: string; images: Record<string, string>; busy: boolean; onSelect: (service: string, image: string) => void }) {
  const [service, setService] = useState<string | null>(Object.keys(images).sort()[0] ?? null)
  const [tags, setTags] = useState<string[]>([])
  const [next, setNext] = useState<string | undefined>()
  const [tag, setTag] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const generation = useRef(0)
  const image = service ? images[service] : undefined
  useEffect(() => {
    const current = ++generation.current
    setTags([]); setNext(undefined); setTag(''); setError('')
    if (!image) { setLoading(false); return }
    setLoading(true)
    void fetchImageTags(id, image).then((value) => {
      if (current === generation.current) { setTags(value.tags); setNext(value.next) }
    }).catch((cause: Error) => { if (current === generation.current) setError(cause.message) }).finally(() => { if (current === generation.current) setLoading(false) })
    return () => { generation.current++ }
  }, [id, image])
  async function more() {
    if (!image || !next || loading) return
    const current = generation.current
    setLoading(true); setError('')
    try {
      const value = await fetchImageTags(id, image, next)
      if (current === generation.current) { setTags((previous) => [...new Set([...previous, ...value.tags])]); setNext(value.next) }
    } catch (cause) { if (current === generation.current) setError(cause instanceof Error ? cause.message : 'Could not load more tags.') }
    finally { if (current === generation.current) setLoading(false) }
  }
  let selected = ''
  try { if (image && tag) selected = imageWithTag(image, tag) } catch { /* Invalid manual tags disable saving. */ }
  return <Stack>
    <Text size="sm">Choose a tag for a service. Saving records the image in Git, then opens a preview to pull the image and recreate the stack.</Text>
    <Select label="Service" data={Object.keys(images).sort()} value={service} onChange={setService} disabled={busy} />
    {image && <Text size="sm" style={{ overflowWrap: 'anywhere' }}>Current image: {image}</Text>}
    {error && <Alert color="yellow" title="Tag lookup unavailable">{error}</Alert>}
    <Select label="Available image tags" placeholder={loading ? 'Loading tags…' : 'Search loaded tags'} searchable clearable limit={100} data={tags} value={tags.includes(tag) ? tag : null} onChange={(value) => setTag(value ?? '')} disabled={busy || loading || !tags.length} nothingFoundMessage="No matching loaded tags. Load more or enter a tag below." />
    <Group><Text size="xs" c="dimmed">{tags.length} tags loaded{next ? ' · more available' : ''}</Text>{next && <Button size="xs" variant="default" loading={loading} disabled={busy} onClick={() => void more()}>Load more tags</Button>}</Group>
    {!loading && !error && !tags.length && <Text size="sm" c="dimmed">The registry returned no tags.</Text>}
    <TextInput label="Selected tag" description="You can also enter a known tag manually." value={tag} onChange={(event) => setTag(event.currentTarget.value)} disabled={busy} error={tag && !selected ? 'Enter a valid image tag.' : undefined} />
    {selected && <Text size="sm" style={{ overflowWrap: 'anywhere' }}>New image: {selected}</Text>}
    <Button loading={busy} disabled={!service || !selected || loading} onClick={() => { if (service && selected) onSelect(service, selected) }}>Save tag and preview deployment</Button>
  </Stack>
}
