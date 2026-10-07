package wolf

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRun records commands and answers from a table keyed by the command's
// leading words.
type fakeRun struct {
	calls   []string
	answers map[string]func() (string, error)
}

func (f *fakeRun) run(argv ...string) (string, error) {
	cmd := strings.Join(argv, " ")
	f.calls = append(f.calls, cmd)
	best := ""
	for prefix := range f.answers {
		if strings.HasPrefix(cmd, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best != "" {
		return f.answers[best]()
	}
	return "", nil
}

func (f *fakeRun) ran(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func ok(s string) func() (string, error) { return func() (string, error) { return s, nil } }
func fail() (string, error)              { return "", errors.New("exit status 1") }

func nolog(string) {}

const ver = "580.178.04"

func volumeFiles(t *testing.T, present bool) (string, func(string) error) {
	mnt := t.TempDir()
	if present {
		for _, f := range driverFiles(ver) {
			_ = os.MkdirAll(filepath.Join(mnt, filepath.Dir(f)), 0o755)
			_ = os.WriteFile(filepath.Join(mnt, f), nil, 0o644)
		}
	}
	return mnt, func(p string) error { _, err := os.Stat(p); return err }
}

func TestDriverVolumeUpToDateDoesNothing(t *testing.T) {
	mnt, stat := volumeFiles(t, true)
	f := &fakeRun{answers: map[string]func() (string, error){"podman volume inspect": ok(mnt)}}
	if err := BuildDriverVolume(f.run, ver, stat, nolog); err != nil {
		t.Fatal(err)
	}
	if f.ran("podman build") || f.ran("podman volume rm") || f.ran("podman create") {
		t.Errorf("a current volume is left alone: %v", f.calls)
	}
}

func TestDriverVolumeRebuild(t *testing.T) {
	mnt, stat := volumeFiles(t, false)
	populated := false
	f := &fakeRun{answers: map[string]func() (string, error){
		"podman volume inspect":                          ok(mnt),
		"podman image exists":                            fail, // not built for this version yet
		"podman volume exists":                           ok(""),
		"podman ps -a --filter volume=nvidia-driver-vol": ok("WolfFFXIV_123 exited\nwolf exited"),
		// The scratch image's /bin/sh cannot run: start fails, after Podman
		// has copied /usr/nvidia into the volume.
		"podman start": func() (string, error) {
			for _, fl := range driverFiles(ver) {
				_ = os.MkdirAll(filepath.Join(mnt, filepath.Dir(fl)), 0o755)
				_ = os.WriteFile(filepath.Join(mnt, fl), nil, 0o644)
			}
			populated = true
			return "", errors.New("crun: executable file `sh` not found: exit status 127")
		},
	}}
	if err := BuildDriverVolume(f.run, ver, stat, nolog); err != nil {
		t.Fatalf("%v\n%v", err, f.calls)
	}
	for _, want := range []string{"podman build --pull=missing -t localhost/gow-nvidia-driver:" + ver + " --build-arg NV_VERSION=" + ver,
		"podman rm --force WolfFFXIV_123", "podman rm --force wolf", "podman volume rm nvidia-driver-vol",
		"podman volume create --label xivstream.nvidia-version=" + ver + " nvidia-driver-vol", "podman create --name xivstream-nvvol-"} {
		if !f.ran(want) {
			t.Errorf("did not run %q: %v", want, f.calls)
		}
	}
	if !populated {
		t.Fatal("never started")
	}
	last := f.calls[len(f.calls)-1]
	if !strings.HasPrefix(last, "podman rm --force xivstream-nvvol-") {
		t.Errorf("the populating container is always removed, last: %s", last)
	}
}

func TestDriverVolumeRefusesARunningUser(t *testing.T) {
	mnt, stat := volumeFiles(t, false)
	f := &fakeRun{answers: map[string]func() (string, error){
		"podman volume inspect": ok(mnt),
		"podman image exists":   ok(""),
		"podman volume exists":  ok(""),
		"podman ps -a --filter": ok("wolf running"),
	}}
	err := BuildDriverVolume(f.run, ver, stat, nolog)
	if err == nil || !strings.Contains(err.Error(), "wolf is running") {
		t.Fatalf("got %v", err)
	}
	if f.ran("podman volume rm") || f.ran("podman rm") {
		t.Error("nothing is removed under a running container")
	}
}

func TestDriverVolumeVerifiesContents(t *testing.T) {
	mnt, stat := volumeFiles(t, false)
	f := &fakeRun{answers: map[string]func() (string, error){
		"podman volume inspect": ok(mnt),
		"podman image exists":   ok(""),
		"podman volume exists":  fail,
		"podman start":          fail, // and nothing copied
	}}
	err := BuildDriverVolume(f.run, ver, stat, nolog)
	if err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Fatalf("got %v", err)
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "podman rm --force xivstream-nvvol-") {
		t.Errorf("removed even on failure; last %s", last)
	}
}

func TestCleanup(t *testing.T) {
	f := &fakeRun{answers: map[string]func() (string, error){
		"podman ps -a": ok("wolf\nWolfFFXIV_8812\nWolfPulseAudio\nalmanac-thing\nWolfFFXIVish"),
	}}
	if err := Cleanup(f.run, nolog); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"podman stop --time 10 WolfFFXIV_8812", "podman rm --force WolfFFXIV_8812", "podman rm --force WolfPulseAudio"} {
		if !f.ran(want) {
			t.Errorf("did not run %q", want)
		}
	}
	for _, never := range []string{"podman rm --force wolf", "podman rm --force almanac-thing", "podman rm --force WolfFFXIVish"} {
		if f.ran(never) {
			t.Errorf("ran %q", never)
		}
	}
}

func TestIncusRunning(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "lxc.payload.ffxiv", "init.scope"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "lxc.payload.ffxiv", "cgroup.procs"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(root, "lxc.payload.ffxiv", "init.scope", "cgroup.procs"), []byte("4242\n"), 0o644)
	if !IncusRunning(root, "ffxiv") {
		t.Error("a process in a child group means running")
	}
	if IncusRunning(root, "almanac") || IncusRunning(root, "") {
		t.Error("no cgroup, not running")
	}
}

func TestHomeHelpers(t *testing.T) {
	f := &fakeRun{answers: map[string]func() (string, error){
		"findmnt -n -o UUID,FSTYPE -T": ok("1c289f95-5b61-4816-9b55-e161e424f3ff btrfs"),
		"findmnt -rn -S UUID=1c289f95": ok("/var/lib/incus/storage-pools/default /\n/var/lib/xivstream/home /xivstream-home"),
		"btrfs subvolume show":         fail,
		"findmnt -n -o FSROOT":         ok("/xivstream-home"),
	}}
	dev, err := DeviceFor(f.run, "")
	if err != nil || dev != "/dev/disk/by-uuid/1c289f95-5b61-4816-9b55-e161e424f3ff" {
		t.Fatalf("%s %v", dev, err)
	}
	if d, _ := DeviceFor(f.run, "UUID=abcd-1234"); d != "/dev/disk/by-uuid/abcd-1234" {
		t.Error(d)
	}
	if exists, err := SubvolumeExists(f.run, dev, "xivstream-home"); err != nil || exists {
		t.Errorf("%v %v", exists, err)
	}
	if !f.ran("btrfs subvolume show /var/lib/incus/storage-pools/default/xivstream-home") {
		t.Errorf("looked in the wrong place: %v", f.calls)
	}
	if !HomeMounted(f.run, "/var/lib/xivstream/home", "xivstream-home") || HomeMounted(f.run, "/x", "other") {
		t.Error("HomeMounted")
	}
	notBtrfs := &fakeRun{answers: map[string]func() (string, error){"findmnt": ok("abcd ext4")}}
	if _, err := DeviceFor(notBtrfs.run, ""); err == nil {
		t.Error("the home needs btrfs")
	}
}

func TestGateSunshineRule(t *testing.T) {
	live, err := os.ReadFile("testdata/70-xivstream-ffxiv.rules.live")
	if err != nil {
		t.Fatal(err)
	}
	gated := GateSunshineRule(live)
	golden(t, "70-xivstream-ffxiv.rules.gated", gated)
	if string(GateSunshineRule(gated)) != string(gated) {
		t.Error("gating twice changes nothing")
	}
	// Sunshine's behaviour is kept: every original line is still there, in order,
	// and the only additions are the gate and its label.
	var added []string
	orig := strings.Split(string(live), "\n")
	i := 0
	for _, line := range strings.Split(string(gated), "\n") {
		if i < len(orig) && line == orig[i] {
			i++
			continue
		}
		added = append(added, line)
	}
	if i != len(orig) {
		t.Fatalf("original lines lost or reordered (matched %d of %d)", i, len(orig))
	}
	joined := strings.Join(added, "\n")
	for _, want := range []string{`SUBSYSTEMS=="input", ATTRS{name}=="Wolf *virtual*", GOTO="xivstream_not_wolf_end"`,
		`KERNEL=="hidraw*", ATTRS{uevent}=="*HID_NAME=Wolf *virtual*", GOTO="xivstream_not_wolf_end"`, `LABEL="xivstream_not_wolf_end"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("gate lacks %s", want)
		}
	}
	for _, line := range added {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") && !strings.Contains(t, "xivstream_not_wolf_end") {
			panic("added a rule that is not the gate: " + line)
		}
	}
	// The gate's matches are exactly 85-wolf.rules' own.
	for _, m := range []string{`ATTRS{name}=="Wolf *virtual*"`, `ATTRS{uevent}=="*HID_NAME=Wolf *virtual*"`} {
		if !strings.Contains(string(UdevRules()), m) {
			t.Errorf("85-wolf.rules does not match on %s", m)
		}
	}
}

// TestPairClient drives the pairing flow against a fake Wolf API socket.
func TestPairClient(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "wolf.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip(err)
	}
	var mu sync.Mutex
	clients := []map[string]any{{"client_id": "1", "settings": map[string]any{"controllers_override": []string{"XBOX"}}}}
	var paired, settings map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pair/pending", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "requests": []map[string]string{{"pair_secret": "s3cret", "client_ip": "100.83.231.2"}}})
	})
	mux.HandleFunc("/api/v1/pair/client", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&paired)
		go func() { // Moonlight finishes the handshake a moment later
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			clients = append(clients, map[string]any{"client_id": "2", "settings": map[string]any{"controllers_override": []string{}}})
			mu.Unlock()
		}()
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	mux.HandleFunc("/api/v1/clients", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "clients": clients})
	})
	mux.HandleFunc("/api/v1/clients/settings", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&settings)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	defer srv.Close()

	err = PairClient(NewAPI(sock), PairOptions{PIN: "1234", Pads: ControllersOverride("ds5"), Wait: 5 * time.Second, Log: nolog})
	if err != nil {
		t.Fatal(err)
	}
	if paired["pair_secret"] != "s3cret" || paired["pin"] != "1234" {
		t.Errorf("pair request: %v", paired)
	}
	if settings["client_id"] != "2" {
		t.Errorf("the new client gets the pads: %v", settings)
	}
	if s, _ := json.Marshal(settings["settings"]); string(s) != `{"controllers_override":["PS"]}` {
		t.Errorf("settings: %s", s)
	}
	if err := PairClient(NewAPI(sock), PairOptions{PIN: "1", ClientIP: "100.1.1.1", Log: nolog}); err == nil {
		t.Error("an unknown client address is an error")
	}
}
