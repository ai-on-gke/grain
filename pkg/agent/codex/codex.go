// Package codex implements agent.Framework by running OpenAI's Codex CLI
// -- the `codex` binary -- as a subprocess on the controller. It is the
// third implementation of the shape agent/claude settled on and
// agent/antigravity followed: build the arguments, hand the CLI a
// prompt, parse the transcript it streams back.
//
// The isolation argument is the one both of those make, and the reason
// the agent runs here rather than on the sandbox: the agent's own
// credential must never enter the untrusted execution environment, so
// codex runs on the controller and reaches the sandbox only through the
// MCP tools grain's own "mcpserver" subcommand exposes
// (pkg/cli/mcpserver.go). Nothing in a tool call itself can change
// where that lands, mirroring v1's "only place it's named" discipline
// (dispatch.py's _mcp_config_json).
//
// # Where this differs from the other two, and why it matters
//
// codex has no --mcp-config flag. Its MCP servers live in a config file
// (config.toml) inside its own config directory, which is ~/.codex
// unless CODEX_HOME names another. A per-user registration is exactly
// the wrong shape here, for the reason agent/antigravity's doc comment
// gives about agy: two runs dispatched concurrently against two
// different sandboxes would share one registration, and whichever wrote
// it last would decide where both runs' tools landed. So Run gives each
// run its own private CODEX_HOME -- a temp directory holding the config
// this run needs and, when configured, a link to one controller-owned
// auth.json -- instead of registering anything globally (writeConfig).
// That keeps the per-run sandbox binding the dispatch model depends on
// without copying shared login state or exposing the rest of the
// controller's real ~/.codex.
//
// A home per run also meant a *thread* per run: codex records its threads
// in that directory, so one deleted as Run returned took the run's
// conversation with it, and a task's next dispatch had nothing to resume.
// A deployment that configures a session store (WithSessionStore) gives
// each task a home of its own instead, kept between its dispatches --
// still one home per task and never one per user, and still holding only
// this run's own MCP registration, because the files grain writes into
// it are rewritten when a run starts and removed when it ends either
// way. What survives is what codex itself wrote, which is the
// conversation. pkg/agent/sessionstore has the whole argument, including
// what bounds that directory; retry.go has what a run does with the
// thread it finds there.
//
// codex also has no --max-turns. The cap RunConfig.MaxTurns asks for is
// therefore enforced here rather than by the binary, exactly as in
// agent/antigravity: Run counts completed assistant messages as they
// stream past and cancels the subprocess once the cap is reached
// (turnCap). Grain owns the MCP child and HTTP listener separately;
// the run's cleanup stops both, including on cancellation or failure.
//
// Codex gets no execution environments, not even a read-only local one.
// That removes environment-backed native tools from its dispatcher.
// Grain starts the stdio MCP child and exposes it on authenticated
// loopback HTTP, which does not require a Codex execution environment. Code Mode stays
// enabled for models that require its JS orchestration surface. Native
// read-only/never policies are additional guardrails, not the boundary.
// See docs/codex-isolation.md for the exact scope and the
// reviewed-version gate that prevents silent CLI feature upgrades.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/sessionstore"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

const (
	// defaultMaxTurns is the cap on agentic turns a run may take before
	// turnCap stops it, and zero means no cap at all -- the same default
	// both other frameworks carry, for the reason agent/claude's own
	// defaultMaxTurns sets out at length: a turn is one model/tool round
	// trip, the ordinary shape of a grain task spends dozens of them,
	// and a number picked here by a package that knows nothing about the
	// task does not slow a run down when it guesses wrong, it fails one
	// that was working.
	//
	// orchestrator's Config.MaxRunRuntime (two hours by default) is what
	// actually bounds a runaway run. A deployment that does want a turn
	// ceiling still sets one -- `grain settings -max-agent-turns`,
	// RunConfig.MaxTurns, or WithMaxTurns -- and turnCap enforces it.
	defaultMaxTurns = 0

	// mcpServerName is the mcp_servers key written into config.toml, and
	// therefore the server name codex reports every tool under. It lives
	// in pkg/mcp rather than here because all three frameworks have to
	// agree on it: one writes the name, the other end takes it back off
	// a reported call before recording it (transcript.go).
	mcpServerName = mcp.ToolNamespace
)

// runner is the one seam this package needs to actually invoke codex --
// narrowed to an interface so a test can drive the parsing and wiring
// logic below without a real codex binary or a live OpenAI credential.
// tee, when non-nil, receives every byte of stdout as the subprocess
// produces it, alongside the buffer this still accumulates and returns
// whole once the process exits.
type runner interface {
	Run(ctx context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (stdout string, err error)
}

type hostRunner struct {
	host      agent.Host
	codexPath string
}

// Run starts codex on whichever machine this Framework's host is -- a
// child of the daemon -- and, when tee is non-nil, mirrors its
// stdout into tee as the subprocess produces it rather than after it
// exits. That live mirror is what both RunConfig.TranscriptPath and the
// turn cap are built on: one needs a run's output on disk while it is
// still running, the other needs to count turns as they happen rather
// than after the fact.
//
// Killing codex's entire process group, including its Code Mode host, is
// the Host's job and is done the same way on both -- see
// agent.LocalHost.Run's procgroup.Prepare. The
// MCP endpoint is grain's rather than codex's child either way, and is
// stopped by the run's own cleanup.
func (r hostRunner) Run(ctx context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (string, error) {
	// stdout is returned alongside any error rather than discarded, the
	// contract both other frameworks already settled on: a run cancelled
	// by the turn cap, or one codex failed partway through, has already
	// streamed the turns it completed, and Framework.Run still owes its
	// caller that record (see agent.Framework's own contract).
	return r.host.Run(ctx, agent.Command{Path: r.codexPath, Args: args, Stdin: stdin, Env: env, Dir: dir}, tee)
}

// Framework implements agent.Framework by running `codex exec`.
type Framework struct {
	run runner
	// host is the machine codex is staged on and runs on: the daemon
	// itself (agent.LocalHost). The private CODEX_HOME and
	// everything in it is written through it rather than through os.
	//
	// codex differs from the other two frameworks in one way that
	// outlives any of this: they fork `grain mcpserver` as a child of
	// the CLI, whereas codex cannot be handed a stdio server at all --
	// its MCP servers live in a config file -- so grain runs that
	// endpoint in its own process and writes codex a URL.
	host            agent.Host
	grainBinaryPath string
	startMCP        func(context.Context, string, []string) (*mcp.ManagedHTTP, error)
	// binPath is the codex binary itself, held here as well as inside
	// the runner above because an interactive session is not run through
	// that seam: TerminalCommand hands the argv to pkg/console, which
	// starts it on a pseudo-terminal of its own (agent.TerminalCommand).
	// A Framework built by a test's own runner has none, and refuses to
	// be driven by hand rather than pretending it can be.
	binPath          string
	checkRuntime     func(context.Context) error
	apiKey           func(context.Context) (string, error)
	authFile         string
	effort           string
	model            string
	maxTurns         int
	granuleSSHUser   string
	granuleExecKey   string
	granuleWorkspace string
	githubDataDir    string
	githubHost       string
	githubInsecure   bool
	grainServerURL   string
	// sessions is where this run's CODEX_HOME comes from when the task
	// it belongs to is allowed one that outlives the run
	// (WithSessionStore). nil -- no store configured, and every caller
	// that is not the daemon -- puts every run in a fresh temp home
	// deleted as Run returns, which is what this package did before the
	// store existed and is still exactly what a run without a task id
	// gets.
	sessions *sessionstore.Store

	// extraConfig is spliced into the config.toml one run gets, between
	// its top-level keys and its first table (configTOML). No deployment
	// sets it: it is here so this package's own live tests can point a
	// real codex at a stub of the responses API running inside the test
	// process, which is what lets the retry loop -- when it resumes,
	// what it sends, when it falls back -- be driven against a real
	// binary and no credential (live_retry_loop_test.go).
	//
	// It is a field on the Framework rather than an argument to
	// writeConfig because a provider is the only way to say any of that
	// to codex, and this file is the only place a provider can be said.
	// codex reads a base URL out of config.toml and nowhere else --
	// OPENAI_BASE_URL is not consulted for the built-in "openai"
	// provider, measured on 0.153.0 -- and it refuses to override a
	// built-in provider's keys at all, so a stub has to be declared as a
	// whole provider of its own. retry.go's header reaches the same
	// finding from the other side, asking whether a run could bound
	// codex's internal retry.
	//
	// That a deployment's runs carry none of this is not left to trust:
	// TestRunLeavesCodexsOwnAPIRetryAlone drives a plain Framework
	// through Run and reads the config it wrote, and fails on a provider
	// table or a retry key in it.
	extraConfig string

	// How many extra execs of codex one run may spend recovering from a
	// transient provider refusal, and how it waits between them --
	// agent/retry.go's own policy, which every framework that re-execs
	// its CLI shares. A test overrides its Sleep so the retry loop costs
	// the suite no wall clock.
	retry agent.RetryPolicy
}

// Option configures a Framework at construction time.
type Option func(*Framework)

// WithModel overrides the model codex is asked for.
func WithModel(model string) Option {
	return func(f *Framework) { f.model = model }
}

// WithEffort selects Codex's model_reasoning_effort; empty uses the model default.
func WithEffort(effort string) Option {
	return func(f *Framework) { f.effort = effort }
}

// Effort is the model_reasoning_effort a run is configured with, or ""
// for the model's own default.
//
// It is empty while no model is named: the effort pairs with a model, and
// a run left to the provider's default model is left its default effort
// too.
func (f *Framework) Effort() string {
	if strings.TrimSpace(f.model) == "" {
		return ""
	}
	return f.effort
}

// WithAPIKey sets the OPENAI_API_KEY passed to the codex subprocess's own
// environment -- never as a command-line argument, so it never lands in
// `ps` output (the same reasoning as claude.WithOAuthToken and
// antigravity.WithAPIKey, and v1's CONTROLLER_AGENT_TOKEN_PATH before
// them).
//
// An API key remains the credential a deployment can rotate from the UI
// between one run and the next. WithAuthFile is the alternative for a
// deployment that authenticates Codex through a ChatGPT login instead.
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
// what an empty string means here (Run then passes no OPENAI_API_KEY at
// all and lets the subprocess authenticate from its own ambient
// environment).
func WithAPIKeyFunc(fn func(context.Context) (string, error)) Option {
	return func(f *Framework) { f.apiKey = fn }
}

// WithAuthFile makes a controller-owned Codex auth.json visible inside
// each run's otherwise-private CODEX_HOME. writeConfig links rather than
// copies it: OAuth refresh tokens are rotated, so every Codex process
// using this account must see updates made by every other one. The
// target stays outside the temporary home and is therefore not removed
// with it, while config.toml, MCP registrations and rollout files remain
// private to the run.
//
// The file must be writable by the Codex subprocess. Codex persists a
// refreshed login through the same path while it runs.
func WithAuthFile(path string) Option {
	return func(f *Framework) { f.authFile = path }
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

// Host returns the Host this framework stages files on and runs the CLI on.
func (f *Framework) Host() agent.Host {
	return f.host
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
// for the same reason, since all three frameworks fork the identical
// server.
//
// Unset leaves that tool unregistered and every run exactly as it was: a
// pushed branch still becomes a pull request when the run finishes.
func WithGrainServer(url string) Option {
	return func(f *Framework) { f.grainServerURL = url }
}

// WithSessionStore gives this Framework the per-task directory a run's
// CODEX_HOME is taken from, so that the thread one run leaves behind is
// still there for the task's next dispatch to be resumed into
// (agent.Result.ResumableSession, model.SuspendedSession).
//
// Unset -- every caller but the daemon, and a daemon with no data
// directory to put one in -- is this package as it was: a fresh
// CODEX_HOME per run, deleted as Run returns, a run that asks to resume
// nothing and reports no session back. That is not a degraded mode; it
// is the isolation this package has always had, and the store is
// deliberately something a deployment opts into by configuring where it
// goes. See pkg/agent/sessionstore for what is kept there, what is
// still rebuilt per run, and who prunes it.
func WithSessionStore(store *sessionstore.Store) Option {
	return func(f *Framework) { f.sessions = store }
}

// Asserted here rather than left to the one call site that type-asserts
// for it (orchestrator.frameworkOpensPullRequests): a Framework that
// stopped implementing this would otherwise not fail to compile, it
// would quietly stop telling its runs about a tool they still have.
var _ agent.PullRequestFramework = (*Framework)(nil)

// CanOpenPullRequest implements agent.PullRequestFramework, exactly as
// the other two frameworks' method of the same name does and for the
// same reason: only a Framework built WithGrainServer passes its forked
// mcpserver the -server/-task pair open_pull_request is registered on,
// so only it can tell orchestrator.BuildPrompt that the tool will be
// there.
func (f *Framework) CanOpenPullRequest() bool { return f.grainServerURL != "" }

// New builds a Framework that runs the real codex binary at codexPath
// (typically just "codex", resolved against $PATH) and points every
// run's config at grainBinaryPath -- the same grain binary this process
// itself is (typically the result of os.Executable()) -- invoked as
// "grainBinaryPath mcpserver -sandbox-root <root>".
func New(codexPath, grainBinaryPath string, opts ...Option) *Framework {
	f := newFramework(nil, grainBinaryPath, opts...)
	f.binPath = codexPath
	// After the options rather than before, because WithHost is one of
	// them and it decides which machine this runs the binary on -- the
	// same ordering agent/claude's and agent/antigravity's New keep.
	f.run = hostRunner{host: f.host, codexPath: codexPath}
	f.checkRuntime = func(ctx context.Context) error { return validateRuntime(ctx, f.host, codexPath) }
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
	// Also after the options, and reading the Framework rather than
	// closing over a copy of the config, so that WithMCPEndpoint is the
	// same whichever order it was passed in. A test replaces this field
	// outright to drive the wiring with no real server at all
	// (mcp_test_helpers_test.go).
	f.startMCP = func(ctx context.Context, binary string, args []string) (*mcp.ManagedHTTP, error) {
		return mcp.StartCommandHTTP(ctx, binary, args)
	}
	return f
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
			return nil, fmt.Errorf("codex: RunConfig.GranuleVM is set but this Framework has no granule SSH config (see WithGranuleSSH)")
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
		return nil, fmt.Errorf("codex: RunConfig.SandboxRoot, .GranuleVM, .Container, or .K8sPod is required")
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

// codexArgs is one attempt's argv: `codex exec` for the first attempt of
// a run, and `codex exec resume <thread>` for one rejoining a thread an
// earlier attempt opened (retry.go).
//
// The two differ by the subcommand and the positional id and by nothing
// else -- every flag below is one `codex exec resume` takes too, which is
// a fact about the binary rather than a guess (see retry.go's header and
// docs/hand-runs/2026-09-07-codex-resume.md). Building both here rather
// than at the call site is what keeps them from drifting apart: a flag
// added to a run's first exec and forgotten on its retry would leave the
// two attempts of one run configured differently.
//
// Every flag below is `exec`'s, and that is not the same thing as being
// codex's: `codex` and `codex exec` are separate commands with separate
// flag sets, and docs/codex-surface.md's "where each flag lives" table
// has --json and --skip-git-repo-check under "exec only". That is why
// TerminalCommand, which starts codex with no subcommand at all, cannot
// simply be this list minus the headless flags -- and why
// surface_capture_test.go reads this argv and that one back against the
// capture.
func (f *Framework) codexArgs(thread string) []string {
	args := []string{"exec"}
	if thread != "" {
		// The subcommand goes immediately after `exec`; the thread id is
		// its first positional argument, and goes after the flags with
		// the "-" behind it.
		args = append(args, "resume")
	}
	args = append(args,
		// The machine-readable event stream everything below parses.
		// Without it codex prints a human-formatted log, which carries
		// the same story in a shape no parser should be reading.
		"--json",
		// A sandbox's working tree is a git repository, but a granule run
		// has no local directory at all and leaves codex started in
		// whatever this daemon's own working directory is. codex exec
		// otherwise refuses to run outside a repository -- "Not inside a
		// trusted directory and --skip-git-repo-check was not
		// specified", in the capture's own string-table read -- a guard
		// for somebody running it by hand in their home directory, and
		// not one that means anything here, where what a run may touch
		// is decided by the MCP server and the sandbox policy rather
		// than by what is under the cwd.
		"--skip-git-repo-check",
	)
	if f.model != "" {
		args = append(args, "--model", f.model)
	}
	if thread != "" {
		args = append(args, thread)
	}
	// "-" is codex's own "read the prompt from stdin". It goes last
	// because it is the positional prompt argument, not a flag -- and on
	// a resume it is the *second* positional, which is why the thread id
	// is appended immediately above it rather than anywhere else.
	return append(args, "-")
}

// configFileName is the file codex reads its configuration from inside
// CODEX_HOME. Named once, here, because it and configTOML are the whole
// of this package's dependence on codex's own on-disk layout rather than
// on its command line or its output: if a future codex moves or renames
// the file, this constant and that function are the change.
const configFileName = "config.toml"

// environmentsFileName is the manifest beside it that withholds codex's
// execution environments. Named here for the same reason configFileName
// is: it is one of the files grain writes into a home it may not own
// outright, and a run must never inherit another run's (runFileNames).
const environmentsFileName = "environments.toml"

// frameworkName is what this package is called wherever a framework has
// to be named to something outside it -- the session store's own
// directory, and model.SuspendedSession.Framework, which is what stops a
// token this CLI wrote being handed to another one.
const frameworkName = "codex"

// authFileName is Codex's file-backed login store. It is deliberately a
// separate constant from configFileName because only this file is shared
// across private homes when WithAuthFile is configured.
const authFileName = "auth.json"

// configTOML gives Codex only a Grain-owned HTTP MCP endpoint. The
// no-environment manifest written alongside this file removes native
// environment-backed tools from the registry, including read access.
// read-only/never are defense in depth, not the isolation mechanism.
// Code Mode remains available for models that require it; its JS host
// orchestrates MCP calls without getting a local execution environment.
//
// Each capability MCP server (docs/capabilities-spec.md §5.2.4) follows
// as a table of its own, its URL grain's proxy and each tool its
// tools/list reported preapproved the same way.
func (f *Framework) configTOML(endpoint *mcp.ManagedHTTP, servers ...agent.CapabilityMCPServer) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Written by grain for one run; see docs/codex-isolation.md.\n")
	b.WriteString("approval_policy = \"never\"\nsandbox_mode = \"read-only\"\n")
	b.WriteString("web_search = \"disabled\"\ninclude_environment_context = false\n")
	if effort := f.Effort(); effort != "" {
		if !ValidEffort(effort) {
			return nil, fmt.Errorf("codex: unsupported reasoning effort %q", effort)
		}
		value, _ := tomlString(effort)
		fmt.Fprintf(&b, "model_reasoning_effort = %s\n", value)
	}
	if f.authFile != "" {
		b.WriteString("cli_auth_credentials_store = \"file\"\n")
	}
	if extra := strings.TrimSpace(f.extraConfig); extra != "" {
		b.WriteString(extra + "\n")
	}
	b.WriteString("[features]\ncode_mode_host = true\napps = false\nplugins = false\nmulti_agent = false\nmulti_agent_v2 = false\n")
	b.WriteString("[orchestrator.skills]\nenabled = false\n")
	b.WriteString("[skills.bundled]\nenabled = false\n")
	if endpoint == nil {
		return []byte(b.String()), nil
	}
	fmt.Fprintf(&b, "[mcp_servers.%s]\n", mcpServerName)
	url, _ := tomlString(endpoint.URL)
	auth, _ := tomlString("Bearer " + endpoint.Token)
	fmt.Fprintf(&b, "url = %s\nhttp_headers = { Authorization = %s }\nrequired = true\n", url, auth)
	// codex caps one MCP tool call at a minute unless told otherwise,
	// which is far short of a chat turn waiting on a person or a
	// wait_for_checks waiting on CI (mcp.MaxBlockingToolWait). The same
	// limit, plus slack, as claude's MCP_TOOL_TIMEOUT and agy's
	// timeoutSeconds, so that grain's own clock ends a wait rather than
	// the CLI abandoning it.
	fmt.Fprintf(&b, "tool_timeout_sec = %d\n", int((mcp.MaxBlockingToolWait + mcpToolTimeoutSlack).Seconds()))
	// Only tools actually granted to this run are preapproved. New tools
	// from unrelated MCP servers cannot acquire approval through this.
	names := make([]string, 0, len(endpoint.Tools))
	for _, tool := range endpoint.Tools {
		names = append(names, tool.Name)
	}
	allowed, _ := tomlStringArray(names)
	fmt.Fprintf(&b, "enabled_tools = %s\n", allowed)
	for _, name := range names {
		quoted, _ := tomlString(name)
		fmt.Fprintf(&b, "[mcp_servers.%s.tools.%s]\napproval_mode = \"approve\"\n", mcpServerName, quoted)
	}
	for _, s := range servers {
		table, err := tomlString(s.Name)
		if err != nil {
			return nil, err
		}
		url, err := tomlString(s.URL)
		if err != nil {
			return nil, err
		}
		allowed, err := tomlStringArray(s.Tools)
		if err != nil {
			return nil, err
		}
		// The proxy URL is the credential (protocol.ServeEndpoint), so
		// there are no http_headers to send.
		fmt.Fprintf(&b, "[mcp_servers.%s]\nurl = %s\nrequired = true\ntool_timeout_sec = %d\nenabled_tools = %s\n",
			table, url, int((mcp.MaxBlockingToolWait + mcpToolTimeoutSlack).Seconds()), allowed)
		for _, tool := range s.Tools {
			quoted, err := tomlString(tool)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&b, "[mcp_servers.%s.tools.%s]\napproval_mode = \"approve\"\n", table, quoted)
		}
	}
	return []byte(b.String()), nil
}

// mcpToolTimeoutSlack is how far past the longest wait grain's own tools
// can do the per-server tool_timeout_sec above is set: enough for the
// round trips either side of the wait, so that the tool's own clock runs
// out first (agent/claude's constant of the same name).
const mcpToolTimeoutSlack = 2 * time.Minute

// tomlString renders one Go string as a TOML basic string. A TOML basic
// string is a JSON string -- same quotes, same backslash escapes, same
// \uXXXX form for anything encoding/json decides to escape (a control
// character, and also "<", ">" and "&", which it escapes by default and
// TOML reads back identically) -- so this is encoding/json rather than a
// quoter of this package's own, which would be one more thing here to
// get subtly wrong for a path with a quote or a backslash in it.
func tomlString(s string) (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("codex: encoding %q for config.toml: %w", s, err)
	}
	return string(data), nil
}

// tomlStringArray is tomlString for a list, TOML's inline array of basic
// strings being spelled exactly as JSON's array of strings. nil renders
// as "[]" rather than as JSON's "null", which TOML has no word for.
func tomlStringArray(items []string) (string, error) {
	if items == nil {
		items = []string{}
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("codex: encoding %v for config.toml: %w", items, err)
	}
	return string(data), nil
}

// writeConfig builds the private CODEX_HOME one run gets: a fresh
// directory containing the config file naming this run's own MCP server
// and, when configured, a link to the shared auth file. It returns that
// directory and a cleanup func the caller defers.
//
// A private CODEX_HOME rather than `codex mcp add` is the whole point --
// see this package's own doc comment on why a per-user registration
// cannot express a per-run sandbox binding. It has the same effect
// claude's --strict-mcp-config has there: the only MCP server this run
// can see is the one written here, because there is no other
// configuration in the config directory it was given to find one in.
//
// A temp directory is where it lands, so codex opens every run with a
// warning on stderr that it will not create its PATH aliases under one.
// That is a convenience it declines rather than a failure -- it says
// "proceeding" and does -- and the alternative, a config directory
// somewhere permanent, would be the shared registration this exists to
// avoid.
// taskID is the task this run belongs to, or "" for a caller that has
// none: it decides whether the home is this task's own, kept between
// dispatches so that the thread survives with it, or the temp directory
// every run used to get. persistent says which it turned out to be, and
// is what Run reads before it asks to resume anything or reports a
// thread back -- see openHome.
func (f *Framework) writeConfig(ctx context.Context, mcpArgs []string, taskID string, servers ...agent.CapabilityMCPServer) (home string, persistent bool, cleanup func(), err error) {
	servers, releaseServers, err := agent.CarryCapabilityServers(f.host, servers)
	if err != nil {
		return "", false, nil, fmt.Errorf("codex: %w", err)
	}
	endpoint, err := f.startMCP(ctx, f.grainBinaryPath, mcpArgs)
	if err != nil {
		releaseServers()
		return "", false, nil, err
	}
	config, err := f.configTOML(endpoint, servers...)
	if err != nil {
		endpoint.Close()
		releaseServers()
		return "", false, nil, err
	}
	home, persistent, removeHome, err := f.writeHome(ctx, config, taskID)
	if err != nil {
		endpoint.Close()
		releaseServers()
		return "", false, nil, err
	}
	return home, persistent, func() { endpoint.Close(); releaseServers(); removeHome() }, nil
}

// openHome is the directory this run's CODEX_HOME will be, and how to
// give it back.
//
// The task's own store when there is one (WithSessionStore): the same
// directory this task's last run was given, still holding the thread
// that run ended in, which is the whole of what lets a second dispatch
// pick the conversation up. Otherwise a fresh temp directory, exactly as
// every run got before the store existed -- a caller with no task id, a
// Framework built without a store, a second run of a task whose store
// somebody else is holding.
//
// What differs between the two is only *whose* directory it is and how
// long it lasts. The files grain writes into it below are written the
// same way either way, per run, and are removed either way when the run
// ends: a persistent home is cleared of this run's config and its link
// to a login and keeps nothing but what codex itself wrote, and a temp
// home is removed whole. So no run ever starts against another run's MCP
// registration, which is what the private home is for (see this
// package's doc comment, and sessionstore's).
func (f *Framework) openHome(ctx context.Context, taskID string) (home string, persistent bool, cleanup func(), err error) {
	// The removals below outlive the run's own context on purpose: a
	// cleanup deferred by Run is reached with ctx already cancelled about
	// as often as not (a turn cap, a deadline, an operator), and a
	// removal that inherited that cancellation would leave a run's
	// CODEX_HOME -- and the link to a login -- behind on the machine it
	// ran on.
	detached := func(remove func(context.Context)) func() {
		return func() {
			removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), homeRemovalTimeout)
			defer cancel()
			remove(removeCtx)
		}
	}
	if dir, release, ok := f.sessions.Open(frameworkName, taskID); ok {
		return dir, true, detached(func(removeCtx context.Context) {
			f.clearRunFiles(removeCtx, dir)
			release()
		}), nil
	}
	home, err = f.host.MkdirTemp(ctx, "grain-codex-home-*")
	if err != nil {
		return "", false, nil, fmt.Errorf("codex: creating codex home: %w", err)
	}
	return home, false, detached(func(removeCtx context.Context) { f.host.RemoveAll(removeCtx, home) }), nil
}

// runFileNames are the files grain itself writes into a CODEX_HOME, and
// the whole of what a run must not inherit from the one before it: the
// config naming this run's own MCP endpoint and the bearer token for it,
// the manifest that withholds codex's execution environments, and the
// link to the deployment's login. Everything else in there was written
// by codex and is the conversation this store exists to keep.
//
// Removed when a run ends and written again when the next one starts,
// rather than only written: a daemon killed mid-run would otherwise
// leave a task's home holding a link to a credential for as long as the
// store keeps that task.
var runFileNames = []string{configFileName, environmentsFileName, authFileName}

// clearRunFiles takes this run's own files back out of a home that
// outlives it. Failures are not reported: this runs in a deferred
// cleanup, after the run has its answer, and the next run overwrites
// every one of these before it starts codex anyway.
func (f *Framework) clearRunFiles(ctx context.Context, home string) {
	for _, name := range runFileNames {
		f.host.RemoveAll(ctx, filepath.Join(home, name))
	}
}

// writeHome stages the private CODEX_HOME on whichever machine codex
// runs on -- through the Host rather than through os, so a remote run's
// config lands next to the binary that reads it.
func (f *Framework) writeHome(ctx context.Context, config []byte, taskID string) (home string, persistent bool, cleanup func(), err error) {
	home, persistent, cleanup, err = f.openHome(ctx, taskID)
	if err != nil {
		return "", false, nil, err
	}
	// Whatever a previous run of this task left here, gone before this
	// run's own is written: a home that outlives a run may still hold
	// that run's config or its link to a login, and a Symlink over an
	// existing name fails rather than replacing it.
	if persistent {
		f.clearRunFiles(ctx, home)
	}
	// File takes precedence over inherited CODEX_EXEC_SERVER_URL. No
	// [[environments]] and include_local=false means not even a local
	// read-only environment is registered.
	if err := f.host.WriteFile(ctx, filepath.Join(home, environmentsFileName), []byte("default = \"none\"\ninclude_local = false\n"), 0o600); err != nil {
		cleanup()
		return "", false, nil, fmt.Errorf("codex: writing no-environment manifest: %w", err)
	}
	if err := f.host.WriteFile(ctx, filepath.Join(home, configFileName), config, 0o600); err != nil {
		cleanup()
		return "", false, nil, fmt.Errorf("codex: writing codex config: %w", err)
	}
	if f.authFile != "" {
		authFile, err := f.resolveAuthFile()
		if err != nil {
			cleanup()
			return "", false, nil, err
		}
		if err := f.host.Symlink(ctx, authFile, filepath.Join(home, authFileName)); err != nil {
			cleanup()
			return "", false, nil, fmt.Errorf("codex: linking shared auth file: %w", err)
		}
	}
	return home, persistent, cleanup, nil
}

// resolveAuthFile turns WithAuthFile's path into the absolute one to
// link, and checks what can be checked from here.
//
// ValidateAuthFile is what can be checked: the file is this process's
// own, and a login codex cannot read, or cannot write a refreshed token
// back through, is a run that fails minutes later saying nothing at all
// about auth.json.
func (f *Framework) resolveAuthFile() (string, error) {
	authFile, err := filepath.Abs(f.authFile)
	if err != nil {
		return "", fmt.Errorf("codex: resolving shared auth file: %w", err)
	}
	if err := ValidateAuthFile(authFile); err != nil {
		return "", err
	}
	return authFile, nil
}

// homeRemovalTimeout bounds that removal. It is generous: the cost of
// waiting is one goroutine at the end of a run, and the cost of giving
// up early is a directory holding a link to a login on somebody else's
// machine.
const homeRemovalTimeout = time.Minute

// Run implements agent.Framework: it writes this run's own config into a
// private CODEX_HOME pointing at grainBinaryPath and cfg.SandboxRoot or
// cfg.GranuleVM (mcpServerArgs), runs `codex exec` with the prompt on
// stdin, and parses the resulting JSON event stream back into a Result.
//
// The prompt travels over stdin -- what the bare "-" argument asks codex
// for -- and never as argv: untrusted issue content must never become a
// shell- or ps-visible argument (dispatch.py's own docstring makes the
// identical point about its stdin-redirect path, and both other
// frameworks pass their prompt the same way).
//
// Run may return a non-nil Result together with a non-nil error, and a
// caller must read both. A run that edits files, commits, pushes and
// only then trips the turn cap has already changed the world, and a
// caller that treats the error as "no result" strands that work -- the
// failure that taught this is recorded in agent.Framework's own
// contract, and it is why a cancelled or failed subprocess's partial
// stdout is still parsed here rather than discarded.
//
// When cfg.TranscriptPath is set, the raw event stream is mirrored there
// live, one line per event, as codex emits them -- the file this
// package's own LiveTranscriptDir reads back with PartialTranscript to
// render a still-running run's transcript-in-progress. It is opened
// O_TRUNC, not O_APPEND: a path is only ever reused across runs by a
// caller passing the same run ID twice, which should never happen, and
// starting clean is what makes a stale previous run's bytes never a way
// to misread this one's. The file is left on disk once Run returns
// either way -- cleaning it up is the caller's job, the same way
// orchestrator.RunDispatch owns cfg.SandboxRoot's own lifecycle rather
// than this package.
//
// cfg.Addenda is not polled, for the reason RunConfig.Addenda's own doc
// comment gives about both other frameworks: this is one blocking
// subprocess call with the whole prompt written before the process
// starts, so there is no turn boundary here to poll at. A comment posted
// while a run is in flight waits for the task's next dispatch instead.
func (f *Framework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	// First, ahead of anything that starts a process: an arrangement
	// that cannot work is better answered before anything is started.
	if f.checkRuntime != nil {
		if err := f.checkRuntime(ctx); err != nil {
			return nil, err
		}
	}
	mcpArgs, err := f.mcpServerArgs(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if f.grainBinaryPath == "" {
		return nil, fmt.Errorf("codex: grainBinaryPath is required")
	}
	maxTurns := f.maxTurns
	if cfg.MaxTurns > 0 {
		maxTurns = cfg.MaxTurns
	}

	// persistent says whether this run's CODEX_HOME is the task's own,
	// kept between dispatches, or the temp directory that goes with the
	// run. It is what the two ends of a cross-dispatch resume hang off
	// below: a thread only means anything while the home holding it
	// exists.
	home, persistent, cleanup, err := f.writeConfig(ctx, mcpArgs, cfg.TaskID, cfg.CapabilityMCPServers...)
	if err != nil {
		return nil, err
	}
	// Kept, CODEX_HOME and all, for a run this daemon detached from as it
	// stopped (agent.ErrDetached): codex is still running in grain hostd
	// against this home, and the daemon that adopts the run reads its
	// stream (ParseResult) without ever learning the home's path.
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
			return nil, fmt.Errorf("codex: opening transcript file: %w", err)
		}
		defer transcriptFile.Close()
		sinks = append(sinks, transcriptFile)
	}

	// The turn cap watches the same live stream the transcript mirror
	// does and cancels the attempt's own context once the cap is reached
	// -- codex having no --max-turns to enforce it in the binary (see
	// this package's doc comment). One cap, retargeted at each attempt,
	// so the cap stays cumulative over a run that execs codex more than
	// once; see turncap.go.
	capWatch := &turnCap{max: maxTurns}
	sinks = append(sinks, capWatch)
	// One sink for the whole run, opened once and reused by every
	// attempt: the transcript mirror above is O_TRUNC, so reopening the
	// path for a retry would throw away everything the run had already
	// streamed.
	live := io.MultiWriter(sinks...)

	env := []string{"CODEX_HOME=" + home, "CODEX_EXEC_SERVER_URL=none"}
	if f.apiKey != nil {
		apiKey, err := f.apiKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("codex: reading the API key: %w", err)
		}
		if apiKey != "" {
			env = append(env, "OPENAI_API_KEY="+apiKey)
		}
	}

	// More than one exec, when the provider refuses this run mid-flight:
	// the sandbox and this run's CODEX_HOME are still here at that
	// moment, so the run rejoins its own thread out of that home rather
	// than costing a dispatch and a fresh clone. See retry.go.
	//
	// And, when this run was given the task's own home, one more exec
	// earlier than that: the thread a *previous dispatch* left behind is
	// in that same directory, so the first exec resumes it rather than
	// starting the task's thinking from nothing. Only then -- a token
	// from a home that no longer exists is a refused exec and nothing
	// else, which is why this is gated on persistent rather than on
	// cfg.ResumeSession alone.
	suspendedThread := ""
	if persistent {
		suspendedThread = cfg.ResumeSession
	}
	attempts := f.runAttempts(ctx, attemptSetup{
		args:            f.codexArgs,
		env:             env,
		dir:             home,
		prompt:          cfg.Prompt,
		resumePrompt:    agent.ResumePrompt,
		restartPrompt:   agent.RestartPreamble + cfg.Prompt,
		resumeThread:    suspendedThread,
		suspendedPrompt: cfg.ResumeMessage(),
		// Whether a thread this run ends in is worth handing to the next
		// dispatch at all: in a temp home it dies with the run, and
		// reporting it would cost that dispatch an exec and show a
		// person a resume token that resumes nothing.
		keepThread: persistent,
		live:       live,
		onThread:   cfg.OnResumableSession,
		cap:        capWatch,
	})
	stdout, runErr, notes := attempts.capture, attempts.runErr, attempts.notes
	if errors.Is(runErr, agent.ErrDetached) {
		detached = true
		return nil, runErr
	}
	return f.endRun(stdout, runErr, notes, attempts.thread, maxTurns, capWatch.tripped())
}

// ParseResult implements agent.ResultParser: the Result and error Run
// would have returned for a run whose stream is stdout and which ended
// with runErr, for a daemon adopting a run another daemon started
// detached in grain hostd (docs/non-disruptive-upgrade.md, "Which
// substrate carries which CLI"). stdout is the one exec hostd held, so it
// is its own last attempt, and the thread it names is the one the run
// leaves resumable. The turn cap was this daemon's to enforce and went
// with it, so an adopted run is never read as having tripped it.
func (f *Framework) ParseResult(stdout string, runErr error) (*agent.Result, error) {
	return f.endRun(stdout, runErr, nil, parseEvents(stdout).threadID, f.maxTurns, false)
}

// ParseAttempts implements agent.AttemptsParser: ParseResult for a run
// that retried before the daemon watching it stopped, whose earlier
// attempts' streams are earlier. The capture is every attempt's, as
// Run's is, and the thread it leaves resumable is the last one any
// attempt named.
func (f *Framework) ParseAttempts(earlier, stdout string, runErr error) (*agent.Result, error) {
	var capture strings.Builder
	appendCapture(&capture, earlier)
	appendCapture(&capture, stdout)
	thread := parseEvents(stdout).threadID
	if thread == "" {
		thread = parseEvents(earlier).threadID
	}
	return f.endRun(capture.String(), runErr, nil, thread, f.maxTurns, false)
}

var (
	_ agent.ResultParser   = (*Framework)(nil)
	_ agent.AttemptsParser = (*Framework)(nil)
)

// endRun is the end of Run, shared with ParseResult: what one run's
// capture says the run came to. notes are the retry loop's own, thread
// the conversation a later dispatch may resume, and capped whether the
// turn cap stopped the run.
func (f *Framework) endRun(stdout string, runErr error, notes []string, thread string, maxTurns int, capped bool) (*agent.Result, error) {
	result, parseErr := parseTranscript(stdout)
	// Whatever the retry loop had to say about itself travels back with
	// the run whichever way it ended, so a transcript that appears to
	// restart half way through says why -- see retry.go.
	//
	// The thread codex ran in travels back the same way, and for a
	// nearer reason: it is what a *later* dispatch of this task can be
	// resumed into (agent.Result.ResumableSession, model.
	// SuspendedSession). Attached here rather than only on the success
	// path because the runs worth resuming are the ones that ended badly
	// -- cut off by the wall clock, stopped by a usage limit -- and those
	// come back through the arms below as a partial result beside an
	// error. Empty unless this run's home outlives it, which is the
	// invariant the whole feature rests on.
	annotate := func(r *agent.Result) *agent.Result {
		if r == nil {
			return nil
		}
		for _, note := range notes {
			r.Transcript = appendNote(r.Transcript, note)
		}
		r.ResumableSession = thread
		return r
	}
	// Parsed once here rather than at each branch below, since two of
	// them read the same pass: what codex said about how the run ended
	// is both the evidence a quota refusal leaves and the sentence that
	// explains an ordinary failure.
	events := parseEvents(stdout)
	// Both refusals below are read off a run that actually failed, and
	// only then. codex reports every attempt of a transport reconnect as
	// an error event of its own -- "Reconnecting... 1/5 (unexpected
	// status 503 Service Unavailable)" -- and a run that recovers goes
	// on to finish normally, so classifying the whole capture
	// unconditionally would report a successful run as a refusal, and
	// pause the deployment for one that mentioned a quota. See retry.go,
	// which reads the same guard before starting an attempt over.
	var limit *agent.UsageLimitError
	var incompat *agent.ModelIncompatibilityError
	var capacity *agent.CapacityError
	if failed(events, runErr) {
		// Read from the stream's own account of the failure plus
		// whatever the subprocess itself reported, because a quota
		// refusal can arrive either way: codex usually reports it as a
		// failed turn (parseErr, below) but a hard enough refusal kills
		// the process first (runErr). See usagelimit.go.
		limit = usageLimitFailure(events.failureText(), runErr)
		incompat = modelIncompatibilityFailure(f.model, events.failureText(), runErr)
		// The same evidence read for the other refusal a provider makes
		// that says nothing about this run: not "you have spent your
		// budget" but "come back in a moment, the server is overloaded".
		// See capacity.go.
		capacity = capacityFailure(events.failureText(), runErr)
	}
	switch {
	case capped:
		// The cap cancelled the subprocess, so runErr is that
		// cancellation rather than a fault of codex's -- report the cap.
		// result is whatever the run managed before it was stopped, and
		// is returned alongside the error, never instead of it.
		return annotate(partialResult(result, stdout)), agent.MaxTurnsExceeded("codex", maxTurns)
	case limit != nil:
		// Ahead of both branches below, which would otherwise render
		// this as "running codex: exit status 1" and lose the one thing
		// about it a deployment can act on: nothing is wrong with this
		// run, the credential it used has no budget left until its
		// window resets. orchestrator.RunDispatch reads the type
		// (agent.UsageLimit) and pauses dispatch rather than sending the
		// next task at the same refusal.
		//
		// result travels back alongside it, as everywhere else here: a
		// run that pushed a branch and then ran out of quota has already
		// changed the world.
		return annotate(partialResult(result, stdout)), limit
	case incompat != nil:
		return annotate(partialResult(result, stdout)), incompat
	case capacity != nil:
		// Ahead of both branches below for the same reason, and behind
		// the limit above because a refusal that is both is a usage
		// limit. What this buys is narrower than a pause: the run is
		// still over, but orchestrator.RunDispatch records it as the
		// provider's doing (model.CapacityOutcome) rather than as this
		// task's fifth strike.
		//
		// result travels back alongside it, as everywhere else here: a
		// run that pushed a branch and was then refused has already
		// changed the world.
		return annotate(partialResult(result, stdout)), capacity
	case runErr != nil:
		// Both halves, not just the exit status: codex reports a run it
		// could not finish in the stream and then exits non-zero with
		// nothing useful on stderr, and this arm used to render the pair
		// as the unactionable "running codex: exit status 1 (stderr: )".
		// events.resultErr is the terminal failure's own sentence -- a
		// non-terminal error event is deliberately not read here, since
		// codex reports each failed attempt of a reconnect that then
		// succeeded as one, and blaming a failure on a recovery would be
		// worse than saying nothing.
		return annotate(partialResult(result, stdout)), agent.RunFailure("codex", "codex", events.resultErr, runErr)
	case parseErr != nil:
		// A capture with no terminal event -- a codex that died without
		// runErr reaching us, or output truncated mid-stream.
		// partialResult, not result: parseTranscript hands back nothing
		// in that case, and a run that pushed a branch before the stream
		// stopped must still come back with the push.
		return annotate(partialResult(result, stdout)), parseErr
	}
	return annotate(result), nil
}

// partialResult is what Run hands back alongside an error: whatever the
// run managed to do before it failed or was stopped.
//
// A run that produced no events at all gets a nil Result, not an empty
// one -- agent.Framework's own contract draws exactly that line ("a nil
// Result with an error means the run never started"), and callers depend
// on it: orchestrator.RunDispatch treats a non-nil Result as a run whose
// tool calls it must process, so an empty one for a run cancelled before
// codex ever spoke would report an agent that touched a sandbox it never
// reached.
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
