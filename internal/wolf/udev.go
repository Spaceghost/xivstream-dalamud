package wolf

import (
	"bytes"
	"strings"
)

// The Sunshine rule (70-xivstream-<container>.rules) hands every uhid device
// with Sony's vendor id to the Incus container. Wolf's emulated DualSense is
// one too, so the rule skips Wolf's devices, matched as Wolf's own
// 85-wolf.rules matches them: every device Wolf creates is named
// "Wolf ... (virtual) ..." (input_handler.cpp: "Wolf DualSense (virtual) pad",
// and its "Touchpad" and "Motion Sensors" children); the hidraw node has no
// name of its own and is matched through its parent's HID_NAME. Sunshine's
// pads are named "Wireless Controller" and its keyboard and mice
// "libvirtualhid ...", so its devices still match.

// SunshineGateStart opens the skip; SunshineGateEnd closes it at the file's end.
const (
	SunshineGateLabel = "xivstream_not_wolf_end"
	SunshineGateStart = `# Wolf's virtual devices ("Wolf ... (virtual) ...") are Wolf's, never this container's.
SUBSYSTEMS=="input", ATTRS{name}=="Wolf *virtual*", GOTO="` + SunshineGateLabel + `"
KERNEL=="hidraw*", ATTRS{uevent}=="*HID_NAME=Wolf *virtual*", GOTO="` + SunshineGateLabel + `"
`
	SunshineGateEnd = `LABEL="` + SunshineGateLabel + `"
`
)

// GateSunshineRule adds the skip to a Sunshine rule written before it existed:
// the gate goes before the first rule (after the header comments), the label
// at the end. A rule that has it is returned unchanged.
func GateSunshineRule(rule []byte) []byte {
	if bytes.Contains(rule, []byte(`LABEL="`+SunshineGateLabel+`"`)) {
		return rule
	}
	lines := strings.SplitAfter(string(rule), "\n")
	var out strings.Builder
	gated := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if !gated && t != "" && !strings.HasPrefix(t, "#") {
			out.WriteString(SunshineGateStart)
			out.WriteString("\n")
			gated = true
		}
		out.WriteString(line)
	}
	s := out.String()
	if !gated {
		return rule // no rules: nothing to skip
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(s + "\n" + SunshineGateEnd)
}
