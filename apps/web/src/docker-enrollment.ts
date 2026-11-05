const capabilities: Record<string, string[]> = {
  read: [],
  manage: ['docker.manage'],
  exec: ['docker.manage', 'docker.exec'],
  deploy: ['docker.manage', 'docker.exec', 'docker.deploy'],
}

export function supportsMountFiles(mode: string | null, target: string | null, host: string) {
  return !!target?.startsWith('linux-') && (mode === 'exec' || mode === 'deploy') && (!host.trim() || host.trim().startsWith('unix://'))
}

export function dockerEnrollment(enabled: boolean, mode: string, target: string | null, host: string, managedRoot: string, stackOwner = '', files = 'preserve') {
  if (!enabled) return { docker: undefined, features: [] as string[] }
  if (!Object.hasOwn(capabilities, mode)) throw new Error('Choose a Docker access level.')
  if (!['preserve', 'disabled', 'read', 'write'].includes(files)) throw new Error('Choose a mounted file access level.')
  if (['read', 'write'].includes(files) && !supportsMountFiles(mode, target, host)) throw new Error('Mounted file access requires Linux, a local Docker socket, and exec or deploy access.')
  if (mode === 'deploy' && !target?.startsWith('linux-')) throw new Error('Stack deployment requires a Linux agent.')
  if (mode === 'deploy' && stackOwner.trim() && !/^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63})?$/.test(stackOwner.trim())) throw new Error('Enter a local stack owner, optionally followed by :group.')
  return {
    docker: { mode, host: host.trim() || (target?.startsWith('linux-') ? 'unix:///var/run/docker.sock' : 'http://127.0.0.1:2375'), managedRoot: mode === 'deploy' ? managedRoot.trim() : undefined, ...(mode === 'deploy' && stackOwner.trim() ? { stackOwner: stackOwner.trim() } : {}), ...(files !== 'preserve' ? { files } : {}) },
    features: [...capabilities[mode]],
  }
}
