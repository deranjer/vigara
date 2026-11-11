import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { ActionIcon, AppShell, Avatar, Group, Menu, Text, UnstyledButton, useMantineColorScheme } from '@mantine/core'
import { useMediaQuery } from '@mantine/hooks'
import { Spotlight, spotlight, type SpotlightActionGroupData } from '@mantine/spotlight'
import { useAtom } from 'jotai'
import { IconArchive, IconArrowUpCircle, IconBell, IconBox, IconCheck, IconChevronDown, IconDots, IconHome, IconLogout, IconMoon, IconSearch, IconServer, IconSettings, IconStack2, IconSun, IconUser } from '@tabler/icons-react'
import { navSection, route as routeOf, type Route, type Section } from './route'
import { deviceScopeAtom } from './scope'
import '@mantine/spotlight/styles.css'

export type ShellUser = { id: string; username: string; displayName: string; role: string }
type NavKey = Exclude<Section, 'images' | 'networks' | 'volumes'>
type Item = { key: NavKey; label: string; icon: typeof IconHome }

const primary: Item[] = [
  { key: 'overview', label: 'Overview', icon: IconHome },
  { key: 'alerts', label: 'Alerts', icon: IconBell },
  { key: 'devices', label: 'Devices', icon: IconServer },
  { key: 'containers', label: 'Containers', icon: IconBox },
  { key: 'stacks', label: 'Stacks', icon: IconStack2 },
  { key: 'updates', label: 'Updates', icon: IconArrowUpCircle },
  { key: 'backups', label: 'Backups', icon: IconArchive },
]
const settingsItem: Item = { key: 'settings', label: 'Settings', icon: IconSettings }
const phoneTabs: NavKey[] = ['overview', 'containers', 'stacks', 'alerts']

export const sectionLabels: Record<Section, string> = {
  overview: 'Overview', alerts: 'Alerts', devices: 'Devices', containers: 'Containers', images: 'Images', networks: 'Networks', volumes: 'Volumes',
  stacks: 'Stacks', updates: 'Updates', backups: 'Backups', settings: 'Settings',
}

type SummaryDevice = { id: string; name: string; status: string; agentId?: string | null }
type Summary = { devices: SummaryDevice[] | null; firing: number | null; stacks: string[] | null; updates: number | null }
type SearchContainer = { hostId: string; hostName: string; name: string; image: string }

async function read<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin' })
  if (!response.ok) throw new Error(`${path}: HTTP ${response.status}`)
  return await response.json() as T
}

/** Counts for the nav and names for search. Each source fails on its own, leaving its count blank. */
function useSummary(): Summary {
  const [summary, setSummary] = useState<Summary>({ devices: null, firing: null, stacks: null, updates: null })
  useEffect(() => {
    let active = true
    const load = () => {
      void read<{ devices: SummaryDevice[] }>('/api/inventory/devices?limit=100&offset=0').then((v) => { if (active) setSummary((s) => ({ ...s, devices: v.devices })) }).catch(() => undefined)
      void read<{ status: string }[]>('/api/alerts/incidents').then((v) => { if (active) setSummary((s) => ({ ...s, firing: v.filter((i) => i.status === 'firing').length })) }).catch(() => undefined)
      void read<{ stacks: { id: string }[] }>('/api/stacks').then((v) => { if (active) setSummary((s) => ({ ...s, stacks: v.stacks.map((stack) => stack.id) })) }).catch(() => undefined)
      void read<{ items: { ignored: boolean; result: { status: string } }[] }>('/api/image-updates').then((v) => { if (active) setSummary((s) => ({ ...s, updates: v.items.filter((i) => !i.ignored && i.result.status === 'available').length })) }).catch(() => undefined)
    }
    load()
    const timer = window.setInterval(load, 60_000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])
  return summary
}

function counts(summary: Summary): Partial<Record<NavKey, { value: number; hot?: boolean }>> {
  return {
    ...(summary.firing ? { alerts: { value: summary.firing, hot: true } } : {}),
    ...(summary.devices ? { devices: { value: summary.devices.length } } : {}),
    ...(summary.stacks ? { stacks: { value: summary.stacks.length } } : {}),
    ...(summary.updates ? { updates: { value: summary.updates } } : {}),
  }
}

function BrandMark() {
  return <div className="vg-logo"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 4 L10 20 L13 13" stroke="currentColor" strokeWidth="3.4" fill="none" strokeLinecap="round" strokeLinejoin="round" /><path d="M15 9 L20 4" stroke="var(--vg-brand)" strokeWidth="3.4" strokeLinecap="round" /></svg>vigara</div>
}

function NavItem({ item, active, count, onClick }: { item: Item; active: boolean; count?: { value: number; hot?: boolean }; onClick: () => void }) {
  const Icon = item.icon
  return <UnstyledButton className="vg-nav-item" data-active={active || undefined} aria-current={active ? 'page' : undefined} onClick={onClick}>
    <Icon size={17} stroke={1.7} aria-hidden="true" /><span>{item.label}</span>
    {count && <span className="vg-count" data-hot={count.hot || undefined} aria-label={`${count.value} ${count.hot ? 'firing' : 'total'}`}>{count.value}</span>}
  </UnstyledButton>
}

function UserMenu({ user, onNavigate, onLogout, compact }: { user: ShellUser; onNavigate: (route: Route) => void; onLogout: () => void; compact?: boolean }) {
  const { colorScheme, toggleColorScheme } = useMantineColorScheme()
  const name = user.displayName || user.username
  return <Menu position={compact ? 'bottom-end' : 'top-start'} width={220} withinPortal>
    <Menu.Target>
      {compact
        ? <ActionIcon variant="subtle" size="lg" radius="xl" aria-label={`Account menu for ${name}`}><Avatar name={name} color="blue" variant="filled" radius="xl" size={30} /></ActionIcon>
        : <UnstyledButton className="vg-account" aria-label={`Account menu for ${name}`}><Avatar name={name} color="blue" variant="filled" radius="xl" size={30} /><div className="vg-account-text"><b>{name}</b><small>{user.role}</small></div><IconChevronDown size={14} aria-hidden="true" /></UnstyledButton>}
    </Menu.Target>
    <Menu.Dropdown>
      <Menu.Label>{user.username}</Menu.Label>
      <Menu.Item leftSection={<IconUser size={15} />} onClick={() => onNavigate(routeOf('settings', 'account'))}>Account settings</Menu.Item>
      <Menu.Item leftSection={colorScheme === 'dark' ? <IconSun size={15} /> : <IconMoon size={15} />} onClick={() => toggleColorScheme()}>{colorScheme === 'dark' ? 'Light theme' : 'Dark theme'}</Menu.Item>
      <Menu.Divider />
      <Menu.Item color="red" leftSection={<IconLogout size={15} />} onClick={onLogout}>Log out</Menu.Item>
    </Menu.Dropdown>
  </Menu>
}

function DeviceSwitcher({ devices }: { devices: SummaryDevice[] | null }) {
  const [scope, setScope] = useAtom(deviceScopeAtom)
  const agents = (devices ?? []).filter((device) => device.agentId)
  const current = agents.find((device) => device.agentId === scope)
  const tone = current ? (current.status === 'healthy' ? 'ok' : current.status === 'stale' ? 'warn' : 'crit') : 'ok'
  return <Menu position="bottom-end" width={240} withinPortal>
    <Menu.Target>
      <UnstyledButton className="vg-scope" aria-label={`Device scope: ${current?.name ?? 'All devices'}`}>
        <span className="vg-status-dot" data-tone={tone} aria-hidden="true" /><span className="vg-scope-label">{current?.name ?? 'All devices'}</span><IconChevronDown size={14} aria-hidden="true" />
      </UnstyledButton>
    </Menu.Target>
    <Menu.Dropdown>
      <Menu.Label>Show containers and updates for</Menu.Label>
      <Menu.Item rightSection={!scope ? <IconCheck size={14} /> : null} onClick={() => setScope(null)}>All devices</Menu.Item>
      {agents.map((device) => <Menu.Item key={device.id} rightSection={scope === device.agentId ? <IconCheck size={14} /> : null} onClick={() => setScope(device.agentId ?? null)}>{device.name}</Menu.Item>)}
    </Menu.Dropdown>
  </Menu>
}

function Breadcrumbs({ route, summary, onNavigate }: { route: Route; summary: Summary; onNavigate: (route: Route) => void }) {
  const nav = navSection(route.section)
  const crumbs: { label: string; to?: Route }[] = [{ label: sectionLabels[nav], to: routeOf(nav as Section) }]
  if (nav !== route.section) crumbs.push({ label: sectionLabels[route.section], to: routeOf(route.section) })
  if (route.id) {
    const device = summary.devices?.find((d) => d.id === route.id || d.agentId === route.id)
    const label = route.section === 'settings' ? { account: 'Account', server: 'Server', about: 'About' }[route.id] ?? route.id : device?.name ?? route.id
    crumbs.push({ label, to: route.sub ? routeOf(route.section, route.id) : undefined })
  }
  if (route.sub) crumbs.push({ label: route.sub })
  return <nav className="vg-crumbs" aria-label="Breadcrumb">{crumbs.map((crumb, index) => {
    const last = index === crumbs.length - 1
    return <span key={index} className="vg-crumb">{index > 0 && <span className="vg-crumb-sep" aria-hidden="true">/</span>}{last || !crumb.to ? <b aria-current={last ? 'page' : undefined}>{crumb.label}</b> : <a href="#" onClick={(event) => { event.preventDefault(); onNavigate(crumb.to!) }}>{crumb.label}</a>}</span>
  })}</nav>
}

function useSearchActions(summary: Summary, onNavigate: (route: Route) => void, isAdmin: boolean): { actions: SpotlightActionGroupData[]; loadContainers: () => void } {
  const [containers, setContainers] = useState<SearchContainer[] | null>(null)
  const [loadedAt, setLoadedAt] = useState(0)
  const loadContainers = useCallback(() => {
    if (Date.now() - loadedAt < 60_000) return
    setLoadedAt(Date.now())
    void read<{ hosts: { agentId: string; name: string; containers: { name?: string; image?: string }[] }[] }>('/api/containers')
      .then((v) => setContainers(v.hosts.flatMap((host) => host.containers.filter((c) => c.name).map((c) => ({ hostId: host.agentId, hostName: host.name, name: c.name!, image: c.image ?? '' })))))
      .catch(() => setContainers([]))
  }, [loadedAt])
  const actions = useMemo<SpotlightActionGroupData[]>(() => {
    const pages = [...primary, ...(isAdmin ? [settingsItem] : []), { key: 'images', label: 'Images', icon: IconBox }, { key: 'networks', label: 'Networks', icon: IconBox }, { key: 'volumes', label: 'Volumes', icon: IconBox }] as { key: Section; label: string; icon: typeof IconHome }[]
    const groups: SpotlightActionGroupData[] = [{ group: 'Pages', actions: pages.map((page) => ({ id: `page:${page.key}`, label: page.label, leftSection: <page.icon size={16} />, onClick: () => onNavigate(routeOf(page.key)) })) }]
    if (summary.devices?.length) groups.push({ group: 'Devices', actions: summary.devices.map((device) => ({ id: `device:${device.id}`, label: device.name, description: device.status, leftSection: <IconServer size={16} />, onClick: () => onNavigate(routeOf('devices', device.id)) })) })
    if (containers?.length) groups.push({ group: 'Containers', actions: containers.map((c) => ({ id: `container:${c.hostId}/${c.name}`, label: c.name, description: `${c.image} · ${c.hostName}`, keywords: [c.image, c.hostName], leftSection: <IconBox size={16} />, onClick: () => onNavigate(routeOf('containers', c.hostId, c.name)) })) })
    if (summary.stacks?.length) groups.push({ group: 'Stacks', actions: summary.stacks.map((id) => ({ id: `stack:${id}`, label: id, leftSection: <IconStack2 size={16} />, onClick: () => onNavigate(routeOf('stacks', id)) })) })
    return groups
  }, [summary.devices, summary.stacks, containers, onNavigate, isAdmin])
  return { actions, loadContainers }
}

/** The signed-in frame: navy rail, top bar with breadcrumbs, search and device scope, and phone tabs. */
export function AppFrame({ user, route, onNavigate, onLogout, children }: { user: ShellUser; route: Route; onNavigate: (route: Route) => void; onLogout: () => void; children: ReactNode }) {
  const phone = useMediaQuery('(max-width: 47.99em)') ?? false
  const summary = useSummary()
  const count = counts(summary)
  const active = navSection(route.section)
  const isAdmin = user.role === 'admin'
  const { actions, loadContainers } = useSearchActions(summary, onNavigate, isAdmin)
  const go = (key: NavKey) => onNavigate(routeOf(key))
  const moreItems = primary.filter((item) => !phoneTabs.includes(item.key)).concat(isAdmin ? [settingsItem] : [])

  return <AppShell layout="alt" header={{ height: 52 }} navbar={{ width: 232, breakpoint: 'sm', collapsed: { mobile: true } }} footer={phone ? { height: 62 } : undefined} padding="lg">
    <AppShell.Header className="vg-topbar">
      {phone && <BrandMark />}
      {!phone && <Breadcrumbs route={route} summary={summary} onNavigate={onNavigate} />}
      <Group gap="xs" ml="auto" wrap="nowrap">
        {phone
          ? <ActionIcon variant="default" size="lg" aria-label="Search" onClick={() => { loadContainers(); spotlight.open() }}><IconSearch size={17} /></ActionIcon>
          : <UnstyledButton className="vg-search" onClick={() => { loadContainers(); spotlight.open() }} aria-label="Search devices, containers and stacks (Ctrl K)"><IconSearch size={15} aria-hidden="true" /><span>Search devices, containers, stacks…</span><kbd>Ctrl K</kbd></UnstyledButton>}
        <DeviceSwitcher devices={summary.devices} />
        {phone && <UserMenu user={user} onNavigate={onNavigate} onLogout={onLogout} compact />}
      </Group>
    </AppShell.Header>
    <AppShell.Navbar className="vg-rail" aria-label="Main navigation">
      <BrandMark />
      <nav className="vg-nav">{primary.map((item) => <NavItem key={item.key} item={item} active={active === item.key} count={count[item.key]} onClick={() => go(item.key)} />)}
        {isAdmin && <><div className="vg-nav-group">Admin</div><NavItem item={settingsItem} active={active === 'settings'} onClick={() => go('settings')} /></>}
        {!isAdmin && <NavItem item={{ ...settingsItem, label: 'Account' }} active={active === 'settings'} onClick={() => onNavigate(routeOf('settings', 'account'))} />}
      </nav>
      <div className="vg-rail-foot"><UserMenu user={user} onNavigate={onNavigate} onLogout={onLogout} /></div>
    </AppShell.Navbar>
    <AppShell.Main>{children}</AppShell.Main>
    {phone && <AppShell.Footer className="vg-tabbar" component="nav" aria-label="Main navigation">
      {phoneTabs.map((key) => { const item = primary.find((p) => p.key === key)!; const Icon = item.icon; const c = count[key]; return <UnstyledButton key={key} className="vg-tab" data-active={active === key || undefined} aria-current={active === key ? 'page' : undefined} onClick={() => go(key)}><span className="vg-tab-icon"><Icon size={21} stroke={1.7} aria-hidden="true" />{c?.hot && <span className="vg-tab-badge">{c.value}</span>}</span>{item.label}</UnstyledButton> })}
      <Menu position="top-end" width={200} withinPortal>
        <Menu.Target><UnstyledButton className="vg-tab" data-active={moreItems.some((item) => item.key === active) || undefined}><span className="vg-tab-icon"><IconDots size={21} stroke={1.7} aria-hidden="true" /></span>More</UnstyledButton></Menu.Target>
        <Menu.Dropdown>{moreItems.map((item) => <Menu.Item key={item.key} leftSection={<item.icon size={16} />} rightSection={count[item.key] ? <Text size="xs" c="dimmed">{count[item.key]!.value}</Text> : null} onClick={() => go(item.key)}>{item.label}</Menu.Item>)}</Menu.Dropdown>
      </Menu>
    </AppShell.Footer>}
    <Spotlight actions={actions} nothingFound="Nothing matches" highlightQuery limit={40} scrollable maxHeight={420} onSpotlightOpen={loadContainers} searchProps={{ leftSection: <IconSearch size={18} />, placeholder: 'Search devices, containers, stacks and pages…' }} />
  </AppShell>
}

/** The frame for signed-out pages (setup, login, errors): brand bar only. */
export function PublicFrame({ children }: { children: ReactNode }) {
  const { colorScheme, toggleColorScheme } = useMantineColorScheme()
  return <AppShell header={{ height: 52 }} padding="lg">
    <AppShell.Header className="vg-topbar vg-topbar-public"><BrandMark /><ActionIcon ml="auto" variant="subtle" size="lg" aria-label={colorScheme === 'dark' ? 'Use light theme' : 'Use dark theme'} onClick={() => toggleColorScheme()}>{colorScheme === 'dark' ? <IconSun size={18} /> : <IconMoon size={18} />}</ActionIcon></AppShell.Header>
    <AppShell.Main>{children}</AppShell.Main>
  </AppShell>
}
