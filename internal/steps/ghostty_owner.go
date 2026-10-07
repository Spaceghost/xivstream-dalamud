package steps

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/ownerfile"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

// No user-owned pathname is accessed by the privileged Target. Only this
// root-installed helper is executed, after runuser drops to the selected owner.
type ghosttyOwnerTarget struct {
	parent                 plan.Target
	username, home, helper string
}

func (o *ghosttyOwnerTarget) Name() string      { return o.parent.Name() + " as " + o.username }
func (*ghosttyOwnerTarget) OwnerIsolated() bool { return true }
func (o *ghosttyOwnerTarget) object(p string) (string, error) {
	rel, err := filepath.Rel(o.home, p)
	if err != nil {
		return "", errors.New("invalid owner object")
	}
	objects := map[string]string{
		".local/lib/xivstream/ghostty-agent":                         "agent-binary",
		".config/systemd/user/xivstream-ghostty-host.service":        "host-unit",
		".config/systemd/user/xivstream-ghostty-container.service":   "container-unit",
		".config/xivstream/ghostty-host.token":                       "host-token-source",
		".config/xivstream/ghostty-container.token":                  "container-token-source",
		".xlcore/pluginConfigs/GhosttyDalamud/host-agent.token":      "host-token-copy",
		".xlcore/pluginConfigs/GhosttyDalamud/container-agent.token": "container-token-copy",
		".xlcore/pluginConfigs/GhosttyDalamud/agent-defaults.lua":    "defaults",
		".xlcore/dalamudConfig.json":                                 "dalamud-config",
	}
	name, ok := objects[filepath.ToSlash(rel)]
	if !ok {
		return "", errors.New("unrecognized owner object")
	}
	return name, nil
}
func (o *ghosttyOwnerTarget) call(data []byte, args ...string) (string, error) {
	if err := o.trustedHelper(); err != nil {
		return "", err
	}
	argv := []string{"/usr/bin/timeout", "--kill-after=2s", "10s", "/usr/sbin/runuser", "-u", o.username, "--", "/usr/bin/env", "-i", o.helper, "internal-owner-file-v1"}
	argv = append(argv, args...)
	out, err := ghosttyOwnerCommand(o.parent, data, argv)
	if err != nil {
		return "", errors.New("owner-isolated Ghostty file operation refused (contents redacted)")
	}
	return out, nil
}
func (o *ghosttyOwnerTarget) probe() error {
	raw, err := o.call(nil, "identity")
	if err != nil {
		return err
	}
	var identity struct{ Protocol, User, UID, Home string }
	if json.Unmarshal([]byte(raw), &identity) != nil {
		return errors.New("owner helper identity mismatch")
	}
	uid, e := strconv.ParseUint(identity.UID, 10, 32)
	if e != nil || uid == 0 || identity.Protocol != ownerfile.Protocol || identity.User != o.username || identity.Home != o.home {
		return errors.New("owner helper identity mismatch")
	}
	return nil
}
func (o *ghosttyOwnerTarget) trustedHelper() error {
	if o.username == "" || (config.Ghostty{HostUser: o.username, HostPort: 7777, ProxyPort: 7780}).Validate() != nil {
		return errors.New("Ghostty requires an explicit non-root owner")
	}
	// Only fixed root-owned installation locations are accepted, never a home,
	// current-directory executable or PATH search. Run this after installSelf.
	if o.helper != "/usr/local/bin/xivstream" && o.helper != "/usr/bin/xivstream" {
		return errors.New("untrusted owner helper path")
	}
	paths := []string{"/", "/usr"}
	if o.helper == "/usr/local/bin/xivstream" {
		paths = append(paths, "/usr/local")
	}
	paths = append(paths, filepath.Dir(o.helper), o.helper)
	args := append([]string{"/usr/bin/stat", "-c", "%f:%u"}, paths...)
	raw, err := o.parent.Run(args...)
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if err != nil || len(lines) != len(paths) {
		return errors.New("root-owned xivstream helper is not installed yet")
	}
	for i, line := range lines {
		parts := strings.Split(line, ":")
		if len(parts) != 2 || parts[1] != "0" {
			return errors.New("unsafe xivstream helper ownership")
		}
		mode, e := strconv.ParseUint(parts[0], 16, 32)
		// Fedora Atomic installs /usr/local as this fixed, root-controlled
		// alias. Validate both the link and every target ancestor, not a
		// generic realpath that could pass through a user-writable directory.
		if paths[i] == "/usr/local" && e == nil && mode&0170000 == 0120000 {
			link, err := o.parent.Run("/usr/bin/readlink", "/usr/local")
			if err != nil || strings.TrimSpace(link) != "../var/usrlocal" {
				return errors.New("untrusted local installation alias")
			}
			raw, err := o.parent.Run("/usr/bin/stat", "-c", "%f:%u", "/var", "/var/usrlocal")
			aliases := strings.Split(strings.TrimSpace(raw), "\n")
			if err != nil || len(aliases) != 2 {
				return errors.New("unsafe local installation ancestors")
			}
			for _, ancestor := range aliases {
				fields := strings.Split(ancestor, ":")
				if len(fields) != 2 || fields[1] != "0" {
					return errors.New("unsafe local installation ownership")
				}
				m, err := strconv.ParseUint(fields[0], 16, 32)
				if err != nil || m&0170000 != 0040000 || m&06022 != 0 || m&0001 == 0 {
					return errors.New("unsafe local installation ancestors")
				}
			}
			continue
		}
		kind := uint64(0040000)
		if i == len(lines)-1 {
			kind = 0100000
		}
		if e != nil || mode&0170000 != kind || mode&06022 != 0 || mode&0001 == 0 {
			return errors.New("unsafe xivstream helper metadata")
		}
	}
	return nil
}
func (o *ghosttyOwnerTarget) metadata(p string) (ownerfile.Metadata, error) {
	var m ownerfile.Metadata
	object, err := o.object(p)
	if err != nil {
		return m, err
	}
	raw, err := o.call(nil, "inspect", object)
	if err != nil {
		return m, err
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return m, errors.New("invalid owner metadata response")
	}
	return m, nil
}
func (o *ghosttyOwnerTarget) Exists(p string) bool {
	m, err := o.metadata(p)
	return err == nil && m.Exists
}
func (o *ghosttyOwnerTarget) ReadFile(p string) ([]byte, error) {
	object, err := o.object(p)
	if err != nil {
		return nil, err
	}
	raw, err := o.call(nil, "read", object)
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}
func (o *ghosttyOwnerTarget) WriteFile(p string, data []byte, mode os.FileMode, owner string) error {
	object, err := o.object(p)
	if err != nil {
		return err
	}
	want := os.FileMode(0600)
	if object == "agent-binary" {
		want = 0755
	}
	if mode != want {
		return errors.New("invalid owner object mode")
	}
	result, err := o.call(data, "write", object)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace([]byte(result)), []byte("ok")) {
		return errors.New("invalid owner write acknowledgement")
	}
	return nil
}
func (*ghosttyOwnerTarget) Run(...string) (string, error) {
	return "", errors.New("owner file target does not execute arbitrary commands")
}
func (*ghosttyOwnerTarget) RunInput([]byte, ...string) (string, error) {
	return "", errors.New("owner file target does not execute arbitrary input")
}

func ghosttyOwnerFiles(parent plan.Target, username, home, helper string) *ghosttyOwnerTarget {
	return &ghosttyOwnerTarget{parent, username, home, helper}
}
func ghosttyHostHelper() string {
	if exe, err := os.Executable(); err == nil && exe == "/usr/bin/xivstream" {
		return exe
	}
	return "/usr/local/bin/xivstream"
}
