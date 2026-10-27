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

// Package demo is `grain demo`'s throwaway deployment: a real
// pkg/ui.Server over a real embedded SQLite database in a fresh temp
// directory, seeded with fake tasks, for trying out the frontend with no
// daemon, no orchestrator, no sandbox, no Gemini key and no real git repo
// behind any of it. cmd/grain's demo.go parses the command's flags into an
// Options and hands it to Run; everything that builds the store, seeds it
// and wires the opt-in scenarios lives here.
package demo

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/systemlog"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// Options is `grain demo`'s flags, parsed and checked: each field is the
// flag of the same name in pkg/cli/demo.go, whose help text says what it
// does.
type Options struct {
	// AccessCodeFile is where the demo's access code lives, written there
	// if it is not already: every caller, loopback included, signs in
	// with it, as on a real deployment (docs/access-code.md). Empty keeps
	// it beside the throwaway store and logs it.
	AccessCodeFile string
	Addr           string
	Actor          string
	Repo           model.RepoRef // -default-target-repo
	Open           bool
	Observability  bool
	// Staging runs two fake persistent sandboxes for the rail's Staging
	// group (demo_persistent_sandboxes.go).
	Staging          bool
	Unconfigured     bool
	AgentPaused      bool
	MergeQueueStates bool
	FakeCI           bool
	GitHubSim        bool
	// ScheduledWorkflows runs the daemon's "declarations" and
	// "scheduled-workflows" reconcilers against -github-sim's repo, adds
	// its candidate branch, and lets a -fake-ci run be finished
	// (demo_scheduled_workflows.go). It needs FakeCI and GitHubSim.
	ScheduledWorkflows bool
	// GitProxy is -git-proxy (demo_gitproxy.go): grain's git proxy in
	// front of -github-sim's repo. It needs GitHubSim.
	GitProxy bool
	// BaseBranchCI is -base-branch-ci (demo_base_branch.go): POST
	// /api/e2e/base-branch/{red,green} turns a branch of -github-sim's
	// repo red or green through grain's own base-branch reconciler. It
	// needs FakeCI and GitHubSim.
	BaseBranchCI bool
	// GitHubSimPublic is -github-sim-public: -git-proxy's simulated
	// forge serves its repo to an anonymous fetch, as GitHub serves a
	// public repository -- what a team whose git credential is "None
	// (public)" reaches. An anonymous push is refused either way.
	GitHubSimPublic bool
	MergeRules      bool
	SensitiveHold   bool
	MechanicalPort  bool
	// ReviewQuestions is -review-questions (demo_review_questions.go): a
	// change held on its review's open questions for its owner, and the
	// review-questions reconciler every Reconcile interval (default 2s),
	// so an owner's reply sends that change back to the queue as a
	// daemon's cycle would.
	ReviewQuestions bool
	// ExternalConfigRepo is -external-config-repo
	// (demo_external_config.go): the remote of an external config
	// repository this deployment's configuration is managed in, so every
	// configuration write is refused with the edit to commit there.
	// ExternalConfigBranch is its branch, "main" when empty.
	ExternalConfigRepo   string
	ExternalConfigBranch string
	// Qualification is -qualification (demo_qualification.go): a release
	// candidate whose qualification run waits for approval.
	Qualification bool
	// SecretsStore is -secrets-store (demo_qualification.go): a real
	// secrets store in the demo's throwaway directory.
	SecretsStore bool
	GitHubAPI    string
	UpgradeImage string
	// UpgradeAllowedBranches is -upgrade-allowed-branches, already split
	// into its patterns.
	UpgradeAllowedBranches []string
	UpgradeVerifyCmd       []string
	// ModelCatalog is -model-catalog: the host:port of an Anthropic model
	// listing stub on loopback (demo_model_catalog.go). Empty wires no
	// lister, so model discovery has nothing to ask.
	ModelCatalog string
	// ModelDiscoveryInterval is -model-discovery-interval: how often a
	// demo with a -model-catalog asks its provider again. Zero means
	// ui.ModelDiscoveryInterval.
	ModelDiscoveryInterval time.Duration
	// Schedules is -schedules (demo_schedules.go): the daemon's
	// "schedule" reconciler every Reconcile interval (default 2s), so a
	// schedule that comes due fires, or records its skip, on its own.
	Schedules bool
	// Reconcile is -reconcile (demo_reconcile.go): how often to run the
	// reconcile loop's suite step, and its release step too with
	// -github-sim. Zero runs no loop at all.
	Reconcile time.Duration
	// HostExecutor is -host-executor: which deployment Settings -> Models'
	// run locations are resolved as (demoCLIRunConfig).
	HostExecutor string
	// HostModeInstall is -host-mode-install: GET /api/config reports the
	// install notice an unmigrated host-mode install gets
	// (pkg/daemon's installnotice.go).
	HostModeInstall bool
}

// demoCLIRunConfig is the daemon config Settings -> Models' run locations
// are resolved under, by -host-executor:
//   - "" (the default): a development daemon on the bare machine, which
//     offers only host, with claude set to the container a release
//     before v2.3.0 could have stored -- shown as running on the host,
//     with a warning;
//   - "kubernetes": deploy/kubernetes/grain.yaml's daemon, which has no
//     host executor and sets no run location;
//   - "none": a daemon in the release container with no grain hostd;
//   - "hostd-down": a daemon in the release container whose hostd is
//     configured but not answering, with agy set to run through it;
//   - "hostd": the one install shape, a daemon in the release container
//     with a real grain hostd beside it (startDemoHostd) offering agy and
//     one capability server, and agy set to run through it;
//   - "hostd-remote": a Kubernetes daemon taking a grain hostd that dials
//     in to it (grain hostd -server), with a real one dialing in over
//     loopback (startDemoRemoteHostd), offering what "hostd"'s does;
//   - "hostd-remote-down": the same daemon with no hostd dialed in.
func demoCLIRunConfig(executor string) (daemon.Config, error) {
	inContainer := whererun.Detection{Host: false, Signal: whererun.InContainerEnv + "=1, which grain's deployment image sets"}
	noSocketDir := func(string) bool { return false }
	var c daemon.Config
	switch executor {
	case "":
		return daemon.Config{
			ClaudeRun: model.CLIRunContainer, AgyRun: model.CLIRunHost,
			AgyPath: "/opt/agy/bin/agy",
		}, nil
	case "kubernetes":
		c = daemon.Config{K8sSandboxes: true}
	case "none":
	case "hostd-down":
		c = daemon.Config{AgyRun: model.CLIRunHost, HostdSocket: filepath.Join(os.TempDir(), "grain-demo-hostd-not-running.sock")}
	case "hostd":
		socket, err := startDemoHostd()
		if err != nil {
			return daemon.Config{}, err
		}
		c = daemon.Config{AgyRun: model.CLIRunHost, HostdSocket: socket}
	case "hostd-remote", "hostd-remote-down":
		remote := &hostd.Remote{Token: "grain-demo-hostd-token"}
		if executor == "hostd-remote" {
			if err := startDemoRemoteHostd(remote); err != nil {
				return daemon.Config{}, err
			}
		}
		c = daemon.Config{K8sSandboxes: true, AgyRun: model.CLIRunHost, HostdRemote: remote}
	default:
		return daemon.Config{}, fmt.Errorf("grain demo: -host-executor=%q: want kubernetes, none, hostd-down, hostd, hostd-remote or hostd-remote-down", executor)
	}
	exec := daemon.DetectHostExecutor(c, inContainer, noSocketDir)
	c.HostExec = &exec
	return c, nil
}

// demoGitHubToken is the demo's one fake GitHub token, the "default"
// credential every demo team is given. -git-proxy's simulated forge
// accepts it and nothing else.
const demoGitHubToken = "ghp_demo_secret_token_acme_widgets"

// Run builds the demo's throwaway store and server and serves it on
// opts.Addr until serving fails. Every error it returns already reads
// "grain demo: ...", ready for the command to print as it is.
func Run(opts Options) error {
	repo := opts.Repo
	cfg := ui.Config{
		Actor:         ui.DefaultActor(daemon.ActorID(opts.Actor)),
		DefaultTarget: &repo,
		Capabilities:  append(ui.OfferedCapabilities(), ui.APICapabilities()...),
		// The sub-case of each task's state (ui.Config.Reasons): a pure
		// function of the rows, so the demo shows it as a deployment does.
		Reasons: daemon.ReasonClassifier{},
	}
	runCfg, err := demoCLIRunConfig(opts.HostExecutor)
	if err != nil {
		return err
	}
	demoSandboxCfg := runCfg
	if demoSandboxCfg.SandboxBackend() == "" {
		demoSandboxCfg.ContainerSandboxes = true
		demoSandboxCfg.ContainerImage = daemon.DefaultSandboxImage
		demoSandboxCfg.ContainerGitProxyHost = "172.17.0.1"
	}
	cfg.SandboxBackends = demoSandboxCfg.AvailableSandboxBackends()
	cfg.SandboxBackendOptions = demoSandboxCfg.SandboxBackendOptions()
	cfg.CLIRuns = func(ctx context.Context, stored map[string]model.CLIRunSetting) any {
		c := runCfg
		c.CLIRun = stored
		return c.ResolveCLIRuns(ctx)
	}
	cfg.CLIRunCheck = runCfg.CheckCLIRunSettings
	cfg.HostExecutor = func(ctx context.Context) any { return runCfg.HostExecutorStatus(ctx) }
	if opts.HostModeInstall {
		notice := daemon.HostModeNotice(
			[]string{"the daemon runs on the bare machine (no container marker found)", "started by systemd with GRAIN_INSTALL_MODE=host"},
			[]daemon.CLIRunResolution{{Framework: model.AgentFrameworkClaude, Location: model.CLIRunHost}})
		cfg.InstallNotice = func() *ui.InstallNotice { return notice }
	}

	dir, err := os.MkdirTemp("", "grain-demo-")
	if err != nil {
		return fmt.Errorf("grain demo: creating a throwaway store: %v", err)
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		return fmt.Errorf("grain demo: opening the throwaway store: %v", err)
	}
	defer db.Close()

	// The one piece of this demo that is not a row in the store: the
	// System pane's Logs tab, which reads ui.Config.Logs rather than
	// anything seedDemo writes. A real deployment hands it a
	// systemlog.Journalctl over grain-daemon.service (cmd/grain/
	// daemon.go), and a demo has no daemon, no unit and no journal -- so
	// it gets the same pane over the other LogSource this project already
	// ships, systemlog.File, over a fake journal written into the same
	// throwaway directory the store lives in. Everything above that file
	// is then the real thing: the real Tail, the real ?lines= truncation,
	// the real handler and the real pane, rather than a note saying this
	// deployment has no log sources configured.
	logPath := filepath.Join(dir, "demo-daemon.log")
	if err := writeDemoLog(logPath); err != nil {
		return fmt.Errorf("grain demo: seeding a fake journal: %v", err)
	}
	cfg.Logs = map[string]ui.LogSource{"daemon": systemlog.File{Path: logPath}}
	// GET /api/metrics' github section, which System → Health reads for
	// its GitHub budget panel: a demo makes no GitHub calls of its own,
	// so it reports a fixed, plausible hour instead (demoGitHubCalls).
	cfg.GitHubCalls = demoGitHubCalls{}

	// Fake GitHub credentials under dir/secrets/github so GitCredentialsConfigured is true.
	githubSecretsDir := filepath.Join(dir, "secrets", "github")
	if err := os.MkdirAll(githubSecretsDir, 0o700); err != nil {
		return fmt.Errorf("grain demo: creating secrets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(githubSecretsDir, "default.token"), []byte(demoGitHubToken+"\n"), 0o600); err != nil {
		return fmt.Errorf("grain demo: writing default.token: %v", err)
	}
	creds, err := gitproxy.LoadCredentialSet(githubSecretsDir)
	if err != nil {
		return fmt.Errorf("grain demo: loading credential set: %v", err)
	}
	cfg.Credentials = creds
	if opts.GitHubAPI != "" {
		// What a deployment wires (daemon.go's gitproxy.Reach over its
		// GitHub transport), pointed at a stub standing in for GitHub:
		// without one the demo has no listing to refuse a task on, and a
		// Test button press would go to api.github.com.
		api := github.NewRealTransport(opts.GitHubAPI)
		api.UseTLS = false
		cfg.GitReach = &gitproxy.Reach{Credentials: creds, API: api}
	}

	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		return fmt.Errorf("grain demo: applying the schema: %v", err)
	}
	cfg.GitHubPullRequests = demoPullRequests{repo: repo, store: store}
	// The demo is single-tenant, as every daemon is: every caller,
	// loopback included, signs in with its access code. Without
	// -access-code-file the code is kept beside the throwaway store.
	accessCodeFile := opts.AccessCodeFile
	if accessCodeFile == "" {
		accessCodeFile = filepath.Join(dir, ui.LocalAPITokenFileName)
	}
	code, err := ui.EnsureLocalAPIToken(accessCodeFile)
	if err != nil {
		return fmt.Errorf("grain demo: writing the access code: %v", err)
	}
	cfg.LocalAPIToken, cfg.LocalAPITokenFile = code, accessCodeFile
	store.SingleTenant = true
	if opts.AccessCodeFile == "" {
		log.Printf("grain demo: the access code is %s (kept in %s)", code, accessCodeFile)
	}

	storedCfg := model.DefaultConfig()
	storedCfg.AgentBackends = []model.AgentBackend{
		{
			ID:        "gemini-pro",
			Name:      "Gemini 2.5 Pro",
			Framework: model.AgentFrameworkAntigravity,
			Models:    []string{"gemini-2.5-pro", "gemini-2.5-flash"},
		},
	}
	storedCfg.DefaultModel = "gemini-2.5-pro"
	storedCfg.GCPProject = "grain-sandbox-dev"
	storedCfg.GCPServiceAccountEmail = "grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com"
	storedCfg.DefaultCapabilities = []string{"self-debug", "gcp-key", "gemini-key"}
	storedCfg.TargetRepos = []string{"acme/widgets"}
	// The deployment's git connection to the fake credential above, as
	// `grain connections import` would record it.
	storedCfg.GitConnections = []model.TeamGitConnection{{Credential: "default"}}
	storedCfg.MaxWorkers = 4
	storedCfg.SandboxBackend = demoSandboxCfg.SandboxBackend()
	if opts.GitHubAPI != "" {
		storedCfg.GitHubHost = opts.GitHubAPI
		storedCfg.GitHubInsecureHTTP = true
	}
	// cfg.Secrets is read at each lookup, so -secrets-store's store,
	// configured below, is the one a saved server configuration uses.
	capabilityServers, err := configureCapabilityServerDemo(ctx, &storedCfg, func(name string) (string, bool) {
		if cfg.Secrets == nil {
			return "", false
		}
		v, err := cfg.Secrets.Resolve(ctx, name)
		return v, err == nil
	}, store)
	if err != nil {
		return fmt.Errorf("grain demo: %v", err)
	}
	cfg.CapabilityServers = capabilityServers
	// A server saved in Settings is attached the way the daemon's
	// reconcile tick would: the stored entries are handed to the registry.
	wake := cfg.Wake
	cfg.Wake = func() {
		if wake != nil {
			wake()
		}
		if stored, err := store.GetConfig(ctx); err == nil && stored != nil {
			capabilityServers.Update(stored.MCPServers)
		}
	}
	if err := store.PutConfig(ctx, storedCfg); err != nil {
		return fmt.Errorf("grain demo: storing config: %v", err)
	}
	var observabilityBackend *demoObservabilityBackend
	if opts.Observability {
		cfg, observabilityBackend, err = configureObservabilityDemo(ctx, dir, store, cfg)
		if err != nil {
			return fmt.Errorf("grain demo: configuring observability scenario: %v", err)
		}
	}
	if opts.FakeCI {
		if cfg, err = configureFakeCIDemo(ctx, store, cfg, repo); err != nil {
			return fmt.Errorf("grain demo: -fake-ci: %v", err)
		}
	}
	var githubSimRepo string
	if opts.GitHubSim {
		if cfg, githubSimRepo, err = configureGitHubSimDemo(dir, cfg, repo); err != nil {
			return fmt.Errorf("grain demo: -github-sim: %v", err)
		}
	}
	var scheduledWorkflows *orchestrator.Deps
	if opts.ScheduledWorkflows {
		if !opts.FakeCI || !opts.GitHubSim {
			return fmt.Errorf("grain demo: -scheduled-workflows runs workflows on -fake-ci against -github-sim's repo, so it needs both")
		}
		client, _ := cfg.GitHubBranches.(github.Client)
		deps, err := configureScheduledWorkflowsDemo(store, repo, githubSimRepo, cfg.CI.(*demoFakeCI), client)
		if err != nil {
			return fmt.Errorf("grain demo: -scheduled-workflows: %v", err)
		}
		scheduledWorkflows = &deps
		log.Printf("grain demo: -scheduled-workflows reads %s's declarations from %s; push .grain files there", repo, githubSimRepo)
	}
	var gitProxy *demoGitProxy
	if opts.GitProxy {
		if !opts.GitHubSim {
			return fmt.Errorf("grain demo: -git-proxy forwards to -github-sim's repo, so it needs -github-sim too")
		}
		if gitProxy, err = configureGitProxyDemo(dir, store, repo, githubSimRepo, demoGitHubToken, opts.GitHubSimPublic); err != nil {
			return fmt.Errorf("grain demo: -git-proxy: %v", err)
		}
	}
	var baseBranch *demoBaseBranch
	if opts.BaseBranchCI {
		if !opts.FakeCI || !opts.GitHubSim {
			return fmt.Errorf("grain demo: -base-branch-ci judges -github-sim's branches on -fake-ci verdicts, so it needs both")
		}
		client, _ := cfg.GitHubBranches.(github.Client)
		baseBranch = configureBaseBranchDemo(store, repo, cfg.CI.(*demoFakeCI), client)
	}
	// Stand-ins for what sits outside grain (demo_scenarios.go): a
	// credential's issuer and remote executors, on every demo; a fresh
	// install's missing setup and a provider's usage limit, on request.
	cfg.CapabilityChecks = demoCapabilityChecks{}
	cfg.Executors = demoExecutors{started: time.Now().UTC()}
	// -staging: two staging environments for the rail's Staging group
	// (demo_persistent_sandboxes.go). Off by default, where System ->
	// Sandboxes says it has no sandbox backend to report on.
	if opts.Staging {
		persistent, err := newDemoPersistentSandboxes(ctx)
		if err != nil {
			return fmt.Errorf("grain demo: -staging: %v", err)
		}
		cfg.PersistentSandboxes = persistent
	}
	if opts.Unconfigured {
		if cfg, err = unconfigureDemo(ctx, store, cfg); err != nil {
			return fmt.Errorf("grain demo: -unconfigured: %v", err)
		}
	}
	if opts.UpgradeImage != "" {
		if cfg, err = configureUpgradeDemo(dir, store, cfg, opts.UpgradeImage, opts.UpgradeAllowedBranches, opts.UpgradeVerifyCmd); err != nil {
			return fmt.Errorf("grain demo: -upgrade-image: %v", err)
		}
		log.Printf("grain demo: upgrades pull %s and rewrite %s, and restart nothing", opts.UpgradeImage, filepath.Join(dir, demoUpgradeRefFile))
	}
	if opts.SecretsStore {
		if cfg, err = configureSecretsDemo(dir, cfg); err != nil {
			return fmt.Errorf("grain demo: -secrets-store: %v", err)
		}
	}
	if opts.AgentPaused {
		cfg.AgentPause = demoAgentPause(time.Now().UTC())
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		return fmt.Errorf("grain demo: seeding fake tasks: %v", err)
	}
	if opts.MergeQueueStates {
		if err := seedMergeQueueStatesDemo(ctx, store, cfg); err != nil {
			return fmt.Errorf("grain demo: seeding merge queue states: %v", err)
		}
	}
	if opts.MergeRules {
		if err := seedMergeRulesDemo(ctx, store, cfg, repo); err != nil {
			return fmt.Errorf("grain demo: -merge-rules: %v", err)
		}
	}
	if opts.SensitiveHold {
		if err := seedSensitiveHoldDemo(ctx, store, cfg, repo, githubSimRepo); err != nil {
			return fmt.Errorf("grain demo: -sensitive-hold: %v", err)
		}
	}
	if opts.ReviewQuestions {
		if err := seedReviewQuestionsDemo(ctx, store, cfg, repo, githubSimRepo); err != nil {
			return fmt.Errorf("grain demo: -review-questions: %v", err)
		}
	}
	if opts.MechanicalPort {
		if err := seedMechanicalPortDemo(ctx, store, cfg, repo); err != nil {
			return fmt.Errorf("grain demo: -mechanical-port: %v", err)
		}
	}
	if opts.Qualification {
		if err := seedQualificationDemo(ctx, store, repo); err != nil {
			return fmt.Errorf("grain demo: -qualification: %v", err)
		}
	}

	creds.SetConnections(func(owner, repo string) (string, bool) {
		name, err := store.GitConnectionOn(ctx, "", owner, repo)
		return name, err == nil && name != ""
	})
	if opts.ModelCatalog != "" {
		cfg.ClaudeModels = demoModelCatalog{baseURL: "http://" + opts.ModelCatalog}
	}
	if opts.ExternalConfigRepo != "" {
		// Only now, past every seed above: the seeds write configuration
		// through the same client guard this turns into a refusal.
		cfg.StateRepo = newExternalConfigRepo(opts.ExternalConfigRepo, opts.ExternalConfigBranch)
	}
	client := ui.NewClient(cfg, store)
	if opts.ModelCatalog != "" {
		interval := opts.ModelDiscoveryInterval
		if interval <= 0 {
			interval = ui.ModelDiscoveryInterval
		}
		// What startUIServer runs beside a deployment's UI: the retry for
		// a connection saved while its provider could not be asked.
		go client.RunModelDiscovery(context.Background(), interval)
	}
	if opts.Reconcile > 0 {
		// Releases only against the simulated GitHub: it is the one
		// GitHub a demo has, and the client the Branches pane already
		// lists it through.
		var releases github.Client
		if opts.GitHubSim {
			releases, _ = cfg.GitHubBranches.(github.Client)
		}
		go runDemoReconcile(context.Background(), store, releases, opts.Reconcile)
	}
	if scheduledWorkflows != nil {
		interval := opts.Reconcile
		if interval <= 0 {
			interval = 2 * time.Second
		}
		go runDemoScheduledWorkflows(context.Background(), *scheduledWorkflows, interval)
	}
	if opts.Schedules {
		interval := opts.Reconcile
		if interval <= 0 {
			interval = 2 * time.Second
		}
		go runDemoSchedules(context.Background(), store, interval)
	}
	if opts.ReviewQuestions {
		interval := opts.Reconcile
		if interval <= 0 {
			interval = 2 * time.Second
		}
		go runDemoReviewQuestions(context.Background(), store, interval)
	}
	var srv http.Handler = ui.NewServerWithClient(client)
	if scheduledWorkflows != nil {
		srv = scheduledWorkflowsDemoHandler(srv, cfg.CI.(*demoFakeCI), githubSimRepo)
	}
	if observabilityBackend != nil {
		srv = observabilityDemoHandler(srv, observabilityBackend)
	}
	if gitProxy != nil {
		srv = gitProxyDemoHandler(srv, gitProxy)
	}
	if baseBranch != nil {
		srv = baseBranchDemoHandler(srv, baseBranch)
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("grain demo: listening on %s: %v", opts.Addr, err)
	}
	// ln.Addr() rather than opts.Addr, so a demo told to listen on port 0
	// says which port the kernel actually gave it. ui/e2e/fixtures.js
	// does exactly that -- it starts one of these per Playwright worker
	// on 127.0.0.1:0 and reads the port back off this line, having no
	// other way to learn it -- so the URL here is a small interface as
	// well as a courtesy to whoever ran the command.
	url := "http://" + ln.Addr().String()
	log.Printf("grain demo: serving fake tasks from a throwaway store at %s -- nothing here is real", url)
	if opts.Open {
		daemon.OpenBrowser(url)
	}
	return http.Serve(ln, ui.HostGuard(srv, opts.Addr, nil))
}

// seedDemo populates a store with a fixed set of fake tasks for `grain
// demo`: one in each model.State a task can read, so a person working on
// pkg/ui/static's frontend can see every card style, badge and button the
// real states produce without an orchestrator, a sandbox, a Gemini key or
// a real git repo behind any of it. It goes through ui.Client and
// model.Store's own exported writes -- the same path a human clicking
// through the UI or the daemon's reconcile loop would take -- rather than
// inserting rows directly, so a demo task is exactly as well-formed as a
// real one and nothing here can drift from what those paths actually
// require. It seeds two saved boards on the way out for the same
// reason -- see the comment on them -- since a Kanban board and a
// timeline are boards now rather than fixed views.
//
// It declares a handful of custom fields first, for the same reason
// again: a deployment's own fields (model.CustomField) are the one part
// of a task that is not built in, so a demo that declares none shows a
// new-task form with no Fields block, rows with no chips and a
// narrowing bar whose menus stop at the built-ins -- none of which is
// what a real deployment looks like. demoCustomFields says which, and
// why there are that many.

func seedDemo(ctx context.Context, store *model.Store, cfg ui.Config) error {
	client := ui.NewClient(cfg, store)
	now := time.Now().UTC()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	// Dates are relative to today, like everything else here: a demo
	// whose one due date was written into this file as a literal would
	// be a demo of an overdue task by the end of the year.
	day := func(d time.Duration) string { return now.Add(d).Format(model.CustomFieldDateLayout) }

	// The declarations before the first task, since a filing is checked
	// against them: a value for a field nothing declares is dropped
	// (ui.Client.normalizeCustomFields), so seeding these second would
	// seed tasks carrying nothing.
	for _, field := range demoCustomFields() {
		if err := store.PutCustomField(ctx, field); err != nil {
			return fmt.Errorf("declaring the %q field: %w", field.Name, err)
		}
	}

	// One API scope set of the deployment's own -- docs/agent-api.md's
	// example -- so Settings → Capabilities shows a config-repo set beside
	// the built-ins. Declared only: nothing here grants it.
	if err := store.PutAPIScopeSet(ctx, model.APIScopeSet{Name: "ops/triage",
		Description: "Triage the queue and its CI on grain itself",
		Includes:    []string{"grain/ci-read", "grain/task-admin"},
		Deny:        []string{"tasks:admin"}, Repos: []string{"bwsalmon/grain"}, Version: 1}); err != nil {
		return fmt.Errorf("declaring the ops/triage scope set: %w", err)
	}
	if err := store.LoadAPIScopeSets(ctx); err != nil {
		return fmt.Errorf("installing the demo's scope sets: %w", err)
	}

	// The backdated clock is put back after each filing, so nothing
	// seeded after the last task inherits its createdAt as "now": the
	// schedules below would otherwise be timed from 40 minutes ago, and
	// "daily at 00:00" seeded in the first 40 minutes of a UTC day comes
	// out already due and fires the moment a -schedules demo starts.
	create := func(createdAt time.Time, req ui.CreateTaskRequest) (ui.Task, error) {
		client.Now = func() time.Time { return createdAt }
		defer func() { client.Now = nil }()
		task, err := client.CreateTask(ctx, req)
		if err != nil {
			return ui.Task{}, fmt.Errorf("seeding %q: %w", req.Title, err)
		}
		return task, nil
	}

	// Values on some of the tasks and not others, which is what a real
	// deployment's tasks look like: "nobody said" is the ordinary answer
	// for a field (model.CustomField.NormalizeValue), and a demo where
	// every task carried every field would show none of the panes that
	// have something to say about a field nobody filled in.
	darkMode, err := create(ago(4*time.Hour), ui.CreateTaskRequest{
		Title:       "Add a dark mode toggle",
		Description: "Settings page needs a theme switch; several people have asked for it.",
		// The one task with every field filled in, so the detail pane has
		// a row of each kind to show.
		CustomFields: map[string]ui.CustomFieldValue{
			"priority":        {"medium"},
			"area":            {"frontend"},
			"requested-by":    {"Northwind"},
			"due-date":        {day(21 * 24 * time.Hour)},
			"estimate":        {"3"},
			"customer-facing": {"true"},
		},
	})
	if err != nil {
		return err
	}

	if _, err := create(ago(3*time.Hour), ui.CreateTaskRequest{
		Title:       "Fix flaky retry test",
		Description: "TestRetryBackoff fails about one run in twenty under -race.",
		Approved:    true,
		CustomFields: map[string]ui.CustomFieldValue{
			"priority": {"high"},
			"area":     {"backend"},
		},
	}); err != nil {
		return err
	}

	running, err := create(ago(90*time.Minute), ui.CreateTaskRequest{
		Title:        "Bump the Go toolchain to 1.24",
		Description:  "go.mod and the CI image both need the version bump.",
		Approved:     true,
		Capabilities: &[]string{"self-debug"},
		CustomFields: map[string]ui.CustomFieldValue{
			"priority": {"medium"},
			// The field that holds a set, on the task with more than one
			// answer to give: a row and a card write it as "backend, infra"
			// (CustomFields.jsx's displayValue).
			"area": {"backend", "infra"},
		},
	})
	if err != nil {
		return err
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        "demo-run-" + running.ID,
		TaskID:    running.ID,
		Sandbox:   "demo-sandbox",
		Attempt:   1,
		StartedAt: ago(6 * time.Minute),
	}, model.Limits{}); err != nil {
		return fmt.Errorf("seeding a running task: %w", err)
	}
	if err := store.SetRunModel(ctx, "demo-run-"+running.ID, "gemini-3.1-pro", "antigravity"); err != nil {
		return fmt.Errorf("seeding a running task: %w", err)
	}
	// The prompt that run was "given", built by the same function a real
	// dispatch builds one with (orchestrator.BuildPrompt) rather than
	// written out here: what the UI's own prompt pane shows in the demo
	// is then the real thing, and stays the real thing as that function
	// changes, instead of a paraphrase of it that quietly goes stale.
	//
	// Read back through the store rather than built from the request
	// above: BuildPrompt takes the model.Task that was actually filed,
	// with the id and target ui.CreateTask resolved for it. A failure
	// here fails the seed, like every other write in this function -- a
	// demo whose prompt pane silently says "nothing has been dispatched"
	// is the one thing that pane must not say about a running task.
	//
	// The wall-clock budget is DefaultMaxRunRuntime for the same reason:
	// there is no orchestrator.Config behind a seeded run to read a
	// deployment's own MaxRunRuntime off, and the default is what a
	// deployment that has not set one really gives its runs.
	seeded, err := store.GetTask(ctx, running.ID)
	if err != nil {
		return fmt.Errorf("re-reading the running task: %w", err)
	}
	if seeded == nil {
		return fmt.Errorf("re-reading the running task: %s was not stored", running.ID)
	}
	// The sandbox directory is the one fact here with no seeded answer:
	// a real dispatch asks its sandbox where its tools land
	// (orchestrator's own sandboxWorkingDir) and there is no sandbox
	// behind a seeded run to ask. A guest's own home is what that
	// question answers with on a real deployment, so the demo's prompt
	// pane shows the paragraph a real run gets rather than dropping it.
	if err := store.SetRunPrompt(ctx, "demo-run-"+running.ID,
		orchestrator.BuildPrompt(*seeded, orchestrator.CheckoutDir, "/home/grain", true,
			orchestrator.DefaultMaxRunRuntime,
			// A first attempt: this seeded run has no earlier one to
			// carry an outcome, and nothing has been said on the task,
			// so orchestrator.History{} is what a real dispatch of it
			// would have assembled too.
			orchestrator.History{Attempt: 1}, nil)); err != nil {
		return fmt.Errorf("seeding a running task's prompt: %w", err)
	}
	// ... and the transcript that run has written so far, which is what
	// clicking the attempt in the task's own Timeline opens
	// (AttemptTranscriptOverlay). A real deployment serves a live run's
	// from ui.Config.LiveTranscripts and a finished one's from this same
	// task_run column (ui.Client.AttemptTranscript), so recording one
	// here is how a demo shows that pane at all -- without it the only
	// thing the demo can demonstrate about a transcript is its "nothing
	// written yet" placeholder.
	if err := store.SetRunTranscript(ctx, "demo-run-"+running.ID, demoTranscript()); err != nil {
		return fmt.Errorf("seeding a running task's transcript: %w", err)
	}

	awaiting, err := create(ago(2*time.Hour), ui.CreateTaskRequest{
		Title:       "Investigate the merge queue stall",
		Description: "Queue head hasn't moved since last night; find out why.",
		Approved:    true,
		CustomFields: map[string]ui.CustomFieldValue{
			"priority": {"high"},
			"area":     {"infra"},
		},
	})
	if err != nil {
		return err
	}
	questionAt := ago(20 * time.Minute)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: awaiting.ID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: cfg.Actor.ID},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "demo-run-" + awaiting.ID},
		},
		Body: "Two PRs are both stuck failing the same lint check -- want me to fix both, or just unblock the queue?\n\n" +
			"Both fail `golangci-lint` on the same unused import, which landed on main before the " +
			"linter was pinned. Fixing both means two small commits; unblocking the queue means " +
			"skipping the check for them once and filing the cleanup as its own task.",
		Summary:   "Fix both lint-stuck PRs, or only unblock the merge queue?",
		CreatedAt: questionAt,
	})
	if err != nil {
		return fmt.Errorf("seeding an awaiting-reply task: %w", err)
	}
	if err := store.ObserveField(ctx, awaiting.ID, questionAt, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	}); err != nil {
		return fmt.Errorf("seeding an awaiting-reply task: %w", err)
	}

	completed, err := create(ago(30*time.Hour), ui.CreateTaskRequest{
		Title:       "Rotate the Gemini signing key",
		Description: "Old key expires this week.",
		Approved:    true,
		CustomFields: map[string]ui.CustomFieldValue{
			"priority": {"high"},
			"area":     {"infra"},
			"due-date": {day(2 * 24 * time.Hour)},
			// A value for a field this deployment used to declare and does
			// not any more -- see demoRetiredField, which is what takes the
			// declaration away again once this task is carrying it.
			demoRetiredField.Name: {"2026-Q2"},
		},
	})
	if err != nil {
		return err
	}
	completedTask, err := store.GetTask(ctx, completed.ID)
	if err != nil {
		return err
	}
	completedTask.Links = append(completedTask.Links,
		model.Link{Kind: model.LinkFixes, Target: "acme/widgets#101"},
		// A changelist (CL) beside the pull request, so the task page's
		// changelist link (model.LinkChangelist) has one to show.
		model.Link{Kind: model.LinkChangelist, Target: "cl/612345678"})
	// Submitted: model.StateAwaitingSubmit is what an unsubmitted pull
	// request reads as, so this is the field that makes this card the
	// "Queued for merge" one rather than a second copy of the card
	// seeded below it.
	completedTask.AutoMerge = true
	if err := store.PutTask(ctx, *completedTask); err != nil {
		return fmt.Errorf("seeding a completed task: %w", err)
	}
	completedAt := ago(29 * time.Hour)
	if err := store.ObserveField(ctx, completed.ID, completedAt, func(o *model.Observation) {
		o.CompletedAt = &completedAt
	}); err != nil {
		return fmt.Errorf("seeding a completed task: %w", err)
	}
	// ...and the declaration that task's last value belongs to, taken
	// away again now the task is carrying it. A field retired in the
	// state repository leaves every value already recorded for it
	// exactly where it is (model.Store.DeleteCustomField), and the task
	// pane still shows it, under its bare name, because it is what
	// somebody recorded about this task (CustomFields.jsx's fieldRows).
	// That row is a real state of a real deployment and there is nowhere
	// else in a demo to see it -- and it costs the new-task form
	// nothing, since the form renders declarations and this is not one
	// any more.
	if err := store.DeleteCustomField(ctx, demoRetiredField.Name); err != nil {
		return fmt.Errorf("retiring the %q field: %w", demoRetiredField.Name, err)
	}

	// The other post-run state: a run that finished and opened a pull
	// request nobody has submitted, which is where a task sits until a
	// human clicks Submit (model.StateAwaitingSubmit). Every seeded state
	// gets a card so the demo shows the whole vocabulary, and this one is
	// the state a deployment that does not auto-merge lives in.
	awaitingSubmit, err := create(ago(6*time.Hour), ui.CreateTaskRequest{
		Title:       "Cache the capability readiness probe",
		Description: "Settings' Capabilities tab re-probes every render; cache it for a minute.",
		Approved:    true,
	})
	if err != nil {
		return err
	}
	awaitingSubmitTask, err := store.GetTask(ctx, awaitingSubmit.ID)
	if err != nil {
		return err
	}
	awaitingSubmitTask.Links = append(awaitingSubmitTask.Links,
		model.Link{Kind: model.LinkFixes, Target: "acme/widgets#102"})
	if err := store.PutTask(ctx, *awaitingSubmitTask); err != nil {
		return fmt.Errorf("seeding a task awaiting submit: %w", err)
	}
	awaitingSubmitAt := ago(5 * time.Hour)
	if err := store.ObserveField(ctx, awaitingSubmit.ID, awaitingSubmitAt, func(o *model.Observation) {
		o.CompletedAt = &awaitingSubmitAt
	}); err != nil {
		return fmt.Errorf("seeding a task awaiting submit: %w", err)
	}

	// The merge queue testing those two pull requests together: one
	// speculative batch on acme/widgets main, still running, so the repo
	// page's Merge batches panel has a batch to draw. The demo runs no
	// queue, so nothing ever advances it; the row is the one the queue
	// itself writes (model.Store.PutMergeBatch), read back through the
	// real GET /api/merge-batches.
	deploymentID, err := store.DeploymentID(ctx)
	if err != nil {
		return fmt.Errorf("seeding a merge batch: %w", err)
	}
	batchStartedAt := ago(5 * time.Minute)
	batchRef := model.MergeBatchRef(deploymentID, "main")
	if err := store.PutMergeBatch(ctx, model.MergeBatch{
		Repo:       *cfg.DefaultTarget,
		Branch:     "main",
		Ref:        batchRef,
		BaseCommit: "1234567890abcdef1234567890abcdef12345678",
		Commit:     "abcdef0123456789abcdef0123456789abcdef01",
		StartedAt:  batchStartedAt,
		Members: []model.MergeBatchMember{
			{TaskID: completed.ID, Number: 101, HeadRef: completedTask.BranchName(), HeadSHA: "fedcba9876543210fedcba9876543210fedcba98"},
			{TaskID: awaitingSubmit.ID, Number: 102, HeadRef: awaitingSubmitTask.BranchName(), HeadSHA: "0123456789fedcba0123456789fedcba01234567"},
		},
	}); err != nil {
		return fmt.Errorf("seeding a merge batch: %w", err)
	}
	// Every member carries the batching state, as the queue writes it on
	// each one (orchestrator's batch note): a task page draws its Merge
	// batch panel only for a task in that state, so a member seeded
	// without it opened from the repo page's batch list with no batch.
	for i, member := range []string{completed.ID, awaitingSubmit.ID} {
		note := fmt.Sprintf(
			"grain CI is testing this change together with 1 other(s) at the front of the %s main queue, "+
				"on `%s` at abcdef012345: one suite for the batch, and all of them merge if it passes",
			cfg.DefaultTarget, batchRef)
		if i == 0 {
			note += " -- this change is first in the batch, so a failure is bisected towards it"
		}
		if err := store.ObserveField(ctx, member, batchStartedAt, func(o *model.Observation) {
			o.MergeQueueState, o.MergeQueueReason = "batching", "batching/batch_run"
			o.MergeQueueStateAt = &batchStartedAt
			o.MergeQueueNote = note
		}); err != nil {
			return fmt.Errorf("seeding batch state on task %s: %w", member, err)
		}
	}

	// The third post-run state: a run that answered on the task itself
	// and opened no pull request at all, which is where an investigation
	// sits until somebody has read it and closed it
	// (model.StateAwaitingAck). It gets a card of its own because it is
	// the state whose badge used to say "Queued for merge" about a task
	// with nothing to merge, and a demo that seeds only the two states
	// with pull requests behind them would show none of that.
	answered, err := create(ago(4*time.Hour), ui.CreateTaskRequest{
		Title:       "Why did last week's nightly suite slow down?",
		Description: "No change needed unless it turns out to be ours -- just find out.",
		Approved:    true,
	})
	if err != nil {
		return err
	}
	// The answer itself, because the state is about there being something
	// to read: a card in this state with an empty conversation behind it
	// would demonstrate the badge and not the thing the badge is for.
	answeredAt := ago(3 * time.Hour)
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: answered.ID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: cfg.Actor.ID},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "demo-run-" + answered.ID},
		},
		Body: "It is the fixture download, not us: the upstream mirror started rate-limiting " +
			"us on 2026-08-31 and every job now waits ~90s on it. Nothing to change in this repo.",
		CreatedAt: answeredAt,
	}); err != nil {
		return fmt.Errorf("seeding an answered task: %w", err)
	}
	if err := store.ObserveField(ctx, answered.ID, answeredAt, func(o *model.Observation) {
		o.CompletedAt = &answeredAt
	}); err != nil {
		return fmt.Errorf("seeding an answered task: %w", err)
	}

	// A task the merge queue is repairing: its pull request went
	// conflicted, so the queue commented and sent the task itself back to
	// an agent on the very branch that pull request is open from
	// (orchestrator.requeueForRepair). There is no second task and no
	// second branch -- what a reader sees instead is the task running
	// again with its mark in green, which is the whole point of seeding
	// it here.
	repairing, err := create(ago(50*time.Minute), ui.CreateTaskRequest{
		Title:       "Add pagination to the tasks API",
		Description: "GET /api/tasks returns everything at once; needs a page size and a cursor.",
		Approved:    true,
		CustomFields: map[string]ui.CustomFieldValue{
			"priority":     {"low"},
			"area":         {"backend"},
			"requested-by": {"Contoso"},
			"estimate":     {"5"},
		},
	})
	if err != nil {
		return err
	}
	repairingTask, err := store.GetTask(ctx, repairing.ID)
	if err != nil {
		return err
	}
	repairingTask.Links = append(repairingTask.Links,
		model.Link{Kind: model.LinkFixes, Target: "acme/widgets#104"})
	// Submitted, like the completed card above and for a stronger reason:
	// the merge queue only ever repairs one of its own members
	// (orchestrator.isQueueMember, which is AutoMerge), so a task being
	// repaired with no AutoMerge would be a shape no real deployment
	// produces.
	repairingTask.AutoMerge = true
	if err := store.PutTask(ctx, *repairingTask); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}
	repairAskedAt := ago(35 * time.Minute)
	if err := store.ObserveField(ctx, repairing.ID, repairAskedAt, func(o *model.Observation) {
		// Exactly what requeueForRepair writes: the record of the repair,
		// and no CompletedAt, which is what put the task back to work.
		o.MergeQueueRepairAt = &repairAskedAt
		o.CompletedAt = nil
		o.RetryRequestedAt = &repairAskedAt
	}); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}
	queue := model.Principal{Kind: model.PrincipalAutomation, ID: "merge-queue"}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: repairing.ID,
		Author: model.Attribution{Actor: queue},
		Body: "acme/widgets#104 has conflicts with `main`, so the merge queue has sent " +
			"this task back to an agent to repair it. No approval needed, and no separate " +
			"task: the work happens on `" + repairingTask.BranchName() + "` -- the " +
			"branch this task's pull request is already open from -- so the resolution and " +
			"the change share one pull request and one round of checks.",
		CreatedAt: repairAskedAt,
	}); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}
	// The first attempt, which built the change and finished before the
	// merge queue sent the task back, with what its host-script grant
	// recorded doing (model.CapabilityAuditRecord): the records a
	// capability server returns when an attempt releases its grants, and
	// what the attempt's overlay lists above its transcript.
	firstRun := "demo-run-" + repairing.ID + "-1"
	if err := store.StartRun(ctx, model.Run{
		ID: firstRun, TaskID: repairing.ID, Sandbox: "demo-sandbox-repair-1",
		Attempt: 1, StartedAt: ago(48 * time.Minute),
	}, model.Limits{}); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}
	if err := store.SetRunModel(ctx, firstRun, "gemini-3.1-pro", "antigravity"); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}
	firstDone := ago(40 * time.Minute)
	if err := store.FinishRun(ctx, firstRun, firstDone, "succeeded", "the run made 14 tool call(s)"); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}
	if err := store.AppendRunCapabilityAudit(ctx, firstRun, demoCapabilityAudit(repairing.ID, ago)); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        "demo-run-" + repairing.ID,
		TaskID:    repairing.ID,
		Sandbox:   "demo-sandbox-repair",
		Attempt:   2,
		StartedAt: ago(30 * time.Minute),
	}, model.Limits{}); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}
	// The second attempt on the default list's fallback, as a run whose
	// first choice is paused on a usage limit is (ui.Attempt.Model).
	if err := store.SetRunModel(ctx, "demo-run-"+repairing.ID, "gemini-3.1-flash", "antigravity"); err != nil {
		return fmt.Errorf("seeding a task under repair: %w", err)
	}

	// A task somebody put aside (model.StateDeferred): approved once, so
	// that picking it back up would put it straight onto the queue, and
	// hidden from the list and the board until the reader asks for it --
	// which is the whole of what the state does and worth seeing here.
	deferred, err := create(ago(150*time.Hour), ui.CreateTaskRequest{
		Title:       "Replace the settings pane with a form library",
		Description: "Worth doing, not worth doing this quarter.",
		Approved:    true,
		CustomFields: map[string]ui.CustomFieldValue{
			"priority": {"low"},
			"area":     {"frontend"},
			"estimate": {"13"},
		},
	})
	if err != nil {
		return err
	}
	// With a wake time on it (model.Task.DeferredUntil), well into the
	// future so the demo store never actually wakes it: the pane's
	// "wakes ..." line is the half of deferring that is new, and a
	// deferral nobody has to remember is the one worth showing.
	deferredAt := ago(140 * time.Hour)
	deferredUntil := now.Add(30 * 24 * time.Hour)
	if err := store.SetDeferred(ctx, deferred.ID, &deferredAt, &deferredUntil, deferredAt); err != nil {
		return fmt.Errorf("seeding a deferred task: %w", err)
	}

	closed, err := create(ago(200*time.Hour), ui.CreateTaskRequest{
		Title:       "Spike: websocket transport",
		Description: "Explored replacing polling with a websocket; decided against it for now.",
		Approved:    true,
	})
	if err != nil {
		return err
	}
	closedAt := ago(190 * time.Hour)
	if err := store.ObserveField(ctx, closed.ID, closedAt, func(o *model.Observation) {
		o.ClosedAt = &closedAt
	}); err != nil {
		return fmt.Errorf("seeding a closed task: %w", err)
	}

	// Four saved boards, for the same reason every state above is
	// seeded: so that somebody working on the frontend can see what the
	// Kanban board, the timeline, the report and the tree actually draw without
	// first having to invent one.
	//
	// They are rows here rather than fixed entries on the nav rail
	// because that is what those two layouts are now (grain/task-162,
	// ui/src/boards.js). A deployment used to get one board and one
	// timeline whether it wanted them or not; it gets as many as
	// somebody makes, each named and each narrowed to what it is about,
	// and a demo is a deployment where somebody has made these two.
	//
	// None of them carries columns, sections or a query of its own: a
	// board that names no columns draws whatever columns its reader's
	// browser is set to, one that names no sections prints the whole
	// report, and one with an empty query is a board over every task,
	// which is exactly what the two fixed views were.
	for _, board := range []ui.CreateBoardRequest{
		{
			Name:        "Board",
			Kind:        "kanban",
			Description: "Every task, in columns of states",
		},
		{
			Name:        "Timeline",
			Kind:        "gantt",
			Description: "Every task as a bar against a clock",
		},
		{
			Name:        "Report",
			Kind:        "report",
			Description: "How this deployment is going, measured",
		},
		{
			Name:        "Tree",
			Kind:        "tree",
			Description: "Every task, under the task that filed it",
		},
	} {
		if _, err := client.CreateBoard(ctx, board); err != nil {
			return fmt.Errorf("seeding the %q board: %w", board.Name, err)
		}
	}

	// A decomposed subtask linked to a parent task and proposal.
	subtask, err := create(ago(40*time.Minute), ui.CreateTaskRequest{
		Title:       "Extract pagination query helper",
		Description: "Decomposed sub-task: extract reusable cursor pagination builder for SQL queries.",
		Approved:    true,
		CustomFields: map[string]ui.CustomFieldValue{
			"priority": {"low"},
			"area":     {"backend"},
			"estimate": {"3"},
		},
	})
	if err != nil {
		return fmt.Errorf("seeding subtask: %w", err)
	}
	st, err := store.GetTask(ctx, subtask.ID)
	if err != nil {
		return err
	}
	st.Origin = model.Origin{
		Attribution: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "director"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "agent-1"},
		},
		Reason: model.ReasonProposal,
	}
	st.Links = append(st.Links,
		model.Link{Kind: model.LinkChildOf, Target: repairing.ID},
		model.Link{Kind: model.LinkDependsOn, Target: repairing.ID},
		model.Link{Kind: model.LinkProposedBy, Target: repairing.ID},
	)
	if err := store.PutTask(ctx, *st); err != nil {
		return err
	}

	// Director session task tagged with console-session:director.
	directorTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Director Session",
		Description: "Standing interactive console session for cluster planning, task construction, and GitOps configuration.",
		Approved:    true,
	})
	if err != nil {
		return fmt.Errorf("seeding director task: %w", err)
	}
	dt, err := store.GetTask(ctx, directorTask.ID)
	if err != nil {
		return err
	}
	dt.Interactive = true
	dt.Tags = append(dt.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
	dt.Grants = model.ConsoleSessionGrants(model.ConsoleSession{ID: model.DirectorSessionID})
	dt.Origin = model.Origin{
		Attribution: model.Attribution{Actor: cfg.Actor},
		Reason:      model.ReasonConsoleSession,
	}
	if err := store.PutTask(ctx, *dt); err != nil {
		return err
	}
	if err := store.SetTaskGoal(ctx, directorTask.ID, model.GoalDecl{Kind: model.GoalPersonCloses}, model.PrincipalString(model.ConsoleSessionPrincipal), now); err != nil {
		return err
	}

	dirAt1 := ago(45 * time.Minute)
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    directorTask.ID,
		Author:    model.Attribution{Actor: cfg.Actor},
		Body:      "Let's plan the Q3 delivery milestone for `acme/widgets`. We need pagination for the tasks API, dark mode toggle, and to bump the Go toolchain to 1.24.",
		CreatedAt: dirAt1,
	}); err != nil {
		return err
	}

	dirAt2 := ago(43 * time.Minute)
	directorPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: "director-agent"}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: directorTask.ID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "director"},
			OnBehalfOf: &directorPrincipal,
		},
		Body: "I've analyzed `acme/widgets` and broken this down into a prioritized task set:\n\n" +
			"1. **Bump Go toolchain to 1.24** (`#3` · Infra / P1) — go.mod and CI image update\n" +
			"2. **Add pagination to the tasks API** (`#5` · Backend / P2) — Cursor-based pagination for `GET /api/tasks`\n" +
			"3. **Extract pagination helper functions** (`#7` · Backend / Subtask) — Query builder & offset utility\n" +
			"4. **Add a dark mode toggle** (`#1` · Frontend / P2) — UI theme switch with localStorage persistence\n\n" +
			"I have constructed the task set with dependency chains, assigned capabilities (`self-debug`), and updated the cluster configuration to allocate 4 concurrent workers.",
		CreatedAt: dirAt2,
	}); err != nil {
		return err
	}

	dirAt3 := ago(20 * time.Minute)
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    directorTask.ID,
		Author:    model.Attribution{Actor: cfg.Actor},
		Body:      "Approved! Also please ensure our regression suites run daily at midnight.",
		CreatedAt: dirAt3,
	}); err != nil {
		return err
	}

	dirAt4 := ago(18 * time.Minute)
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: directorTask.ID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "director"},
			OnBehalfOf: &directorPrincipal,
		},
		Body:      "Scheduled workflow **Daily E2E Regression Sweep** has been registered targeting `acme/widgets:main` daily at 00:00 UTC with auto-merge enabled.",
		CreatedAt: dirAt4,
	}); err != nil {
		return err
	}

	// The tasks the director's chat above says it filed, filed by it: a
	// work tree rooted at the chat (model.WorkOrigin, docs/work-trees.md),
	// so the chat's own page has a tree to draw (GET /api/tasks/{id}/tree)
	// and a Tree board has one to nest. The pagination helper is already
	// the pagination task's own follow-up, one level further down.
	treeOrigin := model.TaskOrigin(directorTask.ID)
	for _, id := range []string{running.ID, repairing.ID, subtask.ID, darkMode.ID} {
		t, err := store.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if t.Parent() == "" {
			t.Links = append(t.Links, model.Link{Kind: model.LinkProposedBy, Target: directorTask.ID})
		}
		t.WorkOrigin = treeOrigin
		if err := store.PutTask(ctx, *t); err != nil {
			return fmt.Errorf("seeding the director's work tree: %w", err)
		}
	}

	// Reusable Templates
	qualTmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:        "release-qualification",
		Title:       "Release Qualification: Run sanity checks & unit suites",
		Description: "Verify release branch against all core integration suites and linters.",
		Repo:        "acme/widgets",
		Base:        "main",
		AutoMerge:   true,
	})
	if err != nil {
		return fmt.Errorf("seeding template: %w", err)
	}

	secTmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:        "security-audit",
		Title:       "Security: Dependency vulnerability scan",
		Description: "Audit dependencies for CVEs and outdated libraries.",
		Repo:        "acme/widgets",
		Base:        "main",
		AutoMerge:   false,
	})
	if err != nil {
		return fmt.Errorf("seeding template: %w", err)
	}

	// Test Suite
	if _, err := client.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name:        "Release Qualification Suite",
		TemplateIDs: []string{qualTmpl.ID, secTmpl.ID},
		Mode:        "count",
		Count:       2,
	}); err != nil {
		return fmt.Errorf("seeding suite: %w", err)
	}

	// Recurring Schedules
	if _, err := client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo:       "acme/widgets",
		Base:       "main",
		TemplateID: qualTmpl.ID,
		Recurrence: ui.Recurrence{Kind: "daily", TimeOfDay: "00:00"},
	}); err != nil {
		return fmt.Errorf("seeding schedule: %w", err)
	}

	if _, err := client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title:        "Weekly Dependency & Vulnerability Audit",
		Description:  "Audits repository dependencies for known vulnerabilities.",
		Repo:         "acme/widgets",
		Base:         "main",
		AutoMerge:    false,
		Capabilities: []string{"self-debug"},
		Recurrence:   ui.Recurrence{Kind: "weekly", TimeOfDay: "06:00", Weekday: "monday"},
	}); err != nil {
		return fmt.Errorf("seeding schedule: %w", err)
	}

	// Repository declarations from acme/widgets (.grain/ directory)
	declRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declTmplID := model.RepoDeclaredID(model.DeclaredTemplate, declRepo, "nightly")
	declSuiteID := model.RepoDeclaredID(model.DeclaredSuite, declRepo, "smoke")
	declSchedID := model.RepoDeclaredID(model.DeclaredSchedule, declRepo, "nightly")
	if err := syncDemoDeclarations(ctx, store, declRepo, "abc123commit", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path: model.TemplateDeclarationDir + "/nightly.yml",
			Template: model.Template{
				ID:        declTmplID,
				Name:      "nightly-maintenance",
				Title:     "Declared Nightly Maintenance",
				Target:    &declRepo,
				AutoMerge: true,
			},
		}},
		Suites: []model.DeclaredSuiteFile{{
			Path: model.SuiteDeclarationDir + "/smoke.yml",
			Suite: model.Suite{
				ID:        declSuiteID,
				Name:      "Smoke Verification Suite",
				Items:     []model.SuiteItem{{TemplateID: declTmplID}},
				Mode:      model.SuiteUntilClean,
				MaxPasses: 3,
				AutoMerge: true,
			},
		}},
		Schedules: []model.DeclaredScheduleFile{{
			Path: model.ScheduleDeclarationDir + "/nightly.yml",
			Schedule: model.Schedule{
				ID:         declSchedID,
				Title:      "Nightly Repo Maintenance Sweep",
				Target:     declRepo,
				TemplateID: &declTmplID,
				Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
				Enabled:    true,
			},
		}},
	}, now); err != nil {
		return fmt.Errorf("seeding repo declarations: %w", err)
	}

	if err := seedGitHubCIDemo(ctx, store, ago); err != nil {
		return err
	}
	if err := seedDemoMemory(ctx, store, cfg, ago); err != nil {
		return err
	}
	if err := seedDemoNotifications(ctx, store, darkMode.ID, ago); err != nil {
		return err
	}

	return nil
}

// seedDemoNotifications raises three operator notifications (DT-5), so
// the alerts bell and the Inbox's Notifications section have rows to
// show: a warning a reconciler would raise, a trunk release waiting for a
// person's Approve or Reject (docs/design/release-from-trunk.md §4), and
// an agent's own note to the operator about a task, already acknowledged.
func seedDemoNotifications(ctx context.Context, store *model.Store, taskID string, ago func(time.Duration) time.Time) error {
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  "pipeline.merge-stalled:" + taskID,
		Severity: model.NotificationWarning,
		Title:    "A merge has been stalled for 2 hours",
		Body:     "The merge queue has retried this pull request three times and its checks keep timing out.",
		TaskID:   taskID,
		RaisedBy: "automation:merge-queue",
		RaisedAt: ago(2 * time.Hour),
		Actions:  []string{"investigate", "open-task"},
	}); err != nil {
		return fmt.Errorf("seeding a notification: %w", err)
	}
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  model.NotificationSubject(model.FamilyReleasePending, "v2.4.0"),
		Severity: model.NotificationWarning,
		Title:    "v2.4.0 is ready to promote (v2.4.0-rc.2 passed its soak)",
		Body: "v2.4.0-rc.2 passed its 24h staging soak and is staged as v2.4.0-pending: images built and " +
			"published as :v2.4.0-pending, nothing left to build or test.",
		Repo:     model.GrainSourceRepo().String(),
		RaisedBy: "system:release-gate",
		RaisedAt: ago(90 * time.Minute),
		Actions:  model.ReleasePendingActions,
	}); err != nil {
		return fmt.Errorf("seeding a notification: %w", err)
	}
	raised, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  "agent." + taskID + ":flaky-e2e",
		Severity: model.NotificationInfo,
		Title:    "The webkit e2e job looks flaky",
		Body:     "It failed once and passed on retry with no change; worth a look if it happens again.",
		TaskID:   taskID,
		RaisedBy: "agent:" + taskID,
		RaisedAt: ago(40 * time.Minute),
	})
	if err != nil {
		return fmt.Errorf("seeding a notification: %w", err)
	}
	return store.AckNotification(ctx, raised.Notification.ID, "human:demo", ago(30*time.Minute))
}

// syncDemoDeclarations merges add into what store already holds for repo and
// syncs the combined set, so opt-in scenario flags (-merge-rules,
// -sensitive-hold, -mechanical-port) add to seedDemo's and each other's
// declarations instead of replacing them. Any declaration read failure an
// earlier scenario recorded is kept alongside the commit that was live before
// it failed.
func syncDemoDeclarations(ctx context.Context, store *model.Store, repo model.RepoRef,
	commit string, add model.RepoDeclarations, now time.Time) error {
	existingSync, err := store.GetRepoDeclarationSync(ctx, repo)
	if err != nil {
		return err
	}
	rows, err := store.RepoDeclarationsOf(ctx, repo)
	if err != nil {
		return err
	}
	for _, row := range rows {
		switch row.Kind {
		case model.DeclaredSettings:
			if add.Settings == nil {
				rc, err := store.GetRepoConfig(ctx, repo)
				if err != nil {
					return err
				}
				if rc != nil {
					add.Settings = &model.DeclaredSettingsFile{
						Path: row.Path,
						Settings: model.RepoSettings{
							PromptExtension: rc.DeclaredPromptExtension,
							SetupCommand:    rc.DeclaredSetupCommand,
							PortBranches:    rc.DeclaredPortBranches,
							PortTypes:       rc.DeclaredPortTypes,
						},
					}
				}
			}
		case model.DeclaredTemplate:
			if !slices.ContainsFunc(add.Templates, func(f model.DeclaredTemplateFile) bool { return f.Template.ID == row.ID }) {
				t, err := store.GetTemplate(ctx, row.ID)
				if err != nil {
					return err
				}
				if t != nil {
					add.Templates = append(add.Templates, model.DeclaredTemplateFile{
						Path:     row.Path,
						Template: *t,
						Push:     row.Push,
					})
				}
			}
		case model.DeclaredSuite:
			if !slices.ContainsFunc(add.Suites, func(f model.DeclaredSuiteFile) bool { return f.Suite.ID == row.ID }) {
				s, err := store.GetSuite(ctx, row.ID)
				if err != nil {
					return err
				}
				if s != nil {
					add.Suites = append(add.Suites, model.DeclaredSuiteFile{
						Path:  row.Path,
						Suite: *s,
					})
				}
			}
		case model.DeclaredSchedule:
			if !slices.ContainsFunc(add.Schedules, func(f model.DeclaredScheduleFile) bool { return f.Schedule.ID == row.ID }) {
				sched, err := store.GetSchedule(ctx, row.ID)
				if err != nil {
					return err
				}
				if sched != nil {
					add.Schedules = append(add.Schedules, model.DeclaredScheduleFile{
						Path:     row.Path,
						Schedule: *sched,
					})
				}
			}
		}
	}
	triggers, err := store.PathTriggersOf(ctx, repo)
	if err != nil {
		return err
	}
	for _, tr := range triggers {
		if !slices.ContainsFunc(add.Triggers, func(t model.PathTrigger) bool { return t.Name == tr.Trigger.Name }) {
			add.Triggers = append(add.Triggers, tr.Trigger)
		}
	}
	if add.MergeRules == nil {
		rules, err := store.MergeRulesOf(ctx, repo)
		if err != nil {
			return err
		}
		add.MergeRules = rules
	}
	if existingSync != nil && existingSync.LastError != "" && existingSync.Commit != "" {
		commit = existingSync.Commit
	}
	if _, err := store.SyncRepoDeclarations(ctx, repo, commit, add, now, time.UTC); err != nil {
		return err
	}
	if existingSync != nil && existingSync.LastError != "" {
		if err := store.RecordDeclarationFailure(ctx, repo, existingSync.LastError, existingSync.CheckedAt); err != nil {
			return err
		}
	}
	return nil
}

// seedDemoMemory writes what this fake deployment has "learned" about
// working here (model.Memory and model.MemoryDocument): the two layers
// every dispatch is given, and a document under each.
//
// It is here for the reason demoCustomFields is. Memory is written by
// the memory agent, and a demo has no agents at all -- so without this,
// the only thing the Settings pane's Memory tab could show somebody
// working on the frontend is what a deployment that has never run one
// looks like, which is an empty box and a line saying nothing has been
// filed. It is also what the e2e spec over that pane reads.
//
// The ages are chosen, not incidental. One document is four months old,
// which is past model.MemoryAge's own boundary between counting days and
// counting months and so past what the pane marks as stale -- staleness
// being the failure mode memory actually has, and a mark nobody can see
// in a demo is a mark nobody reviews.
//
// Written straight to the store rather than through ui.Client.PutMemory,
// which is the one place in this seed that matters: the client stamps
// UpdatedAt with the current time, and every age here is the point.
func seedDemoMemory(ctx context.Context, store *model.Store, cfg ui.Config, ago func(time.Duration) time.Time) error {
	day := 24 * time.Hour
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "demo-memory-run"}
	memories := []model.Memory{{
		Text: "The frontend tests are slow but not flaky: a `npm test` run that fails once " +
			"fails again. Treat a single red run as real rather than retrying it.\n\n" +
			"`make fmt` before pushing -- CI's Format step is a diff check and it is the " +
			"most common way a first push here comes back red.",
		UpdatedAt: ago(2 * day),
		UpdatedBy: agent,
	}, {
		Repo: cfg.DefaultTarget,
		Text: "This repo's CI needs `make frontend` before `go vet` -- the vet step reads " +
			"the embedded assets and fails confusingly without them.",
		UpdatedAt: ago(9 * day),
		// Written by a person rather than by the memory agent, which is
		// the one thing a seed can show that no amount of text can: the
		// pane marks the two apart (model.Memory.UpdatedBy,
		// grain/task-394), and a demo where every row had the same
		// author would show one of the two chips and never the other.
		UpdatedBy: cfg.Actor,
	}}
	for _, m := range memories {
		if err := store.PutMemory(ctx, m); err != nil {
			return fmt.Errorf("seeding the memory for %s: %w", memoryScopeName(m.Repo), err)
		}
	}

	documents := []model.MemoryDocument{{
		Name:    "release-checklist",
		Summary: "what a release of this deployment actually needs, in order",
		Text: "1. `make fmt` and `make frontend`, then the full suite.\n" +
			"2. Tag from `main` only, never from a branch that has not merged.\n" +
			"3. The image build is what publishes; the tag on its own does nothing.\n\n" +
			"Written down because the second step is the one that keeps being skipped, " +
			"and a tag on a branch is not obviously wrong until a week later.",
		UpdatedAt: ago(5 * day),
		UpdatedBy: agent,
	}, {
		Repo:    cfg.DefaultTarget,
		Name:    "flaky-e2e",
		Summary: "why the e2e suite fails on its third retry, and what is not worth trying",
		Text: "The third retry reuses a browser profile the second one left behind, so the " +
			"failure is a stale session rather than the code under test.\n\n" +
			"Not worth trying: raising the timeout, which only moves where it fails.",
		UpdatedAt: ago(120 * day),
		UpdatedBy: agent,
	}}
	for _, d := range documents {
		if err := store.PutMemoryDocument(ctx, d); err != nil {
			return fmt.Errorf("seeding the memory document %q: %w", d.Name, err)
		}
	}
	return nil
}

// memoryScopeName names a memory's scope in this file's own error
// messages, since a nil repo is the deployment itself rather than a repo
// nobody named.
func memoryScopeName(repo *model.RepoRef) string {
	if repo == nil {
		return "this deployment"
	}
	return repo.String()
}

// demoCustomFields is the set of fields this fake deployment declares
// for itself -- what a real one keeps in its state repository's
// `custom_field` table, and files a pull request to change (grain/
// task-53). They are written here rather than left to whoever runs the
// demo because there is no pane that declares one: without these, a
// demo can only show the frontend a deployment that has never declared
// a field sees, which is every input, chip, row and menu behind
// model.CustomField missing.
//
// Six of them survive the seed, which is one per control the form
// actually has: an enum picker, a box of chips for a field holding a
// set, a text box, a date, a number and a checkbox (CustomFields.jsx's
// CustomFieldInput). It is deliberately no longer than that. The demo's
// forms are also what somebody opens to see what grain *is*, and six
// short fields under a "Fields" caption is a deployment with opinions,
// where a dozen would be a wall to scroll past on the way to Create
// task. The seventh, demoRetiredField, is declared and taken away again
// while the seed runs, so it costs the form nothing.
//
// Positions are spaced by ten so that one more field can be slipped
// between two of these without renumbering the rest -- the same reason
// anybody numbers anything in tens.
func demoCustomFields() []model.CustomField {
	return []model.CustomField{
		{
			// The one flagged onto every row and card (ShowOnRow): the
			// field this fake deployment reads its backlog by, and the one
			// whose chip the list, the board and the timeline all show.
			// With a default, so the new-task form opens on the answer
			// most tasks want -- and only the form: a default is never
			// applied to a task by the store (model.CustomField.Default).
			Name:        "priority",
			Label:       "Priority",
			Type:        model.CustomFieldEnum,
			Options:     []string{"low", "medium", "high"},
			Default:     "medium",
			Description: "How soon this is worth picking up.",
			Position:    10,
			ShowOnRow:   true,
		},
		{
			// The field that holds a set rather than one value: an enum
			// again, since the two kinds of multi-valued input differ only
			// in whether the box offers choices, and a task touching two
			// areas is the ordinary case that makes a set worth having.
			Name:        "area",
			Label:       "Area",
			Type:        model.CustomFieldEnum,
			Options:     []string{"frontend", "backend", "infra", "docs"},
			Multiple:    true,
			Description: "Which parts of the product this touches -- as many as apply.",
			Position:    20,
		},
		{
			Name:        "requested-by",
			Label:       "Requested by",
			Description: "Whoever asked for this, when it came from outside the team.",
			Position:    30,
		},
		{
			Name:        "due-date",
			Label:       "Due date",
			Type:        model.CustomFieldDate,
			Description: "The date somebody outside is expecting this by.",
			Position:    40,
		},
		{
			Name:        "estimate",
			Label:       "Estimate (days)",
			Type:        model.CustomFieldNumber,
			Description: "A guess at the working days this takes, for planning only.",
			Position:    50,
		},
		{
			Name:        "customer-facing",
			Label:       "Customer-facing",
			Type:        model.CustomFieldBool,
			Description: "Ticked when somebody outside the project will see the change.",
			Position:    60,
		},
		demoRetiredField,
	}
}

// demoRetiredField is the seventh declaration, which exists for as long
// as it takes one seeded task to record a value under it and is then
// deleted again (seedDemo). What is left behind is the state it is here
// for: a task carrying a value for a field this deployment no longer
// declares, which is what every deployment that has ever retired a
// field has, and which the task pane shows under its bare name rather
// than hiding.
var demoRetiredField = model.CustomField{
	Name:        "sprint",
	Label:       "Sprint",
	Description: "Retired: this deployment stopped planning in sprints.",
	Position:    70,
}

// Both fake documents below are written long on purpose, and that length
// is the point rather than an accident of how much prose there was to
// write.
//
// Two panes in this UI open scrolled to their end rather than their top:
// an attempt's transcript (AttemptTranscriptOverlay.jsx, grain/task-22)
// and the System pane's log tail (LogsPage.jsx, grain/task-43). "Opens
// at the end" is only a claim about anything when there is more document
// than fits on screen -- a browser scrolls nothing when everything fits,
// and scrollTop is 0 whether the pane works or not. So ui/e2e/
// scroll.spec.js, which asks a real browser where each pane actually
// landed, needs a demo whose transcript and whose log are longer than a
// screen; these two are what make that question answerable, and both are
// a comfortable multiple of the ~25 lines .logs-view's own max-height
// (60vh) leaves room for.
const (
	demoLogLines        = 300
	demoTranscriptTurns = 40
)

// writeDemoLog writes the fake grain-daemon journal `grain demo` serves
// as its "daemon" log source, in journalctl's own --output=short-iso
// shape (pkg/systemlog's journalctl) so the pane above it shows lines of
// the form an operator reading a real deployment would see.
func writeDemoLog(path string) error {
	// One reconcile pass' worth of lines, repeated: what the daemon's
	// journal mostly is, between the moments anything happens.
	messages := []string{
		"reconcile: 9 tasks, 2 running, 0 queued, 1 proposed",
		"dispatch: nothing to start -- no approved task is waiting",
		"merge-queue: acme/widgets#101 checks pending, leaving it alone",
		"github: synced 4 pull requests for acme/widgets",
		"sandbox: demo-sandbox up 6m, 2 vCPUs, 2048 MiB",
		"state-repo: no change to export",
	}
	// Four seconds a line, so the whole file reads as the last twenty
	// minutes of a deployment rather than as one instant repeated.
	const every = 4 * time.Second
	start := time.Now().UTC().Add(-demoLogLines * every)
	var b strings.Builder
	for i := 0; i < demoLogLines; i++ {
		fmt.Fprintf(&b, "%s grain-demo grain-daemon[4242]: %s\n",
			start.Add(time.Duration(i)*every).Format("2006-01-02T15:04:05-0700"),
			messages[i%len(messages)])
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// demoCapabilityAudit is what a host-script grant records about one
// attempt: two calls of a deploy script, one of them refused, in the
// shape intree.HostScriptAudit writes them, and grain's note on records
// the server left out.
func demoCapabilityAudit(taskID string, ago func(time.Duration) time.Time) []model.CapabilityAuditRecord {
	attemptID := "demo-" + taskID + "-1-0"
	grant := "https://github.com/ai-on-gke/grain/capabilities/host-script#host-script"
	record := func(at time.Time, fields string) model.CapabilityAuditRecord {
		return model.CapabilityAuditRecord{Grant: grant, AttemptID: attemptID, At: at,
			Record: `{"time":"` + at.UTC().Format(time.RFC3339) + `","attempt_id":"` + attemptID + `",` + fields + `}`}
	}
	return []model.CapabilityAuditRecord{
		record(ago(46*time.Minute), `"script":"deploy-preview","argv":["--env","staging","--ref","grain/task-`+taskID+`"],"exit_code":0,"duration_ms":4210,"caller":"sandbox"`),
		record(ago(44*time.Minute), `"script":"deploy-preview","argv":["--env","staging","--note","a\u0000b"],"exit_code":125,"duration_ms":0,"caller":"sandbox","refused":"argument 4 holds a NUL byte"`),
		{Grant: grant, AttemptID: attemptID, At: ago(40 * time.Minute), Note: "the capability server left out 1 further record(s)"},
	}
}

// demoTranscript is the narrative the seeded running attempt has
// "written" so far, in the shape pkg/agent/claude's own parser builds a
// real one in (transcript.go's parseEvents): the agent's prose as
// paragraphs, each tool call as a "> Name(args)" line, and what it got
// back under it.
func demoTranscript() string {
	// One turn of the loop an agent actually spends its run in -- look,
	// change, check -- with the file it is working on substituted in, so
	// a reader scrolling this can tell one screen of it from the next.
	files := []string{
		"go.mod", ".github/workflows/tests.yml", "Dockerfile.build",
		"scripts/setup.sh", "docs/development.md",
	}
	var b strings.Builder
	b.WriteString("Reading the task: the Go toolchain moves to 1.24, and both go.mod and the\n" +
		"CI image have to say so. Neither is hard on its own; what needs care is that\n" +
		"nothing else in the tree pins the old version behind my back.\n\n")
	for i := 0; i < demoTranscriptTurns; i++ {
		file := files[i%len(files)]
		fmt.Fprintf(&b, "Next: %s. Read it before changing it -- a version pin is rarely\n"+
			"written the same way twice, and this repository has at least three spellings\n"+
			"of the same number.\n\n", file)
		fmt.Fprintf(&b, "> Read(%s)\n", file)
		fmt.Fprintf(&b, "%d lines read.\n\n", 40+7*i)
		fmt.Fprintf(&b, "> Grep(pattern=\"1\\\\.23\", path=%s)\n", file)
		fmt.Fprintf(&b, "%s:%d: pins the toolchain at 1.23\n\n", file, 3+i)
		fmt.Fprintf(&b, "> Edit(%s)\n", file)
		b.WriteString("Applied.\n\n")
		b.WriteString("> Bash(go build ./...)\n")
		if i%7 == 6 {
			b.WriteString("! exit status 2: go: downloading toolchain go1.24 failed, retrying\n\n")
			b.WriteString("A download that failed once, not a real failure of the change -- retrying\n" +
				"the same command before reading anything into it.\n\n")
			continue
		}
		b.WriteString("(no output)\n\n")
		fmt.Fprintf(&b, "That is %s done. %d of the files this touches are settled; the\n"+
			"rest are the same edit again, so the interesting part is over.\n\n",
			file, i+1)
	}
	b.WriteString("Everything builds, the toolchain is named once per file and nothing else\n" +
		"pins the old one. Committing this and pushing the branch.\n")
	return strings.TrimSpace(b.String())
}

// IsLoopbackHostPort reports whether hostPort names a loopback address,
// the only place -github-api may send a team's token in the clear.
func IsLoopbackHostPort(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// startDemoHostd serves a real grain hostd for -host-executor=hostd, in a
// directory of its own, on an allowlist offering agy and one capability
// server, and returns its socket. It lives as long as the demo process.
// Neither binary need exist: nothing in the demo asks hostd to start one,
// and Settings only lists what it offers. hostd refuses to run as root
// and runs on Linux only; either way the demo shows hostd not answering.
func startDemoHostd() (string, error) {
	srv, err := demoHostdServer("hostd")
	if err != nil {
		return "", err
	}
	srv.Socket = filepath.Join(filepath.Dir(srv.Allowlist), "hostd.sock")
	go func() {
		if err := srv.Serve(context.Background()); err != nil {
			log.Printf("grain demo: the demo's grain hostd is not running, so Settings shows it not answering: %v", err)
		}
	}()
	// Up before the first page asks, so Settings never catches it starting.
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(srv.Socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv.Socket, nil
}

// demoHostdServer is the demo's grain hostd, serving nothing yet: an
// allowlist offering agy and one capability server, in a directory of
// its own.
func demoHostdServer(executor string) (*hostd.Server, error) {
	dir, err := os.MkdirTemp("", "grain-demo-hostd-")
	if err != nil {
		return nil, fmt.Errorf("grain demo: -host-executor=%s: %v", executor, err)
	}
	allow := filepath.Join(dir, "hostd.yaml")
	body := "clis:\n  agy:\n    path: /opt/agy/bin/agy\n    cwd_under: [" + dir + "]\n" +
		"capabilities:\n  corp-search:\n    command: /usr/local/bin/corp-search-mcp\n"
	if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
		return nil, fmt.Errorf("grain demo: -host-executor=%s: %v", executor, err)
	}
	return &hostd.Server{
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Logf:            func(string, ...any) {},
		Build:           version.Get().Release,
	}, nil
}

// startDemoRemoteHostd serves a real grain hostd for
// -host-executor=hostd-remote with no socket, dialing in to remote over
// a loopback listener of its own as one on an operator's machine dials
// in to a daemon in a cluster. It is connected before it returns, so
// Settings never catches it connecting, or it is left to Settings to say
// it is not.
func startDemoRemoteHostd(remote *hostd.Remote) error {
	srv, err := demoHostdServer("hostd-remote")
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("grain demo: -host-executor=hostd-remote: %v", err)
	}
	go func() { _ = http.Serve(ln, remote) }()
	srv.Remote = hostd.Dialer{Server: "http://" + ln.Addr().String(), Token: remote.Token, Host: "cloudtop", Build: srv.Build}.Dial
	go func() {
		if err := srv.Serve(context.Background()); err != nil {
			log.Printf("grain demo: the demo's grain hostd is not running, so Settings shows none connected: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = remote.WaitConnected(ctx)
	return nil
}

// demoGitHubCalls is ui.GitHubCallStats for a demo: an hour two-thirds
// spent, falling fast enough to run out before it resets, with the
// spend split across the callers a deployment actually has. Fixed
// relative to the moment it is asked, so the panel always has a
// projection to show.
type demoGitHubCalls struct{}

func (demoGitHubCalls) Stats() github.Stats {
	now := time.Now()
	kinds := func(pairs ...any) map[string]github.KindStats {
		out := map[string]github.KindStats{}
		for i := 0; i+2 < len(pairs); i += 3 {
			out[pairs[i].(string)] = github.KindStats{Sent: int64(pairs[i+1].(int)), Revalidated: int64(pairs[i+2].(int))}
		}
		return out
	}
	return github.Stats{
		Sent: 5210, Revalidated: 3530, Failed: 12,
		Callers: map[string]github.CallerStats{
			"sync":           {Sent: 2400, Revalidated: 1500, Kinds: kinds("PR read", 1200, 820, "head read", 800, 420, "checks", 400, 260)},
			"base-branch-ci": {Sent: 900, Revalidated: 640, Kinds: kinds("head read", 600, 420, "checks", 300, 220)},
			"review-threads": {Sent: 700, Revalidated: 560, Kinds: kinds("review comments", 700, 560)},
			"path-triggers":  {Sent: 410, Revalidated: 330, Kinds: kinds("head read", 300, 260, "repo read", 110, 70)},
			"agent tools":    {Sent: 520, Revalidated: 300, Kinds: kinds("checks", 380, 220, "PR read", 140, 80)},
			"ui":             {Sent: 280, Revalidated: 200, Kinds: kinds("branch list", 180, 140, "PR files", 100, 60)},
		},
		Limit: github.RateLimitSnapshot{Limit: 5000, Remaining: 1650, Resets: now.Add(38 * time.Minute), Seen: now},
		Budget: github.BudgetSnapshot{
			RateLimitSnapshot: github.RateLimitSnapshot{Limit: 5000, Remaining: 1650, Resets: now.Add(38 * time.Minute), Seen: now},
			Credentials:       1, SpendPerHour: 3300, ExhaustedAt: now.Add(30 * time.Minute),
		},
		Cache: github.CacheStats{Entries: 412, Bytes: 3 << 20, MaxEntries: 2048, MaxBytes: 32 << 20},
	}
}
