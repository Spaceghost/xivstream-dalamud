package mods

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

const GhosttyBundleLimit = 128 << 20
const GhosttyFileLimit = 64 << 20
const GhosttyEntryLimit = 1024

var ghosttyVersion = regexp.MustCompile(`^[0-9]{1,5}\.[0-9]{1,5}\.[0-9]{1,5}\.[0-9]{1,5}$`)
var ghosttyPath = regexp.MustCompile(`^[A-Za-z0-9_. /-]+$`)

func ValidGhosttyVersion(s string) bool { return ghosttyVersion.MatchString(s) }
func ValidGhosttyPath(s string) bool {
	if len(s) == 0 || len(s) > 256 || !ghosttyPath.MatchString(s) || path.IsAbs(s) || path.Clean(s) != s || strings.HasPrefix(s, "../") {
		return false
	}
	parts := strings.Split(s, "/")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || strings.Trim(part, " .") == "" || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return false
		}
	}
	return true
}

// Wine uses case-insensitive paths. Validate every directory prefix too, not
// only complete member names: A/x and a/y must not publish two Linux trees.
func ValidateGhosttyPaths(files, directories []string) error {
	canonical := map[string]string{}
	isFile := map[string]bool{}
	isDir := map[string]bool{}
	add := func(name string, directory bool) error {
		if !ValidGhosttyPath(name) {
			return errors.New("unsafe Ghostty path")
		}
		parts := strings.Split(name, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			key := strings.ToLower(prefix)
			if old, ok := canonical[key]; ok && old != prefix {
				return errors.New("ambiguous Ghostty path casing")
			}
			canonical[key] = prefix
			if i < len(parts)-1 || directory {
				if isFile[key] {
					return errors.New("Ghostty file/directory collision")
				}
				isDir[key] = true
			} else {
				if isDir[key] || isFile[key] {
					return errors.New("duplicate Ghostty file or directory collision")
				}
				isFile[key] = true
			}
		}
		return nil
	}
	for _, name := range directories {
		if err := add(name, true); err != nil {
			return err
		}
	}
	for _, name := range files {
		if err := add(name, false); err != nil {
			return err
		}
	}
	return nil
}

// DownloadGhostty bounds this privileged download before archive parsing.
// Other plugin installers keep their existing behavior outside this scope.
func DownloadGhostty(url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, errors.New("cannot download Ghostty mod")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("Ghostty mod download failed")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, GhosttyBundleLimit+1))
	if err != nil || len(data) > GhosttyBundleLimit {
		return nil, errors.New("Ghostty mod archive exceeds bound")
	}
	return data, nil
}

func UnpackGhostty(data []byte, p Plugin, version string, testing bool) (Tree, error) {
	if p.InternalName != "GhosttyDalamud" || !ValidGhosttyVersion(version) || len(data) > GhosttyBundleLimit {
		return nil, errors.New("invalid Ghostty bundle identity")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) > GhosttyEntryLimit {
		return nil, errors.New("invalid or oversized Ghostty zip")
	}
	tree := Tree{}
	total := 0
	seen := map[string]bool{}
	var directories []string
	for _, entry := range zr.File {
		name := strings.ReplaceAll(entry.Name, `\`, "/")
		if entry.FileInfo().IsDir() {
			if !ValidGhosttyPath(strings.TrimSuffix(name, "/")) {
				return nil, errors.New("unsafe Ghostty directory")
			}
			directories = append(directories, strings.TrimSuffix(name, "/"))
			continue
		}
		key := strings.ToLower(name)
		if !ValidGhosttyPath(name) || !entry.Mode().IsRegular() || entry.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || seen[key] || entry.UncompressedSize64 > GhosttyFileLimit {
			return nil, errors.New("unsafe Ghostty zip member")
		}
		seen[key] = true
		r, err := entry.Open()
		if err != nil {
			return nil, errors.New("unreadable Ghostty zip member")
		}
		body, readErr := io.ReadAll(io.LimitReader(r, GhosttyFileLimit+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || len(body) > GhosttyFileLimit {
			return nil, errors.New("invalid Ghostty zip member")
		}
		total += len(body)
		if total > GhosttyBundleLimit {
			return nil, errors.New("Ghostty bundle exceeds bound")
		}
		tree[name] = body
	}
	if len(tree["GhosttyDalamud.dll"]) < 2 {
		return nil, errors.New("missing Ghostty managed assembly")
	}
	manifest, err := Manifest(p, version, testing)
	if err != nil {
		return nil, errors.New("invalid Ghostty manifest")
	}
	tree["GhosttyDalamud.json"] = manifest
	names := make([]string, 0, len(tree))
	total = 0
	for name, data := range tree {
		names = append(names, name)
		total += len(data)
	}
	if total > GhosttyBundleLimit || len(tree) > GhosttyEntryLimit {
		return nil, errors.New("installed Ghostty bundle exceeds bound")
	}
	if err := ValidateGhosttyPaths(names, directories); err != nil {
		return nil, err
	}
	return tree, nil
}
