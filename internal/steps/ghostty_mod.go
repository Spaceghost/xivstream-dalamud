package steps

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Spaceghost/xivstream-dalamud/internal/mods"
	"github.com/Spaceghost/xivstream-dalamud/internal/ownerfile"
	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

func (o *ghosttyOwnerTarget) bundleInstalled(version string) (bool, error) {
	if !mods.ValidGhosttyVersion(version) {
		return false, errors.New("invalid Ghostty mod version")
	}
	raw, err := o.call(nil, "inspect-ghostty-bundle", version)
	if err != nil {
		return false, err
	}
	var metadata ownerfile.Metadata
	if json.Unmarshal([]byte(raw), &metadata) != nil {
		return false, errors.New("invalid Ghostty bundle metadata")
	}
	return metadata.Exists, nil
}
func (o *ghosttyOwnerTarget) installBundle(version string, tree mods.Tree) error {
	if !mods.ValidGhosttyVersion(version) {
		return errors.New("invalid Ghostty mod version")
	}
	raw, err := o.call(tree.Tar(), "install-ghostty-bundle", version)
	if err != nil {
		return fmt.Errorf("Ghostty bundle %s was not confirmed installed; private failed staging may remain under %s/.xlcore/installedPlugins/GhosttyDalamud/.xivstream-bundle-*: %w", version, o.home, err)
	}
	if strings.TrimSpace(raw) != "ok" {
		return errors.New("invalid Ghostty bundle acknowledgement")
	}
	return nil
}
func ghosttyModStep(t plan.Target, files *ghosttyOwnerTarget, p mods.Plugin, version, url string, testing bool, notRunning func() error) plan.Step {
	return step(t, "Install the GhosttyDalamud mod "+version, p.Punchline, []string{"download bounded Ghostty zip and atomically publish owner-private version directory"},
		func() (bool, error) { return files.bundleInstalled(version) },
		func() error {
			if err := notRunning(); err != nil {
				return err
			}
			present, err := files.bundleInstalled(version)
			if err != nil || present {
				return err
			}
			data, err := mods.DownloadGhostty(url)
			if err != nil {
				return err
			}
			tree, err := mods.UnpackGhostty(data, p, version, testing)
			if err != nil {
				return err
			}
			return files.installBundle(version, tree)
		})
}
