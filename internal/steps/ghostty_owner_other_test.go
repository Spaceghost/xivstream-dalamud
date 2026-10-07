//go:build !linux

package steps

import (
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
	"testing"
)

func TestGhosttyOwnerCommandRefusesUnsupportedPlatform(t *testing.T) {
	out, err := ghosttyOwnerCommand(plan.Local{}, []byte("synthetic-private-token-12345"), []string{"/must/never/execute"})
	if err == nil || out != "" {
		t.Fatal("non-Linux owner helper must fail closed")
	}
}
