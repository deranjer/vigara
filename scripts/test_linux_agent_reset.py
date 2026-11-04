"""Run the Linux installer against temporary state and mocked host services."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class LinuxResetTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bundle = self.root / 'bundle'
        self.bundle.mkdir()
        self.state = self.root / 'state'
        self.legacy = self.root / 'legacy'
        self.state.mkdir()
        self.legacy.mkdir()
        self.saved = {'agentId': 'old-agent', 'credential': 'old-credential', 'serverUrl': 'https://old.invalid', 'queuedReports': ['keep-until-reset']}
        (self.state / 'agent.json').write_text(json.dumps(self.saved))
        self.bootstrap = {'serverUrl': 'https://new.invalid', 'token': 'test-only', 'name': 'New device', 'reinstall': False}
        self.write_bootstrap()
        self.binary = self.root / 'bin'
        self.binary.mkdir()
        (self.root / 'installed').mkdir()
        (self.root / 'systemd').mkdir()
        script = (ROOT / 'apps/api/internal/agent/installers/linux.sh').read_text()
        for original, replacement in {
            '/var/lib/vigara-agent': str(self.state), '/var/lib/vigira-agent': str(self.legacy),
            '/usr/local/bin': str(self.root / 'installed'), '/etc/systemd/system': str(self.root / 'systemd'),
        }.items(): script = script.replace(original, replacement)
        (self.bundle / 'install.sh').write_text(script)
        shutil.copy(ROOT / 'apps/api/internal/agent/installers/docker_linux.py', self.bundle / 'configure-docker.py')
        shutil.copy(ROOT / 'apps/api/internal/agent/installers/linux.service', self.bundle / 'vigara-agent.service')
        agent = '''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
state=Path(sys.argv[sys.argv.index('--config')+1])
if os.getenv('EXPECT_USER') and os.getenv('AGENT_RUN_AS') != os.getenv('EXPECT_USER'):
 sys.exit('Enrollment used the wrong state owner')
if 'enroll' in sys.argv:
 if os.getenv('FAIL_ENROLL'): sys.exit(1)
 if os.getenv('EXPECT_FRESH') and state.exists(): sys.exit('Old state reached enrollment')
 state.write_text(json.dumps({'agentId':'new-agent','credential':'new-credential','serverUrl':'https://new.invalid'}))
'''
        (self.bundle / 'vigara-agent').write_text(agent)
        self.command('sudo', '''#!/usr/bin/env python3
import os,subprocess,sys
args=sys.argv[1:]
if args[0]=='-u':
 os.environ['AGENT_RUN_AS']=args[1]
 args=args[2:]
if args[0]=='chown': sys.exit(0)
if args[0]=='install':
 filtered=[]
 i=0
 while i<len(args):
  if args[i] in ('-o','-g'): i+=2
  else: filtered.append(args[i]); i+=1
 args=filtered
sys.exit(subprocess.run(args).returncode)
''')
        self.command('id', '#!/bin/sh\n[ "$2" != vigira-agent ]\n')
        self.command('getent', '#!/bin/sh\nexit 0\n')
        self.command('systemctl', '''#!/usr/bin/env python3
import os,sys
from pathlib import Path
root=Path(os.environ['FIXTURE'])
args=sys.argv[1:]
unit=args[-1]
state=root/(unit+'.running')
with (root/'service-calls').open('a') as f: f.write(' '.join(args)+'\\n')
if args[0]=='is-active': sys.exit(0 if state.exists() else 1)
if args[0]=='cat': sys.exit(1)
if args[0]=='show':
 print(os.getenv('SERVICE_USER','vigara-agent'))
 sys.exit(0)
if args[0]=='stop': state.unlink(missing_ok=True)
if args[0] in ('start','enable'):
 if os.getenv('FAIL_START') and args[0]=='enable': sys.exit(1)
 state.touch()
''')
        (self.root / 'vigara-agent.service.running').touch()
        self.environment = dict(os.environ, PATH=str(self.binary) + os.pathsep + os.environ['PATH'], FIXTURE=str(self.root))

    def command(self, name, content):
        path = self.binary / name
        path.write_text(content)
        path.chmod(0o700)

    def write_bootstrap(self):
        (self.bundle / 'bootstrap.json').write_text(json.dumps(self.bootstrap))

    def install(self, *options, answer='', **env):
        return subprocess.run(['sh', str(self.bundle / 'install.sh'), *options], input=answer, capture_output=True, text=True,
                              env=dict(self.environment, **env))

    def assert_saved(self):
        self.assertEqual(json.loads((self.state / 'agent.json').read_text()), self.saved)

    def test_default_preserves_identity_and_settings(self):
        result = self.install('--non-interactive')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_saved()
        self.assertEqual(list(self.state.glob('agent.before-reset-*.json')), [])

    def test_reinstall_renews_with_existing_root_service_account(self):
        self.bootstrap.update(reinstall=True, agentId='old-agent')
        self.write_bootstrap()
        result = self.install('--non-interactive', SERVICE_USER='root', EXPECT_USER='root')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.state / 'agent.json').read_text())['agentId'], 'new-agent')

    def test_reinstall_keeps_unprivileged_service_account(self):
        self.bootstrap.update(reinstall=True, agentId='old-agent')
        self.write_bootstrap()
        result = self.install('--non-interactive', EXPECT_USER='vigara-agent')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_selected_deploy_uses_root_for_enrollment(self):
        self.bootstrap.update(reinstall=True, agentId='old-agent', docker={'mode': 'deploy'})
        self.write_bootstrap()
        (self.bundle / 'configure-docker.py').write_text('pass\n')
        result = self.install('--non-interactive', EXPECT_USER='root')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_confirmed_reset_backs_up_and_reenrolls(self):
        result = self.install('--reset-enrollment', answer='RESET\n', EXPECT_FRESH='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.state / 'agent.json').read_text())['agentId'], 'new-agent')
        backups = list(self.state.glob('agent.before-reset-*.json'))
        self.assertEqual(len(backups), 1)
        self.assertEqual(json.loads(backups[0].read_text()), self.saved)
        self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)
        self.assertFalse((self.bundle / 'bootstrap.json').exists())

    def test_declined_or_invalid_reset_leaves_state_untouched(self):
        for answer in ('', '\n', 'reset\n', 'RESET wrong\n'):
            with self.subTest(answer=answer):
                result = self.install('--reset-enrollment', answer=answer)
                self.assertNotEqual(result.returncode, 0)
                self.assert_saved()
                self.assertEqual(list(self.state.glob('agent.before-reset-*.json')), [])

    def test_reset_rejects_noninteractive_and_reinstall_bundles(self):
        result = self.install('--reset-enrollment', '--non-interactive', answer='RESET\n')
        self.assertNotEqual(result.returncode, 0)
        self.assert_saved()
        self.bootstrap['reinstall'] = True
        self.write_bootstrap()
        result = self.install('--reset-enrollment', answer='RESET\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('fresh Add device bundle', result.stderr)
        self.assert_saved()

    def test_failed_enrollment_restores_identity_and_running_service(self):
        result = self.install('--reset-enrollment', answer='RESET\n', FAIL_ENROLL='1', EXPECT_FRESH='1')
        self.assertNotEqual(result.returncode, 0)
        self.assert_saved()
        self.assertTrue((self.root / 'vigara-agent.service.running').exists())

    def test_service_failure_keeps_new_credentials(self):
        result = self.install('--reset-enrollment', answer='RESET\n', FAIL_START='1', EXPECT_FRESH='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads((self.state / 'agent.json').read_text())['agentId'], 'new-agent')

    def test_reset_requires_a_fresh_bootstrap(self):
        (self.bundle / 'bootstrap.json').unlink()
        result = self.install('--reset-enrollment', answer='RESET\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('fresh Add device bundle', result.stderr)
        self.assert_saved()

    def test_legacy_state_is_not_migrated_into_a_reset(self):
        (self.state / 'agent.json').rename(self.legacy / 'agent.json')
        result = self.install('--reset-enrollment', answer='RESET\n', EXPECT_FRESH='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.state / 'agent.json').read_text())['agentId'], 'new-agent')
        self.assertEqual(json.loads((self.legacy / 'agent.json').read_text()), self.saved)


if __name__ == '__main__': unittest.main()
