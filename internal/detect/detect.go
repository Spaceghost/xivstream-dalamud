// Package detect finds out what this machine is and has, so the wizard can
// propose a setup and `apply` can fill in what the config leaves empty.
// Nothing here changes the machine.
package detect

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/sys"
)

type Facts struct {
	OS         string   `json:"os"`          // runtime.GOOS
	Distro     string   `json:"distro"`      // os-release ID (linux), "windows", "macos"
	DistroLike []string `json:"distro_like"` // os-release ID_LIKE
	Variant    string   `json:"variant"`     // os-release VARIANT_ID (e.g. "kinoite", "bazzite")
	Pretty     string   `json:"pretty"`
	Immutable  bool     `json:"immutable"` // rpm-ostree/bootc, SteamOS, NixOS: no plain package installs
	Init       string   `json:"init"`      // systemd, openrc, launchd, windows
	Packages   string   `json:"packages"`  // dnf, rpm-ostree, apt, pacman, apk, zypper, nix, brew, winget
	Root       bool     `json:"root"`
	Device     string   `json:"device"` // "steam-deck", "gpd", "rog-ally", "legion-go", "ayaneo", ""

	GPUs []GPU `json:"gpus"`

	Incus        bool          `json:"incus"` // incus usable (daemon answers)
	Docker       bool          `json:"docker"`
	Podman       bool          `json:"podman"`
	Tailscale    string        `json:"tailscale"` // this machine's Tailscale IPv4, "" if none
	UInput       bool          `json:"uinput"`
	UHID         bool          `json:"uhid"`
	Sunshine     string        `json:"sunshine"` // path of an installed sunshine, "" if none
	ModelServers []ModelServer `json:"model_servers"`
}

// GPU is one display adapter.
type GPU struct {
	Vendor     string `json:"vendor"` // nvidia, amd, intel, apple, other
	Name       string `json:"name"`
	PCI        string `json:"pci"`         // 0000:01:00.0 (linux)
	Driver     string `json:"driver"`      // nvidia, amdgpu, i915, xe, nouveau
	RenderNode string `json:"render_node"` // /dev/dri/renderD128 (linux)
	VRAMMB     int    `json:"vram_mb"`
	Discrete   bool   `json:"discrete"`
}

// ModelServer is something that holds GPU memory for LLMs: an almanac gateway
// (which takes VRAM reservations) or a bare Ollama (which can only be stopped).
type ModelServer struct {
	Kind      string `json:"kind"`      // almanac, ollama
	Container string `json:"container"` // Incus container it runs in, "" = this machine
	Unit      string `json:"unit"`      // its service, when known
}

var vendors = map[string]string{"0x10de": "nvidia", "0x1002": "amd", "0x8086": "intel"}

func Run() Facts {
	f := Facts{OS: runtime.GOOS, Root: os.Geteuid() == 0}
	switch runtime.GOOS {
	case "linux":
		linux(&f)
	case "windows":
		f.Distro, f.Pretty, f.Init = "windows", "Windows", "windows"
		if sys.Has("winget") {
			f.Packages = "winget"
		}
		f.Tailscale = tailscaleIP()
		f.Sunshine = firstExisting(`C:\Program Files\Sunshine\sunshine.exe`)
	case "darwin":
		f.Distro, f.Init = "macos", "launchd"
		f.Pretty = "macOS " + mustOut("sw_vers", "-productVersion")
		if sys.Has("brew") {
			f.Packages = "brew"
		}
		f.GPUs = []GPU{{Vendor: "apple", Name: mustOut("sysctl", "-n", "machdep.cpu.brand_string"), Discrete: false}}
		f.Tailscale = tailscaleIP()
		f.Sunshine = lookPath("sunshine")
	}
	return f
}

func linux(f *Facts) {
	rel := osRelease("/etc/os-release")
	f.Distro, f.Variant, f.Pretty = rel["ID"], rel["VARIANT_ID"], rel["PRETTY_NAME"]
	f.DistroLike = strings.Fields(rel["ID_LIKE"])
	switch {
	case sys.Exists("/run/ostree-booted"):
		f.Immutable, f.Packages = true, "rpm-ostree"
	case f.Distro == "steamos":
		f.Immutable = true
	case f.Distro == "nixos":
		f.Immutable, f.Packages = true, "nix"
	}
	if f.Packages == "" {
		for _, pm := range []string{"dnf", "apt-get", "pacman", "apk", "zypper"} {
			if sys.Has(pm) {
				f.Packages = strings.TrimSuffix(pm, "-get")
				break
			}
		}
	}
	switch {
	case sys.Exists("/run/systemd/system"):
		f.Init = "systemd"
	case sys.Has("openrc") || sys.Has("rc-service"):
		f.Init = "openrc"
	}
	f.Device = device(sys.ReadTrim("/sys/class/dmi/id/sys_vendor"), sys.ReadTrim("/sys/class/dmi/id/product_name"))
	f.GPUs = linuxGPUs("/sys/class/drm")
	f.Incus = sys.Has("incus") && func() bool { _, err := sys.Output("incus", "info"); return err == nil }()
	f.Docker = sys.Has("docker")
	f.Podman = sys.Has("podman")
	f.Tailscale = tailscaleIP()
	f.UInput = sys.Exists("/dev/uinput")
	f.UHID = sys.Exists("/dev/uhid")
	f.Sunshine = lookPath("sunshine")
	f.ModelServers = modelServers("/proc")
}

// device names handhelds, whose controls and screen change sensible defaults.
func device(vendor, product string) string {
	v, p := strings.ToLower(vendor), strings.ToLower(product)
	switch {
	case v == "valve" && (p == "jupiter" || p == "galileo"):
		return "steam-deck"
	case strings.Contains(v, "gpd"):
		return "gpd"
	case strings.Contains(p, "rog ally"):
		return "rog-ally"
	case strings.Contains(v, "lenovo") && strings.Contains(p, "83e1"), strings.Contains(p, "legion go"):
		return "legion-go"
	case strings.Contains(v, "ayaneo"):
		return "ayaneo"
	}
	return ""
}

func osRelease(path string) map[string]string {
	out := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return out
	}
	defer file.Close()
	scan := bufio.NewScanner(file)
	for scan.Scan() {
		k, v, ok := strings.Cut(scan.Text(), "=")
		if ok {
			out[k] = strings.Trim(v, `"'`)
		}
	}
	return out
}

// LinuxGPUs is linuxGPUs on this machine's DRM class, for the commands units
// run, which must not pay for (or, at boot, socket-activate) all of Run.
func LinuxGPUs() []GPU { return linuxGPUs("/sys/class/drm") }

// linuxGPUs reads the DRM class: one entry per card with its render node.
func linuxGPUs(drm string) []GPU {
	cards, _ := filepath.Glob(filepath.Join(drm, "card[0-9]*"))
	var gpus []GPU
	seen := map[string]bool{}
	for _, card := range cards {
		if strings.Contains(filepath.Base(card), "-") { // connectors: card1-DP-1
			continue
		}
		dev := filepath.Join(card, "device")
		real, err := filepath.EvalSymlinks(dev)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		g := GPU{PCI: filepath.Base(real)}
		g.Vendor = vendors[sys.ReadTrim(filepath.Join(dev, "vendor"))]
		if g.Vendor == "" {
			g.Vendor = "other"
		}
		if drv, err := filepath.EvalSymlinks(filepath.Join(dev, "driver")); err == nil {
			g.Driver = filepath.Base(drv)
		}
		if nodes, _ := filepath.Glob(filepath.Join(dev, "drm", "renderD*")); len(nodes) > 0 {
			g.RenderNode = "/dev/dri/" + filepath.Base(nodes[0])
		}
		if b, err := strconv.ParseInt(sys.ReadTrim(filepath.Join(dev, "mem_info_vram_total")), 10, 64); err == nil {
			g.VRAMMB = int(b >> 20)
		}
		// Discrete: its own VRAM, or on a PCI bus other than 0 (iGPUs sit on bus 00).
		g.Discrete = g.VRAMMB > 1024 || (len(g.PCI) >= 7 && g.PCI[5:7] != "00")
		gpus = append(gpus, g)
	}
	nvidiaDetails(gpus)
	sort.SliceStable(gpus, func(i, j int) bool { return gpus[i].Discrete && !gpus[j].Discrete })
	return gpus
}

// nvidiaDetails fills names and VRAM from nvidia-smi, matched by PCI address.
func nvidiaDetails(gpus []GPU) {
	if !sys.Has("nvidia-smi") {
		return
	}
	out, err := sys.Output("nvidia-smi", "--query-gpu=pci.bus_id,name,memory.total", "--format=csv,noheader,nounits")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		cols := strings.Split(line, ", ")
		if len(cols) != 3 {
			continue
		}
		bus := strings.ToLower(cols[0]) // 00000000:01:00.0
		for i := range gpus {
			if strings.HasSuffix(bus, gpus[i].PCI[len(gpus[i].PCI)-7:]) {
				gpus[i].Name = cols[1]
				if mb, err := strconv.Atoi(cols[2]); err == nil {
					gpus[i].VRAMMB = mb
				}
				gpus[i].Discrete = true
			}
		}
	}
}

func tailscaleIP() string {
	if !sys.Has("tailscale") {
		return ""
	}
	out, err := sys.Output("tailscale", "ip", "-4")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Split(out, "\n")[0])
}

// modelServers finds almanac gateways and Ollama servers by their processes.
// Incus container processes are visible in the host's /proc, and their cgroup
// path names the container (lxc.payload.<name>), so this needs nothing from
// inside the containers.
func modelServers(proc string) []ModelServer {
	entries, _ := os.ReadDir(proc)
	found := map[ModelServer]bool{}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(proc, e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		kind, unit := classify(argv)
		if kind == "" {
			continue
		}
		container := containerOf(sys.ReadTrim(filepath.Join(proc, e.Name(), "cgroup")))
		found[ModelServer{Kind: kind, Container: container, Unit: unit}] = true
	}
	var out []ModelServer
	for s := range found {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].Container < out[j].Kind+out[j].Container })
	return out
}

func classify(argv []string) (kind, unit string) {
	base := filepath.Base(argv[0])
	joined := strings.Join(argv, " ")
	switch {
	case strings.Contains(joined, "almanac gateway") || (strings.HasPrefix(base, "python") && strings.Contains(joined, "almanac") && strings.Contains(joined, "gateway")):
		return "almanac", "almanac-gateway.service"
	case base == "ollama" && len(argv) > 1 && argv[1] == "serve":
		return "ollama", "ollama.service"
	}
	return "", ""
}

// containerOf reads an Incus/LXC container name from a /proc/<pid>/cgroup.
func containerOf(cgroup string) string {
	const mark = "lxc.payload."
	i := strings.Index(cgroup, mark)
	if i < 0 {
		return ""
	}
	name := cgroup[i+len(mark):]
	if j := strings.IndexAny(name, "/\n"); j >= 0 {
		name = name[:j]
	}
	return name
}

// PrimaryGPU is the GPU to stream from: the first discrete one, else the first.
func (f Facts) PrimaryGPU() (GPU, bool) {
	if len(f.GPUs) == 0 {
		return GPU{}, false
	}
	return f.GPUs[0], true
}

// Almanac returns the first almanac gateway found, if any.
func (f Facts) Almanac() (ModelServer, bool) {
	for _, s := range f.ModelServers {
		if s.Kind == "almanac" {
			return s, true
		}
	}
	return ModelServer{}, false
}

func mustOut(argv ...string) string {
	out, _ := sys.Output(argv...)
	return out
}

func lookPath(name string) string {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if sys.Exists(p) {
			return p
		}
	}
	return ""
}
