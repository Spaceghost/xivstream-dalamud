//go:build linux

package ownerfile

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/mods"
	"golang.org/x/sys/unix"
)

func bundleRule(version string) (rule, error) {
	if !mods.ValidGhosttyVersion(version) {
		return rule{}, errors.New("invalid Ghostty version")
	}
	return rule{path: ".xlcore/installedPlugins/GhosttyDalamud/" + version + "/GhosttyDalamud.dll", limit: mods.GhosttyFileLimit, mode: 0600, kind: "assembly", createOnly: true}, nil
}

func readBundle(input io.Reader, version string) (map[string][]byte, error) {
	if _, err := bundleRule(version); err != nil {
		return nil, err
	}
	tr := tar.NewReader(io.LimitReader(input, mods.GhosttyBundleLimit+(2<<20)))
	files := map[string][]byte{}
	seen := map[string]bool{}
	total := int64(0)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid Ghostty bundle stream")
		}
		key := strings.ToLower(h.Name)
		if !mods.ValidGhosttyPath(h.Name) || h.Typeflag != tar.TypeReg || (h.Mode != 0600 && h.Mode != 0644) || len(h.PAXRecords) != 0 || h.Format != tar.FormatUSTAR || h.Size < 0 || h.Size > mods.GhosttyFileLimit || len(files) >= mods.GhosttyEntryLimit || seen[key] {
			return nil, errors.New("unsafe Ghostty bundle member")
		}
		seen[key] = true
		total += h.Size
		if total > mods.GhosttyBundleLimit {
			return nil, errors.New("Ghostty bundle exceeds bound")
		}
		data, err := io.ReadAll(tr)
		if err != nil || int64(len(data)) != h.Size {
			return nil, errors.New("incomplete Ghostty bundle member")
		}
		files[h.Name] = data
	}
	if !bytes.HasPrefix(files["GhosttyDalamud.dll"], []byte("MZ")) {
		return nil, errors.New("missing Ghostty managed assembly")
	}
	var manifest struct{ InternalName, AssemblyVersion string }
	if json.Unmarshal(files["GhosttyDalamud.json"], &manifest) != nil || manifest.InternalName != "GhosttyDalamud" || manifest.AssemblyVersion != version {
		return nil, errors.New("Ghostty manifest identity mismatch")
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	if err := mods.ValidateGhosttyPaths(names, nil); err != nil {
		return nil, err
	}
	return files, nil
}

func (r *root) installBundle(version string, input io.Reader) error {
	files, err := readBundle(input, version)
	if err != nil {
		return err
	}
	dir, name, err := r.parent(".xlcore/installedPlugins/GhosttyDalamud/"+version, true)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	var existing unix.Stat_t
	if e := unix.Fstatat(dir, name, &existing, unix.AT_SYMLINK_NOFOLLOW); e == nil {
		old, err := r.readInstalledBundle(version)
		if err != nil {
			return err
		}
		if len(old) != len(files) {
			return errors.New("existing Ghostty bundle differs")
		}
		for name, data := range files {
			previous, ok := old[name]
			if !ok || !bytes.Equal(previous, data) {
				return errors.New("existing Ghostty bundle differs")
			}
		}
		return nil
	} else if !errors.Is(e, unix.ENOENT) {
		return e
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	stage := ".xivstream-bundle-" + hex.EncodeToString(random[:])
	if err = unix.Mkdirat(dir, stage, 0700); err != nil {
		return err
	}
	fd, err := unix.Openat(dir, stage, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	stageRoot := &root{fd, r.uid}
	if err = stageRoot.directory(fd); err != nil {
		return err
	}
	var original unix.Stat_t
	if err = unix.Fstat(fd, &original); err != nil {
		return err
	}
	// No recursive cleanup by pathname: a hostile account could have swapped
	// descendants. Failed staging stays private, clearly named, and uninstalled.
	// Successful staging is renamed as one directory and leaves no temporary.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := rule{path: name, limit: mods.GhosttyFileLimit, mode: 0600, kind: "bundle", createOnly: true}
		if err = stageRoot.write(p, files[name]); err != nil {
			return err
		}
	}
	if err = unix.Fsync(fd); err != nil {
		return err
	}
	var named unix.Stat_t
	if err = unix.Fstatat(dir, stage, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if named.Dev != original.Dev || named.Ino != original.Ino || named.Mode != original.Mode || named.Uid != r.uid {
		return errors.New("Ghostty staging directory changed")
	}
	if err = unix.Renameat2(dir, stage, dir, name, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	return unix.Fsync(dir)
}

func (r *root) inspectBundle(version string) (Metadata, error) {
	p, err := bundleRule(version)
	if err != nil {
		return Metadata{}, err
	}
	files, err := r.readInstalledBundle(version)
	if errors.Is(err, os.ErrNotExist) {
		// An existing partial version must not be treated as a fresh install.
		dir, name, e := r.parent(path.Dir(p.path), false)
		if errors.Is(e, os.ErrNotExist) {
			return Metadata{}, nil
		}
		if e != nil {
			return Metadata{}, e
		}
		defer unix.Close(dir)
		var st unix.Stat_t
		e = unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(e, unix.ENOENT) {
			return Metadata{}, nil
		}
		return Metadata{}, errors.New("partial or unsafe Ghostty version already exists")
	}
	if err != nil {
		return Metadata{}, err
	}
	var m struct{ InternalName, AssemblyVersion string }
	if json.Unmarshal(files["GhosttyDalamud.json"], &m) != nil || m.InternalName != "GhosttyDalamud" || m.AssemblyVersion != version || !bytes.HasPrefix(files["GhosttyDalamud.dll"], []byte("MZ")) {
		return Metadata{}, errors.New("installed Ghostty identity mismatch")
	}
	return Metadata{Exists: true, Size: len(files["GhosttyDalamud.dll"])}, nil
}

func (r *root) readInstalledBundle(version string) (map[string][]byte, error) {
	p, err := bundleRule(version)
	if err != nil {
		return nil, err
	}
	dir, name, err := r.parent(path.Dir(p.path), false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, name, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	base := &root{fd, r.uid}
	if err = base.directory(fd); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	seen := map[string]bool{}
	total := 0
	entries := 0
	var walk func(int, string, int) error
	walk = func(current int, prefix string, depth int) error {
		if depth > 8 {
			return errors.New("Ghostty bundle nesting exceeds bound")
		}
		dup, err := unix.FcntlInt(uintptr(current), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(dup), "bundle-directory")
		defer f.Close()
		for {
			items, e := f.ReadDir(64)
			if e != nil && e != io.EOF {
				return e
			}
			for _, item := range items {
				entries++
				if entries > mods.GhosttyEntryLimit*9 {
					return errors.New("Ghostty bundle entry count exceeds bound")
				}
				name := path.Join(prefix, item.Name())
				if !mods.ValidGhosttyPath(name) {
					return errors.New("unsafe installed Ghostty path")
				}
				key := strings.ToLower(name)
				if seen[key] {
					return errors.New("ambiguous installed Ghostty path")
				}
				seen[key] = true
				var st unix.Stat_t
				if err = unix.Fstatat(current, item.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
					return err
				}
				if st.Mode&unix.S_IFMT == unix.S_IFDIR {
					next, e := unix.Openat(current, item.Name(), unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
					if e != nil {
						return e
					}
					e = base.directory(next)
					if e == nil {
						e = walk(next, name, depth+1)
					}
					unix.Close(next)
					if e != nil {
						return e
					}
				} else {
					if len(files) >= mods.GhosttyEntryLimit {
						return errors.New("Ghostty bundle file count exceeds bound")
					}
					data, e := base.readAt(current, item.Name(), rule{limit: mods.GhosttyFileLimit, mode: 0600, kind: "bundle"})
					if e != nil {
						return e
					}
					total += len(data)
					if total > mods.GhosttyBundleLimit {
						return errors.New("Ghostty bundle exceeds bound")
					}
					files[name] = data
				}
			}
			if e == io.EOF {
				break
			}
		}
		return nil
	}
	if err = walk(fd, "", 0); err != nil {
		return nil, err
	}
	return files, nil
}
