#!/usr/bin/env python3
"""Generate static adapter metadata without probing any installed executable.

Run in the environment pinned by catalog-requirements.txt. --check is read-only.
Runtime lazypkg embeds the output and never imports Python.
"""
import argparse
import inspect
import json
from importlib.metadata import version
from pathlib import Path

PIN = "8.0.1"
ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "internal/catalog/catalog.json"
GLOBAL = {
    "brew", "cask", "apt", "dnf", "pacman", "winget", "scoop", "choco",
    "npm", "uvx", "cargo", "mise", "flatpak", "snap", "pipx", "go", "gem", "rustup", "gh-ext",
}
ENVIRONMENT = {"uv", "pip", "pip2", "pip3", "pipxu", "conda", "mamba", "micromamba", "pixi"}
ECOSYSTEMS = {"pypi": "python", "npm": "node", "gem": "ruby", "cargo": "rust", "golang": "go", "deb": "debian", "rpm": "rpm", "alpm": "arch"}
SHELL_COMPONENTS = {"antidote", "antigen", "fisher", "oh-my-fish", "zim", "zinit", "zplug"}
HOSTED_COMPONENTS = {"lazy", "mason", "vim-pack", "emacs", "micro", "gh-ext"}
LAUNCHER_VERSION = {"vim-pack", "emacs", "micro", "gh-ext"}


def generate():
    actual = version("meta-package-manager")
    if actual != PIN:
        raise SystemExit(f"Expected meta-package-manager {PIN}, found {actual}")
    from meta_package_manager.pool import pool
    from meta_package_manager.capabilities import Operations, implements
    from meta_package_manager.specifier import PURL_MAP

    entries = []
    for backend_id in pool.all_manager_ids:
        manager = pool.get(backend_id)
        # Never access cli_path/version/fresh/available/install_root: they probe
        # the generator's host and do not belong in a platform-neutral artifact.
        public_id = "uv-pip" if backend_id == "uv" else backend_id
        scope = "global" if backend_id in GLOBAL else "environment" if backend_id in ENVIRONMENT else "unknown"
        groups = {label for typ, label in ECOSYSTEMS.items() if backend_id in (PURL_MAP.get(typ) or ())}
        if backend_id in {"rustup"}:
            groups.add("rust")
        if backend_id == "gh-ext":
            groups.add("extensions")
        if backend_id in {"mise", "asdf", "rustup", "volta"}:
            groups.add("runtimes")
        if backend_id in {"brew", "cask", "apt", "dnf", "pacman", "winget", "scoop", "choco", "flatpak", "snap"}:
            groups.add("system")
        groups.add(scope)
        source = getattr(manager, "definition_source", None)
        if source:
            filename = Path(source).name
        else:
            filename = Path(inspect.getfile(type(manager))).name
        requirement = manager.requirement or ""
        reason = f"mpm {PIN} requires {requirement}." if requirement else ""
        if backend_id == "npm":
            reason = "mpm 8.0.1 requires npm >=11.10.0 because that release introduced min-release-age; older npm silently ignores the release-age setting."
        entries.append({
            "id": public_id, "backend_id": backend_id,
            "name": "uv tools" if backend_id == "uvx" else "uv pip environment" if backend_id == "uv" else "gh extensions" if backend_id == "gh-ext" else manager.name,
            "requirement": requirement,
            "capabilities": [op.name for op in Operations if implements(manager, op)],
            "platforms": sorted(p.id for p in manager.platforms),
            "cli_names": list(manager.cli_names), "keywords": list(manager.keywords),
            "scope": scope, "groups": sorted(groups), "maintained": not manager.unmaintained,
            "maintenance": manager.unmaintained_message or manager.maintenance_note or "",
            "source_url": f"https://github.com/kdeldycke/meta-package-manager/blob/v{PIN}/meta_package_manager/managers/{filename}",
            "reason": reason,
            "component_kind": "shell" if backend_id in SHELL_COMPONENTS else "hosted" if backend_id in HOSTED_COMPONENTS else "executable",
            "version_subject": "launcher" if backend_id in LAUNCHER_VERSION else "component",
            "launcher": manager.cli_names[0] if manager.cli_names else "",
        })
    entries.sort(key=lambda entry: entry["id"])
    artifact = {"schema_version": 1, "backend_version": PIN, "managers": entries}
    return json.dumps(artifact, ensure_ascii=False, indent=2, sort_keys=True) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="Fail if the checked-in catalog differs")
    parser.add_argument("--output", type=Path, default=OUTPUT)
    args = parser.parse_args()
    result = generate()
    if args.check:
        if not args.output.exists() or args.output.read_text(encoding="utf-8") != result:
            raise SystemExit("Catalog differs; regenerate with the pinned requirements")
        print("Catalog matches pinned mpm " + PIN)
    else:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(result, encoding="utf-8")
        print(f"Wrote {args.output}")


if __name__ == "__main__":
    main()
