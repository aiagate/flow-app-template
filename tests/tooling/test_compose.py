"""Contracts for independently named local Compose applications."""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]


class ComposeDefinitionTests(unittest.TestCase):
    """Keep application instances free of a shared container identity."""

    def test_compose_does_not_override_project_scoped_container_names(self) -> None:
        """Let Compose scope the service container to its project."""
        definition = (ROOT / "compose.yml").read_text(encoding="utf-8")
        self.assertNotRegex(definition, r"(?m)^\s*container_name\s*:")

    def test_host_port_is_configurable_with_the_existing_default(self) -> None:
        """Keep 5432 while permitting another application to publish elsewhere."""
        definition = (ROOT / "compose.yml").read_text(encoding="utf-8")
        self.assertIn('"${POSTGRES_HOST_PORT:-5432}:5432"', definition)

    def test_example_documents_the_default_host_port(self) -> None:
        """Expose the Compose port setting in the example environment."""
        example = (ROOT / ".env.example").read_text(encoding="utf-8")
        self.assertIn("POSTGRES_HOST_PORT=5432", example)


class ComposeConfigurationTests(unittest.TestCase):
    """Validate the actual Compose model without starting containers."""

    @classmethod
    def setUpClass(cls) -> None:
        """Require the Compose CLI on CI, but not on a developer's machine."""
        available = shutil.which("docker") is not None
        if available:
            version = subprocess.run(
                ["docker", "compose", "version"],
                capture_output=True,
                text=True,
                timeout=30,
                check=False,
            )
            available = version.returncode == 0
        if not available:
            if os.environ.get("GITHUB_ACTIONS") == "true":
                raise AssertionError("Docker Compose is required for CI validation")
            raise unittest.SkipTest("Docker Compose is unavailable locally")

    def configuration(self, project: str, port: int | None = None) -> dict[str, Any]:
        """Render an isolated project using only the requested example port."""
        environment = os.environ.copy()
        for key in (
            "POSTGRES_HOST_PORT",
            "COMPOSE_PROJECT_NAME",
            "COMPOSE_FILE",
            "COMPOSE_PROFILES",
            "COMPOSE_ENV_FILES",
            "COMPOSE_DISABLE_ENV_FILE",
        ):
            environment.pop(key, None)
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "compose.env"
            env_file.write_text(
                "" if port is None else f"POSTGRES_HOST_PORT={port}\n",
                encoding="utf-8",
            )
            rendered = subprocess.run(
                [
                    "docker",
                    "compose",
                    "--project-directory",
                    str(ROOT),
                    "--env-file",
                    str(env_file),
                    "-f",
                    str(ROOT / "compose.yml"),
                    "-p",
                    project,
                    "config",
                    "--format",
                    "json",
                ],
                capture_output=True,
                text=True,
                timeout=30,
                check=True,
                env=environment,
            )
        result: dict[str, Any] = json.loads(rendered.stdout)
        return result

    def test_unset_host_port_keeps_5432(self) -> None:
        """Preserve the existing default for a single local application."""
        config = self.configuration("flow-default-port")
        service = config["services"]["postgres"]
        self.assertEqual(str(service["ports"][0]["published"]), "5432")
        self.assertEqual(service["ports"][0]["target"], 5432)

    def test_projects_have_distinct_service_identity_ports_and_volumes(self) -> None:
        """Separate two applications at the rendered Compose model boundary."""
        first = self.configuration("flow-app-one", 5543)
        second = self.configuration("flow-app-two", 5544)
        self.assertNotEqual(first["name"], second["name"])
        for config, port in ((first, 5543), (second, 5544)):
            service = config["services"]["postgres"]
            self.assertNotIn("container_name", service)
            self.assertEqual(str(service["ports"][0]["published"]), str(port))
        self.assertNotEqual(
            first["volumes"]["postgres-data"]["name"],
            second["volumes"]["postgres-data"]["name"],
        )


if __name__ == "__main__":
    unittest.main()
