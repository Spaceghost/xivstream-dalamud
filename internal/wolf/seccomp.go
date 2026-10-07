package wolf

import (
	"encoding/json"
	"fmt"
)

// SessionSeccompFile is read by the host's Podman API, not inside Wolf.
const SessionSeccompFile = "/etc/xivstream/wolf-session-seccomp.json"

// SessionSeccomp derives the session policy from Podman's configured policy.
// Fedora denies futex_waitv with EPERM, which breaks Wine's fsync. Change only
// that syscall; keep all other rules, architecture mappings and conditions.
func SessionSeccomp(base []byte) ([]byte, error) {
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(base, &profile); err != nil {
		return nil, fmt.Errorf("Podman seccomp profile: %w", err)
	}
	var action string
	if err := json.Unmarshal(profile["defaultAction"], &action); err != nil {
		return nil, fmt.Errorf("Podman seccomp profile has no defaultAction")
	}
	switch action {
	case "SCMP_ACT_ERRNO", "SCMP_ACT_TRAP", "SCMP_ACT_KILL", "SCMP_ACT_KILL_THREAD", "SCMP_ACT_KILL_PROCESS":
	default:
		return nil, fmt.Errorf("Podman seccomp defaultAction %q is not restrictive", action)
	}
	var rules []map[string]json.RawMessage
	if err := json.Unmarshal(profile["syscalls"], &rules); err != nil || rules == nil {
		return nil, fmt.Errorf("Podman seccomp profile has no valid syscall rules")
	}
	kept := make([]map[string]json.RawMessage, 0, len(rules)+1)
	for i, rule := range rules {
		var names []string
		var ruleAction string
		if err := json.Unmarshal(rule["names"], &names); err != nil || len(names) == 0 {
			return nil, fmt.Errorf("Podman seccomp rule %d has no syscall names", i)
		}
		if err := json.Unmarshal(rule["action"], &ruleAction); err != nil || ruleAction == "" {
			return nil, fmt.Errorf("Podman seccomp rule %d has no action", i)
		}
		filtered := make([]string, 0, len(names))
		for _, name := range names {
			if name != "futex_waitv" {
				filtered = append(filtered, name)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		if len(filtered) != len(names) {
			rule["names"], _ = json.Marshal(filtered)
		}
		kept = append(kept, rule)
	}
	kept = append(kept, map[string]json.RawMessage{
		"names": json.RawMessage(`["futex_waitv"]`), "action": json.RawMessage(`"SCMP_ACT_ALLOW"`),
	})
	profile["syscalls"], _ = json.Marshal(kept)
	data, err := json.MarshalIndent(profile, "", "  ")
	return append(data, '\n'), err
}
