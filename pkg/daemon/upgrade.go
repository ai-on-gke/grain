package daemon

import (
	"context"
	"database/sql"
	"log"
	"path/filepath"
	"strings"

	"github.com/bwsalmon/grain/pkg/installtrack"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/upgrade"
)

// upgradeStatus is orchestrator.Config.UpgradeStatus for this
// deployment: the last (or current) upgrade as pkg/upgrade recorded it,
// or nil for a deployment that cannot upgrade itself and so has no
// upgrade to check.
//
// nil rather than a function reporting an empty status, because the two
// are read differently at the far end: a deployment with no upgrader
// files no check at all, where an empty status would just mean an
// upgrade that has not happened yet.
func upgradeStatus(upgrader *upgrade.Upgrader) func() (upgrade.Status, error) {
	if upgrader == nil {
		return nil
	}
	return upgrader.Status
}

// startUpgrade is orchestrator.Config.StartUpgrade: what the reconcile
// loop calls when an upgrade somebody scheduled comes due, or nil for a
// deployment that cannot upgrade itself.
//
// nil rather than a function that would report "upgrading is not
// configured here" on every attempt, for upgradeStatus's own reason: the
// two are read differently at the far end, and a nil here is what makes
// the reconciler leave the queue alone entirely rather than hold it for
// an upgrade that could never be applied.
func startUpgrade(upgrader *upgrade.Upgrader) func(string) error {
	if upgrader == nil {
		return nil
	}
	return upgrader.Start
}

// upgradeUnchanged is orchestrator.Config.UpgradeUnchanged: the cheap
// "is this branch already what we run?" the daily automatic upgrade asks
// before it holds the queue, or nil for a deployment that cannot upgrade
// itself.
func upgradeUnchanged(upgrader *upgrade.Upgrader) func(context.Context, string) (bool, error) {
	if upgrader == nil {
		return nil
	}
	return upgrader.AlreadyRunning
}

// SplitBranchPatterns reads -upgrade-allowed-branches: comma-separated,
// with blanks around and between the commas ignored.
func SplitBranchPatterns(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// newUpgrader builds this deployment's in-place upgrader, or nil when it
// was configured for neither path (-upgrade-image, the container one, or
// -upgrade-src-dir, the build one) -- which is the ordinary case for a
// laptop, for `grain demo`, and for any deployment that has not opted
// into upgrading itself from its own UI.
//
// Called once per process, by run(), which hands the one Upgrader to
// both halves of this daemon: the UI's pane, where an operator presses
// the button, and the reconcile loop, which reads its Status to decide
// whether to check an upgrade that has just landed and calls its Start
// when one somebody scheduled comes due. One instance and not two
// because "one upgrade at a time" is a claim an Upgrader makes in its
// own memory (upgrade.ErrUpgradeInProgress), and two of them would each
// be making it about a different upgrade.
//
// db is this deployment's own database, and it is what the schema step
// exports when the build being upgraded to knows a different schema
// (schemaupgrade.go). A nil db leaves that step configured but unable to
// prepare anything -- so it refuses an upgrade whose rows it cannot
// carry across rather than letting one cut over into a store the new
// build cannot open.
//
// deploymentMode is the mode newUpgrader would build an upgrader for,
// from cfg alone -- what a restart's gap is recorded against
// (upgrade.DeploymentModeName), on a deployment that may have no
// upgrader at all, and before the store it needs is open.
func deploymentMode(cfg Config) upgrade.Mode {
	switch {
	case cfg.K8sSandboxes && cfg.UpgradeImage != "" && cfg.UpgradeImageRefFile == "":
		return upgrade.ModeKube
	case cfg.UpgradeImage != "" && (!KubernetesMode(cfg) || cfg.UpgradeImageRefFile != ""):
		return upgrade.ModeImage
	}
	return upgrade.ModeSource
}

// store is the same deployment's rows at the level the pause lives at,
// and it is what an upgrade holds the queue with while it runs
// (orchestrator.UpgradeHold). Nil takes no hold.
func newUpgrader(cfg Config, db *sql.DB, store *model.Store) *upgrade.Upgrader {
	switch {
	case cfg.K8sSandboxes && cfg.UpgradeImage != "" && cfg.UpgradeImageRefFile == "":
		// The Kubernetes path: no pull, no ref file and no restart
		// command -- health-check the branch's published image by
		// running it as a throwaway pod, then repoint this deployment's
		// own Deployment at it and let the rollout replace this process
		// (pkg/upgrade/kube.go).
		//
		// Ask the candidate build for its same-commit Kubernetes sandbox
		// image and start that in a throwaway pod too. This proves the
		// cluster can pull both artifacts before Recreate stops this pod.
		return upgrade.New(upgrade.Config{
			Kube: &upgrade.KubeConfig{
				Repository:       cfg.UpgradeImage,
				Deployment:       cfg.K8sDeployment,
				Container:        cfg.K8sContainer,
				Namespace:        cfg.K8sNamespace,
				Kubectl:          cfg.K8sKubectl,
				VerifyCmd:        cfg.UpgradeVerifyCmd,
				SandboxImageArgs: []string{"k8s-sandbox-image"},
				// And whether the candidate build would accept the args
				// this Deployment hands it. Those args were applied once
				// and the image in the same spec moves with every
				// upgrade and every rollback, so the drift is the host
				// deployment's exactly, with a CrashLoopBackOff of the
				// single replica in place of a restart loop
				// (grain/task-233, grain/task-244).
				UnitFlags:     cfg.UnitFlags,
				FlagNamesArgs: []string{"daemon", "-flag-names"},
				// And that the PriorityClasses sandbox pods name exist,
				// creating grain's own if not, before the cut-over.
				PriorityClasses: []string{
					cfg.K8sPriorityClasses.Interactive, cfg.K8sPriorityClasses.Agent, cfg.K8sPriorityClasses.CI,
				},
			},
			HealthCheckArgs: []string{"schema-version"},
			// What the pane previews when a target is chosen: the
			// release notes that build carries, from this one's
			// version on (pkg/cli/releasenotes.go).
			ReleaseNotesArgs: releaseNotesArgs,
			Schema:           kubeUpgradeSchemaConfig(cfg, db),
			Hold:             orchestrator.UpgradeHold(store),
			// Every run in a run host lets go before the Deployment is
			// repointed, inside staging rather than before a restart
			// command (upgrade.Upgrader.stageKube); the next pod adopts
			// it.
			Flush: cfg.runHostUpgrade.flushHook(),
			// Deliberately no RestartCmd: repointing the Deployment has
			// already started the restart by the time the last step
			// returns (upgrade.Upgrader.restarts).
			StatusFile:      filepath.Join(cfg.DataDir, "upgrade-status.json"),
			AllowedBranches: cfg.UpgradeAllowedBranches,
		})
	case cfg.UpgradeImage != "" && (!KubernetesMode(cfg) || cfg.UpgradeImageRefFile != ""):
		// The container path: no checkout, no build, no install -- pull
		// the branch's published image, run it once to prove it works,
		// and point the unit's EnvironmentFile at it before restarting.
		// HealthCheckArgs is the same "schema-version" the build path
		// uses, run inside the pulled image (pkg/upgrade/image.go).
		return upgrade.New(upgrade.Config{
			Image: &upgrade.ImageConfig{
				Repository: cfg.UpgradeImage,
				RefFile:    cfg.UpgradeImageRefFile,
				VerifyCmd:  cfg.UpgradeVerifyCmd,
				// Only for a deployment that actually runs sandbox
				// containers: it asks the newly pulled image which one
				// it expects (sandboximage.go) and pulls that too, so
				// the two halves of a granule deployment upgrade
				// together. A host-directory deployment has no second
				// image to keep in step -- see SandboxImageArgs's own
				// doc comment.
				SandboxImageArgs: sandboxImageArgs(cfg.GranuleSandboxes),
				// And what this host pins over that answer, if
				// anything, so the step reports a pull whose image
				// nothing will run as exactly that rather than as a
				// sandbox kept in step (sandboxImagePin).
				SandboxImagePin: sandboxImagePin(cfg),
				// And whether the image it pulls would accept the
				// command line this host's unit starts grain with. The
				// unit is written once by scripts/setup.sh and the
				// image moves under it on every upgrade, so this is the
				// one check that is about the *deployment* rather than
				// about the build (grain/task-233).
				UnitFlags:     cfg.UnitFlags,
				FlagNamesArgs: []string{"daemon", "-flag-names"},
				Track: (installtrack.Probe{
					IncludeHost:    true,
					AgyPath:        cfg.AgyPath,
					UIAllowedHosts: cfg.UiAllowedHosts,
					DataDir:        cfg.DataDir,
				}).Detect().Track,
			},
			HealthCheckArgs: []string{"schema-version"},
			// What the pane previews when a target is chosen: the
			// release notes that build carries, from this one's
			// version on (pkg/cli/releasenotes.go).
			ReleaseNotesArgs: releaseNotesArgs,
			Schema:           upgradeSchemaConfig(cfg, db),
			Hold:             orchestrator.UpgradeHold(store),
			// Every run in a run host lets go, its cursor written,
			// before the restart (docs/non-disruptive-upgrade.md, piece
			// 6); the next build adopts it.
			Flush:      cfg.runHostUpgrade.flushHook(),
			RestartCmd: cfg.UpgradeRestartCmd,
			// The start that restart brings up is one this deployment
			// asked for, not a crash (restart_requests.go).
			BeforeRestart: upgradeRestartRequest(cfg),
			// The container install's restart is a touch file: it comes
			// back at once and the stop follows. This is how the upgrade
			// tells that stop from a restart that did nothing, and leaves
			// the queue held for the next startup (task 1504).
			Stopping:        cfg.shutdownStarted,
			Refusal:         hostModeUpgradeRefusal(cfg, systemHostModeProbe()),
			StatusFile:      filepath.Join(cfg.DataDir, "upgrade-status.json"),
			AllowedBranches: cfg.UpgradeAllowedBranches,
		})
	case cfg.UpgradeSrcDir != "":
		return upgrade.New(upgrade.Config{
			SrcDir:      cfg.UpgradeSrcDir,
			BuildCmd:    []string{"make", "container-build"},
			BuiltBinary: filepath.Join(cfg.UpgradeSrcDir, "bin", "grain"),
			InstallPath: cfg.UpgradeInstallPath,
			// "schema-version" (schemaversion.go) touches no store and
			// needs no config, so running the newly installed binary
			// with it is a cheap sanity check that the binary itself
			// isn't broken outright before RestartCmd ever cuts over to
			// it (bwsalmon/agents#418/#422).
			HealthCheckArgs: []string{"schema-version"},
			// What the pane previews when a target is chosen: the
			// release notes that build carries, from this one's
			// version on (pkg/cli/releasenotes.go).
			ReleaseNotesArgs: releaseNotesArgs,
			Schema:           upgradeSchemaConfig(cfg, db),
			// Both pipelines end in the same restart, so both hold the
			// queue while they run -- see orchestrator.UpgradeHold.
			Hold:       orchestrator.UpgradeHold(store),
			Flush:      cfg.runHostUpgrade.flushHook(),
			RestartCmd: cfg.UpgradeRestartCmd,
			// The start that restart brings up is one this deployment
			// asked for, not a crash (restart_requests.go).
			BeforeRestart:   upgradeRestartRequest(cfg),
			Stopping:        cfg.shutdownStarted,
			StatusFile:      filepath.Join(cfg.DataDir, "upgrade-status.json"),
			AllowedBranches: cfg.UpgradeAllowedBranches,
		})
	}
	return nil
}

// hostModeUpgradeRefusal is why a host-mode install -- one an older
// scripts/setup.sh made, upgraded onto this build by its Upgrade button
// or by hand -- takes no further upgrade from the UI. This build still
// runs there, on the bare machine, but host mode is gone (task 5041), and
// its next upgrade is the installer's migration to container mode plus
// grain hostd rather than one more image swap under a unit no installer
// writes any more, into a build that expects container mode. Which
// installs are host mode is hostModeInstall's (installnotice.go) to say;
// empty everywhere else: Kubernetes, the release container, `grain
// init`'s unit and a development daemon.
func hostModeUpgradeRefusal(cfg Config, p hostModeProbe) string {
	if ok, _ := hostModeInstall(cfg, p); !ok {
		return ""
	}
	return hostModeMessage + ". This release expects container mode, so it is not upgraded in place: " +
		"run " + hostModeMigrateCommand + " instead, which also upgrades it"
}

// releaseNotesArgs asks a target build for its release notes
// (pkg/upgrade/releasenotes.go appends --from).
var releaseNotesArgs = []string{"release-notes", "--json"}

// upgradeSchemaConfig is how an in-place upgrade finds out that the build
// it is about to run knows a different database schema, and what it does
// about it: write this deployment's rows out, have the new build rehearse
// importing them, and let its first start rebuild the store from them
// (schemaupgrade.go, pkg/upgrade/schema.go).
//
// The same configuration on both pipelines. What differs between an
// image and a binary is how the new build is run, which is pkg/upgrade's
// business; which schema this build knows, where the rows go and who
// says a migration may happen are this deployment's, and they are the
// same either way.
func upgradeSchemaConfig(cfg Config, db *sql.DB) *upgrade.SchemaConfig {
	sc := &upgrade.SchemaConfig{
		Version:     model.SchemaVersion,
		VersionArgs: []string{"schema-version"},
		// Run inside the build being upgraded to, against the directory
		// Prepare writes: the one check that is about this deployment's
		// data rather than about the new build in isolation.
		CheckArgs: []string{"state", "check", "-migrate"},
		Discard:   func() { discardSchemaUpgrade(cfg.DataDir) },
	}
	if db != nil {
		sc.Prepare = func(ctx context.Context, branch string, from, to int) (string, error) {
			return WriteSchemaUpgrade(ctx, cfg.DataDir, db, branch, from, to)
		}
	}
	return sc
}

// kubeUpgradeSchemaConfig is upgradeSchemaConfig with the migration
// rehearsal removed, which is the one part of it a Kubernetes upgrade
// cannot do.
//
// The rehearsal runs the new build against the directory this
// deployment's rows were just written to, read-only, at the same path
// out here (upgrade's imageRunner.rehearse). That works because a docker
// deployment and the container it starts share a filesystem. A pod does
// not share one with this pod, and the directory is on a ReadWriteOnce
// claim only this pod can mount -- so there is nothing to hand the new
// build, and upgrade.kubeRunner.rehearse says so rather than pretending.
//
// Everything else about the schema step is unchanged: the export still
// happens, the new build is still asked which schema it knows, and its
// first start still rebuilds the store from what was written. What is
// lost is the dry run that would have caught a bad import before the
// cut-over rather than after it, and that is a real gap on this path --
// see deploy/kubernetes/README.md.
func kubeUpgradeSchemaConfig(cfg Config, db *sql.DB) *upgrade.SchemaConfig {
	sc := upgradeSchemaConfig(cfg, db)
	if sc == nil {
		return nil
	}
	sc.CheckArgs = nil
	return sc
}

// upgradeRestartRequest is the upgrade's BeforeRestart: it writes down
// that the restart about to happen is the upgrade's own, so the next
// start's restart-loop count does not hold it against this deployment
// (restart_requests.go).
func upgradeRestartRequest(cfg Config) func(branch string) {
	requests := newRestartRequests(cfg.DataDir)
	return func(branch string) {
		requests.recordOrLog("upgrade to "+branch, log.Printf)
	}
}
