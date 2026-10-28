package demo

// `grain demo -upgrade-image=<repo>` (grain/task-2074): the Upgrade tab
// over a real image-mode upgrade.Upgrader, so its picker and its steps
// can be walked without a deployment to upgrade.
//
// Without the flag the demo wires no upgrader at all, and the tab says
// "not available" -- which is the default and stays one, since a demo is
// a throwaway UI and a real upgrader pulls and runs images. With it,
// everything below the Upgrade button is the daemon's own image path
// (newUpgrader's -upgrade-image case): GET /api/upgrade/targets lists the
// registry's real tags, and starting an upgrade really pulls the tag,
// really checks its signature with -upgrade-verify-cmd, and really runs
// the pulled image's health check. What it leaves out is what only a
// deployment has:
//
//   - the restart. RestartCmd is nil, so a successful upgrade rewrites
//     the demo's own ref file and stops there; nothing replaces this
//     process.
//   - the schema step. There is no build of record to carry the demo's
//     rows into, and the store is thrown away on exit anyway.
//   - the sandbox image and unit-flag checks, which are about a host's
//     granule guest and its systemd unit, neither of which a demo has.

import (
	"fmt"
	"path/filepath"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// demoUpgradeRefFile is the demo's stand-in for scripts/setup.sh's
// EnvironmentFile: the file a successful upgrade rewrites with the image
// the service should run. It lives in the demo's own temp directory, so
// a walkthrough can read what an upgrade would have cut over to.
const demoUpgradeRefFile = "grain-image.env"

// configureUpgradeDemo wires an image-mode upgrader pulling from image
// into cfg. allowed is -upgrade-allowed-branches already split; verify is
// -upgrade-verify-cmd, empty for no signature check.
func configureUpgradeDemo(dir string, store *model.Store, cfg ui.Config, image string, allowed, verify []string) (ui.Config, error) {
	if image == "" {
		return cfg, fmt.Errorf("-upgrade-image names no repository")
	}
	u := upgrade.New(upgrade.Config{
		Image: &upgrade.ImageConfig{
			Repository: image,
			RefFile:    filepath.Join(dir, demoUpgradeRefFile),
			VerifyCmd:  verify,
		},
		HealthCheckArgs:  []string{"schema-version"},
		ReleaseNotesArgs: []string{"release-notes", "--json"},
		// The demo's queue holds while an upgrade runs, as a
		// deployment's does, and opens again when it ends: with no
		// RestartCmd every ending is one this process survives.
		Hold:            orchestrator.UpgradeHold(store),
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
		AllowedBranches: allowed,
	})
	cfg.Upgrader = u
	return cfg, nil
}
