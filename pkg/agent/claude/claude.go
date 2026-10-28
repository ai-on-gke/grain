// Package claude implements agent.Framework by running the real `claude`
// CLI as a subprocess on the controller -- the same shape v1's
// grain/automation/dispatch.py already settled on (see that file's own
// docstring, "Why the agent doesn't run on the sandbox anymore"): the
// agent's OAuth credential must never enter the untrusted execution
// environment, so `claude -p` runs here on the controller with its native
// tool roster emptied out, reaching the sandbox only through the MCP tools
// grain's own "mcpserver" subcommand exposes (pkg/cli/mcpserver.go).
//
// claude -p is the actual product bwsalmon/agents#255 asked to add as an
// option, so Run spawns it directly over os/exec and lets it manage its
// own MCP connection rather than looping turns itself. --mcp-config points that
// connection at grainBinaryPath (this same grain binary -- bwsalmon/
// agents#313 combined what used to be a standalone cmd/mcpserver build
// into a subcommand of the one binary everything else here runs as too)
// with "mcpserver" and either -sandbox-root (RunConfig.SandboxRoot, a
// local directory) or -granule-vm (RunConfig.GranuleVM, a named
// orchestrator.GranuleSandboxes VM, plus WithGranuleSSH's own
// -ssh-user/-exec-key/-workspace) as its args -- claude forks that
// command itself once it loads --mcp-config, the "forking off processes"
// cmd/grain/main.go's own doc comment on this package refers to.
// Nothing in a tool call itself can ever change where that lands,
// mirroring v1's own "only place it's named" discipline (dispatch.py's
// _mcp_config_json).
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/mcp"
)

const (
	// defaultMaxTurns is the cap on model/tool round trips a run may take
	// before claude's own --max-turns cuts it off, and zero means no cap
	// at all: Run passes no --max-turns whatsoever, exactly as v1
	// (grain/automation/dispatch.py) always did.
	//
	// A turn here is one model/tool round trip, and the ordinary shape of
	// a grain task -- read the repo, edit, run the tests, read the
	// failures, edit again, commit, push -- spends dozens of them. Any
	// number picked here is a guess at how much work a task deserves,
	// made by a package that knows nothing about the task; guessing wrong
	// does not slow a run down, it fails one that was working. The 20
	// this started at did exactly that, and while raising it made that
	// rarer it left the same cliff a little further out.
	//
	// Turns were never the bound worth enforcing anyway. What a runaway
	// run actually costs is wall-clock time and tokens, and
	// orchestrator's Config.MaxRunRuntime (two hours by default) already
	// stops one on the first of those regardless of how fast its turns
	// go. A deployment that does want a turn ceiling still sets one --
	// `grain settings -max-agent-turns`, RunConfig.MaxTurns, or
	// WithMaxTurns -- and gets it passed through unchanged.
	defaultMaxTurns = 0

	// mcpServerName is the mcpServers key written into --mcp-config, and
	// therefore the prefix claude reports every tool under
	// ("mcp__<name>__<tool>"). It lives in pkg/mcp rather than here
	// because both halves of that convention -- writing the prefix onto
	// --allowedTools, and taking it back off a reported call before
	// recording it (transcript.go) -- have to agree, and agent/antigravity
	// needs the identical pair for its own CLI.
	mcpServerName = mcp.ToolNamespace
)

// runner is the one seam this package needs to actually invoke the claude
// binary -- narrowed to an interface so a test can supply a canned
// transcript and exercise the parsing logic below without a real claude
// binary or a live Claude Code credential. tee, when non-nil, receives
// every byte of stdout as the subprocess produces it, alongside the
// buffer this still accumulates and returns whole once the process exits
// -- see hostRunner.Run's own doc comment on why a live copy of the exact
// same bytes is worth keeping.
type runner interface {
	Run(ctx context.Context, args []string, stdin string, env []string, tee io.Writer) (stdout string, err error)
}

type hostRunner struct {
	host       agent.Host
	claudePath string
}

// Run starts claude on whichever machine this Framework's host is -- a
// child of the daemon -- and, when tee is non-nil, mirrors its
// stdout into tee as the process produces it rather than after it exits.
// That live mirror is what RunConfig.TranscriptPath is for: a caller
// with access to that path can read a run's own stream-json output while
// claude is still running (bwsalmon/agents#467).
//
// stdout comes back alongside any error rather than being discarded.
// claude exits non-zero for every run whose terminal "result" event is
// an error, including the routine "--max-turns ran out" one, and reports
// that error on stdout as stream-json rather than on stderr: a caller
// handed only `exit status 1 (stderr: )` cannot tell a run that edited,
// committed and pushed before it ran out of turns from one that never
// started.
//
// Killing claude's own mcpserver child along with it is the Host's job
// and is done the same way on both -- see agent.LocalHost.Run's
// procgroup.Prepare.
func (r hostRunner) Run(ctx context.Context, args []string, stdin string, env []string, tee io.Writer) (string, error) {
	return r.host.Run(ctx, agent.Command{Path: r.claudePath, Args: args, Stdin: stdin, Env: env}, tee)
}

// Framework implements agent.Framework by running `claude -p`.
type Framework struct {
	run runner
	// host is the machine claude is staged on and runs on: the daemon
	// itself (agent.LocalHost). The --mcp-config
	// file a run is given is written through it too, so that the CLI
	// and the file it reads are always on the same machine.
	host agent.Host
	// claudePath is the binary New was given, kept alongside the runner
	// that already holds it so that TerminalCommand can name it: an
	// interactive session is not run through `run` at all -- it is a
	// process on a pseudo-terminal somebody else owns (pkg/console) --
	// so the path has to be readable rather than merely bound into a
	// closure. Empty for a Framework built by newFramework with a test
	// double for a runner, which is what makes TerminalCommand refuse
	// one rather than exec'ing "".
	claudePath       string
	grainBinaryPath  string
	oauthToken       func(context.Context) (string, error)
	useVertex        bool
	model            string
	maxTurns         int
	granuleSSHUser   string
	granuleExecKey   string
	granuleWorkspace string
	githubDataDir    string
	githubHost       string
	githubInsecure   bool
	grainServerURL   string
	// startMCP, when set, is how the mcpserver is started instead of as
	// claude's own stdio child: by grain, on this machine, behind an
	// authenticated loopback HTTP endpoint the --mcp-config names
	// (WithHostMCP).
	startMCP func(context.Context, string, []string) (*mcp.ManagedHTTP, error)

	// How many extra execs of claude one run may spend recovering from a
	// transient provider refusal, and how it waits between them --
	// agent/retry.go's own policy, which every framework that re-execs
	// its CLI shares. A test overrides its Sleep so the retry loop costs
	// the suite no wall clock.
	retry agent.RetryPolicy
}

// Option configures a Framework at construction time.
type Option func(*Framework)

// WithModel overrides the model claude is asked for.
func WithModel(model string) Option {
	return func(f *Framework) { f.model = model }
}

// WithMaxTurns sets the cap on agentic turns a single Run allows before
// claude's own --max-turns cuts it off. Zero -- which is also the default
// -- means no cap: see defaultMaxTurns for why that is the right default
// and Config.MaxRunRuntime for what actually bounds a runaway run.
func WithMaxTurns(n int) Option {
	return func(f *Framework) { f.maxTurns = n }
}

// WithOAuthToken sets the CLAUDE_CODE_OAUTH_TOKEN passed to the claude
// subprocess's own environment -- never as a command-line argument, so it
// never lands in `ps` output (the same reasoning as v1's
// CONTROLLER_AGENT_TOKEN_PATH; see dispatch.py's start_unit call site).
//
// Under WithVertex the same value is a Vertex AI service account key (or
// a path to one) instead, and Run hands it to claude through
// GOOGLE_APPLICATION_CREDENTIALS rather than CLAUDE_CODE_OAUTH_TOKEN.
func WithOAuthToken(token string) Option {
	return WithOAuthTokenFunc(func(context.Context) (string, error) { return token, nil })
}

// WithOAuthTokenFunc is WithOAuthToken for a deployment whose token is
// not known once, at construction: fn is called on every Run instead, so
// a token set (or replaced) while the daemon is running takes effect on
// the next run rather than at the next restart. cmd/grain's daemon reads
// it out of the secrets database the UI writes to, which is what makes
// "paste the token into Settings" work at all (see that package's own
// agentCredential).
//
// An error from fn fails the run: a token that cannot be read is not the
// same as a deployment that deliberately configured none, which stays
// what an empty string means here (Run then passes no
// CLAUDE_CODE_OAUTH_TOKEN at all and lets the subprocess authenticate
// from its own ambient environment, as it always could).
func WithOAuthTokenFunc(fn func(context.Context) (string, error)) Option {
	return func(f *Framework) { f.oauthToken = fn }
}

// WithVertex configures claude to run against Google Cloud Vertex AI
// (setting CLAUDE_CODE_USE_VERTEX=1 in the subprocess environment).
func WithVertex(useVertex bool) Option {
	return func(f *Framework) { f.useVertex = useVertex }
}

// WithHostMCP has grain start each run's mcpserver itself, on the
// daemon's machine, and hand claude an HTTP endpoint for it rather than a
// command to fork -- the way agent/codex has always been wired. It is for
// a claude run started somewhere other than as the daemon's own child
// (through grain hostd, agent.HostdHost): the mcpserver keeps the
// daemon's own docker access, granule exec key and GitHub credentials,
// none of which that CLI is given, and it reaches the mcpserver over the
// host network.
func WithHostMCP() Option {
	return func(f *Framework) { f.startMCP = mcp.StartCommandHTTP }
}

// WithGranuleSSH gives a Framework what it needs to reach a
// RunConfig.GranuleVM instead of a RunConfig.SandboxRoot: the same
// deployment-wide sshUser/execKey/workspace triple
// orchestrator.GranuleConfig already carries, passed straight through as
// the forked "mcpserver -granule-vm"'s own -ssh-user/-exec-key/-workspace
// (mirroring mcpserver.go's mustGranuleSandboxTools). A deployment that
// never sets -granule-sandboxes has no use for this; Run rejects a
// RunConfig.GranuleVM without it.
func WithGranuleSSH(sshUser, execKey, workspace string) Option {
	return func(f *Framework) {
		f.granuleSSHUser, f.granuleExecKey, f.granuleWorkspace = sshUser, execKey, workspace
	}
}

// WithGitHubAccess is what lets a run read CI for its own branch: the
// forked "mcpserver" subprocess is given -data-dir, -github-host and
// -github-insecure-http so it can build a GitHub REST client from this
// controller's own secrets/github ladder and answer pull_request_status
// (see pkg/cli/mcpserver.go, and pkg/mcp/pullrequest_tools.go for why
// this does not put GitHub inside the sandbox).
//
// dataDir is the deployment-wide half; which repo and branch that
// process may then ask about comes per-run off agent.RunConfig, never
// from here. A Framework built without this option passes none of the
// three, and the tool answers that it has nothing to report -- so a
// deployment with no GitHub credential still runs, it just cannot see
// its own CI.
func WithGitHubAccess(dataDir, host string, insecureHTTP bool) Option {
	return func(f *Framework) {
		f.githubDataDir, f.githubHost, f.githubInsecure = dataDir, host, insecureHTTP
	}
}

// WithHost overrides the host the agent CLI is executed on.
func WithHost(host agent.Host) Option {
	return func(f *Framework) {
		if host != nil {
			f.host = host
		}
	}
}

// WithGrainServer names the running "grain daemon"'s own UI/API base URL
// (e.g. "http://127.0.0.1:8420") for the forked "mcpserver" subprocess to
// reach it at -- which, together with a RunConfig.TaskID, is what gives a
// run the open_pull_request tool. That tool's effect is real and
// immediate, unlike the mocked escape hatches, and unlike
// WithGitHubAccess's read-only pull_request_status it happens by asking
// the daemon rather than by the forked process holding a GitHub
// credential of its own (pkg/cli/mcpserver.go's daemonPullRequests):
// opening a pull request is a write, and writes stay grain's.
//
// Unset -- a deployment serving no UI/API, or any caller that has no
// daemon to name -- leaves the tool unregistered and every run exactly as
// it was: a pushed branch still becomes a pull request when the run
// finishes.
func WithGrainServer(url string) Option {
	return func(f *Framework) { f.grainServerURL = url }
}

// Asserted here rather than left to the one call site that type-asserts
// for it (orchestrator.frameworkOpensPullRequests): a Framework that
// stopped implementing this would otherwise not fail to compile, it
// would quietly stop telling its runs about a tool they still have.
var _ agent.PullRequestFramework = (*Framework)(nil)
var _ agent.ResultParser = (*Framework)(nil)

// CanOpenPullRequest implements agent.PullRequestFramework: a run driven
// by this Framework is offered open_pull_request exactly when
// WithGrainServer named a daemon for its forked mcpserver to ask (see
// grainServerArgs, which passes -server/-task together or not at all).
//
// It is what lets orchestrator.BuildPrompt tell a run about the tool
// only when the run really has it -- a tool description alone reaches
// nobody who never reads the roster, and a prompt that promised it
// unconditionally would be wrong on every deployment serving no UI/API.
func (f *Framework) CanOpenPullRequest() bool { return f.grainServerURL != "" }

// New builds a Framework that runs the real claude binary at claudePath
// (typically just "claude", resolved against $PATH) and points every
// run's --mcp-config at grainBinaryPath -- the same grain binary this
// process itself is (typically the result of os.Executable()) -- invoked
// as "grainBinaryPath mcpserver -sandbox-root <root>".
func New(claudePath, grainBinaryPath string, opts ...Option) *Framework {
	f := newFramework(nil, grainBinaryPath, opts...)
	f.claudePath = claudePath
	// After the options rather than before, because WithHost is one of
	// them: which machine claude runs on decides what the runner is.
	f.run = hostRunner{host: f.host, claudePath: claudePath}
	return f
}

func newFramework(run runner, grainBinaryPath string, opts ...Option) *Framework {
	f := &Framework{
		run:             run,
		host:            agent.LocalHost{},
		grainBinaryPath: grainBinaryPath,
		maxTurns:        defaultMaxTurns,

		retry: agent.RetryPolicy{Retries: agent.DefaultTransientRetries},
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// GrainBinaryPath is the grain binary every run's --mcp-config starts
// "mcpserver" with, as a path on Host rather than on the daemon's own
// machine.
func (f *Framework) GrainBinaryPath() string {
	return f.grainBinaryPath
}

// Host returns the Host this framework stages files on and runs the CLI on.
func (f *Framework) Host() agent.Host {
	return f.host
}

func isUnconfined(h agent.Host) bool {
	if u, ok := h.(agent.UnconfinedHost); ok {
		return u.Unconfined()
	}
	if u, ok := h.(interface{ Unconfined() bool }); ok {
		return u.Unconfined()
	}
	return false
}

// allowedTools names every tool the "mcpserver" subcommand can
// advertise, spelled the way claude reports and admits one loaded from
// --mcp-config: "mcp__grain-sandbox__<tool>".
//
// The names themselves are agent.PublishedToolNames -- one list, shared
// with agent/antigravity, computed from the constructors that register
// them rather than hand-copied, and its own doc comment says why a tool
// is named there whether or not this particular run will be offered it.
// What is claude's own business, and all that is left here, is the
// spelling: --strict-mcp-config refuses any tool this list omits, so a
// name in the wrong spelling is a tool the run dies on its first call
// to, exactly as an omitted one is.
//
// Each capability MCP server's tools are admitted too, spelled
// mcp__<name>__<tool> (docs/capabilities-spec.md §5.2.4).
func allowedTools(servers ...agent.CapabilityMCPServer) []string {
	names := agent.PublishedToolNames()
	for i, name := range names {
		names[i] = mcp.QualifiedToolName(name)
	}
	for _, s := range servers {
		for _, tool := range s.Tools {
			names = append(names, "mcp__"+s.Name+"__"+tool)
		}
	}
	return names
}

// mcpToolTimeoutVar is claude's own cap on how long it will let a single
// MCP tool call run before it abandons it, in milliseconds.
const mcpToolTimeoutVar = "MCP_TOOL_TIMEOUT"

// mcpToolTimeoutSlack is how far past the longest wait grain's own tools
// can do the cap above is set. Enough for the round trips either side of
// the wait itself -- including the addenda poll every answer makes on its
// way back (mcp's addendaPollTimeout, five seconds against these two
// minutes) -- and no more: the point is that the tool's own clock runs
// out first, not that claude stops capping tool calls.
const mcpToolTimeoutSlack = 2 * time.Minute

// mcpToolTimeout is the environment claude needs for wait_for_checks to
// be able to finish waiting.
//
// That tool blocks for as long as CI takes, up to an hour
// (mcp.MaxWaitForChecksTimeout), which is far past claude's own default
// cap on a tool call. So does chat, for as long as the person it is
// waiting for takes to answer (mcp.ChatWait) -- the cap is set from the
// longer of the two (mcp.MaxBlockingToolWait), so a second blocking tool
// cannot quietly outlive a number sized for the first. Leaving the cap where it is would kill the call
// part-way through a wait the agent deliberately asked for and report it
// as a tool failure -- the one answer that is neither true nor useful,
// since the build it was waiting on is still running and the run now has
// no idea how it ended. Raising it to just past the tool's own maximum
// puts the deadline back where it belongs: on grain's side, where
// running out of time produces a report saying so.
//
// An MCP_TOOL_TIMEOUT already in the environment is left alone. An
// operator who set one deliberately outranks this default, and setting
// it unconditionally would silently override theirs: env entries here
// are appended after os.Environ() under agent.LocalHost, and in a run
// host, which inherits none of the daemon's environment, the operator's
// value is the one operatorEnv forwards.
//
// The run's own wall clock does not lower this number, now that
// wait_for_checks clamps a wait to what is left of the run: that only
// ever shortens a particular call, and a run with an hour and a half
// still ahead of it can legitimately ask for the full hour. This is set
// once, at process start, with the whole run in front of it, so it has
// to cover the longest wait the tool will ever run. What the clamp does
// change is which clock is expected to end the call: a wait can no
// longer outlive the run it belongs to, so grain's side always reports
// first, and this cap is left where it is for the case it was really
// for -- an MCP server that has wedged, rather than a wait that is
// simply long.
//
// claude has a second cap beside that one, on how long a tool call may go
// without sending anything at all (mcpToolIdleTimeoutVar), and it is the
// shorter of the two by default: half an hour. Neither chat nor
// wait_for_checks sends anything while it waits, so a person who took
// thirty-one minutes to reply had their chat call abandoned under them --
// the reply then landed in a conversation no run was reading, and the
// run carried on believing nobody had answered (grain/task-1774, seen on
// task 1679). It is raised to the same limit, under the same rule about
// an operator's own value.
func mcpToolTimeout() []string {
	limit := mcp.MaxBlockingToolWait + mcpToolTimeoutSlack
	var env []string
	for _, name := range []string{mcpToolTimeoutVar, mcpToolIdleTimeoutVar} {
		if _, set := os.LookupEnv(name); set {
			continue
		}
		env = append(env, fmt.Sprintf("%s=%d", name, limit.Milliseconds()))
	}
	return env
}

// mcpToolIdleTimeoutVar is claude's cap, in milliseconds, on how long a
// single MCP tool call may run without a response or a progress
// notification before it is abandoned. claude clamps it to
// MCP_TOOL_TIMEOUT, so it can never be the longer of the two.
const mcpToolIdleTimeoutVar = "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT"

// autoMemoryVar is claude's own switch for the "persistent file-based
// memory" it otherwise offers every session, and truthy turns the
// feature off (its own Boolean spelling: 1/true/yes/on).
const autoMemoryVar = "CLAUDE_CODE_DISABLE_AUTO_MEMORY"

// disableAutoMemory turns that feature off for the runs this package
// dispatches, because none of them can reach it.
//
// Left on, it costs a run two things. Its system prompt gains a preamble
// describing a memory directory under the CLI's own $HOME
// (~/.claude/projects/-/memory), the file format to save facts in and
// the MEMORY.md index to point at them from; and the contents of that
// index are injected into the run's context as "user's auto-memory,
// persists across conversations".
//
// Neither is true of a grain run. claude runs here on the controller
// (this file's own doc comment on why the credential never leaves it),
// so that $HOME is the controller's -- $GRAIN_DATA_DIR/home, laid out by
// scripts/setup.sh -- while every file tool a run actually has acts in
// the sandbox at the other end of the MCP connection, where that path
// does not exist. So the index points at files no run can open (grain's
// own deployment had runs spending a turn apiece on the ENOENT), and
// there is nothing a run could write back: --tools "" empties the native
// roster the preamble's "write to it directly with the Write tool" names,
// and a sandbox is destroyed when the run ends. A memory that cannot be
// read and cannot be written is prompt a run has to reason past.
//
// grain/task-137 decided this with the operator rather than papering
// over it in prompt text: a deployment's own note saying "the memory
// preamble does not apply here" cancels the preamble only for a run that
// reads far enough, and it is a standing correction to something that
// should not arrive. This is that something not arriving.
//
// An operator's own setting is left alone, exactly as mcpToolTimeout
// leaves theirs (and forwarded by operatorEnv the same way), so setting
// this unconditionally would silently override a deployment that had
// deliberately set it either way --
// including the "=0" of one that has since found a way to give its runs
// a memory directory they can really reach.
func disableAutoMemory() []string {
	if _, set := os.LookupEnv(autoMemoryVar); set {
		return nil
	}
	return []string{autoMemoryVar + "=1"}
}

// operatorVars are the daemon variables this package promises an operator
// reach claude: the three grain would otherwise default (mcpToolTimeout,
// disableAutoMemory), which defer to an operator's value only on the
// understanding that the value then arrives, and CLAUDE_CODE_MAX_RETRIES,
// the hatch retry.go leaves open.
//
// Under agent.LocalHost they would arrive anyway, inside os.Environ().
// A run host (-run-host-image) is started with none of the daemon's
// environment -- only Command.Env, over the image's own -- so without this
// list an operator's MCP_TOOL_TIMEOUT there would switch grain's default
// off and put nothing in its place, leaving claude on its own cap, the
// one wait_for_checks outlives (grain/task-3731). Nothing outside the
// list is forwarded: the daemon's credentials stay out of a run host, and
// ANTHROPIC_BASE_URL, HTTPS_PROXY and the like describe the daemon's
// network rather than the container's, so a deployment that wants them
// in a run host sets them in its run-host image
// (docs/non-disruptive-upgrade.md, "Run hosts").
var operatorVars = []string{
	mcpToolTimeoutVar, mcpToolIdleTimeoutVar, autoMemoryVar, maxRetriesVar,
}

// HostdEnv is every variable name Run may send claude: operatorVars, the
// MCP timeouts grain defaults, the OAuth token, and what a Vertex AI
// connection sets. grain hostd refuses a request carrying any name its
// allowlist entry does not list, so setup.sh's container-equivalent entry
// (cli claude-detached) names exactly these, and tests/deploy holds the
// two in step.
func HostdEnv() []string {
	return append(append([]string(nil), operatorVars...),
		"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_VERTEX", "GOOGLE_APPLICATION_CREDENTIALS", "ANTHROPIC_VERTEX_PROJECT_ID")
}

// maxRetriesVar is claude's own count of retries of a refused API call
// (retry.go).
const maxRetriesVar = "CLAUDE_CODE_MAX_RETRIES"

// operatorEnv is operatorVars as this process has them, for Command.Env.
// Under agent.LocalHost each entry repeats the value os.Environ() already
// carries, which changes nothing.
func operatorEnv() []string {
	var env []string
	for _, name := range operatorVars {
		if value, set := os.LookupEnv(name); set {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// mcpConfigJSON is the --mcp-config file content: grainBinaryPath spawned
// with mcpArgs (built by mcpServerArgs below) -- the "mcpserver" argument
// selects the same subcommand mcpserver.go implements, so claude forking
// this exact command is what actually starts an MCP server, rather than
// needing a separately built binary on disk.
//
// Each capability MCP server is registered beside it under its own name,
// as the Streamable HTTP server grain's proxy is (§5.2.4).
func mcpConfigJSON(grainBinaryPath string, mcpArgs []string, servers ...agent.CapabilityMCPServer) ([]byte, error) {
	all := map[string]any{
		mcpServerName: map[string]any{
			"command": grainBinaryPath,
			"args":    mcpArgs,
		},
	}
	for _, s := range servers {
		all[s.Name] = map[string]any{"type": "http", "url": s.URL}
	}
	return json.Marshal(map[string]any{"mcpServers": all})
}

// httpMCPConfigJSON is the --mcp-config file content for a run whose
// mcpserver grain started itself (WithHostMCP): its endpoint and the
// bearer token it was started with.
func httpMCPConfigJSON(url, token string, servers ...agent.CapabilityMCPServer) []byte {
	all := map[string]any{
		mcpServerName: map[string]any{
			"type":    "http",
			"url":     url,
			"headers": map[string]string{"Authorization": "Bearer " + token},
		},
	}
	for _, s := range servers {
		all[s.Name] = map[string]any{"type": "http", "url": s.URL}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": all})
	return b
}

// mcpServerArgs builds the arguments the forked "mcpserver" subcommand
// needs to reach cfg's sandbox: "-sandbox-root <dir>" for a local
// directory, or "-granule-vm <name> -ssh-user ... -exec-key ... -workspace
// ..." (mirroring mcpserver.go's own mustGranuleSandboxTools) for a named
// granule VM, whichever RunConfig set. SandboxRoot wins if somehow both
// are -- RunDispatch never sets both at once in practice (a sandbox is
// either host-rooted or granule-named, never both), but a Framework this
// simple is not the place to enforce that.
//
// pullRequestArgs and grainServerArgs are appended to either, since
// which repo's CI a run may read, and whether it may ask grain to open
// its pull request, are both independent of which backend its sandbox
// runs on. So is agent.GrantArgs, which passes on the tool-granting
// capabilities this run's task holds -- and so whether that server
// serves the read-only tools for grain's own source (self-debug) or for
// grain's own bootstrap playbooks (bootstrap-playbooks). So is
// agent.ProposeRepoArgs, which passes on the repositories a proposal
// this run makes may be filed against, so that propose_task's own
// description can name them. So is agent.InteractiveArgs, which passes
// on whether this run's task is one somebody is sitting with, and so
// whether that server serves the chat tool a live conversation is held
// through (mcp.NewChatTools). So is agent.AddendaArgs, which passes on
// where this run's prompt stopped quoting the task's conversation, so
// that a comment posted after it reaches the run on a tool result rather
// than waiting for the task's next dispatch (mcp's addenda.go).
//
// So is agent.RunDeadlineArgs, which is why ctx is here at all: the
// deadline on the ctx this run was given is what grain will cancel it
// at, and passing it on is what lets that server's tool results tell the
// run how long it has left (see that function, and
// mcp.Registry.AnnounceDeadline).
func (f *Framework) mcpServerArgs(ctx context.Context, cfg agent.RunConfig) ([]string, error) {
	var args []string
	switch {
	case cfg.SandboxRoot != "":
		args = []string{"mcpserver", "-sandbox-root", cfg.SandboxRoot}
	case cfg.GranuleVM != "":
		if f.granuleSSHUser == "" || f.granuleWorkspace == "" {
			return nil, fmt.Errorf("claude: RunConfig.GranuleVM is set but this Framework has no granule SSH config (see WithGranuleSSH)")
		}
		args = []string{
			"mcpserver", "-granule-vm", cfg.GranuleVM,
			"-ssh-user", f.granuleSSHUser, "-workspace", f.granuleWorkspace,
		}
		// Omitted rather than passed empty when unset, which is the
		// normal case: "granule run" generates a keypair for each guest it
		// boots, so `granule exec`'s own default path already holds a key
		// that guest authorizes. An explicit key is only for a custom
		// guest image that authorizes one of its own.
		if f.granuleExecKey != "" {
			args = append(args, "-exec-key", f.granuleExecKey)
		}
	case cfg.Container != "":
		args = []string{"mcpserver", "-container", cfg.Container}
	case cfg.K8sPod.Pod != "":
		args = cfg.K8sPod.MCPServerArgs()
	default:
		return nil, fmt.Errorf("claude: RunConfig.SandboxRoot, .GranuleVM, .Container, or .K8sPod is required")
	}
	args = append(args, f.pullRequestArgs(cfg)...)
	args = append(args, f.grainServerArgs(cfg)...)
	args = append(args, agent.GrantArgs(cfg)...)
	args = append(args, agent.ProposeRepoArgs(cfg)...)
	args = append(args, agent.SandboxEnvArgs(cfg)...)
	args = append(args, agent.InteractiveArgs(cfg)...)
	args = append(args, agent.AddendaArgs(cfg)...)
	args = append(args, agent.RunDeadlineArgs(ctx, cfg)...)
	agent.AuthorizeMCPArgs(f.host, args)
	return args, nil
}

// grainServerArgs is the "-server/-task" pair that turns on the forked
// mcpserver's open_pull_request, or nothing at all when either half is
// missing -- a Framework built without WithGrainServer, or a caller with
// no task to name. One without the other names a question with no
// address to send it to, or an address with nothing to ask about, and
// mcpserver.go rejects either half on its own.
func (f *Framework) grainServerArgs(cfg agent.RunConfig) []string {
	if f.grainServerURL == "" || cfg.TaskID == "" {
		return nil
	}
	return []string{"-server", f.grainServerURL, "-task", cfg.TaskID}
}

// pullRequestArgs is the "-data-dir/-pr-repo/-pr-branch" triple (plus
// "-pr-base", when the task has a base of its own, and "-pr-repair",
// when this run is the merge queue repairing that branch) that
// turns on the forked mcpserver's pull_request_status, or nothing at all
// when either half is missing -- a Framework built without
// WithGitHubAccess, or a run whose task has no repo attached. Passing
// half of it would be worse than passing none: mcpserver would warn on
// stderr about a misconfiguration that is really just a task with no
// target.
func (f *Framework) pullRequestArgs(cfg agent.RunConfig) []string {
	if f.githubDataDir == "" || cfg.Repo == "" || cfg.Branch == "" {
		return nil
	}
	args := []string{"-data-dir", f.githubDataDir, "-pr-repo", cfg.Repo, "-pr-branch", cfg.Branch}
	// The base is passed only when the task carries one: unset, the
	// mcpserver's base_branch_status reads the repo's default branch,
	// which is the base a task with no base of its own really has.
	if cfg.Base != "" {
		args = append(args, "-pr-base", cfg.Base)
	}
	// And the repair flag only for a repair run, for the same reason:
	// unset is the ordinary run, whose base_branch_status answers read
	// exactly as they always have (agent.RunConfig.Repair).
	if cfg.Repair {
		args = append(args, "-pr-repair")
	}
	// And this one only for a repository that has declared GitHub's
	// checks are not a source of verdicts for it, on the same terms:
	// unset is every other repository, whose CI reports read exactly as
	// they always have (agent.RunConfig.GitHubCIIgnored).
	if cfg.GitHubCIIgnored {
		args = append(args, "-pr-ignore-github-ci")
	}
	if cfg.BaseIncident != "" {
		args = append(args, "-pr-base-incident", cfg.BaseIncident)
	}
	if cfg.PullRequestNumber > 0 {
		args = append(args, "-pr-number", fmt.Sprintf("%d", cfg.PullRequestNumber))
	}
	// And the credential the branch is pushed with, by name, so the
	// subprocess's own GitHub reads are made as the same account rather
	// than off the deployment's ladder (agent.RunConfig.GitCredential).
	if cfg.GitCredential != "" {
		args = append(args, "-pr-credential", cfg.GitCredential)
	}
	if f.githubHost != "" {
		args = append(args, "-github-host", f.githubHost)
	}
	if f.githubInsecure {
		args = append(args, "-github-insecure-http")
	}
	return args
}

// Run implements agent.Framework: it writes an --mcp-config file pointing
// at grainBinaryPath and cfg.SandboxRoot or cfg.GranuleVM (mcpServerArgs),
// runs claude -p with its native tool roster emptied out and only the
// grain-sandbox MCP tools admitted, and parses the resulting
// --output-format stream-json transcript into a Result.
//
// When cfg.TranscriptPath is set, the raw stream-json this produces is
// also mirrored there live, one line per event, as claude itself emits
// them (hostRunner.Run's own doc comment) -- the file this package's own
// LiveTranscriptDir reads back with PartialTranscript to render a
// still-running run's transcript-in-progress. It is opened O_TRUNC, not
// O_APPEND: a path is only ever reused across runs by a caller passing
// the same run ID twice, which should never happen, and starting clean
// is what makes a stale previous run's bytes never a way to misread this
// one's. The file is left on disk once Run returns either way -- cleaning
// it up once its contents no longer matter (the run has finished and
// Result.Transcript already carries the same story) is the caller's job,
// the same way orchestrator.RunDispatch owns cfg.SandboxRoot's own
// lifecycle rather than this package.
func (f *Framework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	mcpArgs, err := f.mcpServerArgs(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if f.grainBinaryPath == "" {
		return nil, fmt.Errorf("claude: grainBinaryPath is required")
	}
	maxTurns := f.maxTurns
	if cfg.MaxTurns > 0 {
		maxTurns = cfg.MaxTurns
	}

	var tee io.Writer
	if cfg.TranscriptPath != "" {
		// 0600: the raw stream holds every tool result the model saw --
		// file contents, command output, anything secret-shaped a sandbox
		// printed -- and its only reader is the daemon's own UI server, in
		// this same process (LiveTranscriptDir).
		transcriptFile, err := os.OpenFile(cfg.TranscriptPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("claude: opening transcript file: %w", err)
		}
		defer transcriptFile.Close()
		tee = transcriptFile
	}

	servers, releaseServers, err := agent.CarryCapabilityServers(f.host, cfg.CapabilityMCPServers)
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	defer releaseServers()
	var configPath string
	if f.startMCP != nil {
		endpoint, err := f.startMCP(ctx, f.grainBinaryPath, mcpArgs)
		if err != nil {
			return nil, fmt.Errorf("claude: starting the MCP endpoint: %w", err)
		}
		defer endpoint.Close()
		configPath, err = writeMCPConfigJSON(ctx, f.host, httpMCPConfigJSON(endpoint.URL, endpoint.Token, servers...))
		if err != nil {
			return nil, err
		}
	} else if configPath, err = writeMCPConfig(ctx, f.host, f.grainBinaryPath, mcpArgs, servers...); err != nil {
		return nil, err
	}
	// Removed with the run's own cancellation deliberately dropped: this
	// defer is reached with ctx already cancelled about as often as not
	// (a turn cap, a deadline, an operator), and a removal that
	// inherited that would leave the config behind on whichever machine
	// the run used.
	//
	// Kept, as everything staged below is, for a run this daemon detached
	// from as it stopped: the CLI is still running against it, and the
	// daemon that adopts the run never learns its path (agent.ErrDetached).
	// It goes with the run host's staging directory instead.
	detached := false
	defer func() {
		if detached {
			return
		}
		removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		f.host.RemoveAll(removeCtx, configPath)
	}()

	// --tools '' empties claude's native tool roster entirely (confirmed
	// live in v1 -- dispatch.py's own docstring: "--tools '' --mcp-config
	// <file> --allowedTools <names> genuinely empties the roster down to
	// exactly the MCP tools"), and --strict-mcp-config plus --allowedTools
	// admit only the grain-sandbox tools named above -- unlike v1, nothing
	// here re-admits a native "Task" tool for subagents, since v2 has no
	// self-debug/self-repair story yet for one to inherit.
	// When running on an unconfined remote host, native tools are preserved.
	args := []string{
		"-p",
	}
	if !isUnconfined(f.host) {
		args = append(args,
			"--tools", "",
			"--mcp-config", configPath,
			"--strict-mcp-config",
			"--allowedTools", strings.Join(allowedTools(cfg.CapabilityMCPServers...), ","),
		)
	} else {
		args = append(args,
			"--mcp-config", configPath,
		)
	}
	args = append(args,
		"--output-format", "stream-json",
		"--verbose",
	)
	// --max-turns is deliberately *not* here: it is the one flag that
	// differs between the attempts of a single run, since a retry gets
	// what is left of the run's budget rather than a fresh copy of it
	// (attemptArgs). Omitted entirely rather than passed as a large
	// number when no cap is configured: "unlimited" is a thing the CLI
	// already does by default, and a big number is still a cliff (see
	// defaultMaxTurns).
	if f.model != "" {
		args = append(args, "--model", f.model)
	}
	// The stop rule's Stop hook (stophook.go, agent.DecideStop): a turn
	// that ends promising more work, or a background run that ends before
	// any closing move, is sent back once. Each block is appended to a
	// file staged here and read back onto the Result below. A host that
	// cannot stage the file still gets the hook, unrecorded.
	// Removed, like the MCP config, unless the run was detached.
	stopRecord, err := stageStopRecord(ctx, f.host)
	if err != nil {
		stopRecord = ""
	}
	stopSettings, err := writeStopSettings(ctx, f.host, f.grainBinaryPath, cfg.Interactive, stopRecord)
	if err != nil {
		stopSettings = ""
	} else {
		args = append(args, "--settings", stopSettings)
	}
	defer func() {
		if detached {
			return
		}
		removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		for _, p := range []string{stopRecord, stopSettings} {
			if p != "" {
				f.host.RemoveAll(removeCtx, p)
			}
		}
	}()
	env := append(operatorEnv(), mcpToolTimeout()...)
	env = append(env, disableAutoMemory()...)
	if f.useVertex {
		env = append(env, "CLAUDE_CODE_USE_VERTEX=1")
		if f.oauthToken != nil {
			cred, err := f.oauthToken(ctx)
			if err != nil {
				return nil, fmt.Errorf("claude: reading the Claude Code credential: %w", err)
			}
			if cred != "" {
				vc, err := setupVertexCredentials(ctx, f.host, cred)
				if err != nil {
					return nil, err
				}
				if vc.cleanup != nil {
					defer func() {
						if !detached {
							vc.cleanup()
						}
					}()
				}
				env = append(env, vc.env...)
			}
		}
	} else if f.oauthToken != nil {
		token, err := f.oauthToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("claude: reading the Claude Code OAuth token: %w", err)
		}
		if token != "" {
			env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
		}
	}

	// The prompt travels over stdin, never argv -- untrusted issue content
	// must never become a shell- or ps-visible argument (dispatch.py's own
	// docstring makes the identical point about its `dd`/stdin-redirect
	// path).
	//
	// More than one exec, when the provider refuses this run mid-flight:
	// the sandbox, the config and claude's own session store are still
	// here at that moment, so the run is picked up in place -- --resume
	// on the session claude named, and the nudge below instead of the
	// prompt -- rather than costing a dispatch and a fresh clone. See
	// retry.go, including what happens on a build that will not take the
	// flag.
	attempts := f.runAttempts(ctx, attemptSetup{
		args:          args,
		env:           env,
		prompt:        cfg.Prompt,
		resumePrompt:  agent.ResumePrompt,
		restartPrompt: agent.RestartPreamble + cfg.Prompt,
		// The conversation a *previous dispatch* of this task left
		// suspended, if the caller kept one: this run's first exec joins
		// it rather than starting the task from nothing, and sends the
		// whole prompt behind agent.SuspendedPreamble because neither
		// the task nor its sandbox is what that conversation last saw.
		// See retry.go's header for why claude is the one framework that
		// can do this at all, and agent.RunConfig.ResumeSession for what
		// it is worth.
		resumeSession:   cfg.ResumeSession,
		suspendedPrompt: cfg.ResumeMessage(),
		tee:             tee,
		onSession:       cfg.OnResumableSession,
		maxTurns:        maxTurns,
	})
	stdout, runErr, notes := attempts.capture, attempts.runErr, attempts.notes
	if errors.Is(runErr, agent.ErrDetached) {
		detached = true
		return nil, runErr
	}
	// The last attempt's own events, not a parse of the whole capture:
	// one capture holds a stream per exec, and what the run ended as is
	// what its last attempt said. See attemptsResult.events.
	result, err := endRun(stdout, attempts.events, runErr, notes, attempts.session, maxTurns)
	readCtx, cancelRead := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancelRead()
	withStopBlocks(result, readStopBlocks(readCtx, f.host, stopRecord))
	return result, err
}

// ParseResult implements agent.ResultParser: the Result and error Run
// would have returned for a run whose stream is stdout and which ended
// with runErr, for a daemon adopting a run another daemon started in a
// run host (docs/non-disruptive-upgrade.md, Stage 2). stdout is one
// exec's stream -- a run host holds one -- so it is its own last attempt,
// and the session it names is the one the run leaves resumable.
func (f *Framework) ParseResult(stdout string, runErr error) (*agent.Result, error) {
	events := parseEvents(stdout)
	return endRun(stdout, events, runErr, nil, events.sessionID, f.maxTurns)
}

// ParseAttempts implements agent.AttemptsParser: ParseResult for a run
// that retried before the daemon watching it stopped, whose earlier
// attempts' streams are earlier. The capture is every attempt's, as
// Run's is; what the run ended as is the last attempt's, and the session
// it leaves resumable is the last one any attempt named.
func (f *Framework) ParseAttempts(earlier, stdout string, runErr error) (*agent.Result, error) {
	var capture strings.Builder
	appendCapture(&capture, earlier)
	appendCapture(&capture, stdout)
	events := parseEvents(stdout)
	session := events.sessionID
	if session == "" {
		session = parseEvents(earlier).sessionID
	}
	return endRun(capture.String(), events, runErr, earlierRetryNotes(earlier), session, f.maxTurns)
}

// earlierRetryNotes is the retry loop's own notes for an adopted run's
// earlier attempts, which Run would have written at the end of the
// transcript had the same daemon watched the whole run: one
// agent.RetryNote per attempt that was retried, saying whether the next
// one resumed a session. The daemon that wrote them is gone, so they are
// rebuilt from the streams. An attempt starts at claude's "system"/"init"
// event, the first line of every stream-json run; a refused resume,
// which never reached it, is folded into the attempt before it.
func earlierRetryNotes(earlier string) []string {
	if strings.TrimSpace(earlier) == "" {
		return nil
	}
	var attempts []string
	var current strings.Builder
	for _, line := range strings.SplitAfter(earlier, "\n") {
		if current.Len() > 0 && parseEvents(line).sawInit {
			attempts = append(attempts, current.String())
			current.Reset()
		}
		current.WriteString(line)
	}
	attempts = append(attempts, current.String())
	var notes []string
	session := ""
	for i, a := range attempts {
		if id := parseEvents(a).sessionID; id != "" {
			session = id
		}
		notes = append(notes, agent.RetryNote(i+2, session != ""))
	}
	return notes
}

// endRun is the end of Run, shared with ParseResult: what one run's
// capture and its last attempt's events say the run came to.
func endRun(stdout string, events *parsedEvents, runErr error, notes []string, session string, maxTurns int) (*agent.Result, error) {
	result, parseErr := parseTranscript(stdout)
	// Whatever the retry loop had to say about itself travels back with
	// the run whichever way it ended, so a transcript that appears to
	// restart half way through says why -- see retry.go.
	//
	// The session claude ran in travels back the same way, and for a
	// nearer reason: it is what a *later* dispatch of this task can be
	// resumed into (agent.Result.ResumableSession, model.
	// SuspendedSession). Attached here rather than only on the success
	// path because the runs worth resuming are the ones that ended
	// badly -- cut off by the wall clock, stopped by a usage limit --
	// and those come back through the arms below as a partial result
	// beside an error. Nothing is attached to a nil Result: a run that
	// did nothing at all leaves the caller no object to carry it, and
	// the next dispatch starts the task afresh exactly as it does today.
	annotate := func(r *agent.Result) *agent.Result {
		if r == nil {
			return nil
		}
		for _, note := range notes {
			r.Transcript = appendNote(r.Transcript, note)
		}
		r.ResumableSession = session
		return r
	}
	switch {
	case runErr != nil:
		// partialResult, not nil: a run that pushed a branch before
		// claude exited non-zero has already changed the world, and
		// agent.Framework's own contract is that the caller gets both
		// halves. Returning nil here is what used to strand that work --
		// and, because orchestrator.RunDispatch only records a
		// transcript for a non-nil Result and then removes the live
		// mirror it had been rendering, what used to make a failed run's
		// transcript vanish from the UI the moment it failed.
		return annotate(partialResult(result, stdout)), runFailure(events, maxTurns, runErr)
	case parseErr != nil:
		// A capture with no terminal result event: claude exited 0 but
		// its output was truncated mid-stream. Same reasoning as above --
		// whatever it did get done still comes back.
		return annotate(partialResult(result, stdout)), parseErr
	}
	// A run claude ended by reporting the account's own usage limit --
	// exit 0, is_error unset, the refusal delivered as the run's final
	// answer like any other. Nothing above notices that: the parse
	// succeeded and the "result" is a sentence. It is an
	// agent.UsageLimitError all the same, so that the deployment pauses
	// rather than sending the next task at the same wall -- see
	// usagelimit.go for why this path matches strictly and runFailure's
	// does not.
	//
	// result travels back alongside it, never instead of it: a run that
	// worked for an hour and met the limit on its last turn has already
	// pushed its branch.
	if limit := usageLimitFromResult(events.resultText); limit != nil {
		return annotate(result), limit
	}
	return annotate(result), nil
}

// runFailure says why a claude subprocess that exited non-zero failed,
// leading with the stream's own account of it -- p being the *last*
// attempt's events, since a run refused mid-flight execs claude more
// than once and what it ended as is what that attempt said
// (attemptsResult.events). claude reports a failed
// run as a terminal "result" event with is_error set (subtype
// error_max_turns, error_during_execution, ...) written to stdout, then
// exits 1 with nothing at all on stderr -- so runErr on its own renders
// as the useless "exit status 1 (stderr: )". The stream knows better,
// and agent.RunFailure keeps the exit status behind it rather than
// throwing it away; runErr is all there is for a subprocess that died
// before saying anything (a missing binary, a signal, a cancelled
// context).
func runFailure(p *parsedEvents, maxTurns int, runErr error) error {
	limit := usageLimitFromFailure(p.resultText, runErr)
	// The same evidence read for the other refusal a provider makes that
	// says nothing about this run: not "your account has no budget left"
	// but "come back in a moment, we are overloaded". See capacity.go.
	capacity := capacityFailure(p.resultText, runErr)
	switch {
	case p.resultSubtype == maxTurnsSubtype && maxTurns > 0:
		// Named in plain words rather than passed through as a subtype:
		// this is not a fault of claude or of the model but a configured
		// --max-turns budget running out, and the operator reading the
		// failed run needs to be told which number to raise. Worded by
		// agent.MaxTurnsExceeded, which every Framework shares: what
		// reads that sentence afterwards is model.EndingOf, not only a
		// human.
		return agent.MaxTurnsExceeded("claude", maxTurns)
	case p.resultSubtype == maxTurnsSubtype:
		// No cap was configured, so claude hit one of its own -- naming
		// a number grain never set would send an operator looking for a
		// setting that is already unlimited.
		return agent.MaxTurnsExceededByCLI("claude")
	case limit != nil:
		// Before p.resultErr below, which would otherwise render this as
		// a generic "run ended in error (subtype=...)" and lose the one
		// thing about it a deployment can act on: this run did not fail,
		// the account it ran as has no budget left until its window
		// resets. orchestrator.RunDispatch reads the type
		// (agent.UsageLimit) and pauses dispatch rather than retrying
		// into the same refusal.
		return limit
	case capacity != nil:
		// Before p.resultErr below for the same reason, and after the
		// limit above because a refusal that is both is a usage limit.
		// What this buys is narrower than a pause: the run is still
		// over, but orchestrator.RunDispatch records it as the
		// provider's doing (model.CapacityOutcome) rather than as this
		// task's fifth strike.
		return capacity
	}
	// Both halves when the stream had an account of its own: the
	// sentence claude wrote leads, and the exit status follows it rather
	// than being dropped, so that a reader can still tell a run claude
	// ended deliberately from one that died under it. With no account at
	// all this is the exit status alone, as before.
	return agent.RunFailure("claude", "claude", p.resultErr, runErr)
}

// partialResult is what Run hands back alongside an error: whatever the
// run managed to do before it failed.
//
// A run that produced no events at all gets a nil Result, not an empty
// one -- agent.Framework's own contract draws exactly that line ("a nil
// Result with an error means the run never started"), and callers depend
// on it: orchestrator.RunDispatch treats a non-nil Result as a run whose
// tool calls it must process, so an empty one for a run that never
// reached its sandbox would report an agent that touched it.
func partialResult(parsed *agent.Result, stdout string) *agent.Result {
	if parsed != nil {
		return parsed
	}
	p := parseEvents(stdout)
	if len(p.result.ToolCalls) == 0 && p.transcript.Len() == 0 {
		return nil
	}
	partial := p.result
	partial.Transcript = strings.TrimSpace(p.transcript.String())
	return &partial
}
