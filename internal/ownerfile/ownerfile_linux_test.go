//go:build linux

package ownerfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) (*root, string) {
	t.Helper()
	home := t.TempDir()
	r, err := openRoot(home, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(r.fd) })
	return r, home
}
func TestOwnerPrivateAtomicRoundTripAndReplacement(t *testing.T) {
	r, home := fixture(t)
	p := rules["host-token-copy"]
	for _, data := range [][]byte{[]byte("synthetic-private-token-1234\n"), []byte("synthetic-replacement-token-5678\n")} {
		if err := r.write(p, data); err != nil {
			t.Fatal(err)
		}
		got, err := r.read(p)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("roundtrip: %v", err)
		}
		st, err := os.Stat(filepath.Join(home, p.path))
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatal("not private")
		}
		entries, err := os.ReadDir(filepath.Dir(filepath.Join(home, p.path)))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatal("temporary leaked")
		}
	}
}

func TestOwnerTemporaryReplacementIsNeitherPublishedNorRemoved(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		r, home := fixture(t)
		p := rules["host-token-copy"]
		old := []byte("synthetic-old-private-token-1234\n")
		if err := r.write(p, old); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(t.TempDir(), "protected")
		before := []byte("synthetic protected preimage")
		if err := os.WriteFile(victim, before, 0600); err != nil {
			t.Fatal(err)
		}
		var replaced string
		err := r.writeWithHook(p, []byte("synthetic-new-private-token-5678\n"), func(dir int, name string) {
			replaced = filepath.Join(home, filepath.Dir(p.path), name)
			if e := unix.Renameat(dir, name, dir, name+".original"); e != nil {
				t.Fatal(e)
			}
			if symlink {
				if e := unix.Symlinkat(victim, dir, name); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := os.WriteFile(replaced, []byte("foreign replacement"), 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
		if err == nil {
			t.Fatal("published replaced temporary")
		}
		if _, e := os.Lstat(replaced); e != nil {
			t.Fatal("removed foreign replacement")
		}
		if got, e := r.read(p); e != nil || !bytes.Equal(got, old) {
			t.Fatal("old target lost")
		}
		if got, e := os.ReadFile(victim); e != nil || !bytes.Equal(got, before) {
			t.Fatal("victim changed")
		}
	}
}
func TestOwnerRefusesHardLinkedCredential(t *testing.T) {
	r, home := fixture(t)
	p := rules["host-token-copy"]
	if err := r.write(p, []byte("synthetic-private-token-1234\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(home, p.path), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.read(p); err == nil {
		t.Fatal("read hard-linked credential")
	}
	if err := r.write(p, []byte("synthetic-new-private-token-5678\n")); err == nil {
		t.Fatal("replaced hard-linked credential")
	}
}
func TestOwnerRegularBinaryAndUnitAreNeverReplaced(t *testing.T) {
	r, _ := fixture(t)
	for name, body := range map[string][]byte{"agent-binary": []byte("\x7fELFsynthetic-not-executable"), "host-unit": []byte(UnitMarker + "fixture")} {
		p := rules[name]
		if err := r.write(p, body); err != nil {
			t.Fatal(err)
		}
		if err := r.write(p, body); err != nil {
			t.Fatal("same file should be idempotent")
		}
		if err := r.write(p, append(append([]byte(nil), body...), 'x')); err == nil {
			t.Fatal("replaced existing managed object")
		}
		got, err := r.read(p)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatal("old object lost")
		}
	}
}
func TestOwnerNeverFollowsDestinationOrParentSymlinks(t *testing.T) {
	for _, parent := range []bool{false, true} {
		r, home := fixture(t)
		p := rules["defaults"]
		outside := t.TempDir()
		victim := filepath.Join(outside, "protected")
		before := []byte("synthetic protected")
		if err := os.WriteFile(victim, before, 0600); err != nil {
			t.Fatal(err)
		}
		if parent {
			if err := os.Symlink(outside, filepath.Join(home, ".xlcore")); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(home, p.path)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, filepath.Join(home, p.path)); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.write(p, []byte(DefaultsMarker+"return {}\n")); err == nil {
			t.Fatal("followed symlink")
		}
		got, err := os.ReadFile(victim)
		if err != nil || !bytes.Equal(got, before) {
			t.Fatal("victim changed")
		}
	}
}
func TestOwnerRejectsSourceSymlinkPublicModeAndWrongOwner(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "wrong-owner", "oversize", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			r, home := fixture(t)
			p := rules["host-token-source"]
			target := filepath.Join(home, p.path)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			data := []byte("synthetic-private-token-1234")
			switch kind {
			case "symlink":
				outside := filepath.Join(t.TempDir(), "token")
				os.WriteFile(outside, data, 0600)
				os.Symlink(outside, target)
			case "fifo":
				if err := unix.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if kind == "oversize" {
					data = bytes.Repeat([]byte{'x'}, 1026)
				}
				if err := os.WriteFile(target, data, 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "public" {
					os.Chmod(target, 0644)
				}
				if kind == "wrong-owner" {
					r.uid++
				}
			}
			start := time.Now()
			if _, err := r.read(p); err == nil {
				t.Fatal("unsafe token read")
			}
			if time.Since(start) > time.Second {
				t.Fatal("special file blocked")
			}
		})
	}
}
func TestOwnerRejectsUnmanagedDefaultsBeforeReplacement(t *testing.T) {
	r, home := fixture(t)
	p := rules["defaults"]
	target := filepath.Join(home, p.path)
	os.MkdirAll(filepath.Dir(target), 0700)
	before := []byte("return { user = true }\n")
	os.WriteFile(target, before, 0600)
	if err := r.write(p, []byte(DefaultsMarker+"return {}\n")); err == nil {
		t.Fatal("unmanaged overwritten")
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, before) {
		t.Fatal("unmanaged file changed")
	}
}
func TestOwnerBoundsInputBeforeAnyFilesystemChanges(t *testing.T) {
	r, home := fixture(t)
	p := rules["host-token-copy"]
	if err := r.write(p, bytes.Repeat([]byte{'x'}, 1026)); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := os.Stat(filepath.Join(home, ".xlcore")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversize input mutated directory")
	}
	if err := r.write(p, []byte("contains synthetic spaces")); err == nil {
		t.Fatal("invalid token accepted")
	}
}
func TestOwnerRejectsWritableDirectories(t *testing.T) {
	r, home := fixture(t)
	os.Mkdir(filepath.Join(home, ".xlcore"), 0700)
	os.Chmod(filepath.Join(home, ".xlcore"), 0777)
	if err := r.write(rules["defaults"], []byte(DefaultsMarker+"return {}\n")); err == nil {
		t.Fatal("publicly writable directory accepted")
	}
}
func TestOwnerServeRejectsArbitraryPathAndRoot(t *testing.T) {
	var out bytes.Buffer
	err := Serve([]string{"read", "../../arbitrary"}, strings.NewReader(""), &out)
	if err == nil || out.Len() != 0 {
		t.Fatal("arbitrary path accepted")
	}
	if os.Geteuid() == 0 {
		if err := Serve([]string{"identity"}, nil, &out); err == nil {
			t.Fatal("helper allowed root")
		}
	}
}
