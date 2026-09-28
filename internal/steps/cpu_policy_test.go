package steps

import (
	"runtime"
	"strings"
	"testing"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
)

func TestCPUOnlyPlan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Incus is Linux-only")
	}
	c := config.Default()
	c.Topology = config.TopologyIncus
	c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs = "4-7", "0-7"
	s, err := CPUOnly(c, "systemd")
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 4 || !s[3].IfChanged {
		t.Fatalf("missing configuration, unit, enable or restart: %+v", s)
	}
	for _, step := range s {
		for _, command := range step.Shows {
			if strings.Contains(command, "incus start") || strings.Contains(command, "session.service") {
				t.Fatalf("CPU-only plan touches game: %s", command)
			}
		}
	}
	c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs = "", ""
	s, err = CPUOnly(c, "systemd")
	if err != nil || len(s) != 2 || !strings.Contains(s[1].Shows[0], "disable --now") {
		t.Fatalf("disable plan: %+v %v", s, err)
	}
}
