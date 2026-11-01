"""Check deployment interpolation with Docker Compose; no daemon is required.

Run: python scripts/test_deployment_env.py
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]


class DeploymentEnvironmentTests(unittest.TestCase):
    def render(self, settings, downloads=False, caddy=False, success=True):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            for source in (ROOT / "deploy").glob("compose*.yml"):
                shutil.copy(source, directory / source.name)
            fixture = "POSTGRES_USER=vigira\nPOSTGRES_DB=vigira\nPOSTGRES_PASSWORD=test-only\n"
            (directory / ".env.staging").write_text(fixture)
            (directory / "config.env").write_text(
                fixture + "".join(f"{key}={value}\n" for key, value in settings.items())
            )
            command = ["docker", "compose", "-p", "env-test", "--env-file", "config.env",
                       "-f", "compose.caddy.example.yml" if caddy else "compose.staging.yml"]
            if downloads:
                command += ["-f", "compose.staging.downloads.example.yml"]
            command += ["config", "--format", "json"]
            environment = {key: value for key, value in os.environ.items()
                           if not key.startswith(("VIGARA_", "VIGIRA_", "POSTGRES_", "COMPOSE_"))}
            result = subprocess.run(command, cwd=directory, env=environment,
                                    capture_output=True, text=True)
            if not success:
                self.assertNotEqual(result.returncode, 0)
                return result.stderr
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads(result.stdout)

    def test_staging_defaults(self):
        app = self.render({})["services"]["app"]
        self.assertEqual(app["image"], "ghcr.io/deranjer/vigara:staging")
        self.assertEqual(app["ports"][0]["published"], "8080")
        self.assertEqual(app["environment"]["VIGARA_ADDR"], ":8080")
        self.assertIn("@db:5432/vigira", app["environment"]["VIGARA_DATABASE_URL"])

    def test_canonical_inputs(self):
        for prefixes in (("VIGARA_",), ("VIGARA_", "VIGIRA_")):
            with self.subTest(prefixes=prefixes):
                settings = {}
                for index, prefix in enumerate(prefixes):
                    settings.update({prefix + key: value for key, value in {
                        "IMAGE": f"example/app:test{index}", "PORT": str(18080 + index),
                        "DOMAIN": f"vigara{index}.example.com", "PROXY_NETWORK": f"web{index}",
                        "PUBLIC_URL": f"https://vigara{index}.example.com",
                        "AGENT_RELEASE_HOST_DIR": f"./releases{index}",
                    }.items()})
                app = self.render(settings, downloads=True)["services"]["app"]
                self.assertEqual(app["image"], "example/app:test0")
                self.assertEqual(app["ports"][0]["published"], "18080")
                self.assertEqual(app["environment"]["VIGARA_PUBLIC_URL"], "https://vigara0.example.com")
                self.assertEqual(app["environment"]["VIGARA_AGENT_RELEASE_DIR"], "/opt/vigira/releases")
                self.assertTrue(app["volumes"][-1]["source"].endswith("releases0"))
                config = self.render(settings, caddy=True)
                app = config["services"]["app"]
                self.assertEqual(app["image"], "example/app:test0")
                self.assertEqual(app["labels"]["caddy"], "vigara0.example.com")
                self.assertEqual(app["labels"]["caddy_ingress_network"], "web0")
                self.assertEqual(config["networks"]["proxy"]["name"], "web0")
                self.assertEqual(app["environment"]["VIGARA_COOKIE_SECURE"], "true")
                self.assertNotIn("ports", app)
                self.assertNotIn("ports", config["services"]["postgres"])

    def test_empty_optional_inputs_use_defaults(self):
        app = self.render({"VIGARA_PORT": "", "VIGARA_IMAGE": ""})["services"]["app"]
        self.assertEqual(app["ports"][0]["published"], "8080")
        self.assertEqual(app["image"], "ghcr.io/deranjer/vigara:staging")

    def test_required_settings(self):
        for settings in ({}, {"VIGARA_DOMAIN": "", "VIGIRA_DOMAIN": ""}):
            self.assertIn("VIGARA_DOMAIN", self.render(settings, caddy=True, success=False))
        self.assertIn("VIGARA_PUBLIC_URL", self.render({"VIGARA_AGENT_RELEASE_HOST_DIR": "./releases"}, downloads=True, success=False))
        self.assertIn("VIGARA_AGENT_RELEASE_HOST_DIR", self.render(
            {"VIGARA_PUBLIC_URL": "https://vigara.example.com"}, downloads=True, success=False))

    def test_caddy_mcp_environment(self):
        for origin in ('', 'https://vigara.example.com'):
            with self.subTest(origin=origin):
                app = self.render({'VIGARA_DOMAIN': 'vigara.example.com',
                                   'VIGARA_MCP_URL': origin}, caddy=True)['services']['app']
                self.assertEqual(app['environment']['VIGARA_MCP_URL'], origin)


if __name__ == "__main__":
    unittest.main()
