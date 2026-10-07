// Package steps turns a configuration into the plan that sets a machine up:
// one list of idempotent steps per topology and platform.
//
// Values that depend on the machine (the container's address, its id map,
// the GPU's render node, the newest release of a download) are looked up when
// a step runs, not when the plan is built, so a plan built before the
// container exists still does the right thing.
package steps

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/detect"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

// Build returns the steps for c on the machine described by f.
func Build(c config.Config, f detect.Facts) ([]plan.Step, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	b := &builder{c: c, f: f}
	if err := b.prepareGhostty(); err != nil {
		return nil, err
	}
	switch {
	case c.Topology == config.TopologyIncus:
		return b.incus()
	case runtime.GOOS == "linux" && c.Backend == config.BackendWolf:
		return b.wolf()
	case runtime.GOOS == "linux" && f.Immutable:
		return b.flatpakHost()
	case runtime.GOOS == "linux":
		return b.linuxHost()
	case runtime.GOOS == "windows":
		return b.windows()
	case runtime.GOOS == "darwin":
		return b.macos()
	}
	return nil, fmt.Errorf("no setup for %s", runtime.GOOS)
}

type builder struct {
	c       config.Config
	f       detect.Facts
	ghostty *ghosttySetup
}

// view is what the templates see.
type view struct {
	config.Config
	InContainer     bool
	NVIDIA          bool
	Headless        bool
	Encoder         string
	LauncherCommand string
	HostUID         int
	HostGID         int
	InputMarks      []string
	HidrawDir       string // the host directory for the streamed pads' hidraw nodes, or ""
}

// ContainerHidrawDir is where the container sees them (inputbridge.HidrawDir).
const ContainerHidrawDir = "/dev/hidraw-stream"

// HidrawHostDir is where the host's udev rule copies the hidraw nodes of the
// pads Sunshine emulates as PlayStation pads (gamepad "ds5" or "auto"), for
// the container to see at /dev/hidraw-stream; "" with an Xbox pad or another
// server. It is under /dev because /run is mounted nodev: device nodes there
// cannot be opened, not even through a bind mount.
func HidrawHostDir(c config.Config) string {
	if c.Backend != config.BackendSunshine || c.Stream.Gamepad == "" || c.Stream.Gamepad == "x360" {
		return ""
	}
	return "/dev/xivstream/" + c.Incus.Container
}

func (b *builder) view(inContainer bool) view {
	gpu, _ := b.f.PrimaryGPU()
	v := view{
		Config: b.c, InContainer: inContainer, NVIDIA: gpu.Vendor == "nvidia", Headless: b.c.Session.Headless,
		LauncherCommand: "/opt/xivlauncher/XIVLauncher.Core",
		InputMarks:      []string{"libvirtualhid"},
		HidrawDir:       HidrawHostDir(b.c),
	}
	switch gpu.Vendor {
	case "nvidia":
		v.Encoder = "nvenc"
	case "amd", "intel":
		v.Encoder = "vaapi"
	}
	return v
}

func (b *builder) nvidia() bool {
	gpu, _ := b.f.PrimaryGPU()
	return gpu.Vendor == "nvidia"
}

// secret returns a password kept in the state directory, made on first use,
// so re-running apply keeps the one the user already has.
func secret(name, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	path := filepath.Join(config.StateDir(), name)
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		return strings.TrimSpace(string(data)), nil
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	pw := base64.RawURLEncoding.EncodeToString(raw[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return pw, os.WriteFile(path, []byte(pw+"\n"), 0o600)
}

// SecretPath is where a generated password is kept (shown after apply).
func SecretPath(name string) string { return filepath.Join(config.StateDir(), name) }

func render(name string, v any) []byte {
	return mustRender(name, v)
}

// always is a Check for steps that must run each time (cheap and idempotent).
func always() (bool, error) { return false, nil }

// exists is a Check: done when path exists on t.
func exists(t plan.Target, path string) func() (bool, error) {
	return func() (bool, error) { return t.Exists(path), nil }
}

// succeeds is a Check: done when argv exits 0 on t.
func succeeds(t plan.Target, argv ...string) func() (bool, error) {
	return func() (bool, error) {
		_, err := t.Run(argv...)
		return err == nil, nil
	}
}

// step builds a step whose Apply is a Go function.
func step(t plan.Target, title, why string, shows []string, check func() (bool, error), apply func() error) plan.Step {
	return plan.Step{Title: title, Why: why, On: t, Shows: shows, Check: check, Apply: apply}
}
