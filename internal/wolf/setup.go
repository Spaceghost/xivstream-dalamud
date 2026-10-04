// Package wolf generates what xivstream sets up for the Wolf backend (a
// rootful Podman Quadlet for Wolf, the game's session container, its network
// policy and forwards, the NVIDIA driver volume, the home's mount) and runs
// the small commands its units call (wolf-config, wolf-preflight,
// wolf-cleanup, nvidia-driver-vol, pair). Everything machine-specific comes
// in through Setup, so the generators are pure and golden-tested.
package wolf

import (
	"embed"
	"fmt"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
)

//go:embed upstream/config.v7.toml upstream/85-wolf.rules upstream/nvidia-driver.Dockerfile
var upstream embed.FS

func upstreamFile(name string) []byte {
	data, err := upstream.ReadFile("upstream/" + name)
	if err != nil {
		panic(err)
	}
	return data
}

// UdevRules is Wolf's 85-wolf.rules, pinned (see upstream/README.md).
func UdevRules() []byte { return upstreamFile("85-wolf.rules") }

// DriverDockerfile is GoW's nvidia-driver Dockerfile, pinned.
func DriverDockerfile() []byte { return upstreamFile("nvidia-driver.Dockerfile") }

// DefaultConfig is Wolf's default config.toml (v7), pinned.
func DefaultConfig() []byte { return upstreamFile("config.v7.toml") }

const (
	// ServerImage is ghcr.io/games-on-whales/wolf:stable as of 2026-09-29
	// (revision facb8e0c), by digest.
	ServerImage = "ghcr.io/games-on-whales/wolf@sha256:b0ef253739f9bd49b71eba7e150c955fd05990c0d409e666d89236995351551a"

	Unit           = "wolf.service"
	QuadletPath    = "/etc/containers/systemd/wolf.container"
	ConfigFile     = "/etc/wolf/cfg/config.toml"
	HostRuntimeDir = "/run/wolf"      // on the host: Wolf's sockets (API, Wayland, PulseAudio)
	RuntimeDir     = "/run/user/wolf" // the same directory in Wolf's and the session's containers
	// EnvFile: what wolf-preflight found at this start (the render node), read
	// by Podman when it runs Wolf. Render node numbers can change across boots.
	EnvFile    = "/run/xivstream/wolf.env"
	SocketPath = HostRuntimeDir + "/wolf.sock"

	RunnerName       = "WolfFFXIV" // Wolf names each session's container WolfFFXIV_<session id>
	AppTitle         = "Final Fantasy XIV"
	MoonlightProfile = "moonlight-profile-id"

	DriverVolume  = "nvidia-driver-vol"
	DriverImage   = "localhost/gow-nvidia-driver"
	DriverVolUnit = "xivstream-nvidia-driver-vol.service"

	FirewallUnit = "xivstream-wolf-firewall.service"
	FirewallFile = "/etc/xivstream/wolf.nft"
	Table        = "xivstream_wolf"

	FallbackUnit   = "xivstream-sunshine.service"
	FallbackConfig = "/etc/xivstream/sunshine.toml"

	DefaultBin = "/usr/local/bin/xivstream"
)

// Setup is everything the generators need: the config plus what apply found
// on the machine.
type Setup struct {
	config.Config
	Bin          string // xivstream's path on the host
	RenderNode   string // the streaming GPU's render node ("" = Wolf's default)
	NVIDIA       bool
	Modprobe     bool   // /usr/bin/nvidia-modprobe exists (creates /dev/nvidia-uvm and -modeset)
	ServerImage  string // Wolf
	SessionImage string // the session image
	HomeDevice   string // what the home's mount unit mounts (/dev/disk/by-uuid/...)
	// LegacyAddress: the resolved [wolf] legacy_address ("" = no inbound DNAT).
	LegacyAddress string
	// GameContainer: the Incus container that must be stopped while Wolf runs
	// ("" = no Incus fallback on this machine).
	GameContainer string
	// Fallback: write xivstream-sunshine.service (a Sunshine config was kept).
	Fallback bool
}

// EnvFileContent is EnvFile for a render node ("" = Wolf's default).
func EnvFileContent(renderNode string) []byte {
	if renderNode == "" {
		return []byte("# no render node found: Wolf's default\n")
	}
	return []byte("WOLF_RENDER_NODE=" + renderNode + "\n")
}

// BridgeName is the Podman network's bridge interface (Linux allows 15 bytes).
func BridgeName(network string) string {
	n := strings.NewReplacer("_", "-").Replace(network)
	if len(n) > 14 {
		n = n[:14]
	}
	return n + "0"
}

// MountUnitName is systemd's name for the mount unit of path
// (systemd-escape --path --suffix=mount).
func MountUnitName(path string) string {
	p := strings.Trim(path, "/")
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		ch := p[i]
		switch {
		case ch == '/':
			b.WriteByte('-')
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '_', ch == '.' && i > 0:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, `\x%02x`, ch)
		}
	}
	if b.Len() == 0 {
		return "-.mount"
	}
	return b.String() + ".mount"
}

// HomeUnit is the home's mount unit.
func (s Setup) HomeUnit() string { return MountUnitName(s.Wolf.Home) }

// ForwardUnit is the socket unit's base name for a forward.
func ForwardUnit(f config.WolfForward) string { return "xivstream-fwd-" + f.Name }

// MicForward is the forward the voice microphone's tunnel uses, if any.
func (s Setup) MicForward() (config.WolfForward, bool) {
	for _, f := range s.Wolf.Forwards {
		if f.Name == "voice-mic" {
			return f, true
		}
	}
	return config.WolfForward{}, false
}

func (s Setup) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	return DefaultBin
}

func (s Setup) serverImage() string {
	if s.ServerImage != "" {
		return s.ServerImage
	}
	return ServerImage
}
