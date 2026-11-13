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

// Package ui is a JSON API (and the static frontend it serves) for
// creating and managing grain tasks and their capability grants.
//
// It reads and writes model.Store directly. That is the whole of the
// change bwsalmon's "the input is via direct model updates" asked for:
// this package used to read and write GitHub issues, deriving a task's
// state from labels and keeping its declared fields (/repo, /base,
// /auto-merge) as directive lines in an issue body, because the
// store-backed intake path was not wired into anything that ran. GitHub
// was the record and this was a view onto it.
//
// Now the store is the record. A task is a row, its state comes from
// model.StateOf's own view rather than a second label-shaped derivation
// that had already drifted (this package used to call the same state
// "needs_approval" that the store calls "proposed", and had an
// "untracked" the store has no notion of), its capabilities are
// model.Grants, and its conversation is model.Comments. Nothing here
// talks to GitHub at all: the pull request a task's run produces is
// recorded on the task as a model.LinkFixes link, so rendering it needs
// no API call.
package ui

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/mergelogins"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/upgrade"
	"github.com/ai-on-gke/grain/pkg/version"

	"github.com/ai-on-gke/grain/pkg/github"
)

// Capability is one attachable capability a human toggles on a task --
// the CAPABILITY-tier rows of v1's labels.py _STYLES table that were
// genuinely human-driven. A deployment can start every new task with
// some of these already ticked (model.Config.DefaultCapabilities), and a
// repo can add more of its own for the tasks that target it
// (model.RepoConfig.DefaultCapabilities). Either changes what the picker
// opens as, not what it can do: every row here is still something a
// human turns on and off per task.
//
// Label is gone from this type. It named the GitHub label that used to
// carry the grant; a grant is a model.Grant row now, and ID is what it
// records. waiting_on_dependency_label stays excluded for the reason it
// always was: labels.py's own doc comment marks it as the one grain
// applies itself, never a human, so it does not belong in a picker a
// human drives.
type Capability struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Ready is CapabilityStatus.Ready for this same capability -- whether
	// this deployment has the settings and secrets a task granted it
	// would actually need -- carried on the picker's own row so ticking
	// one that cannot work says so here rather than at the far end of a
	// dispatch.
	//
	// That gap is what this field is for. The Settings pane has known
	// since bwsalmon/agents#611 that gemini-key on a deployment with no
	// GCP project, or no `gcp-key-minter` secret, is Not ready; the
	// picker a human actually attaches capabilities with knew nothing
	// about it and offered the row regardless. Ticking it filed an
	// ordinary grant, and the first sign of trouble was the task's own
	// run failing with "capability \"gemini-key\" refused: ..." some
	// minutes later -- with the fix being in Settings, two panes away
	// from where the mistake was made.
	//
	// A pointer, and omitted when nil, because "nobody computed this" is
	// a third answer distinct from ready and not-ready:
	// OfferedCapabilities is a static listing with no deployment behind
	// it, and a false there would libel every capability grain ships. Only
	// GET /api/config fills it in. The frontend reads `ready === false`
	// for the same reason CapabilitiesPanel reads `grantable === false`.
	Ready *bool `json:"ready,omitempty"`
	// Needs is CapabilityStatus.MissingConfig and MissingSecrets run
	// together -- every deployment setting and every secret this
	// capability is still short of, in the same human-facing words the
	// Capabilities tab uses, so the picker can name the gap rather than
	// only report that there is one. Empty whenever Ready is nil or true.
	Needs []string `json:"needs,omitempty"`
	// The agent API fields, set on api:<scope|preset> rows only
	// (APICapabilities, agent_api.go) so the picker can draw them as
	// one "Grain API" group rather than as dozens of flat rows.
	// Preset is set on a preset's row; Scopes is what the row grants --
	// a preset's expansion, or the one scope; Dangerous is whether any
	// of those scopes only a person may grant; Summary is the scope or
	// preset's own description, without the boilerplate Description
	// adds around it.
	Preset    bool     `json:"preset,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	Dangerous bool     `json:"dangerous,omitempty"`
	Summary   string   `json:"summary,omitempty"`
}

// OfferedCapabilities is the bare in-process capabilities a human can
// attach to a task, the rows Config.Capabilities starts from before the
// api presets (APICapabilities) are added to it.
//
// It is empty: every legacy in-process provider grain shipped was
// retired onto a capability server method, an api preset or a task
// setting (docs/capabilities.md), and the seam an edition used to
// contribute one, capability.RegisterProvider, went with them (task
// 2876). Capability-server methods are offered from the servers'
// catalogues instead. It stays so that every caller building a
// Config keeps one place to start from.
func OfferedCapabilities() []Capability {
	return []Capability{}
}

// Config is what a Client needs to know about the deployment it fronts.
//
// TaskRepo and the label taxonomy are both gone: there is no task repo to
// file issues in any more, and no labels to derive anything from. What
// replaces them is Actor -- who a task created here is attributed to,
// which a GitHub issue used to answer with its own opening account.
type Config struct {
	// OfferedGitCredentials lists the named GitHub credentials under
	// secrets/github this process started with (non-nil when a credential
	// ladder is configured, even if empty; nil when unconfigured). Used by
	// Settings -> GitHub tokens and per-client repo#credential validation.
	OfferedGitCredentials []string
	// MergeLoginWarnings is the reviewed-merge logins that belong to a
	// user account (pkg/mergelogins), for GET /api/sandboxes' and `grain
	// health`'s warning. nil -- a deployment with no forge client -- says
	// nothing.
	MergeLoginWarnings func(context.Context) []mergelogins.Warning
	// LocalScriptApprovals is where a run's local-script calls wait for a
	// person (LocalScriptApprovals). nil gives the server one of its own,
	// which is all a single daemon needs: the runs that post to it and
	// the people who answer reach the same server.
	LocalScriptApprovals *LocalScriptApprovals
	// Actor is the principal the CLI or UI acts as. A task filed here
	// records it as both origin and approval, the same way a human who
	// could apply the trigger label used to land a task queued rather
	// than proposed.
	Actor model.Principal
	// DefaultTarget is the repo a task with no explicit one targets,
	// mirroring orchestrator.Config.DefaultTarget so a single-repo
	// deployment need not repeat itself on every task.
	DefaultTarget *model.RepoRef
	Capabilities  []Capability
	// IssueImporter imports one repository's open GitHub issues now
	// (Client.ImportIssues). nil -- a deployment with no GitHub client, or
	// a test that wires none -- refuses the manual trigger; the periodic
	// importer runs in the orchestrator either way.
	IssueImporter IssueImporter
	// RepoComparer supplies the read-only half of Grain's native pull
	// request view. It talks in Git refs and unified diff rather than
	// forge-specific pull-request numbers.
	RepoComparer RepoComparer
	// Secrets is set only when this UI runs on the same host as the
	// server whose secrets directory it names -- nil means it does not,
	// and the secrets pane and its API routes report themselves
	// unavailable rather than erroring on every call (bwsalmon/agents#357).
	// It is write/list only in the sense that matters: nothing in this
	// package ever calls Store.Resolve, so a value, once set, is never
	// readable back through here -- only Set, DeleteKey, DeleteSecret and
	// List (which reports names and key names, never values).
	Secrets *secrets.Store
	// CapabilityServers reports the daemon's attached capability servers
	// (pkg/capability/servers) for Settings' Capability servers pane;
	// nil where this process attaches none, and the pane then shows the
	// configuration without a status.
	CapabilityServers CapabilityServerStatuses
	// CodexAuthFile is the controller-owned auth.json the Agents pane may
	// replace or clear. It is a dedicated managed path, never an arbitrary
	// -codex-auth-file supplied by an operator, so a UI action cannot
	// overwrite or delete a file outside Grain's data directory. Empty
	// disables the subscription-login controls.
	CodexAuthFile string
	// CodexAuthFallbackFile is an operator-owned login; the UI only reports
	// its presence and never writes or removes it.
	CodexAuthFallbackFile string
	// Reboot, when set, is what the UI's "reboot host" button
	// (bwsalmon/agents#395) calls to reboot the machine this deployment's
	// daemon is running on. nil means handleRebootHost reports the action
	// unavailable rather than erroring on every call -- the same
	// nil-means-unavailable contract Secrets already gives the secrets
	// pane, and the right default for `grain demo`'s throwaway UI, which
	// has no real machine behind it worth rebooting.
	Reboot func(ctx context.Context) error
	// Shutdown is Reboot's other half (grain/task-40): what the UI's
	// "shut down host" button calls to power the machine off rather than
	// bring it back up. An operator who wants a machine to stop -- a bill
	// to stop running, a host to be left alone until somebody starts it
	// again -- otherwise has to reboot it and race the boot to press
	// something, or find an SSH session after all.
	//
	// It is a separate field rather than an argument to Reboot because a
	// deployment can perfectly well be able to do one and not the other:
	// the host-side units scripts/setup.sh installs are one fixed command
	// each, so "this deployment can power itself off" is its own fact.
	// nil means handleShutdownHost reports it unavailable, the same
	// nil-means-unavailable contract Reboot gives.
	Shutdown func(ctx context.Context) error
	// Restart, when set, is what the UI's "restart grain" button calls to
	// bring this daemon down and back up on its own -- `systemctl restart
	// grain-daemon.service`, which under a container deployment is the
	// grain container and nothing else on the machine (grain/task-40).
	//
	// It is the small hammer of the three: rebooting the host to clear a
	// wedged daemon takes every sandbox running on that host down with it
	// and costs a boot, where what actually needed restarting was one
	// process. The Upgrade button already ends by running exactly this
	// command (Upgrader's own restart step); this offers it on its own,
	// without an upgrade to hang it off.
	//
	// Like Reboot, a successful call cuts the connection it arrived on:
	// the process serving it is the process going away.
	Restart func(ctx context.Context) error
	// Credentials is the same GitHub credential material (secrets/github/)
	// the git proxy loads the credential a git connection names from --
	// given here so the GitHub pane can write a credential's material
	// (SetToken). A *token* waits for a restart to become a capability,
	// since the capability set this process offers is fixed at startup. nil (`grain demo`'s throwaway UI, or any UI
	// not colocated with the proxy that built it) means Settings reports
	// no such gaps rather than erroring, the same nil-means-unavailable
	// contract Secrets and Reboot already give. Without this, the only
	// way to learn targetRepos and the credential ladder have drifted
	// apart is a confusing 500 "no credential configured" from the git
	// proxy on the next push (bwsalmon/agents#427).
	Credentials *gitproxy.CredentialSet
	// GitReach is what each of those credentials can reach on GitHub,
	// listed from GitHub itself and cached (gitproxy.Reach): what
	// CreateTask checks a task's repositories against before filing it,
	// and what the New task pickers offer a team with no repositories
	// attached (grain/task-1850). nil -- `grain demo`, a UI with no
	// GitHub API to ask -- checks nothing and offers no such list.
	GitReach *gitproxy.Reach
	// PackageCache is this deployment's proxy cache
	// (pkg/pkgcache), when the process serving this UI runs one. It is
	// what the rail's Proxy cache page reports the size of and what its
	// flush buttons empty. nil -- `grain demo`, a UI not colocated with a
	// daemon -- means both report themselves unavailable rather than
	// erroring, the same nil-means-unavailable contract Secrets and
	// Reboot above already give.
	PackageCache PackageCache
	// ActionsCache is this deployment's GitHub Actions cache
	// (pkg/actionscache), when the process serving this UI keeps one:
	// what a CI job exports with `cache-from: type=gha`, kept on the
	// controller rather than in the sandbox that is destroyed when the
	// job ends. It is what the Actions cache page reports and what its
	// flush button empties. nil -- `grain demo`, a UI not colocated with
	// a daemon, a daemon whose cache would not open -- means the page
	// says this deployment keeps none, the same nil-means-unavailable
	// contract PackageCache above gives.
	ActionsCache ActionsCache
	// GitHubBranches is the daemon's GitHub client, using the same token
	// resolution as the orchestrator. nil means API access is unavailable.
	GitHubBranches GitHubBranchLister
	// RepoBranches lists the branches of a repository GitHub's API does
	// not serve, and is asked before GitHubBranches is: the controller-local
	// config repository (model.IsConfigRepo), read from its own checkout,
	// and a repository on a plain git host, read over git. nil means only
	// GitHub is asked -- and the config repository, which GitHub has
	// never heard of, answers that its branches cannot be listed here.
	RepoBranches RepoBranchSource
	// RepoRevs resolves committed-context git refs (`[owner/name:]path[@rev]`)
	// to full commit SHAs when tasks are filed or updated.
	RepoRevs RepoRevResolver
	// GitHubPullRequestFiles is that same client asked a second question:
	// which files an open pull request touches. It is what lets a shared
	// CI fault name the change that already fixes it (cifault.go), and
	// nil costs that annotation its second half and nothing else.
	GitHubPullRequestFiles PullRequestFileLister
	// GitHubPullRequests is that client asked a third: where an open
	// pull request's head is now. It is what lets a force merge be
	// checked against the commit the operator reviewed while they are
	// still at the button (Client.forceMergeHead); nil falls back to the
	// commit grain CI last judged, and the merge queue checks the head
	// again before it merges either way.
	GitHubPullRequests PullRequestReader
	// Actions is what ci:read reads a repository's GitHub Actions runs,
	// jobs and logs through (ci_actions.go). nil reads them through
	// GitHubPullRequests, or the pull request opener's client, when
	// either is a github.ActionsReader -- the daemon's are.
	Actions github.ActionsReader
	// GitHubForkPullRequests is that client asked which open pull request
	// a fork head label ("owner:branch", model.ForkHead) heads, so a task
	// filed with one as its branch is resolved to that pull request
	// (forkhead.go). nil refuses such a task with a message saying so.
	GitHubForkPullRequests ForkPullRequests
	// GitHubDefaultBranches is that client asked which branch a
	// repository's default is, so a proposal filed live with no base and
	// a proposer filed with an explicit `base: main` read as landing on
	// the one branch they do (model.AlignDefaultBases). nil, or a
	// repository it cannot answer for, compares bases as written -- the
	// proposal is filed for a person rather than queued.
	GitHubDefaultBranches DefaultBranchReader
	// GitHubCredentialAvailable answers through that client's own token source,
	// including a deployment-wide posting credential when one is configured.
	GitHubCredentialAvailable func(owner, repo string) bool
	// AgentKeyChecks, when set, is what POST /api/agent-keys/{framework}/
	// check calls to test one agent framework's own credential against
	// the vendor that issued it -- pkg/cli/daemon.go's own adapter over
	// the same secrets store and key files a dispatch reads a credential
	// from. The "set"/"not set" chip on Settings' Agents tab is the
	// whole of what a pane reading grain's own store can see, and a key revoked
	// at the far end leaves every stored fact unchanged (see
	// Client.CheckAgentKey).
	//
	// nil means this UI was not handed one (`grain demo`'s throwaway UI,
	// or any UI not colocated with a daemon holding the credentials), and
	// the route answers 404 while Settings.AgentKeyChecksEnabled reports
	// false so the pane offers no button that could not work -- the same
	// nil-means-unavailable contract every other optional field here
	// gives.
	AgentKeyChecks AgentKeyChecker
	// AgentModels, when set, is what GET /api/agent-models calls to ask
	// the installed agent CLI which models it can actually run --
	// pkg/cli/daemon.go's agyModelLister over antigravity.Catalog,
	// which runs `agy models` on the same host the dispatches run on. It
	// is what turns Settings' Gemini model field from a name an operator
	// has to remember exactly (a wrong one fails on the next dispatch,
	// before the run starts) into a list of what this deployment's own
	// binary offers, effort variants included (grain/task-365).
	//
	// nil means this UI was not handed one (`grain demo`'s throwaway UI,
	// or any UI not colocated with a daemon that has the binary), and
	// the route answers "not available" while the field stays free text
	// -- the same nil-means-unavailable contract Secrets, Reboot and
	// Credentials above already give. A lister that
	// is wired but fails is a different answer, reported rather than
	// hidden; see agentModelsResponse.
	AgentModels AgentModelLister
	// CodexModels supplies model-specific reasoning efforts from app-server.
	CodexModels AgentModelLister
	// ClaudeModels supplies the Claude model field's own listing, read
	// from Anthropic with this deployment's Claude credential rather
	// than from a CLI (cmd/grain's claudeModelLister). Same
	// nil-means-unavailable contract as the two above: a UI that was not
	// handed one leaves the field free text and says so.
	ClaudeModels AgentModelLister
	// AgentCLIDefaults is what a connection with no CLIPath of its own
	// runs, by binary ("claude", "codex", "agy"): the deployment's
	// -claude-path / -codex-path / -agy-path, or "" where that flag was
	// left empty and the daemon's own PATH decides. Only shown, as the
	// connection page's hint; nil leaves the hint saying PATH.
	AgentCLIDefaults map[string]string
	// StateRepo is set only when this UI runs inside the daemon that owns
	// the state repository its store is exported to (pkg/staterepo) --
	// nil means it does not, and the bootstrap pane and its API routes
	// report themselves unavailable rather than erroring on every call,
	// the same nil-means-unavailable contract Secrets, Reboot and
	// Credentials above already give. See staterepo.go for what the pane
	// is allowed to do, which is choose where state lives and nothing
	// else.
	StateRepo StateRepoManager
	// StateRepoRef answers which repository this deployment keeps its own
	// settings in, as a model.RepoRef rather than as the pane's own
	// status -- orchestrator.Config.StateRepo's exact counterpart, and
	// wired from the same stateManager.settingsRepo in cmd/grain.
	//
	// It is read for one thing: firing a schedule whose template is one
	// grain itself ships (model.SystemTemplate), since what those read
	// is this deployment's state repository and nothing in the template
	// can know which that is. "Run now" on the memory agent's schedule
	// goes through here; every other schedule ignores it.
	//
	// A function rather than a value for the reason the orchestrator's
	// own is one: adopting a different repository swaps the answer out
	// mid-process. nil, or a function returning the zero RepoRef, says
	// there is none -- an honest answer for a deployment whose state is
	// local-only, and for any UI not colocated with a daemon at all.
	StateRepoRef func() model.RepoRef
	// ForbiddenRepos is the live set of external repositories this
	// deployment never reads or serves (such as the encrypted secrets backup
	// repository), shared with the git proxy and declaration poller so
	// schedule writes refuse forbidden git_ref watched repositories up front.
	ForbiddenRepos *gitproxy.ForbiddenSet
	// HostdRemote, when set, takes the upgrades of a grain hostd on
	// another machine dialing in to /api/hostd/connect (hostd.Remote,
	// which checks the hostd token itself); nil refuses them.
	HostdRemote http.Handler
	// LocalHostdLogins, when set, is the agent CLI logins on the machine
	// of the co-located grain hostd (model.LocalHostd), from its hello,
	// and whether it said: what the host pickers show beside "local". A
	// dialing-in hostd's come with its session (hostd.RemotePeer.Logins).
	LocalHostdLogins func(ctx context.Context) ([]string, bool)
	// AmbientNeedsHost, when set, reports whether a connection of
	// framework would start its CLI through grain hostd, with stored as
	// Settings -> Models' run locations: where it would, a connection
	// with ambient credentials has to name its host
	// (model.CheckAmbientPlacement). Nil refuses no ambient connection.
	AmbientNeedsHost func(framework string, stored map[string]model.CLIRunSetting) bool
	// Upgrader is set only when this deployment was told how it
	// upgrades itself: which image to pull per branch
	// (pkg/cli/daemon.go's -upgrade-image, the shape scripts/setup.sh
	// installs) or, on a host that builds its own binary, where its
	// checkout and build/install/restart mechanics live
	// (-upgrade-src-dir). bwsalmon/agents#396 and #645; which of the two
	// this one is, is Upgrader.Mode. nil means neither was configured,
	// and the upgrade pane and its API routes report themselves
	// unavailable rather than erroring on every call, the same as a nil
	// Secrets above.
	Upgrader Upgrader
	// DisableSelfUpgrade disables in-VM self-upgrade actions (pkg/upgrade,
	// automatic/scheduled upgrades, and in-VM SQLite-to-PostgreSQL cutover)
	// and hides in-VM upgrade controls in the UI in favour of an optional
	// external management URL (-disable-self-upgrade).
	DisableSelfUpgrade bool
	// ManagementURL is an optional http:// or https:// URL where this
	// deployment's lifecycle and upgrades are managed externally
	// (-management-url), shown in place of in-VM self-upgrade controls when
	// DisableSelfUpgrade is true.
	ManagementURL string
	// LastRestart is what the restart that started this daemon did to
	// the runs in flight across it (orchestrator.UpgradeAdoption), for
	// the upgrade pane, or nil when there is nothing to report. nil on a
	// UI with no daemon behind it, and the pane shows no such section.
	LastRestart func(ctx context.Context) *RestartAdoption
	// Logs is the set of named log sources the System pane in the UI can
	// tail -- "daemon" for grain daemon's own journal, and, on a
	// deployment colocated with one, the git proxy's audit log
	// (bwsalmon/agents#444). Keyed by the name a caller passes to
	// GET /api/logs/{source}. nil or empty means no sources are
	// configured, and that pane reports itself unavailable
	// rather than a page with nothing on it, the same nil-means-
	// unavailable contract Secrets, Reboot and Upgrader above already
	// give their own panes.
	Logs map[string]LogSource
	// LogRedactor is applied to every line GET /api/logs/{source}
	// returns, before it leaves this process: the daemon's journal and
	// the kernel's carry whatever anything here has ever logged --
	// Authorization headers, tokens in a failed clone's URL, a private
	// key written by mistake -- and the System pane is a browser, not
	// the host. It is called per request, so a secret set since this
	// process started is redacted by the next read rather than by the
	// next restart. An error refuses the read rather than serving the
	// lines unredacted (cmd/grain's grainLogRedactor, which cannot read
	// the secrets it would redact). nil redacts by shape alone
	// (grainlog.NewRedactor(nil)) -- the demo and tests, which hold no
	// secrets of their own to name.
	LogRedactor func() (*grainlog.Redactor, error)
	// LiveTranscripts is where Client.AttemptTranscript looks for a
	// still-running attempt's own transcript-in-progress, before falling
	// back to whatever Store.RunTranscript has recorded (nothing, until
	// the attempt finishes) -- the seam a deployment with filesystem
	// access to a live run's own transcript file plugs in
	// (pkg/agent/claude.LiveTranscriptDir implements it), mirroring Logs'
	// own nil-means-unavailable contract: nil here just means every
	// attempt is read the way bwsalmon/agents#446 originally left it,
	// nothing to show until it finishes (bwsalmon/agents#467).
	LiveTranscripts LiveTranscript
	// ArchivedTranscripts, when set, reads finished run transcripts from an
	// object store (via a local read-through cache) when Store.RunTranscript
	// holds none.
	ArchivedTranscripts ArchivedTranscript
	// Sandboxes, when set, is what GET /api/sandboxes calls to report
	// every live run's own sandbox status -- pkg/cli/daemon.go's
	// sandboxHealthAdapter over whichever of orchestrator.GranuleSandboxes/
	// HostSandboxes this deployment actually runs (run()'s own doc
	// comment: exactly one of the two). nil means this deployment's UI
	// was not handed one (`grain demo`'s throwaway UI, or any UI not
	// colocated with the orchestrator that owns a real sandbox pool), and
	// the sandbox health pane reports itself unavailable rather than
	// erroring on every call, the same nil-means-unavailable contract
	// Logs above already gives the System pane it now sits alongside
	// (bwsalmon/agents#536).
	Sandboxes SandboxHealth
	// SandboxImage, when set, is which container image those sandboxes
	// are created from and which one this build of grain was stamped for
	// -- reported in the same pane's host section, since a deployment
	// whose two differ is one running a daemon and guests from different
	// commits and has no other way of finding out (SandboxImage's own doc
	// comment).
	//
	// Fixed for the life of the process, so a value rather than a func
	// like HostStats below: it is read out of the create args the daemon
	// resolved once at startup, and -granule-create-arg is not one of the
	// settings a running daemon can change under itself.
	//
	// nil where there is no sandbox image to name -- the host-directory
	// backend, whose sandbox is a directory, and any UI not colocated
	// with a sandbox pool -- and the pane shows one line fewer, the same
	// nil-means-unavailable contract Sandboxes above gives.
	SandboxImage *SandboxImage
	// SandboxImageFunc, when set, returns the current SandboxImage dynamically
	// so live changes to model.Config.SandboxImage, k8s_granule_image,
	// k8s_sandbox_image, or sandbox_image_mirror are reflected immediately in
	// GET /api/sandboxes and sandbox guest publishing readings.
	SandboxImageFunc func() *SandboxImage
	// SandboxShapeIgnored says that the sandbox backend this deployment
	// actually runs has no shape to apply, so the three deployment-wide
	// sandbox-shape settings (Settings.SandboxCPUs and friends) are
	// stored and shown but size nothing -- true under
	// orchestrator.HostSandboxes, whose sandbox is a directory on the
	// daemon's own machine ("a host-directory sandbox has no shape of
	// its own", HostSandboxes.Acquire), and false under
	// orchestrator.GranuleSandboxes, which is the backend cmd/grain/
	// daemon.go's own defaultShaper path hands a changed shape to.
	//
	// Reported onward as Settings.SandboxShapeIgnored so the pane and
	// `grain settings` can say so beside the numbers. An operator
	// reading "2 vCPUs, 2048 MiB" on a host-sandbox deployment otherwise
	// reasonably concludes runs are capped there when they are capped at
	// nothing at all -- the one silent gap left by a backend that is
	// otherwise careful to *refuse* a per-task shape rather than ignore
	// it (grain/task-9).
	//
	// And onward again as configResponse.SandboxShapeIgnored, for the
	// per-task override the new-task form offers: there a *refused*
	// shape is the whole problem, since the task is filed and dispatched
	// and only then dies at sandbox setup, so the form disables those
	// fields where this is set (grain/task-26).
	//
	// false is the "say nothing" default, which is what a UI with no
	// backend to speak for (`grain demo`'s throwaway UI, any UI not
	// colocated with an orchestrator) should do -- the same
	// zero-value-means-unavailable reading Sandboxes above has, except
	// that here the unavailable answer is silence rather than a pane
	// reporting itself off.
	SandboxShapeIgnored bool
	// SandboxHostCapacity, when set, reports how much machine the
	// sandboxes this deployment builds have to share -- what
	// Settings.SandboxCommitment holds max_workers + max_mergers + max_ci_jobs
	// sandboxes of the deployment-wide shape against (grain/task-223).
	//
	// It is a seam of its own rather than a reading this package takes,
	// and rather than HostStats above, because the two are answers about
	// two different machines and only the daemon knows when they
	// coincide. HostStats is *this process's* own host, which is the
	// right thing for a pane asking why a run feels slow; this is the
	// host a *sandbox* is built on, and reporting the daemon's own
	// figures for a backend that builds its sandboxes somewhere else
	// would be worse than saying nothing -- an operator would size a
	// deployment against a machine that runs none of its work. So the
	// daemon sets this only where it knows the two are the same machine
	// (pkg/cli/daemon.go's sandboxHostCapacity, which is wired for the
	// granule backend, whose VMs are containers created by a granule
	// exec'd on this same host), and nil is the answer everywhere else:
	// the same nil-means-unavailable contract Sandboxes and HostStats
	// above already give, and what a UI with no backend to speak for
	// (`grain demo`) has.
	SandboxHostCapacity func() (HostCapacity, error)
	// Overcommit, when set, is the daemon's auto overcommit controller's
	// state (model.OvercommitController.State): its effective ratios,
	// recent adjustments and OOM kills, for Settings -> Sandboxes and the
	// sandbox health pane. nil wherever no controller runs.
	Overcommit func() model.OvercommitState
	// HostStats, when set, is what GET /api/sandboxes' own host section
	// calls to report this deployment's daemon's own CPU-load/memory
	// pressure -- pkg/sysstat.Read wrapped to return HostPressure, in a
	// real deployment. A sandbox that looks stuck is often really the
	// host it runs on being starved, which is a question about this
	// process's own machine rather than about any one sandbox
	// (SandboxHealth above), so it is reported separately. nil means no
	// reading is available, the same nil-means-unavailable contract every
	// other optional field here already gives (bwsalmon/agents#536).
	HostStats func() (HostPressure, error)
	// SandboxVolumes, when set, reports the filesystems this deployment
	// writes to and what a given disk floor comes to on each of them --
	// the same volumes HostStats above puts on the health pane
	// (DiskUsage), sized and resolved but without the load-and-memory
	// reading beside them.
	//
	// It is the Settings pane's half of the floor
	// (Settings.SandboxDiskFloors, grain/task-372): the floor is stored
	// as a share of a volume with a minimum under it, which is not a
	// figure at all until it meets a disk -- a tenth is 44 GB of the
	// sandbox volume and its 5 GiB minimum of the store disk beside it.
	// The floor is passed in rather than read from the row so the pane
	// resolves the setting *it* is showing, which immediately after a
	// save is a reconcile tick ahead of the one the daemon has applied.
	//
	// nil is the usual nil-means-unavailable: a UI not colocated with a
	// host of this deployment's own, and a deployment with no volumes
	// resolved. The pane shows the setting without its resolved figures
	// rather than hiding the setting.
	SandboxVolumes func(model.DiskFloor) []DiskUsage
	// HostTop, when set, is what GET /api/host/top calls for a `top`
	// snapshot of the processes on that same machine -- pkg/hosttop.Read,
	// in a real deployment. It is the per-process half of HostStats
	// above: a load average says the machine is starved, and the next
	// question is always which process is doing it, which no aggregate
	// reading can answer. Given as a func rather than read here so this
	// package keeps shelling out to nothing itself, the same way
	// Logs' own LogSource keeps journalctl out of it. nil means no
	// snapshot is available (`grain demo`'s throwaway UI, a deployment
	// whose image has no procps) and the Top tab says so rather than
	// erroring on every poll, the same nil-means-unavailable contract
	// every other optional field here already gives.
	HostTop func(ctx context.Context, lines int) ([]string, error)
	// Daemon, when set, is what GET /api/sandboxes calls for the health
	// of the process serving it: how long this daemon has been up, how
	// many times its service has started in the recent past, and whether
	// the command line it was started with named flags it does not
	// define (DaemonHealth).
	//
	// It is here because a deployment that is restarting in a loop has
	// no surface of its own. Every other reading in this pane is taken
	// by a process that is running; this one is about whether *this*
	// process is the fifty-second attempt at running, which on
	// 2026-09-18 went unnoticed for two hours and was then found by
	// reading the journal by hand (grain/task-233). A daemon that is
	// crash-looping serves this pane only in the seconds it is up, which
	// is exactly when the figure has to be on it.
	//
	// A func rather than a value because uptime moves. nil means no
	// reading is available -- `grain demo`, a UI not colocated with a
	// daemon it can ask -- and the pane shows one line fewer, the same
	// nil-means-unavailable contract every other optional field here
	// gives.
	Daemon func() DaemonHealth
	// A RootShell field sat here (grain/task-13): POST /api/host/shell
	// ran one arbitrary command as root on the daemon's host, through a
	// systemd responder scripts/setup.sh installed. It is gone, end to
	// end (grain/task-193). Every other host-facing field here hands
	// over one fixed question or one fixed command; that one handed over
	// unrestricted root to anything that could reach this UI, which is a
	// larger grant than the rest of this package put together and not
	// one a debugging pane earns. An operator who needs a shell on the
	// host has the same ways in they deployed it with.
	//
	// Consoles, when set, is where this deployment's live interactive
	// sessions are registered -- the same *console.Manager
	// orchestrator.Config.Consoles hands a console task's run, which is
	// the only reason a terminal a person types into here reaches an
	// agent running over there (pkg/console's own doc comment). nil is
	// the deployment that runs none, and every route under /api/console
	// then says so, the same nil-means-unavailable contract Reboot and
	// HostTop above give.
	Consoles ConsoleSessions
	// AutoMergeDegraded, when set, is polled by GET /api/config to report
	// whether this deployment's GitHub credential can read pull request
	// check runs at all -- orchestrator.ChecksUnavailable's own doc
	// comment. A deployment where it cannot (a fine-grained PAT, e.g.
	// terraform/gcp's own staging credential, rather than a GitHub
	// App installation token) still accepts Submit and sets AutoMerge,
	// but never actually merges: PR health can never resolve past
	// "unknown", which orchestrator's own reconcile loop declines to
	// act on. Before this field existed that fact lived only in server
	// logs, so Submit looked like it silently did nothing
	// (bwsalmon/agents#483). nil (`grain demo`'s throwaway UI, or any
	// deployment not wired to orchestrator's own package-level state)
	// means AutoMergeDegraded always reports false, the same
	// nil-means-unavailable-feature default Reboot and Upgrader give
	// their own panes above -- except here false is the "nothing to
	// warn about" answer rather than "the feature is off."
	AutoMergeDegraded func() bool
	// ReconcilerDown, when set, is polled by GET /api/config to report
	// whether this deployment's own reconcile loop has died -- true once
	// pkg/cli/daemon.go's runDaemon has returned an error (or panicked)
	// and given up entirely, as opposed to a one-time setup step still
	// being retried. Before this existed, that fact lived only in a
	// single server log line: the UI/API server stays up on its own
	// (bwsalmon/agents#550, "make sure ui with logs stays up even if the
	// daemon has failed"), so nothing dispatching or reconciling tasks
	// was otherwise visible from outside the process at all
	// (bwsalmon/agents#576). nil (`grain demo`'s throwaway UI, or any
	// deployment not wired to cmd/grain's own package-level state) means
	// ReconcilerDown always reports false, the same nil-means-
	// unavailable-feature default AutoMergeDegraded above gives.
	ReconcilerDown func() bool
	// DispatchConditions, when set, is why this deployment is not
	// dispatching, as named conditions with the error and the time each
	// was set (pkg/dispatchcondition.Set, wired by pkg/daemon): what
	// ReconcilerDown's boolean cannot say. GET /api/sandboxes lists the
	// conditions in force as failing checks, GET /api/config carries
	// them beside reconcilerDown, and GET /api/pipeline/changes shows
	// each set and clear as an entry. nil reports none, the same
	// nil-means-nothing-to-warn-about default ReconcilerDown has.
	DispatchConditions DispatchConditionReader
	// StoreSwitch, when set, is polled by GET /api/config and GET
	// /api/sandbox-health (DaemonHealth.StoreSwitch) for the standing
	// alert that the store was switched underneath this daemon
	// (pkg/daemon/storeswitch.go). nil, or a nil answer, is no alert.
	StoreSwitch func() *StoreSwitch
	// InstallNotice, when set, is what GET /api/config reports as
	// installNotice: something about how this deployment is installed
	// that an operator has to fix, found once at start -- an unmigrated
	// host-mode install (pkg/daemon's installnotice.go). nil, or a nil
	// answer, reports nothing.
	InstallNotice func() *InstallNotice
	// Restarting, when set, is polled by GET /api/config to report that
	// this daemon is about to stop answering for a restart: it has been
	// told to shut down and is draining, or an upgrade that ends in a
	// restart is running. nil reports false.
	Restarting func() bool
	// ShutdownStarted, when set, is closed the moment this daemon has
	// been told to stop. GET /api/restart-watch holds a request open on
	// it and answers restarting: true as it closes, because a daemon
	// with little to drain stops answering in milliseconds -- far
	// sooner than the frontend's next GET /api/config would have asked
	// (grain/task-3543). nil means the watch only re-reads Restarting.
	ShutdownStarted <-chan struct{}
	// Wake asks this deployment's reconcile loop to run a cycle now
	// rather than at its next tick. AddComment calls it when a reply has
	// actually un-parked a task, which is the one moment a person is
	// demonstrably waiting on the next cycle.
	//
	// It exists for the director and every other interactive session
	// (model.ConsoleSession): a reply is dispatched by whichever cycle
	// next reaches the dispatch decision, so the remainder of
	// -poll-interval is the whole of what a chat waits before anything at
	// all happens -- measured at 29.6-29.9s of a 29.9s exchange on the
	// shipped 30s interval, against 0.04-0.44s for everything grain does
	// between that decision and the agent's first turn
	// (the 2026-09-08 director-latency hand run).
	//
	// A hint, never a guarantee, and deliberately so: the loop coalesces
	// wakes and spaces them out, and the periodic tick is still what
	// makes the work happen at all. So nothing here has to be robust
	// against a wake that is dropped -- the reply is a store row either
	// way, and the next cycle reads it. nil (`grain demo`, or any UI not
	// wired to a reconcile loop) means the reply simply waits for the
	// tick, exactly as every reply did before this existed.
	Wake func()
	// TriggerIAPSync requests an immediate IAP IAM synchronization cycle when team
	// memberships or admin access lists are modified.
	TriggerIAPSync func()
	// HubStatus, when set, returns the live HubSnapshot from the
	// daemon's HubManager; nil falls back to reading stored settings and
	// repo configurations directly.
	HubStatus func(ctx context.Context) (model.HubSnapshot, error)
	// TriggerHubHeartbeat, when set, runs an immediate registry
	// registration/heartbeat step and returns the updated snapshot.
	TriggerHubHeartbeat func(ctx context.Context) (model.HubSnapshot, error)
	// TriggerHubSync wakes the daemon's HubManager after settings,
	// secrets, or repository configurations change.
	TriggerHubSync func()
	// HubAllowInsecureHTTP permits non-loopback plain HTTP registry URLs
	// in test setups.
	HubAllowInsecureHTTP bool
	// SandboxBackends names the backend methods this daemon has enough
	// startup configuration to build. SandboxBackendOptions lists every backend
	// in model.SandboxBackends() with Available and Reason so Settings never
	// silently omits a backend.
	SandboxBackends       []model.SandboxBackend
	SandboxBackendOptions []model.SandboxBackendOption
	// StateRepoResetNotice, when set, is polled by GET /api/config to
	// report that a merged settings change replaced a value on this
	// deployment and grain could not say so anywhere but its own journal
	// -- no remote to find a pull request on, no credential to comment
	// with, a commit pushed straight at the branch, or an outright
	// failure (StateRepoStatus.ResetNotices).
	//
	// The notice itself, with the values in it, is on the State pane; this
	// is only whether there is one, because this route is polled by every
	// open tab several times a minute and the values belong on the call
	// the pane makes when it opens. Without it the notice would be on a
	// tab nobody had a reason to open, which is the whole failure it
	// exists to fix: the operator who is owed this is the one who has
	// never opened the pane, and a reset is a thing that happened once
	// rather than a condition that will still be there next month.
	//
	// This is a marker rather than a banner, and true only until somebody
	// dismisses it (StateRepoManager.DismissResetNotices). nil reports false.
	StateRepoResetNotice func() bool
	// StateRepoSyncFailure, when set, reports that this deployment's
	// settings have not reached its state repository for a while: the
	// sync cycle has failed on every tick since Since, with Error the
	// latest reason. nil (the func, or what it returns) is a sync that is
	// working. Polled by GET /api/config for the standing banner, and
	// carried on the sandbox-health response, which is what
	// read_deployment_health reads. grain-eu-host went 36 hours without
	// an export and nobody saw it until a restart rewound the settings
	// changed in that window.
	StateRepoSyncFailure func() *StateRepoSyncFailure
	// RunningConfig, when set, reports the store-backed configuration the
	// daemon this UI runs inside actually has in effect right now -- what
	// it read at startup, plus every later change it was able to apply
	// without being restarted (pkg/cli/daemon.go's liveConfig). Settings
	// compares it against what is stored to answer which of the
	// restart-only settings have been saved but are not running yet
	// (Settings.PendingRestart), so a change that will sit unapplied
	// until someone restarts the daemon says so at the moment it is made
	// rather than looking, like every other setting on the pane, as
	// though it had taken effect.
	//
	// nil (`grain demo`'s throwaway UI, or any UI not colocated with a
	// daemon whose configuration it could speak for) means PendingRestart
	// is always empty -- the same nil-means-unavailable contract
	// ReconcilerDown and AutoMergeDegraded above give, and the honest
	// answer where there is no running configuration to compare with.
	RunningConfig func() model.Config
	// CLIRuns, when set, resolves where each agent CLI runs under the
	// given stored settings, as the daemon would for its next run
	// (daemon.Config.ResolveCLIRuns), for Settings -> Models. nil, as for
	// RunningConfig, where no daemon is colocated with this UI.
	CLIRuns func(ctx context.Context, stored map[string]model.CLIRunSetting) any
	// CLIRunCheck, when set, is what this deployment accepts in place of
	// model.ValidateCLIRun: it refuses "host" where the daemon has no
	// host executor, and "container" on a daemon on the bare machine
	// (daemon.Config.CheckCLIRunSettings). HostExecutor
	// is that executor and, for grain hostd, whether it answers
	// (daemon.HostExecutorStatus), for Settings -> Models to show.
	CLIRunCheck  func(map[string]model.CLIRunSetting) error
	HostExecutor func(ctx context.Context) any
	// PullRequests, when set, is what POST /api/tasks/{id}/pull-request
	// calls to open (or find) a task's own pull request and read its
	// checks -- pkg/cli/daemon.go's pullRequestOpener over
	// orchestrator.OpenPullRequestForTask and that daemon's GitHub
	// client. It is how a dispatched run opens its pull request before it
	// exits, through the open_pull_request tool its own MCP server
	// exposes: the mcpserver process holds no GitHub credential of its
	// own (deliberately -- pkg/gitproxy's whole shape is that a run's
	// route to GitHub is grain's, never the agent's), so it asks the
	// daemon over this same REST API the CLI already speaks.
	//
	// nil means this deployment's UI was not handed one (`grain demo`'s
	// throwaway UI, or any UI not colocated with a daemon that has a
	// GitHub client), and the route answers 404 rather than erroring --
	// the same nil-means-unavailable contract Reboot and Sandboxes above
	// already give.
	PullRequests PullRequests
	// ExternalBase is the deployment's external UI base
	// (https://grain.example), the one place grain writes an absolute
	// link: Grain-Task: in a changelist CL's description, since Critique
	// has no grain-relative root. Empty leaves that line out.
	ExternalBase string
	// RepoNames, when set, is what AddTargetRepo asks for a repository's
	// own spelling before writing it into the allowlist -- cmd/grain/
	// daemon.go's repoNameGate over that daemon's GitHub client.
	//
	// It is the one point where a repo's capitalisation is decided rather
	// than copied: everything downstream spells a repo the way the
	// allowlist does (Client.canonicalRepo), so an entry written as
	// `Bwsalmon/grain-home` becomes the spelling every task filed against
	// it inherits, and grain then holds two spellings of one repository
	// as two repositories (model.RepoRef.Same's own doc comment on why
	// its keys cannot fold case for it).
	//
	// nil means this deployment cannot ask, and an entry is stored as
	// typed -- the same nil-means-unavailable contract PullRequests above
	// gives, and no reason to refuse an add.
	RepoNames RepoNames
	// PullRequestComments, when set, is what Close uses to say on a pull
	// request itself what closing its task has done to it -- see
	// Client.noteOrphanedPullRequests, and model.OrphanedPullRequestNote
	// for what is said and why it is said in two places at once.
	//
	// nil means this deployment's UI was not handed one, exactly as for
	// PullRequests above, and is not an error here: the note still lands
	// on the task, and says in so many words that the pull request itself
	// was not told. A close never fails over this.
	PullRequestComments PullRequestComments
	// PullRequestCloser, when set, is what a close asking for its pull
	// request to be closed too (CloseOptions.ClosePullRequest) uses to
	// close it on GitHub. Nothing else in this package ever reaches it:
	// grain closes a pull request only on that explicit, per-close ask.
	//
	// Its own field rather than a second method on PullRequestComments
	// above, because it is a strictly larger power than commenting and a
	// deployment ought to be able to grant one without the other -- see
	// PullRequestCloser's own doc comment. nil is that choice as much as
	// it is an unwired UI: the task still closes, the pull request is
	// left open, and the note left in both places says grain was asked to
	// close it and could not.
	PullRequestCloser PullRequestCloser
	// SandboxRecreate, when set, is what
	// POST /api/tasks/{id}/sandbox/recreate calls to destroy a live run's
	// sandbox and build an empty one in its place -- pkg/cli/daemon.go's
	// sandboxRecreateAdapter over the orchestrator.SandboxRecreations its
	// dispatched runs register themselves in.
	//
	// It is how a dispatched run rescues itself from a sandbox it cannot
	// repair from inside, through the recreate_sandbox tool its own MCP
	// server exposes: everything else that run has runs *in* the sandbox,
	// so destroying and rebuilding one is necessarily somebody else's
	// call to make -- the same hop, and the same reasoning, as
	// PullRequests above.
	//
	// nil means this deployment's UI was not handed one (`grain demo`'s
	// throwaway UI, or any UI not colocated with the daemon that owns the
	// runs), and the route answers 404 rather than erroring -- the same
	// nil-means-unavailable contract PullRequests and Sandboxes above
	// already give.
	SandboxRecreate SandboxRecreator
	// SandboxProbe, when set, is what POST /api/sandbox-probe builds a
	// throwaway sandbox through (sandbox_probe.go): pkg/daemon's adapter
	// over orchestrator.ProbeSandbox and the daemon's own backend. nil
	// answers 404, the contract SandboxRecreate above gives.
	SandboxProbe SandboxProber
	// StandingSandboxes, when set, reports the standing sandboxes the
	// daemon's reconcile loop is supervising, for GET /api/sandboxes'
	// standingSandboxes -- pkg/cli/daemon.go's adapter over the same
	// orchestrator.StandingSandboxSupervisor its Deps carry. It must
	// answer from a snapshot and never wait on a launch in progress.
	//
	// nil means this UI is not colocated with that loop (`grain demo`,
	// a test), and the field is simply absent from the response.
	StandingSandboxes func() []StandingSandboxStatus
	// PersistentSandboxes, when set, is the daemon's persistent sandbox
	// supervisor: GET /api/persistent-sandboxes, its controls, and the
	// /staging/<name>/ reverse proxy. nil serves none of them.
	PersistentSandboxes PersistentSandboxes
	// WaitCredit, when set, is what POST /api/tasks/{id}/wait-credit
	// calls to credit a live run for the time it has spent blocked on CI
	// -- pkg/cli/daemon.go's waitCreditAdapter over the
	// orchestrator.RunWaitCredits its dispatched runs register their
	// wall-clock timers in.
	//
	// It is the route by which a run's own wait_for_checks keeps itself
	// from being cancelled halfway through the wait it is standing in:
	// the tool measures the blocking, the daemon holds the timer, and
	// this is the only hop between them -- the same shape, and the same
	// reasoning, as SandboxRecreate above.
	//
	// nil means this deployment's UI was not handed one, and the route
	// answers 404: no credit is granted, and every run is bounded by
	// orchestrator.Config.MaxRunRuntime exactly as it was before any of
	// this existed. Same nil-means-unavailable contract as the fields
	// around it.
	WaitCredit WaitCreditor
	// TaskCanceller, when set, is what Client.Stop calls to immediately
	// cancel an in-flight run for a task -- pkg/cli/daemon.go's
	// taskCancellerAdapter over the orchestrator.TaskCancellers its dispatched
	// runs register themselves in.
	TaskCanceller TaskCanceller
	// PauseGrace is how long a graceful Pause of a running task waits for
	// its agent to commit, push and write its handover before the run is
	// cancelled. Zero means model.PauseGracePeriod (2m).
	PauseGrace time.Duration
	// Cycles, when set, is what GET /api/metrics' own "cycles" section
	// reads to report how long this daemon's RunCycle ticks have been
	// taking -- pkg/cli/daemon.go's cycleTimesAdapter over the
	// orchestrator.CycleTimes its reconcile loop writes into. It is the
	// one part of a metrics report that is not derived from rows,
	// because a tick leaves none (see pkg/metrics' Cycles), so unlike
	// every other number there it has to be handed in by the process
	// that produced it.
	//
	// nil means this deployment's UI was not handed one (`grain demo`'s
	// throwaway UI, or any UI not colocated with a reconcile loop whose
	// ticks it could speak for), and the section reports itself
	// unavailable rather than an empty distribution that would read as
	// "no ticks happened" -- the same nil-means-unavailable contract
	// Sandboxes and PullRequests above already give.
	Cycles CycleTimes
	// SettingsHistory, when set, is what GET /api/metrics' own "changes"
	// section reads: when one of this deployment's own parameters last
	// moved, off the state repository's git history
	// (staterepo.SettingsChanges, adapted in pkg/cli/daemon.go).
	//
	// It is handed in for Cycles' reason turned around. A settings change
	// is recorded -- in a commit -- but not in any row a report can
	// derive it from, since a settings row remembers what it is and not
	// when it became that, and reading that history means shelling out to
	// git in a directory only the daemon knows about.
	//
	// nil is a deployment with no state repository, or a UI not
	// colocated with the daemon that has one, and the section reports
	// itself unavailable rather than an empty series -- "nothing has ever
	// been changed here" being a very different claim from "nobody can
	// say". The same nil-means-unavailable contract as Cycles above.
	SettingsHistory SettingsHistoryReader
	// HostLoad, when set, is what GET /api/metrics' own "host" section
	// reads: how hard the machine underneath this deployment has been
	// working, as hourly rollups (sysstat.History, adapted in
	// pkg/cli/daemon.go).
	//
	// HostStats above is the same machine read once, now, for the Sandbox
	// health pane. This is the series, and the two are deliberately
	// separate: one is a reading taken per poll, the other a fold of
	// readings a sampler has been taking all along, and the questions they
	// answer ("is it starved right now" against "is it worse than last
	// week") are not the same question.
	//
	// nil is a UI with no sampler behind it, and the section reports
	// itself unavailable rather than an idle machine.
	HostLoad HostLoadReader
	// GitHubCalls, when set, is what GET /api/metrics' own "github"
	// section reads: how many REST calls this daemon has made, how many
	// of them were free, and how much of the hour's budget is left
	// (github.RealTransport.Stats).
	//
	// Like Cycles above and for the same reason, it is handed in by the
	// process that produced it rather than derived from rows: a request
	// leaves none. nil is a UI not colocated with the transport whose
	// calls it would be speaking for, and the section reports itself
	// unavailable rather than reporting zero -- which would read as a
	// deployment that has made no calls, and is the opposite of the
	// thing this section exists to make visible.
	GitHubCalls GitHubCallStats
	// AgentPause, when set, is the dispatch gate an agent's own usage
	// limit closes -- the *orchestrator.Pause
	// pkg/cli/daemon.go hands both this UI and its reconcile loop.
	// Each pause covers the runs spending the credential that met the
	// limit -- one model connection's key, and so one team's work --
	// and each caller is shown only their own teams' (AgentPauseStatus).
	// GET /api/config reports it as agentPause so a banner can say why
	// a queue of ready tasks has nothing running, GET /api/pause is the
	// same answer on its own, and DELETE /api/pause lifts it by hand.
	//
	// Before this field existed, a paused deployment said so only in a
	// daemon log line and in the detail of the attempts the pause ended
	// -- so an operator looking at a full queue and an idle deployment
	// had nowhere on screen to learn that grain was waiting out a
	// provider's window on purpose (grain/task-132).
	//
	// nil means this deployment's UI was not handed one (`grain demo`'s
	// throwaway UI, or any UI not colocated with a reconcile loop whose
	// dispatching it could speak for): no banner, GET /api/pause reports
	// itself unavailable rather than claiming nothing is paused, and
	// DELETE /api/pause 404s -- the same nil-means-unavailable contract
	// Cycles and Sandboxes above already give.
	AgentPause AgentPause
	// CI, when set, is grain's own CI (pkg/ci) as this deployment holds
	// it: POST /api/ci/runs starts a run against a repo and a branch,
	// GET /api/ci/runs and /api/ci/runs/{id} read them back, and DELETE
	// /api/ci/runs/{id} ends one early.
	//
	// nil means this deployment has no CI wired -- `grain demo`, or a
	// build whose daemon was not given a sandbox backend to run jobs in
	// -- and the routes 404 rather than accepting a run nothing would
	// ever execute, the same nil-means-unavailable contract Sandboxes,
	// Cycles and AgentPause above already give.
	//
	// Being wired is not the same as being *on*: which repositories CI
	// actually runs for is the grain-ci capability's own question, asked
	// per request (Client.ciEnabledFor), so a deployment can have this
	// wired for every repo it knows and enabled for one.
	CI ci.Runner
	// Stall, when set, is what every task read asks whether a task has
	// been in its state longer than that state should take, and in what
	// words -- orchestrator.TaskStallNote, which is where the patience
	// per state and the sentence that goes with it are declared
	// (orchestrator's liveness.go). It answers "" for a task that is
	// inside its patience, which is nearly all of them.
	//
	// Handed in rather than reached for, because the two halves belong
	// on opposite sides of the layering: what a queued task is waiting
	// for, how long that should take and which component ought to have
	// ended the wait are the orchestrator's own facts, and this package
	// draws rows. cmd/grain wires the two together, the way it does for
	// every other field here.
	//
	// nil means no task reads back a stall note -- `grain demo`'s
	// throwaway UI, or any UI not colocated with an orchestrator whose
	// deadlines it could speak for -- the same nil-means-unavailable
	// contract every other optional field here gives.
	Stall StallNoter
	// Reasons, when set, is what every task read asks for the sub-case its
	// state is held in -- queued and ready, queued behind another task,
	// paused on a usage limit, on the merge queue, given up on by it --
	// and which of the three conditions that is (driven, awaiting an
	// external answer, awaiting a person), what it waits on, and the
	// button that ends it (Task.Reason). The merge-queue state's own
	// sub-case, which the cycle records (Observation.MergeQueueReason), is
	// classified the same way (Task.MergeQueueReason).
	//
	// Handed in for Stall's reason: the sub-cases and what ends each are
	// the orchestrator's (its livenessreasons.go), and this package
	// draws them. nil reads every task back with no sub-case.
	Reasons ReasonClassifier
	// DBDriver is the active database backend driver ("sqlite" or "postgres").
	DBDriver string
	// DBDSN is the database DSN or file path in effect.
	DBDSN string
	// DataDir is the root data directory of the grain installation.
	DataDir string
	// ReleaseApprovalDir is where the release-approval signing key
	// (pkg/releaseapproval.KeyFile) is kept, when it is not DataDir: the
	// demo, which has no data directory, keeps one in its throwaway
	// store's directory so an Inbox Approve over -github-sim can sign
	// its record.
	ReleaseApprovalDir string
	// ReleaseApprovalKMSKey is the Cloud KMS EC_SIGN_P256_SHA256 key
	// version (projects/.../cryptoKeyVersions/N) release approvals are
	// signed with, the daemon's -release-approval-kms-key. Empty keeps
	// the key in the secrets store under ReleaseApprovalDir.
	ReleaseApprovalKMSKey string
	// SourceDB is the raw *sql.DB backing this store, which a task's
	// database access queries (task_db.go).
	SourceDB *sql.DB
	// IAPAudience, when set, specifies the expected target audience for verifying
	// Google Cloud IAP JWT assertions (X-Goog-IAP-JWT-Assertion), or
	// several separated by commas. With none (here or in
	// $GRAIN_IAP_AUDIENCE) no assertion is accepted.
	IAPAudience string
	// IAPIssuer is the issuer an assertion must carry; empty means
	// GoogleIAPIssuer. Only a signing proxy other than IAP needs it.
	IAPIssuer string
	// IAPKeysURL is where the assertion's signing keys are published (a
	// PEM map or JWK set); empty means GoogleIAPCertURL.
	IAPKeysURL string
	// HubIssuer is the expected iss claim for verifying grain-registry
	// JWT assertions (X-Grain-Registry-Assertion) (-ui-hub-issuer /
	// $GRAIN_HUB_ISSUER). All three of HubIssuer,
	// HubAudience, and HubKeysURL must be set to enable the
	// built-in "registry" IdentityVerifier.
	HubIssuer string
	// HubAudience is the single expected aud URL for verifying
	// grain-registry JWT assertions (-ui-hub-audience /
	// $GRAIN_HUB_AUDIENCE).
	HubAudience string
	// HubKeysURL is the JWKS endpoint URL publishing grain-registry's
	// ES256 public keys (-ui-hub-keys-url / $GRAIN_HUB_KEYS_URL).
	HubKeysURL string
	// AllowedPrincipals, when non-empty, is the allowlist of user:,
	// serviceAccount:, group:, and domain: entries admitted when a request
	// authenticates through IAP or another registered IdentityVerifier
	// (-ui-allowed-principals). When empty, defaults to $GRAIN_ALLOWED_PRINCIPALS,
	// or the deployment owner (Actor) plus provisioned service accounts.
	AllowedPrincipals []string
	// DeploymentOwner is -deployment-owner ($GRAIN_DEPLOYMENT_OWNER): the
	// human email address that owns this deployment for RBAC
	// (IsDeploymentOwner, requireDeploymentOwner) and holds an immutable
	// grain/operator floor on the access allowlist, regardless of the OS
	// account the daemon runs as.
	DeploymentOwner string
	// ProvisionedServiceAccounts lists daemon-owned GCP service accounts grain
	// itself provisions for this deployment (never gcp-key sandbox agent
	// accounts), included in the default allowlist when AllowedPrincipals is unset.
	ProvisionedServiceAccounts []string
	// GCPKeyServiceAccounts lists the gcp-key agent service account pool
	// configured by daemon flags, if any. Merged with the store's gcp-key pool
	// so those accounts are never admitted by default or by -ui-allowed-principals.
	GCPKeyServiceAccounts []string
	// GroupChecker, when set, resolves group: entries in AllowedPrincipals;
	// nil uses CloudIdentityGroupChecker lazily when at least one group: entry
	// is configured.
	GroupChecker GroupMemberChecker
	// RequireVerifier is -ui-require-verifier: when non-empty, names the
	// IdentityVerifier (e.g. "iap", "registry", or a registered verifier)
	// that network callers must sign in through. The access code, as a
	// bearer token or the grain_access cookie, is accepted only from
	// loopback with no forwarding or proxy headers (accessCodeAccepted),
	// or through a registered IdentityVerifier that declares
	// AccessCodeWins, and the "Enter access code" page is not served.
	RequireVerifier string
	// RequireIAP is -ui-require-iap, an alias for RequireVerifier = "iap".
	RequireIAP bool
	// LocalAPIToken is the secret in -data-dir/local-api-token
	// (EnsureLocalAPIToken) that a loopback request presents as a bearer
	// token to be trusted as loopback was.
	// Empty accepts no token.
	LocalAPIToken string
	// LocalAPITokenFile, when set, is the file LocalAPIToken was read
	// from; the server reads it again when it changes, so `grain
	// access-code rotate` takes effect at once (accessCodeFile).
	LocalAPITokenFile string
	// AccessSessionLifetime is how long a single-tenant daemon's access
	// session lasts without being used before its access code has to be
	// entered again; zero is DefaultAccessSessionLifetime.
	AccessSessionLifetime time.Duration
	// AccessCodeMode is the access code's mode where the daemon names no
	// LocalAPITokenFile to keep it beside (access_code_mode.go); "" is on.
	AccessCodeMode AccessCodeMode
	// InstallTrackProbe, when set, supplies the host and daemon-flag inputs
	// that GET /api/config combines with the store's live configuration to
	// classify this deployment as a Google install ("google") or an
	// open-source install ("oss"). nil evaluates store and DataDir signals
	// alone without reading the host running the process.
	InstallTrackProbe *installtrack.Probe
	// DaemonVersion, when set, returns the running daemon's build version
	// info used to pin default Kubernetes sandbox images. nil calls
	// version.Get().
	DaemonVersion func() version.Info
	// CheckImageManifest, when set, performs a cheap manifest HEAD check
	// for an image reference and returns an error when the image does not
	// resolve.
	CheckImageManifest func(ctx context.Context, imageRef string) error
	// CheckClusterImageConnectivity, when set, checks whether the target
	// Kubernetes cluster can pull imageRef across GCP organization/project
	// boundaries and returns an actionable warning suggesting
	// -sandbox-image-mirror when the cluster cannot pull the image.
	CheckClusterImageConnectivity func(ctx context.Context, imageRef string) error
	// K8sGranuleImage, when set, returns the guest image the daemon
	// creates kubernetes-granule pods from under cfg -- its own stamp,
	// mirrored into cfg's SandboxImageMirror when one is set -- which is
	// the image the Settings pane probes for its Granule VM image
	// warning. nil probes cfg's own k8s-granule image, or this UI's
	// version-derived default, mirrored the same way.
	K8sGranuleImage func(cfg model.Config) string
	// SandboxImageMirrorFor, when set, says which repository the daemon
	// would mirror its stamped guest into under cfg, whether that
	// repository is derived from one of cfg's image settings rather than
	// named by sandbox_image_mirror, and which of them it then reads as
	// the mirror rather than as an image. nil answers from cfg alone
	// (sandboxImageMirrorFromConfig).
	SandboxImageMirrorFor func(cfg model.Config) SandboxImageMirrorInfo
	// InspectImageLabels, when set, reads an image's OCI labels -- how
	// the Settings pane checks a custom sandbox image's contract
	// (sandboximage.CheckCustomImage). nil reports every custom image's
	// contract as unverified.
	InspectImageLabels func(ctx context.Context, imageRef string) (map[string]string, error)
}

// StallNoter is Config.Stall's shape: given the state a task is in and
// when it entered it (model.StateTimes.Since), the sentence to put
// beside its state badge, or "" for a task that is waiting rather than
// stuck.
//
// now is passed rather than read so that a list and the task pane drawn
// from it cannot disagree about the moment, and so tests can move the
// clock the way every other clock in this package moves.
type StallNoter func(state model.State, since, now time.Time) string

// ReasonClassifier is Config.Reasons' shape: orchestrator.TaskReason and
// orchestrator.MergeReason, in this package's types.
type ReasonClassifier interface {
	// TaskReason is the sub-case of state t is in, given its observation
	// (nil for none), the blocking tasks it is still waiting on,
	// which of those only a person can close (model.Store.BlockerHolds,
	// nil for none), the grant wait dispatch recorded for it (nil for
	// none), and, for an awaiting_reply task, which ask it is parked on
	// (Task.replyFacts).
	TaskReason(t model.Task, state model.State, obs *model.Observation, openDeps []string,
		holds []model.BlockerHold, wait *model.GrantWait, asked model.ReplyFacts) Reason
	// MergeReason classifies a merge-queue sub-case the cycle recorded,
	// or answers the zero Reason for one it does not know.
	MergeReason(reason string) Reason
}

// Reason is one liveness sub-case as a row draws it (Task.Reason,
// Task.MergeQueueReason; orchestrator.ReasonLiveness).
type Reason struct {
	// Reason is "<state>/<reason>", as livenessreasons.go names it.
	Reason string `json:"reason"`
	// Class is which of the three conditions this is: "driven",
	// "awaiting a person" or "awaiting an external answer".
	Class string `json:"class,omitempty"`
	// Resource is what the wait is for, in a person's words.
	Resource string `json:"resource,omitempty"`
	// Driver is the component that ends a driven sub-case.
	Driver string `json:"driver,omitempty"`
	// Control is the label of the button that ends the wait ("Run now",
	// "Cancel", "Undefer", "Retry merge", ...), where there is one.
	Control string `json:"control,omitempty"`
}

// TaskCanceller is what cancels an in-flight turn for a task.
type TaskCanceller interface {
	CancelTask(ctx context.Context, taskID string) bool
}

// GitHubCallStats is implemented by whatever can report what this
// deployment has spent against GitHub's rate limit --
// *github.RealTransport, in a real deployment. Named here rather than
// taking the concrete type for the reason CycleTimes below is: pkg/ui
// should be wirable to anything that can answer, including a test's own
// fake.
type GitHubCallStats interface {
	Stats() github.Stats
}

// CycleTimes is implemented by whatever can report the RunCycle ticks a
// daemon has recently run -- pkg/cli/daemon.go's own cycleTimesAdapter
// over orchestrator.CycleTimes, in a real deployment. Named here and
// filled there for the same reason SandboxHealth is: this package does
// not import pkg/orchestrator (see sandbox_health.go's own doc comment),
// so cmd/grain is the one place both types are ever in scope.
//
// It is stated in pkg/metrics' own terms rather than in a third set of
// types, unlike SandboxHealth: this is not a shape the API renders
// directly, it is an *input* to the same metrics.Compute call the rest of
// the report already comes from, and pkg/ui already imports pkg/metrics
// to make that call.
type CycleTimes interface {
	CycleTimes() metrics.CycleHistory
}

// SettingsHistoryReader is implemented by whatever can say when this
// deployment's own parameters moved -- pkg/cli/daemon.go's adapter over
// staterepo.SettingsChanges, filled there for CycleTimes' reason: this
// package does not import pkg/staterepo, and cmd/grain is the one place
// both are in scope.
//
// Stated in pkg/metrics' own terms like CycleTimes, and for the same
// reason: it is an input to the same metrics.Compute call rather than a
// shape this API renders directly.
// HostLoadReader is implemented by whatever has been sampling this
// machine's own pressure -- pkg/cli/daemon.go's adapter over
// sysstat.History -- and is stated in pkg/metrics' terms for CycleTimes'
// reason: it is an input to metrics.Compute rather than a shape this API
// renders directly.
type HostLoadReader interface {
	// HostLoad returns the rollups held right now. A sampler that has just
	// started returns few or none, which the section reports as a history
	// that does not reach back rather than as an idle machine.
	HostLoad() metrics.HostHistory
}

type SettingsHistoryReader interface {
	// SettingsChanges reports the settings changes since a moment, newest
	// first. A reader with nothing to read comes back empty rather than as
	// an error -- a repository seeded this minute has no history and that
	// is not a failure -- and an error here is a report without its
	// changes section rather than no report at all (Client.Metrics).
	//
	// reaches is how far back the reader actually got -- the moment before
	// which a change might be missing -- or zero if it cannot say.
	SettingsChanges(ctx context.Context, since time.Time) (changes []metrics.SettingsChange, reaches time.Time, err error)
}

// LiveTranscript is implemented by whatever can read back a still-running
// run's own transcript-in-progress by run ID -- pkg/agent/claude's own
// LiveTranscriptDir, structurally, the same decoupling LogSource already
// gives pkg/systemlog: this package names the seam, a framework package
// fills it, and neither imports the other.
type LiveTranscript interface {
	// Tail returns runID's transcript-in-progress. ok is false if
	// nothing has been recorded for that run yet (or ever) -- a caller
	// should fall back to the store the same way it would if
	// LiveTranscripts were nil.
	Tail(runID string) (text string, ok bool, err error)
}

// ArchivedTranscript reads finished run transcripts stored outside model.Store
// (for example in gs://<bucket>/instances/<name>/transcripts/ behind a local
// read-through disk cache).
type ArchivedTranscript interface {
	Read(ctx context.Context, runID string) (text string, ok bool, err error)
}

// ArchivedTranscriptSizes is an optional extension of ArchivedTranscript that
// reports byte sizes of stored transcripts for the metrics report.
type ArchivedTranscriptSizes interface {
	Sizes(ctx context.Context) ([]model.RunTranscript, error)
}

// Upgrader is the subset of *upgrade.Upgrader the UI needs -- an
// interface so a test can fake an upgrade without a real git checkout,
// docker daemon, or restart command behind it.
type Upgrader interface {
	Start(branch string) error
	// ValidateBranch is the check Start makes of a branch, for an
	// upgrade that is being scheduled now and started later: a branch
	// Start would refuse is refused while somebody is still looking.
	ValidateBranch(branch string) (string, error)
	Status() (upgrade.Status, error)
	// Mode is which pipeline a Start would run -- the pane describes
	// what the button is about to do, and the two pipelines do
	// genuinely different things (upgrade.Mode's own doc comment).
	Mode() upgrade.Mode
	// Running is where the deployment already is, so the pane can say
	// "now running X, upgrading to Y" rather than leaving an operator to
	// infer the X from the last attempt's Detail string -- which is
	// missing entirely on a deployment installed, or last cut over, by
	// scripts/setup.sh (upgrade.Running, grain/task-50). The error means
	// "cannot say"; a caller shows nothing rather than failing.
	Running() (upgrade.Running, error)
}

// DefaultActor is the principal a deployment gets without saying
// otherwise. A human ID rather than an automation one: the thing on the
// other end of this package is a person at a terminal or a browser, and
// model.LandsQueued's own rule turns on that distinction.
func DefaultActor(id string) model.Principal {
	if id == "" {
		id = "operator"
	}
	return model.Principal{Kind: model.PrincipalHuman, ID: id}
}

// StateRepoSyncFailure is Config.StateRepoSyncFailure's answer.
type StateRepoSyncFailure struct {
	// Since is when the sync first failed in the current run of failures.
	Since time.Time `json:"since"`
	// LastSuccess is when a cycle last completed cleanly in this process,
	// zero when none has since it started.
	LastSuccess time.Time `json:"lastSuccess,omitempty"`
	// Error is the latest failure's own words.
	Error string `json:"error"`
	// Repo names the repository the settings are not reaching, for the
	// sentence ("not exported to grain/config since ...").
	Repo string `json:"repo,omitempty"`
	// External is set when Repo is an external config repository, which
	// grain only reads from: what is failing there is configuration
	// merged in that repository not reaching this deployment, not the
	// UI's settings failing to be exported (UI settings are read-only).
	External bool `json:"external,omitempty"`
}
