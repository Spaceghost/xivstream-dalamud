# CPU-policy package regression

The policy, its embedded service templates, and the CPU selection commands ship
in the normal `xivstream` binary. There is no separate host-specific policy
script to copy. See [config.md](config.md#choosing-busy-and-idle-cpus) for selecting
cores; the IDs in those examples are not imposed by the package.

The Go tests cover accounting, failure-safe hysteresis, core validation and plan
steps. The Linux package regression additionally runs the actual packaged
executable through `cpu-config`, `plan --cpu-policy-only` and
`apply --cpu-policy-only --yes`. It uses bubblewrap user/mount/network namespaces,
an isolated writable `/etc`, fake online CPUs, and strict fake `systemctl` and
`incus` commands. No host service, container, game, or saved configuration is
modified. A denied user namespace fails the test; there is no unsandboxed fallback.

On a Linux development or disposable CI machine with Go, GoReleaser, Python 3,
bubblewrap, rpm, bsdtar (libarchive-tools), binutils, GNU tar and zstd installed:

On Ubuntu with restricted user namespaces, bubblewrap also needs an AppArmor profile allowing its namespace operations. CI prepares a trusted copy with its own profile only on the disposable GitHub-hosted runner; it keeps the system-wide restriction enabled. See [Ubuntu’s explanation](https://discourse.ubuntu.com/t/understanding-apparmor-user-namespace-restriction/58007). The tests retain their networkless namespaces and do not fall back to an unsandboxed run.

```sh
go test ./...
goreleaser release --snapshot --skip=publish --parallelism=1
bash tools/check_cpu_packages.sh dist
```

Use a fresh snapshot/worktree (and fresh `dist`) for the release command. This
does not publish, install, or start packages. The checker extracts only the named
binary and configuration documentation to a new temporary directory. It checks
Linux/amd64 portable, RPM, DEB, APK and Arch payloads for binary equality and the
current configuration documentation, rejects RPM install scripts, then runs nine
CLI regressions for each payload. Its printed temporary directory retains the
test logs and payload hashes. Other architectures are cross-built by the release
pipeline but not executed by this Linux/amd64 gate.

To test one already-built binary:

```sh
python3 tools/test_cpu_policy_package.py --binary ./xivstream --install-path /usr/bin/xivstream
python3 tools/test_cpu_policy_package.py --binary ./xivstream --install-path /usr/local/bin/xivstream
```

Cases include both installed service executable paths, mode-0600 configuration,
read-only planning, unchanged reapply, saved/partial busy and idle selections,
offline-core rejection, restoration after a failed Incus write with the service
already stopped, static-limit changes while disabled (including unlimited), and
refusing to restore limits after a failed service stop. Only the CPU-policy unit
is allowed by the fakes; an unrelated service/game/container command fails.

These tests validate the package and its control flow. They do not certify a real
host's performance, firmware-selected best cores, systemd permissions, or all
Incus/cgroup variants, and do not run an actual policy daemon or game. A running
healthy sampler need not be restarted merely to make a CPU-only CLI fix available;
an authorized binary-only update can leave existing processes and core choices
unchanged.
