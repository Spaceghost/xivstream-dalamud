//go:build !linux

package steps

import (
	"errors"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

func ghosttyOwnerCommand(plan.Target, []byte, []string) (string, error) {
	return "", errors.New("Ghostty owner helper requires Linux")
}
