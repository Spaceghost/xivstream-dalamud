# Ghostty bootstrap owner boundary

This covers the selected Incus/native-host Ghostty companion workflow and its
Ghostty plugin bundle plus Dalamud repository configuration. It does not turn
the general `plan.Target` writer, session setup, other plugin installers, or
other topologies into security-hardened arbitrary file APIs.

## Installation order and identity

`prepareGhostty` resolves a real non-root physical-host account (explicit config,
current UID, or matching `SUDO_USER`/`SUDO_UID`), validates labels/ports, and refuses
a non-local Incus default remote. It does not open user-owned paths.

The existing Incus plan installs its own current XivStream binary on the host
and container **before** creating/configuring the session account and before mod
steps. A packaged `/usr/bin/xivstream` is retained on the host; other host installs
and the container use `/usr/local/bin/xivstream`. The selected mod and companion
steps probe the helper after those prerequisites. A missing, old, or unsafe
helper fails closed; it is not assumed to exist on a fresh host. Plan evaluation
can report these checks as unknown until earlier steps have run.

The helper path and every ancestor must be root-owned and not group/world
writable. The only accepted installation alias is Fedora Atomic's root-owned
`/usr/local -> ../var/usrlocal`, with its target ancestors checked too. It is
never discovered through PATH, the working directory, or a user-provided binary.
It is invoked through absolute `timeout`, `runuser`, and `env -i` paths as the
explicit account. Its own UID/EUID must match and be non-root; home comes from
NSS, not environment. Incus helper execution explicitly selects the local daemon.

## Named operations

The private `internal-owner-file-v1` command accepts fixed object names, not
arbitrary filesystem paths. Agent binary, host/container user units, agent-owned
source tokens, copied tokens, defaults Lua, and Dalamud config have separate
size/type/mode/content rules. Source tokens are read-only to this helper.

Every user-path component below the account home is traversed using directory
FDs and `O_NOFOLLOW`; directories must belong to the selected UID and not be
group/world writable. Regular-file reads check UID, type, mode, single link,
capabilities, and size **before** content is read, with an additional read limit.
FIFOs do not block. Tokens are 16–1024 printable ASCII bytes (plus optional
newline), private mode 0600. Defaults and user units are bounded at 8 KiB,
Dalamud JSON at 4 MiB, and agent binaries at 64 MiB. Legacy owned 0644 units and
JSON can be read; new units, JSON, Lua, and tokens are written 0600. Agent binaries
are written 0755. Existing different binaries and units are never overwritten.

Writes use random exclusive 0600 temporary FDs, complete writes, checked fsync
and close, and same-directory atomic rename. The inode remains pinned through
publication checks and cleanup. Cleanup only attempts removal of the original
matching temporary inode, not an observed foreign replacement. No root process
chowns a temporary file to a user and then reopens its pathname. Credential
contents use inherited pipes only, never argv, environment, plans, or diagnostics.
Helper execution/output is bounded; failures are redacted.

The user unit sets `UMask=0077` and creates its custom token directory before
starting the agent. Bootstrap checks actual private token persistence, briefly
waiting only for confirmed absence; it never assumes listener/service startup
means the token was saved. Existing active services are not restarted. Foreign
units, proxies, or occupied ports are refused, not replaced or stopped.
Both bash and tmux are checked in the physical host; when container profiles are
requested, they are also checked inside the container. Missing tools produce a
clear prerequisite failure rather than a knowingly broken profile.

## Ghostty plugin and config

Only the selected Ghostty plugin uses the new bundle operation. The version is
four bounded numeric components; its destination is fixed beneath
`.xlcore/installedPlugins/GhosttyDalamud`. ZIP input and decoded bundle data are
bounded at 128 MiB, individual files at 64 MiB, with at most 1024 files and eight
path levels. The stdin tar adds a small bounded header allowance. Traversal,
special/link/PAX members, unsafe modes, duplicate members, Win32 reserved names,
and case-insensitive file/directory-prefix aliases are refused before staging.
The manifest must identify the exact Ghostty version and the DLL must have a PE
header. The input is never executed by the installer.

Files are written owner-private into a unique 0700 staging directory, then the
complete directory is published with `RENAME_NOREPLACE`. Exact existing payloads
are idempotent; different or partial versions are refused without deletion.
Failed staging is deliberately not recursively deleted through a pathname that
the owner might have replaced. Diagnostics identify the version and parent
`.xlcore/installedPlugins/GhosttyDalamud/.xivstream-bundle-*`; those private,
uninstalled leftovers may be inspected and removed by the owner.

Dalamud config is owner-read with a size/type/JSON check; unknown read failures
never become an empty config. New JSON is written privately, preserving unrelated
fields. The existing running-game refusal remains; no live game/plugin reload
is part of this installer change. Generic other-plugin paths remain unchanged.

## Guarantees and limits

The primary security boundary is eliminating privileged access through account-
controlled paths. The account is trusted with its own agent and credentials.
This is **not** isolation from a hostile process with the same UID, nor a
filesystem compare-and-swap: the same owner can still race the final check and
rename/unlink or change files after publication. Precommit failures preserve the
previous file; directory-fsync failure can report an error after atomic commit.
Power-loss durability depends on filesystem semantics. No guarantee is made
against malicious root, a compromised package, or a concurrent account edit.

The generic shipped Ghostty Lua consumer creates physical-host bash/tmux profiles
first and optional container profiles second. Existing private `init.lua` overrides
are intentionally preserved, not silently migrated. The matching Ghostty plugin
consumer must be released/installed for these defaults to take effect; source
tests are not proof of a stable release or in-game acceptance.

## Reproducible isolated gates

Run `go test ./...`, then build a Linux XivStream executable and run:

```sh
python3 tools/test_ghostty_owner_package.py --binary /absolute/private/xivstream
```

The package gate requires a non-root user and bubblewrap. It uses a synthetic NSS
account/home and an isolated network namespace, with no sudo, agents, services,
real credentials, or real bootstrap execution. There is no unsandboxed fallback.
Unit tests additionally inject deterministic temporary replacements, validate
archive refusal before staging, mock the complete companion reapply, and prove
that selected mod/config operations do not use privileged user-path access.
The Linux CI release dry-run also builds the current CLI and runs this gate.

An optional real-artifact compatibility fixture accepts existing local built
release files (it never downloads or executes those binaries):

```sh
export XIVSTREAM_FIXTURE_GHOSTTY_ZIP=/private/artifacts/GhosttyDalamud-0.3.2.0.zip
export XIVSTREAM_FIXTURE_GHOSTTY_PORTABLE=/private/artifacts/ghostty-agent-linux-x86_64.tar.gz
export XIVSTREAM_FIXTURE_GHOSTTY_OUTPUT=/private/fresh-fixture-directory
go test ./internal/steps -run '^TestGhosttyRealArtifactFixture$' -count=1 -v
python3 tools/test_ghostty_owner_package.py --binary /private/xivstream \
  --real-bundle "$XIVSTREAM_FIXTURE_GHOSTTY_OUTPUT/ghostty-bundle.tar" \
  --real-agent "$XIVSTREAM_FIXTURE_GHOSTTY_OUTPUT/ghostty-agent" \
  --real-version 0.3.2.0
```

The output directory must already exist and be private; fixture outputs use
exclusive creation, so use a fresh directory for a repeat. Replace the sample
version with the actual local built archive version. The Go fixture runs the
actual bounded ZIP/portable extractors; the Python gate sends the resulting
payloads through the compiled owner helper in the isolated home, verifies exact
bytes/private modes, and checks idempotent complete-bundle publication.
