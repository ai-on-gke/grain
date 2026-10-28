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

// daemon.go implements `grain daemon`, formerly its own cmd/graind
// binary before bwsalmon/agents#313 combined every mode into one: it
// runs pkg/orchestrator's RunCycle in the background on a timer, until
// SIGINT/SIGTERM, against one real embedded SQLite store, and -- unless
// -ui-addr is emptied out -- also serves pkg/ui's JSON API and static
// frontend directly over that same store (bwsalmon/agents#363). That
// used to be a separate "ui" subcommand (formerly its own cmd/ui binary),
// opening the store a second time -- SQLite's own file locking is what
// makes a daemon and a UI both writing that same file at once safe
// (pkg/model/sqlite's own doc comment). Folding UI serving into the same
// process this dispatch loop already runs in removes the need for a
// second process entirely: there is one process, one store connection,
// one thing to run behind Tailscale or IAP (the issue's own "so the
// server ui/api does not need auth on its own"), and cmd/grain's own
// task CLI is a REST client of the API this serves (main.go's own doc
// comment) rather than a second direct store writer.
//
// bwsalmon/agents#254 asked for exactly this, with one simplification: by
// default, sandboxing is still local -- the MCP server's sandbox tools
// confined to a directory on this host, no isolation -- rather than a
// fleet this deployment shape has nowhere to run. -granule-sandboxes
// is the opt in to the real alternative (bwsalmon/agents#274):
// orchestrator.GranuleSandboxes, one real bwsalmon/granule-managed VM per
// run, reached over SSH. -max-workers/-max-mergers cap how many runs
// are in flight at once either way, and a sandbox is built for each of them and
// destroyed with it; nothing
// above pkg/orchestrator.Deps needs to change to serve more than one.
//
// graind originally drove pkg/orchestrate, a package built independently
// of, and in parallel with, pkg/orchestrator (bwsalmon/agents#249) --
// bwsalmon/agents#263 reconciled the two, keeping pkg/orchestrator (issue
// intake, directive parsing, and PR-health sync were all already wired to
// dispatch.Cycle there) and porting pkg/orchestrate's own capability
// resolution/materialization and reconcile-loop shape onto it. See
// README.md for what that merge kept and dropped.
//
// Most of this file's own flags (-max-workers, -max-mergers, -poll-interval, -agent-framework,
// -gemini-model, -gemini-effort, -claude-model, -codex-model, -max-agent-turns, -github-host, -github-insecure-http, -gcp-project,
// -gcp-agent-service-account, -target-repos) are store-backed now
// (bwsalmon/agents#320):
// loadConfig writes them into model.Store's grain_config row the first
// time a deployment's store has none, and reads them back out of it on
// every start after that, so a UI or a CLI editing model.Config is what
// changes them from then on. Almost all of them change what this process
// is doing without waiting for a restart at all: liveConfig re-reads that
// row once per reconcile tick and hands each change to whatever it
// configures -- see its own doc comment for the full list, and for the
// settings (-github-host, -github-insecure-http, and the sandbox backend)
// that genuinely cannot be swapped under a live deployment and so are reported to the
// UI as needing a restart. What stays flags-only either has to
// be known before there is a store to read it from (-data-dir) or names
// secret material rather than being configuration itself
// (-gemini-api-key-file, -granule-ssh-key, -claude-oauth-token-file,
// -openai-api-key-file, -codex-auth-file) -- bwsalmon/agents#320's own
// "but not the secrets." -claude-path and -codex-path join them not
// because they are
// secret but because, like -granule-ssh-key, they name something about
// *this host's* filesystem rather than the deployment's own behaviour.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/actionscache"

	// The capability half of the model proxies, aliased because the
	// package it mints into (pkg/modelproxy, imported by
	// pkg/cli/modelproxy.go) has the same name -- one is the proxies,
	// the other is the grants that hand a run a token for one.

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/modelexec"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

func runDaemonCommand(args []string) {
	cfg, fs, unknownFlags, err := parseDaemonArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			daemonUsage(fs)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "grain daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "grain daemon: run `grain daemon -h` for the flags this build accepts")
		os.Exit(2)
	}
	if fs.Lookup("flag-names").Value.String() == "true" {
		printDaemonFlagNames(fs, os.Stdout)
		return
	}
	reportUnknownDaemonFlags(unknownFlags)

	// What "host" means here -- the bare machine, grain hostd, or
	// nothing at all on Kubernetes -- before the flags that ask for it
	// are checked against it (daemon/hostexec.go).
	hostExec := daemon.DetectHostExecutor(cfg, whererun.Detect(), nil)
	cfg.HostExec = &hostExec
	if hostExec.Kind == daemon.HostExecutorHostd {
		// An ambient git credential's gh (or a registered source's tool) runs on the host too,
		// through hostd's host-git capability server, when its allowlist
		// offers one (pkg/gitproxy/hostgit.go).
		gitproxy.UseHostGit(&gitproxy.HostGitClient{Hostd: hostd.Client{Socket: hostExec.Socket}, Stderr: os.Stderr})
	}

	if err := checkConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "grain daemon: %v\n", err)
		os.Exit(2)
	}

	// Before anything else this start does: is this start one of many?
	// A daemon that is restarting rather than running says so here, in
	// the journal, and goes on saying it in the UI for as long as it is
	// up (daemon_restarts.go, grain/task-233).
	cfg.Health = daemon.DaemonHealthFor(cfg, time.Now())
	cfg.Health.LogRestartLoop(log.Printf)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := daemon.Run(ctx, cfg); err != nil {
		log.Fatalf("grain daemon: %v", err)
	}
}

func parseDaemonArgs(args []string) (daemon.Config, *flag.FlagSet, []string, error) {
	// seedOnly marks a flag loadConfig only consults the first time this
	// -data-dir has ever seen a daemon: it seeds grain_config, and every
	// start after that reads the stored value back instead, silently
	// ignoring whatever this flag says -- see loadConfig's own doc
	// comment for why.
	const seedOnly = " (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig)"

	// ContinueOnError, not ExitOnError: what a failed parse means to a
	// service that systemd will restart for ever is this daemon's
	// decision rather than the flag package's, and ExitOnError takes it
	// away -- along with ~200 lines of usage text per restart. See
	// daemon_unknown_flags.go, and grain/task-233 for the outage.
	fs := flag.NewFlagSet("grain daemon", flag.ContinueOnError)
	// The one flag here that is not configuration: it asks this build to
	// print the flags it accepts and exit, which is how something
	// outside the process -- pkg/upgrade, before it cuts a deployment
	// over to a newly pulled image -- can tell whether a host's unit
	// names a flag that image would ignore.
	flagNames := fs.Bool("flag-names", false,
		"print every flag this build's `grain daemon` accepts, one per line -- the name, then \"bool\" or "+
			"\"value\" for whether it takes an argument -- and exit. "+
			"Not configuration: it is what an upgrade asks a newly pulled image before cutting over to it, "+
			"so that a systemd unit passing a flag the new build dropped is reported then rather than "+
			"discovered at the next start (pkg/upgrade/image.go)")
	dataDir := fs.String("data-dir", "", "root directory for the store and secrets -- the state a redeploy must not lose, and the one "+
		"directory to back up (required; scripts/setup.sh installs it at /var/lib/grain/data)")
	dbDriver := fs.String("db-driver", "sqlite", "database backend driver (\"sqlite\" or \"postgres\"); defaults to sqlite")
	dbDSN := fs.String("db-dsn", "", "database connection string / DSN or path; for sqlite defaults to <data-dir>/store")
	postgresURL := fs.String("postgres-url", "", "PostgreSQL connection URL (alias for -db-driver=postgres -db-dsn=URL)")
	sandboxDir := fs.String("sandbox-dir", "", "root directory for this deployment's disposable scratch space -- host-sandbox run directories, "+
		"and the package and Actions caches default under it, and the UI reports its disk as the sandboxes' own (scripts/setup.sh installs it "+
		"at /var/lib/grain/scratch, beside -data-dir's /var/lib/grain/data). Deliberately a separate flag from -data-dir rather than a "+
		"subdirectory of it (bwsalmon/agents#587): what lives here is disposable, unlike the store and secrets under -data-dir, so it is "+
		"never backed up and belongs on storage -- ideally a disk of its own -- that a VM wipe or redeploy is free to discard")
	dockerRootDir := fs.String("docker-root-dir", "", "path to the local docker engine's own data root, for the UI's host disk figures only "+
		"(grain/task-148): the sandbox image and every granule VM's disk overlay are written there rather than under -sandbox-dir, so on a "+
		"deployment that gives sandboxes a volume of their own it is a disk worth a figure. Empty asks `docker info` for it once at startup, "+
		"which is right whenever the daemon can reach the docker socket and sees that path where dockerd does; name it here when it cannot, "+
		"or when this process sees the same directory at a path of its own. A path this process cannot stat is logged once and left out, not "+
		"reported as a broken figure -- a containerised daemon is usually shown docker's socket and not the tree behind it")
	// Deprecated and ignored (grain/task-28). They seeded a
	// deployment-wide worker and merger ceiling, and there is no such
	// ceiling: what bounds the deployment is its sandbox budget.
	//
	// Still accepted, and still written into the grain_config columns of
	// the same names, because scripts/setup.sh has been writing them into
	// unit files for a long time and a daemon that refused to start on an
	// unknown flag would turn this change into an outage on every
	// deployment that has one. The columns are legacy too: nothing
	// schedules on them.
	maxWorkers := fs.Int("max-workers", model.DefaultMaxWorkers, "maximum number of ordinary tasks dispatched at once -- "+
		"the worker half of the concurrency dispatch.Cycle fills (model.Limits)"+seedOnly)
	maxMergers := fs.Int("max-mergers", model.DefaultMaxMergers, "deprecated and ignored: this deployment's sandbox budget "+
		"is what bounds the host (model.Ledger)")
	// Accepted only so a unit written by an older setup.sh can start the
	// first build that moves this setting into grain_config. Its value is
	// ignored; setup.sh no longer writes it and the stored setting is the
	// sole source of truth.
	fs.Int("max-ci-jobs", 0, "deprecated and ignored; configure max CI jobs in Settings")
	// The other half of every limit above: those are counts, and a count
	// does not say how big the sandbox it admits is (model.Budget,
	// grain/task-173). These three turn this machine into a total the
	// shapes are held against.
	//
	// Stored settings now (grain/task-197), so these three seed a fresh
	// database and are ignored once a row exists, like -max-workers
	// beside them: a budget an operator can only move by editing a
	// systemd unit is one they work around by raising a count instead,
	// which is exactly what the budget is here to stop meaning more
	// machine. Settings -> Sandbox and `grain settings` are where they
	// are changed on a deployment that has started once.
	cpuOvercommit := fs.Float64("sandbox-cpu-overcommit", model.DefaultCPUOvercommit,
		"how much more vCPU than this host has may be committed to sandboxes at once -- 2 promises a 28-core host 56 vCPU "+
			"of agent and CI sandboxes. Some CPU overcommitment is right: a run spends most of its wall time waiting on a "+
			"model or a network rather than on a core. 0 leaves vCPU bounded by the run counts alone (model.Budget)"+seedOnly)
	memoryOvercommit := fs.Float64("sandbox-memory-overcommit", model.DefaultMemoryOvercommit,
		"the same ratio for memory, and 1 by default rather than 2: the way a host finds memory it has already promised "+
			"is to swap, and a deployment that swaps has lost more throughput than the extra sandbox was worth. "+
			"0 leaves memory bounded by the run counts alone (model.Budget)"+seedOnly)
	diskOvercommit := fs.Float64("sandbox-disk-overcommit", model.DefaultDiskOvercommit,
		"the same ratio for sandbox disk: how many times over the sandbox volume (less its free-space floor), or a "+
			"cluster's allocatable ephemeral storage, sandbox disks may be promised. A sandbox disk is a thin overlay a run "+
			"mostly never writes; on Kubernetes this also scales each pod's ephemeral-storage request down while its limit "+
			"stays the full disk. 0 leaves disk bounded by the free-space floor alone (model.Budget)"+seedOnly)
	memoryReserveMB := fs.Int("sandbox-memory-reserve-mb", model.ReserveMemoryDefault,
		"memory kept back from that budget for everything on this host that is not a sandbox -- this daemon, the container "+
			"runtime, each VM's own qemu overhead and the page cache they all read through. Taken off the top, before the "+
			"overcommit ratio, so raising the ratio never eats into it. -1 takes an eighth of the machine, never less "+
			"than 8 GiB and never more than half of it (model.DefaultReserveMemoryMB)"+seedOnly)
	repairReserve := fs.Int("repair-reserve-sandboxes", model.DefaultRepairReserveSandboxes,
		"how many standard agent sandboxes' worth of that budget is kept for base-branch repairs -- tasks marked "+
			"fixes_base_branch and the CI confirming their branch green -- so a red base branch's repair starts on a "+
			"saturated deployment instead of queueing behind the work it blocks. 0 reserves nothing; a repair the "+
			"reserve cannot hold may still pre-empt work blocked on the same base (model.Budget.RepairReserve)")
	// And the dimension none of those three can be said in, because a
	// sandbox's disk is not committed when it is made but written as it
	// runs (model.DiskFloor, grain/task-196).
	//
	// Stored settings too, since grain/task-372, and so seeds like the
	// three above -- more obviously so than any of them: a deployment
	// wants to move this floor at the moment a filling volume has
	// stopped every dispatch, and a lever that needs a unit edit and a
	// daemon restart is not one an operator reaches for while their
	// deployment is already wedged.
	diskFloorShare := fs.Float64("sandbox-disk-floor-share", model.DefaultDiskFloorShare,
		"how much of the volume this deployment writes to (-data-dir, and -sandbox-dir when that is a different "+
			"filesystem) is kept free: new CI jobs are held and agent dispatch deferred while free space is below it. "+
			"A share rather than a size because a fixed number is wrong on both a 100 GB and a 4 TB volume. Never more "+
			"than a quarter of the volume, whatever this and -sandbox-disk-floor-min-mb come to. 0 with a min of 0 "+
			"leaves a filling disk to hold nothing back (model.DiskFloor)"+seedOnly)
	diskFloorMinMB := fs.Int("sandbox-disk-floor-min-mb", model.MinDiskFloorMB,
		"the least that floor is ever worth, whatever the share comes to on a small volume -- a tenth of a 20 GB "+
			"store disk is 2 GB, which is not enough room to notice a runaway write in"+seedOnly)
	// -max-concurrent is what -max-workers was called while it was the
	// whole limit. It stays accepted because a deployment's own systemd
	// unit outlives the build it was written for -- scripts/setup.sh
	// writes the flags in once and the UI's Upgrade button then replaces
	// only the binary -- so a rename that dropped the old spelling would
	// stop the daemon at exactly the moment nobody is watching a terminal.
	// Its zero default is how "not passed" is told from "passed as 0",
	// which flag.Int cannot say on its own.
	maxConcurrent := fs.Int("max-concurrent", 0, "former name of -max-workers, still accepted so an existing unit file keeps working; "+
		"pass one or the other, not both")
	pollInterval := fs.Duration("poll-interval", model.DefaultPollInterval, "how often to run a reconcile cycle"+seedOnly)
	backupInterval := fs.Duration("backup-interval", 0, "how often to upload the database and config repository to Google Drive or GCS; 0 disables scheduled backups")
	backupGCSBucket := fs.String("backup-gcs-bucket", "", "GCS bucket for scheduled backups; when set, upload to GCS instead of Google Drive")
	backupGoogleDriveFolder := fs.String("backup-google-drive-folder", "root", "Google Drive folder ID for scheduled backups")
	backupGoogleCredentialsFile := fs.String("backup-google-credentials-file", "", "Google credential JSON for scheduled backups; omitted uses Application Default Credentials")

	uiAddr := fs.String("ui-addr", "0.0.0.0:4242", "address to serve the UI/API on, in-process, over this same store -- empty disables it")
	uiAllowedHosts := fs.String("ui-allowed-hosts", "", "comma-separated host names the UI/API answers to besides IP literals, localhost and this machine's own "+
		"hostname (\"grain.example.com\", \"*.example.com\"); a request with any other Host header is refused with 421, which is what "+
		"stops a DNS-rebinding page reaching a loopback-bound API (grain#2380). Checked on every request when this is set or -ui-addr names "+
		"a specific address; on an every-interface -ui-addr with this unset, on loopback requests only. \"*\" turns the check off")
	uiAccessSessionLifetime := fs.Duration("ui-access-session-lifetime", ui.DefaultAccessSessionLifetime, ui.AccessSessionLifetimeUsage)
	uiIAPAudience := fs.String("ui-iap-audience", "", "the audience a signed X-Goog-IAP-JWT-Assertion must carry for the UI/API to accept the identity in it, "+
		"or several separated by commas -- IAP's /projects/NUMBER/global/backendServices/ID or /projects/NUMBER/apps/PROJECT, or what a BeyondCorp "+
		"tunnel's assertion names (the proxy's own docs); defaults to $GRAIN_IAP_AUDIENCE. With none, no assertion is accepted")
	uiIAPIssuer := fs.String("ui-iap-issuer", "", "the issuer a signed assertion must carry; defaults to $GRAIN_IAP_ISSUER, then "+ui.GoogleIAPIssuer)
	uiIAPKeysURL := fs.String("ui-iap-keys-url", "", "where the signing keys for those assertions are published (IAP's PEM map or a JWK set); "+
		"defaults to $GRAIN_IAP_KEYS_URL, then "+ui.GoogleIAPCertURL)
	uiOpen := fs.Bool("ui-open", false, "open the UI in the system's default browser once it's listening")
	actor := fs.String("as", "", "principal the UI/API attributes tasks and comments it creates to (defaults to the OS user)")
	defaultTargetRepo := fs.String("default-target-repo", "", "owner/name a task created through the UI/API with no repo of its own targets")
	policyFile := fs.String("policy", "", "local policy file (grain_policy.yml) every task must fit before it gets a sandbox; empty enforces none (docs/local-policy.md)")
	// The tunnel gateway and rendezvous relay these named are gone
	// (grain/task-2099, docs/relay-removed.md). They stay defined only so
	// a unit file still passing them stops the daemon with a clear
	// message (removedTunnelGatewaySettings, removedRelaySettings), rather
	// than being ignored as unknown and leaving the UI unreachable.
	fs.String("tunnel-gateway-url", "", "removed: the tunnel gateway is gone. The daemon refuses to start with this set")
	fs.String("tunnel-token", "", "removed: the tunnel gateway is gone. The daemon refuses to start with this set")
	fs.String("relay-url", "", "removed: the rendezvous relay is gone (docs/relay-removed.md). The daemon refuses to start with this set")
	fs.String("relay-auth-token-file", "", "removed: the rendezvous relay is gone (docs/relay-removed.md). The daemon refuses to start with this set")
	fs.String("relay-token", "", "removed: the rendezvous relay is gone (docs/relay-removed.md). The daemon refuses to start with this set")
	fs.String("relay-audience", "", "removed: the rendezvous relay is gone (docs/relay-removed.md). The daemon refuses to start with this set")
	fs.String("relay-iap-jwt-audience", "", "removed: the rendezvous relay is gone (docs/relay-removed.md). The daemon refuses to start with this set")
	fs.String("relay-session-lifetime", "", "removed: the rendezvous relay is gone (docs/relay-removed.md). The daemon refuses to start with this set")
	local := localFlags(fs)

	geminiAPIKeyFile := fs.String("gemini-api-key-file", "", "file holding the Gemini API key the agent runs as. "+
		"Optional now that the key can be set from the UI instead (Settings -> Models, stored as the "+
		"\""+secrets.GeminiAPIKeySecret+"\" secret): a key set there wins, and this file is what a deployment "+
		"seeded one with before that existed. With neither, a run driven by the gemini framework fails as "+
		"setup-failed saying so, rather than the daemon refusing to start")
	maxAgentTurns := fs.Int("max-agent-turns", 0, "cap on model/tool round trips per run (0 = uncapped; runs are bounded by wall-clock runtime instead)"+seedOnly)

	// claudePath and claudeOAuthTokenFile are only consulted when a run
	// is actually driven by agent/claude -- the store's agent-framework
	// setting (bwsalmon/agents#609, `grain settings`), or a task's own
	// override of it (model.Task.AgentFramework). Neither is required at
	// flag-parse time, and neither is required at all any more: both
	// frameworks' credentials are settable from the UI, into the secrets
	// database (secrets.ClaudeOAuthTokenSecret), which is where
	// agentCredential looks before it looks at either file. They stay
	// flags-only rather than moving into model.Config for the same
	// reason -gemini-api-key-file does: secret material, not
	// configuration itself.
	agyPath := fs.String("agy-path", "", "path to the Antigravity CLI binary (agy) agent/antigravity runs as a "+
		"subprocess; empty resolves \"agy\" against $PATH instead. Only used by a run driven by the "+
		"\""+model.AgentFrameworkAntigravity+"\" framework")
	claudePath := fs.String("claude-path", "", "path to the claude CLI binary agent/claude runs as a subprocess; "+
		"empty resolves \"claude\" against $PATH instead. Only used when the agent-framework setting is \"claude\"")
	claudeOAuthTokenFile := fs.String("claude-oauth-token-file", "", "file holding the Claude Code OAuth token the "+
		"agent authenticates as, passed to the claude subprocess as CLAUDE_CODE_OAUTH_TOKEN. Optional, and the "+
		"exact counterpart of -gemini-api-key-file above: the UI stores one as the "+
		"\""+secrets.ClaudeOAuthTokenSecret+"\" secret and that wins over this file")
	codexPath := fs.String("codex-path", "", "path to the codex CLI binary agent/codex runs as a subprocess; "+
		"empty resolves \"codex\" against $PATH instead. Only used when the agent-framework setting is \""+
		model.AgentFrameworkCodex+"\"")
	runHelp := func(cli string) string {
		return "where the " + cli + " CLI runs: \"container\" (a subprocess in the daemon's own container) or \"host\" " +
			"(through grain hostd, or a child of a daemon on the bare machine). Settings -> Models overrides it; empty " +
			"is container in a container and host on the bare machine. \"auto\" is accepted and ignored for one release"
	}
	claudeRun := fs.String("claude-run", "", runHelp("claude")+". claude also takes \"detached\": the image's claude, "+
		"through grain hostd's claude-detached entry, so a daemon upgrade does not interrupt it")
	codexRun := fs.String("codex-run", "", runHelp("codex"))
	agyRun := fs.String("agy-run", "", runHelp("agy"))
	// The per-run docker launcher these configured is gone (v2.3.0). They
	// stay defined so a unit an older installer wrote still starts, and
	// go in a later release.
	fs.String("cli-image", "", "deprecated and ignored: agent CLIs no longer run in a per-run container of an image")
	fs.String("cli-image-file", "", "deprecated and ignored: agent CLIs no longer run in a per-run container of an image")
	fs.String("cli-container-engine", "", "deprecated and ignored: agent CLIs no longer run in a per-run container of an image")
	hostdSocket := fs.String("hostd-socket", "", "grain hostd's unix socket, for a daemon in a container whose host "+
		"CLIs run through hostd; empty uses "+hostd.DefaultSocketPath+" when its directory is mounted into the container. "+
		"Refused on Kubernetes, which has no host")
	openAIAPIKeyFile := fs.String("openai-api-key-file", "", "file holding the OpenAI API key the agent "+
		"authenticates as, passed to the codex subprocess as OPENAI_API_KEY. Optional, and the exact "+
		"counterpart of the two files above: the UI stores one as the "+
		"\""+secrets.OpenAIAPIKeySecret+"\" secret and that wins over this file")
	modelProxyUnrestricted := fs.Bool("model-proxy-unrestricted", false, "let the model proxies forward every "+
		"method, path and query parameter of a vendor's API with this deployment's credential, rather than only its "+
		"model-inference endpoints (messages, responses, chat completions, generateContent and the model "+
		"listings). Off by default: a sandbox token then cannot reach a vendor's files, batch, fine-tuning or "+
		"organization APIs. Turn it on only for a deployment whose runs deliberately need those")
	modelProxyMaxConcurrent := fs.Int("model-proxy-max-concurrent", modelproxy.DefaultMaxConcurrent, "how many requests one run "+
		"may have open through each model proxy at once, a streamed reply counting until it finishes. The next waits up to -model-proxy-slot-wait "+
		"for one, then is refused with a 429 in the vendor's own error shape, so one run's token cannot monopolize this deployment's shared credential. 0 or less means the default")
	modelProxySlotWait := fs.Duration("model-proxy-slot-wait", modelproxy.DefaultSlotWait, "how long a request over -model-proxy-max-concurrent waits for a slot before its 429, so stock vendor clients ride out the cap. 0 means the default; a negative duration, such as -1s, refuses at once")
	codexAuthFile := fs.String("codex-auth-file", "", "shared auth.json written by `codex login` for ChatGPT "+
		"authentication. Used only when no OpenAI API key is configured. The file must be writable so "+
		"Codex can persist refreshed tokens; Grain links it into each private per-run CODEX_HOME")

	githubHost := fs.String("github-host", "github.com", "GitHub git host -- what the proxy forwards to and, via github.APIHost, where REST calls go; override to point at a mock for local testing"+seedOnly)
	githubInsecureHTTP := fs.Bool("github-insecure-http", false, "speak plain HTTP to -github-host instead of HTTPS (mock servers only)"+seedOnly)

	// The reporting service this deployment reads verdicts from
	// (pkg/checkpost), if it has one. Off unless both the url and the
	// expectation are given: a url with nothing expected reads nothing,
	// and an expectation with no url is a misconfiguration the client
	// reports rather than silently ignores.
	checkpostURL := fs.String("checkpost-url", "", "base url of this deployment's checkpost "+
		"service, which CI reports verdicts to; empty asks GitHub for check runs as before")
	checkpostExpect := fs.String("checkpost-expect", "", "which reporting checks make a "+
		"repository's answer complete, as owner/name=check,check;owner/other=check -- a "+
		"repository not named here is always read from GitHub")
	githubWebhookURL, githubWebhookAddr := githubWebhookFlags(fs)

	executorAddr := fs.String("executor-addr", "", "address to listen on for remote model executors (host:port, e.g. :4243); empty disables remote execution")
	modelExecAddr := fs.String("model-exec-addr", "", "legacy alias for -executor-addr")
	executorTokenFile := fs.String("executor-token-file", "", "file holding the shared token remote executors authenticate with (required with -executor-addr)")
	modelExecTokenFile := fs.String("model-exec-token-file", "", "legacy alias for -executor-token-file")
	executorWait := fs.Duration("executor-wait", modelexec.DefaultWait, "how long a dispatch waits for a remote executor to connect before giving up")
	modelExecWait := fs.Duration("model-exec-wait", 0, "legacy alias for -executor-wait")
	executorCertFile := fs.String("executor-cert-file", "", "PEM certificate file for TLS on -executor-addr (required with -executor-key-file)")
	modelExecCertFile := fs.String("model-exec-cert-file", "", "legacy alias for -executor-cert-file")
	executorKeyFile := fs.String("executor-key-file", "", "PEM private key file for TLS on -executor-addr (required with -executor-cert-file)")
	modelExecKeyFile := fs.String("model-exec-key-file", "", "legacy alias for -executor-key-file")

	gcpProject := fs.String("gcp-project", "", "GCP project the gcp-key/gemini-key capabilities mint into; empty disables both"+seedOnly)
	gcpServiceAccountEmail := fs.String("gcp-agent-service-account", "", "the narrow agent service account gcp-key mints keys for"+seedOnly)
	gcpServiceAccountCount := fs.Int("gcp-agent-account-count", 1, "the number of agent service accounts to use for gcp-key (default 1)"+seedOnly)
	iapBackendService := fs.String("iap-backend-service", "", "GCP Compute backend service name for dynamic IAP IAM reconciliation; empty disables IAP reconciliation"+seedOnly)
	iapResource := fs.String("iap-resource", "", "IAP web resource whose IAP access is reconciled from the teams and access list too, e.g. projects/<NUMBER>/iap_web/compute/services/<SERVICE>; "+
		"empty or \"none\" disables it"+seedOnly)

	// Upgrading (bwsalmon/agents#396, pkg/upgrade): the UI's own
	// "target a branch and click Upgrade" button. -upgrade-src-dir is the
	// opt in -- empty (the default) disables the whole feature and the
	// pane reports itself unavailable, same as -gcp-project disabling
	// gcp-key/gemini-key above. See scripts/setup.sh for how a
	// deployment built by it wires these three flags up so the feature
	// works out of the box.
	upgradeSrcDir := fs.String("upgrade-src-dir", "",
		"git checkout of bwsalmon/grain to fetch/build from when the UI's Upgrade button is used; empty disables the feature")
	upgradeInstallPath := fs.String("upgrade-install-path", "",
		"where a successful upgrade's binary is installed to (required with -upgrade-src-dir)")
	var upgradeRestartCmd stringSliceFlag
	fs.Var(&upgradeRestartCmd, "upgrade-restart-cmd",
		"one argument of the command run at the end of a successful upgrade to bring the new build up -- "+
			"repeat for every argument, e.g. -upgrade-restart-cmd=sudo -upgrade-restart-cmd=systemctl "+
			"-upgrade-restart-cmd=restart -upgrade-restart-cmd=grain-daemon.service. Omitted entirely, the "+
			"upgrade still happens but nothing brings the new build up on its own. Used by both upgrade "+
			"paths below.")

	// The container deployment's own upgrade path (bwsalmon/agents#645,
	// pkg/upgrade/image.go). Set, it replaces the build-a-binary path
	// above entirely: there is no toolchain on a host that runs grain
	// from an image, so "upgrade to branch X" means pulling the tag CI
	// published for X and pointing the unit's own image ref file at it.
	// Neither -upgrade-src-dir nor -upgrade-install-path is needed then,
	// since nothing here builds or installs a binary: an image deployment
	// passes neither, and the self-debug capability reads the source the
	// image carries rather than a checkout a flag names (see sourceDir).
	upgradeImage := fs.String("upgrade-image", "",
		"image repository, with no tag, that CI publishes a tag per branch to (e.g. "+
			"ghcr.io/bwsalmon/grain/grain); set, the UI's Upgrade button pulls a branch's image instead of "+
			"building one, and -upgrade-image-ref-file is required")
	// Which branches an upgrade may install (bwsalmon/grain#2385). An
	// upgrade replaces this daemon with whatever the branch builds, and a
	// branch name says nothing about whether anybody reviewed it, so the
	// trusted ones are the operator's to list here rather than the
	// request's to name.
	upgradeAllowedBranches := fs.String("upgrade-allowed-branches", strings.Join(upgrade.DefaultAllowedBranches, ","),
		"comma-separated branches the UI's Upgrade button, a scheduled upgrade or an agent may upgrade this "+
			"deployment to, each a path.Match pattern ('*' never crosses a '/'; e.g. main,release/*), or ** for "+
			"any branch at all; any other branch is refused. The default is grain's own release names -- main, "+
			"candidate, latest and vX.Y.Z release tags -- which only grain's merge queue and release cut move; "+
			"a task or feature branch needs listing here")
	upgradeDrainWindow := fs.Duration("upgrade-drain-window", 0,
		"maximum time a scheduled upgrade waits for active tasks and CI runs to drain before proceeding with the upgrade "+
			"(0 waits indefinitely for runs to complete normally)")
	upgradeImageRefFile := fs.String("upgrade-image-ref-file", "",
		"file a successful -upgrade-image upgrade rewrites with the image the service should run, as one "+
			"GRAIN_IMAGE=<ref> line for grain-daemon.service to read as an EnvironmentFile "+
			"(required with -upgrade-image)")
	// Who published the image an upgrade is about to run
	// (pkg/upgrade/verify.go, grain/task-1773). The digest pin says
	// which bytes; this says whose, before the health check runs them.
	var upgradeVerifyCmd stringSliceFlag
	fs.Var(&upgradeVerifyCmd, "upgrade-verify-cmd",
		"one argument of the command that verifies an -upgrade-image image's signature before anything runs it "+
			"-- repeat for every argument, e.g. -upgrade-verify-cmd=cosign -upgrade-verify-cmd=verify "+
			"-upgrade-verify-cmd=--key=/etc/grain/cosign.pub -upgrade-verify-cmd=--insecure-ignore-tlog=true "+
			"(docs/image-signing.md). The image is appended (by digest on a host, by tag in a cluster, where the "+
			"command must print cosign's JSON); a non-zero exit refuses the upgrade. Omitted, upgrades go ahead "+
			"unverified and say so.")

	// How the UI's "reboot host" button reboots (pkg/ui/host.go). The
	// default is the `sudo systemctl reboot` this always ran, which is
	// right for a daemon running directly on the host under systemd and
	// impossible for one running in a container, where there is no host
	// systemd to talk to -- scripts/setup.sh points this at a file
	// touch instead, watched by a host-side path unit. See rebootHost.
	var rebootCmd stringSliceFlag
	fs.Var(&rebootCmd, "reboot-cmd",
		"one argument of the command the UI's reboot-host button runs -- repeat for every argument. "+
			"Defaults to `sudo systemctl reboot`.")

	// Its two neighbours (grain/task-40), each the same shape and each
	// its own flag because each is its own host-side command: "shut this
	// machine down" and "restart the grain service alone". Both default
	// the same way -reboot-cmd does -- to the systemctl a daemon running
	// directly on the host under systemd would run -- and setup.sh points
	// both at control-directory touches for the container deployment,
	// where no systemctl in here reaches a systemd that matters.
	var shutdownCmd stringSliceFlag
	fs.Var(&shutdownCmd, "shutdown-cmd",
		"one argument of the command the UI's shut-down-host button runs -- repeat for every argument. "+
			"Defaults to `sudo systemctl poweroff`.")
	var restartCmd stringSliceFlag
	fs.Var(&restartCmd, "restart-cmd",
		"one argument of the command the UI's restart-grain button runs to restart this daemon alone, "+
			"without rebooting the host -- repeat for every argument. Defaults to "+
			"`sudo systemctl restart grain-daemon.service`.")

	// grain's own CI (pkg/ci), and the switch that turns the whole of it
	// off. Which repositories it runs for is each repo's CI mode
	// (model.RepoUsesGrainCI; the grain-ci capability follows from it
	// since 57939261f) -- this is the operator's override above it, for a
	// deployment that wants no CI engine at all without having to go and
	// switch every repo's CI mode back. Off with it: ui.Config.CI is nil, POST /api/ci/runs is
	// a 404 and GET /api/ci/runs reports `enabled: false`, which is the
	// state every deployment was in before this flag existed.
	ciEnabled := fs.Bool("ci", true,
		"run grain's own CI: the .grain/workflows a repository declares, in this deployment's own sandboxes, "+
			"for the repos whose CI mode is \"Use Grain CI\" (Settings -> Repos), which also grants their tasks the "+
			grainci.CapabilityName+" capability. -ci=false turns the engine off entirely, so no repository's "+
			"workflows run however its CI mode is set, and the CI API reports that this deployment has none. "+
			"A job's sandbox is built the way a run's is and given no credential beyond a read-only checkout of "+
			"the one repository under test (pkg/ci, \"What a workflow file is trusted with\").")

	// The CI job cache's bounds (ci.DirCacheOptions). Seeds, like the CI
	// history's four below and unlike what they were until
	// grain/task-317: each is stored on the config row the first time a
	// deployment starts and read out of it from then on
	// (model.Config.CICacheMB), so an operator moves a ceiling in
	// Settings rather than in a systemd unit, and a running daemon adopts
	// it without a restart.
	//
	// Each default is the ci package's own constant, so `grain daemon
	// -h` prints the number in force rather than "0 means the default",
	// and 0 still means the default for a unit file that passes one
	// explicitly.
	//
	// -ci-cache-max-entry-bytes is the one in this group that is still
	// only a flag: it bounds what a single result is worth keeping rather
	// than what the directory may be, and the Storage section shows no
	// reading against it (ci.CacheBounds.MaxEntryBytes).
	// The group's shared explanation is carried by -ci-cache-max-bytes,
	// the one an operator reaches for first, and the other three point at
	// it: repeating a paragraph four times over in `grain daemon -h`
	// makes the flag list harder to read, not easier.
	const ciCacheSeeBytes = " See -ci-cache-max-bytes for what this group is and is not about."
	ciCacheMaxBytes := fs.Int64("ci-cache-max-bytes", ci.DefaultCacheMaxBytes,
		// The one backquoted word in this string is the argument's name:
		// flag.PrintDefaults takes the first it finds and prints it after
		// the flag, so quoting a command for a reader further down would
		// render as "-ci-cache-max-bytes grain ci cache".
		"ceiling on the whole CI job cache directory under -data-dir, in `bytes` -- the oldest entries are swept "+
			"until it fits (0 uses grain's own default, "+strconv.FormatInt(ci.DefaultCacheMaxBytes>>20, 10)+
			"MB). Lower it on a deployment whose data disk is small, raise it on one with room, where a bigger "+
			"cache is simply more jobs that do not run twice. Nothing in this group can change an answer: a "+
			"cache key is a promise that the work is identical, so every bound here trades disk against how "+
			"often a job is re-run and no value of one can make CI report wrongly. Stored on this deployment's "+
			"config row the first time it starts; change it in Settings (or 'grain settings') after that, where "+
			"it binds the next sweep rather than the next restart -- 'grain ci cache' reports what is held "+
			"against it, and 'grain ci cache clear' empties it now")
	ciCacheMaxAge := fs.Duration("ci-cache-max-age", ci.DefaultCacheMaxAge,
		"how long an unasked-for CI job result is kept before the cache's sweep drops it; 0 uses grain's own "+
			"default, "+ci.DefaultCacheMaxAge.String()+"."+ciCacheSeeBytes)
	ciCacheMaxEntries := fs.Int("ci-cache-max-entries", ci.DefaultCacheMaxEntries,
		"ceiling on how many entries the CI job cache holds, alongside -ci-cache-max-bytes: the entries are one "+
			"flat directory and a hit is one open() in it, so small results would otherwise reach six figures "+
			"long before they reached the byte ceiling. 0 uses grain's own default, "+
			strconv.Itoa(ci.DefaultCacheMaxEntries)+"."+ciCacheSeeBytes)
	ciCacheMaxEntryBytes := fs.Int64("ci-cache-max-entry-bytes", ci.DefaultCacheMaxEntryBytes,
		"largest single CI job result worth keeping, in bytes; a result past it is declined and logged rather "+
			"than stored, and its job runs again next time. 0 uses grain's own default, "+
			strconv.FormatInt(ci.DefaultCacheMaxEntryBytes>>20, 10)+"MB, which is a job of a dozen or so "+
			"maximally noisy steps."+ciCacheSeeBytes)

	// The CI artifact store's bounds (ci.DirArtifactsOptions), the same
	// group in the same shape and seeded the same way. What it holds is
	// what a failing job declared worth keeping (pkg/ci's artifacts.go)
	// -- a Playwright trace, a core file, a build log too big for a
	// step's output -- and dropping one costs the re-run that would
	// produce it again, never an answer.
	const ciArtifactSeeBytes = " See -ci-artifact-max-bytes for what this group holds."
	ciArtifactMaxBytes := fs.Int64("ci-artifact-max-bytes", ci.DefaultArtifactMaxBytes,
		"ceiling on the whole CI artifact directory under -data-dir, in `bytes` -- the oldest runs' artifacts "+
			"are swept whole until it fits (0 uses grain's own default, "+
			strconv.FormatInt(ci.DefaultArtifactMaxBytes>>20, 10)+"MB). A job that does not succeed and "+
			"declares 'artifacts:' has those paths copied out of its sandbox before the sandbox is destroyed, "+
			"which is the only way a file survives a grain CI job at all; nothing here can change a verdict, so "+
			"every bound in this group trades disk against how often somebody has to reproduce a failure by "+
			"hand. Stored on the config row at first start and edited in Settings from then on, like the "+
			"-ci-cache-max-* group beside it")
	ciArtifactMaxAge := fs.Duration("ci-artifact-max-age", ci.DefaultArtifactMaxAge,
		"how long a run's CI artifacts are kept before the sweep drops them; 0 uses grain's own default, "+
			ci.DefaultArtifactMaxAge.String()+", which is about how long a red run stays worth investigating."+
			ciArtifactSeeBytes)
	ciArtifactMaxRunBytes := fs.Int64("ci-artifact-max-run-bytes", ci.DefaultArtifactMaxRunBytes,
		"ceiling on what one CI run's artifacts may add up to, in bytes: one push of a workflow whose dozen "+
			"jobs all fail and all keep a trace is the shape this bounds. 0 uses grain's own default, "+
			strconv.FormatInt(ci.DefaultArtifactMaxRunBytes>>20, 10)+"MB."+ciArtifactSeeBytes)
	ciArtifactMaxEntryBytes := fs.Int64("ci-artifact-max-entry-bytes", ci.DefaultArtifactMaxArtifactBytes,
		"largest single CI artifact worth keeping, in bytes; one past it is refused inside the job's own "+
			"sandbox -- so it is never copied out -- and the run records how big it was and that it was not "+
			"kept. 0 uses grain's own default, "+
			strconv.FormatInt(ci.DefaultArtifactMaxArtifactBytes>>20, 10)+"MB."+ciArtifactSeeBytes)

	// How long this deployment's own CI history lives
	// (ci.StoredRunRetention). Unlike the two groups above, these four
	// are *seeds*: they are stored on the config row the first time a
	// deployment starts and read out of it from then on
	// (model.Config.CIRunHistory), so an operator changes them in
	// Settings rather than in a unit file, and a running daemon adopts
	// the change without a restart. What they bound is the database --
	// and, through it, the state repository the tables are exported into
	// -- rather than a directory on disk, which is why they are settings
	// where the cache and the artifacts are flags (grain/task-269).
	const ciHistorySee = " Stored on this deployment's config row the first time it starts; change it in " +
		"Settings (or 'grain settings') after that, where it takes effect without a restart."
	ciRunHistory := fs.Int("ci-run-history", 0,
		"how many concluded grain CI `runs` keep their record -- the id, ref, commit and every job's and "+
			"step's verdict, which is what \"was this job red last week too?\" is answered from and is the "+
			"cheap half of a run. 0 uses grain's own default, "+strconv.Itoa(ci.DefaultStoredRunHistory)+
			", which is a fortnight of a deployment starting a run every few minutes; a negative number keeps "+
			"every run forever."+ciHistorySee)
	ciStepOutputMB := fs.Int("ci-step-output-mb", 0,
		"ceiling on the step output grain CI keeps across its whole history, in `MiB` -- whole runs' output is "+
			"swept, oldest first, until what is left fits, and the runs themselves stay where they are. This "+
			"is the expensive half and the only one that makes a red run diagnosable rather than merely "+
			"countable, so it is bounded by disk rather than by a count of runs: what one run prints is "+
			"between nothing and the -ci-run-output-kb cap. 0 uses grain's own default, "+
			strconv.FormatInt(ci.DefaultStoredRunOutputBytes>>20, 10)+"MiB, which is three or four days of a "+
			"deployment starting 300 runs a day; a negative number keeps every byte forever."+ciHistorySee)
	ciStepOutputDays := fs.Int("ci-step-output-days", 0,
		"how many `days` a grain CI run's step output may live, whatever room there is under "+
			"-ci-step-output-mb. 0 uses grain's own default, "+
			strconv.Itoa(int(ci.DefaultStoredRunOutputAge/(24*time.Hour)))+
			" days; a negative number bounds output by size alone."+ciHistorySee)
	ciRunOutputKB := fs.Int("ci-run-output-kb", 0,
		"largest amount of step output one grain CI run may store, in `KiB`, spent on the steps a reader opens "+
			"first: a run past it keeps the failing steps' logs and a note in place of the rest, and a run "+
			"larger than the whole -ci-step-output-mb ceiling has its output declined rather than sweeping "+
			"every older run's away. 0 uses grain's own default, "+
			strconv.FormatInt(ci.MaxStoredRunOutput>>10, 10)+"KiB."+ciHistorySee)

	// Every deployment names its sandbox backend: -container-sandboxes,
	// -granule-sandboxes, -k8s-sandboxes, -host-sandboxes, or
	// -disable-native-sandboxes for one whose sandboxes are all remote.
	disableNativeSandboxes := fs.Bool("disable-native-sandboxes", false,
		"disable native sandboxes on this host -- in this mode only remote execution sandboxes can exist")
	nativeSandboxes := fs.Bool("native-sandboxes", true,
		"enable native sandboxes on this host; set to false to disable native sandboxes")
	// A flag rather than a setting, unlike the switch that decides
	// whether sandboxes use the cache at all (Settings' own "Package
	// cache"): this is how much of *this host's* disk the daemon may
	// spend, which is the same kind of fact as -data-dir and belongs
	// with the deployment rather than in a row a UI edits.
	packageCacheGB := fs.Int("package-cache-size-gb", int(pkgcache.DefaultMaxBytes>>30),
		"how much disk, in GiB, this deployment's package cache may use before it starts evicting the "+
			"packages it fetched longest ago. 0 leaves it at the default")
	packageCacheDir := fs.String("package-cache-dir", "",
		"root directory for the package cache -- defaults to <sandbox-dir>/cache/packages when -sandbox-dir is set, and <data-dir>/cache/packages otherwise")
	// Where the cache fills itself from, for the installation that
	// already has a mirror. Flags for the same reason -package-cache-dir
	// and -package-cache-size-gb are, and not for the reason the switch
	// beside them is a stored setting: which mirror this host can reach
	// is a fact about the network this deployment was installed on --
	// the same kind of fact as -data-dir or -container-git-proxy-host --
	// rather than a decision anybody makes per task. They are also read
	// once, when the cache's listeners come up: pkgcache.Server takes
	// its upstreams at construction and has no live path, so a row in
	// Settings would be a control that appears to work and takes effect
	// on the next restart, which is worse than a flag that says what it
	// is.
	//
	// Nothing about a *sandbox* changes when one of these is set. A
	// sandbox goes on resolving and asking for deb.debian.org,
	// pypi.org and registry-1.docker.io, and the files that point it
	// here (pkgcache.SandboxFiles) name those same hosts; this is only
	// where the cache itself goes on a miss.
	var packageCacheAptUpstream stringSliceFlag
	fs.Var(&packageCacheAptUpstream, "package-cache-apt-upstream",
		"fetch one intercepted archive from a mirror instead of from itself, as \"archive-host=base-url\" "+
			"(deb.debian.org=http://mirror.internal/debian) -- repeat for each. The host is one this cache "+
			"already serves, and is what sandboxes go on asking for; the URL is what this service fetches "+
			"from, with the request's own path appended. An archive not named here is fetched from itself")
	packageCachePyPIIndex := fs.String("package-cache-pypi-index", "",
		"the PyPI simple index this cache proxies, for a deployment running a mirror of its own "+
			"(http://mirror.internal/pypi/simple). Defaults to https://pypi.org/simple")
	packageCacheNPMRegistry := fs.String("package-cache-npm-registry", "",
		"the npm registry this cache proxies, for a deployment running a mirror of its own "+
			"(http://mirror.internal/npm). Defaults to https://registry.npmjs.org")
	packageCacheGoProxy := fs.String("package-cache-go-proxy", "",
		"the Go module proxy this cache forwards to, for a deployment running an Athens of its own "+
			"(http://athens.internal). Defaults to https://proxy.golang.org")
	packageCacheDockerRegistry := fs.String("package-cache-docker-registry", "",
		"the registry this cache mirrors for Docker Hub, for a deployment running a pull-through cache of "+
			"its own (https://registry.internal). Defaults to https://registry-1.docker.io")
	packageCacheGHCRRegistry := fs.String("package-cache-ghcr-registry", "",
		"the registry this cache mirrors for GitHub Container Registry, for a deployment running a "+
			"pull-through cache of its own (https://registry.internal/ghcr). Defaults to https://ghcr.io")
	packageCacheGCRRegistry := fs.String("package-cache-gcr-registry", "",
		"the registry this cache mirrors for Google Container Registry, for a deployment running a "+
			"pull-through cache of its own (https://registry.internal/gcr). Defaults to https://gcr.io")
	actionsCacheDir := fs.String("actions-cache-dir", "",
		"root directory for the GitHub Actions cache -- defaults to <sandbox-dir>/cache/actions when -sandbox-dir is set, and <data-dir>/cache/actions otherwise")
	// The other cache on this host's disk. A seed rather than the last
	// word, unlike the one above: both halves of this -- the size, and
	// whether CI jobs are pointed at the cache at all -- are stored
	// settings now (model.Config.ActionsCacheSizeGB and
	// ActionsCacheEnabled, grain/task-357), so this is what a database
	// with no configuration in it is seeded from and nothing more.
	//
	// 0 still means "this deployment wants no Actions cache", and on a
	// first boot it seeds the switch off rather than the size to zero:
	// what an operator saying it is deciding is not a disk budget but
	// whether one branch's CI may read what another branch's CI wrote
	// (pkg/actionscache's package doc says what that grants). On a
	// deployment that already has a stored configuration it decides
	// nothing, and the switch in Settings is the only thing that does.
	actionsCacheGB := fs.Int("actions-cache-size-gb", int(actionscache.DefaultMaxBytes>>30),
		"how much disk, in GiB, this deployment's GitHub Actions cache may use before it starts evicting "+
			"the entries nothing has read for longest. It is what makes a `cache-from: type=gha` build warm "+
			"across CI runs, and every branch of a repository shares one. 0 seeds a deployment that keeps "+
			"no such cache. Seeds a fresh database only: the stored setting (Settings -> Sandbox, "+
			"'Actions cache') wins from then on")
	sandboxPortBase := fs.Int("sandbox-port-base", DefaultSandboxPortBase,
		"first port of the block every sandbox-facing service binds in: the git proxy, the model proxies, "+
			"the package cache and the Actions cache each take a reserved offset within it "+
			"(sandboxnet.go's own sandboxPortOffsets). It is one aligned range of "+
			"256 ports, so what has to reach these services -- a firewall rule, a security group, a VPN "+
			"ACL -- is one rule that a seventh service does not change. Must be a multiple of 256, and "+
			"should stay below the ephemeral range this host allocates from "+
			"(/proc/sys/net/ipv4/ip_local_port_range, 32768 by default) so the block cannot collide with "+
			"this daemon's own outbound connections. 0 restores the behaviour before the block existed: "+
			"every service on whatever ephemeral port the kernel gives it, renumbered on every restart -- "+
			"which is why a sandbox that outlives a restart holds addresses that have moved. Not used by "+
			"the package cache's interception listeners or its resolver, whose ports (443, 80, 53) are "+
			"fixed by their protocols.")
	containerSandboxes := fs.Bool("container-sandboxes", false, "dispatch each run into its own local Docker container")
	hostSandboxes := fs.Bool("host-sandboxes", false, daemon.HostSandboxesUsage)
	allowUnisolatedHostSandboxes := fs.Bool("allow-unisolated-host-sandboxes", false, daemon.AllowUnisolatedHostSandboxesUsage)
	var hostSandboxEnv stringList
	fs.Var(&hostSandboxEnv, "host-sandbox-env", daemon.HostSandboxEnvUsage)
	containerImage := fs.String("container-sandbox-image", defaultK8sSandboxImage, "image used by local container sandboxes")
	containerGitProxyHost := fs.String("container-git-proxy-host", "", "host address grain services are reachable at from local sandbox containers")
	containerNetwork := fs.String("container-sandbox-network", "",
		"Docker network local sandbox containers join. Empty uses "+orchestrator.DefaultContainerNetwork+
			", a bridge grain creates with inter-container traffic disabled; any other name must already exist "+
			"(\"bridge\" is Docker's shared default network)")
	containerCapAdd := fs.String("container-sandbox-cap-add", "",
		"comma-separated Linux capabilities handed back to local sandbox containers, which otherwise run with "+
			"every capability dropped and no-new-privileges set; the stock sandbox image needs none")
	granuleSandboxes := fs.Bool("granule-sandboxes", false,
		"dispatch onto real bwsalmon/granule-managed VMs (one per run, named orchestrator.VMNamePrefix+<run id>) "+
			"over SSH, instead of local host directories. This was -granule-vm-name-prefix, whose value both opted "+
			"in and named the VMs; the name is a constant now, since a VM name has 11 bytes to live in and a run "+
			"id needs nine of them, leaving nothing an operator could usefully choose -- see "+
			"orchestrator.VMNamePrefix")
	runHostImage := fs.String("run-host-image", "",
		"drive each claude run in a run host of this image: a container that is a sibling of the daemon, "+
			"not a child of it, writing to a spool the daemon tails (pkg/runhost, docs/non-disruptive-upgrade.md). "+
			"Normally the deployment image itself (\"self\" under -k8s-sandboxes reads it off this Deployment). Empty runs every agent CLI as a child of the daemon, as before. Not with -container-sandboxes: a run host cannot reach a container sandbox")
	runHostCredentialDir := fs.String("run-host-credential-dir", "",
		"the shared credential directory every run host mounts read-write, as a directory and at the same path "+
			"(only used with -run-host-image)")
	runHostDir := fs.String("run-host-dir", "", "where run hosts' spools and staging live, seen at the same path "+
		"by the daemon and every run host (default <data-dir>/runhosts; under -k8s-run-host-rwx, on that volume)")
	runHostDaemonURL := fs.String("run-host-daemon-url", "", "the API address a run host's mcpserver is given, "+
		"when not this process's own: a run pod needs one that outlives the daemon pod, e.g. http://grain.grain.svc")
	var k8sRunHostVolumes stringList
	fs.Var(&k8sRunHostVolumes, "k8s-run-host-volume", "CLAIM:PATH, repeatable: a claim this pod mounts at PATH; "+
		"run pods get the spool, staging and credential directories as subPaths of these, at the same paths")
	k8sRunHostRWX := fs.Bool("k8s-run-host-rwx", false, "the -k8s-run-host-volume claims are ReadWriteMany, so run "+
		"pods may use any node; off, they are ReadWriteOnce and run pods are pinned to this pod's node")
	k8sRunHostSA := fs.String("k8s-run-host-service-account", "grain", "the service account run pods run as")
	var localScripts stringList
	fs.Var(&localScripts, "local-scripts", "NAME=/absolute/path/local-scripts.yaml, repeatable: a host-managed "+
		"local-scripts declaration a task holding local-scripts:NAME is served (docs/local-scripts.md). "+
		"Every other declaration is read from the state repository's local-scripts/NAME/local-scripts.yaml")
	granuleStateDir := fs.String("granule-state-dir", granule.DefaultStateDir,
		"granule's VM state directory (only used with -granule-sandboxes)")
	granuleCommand := fs.String("granule-command", "",
		"how to invoke granule, when it is not a `granule` on this machine's PATH: the whole command, "+
			"space-separated, with granule's own arguments appended to it. granule builds only for Linux "+
			"(its netshim is netlink, tc and tuntap), so a grain running on macOS has no granule to exec and "+
			"reaches one in a local Linux VM instead -- "+
			"-granule-command=\"limactl shell grain-sbx -- granule\", or an ssh, or a docker exec. "+
			"Setting it also moves granule's state directory to that machine, which is why grain then asks "+
			"`granule vm list` what VMs exist rather than reading -granule-state-dir itself "+
			"(granule.Command.Exists). Left empty, granule is exec'd here, which is every deployment that "+
			"runs grain and granule on one host.")
	granuleSSHUser := fs.String("granule-ssh-user", "",
		"username to SSH into each granule VM as (required with -granule-sandboxes)")
	granuleExecKey := fs.String("granule-exec-key", "",
		"path, *inside the VM's container*, of the private key `granule exec` authenticates to the guest with. "+
			"Optional, and normally left unset: `granule run` generates a keypair for each guest it boots and "+
			"hands the guest the public half on its kernel command line, so the default path already holds a "+
			"key that guest authorizes (bwsalmon/granule's internal/guestkey). Set this only for a custom guest "+
			"image that authorizes a key of its own instead -- e.g. /images/granule-exec-key for a key placed in "+
			"the directory -granule-create-arg's own -images-hostpath mounts read-only at /images.")
	granuleWorkspace := fs.String("granule-workspace", "",
		"working directory run_command/read_file/edit_file/write_file operate in on each granule VM (required with -granule-sandboxes)")
	granuleReadyTimeout := fs.Duration("granule-ready-timeout", 0,
		"how long Acquire waits for a freshly created granule VM's guest to answer and carry a default "+
			"route before giving the sandbox up; 0 uses the built-in two minutes. Worth raising wherever a "+
			"guest boots slowly for reasons that are not grain's: nested virtualisation is the one measured "+
			"here, where kernel and initramfs alone took 109s on an arm64 Mac under Lima and the default "+
			"route landed at ~126s, two seconds past the built-in deadline, so every acquire failed with "+
			"\"vsock port 1024 is not being listened on\" -- which reads as a networking fault and is a "+
			"budget one. The same deadline has bitten before for a different slow boot (grain/task-593: 254 "+
			"failed creations in one window), which is why it is a flag now rather than a constant.")
	var granuleCreateArgs stringSliceFlag
	fs.Var(&granuleCreateArgs, "granule-create-arg",
		"one argument appended verbatim to the `granule vm create <name> -state-dir <dir>` that builds a run's "+
			"VM -- repeat for every flag and value bwsalmon/granule's own `granule vm create -h` calls for "+
			"beyond a name and -state-dir (disk mode, guest user, resolver, ...), e.g. "+
			"-granule-create-arg=-disk-mode=overlay -granule-create-arg=-guest-user -granule-create-arg=debian, "+
			"which is what scripts/setup.sh passes for terraform/gcp. The guest image itself -- the repository's "+
			"Granulefile, built and published by scripts/release-images.sh (see scripts/granule/README.md) -- "+
			"comes from -sandbox-image or this binary's own stamp rather than from here. Only used with "+
			"-granule-sandboxes. Under -granule-net nat, prefer -granule-base-ip/-granule-base-port over "+
			"putting -ip/-port here: they are appended last, so they win over this list.")
	sandboxImage := fs.String("sandbox-image", "",
		"the sandbox container image every granule VM is created from, overriding the reference stamped into "+
			"this build at build time (`grain sandbox-image`). Left empty (the default), each start resolves "+
			"that stamp, so an upgrade -- which pulls the new grain image and the sandbox image that grain "+
			"names -- moves both halves together. Set it only to run something else on purpose: a mirror, a "+
			"private copy, or a guest held at an older commit. This is where such a pin belongs; a "+
			"-granule-create-arg -kontur-image naming another tag of the stamp's own repository is read as the "+
			"install-time value an older scripts/setup.sh wrote unconditionally, and the stamp is used instead "+
			"(chooseSandboxImage). Only used with -granule-sandboxes.")
	granuleNet := fs.String("granule-net", granule.NetModeFlat,
		"ignored, and removed in a later release. The granule this build vendors has only the former "+
			"\"flat\" behaviour -- its own -net says \"deprecated: only the former flat behaviour exists "+
			"now, and it is the default\" -- so there is nothing left to select. A start that is passed "+
			"this says so and carries on.")
	granuleBaseIP := fs.String("granule-base-ip", "",
		"ignored, and removed in a later release. A granule guest takes over the address the container "+
			"runtime assigned its container, and granule's own -ip is \"deprecated and ignored\"; there "+
			"has been no way to assign one from here for some time. Assigning a sandbox's address is "+
			"coming back by a different route -- pkg/sandboxaddr, reaching the VM as a docker run option "+
			"on a user-defined network -- which is not this flag.")
	granuleBasePort := fs.Int("granule-base-port", 0,
		"ignored, and removed in a later release. granule publishes ports on the sandbox itself "+
			"(-docker-run-opt -p, or -publish-declared) rather than forwarding one per VM, and its own "+
			"-port is \"deprecated and ignored\".")
	sandboxCIDR := fs.String("granule-sandbox-cidr", "",
		"address pool every sandbox VM is given an address out of, as a CIDR -- for example "+
			"10.128.0.0/16. Empty (the default) is how every deployment has run until now: grain names no "+
			"address, docker assigns one from whichever network the VM container lands on, and grain reads "+
			"it back afterwards. Setting this makes the address grain's own choice, which is what lets the "+
			"host isolation rules be written without first asking docker what happened, lets a sandbox be "+
			"identified by where it is as well as by the token it carries, and stops two sandbox hosts "+
			"handing out the same 172.17.0.0/16. Addresses are allocated in blocks of "+
			"sandboxaddr.FamilySize, one block per sandbox and everything that sandbox goes on to create, "+
			"so a whole family is one aligned CIDR and so destroying a parent frees its children with it. "+
			"Requires -granule-sandboxes, and a pool big enough for the sandboxes this deployment runs at "+
			"once: a /16 holds 2047 families of 32.")
	sandboxNetwork := fs.String("granule-sandbox-network", "grain-sandboxes",
		"name of the user-defined docker network those addresses live on, created with "+
			"-granule-sandbox-cidr as its subnet if it is not already there. It has to be user-defined and "+
			"it has to be this deployment's: docker honours \"--ip\" only on a network it was given a "+
			"subnet for and never on the default bridge, so there is no arrangement in which grain assigns "+
			"an address on 172.17.0.0/16. Only used with -granule-sandbox-cidr.")
	granuleGitProxyHost := fs.String("granule-git-proxy-host", "",
		"host (no port) this daemon's git proxy is reachable at from inside a granule VM's own guest, in place "+
			"of the loopback address the proxy binds to by default -- required with -granule-sandboxes. A "+
			"granule VM (GranuleConfig.createArgs always builds one against -backend docker) runs its guest in its "+
			"own network namespace behind netshim's NAT (third_party/granule/internal/netshim), with its own "+
			"127.0.0.1 that never reaches this process's -- so a clone against the default 127.0.0.1 proxy URL "+
			"fails the moment it leaves the guest (bwsalmon/agents#567: \"Failed to connect to 127.0.0.1 ... "+
			"Couldn't connect to server\"). Setting this makes startGitProxy bind on every interface instead of "+
			"just loopback, and advertise this host to every run's sandbox in loopback's place -- typically the "+
			"docker bridge gateway address the guest's own outbound NAT routes through to reach this host (see "+
			"netshim's masqueradeExprs); run `docker network inspect bridge` (or whichever network the granule VM "+
			"containers join) and read \"Gateway\" if unsure -- commonly 172.17.0.1.")
	// Backwards-compatible aliases for legacy -kontur-* flags (formerly used before
	// the kontur -> granule rename), so that existing deployments whose systemd
	// units still pass -kontur-* flags continue working across upgrades.
	konturSandboxes := fs.Bool("kontur-sandboxes", false,
		"former name of -granule-sandboxes, still accepted so existing systemd unit files keep working")
	konturStateDir := fs.String("kontur-state-dir", "",
		"former name of -granule-state-dir, still accepted so existing systemd unit files keep working")
	konturSSHUser := fs.String("kontur-ssh-user", "",
		"former name of -granule-ssh-user, still accepted so existing systemd unit files keep working")
	konturExecKey := fs.String("kontur-exec-key", "",
		"former name of -granule-exec-key, still accepted so existing systemd unit files keep working")
	konturWorkspace := fs.String("kontur-workspace", "",
		"former name of -granule-workspace, still accepted so existing systemd unit files keep working")
	var konturCreateArgs stringSliceFlag
	fs.Var(&konturCreateArgs, "kontur-create-arg",
		"former name of -granule-create-arg, still accepted so existing systemd unit files keep working")
	konturNet := fs.String("kontur-net", "",
		"former name of -granule-net, still accepted so existing systemd unit files keep working")
	konturBasePort := fs.Int("kontur-base-port", 0,
		"former name of -granule-base-port, still accepted so existing systemd unit files keep working")
	konturGitProxyHost := fs.String("kontur-git-proxy-host", "",
		"former name of -granule-git-proxy-host, still accepted so existing systemd unit files keep working")
	// The Kubernetes backend (grain/task-358). A third sandbox backend
	// alongside the two above, and the only one that needs no host
	// adapter at all: the cluster is the host adapter. Everything a
	// granule deployment configures about a *machine* -- a state
	// directory, a guest image, a VM shape expressed in granule flags,
	// an SSH account and key -- is either the cluster's own concern here
	// or expressed as a pod spec, so this block is shorter than the
	// granule one despite doing the same job.
	k8sSandboxes := fs.Bool("k8s-sandboxes", false,
		"dispatch into Kubernetes pods (one per run, named orchestrator.PodNamePrefix+<run id>) reached with "+
			"`kubectl exec`, instead of local containers or granule VMs. Credentials are kubectl's own: "+
			"in-cluster that is this pod's ServiceAccount token, which deploy/kubernetes/ binds the pod and "+
			"pod/exec permissions this needs to. A pod shares its node's kernel, so this is weaker isolation "+
			"than -granule-sandboxes")
	k8sNamespace := fs.String("k8s-namespace", k8s.DefaultNamespace,
		"namespace sandbox pods are created in (only used with -k8s-sandboxes). Two deployments sharing a "+
			"cluster must not share this: the sweep for pods a dead process or a failed delete left behind "+
			"(K8sSandboxes.SweepOrphans, at startup and on every reconcile tick) deletes every grain sandbox "+
			"pod in the namespace it is not itself holding"+seedOnly)
	k8sKubectl := fs.String("k8s-kubectl", "",
		"kubectl binary pkg/k8s and the exec transport run (only used with -k8s-sandboxes); empty finds "+
			"\"kubectl\" on PATH"+seedOnly)
	k8sKubeconfig := fs.String("k8s-kubeconfig", "",
		"kubeconfig path passed to kubectl for -k8s-sandboxes and -k8s-granule-sandboxes; when empty and "+
			"KUBECONFIG is unset, defaults to <data-dir>/kubeconfig or /var/lib/grain/kubeconfig if present")
	k8sImage := fs.String("k8s-sandbox-image", defaultK8sSandboxImage,
		"container image every sandbox pod runs; defaults to the image stamped into this build. It is the whole of what "+
			"a dispatched agent finds when it lands. It needs the agent framework's own CLI, git, and a shell; "+
			"Dockerfile.k8s-sandbox in this repo builds the published one"+seedOnly)
	k8sServiceAccount := fs.String("k8s-sandbox-service-account", "",
		"ServiceAccount for sandbox pods (only used with -k8s-sandboxes). Normally left empty: nothing in a "+
			"sandbox should be talking to the API server, and no token is mounted into one regardless"+seedOnly)
	k8sPriorityInteractive := fs.String("k8s-priority-class-interactive", model.DefaultK8sPriorityClassInteractive,
		"PriorityClass for the sandbox pods of interactive tasks (Director sessions, chats); empty sets none. "+
			"deploy/kubernetes/ defines it, and the daemon creates grain's own classes when they are missing -- a name the cluster still has no PriorityClass for is left off the pods, with an alert"+seedOnly)
	k8sPriorityAgent := fs.String("k8s-priority-class-agent", model.DefaultK8sPriorityClassAgent,
		"PriorityClass for the sandbox pods of ordinary agent runs and persistent sandboxes; empty sets none"+seedOnly)
	k8sPriorityCI := fs.String("k8s-priority-class-ci", model.DefaultK8sPriorityClassCI,
		"PriorityClass for CI jobs' sandbox pods; empty sets none. The lowest of the three, so on a full cluster "+
			"these are what an interactive session preempts, and a preempted job is retried"+seedOnly)
	k8sWorkspace := fs.String("k8s-workspace", "",
		"working directory run_command/read_file/edit_file/write_file operate in inside each sandbox pod "+
			"(required with -k8s-sandboxes)"+seedOnly)
	var k8sNodeSelector stringSliceFlag
	fs.Var(&k8sNodeSelector, "k8s-node-selector",
		"one \"key=value\" nodeSelector entry for sandbox pods -- repeat for each. Pins sandboxes to a node "+
			"pool sized for build workloads, or one whose nodes offer nested virtualization. Only used with "+
			"-k8s-sandboxes"+seedOnly)
	k8sPrivileged := fs.Bool("k8s-sandbox-privileged", false,
		"run sandbox containers privileged (only used with -k8s-sandboxes). This gives every dispatched agent "+
			"root on the node it lands on; it is here because a sandbox that has to run docker or kind cannot "+
			"work without it, not because it is a reasonable default"+seedOnly)
	k8sNetworkPolicy := fs.Bool("k8s-network-policy", false,
		"assert that this cluster's CNI actually enforces NetworkPolicy objects, and that "+
			"deploy/kubernetes/networkpolicy.yaml is installed. Without it a task asking for no network access "+
			"is refused rather than silently given a networked pod, since a policy object is accepted by every "+
			"cluster and enforced by only some. Only used with -k8s-sandboxes"+seedOnly)
	k8sDeployment := fs.String("k8s-deployment", "grain",
		"name of the Kubernetes Deployment running grain itself, which the Upgrade button repoints at a new "+
			"image (only used with -k8s-sandboxes and -upgrade-image). deploy/kubernetes/grain.yaml calls it "+
			"\"grain\""+seedOnly)
	k8sContainer := fs.String("k8s-container", "grain",
		"name of grain's own container within that Deployment (only used with -k8s-sandboxes and "+
			"-upgrade-image)"+seedOnly)
	k8sGitProxyHost := fs.String("k8s-git-proxy-host", "",
		"address (no port) this daemon's git proxy and model proxies are reachable at from inside a sandbox "+
			"pod, in place of the loopback address the proxy binds to by default -- required with "+
			"-k8s-sandboxes, the same way -granule-git-proxy-host is required with -granule-sandboxes and for the "+
			"same reason: a pod's 127.0.0.1 is its own. In-cluster this is grain's own pod IP, which every "+
			"other pod can route to directly, so it defaults to $GRAIN_POD_IP when that is set -- "+
			"deploy/kubernetes/deployment.yaml fills it from the downward API (status.podIP); see -k8s-sandbox-service"+seedOnly)
	k8sSandboxService := k8sSandboxServiceFlag(fs)
	k8sGranuleSandboxes := fs.Bool("k8s-granule-sandboxes", false,
		"dispatch into privileged Kubernetes pods, each booting a real cloud-hypervisor VM over /dev/kvm "+
			"(one per run), instead of -k8s-sandboxes' plain pods or -granule-sandboxes' single-host VMs. "+
			"Reuses -k8s-namespace/-k8s-kubectl/-k8s-workspace/-k8s-node-selector/-k8s-git-proxy-host, which "+
			"mean the same thing on either Kubernetes backend; see -k8s-granule-image for the one that does "+
			"not. docs/k8s-granule-design.md")
	k8sGranuleImage := fs.String("k8s-granule-image", daemon.DefaultSandboxImage,
		"the packed guest image every pod this backend creates runs: a self-contained image with its own "+
			"guest disk and kernel already baked in -- the shape Granulefile's own build produces, not "+
			"the thin exec target -k8s-sandbox-image names and, just as importantly, not "+
			"\"granule:vendored\" either (grain init's own -granule-runtime-image default, what "+
			"scripts/granule/build-granule-image.sh produces, which has no guest disk and cannot boot at "+
			"all -- confirmed against a real cluster, where a pod running it exits immediately). Required "+
			"with -k8s-granule-sandboxes; no context-free default to pick, since nothing publishes a "+
			"packed guest image under a stable tag yet -- docs/k8s-granule-design.md §4"+seedOnly)
	k8sGranuleRelayAddr := fs.String("k8s-granule-relay-addr", "",
		"host:port of the in-cluster `granule relay` LoadBalancer Service used by an off-cluster controller "+
			"with -k8s-granule-sandboxes (overrides /var/lib/grain/gke-sandboxes/relay.json)")
	sandboxCPUs := fs.Int("sandbox-cpus", 0,
		"deployment-wide default vCPU count for a granule-managed sandbox VM, passed as `granule vm create`'s "+
			"own -cpus (only used with -granule-sandboxes); 0 uses grain's own default, "+
			strconv.Itoa(granule.DefaultCPUs)+". "+
			"Overridable per task from the UI/API (model.Task.SandboxCPUs)"+seedOnly)
	sandboxMemoryMB := fs.Int("sandbox-memory-mb", 0,
		"deployment-wide default guest memory, in MiB, for a granule-managed sandbox VM, passed as `granule vm "+
			"create`'s own -memory-mb (only used with -granule-sandboxes); 0 uses grain's own default, "+
			strconv.Itoa(granule.DefaultMemoryMB)+". Overridable per task from the UI/API "+
			"(model.Task.SandboxMemoryMB)"+seedOnly)
	sandboxDiskGB := fs.Int("sandbox-disk-gb", 0,
		"deployment-wide default root disk size, in GiB, for a granule-managed sandbox VM, passed as `granule "+
			"vm create`'s own -disk-size-mb (only used with -granule-sandboxes); 0 uses grain's own default, "+
			strconv.Itoa(granule.DefaultDiskGB)+" -- every VM is sized explicitly, rather than being left as "+
			"large as the guest image behind it the way it was before. "+
			"Overridable per task from the UI/API (model.Task.SandboxDiskGB)"+seedOnly)
	// A flag this build does not define is reported and ignored rather
	// than being the end of the process; anything else about this line
	// -- a value a defined flag cannot take -- still is, on one line
	// rather than behind the whole flag list (daemon_unknown_flags.go).
	unknownFlags, err := readDaemonFlags(fs, args)
	if err != nil {
		return daemon.Config{}, fs, unknownFlags, err
	}
	if *flagNames {
		return daemon.Config{}, fs, unknownFlags, nil
	}

	passed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { passed[f.Name] = true })
	// Every flag name on this command line, defined or not: this is the
	// unit's ExecStart as this process received it, and the only copy of
	// it anything in here will ever see (config.unitFlags).
	unitFlags := append([]string{}, unknownFlags...)
	fs.Visit(func(f *flag.Flag) { unitFlags = append(unitFlags, "-"+f.Name) })
	slices.Sort(unitFlags)
	if !passed["db-driver"] {
		if env := os.Getenv("GRAIN_DB_DRIVER"); env != "" {
			*dbDriver = env
		} else if passed["postgres-url"] || os.Getenv("GRAIN_POSTGRES_URL") != "" || os.Getenv("DATABASE_URL") != "" {
			*dbDriver = "postgres"
		} else if saved, err := daemon.LoadDatabaseConfig(*dataDir); err != nil {
			return daemon.Config{}, fs, unknownFlags, err
		} else if saved != nil && saved.Driver != "" {
			*dbDriver = saved.Driver
			if !passed["db-dsn"] && saved.DSN != "" {
				*dbDSN = saved.DSN
			}
		}
	}
	if !passed["db-dsn"] {
		if env := os.Getenv("GRAIN_DB_DSN"); env != "" {
			*dbDSN = env
		} else if *postgresURL != "" {
			*dbDSN = *postgresURL
		} else if env := os.Getenv("GRAIN_POSTGRES_URL"); env != "" {
			*dbDSN = env
		} else if env := os.Getenv("DATABASE_URL"); env != "" {
			*dbDSN = env
		}
	}
	if passed["max-concurrent"] {
		if passed["max-workers"] {
			return daemon.Config{}, fs, unknownFlags, errors.New("-max-concurrent is the former name of -max-workers; pass one or the other")
		}
		log.Printf("grain daemon: -max-concurrent is now called -max-workers; using -max-workers=%d", *maxConcurrent)
		*maxWorkers = *maxConcurrent
	}
	if passed["kontur-sandboxes"] {
		if !passed["granule-sandboxes"] {
			*granuleSandboxes = *konturSandboxes
		}
		log.Printf("grain daemon: -kontur-sandboxes is legacy alias for -granule-sandboxes; using -granule-sandboxes=%v", *granuleSandboxes)
	}
	for _, v := range localScripts {
		var check localscripts.Config
		if err := check.ParseHost(v); err != nil {
			return daemon.Config{}, fs, unknownFlags, err
		}
	}
	if passed["kontur-state-dir"] {
		if !passed["granule-state-dir"] {
			*granuleStateDir = *konturStateDir
		}
		log.Printf("grain daemon: -kontur-state-dir is legacy alias for -granule-state-dir; using -granule-state-dir=%s", *granuleStateDir)
	}
	if passed["kontur-ssh-user"] {
		if !passed["granule-ssh-user"] {
			*granuleSSHUser = *konturSSHUser
		}
		log.Printf("grain daemon: -kontur-ssh-user is legacy alias for -granule-ssh-user; using -granule-ssh-user=%s", *granuleSSHUser)
	}
	if passed["kontur-exec-key"] {
		if !passed["granule-exec-key"] {
			*granuleExecKey = *konturExecKey
		}
		log.Printf("grain daemon: -kontur-exec-key is legacy alias for -granule-exec-key; using -granule-exec-key=%s", *granuleExecKey)
	}
	if passed["kontur-workspace"] {
		if !passed["granule-workspace"] {
			*granuleWorkspace = *konturWorkspace
		}
		log.Printf("grain daemon: -kontur-workspace is legacy alias for -granule-workspace; using -granule-workspace=%s", *granuleWorkspace)
	}
	if passed["kontur-create-arg"] {
		granuleCreateArgs = append(konturCreateArgs, granuleCreateArgs...)
		log.Printf("grain daemon: -kontur-create-arg is legacy alias for -granule-create-arg; forwarded %d argument(s)", len(konturCreateArgs))
	}
	if passed["kontur-net"] {
		if !passed["granule-net"] {
			*granuleNet = *konturNet
		}
		log.Printf("grain daemon: -kontur-net is legacy alias for -granule-net; using -granule-net=%s", *granuleNet)
	}
	if passed["kontur-base-port"] {
		if !passed["granule-base-port"] {
			*granuleBasePort = *konturBasePort
		}
		log.Printf("grain daemon: -kontur-base-port is legacy alias for -granule-base-port; using -granule-base-port=%d", *granuleBasePort)
	}
	if passed["kontur-git-proxy-host"] {
		if !passed["granule-git-proxy-host"] {
			*granuleGitProxyHost = *konturGitProxyHost
		}
		log.Printf("grain daemon: -kontur-git-proxy-host is legacy alias for -granule-git-proxy-host; using -granule-git-proxy-host=%s", *granuleGitProxyHost)
	}
	// -granule-net, -granule-base-ip and -granule-base-port describe a
	// granule that no longer exists: the vendored third_party/granule
	// deprecates all three ("only the former flat behaviour exists now, and
	// it is the default"; -ip and -port "deprecated and ignored"), so
	// nothing has reached granule from them for some time. They are
	// accepted and ignored rather than rejected, because a deployment's own
	// systemd unit carries whatever the installer wrote when it was set up
	// and a start that refuses over inert configuration is worse than one
	// that says so -- but they are said loudly, once, because the operator
	// who set them believed they did something.
	//
	// Warned on rather than silently dropped, and warned on only when
	// actually passed, so the line means "this deployment asked for
	// something it is not getting" rather than appearing on every start.
	// They are removed in a later release; see orchestrator.GranuleConfig's
	// NetMode/IP/Port, and pkg/sandboxaddr for how assigning an address is
	// coming back.
	if passed["granule-net"] || passed["kontur-net"] {
		log.Printf("grain daemon: -granule-net=%s is ignored: the granule this build vendors has only "+
			"the former \"flat\" behaviour, and it is the default. Remove the flag; it goes away in a "+
			"later release", *granuleNet)
	}
	if passed["granule-base-ip"] || passed["kontur-base-ip"] {
		log.Printf("grain daemon: -granule-base-ip=%s is ignored: granule assigns a guest the address "+
			"the container runtime gave its container, and its own -ip is deprecated and ignored. "+
			"Remove the flag; it goes away in a later release", *granuleBaseIP)
	}
	if passed["granule-base-port"] || passed["kontur-base-port"] {
		log.Printf("grain daemon: -granule-base-port=%d is ignored: granule publishes ports on the "+
			"sandbox itself rather than forwarding one per VM, and its own -port is deprecated and "+
			"ignored. Remove the flag; it goes away in a later release", *granuleBasePort)
	}

	if err := removedTunnelGatewaySettings(passed); err != nil {
		return daemon.Config{}, fs, unknownFlags, err
	}
	if err := removedRelaySettings(passed, unknownFlags); err != nil {
		return daemon.Config{}, fs, unknownFlags, err
	}

	if *executorAddr == "" && *modelExecAddr != "" {
		*executorAddr = *modelExecAddr
	}
	if *executorTokenFile == "" && *modelExecTokenFile != "" {
		*executorTokenFile = *modelExecTokenFile
	}
	if *executorWait == modelexec.DefaultWait && *modelExecWait > 0 {
		*executorWait = *modelExecWait
	}
	if *executorCertFile == "" && *modelExecCertFile != "" {
		*executorCertFile = *modelExecCertFile
	}
	if *executorKeyFile == "" && *modelExecKeyFile != "" {
		*executorKeyFile = *modelExecKeyFile
	}

	cfg := daemon.Config{
		UnitFlags: unitFlags, UnknownFlags: unknownFlags,
		DataDir: *dataDir, DbDriver: *dbDriver, DbDSN: *dbDSN, SandboxDir: *sandboxDir, DockerRootDir: *dockerRootDir,
		MaxWorkers: *maxWorkers, MaxMergers: *maxMergers, MaxCIJobs: model.DefaultMaxCIJobs,
		Ledger:        &model.Ledger{},
		CpuOvercommit: *cpuOvercommit, MemoryOvercommit: *memoryOvercommit, MemoryReserveMB: *memoryReserveMB,
		DiskOvercommit:         *diskOvercommit,
		RepairReserveSandboxes: *repairReserve,
		DiskFloorShare:         *diskFloorShare, DiskFloorMinMB: *diskFloorMinMB,
		PollInterval:   *pollInterval,
		BackupInterval: *backupInterval, BackupGCSBucket: *backupGCSBucket,
		BackupGoogleDriveFolder: *backupGoogleDriveFolder, BackupGoogleCredentialsFile: *backupGoogleCredentialsFile,
		UiAddr: *uiAddr, UiOpen: *uiOpen, Actor: *actor, DefaultTargetRepo: *defaultTargetRepo, PolicyFile: *policyFile,
		UiAllowedHosts:          splitRepoList(*uiAllowedHosts),
		UiAccessSessionLifetime: *uiAccessSessionLifetime,
		UiIAPAudience:           *uiIAPAudience,
		UiIAPIssuer:             *uiIAPIssuer,
		UiIAPKeysURL:            *uiIAPKeysURL,
		ExecutorAddr:            *executorAddr,
		ExecutorTokenFile:       *executorTokenFile,
		ExecutorWait:            *executorWait,
		ExecutorCertFile:        *executorCertFile,
		ExecutorKeyFile:         *executorKeyFile,
		GeminiAPIKeyFile:        *geminiAPIKeyFile,
		MaxAgentTurns:           *maxAgentTurns,
		AgyPath:                 *agyPath,
		ClaudePath:              *claudePath, ClaudeOAuthTokenFile: *claudeOAuthTokenFile,
		ModelProxyUnrestricted: *modelProxyUnrestricted, ModelProxyMaxConcurrent: *modelProxyMaxConcurrent, ModelProxySlotWait: *modelProxySlotWait,
		CodexPath: *codexPath, OpenAIAPIKeyFile: *openAIAPIKeyFile, CodexAuthFile: *codexAuthFile,
		ClaudeRun: *claudeRun, CodexRun: *codexRun, AgyRun: *agyRun,
		HostdSocket: *hostdSocket,
		GithubHost:  *githubHost, GithubInsecureHTTP: *githubInsecureHTTP,
		CheckpostURL: *checkpostURL, CheckpostExpect: *checkpostExpect,
		GithubWebhookURL: *githubWebhookURL, GithubWebhookAddr: *githubWebhookAddr,
		GcpProject: *gcpProject, GcpServiceAccountEmail: *gcpServiceAccountEmail, GcpServiceAccountCount: *gcpServiceAccountCount,
		IapBackendService: *iapBackendService,
		IapResource:       *iapResource,
		UpgradeSrcDir:     *upgradeSrcDir, UpgradeInstallPath: *upgradeInstallPath, UpgradeRestartCmd: upgradeRestartCmd, UpgradeVerifyCmd: upgradeVerifyCmd,
		UpgradeImage: *upgradeImage, UpgradeImageRefFile: *upgradeImageRefFile,
		UpgradeAllowedBranches: daemon.SplitBranchPatterns(*upgradeAllowedBranches),
		UpgradeDrainWindow:     *upgradeDrainWindow,
		RebootCmd:              rebootCmd,
		ShutdownCmd:            shutdownCmd,
		RestartCmd:             restartCmd,
		Ci:                     *ciEnabled,
		CiCacheMaxAge:          *ciCacheMaxAge,
		CiCacheMaxBytes:        *ciCacheMaxBytes,
		CiCacheMaxEntries:      *ciCacheMaxEntries,
		CiCacheMaxEntryBytes:   *ciCacheMaxEntryBytes,

		CiArtifactMaxAge:        *ciArtifactMaxAge,
		CiArtifactMaxBytes:      *ciArtifactMaxBytes,
		CiArtifactMaxRunBytes:   *ciArtifactMaxRunBytes,
		CiArtifactMaxEntryBytes: *ciArtifactMaxEntryBytes,

		CiRunHistory:     *ciRunHistory,
		CiStepOutputMB:   *ciStepOutputMB,
		CiStepOutputDays: *ciStepOutputDays,
		CiRunOutputKB:    *ciRunOutputKB,

		DisableNativeSandboxes: *disableNativeSandboxes || (passed["native-sandboxes"] && !*nativeSandboxes),
		PackageCacheDir:        *packageCacheDir,
		PackageCacheGB:         *packageCacheGB,

		PackageCacheAptUpstream:    packageCacheAptUpstream,
		PackageCachePyPIIndex:      *packageCachePyPIIndex,
		PackageCacheNPMRegistry:    *packageCacheNPMRegistry,
		PackageCacheGoProxy:        *packageCacheGoProxy,
		PackageCacheDockerRegistry: *packageCacheDockerRegistry,
		PackageCacheGHCRRegistry:   *packageCacheGHCRRegistry,
		PackageCacheGCRRegistry:    *packageCacheGCRRegistry,

		ActionsCacheDir: *actionsCacheDir,
		ActionsCacheGB:  *actionsCacheGB,
		// -actions-cache-size-gb 0 is a deployment saying it wants no
		// Actions cache, which is now the switch rather than the size:
		// this is what a fresh database is seeded with (toModelConfig),
		// and a database that already has a row overrides it like any
		// other seed.
		ActionsCacheEnabled: *actionsCacheGB > 0,
		SandboxCIDR:         *sandboxCIDR,
		SandboxNetwork:      *sandboxNetwork,
		SandboxPortBase:     *sandboxPortBase,
		ContainerSandboxes:  *containerSandboxes, ContainerImage: *containerImage, ContainerGitProxyHost: *containerGitProxyHost,
		HostSandboxes: *hostSandboxes, AllowUnisolatedHostSandboxes: *allowUnisolatedHostSandboxes,
		HostSandboxEnv:   hostSandboxEnv,
		ContainerNetwork: *containerNetwork, ContainerCapAdd: splitRepoList(*containerCapAdd),
		GranuleSandboxes:     *granuleSandboxes,
		GranuleStateDir:      *granuleStateDir,
		LocalScripts:         localScripts,
		RunHostImage:         *runHostImage,
		RunHostCredentialDir: *runHostCredentialDir,
		RunHostDir:           *runHostDir, RunHostDaemonURL: *runHostDaemonURL, K8sRunHostVolumes: k8sRunHostVolumes,
		K8sRunHostRWX: *k8sRunHostRWX, K8sRunHostServiceAccount: *k8sRunHostSA,
		GranuleCommand: daemon.SplitGranuleCommand(*granuleCommand),
		GranuleSSHUser: *granuleSSHUser, GranuleWorkspace: *granuleWorkspace,
		GranuleReadyTimeout: *granuleReadyTimeout,
		GranuleExecKey:      *granuleExecKey,
		GranuleCreateArgs:   granuleCreateArgs, GranuleNet: *granuleNet,
		SandboxImage:  *sandboxImage,
		GranuleBaseIP: *granuleBaseIP, GranuleBasePort: *granuleBasePort,
		GranuleGitProxyHost: *granuleGitProxyHost,
		K8sSandboxes:        *k8sSandboxes,
		K8sNamespace:        *k8sNamespace, K8sKubectl: *k8sKubectl, K8sKubeconfig: *k8sKubeconfig, K8sImage: *k8sImage,
		K8sServiceAccount: *k8sServiceAccount, K8sWorkspace: *k8sWorkspace,
		K8sNodeSelector: k8sNodeSelector, K8sPrivileged: *k8sPrivileged,
		K8sDeployment: *k8sDeployment, K8sContainer: *k8sContainer, K8sSandboxService: *k8sSandboxService,
		K8sPriorityClasses: orchestrator.K8sPriorityClasses{
			Interactive: *k8sPriorityInteractive, Agent: *k8sPriorityAgent, CI: *k8sPriorityCI,
		},
		K8sNetworkPolicy: *k8sNetworkPolicy, K8sGitProxyHost: daemon.K8sGitProxyHostOrPodIP(*k8sGitProxyHost),
		K8sGranuleSandboxes: *k8sGranuleSandboxes, K8sGranuleImage: *k8sGranuleImage, K8sGranuleRelayAddr: *k8sGranuleRelayAddr,
		SandboxCPUs: *sandboxCPUs, SandboxMemoryMB: *sandboxMemoryMB, SandboxDiskGB: *sandboxDiskGB,
	}
	cfg, err = local(cfg, passed)
	return cfg, fs, unknownFlags, err
}

// checkConfig is every "these flags cannot be started with" rule daemon()
// has, over the config they were parsed into rather than over the flag
// pointers themselves -- so the rules are a function a test can call
// instead of an exit code only a subprocess could observe.
//
// What is *not* here is anything about a credential. An agent key is set
// from the UI now and read per dispatch (agentFrameworks), so a daemon
// with none is a daemon whose runs end as setup-failed saying which key
// is missing, and whose UI is up for somebody to paste one into --
// refusing to start over it would take away the screen the fix is made
// on. This checks only what cannot be repaired without a restart anyway:
// a missing directory to keep state in, two sandbox backends asked for
// at once, a granule deployment with no way to reach its VMs, an upgrade
// path half-configured, a concurrency that is not a number of runs, and
// an interval that is not a length of time.
func checkConfig(cfg daemon.Config) error {
	if cfg.DataDir == "" {
		return errors.New("-data-dir is required")
	}
	switch cfg.DbDriver {
	case "", "sqlite":
	case "postgres":
		if cfg.DbDSN == "" && !postgres.IsAvailable() {
			return errors.New("-db-dsn is required when -db-driver is postgres")
		}
	default:
		return fmt.Errorf("-db-driver must be \"sqlite\" or \"postgres\", got %q", cfg.DbDriver)
	}
	if err := daemon.CheckSandboxBackend(cfg, daemon.LocalSandboxes != nil); err != nil {
		return err
	}
	if err := daemon.CheckHostExecutorFlags(cfg); err != nil {
		return err
	}
	if err := daemon.CheckCLIRunFlags(cfg); err != nil {
		return err
	}
	// Four backends now, so this is a count rather than a pair: any two
	// of them together is the same misconfiguration, and naming which
	// two were passed is what tells an operator which flag to drop.
	if selected := daemon.SelectedSandboxBackends(cfg); len(selected) > 1 {
		return fmt.Errorf("%s select different sandbox backends -- pass one of them, not several",
			strings.Join(selected, " and "))
	}
	if cfg.ContainerSandboxes {
		if cfg.ContainerImage == "" {
			return errors.New("-container-sandbox-image is required with -container-sandboxes")
		}
		if cfg.ContainerGitProxyHost == "" {
			return errors.New("-container-git-proxy-host is required with -container-sandboxes")
		}
	}
	if cfg.K8sSandboxes {
		if cfg.K8sWorkspace == "" {
			return errors.New("-k8s-workspace is required with -k8s-sandboxes")
		}
		if cfg.K8sGitProxyHost == "" {
			return errors.New("-k8s-git-proxy-host is required with -k8s-sandboxes (in-cluster, set GRAIN_POD_IP " +
				"from the downward API instead and it is taken from there -- deploy/kubernetes/deployment.yaml does)")
		}
	}
	if cfg.K8sSandboxes || cfg.K8sGranuleSandboxes {
		for _, entry := range cfg.K8sNodeSelector {
			if !strings.Contains(entry, "=") {
				return fmt.Errorf("-k8s-node-selector %q is not a \"key=value\" pair", entry)
			}
		}
	}
	if cfg.K8sGranuleSandboxes {
		if cfg.K8sGranuleImage == "" {
			return errors.New("-k8s-granule-image is required with -k8s-granule-sandboxes")
		}
		if cfg.K8sWorkspace == "" {
			return errors.New("-k8s-workspace is required with -k8s-granule-sandboxes -- the guest account's " +
				"home, the same field -k8s-sandboxes names its container's")
		}
		if cfg.K8sGitProxyHost == "" {
			return errors.New("-k8s-git-proxy-host is required with -k8s-granule-sandboxes (in-cluster, set " +
				"GRAIN_POD_IP from the downward API instead and it is taken from there)")
		}
		if cfg.UpgradeImage != "" && cfg.UpgradeImageRefFile == "" {
			return errors.New("-upgrade-image is not supported yet with -k8s-granule-sandboxes: this backend's " +
				"self-upgrade would need a privileged, /dev/kvm-mounted health-check pod rather than the plain " +
				"one -k8s-sandboxes checks a candidate image with, and nothing here builds that yet -- repoint " +
				"this Deployment's image by hand instead (`kubectl set image deployment/<name> " +
				"<container>=<image>`)")
		}
	}
	if cfg.ExecutorAddr != "" && cfg.ExecutorTokenFile == "" {
		return errors.New("-executor-token-file is required with -executor-addr")
	}
	if (cfg.ExecutorCertFile != "" && cfg.ExecutorKeyFile == "") || (cfg.ExecutorCertFile == "" && cfg.ExecutorKeyFile != "") {
		return errors.New("both -executor-cert-file and -executor-key-file are required for TLS")
	}
	if err := checkPackageCacheUpstreams(cfg); err != nil {
		return err
	}
	if cfg.UpgradeSrcDir != "" && cfg.UpgradeInstallPath == "" && cfg.UpgradeImage == "" {
		return errors.New("-upgrade-install-path is required with -upgrade-src-dir")
	}
	// A ref file is how a *host* deployment tells its unit which image to
	// start, so an -upgrade-image with nowhere to record its answer
	// upgrades nothing. A Kubernetes deployment has no unit and no such
	// file: the Deployment's own spec is the record, and repointing it is
	// the upgrade (upgrade.ModeKube).
	if cfg.UpgradeImage != "" && cfg.UpgradeImageRefFile == "" && !cfg.K8sSandboxes {
		return errors.New("-upgrade-image-ref-file is required with -upgrade-image (except with -k8s-sandboxes, " +
			"where an upgrade repoints this deployment's own Deployment instead of writing a file)")
	}
	if cfg.K8sSandboxes && cfg.UpgradeImage != "" {
		if cfg.K8sDeployment == "" {
			return errors.New("-k8s-deployment is required with -k8s-sandboxes and -upgrade-image: an upgrade " +
				"has to know which Deployment to repoint")
		}
		if cfg.K8sContainer == "" {
			return errors.New("-k8s-container is required with -k8s-sandboxes and -upgrade-image")
		}
	}
	if cfg.GranuleSandboxes {
		if cfg.GranuleSSHUser == "" {
			return errors.New("-granule-ssh-user is required with -granule-sandboxes")
		}
		if cfg.GranuleWorkspace == "" {
			return errors.New("-granule-workspace is required with -granule-sandboxes")
		}
		if cfg.GranuleGitProxyHost == "" {
			return errors.New("-granule-git-proxy-host is required with -granule-sandboxes")
		}
	}
	if cfg.MaxWorkers < 1 {
		return errors.New("-max-workers must be at least 1")
	}
	// Refused for the same reason as -max-workers above, and for one
	// more: this is the value loadConfig falls back to when the stored
	// interval is one no reconcile loop can be paced by
	// (runnableStoredConfig), so a flag that is not itself runnable would
	// leave that fallback with nothing to offer -- and a non-positive
	// interval reaching time.NewTicker panics the loop while leaving the
	// UI server up, which is the failure that is hardest to notice
	// (grain/task-316).
	if cfg.PollInterval <= 0 {
		return errors.New("-poll-interval must be positive")
	}
	if cfg.BackupInterval < 0 {
		return errors.New("-backup-interval cannot be negative")
	}
	if cfg.BackupInterval > 0 && cfg.DbDriver == "postgres" {
		return errors.New("-backup-interval requires SQLite; database snapshotting is not supported on Postgres (use pg_dump or database-native backups)")
	}
	if cfg.MaxMergers < 0 {
		return errors.New("-max-mergers cannot be negative")
	}
	// Negative rather than zero: 0 is "use grain's own default" for each
	// of these (ci.OpenDirCache reads it that way), so only a value that
	// cannot mean anything at all is refused. A cache bounded at a
	// negative size would otherwise be silently promoted to the default
	// and run nothing like what the unit file asked for.
	switch {
	case cfg.CiCacheMaxAge < 0:
		return errors.New("-ci-cache-max-age cannot be negative")
	case cfg.CiCacheMaxBytes < 0:
		return errors.New("-ci-cache-max-bytes cannot be negative")
	case cfg.CiCacheMaxEntries < 0:
		return errors.New("-ci-cache-max-entries cannot be negative")
	case cfg.CiCacheMaxEntryBytes < 0:
		return errors.New("-ci-cache-max-entry-bytes cannot be negative")
	case cfg.CiArtifactMaxAge < 0:
		return errors.New("-ci-artifact-max-age cannot be negative")
	case cfg.CiArtifactMaxBytes < 0:
		return errors.New("-ci-artifact-max-bytes cannot be negative")
	case cfg.CiArtifactMaxRunBytes < 0:
		return errors.New("-ci-artifact-max-run-bytes cannot be negative")
	case cfg.CiArtifactMaxEntryBytes < 0:
		return errors.New("-ci-artifact-max-entry-bytes cannot be negative")
	}
	return nil
}
