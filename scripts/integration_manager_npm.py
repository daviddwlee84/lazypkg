#!/usr/bin/env python3
"""Opt-in network test of manager repair in a disposable mise/Node environment.

Requires Go (unless --binary is supplied), mise with aqua:npm/cli support,
mpm 8.0.1, and an existing Node installation with bundled npm. Copies Node and
npm into private directories, then downloads only an independent npm there.
No host tool, shell profile, npm prefix, or mise configuration is modified.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def digest(path: Path) -> str:
    if not path.exists():
        return "absent"
    value = hashlib.sha256()
    if path.is_symlink():
        value.update(os.readlink(path).encode())
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            value.update(block)
    return value.hexdigest()


def tree_digest(root: Path) -> str:
    value = hashlib.sha256()
    for path in sorted(root.rglob("*")):
        if path.is_file():
            value.update(str(path.relative_to(root)).encode())
            value.update(digest(path).encode())
    return value.hexdigest()


def checked(command: list[str], *, cwd: Path, env: dict[str, str], timeout: int = 180) -> str:
    result = subprocess.run(command, cwd=cwd, env=env, text=True,
                            capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"{command}: exit {result.returncode}\n{result.stderr}\n{result.stdout}")
    return result.stdout


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, help="Use an already built lazypkg executable")
    parser.add_argument("--mpm", default=shutil.which("mpm"))
    parser.add_argument("--mise", default=shutil.which("mise"))
    parser.add_argument("--node", default=shutil.which("node"))
    args = parser.parse_args()
    if os.name != "posix":
        parser.error("the verified aqua:npm/cli recipe currently supports macOS/Linux only")
    if not all((args.mpm, args.mise, args.node)):
        parser.error("existing mpm, mise and Node executables are required")
    repo = Path(__file__).resolve().parents[1]
    original_env = os.environ.copy()
    probe_env = dict(original_env, MISE_AUTO_INSTALL="0", MISE_NOT_FOUND_AUTO_INSTALL="false")
    source_node = Path(checked([args.node, "-p", "process.execPath"], cwd=repo, env=probe_env).strip()).resolve(strict=True)
    source_version = checked([str(source_node), "--version"], cwd=repo, env=probe_env).strip().removeprefix("v")
    source_root = source_node.parent.parent
    source_npm = source_root / "lib" / "node_modules" / "npm"
    if not (source_npm / "package.json").is_file():
        parser.error("selected Node must have a bundled lib/node_modules/npm/package.json")
    original_npm_version = json.loads((source_npm / "package.json").read_text())["version"]
    mise = Path(args.mise).resolve(strict=True)
    mpm = Path(args.mpm).resolve(strict=True)
    config_base = Path(original_env.get("XDG_CONFIG_HOME", str(Path.home() / ".config")))
    mise_config_dir = Path(original_env.get("MISE_CONFIG_DIR", str(config_base / "mise")))
    host_config = Path(original_env.get("MISE_GLOBAL_CONFIG_FILE", str(mise_config_dir / "config.toml")))
    host_paths = [source_node, source_root / "bin" / "npm", source_root / "bin" / "npx", host_config]
    host_before = {path: digest(path) for path in host_paths}
    host_npm_before = tree_digest(source_npm)

    try:
        with tempfile.TemporaryDirectory(prefix="lazypkg-manager-npm-") as temporary:
            work = Path(temporary).resolve()
            data = work / "mise-data"
            private_node = data / "installs" / "node" / source_version
            private_bin = private_node / "bin"
            private_bin.mkdir(parents=True)
            # Physical copy is essential: process.execPath and npm's default
            # prefix must refer to this private Node, not a symlink to the host.
            shutil.copy2(source_node, private_bin / "node")
            private_npm = private_node / "lib" / "node_modules" / "npm"
            shutil.copytree(source_npm, private_npm, symlinks=True)
            for name in ("npm", "npx"):
                source = source_root / "bin" / name
                target = private_bin / name
                if source.is_symlink():
                    resolved = source.resolve(strict=True)
                    if not resolved.is_relative_to(source_root):
                        raise RuntimeError(f"{name} resolves outside the selected Node installation")
                    private_target = private_node / resolved.relative_to(source_root)
                    target.symlink_to(os.path.relpath(private_target, target.parent))
                else:
                    shutil.copy2(source, target)
            tools = work / "tools"
            tools.mkdir()
            (tools / "mise").symlink_to(mise)
            global_config = work / "mise-config" / "config.toml"
            global_config.parent.mkdir()
            global_config.write_text(f"[tools]\nnode = {json.dumps(source_version)}\n")
            user_npmrc = work / "npmrc"
            global_npmrc = private_node / "etc" / "npmrc"
            global_npmrc.parent.mkdir()
            user_npmrc.write_text("")
            global_npmrc.write_text("")
            lazypkg_config = work / "lazypkg.toml"
            lazypkg_config.write_text('managers = ["npm", "mise"]\ntimeout_seconds = 60\n')
            env = {key: value for key, value in original_env.items()
                   if not key.upper().startswith(("MISE_", "NPM_CONFIG_"))}
            env.update(
                PATH=os.pathsep.join((str(private_bin), str(tools), original_env.get("PATH", ""))),
                MISE_DATA_DIR=str(data), MISE_STATE_DIR=str(work / "mise-state"),
                MISE_CACHE_DIR=str(work / "mise-cache"), MISE_CONFIG_DIR=str(global_config.parent),
                MISE_GLOBAL_CONFIG_FILE=str(global_config), MISE_TRUSTED_CONFIG_PATHS=str(work),
                MISE_CEILING_PATHS=str(work), MISE_AUTO_INSTALL="0", MISE_NOT_FOUND_AUTO_INSTALL="false",
                NPM_CONFIG_PREFIX=str(private_node), NPM_CONFIG_USERCONFIG=str(user_npmrc),
                NPM_CONFIG_GLOBALCONFIG=str(global_npmrc), NPM_CONFIG_CACHE=str(work / "npm-cache"),
                NPM_CONFIG_UPDATE_NOTIFIER="false", XDG_CONFIG_HOME=str(work / "xdg-config"),
                XDG_DATA_HOME=str(work / "xdg-data"), XDG_CACHE_HOME=str(work / "xdg-cache"),
                XDG_STATE_HOME=str(work / "xdg-state"), UV_TOOL_DIR=str(work / "uv-tools"),
                UV_CACHE_DIR=str(work / "uv-cache"), NO_COLOR="1",
            )
            binary = args.binary.resolve(strict=True) if args.binary else work / "lazypkg"
            if not args.binary:
                build_env = dict(probe_env, GOTOOLCHAIN="local")
                checked(["go", "build", "-o", str(binary), "./cmd/lazypkg"], cwd=repo, env=build_env)

            def cli(*arguments: str) -> dict:
                return json.loads(checked([str(binary), "--config", str(lazypkg_config),
                    "--mpm", str(mpm), "--json", *arguments], cwd=work, env=env, timeout=300))

            assert Path(checked([str(private_bin / "node"), "-p", "process.execPath"], cwd=work, env=env).strip()).resolve() == (private_bin / "node").resolve()
            copied_before = {name: digest(private_bin / name) for name in ("node", "npm", "npx")}
            bundled_before = tree_digest(private_npm)
            config_before = global_config.read_bytes()
            plan = cli("managers", "upgrade", "npm", "--dry-run")
            health = plan["manager_update"]
            if not health["apply_supported"]:
                raise RuntimeError(f"isolated npm repair is unavailable: {health}")
            target = health["candidate_version"]
            assert health["version"] == original_npm_version, health
            assert health["runtime_version"] == source_version, health
            assert Path(health["prefix"]).resolve() == private_node, health
            assert Path(health["config_path"]).resolve() == global_config, health
            assert target.split(".")[0] == original_npm_version.split(".")[0], health
            assert global_config.read_bytes() == config_before, "dry-run changed global config"
            assert tree_digest(private_npm) == bundled_before, "dry-run changed bundled npm"

            result = cli("managers", "upgrade", "npm", "--yes")
            assert result["steps"] and all(step["status"] == "success" for step in result["steps"]), result
            state = json.loads(checked([str(mise), "ls", "--global", "--json"], cwd=work, env=env))
            assert [entry["version"] for entry in state["node"]] == [source_version], state
            npm_records = state.get("npm", []) + state.get("aqua:npm/cli", [])
            assert any(entry["version"] == target for entry in npm_records), state
            new_root = Path(checked([str(mise), "where", f"aqua:npm/cli@{target}"], cwd=work, env=env).strip()).resolve()
            assert new_root.is_relative_to(data), new_root
            effective = Path(checked([str(mise), "which", "npm"], cwd=work, env=env).strip()).resolve()
            assert effective.is_relative_to(new_root), effective
            for name, previous in copied_before.items():
                assert digest(private_bin / name) == previous, f"copied {name} changed"
            assert tree_digest(private_npm) == bundled_before, "bundled npm was overwritten"
            assert (private_node / "lib" / "node_modules" / "npm" / "package.json").is_file()
            summary = f"PASS: isolated Node {source_version}; bundled npm {original_npm_version} retained; independent npm {target} pinned and selected; host files unchanged"
    finally:
        for path, previous in host_before.items():
            if digest(path) != previous:
                raise AssertionError(f"host file changed unexpectedly: {path}")
        if tree_digest(source_npm) != host_npm_before:
            raise AssertionError("host bundled npm changed unexpectedly")
    print(summary)


if __name__ == "__main__":
    main()
