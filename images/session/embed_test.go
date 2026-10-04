package session

import (
	"os/exec"
	"strings"
	"testing"
)

func TestContext(t *testing.T) {
	cf, err := Files.ReadFile("Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cf), "ARG XIVLAUNCHER_VERSION="+XIVLauncherVersion+"\n") {
		t.Error("the Containerfile's default launcher version is XIVLauncherVersion")
	}
	for _, name := range []string{"entrypoint.sh", "session.sh", "launcher.sh", "ghostty-agent.sh", "nvidia.conf"} {
		if !strings.Contains(string(cf), name) {
			t.Errorf("the Containerfile does not copy %s", name)
		}
	}
	if !strings.Contains(string(cf), "\nFROM registry.fedoraproject.org/fedora:43\n") || strings.Contains(string(cf), "FROM ghcr.io/games-on-whales") {
		t.Error("Fedora 43, not GoW's base-app (its setup_user runs userdel -r on the shared home)")
	}
	if len(Hash()) != 16 || !strings.HasPrefix(Image(), Repository+":") {
		t.Errorf("image %s", Image())
	}
}

// The entrypoint never manages users or homes: the home is the shared install.
func TestEntrypointNeverTouchesUsers(t *testing.T) {
	for _, name := range Names() {
		if name == "Containerfile" { // bakes the user in, at build time
			continue
		}
		data, _ := Files.ReadFile(name)
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, bad := range []string{"userdel", "useradd", "usermod", "groupdel", "rm -r", "chown -R", "mkhomedir"} {
				if strings.Contains(line, bad) {
					t.Errorf("%s runs %s: %s", name, bad, line)
				}
			}
		}
	}
	ep, _ := Files.ReadFile("entrypoint.sh")
	for _, want := range []string{"launcher.ini", "flock -n 9", ".xivstream-game.lock", "setpriv --reuid=1000", "ldconfig", "/usr/nvidia/share/vulkan/icd.d", "trap on_stop TERM"} {
		if !strings.Contains(string(ep), want) {
			t.Errorf("entrypoint.sh lacks %q", want)
		}
	}
}

func TestScriptsParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for _, name := range Names() {
		if !strings.HasSuffix(name, ".sh") {
			continue
		}
		data, _ := Files.ReadFile(name)
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(string(data))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
}
