// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// settingsprint.go is how `grain config` and `grain settings`
// (settingscmd.go) print a deployment's settings and a credential check.
package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func (p *printer) config(cfg ui.Config) {
	if p.json {
		p.encode(cfg)
		return
	}
	fmt.Printf("acting as: %s (%s)\n", cfg.Actor.ID, cfg.Actor.Kind)
	if cfg.DefaultTarget != nil {
		fmt.Printf("default target: %s\n", cfg.DefaultTarget)
	}
	fmt.Println("\ncapabilities:")
	for _, cp := range cfg.Capabilities {
		fmt.Printf("  %-14s %-20s %s\n", cp.ID, cp.Name, cp.Description)
	}
}

func (p *printer) settings(s ui.Settings) {
	if p.json {
		p.encode(s)
		return
	}
	if !s.Configured {
		fmt.Println("not configured yet -- nothing here until a daemon starts, or a value is set")
		return
	}
	// First line, and printed even when unset: "which deployment am I
	// talking to" is the question every line under it is an answer for,
	// and a CLI pointed at the wrong -server has no sidebar badge to
	// give it away.
	if s.EnvironmentName != "" {
		fmt.Printf("environment:    %s\n", s.EnvironmentName)
	} else {
		fmt.Println("environment:    unnamed")
	}
	fmt.Printf("poll interval:  %s\n", s.PollInterval)
	fmt.Printf("max workers:    %d\n", s.MaxWorkers)
	fmt.Printf("max merges/repo: %d\n", s.MaxMergesPerRepo)
	fmt.Printf("max agent turns: %d\n", s.MaxAgentTurns)
	fmt.Printf("github host:    %s\n", s.GitHubHost)
	fmt.Printf("github insecure http: %t\n", s.GitHubInsecureHTTP)
	if s.GCPProject != "" {
		fmt.Printf("gcp project:    %s\n", s.GCPProject)
	}
	if s.GCPServiceAccountEmail != "" {
		fmt.Printf("gcp agent service account: %s\n", s.GCPServiceAccountEmail)
	}
	if s.GCPServiceAccountCount > 1 {
		fmt.Printf("gcp agent service account count: %d\n", s.GCPServiceAccountCount)
	}
	fmt.Printf("sandbox backend: %s\n", s.SandboxBackend)
	k8sSelected := s.SandboxBackend == "kubernetes" || s.SandboxBackend == "kubernetes-granule"
	if k8sSelected || s.K8sNamespace != "" {
		fmt.Printf("k8s namespace:  %s\n", orDefaultValue(s.K8sNamespace, s.K8sNamespaceDefault))
	}
	if s.K8sKubectl != "" {
		fmt.Printf("k8s kubectl:    %s\n", s.K8sKubectl)
	}
	if k8sSelected || s.K8sSandboxImage != "" {
		fmt.Printf("k8s sandbox image: %s\n", orDefaultValue(s.K8sSandboxImage, s.K8sSandboxImageDefault))
	}
	if s.SandboxBackend == "kubernetes-granule" || s.K8sGranuleImage != "" {
		fmt.Printf("k8s granule image: %s\n", orDefaultValue(s.K8sGranuleImage, s.K8sGranuleImageDefault))
	}
	if k8sSelected || s.K8sWorkspace != "" {
		fmt.Printf("k8s workspace:  %s\n", orDefaultValue(s.K8sWorkspace, s.K8sWorkspaceDefault))
	}
	if s.K8sGitProxyHost != "" {
		fmt.Printf("k8s git proxy host: %s\n", s.K8sGitProxyHost)
	}
	if s.K8sSandboxService != "" {
		fmt.Printf("k8s sandbox service: %s\n", s.K8sSandboxService)
	}
	if s.K8sServiceAccount != "" {
		fmt.Printf("k8s service account: %s\n", s.K8sServiceAccount)
	}
	if len(s.K8sNodeSelector) > 0 {
		fmt.Printf("k8s node selector: %s\n", strings.Join(s.K8sNodeSelector, ", "))
	}
	if k8sSelected || s.K8sPrivileged {
		fmt.Printf("k8s privileged: %t\n", s.K8sPrivileged)
	}
	if k8sSelected || s.K8sNetworkPolicy {
		fmt.Printf("k8s network policy: %t\n", s.K8sNetworkPolicy)
	}
	if k8sSelected || s.K8sDeployment != "" {
		fmt.Printf("k8s deployment: %s\n", orDefaultValue(s.K8sDeployment, s.K8sDeploymentDefault))
	}
	if k8sSelected || s.K8sContainer != "" {
		fmt.Printf("k8s container:  %s\n", orDefaultValue(s.K8sContainer, s.K8sContainerDefault))
	}
	if k8sSelected {
		// "" is no class at all here, not the default, so it is said.
		for _, pc := range []struct{ kind, name string }{
			{"interactive", s.K8sPriorityClassInteractive},
			{"agent", s.K8sPriorityClassAgent},
			{"ci", s.K8sPriorityClassCI},
		} {
			if pc.name == "" {
				pc.name = "(none)"
			}
			fmt.Printf("k8s priority class (%s): %s\n", pc.kind, pc.name)
		}
	}
	// The sandbox shape prints what is actually in effect, not the bare
	// stored value: 0 means "whatever grain's own default shape is", and
	// printing that literal 0 would read as a deliberately empty VM.
	// ui.Settings carries those defaults alongside the stored values
	// (SandboxCPUsDefault/SandboxMemoryMBDefault/SandboxDiskGBDefault)
	// for exactly this. Disk has one like the other two now -- every
	// dimension of a sandbox is a size grain names and passes, rather
	// than disk being left to however large a deployment's guest image
	// happens to be. And on a deployment whose sandbox backend has no
	// shape to apply at all, each line says so where the number is
	// (SandboxShapeIgnored) -- these three settings are stored on every
	// deployment but only granule-managed sandboxes are built to them.
	fmt.Printf("sandbox cpus:   %s\n", sandboxShapeValue(s.SandboxCPUs, s.SandboxCPUsDefault, s.SandboxShapeIgnored))
	fmt.Printf("sandbox memory mb: %s\n", sandboxShapeValue(s.SandboxMemoryMB, s.SandboxMemoryMBDefault, s.SandboxShapeIgnored))
	fmt.Printf("sandbox disk gb: %s\n", sandboxShapeValue(s.SandboxDiskGB, s.SandboxDiskGBDefault, s.SandboxShapeIgnored))
	// The same annotation for the same reason, on the setting beside
	// them: on a host-directory deployment a sandbox has the host's own
	// network, so "none" here would be describing an isolation nothing
	// applies. The value itself is already resolved by
	// ui.Settings.NetworkAccess -- an unset setting prints as the "open"
	// it means, never as a blank.
	networkAccess := s.NetworkAccess
	if s.SandboxShapeIgnored {
		networkAccess += " -- not in effect: host-directory sandboxes have the host's own network"
	}
	fmt.Printf("sandbox network: %s\n", networkAccess)
	// The budget those shapes are admitted against, printed beside them
	// rather than only inside the commitment line below: a ratio of 0 is
	// a real setting -- "leave this dimension to the run counts alone" --
	// and a deployment reading its own configuration back has to be able
	// to see which of the three it has chosen. The reserve prints what
	// was stored, with grain's own rule named where nobody has chosen,
	// since what a negative number comes to depends on the host and this
	// line is about the setting.
	fmt.Printf("sandbox overcommit: %s\n", sandboxOvercommitLine(s.SandboxOvercommit, s.SandboxCommitment))
	fmt.Printf("session sandbox idle ttl: %s\n", orDefaultValue(s.SessionSandboxIdleTTL, s.SessionSandboxIdleTTLDefault))
	fmt.Printf("sandbox cpu overcommit: %s\n", overcommitValue(s.SandboxCPUOvercommit))
	fmt.Printf("sandbox memory overcommit: %s\n", overcommitValue(s.SandboxMemoryOvercommit))
	fmt.Printf("sandbox disk overcommit: %s\n", overcommitValue(s.SandboxDiskOvercommit))
	if s.SandboxMemoryReserveMB < 0 {
		fmt.Println("sandbox memory reserve mb: grain's own -- an eighth of the host, never less than 2048")
	} else {
		fmt.Printf("sandbox memory reserve mb: %d\n", s.SandboxMemoryReserveMB)
	}
	// The dimension that is a floor rather than a sum, printed as the
	// setting and then as what it comes to on this deployment's own
	// volumes: a share of a volume is not a figure until it meets a
	// disk, and the store's 20 GB and the sandbox volume's 443 GB get
	// different answers out of the same setting (model.DiskFloor,
	// grain/task-372). The resolved lines are skipped where this process
	// has no volumes to speak for -- `grain settings` run against a
	// remote deployment's API.
	if s.SandboxDiskFloorShare <= 0 && s.SandboxDiskFloorMinMB <= 0 {
		fmt.Println("sandbox disk floor: none -- nothing is held back as this deployment's volumes fill")
	} else {
		fmt.Printf("sandbox disk floor: %.1f%% of each volume, never less than %d MiB and never more than %.0f%%\n",
			s.SandboxDiskFloorShare*100, s.SandboxDiskFloorMinMB, s.SandboxDiskFloorMaxShare*100)
	}
	for _, d := range s.SandboxDiskFloors {
		if d.TotalMB <= 0 {
			continue
		}
		fmt.Printf("  %s (%s GiB): %s GiB kept free, swept back to %s GiB\n",
			d.Path, daemon.GibMB(d.TotalMB), daemon.GibMB(d.FloorMB), daemon.GibMB(d.ReclaimMB))
	}
	// And what all of those together commit the host to, printed right
	// under the shape and only when it is worth saying
	// (ui.SandboxCommitment.Warned): max workers and the sandbox shape
	// are printed a dozen lines apart, which is exactly the gap this
	// arithmetic falls into. Numbers, not a verdict -- the trade-off is
	// the operator's to make, and overcommitting a host that mostly
	// waits on a model is a reasonable way to make it.
	//
	// Printed under the three lines that just said the shape is not in
	// effect, too, and says the honest thing there instead of nothing:
	// the runs are still on that machine and nothing caps them, which is
	// the one deployment where max workers is the whole of the
	// commitment (grain/task-285).
	if s.SandboxCommitment.Warned() {
		fmt.Printf("sandbox commitment: %s\n", s.SandboxCommitment.Summary())
	}
	// How long this deployment remembers its own CI, printed as what is
	// in effect rather than as what is stored, exactly like the sandbox
	// shape above: 0 in any of these four means grain's own number, and
	// printing the literal 0 would read as "keeps nothing". The records
	// and the output are two lines because they are two bounds -- a run
	// answers "what was the verdict" long after it has stopped answering
	// "what did it print" (grain/task-269).
	fmt.Printf("ci run history: %s\n", ciRetentionValue(s.CIRunHistory, s.CIRunHistoryDefault, "runs", "every run kept"))
	fmt.Printf("ci step output: %s, %s\n",
		ciRetentionValue(s.CIStepOutputMB, s.CIStepOutputMBDefault, "MiB", "every byte kept"),
		ciRetentionValue(s.CIStepOutputDays, s.CIStepOutputDaysDefault, "days", "no age bound"))
	fmt.Printf("ci output per run: %s\n", ciRetentionValue(s.CIRunOutputKB, s.CIRunOutputKBDefault, "KiB", "unbounded"))
	// And what CI's two directories on the data disk may occupy, printed
	// the same way and for the same reason -- a stored 0 is grain's own
	// number, not a cache of nothing (grain/task-317). Two lines, because
	// they are two directories: an operator holding these against the
	// System pane's Storage section is reading one bar each.
	fmt.Printf("ci job cache: %s, %s, %s\n",
		ciDiskBoundValue(s.CICacheMB, s.CICacheMBDefault, "MiB"),
		ciDiskBoundValue(s.CICacheEntries, s.CICacheEntriesDefault, "entries"),
		ciDiskBoundValue(s.CICacheDays, s.CICacheDaysDefault, "days"))
	fmt.Printf("ci artifacts: %s, %s, %s per run, %s per artifact\n",
		ciDiskBoundValue(s.CIArtifactMB, s.CIArtifactMBDefault, "MiB"),
		ciDiskBoundValue(s.CIArtifactDays, s.CIArtifactDaysDefault, "days"),
		ciDiskBoundValue(s.CIArtifactRunMB, s.CIArtifactRunMBDefault, "MiB"),
		ciDiskBoundValue(s.CIArtifactFileMB, s.CIArtifactFileMBDefault, "MiB"))
	// And how it orders the jobs it has no room for, printed the same
	// in-effect way: an empty setting is grain's own window, and printing
	// the blank would read as a deployment that does neither
	// (grain/task-325).
	if s.CIJobsPerRun > 0 {
		fmt.Printf("ci jobs per run: %d\n", s.CIJobsPerRun)
	} else {
		fmt.Println("ci jobs per run: unlimited")
	}
	fmt.Printf("ci queue age boost: %s\n", orDefaultValue(s.CIAgeBoost, s.CIAgeBoostDefault))
	fmt.Printf("ci capacity reserved after: %s\n", orDefaultValue(s.CIReserveAfter, s.CIReserveAfterDefault))
	if len(s.DefaultCapabilities) > 0 {
		fmt.Printf("default capabilities: %s\n", strings.Join(s.DefaultCapabilities, ", "))
	} else {
		fmt.Println("default capabilities: none")
	}
	// Printed in full rather than summarised as "set"/"none": it is the
	// text every run on this deployment is given, and "why is every agent
	// doing X" is answered by reading the words, not by learning that
	// some words exist. Indented onto its own lines because it is the one
	// setting here that is prose and may be several lines of it -- see
	// promptExtensionBlock.
	fmt.Print(promptExtensionBlock("prompt extension", s.PromptExtension))
	if s.TaskBranchTemplate == "" {
		fmt.Println("task branches:   grain/task-{id}")
	} else {
		fmt.Printf("task branches:   %s\n", s.TaskBranchTemplate)
	}
	if s.DeploymentID == "" {
		fmt.Printf("deployment id:   %s (default)\n", s.DeploymentIDDefault)
	} else {
		fmt.Printf("deployment id:   %s\n", s.DeploymentID)
	}
	// What new sandboxes are created from, and -- for a custom image --
	// whether its sandbox contract is one this grain still drives: the
	// copy of Settings -> Sandboxes an operator can read from the host.
	fmt.Printf("sandbox image:   %s\n", orDefaultValue(s.SandboxImage, s.SandboxImageDefault))
	if s.SandboxImageMirror != "" {
		fmt.Printf("sandbox image mirror: %s\n", s.SandboxImageMirror)
	}
	if s.K8sGranuleImageWarning != "" {
		fmt.Printf("  WARNING: %s\n", s.K8sGranuleImageWarning)
	}
	if s.SandboxBaseImage != "" {
		fmt.Printf("sandbox base:    %s (sandbox contract v%d, oldest supported v%d)\n",
			s.SandboxBaseImage, s.SandboxContractVersion, s.SandboxMinimumContractVersion)
	}
	if s.SandboxImageWarning != "" {
		fmt.Printf("  WARNING: %s\n", s.SandboxImageWarning)
	}
	// Every other setting above is already in effect: the daemon
	// re-reads this row each reconcile tick (pkg/cli/daemon.go's
	// liveConfig). These are the ones that are not, so saying so here is
	// the CLI's half of what the Settings pane annotates the field with
	// -- otherwise `grain settings -github-host=...` prints the new value
	// back and reads as though it had done something.
	if len(s.PendingRestart) > 0 {
		fmt.Printf("\nsaved, but not running yet -- restart the daemon to apply: %s\n",
			strings.Join(s.PendingRestart, ", "))
	}
	// And the third case beside it (grain/task-369): a stored value the
	// daemon could not run on at all, which it replaced with one it
	// could. The lines above already print the replacement -- they print
	// what is in effect, like the sandbox shape does -- so without this
	// the row in the state repository and the number here would disagree
	// with nothing to explain it. Not a restart: restarting substitutes
	// the same value again, and only storing a usable one ends it.
	if len(s.Unrunnable) > 0 {
		fmt.Println("\nstored, but not runnable -- grain cannot run on these values, so this daemon\n" +
			"substituted its own and printed those above. Save a usable value to change it:")
		for _, u := range s.Unrunnable {
			fmt.Printf("  %s: stored %s, running %s\n", u.Key, u.Stored, u.Running)
		}
	}
	if len(s.Capabilities) > 0 {
		fmt.Println("\ncapabilities:")
		for _, cp := range s.Capabilities {
			fmt.Println(capabilityStatusLine(cp))
		}
		// "ready" above is the whole of what this listing can honestly
		// claim: it says a project, an account and a secret are set, and
		// nothing it reads can see whether the key inside that secret is
		// one the service still accepts (README.md's "Testing a
		// credential, from the pane that calls it Ready"). Printed only
		// where some capability can actually be tested, so it never
		// names a command this deployment would refuse.
		if slices.ContainsFunc(s.Capabilities, func(cp ui.CapabilityStatus) bool { return cp.Checkable }) {
			fmt.Println("\n\"ready\" means configured -- to ask the service whether a stored credential still\n" +
				"works, run: grain settings -check-capability <id>")
		}
	}
}

// promptExtensionBlock renders one layer of the prompt extension
// (model/prompt_extension.go) as lines of CLI output: "label: none" when
// there is nothing to show, and otherwise the label on its own line with
// the text indented under it.
//
// Indented rather than printed after the label, unlike every other
// setting `grain settings` prints, because this one is prose: a
// multi-line value written after "prompt extension: " would put its
// first line in the column and the rest hard against the left margin,
// which reads as though the output had ended and something else began.
// The trailing newline is included, so a caller prints this and nothing
// else.
func promptExtensionBlock(label, text string) string {
	if strings.TrimSpace(text) == "" {
		return label + ": none\n"
	}
	var b strings.Builder
	b.WriteString(label + ":\n")
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// orDefaultValue is sandboxShapeValue's counterpart for a setting whose
// unset value is the empty string rather than 0: the stored value when
// there is one, otherwise the default actually in effect, named as the
// default rather than printed as the blank that is stored. A daemon too
// old to report a default (or one with none) prints "unset", which is all
// this end can honestly say about it.
func orDefaultValue(stored, grainDefault string) string {
	if stored != "" {
		return stored
	}
	if grainDefault != "" {
		return fmt.Sprintf("%s (grain default, unset)", grainDefault)
	}
	return "unset"
}

// sandboxShapeValue renders one dimension of the deployment-wide sandbox
// VM shape: the stored value when there is one, and otherwise the shape
// actually in effect -- grain's own default for that dimension -- named
// as the default rather than printed as the bare 0 that is stored, so
// that "unset" and "in effect" are both legible from one line. A build
// whose ui.Settings reports no default to fall back on (0) says only
// that the setting is unset, which is all it can honestly say; nothing
// this binary talks to reports one now that disk has a default too, but
// an older daemon on the other end of the API still can.
//
// ignored (ui.Settings.SandboxShapeIgnored) is the deployment saying
// this number sizes nothing here, because its sandboxes are directories
// on the daemon's own machine. Said on each of the three lines rather
// than once underneath them: whoever misreads this reads one line and
// stops, and "sandbox cpus: 2" on its own is exactly the sentence that
// misleads (grain/task-9). The number is still printed, and still
// stored -- it is what this deployment would build VMs at if it were
// switched to -granule-sandboxes -- so what is annotated is that it is
// not in effect, not that it is unset.
func sandboxShapeValue(stored, grainDefault int, ignored bool) string {
	value := "unset"
	switch {
	case stored != 0:
		value = strconv.Itoa(stored)
	case grainDefault != 0:
		value = fmt.Sprintf("%d (grain default, unset)", grainDefault)
	}
	if !ignored {
		return value
	}
	return value + " -- not in effect: host-directory sandboxes have no shape"
}

// ciRetentionValue is one of the CI history's four bounds as `grain
// settings` prints it (ui.Settings.CIRunHistory and its neighbours):
// the number with its unit, what a stored 0 actually comes to, and what
// a negative one means, which is the one reading a bare number cannot
// carry.
//
// Three readings rather than two, unlike sandboxShapeValue above,
// because "keep it all" is a value an operator may deliberately store
// here -- disk they have decided to spend -- and it has to be
// distinguishable on sight from a setting nobody has touched.
func ciRetentionValue(stored, grainDefault int, unit, unbounded string) string {
	switch {
	case stored < 0:
		return unbounded
	case stored > 0:
		return fmt.Sprintf("%d %s", stored, unit)
	default:
		return fmt.Sprintf("%d %s (grain default, unset)", grainDefault, unit)
	}
}

// ciDiskBoundValue is one of CI's disk bounds as `grain settings` prints
// it (ui.Settings.CICacheMB and its neighbours): the number with its
// unit, and what a stored 0 actually comes to.
//
// Two readings rather than ciRetentionValue's three. A directory on the
// volume this deployment dispatches out of has no "keep it all" reading
// -- ci.StorageBoundsFrom has why, and ui.UpdateSettings refuses to
// store one -- so there is no third case to print, and a value that
// somehow arrived in the row anyway is being applied as the default,
// which is what this says about it.
func ciDiskBoundValue(stored, grainDefault int, unit string) string {
	if stored > 0 {
		return fmt.Sprintf("%d %s", stored, unit)
	}
	return fmt.Sprintf("%d %s (grain default, unset)", grainDefault, unit)
}

// sandboxOvercommitLine formats the overcommit controller settings and, when
// auto mode is active and the daemon has reported a budget, the live effective
// ratios currently in effect.
func sandboxOvercommitLine(oc model.OvercommitSettings, commitment *ui.SandboxCommitment) string {
	r := oc.Resolved()
	line := fmt.Sprintf("%s (CPU %.2fx..%.2fx, memory %.2fx..%.2fx, targets CPU %.1f%% / memory %.1f%%)",
		r.Mode, r.CPUMin, r.CPUMax, r.MemoryMin, r.MemoryMax, r.CPUPressureTarget, r.MemoryPressureTarget)
	if r.Mode == model.OvercommitAuto && commitment != nil && commitment.Budget != nil {
		budget := commitment.Budget
		switch {
		case budget.Overcommit != nil && (budget.Overcommit.CPUOvercommit > 0 || budget.Overcommit.MemoryOvercommit > 0):
			line += fmt.Sprintf(" -- effective CPU %.2fx, memory %.2fx",
				budget.Overcommit.CPUOvercommit, budget.Overcommit.MemoryOvercommit)
		case budget.CPUOvercommit > 0 || budget.MemoryOvercommit > 0:
			line += fmt.Sprintf(" -- effective CPU %.2fx, memory %.2fx",
				budget.CPUOvercommit, budget.MemoryOvercommit)
		}
	}
	return line
}

// overcommitValue is one of the budget's two ratios as `grain settings`
// prints it: the number, with what a zero means spelled out beside it.
// Zero reads as "unset" everywhere else in this output, and here it is
// the opposite of unset -- it is the one value that takes the bound off
// a dimension entirely (model.Budget).
func overcommitValue(ratio float64) string {
	if ratio <= 0 {
		return "0 -- this dimension is bounded by the run counts alone"
	}
	return fmt.Sprintf("%.2fx", ratio)
}

// capabilityStatusLine renders one ui.CapabilityStatus as a line of
// `grain settings` -- the CLI's half of the Settings pane's own
// Capabilities tab, printed here because "why did a task never get the
// thing it was granted" is a question asked from a shell on the host at
// least as often as from a browser, and until now the only answer
// available there was the two GCP fields above, which say nothing about
// secrets and nothing at all about the two gaps below.
//
// Both gaps are named, not just the configuration one, because they are
// fixed in different places and confusing them costs a debugging
// session: "needs"/"missing secrets" is this deployment (set it in
// Settings, or `grain secrets set`), while "NOT GRANTABLE" is grain's
// own code and cannot be configured around -- see
// ui.CapabilityStatus.Grantable.
func capabilityStatusLine(cp ui.CapabilityStatus) string {
	state := "not ready"
	if cp.Ready {
		state = "ready"
	}
	var notes []string
	// First, ahead of the gaps below, for the same reason the pane puts
	// "not grantable" first: a capability every task is filed holding is
	// the one whose "not ready" is a deployment-wide problem rather than
	// a per-task one.
	if cp.Default {
		notes = append(notes, "default -- every new task is filed with this")
	}
	// The per-repo layer, named as the repos it applies to rather than
	// as a second bare "default": with two layers, a line that only said
	// "default" would describe a deployment-wide default that only some
	// tasks actually get (ui.CapabilityStatus.DefaultRepos). Listed even
	// alongside the deployment-wide note above, since a repo can restate
	// one the deployment already gives and dropping it deployment-wide
	// leaves the repo's own entry standing.
	if len(cp.DefaultRepos) > 0 {
		notes = append(notes, "default in: "+strings.Join(cp.DefaultRepos, ", "))
	}
	if !cp.Grantable {
		notes = append(notes, "NOT GRANTABLE -- grain registers a provider for this, but no task can ask for it")
	}
	if len(cp.MissingConfig) > 0 {
		notes = append(notes, "needs: "+strings.Join(cp.MissingConfig, ", "))
	}
	// Distinct from "needs" above, and a different instruction: these
	// are settings this deployment has stored and the daemon serving
	// this listing is not running with, so there is nothing to type in
	// (ui.CapabilityStatus.PendingConfig).
	if len(cp.PendingConfig) > 0 {
		notes = append(notes, "saved but not yet in effect: "+strings.Join(cp.PendingConfig, ", ")+
			" -- restart the daemon if its reconcile loop has stopped applying settings")
	}
	if len(cp.MissingSecrets) > 0 {
		notes = append(notes, "missing secrets: "+strings.Join(cp.MissingSecrets, ", "))
	}
	line := fmt.Sprintf("  %-20s %-9s", cp.ID, state)
	if len(notes) > 0 {
		line += " " + strings.Join(notes, "; ")
	}
	return strings.TrimRight(line, " ")
}

// capabilityCheck prints one ui.CapabilityCheck: the verdict, what was
// authenticated as, and the sentence saying what the far end actually
// said -- which on a failure is the whole point and is printed in full
// rather than summarised.
func (p *printer) capabilityCheck(check ui.CapabilityCheck) {
	if p.json {
		p.encode(check)
		return
	}
	verdict := "REFUSED"
	if check.OK {
		verdict = "ok"
	}
	fmt.Printf("%s: %s\n", check.ID, verdict)
	if len(check.Credentials) > 0 {
		fmt.Printf("checked as:  %s\n", strings.Join(check.Credentials, ", "))
	}
	fmt.Printf("checked at:  %s\n", check.CheckedAt.Format(time.RFC3339))
	if check.Detail != "" {
		fmt.Println(check.Detail)
	}
}

// agentKeyCheck prints one ui.AgentKeyCheck the way capabilityCheck
// above prints its own: the verdict first, then what was checked, when,
// and the vendor's sentence.
func (p *printer) agentKeyCheck(check ui.AgentKeyCheck) {
	if p.json {
		p.encode(check)
		return
	}
	verdict := "REFUSED"
	if check.OK {
		verdict = "ok"
	}
	fmt.Printf("%s: %s\n", check.Framework, verdict)
	if check.Secret != "" {
		fmt.Printf("checked as:  %s\n", check.Secret)
	}
	fmt.Printf("checked at:  %s\n", check.CheckedAt.Format(time.RFC3339))
	if check.Detail != "" {
		fmt.Println(check.Detail)
	}
}
