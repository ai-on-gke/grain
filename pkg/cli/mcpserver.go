// mcpserver.go implements `grain mcpserver`, formerly its own
// cmd/mcpserver binary before bwsalmon/agents#313 combined every mode
// into one: it serves the sandbox tools and the escape hatches
// (ask_question, request_secret, comment_on_issue, propose_task,
// add_review_comment -- recorded here, relayed by
// orchestrator.ProcessResult once the run finishes) over its own stdin/stdout, so anything that can spawn a
// subprocess and speak MCP -- not just an in-process
// client -- can drive it. Real MCP clients (an actual `claude` or
// `gemini` CLI's --mcp-config, for instance) can point at this same
// grain binary, "mcpserver" and its own flags as the configured args, as
// pkg/agent/claude does.
//
// Its sandbox tools run against one of two backends: a local directory
// (-sandbox-root, unchanged since before bwsalmon/agents#256 -- what
// pkg/mcp's own tests and tests/e2e/ still use, since neither wants a
// real VM), or a real granule-managed VM (-granule-vm, see package granule
// and pkg/mcp's NewSSHSandboxTools) -- the wiring bwsalmon/agents#256
// asked for, so a run_command/read_file/edit_file/write_file call
// actually lands inside one of the sandbox VMs granule is running
// instead of the local-directory stand-in pkg/mcp/sandbox_tools.go's
// own doc comment describes. -granule-backend selects which of granule's
// two ways of running that VM -granule-vm was created with -- "docker"
// (the default, bwsalmon/agents#353) needs just a local docker daemon;
// "static-pod" needs a standalone kubelet
// (deploy/static-kubelet/README.md) and is resolved via crictl instead.
//
// -pr-repo/-pr-branch add pkg/mcp's pull_request_status and the legacy
// GitHub-backed wait_for_checks to that roster. A run holding grain-ci
// gets that wait tool replaced below by the task-scoped Grain CI form,
// which explicitly starts and polls Grain CI without reading GitHub
// checks. The pull-request tools also
// add base_branch_status with them, which reads the same CI for the
// branch that work merges into (-pr-base, or the repo's default branch),
// so a run staring at a red check can tell breakage it caused from
// breakage it inherited instead of guessing. Those
// reads happen *here*, in this
// process, which runs on the controller: the GitHub credential comes off
// the controller's own -data-dir (the same secrets/github ladder
// `grain daemon` loads) and never crosses into the sandbox, so
// docs/design.md's split surface -- "Sandboxes: git transport only" --
// is untouched. See pkg/mcp/pullrequest_tools.go's own doc comment.
//
// -server and -task add three further tools. open_pull_request: a run that
// has pushed its branch can have grain open its pull request there and
// then, and read back what the repo's own CI makes of it, rather than
// exiting blind and leaving the pull request to the finish path. And
// recreate_sandbox: a run whose sandbox has become unusable -- a wedged
// guest, a full disk, a filesystem an interrupted install left in a
// state no command can untangle -- can have grain destroy it and build a
// clean one, and carry on in the turns it has left instead of failing
// every remaining call in a sandbox it cannot repair from inside. And
// update_status: a run can put one short phrase on its own task's row --
// "waiting for CI", "running the test suite" -- so a person watching a
// task that has read 'running' for half an hour can see what that half
// hour is going on, rather than reading the transcript afterwards. It is
// the only one of the three that changes nothing: grain shows the phrase
// and never reads it back.
//
// -interactive adds a fourth to that pair, for a task somebody is
// actually sitting with: chat, which posts a message into the task's own
// conversation, parks the task on it, and blocks until a person replies
// -- a whole turn of a conversation inside one tool call, rather than one
// dispatch per thing said. The waiting is done here, by polling the same
// daemon (daemonChat), which is what pkg/capability/selfrepair's Confirm
// has always needed and never had: a route to the conversation from a
// process holding no store handle.
//
// -propose-repo names a repository this deployment works in, once per
// repository, and adds no tool at all: it narrows one that is always
// registered. propose_task lets a run file a proposal against any
// repository this deployment works in, and nothing in the sandbox says
// which those are -- so a run that does not know a repository is
// nameable files the work against its own instead, and nobody finds out
// until a human retargets the proposal by hand (grain/task-144). The
// flag carries the daemon's own answer (model.Store.WorkRepos) into that
// argument's description, which is where a run reads it. Unset -- a
// caller with no store to ask, which is every test here -- leaves the
// description naming the rule without the list, as it has always read.
//
// -grant names a capability grant this run's task holds, once per grant,
// and is how a grant whose whole effect is which tools a run gets
// reaches a run at all. Until it existed those tools were assembled by
// orchestrator.Config.GrantTools and consumed by nobody: every framework
// that remains forks a CLI and ignores agent.RunConfig.Tools, so a
// capability granted tools no running agent could call.
//
// -grant self-debug adds that capability's own read-only tools:
// read_grain_source and list_grain_source, reading the checkout
// -grain-src-dir names (pkg/capability/selfdebug).
//
// That grant turns on a second half as well -- list_grain_tasks,
// read_grain_task, read_grain_task_prompt and read_grain_task_transcript,
// reading grain's *other* tasks over -server, plus read_grain_metrics,
// reading what this deployment measures about itself over that same
// connection (the report `grain metrics` prints, which a sandbox holding
// no deployment address and no token cannot ask for any other way).
// The task readers were briefly retired, on the grounds that the state
// repository held every one of those rows
// as a file, and came back with the export that stopped holding them:
// staterepo.ExportConfig writes the settings tables and the memory ones
// and no others, so tasks, runs, comments and observations exist only in
// the daemon's database and these four tools are the only way a sandbox
// reaches them. A run dispatched without -server gets the tools anyway
// and each one refuses with a sentence saying why (mcp.NewTaskTools).
//
// -grant grain-log adds read_grain_log, a bounded read of this host's
// own systemd journal (pkg/capability/grainlog). It is served from here
// for the same reason the pull-request tools are: this process runs on
// the controller, so journalctl is a subprocess away, and -- far more
// importantly -- so is the redaction. -grain-secrets-dir names the
// directory whose credentials are cut out of every line before it is
// returned. Nothing in that directory is ever served to a run; the flag
// exists so that this process can recognize those values in the journal
// and remove them, which is the entire basis on which a sandbox is
// allowed to see any of it. Without the flag the tool is still on the
// roster and refuses every call, rather than answering with a journal it
// could not clean.
//
// -grant bootstrap-playbooks adds list_bootstrap_playbooks and
// read_bootstrap_playbook, the runbooks for grain's own setup flows
// (pkg/capability/bootstrap). It needs no hop of any kind: the playbooks
// are markdown embedded in this very binary, so this process is already
// holding everything the tools read.
//
// Both are read-only, which is why both can be served from here at all.
// pkg/capability/selfrepair cannot follow: its one tool blocks on a
// human's reply in the task's own chat, which needs the store handle
// this process deliberately does not have.
//
// -run-deadline is the moment grain will cancel the run this process
// serves. It adds no tool: it makes every tool result carry how much
// wall-clock time is left, once there is little enough of it to change
// what a run should be doing (pkg/mcp's run_deadline.go). The prompt
// states the same budget up front, and is read once, hours earlier --
// this is the half that reaches a run at turn 200.
//
// The flags name the daemon to ask and the task to ask about. Unlike
// pull_request_status above, both of these are *writes*, and writes stay
// grain's: this process asks the daemon over its REST API rather than
// acting with a credential -- or, for the sandbox, an authority -- of
// its own. See daemonPullRequests and daemonSandbox below.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/mcp"
)

// grantedTools is every tool the grants this run holds are worth, and
// the only ones here that depend on which *task* the run belongs to
// rather than only on which sandbox. A run gets each set exactly when a
// human attached that grant to its task, which is what -grant says (see
// that flag, and agent.RunConfig.Grants for how a Framework learns it).
// Everything either set exposes is read-only, so there is no
// confirmation step, matching pkg/capability/selfdebug and
// pkg/capability/bootstrap and unlike pkg/capability/selfrepair.
//
// A deployment with no checkout of grain's source to point at (srcDir
// "") still gets both of self-debug's tools, each refusing the call with
// a sentence saying so rather than being absent from the roster
// (selfdebug.SourceTools' own doc comment), so a run's tool roster stays
// a property of the grants it holds rather than of one deployment's
// configuration.
//
// grain-log follows the same rule with the opposite default. Its tool is
// always on the roster too, but a deployment that gave this process no
// -grain-secrets-dir -- or whose secrets file this host cannot open --
// gets a tool that refuses every call, because the guarantee that
// capability is granted under is that credentials are removed here,
// before the answer leaves, and a process that cannot enumerate them
// cannot make it. Failing closed is the only direction that fails safe:
// an unread journal costs a run one answer, and an unredacted one cannot
// be taken back out of a model's context or a sandbox's network.
//
// bootstrap-playbooks has nothing that can be missing at all: its
// playbooks are markdown embedded in this binary, so its tools read what
// this process is already holding and depend on no flag but the grant
// itself.
func grantedTools(grants daemon.GrantNames, srcDir, secretsDir, taskID string) []mcp.Tool {
	return daemon.GrantedDaemonTools(grants, srcDir, secretsDir, "", taskID, nil)
}

func mcpserver(args []string) {
	if sock := os.Getenv("GRAIN_EXECUTOR_SOCK"); sock != "" {
		if err := tunnelMCPServer(sock, args); err != nil {
			fmt.Fprintf(os.Stderr, "grain mcpserver tunnel: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := serveMCPServer(os.Stdin, os.Stdout, args); err != nil {
		if !errors.Is(err, io.EOF) {
			fmt.Fprintf(os.Stderr, "grain mcpserver: %v\n", err)
			os.Exit(2)
		}
	}
}

func tunnelMCPServer(sock string, args []string) error {
	return tunnelMCPServerIO(sock, args, os.Stdin, os.Stdout)
}

// tunnelMCPServerIO bridges in and out to the executor's MCP tunnel. It
// returns once grain's side of the tunnel is finished, not when in is: a
// client that writes its last request and closes its end -- `echo req |
// grain mcpserver` -- is still owed the answer, which arrives after in has
// hit EOF. Returning on whichever copy finished first dropped that answer
// while grain still acted on the request.
func tunnelMCPServerIO(sock string, args []string, in io.Reader, out io.Writer) error {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("dialing executor MCP tunnel at %s: %w", sock, err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(args); err != nil {
		return fmt.Errorf("sending args to executor tunnel: %w", err)
	}

	go func() {
		_, _ = io.Copy(conn, in)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, err = io.Copy(out, conn)
	return err
}

func serveMCPServer(in io.Reader, out io.Writer, args []string) error {
	return daemon.ServeMCPServerPinned(in, out, args, nil)
}
