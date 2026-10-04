package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWolfDefaults(t *testing.T) {
	w := DefaultWolf()
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	if w.Gateway().String() != "10.89.10.1" {
		t.Errorf("gateway %s", w.Gateway())
	}
	if n, _ := w.MemoryBytes(); n != 12<<30 {
		t.Errorf("memory %d", n)
	}
	for in, want := range map[string]int64{"12GiB": 12 << 30, "512m": 512 << 20, "1073741824": 1 << 30, "": 0} {
		w.Memory = in
		if n, err := w.MemoryBytes(); err != nil || n != want {
			t.Errorf("%q: %d %v", in, n, err)
		}
	}
	if len(DefaultWolf().Forwards) != 6 || DefaultWolf().Forwards[5].Name != "voice-mic" {
		t.Error("the forwards mirror the ffxiv container's proxy devices")
	}
}

func TestWolfConfigFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Wolf is Linux-only")
	}
	path := filepath.Join(t.TempDir(), "c.toml")
	body := `topology = "host"
backend = "wolf"
[incus.cpu_policy]
busy_cpus = "4-7"
idle_cpus = "0-7"
[wolf]
allowed_clients = ["100.83.231.2"]
memory = "8GiB"
[[wolf.forwards]]
name = "almanac-gateway"
port = 41881
target = "100.100.110.16:41881"
`
	_ = os.WriteFile(path, []byte(body), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Wolf.Forwards) != 1 || c.Wolf.Home != "/var/lib/xivstream/home" || c.Wolf.Memory != "8GiB" || len(c.Wolf.Mounts) != 5 {
		t.Errorf("a [wolf] section keeps the defaults it does not set, and replaces lists it sets: %+v", c.Wolf)
	}
	if busy, idle := c.WolfCPUs(); busy != "4-7" || idle != "0-7" {
		t.Errorf("cpu_policy numbers reach Wolf: %s %s", busy, idle)
	}
	c.Wolf.CPUsBusy = "5-7"
	if busy, _ := c.WolfCPUs(); busy != "5-7" {
		t.Error("[wolf] overrides")
	}
	// Round trip through Encode (apply writes the live config this way).
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if again, err := Load(path); err != nil || again.Wolf.CPUsBusy != "5-7" || len(again.Wolf.Forwards) != 1 {
		t.Fatalf("round trip: %v %+v", err, again.Wolf)
	}
}

func TestWolfRejects(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Wolf is Linux-only")
	}
	path := filepath.Join(t.TempDir(), "c.toml")
	head := "topology = \"host\"\nbackend = \"wolf\"\n[wolf]\n"
	for body, want := range map[string]string{
		`subnet = "10.89.10.0/33"`:              "subnet",
		`subnet = "10.89.10.5/24"`:              "subnet",
		`app_ip = "10.89.11.10"`:                "app_ip",
		`app_ip = "10.89.10.1"`:                 "app_ip",
		`home = "relative/home"`:                "home",
		`home_subvolume = "a/b"`:                "home_subvolume",
		`home_device = "sdb3"`:                  "home_device",
		`network = "Has Spaces"`:                "network",
		`allowed_clients = ["everyone"]`:        "allowed_clients",
		`memory = "lots"`:                       "memory",
		`cpus_busy = "4"`:                       "CPU lists",
		`codecs = "av1"`:                        "codecs",
		`mounts = ["relative:/x"]`:              "mounts",
		`mounts = ["/a:/b:rx"]`:                 "mounts",
		`legacy_address = "fd00::1"`:            "legacy_address",
		"inbound = { ports = [0], peers = [] }": "inbound.ports",
		"forwards = [{ name = \"a\", port = 80, target = \"1.2.3.4:80\" }]":                                                         "1024-65535",
		"forwards = [{ name = \"a\", port = 4000, target = \"x:80\" }]":                                                             "target",
		"forwards = [{ name = \"a\", port = 4000, target = \"1.2.3.4:1\" }, { name = \"b\", port = 4000, target = \"1.2.3.4:1\" }]": "both use port",
	} {
		_ = os.WriteFile(path, []byte(head+body+"\n"), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error about %s", body, err, want)
		}
	}
	// The [wolf] section is only checked for the Wolf backend.
	_ = os.WriteFile(path, []byte("[wolf]\nsubnet = \"nonsense\"\n"), 0o600)
	if _, err := Load(path); err != nil {
		t.Errorf("sunshine with an unused [wolf]: %v", err)
	}
	// cpu_policy without Incus is still refused for other backends.
	_ = os.WriteFile(path, []byte("topology = \"host\"\n[incus.cpu_policy]\nbusy_cpus = \"4-7\"\nidle_cpus = \"0-7\"\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("cpu_policy on a Sunshine host setup")
	}
}

// TestDocumentedConfigLoads: docs/config.md's example is a valid config with
// no unknown keys, so the documentation keeps up with the code.
func TestDocumentedConfigLoads(t *testing.T) {
	doc, err := os.ReadFile("../../docs/config.md")
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(doc), "```toml\n")
	block, _, ok2 := strings.Cut(block, "```")
	if !ok || !ok2 {
		t.Fatal("no toml block in docs/config.md")
	}
	path := filepath.Join(t.TempDir(), "c.toml")
	_ = os.WriteFile(path, []byte(block), 0o600)
	c, err := Load(path)
	if err != nil && !(runtime.GOOS != "linux" && strings.Contains(err.Error(), "Linux")) {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		c.Backend, c.Topology = BackendWolf, TopologyHost
		if err := c.Validate(); err != nil {
			t.Errorf("the documented [wolf] section: %v", err)
		}
		if len(c.Wolf.Forwards) != 1 || c.Wolf.Codecs != "auto" || len(c.Wolf.Inbound.Ports) != 3 {
			t.Errorf("documented [wolf]: %+v", c.Wolf)
		}
	}
}
