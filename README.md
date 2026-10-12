# xivstream

> Formerly **ffxiv-stream**. A machine set up under that name carries over: `xivstream apply` reads its config and state and retires its `ffxiv-stream-*` units.

Stream FINAL FANTASY XIV from a machine with a GPU to your phone, tablet, laptop,
TV or handheld, with your mods, set up by a wizard.

```
$ sudo xivstream
```

The wizard looks at the machine and asks only what detection cannot answer:

- **Where the game runs.** In an Incus container with the GPU passed through, which keeps it
  isolated from the host, or directly on the machine.
- **The streaming server.**
  - [Sunshine](https://github.com/LizardByte/Sunshine), for [Moonlight](https://moonlight-stream.org)
    clients on every platform.
  - [Wolf](https://github.com/games-on-whales/wolf), which gives each Moonlight client its own
    session.
  - [Selkies](https://github.com/selkies-project/selkies), for any web browser, with nothing to
    install on the client.
- **Resolution, bitrate cap and where clients connect.** Tailscale is used when the machine has it.
- **Mods.** Dalamud plugins listed live from their repositories, with what they depend on added
  automatically, and optional companions installed outside the game, such as ghostty-agent for
  the Ghostty mod's terminals.
- **GPU sharing.** When something else uses the GPU (an [almanac](https://github.com/Spaceghost/almanac-dalamud)
  model server, Ollama), xivstream can make it step down to a smaller model while you play, or
  pause it.
- **Sound.** Optionally, sound never plays aloud on the machine; it only leaves through the stream.

The last screen shows the resulting configuration and the exact plan (every command and file,
with diffs) before anything changes.

## For nerds

Everything the wizard asks is a key in one TOML file, and nothing is frozen at wizard time.
Render nodes, container addresses, id maps and the newest release downloads are looked up
when a step runs.

```
xivstream detect                 # what this machine is and has (JSON)
xivstream plan -v                # dry run: every step, why, its commands, file diffs
sudo xivstream apply --yes       # do what is not done yet
xivstream doctor                 # is everything still in place?
xivstream pair 1234 ipad         # pair the Moonlight client showing PIN 1234
```

Sunshine pairing selects the pending request before submitting its PIN. If
several clients are waiting, use `xivstream pair --client IP 1234 NAME`
(Sunshine also accepts a pairing ID). Older Sunshine versions without pending
request discovery continue to accept a PIN without `--client`.

Each step checks whether it is already done, so `apply` is safe to re-run after a failure,
a config edit or an upgrade. A minimal config:

```toml
topology = "incus"        # or "host"
backend = "sunshine"      # or "wolf", "selkies"

[stream]
max_bitrate_kbps = 50000

[mods]
install = ["GhosttyDalamud", "Almanac"]

[gpu_share]
mode = "reserve"          # almanac steps down to a model that fits beside the game
container = "almanac"
```

See [`docs/config.md`](docs/config.md) for every key and [`docs/backends.md`](docs/backends.md)
for how each streaming server is set up and why.

## Platforms

| Host | Install | Services |
|---|---|---|
| Fedora, RHEL-likes | `dnf install xivstream-*.rpm` | systemd |
| Debian, Ubuntu | `apt install ./xivstream_*.deb` | systemd |
| Arch, SteamOS dev mode | `pacman -U xivstream-*.pkg.tar.zst` | systemd |
| Alpine | `apk add --allow-untrusted xivstream-*.apk` | OpenRC |
| NixOS, Nix | `nix run github:Spaceghost/xivstream-dalamud`, or the NixOS module | declared |
| Bazzite, SteamOS, Kinoite (read-only) | the static binary in `~/.local/bin` | systemd (Flatpak apps) |
| Windows 10/11 | `winget install Spaceghost.xivstream` or `scoop install xivstream` | Windows service |
| macOS | `brew install spaceghost/tap/xivstream` | launchd |

Every release also has static binaries for linux-amd64, linux-arm64, windows-amd64 and
darwin-amd64/arm64.

Tested on real hardware: the Incus + Sunshine + NVIDIA path on Fedora, end to end from a fresh
container (session, NVENC encoding, streamed input, keyring, mods). The setup it reproduces has
streamed to Moonlight on Linux, iPadOS and iOS. Wolf, Selkies, Debian/Arch guests, the Flatpak
route, Windows and macOS follow their upstream documentation and are experimental until someone
has run them. Reports welcome.

### NixOS

```nix
{
  inputs.xivstream.url = "github:Spaceghost/xivstream-dalamud";
  outputs = { nixpkgs, xivstream, ... }: {
    nixosConfigurations.box = nixpkgs.lib.nixosSystem {
      modules = [
        xivstream.nixosModules.default
        { services.xivstream.enable = true; }
      ];
    };
  };
}
```

## What it fixes that you would otherwise hit

These came out of getting this working for real, and each one is set up for you:

- **Black or flickering stream on NVIDIA.** A small shim makes Sunshine wait for sway's copy of
  each frame (NVIDIA's EGL ignores the implicit fence). Incus port forwards use kernel NAT
  instead of the userspace proxy, which dropped video packets.
- **No keyboard, mouse or pads in a container.** The stream's virtual devices are handed to the
  container, and their uevents are re-announced inside it. Incus's unix-hotplug is avoided
  (it deadlocks incusd in 6.23).
- **VRAM filling up over time.** There is no nested gamescope (on NVIDIA it leaks about 64 KiB
  per frame). DXVK caps the frame rate instead.
- **"No secrets provider installed", so the launcher cannot save your password.** A
  gnome-keyring is set up and unlocked for the headless session.
- **Pointer too fast.** Flat acceleration for streamed mice.

## Limits

- One streaming container per host. The stream's virtual input devices appear in the
  host's `/dev/input` under the same names whichever container created them, so two
  streaming containers would see each other's streamed keyboard and mouse. `apply`
  refuses to set up a second one.
- XIVLauncher's saved password is kept in a keyring with an empty password, protected by the
  session user's file permissions (there is no login screen to unlock a keyring at).

## Building

```
go test ./...
go build ./cmd/xivstream
```

Releases are built by GoReleaser (`.goreleaser.yaml`) from a tag.

## License

MIT
