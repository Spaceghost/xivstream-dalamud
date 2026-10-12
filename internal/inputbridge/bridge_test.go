//go:build linux

package inputbridge

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A device may exist before the host's ownership rule has completed. Sending
// its add event anyway makes libinput abandon it; it must remain retryable.
func TestUnownedDeviceRemainsRetryable(t *testing.T) {
	dir := t.TempDir()
	inputDir, sysDir := filepath.Join(dir, "dev/input"), filepath.Join(dir, "sys")
	deviceDir := filepath.Join(sysDir, "class/input/event99")
	for _, p := range []string{inputDir, filepath.Join(deviceDir, "device")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, data := range map[string]string{
		filepath.Join(inputDir, "event99"):      "",
		filepath.Join(deviceDir, "device/name"): "libvirtualhid Mouse",
		filepath.Join(deviceDir, "uevent"):      "MAJOR=13\nMINOR=163\nDEVNAME=input/event99\n",
	} {
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b := &Bridge{InputDir: inputDir, SysDir: sysDir, UdevData: filepath.Join(dir, "udev"), Marks: Marks, inputGID: os.Getgid() + 1, nl: -1, known: map[string]map[string]string{}}
	b.added("event99", false)
	if b.known["event99"] != nil {
		t.Fatal("unreadable device was marked announced, preventing ownership-change retry")
	}
	// Ownership becoming correct is sufficient to release the permission gate.
	b.inputGID = os.Getgid()
	if !b.waitUntilOurs("event99") {
		t.Fatal("owned device should be ready")
	}
	if b.waitUntilOurs("event100") {
		t.Fatal("a vanished device must not be announced")
	}
}

func TestMessage(t *testing.T) {
	props := map[string]string{"DEVPATH": "/devices/virtual/input/input9/event5", "MAJOR": "13", "MINOR": "69",
		"DEVNAME": "input/event5", "SUBSYSTEM": "input", "NAME": "libvirtualhid Mouse"}
	msg := Message("add", props, "4242")
	if got := binary.NativeEndian.Uint32(msg); int(got) != len(msg) {
		t.Fatalf("length %d, message %d", got, len(msg))
	}
	if typ := binary.NativeEndian.Uint16(msg[4:]); typ != 0x10 {
		t.Fatalf("type %#x, want NLMSG_MIN_TYPE", typ)
	}
	body := string(msg[16:])
	for _, want := range []string{"add@/devices/virtual/input/input9/event5\x00", "ACTION=add\x00", "MAJOR=13\x00", "SEQNUM=4242\x00"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q: %q", want, body)
		}
	}
	if strings.Contains(body, "NAME=libvirtualhid") {
		t.Error("NAME is ours, not a uevent property")
	}
}

func TestMatches(t *testing.T) {
	b := &Bridge{Marks: Marks}
	for name, want := range map[string]bool{
		"libvirtualhid Keyboard": true, "Sunshine (libvirtualhid) X-Box 360 Controller": true,
		"AT Translated Set 2 keyboard": false, "Sony Interactive Entertainment DualSense Wireless Controller": false,
	} {
		if got := b.matches(name); got != want {
			t.Errorf("matches(%q) = %v", name, got)
		}
	}
}

func TestStreamedPad(t *testing.T) {
	for path, want := range map[string]bool{
		"/sys/devices/virtual/misc/uhid/0003:054C:0CE6.0004/hidraw/hidraw3":                true,
		"/sys/devices/virtual/misc/uhid/0003:054c:09cc.0007/input/input30/event12":         true,
		"/sys/devices/virtual/misc/uhid/0005:046D:B023.0002/hidraw/hidraw4":                false, // a BLE mouse through BlueZ
		"/sys/devices/pci0000:00/0000:00:14.0/usb1/1-3/1-3:1.3/0003:054C:0CE6.0001/hidraw": false, // a real DualSense on USB
	} {
		if got := StreamedPad(path); got != want {
			t.Errorf("StreamedPad(%q) = %v", path, got)
		}
	}
}
