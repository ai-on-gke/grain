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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/gitrefs"
	"github.com/ai-on-gke/grain/pkg/hostscriptcli"
	"github.com/ai-on-gke/grain/pkg/sandboxcli"
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
	if len(args) == 1 && args[0] == "hostds" {
		// A bare "grain hostds" listed the registry; "grain host" alone
		// runs the host agent.
		args = []string{"host", "list"}
	}
	canonicalizeCommandWord(args, os.Stderr)
	// Inside an agent's sandbox, the read commands of
	// design/design/sandbox-first-agent-surface.design.md answer over the
	// sandbox's own proxy (pkg/sandboxcli); anything else runs as it
	// always has.
	if code, handled := sandboxcli.Main(args, os.Stdout, os.Stderr); handled {
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
			dbCmd(args[1:])
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
		case "host":
			// grain host list|pending|accept|... is the host registry, a
			// task-CLI command (cmdHostds); anything else runs the host
			// agent on this machine.
			if len(args) < 2 || !hostRegistryVerb(args[1]) {
				hostdCmd(args[1:])
				return
			}
		case "host-script":
			os.Exit(hostscriptcli.Main(args[1:], os.Stdout, os.Stderr))
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
		case codex.DetachedSubcommand:
			runCodexDetached(args[1:])
			return
		}
	}
	if len(args) == 0 {
		// Inside a sandbox, a bare grain gets the sandbox CLI's help.
		if code, handled := sandboxcli.Unknown(args, os.Stderr); handled {
			os.Exit(code)
		}
	}
	if code := runTaskCLI(args, os.Stderr); code != exitOK {
		os.Exit(code)
	}
}

// hiddenCommandAliases are command words grain still answers to but no
// longer shows: "hostd" (the host agent) and "hostds" (the host
// registry) are both "host" now, which users see. Neither is in usage
// or the CLI spec; both keep working for scripts and installed units.
//
// "registry" is "hub" now that the Grain Registry is the Grain Hub; it
// warns (warnDeprecatedCommand) and goes after a release.
var hiddenCommandAliases = map[string]string{
	"hostd":    "host",
	"hostds":   "host",
	"registry": "hub",
}

// canonicalCommand is the command word cmd is spelled as now.
func canonicalCommand(cmd string) string {
	if c, ok := hiddenCommandAliases[cmd]; ok {
		return c
	}
	return cmd
}

// runTaskCLI runs the task CLI and says how it ended (errors.go): the
// exit code, and on stderr either a sentence or, under -json, the one
// {"error": ...} object -- with whatever the flag package or a command
// would have said there held back, so the object is all a script reads.
func runTaskCLI(args []string, stderr io.Writer) int {
	asJSON := jsonRequested(args)
	var err error
	if asJSON {
		var said string
		said, err = holdStderr(func() error { return runCLI(args) })
		if err == nil || errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stderr, said)
		}
	} else {
		err = runCLI(args)
	}
	var unknown unknownCommandError
	if !asJSON && errors.As(err, &unknown) {
		// Inside a sandbox, a command neither CLI has is answered with
		// the sandbox CLI's help, not this CLI's daemon-facing usage;
		// the task-CLI commands above (grain declarations check) still
		// run there.
		if code, handled := sandboxcli.Unknown(args, stderr); handled {
			return code
		}
		fmt.Fprint(stderr, usage)
	}
	return reportError(stderr, err, asJSON)
}

// holdStderr runs fn with os.Stderr captured, and returns what it wrote
// there instead of letting it through.
func holdStderr(fn func() error) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", fn()
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		said, _ := io.ReadAll(r)
		done <- string(said)
	}()
	err = fn()
	os.Stderr = old
	_ = w.Close()
	said := <-done
	_ = r.Close()
	return said, err
}

const usage = `usage: grain [global flags] <command> [args]
       grain daemon [flags]    run pkg/orchestrator's RunCycle on a timer, and serve the UI/API (see daemon.go)
       grain demo [flags]      serve the task UI on localhost over a throwaway store of fake tasks (see demo.go)
       grain mcpserver [flags] speak MCP over stdin/stdout against the sandbox tools (see mcpserver.go)
       grain runhost exec [flags] a run host's entrypoint: run one spooled agent CLI invocation (see pkg/runhost)
       grain host [flags]      on the bare machine, run the host agent: the allowlisted host CLIs and capability servers a containerised daemon asks for (see pkg/hostd)
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

Global flags (before or after the command; -flag and --flag are the same):
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
  -context string the named context to use ($GRAIN_CONTEXT, else config.json's current)

Commands:
  login <url> [flags] / logout [<ctx>] / whoami   sign in to a deployment as a named context, and say as whom
  context list|use|show|delete         the named contexts ($XDG_CONFIG_HOME/grain/config.json)
  skill install|status [flags]         install or check the workstation SKILL.md guide (and with -mcp, register grain mcp)
  mcp [--context <name>]               speak external coordination MCP over stdin/stdout (13 tools; see mcp_external.go)
  list [flags]                         list the tasks in play -- closed and deferred ones are left
                                       out unless -closed/-deferred or a filter naming that state
                                       asks for them (-filter attribute:comparison:value narrows the
                                       list, repeatably, with -state/-repo/-capability and friends
                                       as shorthands; -sort attribute:asc orders it. "grain list -h"
                                       prints the attributes and comparisons -- see list.go)
  get <id> [-comments-since T]         show one task and its conversation (only what came after T)
  show <id> [-comments-since T]        alias for get
  create -title T [flags]              file a new task (-position front|end picks which end of
                                       the backlog it joins, and is remembered for the next one;
                                       -field name=value sets one of the fields this deployment
                                       declares, repeatably -- "grain create -h" lists them)
  update <id> [flags]                  edit a task's title or fields, -field name=value included
  approve [-reason r] <id>             accept a proposed task (proposed -> queued); an external agent
                                       credential (gma_) must give -reason
  withdraw <id>                        withdraw a queued task's approval (queued -> proposed)
  defer [-until <date>] <id>           put a task aside without closing it (proposed/queued -> deferred);
                                       deferred tasks are hidden from task lists until asked for, and
                                       with -until the task picks itself back up on that date
  undefer <id>                         pick a deferred task back up, into whichever of proposed or
                                       queued its approval says
  capability <id> <cap> attach|detach  attach or detach a capability
  comment <id> <body...>               post a comment (and answer a parked question)
  reply|inbox|wait|logs <id...>        answer a parked question, list what needs a person, block on tasks, read a run's log
  close [-close-pull-request] <id>     close a task (grain's "delete" -- see the package doc comment),
                                       and with the flag its open pull request too
  delete <id>                          alias for close
  reopen <id>                          reopen a closed task
  retry <id>                           clear a failed task's retry cap so it dispatches again
  retry-merge <id>                     put a pull request the merge queue gave up on back on the queue
  force-merge [-override-gates g] <id> <reason...>
                                       merge this change anyway, overriding the queue's own decision --
                                       an operator's action (merge:force), recorded with the reason on
                                       the task and pull request, and refused if the branch does not
                                       merge cleanly; -override-gates must explicitly name any skipped
                                       review gate (e.g. security-review); -head <sha> pins the commit
  pr adopt|review|status|submit <pr>   adopt (-mode review-only|finish), review (-design), report on
                                       (-threads), or approve/submit a pull request (see prcmd.go)
  run-now [-reason r] <id>             start a run of this task next, ahead of the queue and whatever
                                       its state says -- including sending a finished or merging task
                                       back to an agent for more work on its own branch
  compact [-handover-file f] <id>      fold a task's conversation into a handover for later prompts
  brief [-file f -reason r] <id>       show or replace a task's current brief and handover age
  rollover <id>                        close a durable task as rolled over to a fresh successor
  config                               show the capabilities this deployment offers
  settings [flags]                     show, or change, the daemon's stored configuration (bwsalmon/agents#320)
  sandbox-budget                       whether one sandbox of this deployment's shape fits in its sandbox budget,
                                       and the settings that would fit when it does not (see sandbox_budget_cmd.go)
  sandbox probe -id <id> [-access dialout|kubectl] -- <command> [args...]
                                       build one throwaway sandbox, run the command in it over the
                                       deployment's guest-access channel, print its output and delete it
                                       (-k8s-granule-sandboxes only; see sandbox_probe_cmd.go)
  repo <subcommand> [args]             list repos, and read or change one repo's own settings --
                                       capabilities, standing instructions and setup command (see repo.go)
  metrics [-window 7d] [-repo R]       throughput and latency over a window -- or over an absolute
                                       span (-since/-until) -- scoped by repeatable -repo and -author
                                       to the tasks it is about (see metrics.go)
  failures [-window 7d] [-suspect]     what runs said when they failed for a reason grain could not
                                       name -- the instrument over its own provider-refusal markers
                                       (see failures.go)
  usage [-mode auto] [-budget c/m=n/5h] token usage, budget, reset and burn rate per connection and model,
                                       and the workers the remaining budget sustains (see usage_cmd.go)
  health                               the counters a layer built on grain keeps in the daemon, by
                                       section, since it started (-json: all of GET /api/sandboxes;
                                       see health.go)
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
  ci wait (<run-id> | -pr <url|owner/name#n> | -ref <branch> -repo <owner/name>) [-jobs j1,j2]
                                       block until a run has a verdict (-timeout, default 30m), with each
                                       failing job and any shared fault: exit 0 green, 1 red, 5 timed out
  ci retrigger <run-id> [-jobs j1,j2]  rerun a run's failed jobs (or -jobs) once, bypassing the job cache; a gha: run is re-run on GitHub
  ci test-history <test> [-repo r] [-limit N] [-provider grain|github-actions]
                                       pass, fail and flake history of one test across recent runs
  merge-queue [-repo owner/name]       queued pull requests, the batches and CI testing them, and held
                                       merge gates (deploy://merge-queue; see mergequeue.go)
  fs ls|cat|read|grep <uri>            read grain's namespace (ci://, task://, deploy://, config://,
                                       memory://, src://) over GET /api/fs/, as a sandbox's grain ls,
                                       read and grep do; read is cat (see fscmd.go)
  declarations check [dir]             read a checkout's own .grain/templates, .grain/suites and
                                       .grain/schedules and print what they declare, or refuse them
                                       and exit non-zero -- no daemon involved, so a repository can
                                       run it as a .grain/workflows step and fail the pull request
                                       that breaks a declaration (see declcmd.go, docs/declarations.md)
  git-connections                      list the git connections this deployment clones and pushes
                                       with, one "host credential" per line (see deployment.go)
  git-login [-web|-from-gh|-with-token] [-host h] [-scope s] [-author name]
                                       delegate your GitHub HTTPS token to this deployment and sign new
                                       pull requests and comments in your name ("Signed by <Name> (via Grain)")
  git-ambient [-host h] [-kind github|git] [-base-url u] [-proxy u] [-source gh|git|auto] [-if-none]
                                       give this deployment an ambient git connection: this host's own
                                       gh / git login, asked for a token at the moment one is needed
                                       (a daemon on the bare machine only; docs/git-hosts.md). -kind git is a plain
                                       git host with no forge API, whose login is git's credential helper
  default-model [-if-none | -if-unserved] <model>
                                       set the model tasks run on when nothing more specific chose one
  qa [status|enable|disable]           inspect or configure the quality control engine and PR rollout (Settings -> Tasks)
  gcp-key list|get|set|delete          manage this deployment's GCP service-account keys
  capability-servers list|add|remove|enable|disable|bind-secret
                                       manage this deployment's capability servers (mcpServers)
  host list|pending|accept|reject|invite|codes|rename|update|revoke|delete|delete-revoked|rekey
                                       hosts (Settings -> Hosts): invite hosts with one shared enrollment
                                       snippet, accept or reject the hosts that join, and manage hosts
  host register|install|set-owner      the legacy host registry: register a host with its owner, the
                                       identity it dials in through IAP as, and print how to install it
  hub status|register|unregister       self-register this deployment with a Grain Hub and inspect
                                       heartbeats and self-reported scopes (Settings -> Hub)
  access list|allow|deny               the allowed users and groups list (Settings -> Access): who may
                                       sign in through IAP or another identity verifier
  broadcast [-all] <message...>        send a notice to all running task agents (or with -all, all active tasks)
  release approve|reject|rollback vX.Y.0
                                       make a pending trunk release official, drop it, or roll a promoted
                                       release back -- a person's act; an agent is refused
  release approval-key                 the public key GitHub's promote-release.yml verifies an Inbox approval
                                       with (repository variable GRAIN_RELEASE_APPROVAL_KEY)
  upgrade status|start|cancel|rollback inspect, start, cancel or roll back an in-place upgrade (Settings -> Upgrade)
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

func audienceDefault() string {
	for _, env := range []string{"GRAIN_AUDIENCE", "GRAIN_TUNNEL_AUDIENCE"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return ""
}

func asUserDefault() string {
	return strings.TrimSpace(os.Getenv("GRAIN_USER"))
}

func runCLI(args []string) error {
	fs := flag.NewFlagSet("grain", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	server := fs.String("server", serverDefault(), "base URL of a running \"grain daemon\"'s UI/API ($"+serverEnvVar+" overrides the default)")
	token := fs.String("token", "", "auth token for a proxy in front of the daemon ($GRAIN_TOKEN, $GRAIN_AUTH_TOKEN, $GRAIN_ID_TOKEN, $GRAIN_TUNNEL_TOKEN)")
	audience := fs.String("audience", audienceDefault(), "Google ID token audience for Cloud Run ($GRAIN_AUDIENCE, $GRAIN_TUNNEL_AUDIENCE)")
	asUser := fs.String("as", asUserDefault(), "identity for request attribution / proxy headers ($GRAIN_USER)")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON instead of a human-readable table")
	connectTimeout := fs.Duration("connect-timeout", defaultConnectTimeout,
		"how long to wait to connect to the daemon, and to wait out one that refuses connections (a restart)")
	callTimeout := fs.Duration("timeout", defaultCallTimeout, "how long one call waits for the daemon to start answering")
	contextName := fs.String("context", "", "the named context to use ($"+contextEnvVar+"; see `grain context list`)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return usageErrorf("a command is required")
	}
	cmdArgs, err := liftGlobalFlags(fs, rest[0], rest[1:])
	if err != nil {
		return err
	}
	rest = append(rest[:1:1], cmdArgs...)
	sel, source, err := resolveTarget(fs, server, *contextName)
	if err != nil {
		return err
	}
	useCLIEnvDataDir()

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
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "timeout" {
			ctx = withWaitLimit(ctx, *callTimeout)
		}
	})

	target := cliTarget{
		server:      *server,
		source:      source,
		tokenSource: useContextCredential(c, sel, tokenSource(*token)),
		connect:     *connectTimeout,
		call:        *callTimeout,
	}
	if sock := hostSocket(c.BaseURL, target.tokenSource); sock != "" {
		c.HTTP, c.Socket = cliSocketHTTPClient(sock, *connectTimeout, *callTimeout), sock
		target.tokenSource = "the API socket " + sock
	}
	if err = explainDaemonError(dispatchCLI(ctx, c, out, fs, sel, rest), target); err == nil {
		warnIfCommandSkillStale(rest[0], sel)
	}
	return err
}

// dispatchCLI runs one task-CLI command against c.
func dispatchCLI(ctx context.Context, c *ui.HTTPClient, out *printer, fs *flag.FlagSet, sel selectedContext, rest []string) error {
	cmd, cmdArgs := canonicalCommand(rest[0]), rest[1:]
	warnDeprecatedCommand(os.Stderr, rest[0], cmd)
	switch cmd {
	case "context", "whoami", "login", "logout":
		return dispatchSignIn(ctx, c, out, sel, cmd, cmdArgs)
	case "skill":
		return cmdSkill(ctx, c, out, sel, cmdArgs)
	case "mcp":
		return cmdMCP(ctx, c, fs, sel, cmdArgs)
	case "list":
		return cmdList(ctx, c, out, cmdArgs)
	case "get", "show":
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
	case "reply", "inbox", "wait", "logs", "compact", "brief", "rollover":
		return externalCommands[cmd](ctx, c, out, cmdArgs)
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
	case "usage":
		return cmdUsage(ctx, c, out, cmdArgs)
	case "health":
		return cmdHealth(ctx, c, out, cmdArgs)
	case "pause":
		return cmdPause(ctx, c, out, cmdArgs)
	case "resume":
		return cmdResume(ctx, c, out, cmdArgs)
	case "ci":
		return cmdCI(ctx, c, out, cmdArgs)
	case "pr":
		return cmdPR(ctx, c, out, cmdArgs)
	case "fs":
		return cmdFS(ctx, c, out, cmdArgs)
	case "merge-queue":
		return cmdMergeQueue(ctx, c, out, cmdArgs)
	case "declarations":
		return cmdDeclarations(ctx, c, out, cmdArgs)
	case "settings":
		return cmdSettings(ctx, c, out, cmdArgs)
	case "qa":
		return cmdQA(ctx, c, out, cmdArgs)
	case "sandbox-budget":
		return cmdSandboxBudget(ctx, c, out, cmdArgs)
	case "sandbox":
		return cmdSandbox(ctx, c, out, cmdArgs)
	case "repo":
		return cmdRepo(ctx, c, out, cmdArgs)
	case "gcp-key":
		return cmdGCPKey(ctx, c, out, cmdArgs)
	case "capability-servers":
		return cmdCapabilityServers(ctx, c, out, cmdArgs)
	case "host":
		return cmdHostds(ctx, c, out, cmdArgs)
	case "hub":
		return cmdHub(ctx, c, out, cmdArgs)
	case "access":
		return cmdAccess(ctx, c, out, cmdArgs)
	case "git-ambient":
		return cmdGitAmbient(ctx, c, out, cmdArgs)
	case "git-login":
		return cmdGitLogin(ctx, c, out, cmdArgs)
	case "git-connections":
		return cmdGitConnections(ctx, c, out, cmdArgs)
	case "default-model":
		return cmdDefaultModel(ctx, c, out, cmdArgs)
	case "broadcast", "notify", "release", "upgrade":
		return operatorCommands[cmd](ctx, c, out, cmdArgs)
	default:
		return unknownCommandError{cmd}
	}
}
