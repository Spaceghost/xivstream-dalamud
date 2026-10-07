package steps

import (
	"errors"
	"reflect"
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
	if err != nil || len(s) != 3 || !strings.Contains(s[1].Shows[0], "disable --now") || !strings.Contains(s[2].Shows[0], "limits.cpu=") {
		t.Fatalf("disable plan: %+v %v", s, err)
	}
}

func TestRestoreStaticCPULimitIndependentOfService(t *testing.T) {
	current := "4-7"
	var commands [][]string
	var readErr, writeErr error
	output := func(argv ...string) (string, error) {
		commands = append(commands, append([]string(nil), argv...))
		switch {
		case reflect.DeepEqual(argv, []string{"incus", "--force-local", "config", "get", "game-fixture", "limits.cpu"}):
			return current, readErr
		case reflect.DeepEqual(argv, []string{"incus", "--force-local", "config", "set", "game-fixture", "limits.cpu=0-7"}):
			if writeErr == nil {
				current = "0-7\n"
			}
			return "", writeErr
		default:
			t.Fatalf("unexpected command (must not start/stop game or require service): %v", argv)
			return "", nil
		}
	}
	s := restoreStaticCPULimit("game-fixture", "0-7", output)
	if done, err := s.Check(); done || err != nil {
		t.Fatalf("busy allocation should need restoration: done=%v err=%v", done, err)
	}
	writeErr = errors.New("Incus unavailable")
	if err := s.Apply(); !errors.Is(err, writeErr) {
		t.Fatalf("write failure must propagate: %v", err)
	}
	if done, err := s.Check(); done || err != nil {
		t.Fatalf("a failed prior restoration must be retried: done=%v err=%v", done, err)
	}
	writeErr = nil
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	if done, err := s.Check(); !done || err != nil {
		t.Fatalf("completed restoration should be idempotent: done=%v err=%v", done, err)
	}
	readErr = errors.New("cannot observe limit")
	if done, err := s.Check(); done || !errors.Is(err, readErr) {
		t.Fatalf("unknown is not completed: done=%v err=%v", done, err)
	}
	if len(commands) != 6 {
		t.Fatalf("unexpected command count: %d", len(commands))
	}
}
