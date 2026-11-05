"""Exercise Linux Docker installation with a socket fixture and mocked host changes."""
import importlib.util
import os
import pwd
from pathlib import Path
import socket
import stat
import tempfile
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler
from socketserver import UnixStreamServer
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('docker_installer', ROOT / 'apps/api/internal/agent/installers/docker_linux.py')
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class DockerInstallerTests(unittest.TestCase):
    def test_mounted_file_permissions_configure_effective_service_access(self):
        for mode in ('exec', 'deploy'):
            for files in ('disabled', 'read', 'write'):
                with self.subTest(mode=mode, files=files):
                    settings = installer.validate({'mode': mode, 'host': self.host, 'files': files,
                                                   **({'managedRoot': str(self.root)} if mode == 'deploy' else {})})
                    calls, installed = [], {}
                    def run(*args):
                        calls.append(args)
                        if args[:3] == ('sudo', 'install', '-m'):
                            installed[args[-1]] = Path(args[-2]).read_text()
                    with patch.object(installer.Path, 'stat', return_value=SimpleNamespace(st_gid=998, st_mode=stat.S_IFSOCK | 0o660)), \
                         patch.object(installer.grp, 'getgrgid', return_value=SimpleNamespace(gr_name='docker')), \
                         patch.object(installer, 'run', side_effect=run):
                        installer.apply(settings, 'vigara-agent')
                    env = installed['/etc/vigara-agent/docker.env']
                    unit = installed['/etc/systemd/system/vigara-agent.service.d/60-docker.conf']
                    self.assertIn(f'VIGIRA_DOCKER_FILES={files}\n', env)
                    if files != 'disabled':
                        self.assertIn(('sudo', '-u', 'root', 'python3', '-c', installer.PROBE, self.host), calls)
                        self.assertIn('User=root\n', unit)
                        self.assertIn('PrivateTmp=false\n', unit)
                        if files == 'write':
                            self.assertIn('ProtectSystem=false\n', unit)
                            self.assertIn('ProtectHome=false\n', unit)
                        else:
                            self.assertNotIn('ProtectSystem=false', unit)
                            self.assertIn('ProtectHome=read-only\n', unit)
                    else:
                        self.assertNotIn('PrivateTmp=false', unit)
                        self.assertNotIn('ProtectSystem=false', unit)

    def test_mounted_file_reinstall_preserves_or_explicitly_revokes_access(self):
        existing = {'/etc/vigara-agent/docker.env': 'VIGIRA_DOCKER_FILES=write\n'}
        settings = {'mode': 'exec', 'host': self.host}
        with patch.object(installer.Path, 'exists', lambda p: str(p) in existing), \
             patch.object(installer.Path, 'read_text', lambda p: existing[str(p)]):
            self.assertEqual(installer.load_settings({'docker': settings})['files'], 'disabled')
            self.assertEqual(installer.load_settings({'reinstall': True, 'docker': settings})['files'], 'write')
            for files in ('disabled', 'read'):
                self.assertEqual(installer.load_settings({'reinstall': True, 'docker': dict(settings, files=files)})['files'], files)
            for replacement in (dict(settings, mode='manage'), dict(settings, host='http://127.0.0.1:2375')):
                self.assertEqual(installer.load_settings({'reinstall': True, 'docker': replacement})['files'], 'disabled')

    def test_mounted_file_invalid_choices_fail_without_host_changes(self):
        for change in ({'files': 'admin'}, {'files': 'write\nUser=root'}, {'mode': 'read'},
                       {'mode': 'manage'}, {'host': 'http://127.0.0.1:2375'}):
            with self.subTest(change=change), patch.object(installer, 'run') as run:
                with self.assertRaises(ValueError):
                    installer.validate(dict({'mode': 'exec', 'host': self.host, 'files': 'write'}, **change))
                run.assert_not_called()

    def test_reinstall_preserves_selected_owner_and_explicit_owner_overrides(self):
        existing = {'/etc/vigara-agent/docker.env': 'VIGIRA_COMPOSE_OWNER=root\n'}
        bootstrap = {'reinstall': True, 'docker': {'mode': 'deploy', 'host': 'http://127.0.0.1:2375', 'managedRoot': '/srv/stacks'}}
        with patch.object(installer.Path, 'exists', lambda p: str(p) in existing), \
             patch.object(installer.Path, 'read_text', lambda p: existing[str(p)]):
            self.assertEqual(installer.load_settings(bootstrap)['stackOwner'], 'root')
            bootstrap['docker']['stackOwner'] = '0:0'
            self.assertEqual(installer.load_settings(bootstrap)['stackOwner'], '0:0')

    def test_explicit_owner_is_verified_and_persisted_without_recursive_chown(self):
        account = pwd.getpwuid(os.getuid())
        owner = f'{account.pw_uid}:{account.pw_gid}'
        settings = installer.validate({'mode': 'deploy', 'host': self.host, 'managedRoot': str(self.root), 'stackOwner': owner})
        calls, installed = [], {}
        def run(*args):
            calls.append(args)
            if args[:3] == ('sudo', 'install', '-m'):
                installed[args[-1]] = Path(args[-2]).read_text()
        with patch.object(installer, 'run', side_effect=run):
            installer.apply(settings, 'vigara-agent')
        self.assertIn(f'VIGIRA_COMPOSE_OWNER={owner}', installed['/etc/vigara-agent/docker.env'])
        self.assertIn(('sudo', '-u', account.pw_name, 'test', '-x', str(self.root)), calls)
        self.assertFalse(any('chown' in call for call in calls))

    def test_invalid_or_missing_owners_fail_before_host_mutation(self):
        for owner in ('missing-vigara-user-123456', 'root:missing-vigara-group-123456', 'root\nUser=root', 'root;id'):
            with self.subTest(owner=owner), patch.object(installer, 'run') as run:
                with self.assertRaises(ValueError):
                    installer.validate({'mode': 'deploy', 'host': self.host, 'managedRoot': str(self.root), 'stackOwner': owner})
                run.assert_not_called()

    def test_reinstall_preserves_root_from_existing_environment_files(self):
        existing = {'/etc/vigara-agent/compose.env': 'VIGIRA_COMPOSE_MANAGED_ROOT="/home/operator/stacks"\n',
                    '/etc/vigara-agent/docker.env': 'VIGIRA_DOCKER_MODE=deploy\n'}
        bootstrap = {'reinstall': True, 'docker': {'mode': 'deploy', 'host': 'http://127.0.0.1:2375'}}
        with patch.object(installer.Path, 'exists', lambda p: str(p) in existing), \
             patch.object(installer.Path, 'read_text', lambda p: existing[str(p)]):
            self.assertEqual(installer.load_settings(bootstrap)['managedRoot'], '/home/operator/stacks')
            existing['/etc/vigara-agent/docker.env'] += 'VIGIRA_COMPOSE_MANAGED_ROOT="/srv/latest stacks"\n'
            self.assertEqual(installer.load_settings(bootstrap)['managedRoot'], '/srv/latest stacks')
            bootstrap['docker']['managedRoot'] = '/srv/explicit'
            self.assertEqual(installer.load_settings(bootstrap)['managedRoot'], '/srv/explicit')

    def test_reinstall_without_existing_root_uses_fresh_default(self):
        with patch.object(installer.Path, 'exists', return_value=False):
            result = installer.load_settings({'reinstall': True, 'docker': {'mode': 'deploy', 'host': 'http://127.0.0.1:2375'}})
        self.assertEqual(result['managedRoot'], '/var/lib/vigara-agent/compose-stacks')

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.socket = socket.socket(socket.AF_UNIX)
        self.socket.bind(str(self.root / 'docker.sock'))
        self.addCleanup(self.socket.close)
        self.host = 'unix://' + str(self.root / 'docker.sock')

    def test_no_choice_preserves_host(self):
        with patch.object(installer, 'run') as run:
            self.assertIsNone(installer.validate(None))
            installer.apply(None, 'vigara-agent')
            run.assert_not_called()

    def test_invalid_choices_do_not_mutate_host(self):
        for settings in ({'mode': 'admin', 'host': self.host},
                         {'mode': 'read', 'host': 'http://example.com:2375'},
                         {'mode': 'read', 'host': 'unix:///no/such/socket'},
                         {'mode': 'read', 'host': self.host + '\nUser=root'},
                         {'mode': 'deploy', 'host': self.host, 'managedRoot': '/'}):
            with self.subTest(settings=settings), patch.object(installer, 'run') as run:
                with self.assertRaises((ValueError, OSError)):
                    installer.validate(settings)
                run.assert_not_called()

    def test_read_access_installs_environment_and_adds_socket_group(self):
        settings = installer.validate({'mode': 'read', 'host': self.host})
        calls, installed = [], {}
        def run(*args):
            calls.append(args)
            if args[:3] == ('sudo', 'install', '-m'):
                installed[args[-1]] = Path(args[-2]).read_text()
        with patch.object(installer.Path, 'stat', return_value=SimpleNamespace(st_gid=998, st_mode=stat.S_IFSOCK | 0o660)), \
             patch.object(installer.grp, 'getgrgid', return_value=SimpleNamespace(gr_name='docker')), \
             patch.object(installer, 'run', side_effect=run):
            installer.apply(settings, 'vigara-agent')
        self.assertIn(('sudo', 'usermod', '-aG', 'docker', 'vigara-agent'), calls)
        self.assertTrue(any(call[:4] == ('sudo', '-u', 'vigara-agent', 'python3') for call in calls))
        self.assertIn('VIGIRA_DOCKER_MODE=read', installed['/etc/vigara-agent/docker.env'])
        self.assertNotIn('User=root', installed['/etc/systemd/system/vigara-agent.service.d/60-docker.conf'])

    def test_probe_failure_does_not_install_settings(self):
        settings = installer.validate({'mode': 'manage', 'host': 'http://127.0.0.1:2375'})
        with patch.object(installer, 'run', side_effect=OSError('connection refused')) as run:
            with self.assertRaises(OSError): installer.apply(settings, 'vigara-agent')
        self.assertFalse(any(call.args[1:2] == ('install',) for call in run.call_args_list))

    def test_deploy_creates_root_without_chowning_existing_data(self):
        root = self.root / 'managed stacks'
        root.mkdir()
        (root / 'keep').write_text('existing stack')
        settings = installer.validate({'mode': 'deploy', 'host': self.host, 'managedRoot': str(root)})
        calls, installed = [], {}
        def run(*args):
            calls.append(args)
            if args[:3] == ('sudo', 'install', '-m'): installed[args[-1]] = Path(args[-2]).read_text()
        with patch.object(installer, 'run', side_effect=run): installer.apply(settings, 'vigara-agent')
        self.assertIn(('docker', 'compose', 'version'), calls)
        self.assertIn(('sudo', 'mkdir', '-p', '--', str(root)), calls)
        self.assertIn('User=root', installed['/etc/systemd/system/vigara-agent.service.d/60-docker.conf'])
        self.assertIn(f'ReadWritePaths="{root}"', installed['/etc/systemd/system/vigara-agent.service.d/60-docker.conf'])
        self.assertEqual((root / 'keep').read_text(), 'existing stack')
        self.assertFalse(any('chown' in call for call in calls))

    def test_probe_connects_to_a_real_unix_http_socket(self):
        class Ping(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200 if self.path == '/_ping' else 404)
                self.end_headers()
                self.wfile.write(b'OK')
            def log_message(self, *args): pass
        server = UnixStreamServer(str(self.root / 'http.sock'), Ping)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            result = subprocess.run([sys.executable, '-c', installer.PROBE, 'unix://' + str(self.root / 'http.sock')], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == '__main__': unittest.main()
