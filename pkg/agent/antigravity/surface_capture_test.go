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

package antigravity

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// docs/agy-surface.md is a capture of what a real agy said about itself
// (scripts/agy-surface.sh, run by .grain/workflows/agy-surface.yml), and
// the whole reason it is committed rather than left as a CI artifact is so
// that this package can be checked against it. Everything below is that
// check: no agy, no network and no credential, just the binary's own
// answers read back out of the tree.
//
// The Dockerfile pin makes a stale capture detectable: a version bump must
// regenerate this evidence before the package suite will pass.

const surfaceCapturePath = "../../../docs/agy-surface.md"

var (
	dockerfileAgyPin   = regexp.MustCompile(`(?m)^ARG AGY_CLI_VERSION=(\S+)$`)
	capturedAgyVersion = regexp.MustCompile(`(?m)^\$ agy --version\n([^\n]+)$`)
)

// pinnedAgyVersion is the agy release a build of this repository installs,
// read from the Dockerfile ARG that every consumer of the pin derives from:
// the image build itself, Dockerfile.k8s-sandbox (held equal to it by
// tests/deploy), and .grain/workflows/live-agent.yml, which installs it and
// then measures it.
//
// Several things in this package are true of one vendor release and no
// other, so they are checked against this rather than against a version
// somebody typed twice.
func pinnedAgyVersion(t *testing.T) string {
	t.Helper()
	dockerfile, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatalf("reading the Dockerfile: %v", err)
	}
	pin := dockerfileAgyPin.FindStringSubmatch(string(dockerfile))
	if pin == nil {
		t.Fatalf("the Dockerfile has no ARG AGY_CLI_VERSION; image builds must not follow a mutable release manifest")
	}
	return pin[1]
}

func TestCapturedAgyIsTheAgyTheImagePins(t *testing.T) {
	captured := capturedAgyVersion.FindStringSubmatch(capturedSurface(t))
	if captured == nil {
		t.Fatalf("%s has no agy --version result", surfaceCapturePath)
	}
	if got, want := strings.TrimSpace(captured[1]), pinnedAgyVersion(t); got != want {
		t.Errorf("%s was captured with agy %s, want Dockerfile pin %s; regenerate it with .grain/workflows/agy-surface.yml", surfaceCapturePath, got, want)
	}
}

// capturedRoster returns the native tool names in the capture's `init`
// event, and the count agy itself reported alongside them.
//
// It reads the section scripts/agy-surface.sh writes as a sorted list
// under a "native tools: N" line rather than parsing the raw JSON event,
// because that list is the one a human reads in the diff when a new
// capture lands -- a test agreeing with a different copy of the roster
// than the reviewer is looking at would be worth very little.
func capturedRoster(t *testing.T) (names []string, reported int) {
	t.Helper()

	f, err := os.Open(surfaceCapturePath)
	if err != nil {
		t.Fatalf("reading the committed agy capture: %v", err)
	}
	defer f.Close()

	const fence = "``````"
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)

	inRoster := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "native tools: "):
			inRoster = true
			reported, err = strconv.Atoi(strings.TrimPrefix(line, "native tools: "))
			if err != nil {
				t.Fatalf("the capture's roster count is not a number: %q", line)
			}
		case inRoster && strings.HasPrefix(line, fence):
			inRoster = false
		case inRoster && line != "" && !strings.HasPrefix(line, "permission_mode:"):
			names = append(names, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the committed agy capture: %v", err)
	}

	if len(names) == 0 {
		t.Fatalf("%s holds no init-event roster. Either the capture is the placeholder that ships before the first dispatch, or agy stopped emitting the event -- and neither is something this package should be asserting against.", surfaceCapturePath)
	}
	if reported != len(names) {
		t.Errorf("the capture reports %d native tools and lists %d", reported, len(names))
	}
	return names, reported
}

// TestWithheldNativeToolsAreToolsAgyStillHas is the check that a denial
// still denies something.
//
// HookDecision matches a hook payload's tool name against
// withheldNativeTools exactly, so a name agy no longer uses -- renamed,
// dropped, or mistyped when it was added here -- is not a harmless extra
// entry. It is a line of this package's only working defence that can
// never match, and nothing else in the repository would say so: the unit
// tests assert that grain denies the names in this list, which stays true
// however wrong the names are.
func TestWithheldNativeToolsAreToolsAgyStillHas(t *testing.T) {
	roster, _ := capturedRoster(t)
	has := make(map[string]bool, len(roster))
	for _, name := range roster {
		has[name] = true
	}

	for _, withheld := range withheldNativeTools {
		if !has[withheld] {
			t.Errorf("withheldNativeTools denies %q, which the captured agy does not offer. Either agy renamed or dropped it -- in which case this list needs the new name, not just the old one removed -- or the name was never right.", withheld)
		}
	}
}

// TestCapturedRosterHoldsNoUntriagedTool is the check in the other
// direction, and the one with the shorter fuse: a tool agy has *added*
// since withheldNativeTools was written is a tool grain's hook does not
// deny.
//
// The list below is not a second copy of the roster for its own sake --
// the capture is the roster. It is the record of which of those names a
// human has looked at and decided about, which is a different fact and
// one no capture can carry. So a new agy bringing a new tool fails here
// rather than passing quietly, and the fix is to decide: deny it in
// withheldNativeTools if it reads, writes, executes or delegates, and
// otherwise add it here with the rest.
//
// Everything not in withheldNativeTools is here because it does not reach
// the controller's filesystem: agy's browser, its knowledge store, its
// task and inbox bookkeeping, the permission and question tools that ask
// rather than act, and `call_mcp_tool`, which reaches grain's own server
// and nothing else. The two exceptions worth naming are `manage_subagents`
// and `browser_subagent`: they are left alone because neither creates a
// subagent that could hold write tools (`define_subagent` does, and is
// denied), but they are the names to look at first if agy's subagent
// surface changes again.
var triagedNativeTools = []string{
	"ask_custom_permission", "ask_permission", "ask_question",
	"browser_click_element", "browser_drag_pixel_to_pixel",
	"browser_get_dom", "browser_get_network_request", "browser_input",
	"browser_list_network_requests", "browser_mouse_down",
	"browser_mouse_up", "browser_move_mouse", "browser_press_key",
	"browser_refresh_page", "browser_resize_window", "browser_scroll",
	"browser_scroll_dom", "browser_select_option", "browser_subagent",
	"call_mcp_tool", "capture_browser_console_logs",
	"capture_browser_screenshot", "click_browser_pixel",
	"delete_knowledge", "execute_browser_javascript", "finish",
	"generate_image", "list_browser_pages", "list_permissions",
	"list_resources", "manage_inbox", "manage_subagents", "manage_task",
	"open_browser_url", "read_browser_page", "read_resource",
	"read_url_content", "schedule", "search_web", "send_message",
	"wait", "wait_5_seconds",
}

func TestCapturedRosterHoldsNoUntriagedTool(t *testing.T) {
	roster, _ := capturedRoster(t)

	known := make(map[string]bool, len(triagedNativeTools)+len(withheldNativeTools))
	for _, name := range triagedNativeTools {
		known[name] = true
	}
	for _, name := range withheldNativeTools {
		known[name] = true
	}

	for _, name := range roster {
		if !known[name] {
			t.Errorf("the captured agy offers %q, which nothing in this package has an opinion about. Decide: add it to withheldNativeTools if it reads, writes, executes or delegates on the controller, and to triagedNativeTools if it does not.", name)
		}
	}

	// The other way round, so that a tool agy drops does not sit in the
	// triaged list forever pretending someone still checks it.
	offered := make(map[string]bool, len(roster))
	for _, name := range roster {
		offered[name] = true
	}
	for _, name := range triagedNativeTools {
		if !offered[name] {
			t.Errorf("triagedNativeTools names %q, which the captured agy no longer offers; drop it.", name)
		}
	}
}

// TestCapturedPathsAreThePathsThisPackageWritesTo checks the three
// filenames in this package against the capture's own candidate-path
// probes, which plant a marker in every plausible location and report
// which ones agy came back holding.
//
// This is the mistake that has actually happened here (see the README on
// ~/.gemini/settings.json): a config written one directory from where agy
// reads it produces no error at all, just a run with no MCP servers, no
// rules or no hook. Every test in this package that is not this one drives
// a stub, and a stub reads whichever file the test told it to.
func TestCapturedPathsAreThePathsThisPackageWritesTo(t *testing.T) {
	capture, err := os.ReadFile(surfaceCapturePath)
	if err != nil {
		t.Fatalf("reading the committed agy capture: %v", err)
	}
	text := string(capture)

	for _, path := range []string{mcpConfigRelPath, cliSettingsRelPath, hooksConfigRelPath} {
		want := "READ       ~/" + filepath.ToSlash(path)
		if !strings.Contains(text, want) {
			t.Errorf("this package writes ~/%s, and the capture does not report agy reading it. Look for a %q line in %s: if it says \"not listed\", agy is loading nothing this package writes there.",
				filepath.ToSlash(path), "~/"+filepath.ToSlash(path), surfaceCapturePath)
		}
	}
}

// The rest of this file is about the two command lines this package
// builds rather than about what it writes to disk, and it is the check
// that was missing when task-253 gave agy a TerminalCommand.
//
// A session's argument list was derived by reading Run and removing the
// flags that make it headless. That is the right derivation, and it is
// not a measurement: a flag that survives it is asserted against nothing,
// and the way that goes wrong is not subtle. It is "unrecognized flag" on
// the terminal of whoever first opens a session, on whichever deployment
// tries it. codex's own TerminalCommand had exactly that fault -- see
// pkg/agent/codex/surface_capture_test.go -- and nothing here could have
// said so about this one.

var agyHelpFlagLine = regexp.MustCompile(`^\s{2,}(--[a-z][a-z0-9-]*)\s{2,}(\S.*)$`)

// capturedAgyFlags reads the `agy --help` listing out of the capture as
// flag name to agy's own description of it.
//
// The listing rather than the flag table the interactive section also
// writes: that table is this repository's list of what it passes, and
// this one is agy's list of what it takes. Checking the first against the
// second is the whole point, so they have to be read separately.
func capturedAgyFlags(t *testing.T) map[string]string {
	t.Helper()

	flags := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(capturedSurface(t)))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)

	const fence = "``````"
	inHelp := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "$ agy --help":
			inHelp = true
		case inHelp && strings.HasPrefix(line, fence):
			inHelp = false
		case inHelp:
			if m := agyHelpFlagLine.FindStringSubmatch(line); m != nil {
				flags[m[1]] = strings.TrimSpace(m[2])
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the committed agy capture: %v", err)
	}

	if len(flags) == 0 {
		t.Fatalf("%s holds no `agy --help` flag listing. Either the capture is the placeholder that ships before the first dispatch, or agy has changed the shape of its help -- and neither is something this package should be asserting against.", surfaceCapturePath)
	}
	return flags
}

func capturedSurface(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(surfaceCapturePath)
	if err != nil {
		t.Fatalf("reading the committed agy capture: %v", err)
	}
	return string(data)
}

// longFlagsIn returns the long flags of an argv. Values, and agy's own
// short spellings, are not what a help listing is keyed by.
func longFlagsIn(args []string) []string {
	var flags []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "--") {
			flags = append(flags, arg)
		}
	}
	return flags
}

// bothArgv is the two command lines, built the way each is really built:
// a run's from agyArgs, with a conversation ID so the resume flag is in
// it too, and a session's from TerminalCommand.
func bothArgv(t *testing.T) (run, session []string) {
	t.Helper()

	cfg := agent.RunConfig{SandboxRoot: t.TempDir()}
	f := New("/usr/local/bin/agy", "/usr/local/bin/grain")
	run = f.agyArgs(context.Background(), cfg, "a-conversation-id")

	cmd, err := f.TerminalCommand(context.Background(), cfg)
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Cleanup != nil {
			cmd.Cleanup()
		}
	})
	return run, cmd.Args
}

// TestCapturedAgyTakesEveryFlagEitherArgvPasses is the check that both
// command lines are command lines agy will parse.
//
// It is the cheapest of the three and the one that catches the worst
// failure: a flag agy has renamed or dropped is a dispatch that dies
// before the model sees the prompt, or a session that dies before its
// first keystroke.
func TestCapturedAgyTakesEveryFlagEitherArgvPasses(t *testing.T) {
	known := capturedAgyFlags(t)
	run, session := bothArgv(t)

	for _, tc := range []struct {
		what string
		args []string
	}{
		{"an unattended run (agyArgs)", run},
		{"a session (TerminalCommand)", session},
	} {
		for _, flag := range longFlagsIn(tc.args) {
			if _, ok := known[flag]; !ok {
				t.Errorf("%s passes %s, and the captured agy's --help does not list it. Either agy renamed or dropped the flag -- in which case this is what a run or a session is about to be refused over -- or the capture is old enough that it predates it; dispatching agy-surface tells the two apart.",
					tc.what, flag)
			}
		}
	}
}

// flagsDroppedForSomethingOtherThanPrintMode is the record of which flags
// a session drops for a reason of its own, which is a fact no capture can
// carry.
//
// Everything else a session drops has to be a print-mode flag by agy's
// own description of it -- that is the argument for dropping it -- and
// the test below holds this package to that. A flag added to agyArgs and
// not to TerminalCommand therefore has to be either a print-mode flag or
// a decision somebody wrote down here.
var flagsDroppedForSomethingOtherThanPrintMode = map[string]string{
	// resume.go's flag, and a session has nothing to resume: it is
	// started once, by a person, and ends when they close it.
	"--conversation": "resuming a previous conversation is a retry's business, not a session's",
}

// TestFlagsASessionDropsAreThePrintModeFlags checks the derivation itself
// rather than its result: every flag a session drops is one agy calls a
// print-mode flag, or one this package has written a reason down for.
//
// This is what makes the interactive section of the capture worth
// generating. `--input-format`, `--output-format` and `--print-timeout`
// are mechanical removals -- agy's own help says each is about print mode
// -- and so, by that same description, is `--disable-slash-commands`,
// which is the one that is a judgement: what it disables is half of what
// somebody sits down at this CLI for, and `/exit` is one of the three
// ways a session ends at all. The capture records agy refusing `-p
// "/exit"` by name, which is what makes "/exit belongs to the session"
// evidence rather than an assumption.
func TestFlagsASessionDropsAreThePrintModeFlags(t *testing.T) {
	known := capturedAgyFlags(t)
	run, session := bothArgv(t)

	kept := make(map[string]bool, len(session))
	for _, flag := range longFlagsIn(session) {
		kept[flag] = true
	}

	dropped := 0
	for _, flag := range longFlagsIn(run) {
		if kept[flag] {
			continue
		}
		dropped++
		if _, ok := flagsDroppedForSomethingOtherThanPrintMode[flag]; ok {
			continue
		}
		description, ok := known[flag]
		if !ok {
			// Reported by the test above; nothing to add here.
			continue
		}
		if !strings.Contains(strings.ToLower(description), "print mode") {
			t.Errorf("a session drops %s, which agy describes as %q -- not a print-mode flag. A session drops print mode and nothing else, so either this flag should be in TerminalCommand's argument list too, or the reason it is not belongs in flagsDroppedForSomethingOtherThanPrintMode with the rest.",
				flag, description)
		}
	}
	if dropped == 0 {
		t.Errorf("a session's argv drops none of the run's flags, which cannot be right: print mode is the difference between them. Something has gone wrong in how this test builds the two.")
	}
}

// TestTheCapturedFlagTableNamesEveryFlagThisPackagePasses keeps the one
// hand-maintained list in scripts/agy-surface.sh honest.
//
// That script discovers almost everything it records, but the interactive
// section's flag table cannot: it is this repository's list of what it
// passes, quoted back with agy's description of each. A flag added to
// either argv and not to that list is a flag the capture silently stops
// covering -- so the failure lands here, next to the code that added it,
// rather than in a document nobody rereads.
func TestTheCapturedFlagTableNamesEveryFlagThisPackagePasses(t *testing.T) {
	capture := capturedSurface(t)
	const heading = "$ the flags pkg/agent/antigravity passes, as agy --help describes them"
	if !strings.Contains(capture, heading) {
		t.Skipf("%s predates the interactive section (no %q block); dispatch agy-surface to regenerate it.", surfaceCapturePath, heading)
	}

	run, session := bothArgv(t)
	for _, flag := range append(longFlagsIn(run), longFlagsIn(session)...) {
		// The table is one line per flag, the flag first.
		if !strings.Contains(capture, "\n"+flag+" ") {
			t.Errorf("this package passes %s and the capture's flag table has no row for it. Add it to grain_flags in scripts/agy-surface.sh and regenerate (dispatch agy-surface), so that what agy says about the flag is written down where the next reader of TerminalCommand can find it.", flag)
		}
	}
}
