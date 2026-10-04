package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGameCPUFence runs the fence's Python tests (fake cgroup and /proc trees
// for the Incus and the Podman layouts; nothing on the host is touched).
func TestGameCPUFence(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	if out, err := exec.Command(python, "-c", "import tomllib").CombinedOutput(); err != nil {
		t.Skipf("python3 has no tomllib (3.11+): %s", out)
	}
	script, _ := filepath.Abs("files/game-cpu-fence")
	cmd := exec.Command(python, "testdata/test_game_cpu_fence.py")
	cmd.Env = append(os.Environ(), "FENCE="+script, "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestFenceIsShippedExecutableText(t *testing.T) {
	data := Raw("game-cpu-fence")
	if len(data) < 100 || string(data[:2]) != "#!" {
		t.Fatal("game-cpu-fence must be a script with a shebang")
	}
	if len(Raw("game-cpu-fence.service")) == 0 {
		t.Fatal("no unit")
	}
}
