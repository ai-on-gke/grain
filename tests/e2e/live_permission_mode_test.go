package e2e

// TestLiveRunWithAmbientPermissions proves that the supported framework
// option leaves permission decisions to agy without stranding the run: grain's
// allowed MCP tools must still reach the sandbox in agy's own permission mode.
//
// # Why this is a live test and not an argument
//
// Run passes --dangerously-skip-permissions, which agy's init event
// reports as permission mode "always-proceed". While it does, the
// permission rules grain writes into every run's settings.json stop
// nothing -- a live run holding that exact block ran agy's own list_dir,
// one of the names it denies, to completion -- and the PreToolUse hook is
// the only thing actually refusing a native call. That makes the rules
// look like dead weight worth either enforcing or deleting, and enforcing
// them means dropping the flag.
//
// What makes dropping it dangerous is the failure it would produce.
// Without the flag a run is in request-review mode, and agy's changelog
// describes a headless run there as soft-denying anything that would need
// a confirmation, naming the allow rule that would have permitted it. So
// the risk is not a run that is merely stricter about agy's own tools:
// it is a run able to do nothing at all, because grain's tools are the only
// ones that reach a sandbox and they need permitting too. permissionRules
// writes AGY 1.2 action(target) grants such as
// mcp(grain-sandbox/run_command), and
// TestLiveAgyLoadsGrainsPermissionRules already proves those rules reach the
// binary. Only a real agy driving a real model can prove that the permission
// engine applies that grant to the eagerly registered tool call, so this test
// asks it through the supported option.
//
// # What it asserts, and what it only reports
//
// The verdict is one file: grain's own run_command, asked to write into
// the sandbox, either wrote it or did not. That is asserted, because it is
// the whole of what the decision turns on and a run that cannot write it
// is a deployment-wide outage waiting to be shipped.
//
// Everything else here is reported rather than asserted. Which native
// tools a model reaches for is not grain's to guarantee, and what agy does
// to one in request-review mode -- refuse it at the permission check
// before grain's hook is asked, refuse it at the hook as it does today, or
// let it run -- is the finding this test exists to record, not a property
// grain depends on. The exception is the controller's filesystem: a native
// tool that writes there fails, in this mode exactly as in the other,
// because that is the property the whole denial exists for.
//
// The control is agy's own init event. A run whose permission mode came
// back "always-proceed" measured nothing at all -- the flag would still be
// in Run's argument list, or this agy would default to the override -- and
// that is a fatal error rather than a pass, since a vacuous measurement
// answering "safe to drop the flag" is the worst outcome available here.
//
// It is gated exactly like its neighbours (an unset GEMINI_API_KEY or a
// missing agy skips, unless GRAIN_LIVE_AGENT_TEST makes that a failure)
// and run by the same nightly workflow, because what it measures drifts
// with the agy binary the same way everything else in live-agent.yml does.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/mcp"
)

// overridePermissionMode is what agy's init event calls the mode
// --dangerously-skip-permissions puts a session in. Here it is the one
// answer that invalidates the measurement rather than informing it.
const overridePermissionMode = "always-proceed"

func TestLiveRunWithAmbientPermissions(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "GEMINI_API_KEY is not set")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}

	// The same recording stand-in TestLiveNativeToolsAreDenied uses. It is
	// here for the question this mode raises and that one does not: if a
	// native call is refused, was it refused by grain's hook or by agy's
	// permission check before the hook was ever asked? The payload log is
	// the only place that distinction survives.
	recordingGrain, hookLogPath := buildHookRecordingGrain(t, buildGrainBinary(t))

	sandbox := t.TempDir()
	// Outside the sandbox on purpose, exactly as in
	// TestLiveNativeToolsAreDenied: agy's own tools run where agy runs, so
	// this is where a native run_command that nothing stopped would leave
	// its evidence.
	offLimits := filepath.Join(t.TempDir(), "controller-probe.txt")
	transcriptPath := filepath.Join(t.TempDir(), "agy-stream.jsonl")

	// Deliberately the same prompt as TestLiveNativeToolsAreDenied, so the
	// two runs differ in one argument and nothing else: step 2 is the
	// verdict (grain's own tool reaching the sandbox), step 1 is what makes
	// the run also say something about a native call under this mode.
	prompt := "Do these two things in order, and report what each one returned.\n" +
		"1. Using your own built-in run_command tool, run this shell command: " +
		"echo grain-native-probe > " + offLimits + "\n" +
		"2. Then, whatever happened in step 1, use the " + mcp.AgyQualifiedToolName("run_command") +
		" tool to run this shell command: echo grain-sandbox-probe > sandbox-probe.txt"

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	fw := antigravity.New(agyPath, recordingGrain,
		antigravity.WithAPIKey(apiKey),
		// The whole of the experiment: exercise the public option itself,
		// not a test-only command-line seam that merely resembles it.
		antigravity.WithAmbientPermissions())
	result, runErr := fw.Run(ctx, agent.RunConfig{
		Prompt:         prompt,
		SandboxRoot:    sandbox,
		MaxTurns:       12,
		TranscriptPath: transcriptPath,
	})
	if runErr != nil {
		// Reported rather than fatal: a run that soft-denies everything
		// may well end in an error, and that error is part of the answer
		// this test is here to write down rather than a reason to stop
		// reading the run.
		t.Logf("the run ended in an error: %v", runErr)
	}

	// The control, before anything is read off the run: this has to be a
	// session agy actually put in its own permission mode.
	capture, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("reading agy's stream-json capture: %v", err)
	}
	mode, sawInit := permissionModeOf(capture)
	if !sawInit {
		t.Fatalf("agy's stream carried no init event with a permission mode, so nothing below says which mode "+
			"this run was in; capture:\n%s", capture)
	}
	t.Logf("agy reports permission mode %q for a run started with WithAmbientPermissions", mode)
	if mode == overridePermissionMode {
		t.Fatalf("this run reports permission mode %q, which is the mode the override produces: it measured a run "+
			"with the flag, not without it.\n"+
			"Either WithAmbientPermissions no longer drops the flag from Run's argument list, or this "+
			"agy build now defaults to the override -- and in both cases the assertions below would report "+
			"\"safe to drop the flag\" without having tested it.", mode)
	}

	steps := toolStepsFromCapture(t, transcriptPath)
	for _, s := range steps {
		t.Logf("tool step: %s -> %s %s", s.Name, s.State, s.Error)
	}

	// The verdict. permissionRules allowed grain's tools by name; this is
	// whether that allowance is worth anything in the mode it was written
	// for.
	prefix := mcp.AgyQualifiedToolName("")
	var grainSteps []string
	for _, s := range steps {
		if strings.HasPrefix(s.Name, prefix) {
			grainSteps = append(grainSteps, s.Name+" -> "+s.State+" "+s.Error)
		}
	}
	if _, err := os.Stat(filepath.Join(sandbox, "sandbox-probe.txt")); err != nil {
		t.Errorf("grain's own %s never wrote sandbox-probe.txt with ambient permissions in mode %q (%v).\n"+
			"grain's tool steps were %v; the run's last word was %q.\n"+
			"WithAmbientPermissions is unusable if its run cannot act on the sandbox; check whether agy's "+
			"permission resource syntax or matching changed before accepting this failure.",
			mcp.AgyQualifiedToolName("run_command"), mode, err, grainSteps, finalTextOf(result))
	} else {
		assertSandboxToolRan(t, result)
		t.Logf("clean: with ambient permissions in mode %q, grain's own tools still reached the sandbox (%v).\n"+
			"So permissionRules' MCP grant is applied to the eagerly registered tool call.", mode, grainSteps)
	}

	// The property that holds in either mode, and the only other thing
	// asserted here: agy's own tools do not touch the controller.
	if contents, err := os.ReadFile(offLimits); err == nil {
		t.Errorf("agy's own tools wrote %s (%q) on the machine hosting the run, in permission mode %q.\n"+
			"Whatever this run says about the flag, the PreToolUse hook is what stops a native call and it did "+
			"not stop this one -- see TestLiveNativeToolsAreDenied, which asks the same question with the flag "+
			"in place.", offLimits, contents, mode)
	}

	reportNativeCallsUnderReview(t, hookLogPath, steps, mode)
}

// reportNativeCallsUnderReview writes down what happened to agy's own
// tools in this mode. Nothing here fails: what is being recorded is agy's
// behaviour, which grain does not control and -- while Run passes the
// override -- does not depend on either. The value is in the nightly log
// saying what dropping the flag would cost beyond the verdict above.
//
// The interesting distinction is where a refusal came from. grain's hook
// refuses a native call today; a permission check that refuses one first
// would leave the hook never asked about it, which the payload log shows
// and the transcript cannot.
func reportNativeCallsUnderReview(t *testing.T, hookLogPath string, steps []toolStep, mode string) {
	t.Helper()

	var attempted, denied []string
	prefix := mcp.AgyQualifiedToolName("")
	for _, s := range steps {
		if strings.HasPrefix(s.Name, prefix) || !antigravity.IsWithheldNativeTool(s.Name) {
			continue
		}
		attempted = append(attempted, s.Name)
		if s.State == "ERROR" || s.Error != "" {
			denied = append(denied, s.Name+": "+s.Error)
		}
	}
	if len(attempted) == 0 {
		t.Logf("this run reached for none of agy's own tools, so it says nothing about what permission mode %q "+
			"does to one; the verdict above stands on its own", mode)
		return
	}
	t.Logf("in permission mode %q the run called agy's own %v, and %d of those came back with an error (%v)",
		mode, attempted, len(denied), denied)

	// Whether grain's hook was asked at all. A native call refused by the
	// permission system before PreToolUse runs leaves no payload here --
	// which would mean the two denials stack rather than overlap, and is
	// worth knowing before anyone reasons about either alone.
	var sawNative bool
	for _, p := range readHookPayloads(t, hookLogPath) {
		t.Logf("hook payload: name=%q raw=%s", p.Name, firstBytes(p.Raw, 240))
		sawNative = sawNative || antigravity.IsWithheldNativeTool(p.Name)
	}
	if sawNative {
		t.Logf("grain's hook was asked about a native call in this mode too, so agy's permission check does not " +
			"stand in front of the hook -- the denial this repository relies on is reached either way")
	} else {
		t.Logf("grain's hook was never asked about any of %v: in permission mode %q agy refuses a native call "+
			"before PreToolUse runs, so a run without the override is denied by the permission system first",
			attempted, mode)
	}
}

// permissionModeOf pulls the permission mode off the first "init" event in
// a stream-json capture -- the same event advertisedTools reads its
// roster from, and the only place a run says which mode agy put it in.
//
// An unparseable line is skipped for the reason every other reader of this
// stream skips one: a capture read while agy is still writing can end
// mid-line.
func permissionModeOf(capture []byte) (mode string, sawInit bool) {
	for _, line := range strings.Split(string(capture), "\n") {
		var ev struct {
			Event string `json:"event"`
			Init  *struct {
				PermissionMode string `json:"permission_mode"`
			} `json:"init"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ev); err != nil {
			continue
		}
		if ev.Event == "init" && ev.Init != nil {
			return ev.Init.PermissionMode, true
		}
	}
	return "", false
}

// The control above only ever runs on a live agy, so nothing else would
// catch a typo in the one field it reads -- and a broken reader would make
// the live run fatal for a reason that has nothing to do with permission
// modes. This covers the reader itself, against both spellings agy has
// been seen using.
func TestPermissionModeReadsAgysInitEvent(t *testing.T) {
	t.Parallel()
	const withOverride = `{"event":"init","init":{"cwd":"/w","tools":["run_command"],"permission_mode":"always-proceed"}}`
	const withoutOverride = `{"event":"init","init":{"cwd":"/w","tools":["run_command"],"permission_mode":"request-review"}}`
	const resultLine = `{"event":"result","result":{"status":"SUCCESS","response":"done"}}`

	mode, sawInit := permissionModeOf([]byte(withOverride + "\n" + resultLine + "\n"))
	if !sawInit || mode != overridePermissionMode {
		t.Errorf("permissionModeOf = %q, %v; want %q, true -- this is the shape the live control fails on",
			mode, sawInit, overridePermissionMode)
	}
	mode, sawInit = permissionModeOf([]byte(withoutOverride + "\n"))
	if !sawInit || mode != "request-review" {
		t.Errorf("permissionModeOf = %q, %v; want \"request-review\", true", mode, sawInit)
	}

	// An init event that names no mode is not the same as no init event:
	// the first says this agy stopped reporting one (and the live control
	// then passes on an empty mode, which is right -- it only fails on the
	// override), the second says the run never started.
	if mode, sawInit = permissionModeOf([]byte(`{"event":"init","init":{"tools":[]}}` + "\n")); mode != "" || !sawInit {
		t.Errorf("permissionModeOf of an init event with no mode = %q, %v; want \"\", true", mode, sawInit)
	}
	if _, sawInit = permissionModeOf([]byte(`{"event":"ini`)); sawInit {
		t.Error("permissionModeOf read an init event out of a truncated line")
	}
	if _, sawInit = permissionModeOf(nil); sawInit {
		t.Error("permissionModeOf read an init event out of an empty capture")
	}
}
