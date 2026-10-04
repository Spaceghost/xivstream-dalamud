//go:build linux

package ownerfile

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Spaceghost/xivstream-dalamud/internal/mods"
)

func bundleFixture() mods.Tree {
	return mods.Tree{"GhosttyDalamud.dll": []byte("MZsynthetic DLL"), "GhosttyDalamud.json": []byte(`{"InternalName":"GhosttyDalamud","AssemblyVersion":"0.3.3.0"}`), "lua/init.lua": []byte("return {}\n")}
}
func TestGhosttyBundlePrivateAtomicPublicationAndExactRetry(t *testing.T) {
	r, home := fixture(t)
	tree := bundleFixture()
	if m, err := r.inspectBundle("0.3.3.0"); err != nil || m.Exists {
		t.Fatalf("fresh %v", err)
	}
	for range 2 {
		if err := r.installBundle("0.3.3.0", bytes.NewReader(tree.Tar())); err != nil {
			t.Fatal(err)
		}
	}
	if m, err := r.inspectBundle("0.3.3.0"); err != nil || !m.Exists {
		t.Fatalf("installed %v", err)
	}
	dir := filepath.Join(home, ".xlcore/installedPlugins/GhosttyDalamud/0.3.3.0")
	for name, data := range tree {
		p := filepath.Join(dir, name)
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("bundle mismatch")
		}
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0600 {
			t.Fatal("not owner private")
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	if len(entries) != 1 {
		t.Fatal("successful stage leaked")
	}
	tree["lua/init.lua"] = []byte("changed payload")
	if err := r.installBundle("0.3.3.0", bytes.NewReader(tree.Tar())); err == nil {
		t.Fatal("same-version differing content overwritten")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "lua/init.lua")); string(got) != "return {}\n" {
		t.Fatal("old bundle changed")
	}
}
func TestGhosttyBundleRejectsUnsafeArchiveBeforeCreatingPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    tar.Header
		body []byte
	}{
		{"traversal", tar.Header{Name: "../victim", Mode: 0644, Typeflag: tar.TypeReg}, nil},
		{"symlink", tar.Header{Name: "alias", Mode: 0644, Typeflag: tar.TypeSymlink, Linkname: "/victim"}, nil},
		{"fifo", tar.Header{Name: "fifo", Mode: 0600, Typeflag: tar.TypeFifo}, nil},
		{"setuid", tar.Header{Name: "GhosttyDalamud.dll", Mode: 04755, Typeflag: tar.TypeReg}, nil},
		{"pax", tar.Header{Name: "GhosttyDalamud.dll", Mode: 0644, Typeflag: tar.TypeReg, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "forbidden metadata"}}, nil},
		{"oversize", tar.Header{Name: "large", Mode: 0644, Typeflag: tar.TypeReg, Size: mods.GhosttyFileLimit + 1}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			w := tar.NewWriter(&b)
			if err := w.WriteHeader(&tc.h); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(tc.body)
			_ = w.Close()
			r, home := fixture(t)
			if err := r.installBundle("0.3.3.0", bytes.NewReader(b.Bytes())); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(filepath.Join(home, ".xlcore")); !os.IsNotExist(err) {
				t.Fatal("unsafe archive created paths")
			}
		})
	}
}
func TestGhosttyBundleRejectsDuplicatesFileParentsAndIdentity(t *testing.T) {
	for _, tree := range []mods.Tree{
		{"GhosttyDalamud.dll": []byte("MZx"), "ghosttydalamud.dll": []byte("MZx")},
		{"a": nil, "a/b": nil},
		{"GhosttyDalamud.dll": []byte("MZx"), "GhosttyDalamud.json": []byte(`{"InternalName":"Another","AssemblyVersion":"0.3.3.0"}`)},
	} {
		r, _ := fixture(t)
		if err := r.installBundle("0.3.3.0", bytes.NewReader(tree.Tar())); err == nil {
			t.Fatal("invalid bundle accepted")
		}
	}
	tree := bundleFixture()
	for i := 0; i < mods.GhosttyEntryLimit; i++ {
		tree[fmt.Sprintf("member-%04d", i)] = nil
	}
	r, _ := fixture(t)
	if err := r.installBundle("0.3.3.0", bytes.NewReader(tree.Tar())); err == nil {
		t.Fatal("entry bound ignored")
	}
}
func TestGhosttyBundleRejectsWineAmbiguityWithValidIdentityBeforeStaging(t *testing.T) {
	for _, extra := range []mods.Tree{{"A/x.lua": nil, "a/y.lua": nil}, {"CON.txt": nil}, {"nested/lpt9.dll": nil}, {"A": nil, "a/y.lua": nil}} {
		tree := bundleFixture()
		for name, data := range extra {
			tree[name] = data
		}
		r, home := fixture(t)
		if err := r.installBundle("0.3.3.0", bytes.NewReader(tree.Tar())); err == nil {
			t.Fatal("ambiguous Wine bundle published")
		}
		if _, err := os.Stat(filepath.Join(home, ".xlcore")); !os.IsNotExist(err) {
			t.Fatal("invalid archive created stage")
		}
	}
}
func TestGhosttyBundleRefusesExistingPartialAndSymlinkedVersions(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		r, home := fixture(t)
		dir := filepath.Join(home, ".xlcore/installedPlugins/GhosttyDalamud/0.3.3.0")
		if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
			t.Fatal(err)
		}
		if symlink {
			if err := os.Symlink(t.TempDir(), dir); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.inspectBundle("0.3.3.0"); err == nil {
			t.Fatal("unsafe existing version accepted")
		}
		if err := r.installBundle("0.3.3.0", bytes.NewReader(bundleFixture().Tar())); err == nil {
			t.Fatal("existing version overwritten")
		}
	}
}
func TestDalamudConfigOwnedLegacyModeReadNewPrivateWrite(t *testing.T) {
	r, home := fixture(t)
	p := rules["dalamud-config"]
	target := filepath.Join(home, p.path)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"preserved":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.read(p); err != nil {
		t.Fatal(err)
	}
	if err := r.write(p, []byte(`{"preserved":true,"new":1}`)); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(target)
	if st.Mode().Perm() != 0600 {
		t.Fatal("new config not private")
	}
	old, _ := os.ReadFile(target)
	for _, data := range [][]byte{[]byte("null"), []byte("[]"), []byte("not JSON")} {
		if err := r.write(p, data); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, old) {
		t.Fatal("old config changed")
	}
}
