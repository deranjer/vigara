"""Exercise the interactive Linux installer with an isolated Docker stub.

No containers or host networks are changed. Run: python3 scripts/test_setup.py
"""
import os
from pathlib import Path
import shutil
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
  'volume ls '*) [[ ${EXISTING_VOLUME:-0} != 1 ]] || echo existing-volume;;
  'volume inspect '*) exit 1;;
  'network inspect --format '*) echo caddy-proxy;;
  'network inspect '*) [[ ${FAIL_NETWORK:-0} != 1 ]];;
  'inspect --format '*) echo true;;
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
        self.deploy = self.root / "deploy"
        self.deploy.mkdir()
        for name in ("setup.sh", "compose.staging.yml", "compose.caddy.example.yml"):
            shutil.copy(ROOT / "deploy" / name, self.deploy / name)
        binary = self.root / "bin"
        binary.mkdir()
        (binary / "docker").write_text(DOCKER)
        (binary / "docker").chmod(0o700)
        # Make the occupied-port check independent of the host running the test.
        (binary / "ss").write_text('#!/usr/bin/env bash\n[[ ${BUSY_PORT:-0} != 1 ]] || echo LISTEN\n')
        (binary / "ss").chmod(0o700)
        (binary / "curl").write_text('#!/usr/bin/env bash\nprintf "health check passed\\n"\n')
        (binary / "curl").chmod(0o700)
        self.calls = self.root / "calls"
        self.environment = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"],
                                CALLS=str(self.calls))

    def run_setup(self, answers, **environment):
        return subprocess.run(["bash", str(self.deploy / "setup.sh")], input=answers,
                              text=True, capture_output=True, cwd=self.root,
                              env=dict(self.environment, **environment))

    def test_direct_configuration_and_rerun(self):
        result = self.run_setup('\n\nn\n\n\nn\nn\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        production = self.deploy / "production"
        env = (production / ".env").read_text()
        self.assertIn('VIGARA_PORT=8080', env)
        self.assertIn('VIGARA_COOKIE_SECURE=false', env)
        self.assertRegex(env, r'POSTGRES_PASSWORD=[a-f0-9]{64}\n')
        self.assertRegex(env, r'VIGARA_BACKUP_MASTER_KEY=[A-Za-z0-9+/]{43}=\n')
        self.assertEqual((production / ".env").stat().st_mode & 0o777, 0o600)
        compose = (production / "compose.yml").read_text()
        self.assertIn('env_file: .env', compose)
        self.assertIn('${VIGARA_BIND_ADDRESS:-127.0.0.1}', compose)
        self.assertNotIn(' up -d ', self.calls.read_text())
        self.assertNotIn('pull', self.calls.read_text())
        self.assertNotIn(env.splitlines()[0], result.stdout)
        self.assertNotEqual(self.run_setup('').returncode, 0)
        self.assertEqual((production / ".env").read_text(), env)

    def test_caddy_configuration(self):
        result = self.run_setup('\n\ny\nvigara.example.com\n\n\nn\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        production = self.deploy / "production"
        self.assertIn('VIGARA_DOMAIN=vigara.example.com', (production / ".env").read_text())
        compose = (production / "compose.yml").read_text()
        self.assertIn('caddy.reverse_proxy:', compose)
        self.assertIn('VIGARA_BACKUP_MASTER_KEY:', compose)
        self.assertNotIn('ports:', compose)

    def test_input_rejection(self):
        for answers in ('../bad\n', '\nimage$bad\n', '\n\ny\nhttps://bad.example\n',
                        '\n\nn\n999.0.0.1\n', '\n\nn\n\n65536\n'):
            with self.subTest(answers=answers):
                self.assertNotEqual(self.run_setup(answers).returncode, 0)
                self.assertFalse((self.deploy / "production").exists())

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
                self.assertFalse((self.deploy / "production").exists())
                self.assertEqual(list(self.deploy.glob('.setup.*')), [])

    def test_pull_failure_preserves_config(self):
        result = self.run_setup('\n\nn\n\n\nn\ny\n', FAIL_PULL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('docker login ghcr.io', result.stderr)
        self.assertTrue((self.deploy / 'production' / '.env').exists())
        self.assertNotIn(' up -d ', self.calls.read_text())

    def test_start_failure_preserves_config(self):
        result = self.run_setup('\n\nn\n\n\nn\ny\n', FAIL_UP="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('logs --tail 100', result.stderr)
        self.assertTrue((self.deploy / 'production' / '.env').exists())
        self.assertIn('up -d --wait --wait-timeout 120', self.calls.read_text())

    def test_custom_configuration_and_start(self):
        result = self.run_setup('vigara-prod\nexample/app:fixed\nn\n0.0.0.0\n18080\ny\ny\n',
                                VIGARA_PORT='9999', VIGARA_IMAGE='ignored/app:wrong')
        self.assertEqual(result.returncode, 0, result.stderr)
        env = (self.deploy / 'production' / '.env').read_text()
        self.assertIn('VIGARA_COOKIE_SECURE=true', env)
        self.assertIn('VIGARA_PORT=18080', env)
        self.assertIn('VIGARA_IMAGE=example/app:fixed', env)
        calls = self.calls.read_text()
        self.assertIn('-p vigara-prod', calls)
        self.assertLess(calls.index(' pull'), calls.index(' up -d'))
        self.assertIn('Open your external HTTPS URL', result.stdout)
        self.assertNotIn(' down', calls)

    def test_early_eof(self):
        self.assertNotEqual(self.run_setup('').returncode, 0)
        self.assertFalse((self.deploy / 'production').exists())

    def test_generated_files_with_real_compose(self):
        # The other tests never require real Docker. CI also parses both generated
        # deployments with Compose, which does not require a running daemon.
        available = subprocess.run(['docker', 'compose', 'version'], capture_output=True)
        if available.returncode:
            self.skipTest('A real Docker Compose CLI is unavailable')
        for answers in ('\n\nn\n\n\nn\nn\n', '\n\ny\nvigara.example.com\n\n\nn\n'):
            with self.subTest(answers=answers):
                result = self.run_setup(answers)
                self.assertEqual(result.returncode, 0, result.stderr)
                production = self.deploy / 'production'
                result = subprocess.run(['bash', str(production / 'compose.sh'), 'config', '--quiet'],
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                shutil.rmtree(production)


if __name__ == '__main__':
    unittest.main()
