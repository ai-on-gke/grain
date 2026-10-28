package codex

import (
	"context"
	"fmt"

	"github.com/bwsalmon/grain/pkg/agent"
)

// Asserted here rather than at the one call site that type-asserts for
// it (orchestrator's console.go), for the reason claude's own copy of
// this line gives: a Framework that stopped implementing this would not
// fail to compile, it would quietly stop being something an interactive
// task can be dispatched to.
var _ agent.TerminalFramework = (*Framework)(nil)

// TerminalCommand implements agent.TerminalFramework: the same codex
// this package already runs, minus the `exec` subcommand that makes it
// headless.
//
// What decides what the agent can reach is not on the command line here
// at all, which makes this the shortest of the three. codex reads its
// whole configuration out of CODEX_HOME, and `writeConfig` builds a
// private one per run holding this run's own MCP server, the two policies
// that keep codex's native tools from being a way around it, and only
// the explicitly configured link to shared login state -- so a session
// gets exactly the config an
// unattended run of the same task gets, by construction rather than by
// this function remembering to repeat anything.
//
// `approval_policy = "never"` is worth a second look here, because
// configTOML's own comment justifies it partly with "there is no human
// at a terminal" and in this one case there is. It stays: what it turns
// off is codex stopping to ask permission for its *own* shell and patch
// tools, which the no-environment manifest removes entirely. Turning approvals back on for a
// session would mean prompting somebody to approve writes that are
// refused either way, on the wrong machine -- everything a run is meant
// to change is in the sandbox, and the only route there is the MCP
// server.
//
// What goes is `exec` and the flag that exists to make its output
// parseable (`--json`), leaving codex's own terminal interface -- with
// no subcommand at all, which is how codex spells it: "If no subcommand
// is specified, options will be forwarded to the interactive CLI". The
// prompt does not travel here either: an interactive session's first
// message is typed by whoever opened it (agent.TerminalCommand.Args).
//
// # Why --skip-git-repo-check is not here, though codexArgs passes it
//
// Because `codex` and `codex exec` are separate commands with separate
// flag sets, and that flag is `exec`'s. Passing it here is not a
// harmless extra argument: codex answers `error: unexpected argument
// '--skip-git-repo-check' found` and exits 2, which is a session that
// dies on the terminal of whoever opened it, before the first prompt.
// This function passed it for one release, derived by reading Run and
// removing what makes it headless -- the right derivation from the wrong
// premise that the two argv share a flag set.
//
// docs/codex-surface.md is the measurement that says so, and
// surface_capture_test.go is what will not let it come back: every flag
// this function puts on the command line has a row in that capture
// saying the pinned codex takes it *at the top level*.
//
// What Run needs the flag for is not left unhandled here; it is handled
// by the person at the terminal. A granule run has no local directory at
// all, so codex starts wherever this daemon's working directory happens
// to be, and the interactive half does not refuse to start there: it
// asks -- "You are in ... an untrusted directory ... 1. Yes, continue"
// -- and a session, unlike an unattended run, has somebody to answer.
// The capture's own before/after table records that a `[projects."..."]
// trust_level = "trusted"` entry in config.toml would pre-empt the
// question; writeConfig writes no such entry, so a session begins by
// being asked about a directory that is nobody's repository, and neither
// answer changes what the run may reach -- that is decided by the MCP
// server and the sandbox policy.
func (f *Framework) TerminalCommand(ctx context.Context, cfg agent.RunConfig) (*agent.TerminalCommand, error) {
	// Ahead of the version gate, which would otherwise answer first and
	// answer about the wrong thing: it starts the binary on the model
	// machine, and what stops this session is not which codex is there
	// but that the terminal and the process would be on two machines.
	// The same refusal both other frameworks make, for the same reason:
	// a session is a pseudo-terminal grain opens in front of a process
	// it starts itself (pkg/console).
	if f.checkRuntime != nil {
		if err := f.checkRuntime(ctx); err != nil {
			return nil, err
		}
	}
	if f.binPath == "" {
		return nil, fmt.Errorf("codex: this Framework was built without a codex binary to run interactively")
	}
	if f.grainBinaryPath == "" {
		return nil, fmt.Errorf("codex: grainBinaryPath is required")
	}
	mcpArgs, err := f.mcpServerArgs(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// Deliberately no task id, so an interactive session gets the temp
	// CODEX_HOME it has always had rather than the task's own store.
	// Two reasons, and either would do on its own: a session is driven
	// by a person who resumes it by sitting back down at it rather than
	// through agent.RunConfig.ResumeSession, which nothing here reads;
	// and a session can be open while a run of the same task is
	// dispatched, which the store would answer by refusing one of them a
	// home it was counting on (sessionstore.Store.Open's lock).
	home, _, cleanup, err := f.writeConfig(ctx, mcpArgs, "", cfg.CapabilityMCPServers...)
	if err != nil {
		return nil, err
	}

	var args []string
	if f.model != "" {
		// -m/--model, one of the flags docs/codex-surface.md's "where
		// each flag lives" table has codex taking in both positions.
		args = append(args, "--model", f.model)
	}

	env := []string{"CODEX_HOME=" + home, "CODEX_EXEC_SERVER_URL=none"}
	if f.apiKey != nil {
		apiKey, err := f.apiKey(ctx)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("codex: reading the API key: %w", err)
		}
		if apiKey != "" {
			env = append(env, "OPENAI_API_KEY="+apiKey)
		}
	}

	return &agent.TerminalCommand{
		Path: f.binPath,
		Args: args,
		Env:  env,
		Dir:  home,
		// Removed once the session ends rather than once this function
		// returns: the CODEX_HOME has to outlive the call, because the
		// process that reads it has not been started yet. Run gets to
		// defer this; a session's cleanup travels with the command.
		Cleanup: cleanup,
	}, nil
}
