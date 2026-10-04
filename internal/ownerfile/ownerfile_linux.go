//go:build linux

// Package ownerfile is the bounded, non-root filesystem side of Ghostty
// provisioning. It accepts named installer objects, never arbitrary paths.
package ownerfile

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const DefaultsMarker = "-- Managed by xivstream: Ghostty first-run defaults v1.\n"
const UnitMarker = "# Managed by xivstream; never restarted automatically on re-apply.\n"
const Protocol = "xivstream-owner-file-v1"

type rule struct {
	path       string
	limit      int64
	mode       uint32
	kind       string
	createOnly bool
}

var rules = map[string]rule{
	"agent-binary":           {".local/lib/xivstream/ghostty-agent", 64 << 20, 0755, "binary", true},
	"host-unit":              {".config/systemd/user/xivstream-ghostty-host.service", 8192, 0600, "unit", true},
	"container-unit":         {".config/systemd/user/xivstream-ghostty-container.service", 8192, 0600, "unit", true},
	"host-token-source":      {".config/xivstream/ghostty-host.token", 1025, 0600, "token", true},
	"container-token-source": {".config/xivstream/ghostty-container.token", 1025, 0600, "token", true},
	"host-token-copy":        {".xlcore/pluginConfigs/GhosttyDalamud/host-agent.token", 1025, 0600, "token", false},
	"container-token-copy":   {".xlcore/pluginConfigs/GhosttyDalamud/container-agent.token", 1025, 0600, "token", false},
	"defaults":               {".xlcore/pluginConfigs/GhosttyDalamud/agent-defaults.lua", 8192, 0600, "defaults", false},
	"dalamud-config":         {".xlcore/dalamudConfig.json", 4 << 20, 0600, "json", false},
}

type Metadata struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int    `json:"size,omitempty"`
}
type root struct {
	fd  int
	uid uint32
}

func openRoot(home string, uid uint32) (*root, error) {
	// The NSS home can include a system alias such as Fedora's /home -> /var/home.
	// Resolve that trusted account root once, as the non-root account. Every
	// component below it is subsequently opened relative to an fd with NOFOLLOW.
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil || !filepath.IsAbs(resolved) || resolved == "/" {
		return nil, errors.New("invalid owner home")
	}
	fd, err := unix.Open(resolved, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	r := &root{fd, uid}
	if err = r.directory(fd); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return r, nil
}
func (r *root) directory(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != r.uid || st.Mode&0022 != 0 {
		return errors.New("unsafe owner directory")
	}
	return nil
}
func (r *root) parent(path string, create bool) (int, string, error) {
	parts := strings.Split(path, "/")
	fd, err := unix.Dup(r.fd)
	if err != nil {
		return -1, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return -1, "", errors.New("unsafe relative path")
		}
		next, e := unix.Openat(fd, part, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) && create {
			if e = unix.Mkdirat(fd, part, 0700); e == nil || errors.Is(e, unix.EEXIST) {
				next, e = unix.Openat(fd, part, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if e != nil {
			return -1, "", e
		}
		fd = next
		if e = r.directory(fd); e != nil {
			unix.Close(fd)
			return -1, "", e
		}
	}
	name := parts[len(parts)-1]
	if name == "" || name == "." || name == ".." {
		unix.Close(fd)
		return -1, "", errors.New("unsafe basename")
	}
	return fd, name, nil
}
func validContent(p rule, data []byte) error {
	if int64(len(data)) > p.limit {
		return errors.New("owner file exceeds bound")
	}
	switch p.kind {
	case "binary":
		if len(data) < 4 || !bytes.Equal(data[:4], []byte("\x7fELF")) {
			return errors.New("not an ELF binary")
		}
	case "unit":
		if !bytes.HasPrefix(data, []byte(UnitMarker)) {
			return errors.New("unmanaged user unit")
		}
	case "defaults":
		if !bytes.HasPrefix(data, []byte(DefaultsMarker)) {
			return errors.New("unmanaged defaults")
		}
	case "token":
		value := bytes.TrimSpace(data)
		if len(value) < 16 || len(value) > 1024 || bytes.IndexFunc(value, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
			return errors.New("invalid private token")
		}
	case "json":
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return errors.New("invalid owner JSON object")
		}
	case "assembly":
		if !bytes.HasPrefix(data, []byte("MZ")) {
			return errors.New("invalid managed assembly")
		}
	case "bundle": // bounded regular archive members, interpreted by the plugin only
	default:
		return errors.New("unknown owner file kind")
	}
	return nil
}
func (r *root) readAt(dir int, name string, p rule) ([]byte, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "owner-file")
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	mode := st.Mode & 07777
	allowedMode := mode == p.mode || ((p.kind == "unit" || p.kind == "json" || p.kind == "assembly" || p.kind == "bundle") && mode == 0644)
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != r.uid || st.Nlink != 1 || !allowedMode || st.Size < 0 || st.Size > p.limit {
		return nil, errors.New("unsafe owner file metadata")
	}
	if size, e := unix.Fgetxattr(fd, "security.capability", nil); size > 0 || (e != nil && !errors.Is(e, unix.ENODATA) && !errors.Is(e, unix.EOPNOTSUPP)) {
		return nil, errors.New("unsafe owner file capabilities")
	}
	data, err := io.ReadAll(io.LimitReader(f, p.limit+1))
	if err != nil {
		return nil, err
	}
	if err = validContent(p, data); err != nil {
		return nil, err
	}
	return data, nil
}
func (r *root) read(p rule) ([]byte, error) {
	dir, name, err := r.parent(p.path, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	return r.readAt(dir, name, p)
}
func (r *root) write(p rule, data []byte) error {
	return r.writeWithHook(p, data, nil)
}

// beforePublish is a deterministic test seam, never set by Serve.
func (r *root) writeWithHook(p rule, data []byte, beforePublish func(int, string)) error {
	if err := validContent(p, data); err != nil {
		return err
	}
	dir, name, err := r.parent(p.path, true)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	old, err := r.readAt(dir, name, p)
	existed := err == nil
	if err == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		if p.createOnly {
			return errors.New("existing managed object differs")
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".xivstream-owner-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "private-owner-temp")
	var created unix.Stat_t
	if err = unix.Fstat(fd, &created); err != nil {
		f.Close()
		return err
	}
	// Keep an inode reference after the checked writer close. An adversarial
	// unlink/recreate cannot recycle its inode number before conditional cleanup.
	pin, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		var named unix.Stat_t
		if unix.Fstatat(dir, tmp, &named, unix.AT_SYMLINK_NOFOLLOW) == nil && named.Ino == created.Ino && named.Dev == created.Dev {
			_ = unix.Unlinkat(dir, tmp, 0)
		}
		f.Close()
		return err
	}
	defer unix.Close(pin)
	defer func() {
		var named unix.Stat_t
		if unix.Fstatat(dir, tmp, &named, unix.AT_SYMLINK_NOFOLLOW) == nil && named.Ino == created.Ino && named.Dev == created.Dev && named.Uid == r.uid && named.Mode&unix.S_IFMT == unix.S_IFREG {
			_ = unix.Unlinkat(dir, tmp, 0)
		}
	}()
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(os.FileMode(p.mode)); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	var written unix.Stat_t
	if err = unix.Fstat(fd, &written); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	closed = true
	if beforePublish != nil {
		beforePublish(dir, tmp)
	}
	// Revalidate the old object before publication; never follow destination
	// symlinks or rewrite a file whose owner/type/private mode is now unsafe.
	current, e := r.readAt(dir, name, p)
	if e != nil && !errors.Is(e, unix.ENOENT) {
		return e
	}
	if !bytes.Equal(current, old) || (e == nil) != existed {
		return errors.New("owner file changed during write")
	}
	var named unix.Stat_t
	if err = unix.Fstatat(dir, tmp, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if named.Ino != written.Ino || named.Dev != written.Dev || named.Mode != written.Mode || named.Uid != r.uid || named.Nlink != 1 {
		return errors.New("owner temporary changed before publication")
	}
	if p.createOnly || !existed {
		err = unix.Renameat2(dir, tmp, dir, name, unix.RENAME_NOREPLACE)
	} else {
		err = unix.Renameat(dir, tmp, dir, name)
	}
	if err != nil {
		return err
	}
	return unix.Fsync(dir)
}

// Serve is invoked only after runuser has selected an explicit native account.
// All errors are deliberately redacted by the caller. Secret bytes only use
// inherited stdin/stdout; no filename or content is accepted from an environment.
func Serve(args []string, input io.Reader, output io.Writer) error {
	if os.Geteuid() == 0 || os.Getuid() != os.Geteuid() {
		return errors.New("owner-file requires a non-root ordinary account")
	}
	u, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return errors.New("owner identity unavailable")
	}
	if len(args) == 1 && args[0] == "identity" {
		return json.NewEncoder(output).Encode(struct{ Protocol, User, UID, Home string }{Protocol, u.Username, u.Uid, u.HomeDir})
	}
	if len(args) != 2 {
		return errors.New("invalid owner-file operation")
	}
	p, ok := rules[args[1]]
	if !ok && args[0] != "inspect-ghostty-bundle" && args[0] != "install-ghostty-bundle" {
		return errors.New("unknown owner-file object")
	}
	r, err := openRoot(u.HomeDir, uint32(os.Geteuid()))
	if err != nil {
		return errors.New("owner-file home unavailable")
	}
	defer unix.Close(r.fd)
	switch args[0] {
	case "inspect-ghostty-bundle":
		metadata, e := r.inspectBundle(args[1])
		if e != nil {
			return errors.New("Ghostty bundle inspection refused")
		}
		return json.NewEncoder(output).Encode(metadata)
	case "install-ghostty-bundle":
		if e := r.installBundle(args[1], input); e != nil {
			return errors.New("Ghostty bundle installation refused")
		}
		_, err = fmt.Fprintln(output, "ok")
		return err
	case "read", "inspect":
		data, e := r.read(p)
		if args[0] == "inspect" && errors.Is(e, unix.ENOENT) {
			return json.NewEncoder(output).Encode(Metadata{})
		}
		if e != nil {
			return errors.New("owner-file read refused")
		}
		if args[0] == "read" {
			_, err = output.Write(data)
			return err
		}
		sum := sha256.Sum256(data)
		return json.NewEncoder(output).Encode(Metadata{true, hex.EncodeToString(sum[:]), len(data)})
	case "write":
		if strings.HasSuffix(args[1], "-source") {
			return errors.New("source tokens are agent-owned")
		}
		data, e := io.ReadAll(io.LimitReader(input, p.limit+1))
		if e != nil {
			return errors.New("owner-file input failed")
		}
		if e = r.write(p, data); e != nil {
			return errors.New("owner-file write refused")
		}
		_, err = fmt.Fprintln(output, "ok")
		return err
	default:
		return errors.New("invalid owner-file operation")
	}
}
