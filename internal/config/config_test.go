package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	c := Default()
	c.Mods.Install = []string{"GhosttyDalamud"}
	c.Stream.ListenAddress = "100.64.0.1"
	if runtime.GOOS == "linux" {
		c.Incus.CPUPolicy.BusyCPUs = "4-7"
		c.Incus.CPUPolicy.IdleCPUs = "8"
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stream.ListenAddress != "100.64.0.1" || len(got.Mods.Install) != 1 || got.Session.User != "player" || got.Incus.CPUPolicy != c.Incus.CPUPolicy {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestPartialFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	_ = os.WriteFile(path, []byte("[game]\nfps = 30\n[selkies]\nport = 8443\n"), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backend != "sunshine" || c.Game.FPS != 30 || c.Game.Width != 1920 || c.Selkies.Port != 8443 || c.Selkies.User != "ffxiv" {
		t.Fatalf("partial: %+v", c)
	}
}

func TestRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	cases := map[string]string{
		"backend = \"vnc\"\n":                                           "backend",
		"topology = \"cloud\"\n":                                        "topology",
		"[gpu_share]\nmode = \"stop-unit\"\n":                           "unit",
		"typo = 1\n":                                                    "unknown keys",
		"[stream]\ncodecs = \"mpeg2\"\n":                                "codecs",
		"[incus.cpu_policy]\nbusy_cpus = \"4-7\"\n":                     "both",
		"[incus.cpu_policy]\nbusy_cpus = \"four\"\nidle_cpus = \"8\"\n": "CPU counts",
	}
	if runtime.GOOS == "linux" {
		cases["backend = \"wolf\"\ntopology = \"incus\"\n"] = "wolf"
	} else {
		cases["[incus.cpu_policy]\nbusy_cpus = \"4-7\"\n"] = "topology"
		cases["[incus.cpu_policy]\nbusy_cpus = \"four\"\nidle_cpus = \"8\"\n"] = "topology"
		cases["backend = \"selkies\"\n"] = "Linux hosts only"
		cases["topology = \"incus\"\n"] = "Linux host"
	}
	for body, want := range cases {
		_ = os.WriteFile(path, []byte(body), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error about %s", body, err, want)
		}
	}
}
