import { useEffect, useState } from 'react'
import { Alert, Badge, Button, Divider, Group, Loader, Select, Stack, Text, Textarea, TextInput, Title } from '@mantine/core'

type Files = Record<string, string>
type Placements = { commit: string; files: Files; branch: string; validationError: string }

async function request<T>(method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch('/api/stack-placements', { method, credentials: 'same-origin', headers: body ? { 'Content-Type': 'application/json' } : undefined, body: body ? JSON.stringify(body) : undefined })
  if (!response.ok) throw new Error((await response.text()).trim().slice(0, 500) || 'Could not load profiles.')
  return response.json() as Promise<T>
}
const validID = (value: string) => value.length <= 63 && /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(value)
const same = (a: Files, b: Files) => JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort())

export function StackProfiles({ stacks, onClose }: { stacks: { id: string; name: string; commit?: string }[]; onClose: () => void }) {
  const [saved, setSaved] = useState<Placements | null>(null)
  const [files, setFiles] = useState<Files>({})
  const [selected, setSelected] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [discard, setDiscard] = useState<'close' | 'reload' | null>(null)
  const [profileID, setProfileID] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [dataMount, setDataMount] = useState('')
  const [targetProfile, setTargetProfile] = useState<string | null>(null)
  const [stackID, setStackID] = useState<string | null>(null)
  const [assignmentID, setAssignmentID] = useState('')
  const dirty = saved !== null && !same(saved.files, files)
  const profileIDs = Object.keys(files).flatMap((path) => /^profiles\/([^/]+)\/vigara-profile.yaml$/.exec(path)?.[1] ?? []).sort()
  const assignmentPath = targetProfile && stackID ? `assignments/${targetProfile}/${stackID}.yaml` : ''
  const proposedID = assignmentID || `${stackID ?? ''}-${targetProfile ?? ''}`

  useEffect(() => {
    let active = true
    void request<Placements>().then((value) => { if (active) { setSaved(value); setFiles(value.files); setSelected(Object.keys(value.files).sort()[0] ?? null) } }).catch((cause: Error) => { if (active) setError(cause.message) })
    return () => { active = false }
  }, [])
  useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  function add(path: string, value: string) { setFiles((current) => ({ ...current, [path]: value })); setSelected(path); setNotice('Added to your draft. Save to branch commits all profile and assignment changes together.') }
  async function reload() {
    setBusy(true); setError('')
    try { const value = await request<Placements>(); setSaved(value); setFiles(value.files); setSelected(Object.keys(value.files).sort()[0] ?? null); setNotice('Loaded the latest saved manifests.'); setDiscard(null) }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not reload profiles.') }
    finally { setBusy(false) }
  }
  async function save() {
    if (!saved) return
    setBusy(true); setError(''); setNotice('')
    try {
      const result = await request<{ state: string; commit: string }>('PUT', { commit: saved.commit, files })
      if (result.state === 'pendingPush') { setNotice('Pending sync: Git has not confirmed the push. Keep this editor open and retry Save to branch.'); return }
      setSaved({ ...saved, commit: result.commit, files: { ...files }, validationError: '' })
      setNotice(`Saved ${result.commit.slice(0, 8)} to ${saved.branch}. Assignments remain disabled.`)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not save profiles.') }
    finally { setBusy(false) }
  }

  return <Stack>
    <Text>Profiles describe a server’s role and storage requirements. Assignments connect saved stacks to that role.</Text>
    <Alert color="blue" title="Manifest preparation">You can prepare and save profiles now. Assignments must remain disabled until device binding and assignment deployment are available. Saving does not move stacks or restore their data.</Alert>
    {error && <Alert color="red" title="Profiles could not complete">{error}</Alert>}
    {notice && <Alert>{notice}</Alert>}
    {saved?.validationError && <Alert color="yellow" title="Saved manifests need review">{saved.validationError}. Review the YAML and save a corrected set.</Alert>}
    {!saved && !error && <Loader />}
    {saved && <>
      <Group><Badge variant="light">Branch {saved.branch}</Badge><Text size="sm" c="dimmed">Revision {saved.commit.slice(0, 8)} · {profileIDs.length} profiles</Text>{dirty && <Badge color="yellow">Unsaved changes</Badge>}</Group>
      <Title order={4}>Add a server profile</Title>
      <Group align="end">
        <TextInput label="Profile ID" placeholder="dns-primary" value={profileID} disabled={busy} onChange={(event) => setProfileID(event.currentTarget.value)} />
        <TextInput label="Display name" placeholder="Primary DNS server" value={displayName} disabled={busy} onChange={(event) => setDisplayName(event.currentTarget.value)} />
        <TextInput label="Data disk mount (optional)" placeholder="/mnt/appdata" value={dataMount} disabled={busy} onChange={(event) => setDataMount(event.currentTarget.value)} />
        <Button variant="light" disabled={busy || !validID(profileID) || !displayName.trim() || displayName.length > 120 || files[`profiles/${profileID}/vigara-profile.yaml`] !== undefined} onClick={() => {
          const mount = dataMount.trim()
          add(`profiles/${profileID}/vigara-profile.yaml`, `apiVersion: vigara.io/v1\nkind: ServerProfile\nprofileId: ${profileID}\ndisplayName: ${JSON.stringify(displayName.trim())}\nrequirements:\n${mount ? `  mounts:\n    - hostPath: ${JSON.stringify(mount)}\n      required: true\n      purpose: Application data disk\n      persistent: true\n      requireMountedFilesystem: true\n` : '  mounts: []\n'}  networks: []\n  volumes: []\n  startupDependencies: []\n`)
          setTargetProfile(profileID); setProfileID(''); setDisplayName(''); setDataMount('')
        }}>Add profile</Button>
      </Group>
      <Title order={4}>Assign a saved stack</Title>
      <Group align="end">
        <Select label="Server profile" searchable data={profileIDs} value={targetProfile} disabled={busy} onChange={setTargetProfile} />
        <Select label="Saved stack" searchable data={stacks.filter((item) => item.commit).map((item) => ({ value: item.id, label: `${item.name || item.id} (${item.id})` }))} value={stackID} disabled={busy} onChange={setStackID} />
        <TextInput label="Assignment ID" placeholder={proposedID} value={assignmentID} disabled={busy} onChange={(event) => setAssignmentID(event.currentTarget.value)} />
        <Button variant="light" disabled={busy || !assignmentPath || !validID(proposedID) || files[assignmentPath] !== undefined} onClick={() => {
          add(assignmentPath, `apiVersion: vigara.io/v1\nkind: DeploymentAssignment\nassignmentId: ${proposedID}\nstackId: ${stackID}\nprofileId: ${targetProfile}\nenvironment: production\nownership:\n  mode: exclusive\n  ownershipEpoch: 1\nenabled: false\n`)
          setAssignmentID('')
        }}>Add assignment</Button>
      </Group>
      <Divider />
      {Object.keys(files).length ? <>
        <Select label="Manifest" searchable data={Object.keys(files).sort()} value={selected} disabled={busy} onChange={setSelected} />
        {selected && files[selected] !== undefined && <Textarea label="Manifest YAML" description="Changes to all manifests are validated and saved together. Keep credentials and application data out of these files." value={files[selected]} onChange={(event) => setFiles({ ...files, [selected]: event.currentTarget.value })} disabled={busy} autosize minRows={14} maxRows={28} spellCheck={false} autoComplete="off" styles={{ input: { fontFamily: 'monospace' } }} />}
        <Group justify="space-between"><Button variant="subtle" color="red" disabled={busy || !selected} onClick={() => { if (!selected) return; const next = { ...files }; delete next[selected]; setFiles(next); setSelected(Object.keys(next).sort()[0] ?? null) }}>Remove from draft</Button><Text size="xs" c="dimmed">Remove a profile’s assignments before removing its profile.</Text></Group>
      </> : <Text c="dimmed">No profiles yet. Add a logical server profile above.</Text>}
    </>}
    {discard && <Alert color="yellow" title="Discard unsaved changes?"><Stack gap="xs"><Text size="sm">Your changes are held in this editor until saved to Git.</Text><Group><Button variant="default" onClick={() => setDiscard(null)}>Keep editing</Button><Button color="red" onClick={() => { if (discard === 'close') onClose(); else void reload() }}>Discard and {discard === 'close' ? 'close' : 'reload'}</Button></Group></Stack></Alert>}
    <Group justify="end"><Button variant="default" disabled={busy} onClick={() => { if (dirty) setDiscard('close'); else onClose() }}>Close</Button><Button variant="default" disabled={busy} onClick={() => { if (dirty) setDiscard('reload'); else void reload() }}>Reload saved manifests</Button><Button loading={busy} disabled={!dirty || !saved} onClick={() => void save()}>Save to branch</Button></Group>
  </Stack>
}
