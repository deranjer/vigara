"""Exercise Linux Docker installation with a socket fixture and mocked host changes."""
import importlib.util
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
