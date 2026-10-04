package wolf

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Spaceghost/xivstream-dalamud/internal/config"
)

// CreateJSON is the session container's base_create_json: Docker-API create
// options Wolf merges its own mounts, devices and env into. Wolf replaces
// DeviceCgroupRules with the live hidraw and input majors; the rule here is
// what it starts from.
func CreateJSON(s Setup) string {
	w := s.Wolf
	host := map[string]any{
		"NetworkMode": w.Network,
		// Wolf's examples all share the host's IPC namespace (its compositor
		// and the session exchange shared-memory buffers).
		"IpcMode":    "host",
		"Privileged": false,
		// MKNOD for Wolf's hot-plugged pads (docker exec ... mknod); the rest is
		// the least any upstream app runs with.
		"CapAdd":            []string{"NET_RAW", "MKNOD", "NET_ADMIN"},
		"SecurityOpt":       []string{"label=disable"}, // the home is unlabeled_t; never relabel 95 GB
		"DeviceCgroupRules": []string{"c 13:* rmw"},
	}
	if mem, _ := w.MemoryBytes(); mem > 0 {
		host["Memory"] = mem
	}
	// CPUs: game-cpu-fence pins the session's scope (busy/idle), not Podman.
	opts := map[string]any{
		"HostConfig": host,
		"NetworkingConfig": map[string]any{
			"EndpointsConfig": map[string]any{
				w.Network: map[string]any{"IPAMConfig": map[string]any{"IPv4Address": w.AppIP}},
			},
		},
	}
	data, _ := json.Marshal(opts) // map keys sort: stable output
	return string(data)
}

// AppEnv is the session's environment from xivstream (Wolf adds its own).
func AppEnv(s Setup) []string {
	var ports []string
	for _, f := range s.Wolf.Forwards {
		ports = append(ports, fmt.Sprint(f.Port))
	}
	env := []string{
		fmt.Sprintf("DXVK_FRAME_RATE=%d", s.Game.FPS),
		"XIVSTREAM_GATEWAY=" + s.Wolf.Gateway().String(),
		"XIVSTREAM_FORWARDS=" + strings.Join(ports, " "),
		"XIVSTREAM_HOME_UNIT=" + s.HomeUnit(),
	}
	if f, ok := s.MicForward(); ok {
		env = append(env, fmt.Sprintf("XIVSTREAM_MIC_PORT=%d", f.Port))
	}
	return env
}

// AppMounts are the session's bind mounts: the home, then the extra mounts
// whose source exists (a missing source would fail every session's start).
func AppMounts(s Setup, exists func(string) bool) (mounts, skipped []string) {
	mounts = []string{s.Wolf.Home + ":/home/player:rw"}
	for _, m := range s.Wolf.Mounts {
		host, ctr, mode, err := config.ParseMount(m)
		if err != nil {
			continue
		}
		if exists != nil && !exists(host) {
			skipped = append(skipped, m)
			continue
		}
		mounts = append(mounts, host+":"+ctr+":"+mode)
	}
	return mounts, skipped
}

// App is xivstream's entry in Wolf's moonlight profile.
func App(s Setup, exists func(string) bool) map[string]any {
	mounts, _ := AppMounts(s, exists)
	return map[string]any{
		"title":                    AppTitle,
		"start_virtual_compositor": true,
		"start_audio_server":       true,
		"runner": map[string]any{
			"type":             "docker",
			"name":             RunnerName,
			"image":            s.SessionImage,
			"env":              toAny(AppEnv(s)),
			"mounts":           toAny(mounts),
			"devices":          []any{},
			"ports":            []any{},
			"base_create_json": CreateJSON(s),
		},
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ControllersOverride is what stream.gamepad asks of Wolf's pads: Wolf
// otherwise follows what the Moonlight client reports.
func ControllersOverride(gamepad string) []string {
	switch gamepad {
	case "ds5":
		return []string{"PS"}
	case "x360":
		return []string{"XBOX"}
	}
	return nil
}

// MergeOptions are the machine's answers the merge needs.
type MergeOptions struct {
	Exists  func(string) bool // whether a mount source exists
	NewUUID func() string     // for a config created from scratch
}

// MergeConfig returns Wolf's config.toml with xivstream's app in it. With no
// existing file (or one Wolf would migrate from an older version, which is
// what an older xivstream wrote) it starts from Wolf's pinned default v7 with
// a new uuid and hostname = stream.name, carrying over any hostname, uuid and
// pairings. Otherwise everything Wolf keeps there (hostname, uuid,
// paired_clients, gstreamer, other profiles and apps) is kept; only
// xivstream's app in the moonlight profile is replaced. Two settings reach
// further, both documented: wolf.codecs = "h264" empties Wolf's HEVC and AV1
// encoder lists (and "auto" restores them from the default when they are
// empty), and paired clients with no controllers_override get stream.gamepad's.
func MergeConfig(existing []byte, s Setup, o MergeOptions) ([]byte, error) {
	cfg := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if _, err := toml.Decode(string(existing), &cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", ConfigFile, err)
		}
	}
	def, err := defaultConfigMap(o)
	if err != nil {
		return nil, err
	}
	fresh := false
	if v, _ := cfg["config_version"].(int64); v < 7 {
		// Not a v7 config: start from the default, keeping identity and pairings.
		fresh = true
		for _, k := range []string{"hostname", "uuid", "paired_clients"} {
			if v, ok := cfg[k]; ok {
				def[k] = v
			}
		}
		if _, ok := cfg["hostname"]; !ok && s.Stream.Name != "" {
			def["hostname"] = s.Stream.Name
		}
		cfg = def
	}

	// xivstream's app in the moonlight profile.
	app := App(s, o.Exists)
	profiles := tables(cfg["profiles"])
	found := false
	for _, p := range profiles {
		if p["id"] != MoonlightProfile {
			continue
		}
		found = true
		apps := tables(p["apps"])
		if fresh {
			apps = nil // Wolf UI and the test ball: not for this setup
		}
		replaced := false
		for i, a := range apps {
			if isOurs(a) {
				apps[i], replaced = app, true
			}
		}
		if !replaced {
			apps = append(apps, app)
		}
		p["apps"] = apps
	}
	if !found {
		profiles = append([]map[string]any{{"id": MoonlightProfile, "apps": []map[string]any{app}}}, profiles...)
	}
	cfg["profiles"] = profiles

	// Codecs.
	if gst, ok := cfg["gstreamer"].(map[string]any); ok {
		if video, ok := gst["video"].(map[string]any); ok {
			var defVideo map[string]any
			if g, ok := def["gstreamer"].(map[string]any); ok {
				defVideo, _ = g["video"].(map[string]any)
			}
			for _, list := range []string{"hevc_encoders", "av1_encoders"} {
				switch {
				case s.Wolf.Codecs == "h264":
					video[list] = []any{}
				case len(tables(video[list])) == 0 && defVideo != nil:
					video[list] = defVideo[list]
				}
			}
		}
	}

	// Pads.
	if want := ControllersOverride(s.Stream.Gamepad); want != nil {
		clients := tables(cfg["paired_clients"])
		for _, c := range clients {
			settings, ok := c["settings"].(map[string]any)
			if !ok {
				settings = map[string]any{}
				c["settings"] = settings
			}
			if len(asList(settings["controllers_override"])) == 0 {
				settings["controllers_override"] = toAny(want)
			}
		}
		if len(clients) > 0 {
			cfg["paired_clients"] = clients
		}
	}

	var out bytes.Buffer
	out.WriteString("# Wolf's configuration. xivstream (`xivstream wolf-config`, run before Wolf\n")
	out.WriteString("# starts) keeps its app here and leaves the rest as Wolf wrote it; Wolf\n")
	out.WriteString("# rewrites the file when a client pairs.\n")
	if err := toml.NewEncoder(&out).Encode(cfg); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// SameConfig: a and b say the same thing (whatever the formatting).
func SameConfig(a, b []byte) bool {
	var ma, mb map[string]any
	if _, err := toml.Decode(string(a), &ma); err != nil {
		return false
	}
	if _, err := toml.Decode(string(b), &mb); err != nil {
		return false
	}
	return reflect.DeepEqual(ma, mb)
}

func defaultConfigMap(o MergeOptions) (map[string]any, error) {
	uuid := ""
	if o.NewUUID != nil {
		uuid = o.NewUUID()
	} else {
		uuid = NewUUID()
	}
	// As Wolf's own create_default: the uuid line, then the default file.
	text := fmt.Sprintf("# A unique identifier for this host\nuuid = %q\n", uuid) + string(DefaultConfig())
	m := map[string]any{}
	if _, err := toml.Decode(text, &m); err != nil {
		return nil, fmt.Errorf("Wolf's default config: %w", err)
	}
	return m, nil
}

// NewUUID is a random (version 4) UUID.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func isOurs(app map[string]any) bool {
	if app["title"] == AppTitle {
		return true
	}
	if r, ok := app["runner"].(map[string]any); ok && r["name"] == RunnerName {
		return true
	}
	return false
}

// tables reads an array of tables, however the decoder typed it.
func tables(v any) []map[string]any {
	switch t := v.(type) {
	case []map[string]any:
		return t
	case []any:
		var out []map[string]any
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func asList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []string:
		return toAny(t)
	}
	return nil
}

// PairedClientCerts lists the client certificates in a config (tests and
// doctor: pairings must survive every merge).
func PairedClientCerts(data []byte) []string {
	var m map[string]any
	if _, err := toml.Decode(string(data), &m); err != nil {
		return nil
	}
	var certs []string
	for _, c := range tables(m["paired_clients"]) {
		if s, ok := c["client_cert"].(string); ok {
			certs = append(certs, s)
		}
	}
	sort.Strings(certs)
	return certs
}
