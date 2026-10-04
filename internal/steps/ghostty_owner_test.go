//go:build linux

package steps

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

func TestGhosttyOwnerHelperRunsOnlyAsExplicitOwnerWithSecretStdin(t *testing.T) {
	f := ghosttyFake(t)
	f.run = func(a []string) (string, error) { return ownerHelperStat(a), nil }
	const secret = "synthetic-private-credential-12345\n"
	f.input = func(data []byte, a []string) (string, error) {
		want := []string{"/usr/bin/timeout", "--kill-after=2s", "10s", "/usr/sbin/runuser", "-u", "player", "--", "/usr/bin/env", "-i", "/usr/local/bin/xivstream", "internal-owner-file-v1", "write", "host-token-copy"}
		if strings.Join(a, "|") != strings.Join(want, "|") || string(data) != secret {
			t.Fatal("unsafe invocation contract")
		}
		return "ok\n", nil
	}
	o := ghosttyOwnerFiles(f, "player", "/home/player", "/usr/local/bin/xivstream")
	if err := o.WriteFile("/home/player/.xlcore/pluginConfigs/GhosttyDalamud/host-agent.token", []byte(secret), 0600, "ignored"); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.commands {
		if strings.Contains(strings.Join(a, " "), secret) {
			t.Fatal("secret in argv")
		}
	}
	if len(f.writes) != 0 {
		t.Fatal("privileged path write")
	}
}
func TestGhosttyOwnerHelperTrustFailureNeverExecutes(t *testing.T) {
	for _, bad := range []string{"", "41ed:0\n41ed:0\n41ed:0\n41ed:0\n81ff:0\n", "41ed:0\n41ed:0\n41ed:0\n41ed:0\n81ed:1000\n", "41ed:0\n41ed:0\n41ed:0\n41ed:0\na1ff:0\n"} {
		f := ghosttyFake(t)
		f.run = func([]string) (string, error) { return bad, nil }
		o := ghosttyOwnerFiles(f, "player", "/home/player", "/usr/local/bin/xivstream")
		if err := o.probe(); err == nil {
			t.Fatal("unsafe helper accepted")
		}
	}
}
func TestGhosttyOwnerHelperRejectsUnknownPathAndModeBeforeExecution(t *testing.T) {
	f := ghosttyFake(t)
	o := ghosttyOwnerFiles(f, "player", "/home/player", "/usr/local/bin/xivstream")
	for _, p := range []string{"/etc/passwd", "/home/other/.config/xivstream/ghostty-host.token", "/home/player/.ssh/id_ed25519"} {
		if _, err := o.ReadFile(p); err == nil {
			t.Fatal("arbitrary path")
		}
	}
	if err := o.WriteFile("/home/player/.xlcore/pluginConfigs/GhosttyDalamud/host-agent.token", nil, 0644, ""); err == nil {
		t.Fatal("public mode")
	}
	if len(f.commands)+len(f.writes) != 0 {
		t.Fatal("invalid request executed")
	}
}
func TestGhosttyTokenRefusesOrdinaryRootTargetsBeforeReading(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "protected")
	before := []byte("synthetic-private-credential-12345\n")
	if err := os.WriteFile(victim, before, 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source")
	if err := os.Symlink(victim, source); err != nil {
		t.Fatal(err)
	}
	f := ghosttyFake(t)
	if err := ghosttyTokenStep(plan.Local{}, source, ghosttyIsolatedFake{f}, "/dest", "").Apply(); err == nil {
		t.Fatal("ordinary root reader accepted")
	}
	f.files[source] = before
	if err := ghosttyTokenStep(ghosttyIsolatedFake{f}, source, plan.Local{}, victim, "").Apply(); err == nil {
		t.Fatal("ordinary root writer accepted")
	}
	got, err := os.ReadFile(victim)
	if err != nil || !bytes.Equal(got, before) {
		t.Fatal("protected preimage changed")
	}
}
func TestGhosttyHelperIdentityMismatchFailsClosed(t *testing.T) {
	f := ghosttyFake(t)
	f.run = func(a []string) (string, error) { return ownerHelperStat(a), nil }
	f.input = func([]byte, []string) (string, error) {
		return `{"Protocol":"xivstream-owner-file-v1","User":"other","UID":"1000","Home":"/home/player"}`, nil
	}
	if err := ghosttyOwnerFiles(f, "player", "/home/player", "/usr/local/bin/xivstream").probe(); err == nil {
		t.Fatal("wrong account")
	}
	f.input = func([]byte, []string) (string, error) { return "synthetic-secret", errors.New("synthetic-secret") }
	if err := ghosttyOwnerFiles(f, "player", "/home/player", "/usr/local/bin/xivstream").probe(); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("unsafe error")
	}
}

func TestGhosttyHelperAcceptsOnlyFixedFedoraRootOwnedAlias(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		f := ghosttyFake(t)
		f.run = func(a []string) (string, error) {
			if a[0] == "/usr/bin/readlink" {
				return "../var/usrlocal\n", nil
			}
			if len(a) == 5 {
				if unsafe {
					return "41ed:0\n41ff:0\n", nil
				}
				return "41ed:0\n41ed:0\n", nil
			}
			return "41ed:0\n41ed:0\na1ff:0\n41ed:0\n81ed:0\n", nil
		}
		if err := ghosttyOwnerFiles(f, "player", "/home/player", "/usr/local/bin/xivstream").trustedHelper(); (err != nil) != unsafe {
			t.Fatal("Fedora alias validation")
		}
	}
}

func TestGhosttyFullReapplyUsesOwnerFilesWithoutRestart(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{false: "host only", true: "optional container"}[include], func(t *testing.T) {
			c := config.Default()
			c.Mods.Ghostty.IncludeContainer = include
			c.Incus.Container = "game-fixture"
			c.Session.User = "player"
			b := builder{c: c, ghostty: &ghosttySetup{account: &user.User{Username: "native-user", Uid: "1000", Gid: "1000", HomeDir: "/home/native-user"}, label: "physical-fixture"}}
			host, ct := ghosttyFake(t), ghosttyFake(t)
			writes := 0
			for _, pair := range []struct {
				f          *ghosttyFakeTarget
				name, kind string
			}{{host, "native-user", "host"}, {ct, "player", "container"}} {
				f, name, kind := pair.f, pair.name, pair.kind
				f.rejectOwnerPaths = true
				f.files["/var/lib/systemd/linger/"+name] = nil
				store := map[string][]byte{"agent-binary": []byte("\x7fELFexisting"), kind + "-unit": ghosttyUnit(7777, kind), kind + "-token-source": []byte("synthetic-private-token-12345\n")}
				f.run = func(a []string) (string, error) {
					switch a[0] {
					case "/usr/bin/stat":
						return ownerHelperStat(a), nil
					case "ss":
						return "", nil
					case "test", "sh":
						return "", nil
					case "systemctl":
						if a[4] == "is-enabled" || a[4] == "is-active" {
							return "", nil
						}
					case "incus":
						if a[1] == "list" {
							data, _ := json.Marshal([]any{map[string]any{"name": "game-fixture", "status": "Running", "devices": map[string]any{ghosttyProxy: ghosttyProxyOptions(c.Mods.Ghostty)}}})
							return string(data), nil
						}
						if len(a) == 7 && a[1] == "config" && a[3] == "get" {
							return ghosttyProxyOptions(c.Mods.Ghostty)[a[6]], nil
						}
					}
					t.Fatalf("unexpected privileged command: %v", a)
					return "", errors.New("forbidden")
				}
				f.input = func(data []byte, a []string) (string, error) {
					if len(a) < 12 || a[0] != "/usr/bin/timeout" || a[5] != name || a[9] != "/usr/local/bin/xivstream" || a[10] != "internal-owner-file-v1" {
						t.Fatalf("unsafe helper argv %v", a)
					}
					if a[11] == "identity" {
						return `{"Protocol":"xivstream-owner-file-v1","User":"` + name + `","UID":"1000","Home":"/home/` + name + `"}`, nil
					}
					if len(a) != 13 {
						t.Fatal("unexpected arguments")
					}
					object := a[12]
					switch a[11] {
					case "inspect":
						_, ok := store[object]
						if ok {
							return `{"exists":true}`, nil
						}
						return `{"exists":false}`, nil
					case "read":
						if value, ok := store[object]; ok {
							return string(value), nil
						}
						return "", errors.New("missing")
					case "write":
						if object != "host-token-copy" && object != "container-token-copy" && object != "defaults" {
							t.Fatal("existing binary/unit replaced")
						}
						store[object] = append([]byte(nil), data...)
						writes++
						return "ok\n", nil
					}
					t.Fatal("unknown helper operation")
					return "", nil
				}
			}
			if err := plan.Apply(io.Discard, b.ghosttyStepsOn(host, ct, "player:player")); err != nil {
				t.Fatal(err)
			}
			want := 2
			if include {
				want = 3
			}
			if writes != want {
				t.Fatalf("private bootstrap writes %d want%d", writes, want)
			}
			if err := plan.Apply(io.Discard, b.ghosttyStepsOn(host, ct, "player:player")); err != nil {
				t.Fatal(err)
			}
			if writes != want {
				t.Fatal("reapply was not idempotent")
			}
		})
	}
}

func TestGhosttyMissingTrustedHelperStopsBeforeBootstrapMutation(t *testing.T) {
	c := config.Default()
	c.Session.User = "player"
	b := builder{c: c, ghostty: &ghosttySetup{account: &user.User{Username: "native-user", Uid: "1000", Gid: "1000", HomeDir: "/home/native-user"}, label: "physical"}}
	f := ghosttyFake(t)
	f.rejectOwnerPaths = true
	f.run = func(a []string) (string, error) {
		if a[0] != "/usr/bin/stat" {
			t.Fatal("control-plane touched before helper")
		}
		return "", errors.New("not installed")
	}
	if err := plan.Apply(io.Discard, b.ghosttyStepsOn(f, f, "player:player")); err == nil {
		t.Fatal("missing helper accepted")
	}
	if len(f.writes) != 0 {
		t.Fatal("missing-helper mutation")
	}
}

type delayedGhosttyToken struct {
	ghosttyIsolatedFake
	reads int
}

func (s *delayedGhosttyToken) ReadFile(string) ([]byte, error) {
	s.reads++
	if s.reads < 3 {
		return nil, os.ErrNotExist
	}
	return []byte("synthetic-delayed-private-token-12345\n"), nil
}
func TestGhosttyWaitsForActualTokenPersistenceWithoutAgentRestart(t *testing.T) {
	src := &delayedGhosttyToken{ghosttyIsolatedFake: ghosttyIsolatedFake{ghosttyFake(t)}}
	dest := ghosttyIsolatedFake{ghosttyFake(t)}
	if err := ghosttyTokenStep(src, "/source", dest, "/target", "").Apply(); err != nil {
		t.Fatal(err)
	}
	if src.reads != 3 || len(dest.writes) != 1 || len(src.commands)+len(dest.commands) != 0 {
		t.Fatal("token readiness changed a service")
	}
}
