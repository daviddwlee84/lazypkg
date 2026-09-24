#!/usr/bin/env python3
"""Network integration check using disposable uv tool directories only.

Usage: python3 scripts/integration_uv.py --mpm /absolute/path/to/mpm
Requires uv and mpm 8.0.1; installs/removes pycowsay under a TemporaryDirectory.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mpm", required=True)
    args = parser.parse_args()
    mpm = Path(args.mpm).resolve(strict=True)
    if not shutil.which("uv"):
        parser.error("uv is required")
    repo = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="lazypkg-uv-integration-") as tmp:
        work = Path(tmp)
        binary = work / ("lazypkg.exe" if os.name == "nt" else "lazypkg")
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/lazypkg"], cwd=repo, check=True)
        config = work / "config.toml"
        config.write_text('managers = ["uvx"]\ntimeout_seconds = 45\n')
        env = os.environ.copy()
        env.update(UV_TOOL_DIR=str(work / "tools"), UV_TOOL_BIN_DIR=str(work / "bin"),
                   UV_CACHE_DIR=str(work / "cache"), UV_PYTHON_INSTALL_DIR=str(work / "python"),
                   XDG_CONFIG_HOME=str(work / "config"), XDG_DATA_HOME=str(work / "data"))

        def run(*command):
            result = subprocess.run([str(binary), "--mpm", str(mpm), "--config", str(config),
                                     *command, "--json"], env=env, cwd=work, text=True,
                                    capture_output=True, timeout=180)
            if result.returncode:
                raise AssertionError(f"{command}: exit {result.returncode}\n{result.stderr}\n{result.stdout}")
            return json.loads(result.stdout)

        assert run("list", "--manager", "uv")["packages"] == []
        plan = run("install", "pycowsay", "--manager", "uv", "--dry-run")
        assert plan["request"]["manager"] == "uvx"
        assert run("list", "--manager", "uv")["packages"] == [], "preview installed a tool"
        result = run("install", "pycowsay", "--manager", "uv", "--yes")
        assert result["steps"][0]["status"] == "success", result
        installed = run("list", "--manager", "uv")
        assert [p["id"] for p in installed["packages"]] == ["pycowsay"], installed
        assert installed["packages"][0]["commands"] == ["pycowsay"], installed
        result = run("remove", "pycowsay", "--manager", "uv", "--yes")
        assert result["steps"][0]["status"] == "success", result
        assert run("list", "--manager", "uv")["packages"] == []
        print("PASS: preview → isolated uv install → inventory/entrypoint verification → remove → empty inventory")


if __name__ == "__main__":
    main()
