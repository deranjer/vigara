"""Exercise the interactive Linux installer with an isolated Docker stub.

No containers or host networks are changed. Run: python3 scripts/test_setup.py
"""
import os
import json
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
DOCKER = r'''#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$CALLS"
case "$*" in
  info) [[ ${FAIL_INFO:-0} != 1 ]];;
  'compose version') echo 'Docker Compose test stub';;
  'compose up --help') echo '--wait-timeout';;
  'ps -aq '*) [[ ${EXISTING:-0} != 1 ]] || echo existing-container;;
  'ps -q '*) [[ ${BUSY_DOCKER_PORT:-0} != 1 ]] || echo existing-port-owner;;
  'volume ls '*) [[ ${EXISTING_VOLUME:-0} != 1 ]] || echo existing-volume;;
  'volume inspect '*) exit 1;;
  'container inspect '*) [[ ${CONFLICT_NAME:-0} == 1 ]];;
  'run '*) [[ ${FAIL_PERMISSIONS:-0} != 1 ]];;
  'network inspect --format '*) echo caddy-proxy;;
  'network inspect '*) [[ ${FAIL_NETWORK:-0} != 1 ]];;
  'inspect --format {{range '*) echo web;;
  'inspect --format '*)
    case "${PROXY_STATE:-running}" in
      running) echo true;;
      stopped) echo false;;
      missing) exit 1;;
    esac;;
  'context inspect '*) echo "${TEST_ENDPOINT:-unix://$TEST_SOCKET}";;
  'network create '*) echo created-network;;
  'start '*) :;;
  compose*)
    case " $* " in
      *' config --quiet '*) [[ ${FAIL_CONFIG:-0} != 1 ]];;
      *' pull '*) [[ ${FAIL_PULL:-0} != 1 ]];;
      *' up '*) [[ ${FAIL_UP:-0} != 1 ]];;
    esac;;
  *) echo "Unexpected Docker call: $*" >&2; exit 1;;
esac
'''


class SetupTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.deploy = self.root / "source" / "deploy"
        self.deploy.mkdir(parents=True)
        self.stacks_root = self.root / 'dockers' / 'compose-stacks'
        self.production = self.stacks_root / 'vigara'
        for name in ("setup.sh", "check-ready.sh", "compose.staging.yml", "compose.caddy.example.yml", "compose.caddy-proxy.example.yml"):
            shutil.copy(ROOT / "deploy" / name, self.deploy / name)
        # Exercise timeout failures immediately rather than waiting two minutes.
        check = self.deploy / 'check-ready.sh'
        check.write_text(check.read_text().replace('SECONDS + 120', 'SECONDS + 0'))
        self.docker_socket = socket.socket(socket.AF_UNIX)
        self.docker_socket.bind(str(self.root / 'docker.sock'))
        self.addCleanup(self.docker_socket.close)
        binary = self.root / "bin"
        binary.mkdir()
        (binary / "docker").write_text(DOCKER)
        (binary / "docker").chmod(0o700)
        # Make the occupied-port check independent of the host running the test.
        (binary / "ss").write_text('#!/usr/bin/env bash\n[[ ${BUSY_PORT:-0} != 1 ]] || echo LISTEN\n')
        (binary / "ss").chmod(0o700)
        (binary / "curl").write_text('''#!/usr/bin/env bash
printf 'curl %s\\n' "$*" >> "$CALLS"
[[ ${FAIL_TLS:-0} != 1 ]] || exit 60
case "$*" in
  *oauth-protected-resource*)
    [[ ${BAD_DISCOVERY:-0} != 1 ]] || { echo '<html>wrong server</html>'; exit 0; }
    echo '{"resource":"https://vigara.example.com/mcp"}';;
  *oauth-authorization-server*) echo '{"issuer":"https://vigara.example.com"}';;
  *http_code*) echo "${MCP_STATUS:-401}";;
  *) echo 'health check passed';;
esac
''')
        (binary / "curl").chmod(0o700)
        self.calls = self.root / "calls"
        self.environment = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"],
                                CALLS=str(self.calls), HOME=str(self.root), TEST_SOCKET=str(self.root / 'docker.sock'))

    def run_setup(self, answers, root_answer='', mcp_answer='n', proxy_answers=None, storage_answer='n', **environment):
        # Fixtures below describe the original project/image/network prompts.
        # Add the shared-root answer and MCP opt-in before the startup answer.
        lines = answers.splitlines()
        if len(lines) >= 6:
            lines.insert(6, mcp_answer)
            if proxy_answers is not None:
                lines.insert(6, proxy_answers)
        if lines:
            lines.insert(1, root_answer)
        if len(lines) >= 3:
            lines.insert(3, storage_answer)
        answers = '\n'.join(lines) + ('\n' if lines else '')
        return subprocess.run(["bash", str(self.deploy / "setup.sh")], input=answers,
                              text=True, capture_output=True, cwd=self.root,
                              env=dict(self.environment, **environment))

    def test_direct_configuration_and_rerun(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        production = self.production
        env = (production / ".env").read_text()
        self.assertIn('VIGARA_PORT=8080', env)
        self.assertIn('VIGARA_COOKIE_SECURE=false', env)
        self.assertRegex(env, r'POSTGRES_PASSWORD=[a-f0-9]{64}\n')
        self.assertRegex(env, r'VIGARA_BACKUP_MASTER_KEY=[A-Za-z0-9+/]{43}=\n')
        self.assertEqual((production / ".env").stat().st_mode & 0o777, 0o600)
        compose = (production / "compose.yml").read_text()
        self.assertIn('env_file: .env', compose)
        self.assertIn('${VIGARA_BIND_ADDRESS:-127.0.0.1}', compose)
        self.assertIn('container_name: vigara-app', compose)
        self.assertIn('container_name: vigara-postgres', compose)
        self.assertIn('postgres-data:/var/lib/postgresql/data', compose)
        self.assertFalse((production / 'data').exists())
        self.assertNotIn(' up -d ', self.calls.read_text())
        self.assertNotIn('pull', self.calls.read_text())
        self.assertNotIn(env.splitlines()[0], result.stdout)
        self.assertIn('Setup already exists', self.run_setup('\n').stderr)
        self.assertEqual((production / ".env").read_text(), env)

    def test_caddy_configuration(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        production = self.production
        self.assertIn('VIGARA_DOMAIN=vigara.example.com', (production / ".env").read_text())
        compose = (production / "compose.yml").read_text()
        self.assertIn('caddy.reverse_proxy:', compose)
        self.assertIn('VIGARA_BACKUP_MASTER_KEY:', compose)
        self.assertEqual(compose.count('      VIGARA_BACKUP_MASTER_KEY:'), 1)
        self.assertNotIn('ports:', compose)
        self.assertIn('VIGARA_MCP_URL=\n', (production / '.env').read_text())

    def test_shared_root_outside_checkout(self):
        existing = self.root / 'shared compose' / 'existing-stack' / 'compose.yml'
        existing.parent.mkdir(parents=True)
        existing.write_text('existing stack stays unchanged\n')
        result = self.run_setup('\n\nn\n\n\nn\nn\n', root_answer='~/shared compose')
        self.assertEqual(result.returncode, 0, result.stderr)
        destination = self.root / 'shared compose' / 'vigara'
        self.assertTrue((destination / 'compose.yml').exists())
        self.assertTrue((destination / 'compose.sh').exists())
        self.assertFalse((self.deploy / 'production').exists())
        self.assertEqual(existing.read_text(), 'existing stack stays unchanged\n')
        self.assertIn('VIGARA_COMPOSE_MANAGED_ROOT=' + str(self.root / 'shared compose').replace(' ', r'\ '), result.stdout)

    def test_reject_checkout_and_relative_roots(self):
        for root in ('relative-path', str(self.deploy / 'runtime')):
            with self.subTest(root=root):
                self.assertNotEqual(self.run_setup('\n', root_answer=root).returncode, 0)

    def test_caddy_mcp_opt_in(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n', mcp_answer='y\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('VIGARA_MCP_URL=https://vigara.example.com\n', (self.production / '.env').read_text())
        self.assertIn('https://vigara.example.com/mcp', result.stdout)

    def test_new_proxy_requires_approval(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                proxy_answers='n', PROXY_STATE='missing')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('deployment declined', result.stderr)
        self.assertFalse(self.production.exists())
        self.assertNotIn('network create', self.calls.read_text())
        self.assertNotIn(' up -d ', self.calls.read_text())

    def test_generate_proxy_without_starting(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                proxy_answers='y\n', mcp_answer='y\n', PROXY_STATE='missing')
        self.assertEqual(result.returncode, 0, result.stderr)
        proxy = self.stacks_root / 'vigara-caddy'
        self.assertTrue((proxy / 'compose.sh').exists())
        self.assertIn('caddy-data:/data', (proxy / 'compose.yml').read_text())
        self.assertIn('VIGARA_CADDY_IMAGE=lucaslorentz/caddy-docker-proxy:alpine', (proxy / '.env').read_text())
        self.assertNotIn('network create web\n', self.calls.read_text())
        self.assertNotIn(' up -d ', self.calls.read_text())
        self.assertIn('check.sh', result.stdout)

    def test_proxy_starts_before_app_and_checks_mcp(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                proxy_answers='y\n', mcp_answer='y\n', PROXY_STATE='missing', FAIL_NETWORK='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls.read_text()
        self.assertIn('network create web', calls)
        proxy_up = calls.index('compose -p vigara-caddy --env-file .env -f compose.yml up')
        app_up = calls.index('compose -p vigara --env-file .env -f compose.yml up')
        self.assertLess(proxy_up, app_up)
        self.assertIn('oauth-protected-resource', calls)
        self.assertIn('oauth-authorization-server', calls)
        self.assertIn('OAuth authentication boundary verified', result.stdout)
        self.assertNotIn(' -k ', calls)

    def test_proxy_conflicts_and_remote_daemons(self):
        for environment in ({'BUSY_PORT': '1'}, {'BUSY_DOCKER_PORT': '1'},
                            {'TEST_ENDPOINT': 'tcp://remote.example:2376'}):
            with self.subTest(environment=environment):
                result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                        proxy_answers='y\n', PROXY_STATE='missing', **environment)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.production.exists())
                self.assertFalse((self.stacks_root / 'vigara-caddy').exists())

    def test_existing_proxy_directory_is_preserved(self):
        proxy = self.stacks_root / 'vigara-caddy'
        proxy.mkdir(parents=True)
        (proxy / '.env').write_text('keep-existing-caddy-data')
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                proxy_answers='y\n', PROXY_STATE='missing')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((proxy / '.env').read_text(), 'keep-existing-caddy-data')

    def test_stopped_proxy_start_is_approved(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                proxy_answers='y', PROXY_STATE='stopped')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls.read_text()
        self.assertIn('start caddy-proxy\n', calls)
        self.assertLess(calls.index('start caddy-proxy'), calls.index(' up -d'))
        self.assertFalse((self.stacks_root / 'vigara-caddy').exists())

    def test_stopped_proxy_can_be_declined(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                proxy_answers='n', PROXY_STATE='stopped')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('start caddy-proxy\n', self.calls.read_text())
        self.assertFalse(self.production.exists())

    def test_proxy_start_failure_retains_both_configurations(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                proxy_answers='y\n', PROXY_STATE='missing', FAIL_UP='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Proxy startup failed', result.stderr)
        self.assertTrue((self.stacks_root / 'vigara-caddy' / 'compose.yml').exists())
        self.assertTrue((self.production / '.env').exists())
        self.assertNotIn('compose -p vigara --env-file .env -f compose.yml up', self.calls.read_text())

    def test_tls_failure_is_not_reported_as_ready(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n', FAIL_TLS='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('readiness was not confirmed', result.stderr)
        self.assertNotIn('complete first administrator setup', result.stdout)
        self.assertTrue((self.production / 'check.sh').exists())

    def test_spa_fallback_does_not_pass_mcp_discovery(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                mcp_answer='y\n', BAD_DISCOVERY='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('MCP readiness failed', result.stderr)

    def test_mcp_must_require_authentication(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                mcp_answer='y\n', MCP_STATUS='200')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('MCP readiness failed', result.stderr)

    def test_proxy_compose_with_real_cli(self):
        available = subprocess.run(['docker', 'compose', 'version'], capture_output=True)
        if available.returncode:
            self.skipTest('A real Docker Compose CLI is unavailable')
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                proxy_answers='y\n', PROXY_STATE='missing')
        self.assertEqual(result.returncode, 0, result.stderr)
        helper = self.stacks_root / 'vigara-caddy' / 'compose.sh'
        result = subprocess.run(['bash', str(helper), 'config', '--format', 'json'], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        config = json.loads(result.stdout)
        caddy = config['services']['caddy']
        self.assertEqual({port['target'] for port in caddy['ports']}, {80, 443})
        self.assertEqual(caddy['environment']['CADDY_INGRESS_NETWORKS'], 'web')
        self.assertEqual(config['networks']['proxy']['name'], 'web')
        self.assertTrue(config['networks']['proxy']['external'])
        mounts = {mount['target']: mount for mount in caddy['volumes']}
        self.assertEqual(mounts['/data']['type'], 'volume')
        self.assertEqual(mounts['/config']['type'], 'volume')
        self.assertTrue(mounts['/var/run/docker.sock']['read_only'])

    def test_mcp_requires_public_https_origin(self):
        for origin in ('http://vigara.example.com', 'https://vigara.example.com/mcp', 'https://other.example.com'):
            with self.subTest(origin=origin):
                result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n', mcp_answer='y\n' + origin)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.production.exists())

    def test_direct_mcp_requires_https_proxy(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', mcp_answer='y\nhttps://vigara.example.com')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('HTTPS reverse proxy', result.stderr)

    def test_input_rejection(self):
        for answers in ('../bad\n', '\nimage$bad\n', '\n\ny\nhttps://bad.example\n',
                        '\n\nn\n999.0.0.1\n', '\n\nn\n\n65536\n'):
            with self.subTest(answers=answers):
                self.assertNotEqual(self.run_setup(answers).returncode, 0)
                self.assertFalse((self.production).exists())

    def test_prerequisite_failures(self):
        for answers, environment in (
            ('', {"FAIL_INFO": "1"}), ('\n', {"EXISTING": "1"}),
            ('\n', {"EXISTING_VOLUME": "1"}),
            ('\n\ny\nvigara.example.com\n\n', {"FAIL_NETWORK": "1"}),
            ('\n\nn\n\n\n', {"BUSY_PORT": "1"}),
            ('\n\nn\n\n\nn\n', {"FAIL_CONFIG": "1"}),
        ):
            with self.subTest(environment=environment):
                self.assertNotEqual(self.run_setup(answers, **environment).returncode, 0)
                self.assertFalse((self.production).exists())
                self.assertEqual(list(self.stacks_root.glob('.vigara-setup.*')), [])

    def test_pull_failure_preserves_config(self):
        result = self.run_setup('\n\nn\n\n\nn\ny\n', FAIL_PULL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('docker login ghcr.io', result.stderr)
        self.assertTrue((self.production / '.env').exists())
        self.assertNotIn(' up -d ', self.calls.read_text())

    def test_start_failure_preserves_config(self):
        result = self.run_setup('\n\nn\n\n\nn\ny\n', FAIL_UP="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('logs --tail 100', result.stderr)
        self.assertTrue((self.production / '.env').exists())
        self.assertIn('up -d --wait --wait-timeout 120', self.calls.read_text())

    def test_custom_configuration_and_start(self):
        result = self.run_setup('vigara-prod\nexample/app:fixed\nn\n0.0.0.0\n18080\ny\ny\n',
                                VIGARA_PORT='9999', VIGARA_IMAGE='ignored/app:wrong')
        self.assertEqual(result.returncode, 0, result.stderr)
        env = (self.stacks_root / 'vigara-prod' / '.env').read_text()
        self.assertIn('VIGARA_COOKIE_SECURE=true', env)
        self.assertIn('VIGARA_PORT=18080', env)
        self.assertIn('VIGARA_IMAGE=example/app:fixed', env)
        calls = self.calls.read_text()
        self.assertIn('-p vigara-prod', calls)
        self.assertLess(calls.index(' pull'), calls.index(' up -d'))
        self.assertIn('Open your external HTTPS URL', result.stdout)
        self.assertNotIn(' down', calls)

    def test_explicit_container_name_conflict(self):
        result = self.run_setup('\n', CONFLICT_NAME='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Container name vigara-app already exists', result.stderr)
        self.assertFalse(self.production.exists())

    def test_proxy_name_cannot_match_app(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\nvigara-app\nn\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('proxy name conflicts', result.stderr)

    def test_bind_storage_direct(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', storage_answer='y', root_answer='~/shared stacks')
        self.assertEqual(result.returncode, 0, result.stderr)
        production = self.root / 'shared stacks' / 'vigara'
        self.assertTrue((production / 'data' / 'postgres').is_dir())
        self.assertTrue((production / 'data' / 'android-backups').is_dir())
        compose = (production / 'compose.yml').read_text()
        self.assertNotIn('\nvolumes:\n', compose)
        self.assertIn('source: ./data/postgres', compose)
        self.assertIn('source: ./data/android-backups', compose)
        self.assertIn('create_host_path: false', compose)
        self.assertIn('container_name: vigara-app', compose)
        self.assertIn('container_name: vigara-postgres', compose)
        self.assertTrue((production / 'prepare-storage.sh').exists())
        self.assertIn('prepare-storage.sh', result.stdout)
        self.assertNotIn('run --rm', self.calls.read_text())

    def test_bind_storage_initializes_permissions_before_start(self):
        result = self.run_setup('\n\nn\n\n\nn\ny\n', storage_answer='y')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls.read_text()
        self.assertIn('--entrypoint chown', calls)
        self.assertIn('10001:10001 /var/lib/postgresql/data', calls)
        self.assertLess(calls.index('run --rm'), calls.index(' up -d'))
        self.assertNotIn('chown -R', calls)

    def test_permission_failure_retains_bind_configuration(self):
        result = self.run_setup('\n\nn\n\n\nn\ny\n', storage_answer='y', FAIL_PERMISSIONS='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Could not prepare bind storage permissions', result.stderr)
        self.assertTrue((self.production / '.env').exists())
        self.assertNotIn(' up -d ', self.calls.read_text())

    def test_bind_storage_rejects_remote_daemon(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', storage_answer='y', TEST_ENDPOINT='tcp://remote.example:2376')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.production.exists())

    def test_bind_mounts_and_names_with_real_compose(self):
        available = subprocess.run(['docker', 'compose', 'version'], capture_output=True)
        if available.returncode:
            self.skipTest('A real Docker Compose CLI is unavailable')
        for caddy in (False, True):
            with self.subTest(caddy=caddy):
                answers = ('vigara-prod\n\ny\nvigara.example.com\n\n\nn\n' if caddy
                           else 'vigara-prod\n\nn\n\n\nn\nn\n')
                result = self.run_setup(answers, storage_answer='y', root_answer='~/shared stacks',
                                        proxy_answers='y\n' if caddy else None, PROXY_STATE='missing')
                self.assertEqual(result.returncode, 0, result.stderr)
                runtime = self.root / 'shared stacks'
                directories = [runtime / 'vigara-prod']
                if caddy:
                    directories.append(runtime / 'vigara-prod-caddy')
                for directory in directories:
                    parsed = subprocess.run(['bash', str(directory / 'compose.sh'), 'config', '--format', 'json'],
                                            capture_output=True, text=True)
                    self.assertEqual(parsed.returncode, 0, parsed.stderr)
                    config = json.loads(parsed.stdout)
                    self.assertFalse(config.get('volumes'))
                    for service in config['services'].values():
                        for mount in service.get('volumes', []):
                            self.assertEqual(mount['type'], 'bind')
                            if mount['target'] != '/var/run/docker.sock':
                                self.assertFalse(mount.get('bind', {}).get('create_host_path', False))
                                self.assertTrue(Path(mount['source']).is_dir())
                    if directory.name == 'vigara-prod':
                        self.assertEqual(config['services']['app']['container_name'], 'vigara-prod-app')
                        db = config['services']['postgres' if caddy else 'db']
                        self.assertEqual(db['container_name'], 'vigara-prod-postgres')
                    else:
                        self.assertEqual(config['services']['caddy']['container_name'], 'caddy-proxy')
                for directory in directories:
                    shutil.rmtree(directory)

    def test_early_eof(self):
        self.assertNotEqual(self.run_setup('').returncode, 0)
        self.assertFalse((self.production).exists())

    def test_generated_files_with_real_compose(self):
        # The other tests never require real Docker. CI also parses both generated
        # deployments with Compose, which does not require a running daemon.
        available = subprocess.run(['docker', 'compose', 'version'], capture_output=True)
        if available.returncode:
            self.skipTest('A real Docker Compose CLI is unavailable')
        for answers, mcp_answer in (
            ('\n\nn\n\n\nn\nn\n', 'n'),
            ('\n\ny\nvigara.example.com\n\n\nn\n', 'y\n'),
            ('\n\nn\n\n\ny\nn\n', 'y\nhttps://vigara.example.com'),
        ):
            with self.subTest(answers=answers):
                result = self.run_setup(answers, mcp_answer=mcp_answer)
                self.assertEqual(result.returncode, 0, result.stderr)
                production = self.production
                result = subprocess.run(['bash', str(production / 'compose.sh'), 'config', '--quiet'],
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                shutil.rmtree(production)


if __name__ == '__main__':
    unittest.main()
