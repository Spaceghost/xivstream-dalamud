package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGhosttyConfigGenericDefaultsAndRoundTrip(t *testing.T) {
	c := Default()
	g := c.Mods.Ghostty
	if g.HostUser != "" || g.HostLabel != "" || g.IncludeContainer || g.HostPort != 7777 || g.ProxyPort != 7780 {
		t.Fatalf("non-generic defaults: %+v", g)
	}
	c.Mods.Ghostty = Ghostty{HostUser: "native-user", HostLabel: "Actual workstation", HostPort: 12001, ProxyPort: 12002, IncludeContainer: true}
	p := filepath.Join(t.TempDir(), "fixture.toml")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || !reflect.DeepEqual(got.Mods.Ghostty, c.Mods.Ghostty) {
		t.Fatalf("roundtrip: %+v %v", got.Mods.Ghostty, err)
	}
}

func TestGhosttyConfigRejectsUnsafeValues(t *testing.T) {
	for _, change := range []func(*Ghostty){
		func(g *Ghostty) { g.HostPort = 0 }, func(g *Ghostty) { g.HostPort = 1023 }, func(g *Ghostty) { g.ProxyPort = 65536 },
		func(g *Ghostty) { g.IncludeContainer = true; g.ProxyPort = 7777 },
		func(g *Ghostty) { g.HostUser = "root" }, func(g *Ghostty) { g.HostUser = "-service" }, func(g *Ghostty) { g.HostUser = "user; command" },
		func(g *Ghostty) { g.HostLabel = strings.Repeat("x", 65) }, func(g *Ghostty) { g.HostLabel = "name\x01bad" },
	} {
		g := Default().Mods.Ghostty
		change(&g)
		if err := g.Validate(); err == nil {
			t.Fatalf("accepted %+v", g)
		}
	}
}
