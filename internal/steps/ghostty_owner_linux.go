//go:build linux

package steps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

type boundedOwnerOutput struct{ bytes.Buffer }

func (b *boundedOwnerOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<20 {
		return 0, errors.New("owner helper output bound exceeded")
	}
	return b.Buffer.Write(p)
}
func ghosttyOwnerCommand(target plan.Target, data []byte, argv []string) (string, error) {
	switch t := target.(type) {
	case plan.Local:
	case plan.Incus:
		argv = append([]string{"/usr/bin/incus", "--force-local", "exec", t.Container, "--"}, argv...)
	default:
		// Injectable plan.Target seam used by isolated mock tests only. Real
		// provisioning constructs exclusively Local and Incus parents.
		return target.RunInput(data, argv...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	var out boundedOwnerOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.Stdin = bytes.NewReader(data)
	if err := cmd.Run(); err != nil {
		return "", errors.New("owner helper failed")
	}
	return out.String(), nil
}
