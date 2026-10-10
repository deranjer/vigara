import React from 'react'
import ReactDOM from 'react-dom/client'
import { AppShell, Badge, MantineProvider, Text, Title } from '@mantine/core'
import '@mantine/core/styles.css'
import './styles.css'

function App() {
  return <MantineProvider>
    <AppShell header={{ height: 64 }} padding="xl">
      <AppShell.Header className="topbar"><Text fw={700} size="lg">Vigira</Text><Badge color="gray" variant="light">Setup in progress</Badge></AppShell.Header>
      <AppShell.Main><section className="welcome">
        <Text c="dimmed" size="sm" fw={600} tt="uppercase">Home infrastructure</Text>
        <Title order={1} mt="xs">Know what’s healthy. Know what’s safe.</Title>
        <Text c="dimmed" mt="md" maw={600}>Device inventory, monitoring, alerts, and recoverable backups in one place. Your Vigira workspace is ready to be connected to devices.</Text>
        <div className="empty-state"><Text fw={600}>No devices yet</Text><Text c="dimmed" size="sm" mt={6}>When you enroll an agent, it will appear here with its latest health and backup status.</Text></div>
      </section></AppShell.Main>
    </AppShell>
  </MantineProvider>
}

ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><App /></React.StrictMode>)
