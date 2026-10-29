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

// sync.go is `grain sync`, bwsalmon/agents#358: the command a GitHub
// Action calls, against a live deployment, whenever a config repo's
// checked-in configuration changes -- the reconciliation half of "add
// commands to the grain cli to allow us to bootstrap external
// infrastructure on a new installation, and the ability to run a syncing
// command in a github action when code or configurations change."
//
// It is a mode of its own (main.go's switch, next to daemon/mcpserver
// and setup), not one of runCLI's task-store commands, mainly so a
// "gcp"-only sync -- one with no "settings" section -- never needs
// -server, or anything else about a running daemon, at all.
//
// `grain sync -config path.json` reads one file with up to two
// independent, optional sections:
//
//   - "settings" is unmarshaled straight into ui.UpdateSettingsRequest
//     and applied through HTTPClient.UpdateSettings, against -server, the
//     same running daemon `grain settings` itself talks to (bwsalmon/
//     agents#363: there is no store flag here to open the database
//     directly with any more) -- sync's whole value over that is that the
//     settings live as one reviewable file in a config repo instead of a
//     workflow step's own imperative flag list, and
//     UpdateSettingsRequest's pointer fields already give "only the keys
//     present in the file change anything" for free, no extra work here.
//     Under -dry-run it goes to PlanSettings instead (grain/task-241),
//     the same request through the same validation with the write left
//     off, and prints the same diff -- so a pull-request check can say
//     what a file would do to the settings, which is the section with
//     the wider blast radius of the two it can plan.
//   - "gcp" re-runs pkg/gcpsetup.EnsureInfrastructure, the same
//     reconciliation `grain setup gcp` uses for a brand new installation
//     (setup.go's own doc comment) -- run here on every sync instead of
//     once at install time, so IAM drift (a binding removed by hand, a
//     newly-enabled gemini-key capability that needs a grant it didn't
//     before) gets repaired automatically. It never mints a new minter
//     key -- gcpsetup.Options.MintMinterKey's own doc comment explains
//     why that stays a one-time, explicit `grain setup gcp -mint-key`.
//
// Both sections use the exact same "safe to re-run, nothing to do if
// nothing changed" discipline scripts/setup.sh already holds itself to,
// so pointing a workflow at `grain sync` on every push touching the
// config file is the whole of what "run this in a GitHub Action when code
// or configurations change" needs -- see this package's own README
// section (README.md) for the workflow shape and how it reaches a
// deployment whose UI/API is bound to loopback only.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"google.golang.org/api/option"

	"github.com/ai-on-gke/grain/pkg/gcpsetup"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// syncConfig is grain sync's -config file shape -- see this file's own
// doc comment for what each section does.
type syncConfig struct {
	Settings *ui.UpdateSettingsRequest `json:"settings"`
	GCP      *syncGCPConfig            `json:"gcp"`
}

// syncGCPConfig names the same knobs `grain setup gcp` takes as flags,
// as JSON fields instead -- gcpsetup.Options restated for this file's
// shape (see gcpsetup.Options's own doc comment for what each one does).
type syncGCPConfig struct {
	Project           string `json:"project"`
	CredentialsFile   string `json:"credentialsFile"`
	AgentAccountID    string `json:"agentAccountId"`
	AgentAccountCount int    `json:"agentAccountCount"`
	MinterAccountID   string `json:"minterAccountId"`
	EnableGeminiKey   bool   `json:"enableGeminiKey"`
}

func syncCmd(args []string) {
	if err := cmdSync(context.Background(), args); err != nil {
		fmt.Fprintln(os.Stderr, "grain sync: "+err.Error())
		os.Exit(1)
	}
}

func cmdSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("grain sync", flag.ExitOnError)
	configPath := fs.String("config", "",
		"path to a JSON file with \"settings\" and/or \"gcp\" sections to reconcile (required)")
	server := fs.String("server", serverDefault(),
		"base URL of a running \"grain daemon\"'s UI/API -- only needed if -config has a \"settings\" section")
	dryRun := fs.Bool("dry-run", false,
		"report what the \"settings\" section would change and write nothing -- \"gcp\" has no "+
			"plan mode and is skipped, so a dry run of a whole config file changes nothing at all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return errors.New("-config is required")
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("reading -config: %w", err)
	}
	var cfg syncConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parsing -config: %w", err)
	}
	if cfg.Settings == nil && cfg.GCP == nil {
		return errors.New("-config has none of a \"settings\" or \"gcp\" section -- nothing to sync")
	}

	// The one section with no plan mode is skipped rather than refused:
	// a config repo's pull-request check runs `grain sync -dry-run`
	// against the whole file it will later apply, and erroring out on a
	// file that happens to carry a "gcp" section too would leave that
	// check with nothing to say about the two sections it can plan. It
	// says out loud that it was skipped, since a run that reported only
	// the other two would read as a plan for the whole file.
	if cfg.GCP != nil {
		if *dryRun {
			fmt.Println("gcp: skipped -- -dry-run writes nothing, and reconciling GCP infrastructure has no plan mode")
		} else if err := syncGCP(ctx, *cfg.GCP); err != nil {
			return fmt.Errorf("reconciling GCP infrastructure: %w", err)
		}
	}

	if cfg.Settings != nil {
		// A single-tenant daemon trusts no loopback caller, so this
		// needs the local API token as much as runCLI does.
		useCLIEnvDataDir()
		if err := syncSettings(ctx, *cfg.Settings, *server, *dryRun); err != nil {
			return fmt.Errorf("%s settings: %w", verbFor(*dryRun, "applying", "planning"), err)
		}
	}

	return nil
}

func syncGCP(ctx context.Context, gc syncGCPConfig) error {
	if gc.Project == "" {
		return errors.New("gcp.project is required")
	}
	var opts []option.ClientOption
	if gc.CredentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(gc.CredentialsFile))
	}
	admin, err := gcpsetup.NewRealAdmin(ctx, gc.Project, opts...)
	if err != nil {
		return err
	}
	result, err := gcpsetup.EnsureInfrastructure(ctx, admin, gcpsetup.Options{
		ProjectID:         gc.Project,
		AgentAccountID:    gc.AgentAccountID,
		AgentAccountCount: gc.AgentAccountCount,
		MinterAccountID:   gc.MinterAccountID,
		EnableGeminiKey:   gc.EnableGeminiKey,
	})
	if err != nil {
		return err
	}
	printSetupResult(result)
	fmt.Println()
	return nil
}

// syncSettings applies the "settings" section and prints what it
// changed -- or, under -dry-run, prints what applying it would change
// and writes nothing.
//
// The plan is the daemon's own (ui.HTTPClient.PlanSettings, POST
// /api/settings/plan): the same request, through the same validation
// and normalization a save runs, answered with the settings a save
// would leave behind. It is deliberately not a comparison worked out
// here between the values in the file and the ones GetSettings reports,
// which is the obvious way to plan a read-compare-write section and the
// wrong one for this one -- every value here is the daemon's to decide.
// "1m" is stored and read back as "60s", a browser URL pasted into
// targetRepos becomes owner/name, a name is trimmed, an upgrade check
// mode is lower-cased, "gemini" becomes the framework it now means; a
// local comparison would report each of those as a change that never
// happens, and would report nothing at all about a value the daemon
// would refuse. A plan a reviewer cannot trust is worse than the
// "skipped" line this replaced (grain/task-180).
func syncSettings(ctx context.Context, req ui.UpdateSettingsRequest, server string, dryRun bool) error {
	c := ui.NewHTTPClient(server)

	before, err := c.GetSettings(ctx)
	if err != nil {
		return err
	}
	var after ui.Settings
	if dryRun {
		after, err = c.PlanSettings(ctx, req)
	} else {
		after, err = c.UpdateSettings(ctx, req)
	}
	if err != nil {
		return settingsPlanError(err, dryRun)
	}
	printSettingsDiff(before, after, dryRun)
	return nil
}

// settingsPlanError explains the one failure that means the deployment
// rather than the file is at fault: a daemon old enough not to route
// POST /api/settings/plan at all, which answers 404 and arrives here as
// a *ui.NotFoundError. Nothing was written -- that is the whole reason
// the plan is a route of its own rather than a parameter an older
// daemon would ignore and save through (ui.Server.handlePlanSettings) --
// but "not found" on its own would read as a config file naming
// something that does not exist.
func settingsPlanError(err error, dryRun bool) error {
	var notFound *ui.NotFoundError
	if !dryRun || !errors.As(err, &notFound) {
		return err
	}
	return fmt.Errorf("this deployment's daemon has no plan mode for settings (it predates "+
		"POST /api/settings/plan), so a dry run cannot say what the \"settings\" section would change -- "+
		"upgrade the daemon, or dry-run a file with no \"settings\" section: %w", err)
}

// settingsDiffRow is one line printSettingsDiff can print: which setting
// a config file changed, what to call it, and how to read it back off the
// settings the daemon reported before and after.
type settingsDiffRow struct {
	// field is the ui.UpdateSettingsRequest JSON name of the setting this
	// row reports -- the very key a config file's "settings" section
	// spells it with. It is what keeps the table honest rather than
	// merely well-intentioned: settingsDiffRowsCoverEveryUpdatableSetting
	// reflects over that struct and fails naming any field no row here
	// claims, so a new setting cannot become syncable without a line of
	// output to go with it.
	field string
	// name is what a workflow log calls the setting -- prose, not the
	// JSON key, since this is read by whoever is looking at the run.
	name string
	// value reads the setting out of one side of the diff. Printed and
	// compared through fmt.Sprint, so a row may return whatever type the
	// setting is (or a string it has composed, as the two list settings
	// below do).
	value func(ui.Settings) any
}

// settingsDiffRows is every setting `grain sync` can change, in the order
// a diff prints them.
//
// Keyed by ui.UpdateSettingsRequest's own JSON names because that struct
// is already the definition of "what a config file can set": syncSettings
// hands UpdateSettings the whole request the file carries, so anything
// with a field there is syncable whether or not anyone remembered this
// table. It was a hand-written list of names for long enough to go
// silently stale twice -- the sandbox VM shape (bwsalmon/agents#534)
// first, then seven more at once (target repos, default capabilities,
// agent framework, newest first, show closed by default, approved by
// default and auto merge by default) -- and the cost each time was the
// worst one this command has: `grain sync` narrowing a deployment's
// target repos, or turning auto-merge on for every task filed after it,
// and printing "already up to date, nothing changed". A sync that changes
// something has to say what.
var settingsDiffRows = []settingsDiffRow{
	{"environmentName", "environment name", func(s ui.Settings) any { return s.EnvironmentName }},
	{"pollInterval", "poll interval", func(s ui.Settings) any { return s.PollInterval }},
	{"maxWorkers", "max workers", func(s ui.Settings) any { return s.MaxWorkers }},
	{"maxMergesPerRepo", "max merges per repo", func(s ui.Settings) any { return s.MaxMergesPerRepo }},
	{"mergeBatchSize", "merge batch size", func(s ui.Settings) any { return s.MergeBatchSize }},
	{"mergeGatherWindow", "merge gather window", func(s ui.Settings) any { return s.MergeGatherWindow }},
	{"maxAutoSubmitDepth", "max auto-submit depth", func(s ui.Settings) any { return s.MaxAutoSubmitDepth }},
	{"maxAutoSubmitFanOut", "max auto-submit fan-out", func(s ui.Settings) any { return s.MaxAutoSubmitFanOut }},
	{"defaultModel", "default model", func(s ui.Settings) any { return s.DefaultModel }},
	{"maxAgentTurns", "max agent turns", func(s ui.Settings) any { return s.MaxAgentTurns }},
	{"githubHost", "github host", func(s ui.Settings) any { return s.GitHubHost }},
	{"githubInsecureHttp", "github insecure http", func(s ui.Settings) any { return s.GitHubInsecureHTTP }},
	{"gcpProject", "gcp project", func(s ui.Settings) any { return s.GCPProject }},
	{"gcpServiceAccountEmail", "gcp agent service account", func(s ui.Settings) any { return s.GCPServiceAccountEmail }},
	{"gcpServiceAccountCount", "gcp agent service account count", func(s ui.Settings) any { return s.GCPServiceAccountCount }},
	{"iapBackendService", "iap backend service", func(s ui.Settings) any { return s.IAPBackendService }},
	{"iapResource", "iap resource", func(s ui.Settings) any { return s.IAPResource }},
	{"sandboxBackend", "sandbox backend", func(s ui.Settings) any { return s.SandboxBackend }},
	{"sandboxCpus", "sandbox cpus", func(s ui.Settings) any { return s.SandboxCPUs }},
	{"sandboxMemoryMb", "sandbox memory mb", func(s ui.Settings) any { return s.SandboxMemoryMB }},
	{"sandboxDiskGb", "sandbox disk gb", func(s ui.Settings) any { return s.SandboxDiskGB }},
	{"sandboxCpuOvercommit", "sandbox cpu overcommit", func(s ui.Settings) any { return s.SandboxCPUOvercommit }},
	{"sandboxMemoryOvercommit", "sandbox memory overcommit", func(s ui.Settings) any { return s.SandboxMemoryOvercommit }},
	{"sandboxDiskOvercommit", "sandbox disk overcommit", func(s ui.Settings) any { return s.SandboxDiskOvercommit }},
	{"sandboxMemoryReserveMb", "sandbox memory reserve mb", func(s ui.Settings) any { return s.SandboxMemoryReserveMB }},
	// The overcommit mode and its bounds, as the one JSON object they are
	// stored as, so a change to any of them is one row.
	{"sandboxOvercommit", "sandbox overcommit", func(s ui.Settings) any {
		b, _ := json.Marshal(s.SandboxOvercommit)
		return string(b)
	}},
	{"externalAgentApi", "external agent api", func(s ui.Settings) any { return s.ExternalAgentAPI }},
	{"sandboxDiskFloorShare", "sandbox disk floor share", func(s ui.Settings) any { return s.SandboxDiskFloorShare }},
	{"sandboxDiskFloorMinMb", "sandbox disk floor min mb", func(s ui.Settings) any { return s.SandboxDiskFloorMinMB }},
	{"networkAccess", "sandbox network", func(s ui.Settings) any { return s.NetworkAccess }},
	// Printed the same %q way as everything else despite being the one
	// setting here that can be several lines long: a diff is where the
	// change is worth seeing whole, and %q keeps a multi-line value on one
	// line of output rather than breaking the two-column shape of every
	// line around it.
	{"promptExtension", "prompt extension", func(s ui.Settings) any { return s.PromptExtension }},
	{"taskBranchTemplate", "task branch template", func(s ui.Settings) any { return s.TaskBranchTemplate }},
	{"deploymentId", "deployment id", func(s ui.Settings) any { return s.DeploymentID }},
	{"repairTemplateID", "repair template", func(s ui.Settings) any { return s.RepairTemplateID }},
	// Multi-line for the same reason the prompt extension above is, and
	// printed the same %q way: it is a shell script, and a sync that
	// replaced what runs inside every sandbox on this deployment has to
	// say so on one line of a workflow log like every other change here.
	{"sandboxImage", "sandbox image", func(s ui.Settings) any { return s.SandboxImage }},
	{"sandboxImageMirror", "sandbox image mirror", func(s ui.Settings) any { return s.SandboxImageMirror }},
	{"k8sNamespace", "k8s namespace", func(s ui.Settings) any { return s.K8sNamespace }},
	{"k8sKubectl", "k8s kubectl", func(s ui.Settings) any { return s.K8sKubectl }},
	{"k8sSandboxImage", "k8s sandbox image", func(s ui.Settings) any { return s.K8sSandboxImage }},
	{"k8sGranuleImage", "k8s granule image", func(s ui.Settings) any { return s.K8sGranuleImage }},
	{"k8sWorkspace", "k8s workspace", func(s ui.Settings) any { return s.K8sWorkspace }},
	{"k8sGitProxyHost", "k8s git proxy host", func(s ui.Settings) any { return s.K8sGitProxyHost }},
	{"k8sSandboxService", "k8s sandbox service", func(s ui.Settings) any { return s.K8sSandboxService }},
	{"k8sServiceAccount", "k8s service account", func(s ui.Settings) any { return s.K8sServiceAccount }},
	{"k8sNodeSelector", "k8s node selector", func(s ui.Settings) any { return strings.Join(s.K8sNodeSelector, ", ") }},
	{"k8sPrivileged", "k8s privileged", func(s ui.Settings) any { return s.K8sPrivileged }},
	{"k8sNetworkPolicy", "k8s network policy", func(s ui.Settings) any { return s.K8sNetworkPolicy }},
	{"k8sDeployment", "k8s deployment", func(s ui.Settings) any { return s.K8sDeployment }},
	{"k8sContainer", "k8s container", func(s ui.Settings) any { return s.K8sContainer }},
	{"k8sPriorityClassInteractive", "k8s priority class interactive", func(s ui.Settings) any { return s.K8sPriorityClassInteractive }},
	{"k8sPriorityClassAgent", "k8s priority class agent", func(s ui.Settings) any { return s.K8sPriorityClassAgent }},
	{"k8sPriorityClassCi", "k8s priority class ci", func(s ui.Settings) any { return s.K8sPriorityClassCI }},
	{"packageCacheEnabled", "package cache", func(s ui.Settings) any { return s.PackageCacheEnabled }},
	{"packageCacheSizeGb", "package cache size gb", func(s ui.Settings) any { return s.PackageCacheSizeGB }},
	{"actionsCacheEnabled", "actions cache", func(s ui.Settings) any { return s.ActionsCacheEnabled }},
	{"actionsCacheSizeGb", "actions cache size gb", func(s ui.Settings) any { return s.ActionsCacheSizeGB }},
	{"ciRunHistory", "ci run history", func(s ui.Settings) any { return s.CIRunHistory }},
	{"ciStepOutputMb", "ci step output mb", func(s ui.Settings) any { return s.CIStepOutputMB }},
	{"ciStepOutputDays", "ci step output days", func(s ui.Settings) any { return s.CIStepOutputDays }},
	{"ciRunOutputKb", "ci run output kb", func(s ui.Settings) any { return s.CIRunOutputKB }},
	{"ciCacheMb", "ci cache mb", func(s ui.Settings) any { return s.CICacheMB }},
	{"ciCacheEntries", "ci cache entries", func(s ui.Settings) any { return s.CICacheEntries }},
	{"ciCacheDays", "ci cache days", func(s ui.Settings) any { return s.CICacheDays }},
	{"ciArtifactMb", "ci artifact mb", func(s ui.Settings) any { return s.CIArtifactMB }},
	{"ciArtifactRunMb", "ci artifact run mb", func(s ui.Settings) any { return s.CIArtifactRunMB }},
	{"ciArtifactFileMb", "ci artifact file mb", func(s ui.Settings) any { return s.CIArtifactFileMB }},
	{"ciArtifactDays", "ci artifact days", func(s ui.Settings) any { return s.CIArtifactDays }},
	{"ciJobsPerRun", "ci jobs per run", func(s ui.Settings) any { return s.CIJobsPerRun }},
	{"ciAgeBoost", "ci queue age boost", func(s ui.Settings) any { return s.CIAgeBoost }},
	{"ciReserveAfter", "ci capacity reserved after", func(s ui.Settings) any { return s.CIReserveAfter }},
	{"defaultCapabilities", "default capabilities", func(s ui.Settings) any { return strings.Join(s.DefaultCapabilities, ", ") }},
	{"agentBackends", "agent backends", func(s ui.Settings) any { return fmt.Sprint(s.AgentBackends) }},
	{"newestFirst", "newest first", func(s ui.Settings) any { return s.NewestFirst }},
	{"showClosedByDefault", "show closed by default", func(s ui.Settings) any { return s.ShowClosedByDefault }},
	{"approvedByDefault", "approved by default", func(s ui.Settings) any { return s.ApprovedByDefault }},
	{"autoMergeByDefault", "auto merge by default", func(s ui.Settings) any { return s.AutoMergeByDefault }},
	{"upgradeCheckMode", "upgrade check", func(s ui.Settings) any { return s.UpgradeCheckMode }},
	{"upgradeCheckPausesPipeline", "upgrade check pauses the pipeline",
		func(s ui.Settings) any { return s.UpgradeCheckPausesPipeline }},
	{"autoUpgradeChannel", "automatic upgrade channel", func(s ui.Settings) any { return s.AutoUpgradeChannel }},
	{"autoUpgradeTime", "automatic upgrade time", func(s ui.Settings) any { return s.AutoUpgradeTime }},
	{"timeZone", "time zone", func(s ui.Settings) any { return s.TimeZone }},
}

// settingsDiffExceptions names any ui.UpdateSettingsRequest field that
// deliberately has no row above, and why -- the one place a gap in the
// table is allowed to be recorded, so that a setting missing from the
// diff is a decision somebody wrote a reason for rather than the
// oversight it has twice been.
//
// Empty today: every setting a config file can set is reported back by
// GetSettings, so every one of them has something to diff. A field that
// genuinely has nothing to compare against -- one UpdateSettings accepts
// but ui.Settings never reports back, say -- belongs here with the reason
// it cannot be diffed. The test rejects a stale entry too, so an
// exception outliving the field it excuses fails rather than quietly
// excusing nothing.
var settingsDiffExceptions = map[string]string{}

// printSettingsDiff prints only what actually changed -- a sync run
// against a config file whose "settings" section already matches the
// store (the common case, once a deployment is settled) should say so
// plainly rather than re-printing every field on every run.
// The two sides are read the same way whichever mode this is in: under
// -dry-run, "after" is the settings the daemon says a save would
// produce rather than the ones it produced, so the rows, the comparison
// and the "nothing changed" line are the same work either way. Only the
// header says which of the two happened, so one run's output reads in
// one voice.
func printSettingsDiff(before, after ui.Settings, dryRun bool) {
	changed := false
	for _, row := range settingsDiffRows {
		beforeValue, afterValue := fmt.Sprint(row.value(before)), fmt.Sprint(row.value(after))
		if beforeValue == afterValue {
			continue
		}
		if !changed {
			fmt.Println(verbFor(dryRun, "settings changed:", "settings -- what would change:"))
			changed = true
		}
		fmt.Printf("  %s: %q -> %q\n", row.name, beforeValue, afterValue)
	}
	if !changed {
		fmt.Println("settings: already up to date, nothing changed")
	}
}

func verbFor(dryRun bool, did, would string) string {
	if dryRun {
		return would
	}
	return did
}
