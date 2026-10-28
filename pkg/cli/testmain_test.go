package cli

import (
	"os"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// forkedSubcommands are the argv[1] values a dispatched run re-invokes
// the grain binary with while it is running: "mcpserver", the MCP server
// every framework points its agent CLI at, and agy's PreToolUse hook.
// Both are forked with the path buildAntigravityFramework (daemon.go)
// reads out of os.Executable() -- which, in the live tests here, is this
// test binary rather than a built grain.
//
// Nothing else in main()'s own switch belongs here. These two are the
// ones a *running daemon* forks, so they are the two a test that stands a
// daemon up has to answer; the rest are things an operator types, and
// re-entering main() for one of those would only widen what a stray argv
// can do to this suite.
var forkedSubcommands = []string{"mcpserver", antigravity.HookSubcommand}

// TestMain does two things.
//
// # It answers as grain when a live run forks this binary
//
// The daemon points every agent run's MCP server and PreToolUse hook at
// os.Executable(): the grain binary already running, re-invoked with
// "mcpserver" (or "agy-tool-hook") as argv[1], rather than a second
// binary a deployment would have to install (main.go's own doc comment).
// Under `go test` that executable is this test binary, and a test binary
// handed "mcpserver" ignores it as a positional argument and runs the
// suite: agy then loads no MCP server at all, so a live run comes up with
// none of grain's tools, and every PreToolUse hook call spends half a
// minute running this whole suite again before failing the tool call with
// "JSON hook ... failed" instead of grain's own denial.
//
// That is exactly what TestRunLiveWithGranuleAndRESTAPIOpensAPullRequest
// hit: a live model reported it had no mcp_grain-sandbox_* tools, reached
// for agy's own run_command, and got a hook failure -- three symptoms of
// one cause, none of which name it. Re-entering main() for the two
// subcommands a run actually forks makes this binary the stand-in for
// grain it was already being used as. Only those two, and only when argv
// names one: `go test` passes flags and never a bare word, so an ordinary
// run of this suite cannot take this path.
//
// # It turns off the check-registration window
//
// The wait that keeps an empty check list from reading as clean until CI
// has had time to register one (orchestrator.SetCheckRegistrationWindow).
//
// The tests here stand a real daemon up against a githubsim over a real
// wall clock, and there is no CI anywhere in that picture: nothing will
// ever report a check run, so every window a pull request here starts
// runs its full two minutes and then ends where it began. Turning it off
// costs these tests no coverage -- the window is about telling "CI has
// not reported yet" from "there is no CI", and this suite only ever has
// the second -- and it is covered where the clock can be handed in, in
// pkg/orchestrator.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && slices.Contains(forkedSubcommands, os.Args[1]) {
		// main() exits by itself on every path that has an exit status
		// to report; reaching the line below means the subcommand
		// finished, which is a zero exit either way.
		Main()
		os.Exit(0)
	}
	restore := orchestrator.SetCheckRegistrationWindow(0)
	code := m.Run()
	restore()
	os.Exit(code)
}
