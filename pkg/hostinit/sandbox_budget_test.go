package hostinit

import (
	"context"
	"strings"
	"testing"
)

// roomySettingsJSON is GET /api/settings on a host whose budget holds
// its sandbox shape several times over: 16 vCPU / 64 GiB, the default
// 8 GiB reserve, 2 vCPU / 8 GiB sandboxes.
const roomySettingsJSON = `{"sandboxCommitment":{"cpusPerSandbox":2,"memoryMbPerSandbox":8192,"hostCpus":16,"hostMemoryMb":65536,
	"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":8192,"cpus":28,"cpusBounded":true,"memoryMb":57344,"memoryBounded":true}}}`

// smallHostSettingsJSON is what the 10-04 walkthrough's 4 vCPU / 7.8 GiB
// Debian host answered (docs/test-runs/2026-10-04-grain-init-platforms.md):
// an 8 GiB reserve on a 7.8 GiB host leaves no sandbox memory at all.
const smallHostSettingsJSON = `{"sandboxCommitment":{"cpusPerSandbox":2,"memoryMbPerSandbox":8192,"hostCpus":4,"hostMemoryMb":7987,
	"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":8192,"reserveIsDefault":true,"cpus":4,"cpusBounded":true,"memoryMb":0,"memoryBounded":true}}}`

func runCheckSandboxBudget(t *testing.T, settings string) Step {
	t.Helper()
	opts := Options{}.withDefaults()
	sys := newFakeSystem()
	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/settings", 200, []byte(settings), nil)
	var r Result
	checkSandboxBudget(context.Background(), sys, opts, &r, true)
	if len(r.Steps) != 1 {
		t.Fatalf("Steps = %+v, want exactly one", r.Steps)
	}
	for _, c := range sys.httpRequestCalls {
		if c.method != "GET" {
			t.Errorf("checkSandboxBudget made a %s %s -- it must only read the settings, never change them", c.method, c.url)
		}
	}
	return r.Steps[0]
}

// The regression from task 1940: init said the deployment was up on a
// host whose budget could never admit a run. It has to be a manual step
// (exit 10) naming the PUT that fits instead.
func TestCheckSandboxBudgetReportsABudgetThatAdmitsNothing(t *testing.T) {
	s := runCheckSandboxBudget(t, smallHostSettingsJSON)
	if s.Status != StepManual {
		t.Fatalf("Status = %s, want %s: %+v", s.Status, StepManual, s)
	}
	for _, want := range []string{"no run can ever be admitted", "7987 MiB host less 8192 MiB reserved", "Settings → Sandbox"} {
		if !strings.Contains(s.Detail, want) {
			t.Errorf("Detail = %q, want it to mention %q", s.Detail, want)
		}
	}
	// A quarter of 7987 MiB, rounded down to 256, is the reserve; the
	// largest whole GiB of what that leaves is the shape.
	want := `curl -s -X PUT http://127.0.0.1:4242/api/settings -H 'Content-Type: application/json' -d '{"sandboxMemoryMb":6144,"sandboxMemoryReserveMb":1792}'`
	if s.Command != want {
		t.Errorf("Command = %q\nwant      %q", s.Command, want)
	}
}

// Lowering the reserve alone is suggested when that is enough, so the
// operator's chosen shape is not changed for nothing.
func TestCheckSandboxBudgetKeepsAShapeTheLowerReserveFits(t *testing.T) {
	s := runCheckSandboxBudget(t, `{"sandboxCommitment":{"cpusPerSandbox":2,"memoryMbPerSandbox":4096,"hostCpus":4,"hostMemoryMb":7987,
		"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":8192,"cpus":4,"cpusBounded":true,"memoryMb":0,"memoryBounded":true}}}`)
	if s.Status != StepManual {
		t.Fatalf("Status = %s, want %s", s.Status, StepManual)
	}
	if !strings.HasSuffix(s.Command, `-d '{"sandboxMemoryReserveMb":1792}'`) {
		t.Errorf("Command = %q, want only the reserve lowered", s.Command)
	}
}

func TestCheckSandboxBudgetReportsACPUBudgetBelowOneSandbox(t *testing.T) {
	// A 2-core host: the controller's 2-core reserve leaves nothing.
	s := runCheckSandboxBudget(t, `{"sandboxCommitment":{"cpusPerSandbox":2,"memoryMbPerSandbox":2048,"hostCpus":2,"hostMemoryMb":65536,
		"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":8192,"cpus":0,"cpusBounded":true,"memoryMb":57344,"memoryBounded":true}}}`)
	if s.Status != StepManual {
		t.Fatalf("Status = %s, want %s", s.Status, StepManual)
	}
	if !strings.HasSuffix(s.Command, `-d '{"sandboxCpuOvercommit":0}'`) {
		t.Errorf("Command = %q, want the cpu bound lifted, the one setting that can fix this", s.Command)
	}
}

func TestCheckSandboxBudgetHostTooSmallForAnySandbox(t *testing.T) {
	s := runCheckSandboxBudget(t, `{"sandboxCommitment":{"cpusPerSandbox":1,"memoryMbPerSandbox":2048,"hostCpus":4,"hostMemoryMb":1200,
		"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":8192,"cpus":4,"cpusBounded":true,"memoryMb":0,"memoryBounded":true}}}`)
	if s.Status != StepManual {
		t.Fatalf("Status = %s, want %s", s.Status, StepManual)
	}
	if s.Command != "" || !strings.Contains(s.Detail, "too little memory") {
		t.Errorf("Step = %+v, want no command to run and the host named too small", s)
	}
}

func TestCheckSandboxBudgetPassesARoomyHost(t *testing.T) {
	if s := runCheckSandboxBudget(t, roomySettingsJSON); s.Status != StepDone {
		t.Errorf("Step = %+v, want %s", s, StepDone)
	}
}

func TestCheckSandboxBudgetSkipsWhereNothingIsBounded(t *testing.T) {
	for name, settings := range map[string]string{
		"no budget":       `{"sandboxCommitment":{"cpusPerSandbox":2,"memoryMbPerSandbox":8192}}`,
		"no commitment":   `{}`,
		"host sandboxing": `{"sandboxCommitment":{"uncapped":true,"budget":{"memoryMb":0,"memoryBounded":true}}}`,
	} {
		if s := runCheckSandboxBudget(t, settings); s.Status != StepSkipped {
			t.Errorf("%s: Step = %+v, want %s", name, s, StepSkipped)
		}
	}
}

func TestCheckSandboxBudgetDryRunAndDaemonDown(t *testing.T) {
	opts := Options{DryRun: true}.withDefaults()
	var r Result
	checkSandboxBudget(context.Background(), newFakeSystem(), opts, &r, true)
	if r.Steps[0].Status != StepWould {
		t.Errorf("dry run: %+v, want %s", r.Steps[0], StepWould)
	}
	r = Result{}
	checkSandboxBudget(context.Background(), newFakeSystem(), Options{}.withDefaults(), &r, false)
	if r.Steps[0].Status != StepSkipped {
		t.Errorf("daemon down: %+v, want %s", r.Steps[0], StepSkipped)
	}
}
