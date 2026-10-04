package steps

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/gpushare"
	"github.com/Spaceghost/xivstream-dalamud/internal/mods"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
	"github.com/Spaceghost/xivstream-dalamud/internal/releases"
)

var errGameRunning = errors.New("the game is running; Dalamud rewrites its configuration while it runs. Close the game and run apply again")

// modSteps installs the configured Dalamud plugins into the launcher's
// directory dir on t (~/.xlcore for XIVLauncher.Core), owned by owner
// ("user:group", or "" where ownership does not apply).
func (b *builder) modSteps(t plan.Target, dir, owner string) []plan.Step {
	c := b.c
	if len(c.Mods.Install) == 0 {
		return nil
	}
	var available []mods.Plugin
	var fetchErr error
	for _, repo := range c.Mods.Repos {
		plugins, err := mods.Fetch(repo)
		if err != nil {
			fetchErr = err
			continue
		}
		available = append(available, plugins...)
	}
	chosen, err := mods.Resolve(available, c.Mods.Install)
	if err != nil {
		if fetchErr != nil {
			err = fmt.Errorf("%w (and %v)", err, fetchErr)
		}
		return []plan.Step{step(t, "Install mods", "", nil, nil, func() error { return err })}
	}
	return b.modStepsResolved(t, dir, owner, chosen)
}

// Kept separate from repository I/O so selected owner-boundary regressions
// exercise the real plan without network calls.
func (b *builder) modStepsResolved(t plan.Target, dir, owner string, chosen []mods.Plugin) []plan.Step {
	c := b.c
	notRunning := func() error {
		if _, local := t.(plan.Local); local {
			if gpushare.GameRunning(c.Game.Process, "") {
				return errGameRunning
			}
			return nil
		}
		probe := `pgrep -f 'ffxiv_dx11\.exe' || true`
		if b.ghostty != nil {
			probe = `pgrep -f 'ffxiv_dx11\.exe'; xivstream_probe_status=$?; test "$xivstream_probe_status" -eq 1`
		}
		out, err := t.Run("sh", "-c", probe)
		if strings.TrimSpace(out) != "" {
			return errGameRunning
		}
		if err != nil && b.ghostty != nil {
			return errors.New("cannot establish that the game is stopped before Ghostty mod/config writes")
		}
		return nil
	}
	var s []plan.Step
	var ownerFiles *ghosttyOwnerTarget
	if b.ghostty != nil {
		ownerFiles = ghosttyOwnerFiles(t, b.c.Session.User, "/home/"+b.c.Session.User, "/usr/local/bin/xivstream")
		if dir != path.Join(ownerFiles.home, ".xlcore") {
			return []plan.Step{step(t, "Validate Ghostty mod owner path", "", nil, nil, func() error { return errors.New("Ghostty mod directory does not match the session account") })}
		}
		s = append(s, step(t, "Validate Ghostty mod owner isolation", "The helper is installed and the session user exists before user-owned mod files are inspected.", []string{"validate trusted helper and explicit session owner"}, func() (bool, error) { err := ownerFiles.probe(); return err == nil, err }, ownerFiles.probe))
	}
	var testing []string
	for _, p := range chosen {
		version, url, isTesting := p.Pick(c.Mods.Testing)
		if isTesting {
			testing = append(testing, p.InternalName)
		}
		dest := path.Join(dir, "installedPlugins", p.InternalName, version)
		if ownerFiles != nil && p.InternalName == "GhosttyDalamud" {
			s = append(s, ghosttyModStep(t, ownerFiles, p, version, url, isTesting, notRunning))
			continue
		}
		s = append(s, step(t, fmt.Sprintf("Install the %s mod %s", p.InternalName, version), p.Punchline,
			[]string{"download " + url, "unpack into " + dest},
			exists(t, path.Join(dest, p.InternalName+".dll")),
			func() error {
				if err := notRunning(); err != nil {
					return err
				}
				zip, err := mods.Download(url)
				if err != nil {
					return err
				}
				tree, err := mods.Unpack(zip, p, version, isTesting)
				if err != nil {
					return err
				}
				return writeTree(t, dest, tree, owner)
			}))
	}
	cfgPath := path.Join(dir, "dalamudConfig.json")
	var configFiles plan.Target = t
	if ownerFiles != nil {
		configFiles = ownerFiles
	}
	readConfig := func() ([]byte, error) {
		if ownerFiles != nil {
			m, err := ownerFiles.metadata(cfgPath)
			if err != nil {
				return nil, err
			}
			if !m.Exists {
				return nil, nil
			}
			return ownerFiles.ReadFile(cfgPath)
		}
		old, _ := t.ReadFile(cfgPath)
		return old, nil
	}
	s = append(s, step(t, "Add the mod repositories to Dalamud", strings.Join(c.Mods.Repos, ", "),
		[]string{"edit " + cfgPath + " (ThirdRepoList" + map[bool]string{true: ", testing opt-ins", false: ""}[len(testing) > 0] + ")"},
		func() (bool, error) {
			old, readErr := readConfig()
			if readErr != nil {
				return false, readErr
			}
			want, err := mods.ConfigureDalamud(old, c.Mods.Repos, testing)
			return err == nil && len(old) > 0 && strings.TrimSpace(string(old)) == strings.TrimSpace(string(want)), nil
		},
		func() error {
			if err := notRunning(); err != nil {
				return err
			}
			old, readErr := readConfig()
			if readErr != nil {
				return readErr
			}
			want, err := mods.ConfigureDalamud(old, c.Mods.Repos, testing)
			if err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if ownerFiles != nil {
				mode = 0600
			}
			return configFiles.WriteFile(cfgPath, want, mode, owner)
		}))
	if c.Mods.Companions {
		s = append(s, b.companions(t, chosen, owner)...)
	}
	return s
}

// companions installs what a mod needs outside the game.
func (b *builder) companions(t plan.Target, chosen []mods.Plugin, owner string) []plan.Step {
	var s []plan.Step
	for _, p := range chosen {
		switch p.InternalName {
		case "GhosttyDalamud":
			if b.ghostty != nil {
				s = append(s, b.ghosttySteps(t, owner)...)
				continue
			}
			if targetOS(t) != "linux" {
				continue // Windows: the plugin runs its shells in-process when no agent answers
			}
			user := strings.SplitN(owner, ":", 2)[0]
			s = append(s, step(t, "Install ghostty-agent (the Ghostty mod's terminal server)",
				"The in-game terminals connect to it on 127.0.0.1:7777; it runs the shells on the Linux side of Wine.",
				[]string{"install the newest ghostty-agent release package", "systemctl --user -M " + user + "@ enable --now ghostty-agent"},
				succeeds(t, "sh", "-c", "command -v ghostty-agent && systemctl --user -M "+user+"@ is-enabled -q ghostty-agent"),
				func() error {
					pm, err := packageManager(t)
					if err != nil {
						return err
					}
					pre := b.c.Mods.Testing
					switch pm {
					case "dnf":
						fc, _ := t.Run("rpm", "-E", "%fedora")
						a, err := releases.Find("Spaceghost/ghostty-dalamud", `^ghostty-agent\.fc`+strings.TrimSpace(fc)+`\.x86_64\.rpm$`, pre)
						if err != nil {
							return err
						}
						_, err = t.Run("sh", "-c", `set -e; d=$(mktemp -d); curl -fsSL -o "$d/agent.rpm" "$1"; dnf -y install "$d/agent.rpm"; rm -rf "$d"`, "-", a.URL)
						if err != nil {
							return err
						}
					default:
						a, err := releases.Find("Spaceghost/ghostty-dalamud", `^ghostty-agent-linux-x86_64\.tar\.gz$`, pre)
						if err != nil {
							return err
						}
						if _, err := t.Run("sh", "-c", `set -e; curl -fsSL "$1" | tar -xz -C /usr/local`, "-", a.URL); err != nil {
							return err
						}
					}
					_, err = t.Run("systemctl", "--user", "-M", user+"@", "enable", "--now", "ghostty-agent.service")
					return err
				}))
		}
	}
	return s
}

// writeTree writes an unpacked plugin: in one tar stream on Linux targets
// (one exec into a container, not one per file), file by file elsewhere.
func writeTree(t plan.Target, dest string, tree mods.Tree, owner string) error {
	if targetOS(t) == "linux" {
		// Ownership: the plugin's own directory recursively, and the two levels
		// above it (installedPlugins, the launcher's directory) only themselves;
		// the launcher's directory also holds the ~80 GB game install.
		script := `set -e; mkdir -p "$1"; tar -C "$1" -xf -
[ -z "$2" ] && exit 0
p=$(dirname "$1"); chown -R "$2" "$p"; chown "$2" "$(dirname "$p")" "$(dirname "$(dirname "$p")")"`
		_, err := t.RunInput(tree.Tar(), "sh", "-c", script, "-", dest, owner)
		return err
	}
	for name, data := range tree {
		if err := t.WriteFile(path.Join(dest, name), data, 0o644, ""); err != nil {
			return err
		}
	}
	return nil
}

func targetOS(t plan.Target) string {
	if _, ok := t.(plan.Incus); ok {
		return "linux"
	}
	return runtime.GOOS
}
