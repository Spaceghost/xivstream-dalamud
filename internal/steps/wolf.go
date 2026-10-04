package steps

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/Spaceghost/xivstream-dalamud/images/session"
	"github.com/Spaceghost/xivstream-dalamud/internal/assets"
	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/detect"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
	"github.com/Spaceghost/xivstream-dalamud/internal/sys"
	"github.com/Spaceghost/xivstream-dalamud/internal/wolf"
)

// LiveConfig is the config the host services read; apply rewrites it.
const LiveConfig = "/etc/xivstream/config.toml"

// long runs a command that may take a while (image builds, downloads).
func long(argv ...string) (string, error) { return sys.OutputTimeout(time.Hour, argv...) }

// FallbackConfig returns the Sunshine config the fallback runs from: the kept
// copy, else the live config if it still is the Incus/Sunshine one (apply
// keeps it before writing the Wolf config over it).
func FallbackConfig() (config.Config, []byte, bool) {
	for _, path := range []string{wolf.FallbackConfig, LiveConfig} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		c := config.Default()
		if _, err := toml.Decode(string(data), &c); err != nil {
			continue
		}
		if c.Topology == config.TopologyIncus && c.Backend == config.BackendSunshine {
			return c, data, true
		}
	}
	return config.Config{}, nil, false
}

// WolfSetup is what the Wolf generators need for c on this machine. It asks
// Incus (the old container's address, when legacy_address is empty) and
// findmnt (the home's filesystem); neither changes anything.
func WolfSetup(c config.Config, f detect.Facts) (wolf.Setup, error) {
	return wolfSetup(c, f, true)
}

// WolfRuntimeSetup is WolfSetup for the commands Wolf's units run: it never
// calls incusd (socket-activated, and at boot that would start it), which
// only the firewall's legacy address needs, and apply has written that.
func WolfRuntimeSetup(c config.Config, f detect.Facts) (wolf.Setup, error) {
	return wolfSetup(c, f, false)
}

func wolfSetup(c config.Config, f detect.Facts, probeIncus bool) (wolf.Setup, error) {
	gpu, _ := f.PrimaryGPU()
	s := wolf.Setup{
		Config: c, Bin: wolf.DefaultBin, RenderNode: gpu.RenderNode, NVIDIA: gpu.Vendor == "nvidia",
		Modprobe:     sys.Exists("/usr/bin/nvidia-modprobe"),
		ServerImage:  c.Wolf.ServerImage,
		SessionImage: c.Wolf.Image,
	}
	if exe, err := os.Executable(); err == nil && exe == "/usr/bin/xivstream" {
		s.Bin = exe
	}
	if s.SessionImage == "" {
		s.SessionImage = session.Image()
	}
	if fb, _, ok := FallbackConfig(); ok {
		s.Fallback, s.GameContainer = true, fb.Incus.Container
	}
	s.LegacyAddress = c.Wolf.LegacyAddress
	if probeIncus && s.LegacyAddress == "" && s.GameContainer != "" && sys.Has("incus") {
		if ip, err := sys.Output("incus", "config", "device", "get", s.GameContainer, "eth0", "ipv4.address"); err == nil {
			s.LegacyAddress = strings.TrimSpace(ip)
		}
	}
	dev, err := wolf.DeviceFor(sys.Output, c.Wolf.HomeDevice)
	if err != nil {
		return s, err
	}
	s.HomeDevice = dev
	return s, nil
}

// changes remembers which steps of this apply did something, so a reload or
// restart follows only its own files.
type changes map[string]bool

func (ch changes) track(key string, st plan.Step) plan.Step {
	apply := st.Apply
	st.Apply = func() error {
		if err := apply(); err != nil {
			return err
		}
		ch[key] = true
		return nil
	}
	return st
}

func active(unit string) bool {
	_, err := sys.Output("systemctl", "is-active", "-q", unit)
	return err == nil
}
func enabled(unit string) bool {
	_, err := sys.Output("systemctl", "is-enabled", "-q", unit)
	return err == nil
}

func daemonReload() error { _, err := sys.Output("systemctl", "daemon-reload"); return err }

// wolf: Wolf in a rootful Podman Quadlet on this host, running the game in
// xivstream's session image, one container per Moonlight client, with the
// game's home on its own btrfs subvolume. Experimental: see docs/backends.md.
func (b *builder) wolf() ([]plan.Step, error) {
	c := b.c
	host := plan.Local{}
	if !b.f.Podman {
		return nil, fmt.Errorf("wolf needs podman (rootful, with its systemd socket and Quadlet)")
	}
	if b.f.Init != "systemd" {
		return nil, fmt.Errorf("wolf is set up with systemd units and a Podman Quadlet; this host's init is %s", b.f.Init)
	}
	s, err := WolfSetup(c, b.f)
	if err != nil {
		return nil, err
	}
	ch := changes{}
	var steps []plan.Step
	add := func(st ...plan.Step) { steps = append(steps, st...) }

	add(b.installSelf(host, plan.Incus{})[0])

	// The Sunshine config, before the Wolf one replaces it.
	if _, data, ok := FallbackConfig(); ok {
		add(step(host, "Keep the Sunshine setup's config for the fallback", "xivstream-sunshine.service starts the Incus game container from it ("+wolf.FallbackConfig+").",
			[]string{"cp " + LiveConfig + " " + wolf.FallbackConfig},
			exists(host, wolf.FallbackConfig),
			func() error { return host.WriteFile(wolf.FallbackConfig, data, 0o600, "") }))
	}

	// Kernel modules: uhid for Wolf's DualSense; nvidia_uvm (CUDA, the encoder)
	// and nvidia_modeset are otherwise only loaded once something asks.
	mods := []string{"uhid"}
	if s.NVIDIA {
		mods = append(mods, "nvidia_uvm", "nvidia_modeset")
	}
	add(plan.File(host, "/etc/modules-load.d/xivstream-wolf.conf", []byte(strings.Join(mods, "\n")+"\n"), 0o644, "",
		"Load "+strings.Join(mods, ", ")+" at boot", "Wolf needs them before it starts; at boot nothing else loads them in time."),
		step(host, "Load "+strings.Join(mods, ", ")+" now", "", []string{"modprobe " + strings.Join(mods, " ")},
			func() (bool, error) {
				for _, m := range mods {
					if !sys.Exists("/sys/module/" + m) {
						return false, nil
					}
				}
				return true, nil
			},
			func() error {
				for _, m := range mods {
					if _, err := sys.Output("modprobe", m); err != nil {
						return err
					}
				}
				return nil
			}))

	// udev: Wolf's rules, and the Sunshine rule taught to leave Wolf's pads alone.
	add(ch.track("udev", plan.File(host, "/etc/udev/rules.d/85-wolf.rules", wolf.UdevRules(), 0o644, "",
		"Install Wolf's udev rules (pinned)", "uinput and uhid for the input group; Wolf's virtual pads on their own seat, so a streamed controller cannot drive this machine's desktop.")))
	for _, rule := range sunshineRules() {
		rule := rule
		add(ch.track("udev", step(host, "Keep the Sunshine rule off Wolf's pads ("+filepath.Base(rule)+")",
			"It hands every Sony uhid pad to the Incus container; Wolf's emulated DualSense is one too. Sunshine's own devices still match.",
			[]string{"insert a GOTO past the rule for \"Wolf *virtual*\" devices into " + rule},
			func() (bool, error) {
				data, err := os.ReadFile(rule)
				return err == nil && bytes.Equal(wolf.GateSunshineRule(data), data), nil
			},
			func() error {
				data, err := os.ReadFile(rule)
				if err != nil {
					return err
				}
				return host.WriteFile(rule, wolf.GateSunshineRule(data), 0o644, "")
			})))
	}
	add(step(host, "Reload udev's rules (no trigger)", "Only new events see them: re-triggering would replay the live stream's devices.",
		[]string{"udevadm control --reload"},
		func() (bool, error) { return !ch["udev"], nil },
		func() error { _, err := sys.Output("udevadm", "control", "--reload"); return err }))

	add(step(host, "Enable the Podman socket", "Wolf starts each session's container through it (as Docker's API).",
		[]string{"systemctl enable --now podman.socket"},
		func() (bool, error) { return enabled("podman.socket") && active("podman.socket"), nil },
		func() error { _, err := sys.Output("systemctl", "enable", "--now", "podman.socket"); return err }))

	gw := c.Wolf.Gateway().String()
	bridge := wolf.BridgeName(c.Wolf.Network)
	add(step(host, "Create the session network "+c.Wolf.Network+" ("+c.Wolf.Subnet+")",
		"A bridge of its own (no DNS): the session gets a fixed address, the host's forwards on the gateway and nothing else of this host or the tailnet.",
		[]string{fmt.Sprintf("podman network create --subnet %s --gateway %s --interface-name %s --disable-dns %s", c.Wolf.Subnet, gw, bridge, c.Wolf.Network)},
		func() (bool, error) { return networkMatches(c.Wolf.Network, c.Wolf.Subnet, gw, bridge) },
		func() error {
			if _, err := sys.Output("podman", "network", "exists", c.Wolf.Network); err == nil {
				return fmt.Errorf("podman network %s exists with other settings; remove it (podman network rm %s, with Wolf stopped) and apply again", c.Wolf.Network, c.Wolf.Network)
			}
			_, err := sys.Output("podman", "network", "create", "--subnet", c.Wolf.Subnet, "--gateway", gw,
				"--interface-name", bridge, "--disable-dns", c.Wolf.Network)
			return err
		}))
	if active("firewalld.service") {
		add(step(host, "Re-register Podman networks after firewalld reloads",
			"Netavark adds each network to firewalld's trusted zone when a container starts on it; a firewalld reload drops that, and the session's forwards with it.",
			[]string{"systemctl enable netavark-firewalld-reload.service"},
			func() (bool, error) { return enabled("netavark-firewalld-reload.service"), nil },
			func() error {
				_, err := sys.Output("systemctl", "enable", "--now", "netavark-firewalld-reload.service")
				return err
			}))
	}

	if s.NVIDIA {
		add(ch.track("units", plan.File(host, "/etc/systemd/system/"+wolf.DriverVolUnit, wolf.DriverVolUnitFile(s), 0o644, "",
			"Write the NVIDIA driver volume service", "Before Wolf: rebuilds "+wolf.DriverVolume+" when the host's driver changes (CDI would need nvidia-container-toolkit layered and a reboot; Wolf's docs recommend the volume).")))
		add(step(host, "Build the NVIDIA driver volume for this driver",
			"Wolf and every session load the driver's userspace from it; it must match the kernel module.",
			[]string{"xivstream nvidia-driver-vol  (GoW's pinned nvidia-driver Dockerfile, NV_VERSION=$(cat " + wolf.DriverVersionFile + "))"},
			func() (bool, error) {
				v, err := wolf.DriverVersion(os.ReadFile)
				if err != nil {
					return false, err
				}
				return wolf.DriverVolumeCurrent(sys.Output, v, func(p string) error { _, err := os.Stat(p); return err })
			},
			func() error { return RunDriverVolume(func(m string) { fmt.Print("(" + m + ") ") }) }))
	}

	// The home.
	home := c.Wolf.Home
	add(step(host, "Create the home's btrfs subvolume "+c.Wolf.HomeSubvolume,
		"The game's home (95 GB in the old container) lives on its own subvolume, on the same filesystem as the Incus pool so the old one can be reflink-copied in.",
		[]string{"btrfs subvolume create <" + s.HomeDevice + " top level>/" + c.Wolf.HomeSubvolume},
		func() (bool, error) {
			if wolf.HomeMounted(sys.Output, home, c.Wolf.HomeSubvolume) {
				return true, nil
			}
			return wolf.SubvolumeExists(sys.Output, s.HomeDevice, c.Wolf.HomeSubvolume)
		},
		func() error { return wolf.CreateSubvolume(sys.Output, s.HomeDevice, c.Wolf.HomeSubvolume) }))
	unit := s.HomeUnit()
	add(ch.track("units", plan.File(host, "/etc/systemd/system/"+unit, wolf.MountUnit(s), 0o644, "",
		"Write the home's mount unit ("+home+")", "By filesystem UUID: the Incus pool is mounted by incusd on demand, so a bind of it would race (or miss) at boot.")))
	add(step(host, "Mount the home ("+unit+")", "", []string{"systemctl enable --now " + unit},
		func() (bool, error) {
			return enabled(unit) && wolf.HomeMounted(sys.Output, home, c.Wolf.HomeSubvolume), nil
		},
		func() error {
			if err := daemonReload(); err != nil {
				return err
			}
			_, err := sys.Output("systemctl", "enable", "--now", unit)
			return err
		}))

	if c.Wolf.Image == "" {
		add(step(host, "Build the session image "+s.SessionImage,
			"Fedora 43 with the launcher, Wine's dependencies and the in-game terminal's tools (images/session); the launcher and agent are taken from the Incus guest when there is one.",
			[]string{"podman build -t " + s.SessionImage + " <images/session + what the Incus guest has>"},
			func() (bool, error) { return wolf.ImageExists(sys.Output, s.SessionImage), nil },
			func() error { return BuildSessionImage(s, func(m string) { fmt.Print("(" + m + ") ") }) }))
	}

	// Network policy and forwards.
	add(ch.track("firewall", plan.File(host, wolf.FirewallFile, wolf.Firewall(s), 0o644, "",
		"Write Wolf's nft table", "Wolf binds every address: its ports are for loopback and the tailnet clients. The session reaches only its forwards on this host, and never the tailnet.")))
	add(ch.track("units", plan.File(host, "/etc/systemd/system/"+wolf.FirewallUnit, wolf.FirewallUnitFile(s), 0o644, "",
		"Write the firewall unit", "Loaded with Wolf (PartOf): the old address's DNAT never outlives Wolf.")))
	add(step(host, "Reload Wolf's nft table", "Only while Wolf runs (the unit is part of it).",
		[]string{"systemctl try-restart " + wolf.FirewallUnit},
		func() (bool, error) { return !ch["firewall"] || !active(wolf.FirewallUnit), nil },
		func() error { _, err := sys.Output("systemctl", "try-restart", wolf.FirewallUnit); return err }))

	var sockets []string
	for _, f := range c.Wolf.Forwards {
		base := wolf.ForwardUnit(f)
		sockets = append(sockets, base+".socket")
		add(ch.track("units", plan.File(host, "/etc/systemd/system/"+base+".socket", wolf.ForwardSocket(s, f), 0o644, "",
			"Write the "+f.Name+" forward ("+fmt.Sprintf("%s:%d to %s", gw, f.Port, f.Target)+")", "")))
		add(ch.track("units", plan.File(host, "/etc/systemd/system/"+base+".service", wolf.ForwardService(s, f), 0o644, "", "Write the "+f.Name+" relay", "")))
	}
	if stale := staleForwards(c.Wolf.Forwards); len(stale) > 0 {
		add(step(host, "Remove forwards no longer configured", "", []string{"systemctl disable --now " + strings.Join(stale, " ")}, nil,
			func() error {
				for _, sock := range stale {
					_, _ = sys.Output("systemctl", "disable", "--now", sock, strings.TrimSuffix(sock, ".socket")+".service")
					for _, p := range []string{sock, strings.TrimSuffix(sock, ".socket") + ".service"} {
						if err := os.Remove("/etc/systemd/system/" + p); err != nil && !os.IsNotExist(err) {
							return err
						}
					}
				}
				return daemonReload()
			}))
	}
	if len(sockets) > 0 {
		add(step(host, "Start the session's forwards", "Socket units on the gateway (FreeBind: the address exists only while the network is up).",
			[]string{"systemctl enable --now " + strings.Join(sockets, " ")},
			func() (bool, error) {
				if ch["units"] {
					return false, nil
				}
				for _, sock := range sockets {
					if !enabled(sock) || !active(sock) {
						return false, nil
					}
				}
				return true, nil
			},
			func() error {
				if err := daemonReload(); err != nil {
					return err
				}
				_, err := sys.Output(append([]string{"systemctl", "enable", "--now"}, sockets...)...)
				if err == nil {
					_, err = sys.Output(append([]string{"systemctl", "restart"}, sockets...)...)
				}
				return err
			}))
	}

	add(ch.track("wolf", plan.File(host, wolf.QuadletPath, wolf.Quadlet(s), 0o644, "",
		"Write Wolf's Quadlet unit", "Rootful (Wolf creates device cgroup rules for each session), the NVIDIA driver volume, a pinned image; never with the Incus game running or the home unmounted.")))
	if s.Fallback {
		add(ch.track("units", plan.File(host, "/etc/systemd/system/"+wolf.FallbackUnit, wolf.FallbackUnitFile(s), 0o644, "",
			"Write the Sunshine fallback unit", "systemctl start "+wolf.FallbackUnit+" stops Wolf and starts the "+s.GameContainer+" container with Sunshine; systemctl start wolf switches back.")))
	}

	add(step(host, "Put xivstream's app into Wolf's config", "Created from Wolf's pinned default (v7) when missing; otherwise merged: hostname, uuid, pairings and encoders are kept.",
		[]string{"xivstream wolf-config  (" + wolf.ConfigFile + ")"},
		func() (bool, error) {
			old, err := os.ReadFile(wolf.ConfigFile)
			if err != nil {
				return false, nil
			}
			merged, err := wolf.MergeConfig(old, s, wolf.MergeOptions{Exists: sys.Exists})
			return err == nil && wolf.SameConfig(old, merged), err
		},
		func() error {
			if active(wolf.Unit) {
				fmt.Print("(Wolf is running: its config is merged when it next starts) ")
				return nil
			}
			return WriteWolfConfig(s, func(m string) { fmt.Print("(" + m + ") ") })
		}))

	steps = append(steps, b.cpuFence(host)...)

	add(step(host, "Turn off the Incus-only services", "xivstream-container would start the Incus game at boot and xivstream-cpu-policy sets Incus limits; with Wolf, game-cpu-fence pins the game instead. The fallback unit starts the container when asked.",
		[]string{"systemctl disable --now xivstream-container.service xivstream-cpu-policy.service"},
		func() (bool, error) {
			return !enabled("xivstream-container.service") && !enabled("xivstream-cpu-policy.service"), nil
		},
		func() error {
			for _, u := range []string{"xivstream-container.service", "xivstream-cpu-policy.service"} {
				if sys.Exists("/etc/systemd/system/" + u) {
					if _, err := sys.Output("systemctl", "disable", "--now", u); err != nil {
						return err
					}
				}
			}
			return nil
		}))

	steps = append(steps, b.hostServices(host)...)

	add(step(host, "Reload systemd (Quadlet regenerates wolf.service)", "", []string{"systemctl daemon-reload"},
		func() (bool, error) { return !ch["wolf"] && !ch["units"], nil }, daemonReload))
	if c.Wolf.Autostart {
		add(step(host, "Start Wolf", "Only once the Incus game container is stopped (wolf-preflight refuses otherwise): log out of the game, then systemctl stop xivstream-sunshine (or incus stop).",
			[]string{"systemctl start wolf"},
			func() (bool, error) { return active(wolf.Unit), nil },
			func() error {
				if s.GameContainer != "" && wolf.IncusRunning("/sys/fs/cgroup", s.GameContainer) {
					fmt.Printf("(the %s container is running: not starting Wolf; see docs/backends.md for the switch) ", s.GameContainer)
					return nil
				}
				_, err := sys.OutputTimeout(20*time.Minute, "systemctl", "start", wolf.Unit)
				return err
			}))
	}
	return steps, nil
}

// cpuFence installs game-cpu-fence: everything but the game on the other
// CPUs while it runs, and the game's busy/idle CPUs for a Podman session.
func (b *builder) cpuFence(host plan.Local) []plan.Step {
	const bin, unit = "/usr/local/bin/game-cpu-fence", "game-cpu-fence.service"
	ch := changes{}
	script, unitFile := assets.Raw("game-cpu-fence"), assets.Raw("game-cpu-fence.service")
	var s []plan.Step
	if old, err := os.ReadFile(bin); err == nil && !bytes.Equal(old, script) && !sys.Exists(bin+".pre-xivstream") {
		s = append(s, step(host, "Keep the hand-installed game-cpu-fence", "", []string{"cp " + bin + " " + bin + ".pre-xivstream"},
			exists(host, bin+".pre-xivstream"),
			func() error { return host.WriteFile(bin+".pre-xivstream", old, 0o755, "") }))
	}
	s = append(s,
		ch.track("fence", plan.File(host, bin, script, 0o755, "", "Install game-cpu-fence",
			"While the game runs, everything else (Incus instances, user.slice, system.slice, Podman's other containers) gets the CPUs the game does not; exempt: tailscaled and Wolf, which carry the stream.")),
		ch.track("fence", plan.File(host, "/etc/systemd/system/"+unit, unitFile, 0o644, "", "Write game-cpu-fence's unit", "")),
		step(host, "Run game-cpu-fence (restarted when it changed)", "A restart gives everything back first, then fences again if the game runs.",
			[]string{"systemctl enable game-cpu-fence", "systemctl restart game-cpu-fence"},
			func() (bool, error) { return !ch["fence"] && enabled(unit) && active(unit), nil },
			func() error {
				if err := daemonReload(); err != nil {
					return err
				}
				if _, err := sys.Output("systemctl", "enable", unit); err != nil {
					return err
				}
				_, err := sys.Output("systemctl", "restart", unit)
				return err
			}))
	return s
}

// sunshineRules are the Incus streaming rules on this host.
func sunshineRules() []string {
	rules, _ := filepath.Glob("/etc/udev/rules.d/70-xivstream-*.rules")
	sort.Strings(rules)
	return rules
}

// staleForwards are forward socket units for forwards no longer configured.
func staleForwards(fwd []config.WolfForward) []string {
	want := map[string]bool{}
	for _, f := range fwd {
		want[wolf.ForwardUnit(f)+".socket"] = true
	}
	units, _ := filepath.Glob("/etc/systemd/system/xivstream-fwd-*.socket")
	var stale []string
	for _, u := range units {
		if !want[filepath.Base(u)] {
			stale = append(stale, filepath.Base(u))
		}
	}
	sort.Strings(stale)
	return stale
}

// networkMatches: the Podman network exists with this subnet, gateway and
// bridge, and without DNS.
func networkMatches(name, subnet, gateway, bridge string) (bool, error) {
	out, err := sys.Output("podman", "network", "inspect", name, "--format",
		"{{range .Subnets}}{{.Subnet}} {{.Gateway}} {{end}}|{{.NetworkInterface}}|{{.DNSEnabled}}")
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(out) == fmt.Sprintf("%s %s |%s|false", subnet, gateway, bridge), nil
}

// RunDriverVolume is `xivstream nvidia-driver-vol`: the volume for the
// loaded driver, rebuilt only when it changed.
func RunDriverVolume(log func(string)) error {
	v, err := wolf.DriverVersion(os.ReadFile)
	if err != nil {
		return err
	}
	return wolf.BuildDriverVolume(long, v, func(p string) error { _, err := os.Stat(p); return err }, log)
}

// WriteWolfConfig is `xivstream wolf-config`: Wolf's config with xivstream's
// app in it, written only when it changes (the previous file kept as .bak).
func WriteWolfConfig(s wolf.Setup, log func(string)) error {
	old, err := os.ReadFile(wolf.ConfigFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, skipped := wolf.AppMounts(s, sys.Exists)
	for _, m := range skipped {
		log("skipping the mount " + m + ": its source does not exist")
	}
	merged, err := wolf.MergeConfig(old, s, wolf.MergeOptions{Exists: sys.Exists})
	if err != nil {
		return err
	}
	if old != nil && wolf.SameConfig(old, merged) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(wolf.ConfigFile), 0o755); err != nil {
		return err
	}
	if old != nil {
		if err := os.WriteFile(wolf.ConfigFile+".bak", old, 0o600); err != nil {
			return err
		}
	}
	if err := (plan.Local{}).WriteFile(wolf.ConfigFile, merged, 0o600, ""); err != nil {
		return err
	}
	if old == nil {
		log("created " + wolf.ConfigFile + " from Wolf's default")
	} else {
		log("updated " + wolf.ConfigFile + " (previous: " + wolf.ConfigFile + ".bak)")
	}
	return nil
}

// BuildSessionImage builds the session image from the embedded context and
// what the Incus guest (if any) has.
func BuildSessionImage(s wolf.Setup, log func(string)) error {
	dir := filepath.Join(config.StateDir(), "session-build")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "extras"), 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, name := range session.Names() {
		data, err := session.Files.ReadFile(name)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			return err
		}
	}
	if ct := s.GameContainer; ct != "" && sys.Has("incus") {
		if err := exportGuestExtras(ct, filepath.Join(dir, "extras"), log); err != nil {
			log("not using the " + ct + " container's files (" + err.Error() + "); downloading XIVLauncher.Core " + session.XIVLauncherVersion)
			_ = os.RemoveAll(filepath.Join(dir, "extras"))
			_ = os.MkdirAll(filepath.Join(dir, "extras"), 0o755)
		}
	}
	_, err := long("podman", "build", "--pull=missing", "-t", s.SessionImage,
		"--build-arg", "XIVLAUNCHER_VERSION="+session.XIVLauncherVersion, "-f", filepath.Join(dir, "Containerfile"), dir)
	return err
}

// guestExtras are what the session image takes from the Incus guest.
var guestExtras = []string{"/opt/xivlauncher", "/usr/local/bin/ghostty-voice", "/usr/local/bin/whisper-cli"}

// exportGuestExtras copies the guest's launcher, voice tools and the
// ghostty-agent package's files into dir (read-only for the guest).
func exportGuestExtras(ct, dir string, log func(string)) error {
	state, err := sys.Output("incus", "list", "^"+ct+"$", "-c", "s", "-f", "csv")
	if err != nil {
		return err
	}
	if strings.TrimSpace(state) == "RUNNING" {
		script := `set -e; cd /
files="opt/xivlauncher"
for f in usr/local/bin/ghostty-voice usr/local/bin/whisper-cli; do [ -f "/$f" ] && files="$files $f"; done
if command -v rpm >/dev/null && rpm -q ghostty-agent >/dev/null 2>&1; then
  for f in $(rpm -ql ghostty-agent); do [ -f "$f" ] && [ ! -L "$f" ] && files="$files ${f#/}"; done
fi
tar -cf - $files`
		log("copying the launcher and tools out of the running " + ct + " container")
		_, err := sys.OutputTimeout(30*time.Minute, "sh", "-c", `incus exec "$1" -- sh -c "$2" | tar -C "$3" -xf -`, "-", ct, script, dir)
		return err
	}
	log("copying the launcher and tools out of the stopped " + ct + " container")
	for _, p := range append(guestExtras, "/usr/bin/ghostty-agent") {
		dst := filepath.Join(dir, filepath.Dir(p))
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if _, err := sys.OutputTimeout(30*time.Minute, "incus", "file", "pull", "-r", ct+p, dst+"/"); err != nil && p == "/opt/xivlauncher" {
			return err
		}
	}
	return nil
}
