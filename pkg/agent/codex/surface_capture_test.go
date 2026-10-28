package codex

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// docs/codex-surface.md is a capture of what a real codex said about
// itself (scripts/codex-surface.sh, run by
// .github/workflows/codex-surface.yml), and the whole reason it is
// committed rather than left as a CI artifact is so that this package can
// be checked against it. Everything below is that check: no codex, no
// network and no credential, just the binary's own answers read back out
// of the tree.
//
// The check that was missing here is the reason the capture exists at
// all. This package builds two command lines -- codexArgs for an
// unattended run and TerminalCommand for an interactive session -- and
// codex parses them with two different flag sets. The first fails loudly
// the first time a task is dispatched. The second fails in front of
// whoever opens a console, on whichever deployment tries it first, with
// "error: unexpected argument" and a dead session; it did exactly that
// with --skip-git-repo-check, which is exec's flag and was being passed
// at the top level.
//
// Unlike its agy counterpart, this test can also tell that the capture is
// stale: codex is pinned, so TestCapturedCodexIsTheCodexTheImagePins
// compares the version the capture was taken against with the release the
// Dockerfile installs.
const codexSurfaceCapturePath = "../../../docs/codex-surface.md"

func codexCapture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(codexSurfaceCapturePath)
	if err != nil {
		t.Fatalf("reading the committed codex capture: %v", err)
	}
	return string(data)
}

// flagVerdict is one row of the capture's "does this codex accept the
// flag at that position" table: a flag, where on the command line it was
// tried, and what codex said.
type flagVerdict struct {
	accepted bool
	detail   string
}

var flagVerdictLine = regexp.MustCompile(`^(accepted|rejected) +(top-level|exec) +(--[a-z0-9-]+) *(.*)$`)

// capturedFlagVerdicts reads that table, keyed by "position flag".
//
// It parses the rows rather than searching the document for a phrase,
// because a test that greps for "--skip-git-repo-check" would pass on the
// sentence in the prose that explains why the flag is not passed here.
func capturedFlagVerdicts(t *testing.T) map[string]flagVerdict {
	t.Helper()

	verdicts := map[string]flagVerdict{}
	scanner := bufio.NewScanner(strings.NewReader(codexCapture(t)))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		m := flagVerdictLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		verdicts[m[2]+" "+m[3]] = flagVerdict{accepted: m[1] == "accepted", detail: m[4]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the committed codex capture: %v", err)
	}

	if len(verdicts) == 0 {
		t.Fatalf("%s holds no flag verdicts. Either the capture is the placeholder that ships before the first dispatch, or scripts/codex-surface.sh's probe table has changed shape -- and neither is something this package should be asserting against.", codexSurfaceCapturePath)
	}
	return verdicts
}

// flagsIn returns the long flags of an argv, which is what a verdict row
// is about. Values, the `exec` subcommand and the `-` that means stdin
// are not flags and are not probed: codex resolves neither a model name
// nor a path while parsing.
func flagsIn(args []string) []string {
	var flags []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "--") {
			flags = append(flags, arg)
		}
	}
	return flags
}

// TestCapturedCodexAcceptsTheArgvThisPackageBuilds is the check that both
// command lines are command lines codex will parse.
//
// A rejected flag is an "unexpected argument" and exit 2 -- for Run, a
// dispatch that dies before the model sees the prompt; for
// TerminalCommand, the same thing in front of a human who was expecting a
// session. Neither is something any other test in this package can see,
// because every one of them drives this package's own argument building
// rather than a codex.
func TestCapturedCodexAcceptsTheArgvThisPackageBuilds(t *testing.T) {
	verdicts := capturedFlagVerdicts(t)

	f := newTerminalTestFramework("/usr/local/bin/codex", "/usr/local/bin/grain")
	session, err := f.TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	t.Cleanup(func() {
		if session.Cleanup != nil {
			session.Cleanup()
		}
	})

	// A first attempt and a retry's resume, since codexArgs builds both
	// and a session's argv is neither.
	for _, tc := range []struct {
		position string
		args     []string
	}{
		{"exec", f.codexArgs("")},
		{"exec", f.codexArgs("a-thread-id")},
		{"top-level", session.Args},
	} {
		for _, flag := range flagsIn(tc.args) {
			verdict, ok := verdicts[tc.position+" "+flag]
			if !ok {
				t.Errorf("this package passes %s %s and %s has no verdict for it. scripts/codex-surface.sh probes a named list of flags: add this one to it and regenerate the capture (dispatch codex-surface), because an unprobed flag is one nothing here can tell you is spelled right.",
					tc.position, flag, codexSurfaceCapturePath)
				continue
			}
			if !verdict.accepted {
				t.Errorf("this package passes %s %s, and the captured codex refuses it: %s\nThat is not a warning about a future codex -- it is what a run or a session gets today, on the release the Dockerfile pins. Check %s's \"where each flag lives\" table for where the flag actually belongs.",
					tc.position, flag, verdict.detail, codexSurfaceCapturePath)
			}
		}
	}
}

// TestCapturedCodexRefusesTheFlagThatIsNotHere is the same check standing
// the other way round, and it is here so that the finding this capture was
// added for does not quietly become a passing test about nothing.
//
// TerminalCommand does not pass --skip-git-repo-check because codex does
// not take it at the top level. If a future codex promotes the flag, this
// test fails -- and the right answer then is not to add the flag back
// without thinking, but to decide whether a session should be pre-empting
// the trust question a human is there to answer at all (see
// TerminalCommand's own comment, and the capture's before/after table).
func TestCapturedCodexRefusesTheFlagThatIsNotHere(t *testing.T) {
	verdicts := capturedFlagVerdicts(t)

	verdict, ok := verdicts["top-level --skip-git-repo-check"]
	if !ok {
		t.Fatalf("%s has no top-level verdict for --skip-git-repo-check, which is the one probe this capture was added for. scripts/codex-surface.sh should still be asking.", codexSurfaceCapturePath)
	}
	if verdict.accepted {
		t.Errorf("the captured codex now accepts --skip-git-repo-check at the top level, which it did not when TerminalCommand stopped passing it. Read that function's comment before adding it back: what the flag suppresses is a question the person who opened the session is there to answer.")
	}
}

var (
	capturedPinLine     = regexp.MustCompile(`(?m)^pinned by the Dockerfile +(\S.*)$`)
	dockerfilePinLine   = regexp.MustCompile(`(?m)^ARG CODEX_CLI_VERSION=(\S+)$`)
	capturedVersionLine = regexp.MustCompile(`(?m)^captured here +(\S.*)$`)
)

// TestCapturedCodexIsTheCodexTheImagePins is the check docs/agy-surface.md
// cannot have, and the reason this capture records a version at all.
//
// A capture nobody regenerated agrees with itself forever: every test
// above passes just as green against a document describing a codex from
// six releases ago. agy is installed unpinned, so nothing can be said
// about that there. codex is pinned in the Dockerfile, which makes "the
// capture is older than the binary this repository ships" a fact a test
// can hold -- so a CODEX_CLI_VERSION bump now arrives with a
// regeneration, which is the moment somebody should be reading what the
// new release changed about the two command lines above.
func TestCapturedCodexIsTheCodexTheImagePins(t *testing.T) {
	dockerfile, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatalf("reading the Dockerfile: %v", err)
	}
	pin := dockerfilePinLine.FindStringSubmatch(string(dockerfile))
	if pin == nil {
		t.Fatalf("no ARG CODEX_CLI_VERSION in the Dockerfile; this test compares the capture against the release the image installs, and there is nothing to compare it with.")
	}

	capture := codexCapture(t)
	captured := capturedPinLine.FindStringSubmatch(capture)
	if captured == nil {
		t.Fatalf("%s has no \"pinned by the Dockerfile\" line. scripts/codex-surface.sh writes one from the checkout it runs in; a capture without it was taken by something else.", codexSurfaceCapturePath)
	}

	if got, want := strings.TrimSpace(captured[1]), pin[1]; got != want {
		t.Errorf("the Dockerfile pins codex %s and %s was captured against %s.\nThe capture is now about a codex this repository does not ship, and every other test in this file is checking today's argv against yesterday's binary. Dispatch .github/workflows/codex-surface.yml against this branch and merge the pull request it opens into it.",
			want, codexSurfaceCapturePath, got)
	}

	// And that the binary agreed: the pin is a release tag the workflow
	// asked for, this is what the thing that answered called itself.
	version := capturedVersionLine.FindStringSubmatch(capture)
	if version == nil {
		t.Fatalf("%s has no \"captured here\" line, so nothing says which codex actually answered the probes in it.", codexSurfaceCapturePath)
	}
	// rust-v0.153.0 against "codex-cli 0.153.0": the tag carries a
	// prefix the version string does not, and comparing the number is
	// what the two spellings have in common.
	number := strings.TrimPrefix(pin[1], "rust-v")
	if !strings.Contains(version[1], number) {
		t.Errorf("the Dockerfile pins %s, and the codex that answered these probes called itself %q. Either the workflow installed something other than the pinned release, or codex has changed how it spells its version and scripts/codex-surface.sh is reading the wrong line.",
			pin[1], strings.TrimSpace(version[1]))
	}
}
