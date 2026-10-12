// Package inputbridge announces the streaming server's virtual input devices
// to a container's udev (`xivstream input-bridge`, run inside the guest).
//
// Sunshine creates the Moonlight client's keyboard, mice and gamepads through
// /dev/uinput and /dev/uhid. The kernel puts their event nodes in the host's
// /dev/input, which is bind-mounted into the container, but it sends the
// uevents only to the host. Without an "add" event the container's udevd never
// records the devices, and libinput (sway) only uses devices udev has
// recorded, so the stream would have no input.
//
// This watches /dev/input and, for the server's devices, injects the add or
// remove uevent into the container's network namespace, which the kernel then
// re-broadcasts as its own. That is the mechanism Incus uses for hotplugged
// devices (unicast to the kernel on NETLINK_KOBJECT_UEVENT; needs
// CAP_SYS_ADMIN in the container). Incus's own unix-hotplug device is not used
// because in Incus 6.23 it deadlocks incusd when a container stops while a
// matched device is being removed.
//
// An emulated DualSense (gamepad = "ds5") is a uhid device with Sony's ids,
// and Wine reads PlayStation pads through hidraw. hidraw nodes sit in /dev
// itself, so the host's udev rule copies the streamed pad's node into a
// directory of its own, bind-mounted here at HidrawDir; this links each into
// /dev under its own name and announces it like the event nodes.
package inputbridge

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Marks are substrings of the device names the streaming servers give their
// virtual devices: Sunshine's "libvirtualhid Keyboard" and "Sunshine
// (libvirtualhid) X-Box 360 Controller"; Selkies' and Wolf's "Wolf ... virtual"
// style names. Nothing else is announced, so the container never sees the
// host's own keyboards.
var Marks = []string{"libvirtualhid", "Selkies", "Wolf"}

// HidrawDir is where the host's rule puts the streamed pads' hidraw nodes.
const HidrawDir = "/dev/hidraw-stream"

// StreamedPad says whether a device's sysfs path is a pad the streaming server
// emulates through uhid with Sony's vendor id (a DualSense or DualShock 4):
// "/devices/virtual/misc/uhid/0003:054C:0CE6.0004/...". A real pad is on a
// USB or Bluetooth bus, never under uhid with that id (BlueZ's uhid devices
// are Bluetooth LE, which Sony pads are not).
func StreamedPad(sysPath string) bool {
	i := strings.Index(sysPath, "/virtual/misc/uhid/")
	return i >= 0 && strings.Contains(strings.ToUpper(sysPath[i:]), ":054C:")
}

type Bridge struct {
	InputDir  string
	HidrawDir string
	DevDir    string // /dev, where the hidraw links go
	SysDir    string // /sys
	UdevData  string // /run/udev/data
	Marks     []string
	inputGID  int
	nl        int
	known     map[string]map[string]string // event node -> uevent properties
}

func New() (*Bridge, error) {
	b := &Bridge{InputDir: "/dev/input", HidrawDir: HidrawDir, DevDir: "/dev", SysDir: "/sys", UdevData: "/run/udev/data", Marks: Marks, known: map[string]map[string]string{}}
	g, err := user.LookupGroup("input")
	if err != nil {
		return nil, fmt.Errorf("group input: %w", err)
	}
	b.inputGID, _ = strconv.Atoi(g.Gid)
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("netlink bind: %w", err)
	}
	b.nl = fd
	return b, nil
}

func (b *Bridge) Run() error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("inotify: %w", err)
	}
	inputWD, err := unix.InotifyAddWatch(fd, b.InputDir, unix.IN_CREATE|unix.IN_DELETE|unix.IN_ATTRIB)
	if err != nil {
		return fmt.Errorf("inotify on %s: %w", b.InputDir, err)
	}
	hidWD := -1
	if st, err := os.Stat(b.HidrawDir); err == nil && st.IsDir() { // only with a DualSense-emulating stream
		if hidWD, err = unix.InotifyAddWatch(fd, b.HidrawDir, unix.IN_CREATE|unix.IN_DELETE|unix.IN_ATTRIB); err != nil {
			return fmt.Errorf("inotify on %s: %w", b.HidrawDir, err)
		}
	}
	for _, dir := range []string{b.InputDir, b.HidrawDir} { // devices that predate us
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			if dir == b.InputDir {
				b.added(n, true)
			} else if hidWD >= 0 {
				b.hidrawAdded(n, true)
			}
		}
	}
	buf := make([]byte, 64*1024)
	for {
		n, err := unix.Read(fd, buf)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return fmt.Errorf("inotify read: %w", err)
		}
		for i := 0; i+unix.SizeofInotifyEvent <= n; {
			ev := (*unix.InotifyEvent)(unsafePointer(&buf[i]))
			nameBytes := buf[i+unix.SizeofInotifyEvent : i+unix.SizeofInotifyEvent+int(ev.Len)]
			name := strings.TrimRight(string(nameBytes), "\x00")
			i += unix.SizeofInotifyEvent + int(ev.Len)
			switch {
			case int(ev.Wd) == hidWD && ev.Mask&(unix.IN_CREATE|unix.IN_ATTRIB) != 0:
				b.hidrawAdded(name, false) // ATTRIB: the host's chown landed
			case int(ev.Wd) == hidWD && ev.Mask&unix.IN_DELETE != 0:
				b.hidrawRemoved(name)
			case int(ev.Wd) == inputWD && ev.Mask&(unix.IN_CREATE|unix.IN_ATTRIB) != 0:
				b.added(name, false)
			case int(ev.Wd) == inputWD && ev.Mask&unix.IN_DELETE != 0:
				b.removed(name)
			}
		}
	}
}

// describe returns the uevent properties of a streamed event node, or nil.
func (b *Bridge) describe(node string) map[string]string {
	sysdir := filepath.Join(b.SysDir, "class", "input", node)
	var name, uevent string
	for i := 0; ; i++ { // the node can appear a moment before sysfs settles
		n, err1 := os.ReadFile(filepath.Join(sysdir, "device", "name"))
		u, err2 := os.ReadFile(filepath.Join(sysdir, "uevent"))
		if err1 == nil && err2 == nil {
			name, uevent = strings.TrimSpace(string(n)), string(u)
			break
		}
		if i >= 50 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	real, err := filepath.EvalSymlinks(sysdir)
	if err != nil {
		return nil
	}
	if !b.matches(name) && !StreamedPad(real) {
		return nil
	}
	props := map[string]string{}
	for _, line := range strings.Fields(uevent) {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	props["DEVPATH"] = strings.TrimPrefix(real, b.SysDir)
	props["SUBSYSTEM"] = "input"
	props["NAME"] = name
	return props
}

func (b *Bridge) matches(name string) bool {
	for _, m := range b.Marks {
		if strings.Contains(name, m) {
			return true
		}
	}
	return false
}

func (b *Bridge) added(node string, startup bool) {
	if !strings.HasPrefix(node, "event") || b.known[node] != nil {
		return
	}
	props := b.describe(node)
	if props == nil {
		return
	}
	// After a restart of this service, devices announced by the previous run
	// are already in udev (and open in libinput); a second "add" would give
	// sway duplicate keyboards and mice.
	if startup && os.Getenv("XIVSTREAM_REANNOUNCE") == "" {
		if _, err := os.Stat(filepath.Join(b.UdevData, "c"+props["MAJOR"]+":"+props["MINOR"])); err == nil {
			b.known[node] = props
			return
		}
	}
	if !b.waitUntilOurs(node) {
		return // A later ownership change retries this node through IN_ATTRIB.
	}
	if err := b.inject("add", props); err != nil {
		log.Printf("add %s: %v", props["DEVNAME"], err)
		return
	}
	b.known[node] = props
}

func (b *Bridge) removed(node string) {
	props := b.known[node]
	if props == nil {
		return
	}
	delete(b.known, node)
	if err := b.inject("remove", props); err != nil {
		log.Printf("remove %s: %v", props["DEVNAME"], err)
	}
}

// hidrawAdded: a streamed pad's hidraw node arrived in HidrawDir (or its
// owner changed). Once it is ours, /dev/<node> links to it and udev hears of
// it, with the HID name as NAME for the log.
func (b *Bridge) hidrawAdded(node string, startup bool) {
	key := "hidraw:" + node
	if !strings.HasPrefix(node, "hidraw") || b.known[key] != nil {
		return
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(b.HidrawDir, node), &st); err != nil || int(st.Gid) != b.inputGID {
		return // not handed over yet: the ATTRIB of its chown brings it back here
	}
	sysdir := filepath.Join(b.SysDir, "class", "hidraw", node)
	real, err := filepath.EvalSymlinks(sysdir)
	if err != nil || !StreamedPad(real) {
		return
	}
	props := map[string]string{}
	for _, line := range strings.Fields(readFile(filepath.Join(sysdir, "uevent"))) {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	props["DEVPATH"] = strings.TrimPrefix(real, b.SysDir)
	props["SUBSYSTEM"] = "hidraw"
	props["DEVNAME"] = node
	for _, line := range strings.Split(readFile(filepath.Join(sysdir, "device", "uevent")), "\n") {
		if v, ok := strings.CutPrefix(line, "HID_NAME="); ok {
			props["NAME"] = v
		}
	}
	link := filepath.Join(b.DevDir, node)
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		log.Printf("warning: %s exists and is not ours; %s not linked", link, node)
		return
	}
	_ = os.Remove(link)
	if err := os.Symlink(filepath.Join(b.HidrawDir, node), link); err != nil {
		log.Printf("link %s: %v", link, err)
		return
	}
	b.known[key] = props
	if startup && os.Getenv("XIVSTREAM_REANNOUNCE") == "" {
		if _, err := os.Stat(filepath.Join(b.UdevData, "c"+props["MAJOR"]+":"+props["MINOR"])); err == nil {
			return
		}
	}
	if err := b.inject("add", props); err != nil {
		log.Printf("add %s: %v", node, err)
	}
}

func (b *Bridge) hidrawRemoved(node string) {
	key := "hidraw:" + node
	props := b.known[key]
	if props == nil {
		return
	}
	delete(b.known, key)
	link := filepath.Join(b.DevDir, node)
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(link)
	}
	if err := b.inject("remove", props); err != nil {
		log.Printf("remove %s: %v", node, err)
	}
}

// waitUntilOurs: the host's udev hands each node to the container with a chown
// that lands a little after the node appears. Announcing before it would have
// libinput try the node while it is still unreadable here, and give up.
func (b *Bridge) waitUntilOurs(node string) bool {
	path := filepath.Join(b.InputDir, node)
	for i := 0; i < 250; i++ {
		var st unix.Stat_t
		if err := unix.Stat(path, &st); err != nil {
			return false
		}
		if int(st.Gid) == b.inputGID {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	log.Printf("warning: %s never became group input; is the host's xivstream udev rule installed?", path)
	return false
}

// Message builds the netlink message the kernel re-broadcasts as a uevent.
// The kernel runs injected messages through netlink_rcv_skb, which silently
// skips anything without a request header of at least NLMSG_MIN_TYPE; asking
// for an ACK is how a refusal (e.g. missing CAP_SYS_ADMIN) becomes visible.
func Message(action string, props map[string]string, seqnum string) []byte {
	keys := make([]string, 0, len(props))
	for k := range props {
		if k != "NAME" && k != "ACTION" && k != "SEQNUM" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var body strings.Builder
	body.WriteString(action + "@" + props["DEVPATH"] + "\x00")
	body.WriteString("ACTION=" + action + "\x00")
	for _, k := range keys {
		body.WriteString(k + "=" + props[k] + "\x00")
	}
	body.WriteString("SEQNUM=" + seqnum + "\x00")
	payload := []byte(body.String())
	msg := make([]byte, 16+len(payload))
	binary.NativeEndian.PutUint32(msg[0:], uint32(len(msg)))
	binary.NativeEndian.PutUint16(msg[4:], 0x10)                              // NLMSG_MIN_TYPE
	binary.NativeEndian.PutUint16(msg[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK) // flags
	copy(msg[16:], payload)
	return msg
}

func (b *Bridge) inject(action string, props map[string]string) error {
	seq := strings.TrimSpace(readFile(filepath.Join(b.SysDir, "kernel", "uevent_seqnum")))
	if err := unix.Sendto(b.nl, Message(action, props, seq), 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	ack := make([]byte, 4096)
	n, _, err := unix.Recvfrom(b.nl, ack, 0)
	if err != nil {
		return err
	}
	if n >= 20 {
		if code := int32(binary.NativeEndian.Uint32(ack[16:])); code != 0 {
			return fmt.Errorf("kernel refused the uevent: %w", syscall.Errno(-code))
		}
	}
	log.Printf("%s %s (%s)", action, props["DEVNAME"], props["NAME"])
	return nil
}

func readFile(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}
