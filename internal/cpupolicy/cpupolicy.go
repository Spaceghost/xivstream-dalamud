// Package cpupolicy yields CPUs from the game container while the host is busy.
package cpupolicy

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/sys"
)

const sampleInterval = 5 * time.Second

type Sample struct {
	Total, Idle uint64
	ContainerNS uint64
	At          time.Time
}

// NonGameBusyPercent returns whole-system CPU busy percentage after subtracting
// the measured container usage. It is pure so the accounting is host-tested.
func NonGameBusyPercent(before, after Sample, cpus int) int {
	if cpus <= 0 || !after.At.After(before.At) || after.Total <= before.Total || after.Idle < before.Idle {
		return 0
	}
	total := after.Total - before.Total
	idle := after.Idle - before.Idle
	if idle > total {
		idle = total
	}
	hostBusy := float64(total-idle) / float64(total) * 100
	containerBusy := 0.0
	if after.ContainerNS >= before.ContainerNS {
		containerBusy = float64(after.ContainerNS-before.ContainerNS) / float64(after.At.Sub(before.At)) / float64(cpus) * 100
	}
	result := int(hostBusy - containerBusy + 0.5)
	if result < 0 {
		return 0
	}
	if result > 100 {
		return 100
	}
	return result
}

// Run watches non-game CPU use until the service manager stops it.
func Run(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	p := c.Incus.CPUPolicy
	if !p.Enabled() {
		return nil
	}
	online, err := os.ReadFile("/sys/devices/system/cpu/online")
	if err != nil {
		return err
	}
	for _, selection := range []string{p.BusyCPUs, p.IdleCPUs} {
		if err := ValidateSelection(selection, strings.TrimSpace(string(online))); err != nil {
			return err
		}
	}
	current, err := limit(c.Incus.Container)
	if err != nil {
		return err
	}
	// Start conservatively; earn the idle allocation with a fresh idle window.
	mode := "busy"
	if current != p.BusyCPUs {
		if err := setLimit(c.Incus.Container, p.BusyCPUs); err != nil {
			return err
		}
	}
	previous, err := readSample(c.Incus.Container)
	if err != nil {
		return err
	}
	busyFor, idleFor := time.Duration(0), time.Duration(0)
	if err := writeStatus(mode, "unknown", c); err != nil {
		log.Printf("cpu-policy status: %v", err)
	}

	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for range ticker.C {
		now, err := readSample(c.Incus.Container)
		if err != nil {
			log.Printf("cpu-policy sample: %v", err)
			busyFor, idleFor = 0, 0
			previous = Sample{}
			continue
		}
		if previous.At.IsZero() || now.ContainerNS < previous.ContainerNS {
			previous = now
			busyFor, idleFor = 0, 0
			continue
		}
		percent := NonGameBusyPercent(previous, now, runtime.NumCPU())
		previous = now
		switch {
		case percent >= p.BusyThresholdPercent:
			busyFor += sampleInterval
			idleFor = 0
		case percent <= p.IdleThresholdPercent:
			idleFor += sampleInterval
			busyFor = 0
		default:
			busyFor, idleFor = 0, 0
		}
		if mode != "busy" && busyFor >= time.Duration(p.BusyAfterSeconds)*time.Second {
			if err := setLimit(c.Incus.Container, p.BusyCPUs); err != nil {
				log.Printf("cpu-policy restrict: %v", err)
			} else {
				mode, busyFor = "busy", 0
				log.Printf("host is busy; set %s limits.cpu=%s", c.Incus.Container, p.BusyCPUs)
			}
		} else if mode != "idle" && idleFor >= time.Duration(p.IdleAfterSeconds)*time.Second {
			if err := setLimit(c.Incus.Container, p.IdleCPUs); err != nil {
				log.Printf("cpu-policy expand: %v", err)
			} else {
				mode, idleFor = "idle", 0
				log.Printf("host is idle; set %s limits.cpu=%s", c.Incus.Container, p.IdleCPUs)
			}
		}
		if err := writeStatus(mode, strconv.Itoa(percent), c); err != nil {
			log.Printf("cpu-policy status: %v", err)
		}
	}
	return nil
}

func readSample(container string) (Sample, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return Sample{}, err
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 6 || fields[0] != "cpu" {
		return Sample{}, fmt.Errorf("unexpected /proc/stat CPU line %q", line)
	}
	var values []uint64
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return Sample{}, fmt.Errorf("parse /proc/stat: %w", err)
		}
		values = append(values, value)
	}
	var total uint64
	// guest and guest_nice are already included in user and nice.
	for _, value := range values[:min(8, len(values))] {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	out, err := sys.OutputTimeout(4*time.Second, "incus", "--force-local", "query", "/1.0/instances/"+container+"/state")
	if err != nil {
		return Sample{}, err
	}
	var state struct {
		CPU struct {
			Usage *uint64 `json:"usage"`
		} `json:"cpu"`
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return Sample{}, err
	}
	if state.CPU.Usage == nil {
		return Sample{}, fmt.Errorf("container CPU usage unavailable")
	}
	return Sample{Total: total, Idle: idle, ContainerNS: *state.CPU.Usage, At: time.Now()}, nil
}

func limit(container string) (string, error) {
	return sys.Output("incus", "--force-local", "config", "get", container, "limits.cpu")
}

func setLimit(container, cpus string) error {
	_, err := sys.Output("incus", "--force-local", "config", "set", container, "limits.cpu="+cpus)
	return err
}

func writeStatus(mode, percent string, c config.Config) error {
	dir := os.Getenv("RUNTIME_DIRECTORY")
	if dir == "" {
		dir = "/run/xivstream-cpu-policy"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	current, err := limit(c.Incus.Container)
	if err != nil {
		current = "unknown"
	}
	text := fmt.Sprintf("mode=%s\nnon_game_busy_percent=%s\ncontainer=%s\nlimits_cpu=%s\nbusy_cpus=%s\nidle_cpus=%s\n",
		mode, percent, c.Incus.Container, current, c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs)
	temporary := filepath.Join(dir, "status.tmp")
	if err := os.WriteFile(temporary, []byte(text), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(dir, "status"))
}
