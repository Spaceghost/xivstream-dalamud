package wolf

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DriverVersionFile is where the loaded NVIDIA kernel module says its version.
const DriverVersionFile = "/sys/module/nvidia/version"

// driverFiles must be in the volume for driver version v: the encoder and
// CUDA libraries Wolf's GStreamer loads, and the Vulkan manifest the
// session's entrypoint copies.
func driverFiles(v string) []string {
	return []string{"lib/libcuda.so." + v, "lib/libnvidia-encode.so." + v, "share/vulkan/icd.d/nvidia_icd.json"}
}

// DriverVolumeCurrent reports whether the volume holds driver version v's
// userspace (the version is in the library file names).
func DriverVolumeCurrent(run Runner, v string, stat func(string) error) (bool, error) {
	if v == "" {
		return false, fmt.Errorf("no NVIDIA driver version")
	}
	mnt, err := run("podman", "volume", "inspect", DriverVolume, "--format", "{{.Mountpoint}}")
	if err != nil || mnt == "" {
		return false, nil // no volume yet
	}
	for _, f := range driverFiles(v) {
		if stat(filepath.Join(mnt, f)) != nil {
			return false, nil
		}
	}
	return true, nil
}

// BuildDriverVolume makes the volume match driver version v, the way Wolf's
// docs do it by hand: build GoW's nvidia-driver image for v (kept, so a
// rebuild for the same version needs no download), then let Podman copy its
// /usr/nvidia into a fresh volume through a throwaway container.
//
// The image's final stage is FROM scratch with a dynamically linked /bin/sh
// and no libc, so starting that container fails after Podman has populated
// the volume: its exit status means nothing, the volume's contents are
// checked instead, and the container is always removed. A volume Podman has
// populated is never refreshed in place, so an old one is removed first,
// with every container that still references it (none may be running).
func BuildDriverVolume(run Runner, v string, stat func(string) error, log func(string)) error {
	if ok, err := DriverVolumeCurrent(run, v, stat); err != nil {
		return err
	} else if ok {
		log(DriverVolume + " already holds NVIDIA " + v)
		return nil
	}
	image := DriverImage + ":" + v
	if !ImageExists(run, image) {
		dir, err := os.MkdirTemp("", "xivstream-nvidia-driver-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), DriverDockerfile(), 0o644); err != nil {
			return err
		}
		log("building " + image + " (downloads NVIDIA's " + v + " installer)")
		if _, err := run("podman", "build", "--pull=missing", "-t", image, "--build-arg", "NV_VERSION="+v, "-f", filepath.Join(dir, "Dockerfile"), dir); err != nil {
			return fmt.Errorf("building %s: %w", image, err)
		}
	}
	if _, err := run("podman", "volume", "exists", DriverVolume); err == nil {
		users, err := run("podman", "ps", "-a", "--filter", "volume="+DriverVolume, "--format", "{{.Names}} {{.State}}")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(users, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			if len(f) > 1 && f[1] == "running" {
				return fmt.Errorf("%s is running with the old driver volume; stop it (systemctl stop wolf) and run this again", f[0])
			}
			log("removing " + f[0] + ", which references the old driver volume")
			if _, err := run("podman", "rm", "--force", f[0]); err != nil {
				return err
			}
		}
		log("removing the old " + DriverVolume)
		if _, err := run("podman", "volume", "rm", DriverVolume); err != nil {
			return err
		}
	}
	if _, err := run("podman", "volume", "create", "--label", "xivstream.nvidia-version="+v, DriverVolume); err != nil {
		return err
	}
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	name := "xivstream-nvvol-" + hex.EncodeToString(suffix[:])
	if _, err := run("podman", "create", "--name", name, "--mount", "type=volume,source="+DriverVolume+",destination=/usr/nvidia", image, "sh"); err != nil {
		return fmt.Errorf("creating the volume's populating container: %w", err)
	}
	defer func() { _, _ = run("podman", "rm", "--force", name) }()
	_, _ = run("podman", "start", name) // fails at exec (see above); the volume is populated by then
	ok, err := DriverVolumeCurrent(run, v, stat)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s does not hold NVIDIA %s's libraries after populating it (looked for %s)", DriverVolume, v, strings.Join(driverFiles(v), ", "))
	}
	log(DriverVolume + " now holds NVIDIA " + v)
	return nil
}

// DriverVersion reads the loaded kernel module's version.
func DriverVersion(read func(string) ([]byte, error)) (string, error) {
	data, err := read(DriverVersionFile)
	if err != nil {
		return "", fmt.Errorf("the NVIDIA kernel module is not loaded (%s): %w", DriverVersionFile, err)
	}
	return strings.TrimSpace(string(data)), nil
}
