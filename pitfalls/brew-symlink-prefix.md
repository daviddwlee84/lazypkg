# Homebrew rejects its own Cellar after resolving the brew symlink

Symptom: `selected brew does not own the executable's Cellar; select its owning brew`
after a successful Linuxbrew formula installation.

Homebrew derives HOMEBREW_PREFIX from its invoked `$0` path before resolving the
symlink to its repository script. Running the canonical Homebrew/bin/brew target
instead of the selected prefix/bin/brew changes the prefix and therefore Cellar.

Keep Plan.BrewPath as the selected absolute invocation path. Fingerprint the
resolved file separately and verify that the invocation symlink still resolves
to that exact file before applying. Do not weaken the receipt or Cellar checks,
add prefix environment overrides, or update a different Homebrew installation.

The symlinked-manager regression fixture requires the logical invocation path,
then retargets the symlink and verifies refusal. The central tap additionally
runs each installed binary's upgrade --check JSON against real Linuxbrew.
Found by tap CI 36096538191 on 2026-09-25. Earlier published assets are immutable;
this correction ships in a new patch release.
