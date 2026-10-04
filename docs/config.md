# Configuration

`xivstream wizard` writes this file; `xivstream apply` reads it. Keys you
leave out take the defaults shown. Empty values are detected when apply runs.

Location: `/etc/xivstream/config.toml` (root on Linux), `~/.config/xivstream/config.toml`
(other users), `%ProgramData%\xivstream\config.toml` (Windows),
`~/Library/Application Support/xivstream/config.toml` (macOS), or `--config PATH` /
`$XIVSTREAM_CONFIG`.

```toml
topology = "incus"            # "incus": an Incus container on this Linux host; "host": this machine
backend  = "sunshine"         # "sunshine", "wolf" (Linux host, experimental), "selkies" (Linux, experimental)

[game]
launcher = "xivlauncher"
width = 1920
height = 1080
fps = 60                      # also DXVK's frame cap
process = "ffxiv_dx11.exe"    # its presence starts and ends GPU sharing

[stream]
name = "ffxiv"                # the name Moonlight shows
listen_address = ""           # "" = this host's Tailscale IPv4, else its primary address
port_base = 47989             # Moonlight's layout: https -5, webui +1, video +9, control +10, audio +11, mic +13, rtsp +21
max_bitrate_kbps = 50000      # a cap whatever the client asks for
codecs = "h264"               # "h264" or "auto" (HEVC/AV1 when the client can)
gamepad = "x360"              # "x360", "ds5", "auto"
web_user = "ffxiv"            # Sunshine's web UI
web_password = ""             # "" = generated once, kept in the state directory

[session]
headless = true               # a dedicated headless sway; false streams your desktop (host only)
user = "player"               # the session user (created if missing)

[incus]                       # topology = "incus"
container = "ffxiv"
image = "images:fedora/43"
cpu = ""                      # limits.cpu, "" = unlimited
memory = ""                   # limits.memory, "" = unlimited
gpu = ""                      # PCI address, "" = the first discrete GPU
autostart = true              # start at boot once the listen address exists

[incus.cpu_policy]            # optional; both CPU selections enable it (Wolf reads them too)
busy_cpus = ""                # e.g. "4-7" pins four cores, or "4" allows any four
idle_cpus = ""                # e.g. "0-7" pins all eight, or "8" allows any eight
busy_threshold_percent = 25   # non-game share of total machine CPU
idle_threshold_percent = 10   # must stay below the busy threshold
busy_after_seconds = 15       # sustained load before restricting the game
idle_after_seconds = 90       # sustained idle before restoring its CPUs

[wolf]                        # backend = "wolf" (topology = "host"); see docs/backends.md
image = ""                    # the session image; "" = built from images/session as localhost/xivstream-session:<hash>
server_image = ""             # Wolf; "" = the digest this release was tested with
autostart = true              # start Wolf at boot ([Install] in its Quadlet); apply starts it too
home = "/var/lib/xivstream/home"   # the game's home, mounted at /home/player in the session
home_device = ""              # "UUID=<btrfs uuid>" or a /dev path; "" = the filesystem of the Incus "default" pool
home_subvolume = "xivstream-home"  # the home's btrfs subvolume (top level of home_device)
network = "xivstream"         # the session's Podman bridge (no DNS; bridge interface xivstream0)
subnet = "10.89.10.0/24"      # its gateway, the first address, carries the forwards
app_ip = "10.89.10.10"        # the session's fixed address
legacy_address = ""           # the old Incus game container's address; "" = its eth0 ipv4.address at apply
allowed_clients = []          # tailnet addresses or ranges that may reach Wolf; [] = anything on tailscale0
mounts = [                    # extra "host:container[:ro]" mounts; ones whose source is missing are skipped
  "/home/jack/.local/bin/claude:/usr/local/bin/claude:ro",
  "/home/jack/.claude.json:/home/player/.claude.json",
  "/home/jack/.claude:/home/player/.claude",
  "/home/jack/.local/bin/codex:/usr/local/bin/codex:ro",
  "/home/jack/.codex:/home/player/.codex",
]
memory = "12g"                # the session container's memory limit ("" = none)
cpus_busy = ""                # the game's CPU list while the host is busy; "" = [incus.cpu_policy] busy_cpus
cpus_idle = ""                # and while it is idle; "" = [incus.cpu_policy] idle_cpus
zero_copy = false             # Wolf's DMA-BUF to CUDA path (NVIDIA multi-session crashes: wolf#501, #265)
codecs = "auto"               # "auto" (HEVC when Wolf and the client can) or "h264" (Wolf offers H.264 only)

[[wolf.forwards]]             # the session's 127.0.0.1:<port> reaches <target> (defaults: the Incus proxy devices)
name = "almanac-gateway"
port = 41881
target = "100.100.110.16:41881"
# ... bak-agent 7787 -> 100.104.232.33:7777, alienware-agent 7788 -> 100.83.231.2:7777,
#     fedora-agent 7789 -> 100.100.110.16:7788, voice-whisper 8178 -> 100.83.231.2:8178,
#     voice-mic 4713 -> 100.83.231.2:4713 (also the session's PulseAudio microphone tunnel)

[wolf.inbound]                # peers reaching legacy_address on these ports reach the session
ports = [41800, 41881, 7777]
peers = ["100.83.231.2", "100.104.232.33", "10.61.200.1"]

[selkies]                     # backend = "selkies"
port = 8080
user = "ffxiv"
password = ""                 # "" = generated once

[audio]
stream_only = true            # nothing plays aloud on this machine

[gpu_share]
mode = "none"                 # "reserve" (almanac), "stop-unit" (e.g. Ollama), "none"
container = ""                # Incus container the model server runs in, "" = this machine
gateway = "http://127.0.0.1:41881"
token_file = ""               # almanac's gateway token
owner = "ffxiv"               # the reservation's name in almanac
margin = 1.25                 # reserve the game's peak VRAM use times this
unit = ""                     # stop-unit: the service to stop while playing
grace_seconds = 20            # after the game exits, before the room is given back

[mods]
repos = ["https://spacegho.st/mods/ffxiv/plugins.json"]
install = []                  # plugin InternalNames, e.g. ["GhosttyDalamud", "Almanac"]
testing = false               # testing versions where a plugin has one
companions = true             # e.g. ghostty-agent for GhosttyDalamud
```

State directory (generated passwords, the game's measured VRAM peak):
`/var/lib/xivstream` as root, `~/.local/state/xivstream` otherwise.

## Choosing busy and idle CPUs

On an Incus Linux host, first list the CPU IDs and their physical cores:

```sh
xivstream cpu-config --list
sudo xivstream cpu-config --busy 4-7 --idle 0-7
sudo xivstream apply --cpu-policy-only --yes
```

The numbers above are examples for an eight-CPU machine. A bare `4` means any four logical CPUs;
`4-7` or `0,2,4,6` selects specific IDs. Use `0-0` to select just CPU 0. SMT siblings share a
physical core; consult the CORE column before choosing. Both selections are checked against online CPUs.
These settings are also available in `wizard --advanced` and are stored in the main TOML file.

CPU-only apply is supported on systemd hosts and does not start or restart the container. Normal apply
also installs the service; OpenRC and the NixOS module provide it too. The policy starts with the busy
selection and expands only after a fresh idle window. CPU use from the entire game container, including
Sunshine, is subtracted before evaluating host load. These are CPU restrictions, not memory/GPU limits;
other processes can still use the selected CPUs. The service does not create exclusive core reservations.

On standard local Incus cgroup-v2 layouts, each sample reads the kernel's
`lxc.payload.<container>/cpu.stat` counter (including descendants), avoiding a full
daemon state query every five seconds. Other layouts retain a four-second-bounded
Incus fallback. Missing, stale or reset measurements immediately request the busy
selection and restart the idle window; they never count as idle time.

Read `/run/xivstream-cpu-policy/status` for the mode, measurement and selected CPUs.
It includes `updated_at`, `sample_source`, and `sample_error`; `limits_cpu` is the
last confirmed policy setting, not a fresh daemon query. A failed limit change is
reported and retried without restarting the container. To disable the policy
and restore the static `[incus] cpu` setting on systemd:

```sh
sudo xivstream cpu-config --disable
sudo xivstream apply --cpu-policy-only --yes
```

Stopping the policy and restoring the static allocation are separate checked
steps. If Incus rejects the allocation after the service stops, rerun the same
apply command: it retries the allocation even though the service is already
disabled. It also applies later changes to `[incus] cpu` while the policy is off.

## The Wolf backend's [wolf] section

The defaults reproduce the Incus game container they replace on the machine this was built
for: its proxy devices (`forwards`), its tailnet ACL (`inbound`), its memory limit and its
temporary claude/codex disk devices (`mounts`). Change them for another machine. A list you
set replaces the default list.

`[wolf]` is only checked when `backend = "wolf"`. With Wolf, `[incus.cpu_policy]` stays valid:
game-cpu-fence gives the game's Podman scope `cpus_busy` (else `busy_cpus`) and switches to
`cpus_idle` (else `idle_cpus`) after the host has been idle for `idle_after_seconds`, with the same
thresholds as the Incus policy. Lists only (`4-7`, `0,2,4,6`): a bare count cannot pin a scope.

`legacy_address`, when empty, is read from Incus at apply time; set it to keep Incus out of the
picture. The services Wolf's units run (`wolf-preflight`, `wolf-config`) never call incusd.

Every command reads `$XIVSTREAM_CONFIG` when `--config` is not given; the Sunshine fallback
unit runs `start-container` and `stop-container` with `XIVSTREAM_CONFIG=/etc/xivstream/sunshine.toml`.
