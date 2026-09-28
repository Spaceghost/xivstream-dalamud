package steps

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/config"
	"github.com/Spaceghost/xivstream-dalamud/internal/detect"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
	"github.com/Spaceghost/xivstream-dalamud/internal/sys"
)

// CPUOnly installs the CPU configuration/service without touching the game or
// streaming session. The binary must already be installed at its normal path.
func CPUOnly(c config.Config, init string) ([]plan.Step, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Topology != config.TopologyIncus || init != "systemd" {
		return nil, fmt.Errorf("CPU-only apply requires an Incus host with systemd")
	}
	data, err := c.Encode()
	if err != nil {
		return nil, err
	}
	s := []plan.Step{plan.File(plan.Local{}, "/etc/xivstream/config.toml", data, 0o600, "", "Save xivstream configuration", "")}
	b := builder{c: c, f: detect.Facts{Init: init}}
	return append(s, b.cpuPolicySteps()...), nil
}

func (b *builder) cpuPolicySteps() []plan.Step {
	host := plan.Local{}
	const unit = "xivstream-cpu-policy.service"
	if !b.c.Incus.CPUPolicy.Enabled() {
		return []plan.Step{step(host, "Disable dynamic CPU limits if previously installed", "Restore the configured static incus.cpu limit.",
			[]string{"systemctl disable --now " + unit},
			func() (bool, error) {
				_, enabled := sys.Output("systemctl", "is-enabled", "--quiet", unit)
				_, active := sys.Output("systemctl", "is-active", "--quiet", unit)
				return enabled != nil && active != nil, nil
			}, func() error {
				if _, err := sys.Output("systemctl", "disable", "--now", unit); err != nil {
					return err
				}
				_, err := sys.Output("incus", "--force-local", "config", "set", b.c.Incus.Container, "limits.cpu="+b.c.Incus.CPU)
				return err
			})}
	}
	content := string(render("cpu-policy.service", b.view(false)))
	if exe, err := os.Executable(); err == nil && exe == "/usr/bin/xivstream" {
		content = strings.ReplaceAll(content, "/usr/local/bin/xivstream", "/usr/bin/xivstream")
	}
	// Changing policy values forces a reload/restart even when the caller saved
	// directly to the configuration the service reads.
	settings, _ := json.Marshal(struct {
		Container string
		Policy    config.CPUPolicy
	}{b.c.Incus.Container, b.c.Incus.CPUPolicy})
	content += fmt.Sprintf("\n# CPU policy configuration: %x\n", sha256.Sum256(settings))
	file := plan.File(host, "/etc/systemd/system/"+unit, []byte(content), 0o644, "", "Write the dynamic CPU policy service", "")
	start := step(host, "Enable the CPU policy", "", []string{"systemctl enable --now " + unit},
		func() (bool, error) {
			_, enabled := sys.Output("systemctl", "is-enabled", "--quiet", unit)
			_, active := sys.Output("systemctl", "is-active", "--quiet", unit)
			return enabled == nil && active == nil, nil
		}, func() error {
			if _, err := sys.Output("systemctl", "daemon-reload"); err != nil {
				return err
			}
			_, err := sys.Output("systemctl", "enable", "--now", unit)
			return err
		})
	restart := step(host, "Reload the CPU policy after configuration changes", "", []string{"systemctl daemon-reload", "systemctl restart " + unit}, nil,
		func() error {
			if _, err := sys.Output("systemctl", "daemon-reload"); err != nil {
				return err
			}
			_, err := sys.Output("systemctl", "restart", unit)
			return err
		})
	restart.IfChanged = true
	return []plan.Step{file, start, restart}
}
