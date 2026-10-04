# Streaming backends

xivstream sets up one of three streaming servers. This page is what each one
needs and how xivstream sets it up, with the upstream sources it follows
(checked 2026-09-23; Wolf 2026-10-04).

| | Sunshine | Wolf | Selkies |
|---|---|---|---|
| Clients | Moonlight (every platform) | Moonlight | any web browser |
| Host OS | Linux, Windows, macOS | Linux | Linux |
| Sessions | one, shared | one private session per client | one, shared |
| xivstream topologies | incus, host | host | incus, host |
| Status in xivstream | tested (NVIDIA, Incus) | experimental | starts and serves; picture unchecked |
| License | GPL-3.0 | MIT | MPL-2.0 |

## Sunshine

<https://github.com/LizardByte/Sunshine>

What xivstream does:

- **Linux headless (Incus or host).** The session is a headless sway, which implements
  wlr-screencopy (`capture = wlr`); gamescope does not, and a container has no scanout
  for kmsgrab. Sunshine is started by sway so it inherits the right `WAYLAND_DISPLAY`,
  and `adapter_name` is set at every start to the streaming GPU's render node.
- **NVIDIA.** `dmabuf-wait` is preloaded into Sunshine. NVIDIA's EGL ignores the implicit
  fence on screencopy dma-bufs, so under load Sunshine read frames sway had not finished
  copying, and the stream flickered black. The shim waits on the dma-buf before
  `eglCreateImage`. Sunshine's binary has file capabilities, so glibc runs it in secure-exec
  mode, where only a bare library name from the system directory is preloaded, set-user-ID.
- **Incus port forwards** use `nat=true`. Incus's userspace proxy dropped over 400k UDP
  video packets in a session, which looked like flicker.
- **Input in a container.** Sunshine's virtual devices appear in the host's `/dev/input`. A
  host udev rule hands just those nodes to the container's user, and `xivstream
  input-bridge` inside the container injects their uevents, so the container's udev and
  libinput see them. Incus's own `unix-hotplug` device is not used: in Incus 6.23 it deadlocks
  incusd.
- **Codecs and bitrate.** The defaults are H.264 only (some Linux clients show cycling colours
  on hardware HEVC decode) and a 50 Mbit/s cap (Wi-Fi clients lose packets above what their link
  carries). Both can be changed in the wizard's advanced mode.
- **Pads.** They are presented as Xbox 360 pads (`gamepad = x360`), which Wine reads over plain
  evdev. `gamepad = ds5` emulates a DualSense instead, so the touchpad and the PS button reach
  the game (Ghostty for FFXIV uses them for its terminal). Wine reads PlayStation pads through
  hidraw, and a container has none: the host's udev rule copies the streamed pad's hidraw node
  into `/dev/xivstream/<container>/` (under `/dev`, because `/run` is mounted `nodev`), Incus
  bind-mounts that at `/dev/hidraw-stream`, and the input bridge links each node into `/dev` and
  announces it to the container's udev. Only uhid devices with Sony's vendor id are passed: a
  real pad is on USB or Bluetooth, never uhid.
- **Install routes.**
  - Fedora: LizardByte's Copr.
  - Debian/Ubuntu and Arch: the release packages.
  - Bazzite: its bundled Sunshine.
  - SteamOS and other read-only systems: Flathub's `dev.lizardbyte.app.Sunshine`.
  - Windows: `winget install LizardByte.Sunshine`.
  - macOS: `brew install lizardbyte/homebrew/sunshine`.

## Wolf

<https://github.com/games-on-whales/wolf>, docs at <https://games-on-whales.github.io/wolf/stable/>

**Experimental:** xivstream's Wolf setup is generated, tested against golden files and Podman's
own Quadlet generator, but has not yet carried a real cutover. It runs with `topology = "host"`
only (Wolf runs its sessions as containers itself; inside an Incus container upstream supports
only a privileged LXC with nested Docker).

Each Moonlight client gets its own Wolf session: its own virtual compositor at that client's
resolution, its own encoder and its own virtual pads, with the game in a container Wolf starts
for it. There is one game home, so one game runs at a time; a second client gets a message
until the first quits.

What `xivstream apply` sets up, and why:

- **Wolf itself** is a rootful Podman Quadlet, `/etc/containers/systemd/wolf.container`
  (rootless Podman cannot create the device cgroup rules each session needs, issue #477). It
  follows Wolf's documented setup (host network, the Podman socket as `docker.sock`, `/dev` and
  `/run/udev`, `--ipc=host`, `--device-cgroup-rule "c 13:* rmw"`) with these changes:
  - The image is pinned by digest (`wolf.server_image`, default the `stable` build of
    2026-09-29, revision `facb8e0c`). Upstream has no versioned release after `v2024.07`.
  - NVIDIA uses Wolf's recommended driver volume, not CDI: CDI needs nvidia-container-toolkit
    (an rpm-ostree layer and a reboot on Atomic hosts), and under it libnvrtc can go missing so
    NVENC silently falls back to x264 (#482). `NVIDIA_DRIVER_VOLUME_NAME` makes Wolf mount the
    volume into every session too.
  - The NVIDIA nodes are required devices, created just before the container starts by
    `nvidia-modprobe` (`ExecStartPre`). Quadlet decides optional devices (`AddDevice=-...`)
    when it generates the unit, which at boot is before anything has created `nvidia-uvm`; the
    MIG-only `nvidia-caps` nodes are left out.
  - Wolf's sockets (its API, each session's Wayland and PulseAudio) live in `/run/wolf` on the
    host, mounted at Wolf's own runtime directory, so the sessions can be given them.
  - `WOLF_RENDER_NODE` is the streaming GPU's render node (Wolf's default is `renderD128`,
    which on a machine with an iGPU is the wrong card). `WOLF_USE_ZERO_COPY=FALSE` unless
    `wolf.zero_copy`: NVIDIA's zero-copy path crashes on a second session (#501, #265).
  - `Requires=` the Podman socket, the driver volume and the firewall units;
    `RequiresMountsFor=` the game's home, so Wolf never starts on an empty mountpoint;
    `Conflicts=` the Sunshine fallback; `Restart=on-failure` with a start limit.
  - `ExecStartPre=xivstream wolf-preflight` refuses to start while the Incus game container
    runs (two games on one home, and its port forwards would split the stream), checks the
    home is mounted, removes leftover session containers and merges the config.
    `ExecStopPost=xivstream wolf-cleanup` removes session containers too: Wolf removes one
    only when its session ends normally.
  - `[Install]` only with `wolf.autostart` (the default).
- **Wolf's config.** `xivstream wolf-config` (also run by the preflight) creates
  `/etc/wolf/cfg/config.toml` from Wolf's pinned default (v7) with a new uuid when it is
  missing, or when it is older than v7 (what earlier xivstream versions wrote, which Wolf's v0
  migration crashes on). Otherwise it merges: hostname, uuid, pairings, encoders and every
  other profile and app are kept, and only xivstream's app in the Moonlight profile is replaced.
  Two settings reach further: `wolf.codecs = "h264"` empties Wolf's HEVC and AV1 encoder lists
  (`"auto"` restores them), and paired clients without a pad override get `stream.gamepad`'s
  (`ds5` gives `PS`). Wolf UI and lobbies are left out: lobbies share one character between
  clients and have open crash and leak bugs (#364, #518, #490).
- **The session image** (`images/session`, built locally as
  `localhost/xivstream-session:<hash of its files>`) is Fedora 43, like the Incus guest, and
  deliberately not GoW's `base-app`: its `10-setup_user.sh` runs `userdel -r` on the user that
  already has `PUID` at every container start, which here would delete the shared game home.
  The user `player` (uid 1000) is baked in and nothing creates or deletes users or homes. The
  build takes `/opt/xivlauncher`, the ghostty-agent package and the voice tools from the Incus
  guest when there is one, and otherwise downloads XIVLauncher.Core 1.4.0. Its entrypoint (as
  root, then the player through `setpriv`):
  - sets up the NVIDIA driver volume as GoW's Fedora image does (`ldconfig` on
    `/usr/nvidia/lib{,32}`, the Vulkan, EGL and GBM manifests);
  - makes the input nodes usable (Wolf hot-plugs pads later with `docker exec ... mknod`) and
    gives the player the GPU nodes' groups; makes `XDG_RUNTIME_DIR` the player's (0700);
  - refuses to start the launcher when `.xlcore/launcher.ini` is missing (an unmounted home
    would mean a fresh 90 GB install);
  - takes `flock` on `~/.xlcore/.xivstream-game.lock` for the session's lifetime, and shows a
    message instead of a second game when it is held (the Incus launcher takes the same lock);
  - starts the forwards (`socat` on `127.0.0.1:<port>` to the gateway), a session D-Bus with
    the empty-password login keyring, and the voice microphone as a PulseAudio tunnel source in
    Wolf's PulseAudio (named `xivstream-dualsense-mic`, so ghostty-voice picks it), replacing
    the Incus session's PipeWire tunnel;
  - runs sway nested on Wolf's compositor (fullscreen rules, flat pointer acceleration),
    which starts ghostty-agent (`~/.local/bin` with `~/.local/lib`, as the Incus session's
    unit override did) and the XIVLauncher loop with `DXVK_FRAME_RATE`.
- **The session container** is created by Wolf from xivstream's `base_create_json`: the
  `wolf.network` bridge with the fixed `wolf.app_ip`, `SecurityOpt label=disable` (the home's
  files are `unlabeled_t`; relabelling 95 GB is never an option), the upstream minimum
  capabilities, `IpcMode host` as in all of Wolf's examples, and `wolf.memory`. Its mounts are
  the home at `/home/player` and `wolf.mounts` whose source exists (the in-game terminal's
  claude and codex, as the Incus container's disk devices gave them). CPUs are left to
  game-cpu-fence.
- **The game's home** is a btrfs subvolume (`wolf.home_subvolume`) on the filesystem of the
  Incus storage pool, mounted at `wolf.home` by its own mount unit, by filesystem UUID. Incus
  mounts its pool itself, on demand (incusd is socket-activated), so a bind of a path inside it
  would race or miss at boot. On the same filesystem the old guest's home is reflink-copied in
  once, at the cutover.
- **Network.** The session runs on its own Podman bridge (`wolf.network`, no DNS), and the nft
  table `inet xivstream_wolf` (loaded while Wolf runs, by `xivstream-wolf-firewall.service`)
  decides what it may reach. It only drops or DNATs, and firewalld decides the rest.
  - Wolf binds every IPv4 address with no option to choose one, so its ports (tcp 47984,
    47989, 48010; udp 47999, 48100, 48200) take loopback and `wolf.allowed_clients` on
    tailscale0 only (anything on tailscale0 when the list is empty).
  - The session may reach this host only at its gateway's forwards. It may never reach
    tailscale0 or a private, CGNAT or special-purpose range. The internet stays open: the
    launcher logs in and patches.
  - Netavark adds the network to firewalld's trusted zone when a session starts, which is what
    lets the forwards through; `netavark-firewalld-reload.service` is enabled so a firewalld
    reload does not drop that.
  - Each `wolf.forwards` entry is a socket unit on the gateway (`FreeBind`: the address only
    exists while the network is up) with `systemd-socket-proxyd` to its target: the Incus
    container's proxy devices (almanac's gateway, the Ghostty agents, voice).
  - `wolf.inbound`: tailnet peers that reached the old container at `wolf.legacy_address`
    (its subnet route and tailnet ACL stay) are DNATed to the session, as is this host's own
    traffic to that address.
- **Input.** Wolf's `85-wolf.rules` (pinned) and `uhid` (plus `nvidia_uvm` and `nvidia_modeset`)
  in `modules-load.d`. udev's rules are reloaded but never re-triggered, which would replay a
  live stream's devices. The Sunshine rule, which hands every Sony uhid pad to the Incus
  container, gets a guard that skips Wolf's devices, matched as `85-wolf.rules` matches them
  (`Wolf ... (virtual) ...` names; the hidraw node through its parent's `HID_NAME`). Sunshine's
  own pads are `Wireless Controller`, so its behaviour is unchanged.
- **The NVIDIA driver volume** is rebuilt by `xivstream-nvidia-driver-vol.service` (before Wolf)
  only when `/sys/module/nvidia/version` has no matching libraries in it. It builds GoW's
  nvidia-driver Dockerfile (pinned in the repo, built in an empty directory) for that version
  and keeps the image. It removes the old volume and every container that references it (it
  refuses while one runs), lets Podman populate a new volume through a throwaway container, and
  always removes that container. The image's last stage is `FROM scratch` with a dynamically
  linked `/bin/sh`, so starting that container fails after Podman has populated the volume;
  the volume's contents are checked instead of the exit status.
- **CPU.** game-cpu-fence (installed from the repo; the hand-installed one is kept as
  `.pre-xivstream`) recognises the game's Podman scope. It fences everything else, including
  the other children of `machine.slice`, but never the scope itself; `tailscaled.service` and
  `wolf.service` are exempt. It gives the scope `wolf.cpus_busy` and switches to
  `wolf.cpus_idle` with cpu-policy's thresholds (defaults: `[incus.cpu_policy]`). The Incus-only
  `xivstream-container` and `xivstream-cpu-policy` are disabled. `xivstream-gpu-share`
  works unchanged: with `topology = "host"` it counts the game in any cgroup.
- **Pairing.** `sudo xivstream pair PIN` answers the waiting client through
  `/var/run/wolf/wolf.sock` (`GET /api/v1/pair/pending`, `POST /api/v1/pair/client`), then sets
  the new client's `controllers_override` from `stream.gamepad`
  (`POST /api/v1/clients/settings`). With several clients waiting, add `--client <its IP>`.
  Every client pairs again: Wolf is a new host to Moonlight.
- **The Sunshine fallback.** When the machine had the Incus/Sunshine setup, apply keeps its
  config as `/etc/xivstream/sunshine.toml` and writes `xivstream-sunshine.service`.
  `systemctl start xivstream-sunshine` stops Wolf (`Conflicts=`), removes Wolf's session
  containers and starts the Incus container; `systemctl start wolf` switches back. The old
  Moonlight pairings stay valid for it.

Things to know:

- **Log out before quitting from Moonlight, or stopping Wolf.** Wolf stops a session's container
  with a two-second timeout and then kills it. The entrypoint asks the game window to close,
  which only helps at the title screen. Settings, Dalamud's configs and the plugins' databases
  are written on logout, and the home is shared with the fallback.
- **Switching devices means quitting first**: a paused session keeps the game, and its lock.
- **Restarting Wolf** while a game runs ends it: the preflight removes leftover session
  containers (after ten seconds), and Wolf would replace a client's container on its next
  connect anyway.
- **Codecs on Pascal.** The one Pascal report (#400, a P100) had H.264 fail in
  `cudaconvertscale` while HEVC worked. Both of Wolf's NVIDIA paths use that element, so
  zero-copy off does not avoid it. Try a session before relying on it, and prefer HEVC on the
  client if H.264 fails.
- **Wolf has no web UI.** Anything that pointed at Sunshine's (port 47990) has nothing behind
  it while Wolf runs.
- **Wolf's controller hotkey** (START+UP+R1) returns to Wolf UI; without Wolf UI it does nothing.

### Switching an existing Incus/Sunshine setup to Wolf

Stage while playing; the switch itself is one planned stop.

1. **Stage.** Copy the live config to `/etc/xivstream/config.wolf.toml`, set
   `topology = "host"`, `backend = "wolf"` and, in `[wolf]`, `autostart = false` (plus
   `allowed_clients`, `legacy_address` as needed), then
   `sudo xivstream apply --config /etc/xivstream/config.wolf.toml`. It keeps the Sunshine
   config as `sunshine.toml` and makes `xivstream-sunshine.service` what starts at boot (in
   place of `xivstream-container`), so a reboot still brings Sunshine back. It also builds
   the images and the driver volume, creates and mounts the (empty) home, and writes every
   unit. Wolf stays stopped. The running game sees only a udev rule reload and
   game-cpu-fence restarting (which gives the cores back for a moment and fences again).
2. **Switch.** Log out of the game and quit it. Then:
   - `sudo incus stop ffxiv --timeout 90 && sudo incus snapshot create ffxiv pre-wolf`
     (snapshot taken stopped, so it is consistent);
   - copy the home in, once, by reflink (same filesystem: seconds to minutes, no second
     95 GB):
     `sudo cp -a --reflink=always /var/lib/incus/storage-pools/default/containers/ffxiv/rootfs/home/player/. /var/lib/xivstream/home/`;
   - set `autostart = true` and run the same apply again: Wolf becomes the boot default and
     is started, `xivstream-cpu-policy` is disabled.
3. **Pair** each client (`sudo xivstream pair PIN`, Moonlight sees a new host) and launch
   "Final Fantasy XIV".
4. **Optionally, once Wolf has proved itself**, give the fallback the same home, so the two
   never diverge. With the container stopped:
   `sudo incus config device add ffxiv home disk source=/var/lib/xivstream/home path=/home/player shift=true`.
   Until then the fallback plays on the guest's own, older home.

To fall back: quit the session in Moonlight, then `sudo systemctl start xivstream-sunshine`
(it stops Wolf and removes Wolf's session containers first). `sudo systemctl start wolf`
switches back. To undo the switch entirely: fall back, remove Wolf's Quadlet
(`sudo rm /etc/containers/systemd/wolf.container && sudo systemctl daemon-reload`), put the
old config back (`sudo cp /etc/xivstream/sunshine.toml /etc/xivstream/config.toml`) and
`sudo xivstream apply`, which re-enables `xivstream-container` and the CPU policy. The
guest's own home is never written by the copy; `incus snapshot restore ffxiv pre-wolf`
returns the container to the moment of the switch. The copy can be dropped with
`sudo systemctl disable --now var-lib-xivstream-home.mount` and
`sudo btrfs subvolume delete /var/lib/incus/storage-pools/default/xivstream-home`.

## Selkies

<https://github.com/selkies-project/selkies>

- **Version 2.0.0 (2026-09-23) dropped GStreamer.** Guides for selkies-gstreamer 1.x
  (`nvh264enc`, WebRTC by default) no longer apply.
- **Install routes.** Release packages for Fedora (`-fc-x86_64.rpm`), Debian/Ubuntu, Arch and
  Alpine; an AppImage; `pip install selkies`; and container images.
- **What xivstream runs** inside the headless sway:
  `selkies --wayland=true --wayland-host-display=$WAYLAND_DISPLAY --public --port=8080 --enable-https=true --encoder=h264enc`.
  `--wayland-host-display` only takes effect with `--wayland=true`. Without it, Selkies captures X11,
  which here is sway's rootless Xwayland. Keyboard and mouse go through sway's virtual-keyboard and
  virtual-pointer protocols, not uinput. Selkies refuses to start with basic auth and no password,
  so a password is generated, kept in the state directory, and passed as `SELKIES_BASIC_AUTH_PASSWORD`
  from a mode-600 file, never on the command line.
- **Encoders.** `--encoder=h264enc` picks NVENC or VA-API where the GPU has it.
- **HTTPS matters.** Browsers allow gamepads, clipboard and pointer lock only in a secure
  context. Selkies makes a self-signed certificate on first start.
- **Transport.** The default is WebSockets on one TCP port, with no STUN/TURN needed on a LAN
  or a tailnet. WebRTC (`--mode=webrtc`) needs UDP 49152–65535 or a TURN server.
- **Tested 2026-09-24** in an Incus container on Fedora with an NVIDIA P4000 (Selkies 2.0.0 from the
  `-fc-x86_64.rpm`): it installs and starts, finds NVENC ("Render node 1 encodes H264, H265 on nvenc"),
  reports "Wayland (host compositor 'wayland-1') capture", and answers 401 without the login and 200
  with it. The picture in a real browser has not been checked yet.
