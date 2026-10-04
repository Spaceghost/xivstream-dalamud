package mods

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

func TestGhosttyBundleStrictPathsAndVersions(t *testing.T) {
	for _, v := range []string{"", "../x", "0.3.3", "0.3.3.0/other", "0.3.3.0\n"} {
		if ValidGhosttyVersion(v) {
			t.Fatal("invalid version")
		}
	}
	for _, v := range []string{"0.3.3.0", "1.0.0.123"} {
		if !ValidGhosttyVersion(v) {
			t.Fatal("valid version refused")
		}
	}
	for _, p := range []string{"../x", "/x", "x/../../y", "x//y", "x/./y", "C:/file", "x\\y", "x/..", "x.", "x "} {
		if ValidGhosttyPath(p) {
			t.Fatalf("unsafe path %q", p)
		}
	}
	for _, p := range []string{"CON", "con.lua", "nested/NUL.json", "COM1.dll", "lpt9.txt", "AUX"} {
		if ValidGhosttyPath(p) {
			t.Fatal("Win32 reserved basename accepted")
		}
	}
}

func TestGhosttyZipRejectsAmbiguousDirectoryCasingWithValidIdentity(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range map[string]string{"GhosttyDalamud.dll": "MZsynthetic", "GhosttyDalamud.json": `{"InternalName":"GhosttyDalamud","AssemblyVersion":"0.3.3.0"}`, "A/x.lua": "x", "a/y.lua": "y"} {
		h := &zip.FileHeader{Name: name, Method: zip.Store}
		h.SetMode(0644)
		f, _ := w.CreateHeader(h)
		_, _ = f.Write([]byte(data))
	}
	_ = w.Close()
	if _, err := UnpackGhostty(b.Bytes(), Plugin{InternalName: "GhosttyDalamud"}, "0.3.3.0", false); err == nil {
		t.Fatal("ambiguous ZIP directories accepted")
	}
}
func TestGhosttyZipRejectsDuplicateLinksAndTraversal(t *testing.T) {
	for _, extra := range []struct {
		name string
		mode os.FileMode
	}{{"../outside", 0644}, {"GhosttyDalamud.dll", 0644}, {"ghosttydalamud.dll", 0644}, {"link", os.ModeSymlink | 0777}, {"setuid", os.ModeSetuid | 0755}} {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		h := &zip.FileHeader{Name: "GhosttyDalamud.dll", Method: zip.Store}
		h.SetMode(0644)
		f, _ := w.CreateHeader(h)
		_, _ = f.Write([]byte("MZsynthetic"))
		h = &zip.FileHeader{Name: extra.name, Method: zip.Store}
		h.SetMode(extra.mode)
		f, _ = w.CreateHeader(h)
		_, _ = f.Write([]byte("synthetic"))
		_ = w.Close()
		if _, err := UnpackGhostty(b.Bytes(), Plugin{InternalName: "GhosttyDalamud"}, "0.3.3.0", false); err == nil {
			t.Fatal("unsafe ZIP accepted")
		}
	}
}
