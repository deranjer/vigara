const capabilities: Record<string, string[]> = {
  read: [],
  manage: ['docker.manage'],
  exec: ['docker.manage', 'docker.exec'],
  deploy: ['docker.manage', 'docker.exec', 'docker.deploy'],
}

export function dockerEnrollment(enabled: boolean, mode: string, target: string | null, host: string, managedRoot: string) {
  if (!enabled) return { docker: undefined, features: [] as string[] }
  if (!Object.hasOwn(capabilities, mode)) throw new Error('Choose a Docker access level.')
  if (mode === 'deploy' && !target?.startsWith('linux-')) throw new Error('Stack deployment requires a Linux agent.')
  return {
    docker: { mode, host: host.trim() || (target?.startsWith('linux-') ? 'unix:///var/run/docker.sock' : 'http://127.0.0.1:2375'), managedRoot: mode === 'deploy' ? managedRoot.trim() : undefined },
    features: [...capabilities[mode]],
  }
}
