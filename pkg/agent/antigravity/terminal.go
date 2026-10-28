package antigravity

import (
	"context"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// Asserted here rather than at the one call site that type-asserts for
// it (orchestrator's console.go), for the reason claude's own copy of
// this line gives: a Framework that stopped implementing this would not
// fail to compile, it would quietly stop being something an interactive
// task can be dispatched to.
var _ agent.TerminalFramework = (*Framework)(nil)

// TerminalCommand implements agent.TerminalFramework: the same agy this
// package already runs, minus print mode.
//
// What keeps a session inside the same fence an unattended run is inside
// is the private HOME, not this argument list. `writeAgyHome` builds one
// per run holding the MCP settings that name this run's sandbox and the
// permission rules that deny agy's own filesystem tools by name
// (permissionRules, withheldNativeTools), and it is built here exactly as
// Run builds it -- so a session reaches the same sandbox through the same
// MCP server under the same denials, and the person typing is the only
// thing that changed.
//
// What stays on the command line is everything that decides what the
// agent is and what it may touch: `--dangerously-skip-permissions` (which
// switches off agy's *approval prompts*, not the denials above -- see
// Run), `--add-dir` naming the one directory a host-rooted run may reach,
// and the model and effort the deployment asked for. Each of those is a
// flag the captured agy lists in its own `--help`, which is a thing
// surface_capture_test.go checks rather than a thing this comment
// asserts: docs/agy-surface.md's "Interactive mode" section is where the
// binary's answers about this argument list are written down.
//
// # What is dropped, and the one that is a judgement rather than a
// mechanical removal
//
// `--input-format`/`--output-format stream-json` and `--print-timeout`
// all exist to make print mode machine-readable and to bound a run
// nobody is watching -- agy's own help says "print mode" of each of them,
// which is the whole argument for dropping them and is the rule
// surface_capture_test.go holds this list to. Without them agy opens its
// own terminal interface (the capture's probe of a flagless `agy` is
// "bubbletea: error opening TTY", which is what that looks like to
// something that is not a terminal), which is the thing the person
// attaching wants to see, and it is bounded by
// orchestrator.DefaultMaxConsoleRuntime instead.
//
// `--disable-slash-commands` is the judgement. Run passes it because a
// headless run has no use for `/help` or `/model` and every one of them
// is a way for a prompt to reach something the transcript parser does not
// expect. A session is the opposite case on both counts: the slash
// commands are half of what somebody sits down at this CLI for, and
// `/exit` is one of the three documented ways a session ends at all
// (pkg/console's own Framework.Run). Leaving them off would be handing
// somebody a terminal with its own controls removed.
//
// That `/exit` is a session's to begin with is measured rather than
// assumed: docs/agy-surface.md's interactive section has agy refusing it
// by name in print mode -- "/exit is not available in print mode (the
// process exits as soon as the run completes)" -- beside the eleven
// commands print mode does answer. A command refused by name is a command
// that exists and belongs to the terminal interface.
//
// The prompt does not travel here, and one thing goes with it that is
// worth naming rather than leaving to be noticed. An unattended run's
// prompt opens with `toolPreamble`, the paragraph telling the model to
// treat agy's native run/read/write/search tools as unavailable and to
// use grain's instead. A session has no prompt to carry it -- its first
// message is typed by whoever opened it -- so the model is not *told*
// about the withheld tools. It is still stopped from using them:
// permissionRules denies them in the settings file above and the hook
// blocks a call to one either way, which is why the preamble was never
// the enforcement. The cost is a session where the model may reach for a
// denied tool once before it learns, rather than a run where it was
// warned first.
func (f *Framework) TerminalCommand(ctx context.Context, cfg agent.RunConfig) (*agent.TerminalCommand, error) {
	if f.binPath == "" {
		return nil, fmt.Errorf("antigravity: this Framework was built without an agy binary to run interactively")
	}
	if f.grainBinaryPath == "" {
		return nil, fmt.Errorf("antigravity: grainBinaryPath is required")
	}
	mcpArgs, err := f.mcpServerArgs(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// Read before the HOME is built, for the reason Run reads it there:
	// whether this run has a key of its own decides what goes into that
	// HOME, since a key agy is never told to look for is a key it does
	// not use (writeAgyHome).
	apiKey, err := f.resolveAPIKey(ctx)
	if err != nil {
		return nil, err
	}
	// Deliberately no task id, so an interactive session gets the temp
	// HOME it has always had rather than the task's own store. Two
	// reasons, and either would do on its own: a session is driven by a
	// person who resumes it by sitting back down at it rather than
	// through agent.RunConfig.ResumeSession, which nothing here reads;
	// and a session can be open while a run of the same task is
	// dispatched, which the store would answer by refusing one of them a
	// home it was counting on (sessionstore.Store.Open's lock).
	//
	// The capability grants' MCP servers go into it as they go into a
	// run's (§5.2.4), and nothing else of cfg does: the host-tool grants
	// hookArgs also reads record their calls for a run's report, which a
	// session does not write.
	servers, releaseServers, err := agent.CarryCapabilityServers(f.host, cfg.CapabilityMCPServers)
	if err != nil {
		return nil, fmt.Errorf("antigravity: %w", err)
	}
	home, _, removeHome, err := writeAgyHome(ctx, f.host, nil, f.effectiveRemoteSessionRoot(), "", f.grainBinaryPath, mcpArgs, providerFor(apiKey), agent.RunConfig{CapabilityMCPServers: servers})
	if err != nil {
		releaseServers()
		return nil, err
	}
	cleanup := func() { releaseServers(); removeHome() }

	var args []string
	if !f.ambientPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if f.model != "" {
		args = append(args, "--model", f.model)
	}
	if effort := f.Effort(); effort != "" {
		args = append(args, "--effort", effort)
	}
	if cfg.SandboxRoot != "" {
		args = append(args, "--add-dir", cfg.SandboxRoot)
	}

	// The same environment a dispatch's agy gets, for the same reasons:
	// the private HOME, GOOGLE_API_KEY cleared beside the key, and the
	// updater switched off (selfupdate.go). A session is an exec like any
	// other, and an interactive one is the likeliest place for a person
	// to be told their pinned agy has become a different version.
	env := agyEnv(home, apiKey)

	return &agent.TerminalCommand{
		Path: f.binPath,
		Args: args,
		Env:  env,
		// The same directory Run starts agy in: the sandbox root for a
		// host-rooted run, and the workspace inside the private HOME for
		// a granule one, which has no local directory of its own.
		Dir: workDir(cfg, home),
		// Removed once the session ends rather than once this function
		// returns: the HOME has to outlive the call, because the process
		// that reads it has not been started yet. Run gets to defer this;
		// a session's cleanup travels with the command.
		Cleanup: cleanup,
	}, nil
}
