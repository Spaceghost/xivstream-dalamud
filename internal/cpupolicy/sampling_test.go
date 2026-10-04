package cpupolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
)

func TestKernelContainerUsageDoesNotCallDaemon(t *testing.T) {
	got, source, err := containerUsage("ffxiv", func(path string) ([]byte, error) {
		if path != "/sys/fs/cgroup/lxc.payload.ffxiv/cpu.stat" {
			t.Fatalf("wrong group: %s", path)
		}
		return []byte("usage_usec 746414883524\nuser_usec 634978387494\nsystem_usec 111436496030\n"), nil
	}, func(string) (uint64, error) { t.Fatal("kernel sampling called the daemon"); return 0, nil })
	if err != nil || source != "cgroup-v2" || got != 746414883524000 {
		t.Fatalf("usage=%d source=%s err=%v", got, source, err)
	}
}

func TestOtherCgroupLayoutsUseFallback(t *testing.T) {
	for _, unavailable := range []error{os.ErrNotExist, os.ErrPermission} {
		got, source, err := containerUsage("game-box", func(string) ([]byte, error) { return nil, unavailable },
			func(name string) (uint64, error) {
				if name != "game-box" {
					t.Fatal(name)
				}
				return 123, nil
			})
		if err != nil || source != "incus" || got != 123 {
			t.Fatalf("%d %s %v", got, source, err)
		}
	}
	want := errors.New("query deadline")
	_, _, err := containerUsage("game-box", func(string) ([]byte, error) { return nil, os.ErrNotExist },
		func(string) (uint64, error) { return 0, want })
	if !errors.Is(err, want) {
		t.Fatalf("lost fallback error: %v", err)
	}
}

func TestCgroupCounterRejectsMalformedOverflowAndAmbiguousValues(t *testing.T) {
	for _, text := range []string{"", "user_usec 12", "usage_usec", "usage_usec 1 2", "usage_usec -1", "usage_usec nope",
		"usage_usec 18446744073709552", "usage_usec 1\nusage_usec 2", strings.Repeat("x", 65537)} {
		if _, err := usageNanoseconds([]byte(text)); err == nil {
			t.Fatalf("accepted %q", text[:min(80, len(text))])
		}
	}
	if got, err := usageNanoseconds([]byte("usage_usec 0\nnr_periods 0\n")); err != nil || got != 0 {
		t.Fatalf("valid zero: %d %v", got, err)
	}
	_, _, err := containerUsage("ffxiv", func(string) ([]byte, error) { return []byte("usage_usec broken"), nil },
		func(string) (uint64, error) { t.Fatal("malformed accounting silently fell back"); return 0, nil })
	if err == nil {
		t.Fatal("accepted malformed accounting")
	}
}

func TestContainerNameCannotSelectAnotherCgroupOrAPIPath(t *testing.T) {
	for _, name := range []string{"", "../host", "game/other", "game?project=other", ".", strings.Repeat("a", 64)} {
		_, _, err := containerUsage(name, func(string) ([]byte, error) { t.Fatal("unsafe name read a path"); return nil, nil },
			func(string) (uint64, error) { t.Fatal("unsafe name reached API"); return 0, nil })
		if err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

func policyFixture() config.CPUPolicy {
	return config.CPUPolicy{BusyCPUs: "4-7", IdleCPUs: "0-7", BusyThresholdPercent: 40, IdleThresholdPercent: 15, BusyAfterSeconds: 10, IdleAfterSeconds: 20}
}

func idleSample(at int) Sample {
	return Sample{Total: uint64(1000 + at*100), Idle: uint64(500 + at*100), ContainerNS: 100, At: time.Unix(int64(at+1000), 0), Source: "cgroup-v2"}
}

func TestFailureClearsIdleWindowAndDemandsBusyUntilFreshMeasurements(t *testing.T) {
	var w loadWindow
	p := policyFixture()
	for _, at := range []int{0, 5, 10, 15, 20} {
		w.observe(idleSample(at), p, 8)
	}
	_, known, wanted := w.observe(idleSample(25), p, 8)
	if !known || wanted != "idle" {
		t.Fatalf("idle was not earned: %v %s", known, wanted)
	}
	_, known, wanted = w.observe(Sample{}, p, 8)
	if known || wanted != "busy" {
		t.Fatalf("failure did not restrict: %v %s", known, wanted)
	}
	_, known, wanted = w.observe(idleSample(40), p, 8)
	if known || wanted != "busy" {
		t.Fatal("first recovered sample expanded")
	}
	for _, at := range []int{45, 50, 55} {
		_, _, wanted = w.observe(idleSample(at), p, 8)
		if wanted == "idle" {
			t.Fatal("retained pre-failure idle time")
		}
	}
	_, _, wanted = w.observe(idleSample(60), p, 8)
	if wanted != "idle" {
		t.Fatal("fresh idle window never expanded")
	}
}

func TestCounterResetClockResetBackendChangeAndStaleSamplesAreConservative(t *testing.T) {
	for _, kind := range []string{"counter", "clock", "backend", "stale"} {
		var w loadWindow
		p := policyFixture()
		w.observe(idleSample(0), p, 8)
		w.observe(idleSample(5), p, 8)
		next := idleSample(10)
		switch kind {
		case "counter":
			next.ContainerNS = 0
		case "clock":
			next.At = idleSample(0).At
		case "backend":
			next.Source = "incus"
		case "stale":
			next = idleSample(100)
		}
		_, known, wanted := w.observe(next, p, 8)
		if known || wanted != "busy" {
			t.Fatalf("%s did not reset: %v %s", kind, known, wanted)
		}
	}
}

func TestBusyHysteresisAndLongTicksCannotEarnExtraIdleTime(t *testing.T) {
	var w loadWindow
	p := policyFixture()
	start := idleSample(0)
	w.observe(start, p, 8)
	next := idleSample(5)
	next.Idle = start.Idle
	_, _, wanted := w.observe(next, p, 8)
	if wanted != "" {
		t.Fatal("restricted before busy window")
	}
	next = idleSample(10)
	next.Idle = start.Idle
	_, _, wanted = w.observe(next, p, 8)
	if wanted != "busy" {
		t.Fatal("did not restrict after busy window")
	}
	w = loadWindow{}
	w.observe(idleSample(0), p, 8)
	_, _, wanted = w.observe(idleSample(14), p, 8)
	if wanted == "idle" || w.idleFor != sampleInterval {
		t.Fatal("banked missing observation time")
	}
}

func TestStatusHasFreshFailureDiagnosticsWithoutDaemonReads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RUNTIME_DIRECTORY", dir)
	c := config.Default()
	c.Incus.CPUPolicy = policyFixture()
	if err := writeStatus("busy", "unknown", "4-7", "", "query\ndeadline", c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode=busy\n", "limits_cpu=4-7\n", "busy_cpus=4-7\n", "idle_cpus=0-7\n",
		"non_game_busy_percent=unknown\n", "sample_error=query deadline\n", "updated_at=", "limits_source=last_confirmed\n"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q in status", want)
		}
	}
}

func TestFailedRestrictionRemainsPendingWithoutChangingConfiguredCores(t *testing.T) {
	s := allocationState{mode: "idle", cpus: "0-7"}
	p := policyFixture()
	want := errors.New("daemon unavailable")
	calls := 0
	set := func(cpus string) error {
		calls++
		if cpus != "4-7" {
			t.Fatalf("wrong cores %s", cpus)
		}
		if calls == 1 {
			return want
		}
		return nil
	}
	if err := s.apply("busy", p, set); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if s.mode != "idle" || s.cpus != "0-7" {
		t.Fatal("unconfirmed write reported as applied")
	}
	if err := s.apply("", p, set); err != nil {
		t.Fatal(err)
	} // neutral subsequent load still retries restriction
	if calls != 2 || s.mode != "busy" || s.cpus != "4-7" {
		t.Fatalf("restriction was lost: %+v", s)
	}
	if err := s.apply("idle", p, func(cpus string) error {
		if cpus != "0-7" {
			t.Fatal(cpus)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if s.mode != "idle" {
		t.Fatal("fresh idle decision could not expand")
	}
}

func TestFailedExpansionIsNotRetriedOnNeutralOrUnknownInput(t *testing.T) {
	s := allocationState{mode: "busy", cpus: "4-7"}
	p := policyFixture()
	calls := 0
	set := func(string) error { calls++; return errors.New("unavailable") }
	s.apply("idle", p, set)
	s.apply("", p, set)
	s.apply("busy", p, set)
	if calls != 1 || s.mode != "busy" {
		t.Fatal("unsafe expansion was queued")
	}
}
