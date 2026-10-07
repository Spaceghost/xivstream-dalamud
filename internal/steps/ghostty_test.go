package steps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

type ghosttyFakeTarget struct {
	t                *testing.T
	files            map[string][]byte
	commands         [][]string
	writes           []string
	run              func([]string) (string, error)
	input            func([]byte, []string) (string, error)
	rejectOwnerPaths bool
}

type ghosttyIsolatedFake struct{ *ghosttyFakeTarget }

func (ghosttyIsolatedFake) OwnerIsolated() bool { return true }
func ownerHelperStat(args []string) string {
	if len(args) < 4 || args[0] != "/usr/bin/stat" {
		return ""
	}
	return strings.Repeat("41ed:0\n", len(args)-4) + "81ed:0\n"
}

func ghosttyFake(t *testing.T) *ghosttyFakeTarget {
	return &ghosttyFakeTarget{t: t, files: map[string][]byte{}}
}
func (f *ghosttyFakeTarget) Name() string { return "isolated fake" }
func (f *ghosttyFakeTarget) Run(argv ...string) (string, error) {
	f.commands = append(f.commands, append([]string(nil), argv...))
	if f.run != nil {
		return f.run(argv)
	}
	f.t.Fatalf("unexpected command, never run on host: %v", argv)
	return "", errors.New("unexpected command")
}
func (f *ghosttyFakeTarget) RunInput(data []byte, argv ...string) (string, error) {
	f.commands = append(f.commands, append([]string(nil), argv...))
	if f.input != nil {
		return f.input(data, argv)
	}
	f.t.Fatal("unexpected stdin command")
	return "", errors.New("unexpected stdin command")
}
func (f *ghosttyFakeTarget) ReadFile(p string) ([]byte, error) {
	if f.rejectOwnerPaths && strings.HasPrefix(p, "/home/") {
		f.t.Fatal("privileged user-path read")
	}
	if data, ok := f.files[p]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, os.ErrNotExist
}
func (f *ghosttyFakeTarget) WriteFile(p string, data []byte, mode os.FileMode, owner string) error {
	if f.rejectOwnerPaths && strings.HasPrefix(p, "/home/") {
		f.t.Fatal("privileged user-path write")
	}
	f.files[p] = append([]byte(nil), data...)
	f.writes = append(f.writes, p)
	return nil
}
func (f *ghosttyFakeTarget) Exists(p string) bool {
	if f.rejectOwnerPaths && strings.HasPrefix(p, "/home/") {
		f.t.Fatal("privileged user-path existence check")
	}
	_, ok := f.files[p]
	return ok
}

func TestGhosttyAccountUsesOnlyVerifiedNativeIdentity(t *testing.T) {
	u := &user.User{Username: "native-user", Uid: "1000", Gid: "1000", HomeDir: t.TempDir()}
	for _, tc := range []struct {
		name, explicit string
		uid            int
		env            map[string]string
		want           bool
	}{
		{"explicit", "native-user", 0, nil, true},
		{"ordinary UID ignores ambient names", "", 1000, map[string]string{"USER": "root", "LOGNAME": "container-user"}, true},
		{"verified sudo", "", 0, map[string]string{"SUDO_USER": "native-user", "SUDO_UID": "1000"}, true},
		{"sudo mismatch", "", 0, map[string]string{"SUDO_USER": "native-user", "SUDO_UID": "2000"}, false},
		{"root without verified caller", "", 0, map[string]string{"USER": "native-user"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ghosttyAccount(tc.explicit, tc.uid, func(k string) string { return tc.env[k] },
				func(name string) (*user.User, error) {
					if name != u.Username {
						t.Fatal(name)
					}
					return u, nil
				},
				func(id string) (*user.User, error) {
					if id != u.Uid {
						t.Fatal(id)
					}
					return u, nil
				})
			if (err == nil) != tc.want || tc.want && got != u {
				t.Fatalf("account=%v error=%v", got, err)
			}
		})
	}
}

func TestGhosttyAccountRejectsUnsafeNSSValues(t *testing.T) {
	for _, change := range []func(*user.User){
		func(u *user.User) { u.Uid = "0" }, func(u *user.User) { u.Username = "root" },
		func(u *user.User) { u.HomeDir = "relative" }, func(u *user.User) { u.HomeDir = "/" },
		func(u *user.User) { u.HomeDir = "/home/user\nother" }, func(u *user.User) { u.Username = "user;bad" },
		func(u *user.User) { u.Uid = "001000" }, func(u *user.User) { u.Gid = "-1" },
	} {
		u := &user.User{Username: "native-user", Uid: "1000", Gid: "1000", HomeDir: "/home/native-user"}
		change(u)
		if _, err := ghosttyAccount("native-user", 0, func(string) string { return "" }, func(string) (*user.User, error) { return u, nil }, nil); err == nil {
			t.Fatalf("accepted unsafe account %+v", u)
		}
	}
}

func TestGhosttyDefaultsKeepPhysicalHostPrimaryAndContainerOptional(t *testing.T) {
	g := config.Default().Mods.Ghostty
	for _, include := range []bool{false, true} {
		g.IncludeContainer = include
		text := string(ghosttyDefaults("Actual host \\\" ☃", "game-fixture", g))
		if !strings.HasPrefix(text, ghosttyMarker) || !strings.Contains(text, "primary = { label = \"Actual host \\\\\\\" ☃\"") ||
			!strings.Contains(text, "port = 7780, token_file = 'host-agent.token'") {
			t.Fatalf("invalid primary contract: %s", text)
		}
		if strings.Contains(text, "container = ") != include {
			t.Fatal("optional container is not optional")
		}
		if include && (!strings.Contains(text, "container = { label = \"game-fixture\"") || !strings.Contains(text, "port = 7777, token_file = 'container-agent.token'")) {
			t.Fatal("container contract missing")
		}
		for _, personal := range []string{"fedora", "alienware", "bak", "jack"} {
			if strings.Contains(text, personal) {
				t.Fatalf("personal default %s", personal)
			}
		}
	}
}

func TestGhosttyLuaStringEscapesBytesWithoutExecutableSyntax(t *testing.T) {
	if got := luaString("a\x01\n\"\\b"); got != `"a\001\010\"\\b"` {
		t.Fatal(got)
	}
}

func TestGhosttyExistingUnitMismatchRefusesWithoutWriting(t *testing.T) {
	f := ghosttyFake(t)
	p := agentUnitPath("/home/native-user", "host")
	f.files[p] = []byte("unmanaged user service")
	if err := ghosttyExistingUnit(f, "/home/native-user", "host", 7777); err == nil {
		t.Fatal("overwrote user unit")
	}
	if len(f.commands)+len(f.writes) != 0 {
		t.Fatal("read-only preflight mutated")
	}
	f.files[p] = ghosttyUnit(7777, "host")
	if err := ghosttyExistingUnit(f, "/home/native-user", "host", 7777); err != nil {
		t.Fatal(err)
	}
}

func TestGhosttyActiveAgentIsNeverRestartedOrReplaced(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native systemd agent lifecycle runs on Linux")
	}
	f := ghosttyFake(t)
	home := "/home/native-user"
	f.files[filepath.Join(home, ".local/lib/xivstream/ghostty-agent")] = []byte("existing native binary")
	f.files[agentUnitPath(home, "host")] = ghosttyUnit(7777, "host")
	f.files["/var/lib/systemd/linger/native-user"] = nil
	f.run = func(a []string) (string, error) {
		if len(a) == 7 && a[0] == "systemctl" && (a[4] == "is-enabled" || a[4] == "is-active") {
			return "", nil
		}
		t.Fatalf("active agent must not mutate: %v", a)
		return "", nil
	}
	b := builder{c: config.Default()}
	if err := plan.Apply(io.Discard, b.nativeGhosttySteps(f, f, "native-user", home, "1000:1000", "host", 7777)); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 2 || len(f.writes) != 0 {
		t.Fatalf("unexpected mutation: %v %v", f.commands, f.writes)
	}
}

func TestGhosttyPortConflictCannotStopForeignAgent(t *testing.T) {
	f := ghosttyFake(t)
	f.run = func(a []string) (string, error) {
		if reflect.DeepEqual(a, []string{"ss", "-H", "-ltn", "sport = :7777"}) {
			return "LISTEN foreign", nil
		}
		t.Fatalf("unexpected %v", a)
		return "", nil
	}
	if err := ghosttyPortConflict(f, f, "/home/native-user", "native-user", "host", 7777); err == nil {
		t.Fatal("accepted foreign listener")
	}
	if len(f.writes) != 0 || len(f.commands) != 1 {
		t.Fatal("conflict handling mutated")
	}
}

func TestGhosttyProxyConflictsFailWithoutChangingDevices(t *testing.T) {
	g := config.Default().Mods.Ghostty
	good := ghosttyProxyOptions(g)
	if err := ghosttyProxyConflicts(map[string]map[string]string{ghosttyProxy: good}, g); err != nil {
		t.Fatal(err)
	}
	for _, devices := range []map[string]map[string]string{
		{"foreign": good},
		{ghosttyProxy: {"type": "proxy", "bind": "instance", "listen": "tcp:127.0.0.1:7780", "connect": "tcp:127.0.0.1:9999"}},
		{ghosttyProxy: {"type": "proxy", "bind": "instance", "listen": "tcp:127.0.0.1:7780", "connect": "tcp:127.0.0.1:7777", "security.uid": "0"}},
	} {
		if err := ghosttyProxyConflicts(devices, g); err == nil {
			t.Fatalf("accepted conflict %v", devices)
		}
	}
}

func TestGhosttyTokenUsesOnlyStdinAndRedactedErrors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("owner helper intentionally unsupported on this platform")
	}
	const secret = "fixture-credential-only-not-real-1234"
	src, dest := ghosttyFake(t), ghosttyFake(t)
	src.files["/synthetic/token"] = []byte(secret + "\n")
	dest.run = func(args []string) (string, error) { return ownerHelperStat(args), nil }
	dest.input = func(data []byte, args []string) (string, error) {
		if string(data) != secret+"\n" {
			t.Fatal("wrong stdin")
		}
		if strings.Contains(strings.Join(args, " "), secret) {
			t.Fatal("credential in argv")
		}
		return "", errors.New(secret)
	}
	s := ghosttyTokenStep(ghosttyIsolatedFake{src}, "/synthetic/token", ghosttyOwnerFiles(dest, "player", "/home/player", "/usr/local/bin/xivstream"), "/home/player/.xlcore/pluginConfigs/GhosttyDalamud/host-agent.token", "player:player")
	if strings.Contains(strings.Join(s.Shows, " "), secret) {
		t.Fatal("credential in plan")
	}
	if err := s.Apply(); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("write error leaked credential")
	}
	if len(dest.writes) != 0 {
		t.Fatal("ordinary Target.WriteFile used for credential")
	}
}

func TestGhosttyTokenValidationBeforeWrite(t *testing.T) {
	for _, value := range []string{"", "short", strings.Repeat("x", 1025), "long enough but whitespace", "unicode-fake-secret-☃"} {
		src, dest := ghosttyFake(t), ghosttyFake(t)
		src.files["/source"] = []byte(value)
		if err := ghosttyTokenStep(ghosttyIsolatedFake{src}, "/source", ghosttyIsolatedFake{dest}, "/dest", "player:player").Apply(); err == nil {
			t.Fatalf("accepted invalid token length%d", len(value))
		}
		if len(dest.commands)+len(dest.writes) != 0 {
			t.Fatal("invalid credential reached destination")
		}
	}
}

type ghosttyTarEntry struct {
	name string
	kind byte
	body []byte
}

func ghosttyArchive(t *testing.T, entries ...ghosttyTarEntry) []byte {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		h := &tar.Header{Name: entry.name, Typeflag: entry.kind, Mode: 0755, Size: int64(len(entry.body))}
		if entry.kind == tar.TypeSymlink {
			h.Linkname = "/tmp/never-accessed"
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestGhosttyPortableReadsOnlyOneRegularELF(t *testing.T) {
	entry := ghosttyTarEntry{"ghostty-agent-0.3.3-linux-x86_64/ghostty-agent", tar.TypeReg, []byte("\x7fELFfixture-not-executable")}
	got, err := ghosttyPortableBinary(bytes.NewReader(ghosttyArchive(t, entry)))
	if err != nil || !bytes.Equal(got, entry.body) {
		t.Fatalf("%v", err)
	}
	for _, bad := range [][]ghosttyTarEntry{
		{entry, entry}, {{entry.name, tar.TypeSymlink, nil}},
		{{"../outside", tar.TypeReg, nil}, entry}, {{entry.name, tar.TypeReg, []byte("not ELF")}},
	} {
		if _, err := ghosttyPortableBinary(bytes.NewReader(ghosttyArchive(t, bad...))); err == nil {
			t.Fatal("accepted unsafe archive")
		}
	}
}

func TestGhosttyUnitCreatesCustomTokenDirectoryBeforeAgent(t *testing.T) {
	for _, kind := range []string{"host", "container"} {
		unit := string(ghosttyUnit(7777, kind))
		if !strings.Contains(unit, "UMask=0077\n") || !strings.Contains(unit, "ExecStartPre=/usr/bin/install -d -m0700 %h/.config/xivstream\n") {
			t.Fatal("fresh native account lacks private custom-token directory before agent start")
		}
		if strings.Contains(unit, "podman") || strings.Contains(unit, "toolbox") || !strings.Contains(unit, "--windows off") {
			t.Fatal("native shell contract changed")
		}
	}
}
