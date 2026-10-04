package steps

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Spaceghost/xivstream-dalamud/internal/assets"
	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/detect"
	"github.com/Spaceghost/xivstream-dalamud/internal/wolf"
)

func wolfConfig() config.Config {
	c := config.Default()
	c.Topology, c.Backend = config.TopologyHost, config.BackendWolf
	c.Stream.Gamepad = "ds5"
	c.Share.Mode, c.Share.Container, c.Share.TokenFile = config.ShareReserve, "almanac", "/x"
	c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs = "4-7", "0-7"
	c.Wolf.HomeDevice = "UUID=1c289f95-5b61-4816-9b55-e161e424f3ff" // no findmnt in the test
	return c
}

func wolfFacts() detect.Facts {
	return detect.Facts{OS: "linux", Init: "systemd", Podman: true,
		GPUs: []detect.GPU{{Vendor: "nvidia", RenderNode: "/dev/dri/renderD129", Discrete: true}}}
}

func titles(t *testing.T, c config.Config) []string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Wolf is Linux-only")
	}
	s, err := Build(c, wolfFacts())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, st := range s {
		out = append(out, st.Title)
	}
	return out
}

func index(list []string, prefix string) int {
	for i, s := range list {
		if strings.HasPrefix(s, prefix) {
			return i
		}
	}
	return -1
}

func TestWolfPlan(t *testing.T) {
	got := titles(t, wolfConfig())
	order := []string{
		"Install xivstream on this host",
		"Load uhid, nvidia_uvm, nvidia_modeset at boot",
		"Install Wolf's udev rules (pinned)",
		"Reload udev's rules (no trigger)",
		"Enable the Podman socket",
		"Create the session network xivstream (10.89.10.0/24)",
		"Write the NVIDIA driver volume service",
		"Build the NVIDIA driver volume for this driver",
		"Create the home's btrfs subvolume xivstream-home",
		"Write the home's mount unit (/var/lib/xivstream/home)",
		"Mount the home (var-lib-xivstream-home.mount)",
		"Build the session image localhost/xivstream-session:",
		"Write Wolf's nft table",
		"Write the firewall unit",
		"Write the almanac-gateway forward",
		"Start the session's forwards",
		"Write Wolf's Quadlet unit",
		"Put xivstream's app into Wolf's config",
		"Install game-cpu-fence",
		"Run game-cpu-fence",
		"Turn off the Incus-only services",
		"Write /etc/xivstream/config.toml",
		"Write the GPU sharing service",
		"Reload systemd (Quadlet regenerates wolf.service)",
		"Start Wolf",
	}
	last := -1
	for _, want := range order {
		i := index(got, want)
		if i < 0 {
			t.Errorf("no step %q in:\n%s", want, strings.Join(got, "\n"))
			continue
		}
		if i < last {
			t.Errorf("%q comes too early", want)
		}
		last = i
	}
	for _, never := range []string{"Write the container start service", "Write the dynamic CPU policy service", "Create the Incus container"} {
		if index(got, never) >= 0 {
			t.Errorf("an Incus step in the Wolf plan: %s", never)
		}
	}
	c := wolfConfig()
	c.Wolf.Autostart = false
	c.Wolf.Image = "registry.example/session:1"
	got = titles(t, c)
	if index(got, "Start Wolf") >= 0 || index(got, "Build the session image") >= 0 {
		t.Error("autostart = false does not start Wolf; a configured image is not built")
	}
}

func TestWolfPlanNeedsPodmanAndSystemd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	f := wolfFacts()
	f.Podman = false
	if _, err := Build(wolfConfig(), f); err == nil {
		t.Error("no podman")
	}
	f = wolfFacts()
	f.Init = "openrc"
	if _, err := Build(wolfConfig(), f); err == nil {
		t.Error("no systemd")
	}
}

func TestWolfPorts(t *testing.T) {
	c := wolfConfig()
	byName := map[string]int{}
	for _, p := range Ports(c) {
		byName[p.Name] = p.Num
	}
	if byName["video"] != 48100 || byName["audio"] != 48200 || byName["http"] != 47989 {
		t.Errorf("Wolf's ports: %v", byName)
	}
}

// TestSunshineRuleTemplateIsTheGatedRule: what the incus path renders now is
// exactly an older rule with the gate added (so the gate is all that changes
// for Sunshine), and udev accepts it.
func TestSunshineRuleTemplateIsTheGatedRule(t *testing.T) {
	for _, hidraw := range []string{"", "/dev/xivstream/ffxiv"} {
		v := view{Config: config.Default(), HostUID: 1001000, HostGID: 1000104, InputMarks: []string{"libvirtualhid"}, HidrawDir: hidraw}
		rule := string(assets.MustRender("udev.rules", v))
		if !strings.Contains(rule, wolf.SunshineGateStart) || !strings.HasSuffix(rule, "\n"+wolf.SunshineGateEnd) {
			t.Fatalf("the template lacks the gate:\n%s", rule)
		}
		old := strings.Replace(rule, wolf.SunshineGateStart+"\n", "", 1)
		old = strings.TrimSuffix(old, "\n"+wolf.SunshineGateEnd)
		if got := string(wolf.GateSunshineRule([]byte(old))); got != rule {
			t.Errorf("gating the old rule gives:\n%s\nthe template:\n%s", got, rule)
		}
		if udevadm, err := exec.LookPath("udevadm"); err == nil {
			path := filepath.Join(t.TempDir(), "70-xivstream-ffxiv.rules")
			_ = os.WriteFile(path, []byte(rule), 0o644)
			if out, err := exec.Command(udevadm, "verify", "--no-style", path).CombinedOutput(); err != nil {
				t.Errorf("udevadm verify: %v\n%s", err, out)
			}
		}
	}
}
