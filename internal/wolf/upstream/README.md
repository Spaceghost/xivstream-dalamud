Pinned copies of upstream files xivstream installs or builds from, so a setup
never depends on what a branch says today.

| File | Upstream | Revision |
|---|---|---|
| `config.v7.toml` | games-on-whales/wolf `src/moonlight-server/state/default/config.v7.toml` | `facb8e0c` (stable, the pinned Wolf image) |
| `85-wolf.rules` | games-on-whales/wolf `85-wolf.rules` | `facb8e0c` (stable) |
| `nvidia-driver.Dockerfile` | games-on-whales/gow `images/nvidia-driver/Dockerfile` | `9c486cf9` (master, 2025-11-05) |

Update them together with `WolfServerImage` in `internal/wolf/setup.go`.
