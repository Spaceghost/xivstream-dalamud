// Package session is the Wolf session image's build context, embedded so
// `xivstream apply` can build it without a checkout. `podman build
// images/session` builds the same image by hand.
package session

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sort"
)

//go:embed Containerfile packages.txt nvidia.conf entrypoint.sh session.sh launcher.sh ghostty-agent.sh sway.conf
var Files embed.FS

// XIVLauncherVersion is the XIVLauncher.Core release downloaded when no Incus
// guest provides /opt/xivlauncher (the version that guest runs).
const XIVLauncherVersion = "1.4.0"

// Repository is the local name the image is built under.
const Repository = "localhost/xivstream-session"

// Names lists the context's files, sorted.
func Names() []string {
	entries, _ := fs.ReadDir(Files, ".")
	var names []string
	for _, e := range entries {
		if !e.IsDir() && e.Name() != "embed.go" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// Hash identifies this build context: its files and the launcher version.
// The image is tagged with it, so a changed context is a new image.
func Hash() string {
	h := sha256.New()
	for _, name := range Names() {
		data, _ := Files.ReadFile(name)
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	h.Write([]byte(XIVLauncherVersion))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Image is the tag `xivstream apply` builds.
func Image() string { return Repository + ":" + Hash() }
