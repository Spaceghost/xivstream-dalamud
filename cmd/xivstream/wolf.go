package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/detect"
	"github.com/Spaceghost/xivstream-dalamud/internal/steps"
	"github.com/Spaceghost/xivstream-dalamud/internal/sys"
	"github.com/Spaceghost/xivstream-dalamud/internal/wolf"
)

func logf(m string) { log.Print(m) }

// loadWolf reads the config and insists on the Wolf backend.
func loadWolf(path string) (config.Config, wolf.Setup, error) {
	c, err := load(path)
	if err != nil {
		return c, wolf.Setup{}, err
	}
	if c.Backend != config.BackendWolf {
		return c, wolf.Setup{}, fmt.Errorf("%s: backend is %s, not wolf", path, c.Backend)
	}
	s, err := steps.WolfRuntimeSetup(c, detect.Facts{})
	return c, s, err
}

// runWolfConfig merges xivstream's app into Wolf's config.toml (creating it
// from Wolf's pinned default when missing). Wolf must not be running: it only
// reads the file at start.
func runWolfConfig(args []string) error {
	fs := flag.NewFlagSet("wolf-config", flag.ExitOnError)
	path := configFlag(fs)
	dry := fs.Bool("dry-run", false, "print the merged config instead of writing it")
	_ = fs.Parse(args)
	_, s, err := loadWolf(*path)
	if err != nil {
		return err
	}
	if *dry {
		old, _ := os.ReadFile(wolf.ConfigFile)
		out, err := wolf.MergeConfig(old, s, wolf.MergeOptions{Exists: sys.Exists})
		if err != nil {
			return err
		}
		os.Stdout.Write(out)
		return nil
	}
	return steps.WriteWolfConfig(s, logf)
}

// runWolfPreflight is wolf.service's ExecStartPre: refuse to run beside the
// Incus game, or without the game's home; clear Wolf's leftover session
// containers; then merge the config.
func runWolfPreflight(args []string) error {
	fs := flag.NewFlagSet("wolf-preflight", flag.ExitOnError)
	path := configFlag(fs)
	_ = fs.Parse(args)
	c, s, err := loadWolf(*path)
	if err != nil {
		return err
	}
	guard := s.GameContainer
	if guard == "" {
		guard = c.Incus.Container
	}
	if wolf.IncusRunning("/sys/fs/cgroup", guard) {
		return fmt.Errorf("the Incus game container %s is running (the Sunshine setup): two games would share one home and the stream's ports. Log out of the game, then `systemctl stop %s` (or `incus stop %s`) and start Wolf again", guard, wolf.FallbackUnit, guard)
	}
	if !wolf.HomeMounted(sys.Output, c.Wolf.Home, c.Wolf.HomeSubvolume) {
		return fmt.Errorf("%s is not the mounted %s subvolume (systemctl status %s)", c.Wolf.Home, c.Wolf.HomeSubvolume, s.HomeUnit())
	}
	if err := os.MkdirAll(wolf.HostRuntimeDir, 0o755); err != nil {
		return err
	}
	if err := wolf.Cleanup(sys.Output, logf); err != nil {
		return err
	}
	return steps.WriteWolfConfig(s, logf)
}

// runWolfCleanup stops and removes Wolf's session containers (wolf.service's
// ExecStopPost, and before the Sunshine fallback starts).
func runWolfCleanup(args []string) error {
	fs := flag.NewFlagSet("wolf-cleanup", flag.ExitOnError)
	_ = configFlag(fs)
	_ = fs.Parse(args)
	if !sys.Has("podman") {
		return nil
	}
	return wolf.Cleanup(sys.Output, logf)
}

// runDriverVolume (re)builds the NVIDIA driver volume when the loaded driver
// is not the one it holds.
func runDriverVolume(args []string) error {
	fs := flag.NewFlagSet("nvidia-driver-vol", flag.ExitOnError)
	_ = configFlag(fs)
	_ = fs.Parse(args)
	return steps.RunDriverVolume(logf)
}

// runStopContainer stops the Incus game container (the Sunshine fallback's
// ExecStop). Log out of the game first: this gives it 90 seconds.
func runStopContainer(args []string) error {
	fs := flag.NewFlagSet("stop-container", flag.ExitOnError)
	path := configFlag(fs)
	_ = fs.Parse(args)
	c, err := load(*path)
	if err != nil {
		return err
	}
	name := c.Incus.Container
	state, _ := sys.Output("incus", "list", "^"+name+"$", "-c", "s", "-f", "csv")
	if strings.TrimSpace(state) != "RUNNING" {
		return nil
	}
	_, err = sys.OutputTimeout(2*time.Minute, "incus", "stop", name, "--timeout", "90")
	return err
}

// pairWolf answers a pending Moonlight pairing through Wolf's API socket and
// sets the new client's pad type from stream.gamepad.
func pairWolf(c config.Config, pin, clientIP string) error {
	if os.Geteuid() != 0 {
		return errors.New("Wolf's API socket is root's: run pair with sudo")
	}
	err := wolf.PairClient(wolf.NewAPI(wolf.SocketPath), wolf.PairOptions{
		PIN: pin, ClientIP: clientIP, Pads: wolf.ControllersOverride(c.Stream.Gamepad),
		Wait: 30 * time.Second, Log: func(m string) { fmt.Println(m) },
	})
	if err != nil {
		return err
	}
	fmt.Println("Paired.")
	return nil
}
