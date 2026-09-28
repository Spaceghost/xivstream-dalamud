package cpupolicy

import (
	"fmt"
	"strconv"
	"strings"
)

// ValidateSelection checks an Incus CPU count or explicit list against online
// host CPU IDs. A bare number is a count; N-N selects one specific CPU.
func ValidateSelection(selection, online string) error {
	available, err := ids(online)
	if err != nil {
		return err
	}
	if !strings.ContainsAny(selection, ",-") {
		n, err := strconv.Atoi(selection)
		if err != nil || n <= 0 || n > len(available) {
			return fmt.Errorf("CPU count %q exceeds available CPUs or is invalid", selection)
		}
		return nil
	}
	chosen, err := ids(selection)
	if err != nil {
		return err
	}
	for id := range chosen {
		if !available[id] {
			return fmt.Errorf("CPU %d is not online (online: %s)", id, online)
		}
	}
	return nil
}

func ids(value string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(value, ",") {
		lo, hi, ranged := strings.Cut(part, "-")
		first, err := strconv.Atoi(lo)
		if err != nil || first < 0 {
			return nil, fmt.Errorf("invalid CPU list %q", value)
		}
		last := first
		if ranged {
			last, err = strconv.Atoi(hi)
		}
		if err != nil || last < first || last > 1048576 {
			return nil, fmt.Errorf("invalid CPU range %q", part)
		}
		for id := first; id <= last; id++ {
			out[id] = true
		}
	}
	return out, nil
}
