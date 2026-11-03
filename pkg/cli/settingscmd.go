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

// settingscmd.go is `grain config` and `grain settings`: reading and
// changing a deployment's stored settings over the daemon's API, checking
// a capability's or an agent's credential, and how all of that is printed.
package cli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func cmdConfig(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain config", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	cfg, err := c.Config(ctx)
	if err != nil {
		return err
	}
	out.config(cfg)
	return nil
}

// cmdSettings shows, or changes, the daemon's own store-backed
// configuration (bwsalmon/agents#320, ui.Settings/model.Config) -- with
// no flags given it prints what is currently stored (or that nothing is
// yet, on a store no daemon has ever started against); with any given,
// it applies only those, the same fs.Visit convention cmdUpdate uses for
// a task's own fields, and prints the result.
func cmdSettings(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain settings", flag.ContinueOnError)
	pollInterval := fs.String("poll-interval", "", "how often the daemon runs a reconcile cycle, e.g. 10s")
	maxWorkers := fs.Int("max-workers", 0, "maximum number of ordinary tasks dispatched at once")
	_ = fs.Int("max-mergers", 0, "deprecated and ignored: this deployment's sandbox budget is what bounds the host")
	maxMergesPerRepo := fs.Int("max-merges-per-repo", 0, "maximum ready pull requests one repository may merge per queue cycle")
	_ = fs.Int("merge-batch-size", 0, "deprecated and ignored: the merge queue no longer batches")
	_ = fs.String("merge-gather-window", "", "deprecated and ignored: the merge queue no longer batches")
	_ = fs.Int("max-ci-jobs", 0, "deprecated and ignored: this deployment's sandbox budget is what bounds the host")
	// The deployment-wide framework and model settings are gone: a run's
	// model is a model connection's, chosen per task or by the
	// deployment's default model (docs/single-tenant-reset.md, stage 2).
	// Kept so a script that passes one gets a note rather than a usage
	// error.
	for _, name := range []string{"agent-framework", "gemini-model", "gemini-effort", "claude-model", "codex-model"} {
		_ = fs.String(name, "", "removed and ignored: models are chosen through model connections (Settings -> Models)")
	}
	maxAgentTurns := fs.Int("max-agent-turns", 0, "cap on model/tool round trips per run (0 = uncapped; runs are bounded by wall-clock runtime instead)")
	githubHost := fs.String("github-host", "", "GitHub API host (the one setting here that needs a daemon restart to take effect)")
	var githubInsecureHTTP bool
	fs.BoolVar(&githubInsecureHTTP, "github-insecure-http", false, "speak plain HTTP to -github-host instead of HTTPS (needs a daemon restart to take effect)")
	gcpProject := fs.String("gcp-project", "", "GCP project the gcp-key/gemini-key capabilities mint into")
	gcpServiceAccountEmail := fs.String("gcp-agent-service-account", "", "the narrow agent service account gcp-key mints keys for")
	gcpServiceAccountCount := fs.Int("gcp-agent-account-count", 0, "the number of agent service accounts for gcp-key")
	iapBackendService := fs.String("iap-backend-service", "", "GCP Compute backend service name for dynamic IAP IAM reconciliation")
	iapResource := fs.String("iap-resource", "", "IAP web resource whose IAP access is reconciled, e.g. projects/<NUMBER>/iap_web/compute/services/<SERVICE>; empty or \"none\" turns that off")
	// The deployment-wide sandbox VM shape (ui.Settings.SandboxCPUs/
	// SandboxMemoryMB/SandboxDiskGB, bwsalmon/agents#534,
	// grain/task-41). 0 is a real value for all three -- "leave grain's
	// own default in place" -- so an operator putting a deployment back
	// on that default sets the flag to 0 rather than omitting it, the
	// same way an empty -target-repos clears the allowlist. All three
	// name a number now: a sandbox is always created at a size this repo
	// chose (granule.DefaultCPUs/DefaultMemoryMB/DefaultDiskGB), rather
	// than at whatever `granule vm create` or the deployment's own
	// guest image would otherwise decide.
	sandboxBackend := fs.String("sandbox-backend", "",
		"sandbox isolation method to use after the daemon restarts: "+model.SandboxBackendNames())
	sandboxCPUs := fs.Int("sandbox-cpus", 0,
		"deployment-wide default vCPU count for a granule-managed sandbox VM; 0 uses grain's own default, "+strconv.Itoa(granule.DefaultCPUs))
	sandboxMemoryMB := fs.Int("sandbox-memory-mb", 0,
		"deployment-wide default guest memory, in MiB, for a granule-managed sandbox VM; 0 uses grain's own default, "+strconv.Itoa(granule.DefaultMemoryMB))
	sandboxDiskGB := fs.Int("sandbox-disk-gb", 0,
		"deployment-wide default root disk size, in GiB, for a granule-managed sandbox VM; 0 uses grain's own default, "+strconv.Itoa(granule.DefaultDiskGB))
	// What all of those shapes are admitted against (model.Budget): the
	// counts above say how many sandboxes may exist, and these three say
	// how much machine they may commit between them. Settable here as
	// well as in the pane for the reason -network-access below is -- a
	// deployment installed by scripts/setup.sh binds its UI to loopback,
	// and a budget is exactly the thing an operator wants to move from
	// the ssh session where they have just watched the host swap.
	sandboxCPUOvercommit := fs.Float64("sandbox-cpu-overcommit", model.DefaultCPUOvercommit,
		"how much more vCPU than the sandbox host has may be committed to sandboxes at once; 0 leaves vCPU bounded by the run counts alone")
	sandboxMemoryOvercommit := fs.Float64("sandbox-memory-overcommit", model.DefaultMemoryOvercommit,
		"the same ratio for memory -- 1 promises exactly what the host has, and the way a host finds memory it has already promised is to swap")
	sandboxDiskOvercommit := fs.Float64("sandbox-disk-overcommit", model.DefaultDiskOvercommit,
		"the same ratio for sandbox disk -- how many times over the sandbox volume (or a cluster's allocatable ephemeral storage), "+
			"less its free-space floor, sandbox disks may be promised; on Kubernetes it also scales each pod's ephemeral-storage request. "+
			"0 leaves disk bounded by the floor alone")
	sandboxOvercommitMode := fs.String("sandbox-overcommit", "",
		"overcommit mode: fixed holds the configured ratios, auto adjusts them from host CPU/memory PSI within the bounds below")
	sandboxOvercommitModeAlias := fs.String("sandbox-overcommit-mode", "",
		"alias for -sandbox-overcommit (fixed or auto)")
	sandboxOvercommitCPUMin := fs.Float64("sandbox-overcommit-cpu-min", model.DefaultOvercommitCPUMin,
		"lower bound on effective CPU overcommit in auto mode (0 uses grain's default, 1.0)")
	sandboxOvercommitCPUMax := fs.Float64("sandbox-overcommit-cpu-max", model.DefaultOvercommitCPUMax,
		"upper bound on effective CPU overcommit in auto mode (0 uses grain's default, 6.0)")
	sandboxOvercommitMemoryMin := fs.Float64("sandbox-overcommit-memory-min", model.DefaultOvercommitMemoryMin,
		"lower bound on effective memory overcommit in auto mode (0 uses grain's default, 1.0)")
	sandboxOvercommitMemoryMax := fs.Float64("sandbox-overcommit-memory-max", model.DefaultOvercommitMemoryMax,
		"upper bound on effective memory overcommit in auto mode (0 uses grain's default, 2.0)")
	sandboxOvercommitCPUPressureTarget := fs.Float64("sandbox-overcommit-cpu-pressure-target", model.DefaultOvercommitCPUPressureTarget,
		"target CPU PSI some avg60 percentage in auto mode (0 uses grain's default, 25)")
	sandboxOvercommitMemoryPressureTarget := fs.Float64("sandbox-overcommit-memory-pressure-target", model.DefaultOvercommitMemPressureTarget,
		"target memory PSI some avg60 percentage in auto mode (0 uses grain's default, 5)")
	sandboxMemoryReserveMB := fs.Int("sandbox-memory-reserve-mb", model.ReserveMemoryDefault,
		"memory kept back from that budget, in MiB, for everything on the sandbox host that is not a sandbox: the daemon, "+
			"the container runtime, each VM's own qemu overhead and the page cache they read through. "+
			"Negative uses grain's own, an eighth of the machine and never less than 2 GiB")
	// And the budget's third dimension, which a shell is the most likely
	// place of all to be moved from: the deployment whose disk floor is
	// holding every dispatch is the one whose operator is already in an
	// ssh session watching `df` (model.DiskFloor, grain/task-372).
	sandboxDiskFloorShare := fs.Float64("sandbox-disk-floor-share", model.DefaultDiskFloorShare,
		"how much of each volume this deployment writes to is kept free: new CI jobs are held and agent dispatch "+
			"deferred while free space is below it. A share of the volume, so 0.1 is a tenth; never more than a "+
			"quarter is kept free however this is set, and 0 with a minimum of 0 holds nothing back at all")
	sandboxDiskFloorMinMB := fs.Int("sandbox-disk-floor-min-mb", model.MinDiskFloorMB,
		"the least that floor is ever worth, in MiB, whatever the share comes to on a small volume -- a tenth of a "+
			"20 GB store disk is 2 GB, which is not enough room to notice a runaway write in")
	// The fourth dimension of the same sandbox (model.NetworkAccess,
	// grain/task-269), and settable from a shell because a deployment
	// installed by scripts/setup.sh
	// binds its UI to loopback, so a pane is not where its operator is.
	// Validated by ui.UpdateSettings rather than here, like the
	// framework, so there is one copy of the vocabulary and it is at the
	// end that stores it. Empty puts it back to grain's own answer,
	// which is "open".
	networkAccess := fs.String("network-access", "",
		"how much of the internet a run's sandbox reaches unless its task says otherwise: "+
			model.NetworkAccessNames()+" -- "+string(model.NetworkNone)+
			" needs granule sandboxes, and empty means "+string(model.NetworkOpen))
	sandboxImage := fs.String("sandbox-image", "", "custom OCI sandbox image for the active backend, built FROM grain's guest-base or "+
		"k8s-sandbox-base image (empty uses the image this build is stamped for; see docs/sandbox-image-contract.md)")
	sandboxImageMirror := fs.String("sandbox-image-mirror", "", "OCI repository (without tag, e.g. us-docker.pkg.dev/<cluster-project>/grain/guest) "+
		"into which the build-stamped guest sandbox image is mirrored on upgrade and startup so a cluster in another GCP organization can pull it")
	sessionSandboxIdleTTL := fs.String("session-sandbox-idle-ttl", "",
		"how long an interactive session task's sandbox (and in-process agent session) stays warm between turns awaiting a reply (e.g. 30m; empty uses grain's default, -1s disables warm retention)")
	k8sNamespace := fs.String("k8s-namespace", "", "Kubernetes namespace sandbox pods are created in")
	k8sKubectl := fs.String("k8s-kubectl", "", "kubectl binary path for Kubernetes sandboxes (empty uses kubectl on PATH)")
	k8sSandboxImage := fs.String("k8s-sandbox-image", "", "container image for Kubernetes sandbox pods")
	k8sGranuleImage := fs.String("k8s-granule-image", "", "packed guest image for Kubernetes Granule sandbox pods")
	k8sWorkspace := fs.String("k8s-workspace", "", "working directory inside each Kubernetes sandbox pod")
	k8sGitProxyHost := fs.String("k8s-git-proxy-host", "", "host address Kubernetes sandboxes reach the git and model proxies at")
	k8sSandboxService := fs.String("k8s-sandbox-service", "", "ClusterIP Service fronting the git and model proxies for Kubernetes sandboxes")
	k8sServiceAccount := fs.String("k8s-sandbox-service-account", "", "ServiceAccount for Kubernetes sandbox pods")
	k8sNodeSelector := fs.String("k8s-node-selector", "", "comma-separated key=value nodeSelector entries for Kubernetes sandbox pods")
	k8sPrivileged := fs.Bool("k8s-sandbox-privileged", false, "run Kubernetes sandbox containers privileged")
	k8sNetworkPolicy := fs.Bool("k8s-network-policy", false, "whether cluster CNI enforces NetworkPolicy for network:none runs")
	k8sDeployment := fs.String("k8s-deployment", "", "Kubernetes Deployment name for self-upgrade")
	k8sContainer := fs.String("k8s-container", "", "container name in the Kubernetes Deployment for self-upgrade")
	k8sPriorityInteractive := fs.String("k8s-priority-class-interactive", "",
		"PriorityClass for interactive tasks' Kubernetes sandbox pods; empty sets none")
	k8sPriorityAgent := fs.String("k8s-priority-class-agent", "",
		"PriorityClass for agent runs' and persistent sandboxes' Kubernetes sandbox pods; empty sets none")
	k8sPriorityCI := fs.String("k8s-priority-class-ci", "", "PriorityClass for CI jobs' Kubernetes sandbox pods; empty sets none")
	defaultCapabilities := fs.String("default-capabilities", "",
		"comma-separated capability IDs every new task is filed holding -- empty files each task with only what it asks for")
	// Settable from a shell, not only from the Settings pane, because
	// naming a deployment is something scripts/setup.sh-style
	// provisioning wants to do as it brings one up -- "grain settings
	// -environment-name=staging" beside the rest of the deployment's
	// configuration, rather than a browser trip afterwards. Empty clears
	// it back to an unnamed deployment, the same way an empty
	// -target-repos clears the allowlist.
	environmentName := fs.String("environment-name", "",
		"what this deployment is called in the UI, e.g. staging -- empty leaves it unnamed and shows nothing")
	// Settable from a shell for the same reason -environment-name is,
	// and rather more so: standing instructions for every run here are
	// exactly the kind of thing a provisioning script writes once, and
	// the kind of thing an operator wants to read back from the host
	// while working out why every run is doing something unexpected.
	// Empty clears it, the same as every other whole-value flag here.
	promptExtension := fs.String("prompt-extension", "",
		"standing instructions appended to every run's prompt on this deployment -- empty adds nothing")
	taskBranchTemplate := fs.String("task-branch-template", "",
		"branch template for new tasks, using {id} and optionally {deployment}, {instance} or {slug} (a few words of the task's title, only at the end after {id} and a hyphen, as in grain/{deployment}/task-{id}-{slug}) -- empty uses grain/task-{id}")
	deploymentID := fs.String("deployment-id", "",
		"this deployment's id, which {deployment} in the branch template expands to -- lowercase letters, digits and hyphens; "+
			"empty uses the random default")
	packageCache := fs.Bool("package-cache", true,
		"whether every sandbox's package managers are pointed at this deployment's package cache")
	packageCacheSizeGB := fs.Int("package-cache-size-gb", 0,
		"maximum package cache size in GiB (0 uses grain's default)")
	// The Actions cache's own two (pkg/actionscache). The switch is
	// worth reading twice before it is flipped on: every branch of a
	// repository shares that repository's entries, so anything one
	// branch's CI writes another branch's CI can read back.
	actionsCache := fs.Bool("actions-cache", true,
		"whether this deployment's CI jobs are handed its GitHub Actions cache, so a "+
			"`cache-from: type=gha` build is warm across runs. Every branch of a repository shares one")
	actionsCacheSizeGB := fs.Int("actions-cache-size-gb", 0,
		"maximum Actions cache size in GiB (0 uses grain's default)")
	// How long this deployment keeps what its own CI recorded
	// (ci.StoredRunRetention). Two bounds, because the records are cheap
	// and the output is not: the first keeps "was this job red last
	// week", the other three keep the log that says why. 0 is grain's
	// own default in each, and a negative number is no bound at all.
	ciRunHistory := fs.Int("ci-run-history", 0,
		"how many concluded grain CI runs keep their record -- id, ref, commit and every verdict "+
			"(0 uses grain's default, negative keeps every run)")
	ciStepOutputMB := fs.Int("ci-step-output-mb", 0,
		"ceiling on the grain CI step output kept across the whole history, in MiB -- whole runs' output is "+
			"swept oldest first until it fits (0 uses grain's default, negative keeps every byte)")
	ciStepOutputDays := fs.Int("ci-step-output-days", 0,
		"how many days a grain CI run's step output may live, whatever room there is under the ceiling "+
			"(0 uses grain's default, negative bounds it by size alone)")
	ciRunOutputKB := fs.Int("ci-run-output-kb", 0,
		"largest amount of step output one grain CI run may store, in KiB (0 uses grain's default)")
	// What CI's two directories on the data disk may occupy
	// (ci.StorageBounds). These were the -ci-cache-max-* and
	// -ci-artifact-max-* daemon flags until grain/task-317, and they take
	// 0 for grain's own default and a positive bound -- but no negative
	// one: a directory on the volume this deployment dispatches out of
	// has no unbounded reading, and the daemon refuses to store one.
	ciCacheMB := fs.Int("ci-cache-mb", 0,
		"ceiling on the whole grain CI job cache directory, in MiB -- the oldest entries are swept until it "+
			"fits (0 uses grain's default)")
	ciCacheEntries := fs.Int("ci-cache-entries", 0,
		"ceiling on how many entries the grain CI job cache holds, alongside -ci-cache-mb (0 uses grain's default)")
	ciCacheDays := fs.Int("ci-cache-days", 0,
		"how many days an unasked-for grain CI job result is kept before the cache's sweep drops it "+
			"(0 uses grain's default)")
	ciArtifactMB := fs.Int("ci-artifact-mb", 0,
		"ceiling on the whole grain CI artifact directory, in MiB -- the oldest runs' artifacts are swept "+
			"whole until it fits (0 uses grain's default)")
	ciArtifactRunMB := fs.Int("ci-artifact-run-mb", 0,
		"ceiling on what one grain CI run's artifacts may come to, in MiB (0 uses grain's default)")
	ciArtifactFileMB := fs.Int("ci-artifact-file-mb", 0,
		"largest single grain CI artifact kept, in MiB -- one past it is refused inside the job's own sandbox, "+
			"so it is never copied out (0 uses grain's default)")
	ciArtifactDays := fs.Int("ci-artifact-days", 0,
		"how many days a grain CI run's artifacts are kept before the sweep drops them (0 uses grain's default)")
	ciJobsPerRun := fs.Int("ci-jobs-per-run", 0,
		"maximum grain CI jobs one run may hold sandboxes for at once (0 sets no per-run limit)")
	// How this deployment orders the CI jobs it has no capacity for
	// (ci.SharedGate, grain/task-325). Durations rather than counts, and
	// empty rather than 0 for "grain's own", because these are the one
	// pair here where a bare 0 would read as a window of no length.
	ciAgeBoost := fs.String("ci-age-boost", "",
		"how long a queueing grain CI job waits to gain one step of rank, as a Go duration -- the floor "+
			"that stops lower-ranked work starving (empty uses grain's default)")
	ciReserveAfter := fs.String("ci-reserve-after", "",
		"how long the sandbox budget must have been refusing a grain CI job before capacity is held for it "+
			"rather than given to smaller jobs, as a Go duration (empty uses grain's default, negative turns "+
			"reservation off)")
	// Accepted and ignored: there are no system admins any more. Whoever
	// holds the access code is the deployment's owner
	// (docs/access-code.md), and an older installer still passes it.
	addAdmin := fs.String("add-admin", "",
		"removed and ignored: whoever holds the access code is the deployment's owner")
	// Not a setting: the one flag here that changes nothing and asks a
	// question instead. It belongs on this command anyway, because the
	// answer it gives is about the same capability listing this command
	// already prints, and because "ready" on that listing means
	// configured -- see cmdCheckCapability below.
	checkCapability := fs.String("check-capability", "",
		"test one capability's standing credential against the service that issued it, and print what came back -- changes nothing")
	// The same question about the other kind of credential this command
	// already reports on: an agent framework's own key, which this
	// listing can likewise only say is *set*. See checkAgentCredential.
	checkAgentKey := fs.String("check-agent-key", "",
		"test one agent framework's credential (antigravity, claude, codex) against the vendor that issued it, "+
			"and print what came back -- changes nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	if *checkCapability != "" {
		return checkCapabilityCredential(ctx, c, out, *checkCapability)
	}
	if *checkAgentKey != "" {
		return checkAgentCredential(ctx, c, out, *checkAgentKey)
	}

	var req ui.UpdateSettingsRequest
	var overcommitEdits []func(*model.OvercommitSettings)
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "poll-interval":
			v := *pollInterval
			req.PollInterval = &v
		case "max-workers":
			v := *maxWorkers
			req.MaxWorkers = &v
		case "max-mergers":
			out.note(fmt.Sprintf("-%s no longer sets anything: this deployment's sandbox budget is what "+
				"bounds the host", f.Name))
		case "max-merges-per-repo":
			v := *maxMergesPerRepo
			req.MaxMergesPerRepo = &v
		case "merge-batch-size", "merge-gather-window":
			out.note(fmt.Sprintf("-%s no longer sets anything: the merge queue no longer batches "+
				"(grain/task-1023)", f.Name))
		case "max-ci-jobs":
			out.note("-max-ci-jobs no longer sets anything: this deployment's sandbox budget is what " +
				"bounds the host")
		case "agent-framework", "gemini-model", "gemini-effort", "claude-model", "codex-model":
			out.note("-" + f.Name + " no longer sets anything: the deployment-wide framework and model settings " +
				"are gone, and a run's model is chosen through model connections (Settings -> Models)")
		case "max-agent-turns":
			v := *maxAgentTurns
			req.MaxAgentTurns = &v
		case "github-host":
			v := *githubHost
			req.GitHubHost = &v
		case "github-insecure-http":
			v := githubInsecureHTTP
			req.GitHubInsecureHTTP = &v
		case "gcp-project":
			v := *gcpProject
			req.GCPProject = &v
		case "gcp-agent-service-account":
			v := *gcpServiceAccountEmail
			req.GCPServiceAccountEmail = &v
		case "gcp-agent-account-count":
			v := *gcpServiceAccountCount
			req.GCPServiceAccountCount = &v
		case "iap-backend-service":
			v := *iapBackendService
			req.IAPBackendService = &v
		case "iap-resource":
			v := *iapResource
			req.IAPResource = &v
		case "sandbox-backend":
			v := *sandboxBackend
			req.SandboxBackend = &v
		case "sandbox-cpus":
			v := *sandboxCPUs
			req.SandboxCPUs = &v
		case "sandbox-memory-mb":
			v := *sandboxMemoryMB
			req.SandboxMemoryMB = &v
		case "sandbox-disk-gb":
			v := *sandboxDiskGB
			req.SandboxDiskGB = &v
		case "sandbox-cpu-overcommit":
			v := *sandboxCPUOvercommit
			req.SandboxCPUOvercommit = &v
		case "sandbox-memory-overcommit":
			v := *sandboxMemoryOvercommit
			req.SandboxMemoryOvercommit = &v
		case "sandbox-disk-overcommit":
			v := *sandboxDiskOvercommit
			req.SandboxDiskOvercommit = &v
		case "sandbox-overcommit":
			v := *sandboxOvercommitMode
			overcommitEdits = append(overcommitEdits, func(oc *model.OvercommitSettings) { oc.Mode = v })
		case "sandbox-overcommit-mode":
			v := *sandboxOvercommitModeAlias
			overcommitEdits = append(overcommitEdits, func(oc *model.OvercommitSettings) { oc.Mode = v })
		case "sandbox-overcommit-cpu-min":
			v := *sandboxOvercommitCPUMin
			overcommitEdits = append(overcommitEdits, func(oc *model.OvercommitSettings) { oc.CPUMin = v })
		case "sandbox-overcommit-cpu-max":
			v := *sandboxOvercommitCPUMax
			overcommitEdits = append(overcommitEdits, func(oc *model.OvercommitSettings) { oc.CPUMax = v })
		case "sandbox-overcommit-memory-min":
			v := *sandboxOvercommitMemoryMin
			overcommitEdits = append(overcommitEdits, func(oc *model.OvercommitSettings) { oc.MemoryMin = v })
		case "sandbox-overcommit-memory-max":
			v := *sandboxOvercommitMemoryMax
			overcommitEdits = append(overcommitEdits, func(oc *model.OvercommitSettings) { oc.MemoryMax = v })
		case "sandbox-overcommit-cpu-pressure-target":
			v := *sandboxOvercommitCPUPressureTarget
			overcommitEdits = append(overcommitEdits, func(oc *model.OvercommitSettings) { oc.CPUPressureTarget = v })
		case "sandbox-overcommit-memory-pressure-target":
			v := *sandboxOvercommitMemoryPressureTarget
			overcommitEdits = append(overcommitEdits, func(oc *model.OvercommitSettings) { oc.MemoryPressureTarget = v })
		case "sandbox-memory-reserve-mb":
			v := *sandboxMemoryReserveMB
			req.SandboxMemoryReserveMB = &v
		case "sandbox-disk-floor-share":
			v := *sandboxDiskFloorShare
			req.SandboxDiskFloorShare = &v
		case "sandbox-disk-floor-min-mb":
			v := *sandboxDiskFloorMinMB
			req.SandboxDiskFloorMinMB = &v
		case "network-access":
			v := *networkAccess
			req.NetworkAccess = &v
		case "sandbox-image":
			v := *sandboxImage
			req.SandboxImage = &v
		case "sandbox-image-mirror":
			v := *sandboxImageMirror
			req.SandboxImageMirror = &v
		case "session-sandbox-idle-ttl":
			v := *sessionSandboxIdleTTL
			req.SessionSandboxIdleTTL = &v
		case "k8s-namespace":
			v := *k8sNamespace
			req.K8sNamespace = &v
		case "k8s-kubectl":
			v := *k8sKubectl
			req.K8sKubectl = &v
		case "k8s-sandbox-image":
			v := *k8sSandboxImage
			req.K8sSandboxImage = &v
		case "k8s-granule-image":
			v := *k8sGranuleImage
			req.K8sGranuleImage = &v
		case "k8s-workspace":
			v := *k8sWorkspace
			req.K8sWorkspace = &v
		case "k8s-git-proxy-host":
			v := *k8sGitProxyHost
			req.K8sGitProxyHost = &v
		case "k8s-sandbox-service":
			v := *k8sSandboxService
			req.K8sSandboxService = &v
		case "k8s-sandbox-service-account":
			v := *k8sServiceAccount
			req.K8sServiceAccount = &v
		case "k8s-node-selector":
			v := splitRepoList(*k8sNodeSelector)
			req.K8sNodeSelector = &v
		case "k8s-sandbox-privileged":
			v := *k8sPrivileged
			req.K8sPrivileged = &v
		case "k8s-network-policy":
			v := *k8sNetworkPolicy
			req.K8sNetworkPolicy = &v
		case "k8s-deployment":
			v := *k8sDeployment
			req.K8sDeployment = &v
		case "k8s-container":
			v := *k8sContainer
			req.K8sContainer = &v
		case "k8s-priority-class-interactive":
			v := *k8sPriorityInteractive
			req.K8sPriorityClassInteractive = &v
		case "k8s-priority-class-agent":
			v := *k8sPriorityAgent
			req.K8sPriorityClassAgent = &v
		case "k8s-priority-class-ci":
			v := *k8sPriorityCI
			req.K8sPriorityClassCI = &v
		case "environment-name":
			v := *environmentName
			req.EnvironmentName = &v
		case "prompt-extension":
			v := *promptExtension
			req.PromptExtension = &v
		case "task-branch-template":
			v := *taskBranchTemplate
			req.TaskBranchTemplate = &v
		case "deployment-id":
			v := *deploymentID
			req.DeploymentID = &v
		case "package-cache":
			v := *packageCache
			req.PackageCacheEnabled = &v
		case "package-cache-size-gb":
			v := *packageCacheSizeGB
			req.PackageCacheSizeGB = &v
		case "actions-cache":
			v := *actionsCache
			req.ActionsCacheEnabled = &v
		case "actions-cache-size-gb":
			v := *actionsCacheSizeGB
			req.ActionsCacheSizeGB = &v
		case "ci-run-history":
			v := *ciRunHistory
			req.CIRunHistory = &v
		case "ci-step-output-mb":
			v := *ciStepOutputMB
			req.CIStepOutputMB = &v
		case "ci-step-output-days":
			v := *ciStepOutputDays
			req.CIStepOutputDays = &v
		case "ci-run-output-kb":
			v := *ciRunOutputKB
			req.CIRunOutputKB = &v
		case "ci-cache-mb":
			v := *ciCacheMB
			req.CICacheMB = &v
		case "ci-cache-entries":
			v := *ciCacheEntries
			req.CICacheEntries = &v
		case "ci-cache-days":
			v := *ciCacheDays
			req.CICacheDays = &v
		case "ci-artifact-mb":
			v := *ciArtifactMB
			req.CIArtifactMB = &v
		case "ci-artifact-run-mb":
			v := *ciArtifactRunMB
			req.CIArtifactRunMB = &v
		case "ci-artifact-file-mb":
			v := *ciArtifactFileMB
			req.CIArtifactFileMB = &v
		case "ci-artifact-days":
			v := *ciArtifactDays
			req.CIArtifactDays = &v
		case "ci-jobs-per-run":
			v := *ciJobsPerRun
			req.CIJobsPerRun = &v
		case "ci-age-boost":
			v := *ciAgeBoost
			req.CIAgeBoost = &v
		case "ci-reserve-after":
			v := *ciReserveAfter
			req.CIReserveAfter = &v
		case "default-capabilities":
			// splitRepoList is a comma-separated list with "" meaning
			// none, which is exactly what this needs too -- an empty
			// -default-capabilities is how an operator turns the default
			// set back off.
			v := splitRepoList(*defaultCapabilities)
			req.DefaultCapabilities = &v
		}
	})

	if name := strings.TrimSpace(*addAdmin); name != "" {
		out.note(fmt.Sprintf("-add-admin %s is ignored: there are no system admins, whoever holds the access code is the owner", name))
		if fs.NFlag() == 1 {
			current, err := c.GetSettings(ctx)
			if err != nil {
				return err
			}
			out.settings(current)
			return nil
		}
	}

	if fs.NFlag() == 0 {
		settings, err := c.GetSettings(ctx)
		if err != nil {
			return err
		}
		out.settings(settings)
		return nil
	}
	if len(overcommitEdits) > 0 {
		var oc model.OvercommitSettings
		if current, err := c.GetSettings(ctx); err == nil {
			oc = current.SandboxOvercommit
		}
		for _, apply := range overcommitEdits {
			apply(&oc)
		}
		req.SandboxOvercommit = &oc
	}
	settings, err := c.UpdateSettings(ctx, req)
	if err != nil {
		return err
	}
	out.settings(settings)
	return nil
}

// splitRepoList parses a comma-separated owner/name list -- "" means
// none, unlike strings.Split's own [""], since an empty -target-repos is
// how an operator clears the restriction back to unrestricted.
//
// Empty is an empty slice rather than a nil one, and that distinction is
// the whole difference between clearing the list and doing nothing at
// all: these values are sent over REST, where a *[]string pointing at a
// nil slice marshals to `null`, and a `null` unmarshals back into the
// pointer the server reads as "field omitted, leave it alone". So a nil
// here made `grain settings -target-repos ""` a command that printed the
// unchanged list back and said nothing -- with no way to unlock a
// deployment from a shell once its allowlist had been narrowed
// (grain/task-399). parseRepoCapabilities in repo.go has always turned
// this nil into an empty slice for the same reason; now it does not have
// to.
func splitRepoList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// checkCapabilityCredential is `grain settings -check-capability <id>`:
// it asks the daemon to authenticate as that capability's standing
// credential and make one cheap, harmless call with it, then prints what
// came back.
//
// The line above it -- `grain settings`' own capability listing -- can
// only ever say "ready", meaning this deployment has the settings and
// the secrets for it. Whether the key inside one of those secrets is
// still one GCP (or GitHub) will accept is a question no stored
// configuration can answer, and the whole of README.md's "Debugging
// `gcp-key` again" is what it costs to find out from a failed task
// instead. This is that question, asked from the host, where whoever is
// reading the failed task's error is usually already standing.
//
// A refused credential is a successful call that prints a bad answer,
// not a non-zero exit: nothing about the *command* failed, and the
// sentence it prints names the secret to replace. The exit code stays
// for the calls that could not be made at all -- an unknown capability,
// a daemon that does not offer this.
func checkCapabilityCredential(ctx context.Context, c *ui.HTTPClient, out *printer, id string) error {
	check, err := c.CheckCapability(ctx, id)
	if err != nil {
		return err
	}
	out.capabilityCheck(check)
	return nil
}

// checkAgentCredential is `grain settings -check-agent-key <framework>`:
// the same question one line down, asked of the credential a run is
// dispatched with rather than of a capability's.
//
// `grain settings` prints which agent credentials are set, and "set" is
// the whole of what the store knows: a Gemini key revoked in the console,
// a Claude Code OAuth token whose session was ended, an OpenAI key
// deleted by whoever else holds that account -- all of them leave the
// stored secret exactly where it was, so grain reports it present and the
// next dispatched run is what discovers otherwise, several minutes into
// somebody's task. This authenticates as it and lists the models it can
// see.
//
// A refused credential is a successful command printing a bad answer,
// exactly as it is for -check-capability: nothing about the command
// failed, and the sentence names the secret to replace. A non-zero exit
// stays for the calls that could not be made at all -- a framework grain
// does not have, a daemon not offering this.
func checkAgentCredential(ctx context.Context, c *ui.HTTPClient, out *printer, framework string) error {
	check, err := c.CheckAgentKey(ctx, framework)
	if err != nil {
		return err
	}
	out.agentKeyCheck(check)
	return nil
}
