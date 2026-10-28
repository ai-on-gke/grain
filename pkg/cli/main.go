// Package cli is grain's command, the one binary this repo builds
// (bwsalmon/agents#313). cmd/grain is a one-line main over Main, and
// google/cmd/grain is the same main with the Google-internal registrations
// linked in (google/README.md).
//
// Given no recognized subcommand, or one of the task-management verbs
// below, grain is a standalone CLI over pkg/ui's HTTPClient -- a plain REST
// client of the same JSON API (`grain daemon`'s own embedded `pkg/ui.Server`)
// the browser frontend speaks, driven straight from a terminal instead.
// It lets the caller do what a human does to a task: create one, edit its
// fields, attach or detach a capability, accept (approve) a proposal or
// withdraw that approval again, defer one for later or pick it back up,
// comment, and close (grain's own stand-in for "delete" -- see
// Client.Close's own doc comment for why) or reopen one.
//
// This used to open the task store directly -- embedded, or a Dolt SQL
// server via -store-addr -- and call pkg/ui.Client's methods in-process,
// which made the CLI a second direct writer against whatever daemon a
// deployment also runs (README.md used to carry a whole "Single
// writer" section about exactly that). bwsalmon/agents#363 closed that
// gap: the daemon is the only thing that ever opens the store now, and
// serves pkg/ui's JSON API and static frontend itself (daemon.go's own
// doc comment); this talks to that API over plain HTTP, wherever an
// operator's network puts it -- a loopback port, an SSH tunnel,
// Tailscale, IAP. -server names it; there is no GitHub credential and no
// store flag here at all any more.
//
// Other subcommands -- daemon, mcpserver, secrets, backup, controller, setup,
// sync -- select entirely different modes, each what used to be its own
// binary, its own flag or store carve-out, or a later addition with no
// business touching the task store at all. daemon (daemon.go, formerly
// cmd/graind) runs pkg/orchestrator's RunCycle on a timer and, unless
// -ui-addr is emptied out, serves the UI/API in-process too -- the
// standalone "ui" subcommand this project used to have is gone,
// folded into daemon by #363 rather than kept as a second way to open
// the same store. mcpserver (mcpserver.go, formerly cmd/mcpserver)
// speaks MCP over stdin/stdout against the sandbox tools; a running
// daemon forks mcpserver instances of itself -- this same binary,
// re-invoked with "mcpserver" as argv[1] -- rather than needing a second
// binary on disk (see pkg/agent/claude's own doc comment for the one
// place that matters today). secrets (bwsalmon/agents#357, secrets.go)
// sets/deletes/lists a colocated server's secrets directly on disk, no
// store or RPC involved. controller (controller.go) runs one-time
// interactive setup verbs (currently just bootstrap-github-app) against
// an operator's own machine, never a running daemon. setup and sync
// (bwsalmon/agents#358, setup.go and sync.go) bootstrap and reconcile a
// deployment's external GCP infrastructure and, for sync, its stored
// settings too -- sync reaches those over the same REST API this CLI
// does now, rather than opening the store itself. "demo" (demo.go) is a
// smaller mode still: a throwaway UI server over fake data, for trying
// out the frontend with no daemon, no store and no deployment behind it
// at all.
//
// The task CLI itself takes no GitHub credentials at all. A task used to
// be a GitHub issue, so this command needed a token and a task repo to
// file one; a task is a store row now, and creating one is a write to
// the store (README, "Input is a model update, not a GitHub issue").
//
// Folder management (docs/data-model.md's Folder tree, the containment
// structure a capability's `offers` are attached to) is deliberately
// absent: README.md already states folders are unbuilt, so there is no
// store-backed concept yet for a command here to manage. What a repo
// *does* have stored -- its own default capability set, and whether the
// deployment's allowlist names it -- is `grain repo` (repo.go, grain/
// task-36), which is deployment configuration in the same sense `grain
// settings` and `grain secrets` are, not the containment tree. -repo on
// `create`/`update` still only ever sets one task's own target.
//
// Schedules, templates, suites and qualification plans remain UI-only,
// and repo.go's own doc comment has why that is not the same question:
// they are authored content rather than deployment configuration, and
// docs/schedules.md records their absence here as an open gap waiting on
// somebody who needs it. The one thing here that touches any of the
// three is "declarations" (declcmd.go), and it writes nothing at all: it
// reads the templates, suites and schedules a repository declares in its
// own tree (docs/declarations.md) and says whether they parse, which is
// a question asked from inside a checkout with no deployment to ask.
//
// This file is the dispatch and the helpers every verb shares. The task
// verbs are taskcmd.go (printed by taskprint.go), and `grain config` /
// `grain settings` are settingscmd.go (printed by settingsprint.go).
// size_test.go keeps every file here under a size limit, so logic that
// is more than flags and printing goes in pkg/.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/gitrefs"
	"github.com/ai-on-gke/grain/pkg/hostscriptcli"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Main dispatches on argv[1] before parsing anything else: "daemon",
// "demo", "mcpserver", "secrets", "controller", "setup", "init" and
// "sync" are modes with their own, unrelated flag sets, so they are
// matched exactly
// and handed the rest of argv verbatim rather than folded into runCLI's
// own flag.FlagSet. Anything else -- including no arguments at all, or a
// leading global flag like -json -- falls through to the task CLI
// exactly as it always has, so a task command itself is never allowed to
// collide with one of the mode names above (none of the ones below do).
func Main() {
	// Exec'ing a file-capability /usr/local/bin/grain (Dockerfile's setcap
	// cap_net_bind_service) clears dumpable, leaving /proc/<pid>/fd 0500
	// root:root and unreadable by the daemon's port relay or grain hostd.
	protocol.MakeDumpable()
	// Before any mode runs a git: every repository this process or its
	// children create is in the files ref format, whatever a user's
	// init.defaultRefFormat says, so the release image's older git can
	// still read it (pkg/gitrefs).
	gitrefs.Pin()
	args := os.Args[1:]
	// Inside an agent's sandbox, the read commands of
	// docs/design/sandbox-first-agent-surface.md answer over the
	// sandbox's own proxy (sandboxcli.go); anything else runs as it
	// always has.
	if code, handled := maybeRunSandboxCLI(args, osSandboxEnv(), os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	if len(args) > 0 {
		switch args[0] {
		case "daemon":
			runDaemonCommand(args[1:])
			return
		case "upgrade-postgres", "upgrade-sqlite-to-postgres":
			upgradePostgresCmd(args[1:])
			return
		case "db":
			if len(args) > 1 && (args[1] == "upgrade-postgres" || args[1] == "upgrade-sqlite-to-postgres") {
				upgradePostgresCmd(args[2:])
				return
			}
			upgradePostgresCmd(args[1:])
			return
		case "demo":
			cmdDemo(args[1:])
			return
		case "mcpserver":
			mcpserver(args[1:])
			return
		case "capability":
			if servesCapability(args[1:]) {
				capabilityServeCmd(args[1:])
				return
			}
		case "runhost":
			runHostCmd(args[1:])
			return
		case "hostd":
			hostdCmd(args[1:])
			return
		case "host-script":
			os.Exit(hostscriptcli.Main(args[1:], os.Stdout, os.Stderr))
		case "executor", "modelexec":
			executorCmd(args[1:])
			return
		case "access-code":
			accessCodeCmd(args[1:])
			return
		case "secrets":
			secretsCmd(args[1:])
			return
		case "connections":
			connectionsCmd(args[1:])
			return
		case "backup":
			backupCmd(args[1:])
			return
		case "export":
			exportCmd(args[1:])
			return
		case "import":
			importCmd(args[1:])
			return
		case "state":
			stateCmd(args[1:])
			return
		case "controller":
			controller(args[1:])
			return
		case "setup":
			setupCmd(args[1:])
			return
		case "init":
			initCmd(args[1:])
			return
		case "sync":
			syncCmd(args[1:])
			return
		case "repo-case":
			repoCaseCmd(args[1:])
			return
		case "version":
			versionCmd(args[1:])
			return
		case "schema-version":
			schemaVersionCmd(args[1:])
			return
		case "release-notes":
			releaseNotesCmd(args[1:])
			return
		case "sandbox-image":
			sandboxImageCmd(args[1:])
			return
		case "container-sandbox-image":
			containerSandboxImageCmd(args[1:])
			return
		case "k8s-sandbox-image":
			k8sSandboxImageCmd(args[1:])
			return
		case "image":
			grainImageCmd(args[1:])
			return
		case antigravity.HookSubcommand:
			agyToolHook(args[1:])
			return
		case claude.StopHookSubcommand:
			claude.ServeStopHook(args[1:], os.Stdin, os.Stdout, os.Stderr)
			return
		}
	}
	if err := runCLI(args); err != nil {
		fmt.Fprintln(os.Stderr, "grain: "+err.Error())
		os.Exit(1)
	}
}

const usage = `usage: grain [global flags] <command> [args]
       grain daemon [flags]    run pkg/orchestrator's RunCycle on a timer, and serve the UI/API (see daemon.go)
       grain demo [flags]      serve the task UI on localhost over a throwaway store of fake tasks (see demo.go)
       grain mcpserver [flags] speak MCP over stdin/stdout against the sandbox tools (see mcpserver.go)
       grain runhost exec [flags] a run host's entrypoint: run one spooled agent CLI invocation (see pkg/runhost)
       grain hostd [flags]     on the bare machine, run the allowlisted host CLIs and capability servers a containerised daemon asks for (see pkg/hostd)
       grain host-script <name> [args]  in a sandbox, run a granted host script on grain's side (see pkg/hostscriptcli)
       grain secrets [flags]   set/delete/list a colocated server's secrets on disk (see secrets.go)
       grain connections import FILE  record a seed file's git and model connections (see connections.go)
       grain backup [flags]    back up the database and config repository to Google Drive or GCS (see backup.go)
       grain export [flags]    export database, state repository and secrets to a migration bundle (see export.go)
       grain import [flags]    import migration bundle into a target data directory (see import.go)
       grain state [flags]     where this installation's state repository lives, and what it holds (see state.go)
       grain controller bootstrap-github-app [flags]  one-time interactive setup for the github-sandbox capability (see controller.go)
       grain setup gcp [flags] bootstrap external GCP infrastructure for a new installation (see setup.go)
       grain sync [flags]      reconcile a live deployment's settings and/or GCP infrastructure from a config file (see sync.go)
       grain repo-case [flags] report, and with -apply fix, repos stored in a case this
                               deployment spells another way (see repocase.go)
       grain version [flags]   print the running grain version / build stamp and exit (see version.go)
       grain schema-version    print pkg/model.SchemaVersion and exit (see schemaversion.go)
       grain release-notes [--from vA] [--to vB] [--json]  print the per-version release notes this build carries (see releasenotes.go)
       grain sandbox-image     print the Granule sandbox container this build expects and exit (see sandboximage.go)
       grain container-sandbox-image print the local Docker sandbox container this build expects and exit
       grain k8s-sandbox-image print the Kubernetes sandbox container this build expects and exit
       grain image             print the image this build is published as and exit (see grainimage.go)
       grain agy-tool-hook     answer one of agy's PreToolUse hooks on stdin/stdout (see agyhook.go)
       grain claude-stop-hook  answer one of Claude Code's Stop hooks on stdin/stdout (see pkg/agent/claude/stophook.go)

Global flags (must come before the command):
  -server string  base URL of a running "grain daemon"'s UI/API. Defaults to
                  $GRAIN_SERVER when that is set, else GRAIN_SERVER in
                  /etc/grain/cli.env ($GRAIN_CLI_ENV names another file), else
                  http://127.0.0.1:4242 -- scripts/setup.sh writes that file
  -connect-timeout duration  how long to wait to connect, and to wait out a daemon
                  that refuses connections (default 5s)
  -timeout duration  how long one call waits for the daemon to answer (default 2m)
  -token string   auth token or shared secret for a proxy in front of the daemon
                  ($GRAIN_TOKEN, $GRAIN_AUTH_TOKEN, $GRAIN_ID_TOKEN, $GRAIN_TUNNEL_TOKEN)
  -audience string target audience for Google ID token authentication ($GRAIN_AUDIENCE, $GRAIN_TUNNEL_AUDIENCE)
  -as string      identity for request attribution / proxy headers ($GRAIN_USER)
  -json           print machine-readable JSON instead of a human-readable table

Commands:
  list [flags]                         list the tasks in play -- closed and deferred ones are left
                                       out unless -closed/-deferred or a filter naming that state
                                       asks for them (-filter attribute:comparison:value narrows the
                                       list, repeatably, with -state/-repo/-capability and friends
                                       as shorthands; -sort attribute:asc orders it. "grain list -h"
                                       prints the attributes and comparisons -- see list.go)
  get <id>                             show one task and its conversation
  create -title T [flags]              file a new task (-position front|end picks which end of
                                       the backlog it joins, and is remembered for the next one;
                                       -field name=value sets one of the fields this deployment
                                       declares, repeatably -- "grain create -h" lists them)
  update <id> [flags]                  edit a task's title or fields, -field name=value included
  approve <id>                         accept a proposed task (proposed -> queued)
  withdraw <id>                        withdraw a queued task's approval (queued -> proposed)
  defer [-until <date>] <id>           put a task aside without closing it (proposed/queued -> deferred);
                                       deferred tasks are hidden from task lists until asked for, and
                                       with -until the task picks itself back up on that date
  undefer <id>                         pick a deferred task back up, into whichever of proposed or
                                       queued its approval says
  capability <id> <cap> attach|detach  attach or detach a capability
  comment <id> <body...>               post a comment (and answer a parked question)
  close [-close-pull-request] <id>     close a task (grain's "delete" -- see the package doc comment),
                                       and with the flag its open pull request too
  delete <id>                          alias for close
  reopen <id>                          reopen a closed task
  retry <id>                           clear a failed task's retry cap so it dispatches again
  retry-merge <id>                     put a pull request the merge queue gave up on back on the queue
  force-merge <id> <reason...>         merge this change anyway, overriding the queue's own decision --
                                       an operator's action, recorded with the reason on the task and
                                       the pull request, and refused if the branch does not merge cleanly;
                                       -head <sha> names the commit you reviewed, and the override is
                                       withdrawn if anything else is pushed before the merge
  run-now <id>                         start a run of this task next, ahead of the queue and whatever
                                       its state says -- including sending a finished or merging task
                                       back to an agent for more work on its own branch
  config                               show the capabilities this deployment offers
  settings [flags]                     show, or change, the daemon's stored configuration (bwsalmon/agents#320)
  repo <subcommand> [args]             list repos, and read or change one repo's own settings --
                                       capabilities, standing instructions and setup command (see repo.go)
  metrics [-window 7d] [-repo R]       throughput and latency over a window -- or over an absolute
                                       span (-since/-until) -- scoped by repeatable -repo and -author
                                       to the tasks it is about (see metrics.go)
  failures [-window 7d] [-suspect]     what runs said when they failed for a reason grain could not
                                       name -- the instrument over its own provider-refusal markers
                                       (see failures.go)
  pause [-lift|-set|-resume]           why nothing is dispatching, and stop or resume this deployment (see pause.go)
  pause [-now] <id>                    pause one task in any open state: release what it holds, keep its place
                                       (a running task is given 2m to push and write its handover; -now skips it)
  resume <id>                          resume a paused task, back to exactly the state and standing it had
  ci [run|list|show|watch|cancel]      grain's own CI: start a run against a repo and a branch, list
                                       what has run (-repo, -ref, -conclusion, -job, -since narrow it,
                                       -sort and -order put it in an order), read one run's jobs and
                                       steps, follow a run in flight ("watch <id>", or "show -follow
                                       <id>": its output as it is printed), cancel one still going.
                                       A bare "grain ci" lists (see cicmd.go)
  ci cache [clear]                     what the CI job cache holds -- entries, size, hit rate -- and
                                       "clear" to empty it (safe: a cached job simply runs again)
  ci faults [-repo r] [-source s]      shared CI faults across branches right now (grain CI and GitHub CI,
                                       labelled by source)
  declarations check [dir]             read a checkout's own .grain/templates, .grain/suites and
                                       .grain/schedules and print what they declare, or refuse them
                                       and exit non-zero -- no daemon involved, so a repository can
                                       run it as a .grain/workflows step and fail the pull request
                                       that breaks a declaration (see declcmd.go, docs/declarations.md)
  git-connections                      list the git connections this deployment clones and pushes
                                       with, one "host credential" per line (see deployment.go)
  git-ambient [-host h] [-kind github|git] [-base-url u] [-proxy u] [-source gh|git|auto] [-if-none]
                                       give this deployment an ambient git connection: this host's own
                                       gh / git login, asked for a token at the moment one is needed
                                       (a daemon on the bare machine only; docs/git-hosts.md). -kind git is a plain
                                       git host with no forge API, whose login is git's credential helper
  default-model [-if-none | -if-unserved] <model>
                                       set the model tasks run on when nothing more specific chose one
  gcp-key list|get|set|delete          manage this deployment's GCP service-account keys
  capability-servers list|add|remove|enable|disable|bind-secret
                                       manage this deployment's capability servers (mcpServers)
  broadcast [-all] <message...>        send a notice to all running task agents (or with -all, all active tasks)
  release approve|reject vX.Y.0        make a pending trunk release official, or drop it -- a person's act;
                                       an agent is refused
`

// defaultServerURL is loopback on the port every installer binds the
// UI/API to by default (0.0.0.0:4242: the daemon's own -ui-addr,
// grain init's, scripts/setup.sh's and google/scripts/cloudtop-install.sh's),
// so the CLI on a freshly installed host reaches it with nothing set.
const defaultServerURL = "http://127.0.0.1:4242"

// serverEnvVar overrides defaultServerURL, so a deployment serving the
// UI on any other port does not make every CLI invocation on its own
// host carry a -server flag. A deployment sets it once, for every shell
// on the box, rather than each operator remembering the port: scripts/
// setup.sh writes /etc/profile.d/grain.sh from the -ui-addr it started
// the daemon with.
//
// An explicit -server still wins -- see serverDefault, which supplies
// this only as the flag's default.
const serverEnvVar = "GRAIN_SERVER"

// serverDefault is what -server defaults to: GRAIN_SERVER if it is set
// to anything non-empty, defaultServerURL otherwise. Empty is treated as
// unset rather than as a request to talk to no server at all, since an
// exported-but-empty variable is a broken profile script, not an
// intention.
//
// Between the two comes /etc/grain/cli.env (cliconn.go), which
// scripts/setup.sh writes in both install modes so that a shell that
// never read /etc/profile.d/grain.sh -- the one that just ran setup.sh
// -- still finds this host's daemon.
func serverDefault() string {
	url, _ := serverSource()
	return url
}

// dataDirEnvVar is the same idea for the subcommands that edit a
// colocated deployment's files on disk rather than talking to its API:
// `grain state` and `grain secrets` (grain/task-303). Both need the root
// that deployment's daemon was started with, and there is exactly one
// such root per host -- so a deployment that knows it can say so once,
// for every shell on the box, instead of every operator remembering the
// path. scripts/setup.sh exports it both in the CLI wrapper's container
// and in /etc/profile.d/grain.sh, beside GRAIN_SERVER.
//
// That matters most for the commands a deployment prints itself: its
// closing report tells the operator to run `grain state status`, and
// typed exactly as printed that used to fail with "-data-dir is
// required".
//
// An explicit -data-dir still wins -- see dataDirDefault, which supplies
// this only as the flag's default -- and a host that sets neither gets
// the same error it always did.
const dataDirEnvVar = "GRAIN_DATA_DIR"

// dataDirDefault is what -data-dir defaults to: GRAIN_DATA_DIR if it is
// set to anything non-empty, and otherwise empty, which the subcommands
// go on to reject. Empty is treated as unset rather than as a data
// directory named "", for the same reason serverDefault does it.
//
// /etc/grain/cli.env's GRAIN_DATA_DIR comes after the variable, the same
// precedence serverDefault gives GRAIN_SERVER.
func dataDirDefault() string {
	if v := strings.TrimSpace(os.Getenv(dataDirEnvVar)); v != "" {
		return v
	}
	return readCLIEnv().dataDir
}

func runCLI(args []string) error {
	fs := flag.NewFlagSet("grain", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	server := fs.String("server", serverDefault(), "base URL of a running \"grain daemon\"'s UI/API ($"+serverEnvVar+" overrides the default)")
	token := fs.String("token", "", "auth token for a proxy in front of the daemon ($GRAIN_TOKEN, $GRAIN_AUTH_TOKEN, $GRAIN_ID_TOKEN, $GRAIN_TUNNEL_TOKEN)")
	audience := fs.String("audience", "", "Google ID token audience for Cloud Run ($GRAIN_AUDIENCE, $GRAIN_TUNNEL_AUDIENCE)")
	asUser := fs.String("as", "", "identity for request attribution / proxy headers ($GRAIN_USER)")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON instead of a human-readable table")
	connectTimeout := fs.Duration("connect-timeout", defaultConnectTimeout,
		"how long to wait to connect to the daemon, and to wait out one that refuses connections (a restart)")
	callTimeout := fs.Duration("timeout", defaultCallTimeout, "how long one call waits for the daemon to start answering")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, source := serverSource()
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "server" {
			source = "-server"
		}
	})
	useCLIEnvDataDir()

	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return errors.New("a command is required")
	}

	c := ui.NewHTTPClient(*server)
	c.HTTP = cliHTTPClient(*connectTimeout, *callTimeout)
	c.RetryPolicy = cliRetryPolicy(*connectTimeout)
	if *token != "" {
		c.Token = *token
	}
	if *audience != "" {
		c.Audience = *audience
	}
	if *asUser != "" {
		c.User = *asUser
	}
	out := &printer{json: *jsonOutput}
	ctx := context.Background()

	target := cliTarget{server: *server, source: source, connect: *connectTimeout, call: *callTimeout}
	return explainDaemonError(dispatchCLI(ctx, c, out, fs, rest), target)
}

// dispatchCLI runs one task-CLI command against c.
func dispatchCLI(ctx context.Context, c *ui.HTTPClient, out *printer, fs *flag.FlagSet, rest []string) error {
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "list":
		return cmdList(ctx, c, out, cmdArgs)
	case "get":
		return cmdGet(ctx, c, out, cmdArgs)
	case "create":
		return cmdCreate(ctx, c, out, cmdArgs)
	case "update":
		return cmdUpdate(ctx, c, out, cmdArgs)
	case "approve":
		return cmdApprove(ctx, c, out, cmdArgs)
	case "withdraw":
		return cmdWithdrawApproval(ctx, c, out, cmdArgs)
	case "defer":
		return cmdDefer(ctx, c, out, cmdArgs)
	case "undefer":
		return cmdUndefer(ctx, c, out, cmdArgs)
	case "capability":
		return cmdCapability(ctx, c, out, cmdArgs)
	case "comment":
		return cmdComment(ctx, c, out, cmdArgs)
	case "close", "delete":
		return cmdClose(ctx, c, out, cmdArgs)
	case "reopen":
		return cmdReopen(ctx, c, out, cmdArgs)
	case "retry":
		return cmdRetry(ctx, c, out, cmdArgs)
	case "retry-merge":
		return cmdRetryMerge(ctx, c, out, cmdArgs)
	case "force-merge":
		return cmdForceMerge(ctx, c, out, cmdArgs)
	case "run-now":
		return cmdRunNow(ctx, c, out, cmdArgs)
	case "config":
		return cmdConfig(ctx, c, out, cmdArgs)
	case "metrics":
		return cmdMetrics(ctx, c, out, cmdArgs)
	case "failures":
		return cmdFailures(ctx, c, out, cmdArgs)
	case "pause":
		return cmdPause(ctx, c, out, cmdArgs)
	case "resume":
		return cmdResume(ctx, c, out, cmdArgs)
	case "ci":
		return cmdCI(ctx, c, out, cmdArgs)
	case "declarations":
		return cmdDeclarations(ctx, c, out, cmdArgs)
	case "settings":
		return cmdSettings(ctx, c, out, cmdArgs)
	case "repo":
		return cmdRepo(ctx, c, out, cmdArgs)
	case "gcp-key":
		return cmdGCPKey(ctx, c, out, cmdArgs)
	case "capability-servers":
		return cmdCapabilityServers(ctx, c, out, cmdArgs)
	case "git-ambient":
		return cmdGitAmbient(ctx, c, out, cmdArgs)
	case "git-connections":
		return cmdGitConnections(ctx, c, out, cmdArgs)
	case "default-model":
		return cmdDefaultModel(ctx, c, out, cmdArgs)
	case "broadcast", "notify":
		return cmdBroadcast(ctx, c, out, cmdArgs)
	case "release":
		return cmdRelease(ctx, c, out, cmdArgs)
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// taskID reads the task id positional argument. It is not parsed as a
// number: ids are decimal today because that is what Store.NewTaskID
// allocates, but Task.ID is an opaque string and nothing here is entitled
// to assume otherwise.
func taskID(s string) (string, error) {
	if s == "" {
		return "", errors.New("a task id is required")
	}
	return s, nil
}

// stringList is a repeatable flag.Value -- one -capability per capability
// ID, rather than a single comma-joined flag a capability ID containing
// a comma could never round-trip through.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseArgs is flag.FlagSet.Parse for a command that takes positional
// arguments as well as flags, and it exists because Go's own Parse stops
// at the first word that is not a flag. Every usage line here is written
// the way people type these commands -- `grain update <id> [flags]`,
// `grain repo capabilities [-set a,b] <owner/name>` -- and with a plain
// Parse the first of those forms silently did nothing at all: the id
// ended parsing, -title was left among the leftover arguments, fs.Visit
// saw no flag set, and the command sent an empty request and printed the
// unchanged task as though it had worked. `grain repo capabilities
// acme/widgets -set self-debug` was the same shape of quiet failure --
// it printed the repo's capabilities instead of replacing them.
//
// So flags are parsed wherever they appear: Parse is called again on
// what is left after each positional word, which is what a GNU-style
// getopt does with permutation, and the positional words are returned in
// the order they were written. A bare "--" still ends the flags, and
// everything after it is positional verbatim -- which is how a value
// that starts with a dash is passed to a command that takes one.
//
// `grain comment <id> <body...>` deliberately does not use this: its
// body is free text, and a body word that happens to name a flag has to
// stay a body word. misplacedFlag below is what that command says
// instead of guessing.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var terminated []string
	for i, arg := range args {
		if arg == "--" {
			terminated = args[i+1:]
			args = args[:i]
			break
		}
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return append(positional, terminated...), nil
}

// first is args[0] or "", so a command missing its one positional
// argument reports that through taskID's own error rather than panicking
// on an empty slice -- flag.FlagSet.Arg's behaviour, which parseArgs'
// callers no longer go through.
func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// misplacedFlag reports the first word in args that names a flag defined
// on fs -- a flag written after the positional arguments, where
// flag.Parse has already stopped looking at them.
func misplacedFlag(fs *flag.FlagSet, args []string) (string, bool) {
	for _, arg := range args {
		name := strings.TrimLeft(arg, "-")
		if name == arg || name == "" {
			continue // not a flag-shaped word, or a bare "--"
		}
		name, _, _ = strings.Cut(name, "=")
		if fs.Lookup(name) != nil {
			return name, true
		}
	}
	return "", false
}

// printer renders CLI output either as a human-readable table (the
// default) or as JSON (-json) -- the same shape a script or another
// program (the UI included, per this command's own doc comment) would
// want instead of text meant for a terminal.
type printer struct{ json bool }

// note is a line about the listing rather than a line of it -- what was
// left out of a task list and how to ask for it (list.go's hiddenNote).
// It goes to stderr so that a listing stays a listing: `grain list |
// wc -l` counts tasks, and -json emits JSON and nothing else.
func (p *printer) note(text string) {
	if p.json || text == "" {
		return
	}
	fmt.Fprintln(os.Stderr, text)
}

func (p *printer) encode(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
