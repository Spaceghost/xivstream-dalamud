package steps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
	"github.com/Spaceghost/xivstream-dalamud/internal/releases"
)

const ghosttyProxy = "xivstream-ghostty-host"
const ghosttyMarker = "-- Managed by xivstream: Ghostty first-run defaults v1.\n"
const hostTokenName = "host-agent.token"
const containerTokenName = "container-agent.token"

var errGhosttyTokenNotReady = errors.New("Ghostty agent token is not ready; retry after the native user service starts (it is not restarted)")

type ghosttySetup struct {
	account *user.User
	label   string
}

// Resolving an account is read-only and happens before the first plan step.
// Even sudo must prove SUDO_USER corresponds to SUDO_UID; USER/LOGNAME and
// the container's session user are not evidence of a physical-host account.
func ghosttyAccount(explicit string, uid int, getenv func(string) string, lookup func(string) (*user.User, error), lookupID func(string) (*user.User, error)) (*user.User, error) {
	var u *user.User
	var err error
	switch {
	case explicit != "":
		u, err = lookup(explicit)
	case uid != 0:
		u, err = lookupID(strconv.Itoa(uid))
	case getenv("SUDO_USER") != "" && getenv("SUDO_UID") != "":
		u, err = lookup(getenv("SUDO_USER"))
		if err == nil && (u == nil || u.Uid != getenv("SUDO_UID")) {
			err = errors.New("sudo identity mismatch")
		}
	default:
		err = errors.New("no invoking account")
	}
	if err != nil || u == nil || u.Uid == "0" || u.Username == "root" || !filepath.IsAbs(u.HomeDir) || u.HomeDir == "/" {
		return nil, errors.New("set mods.ghostty.host_user to an existing non-root physical-host account (or invoke xivstream through sudo as that user); no changes were made")
	}
	// Account names become a user-service selector; reject unusual NSS values
	// instead of turning them into shell/systemd syntax.
	if err := (config.Ghostty{HostUser: u.Username, HostPort: 7777, ProxyPort: 7780}).Validate(); err != nil {
		return nil, err
	}
	for _, id := range []string{u.Uid, u.Gid} {
		n, e := strconv.Atoi(id)
		if e != nil || n < 0 || strconv.Itoa(n) != id {
			return nil, errors.New("host account has invalid numeric ownership")
		}
	}
	if strings.ContainsAny(u.HomeDir, "\x00\r\n") {
		return nil, errors.New("host account home has unsafe characters")
	}
	return u, nil
}

func (b *builder) prepareGhostty() error {
	if b.c.Topology != config.TopologyIncus || !b.c.Mods.Companions {
		return nil
	}
	wanted := false
	for _, name := range b.c.Mods.Install {
		if name == "GhosttyDalamud" {
			wanted = true
		}
	}
	if !wanted {
		return nil
	}
	if b.f.Init != "systemd" || runtime.GOARCH != "amd64" {
		return errors.New("native Ghostty host provisioning currently needs a systemd x86-64 Linux host; set mods.companions=false to provision the native host agent manually")
	}
	u, err := ghosttyAccount(b.c.Mods.Ghostty.HostUser, os.Getuid(), os.Getenv, user.Lookup, user.LookupId)
	if err != nil {
		return err
	}
	label := b.c.Mods.Ghostty.HostLabel
	if label == "" {
		label, err = os.Hostname()
		if err != nil {
			return errors.New("cannot read physical host name; set mods.ghostty.host_label")
		}
	}
	g := b.c.Mods.Ghostty
	g.HostLabel = label
	if err := g.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(label) == "" {
		return errors.New("mods.ghostty.host_label must not be blank")
	}
	// Incus commands in this topology address the default remote. Refuse a
	// remote daemon: a host-local account and loopback endpoint would be wrong.
	host := plan.Local{}
	remote, err := host.Run("incus", "remote", "get-default")
	if err != nil || strings.TrimSpace(remote) != "local" {
		return errors.New("physical-host Ghostty provisioning requires the local Incus daemon; select the local remote before planning")
	}
	b.ghostty = &ghosttySetup{account: u, label: label}
	if b.c.Session.User == "" || (config.Ghostty{HostUser: b.c.Session.User, HostPort: 7777, ProxyPort: 7780}).Validate() != nil {
		return errors.New("Ghostty requires an explicit non-root container session account")
	}
	// User-file preflight runs in the plan after installSelf and session account
	// creation. A fresh install cannot invoke a helper that is not installed yet.
	return nil
}

func ghosttyUnit(port int, kind string) []byte {
	return []byte(fmt.Sprintf(`# Managed by xivstream; never restarted automatically on re-apply.
[Unit]
Description=xivstream Ghostty %s agent (native account, not a container shell)

[Service]
UMask=0077
ExecStartPre=/usr/bin/install -d -m0700 %%h/.config/xivstream
ExecStart=%%h/.local/lib/xivstream/ghostty-agent --listen 127.0.0.1:%d --token-file %%h/.config/xivstream/ghostty-%s.token --windows off
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, kind, port, kind))
}

func agentUnitPath(home, kind string) string {
	return filepath.Join(home, ".config/systemd/user/xivstream-ghostty-"+kind+".service")
}
func agentTokenPath(home, kind string) string {
	return filepath.Join(home, ".config/xivstream/ghostty-"+kind+".token")
}

func ghosttyExistingUnit(t plan.Target, home, kind string, port int) error {
	p := agentUnitPath(home, kind)
	exists, err := ghosttyFileExists(t, p)
	if err != nil {
		return err
	}
	if exists {
		old, err := t.ReadFile(p)
		if err != nil || !bytes.Equal(old, ghosttyUnit(port, kind)) {
			return fmt.Errorf("Ghostty %s unit already exists with different settings; reconcile it manually before applying (no service is overwritten or restarted)", kind)
		}
	}
	return nil
}

func ghosttyFileExists(files plan.Target, p string) (bool, error) {
	if owner, ok := files.(*ghosttyOwnerTarget); ok {
		m, err := owner.metadata(p)
		return m.Exists, err
	}
	return files.Exists(p), nil // injected filesystem fixture; production uses owner targets
}

func (b *builder) ghosttyPreflight(host, ct plan.Target, hostFiles, ctFiles *ghosttyOwnerTarget) error {
	g, u := b.c.Mods.Ghostty, b.ghostty.account
	for _, files := range []*ghosttyOwnerTarget{hostFiles, ctFiles} {
		if err := files.probe(); err != nil {
			return err
		}
		// Fail closed on unsafe/unreadable objects, not only objects Exists can
		// positively identify. Include the optional container source only if used.
		objects := []string{".xlcore/pluginConfigs/GhosttyDalamud/agent-defaults.lua", ".xlcore/pluginConfigs/GhosttyDalamud/host-agent.token"}
		if files == hostFiles {
			objects = []string{".local/lib/xivstream/ghostty-agent", ".config/systemd/user/xivstream-ghostty-host.service", ".config/xivstream/ghostty-host.token"}
		}
		if files == ctFiles && g.IncludeContainer {
			objects = append(objects, ".local/lib/xivstream/ghostty-agent", ".config/systemd/user/xivstream-ghostty-container.service", ".config/xivstream/ghostty-container.token", ".xlcore/pluginConfigs/GhosttyDalamud/container-agent.token")
		}
		for _, object := range objects {
			if _, err := files.metadata(path.Join(files.home, object)); err != nil {
				return err
			}
		}
	}
	if err := ghosttyExistingUnit(hostFiles, u.HomeDir, "host", g.HostPort); err != nil {
		return err
	}
	// Refuse a port owned by another service, even a regular ghostty-agent:
	// we cannot infer its token path, native namespace, or service ownership.
	if err := ghosttyPortConflict(host, hostFiles, u.HomeDir, u.Username, "host", g.HostPort); err != nil {
		return err
	}
	if _, err := host.Run("test", "-x", "/bin/bash"); err != nil {
		return errors.New("the native host needs /bin/bash for the provisioned terminal profiles")
	}
	if _, err := host.Run("sh", "-c", "command -v tmux >/dev/null"); err != nil {
		return errors.New("install tmux on the physical host before enabling Ghostty companions; a container tmux is not a host shell")
	}
	if g.IncludeContainer {
		if _, err := ct.Run("test", "-x", "/bin/bash"); err != nil {
			return errors.New("the optional container profiles require /bin/bash inside the game container")
		}
		if _, err := ct.Run("sh", "-c", "command -v tmux >/dev/null"); err != nil {
			return errors.New("the optional container profiles require tmux inside the game container; install it there or disable include_container")
		}
	}
	// No container yet is normal on a clean install. Existing devices are
	// compared exactly; never delete a foreign proxy to make our port free.
	raw, err := host.Run("incus", "list", b.c.Incus.Container, "--format=json")
	if err != nil {
		return errors.New("cannot inspect local Incus instances before Ghostty provisioning")
	}
	var instances []struct {
		Name            string                       `json:"name"`
		Status          string                       `json:"status"`
		Devices         map[string]map[string]string `json:"devices"`
		ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
	}
	if json.Unmarshal([]byte(raw), &instances) != nil {
		return errors.New("cannot parse local Incus instance metadata")
	}
	for _, instance := range instances {
		if instance.Name != b.c.Incus.Container {
			continue
		}
		devices := instance.ExpandedDevices
		if devices == nil {
			devices = instance.Devices
		}
		if err := ghosttyProxyConflicts(devices, g); err != nil {
			return err
		}
		if instance.Status != "Running" {
			continue
		}
		if _, exists := devices[ghosttyProxy]; !exists {
			if out, err := ct.Run("ss", "-H", "-ltn", "sport = :"+strconv.Itoa(g.ProxyPort)); err != nil || strings.TrimSpace(out) != "" {
				return errors.New("Ghostty container proxy port is occupied or cannot be inspected; choose another mods.ghostty.proxy_port")
			}
		}
		if g.IncludeContainer {
			home := "/home/" + b.c.Session.User
			if err := ghosttyExistingUnit(ctFiles, home, "container", 7777); err != nil {
				return err
			}
			if err := ghosttyPortConflict(ct, ctFiles, home, b.c.Session.User, "container", 7777); err != nil {
				return err
			}
		}
		p := path.Join("/home", b.c.Session.User, ".xlcore/pluginConfigs/GhosttyDalamud/agent-defaults.lua")
		if ctFiles.Exists(p) {
			old, err := ctFiles.ReadFile(p)
			if err != nil || !bytes.HasPrefix(old, []byte(ghosttyMarker)) {
				return errors.New("agent-defaults.lua already exists and is not owned by xivstream; preserve it or move it manually before applying")
			}
		}
	}
	return nil
}

func ghosttyPortConflict(t, files plan.Target, home, username, kind string, port int) error {
	out, err := t.Run("ss", "-H", "-ltn", "sport = :"+strconv.Itoa(port))
	if err != nil {
		return errors.New("cannot inspect Ghostty listener conflicts (ss is required)")
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	exists, e := ghosttyFileExists(files, agentUnitPath(home, kind))
	if e != nil {
		return e
	}
	if exists {
		if _, err := t.Run("systemctl", "--user", "-M", username+"@", "is-active", "--quiet", "xivstream-ghostty-"+kind+".service"); err == nil {
			return nil
		}
	}
	return fmt.Errorf("Ghostty %s port %d is already occupied by an unmanaged service; choose an unused configured port (no service is stopped)", kind, port)
}

func ghosttyProxyOptions(g config.Ghostty) map[string]string {
	return map[string]string{"type": "proxy", "bind": "instance", "listen": fmt.Sprintf("tcp:127.0.0.1:%d", g.ProxyPort), "connect": fmt.Sprintf("tcp:127.0.0.1:%d", g.HostPort)}
}

func ghosttyProxyConflicts(devices map[string]map[string]string, g config.Ghostty) error {
	want := ghosttyProxyOptions(g)
	for name, d := range devices {
		if name == ghosttyProxy {
			if len(d) != len(want) {
				return errors.New("existing xivstream Ghostty proxy differs; reconcile it manually without replacing a live proxy")
			}
			for key, value := range want {
				if d[key] != value {
					return errors.New("existing xivstream Ghostty proxy differs; reconcile it manually without replacing a live proxy")
				}
			}
		} else if d["type"] == "proxy" && d["bind"] == "instance" && d["listen"] == want["listen"] {
			return errors.New("another container proxy owns the selected Ghostty port; choose another mods.ghostty.proxy_port")
		}
	}
	return nil
}

// Quote only validated, non-secret metadata into Lua. strconv.Quote may emit
// Go-only escapes, so use a byte encoder with Lua's fixed-width decimal escapes.
func luaString(s string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, b := range []byte(s) {
		switch b {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteByte(b)
		default:
			if b < 32 || b == 127 {
				fmt.Fprintf(&out, "\\%03d", b)
			} else {
				out.WriteByte(b)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

func ghosttyDefaults(label, container string, g config.Ghostty) []byte {
	var out strings.Builder
	out.WriteString(ghosttyMarker)
	fmt.Fprintf(&out, "return { version = 1,\n  primary = { label = %s, host = '127.0.0.1', port = %d, token_file = '%s' },\n", luaString(label), g.ProxyPort, hostTokenName)
	if g.IncludeContainer {
		fmt.Fprintf(&out, "  container = { label = %s, host = '127.0.0.1', port = 7777, token_file = '%s' },\n", luaString(container), containerTokenName)
	}
	out.WriteString("}\n")
	return []byte(out.String())
}

// Extract exactly the regular binary member from the authoritative portable
// layout (tools/ci/agent-rpm.sh). Never unpack a release over /usr/local or an
// existing user home; reject traversal, links, duplicate or oversized binaries.
func ghosttyPortableBinary(src io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return nil, errors.New("invalid Ghostty portable archive")
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, 128<<20))
	var binary []byte
	member := regexp.MustCompile(`^ghostty-agent-[0-9][0-9A-Za-z._-]*-linux-x86_64/ghostty-agent$`)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid Ghostty portable archive member")
		}
		name := strings.TrimPrefix(h.Name, "./")
		if path.IsAbs(name) || path.Clean(name) != strings.TrimSuffix(name, "/") || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") {
			return nil, errors.New("unsafe Ghostty archive path")
		}
		if !member.MatchString(name) {
			continue
		}
		if binary != nil || h.Typeflag != tar.TypeReg || h.Size < 4 || h.Size > 64<<20 {
			return nil, errors.New("invalid Ghostty binary member")
		}
		binary, err = io.ReadAll(tr)
		if err != nil {
			return nil, errors.New("cannot read Ghostty binary member")
		}
	}
	if len(binary) < 4 || string(binary[:4]) != "\x7fELF" {
		return nil, errors.New("portable archive has no native ELF ghostty-agent binary")
	}
	return binary, nil
}

func downloadGhostty(pre bool) ([]byte, error) {
	a, err := releases.Find("Spaceghost/ghostty-dalamud", `^ghostty-agent-linux-x86_64\.tar\.gz$`, pre)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(a.URL)
	if err != nil {
		return nil, errors.New("cannot download Ghostty portable release")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ghostty portable download returned HTTP %d", resp.StatusCode)
	}
	return ghosttyPortableBinary(io.LimitReader(resp.Body, 64<<20))
}

func (b *builder) nativeGhosttySteps(t, files plan.Target, username, home, owner, kind string, port int) []plan.Step {
	binary := path.Join(home, ".local/lib/xivstream/ghostty-agent")
	unit := "xivstream-ghostty-" + kind + ".service"
	return []plan.Step{
		step(t, "Install the native Ghostty "+kind+" agent", "Shells run as "+username+" in this machine's native namespace; no Podman/Toolbox wrapper.",
			[]string{"install the portable release binary into " + binary + " (existing binary is retained)"}, func() (bool, error) { return ghosttyFileExists(files, binary) }, func() error {
				present, err := ghosttyFileExists(files, binary)
				if err != nil {
					return err
				}
				if present {
					return nil
				}
				data, err := downloadGhostty(b.c.Mods.Testing)
				if err != nil {
					return err
				}
				return files.WriteFile(binary, data, 0o755, owner)
			}),
		step(t, "Write the native Ghostty "+kind+" user service", "Existing differently configured services are never overwritten.",
			[]string{"write " + agentUnitPath(home, kind)}, func() (bool, error) {
				return files.Exists(agentUnitPath(home, kind)), ghosttyExistingUnit(files, home, kind, port)
			}, func() error {
				if err := ghosttyExistingUnit(files, home, kind, port); err != nil {
					return err
				}
				return files.WriteFile(agentUnitPath(home, kind), ghosttyUnit(port, kind), 0o600, owner)
			}),
		step(t, "Start the native Ghostty "+kind+" user service", "Enable user linger for boot persistence. An active service is not restarted.",
			[]string{"loginctl enable-linger " + username, "systemctl --user -M " + username + "@ enable --now " + unit},
			func() (bool, error) {
				if !t.Exists("/var/lib/systemd/linger/" + username) {
					return false, nil
				}
				_, enabled := t.Run("systemctl", "--user", "-M", username+"@", "is-enabled", "--quiet", unit)
				_, active := t.Run("systemctl", "--user", "-M", username+"@", "is-active", "--quiet", unit)
				return enabled == nil && active == nil, nil
			}, func() error {
				if err := ghosttyPortConflict(t, files, home, username, kind, port); err != nil {
					return err
				}
				if _, err := t.Run("loginctl", "enable-linger", username); err != nil {
					return err
				}
				if _, err := t.Run("systemctl", "--user", "-M", username+"@", "daemon-reload"); err != nil {
					return err
				}
				_, err := t.Run("systemctl", "--user", "-M", username+"@", "enable", "--now", unit)
				return err
			}),
	}
}

func (b *builder) ghosttySteps(ct plan.Target, owner string) []plan.Step {
	return b.ghosttyStepsOn(plan.Local{}, ct, owner)
}

// Separate privileged control-plane targets from owner-file targets; this
// seam also lets the complete bootstrap run against strict command fixtures.
func (b *builder) ghosttyStepsOn(host, ct plan.Target, owner string) []plan.Step {
	g, u := b.c.Mods.Ghostty, b.ghostty.account
	hostFiles := ghosttyOwnerFiles(host, u.Username, u.HomeDir, ghosttyHostHelper())
	ctFiles := ghosttyOwnerFiles(ct, b.c.Session.User, "/home/"+b.c.Session.User, "/usr/local/bin/xivstream")
	preflight := func() error { return b.ghosttyPreflight(host, ct, hostFiles, ctFiles) }
	s := []plan.Step{step(host, "Validate native Ghostty owner-file isolation", "Runs after the trusted helper and session account exist; refuses unsafe paths before Ghostty changes.", []string{"verify root-installed helper, non-root owner identity and bounded no-follow file metadata"}, func() (bool, error) { err := preflight(); return err == nil, err }, preflight)}
	s = append(s, b.nativeGhosttySteps(host, hostFiles, u.Username, u.HomeDir, u.Uid+":"+u.Gid, "host", g.HostPort)...)
	if g.IncludeContainer {
		s = append(s, b.nativeGhosttySteps(ct, ctFiles, b.c.Session.User, "/home/"+b.c.Session.User, owner, "container", 7777)...)
	}
	proxyArgs := []string{"incus", "config", "device", "add", b.c.Incus.Container, ghosttyProxy, "proxy", "bind=instance", fmt.Sprintf("listen=tcp:127.0.0.1:%d", g.ProxyPort), fmt.Sprintf("connect=tcp:127.0.0.1:%d", g.HostPort)}
	s = append(s, step(host, "Connect the game's loopback to the physical host agent", "The reverse Incus proxy exposes neither the token nor a LAN listener.", []string{strings.Join(proxyArgs, " ")},
		func() (bool, error) { return ghosttyProxyMatches(host, b.c.Incus.Container, g) }, func() error {
			ok, err := ghosttyProxyMatches(host, b.c.Incus.Container, g)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
			_, err = host.Run(proxyArgs...)
			return err
		}))
	dir := path.Join("/home", b.c.Session.User, ".xlcore/pluginConfigs/GhosttyDalamud")
	s = append(s, ghosttyTokenStep(hostFiles, agentTokenPath(u.HomeDir, "host"), ctFiles, path.Join(dir, hostTokenName), owner))
	if g.IncludeContainer {
		s = append(s, ghosttyTokenStep(ctFiles, agentTokenPath("/home/"+b.c.Session.User, "container"), ctFiles, path.Join(dir, containerTokenName), owner))
	}
	want := ghosttyDefaults(b.ghostty.label, b.c.Incus.Container, g)
	p := path.Join(dir, "agent-defaults.lua")
	s = append(s, step(ct, "Seed Ghostty's physical-host defaults", "Used only by shipped init.lua. Saved user profiles, settings and paired hosts are not edited.", []string{"write " + p + " (non-secret metadata, mode 0600)"},
		func() (bool, error) {
			old, err := ctFiles.ReadFile(p)
			return err == nil && bytes.Equal(bytes.TrimSpace(old), bytes.TrimSpace(want)), nil
		}, func() error {
			if ctFiles.Exists(p) {
				old, err := ctFiles.ReadFile(p)
				if err != nil || !bytes.HasPrefix(old, []byte(ghosttyMarker)) {
					return errors.New("refusing to overwrite unowned agent-defaults.lua")
				}
			}
			return ctFiles.WriteFile(p, want, 0o600, owner)
		}))
	return s
}

func ghosttyProxyMatches(host plan.Target, container string, g config.Ghostty) (bool, error) {
	raw, err := host.Run("incus", "config", "device", "get", container, ghosttyProxy, "type")
	if err != nil {
		return false, nil
	}
	got := map[string]string{"type": strings.TrimSpace(raw)}
	for _, key := range []string{"bind", "listen", "connect"} {
		v, err := host.Run("incus", "config", "device", "get", container, ghosttyProxy, key)
		if err != nil {
			return false, errors.New("cannot inspect existing Ghostty proxy")
		}
		got[key] = strings.TrimSpace(v)
	}
	if err := ghosttyProxyConflicts(map[string]map[string]string{ghosttyProxy: got}, g); err != nil {
		return false, err
	}
	return true, nil
}

func ghosttyTokenStep(src plan.Target, source string, dest plan.Target, target, owner string) plan.Step {
	read := func() ([]byte, error) {
		for _, files := range []plan.Target{src, dest} {
			isolated, ok := files.(interface{ OwnerIsolated() bool })
			if !ok || !isolated.OwnerIsolated() {
				return nil, errors.New("Ghostty credentials require owner-isolated file access")
			}
		}
		if ownerFiles, ok := src.(*ghosttyOwnerTarget); ok {
			m, err := ownerFiles.metadata(source)
			if err != nil {
				return nil, err
			}
			if !m.Exists {
				return nil, errGhosttyTokenNotReady
			}
		}
		data, err := src.ReadFile(source)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, errGhosttyTokenNotReady
			}
			return nil, errors.New("Ghostty agent token is not ready or readable; retry after the native user service starts")
		}
		value := bytes.TrimSpace(data)
		if len(value) < 16 || len(value) > 1024 || bytes.IndexFunc(value, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
			return nil, errors.New("Ghostty agent token is invalid (contents redacted)")
		}
		return append(append([]byte(nil), value...), '\n'), nil
	}
	return step(dest, "Provision the Ghostty credential privately", "The bearer token grants shells as the selected host account; keep the game account trusted.",
		[]string{"copy native agent credential to " + target + " (mode 0600, contents never displayed)"},
		func() (bool, error) {
			data, err := read()
			if err != nil {
				return false, err
			}
			old, err := dest.ReadFile(target)
			return err == nil && bytes.Equal(bytes.TrimSpace(old), bytes.TrimSpace(data)), nil
		},
		func() error {
			data, err := read()
			// Starting a user unit does not prove the agent has persisted its
			// custom token yet. Wait briefly only for confirmed absence, never
			// retry unsafe metadata and never restart an already active agent.
			deadline := time.Now().Add(2 * time.Second)
			for errors.Is(err, errGhosttyTokenNotReady) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
				data, err = read()
			}
			if err != nil {
				return err
			}
			// This capability is implemented only by the non-root named-object
			// helper. No root shell ever reopens an account-owned temporary path.
			if err := dest.WriteFile(target, data, 0o600, owner); err != nil {
				return errors.New("cannot write private Ghostty token (contents redacted)")
			}
			return nil
		})
}
