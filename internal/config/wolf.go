package config

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Wolf is the [wolf] section: how the game's session container is built,
// where its home lives, and what it may reach. Used with backend = "wolf"
// (topology = "host"); see docs/backends.md.
type Wolf struct {
	// Image is the session image Wolf starts per client. "" = the image
	// xivstream builds from images/session, tagged with a hash of its files
	// (localhost/xivstream-session:<hash>).
	Image string `toml:"image"`
	// ServerImage is Wolf itself, pinned by digest. "" = the digest this
	// xivstream release was tested with (steps.WolfServerImage).
	ServerImage string `toml:"server_image"`
	// Autostart: start Wolf at boot ([Install] in its Quadlet). Apply also
	// starts it, once the Incus game container is stopped.
	Autostart bool `toml:"autostart"`

	// Home is the host path of the game's home, bind-mounted at /home/player
	// in the session. It is a btrfs subvolume mounted by its own systemd mount
	// unit (by filesystem UUID), independent of Incus.
	Home string `toml:"home"`
	// HomeDevice holds the subvolume: "UUID=<filesystem uuid>" or a /dev path.
	// "" = the filesystem holding the Incus "default" storage pool, so the
	// old container's home can be reflink-copied into it.
	HomeDevice    string `toml:"home_device"`
	HomeSubvolume string `toml:"home_subvolume"`

	// Network is the Podman bridge the session runs on (no DNS), Subnet its
	// range and AppIP the session's fixed address on it. The bridge's gateway
	// (the subnet's first address) carries the forwards below.
	Network string `toml:"network"`
	Subnet  string `toml:"subnet"`
	AppIP   string `toml:"app_ip"`
	// LegacyAddress is the old Incus game container's address: tailnet peers
	// (Inbound.Peers) reaching it on Inbound.Ports are sent to the session
	// instead. "" = read from the Incus container's eth0 at apply time.
	LegacyAddress string `toml:"legacy_address"`

	// AllowedClients: tailnet addresses (or ranges) that may reach Wolf's
	// ports. Empty = anything arriving on tailscale0.
	AllowedClients []string `toml:"allowed_clients"`
	// Forwards: host services the session reaches at 127.0.0.1:<port>.
	Forwards []WolfForward `toml:"forwards"`
	Inbound  WolfInbound   `toml:"inbound"`
	// Mounts: extra "host:container[:ro]" bind mounts for the session.
	Mounts []string `toml:"mounts"`

	// Memory limit of the session container ("12g", "12GiB", bytes).
	Memory string `toml:"memory"`
	// CPUsBusy/CPUsIdle: CPU lists for the game's container while the rest of
	// the host is busy or idle (game-cpu-fence switches between them).
	// "" = [incus.cpu_policy] busy_cpus / idle_cpus.
	CPUsBusy string `toml:"cpus_busy"`
	CPUsIdle string `toml:"cpus_idle"`
	// ZeroCopy: Wolf's DMA-BUF to CUDA path. Off by default: NVIDIA
	// multi-session crashes (games-on-whales/wolf#501, #265).
	ZeroCopy bool `toml:"zero_copy"`
	// Codecs: "auto" (whatever Wolf's encoders and the client agree on, HEVC
	// when both can) or "h264" (Wolf offers H.264 only).
	Codecs string `toml:"codecs"`
}

// WolfForward: the session's 127.0.0.1:Port reaches Target through the
// bridge's gateway (a systemd socket unit on the host).
type WolfForward struct {
	Name   string `toml:"name"`
	Port   int    `toml:"port"`
	Target string `toml:"target"`
}

// WolfInbound: Peers reaching LegacyAddress on Ports reach the session.
type WolfInbound struct {
	Ports []int    `toml:"ports"`
	Peers []string `toml:"peers"`
}

// DefaultWolf mirrors the Incus game container this replaces: its proxy
// devices, its tailnet ACL and its memory limit.
func DefaultWolf() Wolf {
	return Wolf{
		Autostart:     true,
		Home:          "/var/lib/xivstream/home",
		HomeSubvolume: "xivstream-home",
		Network:       "xivstream",
		Subnet:        "10.89.10.0/24",
		AppIP:         "10.89.10.10",
		Forwards: []WolfForward{
			{"almanac-gateway", 41881, "100.100.110.16:41881"},
			{"bak-agent", 7787, "100.104.232.33:7777"},
			{"alienware-agent", 7788, "100.83.231.2:7777"},
			{"fedora-agent", 7789, "100.100.110.16:7788"},
			{"voice-whisper", 8178, "100.83.231.2:8178"},
			{"voice-mic", 4713, "100.83.231.2:4713"},
		},
		Inbound: WolfInbound{
			Ports: []int{41800, 41881, 7777},
			Peers: []string{"100.83.231.2", "100.104.232.33", "10.61.200.1"},
		},
		Mounts: []string{
			// The in-game terminal's tools, as the Incus container's temp-claude-*
			// and temp-codex-* disk devices gave them (the symlinks, so an update
			// is picked up by the next session).
			"/home/jack/.local/bin/claude:/usr/local/bin/claude:ro",
			"/home/jack/.claude.json:/home/player/.claude.json",
			"/home/jack/.claude:/home/player/.claude",
			"/home/jack/.local/bin/codex:/usr/local/bin/codex:ro",
			"/home/jack/.codex:/home/player/.codex",
		},
		Memory: "12g",
		Codecs: "auto",
	}
}

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	netRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)
	memoryRe = regexp.MustCompile(`^([0-9]+)\s*([kmgt]i?b?|b)?$`)
)

// Validate checks the [wolf] section (only when backend = "wolf").
func (w Wolf) Validate() error {
	if !filepath.IsAbs(w.Home) || filepath.Clean(w.Home) != w.Home || w.Home == "/" {
		return fmt.Errorf("wolf.home must be a clean absolute path")
	}
	if w.HomeSubvolume == "" || strings.ContainsAny(w.HomeSubvolume, "/,= \t\n") {
		return fmt.Errorf("wolf.home_subvolume must be a plain subvolume name")
	}
	if d := w.HomeDevice; d != "" && !strings.HasPrefix(d, "/dev/") && !regexp.MustCompile(`^UUID=[0-9a-fA-F-]{8,}$`).MatchString(d) {
		return fmt.Errorf("wolf.home_device %q: want UUID=<filesystem uuid> or a /dev path", d)
	}
	if !netRe.MatchString(w.Network) {
		return fmt.Errorf("wolf.network %q: lower-case letters, digits, - and _", w.Network)
	}
	prefix, err := netip.ParsePrefix(w.Subnet)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() > 29 || prefix.Masked() != prefix {
		return fmt.Errorf("wolf.subnet %q: want an IPv4 network such as 10.89.10.0/24", w.Subnet)
	}
	app, err := netip.ParseAddr(w.AppIP)
	if err != nil || !prefix.Contains(app) || app == prefix.Addr() || app == w.Gateway() {
		return fmt.Errorf("wolf.app_ip %q must be a host address in %s other than its gateway %s", w.AppIP, w.Subnet, w.Gateway())
	}
	if w.LegacyAddress != "" {
		if a, err := netip.ParseAddr(w.LegacyAddress); err != nil || !a.Is4() {
			return fmt.Errorf("wolf.legacy_address %q: want an IPv4 address", w.LegacyAddress)
		}
	}
	for _, c := range w.AllowedClients {
		if !validIPOrPrefix(c) {
			return fmt.Errorf("wolf.allowed_clients: %q is not an IPv4 address or range", c)
		}
	}
	seen := map[int]string{}
	names := map[string]bool{}
	for _, f := range w.Forwards {
		if !nameRe.MatchString(f.Name) || names[f.Name] {
			return fmt.Errorf("wolf.forwards: name %q must be unique, lower-case letters, digits and -", f.Name)
		}
		names[f.Name] = true
		if f.Port < 1024 || f.Port > 65535 {
			return fmt.Errorf("wolf.forwards %s: port %d must be 1024-65535 (the session listens unprivileged)", f.Name, f.Port)
		}
		if other, dup := seen[f.Port]; dup {
			return fmt.Errorf("wolf.forwards %s and %s both use port %d", other, f.Name, f.Port)
		}
		seen[f.Port] = f.Name
		if _, err := netip.ParseAddrPort(f.Target); err != nil {
			return fmt.Errorf("wolf.forwards %s: target %q must be IPv4:port", f.Name, f.Target)
		}
	}
	for _, p := range w.Inbound.Ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("wolf.inbound.ports: %d is not a TCP port", p)
		}
	}
	for _, p := range w.Inbound.Peers {
		if !validIPOrPrefix(p) {
			return fmt.Errorf("wolf.inbound.peers: %q is not an IPv4 address or range", p)
		}
	}
	for _, m := range w.Mounts {
		if _, _, _, err := ParseMount(m); err != nil {
			return err
		}
	}
	if _, err := w.MemoryBytes(); err != nil {
		return err
	}
	for _, cpus := range []string{w.CPUsBusy, w.CPUsIdle} {
		if cpus != "" && (!strings.ContainsAny(cpus, ",-") || !validCPUSet(cpus)) {
			return fmt.Errorf("wolf.cpus_busy and cpus_idle are CPU lists such as %q (a bare count cannot pin a Podman scope)", "4-7")
		}
	}
	switch w.Codecs {
	case "auto", "h264":
	default:
		return fmt.Errorf("wolf.codecs %q: want auto or h264", w.Codecs)
	}
	return nil
}

func validIPOrPrefix(s string) bool {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Is4()
	}
	p, err := netip.ParsePrefix(s)
	return err == nil && p.Addr().Is4()
}

// Gateway is the bridge's address on the host: the subnet's first address.
func (w Wolf) Gateway() netip.Addr {
	p, err := netip.ParsePrefix(w.Subnet)
	if err != nil {
		return netip.Addr{}
	}
	return p.Masked().Addr().Next()
}

// ParseMount splits "host:container[:ro|rw]".
func ParseMount(m string) (host, container, mode string, err error) {
	parts := strings.Split(m, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return "", "", "", fmt.Errorf("wolf.mounts %q: want host:container[:ro]", m)
	}
	host, container, mode = parts[0], parts[1], "rw"
	if len(parts) == 3 {
		mode = parts[2]
	}
	if !filepath.IsAbs(host) || !filepath.IsAbs(container) || (mode != "ro" && mode != "rw") {
		return "", "", "", fmt.Errorf("wolf.mounts %q: absolute paths, and ro or rw", m)
	}
	return host, container, mode, nil
}

// MemoryBytes parses Memory ("" = no limit).
func (w Wolf) MemoryBytes() (int64, error) {
	s := strings.ToLower(strings.TrimSpace(w.Memory))
	if s == "" {
		return 0, nil
	}
	m := memoryRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("wolf.memory %q: want a size such as 12g or 12GiB", w.Memory)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("wolf.memory %q: %w", w.Memory, err)
	}
	unit := int64(1)
	switch strings.TrimSuffix(strings.TrimSuffix(m[2], "b"), "i") {
	case "k":
		unit = 1 << 10
	case "m":
		unit = 1 << 20
	case "g":
		unit = 1 << 30
	case "t":
		unit = 1 << 40
	}
	if n <= 0 || n > (1<<62)/unit {
		return 0, fmt.Errorf("wolf.memory %q is out of range", w.Memory)
	}
	return n * unit, nil
}

// WolfCPUs are the game's CPU lists while the host is busy and idle: [wolf]'s,
// else [incus.cpu_policy]'s. Either may be "" (no switching).
func (c Config) WolfCPUs() (busy, idle string) {
	busy, idle = c.Wolf.CPUsBusy, c.Wolf.CPUsIdle
	if busy == "" {
		busy = c.Incus.CPUPolicy.BusyCPUs
	}
	if idle == "" {
		idle = c.Incus.CPUPolicy.IdleCPUs
	}
	return busy, idle
}
