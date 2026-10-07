// Package wizard asks what detection cannot answer and writes the
// configuration. Every question has the detected answer preselected; the last
// screen shows the resulting config and the exact plan before anything runs.
package wizard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/detect"
	"github.com/Spaceghost/xivstream-dalamud/internal/mods"
)

// Result is what the user chose to do with the configuration.
type Result int

const (
	Apply Result = iota
	SaveOnly
	Cancel
)

// Run asks the questions, starting from start (the existing config or the
// defaults). advanced adds the questions most people never change.
func Run(start config.Config, f detect.Facts, advanced bool, review func(config.Config) string) (config.Config, Result, error) {
	c := start
	suggest(&c, f)

	if err := form(huh.NewGroup(
		huh.NewNote().Title("xivstream").Description(summary(f)),
		huh.NewSelect[string]().Title("Where should the game run?").
			Options(topologies(f)...).Value(&c.Topology),
	)).Run(); err != nil {
		return c, Cancel, err
	}

	if err := form(huh.NewGroup(
		huh.NewSelect[string]().Title("Streaming server").
			Description("Sunshine and Wolf serve Moonlight clients (phones, tablets, PCs, TVs, handhelds); Selkies serves any web browser.").
			Options(backends(c, f)...).Value(&c.Backend),
	)).Run(); err != nil {
		return c, Cancel, err
	}

	var groups []*huh.Group
	if c.Topology == config.TopologyIncus {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Container name").Value(&c.Incus.Container),
			huh.NewInput().Title("Container image").Description("Fedora is what this is tested on.").Value(&c.Incus.Image),
		))
	}
	if runtime.GOOS == "linux" && c.Topology == config.TopologyHost && c.Backend != config.BackendWolf && !f.Immutable {
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().Title("A separate headless session?").
				Description("Yes: a dedicated session nobody sees locally (a server, a spare PC).\nNo: stream the desktop you are logged in to.").
				Value(&c.Session.Headless),
		))
	}
	if runtime.GOOS == "linux" {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Session user").Description("Created if missing. Empty keeps "+c.Session.User+".").Value(&c.Session.User),
		))
	}
	resolution := fmt.Sprintf("%dx%d@%d", c.Game.Width, c.Game.Height, c.Game.FPS)
	groups = append(groups, huh.NewGroup(
		huh.NewSelect[string]().Title("Stream resolution").Options(resolutions(resolution, f)...).Value(&resolution),
		huh.NewSelect[int]().Title("Bitrate cap").
			Description("What the server encodes at most, whatever a client asks. Wi-Fi clients lose packets (flicker) above what their link carries.").
			Options(huh.NewOption("20 Mbit/s (Wi-Fi, mobile data)", 20000), huh.NewOption("35 Mbit/s", 35000),
				huh.NewOption("50 Mbit/s (recommended)", 50000), huh.NewOption("80 Mbit/s (wired LAN)", 80000)).
			Value(&c.Stream.MaxBitrateKbps),
		huh.NewSelect[string]().Title("Where clients connect").Options(listens(c, f)...).Value(&c.Stream.ListenAddress),
	))
	if advanced {
		port := strconv.Itoa(c.Stream.PortBase)
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().Title("Codecs").Options(
				huh.NewOption("H.264 only (every client decodes it)", "h264"), huh.NewOption("Automatic (HEVC/AV1 when the client can)", "auto")).
				Value(&c.Stream.Codecs),
			huh.NewSelect[string]().Title("Controller type the game sees").Options(
				huh.NewOption("Xbox 360 (works everywhere, also under Wine)", "x360"), huh.NewOption("DualSense (touchpad, PS button; hidraw is passed through for you)", "ds5"), huh.NewOption("Automatic", "auto")).
				Value(&c.Stream.Gamepad),
			huh.NewInput().Title("Base port").Value(&port).Validate(func(s string) error {
				n, err := strconv.Atoi(s)
				if err != nil || n < 1030 || n > 65000 {
					return errors.New("a port between 1030 and 65000")
				}
				c.Stream.PortBase = n
				return nil
			}),
		))
		if c.Topology == config.TopologyIncus {
			groups = append(groups, huh.NewGroup(
				huh.NewInput().Title("CPUs while the host is busy").
					Description("Examples: 4-7 pins four cores; 4 allows any four. Empty disables dynamic CPU limits.").Value(&c.Incus.CPUPolicy.BusyCPUs),
				huh.NewInput().Title("CPUs while the host is idle").
					Description("Examples: 0-7 pins all eight; 8 allows any eight. Set both busy and idle values.").Value(&c.Incus.CPUPolicy.IdleCPUs),
			))
		}
	}
	groups = append(groups, huh.NewGroup(
		huh.NewConfirm().Title("Keep all sound inside the stream?").
			Description("Nothing can play aloud on this machine; you hear the game only through the client.").
			Value(&c.Audio.StreamOnly),
	))
	if share := shareOptions(f); len(share) > 1 {
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().Title("Something else uses this GPU").
				Description("What should happen to it while the game runs?").Options(share...).Value(&c.Share.Mode),
		))
	}
	keep := c
	if err := form(groups...).Run(); err != nil {
		return c, Cancel, err
	}
	// An empty answer keeps the default (in accessible mode an empty line
	// clears a prefilled field).
	for _, f := range []struct {
		now *string
		was string
	}{
		{&c.Incus.Container, keep.Incus.Container}, {&c.Incus.Image, keep.Incus.Image}, {&c.Session.User, keep.Session.User},
	} {
		if strings.TrimSpace(*f.now) == "" {
			*f.now = f.was
		}
	}
	fmt.Sscanf(resolution, "%dx%d@%d", &c.Game.Width, &c.Game.Height, &c.Game.FPS)
	applyShare(&c, f)

	if err := chooseMods(&c); err != nil {
		return c, Cancel, err
	}

	result := Apply
	if err := form(huh.NewGroup(
		huh.NewNote().Title("Review").Description(review(c)),
		huh.NewSelect[Result]().Title("Now what?").Options(
			huh.NewOption("Apply it", Apply), huh.NewOption("Save the config only (apply later)", SaveOnly), huh.NewOption("Cancel", Cancel)).
			Value(&result),
	)).Run(); err != nil {
		return c, Cancel, err
	}
	return c, result, nil
}

// form builds a form. ACCESSIBLE=1, or input that is not a terminal (answers
// piped in by a script), gives plain line-by-line prompts; they also suit
// screen readers.
func form(groups ...*huh.Group) *huh.Form {
	f := huh.NewForm(groups...)
	if accessible() {
		f = f.WithAccessible(true).WithInput(stdin)
	}
	return f
}

func accessible() bool {
	if os.Getenv("ACCESSIBLE") != "" {
		return true
	}
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice == 0
}

// stdin hands out one byte per Read. huh's accessible mode starts a new
// bufio.Scanner for every prompt, and a scanner reading the real stdin buffers
// ahead: with answers piped in, the first prompt swallowed all the others.
var stdin io.Reader = byteReader{os.Stdin}

type byteReader struct{ r io.Reader }

func (b byteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return b.r.Read(p[:1])
}

// suggest fills in what detection knows, where the config has no opinion yet.
func suggest(c *config.Config, f detect.Facts) {
	if !f.Incus && c.Topology == config.TopologyIncus {
		c.Topology = config.TopologyHost
	}
	if c.Topology == config.TopologyHost && runtime.GOOS == "linux" {
		if u, err := user.Current(); err == nil && u.Username != "root" && c.Session.User == "player" {
			c.Session.User = u.Username
		}
		if s := os.Getenv("SUDO_USER"); s != "" && c.Session.User == "player" {
			c.Session.User = s
		}
		c.Session.Headless = f.Device == "" && !hasDesktop()
	}
	switch f.Device {
	case "steam-deck":
		c.Game.Width, c.Game.Height = 1280, 800
	}
}

func hasDesktop() bool { return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" }

func summary(f detect.Facts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sets up game streaming for FINAL FANTASY XIV on this machine.\nNothing changes until the last screen.\n\n")
	fmt.Fprintf(&b, "System:     %s (%s, %s)\n", f.Pretty, orDash(f.Init), orDash(f.Packages))
	if f.Device != "" {
		fmt.Fprintf(&b, "Device:     %s\n", f.Device)
	}
	for _, g := range f.GPUs {
		name := g.Name
		if name == "" {
			name = g.Vendor + " (" + g.Driver + ")"
		}
		vram := ""
		if g.VRAMMB > 0 {
			vram = fmt.Sprintf(", %d MB", g.VRAMMB)
		}
		fmt.Fprintf(&b, "GPU:        %s%s %s\n", name, vram, g.PCI)
	}
	fmt.Fprintf(&b, "Incus:      %s   Podman: %s   Docker: %s\n", yes(f.Incus), yes(f.Podman), yes(f.Docker))
	fmt.Fprintf(&b, "Tailscale:  %s\n", orDash(f.Tailscale))
	for _, m := range f.ModelServers {
		where := "this machine"
		if m.Container != "" {
			where = "container " + m.Container
		}
		fmt.Fprintf(&b, "On the GPU: %s in %s\n", m.Kind, where)
	}
	return b.String()
}

func topologies(f detect.Facts) []huh.Option[string] {
	var o []huh.Option[string]
	if f.Incus {
		o = append(o, huh.NewOption("In an Incus container on this host (isolated; the GPU is passed through)", config.TopologyIncus))
	}
	o = append(o, huh.NewOption("Directly on this machine", config.TopologyHost))
	return o
}

func backends(c config.Config, f detect.Facts) []huh.Option[string] {
	o := []huh.Option[string]{huh.NewOption("Sunshine: Moonlight clients (recommended)", config.BackendSunshine)}
	if runtime.GOOS != "linux" || f.Immutable {
		return o
	}
	o = append(o, huh.NewOption("Selkies: any web browser, no app needed", config.BackendSelkies))
	if c.Topology == config.TopologyHost && (f.Podman || f.Docker) {
		o = append(o, huh.NewOption("Wolf: Moonlight, one private session per client (experimental)", config.BackendWolf))
	}
	return o
}

func resolutions(current string, f detect.Facts) []huh.Option[string] {
	list := []string{"1280x720@60", "1280x800@60", "1920x1080@60", "1920x1200@60", "2560x1440@60", "3840x2160@60", "1920x1080@120"}
	found := false
	for _, r := range list {
		found = found || r == current
	}
	if !found {
		list = append(list, current)
	}
	sort.Strings(list)
	var o []huh.Option[string]
	for _, r := range list {
		label := strings.Replace(r, "@", " at ", 1) + " Hz"
		switch r {
		case "1280x800@60":
			label += " (Steam Deck)"
		case "1920x1080@60":
			label += " (recommended)"
		}
		o = append(o, huh.NewOption(label, r))
	}
	return o
}

func listens(c config.Config, f detect.Facts) []huh.Option[string] {
	var o []huh.Option[string]
	if f.Tailscale != "" {
		o = append(o, huh.NewOption("Tailscale only: "+f.Tailscale+" (recommended: reachable anywhere, never public)", f.Tailscale))
	}
	o = append(o, huh.NewOption("This machine's LAN address (decided at apply time)", ""))
	if c.Stream.ListenAddress != "" && c.Stream.ListenAddress != f.Tailscale {
		o = append(o, huh.NewOption("Keep "+c.Stream.ListenAddress, c.Stream.ListenAddress))
	}
	return o
}

func shareOptions(f detect.Facts) []huh.Option[string] {
	o := []huh.Option[string]{huh.NewOption("Nothing: leave it alone", config.ShareNone)}
	for _, m := range f.ModelServers {
		switch m.Kind {
		case "almanac":
			return append([]huh.Option[string]{huh.NewOption("almanac: switch to a model that fits beside the game (recommended)", config.ShareReserve)}, o...)
		case "ollama":
			o = append(o, huh.NewOption("Ollama: stop it while the game runs", config.ShareStopUnit))
		}
	}
	return o
}

func applyShare(c *config.Config, f detect.Facts) {
	for _, m := range f.ModelServers {
		if (c.Share.Mode == config.ShareReserve && m.Kind == "almanac") || (c.Share.Mode == config.ShareStopUnit && m.Kind == "ollama") {
			c.Share.Container, c.Share.Unit = m.Container, m.Unit
			if m.Kind == "almanac" && c.Share.TokenFile == "" {
				c.Share.TokenFile = "/home/almanac/.config/almanac/token"
				if m.Container == "" {
					home, _ := os.UserHomeDir()
					c.Share.TokenFile = home + "/.config/almanac/token"
				}
			}
			return
		}
	}
}

// chooseMods lists what the configured repositories offer, live.
func chooseMods(c *config.Config) error {
	var available []mods.Plugin
	var failed []string
	for _, repo := range c.Mods.Repos {
		plugins, err := mods.Fetch(repo)
		if err != nil {
			failed = append(failed, repo)
			continue
		}
		available = append(available, plugins...)
	}
	if len(available) == 0 {
		return nil
	}
	var opts []huh.Option[string]
	for _, p := range available {
		label := p.Name
		if p.Punchline != "" {
			label += ": " + p.Punchline
		}
		if needs := p.Needs(); len(needs) > 0 {
			label += " (adds " + strings.Join(needs, ", ") + ")"
		}
		opts = append(opts, huh.NewOption(label, p.InternalName).Selected(contains(c.Mods.Install, p.InternalName)))
	}
	desc := "From " + strings.Join(c.Mods.Repos, ", ") + ". Space to pick, enter to go on. Installed when the game is closed."
	if len(failed) > 0 {
		desc += "\n(Could not reach " + strings.Join(failed, ", ") + ".)"
	}
	return form(huh.NewGroup(
		huh.NewMultiSelect[string]().Title("Mods (Dalamud plugins)").Description(desc).Options(opts...).Value(&c.Mods.Install),
		huh.NewConfirm().Title("Use testing versions where there are any?").Value(&c.Mods.Testing),
		huh.NewConfirm().Title("Also install what the mods need outside the game?").
			Description("For example ghostty-agent, the terminal server the Ghostty mod's in-game terminals connect to.").
			Value(&c.Mods.Companions),
	)).Run()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
