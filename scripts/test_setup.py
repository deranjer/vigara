"""Exercise the interactive Linux installer with an isolated Docker stub.

No containers or host networks are changed. Run: python3 scripts/test_setup.py
"""
import os
import base64
import json
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[1]
DOCKER = r'''#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$CALLS"
case "$*" in
  info) [[ ${FAIL_INFO:-0} != 1 ]];;
  'compose version') echo 'Docker Compose test stub';;
  'compose up --help') echo '--wait-timeout';;
  'ps -aq --filter volume='*) [[ ${VOLUME_IN_USE:-0} != 1 ]] || echo stopped-container;;
  'ps -aq --filter label=com.docker.compose.project=vigara-caddy') [[ ${PROXY_CONTAINERS:-0} != 1 ]] || echo orphaned-proxy-container;;
  'ps -aq '*) [[ ${EXISTING:-0} != 1 ]] || echo existing-container;;
  'ps -q '*) [[ ${BUSY_DOCKER_PORT:-0} != 1 ]] || echo existing-port-owner;;
  'volume ls '*)
    [[ ${EXISTING_VOLUME:-0} != 1 ]] || echo existing-volume
    if [[ $* == *project=vigara-caddy && ${PROXY_VOLUMES:-0} == 1 ]]; then
      printf '%s\n' vigara-caddy_caddy-data vigara-caddy_caddy-config vigara-caddy_unrelated
    fi;;
  'volume inspect --format '*)
    if [[ $* == *vigara-caddy_caddy-* ]]; then echo "${VOLUME_OWNER:-vigara-caddy}";
    else echo "${VOLUME_OWNER:-vigara}"; fi;;
  'volume inspect vigara-caddy_caddy-'*) [[ ${PROXY_VOLUMES:-0} == 1 ]];;
  'volume inspect '*) [[ ${NAMED_VOLUME:-0} == 1 ]];;
  'rm -f '*|'volume rm '*) [[ ${FAIL_PURGE:-0} != 1 && ${FAIL_REMOVE:-0} != 1 ]];;
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
      *' host-agent-bundle '*) [[ ${FAIL_HOST_BUNDLE:-0} != 1 ]] && cat "$HOST_BUNDLE";;
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
        for name in ("setup.sh", "enroll-host.sh", "check-ready.sh", "compose.staging.yml", "compose.caddy.example.yml", "compose.caddy-proxy.example.yml"):
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

    def run_setup(self, answers, root_answer='', mcp_answer='n', proxy_answers=None, storage_answer='n', public_url_answer='', reset_answers=None, bind_root_answer='', host_answer='n', **environment):
        # Fixtures below describe the original project/image/network prompts.
        # Add the shared-root answer and MCP opt-in before the startup answer.
        lines = answers.splitlines()
        if len(lines) >= 6:
            lines.insert(6, mcp_answer)
            if proxy_answers is not None:
                lines.insert(6, proxy_answers)
            if lines[2].lower() not in ('y', 'yes'):
                lines.insert(6, public_url_answer)
        if lines:
            lines.insert(1, root_answer)
        if len(lines) >= 3:
            lines.insert(3, storage_answer)
            if storage_answer.lower() in ('y', 'yes'):
                lines.insert(4, bind_root_answer)
        if reset_answers is not None:
            lines[2:2] = reset_answers.splitlines()
        answers = '\n'.join(lines) + ('\n' if lines else '')
        answers += host_answer + '\n'
        return subprocess.run(["bash", str(self.deploy / "setup.sh")], input=answers,
                              text=True, capture_output=True, cwd=self.root,
                              env=dict(self.environment, **environment))

    def test_direct_configuration_and_rerun(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        production = self.production
        env = (production / ".env").read_text()
        self.assertIn('VIGIRA_PORT=8080', env)
        self.assertIn('VIGIRA_COOKIE_SECURE=false', env)
        self.assertRegex(env, r'POSTGRES_PASSWORD=[a-f0-9]{64}\n')
        self.assertRegex(env, r'VIGIRA_BACKUP_MASTER_KEY=[A-Za-z0-9+/]{43}=\n')
        settings = dict(line.split('=', 1) for line in env.splitlines() if '=' in line)
        github_key = settings['VIGIRA_GITHUB_APP_MASTER_KEY']
        self.assertEqual(len(base64.b64decode(github_key, validate=True)), 32)
        self.assertNotEqual(github_key, settings['VIGIRA_BACKUP_MASTER_KEY'])
        self.assertNotIn(github_key, result.stdout + result.stderr)
        self.assertNotIn(github_key, (production / 'agent.env').read_text())
        self.assertEqual((production / ".env").stat().st_mode & 0o777, 0o600)
        compose = (production / "compose.yml").read_text()
        self.assertIn('env_file: .env', compose)
        self.assertIn('${VIGIRA_BIND_ADDRESS:-127.0.0.1}', compose)
        self.assertIn('container_name: vigara-app', compose)
        self.assertIn('container_name: vigara-postgres', compose)
        self.assertIn('postgres-data:/var/lib/postgresql/data', compose)
        self.assertIn('stack-git:/var/lib/vigira/stacks.git', compose)
        self.assertIn('Before connecting GitHub, set VIGIRA_PUBLIC_URL', result.stdout)
        self.assertFalse((production / 'data').exists())
        self.assertNotIn(' up -d ', self.calls.read_text())
        self.assertNotIn('pull', self.calls.read_text())
        self.assertNotIn(env.splitlines()[0], result.stdout)
        self.assertIn('Setup already exists', self.run_setup('\n', reset_answers='n').stderr)
        self.assertEqual((production / ".env").read_text(), env)

    def test_github_app_setup_uses_public_origin_without_printing_key(self):
        result = self.run_setup('\n\nn\n\n\ny\nn\n', public_url_answer='https://vigara.example.com')
        self.assertEqual(result.returncode, 0, result.stderr)
        env = (self.production / '.env').read_text()
        self.assertIn('VIGIRA_PUBLIC_URL=https://vigara.example.com', env)
        self.assertIn('GitHub App setup: sign in at https://vigara.example.com', result.stdout)
        self.assertNotIn('Before connecting GitHub', result.stdout)
        key = next(line.split('=', 1)[1] for line in env.splitlines() if line.startswith('VIGIRA_GITHUB_APP_MASTER_KEY='))
        self.assertNotIn(key, result.stdout + result.stderr)

    def test_caddy_configuration(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        production = self.production
        self.assertIn('VIGIRA_DOMAIN=vigara.example.com', (production / ".env").read_text())
        compose = (production / "compose.yml").read_text()
        self.assertIn('caddy.reverse_proxy:', compose)
        self.assertIn('VIGIRA_BACKUP_MASTER_KEY:', compose)
        self.assertIn('VIGIRA_GITHUB_APP_MASTER_KEY: ${VIGIRA_GITHUB_APP_MASTER_KEY:-}', compose)
        self.assertIn('stack-git:/var/lib/vigira/stacks.git', compose)
        self.assertIn('GitHub App setup: sign in at https://vigara.example.com', result.stdout)
        self.assertEqual(compose.count('      VIGIRA_BACKUP_MASTER_KEY:'), 1)
        self.assertNotIn('ports:', compose)
        self.assertIn('VIGIRA_MCP_URL=\n', (production / '.env').read_text())
        self.assertIn('VIGIRA_PUBLIC_URL=https://vigara.example.com\n', (production / '.env').read_text())

    def test_reset_defaults_to_keep_and_requires_exact_confirmation(self):
        for answer in ('', 'n', 'y\nPURGE other', 'y'):
            with self.subTest(answer=answer):
                result = self.run_setup('\n\nn\n\n\nn\nn\n', reset_answers=answer,
                                        EXISTING='1', EXISTING_VOLUME='1')
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('rm -f existing-container', self.calls.read_text())
                self.assertNotIn('volume rm existing-volume', self.calls.read_text())
                self.assertFalse(self.production.exists())

    def test_confirmed_reset_removes_only_inventoried_resources(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', reset_answers='y\nPURGE vigara',
                                EXISTING='1', EXISTING_VOLUME='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls.read_text()
        self.assertIn('rm -f existing-container\n', calls)
        self.assertIn('volume rm existing-volume\n', calls)
        self.assertLess(calls.index('rm -f existing-container'), calls.index('volume rm existing-volume'))
        self.assertNotIn('prune', calls)
        self.assertNotIn('rm -f caddy-proxy', calls)
        self.assertIn('NO UNDO', result.stdout)
        self.assertTrue((self.production / '.env').exists())

    def test_orphaned_volume_can_be_reset_without_containers(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', reset_answers='y\nPURGE vigara',
                                EXISTING_VOLUME='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('volume rm existing-volume', self.calls.read_text())
        self.assertNotIn('rm -f ', self.calls.read_text())

    def test_unlabelled_named_volume_is_not_purged(self):
        result = self.run_setup('\n', reset_answers='y\nPURGE vigara',
                                NAMED_VOLUME='1', VOLUME_OWNER='another-project')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('no matching project label', result.stderr)
        self.assertNotIn('volume rm ', self.calls.read_text())

    def test_orphaned_proxy_storage_requires_its_own_confirmation(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                proxy_answers='y\npurge\nPURGE vigara-caddy\n', PROXY_STATE='missing',
                                PROXY_VOLUMES='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('volume rm vigara-caddy_caddy-data', self.calls.read_text())
        self.assertIn('project vigara-caddy', result.stdout)

    def test_proxy_directory_reset_archives_config_and_skips_volume_choice(self):
        proxy = self.stacks_root / 'vigara-caddy'
        proxy.mkdir(parents=True)
        for name in ('compose.sh', 'compose.yml', '.env'):
            (proxy / name).write_text('previous proxy configuration')
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                proxy_answers='y\ny\nPURGE vigara-caddy\n',
                                PROXY_STATE='missing', PROXY_VOLUMES='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        backups = list(self.stacks_root.glob('vigara-caddy.before-reset.*'))
        self.assertEqual(len(backups), 1)
        self.assertEqual((backups[0] / '.env').read_text(), 'previous proxy configuration')
        self.assertIn('VIGIRA_CADDY_CONTAINER=caddy-proxy', (proxy / '.env').read_text())
        self.assertNotIn('Warning: existing volumes', result.stdout)
        self.assertNotIn('purge-volumes.sh', result.stdout)

    def test_proxy_container_reset_requires_separate_confirmation(self):
        for confirmation in ('n', 'y\nPURGE vigara', 'y\nPURGE vigara-caddy'):
            with self.subTest(confirmation=confirmation):
                result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                        proxy_answers='y\n' + confirmation + '\n',
                                        PROXY_STATE='missing', PROXY_CONTAINERS='1')
                if confirmation.endswith('PURGE vigara-caddy'):
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn('rm -f orphaned-proxy-container', self.calls.read_text())
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertNotIn('rm -f orphaned-proxy-container', self.calls.read_text())
                    self.assertFalse(self.production.exists())

    def test_reset_archives_config_and_bind_data(self):
        self.assertEqual(self.run_setup('\n\nn\n\n\nn\nn\n').returncode, 0)
        original = (self.production / '.env').read_text()
        data = self.production / 'data' / 'postgres'
        data.mkdir(parents=True)
        (data / 'important').write_text('old database')
        result = self.run_setup('\n\nn\n\n\nn\nn\n', reset_answers='y\nPURGE vigara')
        self.assertEqual(result.returncode, 0, result.stderr)
        backups = list(self.stacks_root.glob('vigara.before-reset.*'))
        self.assertEqual(len(backups), 1)
        self.assertEqual((backups[0] / '.env').read_text(), original)
        self.assertEqual((backups[0] / 'data/postgres/important').read_text(), 'old database')
        self.assertNotEqual((self.production / '.env').read_text(), original)
        self.assertFalse((self.production / 'data').exists())

    def test_failed_purge_stops_before_creating_config(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', reset_answers='y\nPURGE vigara',
                                EXISTING='1', EXISTING_VOLUME='1', FAIL_PURGE='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.production.exists())
        self.assertNotIn('volume rm existing-volume', self.calls.read_text())

    def test_remote_context_cannot_purge(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', reset_answers='y\nPURGE vigara',
                                EXISTING_VOLUME='1', TEST_ENDPOINT='ssh://example.invalid')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('volume rm existing-volume', self.calls.read_text())
        self.assertFalse(self.production.exists())

    def test_reset_refuses_unrecognized_directory_or_symlink(self):
        self.production.mkdir(parents=True)
        (self.production / 'unrelated').write_text('keep')
        result = self.run_setup('\n', reset_answers='y\nPURGE vigara')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.production / 'unrelated').read_text(), 'keep')
        self.production.rename(self.stacks_root / 'other')
        self.production.symlink_to(self.stacks_root / 'other', target_is_directory=True)
        result = self.run_setup('\n', reset_answers='y\nPURGE vigara')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.production.is_symlink())

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
        deployment = self.root / 'shared compose' / 'vigara'
        setting = 'VIGIRA_COMPOSE_MANAGED_ROOT="' + str(self.root / 'shared compose') + '"\n'
        self.assertIn(setting, (deployment / '.env').read_text())
        self.assertEqual((deployment / 'agent.env').read_text(), setting)
        self.assertIn('configure-agent.sh', result.stdout)

    def test_reject_checkout_and_relative_roots(self):
        for root in ('relative-path', str(self.deploy / 'runtime')):
            with self.subTest(root=root):
                self.assertNotEqual(self.run_setup('\n', root_answer=root).returncode, 0)

    def test_caddy_mcp_opt_in(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n', mcp_answer='y\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('VIGIRA_MCP_URL=https://vigara.example.com\n', (self.production / '.env').read_text())
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
        self.assertIn('VIGIRA_CADDY_IMAGE=lucaslorentz/caddy-docker-proxy:alpine', (proxy / '.env').read_text())
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

    def test_existing_proxy_volumes_default_to_keep(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                proxy_answers='y\n\n', PROXY_STATE='missing', PROXY_VOLUMES='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Warning: existing volumes', result.stdout)
        self.assertIn('caddy-data:/data', (self.stacks_root / 'vigara-caddy' / 'compose.yml').read_text())
        self.assertNotIn('volume rm', self.calls.read_text())

    def test_keep_proxy_volumes_overrides_bind_storage(self):
        data_root = self.root / 'mounted data'
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n', storage_answer='y',
                                bind_root_answer=str(data_root), proxy_answers='y\nkeep\n', PROXY_STATE='missing', PROXY_VOLUMES='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('source: ' + str(data_root / 'vigara/data/postgres'), (self.production / 'compose.yml').read_text())
        proxy = self.stacks_root / 'vigara-caddy'
        self.assertIn('caddy-data:/data', (proxy / 'compose.yml').read_text())
        self.assertFalse((proxy / 'data').exists())
        self.assertFalse((data_root / 'vigara-caddy').exists())

    def test_proxy_purge_is_scoped_and_ordered(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                proxy_answers='y\npurge\nPURGE vigara-caddy\n',
                                PROXY_STATE='missing', PROXY_VOLUMES='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls.read_text()
        removals = [line for line in calls.splitlines() if line.startswith('volume rm')]
        self.assertEqual(removals, ['volume rm vigara-caddy_caddy-data', 'volume rm vigara-caddy_caddy-config'])
        self.assertLess(calls.index(' pull'), calls.index('volume rm'))
        self.assertLess(calls.index('volume rm'), calls.index(' up -d'))
        self.assertNotIn('prune', calls)
        self.assertNotIn(' down', calls)

    def test_proxy_purge_deferred_and_rechecks_ownership(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                proxy_answers='y\npurge\nPURGE vigara-caddy\n',
                                PROXY_STATE='missing', PROXY_VOLUMES='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('purge-volumes.sh', result.stdout)
        self.assertNotIn('volume rm', self.calls.read_text())
        helper = self.stacks_root / 'vigara-caddy' / 'purge-volumes.sh'
        result = subprocess.run(['bash', str(helper)], capture_output=True, text=True,
                                env=dict(self.environment, PROXY_VOLUMES='1', VOLUME_OWNER='another-project'))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('ownership changed', result.stderr)
        self.assertNotIn('volume rm', self.calls.read_text())

    def test_proxy_purge_requires_confirmation(self):
        for answer in ('purge\nwrong\n', 'cancel\n', 'purge\n'):
            with self.subTest(answer=answer):
                result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                        proxy_answers='y\n' + answer, PROXY_STATE='missing', PROXY_VOLUMES='1')
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('volume rm', self.calls.read_text())
                self.assertFalse(self.production.exists())

    def test_proxy_purge_refuses_container_references(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                proxy_answers='y\npurge\nPURGE vigara-caddy\n',
                                PROXY_STATE='missing', PROXY_VOLUMES='1', VOLUME_IN_USE='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('container still references', result.stderr)
        calls = self.calls.read_text()
        self.assertNotIn('volume rm', calls)
        self.assertNotIn(' up -d', calls)
        self.assertTrue((self.production / '.env').exists())

    def test_foreign_proxy_volume_rejected(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n',
                                proxy_answers='y\n', PROXY_STATE='missing', PROXY_VOLUMES='1', VOLUME_OWNER='other')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('not owned by vigara-caddy', result.stderr)
        self.assertNotIn('volume rm', self.calls.read_text())
        self.assertFalse(self.production.exists())

    def test_proxy_purge_removal_failure_stops_startup(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                proxy_answers='y\npurge\nPURGE vigara-caddy\n',
                                PROXY_STATE='missing', PROXY_VOLUMES='1', FAIL_REMOVE='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Proxy volume purge failed', result.stderr)
        self.assertNotIn(' up -d', self.calls.read_text())
        self.assertTrue((self.production / '.env').exists())

    def test_proxy_purge_does_not_run_before_validation_or_pull(self):
        for failure in ('FAIL_CONFIG', 'FAIL_PULL'):
            with self.subTest(failure=failure):
                result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n',
                                        proxy_answers='y\npurge\nPURGE vigara-caddy\n',
                                        PROXY_STATE='missing', PROXY_VOLUMES='1', **{failure: '1'})
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('volume rm', self.calls.read_text())
                if self.production.exists():
                    shutil.rmtree(self.production)
                    shutil.rmtree(self.stacks_root / 'vigara-caddy')

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
                                VIGIRA_PORT='9999', VIGIRA_IMAGE='ignored/app:wrong')
        self.assertEqual(result.returncode, 0, result.stderr)
        env = (self.stacks_root / 'vigara-prod' / '.env').read_text()
        self.assertIn('VIGIRA_COOKIE_SECURE=true', env)
        self.assertIn('VIGIRA_PORT=18080', env)
        self.assertIn('VIGIRA_IMAGE=example/app:fixed', env)
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

    def prepare_host_registration(self):
        binary = self.root / 'bin'
        (binary / 'sudo').write_text('''#!/usr/bin/env bash
printf 'sudo %s\\n' "$*" >> "$CALLS"
if [[ $1 == test ]]; then [[ ${EXISTING_HOST_AGENT:-0} == 1 ]]; elif [[ $1 == -v ]]; then exit 0; else exit 1; fi
''')
        (binary / 'systemctl').write_text('#!/usr/bin/env bash\nprintf "systemctl %s\\n" "$*" >> "$CALLS"\n')
        (binary / 'hostname').write_text('#!/usr/bin/env bash\necho server-host\n')
        (binary / 'uname').write_text('#!/usr/bin/env bash\nif [[ $1 == -m ]]; then echo "${HOST_ARCH:-x86_64}"; else echo Linux; fi\n')
        for name in ('sudo', 'systemctl', 'hostname', 'uname'):
            (binary / name).chmod(0o700)
        bundle = self.root / 'host.zip'
        with zipfile.ZipFile(bundle, 'w') as archive:
            for name in ('vigara-agent', 'bootstrap.json', 'configure-docker.py', 'vigara-agent.service'):
                archive.writestr(name, '{}')
            archive.writestr('install.sh', '#!/bin/sh\nprintf "host-install %s\\n" "$*" >> "$CALLS"\nexit "${FAIL_HOST_INSTALL:-0}"\n')
        self.environment['HOST_BUNDLE'] = str(bundle)

    def test_host_registration_runs_after_readiness_with_deploy_settings(self):
        self.prepare_host_registration()
        result = self.run_setup('\n\nn\n\n\nn\ny\n', host_answer='y\nmy-host', root_answer='~/shared stacks')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('now registered as a Docker host', result.stdout)
        calls = self.calls.read_text()
        self.assertLess(calls.index('/healthz'), calls.index('host-agent-bundle'))
        self.assertIn('--name my-host --target linux-amd64 --server http://127.0.0.1:8080', calls)
        self.assertIn('--docker-host unix://' + str(self.root / 'docker.sock'), calls)
        self.assertIn('--compose-root ' + str(self.root / 'shared stacks'), calls)
        self.assertIn('host-install --non-interactive', calls)
        self.assertEqual(list((self.root / 'shared stacks/vigara').glob('.host-agent.*')), [])

    def test_host_registration_arm64_and_https(self):
        self.prepare_host_registration()
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\ny\n', host_answer='y\n', HOST_ARCH='aarch64')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('--name server-host --target linux-arm64 --server https://vigara.example.com', self.calls.read_text())

    def test_host_registration_declined_and_deferred(self):
        for startup in ('n', 'y'):
            with self.subTest(startup=startup):
                result = self.run_setup('\n\nn\n\n\nn\n' + startup + '\n')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('register-host.sh', result.stdout)
                self.assertTrue((self.production / 'register-host.sh').is_file())
                self.assertNotIn('host-agent-bundle', self.calls.read_text())
                shutil.rmtree(self.production)

    def test_host_registration_does_not_replace_existing_agent(self):
        self.prepare_host_registration()
        result = self.run_setup('\n\nn\n\n\nn\ny\n', host_answer='y', EXISTING_HOST_AGENT='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('already has an enrolled agent', result.stderr)
        self.assertNotIn('host-agent-bundle', self.calls.read_text())
        self.assertTrue((self.production / '.env').is_file())

    def test_host_registration_failure_retains_running_server(self):
        self.prepare_host_registration()
        for failure in ('FAIL_HOST_BUNDLE', 'FAIL_HOST_INSTALL', 'FAIL_TLS'):
            with self.subTest(failure=failure):
                result = self.run_setup('\n\nn\n\n\nn\ny\n', host_answer='y\n', **{failure: '1'})
                self.assertTrue((self.production / '.env').is_file())
                bundles = list(self.production.glob('.host-agent.*'))
                self.assertEqual(len(bundles), 1 if failure == 'FAIL_HOST_INSTALL' else 0)
                if failure == 'FAIL_HOST_INSTALL':
                    self.assertIn('private bundle is retained', result.stderr)
                if failure == 'FAIL_TLS':
                    self.assertNotEqual(result.returncode, 0)
                    self.assertNotIn('host-agent-bundle', self.calls.read_text())
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn('Host registration did not complete', result.stdout)
                shutil.rmtree(self.production)
                self.calls.write_text('')

    def test_bind_storage_direct(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', storage_answer='y', root_answer='~/shared stacks')
        self.assertEqual(result.returncode, 0, result.stderr)
        production = self.root / 'shared stacks' / 'vigara'
        self.assertTrue((production / 'data' / 'postgres').is_dir())
        self.assertTrue((production / 'data' / 'android-backups').is_dir())
        self.assertTrue((production / 'data' / 'agent-releases').is_dir())
        self.assertTrue((production / 'data' / 'stack-git').is_dir())
        compose = (production / 'compose.yml').read_text()
        self.assertNotIn('\nvolumes:\n', compose)
        self.assertIn('source: ./data/postgres', compose)
        self.assertIn('source: ./data/android-backups', compose)
        self.assertIn('source: ./data/stack-git', compose)
        self.assertIn('create_host_path: true', compose)
        self.assertIn('container_name: vigara-app', compose)
        self.assertIn('container_name: vigara-postgres', compose)
        self.assertTrue((production / 'prepare-storage.sh').exists())
        self.assertIn('stack-git', (production / 'prepare-storage.sh').read_text())
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

    def test_custom_bind_root_creates_data_and_prepares_external_permissions(self):
        data_root = self.root / 'mounted data'
        result = self.run_setup('\n\nn\n\n\nn\ny\n', storage_answer='y', bind_root_answer=str(data_root))
        self.assertEqual(result.returncode, 0, result.stderr)
        for name in ('postgres', 'android-backups', 'agent-releases'):
            self.assertTrue((data_root / 'vigara' / 'data' / name).is_dir())
        self.assertFalse((self.production / 'data').exists())
        self.assertIn('source: ' + str(data_root / 'vigara/data/postgres'), (self.production / 'compose.yml').read_text())
        calls = self.calls.read_text()
        self.assertIn('source=' + str(data_root / 'vigara/data/android-backups'), calls)
        self.assertLess(calls.index('run --rm'), calls.index(' up -d'))
        self.assertIn(str(data_root / 'vigara/data'), result.stdout)

    def test_bind_directory_failure_prints_manual_commands(self):
        data_root = self.root / 'mounted data'
        real_mkdir = shutil.which('mkdir')
        mkdir = self.root / 'bin' / 'mkdir'
        mkdir.write_text('''#!/usr/bin/env bash
for path in "$@"; do
  if [[ $path == "$FAIL_MKDIR_PATH" ]]; then
    echo 'mkdir: Permission denied' >&2
    exit 1
  fi
done
exec "$REAL_MKDIR" "$@"
''')
        mkdir.chmod(0o700)
        for failing_path in (data_root, data_root / 'vigara/data/postgres'):
            with self.subTest(path=failing_path):
                result = self.run_setup('\n\nn\n\n\nn\nn\n', storage_answer='y',
                                        bind_root_answer=str(data_root),
                                        FAIL_MKDIR_PATH=str(failing_path), REAL_MKDIR=real_mkdir)
                self.assertNotEqual(result.returncode, 0)
                escaped_project = str(data_root / 'vigara').replace(' ', r'\ ')
                self.assertIn('Run these commands, then rerun setup:', result.stderr)
                self.assertIn('sudo mkdir -p -- ' + escaped_project + '/data', result.stderr)
                self.assertIn(f'sudo chown -- {os.getuid()}:{os.getgid()} '
                              f'{escaped_project} {escaped_project}/data', result.stderr)
                self.assertNotIn('chown -R', result.stderr)
                self.assertFalse(self.production.exists())
                self.assertNotIn(' up -d', self.calls.read_text())

    def test_bind_root_rejects_invalid_paths_and_existing_data(self):
        for path in ('relative/path', '/tmp/bad,root', str(self.deploy.parent), str(self.production)):
            with self.subTest(path=path):
                result = self.run_setup('\n\nn\n\n\nn\nn\n', storage_answer='y', bind_root_answer=path)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.production.exists())
        data_root = self.root / 'existing data'
        data = data_root / 'vigara/data/postgres'
        data.mkdir(parents=True)
        (data / '.database-marker').write_text('preserve')
        result = self.run_setup('\n\nn\n\n\nn\nn\n', storage_answer='y', bind_root_answer=str(data_root))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Bind data already exists', result.stderr)
        self.assertEqual((data / '.database-marker').read_text(), 'preserve')
        self.assertFalse(self.production.exists())

    def test_bind_root_equal_to_compose_root_keeps_relative_layout(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n', storage_answer='y', bind_root_answer=str(self.stacks_root))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('source: ./data/postgres', (self.production / 'compose.yml').read_text())
        self.assertTrue((self.production / 'data/postgres').is_dir())

    def test_bind_root_refuses_symlink_data_without_touching_target(self):
        data_root = self.root / 'mounted data'
        target = self.root / 'existing storage'
        target.mkdir()
        (target / 'important').write_text('preserve')
        project = data_root / 'vigara'
        project.mkdir(parents=True)
        (project / 'data').symlink_to(target, target_is_directory=True)
        result = self.run_setup('\n\nn\n\n\nn\nn\n', storage_answer='y', bind_root_answer=str(data_root))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('symlink', result.stderr)
        self.assertEqual((target / 'important').read_text(), 'preserve')
        self.assertEqual(list(target.iterdir()), [target / 'important'])

    def test_saved_agent_root_handles_compose_interpolation_characters(self):
        available = subprocess.run(['docker', 'compose', 'version'], capture_output=True)
        if available.returncode:
            self.skipTest('A real Docker Compose CLI is unavailable')
        root = self.root / 'shared $compose "root"'
        result = self.run_setup('\n\nn\n\n\nn\nn\n', root_answer=str(root))
        self.assertEqual(result.returncode, 0, result.stderr)
        parsed = subprocess.run(['bash', str(root / 'vigara/compose.sh'), 'config', '--environment'],
                                capture_output=True, text=True)
        self.assertEqual(parsed.returncode, 0, parsed.stderr)
        self.assertIn('VIGIRA_COMPOSE_MANAGED_ROOT=' + str(root), parsed.stdout.splitlines())

    def test_custom_bind_root_with_real_compose_and_proxy(self):
        available = subprocess.run(['docker', 'compose', 'version'], capture_output=True)
        if available.returncode:
            self.skipTest('A real Docker Compose CLI is unavailable')
        data_root = self.root / 'mounted data'
        result = self.run_setup('vigara-prod\n\ny\nvigara.example.com\n\n\nn\n', storage_answer='y',
                                bind_root_answer=str(data_root), proxy_answers='y\n', PROXY_STATE='missing')
        self.assertEqual(result.returncode, 0, result.stderr)
        for name in ('vigara-prod', 'vigara-prod-caddy'):
            directory = self.stacks_root / name
            parsed = subprocess.run(['bash', str(directory / 'compose.sh'), 'config', '--format', 'json'],
                                    capture_output=True, text=True)
            self.assertEqual(parsed.returncode, 0, parsed.stderr)
            config = json.loads(parsed.stdout)
            for service in config['services'].values():
                for mount in service.get('volumes', []):
                    if mount['target'] == '/var/run/docker.sock':
                        continue
                    self.assertTrue(mount['source'].startswith(str(data_root / name / 'data') + '/'))
                    self.assertTrue(Path(mount['source']).is_dir())
                    # Compose v5 omits this default-valued option from JSON.
                    self.assertTrue(mount['bind'].get('create_host_path', True))

    def test_generated_agent_helper_applies_only_agent_settings(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        agent_env = self.production / 'agent.env'
        self.assertEqual(agent_env.read_text(), f'VIGIRA_COMPOSE_MANAGED_ROOT="{self.stacks_root}"\n')
        self.assertEqual(agent_env.stat().st_mode & 0o777, 0o600)
        binary = self.root / 'bin'
        for name in ('sudo', 'systemctl'):
            stub = binary / name
            stub.write_text('#!/usr/bin/env bash\nprintf "%s %s\\n" "' + name + '" "$*" >> "$CALLS"\n'
                            'if [[ ${1:-} == tee ]]; then cat >/dev/null; fi\nexit 0\n')
            stub.chmod(0o700)
        helper = self.production / 'configure-agent.sh'
        result = subprocess.run(['bash', str(helper)], env=self.environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls.read_text()
        self.assertIn('sudo install -D -m 0600 -- agent.env /etc/vigara-agent/compose.env', calls)
        self.assertIn('sudo systemctl try-restart vigara-agent.service', calls)
        self.assertNotIn('install -D -m 0600 -- .env ', calls)

    def test_bind_storage_applies_to_new_caddy_data_and_config(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n', storage_answer='y',
                                proxy_answers='y\n', PROXY_STATE='missing')
        self.assertEqual(result.returncode, 0, result.stderr)
        proxy = self.stacks_root / 'vigara-caddy'
        compose = (proxy / 'compose.yml').read_text()
        self.assertIn('source: ./data/caddy\n        target: /data', compose)
        self.assertIn('source: ./data/config\n        target: /config', compose)
        self.assertNotIn('caddy-data:/data', compose)
        self.assertNotIn('caddy-config:/config', compose)
        self.assertNotIn('\nvolumes:\n', compose)
        self.assertTrue((proxy / 'data/caddy').is_dir())
        self.assertTrue((proxy / 'data/config').is_dir())

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
                                # Also inspect the generated input: newer Compose
                                # versions omit default-valued options from JSON.
                                self.assertIn('create_host_path: true', (directory / 'compose.yml').read_text())
                                self.assertTrue(mount.get('bind', {}).get('create_host_path', True))
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

    def test_agent_public_url_forwarding(self):
        available = subprocess.run(['docker', 'compose', 'version'], capture_output=True)
        if available.returncode:
            self.skipTest('A real Docker Compose CLI is unavailable')
        for caddy in (False, True):
            with self.subTest(caddy=caddy):
                answers = '\n\ny\nvigara.example.com\n\n\nn\n' if caddy else '\n\nn\n\n\ny\nn\n'
                result = self.run_setup(answers, public_url_answer='https://vigara.example.com')
                self.assertEqual(result.returncode, 0, result.stderr)
                parsed = subprocess.run(['bash', str(self.production / 'compose.sh'), 'config', '--format', 'json'], capture_output=True, text=True)
                self.assertEqual(parsed.returncode, 0, parsed.stderr)
                app = json.loads(parsed.stdout)['services']['app']
                self.assertEqual(app['environment']['VIGIRA_PUBLIC_URL'], 'https://vigara.example.com')
                self.assertEqual(app['environment']['VIGIRA_AGENT_RELEASE_TAG'], 'auto')
                self.assertTrue(any(mount['target'] == '/var/lib/vigira/agent-releases' for mount in app['volumes']))
                shutil.rmtree(self.production)


if __name__ == '__main__':
    unittest.main()
