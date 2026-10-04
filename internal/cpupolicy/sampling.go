package cpupolicy

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/sys"
)

// Incus's standard local LXC payload group accounts for the entire container,
// including its child slices. This read is independent of daemon/network state
// collection. Other cgroup layouts and v1 hosts retain the bounded API fallback.
func containerUsage(container string, readFile func(string) ([]byte, error), fallback func(string) (uint64, error)) (uint64, string, error) {
	if container == "" || len(container) > 63 {
		return 0, "", fmt.Errorf("invalid local container name")
	}
	for _, ch := range container {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
			return 0, "", fmt.Errorf("invalid local container name")
		}
	}
	path := filepath.Join("/sys/fs/cgroup", "lxc.payload."+container, "cpu.stat")
	if data, err := readFile(path); err == nil {
		usage, err := usageNanoseconds(data)
		return usage, "cgroup-v2", err // malformed accounting must not become zero/idle
	}
	usage, err := fallback(container)
	return usage, "incus", err
}

func usageNanoseconds(data []byte) (uint64, error) {
	if len(data) > 64*1024 {
		return 0, fmt.Errorf("cgroup CPU accounting exceeds its bound")
	}
	var usage uint64
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "usage_usec" {
			continue
		}
		if found || len(fields) != 2 {
			return 0, fmt.Errorf("invalid cgroup usage_usec field")
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > math.MaxUint64/1000 {
			return 0, fmt.Errorf("invalid or overflowing cgroup usage_usec")
		}
		usage, found = value*1000, true
	}
	if !found {
		return 0, fmt.Errorf("cgroup CPU usage unavailable")
	}
	return usage, nil
}

func apiContainerUsage(container string) (uint64, error) {
	out, err := sys.OutputTimeout(4*time.Second, "incus", "--force-local", "query", "/1.0/instances/"+container+"/state")
	if err != nil {
		return 0, err
	}
	var state struct {
		CPU struct {
			Usage *uint64 `json:"usage"`
		} `json:"cpu"`
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return 0, err
	}
	if state.CPU.Usage == nil {
		return 0, fmt.Errorf("container CPU usage unavailable")
	}
	return *state.CPU.Usage, nil
}

type loadWindow struct {
	previous         Sample
	busyFor, idleFor time.Duration
}

type allocationState struct {
	mode, cpus      string
	restrictPending bool
}

// A rejected conservative write stays pending even if the next load lies in
// the hysteresis gap. Expansion is never queued across unknown/neutral input.
func (s *allocationState) apply(wanted string, policy config.CPUPolicy, set func(string) error) error {
	if wanted == "busy" && s.mode != "busy" {
		s.restrictPending = true
	}
	if s.restrictPending {
		wanted = "busy"
	}
	if wanted == "" || wanted == s.mode {
		return nil
	}
	cpus := policy.BusyCPUs
	if wanted == "idle" {
		cpus = policy.IdleCPUs
	}
	if err := set(cpus); err != nil {
		return err
	}
	s.mode, s.cpus, s.restrictPending = wanted, cpus, false
	return nil
}

// Unknown, reset or stale measurements immediately request the conservative
// allocation and clear both hysteresis windows. Never treat failed queries as
// idle, nor bank the missing time toward a later expansion.
func (w *loadWindow) observe(now Sample, policy config.CPUPolicy, cpus int) (percent int, known bool, wanted string) {
	before := w.previous
	w.previous = now
	elapsed := now.At.Sub(before.At)
	if now.At.IsZero() || before.At.IsZero() || elapsed <= 0 || elapsed > sampleInterval*3 ||
		now.Total <= before.Total || now.Idle < before.Idle || now.ContainerNS < before.ContainerNS ||
		now.Source != before.Source || cpus <= 0 {
		w.busyFor, w.idleFor = 0, 0
		return 0, false, "busy"
	}
	percent = NonGameBusyPercent(before, now, cpus)
	// One observation earns at most one interval, even if the daemon stalled.
	elapsed = min(elapsed, sampleInterval)
	switch {
	case percent >= policy.BusyThresholdPercent:
		w.busyFor += elapsed
		w.idleFor = 0
	case percent <= policy.IdleThresholdPercent:
		w.idleFor += elapsed
		w.busyFor = 0
	default:
		w.busyFor, w.idleFor = 0, 0
	}
	if w.busyFor >= time.Duration(policy.BusyAfterSeconds)*time.Second {
		return percent, true, "busy"
	}
	if w.idleFor >= time.Duration(policy.IdleAfterSeconds)*time.Second {
		return percent, true, "idle"
	}
	return percent, true, ""
}
