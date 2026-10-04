package wolf

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/Spaceghost/xivstream-dalamud/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/name (go test ./internal/wolf -update
// rewrites them; review the diff).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs from the golden file (run with -update and review the diff):\n%s", name, got)
	}
}

// fixture is this machine's setup (fedora: P4000 on renderD129, the ffxiv
// Incus container at 10.61.200.110, the pool on 1c289f95-...).
func fixture() Setup {
	c := config.Default()
	c.Topology, c.Backend = config.TopologyHost, config.BackendWolf
	c.Stream.Name, c.Stream.Gamepad = "ffxiv", "ds5"
	c.Incus.CPUPolicy.BusyCPUs, c.Incus.CPUPolicy.IdleCPUs = "4-7", "0-7"
	c.Wolf.AllowedClients = []string{"100.83.231.2", "100.120.57.57", "100.64.131.52"}
	return Setup{
		Config: c, Bin: DefaultBin, RenderNode: "/dev/dri/renderD129", NVIDIA: true, Modprobe: true,
		SessionImage:  "localhost/xivstream-session:0123456789abcdef",
		HomeDevice:    "/dev/disk/by-uuid/1c289f95-5b61-4816-9b55-e161e424f3ff",
		LegacyAddress: "10.61.200.110", GameContainer: "ffxiv", Fallback: true,
	}
}

func allExist(string) bool { return true }

func TestFixtureValidates(t *testing.T) {
	if err := fixture().Config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEnvFileContent(t *testing.T) {
	if got := string(EnvFileContent("/dev/dri/renderD128")); got != "WOLF_RENDER_NODE=/dev/dri/renderD128\n" {
		t.Errorf("got %q", got)
	}
	if got := string(EnvFileContent("")); strings.Contains(got, "WOLF_RENDER_NODE") {
		t.Errorf("no render node still sets WOLF_RENDER_NODE: %q", got)
	}
}

func TestQuadlet(t *testing.T) {
	s := fixture()
	q := Quadlet(s)
	golden(t, "wolf.container", q)
	for _, want := range []string{"Image=" + ServerImage, "AddDevice=/dev/nvidia-uvm", "AddDevice=-/dev/nvidia-uvm-tools", "ExecStartPre=-/usr/bin/nvidia-modprobe -u -c0", "Volume=nvidia-driver-vol:/usr/nvidia:rw",
		"Environment=NVIDIA_DRIVER_VOLUME_NAME=nvidia-driver-vol", "EnvironmentFile=/run/xivstream/wolf.env",
		"Environment=WOLF_USE_ZERO_COPY=FALSE", "Environment=WOLF_STOP_CONTAINER_ON_EXIT=TRUE", "Conflicts=xivstream-sunshine.service",
		"RequiresMountsFor=/var/lib/xivstream/home", "ExecStartPre=/usr/local/bin/xivstream wolf-preflight",
		"ExecStopPost=-/usr/local/bin/xivstream wolf-cleanup", "Requires=podman.socket xivstream-wolf-firewall.service xivstream-nvidia-driver-vol.service",
		"WantedBy=multi-user.target", "Restart=on-failure", "StartLimitBurst=5"} {
		if !bytes.Contains(q, []byte(want+"\n")) {
			t.Errorf("the Quadlet lacks %q", want)
		}
	}
	if bytes.Contains(q, []byte("nvidia.com/gpu")) {
		t.Error("no CDI: the host has no nvidia-container-toolkit")
	}
	s.Wolf.Autostart = false
	if bytes.Contains(Quadlet(s), []byte("[Install]")) {
		t.Error("autostart = false: no [Install]")
	}
	s.NVIDIA, s.Fallback = false, false
	plain := Quadlet(s)
	if bytes.Contains(plain, []byte("nvidia")) || bytes.Contains(plain, []byte("Conflicts=")) {
		t.Errorf("no NVIDIA, no fallback:\n%s", plain)
	}
}

// TestQuadletGenerates runs Podman's Quadlet generator on the unit, in a
// temporary directory (the documented check; skipped without Podman).
func TestQuadletGenerates(t *testing.T) {
	gen := "/usr/lib/systemd/system-generators/podman-system-generator"
	if _, err := os.Stat(gen); err != nil {
		t.Skip("no Podman Quadlet generator here")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wolf.container"), Quadlet(fixture()), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gen, "--dryrun")
	cmd.Env = append(os.Environ(), "QUADLET_UNIT_DIRS="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil || bytes.Contains(out, []byte("converting")) || bytes.Contains(out, []byte("unsupported key")) {
		t.Fatalf("quadlet: %v\n%s", err, out)
	}
	for _, want := range []string{"---wolf.service---", "--device /dev/nvidia0", "--device /dev/nvidia-uvm ", "-v nvidia-driver-vol:/usr/nvidia:rw",
		"ExecStartPre=/usr/local/bin/xivstream wolf-preflight", "RequiresMountsFor=/var/lib/xivstream/home", "--network host"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("generated wolf.service lacks %q:\n%s", want, out)
		}
	}
}

func TestFirewall(t *testing.T) {
	s := fixture()
	fw := Firewall(s)
	golden(t, "wolf.nft", fw)
	for _, want := range []string{
		`tcp dport { 47984, 47989, 48010 } iifname "tailscale0" ip saddr @wolf_clients accept`,
		`udp dport { 47999, 48100, 48200 } iifname "tailscale0" ip saddr @wolf_clients accept`,
		`iifname "xivstream0" ip daddr 10.89.10.1 tcp dport { 41881, 54713, 57787, 57788, 57789, 58178 } accept`,
		`iifname "xivstream0" oifname "tailscale0" counter drop`,
		`ip daddr 10.61.200.110 tcp dport { 41800, 41881, 7777 } ip saddr @inbound_peers dnat ip to 10.89.10.10`,
	} {
		if !bytes.Contains(fw, []byte(want)) {
			t.Errorf("the table lacks %q", want)
		}
	}
	s.Wolf.AllowedClients = nil
	s.LegacyAddress = ""
	open := Firewall(s)
	if bytes.Contains(open, []byte("wolf_clients")) || bytes.Contains(open, []byte("dnat")) {
		t.Errorf("no clients listed means tailscale0 only; no legacy address means no DNAT:\n%s", open)
	}
	if !bytes.Contains(open, []byte(`tcp dport { 47984, 47989, 48010 } iifname "tailscale0" accept`)) {
		t.Error("tailscale0 without a client list")
	}
	nftCheck(t, fw)
	nftCheck(t, open)
}

// nftCheck parses a table with nft in a throwaway user and network namespace.
func nftCheck(t *testing.T, table []byte) {
	t.Helper()
	nft, err := exec.LookPath("nft")
	if err != nil {
		t.Log("no nft: syntax not checked")
		return
	}
	path := filepath.Join(t.TempDir(), "t.nft")
	_ = os.WriteFile(path, table, 0o644)
	// The file starts with "delete table", which fails on a fresh namespace's
	// missing table only when applied; -c checks without applying.
	out, err := exec.Command("unshare", "-rn", nft, "-c", "-f", path).CombinedOutput()
	if err != nil {
		if bytes.Contains(out, []byte("Operation not permitted")) || bytes.Contains(out, []byte("unshare")) {
			t.Logf("cannot make a namespace to check nft syntax: %s", out)
			return
		}
		t.Fatalf("nft -c: %v\n%s", err, out)
	}
}

func TestUnits(t *testing.T) {
	s := fixture()
	golden(t, "xivstream-nvidia-driver-vol.service", DriverVolUnitFile(s))
	golden(t, "xivstream-wolf-firewall.service", FirewallUnitFile(s))
	golden(t, "var-lib-xivstream-home.mount", MountUnit(s))
	golden(t, "xivstream-sunshine.service", FallbackUnitFile(s))
	f := s.Wolf.Forwards[0]
	golden(t, "xivstream-fwd-almanac-gateway.socket", ForwardSocket(s, f))
	golden(t, "xivstream-fwd-almanac-gateway.service", ForwardService(s, f))
	if bytes.Contains(FallbackUnitFile(s), []byte("[Install]")) {
		t.Error("with Wolf the default, the fallback is not started at boot")
	}
	s.Wolf.Autostart = false
	if !bytes.Contains(FallbackUnitFile(s), []byte("[Install]\nWantedBy=multi-user.target\n")) || bytes.Contains(Quadlet(s), []byte("[Install]")) {
		t.Error("Wolf staged (autostart = false): the Sunshine fallback starts at boot, Wolf does not")
	}
	if s.HomeUnit() != "var-lib-xivstream-home.mount" {
		t.Errorf("home unit %s", s.HomeUnit())
	}
}

func TestMountUnitName(t *testing.T) {
	for path, want := range map[string]string{
		"/var/lib/xivstream/home": "var-lib-xivstream-home.mount",
		"/srv/my-games/ffxiv":     `srv-my\x2dgames-ffxiv.mount`,
		"/a/.hidden":              "a-.hidden.mount",
		"/.x":                     `\x2ex.mount`,
		"/":                       "-.mount",
	} {
		if got := MountUnitName(path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
	if BridgeName("xivstream") != "xivstream0" || len(BridgeName("a_very_long_network_name")) > 15 {
		t.Error("bridge names")
	}
}

func TestApp(t *testing.T) {
	s := fixture()
	var opts map[string]any
	if err := json.Unmarshal([]byte(CreateJSON(s)), &opts); err != nil {
		t.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(opts, "", "  ")
	golden(t, "base_create_json.json", append(pretty, '\n'))
	host := opts["HostConfig"].(map[string]any)
	if host["NetworkMode"] != "xivstream" || host["Memory"].(float64) != 12*1024*1024*1024 || host["Privileged"] != false {
		t.Errorf("HostConfig: %v", host)
	}
	ep := opts["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)["xivstream"].(map[string]any)
	if ep["IPAMConfig"].(map[string]any)["IPv4Address"] != "10.89.10.10" {
		t.Errorf("endpoint: %v", ep)
	}
	env := strings.Join(AppEnv(s), "\n")
	for _, want := range []string{"DXVK_FRAME_RATE=60", "XIVSTREAM_GATEWAY=10.89.10.1", "XIVSTREAM_FORWARDS=41881:41881 7787:57787 7788:57788 7789:57789 8178:58178 4713:54713", "XIVSTREAM_MIC_PORT=54713"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s:\n%s", want, env)
		}
	}
	mounts, skipped := AppMounts(s, func(p string) bool { return !strings.Contains(p, "codex") })
	if mounts[0] != "/var/lib/xivstream/home:/home/player:rw" || len(skipped) != 2 {
		t.Errorf("mounts %v, skipped %v", mounts, skipped)
	}
	for _, m := range mounts {
		if strings.Contains(m, "codex") {
			t.Error("a missing source must be skipped")
		}
	}
}

func fixedUUID() string { return "00000000-1111-4222-8333-444444444444" }

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	m := map[string]any{}
	if _, err := toml.Decode(string(data), &m); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	return m
}

func ourApps(t *testing.T, m map[string]any) (ours, others int) {
	for _, p := range tables(m["profiles"]) {
		if p["id"] != MoonlightProfile {
			continue
		}
		for _, a := range tables(p["apps"]) {
			if isOurs(a) {
				ours++
			} else {
				others++
			}
		}
	}
	return
}

func TestCreateConfig(t *testing.T) {
	s := fixture()
	out, err := MergeConfig(nil, s, MergeOptions{Exists: allExist, NewUUID: fixedUUID})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "config.created.toml", out)
	m := decode(t, out)
	if m["config_version"] != int64(7) || m["uuid"] != fixedUUID() || m["hostname"] != "ffxiv" {
		t.Errorf("identity: %v %v %v", m["config_version"], m["uuid"], m["hostname"])
	}
	if ours, others := ourApps(t, m); ours != 1 || others != 0 {
		t.Errorf("a new config's moonlight profile is xivstream's app alone: %d ours, %d others", ours, others)
	}
	video := m["gstreamer"].(map[string]any)["video"].(map[string]any)
	if len(tables(video["h264_encoders"])) == 0 || len(tables(video["hevc_encoders"])) == 0 {
		t.Error("the encoders come from Wolf's default")
	}
	// Wolf's other profiles (shown by Wolf UI only) stay.
	if len(tables(m["profiles"])) != 2 {
		t.Errorf("profiles: %v", len(tables(m["profiles"])))
	}
	again, _ := MergeConfig(out, s, MergeOptions{Exists: allExist, NewUUID: NewUUID})
	if !SameConfig(out, again) {
		t.Error("merging is idempotent")
	}
}

func TestMergeKeepsWolfsState(t *testing.T) {
	s := fixture()
	existing, err := os.ReadFile("testdata/config.wolf-written.toml")
	if err != nil {
		t.Fatal(err)
	}
	out, err := MergeConfig(existing, s, MergeOptions{Exists: allExist, NewUUID: fixedUUID})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "config.merged.toml", out)
	before, after := decode(t, existing), decode(t, out)
	for _, k := range []string{"hostname", "uuid", "config_version"} {
		if before[k] != after[k] {
			t.Errorf("%s: %v became %v", k, before[k], after[k])
		}
	}
	if got, want := PairedClientCerts(out), PairedClientCerts(existing); strings.Join(got, ",") != strings.Join(want, ",") || len(want) != 2 {
		t.Errorf("pairings: %v, want %v", got, want)
	}
	clients := tables(after["paired_clients"])
	for _, c := range clients {
		st := c["settings"].(map[string]any)
		pads := asList(st["controllers_override"])
		switch c["app_state_folder"] {
		case "1111":
			if len(pads) != 1 || pads[0] != "XBOX" {
				t.Errorf("an explicit override is kept: %v", pads)
			}
			if st["mouse_acceleration"] != 1.5 {
				t.Errorf("other settings are kept: %v", st)
			}
		case "2222":
			if len(pads) != 1 || pads[0] != "PS" {
				t.Errorf("stream.gamepad = ds5 fills in PS: %v", pads)
			}
		}
	}
	if !reflectEqual(before["gstreamer"], after["gstreamer"]) {
		t.Error("gstreamer is Wolf's (codecs = auto)")
	}
	if ours, others := ourApps(t, after); ours != 1 || others != 2 {
		t.Errorf("our app replaced, the user's own apps kept: %d ours, %d others", ours, others)
	}
	if !SameConfig(out, mustMerge(t, out, s)) {
		t.Error("idempotent")
	}
}

func TestMergeMigratesAnOlderXivstreamConfig(t *testing.T) {
	// What xivstream wrote before: profiles only, no version, so Wolf would
	// run its v0 migration and fail on the missing hostname/uuid.
	old := []byte("[[profiles]]\nid = 'moonlight-profile-id'\n  [[profiles.apps]]\n  title = 'Final Fantasy XIV'\n")
	out, err := MergeConfig(old, fixture(), MergeOptions{Exists: allExist, NewUUID: fixedUUID})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out)
	if m["config_version"] != int64(7) || m["uuid"] != fixedUUID() || m["hostname"] != "ffxiv" || m["gstreamer"] == nil {
		t.Errorf("not a v7 config:\n%s", out)
	}
}

func TestCodecs(t *testing.T) {
	s := fixture()
	s.Wolf.Codecs = "h264"
	out := mustMerge(t, nil, s)
	video := decode(t, out)["gstreamer"].(map[string]any)["video"].(map[string]any)
	if len(asList(video["hevc_encoders"]))+len(tables(video["hevc_encoders"])) != 0 || len(tables(video["av1_encoders"])) != 0 {
		t.Error("h264 empties the HEVC and AV1 encoder lists")
	}
	if len(tables(video["h264_encoders"])) == 0 {
		t.Error("H.264 stays")
	}
	s.Wolf.Codecs = "auto"
	back := decode(t, mustMerge(t, out, s))["gstreamer"].(map[string]any)["video"].(map[string]any)
	if len(tables(back["hevc_encoders"])) == 0 || len(tables(back["av1_encoders"])) == 0 {
		t.Error("auto restores them from Wolf's default")
	}
}

func mustMerge(t *testing.T, existing []byte, s Setup) []byte {
	t.Helper()
	out, err := MergeConfig(existing, s, MergeOptions{Exists: allExist, NewUUID: fixedUUID})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func reflectEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func TestDefaultConfigIsWolfsV7(t *testing.T) {
	m := decode(t, DefaultConfig())
	if m["config_version"] != int64(7) {
		t.Fatal("pinned default is not v7")
	}
	if !bytes.Contains(UdevRules(), []byte(`ATTRS{name}=="Wolf *virtual*"`)) {
		t.Fatal("85-wolf.rules")
	}
	if !bytes.Contains(DriverDockerfile(), []byte("FROM scratch")) {
		t.Fatal("nvidia-driver Dockerfile")
	}
}

func TestPorts(t *testing.T) {
	var got []string
	for _, p := range Ports(47989) {
		got = append(got, p.Proto+"/"+itoa(p.Num))
	}
	if strings.Join(got, " ") != "tcp/47984 tcp/47989 tcp/48010 udp/47999 udp/48100 udp/48200" {
		t.Fatalf("Wolf's ports: %v", got)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
