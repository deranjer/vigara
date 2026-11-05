"""Apply only explicitly selected Docker settings to the Linux agent service."""
import grp
import pwd
import json
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit
import ipaddress
import shlex


def load_settings(bootstrap):
    settings = bootstrap.get('docker')
    if isinstance(settings, dict) and not settings.get('files'):
        files = 'disabled'
        if (bootstrap.get('reinstall') and settings.get('mode') in ('exec', 'deploy')
                and settings.get('host', '').startswith('unix://')):
            for file in ('/etc/vigira-agent/docker.env', '/etc/vigara-agent/docker.env'):
                path = Path(file)
                if not path.exists():
                    continue
                for line in path.read_text().splitlines():
                    if line.startswith('VIGIRA_DOCKER_FILES='):
                        values = shlex.split(line.split('=', 1)[1], comments=True)
                        if len(values) != 1 or values[0] not in ('disabled', 'read', 'write'):
                            raise ValueError('Invalid existing mounted file access; select it explicitly in the reinstall bundle.')
                        files = values[0]
        settings = dict(settings, files=files)
    if (bootstrap.get('reinstall') and isinstance(settings, dict)
            and settings.get('mode') == 'deploy' and not settings.get('stackOwner')):
        owner = ''
        for file in ('/etc/vigara-agent/compose.env', '/etc/vigara-agent/docker.env'):
            path = Path(file)
            if not path.exists():
                continue
            for line in path.read_text().splitlines():
                if line.startswith('VIGIRA_COMPOSE_OWNER='):
                    values = shlex.split(line.split('=', 1)[1], comments=True)
                    if len(values) != 1:
                        raise ValueError('Invalid existing Compose owner; set it explicitly in the reinstall bundle.')
                    owner = values[0]
        if owner:
            settings = dict(settings, stackOwner=owner)
    if (bootstrap.get('reinstall') and isinstance(settings, dict)
            and settings.get('mode') == 'deploy' and not settings.get('managedRoot')):
        # These are the installer's EnvironmentFiles, in service precedence order.
        root = ''
        for file in ('/etc/vigara-agent/compose.env', '/etc/vigara-agent/docker.env'):
            path = Path(file)
            if not path.exists():
                continue
            for line in path.read_text().splitlines():
                if line.startswith('VIGIRA_COMPOSE_MANAGED_ROOT='):
                    values = shlex.split(line.split('=', 1)[1], comments=True)
                    if len(values) != 1:
                        raise ValueError('Invalid existing managed Compose root; set it explicitly in the reinstall bundle.')
                    root = values[0]
        if root:
            settings = dict(settings, managedRoot=root)
    return validate(settings)


def validate(settings):
    if settings is None:
        return None
    if not isinstance(settings, dict) or settings.get('mode') not in ('read', 'manage', 'exec', 'deploy'):
        raise ValueError('Invalid Docker access level.')
    files = settings.get('files', 'disabled')
    if files not in ('disabled', 'read', 'write'):
        raise ValueError('Invalid mounted file access level.')
    host = settings.get('host', '')
    if files != 'disabled' and (settings['mode'] not in ('exec', 'deploy') or not host.startswith('unix://')):
        raise ValueError('Mounted file access requires a local Docker socket and exec or deploy access.')
    if host.startswith('unix://'):
        path = host[7:]
        if not re.fullmatch(r'/[a-zA-Z0-9_./-]+', path) or '..' in path:
            raise ValueError('Docker requires an absolute Unix socket path.')
        if not stat.S_ISSOCK(Path(path).stat().st_mode):
            raise ValueError('The selected Docker socket does not exist or is not a socket.')
    else:
        url = urlsplit(host)
        if (url.scheme != 'http' or url.username or url.password or url.path or url.query or url.fragment
                or not ipaddress.ip_address(url.hostname).is_loopback or not url.port):
            raise ValueError('Docker HTTP requires a loopback IP and port.')
    if settings['mode'] == 'deploy':
        owner = settings.get('stackOwner', '')
        if owner:
            if not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63})?', owner):
                raise ValueError('Invalid stack owner. Use a local user or UID, optionally followed by :group or :GID.')
            parts = owner.split(':')
            try:
                pwd.getpwuid(int(parts[0])) if parts[0].isdigit() else pwd.getpwnam(parts[0])
                if len(parts) == 2:
                    grp.getgrgid(int(parts[1])) if parts[1].isdigit() else grp.getgrnam(parts[1])
            except (KeyError, OverflowError):
                raise ValueError('The selected stack owner or group does not exist on this device.')
        root = settings.get('managedRoot') or '/var/lib/vigara-agent/compose-stacks'
        if not re.fullmatch(r'/[a-zA-Z0-9_./ -]+', root) or '..' in root or not root.strip('/ '):
            raise ValueError('Invalid managed Compose root.')
        # Do not follow symlinks into an operator-unapproved tree.
        if str(Path(root).resolve()) != root.rstrip('/'):
            raise ValueError('The managed Compose root must be canonical and contain no symlinks.')
        settings = dict(settings, managedRoot=root.rstrip('/'))
    elif settings.get('managedRoot'):
        raise ValueError('A Compose root requires deploy access.')
    elif settings.get('stackOwner'):
        raise ValueError('A stack owner requires deploy access.')
    return settings


PROBE = '''import http.client,socket,sys,urllib.request
host=sys.argv[1]
if host.startswith("unix://"):
 class DockerHTTP(http.client.HTTPConnection):
  def connect(self):
   self.sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
   self.sock.settimeout(10)
   self.sock.connect(host[7:])
 c=DockerHTTP("localhost",timeout=10)
 c.request("GET","/_ping")
 r=c.getresponse()
 assert r.status==200 and r.read().strip()==b"OK", "Docker ping failed"
else:
 opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
 r=opener.open(host+"/_ping",timeout=10)
 assert r.status==200 and r.read().strip()==b"OK", "Docker ping failed"
'''


def run(*args):
    subprocess.run(args, check=True)


def prepare(settings, agent_user):
    if settings is None:
        return
    mode, host = settings['mode'], settings['host']
    service_user = 'root' if mode == 'deploy' or settings.get('files', 'disabled') != 'disabled' else agent_user
    if mode == 'deploy':
        run('docker', 'compose', 'version')
        if settings.get('stackOwner'):
            parts = settings['stackOwner'].split(':')
            account = pwd.getpwuid(int(parts[0])) if parts[0].isdigit() else pwd.getpwnam(parts[0])
            # Keep the agent's private state directory private. A selected owner
            # must already be able to traverse the existing Compose root/parent.
            accessible = Path(settings['managedRoot'])
            while not accessible.exists():
                accessible = accessible.parent
            try:
                run('sudo', '-u', account.pw_name, 'test', '-x', str(accessible))
            except subprocess.CalledProcessError:
                raise ValueError('The stack owner cannot access the Compose directory. Choose an accessible directory, such as /home/USER/stacks; agent state permissions were not changed.')
    if host.startswith('unix://') and service_user != 'root':
        info = Path(host[7:]).stat()
        if info.st_gid == 0 or not info.st_mode & stat.S_IWGRP:
            raise ValueError('Docker socket needs a non-root group with socket write access; its permissions were not changed.')
        group = grp.getgrgid(info.st_gid).gr_name
        run('sudo', 'usermod', '-aG', group, agent_user)
    # Check as the service account, not as the installing administrator.
    run('sudo', '-u', service_user, 'python3', '-c', PROBE, host)


def apply(settings, agent_user):
    if settings is None:
        return
    prepare(settings, agent_user)
    mode, host = settings['mode'], settings['host']
    files = settings.get('files', 'disabled')
    env = f'VIGIRA_DOCKER_ENABLED=true\nVIGIRA_DOCKER_HOST={host}\nVIGIRA_DOCKER_MODE={mode}\nVIGIRA_DOCKER_FILES={files}\n'
    dropin = '[Service]\nEnvironmentFile=/etc/vigara-agent/docker.env\n'
    if mode == 'deploy':
        root = settings['managedRoot']
        run('sudo', 'mkdir', '-p', '--', root)
        env += f'VIGIRA_COMPOSE_MANAGED_ROOT="{root}"\n'
        if settings.get('stackOwner'):
            env += f'VIGIRA_COMPOSE_OWNER={settings["stackOwner"]}\n'
        # Stack file operations need host access, limited to the selected root.
        dropin += f'User=root\nGroup=root\nProtectHome=read-only\nReadWritePaths="{root}"\n'
    if files != 'disabled':
        # Docker resolves mount sources anywhere on the native host. The base
        # unit hides home and temporary directories and makes storage read-only.
        dropin += 'User=root\nGroup=root\nPrivateTmp=false\n'
        dropin += 'ProtectHome=false\nProtectSystem=false\n' if files == 'write' else 'ProtectHome=read-only\n'
    run('sudo', 'install', '-d', '-m', '0755', '/etc/vigara-agent', '/etc/systemd/system/vigara-agent.service.d')
    with tempfile.TemporaryDirectory() as directory:
        envfile, unit = Path(directory) / 'docker.env', Path(directory) / 'docker.conf'
        envfile.write_text(env)
        envfile.chmod(0o600)
        unit.write_text(dropin)
        run('sudo', 'install', '-m', '0600', str(envfile), '/etc/vigara-agent/docker.env')
        run('sudo', 'install', '-m', '0644', str(unit), '/etc/systemd/system/vigara-agent.service.d/60-docker.conf')
    print(f'Docker {mode} access configured and verified for the agent service.')


if __name__ == '__main__':
    try:
        settings = load_settings(json.loads(Path(sys.argv[2]).read_text()))
        if sys.argv[1] == 'apply':
            apply(settings, sys.argv[3])
        elif sys.argv[1] == 'probe':
            prepare(settings, sys.argv[3])
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(f'Docker setup failed: {error}. Correct the endpoint or permissions and rerun the installer.')
