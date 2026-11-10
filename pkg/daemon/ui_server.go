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

package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// CheckDeploymentOwner validates -deployment-owner (or $GRAIN_DEPLOYMENT_OWNER
// when the flag is unset).
func CheckDeploymentOwner(cfg Config) error {
	raw := strings.TrimSpace(cfg.DeploymentOwner)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(ui.DeploymentOwnerEnv))
	}
	_, err := ui.NormalizeDeploymentOwner(raw)
	return err
}

// CheckUIAllowedPrincipals validates -ui-allowed-principals (or
// $GRAIN_ALLOWED_PRINCIPALS when the flag is unset), including refusing any
// serviceAccount: entry that names a gcp-key agent pool account.
func CheckUIAllowedPrincipals(cfg Config) error {
	if err := CheckDeploymentOwner(cfg); err != nil {
		return err
	}
	return checkUIAllowedPrincipalsWithPool(cfg, ui.GCPKeyPoolEmails(cfg.GcpServiceAccountEmail, cfg.GcpServiceAccountCount, cfg.GcpProject))
}

func checkUIAllowedPrincipalsWithPool(cfg Config, gcpPoolEmails []string) error {
	if len(cfg.UiAllowedPrincipals) > 0 {
		_, err := ui.ValidateAllowedPrincipals(cfg.UiAllowedPrincipals, gcpPoolEmails)
		return err
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_ALLOWED_PRINCIPALS")); env != "" {
		_, err := ui.ValidateAllowedPrincipals(ui.SplitAllowedPrincipals(env), gcpPoolEmails)
		return err
	}
	return nil
}

// CheckUIHubVerifier validates -ui-hub-issuer, -ui-hub-audience,
// and -ui-hub-keys-url (or $GRAIN_HUB_ISSUER,
// $GRAIN_HUB_AUDIENCE, and $GRAIN_HUB_KEYS_URL): if any is set, all
// three must be set, and neither issuer nor audience may contain commas.
func CheckUIHubVerifier(cfg Config) error {
	ctx := ui.WithIAPConfig(context.Background(), ui.IAPConfig{
		HubIssuer:   cfg.UiHubIssuer,
		HubAudience: cfg.UiHubAudience,
		HubKeysURL:  cfg.UiHubKeysURL,
	})
	iss := strings.TrimSpace(ui.GetHubIssuer())
	aud := strings.TrimSpace(ui.GetHubAudience())
	keysURL := strings.TrimSpace(ui.GetHubKeysURL())
	if rc, ok := ui.HubConfigFromContext(ctx); ok {
		if v := strings.TrimSpace(rc.Issuer); v != "" {
			iss = v
		}
		if v := strings.TrimSpace(rc.Audience); v != "" {
			aud = v
		}
		if v := strings.TrimSpace(rc.KeysURL); v != "" {
			keysURL = v
		}
	}
	if iss == "" && aud == "" && keysURL == "" {
		return nil
	}
	var missing []string
	if iss == "" {
		missing = append(missing, "-ui-hub-issuer")
	}
	if aud == "" {
		missing = append(missing, "-ui-hub-audience")
	}
	if keysURL == "" {
		missing = append(missing, "-ui-hub-keys-url")
	}
	if len(missing) > 0 {
		return fmt.Errorf("incomplete registry verifier configuration: missing %s (set all three of -ui-hub-issuer, -ui-hub-audience, and -ui-hub-keys-url, or none)", strings.Join(missing, ", "))
	}
	if strings.Contains(iss, ",") {
		return errors.New("-ui-hub-issuer must be a single issuer URL, not a comma-separated list")
	}
	if strings.Contains(aud, ",") {
		return errors.New("-ui-hub-audience must be a single URL (https://<name>.<instance-domain>), not a comma-separated list")
	}
	return nil
}

// CheckHubConnect validates -hub-connect: starting with
// -hub-connect requires an active "registry" IdentityVerifier
// (-ui-hub-issuer, -ui-hub-audience, and -ui-hub-keys-url, or a
// registered "registry" IdentityVerifier) so incoming tunnel streams can never
// be served without X-Grain-Registry-Assertion verification.
func CheckHubConnect(cfg Config) error {
	if err := CheckUIHubVerifier(cfg); err != nil {
		return err
	}
	if !cfg.HubConnect {
		return nil
	}
	ctx := ui.WithIAPConfig(context.Background(), ui.IAPConfig{
		Audience:    cfg.UiIAPAudience,
		Issuer:      cfg.UiIAPIssuer,
		KeysURL:     cfg.UiIAPKeysURL,
		HubIssuer:   cfg.UiHubIssuer,
		HubAudience: cfg.UiHubAudience,
		HubKeysURL:  cfg.UiHubKeysURL,
	})
	if ui.HasActiveVerifier(ctx, string(ui.AuthMethodHub)) {
		return nil
	}
	return errors.New("-hub-connect requires -ui-hub-issuer, -ui-hub-audience, and -ui-hub-keys-url")
}

// CheckUIRequireVerifier validates -ui-require-verifier (and its
// -ui-require-iap alias): it refuses conflicting flags and refuses to start
// when the named IdentityVerifier is not active, because with no assertion
// able to verify and no access code accepted over the network, nobody could
// reach the UI except from the daemon's own host.
func CheckUIRequireVerifier(cfg Config) error {
	if err := CheckHubConnect(cfg); err != nil {
		return err
	}
	req := strings.TrimSpace(cfg.UiRequireVerifier)
	if cfg.UiRequireIAP && req != "" && req != string(ui.AuthMethodIAP) {
		return fmt.Errorf("cannot set both -ui-require-iap and -ui-require-verifier=%q: -ui-require-iap is an alias for -ui-require-verifier=iap", req)
	}
	fromRequireIAPAlias := false
	if req == "" && cfg.UiRequireIAP {
		req = string(ui.AuthMethodIAP)
		fromRequireIAPAlias = true
	}
	if req == "" {
		return nil
	}
	ctx := ui.WithIAPConfig(context.Background(), ui.IAPConfig{
		Audience:    cfg.UiIAPAudience,
		Issuer:      cfg.UiIAPIssuer,
		KeysURL:     cfg.UiIAPKeysURL,
		HubIssuer:   cfg.UiHubIssuer,
		HubAudience: cfg.UiHubAudience,
		HubKeysURL:  cfg.UiHubKeysURL,
	})
	if ui.HasActiveVerifier(ctx, req) {
		return nil
	}
	if req == string(ui.AuthMethodIAP) {
		if fromRequireIAPAlias {
			return errors.New("-ui-require-iap needs an IAP audience: set -ui-iap-audience (or $GRAIN_IAP_AUDIENCE) to the IAP backend's " +
				"/projects/NUMBER/global/backendServices/ID, or drop -ui-require-iap to keep accepting the access code over the network")
		}
		return errors.New("-ui-require-verifier=iap needs an IAP audience: set -ui-iap-audience (or $GRAIN_IAP_AUDIENCE) to the IAP backend's " +
			"/projects/NUMBER/global/backendServices/ID, or drop -ui-require-verifier to keep accepting the access code over the network")
	}
	if req == string(ui.AuthMethodHub) {
		return fmt.Errorf("-ui-require-verifier=%q names no active IdentityVerifier: set -ui-hub-issuer, -ui-hub-audience, and -ui-hub-keys-url first, or drop -ui-require-verifier to keep accepting the access code over the network", req)
	}
	return fmt.Errorf("-ui-require-verifier=%q names no active IdentityVerifier: configure that verifier first, or drop -ui-require-verifier to keep accepting the access code over the network", req)
}

// CheckUIRequireIAP is an alias for CheckUIRequireVerifier.
func CheckUIRequireIAP(cfg Config) error {
	return CheckUIRequireVerifier(cfg)
}

// startUIServer serves pkg/ui.Server -- the JSON API plus the static
// frontend -- on addr, over the same store RunCycle dispatches against,
// returning a shutdown func the same shape startGitProxy's own is.
//
// db is that same store's connection, and it is here for one thing: the
// Upgrade button this server offers has to be able to write this
// deployment's rows out when the build it is cutting over to knows a
// different schema (newUpgrader, and schemaupgrade.go for why an upgrade
// that cannot do that used to take the deployment down).
// Running it in-process rather than as a separate "ui" binary/service is
// exactly what bwsalmon/agents#363 asked for: one store connection, no
// second process needed just to let a daemon and a UI coexist (see this
// file's own doc comment).
//
// uiCfg.Secrets always points at this same process's own secrets
// directory (dataDir/secrets, the exact root run() hands orchestrator.Deps'
// own Credentials, above): bwsalmon/agents#357 added write-only secrets
// access for a UI colocated with the server, gated behind a
// -server-data-dir flag on the old standalone "ui" subcommand naming the
// server's own -data-dir from a second process. Now that the UI only
// ever runs inside the daemon that owns the store (the old standalone
// mode is gone -- see this file's own doc comment), it always has that
// directory to hand; there is no longer a cross-process case where it
// would not.
func startUIServer(cfg Config, uiLn net.Listener, store *model.Store, db *sql.DB, transcriptDir string, sandboxes orchestrator.Sandboxes, live *LiveConfig, pkgCache *PackageCache, actionsCache *ActionsCache, stateRepo ui.StateRepoManager, settingsRepo func() model.RepoRef, forbidden *gitproxy.ForbiddenSet, upgrader *upgrade.Upgrader, secretStore *secrets.Store) (stop func(context.Context) error, err error) {
	defer func() {
		if err != nil && uiLn != nil {
			_ = uiLn.Close()
		}
	}()
	// The credential ladder run() already loaded, not a second copy of
	// it: this is the one Settings checks targetRepos against to flag
	// bwsalmon/agents#427's drift before a push ever reaches the proxy
	// with a confusing 500, *and* the one the tokens pane writes through
	// -- and writing through it is what makes it forget the token it had
	// cached (gitproxy.CredentialSet.forget). A check made through a
	// different copy would go on testing the token an operator has just
	// replaced on this very pane, which is the moment the button exists
	// for. So both halves are handed the same ladder.
	uiCredentials := live.gitHubCredentials()
	githubClient, githubTransport, githubTokens := newForgeClient(cfg, store, uiCredentials)
	// Charged to "ui" in the metrics' per-caller breakdown (github.WithCaller).
	githubClient = githubClient.As("ui")
	disks := cfg.hostVolumes()
	// Shared with run(): the secrets fields on the Capabilities tab write
	// into it, and the "test this credential" action on that same tab
	// resolves the standing credential it is about to authenticate as out
	// of it.
	uiSecrets := secretStore
	if uiSecrets == nil {
		uiSecrets = OpenSecrets(cfg.DataDir)
	}
	// The local API token: how this host's grain CLI, and the `grain
	// mcpserver` each run starts here, reach the API. Named in this
	// process's environment so every child it starts can find it
	// without being told the data directory (ui/localtoken.go).
	var localAPIToken, localAPITokenFile string
	if cfg.DataDir != "" {
		path := ui.LocalAPITokenPath(cfg.DataDir)
		if envFile := strings.TrimSpace(os.Getenv(ui.LocalAPITokenFileEnv)); envFile != "" && envFile != path {
			if b, err := os.ReadFile(envFile); err == nil {
				if tok := strings.TrimSpace(string(b)); tok != "" {
					_ = os.WriteFile(path, []byte(tok+"\n"), 0o600)
				}
			}
		}
		tok, err := ui.EnsureLocalAPIToken(path)
		if err != nil {
			log.Printf("grain daemon: WARNING: writing the local API token %s: %v -- "+
				"the grain CLI on this host will have to sign in", path, err)
		} else {
			localAPIToken, localAPITokenFile = tok, path
			os.Setenv(ui.LocalAPITokenFileEnv, path)
		}
	}
	// Whether the manager handed in can say when a setting last changed.
	// Asked of the interface rather than assumed, exactly as
	// localRepoComparer below is: startUIServer takes a
	// ui.StateRepoManager, and a caller with no state repository at all
	// (`grain demo`, a test) hands one that is neither.
	var settingsHistory ui.SettingsHistoryReader
	if reader, ok := stateRepo.(ui.SettingsHistoryReader); ok {
		settingsHistory = reader
	}
	var (
		hubStatus           func(context.Context) (model.HubSnapshot, error)
		triggerHubHeartbeat func(context.Context) (model.HubSnapshot, error)
		triggerHubSync      func()
	)
	if reg := live.Hub(); reg != nil {
		hubStatus = reg.Snapshot
		triggerHubHeartbeat = reg.TriggerHeartbeat
		triggerHubSync = reg.Trigger
	}
	gcpPoolSAs := ui.GCPKeyPoolEmails(cfg.GcpServiceAccountEmail, cfg.GcpServiceAccountCount, cfg.GcpProject)
	combinedPoolSAs := append([]string(nil), gcpPoolSAs...)
	if store != nil {
		ctx := context.Background()
		mc, _ := store.GetConfig(ctx)
		keys, _ := store.ListGCPKeys(ctx)
		combinedPoolSAs = append(combinedPoolSAs, ui.ConfigGCPKeyServiceAccounts(mc, keys)...)
	}
	if err := CheckDeploymentOwner(cfg); err != nil {
		return nil, err
	}
	if err := checkUIAllowedPrincipalsWithPool(cfg, combinedPoolSAs); err != nil {
		return nil, err
	}
	if err := CheckHubConnect(cfg); err != nil {
		return nil, err
	}
	var provisionedSAs []string
	if proj := strings.TrimSpace(cfg.GcpProject); proj != "" {
		daemonSA := "grain-daemon@" + strings.ToLower(proj) + ".iam.gserviceaccount.com"
		inPool := false
		for _, sa := range combinedPoolSAs {
			if strings.EqualFold(sa, daemonSA) {
				inPool = true
				break
			}
		}
		if !inPool {
			provisionedSAs = append(provisionedSAs, daemonSA)
		}
	}
	uiCfg := ui.Config{
		Actor:                      ui.DefaultActor(ActorID(cfg.Actor)),
		LocalAPIToken:              localAPIToken,
		LocalAPITokenFile:          localAPITokenFile,
		AccessSessionLifetime:      cfg.UiAccessSessionLifetime,
		IAPAudience:                cfg.UiIAPAudience,
		IAPIssuer:                  cfg.UiIAPIssuer,
		IAPKeysURL:                 cfg.UiIAPKeysURL,
		HubIssuer:                  cfg.UiHubIssuer,
		HubAudience:                cfg.UiHubAudience,
		HubKeysURL:                 cfg.UiHubKeysURL,
		RequireVerifier:            cfg.UiRequireVerifier,
		RequireIAP:                 cfg.UiRequireIAP,
		AllowedPrincipals:          cfg.UiAllowedPrincipals,
		DeploymentOwner:            cfg.DeploymentOwner,
		ProvisionedServiceAccounts: provisionedSAs,
		GCPKeyServiceAccounts:      gcpPoolSAs,
		DBDriver:                   cfg.DbDriver,
		DBDSN:                      cfg.DbDSN,
		DataDir:                    cfg.DataDir,
		SourceDB:                   db,
		// The fixed set grain ships providers for, plus the api rows appended
		// last as a marker that this picker offers the agent API (pickerCapabilities).
		// Named GitHub credentials are served on the in-process repo server
		// (repo#credential) rather than as legacy checklist rows; OfferedGitCredentials
		// records the startup credential list for Settings -> GitHub tokens and
		// per-client repo#credential validation.
		Capabilities:          append(ui.OfferedCapabilities(), ui.APICapabilities()...),
		OfferedGitCredentials: live.GitHubTokens(),
		Consoles:              consoleSessions,
		Secrets:               uiSecrets,
		LocalScriptApprovals:  localScriptApprovals,
		// The registry run() attaches capability servers into, for the
		// status column of Settings' Capability servers pane.
		CapabilityServers: capabilityServerStatuses(live),
		// The package cache this process is serving, so Settings can say
		// how much it is holding and its flush button has something to
		// empty. nil on a deployment whose cache would not start.
		PackageCache: pkgCache.uiPackageCache(),
		ActionsCache: actionsCache.uiActionsCache(),
		// Unlike -codex-auth-file, this path is owned by the UI: replacing
		// or clearing it can never touch an operator-managed file.
		CodexAuthFile:         ManagedCodexAuthFile(cfg.DataDir),
		CodexAuthFallbackFile: cfg.CodexAuthFile,
		// What every task read asks whether a task's own state has stood
		// for longer than that state should take (ui.Config.Stall). The
		// answer is the orchestrator's -- it is the package that knows
		// which component ought to have ended each wait and how long
		// each one is given (its liveness.go) -- and the UI is what puts
		// it on the row, so the two are joined here rather than by
		// either reaching for the other. Wired unconditionally: it reads nothing but the state, the
		// timestamps the store already holds and the clock, none of
		// which waits on the reconcile loop.
		Stall: orchestrator.TaskStallNote,
		// And the sub-case of that state, with its control
		// (ui.Config.Reasons) -- joined here for the same reason.
		// With the deployment's own dispatch conditions, so a queued
		// task says why nothing is dispatching rather than that it is
		// ready (DispatchConditions).
		Reasons: ReasonClassifier{PolicyFile: cfg.PolicyFile, DispatchBlocked: DispatchConditions.Why},
		// The same action for the other kind of credential this
		// deployment holds (ui.Config.AgentKeyChecks): the key each agent
		// framework runs as, which the Agents tab can otherwise only
		// report as "set". Wired here for the same reasons as the two
		// around it -- it needs the live configuration (for the
		// -*-key-file fallbacks) and this same secrets directory, and
		// nothing the reconcile loop builds.
		AgentKeyChecks: AgentKeyCheckAdapter{Live: live, Creds: uiSecrets},
		// The Gemini model field's own picker (ui.Config.AgentModels):
		// the model names agy will accept are agy's to say, and this
		// deployment has the binary right here. Wired unconditionally,
		// like AgentKeyChecks above and for the same reason -- it
		// needs nothing the reconcile loop builds, just the live
		// configuration and this same secrets directory -- and a
		// deployment whose image somehow has no agy gets that sentence
		// on the field rather than a pane that will not load.
		AgentModels: &AgyModelLister{Live: live, creds: uiSecrets},
		CodexModels: &CodexModelLister{Live: live, Creds: uiSecrets},
		// And the same picker for Claude, filled from Anthropic's own
		// listing rather than from a binary, since the claude CLI has no
		// listing to run (claudeModelLister, claude.Catalog).
		ClaudeModels: &ClaudeModelLister{Live: live, Creds: uiSecrets},
		// What a connection with no CLI path of its own falls back to,
		// shown as that field's hint: the flags, which only a restart
		// changes, so read once here.
		AgentCLIDefaults: map[string]string{"claude": cfg.ClaudePath, "codex": cfg.CodexPath, "agy": cfg.AgyPath},
		// The bootstrap pane: this process owns the state repository its
		// store is exported to, so the UI it serves is the one place that
		// can offer the choice of where state lives (pkg/ui/staterepo.go).
		StateRepo: stateRepo,
		// And the same manager's answer to which repository that is, in
		// owner/name form (ui.Config.StateRepoRef): what "Run now" on one
		// of grain's own scheduled jobs fires with, since the template it
		// fires reads this deployment's state repository and nothing in
		// the template can know which that is. The reconcile loop is
		// handed the identical function for the identical reason
		// (orchestrator.Config.StateRepo above).
		StateRepoRef:   settingsRepo,
		ForbiddenRepos: forbidden,
		// The three host-power actions, nil under Kubernetes: there is
		// no machine here that is this deployment's to reboot or power
		// off (the node runs other people's workloads), and restarting
		// grain is the Deployment controller's job -- `kubectl rollout
		// restart` or a deleted pod -- rather than a command this
		// process runs against itself. The UI reports all three
		// unavailable rather than offering buttons that could only 404
		// (ui.Config.Reboot's nil-means-unavailable contract, and
		// kubernetesMode for why the whole group goes together).
		Reboot:      HostAction(cfg, newRestartRequests(cfg.DataDir).wrap("the reboot button", rebootHost(cfg.RebootCmd))),
		Shutdown:    HostAction(cfg, shutdownHost(cfg.ShutdownCmd)),
		Restart:     HostAction(cfg, newRestartRequests(cfg.DataDir).wrap("the restart button", restartDaemon(cfg.RestartCmd))),
		RepoNames:   repoNameGate{},
		Credentials: uiCredentials,
		// What each of those credentials reaches on GitHub, so a task is
		// refused at filing against a repository its team cannot reach
		// and the New task pickers can offer what it can (grain/task-1850).
		GitReach:               &gitproxy.Reach{Credentials: uiCredentials, API: githubTransport, Host: cfg.GithubHost},
		GitHubBranches:         githubClient,
		GitHubPullRequestFiles: githubClient,
		GitHubPullRequests:     githubClient,
		GitHubForkPullRequests: githubClient,
		GitHubDefaultBranches:  githubClient,
		// The manual "Import issues now" (POST .../import-issues): the
		// orchestrator's own pass, read through the UI's GitHub client.
		IssueImporter: func(ctx context.Context, repo model.RepoRef) (model.IssueImportResult, error) {
			return orchestrator.ImportIssues(ctx, store, githubClient, repo, time.Now().UTC())
		},
		GitHubCredentialAvailable: githubTokens.HasCredential,
		// The System pane's Logs tab: which streams this deployment can
		// show is a property of the shape it runs as, so logSources
		// decides it rather than a literal here.
		Logs:        LogSources(cfg),
		LogRedactor: logRedactor(SecretsDir(cfg.DataDir), uiSecrets),
		// The deployment's external UI base, for the one absolute link
		// grain writes (Grain-Task: in a changelist CL's description); unset
		// leaves that line out rather than writing localhost.
		ExternalBase: os.Getenv(ExternalBaseEnv),
		// liveTranscriptDir reads back whatever the Framework driving a
		// still-running attempt has mirrored so far into
		// transcriptDir/<runID> (transcriptDir's own doc comment on the
		// shared directory convention). antigravity.LiveTranscriptDir and
		// claude.LiveTranscriptDir read two different file formats
		// (RunConfig.TranscriptPath's own doc comment), and with a task
		// able to override this deployment's framework both formats can
		// be in this one directory at once -- so the reader wired here
		// picks between them per file rather than per deployment.
		LiveTranscripts: LiveTranscriptDirWithStore(transcriptDir, cfg.transcriptObjects),
		ArchivedTranscripts: func() ui.ArchivedTranscript {
			if cfg.transcriptObjects == nil {
				return nil
			}
			return cfg.transcriptObjects
		}(),
		// orchestrator.ChecksUnavailable reads process-lifetime state
		// RunCycle's own reconcile loop sets the first time GitHub 403s a
		// check-runs read against this deployment's credential -- see its
		// own doc comment and Config.AutoMergeDegraded's.
		AutoMergeDegraded: orchestrator.ChecksUnavailable,
		// An unmigrated host-mode install, found by run() at start
		// (installnotice.go).
		InstallNotice: InstallNotice,
		// sandboxHealthAdapter wraps whichever of orchestrator.
		// GranuleSandboxes/HostSandboxes run() built as sandboxes (exactly
		// one, per run()'s own doc comment) -- both implement Health, just
		// not the interface ui.Config.Sandboxes actually names, since ui
		// does not import pkg/orchestrator (see ui/sandbox_health.go's own
		// doc comment). The sandbox health pane (bwsalmon/agents#536).
		Sandboxes: SandboxHealthAdapter{
			Inner:  sandboxes,
			Limits: func() model.Limits { return live.Current().Limits() },
		},
		// And which image those sandboxes are created from, beside the
		// one this build is stamped for: a pane is where an operator
		// looks when a sandbox is behaving like an older commit's, and
		// the startup warning about the two disagreeing has scrolled out
		// of the journal by then (uiSandboxImage, grain/task-306).
		SandboxImage:     uiSandboxImage(cfg),
		SandboxImageFunc: func() *ui.SandboxImage { return uiSandboxImage(live.Current()) },
		// Whether the three sandbox-shape settings on that same pane
		// actually shape anything here, which is a property of the
		// backend built above rather than of the row they are stored in
		// (sandboxShapeIgnored's own doc comment). Under the default
		// host-directory sandboxing they shape nothing at all, and
		// saying so beside them is the whole of grain/task-9.
		SandboxShapeIgnored: SandboxShapeIgnored(sandboxes),
		// And how much host there is for the shape those settings ask
		// for, times every slot this deployment allows -- the question
		// no pane was asking, since max_workers is on one tab and the
		// shape is on another and the machine's own size is on a third
		// (ui.SandboxCommitment, grain/task-223). nil unless the
		// backend built above is one whose sandboxes really are on this
		// process's own machine; sandboxHostCapacity's own doc comment
		// for why that is asserted rather than assumed.
		SandboxHostCapacity: sandboxHostCapacity(sandboxes),
		// The auto overcommit controller's live state, for the same panes
		// (model.OvercommitController); there is one wherever there is a
		// host capacity to scale.
		Overcommit: overcommitState(live, sandboxes),
		// hostStats reads this same process's own machine, not any one
		// sandbox -- see pkg/sysstat's own doc comment on why that's a
		// separate reading from Sandboxes above. It takes a list of
		// paths because a disk figure has to name a filesystem, and this
		// deployment has more than one worth naming (hostDisks' own doc
		// comment on which). Resolved once, out here, rather than per
		// poll: which filesystems they are is fixed for the life of the
		// process, and finding docker's own asks its engine.
		// nil under Kubernetes, like the power actions above: /proc in
		// this container is the node's, so what this would report is
		// the pressure on a machine shared with workloads that are
		// nothing to do with grain, labelled as though it were this
		// deployment's own. The sandbox rows beside it stay -- those
		// are pods this deployment really does own -- and the node's
		// real figures are `kubectl top node`'s to give.
		HostStats: HostStatsOrNil(cfg, disks, live.diskFloor),
		// The same volumes, for the pane that is editing the floor rather
		// than watching it: Settings -> Sandbox resolves the share it is
		// showing against each of them, since a floor of "a tenth" is not
		// a figure until it meets a disk (ui.Settings.SandboxDiskFloors,
		// grain/task-372).
		SandboxVolumes: HostVolumesOrNil(cfg, disks),
		// hostTop is the per-process half of that same reading -- which
		// processes are spending the machine HostStats above says is
		// busy (pkg/hosttop's own doc comment). Same machine, and in a
		// container deployment the same PID namespace this process is
		// in: `docker run` is given no --pid=host (scripts/setup.sh's
		// own docker_run_args), so what this lists is the daemon and
		// everything it forked, which is where daemon-side load comes
		// from. A sandbox's own processes belong to its VM and are not
		// in it -- that is what the sandbox rows beside this report.
		// Also nil under Kubernetes, and for HostStats' reason narrowed
		// to processes: this container's PID namespace holds the daemon
		// and what it forked, but the machine whose load an operator is
		// chasing is the node, whose other pods are invisible from here
		// -- so the pane would be at its most misleading exactly when it
		// was most wanted.
		HostTop: HostTopOrNil(cfg),
		// And the reading that is about this process rather than about
		// the machine under it: its uptime, how many times this service
		// has started in the last half hour, and the flags it was
		// started with and does not define. A deployment in a restart
		// loop is one whose every other figure here looks healthy in the
		// seconds it is up (ui.DaemonHealth, grain/task-233).
		Daemon: daemonHealthOrNil(cfg),
		// A fourth reading sat here (ui.Config.RootShell,
		// grain/task-13): one command, as root on this same machine,
		// for the failure the three panes above could not explain. It
		// is gone (grain/task-193) -- what it handed whoever reached
		// this UI was unrestricted root on the host, which is more than
		// the whole of the rest of this config put together, and no
		// debugging pane is worth it. An operator still has whatever
		// way in they deployed the host with.
		//
		// ReconcilerDown mirrors this same process's own package-level
		// reconcilerDown (daemon.go), the same way AutoMergeDegraded above
		// mirrors orchestrator.ChecksUnavailable -- bwsalmon/agents#576.
		ReconcilerDown: func() bool { return ReconcilerDown.Load() },
		// And why, with the error and since when: the same fact as
		// named conditions, for the health route, the config response
		// and the pipeline feed.
		DispatchConditions: DispatchConditions,
		// And the store switched underneath it (storeswitch.go).
		StoreSwitch: CurrentStoreSwitch,
		// Restarting: this process has been told to stop, or the
		// upgrader is running an upgrade that ends in a restart.
		Restarting: daemonRestarting(cfg, upgrader),
		// And the moment the first half of that starts, for the long
		// poll that has to hear it before the API goes away.
		ShutdownStarted: cfg.shutdownStarted,
		// The other half of this process's own cycleWakes: a reply that
		// un-parks a task asks the reconcile loop for a cycle now instead
		// of leaving the person to wait out -poll-interval. Wired
		// unconditionally, since a wake written before that loop starts
		// (this server is up first, deliberately) sits in the buffer and
		// is read when it does.
		Wake:                  wakeCycle,
		TriggerIAPSync:        TriggerIAPSync,
		HubStatus:             hubStatus,
		TriggerHubHeartbeat:   triggerHubHeartbeat,
		TriggerHubSync:        triggerHubSync,
		SandboxBackends:       cfg.availableSandboxBackends(),
		SandboxBackendOptions: cfg.sandboxBackendOptions(),
		// And whether a merged settings change reset something on this
		// deployment that grain had nowhere to say out loud -- the same
		// marker, for a fact that reaches nobody without one
		// (ui.Config.StateRepoResetNotice, grain/task-197). Answered off
		// an atomic the manager keeps up to date, so this poll neither
		// waits on that lock nor runs git.
		StateRepoResetNotice: stateRepoResetNotice(stateRepo),
		// And whether the settings have stopped reaching the state
		// repository at all, for the standing banner and for
		// read_deployment_health (StateManager.syncFailure).
		StateRepoSyncFailure: stateRepoSyncFailure(stateRepo),
		// RunningConfig is what this process actually has in effect right
		// now (liveConfig's own doc comment), which the Settings pane
		// compares the stored row against to say which of the two
		// restart-only settings have been saved but not yet applied.
		RunningConfig: live.ModelConfig,
		// Where each agent CLI runs, resolved from the flags this daemon
		// started with and the settings the pane is showing (clirun.go).
		CLIRuns: func(ctx context.Context, stored map[string]model.CLIRunSetting) any {
			resolving := cfg
			resolving.CLIRun = stored
			return resolving.ResolveCLIRuns(ctx)
		},
		CLIRunCheck:  cfg.CheckCLIRunSettings,
		HostExecutor: func(ctx context.Context) any { return hostExecutorStatus(ctx, cfg, store) },
		// PullRequests is how a dispatched run opens its own pull request
		// before it exits (ui.Config.PullRequests' own doc comment): its
		// mcpserver asks this API, which asks the daemon's GitHub client.
		// The gate, rather than that client directly, because this server
		// starts before runDaemon has built one -- deliberately, so the
		// UI survives a reconcile loop that never comes up at all -- and
		// livePullRequests is what closes that gap once it does.
		PullRequests: PullRequestGate{},
		// PullRequestComments is how closing a task says so on the pull
		// request that close just orphaned (ui.Config.PullRequestComments'
		// own doc comment). Behind the same gate, and for the same reason
		// -- and where PullRequests turns a missing client into a refused
		// request, this one turns it into a line in the note left on the
		// task saying the pull request itself was not told.
		PullRequestComments: PullRequestGate{},
		// PullRequestCloser is the other half of that same close, and the
		// only route in a grain deployment by which a pull request is
		// ever closed rather than merged: a human who ticked the box
		// beside Close asking for this task's pull request to be closed
		// with it (ui.CloseOptions.ClosePullRequest). Behind the same
		// gate as the two above. A deployment that wanted its UI able to
		// say things on GitHub but never to shut anything would leave
		// exactly this field nil, which is why it is a field of its own.
		PullRequestCloser: PullRequestGate{},
		// SandboxRecreate is how a dispatched run gets out of a sandbox
		// it has broken beyond what it can fix from inside one
		// (ui.Config.SandboxRecreate's own doc comment): its mcpserver
		// asks this API, which asks the registry the run put itself in
		// when it was dispatched. No gate, unlike PullRequests above --
		// see sandboxRecreations' own doc comment.
		SandboxRecreate: sandboxRecreateAdapter{sandboxRecreations},
		// SandboxProbe builds one throwaway sandbox from the same backend
		// runs use, admitted and metered as they are, for `grain sandbox
		// probe`. Gated by its route's scope, not here.
		SandboxProbe: sandboxProbeAdapter{
			sandboxes: sandboxes,
			meter:     orchestrator.MeterFor(store),
			cache: orchestrator.PackageCacheSource{
				Base: pkgCache.sandboxURL(live), CA: pkgCache.sandboxCA(live), DNS: pkgCache.SandboxDNS(live),
			},
			config: live.Current,
		},
		// StandingSandboxes is what the reconcile loop's own supervisor
		// is running, for GET /api/sandboxes. No gate, for the reason
		// SandboxRecreate needs none (standingSandboxes' doc comment).
		StandingSandboxes: standingSandboxStatuses,
		// PersistentSandboxes is the same supervisor the reconcile loop
		// runs them through: their status, controls and /staging/ proxy.
		PersistentSandboxes: persistentSandboxAdapter{store: store, sup: persistentSandboxes},
		// WaitCredit is how a run waiting on its own CI keeps the clock
		// from cancelling it halfway through the wait: the same hop,
		// asking the same kind of registry, and needing no gate for the
		// same reason (ui.Config.WaitCredit, runWaitCredits).
		WaitCredit: waitCreditAdapter{runWaitCredits},
		// TaskCanceller is how the Stop button in the UI cleanly interrupts
		// an in-flight turn (ui.Config.TaskCanceller, taskCancellers).
		TaskCanceller: taskCancellerAdapter{taskCancellers},
		// The reconcile loop's own tick, for GET /api/metrics' "cycles"
		// section. No gate needed, unlike PullRequests above: cycleTimes
		// is allocated at process start and runDaemon writes into that
		// same ring once it gets there (cycleTimes' own doc comment).
		Cycles: cycleTimesAdapter{cycleTimes},
		// And when this deployment's own parameters last moved, for the
		// "changes" section beside them: the state manager reads it off the
		// repository it owns (its own SettingsChanges). nil for a UI
		// handed no manager, which the section reports as "nobody can say"
		// rather than as "nothing has changed".
		SettingsHistory: settingsHistory,
		// And the machine underneath all of it, over time (hostLoad's own
		// doc comment). No gate, for the reason Cycles needs none: the ring
		// is allocated at process start and the sampler writes into that
		// same ring once runDaemon gets there.
		HostLoad:    hostLoadAdapter{hostLoad},
		GitHubCalls: gitHubCallStats{},
		// The dispatch gate an agent's usage limit closes, for the
		// banner that says why a queue of ready tasks has nothing
		// running and for the operator who wants it open again
		// (ui.Config.AgentPause's own doc comment). No adapter: unlike
		// Sandboxes above there is no shape to convert, and
		// *orchestrator.Pause satisfies ui.AgentPause as it stands. No
		// gate either, for the reason Cycles above needs none --
		// agentPause is allocated at process start, and reading it
		// before runDaemon gets there reports nothing paused.
		AgentPause: agentPause,
		InstallTrackProbe: &installtrack.Probe{
			IncludeHost:    true,
			AgyPath:        cfg.AgyPath,
			UIAllowedHosts: cfg.UiAllowedHosts,
			DataDir:        cfg.DataDir,
		},
		CheckImageManifest: func(ctx context.Context, imageRef string) error {
			if LocalSandboxes != nil && !strings.HasPrefix(imageRef, "127.0.0.1:") && !strings.HasPrefix(imageRef, "localhost:") {
				return nil
			}
			if err := ui.CheckImageManifest(ctx, imageRef); errors.Is(err, ui.ErrImageNotFound) {
				return err
			}
			return nil
		},
		CheckClusterImageConnectivity: func(ctx context.Context, imageRef string) error {
			if LocalSandboxes != nil {
				return nil
			}
			return checkClusterSandboxImageConnectivity(ctx, live.Current(), imageRef)
		},
		// The Granule VM image the Settings pane probes is the one this
		// daemon resolves -- its stamp, mirrored -- not the UI's
		// version-derived default (k8sGranuleImageForSettings).
		K8sGranuleImage: func(mc model.Config) string {
			return k8sGranuleImageForSettings(live.Current(), mc)
		},
		// Which repository that image is mirrored into, and whether
		// k8s_granule_image is read as it rather than as an image
		// (sandboxImageMirrorForSettings), so the pane can say so and a
		// save clearing such a pin keeps the mirror.
		SandboxImageMirrorFor: func(mc model.Config) ui.SandboxImageMirrorInfo {
			return sandboxImageMirrorForSettings(live.Current(), mc)
		},
		// The Settings pane's sandbox image check reads labels the way
		// the daemon's own startup check does: local docker first, then
		// the registry (FetchImageLabels), Google-authenticated for an
		// image on Artifact Registry or GCR.
		InspectImageLabels: sandboxImageLabels(),
	}
	var localRepoComparer ui.RepoComparer
	if comparer, ok := stateRepo.(ui.RepoComparer); ok {
		localRepoComparer = comparer
	}
	uiCfg.RepoComparer = repoComparisonGate{local: localRepoComparer}
	// Assigned only when there is one, as Upgrader below: a nil
	// *hostd.Remote in the interface would not read as nil.
	if cfg.HostdHub != nil {
		uiCfg.HostdRemote = cfg.HostdHub
	} else if cfg.HostdRemote != nil {
		uiCfg.HostdRemote = cfg.HostdRemote
	}
	uiCfg.LocalHostdLogins = cfg.localHostdLogins
	uiCfg.AmbientNeedsHost = cfg.CLIRunsThroughHostd
	branchGate := repoBranchGate{remote: githubTokens}
	if lister, ok := stateRepo.(configRepoBranchLister); ok {
		branchGate.local = lister
	}
	uiCfg.RepoBranches = branchGate
	revGate := repoRevGate{remote: githubTokens}
	if resolver, ok := stateRepo.(configRepoRevResolver); ok {
		revGate.local = resolver
	}
	uiCfg.RepoRevs = revGate
	// grain's own CI, behind the same kind of gate PullRequests above is
	// behind and for the same reason: the engine belongs to runDaemon
	// (it needs the git proxy every job clones through), and this server
	// starts before it. Assigned only when this deployment runs CI at
	// all, because a nil ui.Config.CI is how the API says so -- a gate
	// wired unconditionally would have GET /api/ci/runs answering
	// `enabled: true` on a daemon started with -ci=false.
	if cfg.Ci {
		uiCfg.CI = CiGate{}
	}
	if cfg.DefaultTargetRepo != "" {
		repo, err := model.ParseRepo(cfg.DefaultTargetRepo)
		if err != nil {
			return nil, fmt.Errorf("-default-target-repo: %w", err)
		}
		uiCfg.DefaultTarget = &repo
	}
	// Assigned only when there is one: uiCfg.Upgrader is an interface,
	// and a nil *upgrade.Upgrader put in one is not nil -- which is the
	// very check every upgrade route makes to decide whether this
	// deployment offers upgrading at all.
	if upgrader != nil {
		uiCfg.Upgrader = upgrader
		uiCfg.LastRestart = lastRestart(store, upgrader, cfg.runHostUpgrade.adoption())
	}
	uiCfg.DisableSelfUpgrade = cfg.DisableSelfUpgrade
	uiCfg.ManagementURL = strings.TrimSpace(cfg.ManagementURL)
	// And one line in the journal saying which of the two pipelines this
	// deployment's upgrades actually run, or that they are off. Here
	// rather than inside newUpgrader, so that it is said where the
	// upgrader is put to use rather than wherever it happens to be
	// built.
	switch {
	case cfg.DisableSelfUpgrade:
		if uiCfg.ManagementURL != "" {
			log.Printf("grain daemon: in-VM self-upgrade is disabled (-disable-self-upgrade); upgrades are managed externally at %s", uiCfg.ManagementURL)
		} else {
			log.Print("grain daemon: in-VM self-upgrade is disabled (-disable-self-upgrade); upgrades are managed externally")
		}
	case cfg.UpgradeImage != "":
		log.Print(upgradeImageNote(cfg))
		if len(cfg.UpgradeVerifyCmd) == 0 {
			log.Print("grain daemon: warning: upgrades do not verify image signatures (no -upgrade-verify-cmd), so they run whatever the registry serves")
		}
	case cfg.UpgradeSrcDir != "":
		log.Printf("grain daemon: upgrades build in %s and install to %s%s",
			cfg.UpgradeSrcDir, cfg.UpgradeInstallPath, restartNote(cfg.UpgradeRestartCmd))
	default:
		log.Print("grain daemon: upgrading from the UI is off -- neither -upgrade-image nor -upgrade-src-dir is set")
	}

	if ep := strings.TrimSpace(cfg.UiCloudIdentityEndpoint); ep != "" {
		uiCfg.GroupChecker = ui.NewCloudIdentityGroupChecker(ui.WithCloudIdentityEndpoint(ep))
	}
	uiClient := ui.NewClient(uiCfg, store)
	if err := uiClient.MigrateAllowedPrincipals(context.Background()); err != nil {
		log.Printf("grain daemon: WARNING: migrating allowed principals: %v", err)
	}
	// The sandbox proxy's acting /cli/ routes act through this same
	// client (sandbox_cli.go).
	liveSandboxCLI.Store(uiClient)
	srv := ui.NewServerWithClient(uiClient)
	if cfg.HubConnect {
		if reg := live.Hub(); reg != nil && !reg.HasTunnelConnector() {
			reg.SetTunnelConnector(NewHubTunnelSupervisor(HubTunnelSupervisorOptions{
				Handler:        srv.HubTunnelHandler(),
				OnUnauthorized: reg.InvalidateOIDCCache,
			}))
		}
	}
	// A CI job's api token is answered by this same handler, in process,
	// held to its route table (pkg/capability/ciapi).
	CIAPI.SetUpstream(srv)
	// And the sandbox proxy's task://, deploy:// and /cli/ reads ask the
	// same handler, in process, what the MCP tools ask it over the wire
	// (sandbox_namespace.go).
	LiveAPI.Set(srv)
	// GET /api/fs/ serves the namespace the sandbox proxy is built with
	// (startGitProxy), which comes up after this server does.
	srv.SetNamespace(liveNamespace.Load)

	// Loopback listener for capability servers (host-script approve: true)
	// to request, wait on, and expire per-call approvals under their minted
	// task-scoped ghsa_ bearer tokens (localScriptCallbackServer).
	callbackSrv, callbackLn, err := localScriptCallbackServer(srv)
	if err != nil {
		return nil, err
	}
	localScriptApprovalURL.Store("http://" + callbackLn.Addr().String())
	go func() {
		_ = callbackSrv.Serve(callbackLn)
	}()

	var httpSrv *http.Server
	if cfg.UiAddr != "" {
		ln := uiLn
		uiLn = nil
		if ln == nil {
			ln, err = net.Listen("tcp", cfg.UiAddr)
			if err != nil {
				localScriptApprovalURL.Store("")
				_ = callbackSrv.Close()
				return nil, fmt.Errorf("listening on %s: %w", cfg.UiAddr, err)
			}
		}
		url := "http://" + ln.Addr().String()
		log.Printf("grain daemon: serving the UI/API on %s as %s", url, uiCfg.Actor.ID)
		if cfg.UiOpen {
			OpenBrowser(url)
		}
		if !ui.HostGuardEnforced(cfg.UiAddr, cfg.UiAllowedHosts) {
			log.Printf("grain daemon: -ui-addr %s listens on every interface and -ui-allowed-hosts is unset, so only requests "+
				"arriving over loopback have their Host header checked (localhost, IP literals and this machine's own names); "+
				"name the hosts this UI is reached by with -ui-allowed-hosts to check every request", cfg.UiAddr)
		}
		httpSrv = &http.Server{Handler: ui.HostGuard(srv, cfg.UiAddr, cfg.UiAllowedHosts)}
		go func() {
			if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("grain daemon: UI/API server: %v", err)
			}
		}()
	}

	log.Printf("grain daemon: single-tenant: every caller, this host's loopback included, presents the access code in %s -- "+
		"a browser on the UI's \"Enter access code\" page, the grain CLI as a bearer token; `grain access-code` prints it",
		ui.LocalAPITokenPath(cfg.DataDir))

	// Every connection whose provider can be asked keeps what it lists
	// up to date (ui.Client.AdvertiseDiscoveredModels), so adding a
	// provider puts every model its key reaches in the Model pickers, and
	// a model it adds or drops later follows on its own.
	discoveryCtx, cancelDiscovery := context.WithCancel(context.Background())
	go uiClient.RunModelDiscovery(discoveryCtx, ui.ModelDiscoveryInterval)

	return func(ctx context.Context) error {
		cancelDiscovery()
		localScriptApprovalURL.Store("")
		_ = callbackSrv.Shutdown(ctx)
		if httpSrv != nil {
			return httpSrv.Shutdown(ctx)
		}
		return nil
	}, nil
}

// daemonRestarting is ui.Config.Restarting for this process: true once
// run()'s context is cancelled (cfg.shuttingDown) and while upgrader
// reports an upgrade running, since the one that succeeds ends by
// restarting this process.
func daemonRestarting(cfg Config, upgrader *upgrade.Upgrader) func() bool {
	return func() bool {
		if cfg.shuttingDown != nil && cfg.shuttingDown.Load() {
			return true
		}
		if upgrader == nil {
			return false
		}
		status, err := upgrader.Status()
		return err == nil && status.Phase == upgrade.PhaseRunning
	}
}

// lastRestart is ui.Config.LastRestart: the report the post-upgrade check
// is told (orchestrator.UpgradeAdoption), read again for each request so
// the upgrade pane and the check say the same thing.
func lastRestart(store *model.Store, upgrader *upgrade.Upgrader,
	adoption func() *orchestrator.AdoptionReport) func(context.Context) *ui.RestartAdoption {
	return func(ctx context.Context) *ui.RestartAdoption {
		status, err := upgrader.Status()
		if err != nil {
			log.Printf("ui: reading the upgrade status for the last restart's report: %v", err)
			return nil
		}
		return restartAdoption(orchestrator.UpgradeAdoption(ctx, store, adoption, status, time.Now().UTC()))
	}
}

// localScriptCallbackAddr is where the local-script approval callback
// listener binds: loopback only, on a port the kernel picks.
const localScriptCallbackAddr = "127.0.0.1:0"

// localScriptCallbackServer opens the second listener startUIServer runs:
// a loopback one whose address is published as the approval-callback base
// URL (localScriptApprovalURL) that localexec.HTTPApprover dials. It
// serves the whole UI handler, so it is guarded the way the -ui-addr
// listener is: HostGuard refuses a Host naming a domain this daemon does
// not answer to with 421, which is what keeps a DNS-rebound page that
// found the port by scanning from reaching it (grain#2380). HTTPApprover
// sends the IP-literal Host of that URL, which HostGuard always allows.
func localScriptCallbackServer(handler http.Handler) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", localScriptCallbackAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("listening for local-script approval callbacks: %w", err)
	}
	srv := &http.Server{Handler: ui.HostGuard(handler, localScriptCallbackAddr, nil), ReadHeaderTimeout: 30 * time.Second}
	return srv, ln, nil
}

// restartAdoption is report in the shape the upgrade pane reads, or nil.
func restartAdoption(report *orchestrator.AdoptionReport) *ui.RestartAdoption {
	if report == nil {
		return nil
	}
	notes := func(in []orchestrator.AdoptedRunNote) []ui.RestartRun {
		var out []ui.RestartRun
		for _, n := range in {
			out = append(out, ui.RestartRun{Run: n.Run, Task: n.Task, Framework: n.Framework, Setup: n.Setup, Reason: n.Reason, Session: n.Session})
		}
		return out
	}
	return &ui.RestartAdoption{At: report.At, Adopted: notes(report.Adopted), Lost: notes(report.Lost), Freed: report.Freed}
}

// controllerMCPTransport carries a run's grain-controller calls to this
// daemon's own POST /api/mcp as the daemon itself: in-process through
// LiveAPI (which startUIServer publishes whether or not -ui-addr opens a
// listener), as an authenticated loopback request that runs the same
// gates a TCP one does (LoopbackRoundTrip) -- so it needs no listener to
// be reachable, no IAP to admit it, and sends the access code nowhere.
type controllerMCPTransport struct {
	dataDir string
}

func (t controllerMCPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	// Read per request rather than once: the access code rotates
	// (RotateLocalAPIToken) and a run outlives a rotation.
	if t.dataDir != "" {
		if raw, err := os.ReadFile(ui.LocalAPITokenPath(t.dataDir)); err == nil {
			if tok := strings.TrimSpace(string(raw)); tok != "" {
				out.Header.Set(ui.AccessCodeHeader, tok)
			}
		}
	}
	return LiveAPI.LoopbackRoundTrip(out)
}

// controllerMCPProxy returns orchestrator.Config.ControllerMCP for this daemon:
// a host-side capability proxy targeting POST /api/mcp with daemon auth
// (X-Grain-Access-Code) and X-Grain-Acting-Task: <taskID> injected.
func controllerMCPProxy(cfg Config) func(taskID string) (*protocol.Proxy, error) {
	client := &http.Client{Transport: controllerMCPTransport{dataDir: cfg.DataDir}}
	return func(taskID string) (*protocol.Proxy, error) {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" {
			// Without an acting task the endpoint would answer as the
			// deployment's own actor, with every scope.
			return nil, errors.New("grain-controller needs the task it acts for")
		}
		// The URL's host is nominal: the transport never dials it.
		return protocol.NewEndpointProxy("http://127.0.0.1/api/mcp", map[string]string{ui.ActingTaskHeader: taskID}, client)
	}
}
