//go:build linux

package steps

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/mods"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

func TestGhosttySelectedModConfigNeverUsesPrivilegedUserPaths(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		c := config.Default()
		c.Session.User = "player"
		c.Mods.Companions = false
		c.Mods.Repos = []string{"https://fixture.invalid/plugins.json"}
		b := builder{c: c, ghostty: &ghosttySetup{label: "physical"}}
		plugins, err := mods.Parse([]byte(`[{"InternalName":"GhosttyDalamud","AssemblyVersion":"0.3.3.0","DownloadLinkInstall":"https://fixture.invalid/never-downloaded.zip"}]`), c.Mods.Repos[0])
		if err != nil {
			t.Fatal(err)
		}
		f := ghosttyFake(t)
		f.rejectOwnerPaths = true
		writes := 0
		payload := []byte(`{"UserSetting":"preserved"}`)
		f.run = func(a []string) (string, error) {
			if a[0] == "/usr/bin/stat" {
				return ownerHelperStat(a), nil
			}
			if a[0] == "sh" && strings.Contains(a[2], "pgrep") {
				return "", nil
			}
			t.Fatalf("unexpected privileged command %v", a)
			return "", nil
		}
		f.input = func(data []byte, a []string) (string, error) {
			if len(a) < 12 || a[0] != "/usr/bin/timeout" || a[5] != "player" {
				t.Fatal("unsafe owner invocation")
			}
			switch a[11] {
			case "identity":
				return `{"Protocol":"xivstream-owner-file-v1","User":"player","UID":"1000","Home":"/home/player"}`, nil
			case "inspect-ghostty-bundle":
				if a[12] != "0.3.3.0" {
					t.Fatal("wrong bundle")
				}
				return `{"exists":true}`, nil
			case "inspect":
				if a[12] != "dalamud-config" {
					t.Fatal("wrong object")
				}
				if unreadable {
					return "", errors.New("refused")
				}
				return `{"exists":true}`, nil
			case "read":
				return string(payload), nil
			case "write":
				if unreadable || a[12] != "dalamud-config" {
					t.Fatal("unsafe config write")
				}
				var object map[string]any
				if json.Unmarshal(data, &object) != nil || object["UserSetting"] != "preserved" || object["ThirdRepoList"] == nil {
					t.Fatal("user config lost")
				}
				payload = append([]byte(nil), data...)
				writes++
				return "ok\n", nil
			}
			t.Fatalf("unexpected helper operation %v", a)
			return "", nil
		}
		err = plan.Apply(io.Discard, b.modStepsResolved(f, "/home/player/.xlcore", "player:player", plugins))
		if (err != nil) != unreadable {
			t.Fatalf("apply err=%v unreadable=%v", err, unreadable)
		}
		if unreadable {
			if writes != 0 {
				t.Fatal("unreadable config replaced")
			}
		} else {
			if writes != 1 {
				t.Fatal("expected private write")
			}
			if err := plan.Apply(io.Discard, b.modStepsResolved(f, "/home/player/.xlcore", "player:player", plugins)); err != nil {
				t.Fatal(err)
			}
			if writes != 1 {
				t.Fatal("config reapply not idempotent")
			}
		}
	}
}

func TestGhosttyModRefusesLiveGameBeforeDownloadOrOwnerMutation(t *testing.T) {
	f := ghosttyFake(t)
	o := ghosttyOwnerFiles(f, "player", "/home/player", "/usr/local/bin/xivstream")
	s := ghosttyModStep(f, o, mods.Plugin{InternalName: "GhosttyDalamud"}, "0.3.3.0", "https://fixture.invalid/never", false, func() error { return errGameRunning })
	if err := s.Apply(); err != errGameRunning {
		t.Fatal("running game not refused")
	}
	if len(f.commands)+len(f.writes) != 0 {
		t.Fatal("live game refusal mutated")
	}
}

func TestGhosttySelectedConfigRefusesFailedGameProbe(t *testing.T) {
	c := config.Default()
	c.Session.User = "player"
	c.Mods.Companions = false
	b := builder{c: c, ghostty: &ghosttySetup{}}
	f := ghosttyFake(t)
	f.rejectOwnerPaths = true
	f.run = func(a []string) (string, error) {
		if a[0] == "/usr/bin/stat" {
			return ownerHelperStat(a), nil
		}
		if a[0] == "sh" {
			return "", errors.New("probe unavailable")
		}
		t.Fatal("unexpected command")
		return "", nil
	}
	f.input = func(_ []byte, a []string) (string, error) {
		switch a[11] {
		case "identity":
			return `{"Protocol":"xivstream-owner-file-v1","User":"player","UID":"1000","Home":"/home/player"}`, nil
		case "inspect":
			return `{"exists":false}`, nil
		default:
			t.Fatal("failed game probe permitted owner write")
		}
		return "", nil
	}
	if err := plan.Apply(io.Discard, b.modStepsResolved(f, "/home/player/.xlcore", "player:player", nil)); err == nil {
		t.Fatal("failed game probe accepted")
	}
}
