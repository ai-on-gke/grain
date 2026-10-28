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

// Package antigravity implements agent.Framework by running Google's
// Antigravity CLI -- the `agy` binary that replaced Gemini CLI -- as a
// subprocess on the controller. It is the replacement for this repo's own
// home-grown Gemini runtime (the former pkg/agent/gemini), which drove the
// Gemini API's function calling directly and looped tool calls in-process
// against its own pkg/mcp registry. Everything that runtime hand-rolled --
// the turn loop, the function-declaration translation, the tool dispatch --
// is agy's job now; what is left here is the same shape agent/claude
// already settled on for a real CLI: build the arguments, hand it a
// prompt, parse the transcript it streams back.
//
// The isolation argument is the same one agent/claude makes, and the
// reason the agent runs here rather than on the sandbox: the agent's own
// credential must never enter the untrusted execution environment, so agy
// runs on the controller and reaches the sandbox only through the MCP
// tools grain's own "mcpserver" subcommand exposes (cmd/grain/
// mcpserver.go). Nothing in a tool call itself can change where that
// lands, mirroring v1's "only place it's named" discipline (dispatch.py's
// _mcp_config_json).
//
// # Where this differs from agent/claude, and why it matters
//
// agy has no --mcp-config flag. Its MCP servers live in a per-user config
// file that `agy mcp add` edits (~/.gemini/config/mcp_config.json), and
// its tool manifests are cached under
// ~/.gemini/antigravity-cli/mcp/<server>/. A per-user registration is
// exactly the wrong shape here: two runs dispatched concurrently against
// two different sandboxes would share one registration, and whichever
// wrote it last would decide where both runs' tools landed. So Run gives
// each run its own private HOME (a temp directory holding just the two
// files agy reads out of one) instead of registering anything globally --
// see writeAgyHome. That keeps the per-run sandbox binding the dispatch
// model depends on, and leaves the controller's real ~/.gemini untouched.
//
// A HOME per run also meant a *conversation* per run: agy keeps its
// conversations under the HOME it was started with, so one deleted as Run
// returned took the run's conversation with it, and a task's next
// dispatch had nothing to resume. A deployment that configures a session
// store (WithSessionStore) gives each task a HOME of its own instead,
// kept between its dispatches -- still one HOME per task and never one
// per user, and still holding only this run's own MCP registration,
// because the files grain writes into it are rewritten when a run starts
// and removed when it ends either way. What survives is what agy itself
// wrote, which is the conversation. pkg/agent/sessionstore has the whole
// argument, including what bounds that directory; resume.go has what a
// run does with the conversation it finds there, and what about that is
// still unmeasured.
//
// A private HOME also decides how a run authenticates. agy's own
// credential is an OAuth session it stores under the HOME it was started
// with, which a run given a fresh directory by definition does not have;
// what it does have is the deployment's Gemini API key. agy reads that
// key from GEMINI_API_KEY, but only for a session whose settings file
// asks for it -- with no "modelProvider": "gemini" in
// ~/.gemini/antigravity-cli/settings.json it ignores the variable
// entirely, falls through to its interactive browser login, and (with a
// prompt on stdin rather than a terminal) exits 1 with "authentication
// required. Run 'agy' to log in, then retry." So writeAgyHome writes that
// setting alongside the MCP config whenever grain has a key to pass.
//
// A private HOME is also where this package tells agy how to put grain's
// tools in front of the model, which it does not do by default. agy loads
// an MCP server's tools lazily: they appear in no roster, and the only
// way to one is a native dispatcher, call_mcp_tool. A run configured that
// way reaches for agy's own 57 native tools instead -- which execute
// wherever agy does, on the controller -- and every call grain does see
// comes back named after the dispatcher rather than the tool. So the MCP
// config asks for every published tool with "eager": true, which
// registers each as a tool of agy's own under the name
// mcp.AgyQualifiedToolName spells (see eagerToolsConfig, and
// transcript.go for the two names a call can arrive under).
//
// agy caps a whole print-mode run at five minutes unless told otherwise,
// which ends a grain-shaped run long before it can push anything. Run
// passes --print-timeout derived from the deadline on its own context, so
// that grain's cancellation is always what stops a run; see printTimeout.
// That does not leave one model call unbounded: agy 1.2.2 grew an internal
// refusal ladder that was still running after five minutes, so turnDeadline
// cancels a model turn whose --log-file shows agy retrying a provider error
// once it passes the 100-second ceiling that used to bound the live probe,
// and gives a turn with no retries logged up to a much longer slow-turn
// ceiling. Tool calls pause that clock and keep their longer grain-owned
// deadlines; see turndeadline.go.
//
// agy also has no --max-turns. The cap RunConfig.MaxTurns asks for is
// therefore enforced here rather than by the binary: Run counts completed
// agent_response steps as they stream past and cancels the subprocess
// once the cap is reached (turnCap). procgroup.Prepare is what makes that
// cancellation reach agy's own MCP child too, rather than orphaning it.
//
// agy does cap a single MCP tool call, and there is no environment
// variable for it the way MCP_TOOL_TIMEOUT is for claude: the cap is a
// per-server "timeoutSeconds" key in the MCP config above, which is why
// mcpToolTimeoutSeconds writes one rather than Run adding to the
// subprocess environment. See that function for the measured default and
// why wait_for_checks makes leaving it alone the wrong answer.
package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/sessionstore"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

const (
	// DefaultEffort is the reasoning effort agy is asked for when a
	// caller names a model but no effort: the depth half of the model
	// selection, passed as --effort. A caller that names no model leaves
	// agy to its own default model ("auto" or no --model at all), and Run
	// passes no --effort with it (modelTakesNoEffort). grain names no
	// default model of its own: which models a deployment can run is its
	// provider's to say, not a name compiled in here
	// (docs/connections-and-models.md).
	//
	// A model selection is two values to agy, not one, and it accepts
	// them written either way -- measured against agy 1.1.26 with a live
	// key, every line of this checked by running it:
	//
	//	--model gemini-3.1-pro                 refused, "--model
	//	                                       gemini-3.1-pro requires
	//	                                       --effort (available: low,
	//	                                       high)"
	//	--model gemini-3.1-pro --effort high   runs
	//	--model gemini-3.1-pro-high            runs
	//	--model gemini-3.1-pro-high
	//	                       --effort high   runs
	//	--model gemini-3.1-pro-high
	//	                       --effort low    refused, "--model
	//	                                       gemini-3.1-pro-high
	//	                                       conflicts with
	//	                                       --effort=low"
	//	--model gemini-3.1-pro --effort medium refused, "gemini-3.1-pro
	//	                                       has no \"medium\" effort"
	//
	// So the two forms name the same run, and the pair is the one that
	// lets a deployment change the effort without touching the model.
	// `agy models` still lists the suffixed spelling
	// (gemini-3.1-pro-high, gemini-3.8-flash-medium), which is what a
	// deployment that named a model before this existed has stored, so
	// Run drops --effort for a model whose name already carries one --
	// see modelNamesItsEffort (and modelTakesNoEffort for "auto").
	//
	//
	// agy takes low, medium or high (`agy --help`), but which of them a
	// given model has is the model's own business -- Gemini 3.1 Pro
	// offers low and high and refuses medium, the Flash models offer all
	// three -- so a deployment changing one of these two settings
	// (`grain settings -gemini-effort`, Settings -> Models)
	// wants to know what the other one is. high, rather than agy's own
	// default, so that a run's depth on a model the deployment chose does
	// not change under it because the binary was upgraded.
	DefaultEffort = "high"

	// defaultMaxTurns is the cap on agentic turns a run may take before
	// turnCap stops it, and zero means no cap at all -- the same default
	// agent/claude carries, and for the same reason: a turn is one
	// model/tool round trip, the ordinary shape of a grain task spends
	// dozens of them, and a number picked here by a package that knows
	// nothing about the task does not slow a run down when it guesses
	// wrong, it fails one that was working.
	//
	// orchestrator's Config.MaxRunRuntime (two hours by default) is what
	// actually bounds a runaway run. A deployment that does want a turn
	// ceiling still sets one -- `grain settings -max-agent-turns`,
	// RunConfig.MaxTurns, or WithMaxTurns -- and turnCap enforces it
	// exactly as before; it already reads a non-positive max as "no cap"
	// (turnCap.Write), so nothing else here changes.
	defaultMaxTurns = 0

	// mcpServerName is the key written into agy's MCP settings, and
	// therefore the prefix agy reports every tool under
	// ("mcp__<name>__<tool>"). It lives in pkg/mcp rather than here
	// because both halves of that convention -- writing the prefix onto
	// the allowed-tools list, and taking it back off a reported call
	// before recording it (transcript.go) -- have to agree, and
	// agent/claude needs the identical pair for its own CLI.
	mcpServerName = mcp.ToolNamespace
)

// runner is the one seam this package needs to actually invoke agy --
// narrowed to an interface so a test can drive the parsing and wiring
// logic below without a real agy binary or a live credential (see
// testing.go, whose fake speaks the same stream-json protocol and runs
// the scripted tool calls for real). tee, when non-nil, receives every
// byte of stdout as the subprocess produces it, alongside the buffer this
// still accumulates and returns whole once the process exits.
type runner interface {
	Run(ctx context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (stdout string, err error)
}

type hostRunner struct {
	host    agent.Host
	agyPath string
}

// Run starts agy on whichever machine this Framework's host is -- a
// child of the daemon -- and, when tee is non-nil, mirrors its
// stdout into tee as the subprocess produces it rather than after it
// exits. That live mirror is what both RunConfig.TranscriptPath and the
// turn cap are built on: one needs a run's output on disk while it is
// still running, the other needs to count steps as they happen.
//
// Killing agy's own MCP child along with it (agy forks one the moment it
// loads the settings writeAgyHome wrote) is the Host's job and is done
// the same way on both: see agent.LocalHost.Run's procgroup.Prepare, and

func (r hostRunner) Run(ctx context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (string, error) {
	// stdout is returned alongside any error rather than discarded: a
	// run cancelled by the turn cap, or one agy failed partway through,
	// has already streamed the steps it completed, and Framework.Run
	// still owes its caller that record (see agent.Framework's own
	// contract).
	return r.host.Run(ctx, agent.Command{Path: r.agyPath, Args: args, Stdin: stdin, Env: env, Dir: dir}, tee)
}

// Framework implements agent.Framework by running `agy`.
type Framework struct {
	run runner
	// host is the machine agy is staged on and runs on: the daemon
	// itself (agent.LocalHost). Every file this
	// package writes for a run -- the private HOME, its config, the
	// diagnostic log -- is written through it rather than through os,
	// so that the CLI and the files it reads are always on the same
	// machine.
	host            agent.Host
	grainBinaryPath string
	// binPath is the agy binary itself, held here as well as inside the
	// runner above because an interactive session is not run through
	// that seam: TerminalCommand hands the argv to pkg/console, which
	// starts it on a pseudo-terminal of its own (agent.TerminalCommand).
	// A Framework built by a test's own runner has none, and refuses to
	// be driven by hand rather than pretending it can be.
	binPath           string
	apiKey            func(context.Context) (string, error)
	model             string
	effort            string
	maxTurns          int
	retryCutoff       time.Duration
	turnCeiling       time.Duration
	connection        string
	granuleSSHUser    string
	granuleExecKey    string
	granuleWorkspace  string
	githubDataDir     string
	githubHost        string
	githubInsecure    bool
	grainServerURL    string
	remoteSessionRoot string
	// sessions is where this run's HOME comes from when the task it
	// belongs to is allowed one that outlives the run
	// (WithSessionStore). nil -- no store configured, and every caller
	// that is not the daemon -- puts every run in a fresh temp HOME
	// deleted as Run returns, which is what this package did before the
	// store existed and is still what a run with no task id gets.
	sessions *sessionstore.Store

	// ambientPermissions leaves permission decisions to agy's configured
	// permission rules. The default is false, preserving unattended runs'
	// historical --dangerously-skip-permissions behavior.
	ambientPermissions bool

	// How many extra execs of agy one run may spend recovering from a
	// transient provider refusal, and how it waits between them --
	// agent/retry.go's own policy, which every framework that re-execs
	// its CLI shares. A test overrides its Sleep so the retry loop costs
	// the suite no wall clock.
	retry agent.RetryPolicy
}

// Option configures a Framework at construction time.
type Option func(*Framework)

// WithModel overrides the model agy is asked for.
func WithModel(model string) Option {
	return func(f *Framework) { f.model = model }
}

// WithEffort overrides the reasoning effort agy is asked for -- the
// other half of a model selection (DefaultEffort has the two forms agy
// accepts and how they were measured). Empty passes no
// --effort at all, leaving whichever effort the model name itself names,
// and a model name that already names one wins over this either way,
// since agy refuses the pair when they disagree.
func WithEffort(effort string) Option {
	return func(f *Framework) { f.effort = effort }
}

// WithMaxTurns sets the cap on agentic turns a single Run allows. Unlike
// agent/claude's option of the same name, this is not passed to the
// binary -- agy has no --max-turns -- but enforced by Run itself; see
// this package's own doc comment. Zero -- which is also the default --
// means no cap; see defaultMaxTurns.
func WithMaxTurns(n int) Option {
	return func(f *Framework) { f.maxTurns = n }
}

// WithAmbientPermissions leaves tool approval to agy's permission engine
// instead of passing --dangerously-skip-permissions. Grain still writes the
// per-run allow and deny rules and installs its tool hook; this option changes
// only whether agy bypasses those permission decisions. It is useful when the
// caller can provide approvals interactively or has configured rules that let
// a headless run proceed. The default keeps the bypass for compatibility with
// existing unattended dispatches.
func WithAmbientPermissions() Option {
	return func(f *Framework) { f.ambientPermissions = true }
}

// WithAPIKey sets the GEMINI_API_KEY passed to the agy subprocess's own
// environment -- never as a command-line argument, so it never lands in
// `ps` output (the same reasoning as claude.WithOAuthToken, and v1's
// CONTROLLER_AGENT_TOKEN_PATH before it). This is also what makes the
// private HOME writeAgyHome hands agy workable: a run authenticating by
// env var needs nothing out of the real ~/.gemini it no longer sees --
// but only together with the settings file writeAgyHome writes for it,
// since agy ignores the variable unless it is asked to use it.
func WithAPIKey(key string) Option {
	return WithAPIKeyFunc(func(context.Context) (string, error) { return key, nil })
}

// WithAPIKeyFunc is WithAPIKey for a deployment whose key is not known
// once, at construction: fn is called on every Run instead, so a key set
// (or replaced) while the daemon is running takes effect on the next run
// rather than at the next restart. cmd/grain's daemon reads it out of the
// secrets database the UI writes to, which is what makes "paste the key
// into Settings" work at all (see that package's own agentCredential).
//
// An error from fn fails the run: a key that cannot be read is not the
// same as a deployment that deliberately configured none, which stays
// what an empty string means here (Run then passes no GEMINI_API_KEY at
// all and lets the subprocess authenticate from its own ambient
// environment).
func WithAPIKeyFunc(fn func(context.Context) (string, error)) Option {
	return func(f *Framework) { f.apiKey = fn }
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

// WithHost overrides the host the agent CLI is executed on.
func WithHost(host agent.Host) Option {
	return func(f *Framework) {
		if host != nil {
			f.host = host
		}
	}
}

// WithGitHubAccess is agent/claude's option of the same name: it gives
// the forked "mcpserver" subprocess the -data-dir/-github-host/
// -github-insecure-http it needs to answer pull_request_status from this
// controller's own secrets/github ladder. See that package's own doc
// comment, and pkg/mcp/pullrequest_tools.go for why reading CI here does
// not put GitHub inside the sandbox.
func WithGitHubAccess(dataDir, host string, insecureHTTP bool) Option {
	return func(f *Framework) {
		f.githubDataDir, f.githubHost, f.githubInsecure = dataDir, host, insecureHTTP
	}
}

// WithGrainServer names the running "grain daemon"'s own UI/API base URL
// (e.g. "http://127.0.0.1:8420") for the forked "mcpserver" subprocess to
// reach it at -- which, together with a RunConfig.TaskID, is what gives a
// run the open_pull_request tool: agent/claude's option of the same name,
// for the same reason, since both frameworks fork the identical server.
//
// Unset leaves that tool unregistered and every run exactly as it was: a
// pushed branch still becomes a pull request when the run finishes.
func WithGrainServer(url string) Option {
	return func(f *Framework) { f.grainServerURL = url }
}

// WithSessionStore gives this Framework the per-task directory a run's
// HOME is taken from, so that the conversation one run leaves behind is
// still there for the task's next dispatch to be resumed into
// (agent.Result.ResumableSession, model.SuspendedSession).
//
// Unset -- every caller but the daemon, and a daemon with no data
// directory to put one in -- is this package as it was: a fresh HOME per
// run, deleted as Run returns, a run that asks to resume nothing and
// reports no conversation back. That is not a degraded mode; it is the
// isolation this package has always had, and the store is deliberately
// something a deployment opts into by configuring where it goes. See
// pkg/agent/sessionstore for what is kept there, what is still rebuilt
// per run, and who prunes it.
func WithSessionStore(store *sessionstore.Store) Option {
	return func(f *Framework) { f.sessions = store }
}

// WithTurnLimits sets the model-turn deadline's two limits for the
// connection this Framework runs on (turndeadline.go): turnTimeout is the
// slow-turn ceiling a turn with no provider retries logged may run to,
// retryCutoff the point past which a turn agy is logging retries for is
// cancelled. Zero leaves either at its default. connection names the
// connection in the message a timed-out turn is reported with, so an
// operator knows whose setting to change.
func WithTurnLimits(turnTimeout, retryCutoff time.Duration, connection string) Option {
	return func(f *Framework) {
		if turnTimeout > 0 {
			f.turnCeiling = turnTimeout
		}
		if retryCutoff > 0 {
			f.retryCutoff = retryCutoff
		}
		f.connection = connection
	}
}

// TurnLimits reports the slow-turn ceiling, the retry cutoff and the
// connection name this Framework's model-turn deadline will apply.
func (f *Framework) TurnLimits() (turnTimeout, retryCutoff time.Duration, connection string) {
	return f.turnCeiling, f.retryCutoff, f.connection
}

// WithRemoteSessionRoot configures the root directory for session stores
// when running against a remote executor host (defaulting to "~/.grain/sessions").
func WithRemoteSessionRoot(dir string) Option {
	return func(f *Framework) { f.remoteSessionRoot = dir }
}

const defaultRemoteSessionRoot = "~/.grain/sessions"

func (f *Framework) effectiveRemoteSessionRoot() string {
	if f.remoteSessionRoot != "" {
		return f.remoteSessionRoot
	}
	return defaultRemoteSessionRoot
}

// Asserted here rather than left to the one call site that type-asserts
// for it (orchestrator.frameworkOpensPullRequests): a Framework that
// stopped implementing this would otherwise not fail to compile, it
// would quietly stop telling its runs about a tool they still have.
var _ agent.PullRequestFramework = (*Framework)(nil)

// CanOpenPullRequest implements agent.PullRequestFramework, exactly as
// agent/claude's method of the same name does and for the same reason:
// only a Framework built WithGrainServer passes its forked mcpserver the
// -server/-task pair open_pull_request is registered on, so only it can
// tell orchestrator.BuildPrompt that the tool will be there.
func (f *Framework) CanOpenPullRequest() bool { return f.grainServerURL != "" }

// New builds a Framework that runs the real agy binary at agyPath
// (typically just "agy", resolved against $PATH) and points every run's
// MCP settings at grainBinaryPath -- the same grain binary this process
// itself is (typically the result of os.Executable()) -- invoked as
// "grainBinaryPath mcpserver -sandbox-root <root>".
func New(agyPath, grainBinaryPath string, opts ...Option) *Framework {
	f := newFramework(nil, grainBinaryPath, opts...)
	f.binPath = agyPath
	// After the options rather than before, because WithHost is one of
	// them: which machine agy runs on decides what the runner is.
	f.run = hostRunner{host: f.host, agyPath: agyPath}
	return f
}

func newFramework(run runner, grainBinaryPath string, opts ...Option) *Framework {
	f := &Framework{
		run:             run,
		host:            agent.LocalHost{},
		grainBinaryPath: grainBinaryPath,
		effort:          DefaultEffort,
		maxTurns:        defaultMaxTurns,
		retryCutoff:     DefaultModelRetryCutoff,
		turnCeiling:     DefaultModelTurnTimeout,

		retry: agent.RetryPolicy{Retries: agent.DefaultTransientRetries},
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// Efforts is the reasoning efforts agy's --effort accepts, in the order
// its own help text lists them ("Reasoning effort for the current CLI
// session (low|medium|high)"). Whether a given model offers all three is
// the model's own business and only agy knows it -- Gemini 3.1 Pro
// refuses medium -- so this is what can be checked without the binary,
// and it is what ui.UpdateSettings validates a saved effort against
// rather than letting a typo reach the run that fails on it.
func Efforts() []string { return []string{"low", "medium", "high"} }

// modelNamesItsEffort reports whether model is one of the catalog names
// that spells its reasoning effort into the model name itself
// (gemini-3.1-pro-high, gemini-3.8-flash-medium -- the only spelling
// `agy models` prints). Run passes no --effort alongside one of those;
// DefaultEffort has why.
func modelNamesItsEffort(model string) bool {
	effort, _ := splitEffort(model, Efforts())
	return effort != ""
}

// modelTakesNoEffort reports whether agy refuses any --effort alongside
// model: a name that carries its own (modelNamesItsEffort), or "auto" or
// no model at all, which leave agy to pick the model and with it the
// effort, and fail the run on --effort. `agy models` says nothing about which models take
// an effort, so "auto" is special-cased by name.
func modelTakesNoEffort(model string) bool {
	m := strings.TrimSpace(model)
	return m == "" || strings.EqualFold(m, "auto") || modelNamesItsEffort(model)
}

// Model is the model Run passes as --model, or "" for agy's own default.
func (f *Framework) Model() string { return f.model }

// Effort is the reasoning effort Run passes as --effort: the configured
// one (WithEffort), or "" for none -- which is also what a model that
// takes no effort gets, whatever was configured (modelTakesNoEffort).
func (f *Framework) Effort() string {
	if modelTakesNoEffort(f.model) {
		return ""
	}
	return f.effort
}

// publishedTools names every tool the "mcpserver" subcommand can
// advertise, bare, as that subcommand registers them.
//
// The names themselves are agent.PublishedToolNames -- one list, shared
// with agent/claude, computed from the constructors that register them
// rather than hand-copied, and its own doc comment says why a tool is
// named there whether or not this particular run will be offered it.
//
// What this list decides here is whether a run has grain's tools as
// tools at all: it is what eagerToolsConfig asks agy to register
// eagerly, and what HookDecision admits, so a tool left off it is not
// merely unlisted -- it is one the model can reach only through the
// call_mcp_tool fallback, and one the hook denies when it does. The
// spelling matters for the same reason the membership does: agy names a
// bare tool, so a name carrying claude's "mcp__grain-sandbox__" prefix
// is a tool this run does not have under a name nothing offers.
func publishedTools() []string {
	return agent.PublishedToolNames()
}

// eagerToolNames is publishedTools spelled the way agy reports a call to
// one of them once it has registered them eagerly -- what a transcript
// reader and the roster check below both have to recognize.
func eagerToolNames() []string {
	names := publishedTools()
	for i, name := range names {
		names[i] = mcp.AgyQualifiedToolName(name)
	}
	return names
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
			return nil, fmt.Errorf("antigravity: RunConfig.GranuleVM is set but this Framework has no granule SSH config (see WithGranuleSSH)")
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
		return nil, fmt.Errorf("antigravity: RunConfig.SandboxRoot, .GranuleVM, .Container, or .K8sPod is required")
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

// mcpToolTimeoutSlack is how far past the longest wait grain's own tools
// can do the per-server cap below is set. Enough for the round trips
// either side of the wait itself, and no more: the point is that the
// tool's own clock runs out first, not that agy stops capping tool calls.
const mcpToolTimeoutSlack = 2 * time.Minute

// mcpToolTimeoutSeconds is the "timeoutSeconds" this run's MCP server
// entry carries, and it is agent/claude's mcpToolTimeout in the one shape
// agy offers: a key in the config file rather than an environment
// variable, since agy reads no MCP_TOOL_TIMEOUT of its own.
//
// agy caps a single MCP tool call at three minutes when the key is
// absent, takes a positive value as that many seconds, and treats a
// negative one as no cap at all (measured against agy 1.1.25's own
// mcpcore.(*McpClientSessionInstance).callToolTimeout, which is also
// where "MCP tool call to server %q timed out after %s" comes from).
// Three minutes is far short of wait_for_checks, which blocks for as long
// as CI takes, up to an hour (mcp.MaxWaitForChecksTimeout) -- and of
// chat, which blocks for as long as the person it is waiting for takes to
// answer (mcp.ChatWait). The value is set from the longer of the two
// (mcp.MaxBlockingToolWait). Leaving the
// default in place would kill the call part-way through a wait the agent
// deliberately asked for and report it as a tool failure -- the one
// answer that is neither true nor useful, since the build it was waiting
// on is still running and the run now has no idea how it ended. Raising
// it to just past the tool's own maximum puts the deadline back where it
// belongs: on grain's side, where running out of time produces a report
// saying so.
//
// A finite value rather than the negative "never time out" agy also
// accepts, for the reason the slack above gives: an MCP server that has
// genuinely wedged should still end the call eventually.
func mcpToolTimeoutSeconds() int {
	return int((mcp.MaxBlockingToolWait + mcpToolTimeoutSlack).Seconds())
}

// printTimeoutSlack is how far past grain's own deadline for this run the
// --print-timeout below is set: enough that agy is never the thing that
// ends a run, and grain's own cancellation always gets there first.
const printTimeoutSlack = 10 * time.Minute

// defaultPrintTimeout is the cap for a run whose context carries no
// deadline at all -- a test, or a caller that means a run to take as long
// as it takes. Long rather than absent because agy's flag has no "never"
// value, and a day is past any run grain would leave in flight.
const defaultPrintTimeout = 24 * time.Hour

// printTimeout is the value of agy's --print-timeout for this run, and
// passing it at all is a bug fix rather than a tuning knob.
//
// agy's print mode caps a whole non-interactive run at five minutes by
// default ("--print-timeout ... (default 5m0s)"). It is a wall-clock cap
// on the run, not on a single model call: reaching it kills the run
// mid-tool-call and emits a terminal result event of
//
//	{"status":"ERROR","error":"timeout waiting for response"}
//
// with exit status 1. grain never passed the flag, so every dispatched
// antigravity run died five minutes in -- long enough to clone, read
// around and start editing, and far short of pushing anything -- and
// surfaced as this package's generic "run ended in status ERROR". The
// ordinary shape of a grain task is dozens of turns over tens of minutes;
// orchestrator.Config.MaxRunRuntime (two hours by default) is what is
// meant to bound it.
//
// So the cap is put back where the rest of this package already puts a
// deadline: on grain's side. The value tracks the deadline on the run's
// own context -- the moment grain will cancel it -- plus enough slack
// that agy's clock never runs out first. A run stopped by grain reports
// what stopped it; a run stopped by agy reports only that something timed
// out waiting.
func printTimeout(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return defaultPrintTimeout
	}
	// A deadline already past (or nearly) still gets the slack rather
	// than a zero or negative duration, which agy would reject: the run
	// is about to be cancelled either way, and by grain.
	return time.Until(deadline) + printTimeoutSlack
}

// eagerToolsConfig is the per-server "tools" map that decides how agy
// puts grain's tools in front of the model, and it is not an
// optimization: without it a run effectively has none of them.
//
// agy loads an MCP server's tools *lazily* by default. Lazily loaded
// tools are not tools as far as the model is concerned -- they are
// manifest files under ~/.gemini/antigravity-cli/mcp/<server>/, reachable
// only by way of one native dispatcher, call_mcp_tool, taking
// {"ServerName", "ToolName", "Arguments"}. Measured against agy 1.1.25
// that has two consequences, and both of them break a grain run:
//
//   - The model prefers agy's own 57 native tools, which it can see. Told
//     to run a command it calls agy's *native* run_command, which executes
//     on the controller rather than through grain's mcpserver -- for a
//     granule-VM run, a machine the run was never meant to touch, and one
//     where the work it does lands nowhere.
//   - What grain records is the dispatcher. Every call comes back named
//     "call_mcp_tool", so orchestrator.ProcessResult -- which matches
//     ToolCall.Name against "ask_question", "comment_on_issue" and
//     "propose_task" exactly -- sees none of them, and a run that asked a
//     question or left a closing note is recorded as having done nothing.
//     (transcript.go unwraps that shape as well, since a model may still
//     take the dispatcher route, but the whole point here is that it
//     should not have to.)
//
// "eager": true per tool registers each one as a native tool of agy's
// own, named mcp_<server>_<tool> (mcp.AgyQualifiedToolName). The model
// then sees grain's tools beside agy's, and calls them by name.
func eagerToolsConfig() map[string]any {
	tools := map[string]any{}
	for _, name := range publishedTools() {
		tools[name] = map[string]any{"eager": true}
	}
	return tools
}

// mcpConfigJSON is the content of the file agy reads its MCP servers
// from: grainBinaryPath spawned with mcpArgs (built by mcpServerArgs
// above) -- the "mcpserver" argument selects the same subcommand
// mcpserver.go implements, so agy forking this exact command is what
// actually starts an MCP server, rather than needing a separately built
// binary on disk. The schema is Gemini CLI's own mcpServers map, which
// agy inherited along with the ~/.gemini config directory, plus the
// per-server timeoutSeconds and tools keys agy added to it.
//
// Those two keys have to be the right JSON *type*, and getting one wrong
// is not a partial load: agy drops the whole server entry and reports "No
// MCP servers configured", with no error, no warning and nothing in the
// file to say which key did it. Measured in docs/agy-surface.md, one key
// at a time -- `tools` as a list rather than the object eagerToolsConfig
// builds drops the server, `timeoutSeconds` as a string drops the server,
// and a key agy has never heard of is ignored without harm. So the
// asymmetry to hold on to is that an unknown key here is safe and a known
// key of the wrong type costs a run every tool it has.
//
// Each capability MCP server (docs/capabilities-spec.md §5.2.4) is an
// entry beside grain-sandbox, its serverUrl grain's proxy and every tool
// its tools/list reported registered eagerly the same way.
func mcpConfigJSON(grainBinaryPath string, mcpArgs []string, servers ...agent.CapabilityMCPServer) ([]byte, error) {
	all := map[string]any{
		mcpServerName: map[string]any{
			"command":        grainBinaryPath,
			"args":           mcpArgs,
			"timeoutSeconds": mcpToolTimeoutSeconds(),
			"tools":          eagerToolsConfig(),
		},
	}
	for _, s := range servers {
		tools := map[string]any{}
		for _, tool := range s.Tools {
			tools[tool] = map[string]any{"eager": true}
		}
		all[s.Name] = map[string]any{
			"serverUrl":      s.URL,
			"timeoutSeconds": mcpToolTimeoutSeconds(),
			"tools":          tools,
		}
	}
	return json.Marshal(map[string]any{"mcpServers": all})
}

// capabilityToolName is how agy names a capability server's tool in the
// model's roster and in the calls its hook sees: mcp_<server>_<tool>, the
// spelling mcp.AgyQualifiedToolName gives grain-sandbox's.
func capabilityToolName(server, tool string) string { return "mcp_" + server + "_" + tool }

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

// isRemoteHost reports whether h is a remote host rather than the local controller.
func isRemoteHost(h agent.Host) bool {
	if h == nil {
		return false
	}
	if lh, ok := h.(interface{ IsLocal() bool }); ok && lh.IsLocal() {
		return false
	}
	if _, ok := h.(agent.LocalHost); ok {
		return false
	}
	if _, ok := h.(*agent.LocalHost); ok {
		return false
	}
	return true
}

// settingsJSON is the content of agy's own settings file: the permission
// rules that tell agy which tools this run may use, plus -- for a run
// that authenticates with a Gemini API key -- the one setting that makes
// agy read GEMINI_API_KEY at all rather than expecting the OAuth session
// a private HOME cannot have (see this package's doc comment).
//
// Nothing else is written into it. agy merges what it finds with its own
// defaults, so naming only the settings this package actually depends on
// leaves every other CLI behaviour at whatever the installed binary
// chose.
func settingsJSON(provider string, unconfined ...bool) ([]byte, error) {
	isUnc := false
	if len(unconfined) > 0 && unconfined[0] {
		isUnc = true
	}
	return settingsJSONWith(provider, isUnc, nil)
}

// settingsJSONWith is settingsJSON for a run whose capability grants
// expose MCP servers: each of their tools is allowed by an
// mcp(<server>/<tool>) rule beside grain's own.
func settingsJSONWith(provider string, isUnc bool, servers []agent.CapabilityMCPServer) ([]byte, error) {
	rules := permissionRules(isUnc)
	for _, s := range servers {
		for _, tool := range s.Tools {
			rules["allow"] = append(rules["allow"].([]string), "mcp("+s.Name+"/"+tool+")")
		}
	}
	settings := map[string]any{"permissions": rules}
	if provider != "" {
		settings["modelProvider"] = provider
	}
	return json.Marshal(settings)
}

// permissionRules is the "permissions" block of that settings file.
//
// AGY 1.2 validates every entry as an action(target) resource. Grain
// allows each tool published by its private MCP server and denies every
// documented native resource class. Run uses always-proceed mode by default
// for compatibility, while WithAmbientPermissions leaves these decisions to
// agy. TestLiveRunWithAmbientPermissions is the credentialed proof that
// request-review mode admits the MCP resources; HookDecision remains the
// independently enforced boundary and allows only grain's exact MCP tools.
//
// AGY 1.2 replaced bare tool-name grants with typed action(target)
// resources. MCP tools are addressed by their configured server and
// protocol tool names, not by the mcp_<server>_<tool> spelling used in the
// model's eager tool roster. Native access is denied by resource class,
// which also covers new native implementations of those actions.
var deniedNativeResources = []string{
	"command(*)",
	"unsandboxed(*)",
	"read_file(*)",
	"write_file(*)",
	"read_url(*)",
	"execute_url(*)",
}

func agyMCPPermission(tool string) string {
	return "mcp(" + mcp.ToolNamespace + "/" + tool + ")"
}

func permissionRules(unconfined ...bool) map[string]any {
	isUnc := false
	if len(unconfined) > 0 && unconfined[0] {
		isUnc = true
	}
	var allow []string
	for _, name := range publishedTools() {
		allow = append(allow, agyMCPPermission(name))
	}
	rules := map[string]any{
		"allow": allow,
	}
	if !isUnc {
		rules["deny"] = append([]string(nil), deniedNativeResources...)
	}
	return rules
}

// The three paths, inside the private HOME writeAgyHome builds, that agy
// reads its MCP servers, its own settings and its lifecycle hooks from.
// Named once, here, because they are the whole of what this package
// depends on in agy's on-disk layout rather than in its command line or
// its output: if a future agy moves one of them, these three vars are the
// change.
//
// They are not all in one directory, and that is not an oversight: agy
// keeps MCP servers in ~/.gemini/config/mcp_config.json (what `agy mcp
// add` edits and `agy mcp list` reads) and CLI settings in
// ~/.gemini/antigravity-cli/settings.json. Written anywhere else --
// ~/.gemini/settings.json, say, which is where Gemini CLI kept both --
// they are silently ignored: `agy mcp list` reports "No MCP servers
// configured", and a run authenticates as though no API key had been
// configured at all. hooks.json sits beside mcp_config.json because both
// are "customizations" in agy's own vocabulary, and ~/.gemini/config is
// the global customization root its shipped guide names (the binary
// unpacks that guide into a fresh HOME under
// antigravity-cli/builtin/skills/agy-customizations/, which is where the
// hook contract quoted in hookConfigJSON comes from).
//
// Settings and MCP servers are read from exactly one path each, but hooks
// are not, and the difference is worth knowing about: 1.1.26 loads
// hooks.json from ~/.gemini/config/, ~/.gemini/antigravity-cli/ and
// ~/.gemini/ alike, and a file at the antigravity-cli path *replaces* the
// one here rather than merging with it (measured both ways in
// docs/agy-surface.md, which is also where "planted together" and
// "planted one at a time" first disagreed). Nothing bites today, because
// writeAgyHome builds the whole HOME per run and agy unpacks no hooks.json
// of its own into a fresh one. It would bite the first time a run is given
// a HOME it does not own outright -- a reused HOME, a mounted one, a
// future agy that ships a default -- and it would bite silently, with the
// run's own hook simply absent. `agy -p /hooks` is the check, and
// TestLiveAgyLoadsGrainsHookConfig is where it is already asked.
var (
	mcpConfigRelPath   = filepath.Join(".gemini", "config", "mcp_config.json")
	cliSettingsRelPath = filepath.Join(".gemini", "antigravity-cli", "settings.json")
	hooksConfigRelPath = filepath.Join(".gemini", "config", "hooks.json")
)

const recordedToolsFilename = "recorded-tools.jsonl"

// hookConfigJSON is the content of agy's hooks.json: one PreToolUse hook,
// matching every tool, whose command is grain forking itself back into
// HookDecision (cmd/grain's "agy-tool-hook" subcommand, the same
// fork-myself trick mcpConfigJSON plays for the MCP server).
//
// This is the one denial agy documents as a denial, and the one that has
// been watched working: a live agy 1.1.26 driving gemini-3.1-pro-high,
// told in as many words to run `echo ... > /tmp/agyprobe.txt` as a shell
// command, called its own run_command and then its own write_to_file and
// find_by_name, had all three blocked by this hook, and gave up with the
// file never created. Its own shipped guide
// (builtin/skills/agy-customizations/docs/hooks.md, unpacked into any
// fresh HOME) gives PreToolUse the contract "use to gate, block, or audit
// tool executions": the hook is handed {"toolCall": {"name": ..., "args":
// ...}} on stdin before the tool runs, and a reply of {"decision":
// "deny"} is spelled out as "hard block the execution immediately" --
// which is a different mechanism from the permission prompt that
// --dangerously-skip-permissions auto-approves, and the reason this is
// here as well as permissionRules.
//
// The matcher is "*" rather than a list of tool names because the names
// it matches on are agy's step types, and the name grain must decide on
// is the one inside the payload; HookDecision reads that instead. See
// there for why the decision is a deny list rather than an allow list,
// and what a run does when this hook does not work at all.
//
// The schema is agy's own: a map from hook name to its per-event
// configuration, each event a list of {matcher, hooks} groups. It is not
// validated on load -- a hooks.json agy cannot make sense of leaves a run
// with no hooks and no complaint -- so what proves this file is the shape
// agy reads is `agy -p /hooks`, which prints the loaded hooks the way
// -p /permissions prints the loaded rules.
func hookConfigJSON(grainBinaryPath string, args ...string) ([]byte, error) {
	command := shellQuote(grainBinaryPath) + " " + HookSubcommand
	for _, arg := range args {
		command += " " + shellQuote(arg)
	}
	return json.Marshal(map[string]any{
		HookName: map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "*",
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": command,
					// Seconds. A fork of grain that only reads stdin and
					// writes a decision does not take one of these, but a
					// loaded controller can be slow to start a process,
					// and agy's own default is 30.
					"timeout": 30,
				}},
			}},
		},
	})
}

// HookName is what this run's hook is called in hooks.json. agy merges
// hooks from every source it finds by name, so a name of grain's own
// keeps this one distinct from anything a future agy ships built in.
//
// Exported because it is also how a real agy reports the hook back
// (`agy -p /hooks` prints this name in the first field of the record it
// loaded), and the live test that reads that output should match on this
// constant rather than on a second copy of the string.
const HookName = "grain-native-tool-denial"

// HookSubcommand is the argv[1] cmd/grain matches to run HookDecision.
// Named here rather than there because this package is what writes it
// into agy's config, and the two must agree.
const HookSubcommand = "agy-tool-hook"

// shellQuote wraps a path for the `sh -c` agy runs a hook command
// through. Deployments put grain in /usr/local/bin, but a path is not
// grain's to assume.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// HookDecision answers one PreToolUse payload: it returns the JSON agy
// reads back on stdout, and it is the whole of what cmd/grain's
// "agy-tool-hook" subcommand does.
//
// This hook is the fail-closed half of the boundary. It sees every tool
// call and allows only exact, eagerly registered tools published by
// grain's private MCP server. AGY's native roster can grow independently
// of this repository, so enumerating the native tools to deny would make
// every newly added tool an unreviewed controller capability.
//
// Saying "allow" here would bypass the independent permission layer.
// Grain's tools therefore receive no decision and continue through AGY's
// normal permission check; every other name, and every malformed payload,
// receives a hard denial.
func HookDecision(payload []byte) []byte {
	return HookDecisionFor(HookOptions{}, payload)
}

// HookOptions is what one run's hook is told about that run, from the
// arguments hookConfigJSON wrote into its command (ParseHookArgs). The
// zero value is every run without a host-tool grant, and decides
// exactly what HookDecision always has.
type HookOptions struct {
	// Grants are the hook-relevant grants this run holds. Only a
	// registered HostToolGrant's Capability means anything, and what it
	// admits is that grant's Tools -- a list in code, never in these
	// options, so no argument can name a host tool into the allowlist.
	Grants []string
	// RecordPath is the JSON-lines file every admitted host-tool call is
	// appended to before it is allowed (HostToolCall). A call that cannot
	// be recorded is denied: host-side calls must never go unrecorded.
	RecordPath string
	// TaskID and RunID are written into each record.
	TaskID, RunID string
	// CapabilityTools are the tools of this run's capability MCP servers
	// (agent.RunConfig.CapabilityMCPServers), each "<server>/<tool>":
	// admitted like grain's own, and nothing else is.
	CapabilityTools []string
}

// The flags hookArgs writes and ParseHookArgs reads.
const (
	hookFlagGrant  = "-grant"
	hookFlagRecord = "-record"
	hookFlagTask   = "-task"
	hookFlagRun    = "-run"
	// hookFlagCapabilityTool names one "<server>/<tool>" a capability
	// grant's MCP server reported.
	hookFlagCapabilityTool = "-capability-tool"
)

// hookArgs is the argument list a run's hook command carries after
// HookSubcommand: nothing at all for a run without a host-tool grant
// (HostToolGrant), so its hooks.json is byte-for-byte what it always was.
//
// A run whose capability grants expose MCP servers carries one
// -capability-tool per tool they reported, so the hook admits them.
func hookArgs(cfg agent.RunConfig, home string) []string {
	var args []string
	for _, s := range cfg.CapabilityMCPServers {
		for _, tool := range s.Tools {
			args = append(args, hookFlagCapabilityTool, s.Name+"/"+tool)
		}
	}
	held := heldHostToolGrants(cfg.Grants)
	if len(held) == 0 {
		return args
	}
	for _, g := range held {
		args = append(args, hookFlagGrant, g.Capability)
	}
	args = append(args, hookFlagRecord, filepath.Join(runScratchDir(home), hostToolCallsFilename))
	if cfg.TaskID != "" {
		args = append(args, hookFlagTask, cfg.TaskID)
	}
	if cfg.RunID != "" {
		args = append(args, hookFlagRun, cfg.RunID)
	}
	return args
}

// ParseHookArgs reads what hookArgs wrote. An argument it does not
// recognize is skipped rather than fatal, because the hook must answer
// every call whatever it was started with (cmd/grain's agyToolHook), and
// returned in ignored for the caller to report; an unrecognized grant is
// kept but admits nothing.
func ParseHookArgs(args []string) (opts HookOptions, ignored []string) {
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			ignored = append(ignored, args[i])
			break
		}
		switch args[i] {
		case hookFlagGrant:
			opts.Grants = append(opts.Grants, args[i+1])
		case hookFlagRecord:
			opts.RecordPath = args[i+1]
		case hookFlagTask:
			opts.TaskID = args[i+1]
		case hookFlagRun:
			opts.RunID = args[i+1]
		case hookFlagCapabilityTool:
			opts.CapabilityTools = append(opts.CapabilityTools, args[i+1])
		default:
			ignored = append(ignored, args[i])
			continue
		}
		i++
	}
	return opts, ignored
}

// ServeHook is the whole of cmd/grain's HookSubcommand: it reads one
// PreToolUse payload from stdin, writes HookDecisionFor's reply for the
// options args carry (ParseHookArgs) to stdout, and says on stderr which
// arguments it did not recognize.
//
// It never fails and writes nothing at all for a call it has no opinion
// about, because agy takes a hook that exits non-zero, or writes
// anything but a decision, as a failed tool call (noOpinion). So a
// payload it cannot read is answered like any other unrecognized call,
// and an argument it does not know is reported rather than fatal.
func ServeHook(args []string, stdin io.Reader, stdout, stderr io.Writer) {
	payload, err := io.ReadAll(stdin)
	if err != nil {
		payload = nil
	}
	opts, ignored := ParseHookArgs(args)
	if len(ignored) > 0 {
		fmt.Fprintf(stderr, "grain %s: ignoring unrecognized arguments %q\n", HookSubcommand, ignored)
	}
	stdout.Write(HookDecisionFor(opts, payload))
}

// allowsHostTool returns the grant opts holds that admits the host tool
// name, if there is one.
func (opts HookOptions) allowsHostTool(name string) (HostToolGrant, bool) {
	for _, g := range heldHostToolGrants(opts.Grants) {
		if slices.Contains(g.Tools, name) {
			return g, true
		}
	}
	return HostToolGrant{}, false
}

// hostToolCallsFilename is where, inside a run's scratch directory, the
// hook records the host-tool calls it admitted.
const hostToolCallsFilename = "host-tool-calls.jsonl"

// HostToolCall is one admitted host-tool call as the hook recorded it.
type HostToolCall struct {
	At    time.Time      `json:"at"`
	Task  string         `json:"task,omitempty"`
	Run   string         `json:"run,omitempty"`
	Host  string         `json:"host,omitempty"`
	Tool  string         `json:"tool"`
	Query string         `json:"query,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
}

// recordHostToolCall appends call to path as one JSON line.
func recordHostToolCall(path string, call HostToolCall) error {
	if path == "" {
		return fmt.Errorf("no record path")
	}
	line, err := json.Marshal(call)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// hostToolQuery is the part of a host tool's arguments worth a line in
// the daemon log: its query, or the whole of its arguments when it names
// none.
func hostToolQuery(args map[string]any) string {
	for _, key := range []string{"query", "Query", "q"} {
		if q, ok := args[key].(string); ok {
			return q
		}
	}
	if len(args) == 0 {
		return ""
	}
	b, _ := json.Marshal(args)
	return string(b)
}

// HookDecisionFor is HookDecision for a run whose hook was started with
// opts: grain's own MCP tools get no opinion, as always; for a run holding
// a registered HostToolGrant, the host tools it names are recorded and
// then get no opinion too; everything else is denied.
func HookDecisionFor(opts HookOptions, payload []byte) []byte {
	var in struct {
		ToolCall struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		} `json:"toolCall"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return denyNonGrainTool("unrecognized tool call")
	}
	for _, tool := range publishedTools() {
		if in.ToolCall.Name == mcp.AgyQualifiedToolName(tool) {
			return noOpinion
		}
	}
	for _, st := range opts.CapabilityTools {
		server, tool, ok := strings.Cut(st, "/")
		if ok && server != "" && tool != "" && in.ToolCall.Name == capabilityToolName(server, tool) {
			return noOpinion
		}
	}
	if grant, ok := opts.allowsHostTool(in.ToolCall.Name); ok {
		host, _ := os.Hostname()
		call := HostToolCall{
			At: time.Now().UTC(), Task: opts.TaskID, Run: opts.RunID, Host: host,
			Tool: in.ToolCall.Name, Query: hostToolQuery(in.ToolCall.Args), Args: in.ToolCall.Args,
		}
		if err := recordHostToolCall(opts.RecordPath, call); err != nil {
			return denyReason(in.ToolCall.Name + " is allowed by this task's " + grant.Capability +
				" grant, but grain could not record the call (" + err.Error() + "), and an unrecorded call " +
				"on the machine hosting this session is not one grain will let through.")
		}
		return noOpinion
	}
	name := in.ToolCall.Name
	if name == "" {
		name = "unrecognized tool call"
	}
	return denyNonGrainTool(name)
}

func denyNonGrainTool(name string) []byte {
	return denyReason(name + " is not one of grain's sandbox-bound MCP tools and cannot run on the " +
		"machine hosting this session. Use " + joinNames(sandboxToolNames()) +
		" instead, or another " + mcp.AgyQualifiedToolName("") + "* tool.")
}

func denyReason(reason string) []byte {
	reply, err := json.Marshal(map[string]any{
		"decision": "deny",
		"reason":   reason,
	})
	if err != nil {
		// json.Marshal cannot fail for this string-only object. Preserve
		// fail-closed behavior if that invariant ever changes.
		return []byte(`{"decision":"deny","reason":"grain could not validate this tool call"}`)
	}
	return reply
}

// noOpinion leaves an exact grain MCP tool to AGY's permission engine:
// nothing at all, written on stdout before the hook exits successfully.
// Every other name and every unreadable payload is denied above.
//
// It used to be `{}`, on the reading that an object carrying no decision
// carries no opinion. Against a real agy 1.1.26 driving a real model that
// is precisely backwards, and it denied every tool call a run made --
// grain's own included, so a run so configured could do nothing at all
// except say why. The whole measured contract, since agy's own guide
// documents only the four decision values and not what it does with
// anything else:
//
//   - no output at all, exit 0: the call proceeds. This is "no opinion",
//     and it is the only spelling of it that is not also something else.
//   - {"decision":"allow"}: proceeds, skipping the permission prompt.
//   - {"decision":"ask"}: proceeds, the prompt being auto-approved by
//     Run's --dangerously-skip-permissions.
//   - {"decision":"deny","reason":r}: "tool call denied by pre-tool hook:
//     r", and the tool does not run. This is the one grain wants.
//   - {} or any object with no "decision": denied, with an empty reason.
//     An absent decision is not an abstention; it is a deny nobody
//     explained.
//   - output that is not JSON: the call fails with "failed to unmarshal
//     result from hook ... via protojson".
//   - a hook command that exits non-zero, or is not there at all: the
//     call fails with "JSON hook ... failed: command failed: exit status
//     N".
//
// The last two are why cmd/grain's subcommand exits 0 whatever happens
// and writes only this or a decision: a hook that errors does not fail
// open, it fails the tool call, so a broken grain binary would take the
// run's tools with it.
var noOpinion []byte

// providerFor determines the "modelProvider" value that turns
// GEMINI_API_KEY into a working credential.
func providerFor(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if strings.HasPrefix(apiKey, "ya29.") || strings.HasPrefix(apiKey, "{") {
		return "vertex"
	}
	if apiKey == "" {
		return ""
	}
	return "gemini"
}

// writeAgyHome builds the private HOME one run gets: a fresh directory
// containing the config naming this run's own MCP server, the settings
// file carrying this run's permission rules (and, when this run has an
// API key to authenticate with, the setting that makes agy use it), and
// the hooks.json that puts grain in front of every tool call. It returns
// that directory and a cleanup func the caller defers.
//
// A private HOME rather than `agy mcp add` is the whole point -- see this
// package's own doc comment on why a per-user registration cannot express
// a per-run sandbox binding. It has the same effect claude's
// --strict-mcp-config has there: the only MCP server this run can see is
// the one written here, because there is no other config file in the
// HOME it was given to find one in.
//
// store and taskID decide which directory that is: the task's own,
// kept between its dispatches so that the conversation in it survives
// with it, or the temp directory every run used to get. persistent says
// which it turned out to be, and is what Run reads before it asks to
// resume anything or reports a conversation back -- see openAgyHome.
func writeAgyHome(ctx context.Context, host agent.Host, store *sessionstore.Store, remoteSessionRoot, taskID, grainBinaryPath string, mcpArgs []string, provider string, cfg ...agent.RunConfig) (home string, persistent bool, cleanup func(), err error) {
	home, persistent, cleanup, err = openAgyHome(ctx, host, store, remoteSessionRoot, taskID)
	if err != nil {
		return "", false, nil, err
	}
	if persistent {
		clearRunFiles(ctx, host, home)
	}
	recordedToolsPath := filepath.Join(runScratchDir(home), recordedToolsFilename)
	runMcpArgs := append(slices.Clone(mcpArgs), "-recorded-tools", recordedToolsPath)
	var runHookArgs []string
	var servers []agent.CapabilityMCPServer
	if len(cfg) > 0 {
		runHookArgs = hookArgs(cfg[0], home)
		servers = cfg[0].CapabilityMCPServers
	}
	files, err := agyHomeFilesWith(grainBinaryPath, runMcpArgs, provider, isUnconfined(host), servers, runHookArgs...)
	if err != nil {
		cleanup()
		return "", false, nil, err
	}
	for rel, content := range files {
		path := filepath.Join(home, rel)
		if err := host.MkdirAll(ctx, filepath.Dir(path), 0o700); err != nil {
			cleanup()
			return "", false, nil, fmt.Errorf("antigravity: creating agy config dir: %w", err)
		}
		if err := host.WriteFile(ctx, path, content, 0o600); err != nil {
			cleanup()
			return "", false, nil, fmt.Errorf("antigravity: writing %s: %w", rel, err)
		}
	}
	if err := host.MkdirAll(ctx, agyWorkspaceDir(home), 0o700); err != nil {
		cleanup()
		return "", false, nil, fmt.Errorf("antigravity: creating agy workspace: %w", err)
	}
	return home, persistent, cleanup, nil
}

// agyHomeFiles is what goes into that HOME, keyed by its path relative
// to it. Built apart from writing it because a run may have to write it
// more than once: see refreshAgyHome.
func agyHomeFiles(grainBinaryPath string, mcpArgs []string, provider string, isUnc bool, hookArgs ...string) (map[string][]byte, error) {
	return agyHomeFilesWith(grainBinaryPath, mcpArgs, provider, isUnc, nil, hookArgs...)
}

// agyHomeFilesWith is agyHomeFiles for a run whose capability grants
// expose MCP servers (agent.RunConfig.CapabilityMCPServers).
func agyHomeFilesWith(grainBinaryPath string, mcpArgs []string, provider string, isUnc bool, servers []agent.CapabilityMCPServer, hookArgs ...string) (map[string][]byte, error) {
	mcpConfig, err := mcpConfigJSON(grainBinaryPath, mcpArgs, servers...)
	if err != nil {
		return nil, fmt.Errorf("antigravity: building agy's mcp config: %w", err)
	}
	settings, err := settingsJSONWith(provider, isUnc, servers)
	if err != nil {
		return nil, fmt.Errorf("antigravity: building agy's settings: %w", err)
	}
	files := map[string][]byte{
		mcpConfigRelPath:   mcpConfig,
		cliSettingsRelPath: settings,
	}
	if !isUnc {
		hooks, err := hookConfigJSON(grainBinaryPath, hookArgs...)
		if err != nil {
			return nil, fmt.Errorf("antigravity: building agy's hook config: %w", err)
		}
		files[hooksConfigRelPath] = hooks
	}
	return files, nil
}

// refreshAgyHome puts this run's configuration back into a HOME agy has
// already been started in once, and it is what makes exec'ing agy a
// second time in the same HOME safe (resume.go).
//
// agy 1.1.26 rewrites settings.json as it starts, keeping the keys it
// owns -- modelProvider, which is what makes GEMINI_API_KEY a credential
// -- and dropping the permissions block writeAgyHome wrote entirely, so
// the rules live for exactly the launch that found them. That was
// harmless while a HOME saw one launch and a run passed
// --dangerously-skip-permissions anyway; it stops being harmless the
// moment either changes, and a second launch reading settings agy itself
// wrote would say nothing about it either way.
//
// So every attempt after the first gets the files written again, over
// whatever the last one left behind. Only those files: the conversation
// being resumed lives elsewhere under this same HOME
// (~/.gemini/antigravity-cli/conversations) and must survive.
// tests/e2e/live_hook_config_test.go is where agy's own behaviour here
// is measured.
func refreshAgyHome(ctx context.Context, host agent.Host, home, grainBinaryPath string, mcpArgs []string, provider string, cfg ...agent.RunConfig) error {
	recordedToolsPath := filepath.Join(runScratchDir(home), recordedToolsFilename)
	runMcpArgs := append(slices.Clone(mcpArgs), "-recorded-tools", recordedToolsPath)
	var runHookArgs []string
	var servers []agent.CapabilityMCPServer
	if len(cfg) > 0 {
		runHookArgs = hookArgs(cfg[0], home)
		servers = cfg[0].CapabilityMCPServers
	}
	files, err := agyHomeFilesWith(grainBinaryPath, runMcpArgs, provider, isUnconfined(host), servers, runHookArgs...)
	if err != nil {
		return err
	}
	for rel, content := range files {
		path := filepath.Join(home, rel)
		if err := host.MkdirAll(ctx, filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("antigravity: creating agy config dir: %w", err)
		}
		if err := host.WriteFile(ctx, path, content, 0o600); err != nil {
			return fmt.Errorf("antigravity: writing %s: %w", rel, err)
		}
	}
	return nil
}

// openAgyHome is the directory this run's HOME will be, and how to give
// it back.
//
// The task's own store when there is one (WithSessionStore): the same
// directory this task's last run was given, still holding the
// conversation that run ended in, which is the whole of what lets a
// second dispatch pick it up. Otherwise a fresh temp directory, exactly
// as every run got before the store existed -- a caller with no task id,
// a Framework built without a store, a second run of a task whose store
// somebody else is holding, and every probe and test double in here.
//
// What differs between the two is only *whose* directory it is and how
// long it lasts. The files grain writes into it are written the same way
// either way, per run, and are taken back out either way when the run
// ends: a persistent HOME is cleared of this run's MCP config, settings,
// hooks and scratch and keeps nothing but what agy itself wrote, and a
// temp HOME is removed whole. So no run ever starts against another
// run's MCP registration, which is what the private HOME is for (see
// this package's doc comment, and sessionstore's).
func openAgyHome(ctx context.Context, host agent.Host, store *sessionstore.Store, remoteSessionRoot, taskID string) (home string, persistent bool, cleanup func(), err error) {
	// The removals below outlive the run's own context on purpose: a
	// cleanup deferred by Run is reached with ctx already cancelled
	// about as often as not (a turn cap, a deadline, an operator), and a
	// removal that inherited that cancellation would leave a run's HOME
	// -- credentials and all -- behind on the machine it ran on, where
	// it used to be removed reliably by os.RemoveAll.
	detached := func(remove func(context.Context)) func() {
		return func() {
			removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), homeRemovalTimeout)
			defer cancel()
			remove(removeCtx)
		}
	}
	if store != nil && taskID != "" {
		if isRemoteHost(host) {
			root := remoteSessionRoot
			if root == "" {
				root = defaultRemoteSessionRoot
			}
			base := filepath.Join(root, frameworkName)
			pruneRemoteSessions(ctx, host, base)
			dir := filepath.Join(base, taskID)
			if err := host.MkdirAll(ctx, dir, 0o700); err == nil {
				return dir, true, detached(func(removeCtx context.Context) {
					clearRunFiles(removeCtx, host, dir)
				}), nil
			}
		} else {
			if dir, release, ok := store.Open(frameworkName, taskID); ok {
				return dir, true, detached(func(removeCtx context.Context) {
					clearRunFiles(removeCtx, host, dir)
					release()
				}), nil
			}
		}
	}
	home, err = host.MkdirTemp(ctx, "grain-agy-home-*")
	if err != nil {
		return "", false, nil, fmt.Errorf("antigravity: creating agy home: %w", err)
	}
	return home, false, detached(func(removeCtx context.Context) { host.RemoveAll(removeCtx, home) }), nil
}

// pruneRemoteSessions prunes stale session directories on a remote host.
// It removes directories older than 7 days, and ensures no more than 64 task sessions are kept.
// All errors are ignored, matching sessionstore.Store's prune contract.
func pruneRemoteSessions(ctx context.Context, host agent.Host, base string) {
	if host == nil || base == "" {
		return
	}
	cmdStr := fmt.Sprintf(`BASE=%q
case "$BASE" in
  "~"*) BASE="$HOME${BASE#\~}" ;;
esac
if [ -d "$BASE" ]; then
  find "$BASE" -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf {} + 2>/dev/null
  ls -1td "$BASE"/*/ 2>/dev/null | tail -n +65 | xargs -r rm -rf 2>/dev/null
fi`, base)

	pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	_, _ = host.Run(pruneCtx, agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", cmdStr},
	}, nil)
}

// clearRunFiles takes this run's own files back out of a HOME that
// outlives it: the three agyHomeFiles writes, and the scratch directory
// holding this run's diagnostic logs and its granule workspace. That is
// the whole of what grain puts in there -- everything else under that
// HOME was written by agy, and is the conversation the store exists to
// keep.
//
// Failures are not reported: this runs in a deferred cleanup, after the
// run has its answer, and the next run writes every one of these again
// before it starts agy anyway.
func clearRunFiles(ctx context.Context, host agent.Host, home string) {
	for _, rel := range []string{mcpConfigRelPath, cliSettingsRelPath, hooksConfigRelPath} {
		host.RemoveAll(ctx, filepath.Join(home, rel))
	}
	host.RemoveAll(ctx, runScratchDir(home))
}

// writeHomeFiles is the mechanics under writeAgyHome: the run's home,
// wherever it comes from, with each named file written under it, and the
// cleanup that gives it back. Shared with catalog.go's own smaller probe
// home, which wants the same private-HOME discipline with none of a
// run's per-run configuration in it -- and which passes no task, so it
// gets a temp directory as it always did.
func writeHomeFiles(ctx context.Context, host agent.Host, store *sessionstore.Store, remoteSessionRoot, taskID string, files map[string][]byte) (home string, persistent bool, cleanup func(), err error) {
	home, persistent, cleanup, err = openAgyHome(ctx, host, store, remoteSessionRoot, taskID)
	if err != nil {
		return "", false, nil, err
	}
	if persistent {
		// Whatever the previous run of this task left here, gone before
		// this run's own is written: a HOME that outlives a run may
		// still hold that run's MCP config, and a run must never be
		// started against another run's sandbox binding, however
		// certainly the writes below are about to replace it.
		clearRunFiles(ctx, host, home)
	}
	for rel, content := range files {
		path := filepath.Join(home, rel)
		if err := host.MkdirAll(ctx, filepath.Dir(path), 0o700); err != nil {
			cleanup()
			return "", false, nil, fmt.Errorf("antigravity: creating agy config dir: %w", err)
		}
		if err := host.WriteFile(ctx, path, content, 0o600); err != nil {
			cleanup()
			return "", false, nil, fmt.Errorf("antigravity: writing %s: %w", rel, err)
		}
	}
	return home, persistent, cleanup, nil
}

// homeRemovalTimeout bounds that removal. It is generous: the cost of
// waiting is one goroutine at the end of a run, and the cost of giving
// up early is a directory holding a credential on somebody else's
// machine.
const homeRemovalTimeout = time.Minute

// runScratchDir is where everything grain writes into a run's HOME that
// is not one of agyHomeFiles' three goes: this run's diagnostic logs and
// the workspace below.
//
// One directory rather than files scattered through the HOME, because a
// HOME that outlives the run has to be handed back holding nothing of
// this run's (clearRunFiles), and a single removal cannot miss one. The
// name is grain's own and agy has no opinion about it: what it reads out
// of a HOME is the three paths agyHomePaths names and its own trees.
func runScratchDir(home string) string { return filepath.Join(home, "grain-run") }

// agyWorkspaceDir is the empty directory inside a run's private HOME that
// workDir hands a granule run. See workDir.
func agyWorkspaceDir(home string) string { return filepath.Join(runScratchDir(home), "workspace") }

// frameworkName is what this package is called wherever a framework has
// to be named to something outside it -- the session store's own
// directory, and model.SuspendedSession.Framework, which is what stops a
// token this CLI wrote being handed to another one.
const frameworkName = "antigravity"

// workDir is the directory agy is started in.
//
// For a host-rooted sandbox that is the sandbox itself, as it has always
// been. For a granule run there is no local path to name -- the sandbox is
// a VM the forked mcpserver reaches over SSH -- and cfg.SandboxRoot is
// empty, which used to leave the subprocess in whatever directory the
// daemon itself was started in. That matters because agy brings its own
// native file and command tools, which run wherever it runs: a model
// reaching for run_command or write_to_file on a granule run would act on
// the controller, and in the daemon's own working directory at that. An
// empty scratch directory inside the run's private HOME is somewhere
// those tools can do no harm, and it disappears with the HOME.
//
// It is not a substitute for the guarantee: agy has no way to withhold
// its own tools. Measured against 1.1.26 by reading the shipped binary
// (see the README): no flag names a tool at all, enabledTools and
// disabledTools are keys on an MCP *server's* entry in mcp_config.json,
// a custom agent definition can replace the system prompt but not the
// tool roster, and settings.json holds the permission system --
// machinery for approving a call, which Run's
// --dangerously-skip-permissions switches off -- rather than a roster.
// So what steers a run to grain's tools is eager registration plus
// toolPreamble, and what actually contains it is a granule sandbox.
func workDir(cfg agent.RunConfig, home string) string {
	if cfg.SandboxRoot != "" {
		return cfg.SandboxRoot
	}
	return agyWorkspaceDir(home)
}

// withheldNativeTools names the AGY built-ins called out in the prompt as
// concrete examples of tools that run on the controller. It is not the
// security boundary: HookDecision rejects every tool outside grain's
// exact MCP roster, including native tools added after this list was
// written. The list remains useful for the prompt and for live tests that
// deliberately exercise known native calls.
var withheldNativeTools = []string{
	"run_command", "command_status", "send_command_input",
	"view_file", "write_to_file", "replace_file_content",
	"multi_replace_file_content", "sed_file", "list_dir",
	"grep_search", "find_by_name", "notebook_edit", "notebook_execution",
	"define_subagent", "invoke_subagent",
}

// IsWithheldNativeTool reports whether name is one of agy's own tools
// this package refuses -- spelled as agy spells it in a hook payload,
// which is bare rather than prefixed (see HookDecision on why the raw
// name is what matters).
//
// Exported for the live test that watches a real agy honour the refusal
// (tests/e2e/live_native_tools_test.go). That test has to tell a native
// call apart from grain's own in agy's own vocabulary, and a second copy
// of this list written out there would go stale the first time a name is
// added here.
func IsWithheldNativeTool(name string) bool {
	return slices.Contains(withheldNativeTools, name)
}

// sandboxToolNames is the handful of grain tools that actually act on the
// sandbox -- run_command, read_file, write_file, edit_file -- spelled the
// way agy names them once eagerly registered.
//
// Taken from the constructor rather than written out, for the reason
// publishedTools is: a tool added to (or renamed in) mcp.NewSandboxTools
// should change what the prompt tells a run it has, and nobody remembers
// to edit prose. The rest of the published tools are described by
// category instead of by name, because which of them a given run actually
// holds depends on its configuration (pull_request_status needs a repo,
// open_pull_request and recreate_sandbox a -server/-task pair, the
// selfdebug tools a grant), and a prompt that named a tool this run does
// not have would send it looking for one.
func sandboxToolNames() []string {
	var names []string
	for _, t := range mcp.NewSandboxTools("") {
		names = append(names, mcp.AgyQualifiedToolName(t.Name))
	}
	return names
}

// toolPreamble is the short note prepended to a run's prompt naming which
// tools reach its sandbox and which ones it must not use, and it exists
// because agy cannot be told to withhold its own.
//
// Every other framework grain drives can be handed an empty native tool
// roster (claude's --tools ” plus --strict-mcp-config), or one that can
// do no damage (codex's read-only sandbox_mode). agy offers neither, so a
// run always sees agy's own run_command, view_file, write_to_file,
// replace_file_content and the rest alongside grain's -- and those
// execute wherever agy itself is running, which is the controller. On a
// host-rooted sandbox that lands in the sandbox directory by accident,
// because it is also agy's working directory; on a granule run it does not
// land in the sandbox at all. Either way the call bypasses everything
// grain's own tools carry with them: the result caps, the timeout
// reporting, the remaining-runtime announcements, and the record of the
// call that orchestrator reads a run's outcome from.
//
// Saying so in the prompt is the only lever there is, so it says three
// things rather than one: which tools do the work here (by name, from the
// constructor), that the built-in ones are to be treated as unavailable
// (by name, so the model is not left to work out which of its own tools
// the rule covers), and that the two rosters share names -- agy's own
// run_command and grain's mcp_<server>_run_command are different tools on
// different machines, and a model that has both in front of it and
// notices neither the prefix nor the collision picks the wrong one while
// believing it picked the right one.
//
// Each is stated as what the tools do rather than as a rule for its own
// sake, because a model that understands why its native tools are the
// wrong ones here keeps choosing correctly in situations this text did
// not anticipate.
func toolPreamble(cfg agent.RunConfig) string {
	where := "your sandbox"
	if cfg.GranuleVM != "" {
		where = "your sandbox, which is a separate virtual machine"
	}
	prefix := mcp.AgyQualifiedToolName("")
	return "Use the " + prefix + "* tools for everything you do to " + where + ". " +
		joinNames(sandboxToolNames()) + " are the ones that reach " + where + "; " +
		"the rest of the " + prefix + "* tools are how you report back, ask a question, " +
		"read CI, or ask grain to open your pull request.\n" +
		"Treat every one of your own built-in tools as unavailable -- " +
		strings.Join(withheldNativeTools, ", ") + ", and anything else whose name does not begin " +
		prefix + ". They do not run in " + where + ": they run on the machine hosting this " +
		"session, which is not the sandbox and is not where your work belongs, and grain does " +
		"not see the calls, so anything you do with them is both in the wrong place and " +
		"unrecorded. The names collide, which is the trap -- your own run_command and " +
		mcp.AgyQualifiedToolName("run_command") + " are different tools on different machines, " +
		"and only the prefixed one does what you were asked to do." + hostToolException(cfg) + "\n" +
		"When leaving a closing note or report with " + mcp.AgyQualifiedToolName("comment_on_issue") +
		", include your full, complete, untruncated report directly in the comment parameter -- never truncate it, summarize it with an ellipsis (…), or save the rest for a conversational response, because grain relays only that tool argument into the conversation and any conversational text after tool calls is not relayed.\n\n"
}

// hostToolException is what toolPreamble adds, after it has finished
// saying why the built-ins are unavailable, for a run holding a host-tool
// grant: each held grant's Preamble, which names the host tools it
// admits, what they run as, and what to say when they fail on auth. ""
// for every other run, whose preamble is unchanged.
//
// It follows that whole explanation rather than sitting inside the list
// of built-ins it excepts: placed there, the next sentence's "They do not
// run in your sandbox ... grain does not see the calls ... unrecorded"
// read as being about these tools -- the opposite of what the clause had
// just said about them, since the hook records every one.
func hostToolException(cfg agent.RunConfig) string {
	var b strings.Builder
	for _, g := range heldHostToolGrants(cfg.Grants) {
		b.WriteString(g.Preamble)
	}
	return b.String()
}

// joinNames renders a tool list as prose: "a, b and c". A plain
// strings.Join would read as a fragment of JSON in the middle of a
// sentence the model is meant to take as an instruction.
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// userEvent is the one stdin line a `--input-format stream-json` run
// needs: the prompt, as a user turn. The prompt travels over stdin rather
// than as the argument to --print because untrusted issue content must
// never become a shell- or ps-visible argument (dispatch.py's own
// docstring makes the identical point about its stdin-redirect path, and
// agent/claude passes its prompt the same way). agy's other print mode,
// `--print <prompt>`, puts it in argv instead, which is why this package
// does not use it.
func userEvent(prompt string) (string, error) {
	line, err := json.Marshal(map[string]any{
		"event":   "user",
		"message": map[string]any{"role": "user", "content": prompt},
	})
	if err != nil {
		return "", fmt.Errorf("antigravity: encoding prompt: %w", err)
	}
	return string(line) + "\n", nil
}

// agyArgs is the argv for one exec of agy. ctx is this attempt's own
// context rather than the run's, so --print-timeout tracks what is left
// of the deadline rather than what was left when the run started;
// conversationID is empty for a first attempt and the session to resume
// for a retry (see resume.go).
func (f *Framework) agyArgs(ctx context.Context, cfg agent.RunConfig, conversationID string) []string {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
	}
	// Every dispatch passes this unless its caller explicitly asks agy to
	// use ambient permissions. permissionRules still supplies the per-run
	// policy in either mode.
	if !f.ambientPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args,
		"--disable-slash-commands",
		// Without this a run is over in five minutes; see printTimeout.
		"--print-timeout", printTimeout(ctx).String(),
	)
	if f.model != "" {
		args = append(args, "--model", f.model)
	}
	// The other half of the selection, and dropped for a model whose own
	// name already carries an effort: agy refuses that pair outright
	// unless the two happen to agree ("--model gemini-3.1-pro-high
	// conflicts with --effort=low"), and a deployment that stored a
	// suffixed catalog name -- the only spelling `agy models` prints --
	// has said which effort it wants in the model it named. "auto"
	// takes none either, and nor does a run that names no model at all
	// (modelTakesNoEffort). See DefaultEffort for the whole measured table.
	if effort := f.Effort(); effort != "" {
		args = append(args, "--effort", effort)
	}
	// --add-dir names the one directory this run may touch. It is only
	// meaningful for a host-rooted sandbox; a granule run reaches its
	// workspace through the forked mcpserver's SSH transport instead, and
	// there is no local path here to name.
	if cfg.SandboxRoot != "" {
		args = append(args, "--add-dir", cfg.SandboxRoot)
	}
	if conversationID != "" {
		args = append(args, "--conversation", conversationID)
	}
	return args
}

// Run implements agent.Framework: it writes this run's own MCP settings
// into a private HOME pointing at grainBinaryPath and cfg.SandboxRoot or
// cfg.GranuleVM (mcpServerArgs), runs agy in stream-json mode with the
// prompt on stdin, and parses the resulting stream back into a Result.
//
// Run may return a non-nil Result together with a non-nil error, and a
// caller must read both. A run that edits files, commits, pushes and only
// then trips the turn cap has already changed the world, and a caller
// that treats the error as "no result" strands that work -- the failure
// that taught this is recorded in agent.Framework's own contract, and it
// is why a cancelled or failed subprocess's partial stdout is still
// parsed here rather than discarded.
//
// When cfg.TranscriptPath is set, the raw stream-json is mirrored there
// live, one line per event, as agy emits them -- the file this package's
// own LiveTranscriptDir reads back with PartialTranscript to render a
// still-running run's transcript-in-progress. It is opened O_TRUNC, not
// O_APPEND: a path is only ever reused across runs by a caller passing
// the same run ID twice, which should never happen, and starting clean is
// what makes a stale previous run's bytes never a way to misread this
// one's. The file is left on disk once Run returns either way -- cleaning
// it up is the caller's job, the same way orchestrator.RunDispatch owns
// cfg.SandboxRoot's own lifecycle rather than this package.
//
// cfg.Addenda is not polled. The former in-process Gemini runtime could
// fold a comment posted mid-run into its next turn because it owned the
// turn loop; agy owns it now, and a `--print` run is one blocking
// subprocess call with its prompt written before the process starts. A
// comment posted while a run is in flight waits for the task's next
// dispatch instead, exactly as it already does under agent/claude -- see
// RunConfig.Addenda's own doc comment.
//
// # The native tool roster
//
// claude empties its own tool roster with --tools ” and admits only the
// grain-sandbox tools with --strict-mcp-config plus --allowedTools. agy
// exposes no equivalent switch: no --tools, no --strict-mcp-config, and
// its disabledTools setting applies to an MCP server's tools rather than
// its own. Every run therefore sees agy's native run_command, view_file,
// write_to_file and the rest alongside grain's, and those execute
// wherever agy does -- on the controller.
//
// Three things stand in for the switch that does not exist. The private
// HOME holds exactly one MCP server, so grain's tools are the only MCP
// tools there are. Those tools are registered eagerly, so the model can
// see them rather than having to go looking. And toolPreamble opens the
// prompt by naming both rosters: the grain tools that reach the sandbox,
// and the agy tools to treat as unavailable (withheldNativeTools),
// including the names the two rosters share. verifyToolRoster then
// reports -- as a transcript line, not a failure -- a roster with no
// route to grain's server at all.
//
// None of that is a guarantee, and it is not offered as one. A deployment
// that needs one should run agy against a granule sandbox (cfg.GranuleVM),
// where the controller's filesystem is not reachable from the guest at
// all; workDir gives such a run an empty scratch directory to start in,
// so a native tool reached for by mistake writes somewhere harmless.
func (f *Framework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	mcpArgs, err := f.mcpServerArgs(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if f.grainBinaryPath == "" {
		return nil, fmt.Errorf("antigravity: grainBinaryPath is required")
	}
	maxTurns := f.maxTurns
	if cfg.MaxTurns > 0 {
		maxTurns = cfg.MaxTurns
	}

	// Read before the HOME is built rather than just before the exec:
	// whether this run has a key of its own decides what goes into that
	// HOME, since a key agy is never told to look for is a key it does
	// not use (see writeAgyHome).
	apiKey, err := f.resolveAPIKey(ctx)
	if err != nil {
		return nil, err
	}
	// persistent says whether this run's HOME is the task's own, kept
	// between dispatches, or the temp directory that goes with the run.
	// It is what the two ends of a cross-dispatch resume hang off below:
	// a conversation only means anything while the HOME holding it
	// exists.
	servers, releaseServers, err := agent.CarryCapabilityServers(f.host, cfg.CapabilityMCPServers)
	if err != nil {
		return nil, fmt.Errorf("antigravity: %w", err)
	}
	defer releaseServers()
	cfg.CapabilityMCPServers = servers
	home, persistent, cleanup, err := writeAgyHome(ctx, f.host, f.sessions, f.effectiveRemoteSessionRoot(), cfg.TaskID, f.grainBinaryPath, mcpArgs, providerFor(apiKey), cfg)
	if err != nil {
		return nil, err
	}
	// Kept for a run this daemon detached from as it stopped
	// (agent.ErrDetached): agy is still running in grain hostd against
	// this HOME, and the daemon that adopts the run reads its stream
	// (ParseResult).
	detached := false
	defer func() {
		if !detached {
			cleanup()
		}
	}()

	var sinks []io.Writer
	if cfg.TranscriptPath != "" {
		// 0600: the raw stream holds every tool result the model saw --
		// file contents, command output, anything secret-shaped a sandbox
		// printed -- and its only reader is the daemon's own UI server, in
		// this same process (LiveTranscriptDir).
		transcriptFile, err := os.OpenFile(cfg.TranscriptPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("antigravity: opening transcript file: %w", err)
		}
		defer transcriptFile.Close()
		sinks = append(sinks, transcriptFile)
	}

	// The turn cap and model-turn deadline watch the same live stream the
	// transcript mirror does and cancel the attempt's own context. Both are
	// retargeted at each exec; the cap stays cumulative across them, while
	// the deadline starts fresh for the attempt's first turn. See
	// turncap.go and turndeadline.go.
	capWatch := &turnCap{max: maxTurns}
	diagnosticSecrets := []string{apiKey, os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY")}
	deadlineWatch := newTurnDeadline(f.retryCutoff, f.turnCeiling, f.connection)
	deadlineWatch.probe = func(path string) ([]string, error) {
		readCtx, cancel := context.WithTimeout(ctx, turnLogReadTimeout)
		defer cancel()
		records, _, err := diagnosticRecords(readCtx, f.host, path, diagnosticSecrets)
		return records, err
	}
	sinks = append(sinks, capWatch, deadlineWatch)
	// One sink for the whole run, opened once and reused by every
	// attempt: the transcript mirror above is O_TRUNC, so reopening the
	// path for a retry would throw away everything the run had already
	// streamed.
	live := io.MultiWriter(sinks...)

	// The private HOME, the key (with GOOGLE_API_KEY cleared beside it),
	// and the switch that stops agy replacing its own binary mid-run --
	// selfupdate.go has the reasoning for all three.
	env := agyEnv(home, apiKey)

	firstTurn, err := userEvent(toolPreamble(cfg) + cfg.Prompt)
	if err != nil {
		return nil, err
	}
	// The turn a resumed attempt sends instead; see agent.ResumePrompt
	// for why it is not the prompt again.
	nextTurn, err := userEvent(agent.ResumePrompt)
	if err != nil {
		return nil, err
	}
	// And the same for an attempt resumed after agy was cut off rather
	// than refused -- which says so instead of claiming a provider
	// refusal nothing here measured (agent.InterruptedPrompt, resume.go).
	interruptedTurn, err := userEvent(agent.InterruptedPrompt)
	if err != nil {
		return nil, err
	}
	// And the turn an attempt with no conversation to resume sends: the
	// task from the top, behind the paragraph saying the sandbox already
	// holds an earlier attempt's work (agent.RestartPreamble).
	restartTurn, err := userEvent(toolPreamble(cfg) + agent.RestartPreamble + cfg.Prompt)
	if err != nil {
		return nil, err
	}
	// And the turn a *new dispatch* sends into the conversation a
	// previous run of this task left suspended: when SuspendedPrompt is
	// supplied, only the delta since the suspended run (without repeating
	// toolPreamble or the static full prompt already in the conversation);
	// otherwise the whole prompt behind agent.SuspendedPreamble.
	suspendedBody := toolPreamble(cfg) + agent.SuspendedPreamble + cfg.Prompt
	if cfg.SuspendedPrompt != "" {
		suspendedBody = agent.SuspendedPreamble + cfg.SuspendedPrompt
	}
	// Or, for the stop rule's one resumed turn, the nudge alone
	// (agent.RunConfig.ContinuePrompt): the conversation ended seconds
	// ago in this same sandbox, and nothing else about it has changed.
	if cfg.ContinuePrompt != "" {
		suspendedBody = cfg.ContinuePrompt
	}
	suspendedTurn, err := userEvent(suspendedBody)
	if err != nil {
		return nil, err
	}

	// The conversation a previous dispatch left in this same HOME, and
	// only where this run was actually given that HOME: a token naming a
	// conversation in a directory that no longer exists is a refused
	// exec and nothing else.
	suspendedConversation := ""
	if persistent {
		suspendedConversation = cfg.ResumeSession
	}
	attempts := f.runAttempts(ctx, cfg, attemptSetup{
		// This run's own scratch, inside the HOME but never part of what
		// a HOME that outlives the run keeps (runScratchDir).
		diagnosticHome:     runScratchDir(home),
		diagnosticSecrets:  diagnosticSecrets,
		env:                env,
		dir:                workDir(cfg, home),
		firstTurn:          firstTurn,
		nextTurn:           nextTurn,
		interruptedTurn:    interruptedTurn,
		restartTurn:        restartTurn,
		suspendedTurn:      suspendedTurn,
		deltaSuspended:     cfg.SuspendedPrompt != "",
		resumeConversation: suspendedConversation,
		// Whether a conversation this run ends in is worth handing to
		// the next dispatch at all: in a temp HOME it dies with the run,
		// and reporting it would cost that dispatch an exec and show a
		// person a resume token that resumes nothing.
		keepConversation: persistent,
		live:             live,
		onConversation:   cfg.OnResumableSession,
		cap:              capWatch,
		deadline:         deadlineWatch,
		refresh: func() error {
			return refreshAgyHome(ctx, f.host, home, f.grainBinaryPath, mcpArgs, providerFor(apiKey), cfg)
		},
	})
	stdout, runErr, diagnostic, notes := attempts.capture, attempts.runErr, attempts.diagnostic, attempts.notes
	if errors.Is(runErr, agent.ErrDetached) {
		detached = true
		return nil, runErr
	}
	turnTimedOut := attempts.turnTimedOut
	turnVerdict := attempts.turnVerdict

	recordedTools := loadRecordedTools(filepath.Join(runScratchDir(home), recordedToolsFilename))
	hostCalls := loadHostToolCalls(filepath.Join(runScratchDir(home), hostToolCallsFilename))
	result, parseErr := parseTranscriptWithRecorded(stdout, recordedTools)
	// Whatever the retry loop had to say about itself travels back with
	// the run whichever way it ended, so a transcript that appears to
	// restart half way through says why -- see resume.go.
	//
	// The conversation agy ran in travels back the same way, and for a
	// nearer reason: it is what a *later* dispatch of this task can be
	// resumed into (agent.Result.ResumableSession, model.
	// SuspendedSession). Attached here rather than only on the success
	// path because the runs worth resuming are the ones that ended badly
	// -- cut off by the wall clock, stopped by a usage limit, parked
	// waiting for a person -- and those come back through the arms below
	// as a partial result beside an error. Empty unless this run's HOME
	// outlives it, which is the invariant the whole feature rests on.
	annotate := func(r *agent.Result) *agent.Result {
		if r == nil {
			return nil
		}
		for _, note := range notes {
			r.Transcript = appendRosterNote(r.Transcript, note)
		}
		for _, note := range reportHostToolCalls(r, hostCalls, hostName(f.host)) {
			r.Transcript = appendRosterNote(r.Transcript, note)
		}
		r.ResumableSession = attempts.conversation
		return r
	}
	// The last attempt's own events, not a parse of the whole capture:
	// one capture holds a stream per exec once a run has been refused or
	// resumed, and what the run ended as is what its last attempt said.
	// See attemptsResult.events. Read once here rather than at each
	// branch below, since several of them read the same pass: what agy
	// said about how the run ended is both the evidence a quota refusal
	// leaves and the sentence that explains an ordinary failure.
	events := attempts.events
	// Read from the terminal event's own text plus whatever the
	// subprocess itself reported, because a quota refusal can arrive
	// either way: agy usually reports it as a failed terminal status
	// (parseErr, below) but a hard enough refusal kills the process
	// first (runErr). See usagelimit.go.
	limit := usageLimitFailure(events.resultText+"\n"+diagnostic, runErr)
	// The same evidence read for the other refusal a provider makes that
	// says nothing about this run: not "you have spent your quota" but
	// "come back in a moment, the model is overloaded". See capacity.go.
	capacity := capacityFailure(events.resultText+"\n"+diagnostic, runErr)
	switch {
	case capWatch.tripped():
		// The cap cancelled the subprocess, so runErr is that
		// cancellation rather than a fault of agy's -- report the cap.
		// result is whatever the run managed before it was stopped, and
		// is returned alongside the error, never instead of it.
		return annotate(partialResult(result, stdout, recordedTools)), agent.MaxTurnsExceeded("antigravity", maxTurns)
	case limit != nil:
		// Ahead of both branches below, which would otherwise render
		// this as "running agy: exit status 1" or as agy's own generic
		// "run ended in status FAILURE", and lose the one thing about it
		// a deployment can act on: nothing is wrong with this run, the
		// credential it used has no quota left until its window resets.
		// orchestrator.RunDispatch reads the type (agent.UsageLimit) and
		// pauses dispatch rather than sending the next task at the same
		// refusal.
		//
		// result travels back alongside it, as everywhere else here: a
		// run that pushed a branch and then ran out of quota has already
		// changed the world.
		return annotate(partialResult(result, stdout, recordedTools)), limit
	case capacity != nil:
		// Ahead of both branches below for the same reason, and behind
		// the limit above because a refusal that is both is a quota
		// refusal. What this buys is narrower than a pause: the run is
		// still over, but orchestrator.RunDispatch records it as the
		// provider's doing (model.CapacityOutcome) rather than as this
		// task's fifth strike.
		//
		// result travels back alongside it, as everywhere else here: a
		// run that pushed a branch and was then refused has already
		// changed the world.
		return annotate(partialResult(result, stdout, recordedTools)), capacity
	case turnTimedOut:
		// A provider refusal still wins above: its diagnostic is why the
		// deadline cancelled this turn and the classification is the useful
		// result. With no such evidence, say what grain itself stopped.
		return annotate(partialResult(result, stdout, recordedTools)), fmt.Errorf("antigravity: %s", deadlineWatch.describe(turnVerdict))
	case runErr != nil && (!events.sawResult || events.resultErr != nil):
		// Both halves, not just the exit status. agy reports a run it
		// could not finish as a terminal result event with a status of
		// ERROR or FAILURE -- "There was a network issue connecting to
		// the server, please try again." -- and then exits 1 with
		// nothing at all on stderr, so this arm used to render the one
		// failure a reader can act on as "running agy: exit status 1
		// (stderr: )". events.resultErr is that sentence; the stream is
		// removed once the run finishes (orchestrator's own cleanup), so
		// what this returns is the last place it exists.
		reported := events.resultErr
		if diagnostic != "" {
			if reported != nil {
				reported = fmt.Errorf("%w\n\n%s", reported, strings.TrimSpace(diagnostic))
			} else {
				reported = fmt.Errorf("%s", strings.TrimSpace(diagnostic))
			}
		}
		return annotate(partialResult(result, stdout, recordedTools)), agent.RunFailure("antigravity", "agy", reported, runErr)
	case parseErr != nil:
		// A capture with no terminal result event -- an agy that died
		// without runErr reaching us, or output truncated mid-stream.
		// partialResult, not result: parseTranscript hands back nothing
		// in that case, and a run that pushed a branch before the stream
		// stopped must still come back with the push.
		return annotate(partialResult(result, stdout, recordedTools)), parseErr
	}
	result.Transcript = appendRosterNote(result.Transcript, verifyToolRoster(stdout))
	return annotate(result), nil
}

// ParseResult implements agent.ResultParser: the Result and error Run
// would have returned for a run whose stream is stdout and which ended
// with runErr, for a daemon adopting a host run another daemon started
// detached in grain hostd (docs/design/cli-run-location.md, "Upgrades and
// host runs"). stdout is the one exec hostd held. What Run reads from the
// run's HOME -- the recorded tool calls, the diagnostic log -- the
// adopting daemon does not have, so the stream alone decides.
func (f *Framework) ParseResult(stdout string, runErr error) (*agent.Result, error) {
	result, parseErr := parseTranscriptWithRecorded(stdout, nil)
	events := parseEvents(stdout)
	switch {
	case runErr != nil && (!events.sawResult || events.resultErr != nil):
		return partialResult(result, stdout, nil), agent.RunFailure("antigravity", "agy", events.resultErr, runErr)
	case parseErr != nil:
		return partialResult(result, stdout, nil), parseErr
	}
	result.Transcript = appendRosterNote(result.Transcript, verifyToolRoster(stdout))
	return result, nil
}

var _ agent.ResultParser = (*Framework)(nil)

// resolveAPIKey reads this run's Gemini API key, or "" for a deployment
// that configured none and means agy to authenticate from its own
// ambient environment (see WithAPIKeyFunc, whose contract this keeps: an
// error is a key that could not be read, which is not the same thing).
func (f *Framework) resolveAPIKey(ctx context.Context) (string, error) {
	if f.apiKey == nil {
		return "", nil
	}
	key, err := f.apiKey(ctx)
	if err != nil {
		return "", fmt.Errorf("antigravity: reading the API key: %w", err)
	}
	return key, nil
}

// partialResult is what Run hands back alongside an error: whatever the
// run managed to do before it failed or was stopped.
//
// A run that produced no events at all gets a nil Result, not an empty
// one -- agent.Framework's own contract draws exactly that line ("a nil
// Result with an error means the run never started"), and callers depend
// on it: orchestrator.RunDispatch treats a non-nil Result as a run whose
// tool calls it must process, so an empty one for a run cancelled before
// agy ever spoke would report an agent that touched a sandbox it never
// reached.
func partialResult(parsed *agent.Result, stdout string, recorded []mcp.ToolCallRecord) *agent.Result {
	if parsed != nil {
		return parsed
	}
	p := parseEventsWithRecorded(stdout, recorded)
	if len(p.result.ToolCalls) == 0 && p.transcript.Len() == 0 && p.result.FinalText == "" {
		return nil
	}
	partial := p.result
	if partial.FinalText == "" {
		partial.FinalText = p.lastAgentResponse()
	}
	partial.Transcript = strings.TrimSpace(p.transcript.String())
	return &partial
}

func loadRecordedTools(path string) []mcp.ToolCallRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var records []mcp.ToolCallRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec mcp.ToolCallRecord
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			records = append(records, rec)
		}
	}
	return records
}

// loadHostToolCalls reads back what the pre-tool hook recorded of the
// host-tool calls it admitted (HookDecisionFor). Read with os.ReadFile,
// as loadRecordedTools is: on a remote host the file stays where the hook
// wrote it, and the calls reach grain through the transcript alone.
func loadHostToolCalls(path string) []HostToolCall {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var calls []HostToolCall
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var call HostToolCall
		if err := json.Unmarshal([]byte(line), &call); err == nil {
			calls = append(calls, call)
		}
	}
	return calls
}

// hostName is what an operator would call the machine agy ran on: a
// remote run host's own name, or this machine's.
func hostName(h agent.Host) string {
	if n, ok := h.(interface{ Name() string }); ok && n.Name() != "" {
		return n.Name()
	}
	name, _ := os.Hostname()
	return name
}

// reportHostToolCalls puts the host-tool calls a HostToolGrant admitted
// on grain's record, and returns the transcript notes to say so.
//
// Each recorded call goes to the daemon log, with its task, run, tool and
// query. A recorded call the transcript did not carry -- a capture cut
// short -- is added to r.ToolCalls, so the run's tool census counts every
// call the hook let through whatever happened to the stream. And an
// allowlisted call that failed on auth gets its grant's AuthHint, since
// that is the operator's to fix on the host rather than the run's.
func reportHostToolCalls(r *agent.Result, calls []HostToolCall, fallbackHost string) []string {
	var notes []string
	seen := map[string]int{}
	for _, tc := range r.ToolCalls {
		if _, ok := hostToolGrantFor(tc.Name); ok {
			seen[tc.Name]++
		}
	}
	host := fallbackHost
	perGrant := map[string]int{}
	for _, call := range calls {
		grant, _ := hostToolGrantFor(call.Tool)
		perGrant[grant.Capability]++
		log.Printf("antigravity: %s host tool call: task=%s run=%s host=%s tool=%s query=%q",
			grantLabel(grant), call.Task, call.Run, call.Host, call.Tool, call.Query)
		if call.Host != "" {
			host = call.Host
		}
		if seen[call.Tool] > 0 {
			seen[call.Tool]--
			continue
		}
		r.ToolCalls = append(r.ToolCalls, agent.ToolCall{Name: call.Tool, Arguments: call.Args})
	}
	grants := registeredHostToolGrants()
	if perGrant[""] > 0 {
		// Recorded under a grant this binary no longer has registered.
		grants = append(grants, HostToolGrant{})
	}
	for _, g := range grants {
		if n := perGrant[g.Capability]; n > 0 {
			identity := g.HostIdentity
			if identity == "" {
				identity = "own identity"
			}
			notes = append(notes, fmt.Sprintf("grain: %d host tool call(s) made under the %s grant, on %s, as that host's %s",
				n, grantLabel(g), host, identity))
		}
	}
	for _, tc := range r.ToolCalls {
		g, ok := hostToolGrantFor(tc.Name)
		if ok && tc.IsError && g.IsAuthFailure != nil && g.AuthHint != nil && g.IsAuthFailure(tc.Text) {
			hint := g.AuthHint(host)
			log.Printf("antigravity: %s failed on auth: %s", tc.Name, hint)
			notes = append(notes, "grain: "+tc.Name+" failed on auth -- "+hint)
			break
		}
	}
	return notes
}

// grantLabel is g's capability for a log line or a note, or a stand-in
// for a call recorded under a grant no longer registered.
func grantLabel(g HostToolGrant) string {
	if g.Capability == "" {
		return "host-tool"
	}
	return g.Capability
}

// verifyToolRoster reports whether agy's opening init event shows any way
// for this run to reach grain's tools, and says so when it does not.
//
// This used to ask the opposite question -- which advertised tools grain
// had not published -- on the theory that the roster was the run's whole
// tool vocabulary and so a stand-in for claude's --strict-mcp-config.
// Against the real binary that theory is simply wrong twice over. The
// roster is agy's own native tools and nothing else: MCP tools never
// appear in it, eagerly registered or not (measured on agy 1.1.25, which
// advertises 57 native tools and no grain tool under either spelling). So
// the old check both flagged all 57 of them on every single run, and
// missed the one thing worth noticing -- a run whose MCP server failed to
// load, which is invisible in that roster either way.
//
// What is visible is the bridge. call_mcp_tool is agy's dispatcher for
// lazily loaded MCP tools; an eagerly registered one appears under its
// own mcp_<server>_<tool> name (mcp.AgyQualifiedToolName). A roster with
// neither is a run that cannot reach grain at all, which is worth a line
// in the transcript -- as a note, not a failure, since the run may still
// have said something useful about why.
//
// An empty roster -- no init event, a capture that starts mid-stream --
// reports nothing: there is no roster to draw a conclusion from.
func verifyToolRoster(stdout string) string {
	roster := parseEvents(stdout).tools
	if len(roster) == 0 {
		return ""
	}
	eager := map[string]bool{}
	for _, name := range eagerToolNames() {
		eager[name] = true
	}
	for _, name := range roster {
		if name == mcpDispatcherTool || eager[name] {
			return ""
		}
	}
	return fmt.Sprintf("! agy advertised %d tool(s), none of them grain's and no %s to reach them "+
		"through: this run had no way to touch its sandbox or to report back",
		len(roster), mcpDispatcherTool)
}

// appendRosterNote records verifyToolRoster's finding in the transcript,
// where an operator reading a run will see it, rather than only in a log
// this deployment may not be collecting.
func appendRosterNote(transcript, note string) string {
	switch {
	case note == "":
		return transcript
	case transcript == "":
		return note
	}
	return transcript + "\n\n" + note
}
