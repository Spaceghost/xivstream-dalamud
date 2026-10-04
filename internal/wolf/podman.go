package wolf

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Runner runs a command and returns its trimmed output (sys.Output in
// production; a fake in tests).
type Runner func(argv ...string) (string, error)

// SessionContainers lists the containers Wolf made for xivstream's app (and
// its PulseAudio sidecar, if an older Wolf started one).
func SessionContainers(run Runner) ([]string, error) {
	out, err := run("podman", "ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range strings.Fields(out) {
		if strings.HasPrefix(n, RunnerName+"_") || n == "WolfPulseAudio" {
			names = append(names, n)
		}
	}
	return names, nil
}

// Cleanup stops and removes Wolf's session containers: a game may outlive
// Wolf (Wolf removes a container only when its session ends normally). The
// stop gives the session ten seconds, more than Wolf's two.
func Cleanup(run Runner, log func(string)) error {
	names, err := SessionContainers(run)
	if err != nil {
		return err
	}
	var failed []string
	for _, n := range names {
		log("removing Wolf's leftover container " + n)
		_, _ = run("podman", "stop", "--time", "10", n)
		if _, err := run("podman", "rm", "--force", n); err != nil {
			failed = append(failed, n)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not remove %s", strings.Join(failed, ", "))
	}
	return nil
}

// IncusRunning reports whether an Incus container runs, from its cgroup (no
// incusd call: incusd is socket-activated, and this runs at boot).
func IncusRunning(cgroupRoot, name string) bool {
	if name == "" {
		return false
	}
	procs, err := os.ReadFile(filepath.Join(cgroupRoot, "lxc.payload."+name, "cgroup.procs"))
	if err == nil && len(strings.TrimSpace(string(procs))) > 0 {
		return true
	}
	// Processes live in child groups (init.scope, user.slice...).
	found := false
	_ = filepath.WalkDir(filepath.Join(cgroupRoot, "lxc.payload."+name), func(path string, d os.DirEntry, err error) error {
		if err != nil || found || d.IsDir() || d.Name() != "cgroup.procs" {
			return nil
		}
		if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			found = true
		}
		return nil
	})
	return found
}

// ImageExists: podman has the image locally.
func ImageExists(run Runner, image string) bool {
	_, err := run("podman", "image", "exists", image)
	return err == nil
}
