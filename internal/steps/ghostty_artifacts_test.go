//go:build linux

package steps

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Spaceghost/xivstream-dalamud/internal/mods"
)

// Optional real-artifact compatibility bridge. Outputs are exclusively created
// in an explicit private fixture directory; no downloads or live installation.
func TestGhosttyRealArtifactFixture(t *testing.T) {
	zipPath, portable, out := os.Getenv("XIVSTREAM_FIXTURE_GHOSTTY_ZIP"), os.Getenv("XIVSTREAM_FIXTURE_GHOSTTY_PORTABLE"), os.Getenv("XIVSTREAM_FIXTURE_GHOSTTY_OUTPUT")
	if zipPath == "" && portable == "" && out == "" {
		t.Skip("optional locally built Ghostty artifact paths not supplied")
	}
	if !filepath.IsAbs(zipPath) || !filepath.IsAbs(portable) || !filepath.IsAbs(out) {
		t.Fatal("all fixture paths must be explicit absolute paths")
	}
	version := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(zipPath), "GhosttyDalamud-"), ".zip")
	if !mods.ValidGhosttyVersion(version) {
		t.Fatal("invalid artifact filename version")
	}
	f, err := os.Open(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(io.LimitReader(f, mods.GhosttyBundleLimit+1))
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := json.Marshal([]mods.Plugin{{InternalName: "GhosttyDalamud", AssemblyVersion: version}})
	plugins, err := mods.Parse(entry, "https://fixture.invalid/local-artifact")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := mods.UnpackGhostty(data, plugins[0], version, false)
	if err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(portable)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := ghosttyPortableBinary(io.LimitReader(f, 64<<20))
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"ghostty-bundle.tar": tree.Tar(), "ghostty-agent": binary} {
		p := filepath.Join(out, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		n, err := f.Write(body)
		if err == nil && n != len(body) {
			err = io.ErrShortWrite
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal("fixture output failed")
		}
	}
	t.Logf("validated real Ghostty %s: %d plugin files, %d native agent bytes; no binaries executed", version, len(tree), len(binary))
}
