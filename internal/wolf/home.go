package wolf

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// IncusPool is where Incus mounts its "default" storage pool.
const IncusPool = "/var/lib/incus/storage-pools/default"

// DeviceFor turns [wolf] home_device ("UUID=..." or a /dev path) into what
// the mount unit mounts. "" asks findmnt for the filesystem holding the
// Incus pool (so the old guest's home can be reflink-copied into it).
func DeviceFor(run Runner, configured string) (string, error) {
	switch {
	case strings.HasPrefix(configured, "UUID="):
		return "/dev/disk/by-uuid/" + strings.TrimPrefix(configured, "UUID="), nil
	case configured != "":
		return configured, nil
	}
	out, err := run("findmnt", "-n", "-o", "UUID,FSTYPE", "-T", IncusPool)
	f := strings.Fields(out)
	if err != nil || len(f) != 2 || f[1] != "btrfs" {
		return "", fmt.Errorf("wolf.home_device is empty and %s is not a mounted btrfs pool to put the home in; set home_device = \"UUID=<btrfs filesystem uuid>\"", IncusPool)
	}
	return "/dev/disk/by-uuid/" + f[0], nil
}

// uuidOf is the filesystem UUID in a /dev/disk/by-uuid path, or "".
func uuidOf(device string) string {
	if strings.HasPrefix(device, "/dev/disk/by-uuid/") {
		return strings.TrimPrefix(device, "/dev/disk/by-uuid/")
	}
	return ""
}

// topMount finds where device's top-level subvolume is already mounted.
func topMount(run Runner, device string) string {
	src := device
	if u := uuidOf(device); u != "" {
		src = "UUID=" + u
	}
	out, err := run("findmnt", "-rn", "-S", src, "-o", "TARGET,FSROOT")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "/" {
			return f[0]
		}
	}
	return ""
}

// SubvolumeExists reports whether device has the subvolume, when that can be
// told without mounting (the top level is mounted somewhere).
func SubvolumeExists(run Runner, device, subvol string) (bool, error) {
	top := topMount(run, device)
	if top == "" {
		return false, fmt.Errorf("the filesystem's top level is not mounted; apply mounts it to look")
	}
	_, err := run("btrfs", "subvolume", "show", path.Join(top, subvol))
	return err == nil, nil
}

// CreateSubvolume creates device's top-level subvolume subvol if missing,
// mounting the filesystem's top level for a moment when it is not mounted.
func CreateSubvolume(run Runner, device, subvol string) error {
	top := topMount(run, device)
	if top == "" {
		dir, err := os.MkdirTemp("", "xivstream-btrfs-")
		if err != nil {
			return err
		}
		defer os.Remove(dir)
		if _, err := run("mount", "-t", "btrfs", "-o", "subvolid=5", device, dir); err != nil {
			return err
		}
		defer func() { _, _ = run("umount", dir) }()
		top = dir
	}
	path := path.Join(top, subvol)
	if _, err := run("btrfs", "subvolume", "show", path); err == nil {
		return nil
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s exists and is not a btrfs subvolume", path)
	}
	if _, err := run("btrfs", "subvolume", "create", path); err != nil {
		return err
	}
	// The player (uid 1000 in the session) owns the home; nothing else reads it.
	if err := os.Chown(path, 1000, 1000); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

// HomeMounted: the home's mount point has the subvolume mounted.
func HomeMounted(run Runner, home, subvol string) bool {
	out, err := run("findmnt", "-n", "-o", "FSROOT", "--mountpoint", home)
	return err == nil && strings.TrimSpace(out) == "/"+subvol
}
