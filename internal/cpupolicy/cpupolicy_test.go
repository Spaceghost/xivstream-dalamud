package cpupolicy

import (
	"testing"
	"time"
)

func TestNonGameBusySubtractsContainer(t *testing.T) {
	start := time.Unix(0, 0)
	before := Sample{Total: 1000, Idle: 500, ContainerNS: 10_000_000_000, At: start}
	// Across four CPUs for ten seconds: host is 75% busy, the container used
	// 50% of the whole machine, so other work is using 25%.
	after := Sample{Total: 5000, Idle: 1500, ContainerNS: 30_000_000_000, At: start.Add(10 * time.Second)}
	if got := NonGameBusyPercent(before, after, 4); got != 25 {
		t.Fatalf("got %d, want 25", got)
	}
}

func TestNonGameBusyClampsAndRejectsBadSamples(t *testing.T) {
	start := time.Unix(0, 0)
	if got := NonGameBusyPercent(Sample{Total: 1, At: start}, Sample{Total: 2, ContainerNS: 99_000_000_000, At: start.Add(time.Second)}, 8); got != 0 {
		t.Fatalf("negative result was not clamped: %d", got)
	}
	if got := NonGameBusyPercent(Sample{Total: 2, At: start}, Sample{Total: 1, At: start.Add(time.Second)}, 8); got != 0 {
		t.Fatalf("bad sample = %d", got)
	}
}
