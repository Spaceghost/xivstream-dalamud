package steps

import (
	"fmt"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
	"github.com/Spaceghost/xivstream-dalamud/internal/sys"
)

// linuxHost: the game and the server run on this Linux machine directly.
// Headless: a dedicated session for Session.User (a spare PC, a server).
// Otherwise: Sunshine streams the desktop you are logged in to.
func (b *builder) linuxHost() ([]plan.Step, error) {
	c := b.c
	host := plan.Local{}
	u := c.Session.User
	var s []plan.Step
	s = append(s, b.installSelf(host, plan.Incus{})[0])
	if c.Session.Headless {
		s = append(s, b.session(host, false)...)
	} else {
		s = append(s, b.backendInstall(host)...)
		s = append(s, step(host, "Start Sunshine with your desktop session", "Sunshine captures the desktop you are logged in to.",
			[]string{"systemctl --user -M " + u + "@ enable --now sunshine"},
			succeeds(host, "systemctl", "--user", "-M", u+"@", "is-enabled", "-q", "sunshine"),
			func() error {
				_, err := sys.Output("systemctl", "--user", "-M", u+"@", "enable", "--now", "sunshine")
				return err
			}))
	}
	s = append(s, b.hostServices(host)...)
	return append(s, b.modSteps(host, "/home/"+u+"/.xlcore", u+":"+u)...), nil
}

// flatpakHost: read-only systems (Bazzite, SteamOS, Kinoite, ...): Flatpaks,
// and Sunshine streaming the desktop or Game Mode you are in.
func (b *builder) flatpakHost() ([]plan.Step, error) {
	c := b.c
	host := plan.Local{}
	u := c.Session.User
	var s []plan.Step
	if c.Backend != config.BackendSunshine {
		return nil, fmt.Errorf("on %s use backend = sunshine (Selkies needs packages this system does not layer)", b.f.Pretty)
	}
	s = append(s, step(host, "Install Flathub's XIVLauncher", "dev.goats.xivlauncher: the launcher and Dalamud, sandboxed.",
		[]string{"flatpak install -y --noninteractive flathub dev.goats.xivlauncher"},
		succeeds(host, "flatpak", "info", "dev.goats.xivlauncher"),
		func() error {
			_, err := sys.Output("flatpak", "install", "-y", "--noninteractive", "flathub", "dev.goats.xivlauncher")
			return err
		}))
	if b.f.Sunshine != "" { // Bazzite ships it
		s = append(s, step(host, "Start the system's Sunshine with your session", "Bazzite ships Sunshine; this enables it for "+u+".",
			[]string{"systemctl --user -M " + u + "@ enable --now sunshine"},
			succeeds(host, "systemctl", "--user", "-M", u+"@", "is-enabled", "-q", "sunshine"),
			func() error {
				_, err := sys.Output("systemctl", "--user", "-M", u+"@", "enable", "--now", "sunshine")
				return err
			}))
	} else {
		s = append(s, step(host, "Install Flathub's Sunshine", "dev.lizardbyte.app.Sunshine, plus its input rules and autostart (its own installer script).",
			[]string{"flatpak install -y --noninteractive flathub dev.lizardbyte.app.Sunshine", "flatpak run --command=additional-install.sh dev.lizardbyte.app.Sunshine"},
			succeeds(host, "flatpak", "info", "dev.lizardbyte.app.Sunshine"),
			func() error {
				if _, err := sys.Output("flatpak", "install", "-y", "--noninteractive", "flathub", "dev.lizardbyte.app.Sunshine"); err != nil {
					return err
				}
				_, err := sys.Output("su", "-", u, "-c", "flatpak run --command=additional-install.sh dev.lizardbyte.app.Sunshine")
				return err
			}))
	}
	s = append(s, b.hostServices(host)...)
	return append(s, b.modSteps(host, "/home/"+u+"/.var/app/dev.goats.xivlauncher/data/xlcore", u+":"+u)...), nil
}
