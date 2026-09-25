# Installation and upgrades

Version **v0.1.3** provides macOS and Linux binaries for amd64 and arm64. Go is
needed only for source builds. Other operating systems are not part of this
binary release. Runtime integrations remain separate from installing this CLI.

```sh
brew install daviddwlee84/tap/lazypkg
lazypkg --version
lazypkg self upgrade --check
lazypkg self upgrade --yes
```

The personal tap consumes complete verified GitHub Releases; a new tag can
precede formula publication. Homebrew owns its installed version and completion
files. Check mode inspects the running executable, receipt, formula and owning
Brew without upgrading or querying GitHub. Apply delegates only that formula,
then verifies the effective installed product/version. A current or pinned
formula can be unchanged. Failure may have partial manager effects; no rollback
or switch of installer is promised. Noninteractive/JSON apply requires `--yes`.

For standalone installs, download the matching `lazypkg_VERSION_OS_ARCH.tar.gz`
from [GitHub Releases](https://github.com/daviddwlee84/lazypkg/releases), verify its
exact entry in `checksums.txt`, and put the executable on PATH. Bash and Zsh
completion files are included under `completions/`; Homebrew installs them,
while manual archive users can place them in their shell's completion directory.

This release intentionally keeps standalone binary updates external: chezmoi's
`installPersonalTools` registry uses verified binaries on Linux and the personal
tap on macOS. `just upgrade-personal` preserves that installed owner, validates
candidates and refreshes completions. The in-program manager adapter does not
replace a standalone/local build. Never overwrite a Homebrew keg manually or
assume an unrelated PATH copy is the installed executable being updated.

Source installation remains available:

```sh
go install github.com/daviddwlee84/lazypkg/cmd/lazypkg@v0.1.3
# Update an existing Go installation through its original destination:
go install github.com/daviddwlee84/lazypkg/cmd/lazypkg@latest
```

`go install` uses `GOBIN` or `GOPATH/bin`; inspect `go env GOBIN GOPATH` if it is
not on PATH. A moved executable does not follow a later Go install automatically.
Source tags recover their module version; release archives inject v0.1.3; local
checkout builds retain a development identity. `@latest` selects a suitable
version tag, not necessarily the newest main-branch commit.

## Release contract

The tag workflow runs the native test suites and packaging checks before
publishing a complete draft. Each immutable release contains four binary
archives, one rootless source archive and `checksums.txt`. Archives contain only
the binary, MIT license and generated Bash/Zsh completions. Source archives
retain build inputs, tests, embedded assets and documentation, while excluding
development evidence. Go module ZIPs independently exclude evidence through
nested `go.mod` boundaries; neither mechanism rewrites existing Git history.

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish,announce
python3 scripts/release.py check --project lazypkg --binary lazypkg --smoke
python3 scripts/check-distribution.py
```

The publisher rejects incomplete sets, checksum/architecture/member mismatches,
and attempts to replace a published asset. Publish a new version for corrections.
The Homebrew tap is the single formula writer. Release automation does not need
a cross-repository token. Native Linux/macOS CI and macOS local verification are
reported separately; cross-builds alone do not prove terminal/backend behavior.
