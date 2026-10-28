package e2e

// The two halves of "grain writes a denial into every run's HOME" that a
// real agy can be asked about without spending a model turn: did it load
// the hook, and did it load the permission rules.
//
// Everything else in this repository that covers those files reads the
// bytes agent/antigravity produces, which says that grain wrote what it
// meant to write and nothing at all about whether agy opened the file.
// That distinction is not academic here. agy validates neither file on
// load -- a hooks.json it cannot make sense of leaves a run with no hooks
// and no complaint, an unknown settings key is ignored in silence -- so
// the failure mode this pair guards against is not an error message, it
// is a run that quietly has no denial in front of its tools. grain has
// already shipped exactly that once, with an MCP config written to a path
// agy stopped reading, and a green suite the whole time.
//
// Print mode is what makes this cheap: `agy -p /hooks` and
// `agy -p /permissions` answer out of the config it just loaded, without
// an agent turn and without a valid credential, so these tests need agy
// on PATH and nothing else. They sit here rather than in
// pkg/agent/antigravity because that package's tests must pass on the
// pull-request runner, which has no agy -- and they are named TestLive*
// so live-agent.yml's nightly `-run TestLive` picks them up beside the
// tests that do spend a credential.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
)

// TestLiveAgyLoadsGrainsHookConfig asserts that the hooks.json
// agent/antigravity writes is the file a real agy reads, in the place it
// reads it, in a shape it understands.
//
// It is the question underneath TestLiveNativeToolsAreDenied, asked
// without a model: that test can only see a denial when a live model
// chooses to reach for a native tool, and a run where it never does
// proves nothing about the hook. This one proves the hook is loaded
// whatever the model does.
func TestLiveAgyLoadsGrainsHookConfig(t *testing.T) {
	t.Parallel()
	agyPath := liveAgyBinary(t)
	grainPath := buildGrainBinary(t)
	home := liveRunHome(t, grainPath)

	out := agyPrintMode(t, agyPath, home, "/hooks")
	t.Logf("agy -p /hooks with grain's HOME:\n%s", out)

	// One record per loaded hook, tab separated: name, whether it is
	// enabled, the event, the matcher, the hook type, the command. Each
	// field is asserted on its own rather than as one exact line, since
	// the ordering and the spacing of that record are agy's to change and
	// none of this depends on them.
	for _, want := range []string{
		antigravity.HookName,
		"PreToolUse",
		"enabled",
		grainPath,
		antigravity.HookSubcommand,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agy -p /hooks does not mention %q; it loaded:\n%s\n"+
				"grain's hook is not in front of this run's tool calls. Check that agent/antigravity's "+
				"hooksConfigRelPath is still where this agy build reads hooks from, and that hookConfigJSON "+
				"is still a shape it accepts -- an unreadable hooks.json loads as no hooks at all, silently.",
				want, out)
		}
	}

	// The control, because "contains the hook's name" is only evidence if
	// the same command says nothing when the file is not there. Without
	// this, an agy that echoed its argument, or one that printed some
	// built-in default carrying grain's name for unrelated reasons, would
	// pass the assertions above.
	bare := liveRunHome(t, grainPath)
	if err := os.Remove(antigravity.HooksConfigPathForTest(bare)); err != nil {
		t.Fatalf("removing hooks.json from the control HOME: %v", err)
	}
	if control := agyPrintMode(t, agyPath, bare, "/hooks"); strings.Contains(control, antigravity.HookName) {
		t.Errorf("agy -p /hooks named grain's hook for a HOME with no hooks.json:\n%s\n"+
			"So the assertions above are not evidence that agy read grain's file.", control)
	}

	// And the reason any of this is asserted rather than assumed: a
	// hooks.json agy cannot read is not an error. It loads no hooks,
	// says nothing, and leaves the binary working -- which from grain's
	// side is a run whose every tool call goes straight past the denial.
	// The day hookConfigJSON emits a shape this agy no longer accepts
	// looks exactly like this.
	if err := os.WriteFile(antigravity.HooksConfigPathForTest(bare), []byte("not json at all"), 0o600); err != nil {
		t.Fatalf("writing an unreadable hooks.json: %v", err)
	}
	broken, err := agyPrintModeResult(t, agyPath, bare, "/hooks")
	switch {
	case err != nil:
		t.Logf("agy now refuses to start on an unreadable hooks.json (%v): it fails loudly rather than "+
			"silently, which makes the assertions above cheaper than they need to be rather than wrong", err)
	case strings.Contains(broken, antigravity.HookName):
		t.Errorf("agy -p /hooks named grain's hook for a HOME whose hooks.json is not JSON:\n%s", broken)
	default:
		t.Logf("an unreadable hooks.json loads as no hooks, exit 0, no message: the silent failure this test exists for")
	}
}

// TestLiveAgyLoadsGrainsPermissionRules asserts the other file: that the
// permissions block in settings.json reaches agy as rules rather than
// being dropped on the floor.
//
// Those rules stop nothing while Run passes
// --dangerously-skip-permissions (see permissionRules, and the README
// section this test's neighbours are cited in), so this is not a test of
// a denial that bites today. It is what would have to be true first for
// dropping that flag to be safe: agy's default permission mode is
// request-review, a headless run soft-denies anything needing
// confirmation, and a run whose allow rules did not reach agy would
// therefore be a run that can call none of grain's own tools. The rules
// being loaded is the cheap half of that question and this answers it
// nightly; whether the mode change is safe is the expensive half, and it
// needs a live turn -- which TestLiveRunWithAmbientPermissions now spends,
// in this same nightly, by exercising the supported framework option.
func TestLiveAgyLoadsGrainsPermissionRules(t *testing.T) {
	t.Parallel()
	agyPath := liveAgyBinary(t)
	home := liveRunHome(t, buildGrainBinary(t))

	settingsPath := antigravity.CLISettingsPathForTest(home)
	written, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading the settings agent/antigravity wrote: %v", err)
	}
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(written, &settings); err != nil {
		t.Fatalf("decoding %s: %v", settingsPath, err)
	}
	if len(settings.Permissions.Allow) == 0 || len(settings.Permissions.Deny) == 0 {
		t.Fatalf("agent/antigravity wrote no allow or no deny rules (%s), so this test would assert nothing", written)
	}

	// Expectations read off the file grain just wrote rather than listed
	// here: which tools are allowed and which denied is permissionRules'
	// to decide, and a second copy of that list in this package would go
	// stale the first time a name is added to it.
	out := agyPrintMode(t, agyPath, home, "/permissions")
	t.Logf("agy -p /permissions with grain's HOME:\n%s", out)
	for decision, names := range map[string][]string{
		"allow": settings.Permissions.Allow,
		"deny":  settings.Permissions.Deny,
	} {
		for _, name := range names {
			// agy prints one tab-separated record per rule, scope first:
			// "global<TAB>deny<TAB>run_command".
			if record := fmt.Sprintf("%s\t%s", decision, name); !strings.Contains(out, record) {
				t.Errorf("agy -p /permissions has no %q rule for %s; it loaded:\n%s\n"+
					"A malformed permissions block is dropped in silence by this binary -- no rule and no "+
					"complaint -- so this is what standing between permissionRules and nothing looks like.",
					decision, name, out)
			}
		}
	}

	// And what that launch did to the file it read. agy 1.1.26 rewrites
	// settings.json on every start, keeping the keys it owns
	// (modelProvider) and dropping the permissions block entirely -- so
	// the rules live for exactly the one launch that finds them.
	//
	// That used to be survivable only because writeAgyHome builds a fresh
	// HOME per run and Run started agy exactly once in it. Run does start
	// agy again now -- resuming a conversation the provider cut short,
	// see agent/antigravity's resume.go -- which is exactly the change
	// this paragraph warned about, and refreshAgyHome is the answer to
	// it: every attempt after the first is handed this run's own
	// configuration again, written over whatever the last launch left
	// behind. Logged rather than failed, in both directions -- a binary
	// that keeps what it loads is no problem for grain, and the
	// assertion that catches a Run which forgot to rewrite the file is
	// one about Run, in that package's own tests
	// (TestARetryGetsThisRunsConfigurationBack). What this leaves behind
	// is the record: the nightly log says which way this agy behaves, so
	// nobody has to measure it again.
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("re-reading the settings after agy read them: %v", err)
	}
	if bytes.Contains(after, []byte("permissions")) {
		t.Logf("agy left the permissions block in %s (%s): it no longer erases what it loads, "+
			"which makes the paragraph above -- and this assertion -- stale rather than wrong",
			settingsPath, after)
	}
	if second := agyPrintMode(t, agyPath, home, "/permissions"); strings.TrimSpace(second) != "" {
		t.Logf("a second launch in the same HOME still has rules:\n%s", second)
	} else {
		t.Logf("a second launch in the same HOME has no rules at all: agy erased what it loaded "+
			"from %s, which is why a run gets a HOME of its own", settingsPath)
	}
}

// liveAgyBinary is the gate these tests share with the two that spend a
// credential: no agy on PATH is a skip, and a failure once
// GRAIN_LIVE_AGENT_TEST says a live agy was supposed to be here. No
// credential is required -- see agyPrintMode.
func liveAgyBinary(t *testing.T) string {
	t.Helper()
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}
	return agyPath
}

// liveRunHome builds the private HOME a dispatch would have handed agy,
// with grain's MCP config, settings and hooks in it, and cleans it up
// with the test.
func liveRunHome(t *testing.T, grainPath string) string {
	t.Helper()
	// The arguments a host-rooted run's forked MCP server would have been
	// given. Nothing here starts that server; they are written into the
	// config for shape's sake.
	home, cleanup, err := antigravity.WriteRunHomeForTest(context.Background(), grainPath, []string{"mcpserver", "-sandbox-root", t.TempDir()}, true)
	if err != nil {
		t.Fatalf("building the private HOME a run gets: %v", err)
	}
	t.Cleanup(cleanup)
	return home
}

// agyPrintMode runs one of agy's slash commands in print mode against a
// HOME and returns its stdout.
//
// The environment is built rather than inherited, exactly as
// Framework.Run builds one: a HOME of the controller's would send agy to
// the controller's own config, which is the one thing these tests must
// not read. PATH comes along because agy is a wrapper that finds its own
// runtime through it.
//
// GEMINI_API_KEY is set to whatever this runner has, or to a placeholder
// when it has none. agy refuses to start at all when settings.json names
// modelProvider "gemini" and no key is in the environment ("Set
// GEMINI_API_KEY to your Gemini API key"), but it never spends one here:
// /hooks and /permissions answer out of the loaded config, and both were
// measured answering in full against a key of "not-a-real-key". So these
// tests cost no quota even on the nightly that has a real credential.
func agyPrintMode(t *testing.T, agyPath, home, slashCommand string) string {
	t.Helper()
	out, err := agyPrintModeResult(t, agyPath, home, slashCommand)
	if err != nil {
		t.Fatalf("agy -p %s: %v\nstdout:\n%s", slashCommand, err, out)
	}
	return out
}

// agyPrintModeResult is the same, for the one caller that is asking what
// agy does with a config it cannot read and must not fail when the answer
// is "refuses to start".
func agyPrintModeResult(t *testing.T, agyPath, home, slashCommand string) (string, error) {
	t.Helper()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = "grain-print-mode-probe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agyPath, "-p", slashCommand)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GEMINI_API_KEY=" + apiKey}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w\nstderr:\n%s", err, stderr.String())
	}
	return stdout.String(), nil
}
