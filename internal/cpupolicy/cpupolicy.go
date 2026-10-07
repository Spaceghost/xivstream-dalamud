// Package cpupolicy yields CPUs from the game container while the host is busy.
package cpupolicy

import (
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
	Source      string
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
	current = p.BusyCPUs
	window := loadWindow{}
	allocation := allocationState{mode: mode, cpus: current}
	if err := writeStatus(mode, "unknown", current, "warming", "", c); err != nil {
		log.Printf("cpu-policy status: %v", err)
	}

	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for range ticker.C {
		now, err := readSample(c.Incus.Container)
		sampleError := ""
		if err != nil {
			log.Printf("cpu-policy sample: %v", err)
			sampleError = err.Error()
			now = Sample{} // unknown never earns the idle allocation
		}
		percent, known, wanted := window.observe(now, p, runtime.NumCPU())
		if err := allocation.apply(wanted, p, func(cpus string) error { return setLimit(c.Incus.Container, cpus) }); err != nil {
			log.Printf("cpu-policy allocation: %v", err)
			sampleError = err.Error()
		}
		if mode != allocation.mode {
			mode = allocation.mode
			log.Printf("CPU policy %s; set %s limits.cpu=%s", mode, c.Incus.Container, allocation.cpus)
		}
		current = allocation.cpus
		measurement := "unknown"
		if known {
			measurement = strconv.Itoa(percent)
		}
		if err := writeStatus(mode, measurement, current, now.Source, sampleError, c); err != nil {
			log.Printf("cpu-policy status: %v", err)
		}
	}
	return nil
}

func readSample(container string) (Sample, error) {
	usage, source, err := containerUsage(container, os.ReadFile, apiContainerUsage)
	if err != nil {
		return Sample{}, err
	}
	// Read host counters AFTER a potentially slow API fallback, not seconds
	// before it. The kernel fast path keeps both sides of this sample adjacent.
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
	return Sample{Total: total, Idle: idle, ContainerNS: usage, At: time.Now(), Source: source}, nil
}

func limit(container string) (string, error) {
	return sys.OutputTimeout(4*time.Second, "incus", "--force-local", "config", "get", container, "limits.cpu")
}

func setLimit(container, cpus string) error {
	_, err := sys.OutputTimeout(4*time.Second, "incus", "--force-local", "config", "set", container, "limits.cpu="+cpus)
	return err
}

func writeStatus(mode, percent, current, source, sampleError string, c config.Config) error {
	dir := os.Getenv("RUNTIME_DIRECTORY")
	if dir == "" {
		dir = "/run/xivstream-cpu-policy"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Status must not introduce another daemon round trip into every sample.
	// limits_cpu is the last confirmed successful startup/read or policy write.
	sampleError = strings.Join(strings.Fields(sampleError), " ")
	if len(sampleError) > 400 {
		sampleError = sampleError[:400]
	}
	text := fmt.Sprintf("mode=%s\nnon_game_busy_percent=%s\ncontainer=%s\nlimits_cpu=%s\nlimits_source=last_confirmed\nbusy_cpus=%s\nidle_cpus=%s\nsample_source=%s\nsample_error=%s\nupdated_at=%s\n",
		mode, percent, c.Incus.Container, current, c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs,
		source, sampleError, time.Now().UTC().Format(time.RFC3339))
	temporary := filepath.Join(dir, "status.tmp")
	if err := os.WriteFile(temporary, []byte(text), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(dir, "status"))
}
