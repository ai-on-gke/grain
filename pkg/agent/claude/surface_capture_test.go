package claude

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// docs/claude-surface.md is a capture of what a real claude said about
// itself (scripts/claude-surface.sh, run by
// .github/workflows/claude-surface.yml), and the whole reason it is
// committed rather than left as a CI artifact is so that this package can
// be checked against it. Everything below is that check: no claude, no
// network and no credential, just the binary's own answers read back out
// of the tree.
//
// The check that was missing here is the reason the capture exists at
// all. This package builds two command lines -- Run's own argv (plus
// retry.go's --max-turns and --resume) for an unattended run, and
// TerminalCommand's for an interactive session -- and the second was
// derived from the first by hand: take away -p and the two flags that
// exist to make print mode parseable. The first fails loudly the first
// time a task is dispatched. The second fails in front of whoever opens a
// console, on whichever deployment tries it first, with a dead session.
//
// That same derivation is what put `codex exec`'s --skip-git-repo-check
// on an interactive codex session, which docs/codex-surface.md's first
// run found. Nothing had ever run `claude --help` to check this one.
//
// Unlike its codex counterpart, this test cannot tell that the capture is
// stale: claude is installed unpinned (the Dockerfile runs
// https://claude.ai/install.sh), so there is no version to compare it
// with -- exactly docs/agy-surface.md's position.
// TestClaudeIsStillInstalledUnpinned below is what turns that from an
// assumption into something the tree checks.
const claudeSurfaceCapturePath = "../../../docs/claude-surface.md"

func claudeCapture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(claudeSurfaceCapturePath)
	if err != nil {
		t.Fatalf("reading the committed claude capture: %v", err)
	}
	return string(data)
}

// flagVerdict is one row of the capture's "does this claude accept the
// flag in that argv" table: a flag, which of the two argv it was tried
// in, and what claude said.
type flagVerdict struct {
	accepted bool
	detail   string
}

// Upper case in the flag pattern is not decoration: --allowedTools is
// camelCase, and it is one of the four arguments this package's whole
// sandboxing rests on.
var flagVerdictLine = regexp.MustCompile(`^(accepted|rejected) +(print|session) +(--[A-Za-z0-9-]+) *(.*)$`)

// capturedFlagVerdicts reads that table, keyed by "position flag".
//
// It parses the rows rather than searching the document for a phrase,
// because a test that greps for "--max-turns" would pass on the sentence
// in the prose that explains why that flag is not in claude's own --help.
func capturedFlagVerdicts(t *testing.T) map[string]flagVerdict {
	t.Helper()

	verdicts := map[string]flagVerdict{}
	scanner := bufio.NewScanner(strings.NewReader(claudeCapture(t)))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		m := flagVerdictLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		verdicts[m[2]+" "+m[3]] = flagVerdict{accepted: m[1] == "accepted", detail: m[4]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the committed claude capture: %v", err)
	}

	if len(verdicts) == 0 {
		t.Fatalf("%s holds no flag verdicts. Either the capture is the placeholder that ships before the first dispatch, or scripts/claude-surface.sh's probe table has changed shape -- and neither is something this package should be asserting against.", claudeSurfaceCapturePath)
	}
	return verdicts
}

// flagsIn returns the long flags of an argv, which is what a verdict row
// is about. Values, the `-p` that selects print mode and the prompt (which
// never reaches argv at all) are not flags and are not probed: claude
// resolves neither a model name nor a path while parsing.
func flagsIn(args []string) []string {
	var flags []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "--") {
			flags = append(flags, arg)
		}
	}
	return flags
}

// headlessArgs is the argv Run actually puts on the command line, taken
// the way every other test in this package takes it: through a runner
// that records its arguments instead of starting a binary.
//
// Built rather than written down, so that a flag added to Run arrives
// here without anybody remembering to add it -- an unprobed flag then
// fails the test below rather than going unmeasured.
func headlessArgs(t *testing.T) []string {
	t.Helper()
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "/usr/local/bin/grain")
	if _, err := f.Run(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return fake.gotArgs
}

// TestCapturedClaudeAcceptsTheArgvThisPackageBuilds is the check that both
// command lines are command lines claude will parse.
//
// A rejected flag is `error: unknown option` and a non-zero exit -- for
// Run, a dispatch that dies before the model sees the prompt; for
// TerminalCommand, the same thing in front of a human who was expecting a
// session. Neither is something any other test in this package can see,
// because every one of them drives this package's own argument building
// rather than a claude.
func TestCapturedClaudeAcceptsTheArgvThisPackageBuilds(t *testing.T) {
	verdicts := capturedFlagVerdicts(t)

	f := New("/usr/local/bin/claude", "/usr/local/bin/grain")
	session, err := f.TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	t.Cleanup(func() {
		if session.Cleanup != nil {
			session.Cleanup()
		}
	})

	// A first attempt, a retry that resumes under a turn budget, and a
	// session -- the three argv this package can put on a command line.
	// The middle one is built through attemptArgs itself rather than by
	// listing --max-turns and --resume here, because that function is
	// where a retry's argv comes from in production too.
	headless := headlessArgs(t)
	for _, tc := range []struct {
		position string
		args     []string
	}{
		{"print", headless},
		{"print", attemptArgs(attemptSetup{args: headless, maxTurns: 20}, 12, "a-session-id")},
		{"session", session.Args},
	} {
		for _, flag := range flagsIn(tc.args) {
			verdict, ok := verdicts[tc.position+" "+flag]
			if !ok && awaitingProbe[flag] && capturedFlagListed(t, flag) {
				continue
			}
			if !ok {
				t.Errorf("this package passes %s %s and %s has no verdict for it. scripts/claude-surface.sh probes a named list of flags: add this one to it and regenerate the capture (dispatch claude-surface), because an unprobed flag is one nothing here can tell you is spelled right.",
					tc.position, flag, claudeSurfaceCapturePath)
				continue
			}
			if !verdict.accepted {
				t.Errorf("this package passes %s %s, and the captured claude refuses it: %s\nThat is not a warning about a future claude -- it is what a run or a session gets today, on the release https://claude.ai/install.sh was serving when the capture was taken. Check %s's flag listing for where the flag went.",
					tc.position, flag, verdict.detail, claudeSurfaceCapturePath)
			}
		}
	}
}

// awaitingProbe are flags this package passes that scripts/claude-surface.sh
// now probes but the committed capture predates: each is still checked
// against the capture's own flag listing (capturedFlagListed), so a
// misspelling fails here, and the next regenerated capture gives it a
// verdict row, which then wins. --settings carries the stop rule's Stop
// hook (stophook.go); 2.1.286's help lists it as
// "--settings <file-or-json>" for both positions.
var awaitingProbe = map[string]bool{"--settings": true}

// capturedFlagListed reports whether the capture's flag listing -- one
// flag per line -- names flag exactly.
func capturedFlagListed(t *testing.T, flag string) bool {
	t.Helper()
	for _, line := range strings.Split(claudeCapture(t), "\n") {
		if strings.TrimSpace(line) == flag {
			return true
		}
	}
	return false
}

var capturedVersionLine = regexp.MustCompile(`(?m)^captured here +(\S.*)$`)

// TestCapturedClaudeSaysWhichClaudeAnswered is the least this capture has
// to say about itself.
//
// It cannot say whether it is current -- see the file comment, and the
// test below -- so the one thing that keeps it readable as evidence is
// that it names the claude the probes were run against. A capture that
// records no version is one nobody can date, and every assertion above it
// is then about a binary nobody can identify.
func TestCapturedClaudeSaysWhichClaudeAnswered(t *testing.T) {
	version := capturedVersionLine.FindStringSubmatch(claudeCapture(t))
	if version == nil {
		t.Fatalf("%s has no \"captured here\" line, so nothing says which claude actually answered the probes in it. scripts/claude-surface.sh writes one from `claude --version`; a capture without it was taken by something else.", claudeSurfaceCapturePath)
	}
	if strings.HasPrefix(strings.TrimSpace(version[1]), "[") {
		t.Errorf("%s says the claude it was taken against was %s -- the script's own placeholder for a binary that printed nothing. Dispatch .github/workflows/claude-surface.yml and read what came back before trusting anything else in that file.",
			claudeSurfaceCapturePath, strings.TrimSpace(version[1]))
	}
}

var (
	dockerfileClaudePin = regexp.MustCompile(`(?m)^ARG CLAUDE_CLI_VERSION=(\S+)$`)
	capturedInstallLine = regexp.MustCompile(`(?m)^installed by the Dockerfile +(\S.*)$`)
)

// TestCapturedClaudeIsTheClaudeTheImagePins makes a CLI upgrade an explicit
// source change and makes the committed surface capture fail stale.
func TestCapturedClaudeIsTheClaudeTheImagePins(t *testing.T) {
	dockerfile, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatalf("reading the Dockerfile: %v", err)
	}
	pin := dockerfileClaudePin.FindStringSubmatch(string(dockerfile))
	if pin == nil {
		t.Fatalf("the Dockerfile has no ARG CLAUDE_CLI_VERSION; image builds must not follow the installer's mutable latest release")
	}

	// And that the capture agrees, which is what makes the paragraph in
	// it about being unpinned a measurement rather than a claim: the
	// script writes this line by reading the Dockerfile of the checkout it
	// ran in.
	installed := capturedInstallLine.FindStringSubmatch(claudeCapture(t))
	if installed == nil {
		t.Fatalf("%s has no \"installed by the Dockerfile\" line. scripts/claude-surface.sh writes one from the checkout it runs in; a capture without it was taken by something else.", claudeSurfaceCapturePath)
	}
	wantInstall := "pinned: ARG CLAUDE_CLI_VERSION=" + pin[1]
	if got := strings.TrimSpace(installed[1]); got != wantInstall {
		t.Errorf("%s says claude was %q when the Dockerfile pins %s; regenerate it with .github/workflows/claude-surface.yml", claudeSurfaceCapturePath, got, pin[1])
	}

	version := capturedVersionLine.FindStringSubmatch(claudeCapture(t))
	if version == nil || strings.Fields(version[1])[0] != pin[1] {
		got := "missing"
		if version != nil {
			got = strings.TrimSpace(version[1])
		}
		t.Errorf("%s was captured with %s, want pinned Claude %s", claudeSurfaceCapturePath, got, pin[1])
	}
}
