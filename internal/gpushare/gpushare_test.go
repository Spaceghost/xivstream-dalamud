package gpushare

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGameMB(t *testing.T) {
	var r gpuReport
	_ = json.Unmarshal([]byte(`{"inference":{"used_mb":6691},"loaded":[{"model":"qwen3.5:4b","vram_mb":3541}]}`), &r)
	if mb, ok := GameMB(r); !ok || mb != 3150 {
		t.Fatalf("GameMB = %d, %v", mb, ok)
	}
	if _, ok := GameMB(gpuReport{}); ok {
		t.Fatal("no inference GPU must not measure")
	}
}

func TestProcRunning(t *testing.T) {
	proc := t.TempDir()
	add := func(pid, cmdline, cgroup string) {
		dir := filepath.Join(proc, pid)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "cgroup"), []byte(cgroup), 0o644)
	}
	add("10", "Z:\\home\\player\\.xlcore\\ffxiv\\game\\ffxiv_dx11.exe\x00//**sqex**\x00", "0::/lxc.payload.ffxiv/user.slice\n")
	add("11", "/usr/bin/bash\x00", "0::/user.slice\n")
	if !procRunning(proc, "ffxiv_dx11.exe", "ffxiv") {
		t.Error("the game in the ffxiv container was not found")
	}
	if procRunning(proc, "ffxiv_dx11.exe", "other") {
		t.Error("matched a game in a different container")
	}
	if !procRunning(proc, "FFXIV_DX11.EXE", "") {
		t.Error("names match case-insensitively")
	}
	add("12", "wine64-preloader\x00C:\\game\\notepad.exe\x00", "0::/\n")
	if !procRunning(proc, "notepad.exe", "") {
		t.Error("argv[1] under a Wine loader counts")
	}
	// Wolf: the game in a Podman session container (topology host: no
	// container filter, so a game in any cgroup counts).
	add("13", "Z:\\home\\player\\.xlcore\\ffxiv\\game\\ffxiv_dx11.exe\x00", "0::/machine.slice/libpod-0123abcd.scope/container\n")
	_ = os.RemoveAll(filepath.Join(proc, "10"))
	if !procRunning(proc, "ffxiv_dx11.exe", "") {
		t.Error("the game in a Podman session container was not found")
	}
	if procRunning(proc, "ffxiv_dx11.exe", "ffxiv") {
		t.Error("an Incus filter must not match a Podman game")
	}
}
