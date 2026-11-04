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
	t.Setenv(localAPITokenFileEnv, "")
	opts := Options{DataDir: "/var/lib/grain/data"}.withDefaults()
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
	want := `curl -s -X PUT http://127.0.0.1:4242/api/settings -H "Authorization: Bearer $(sudo cat /var/lib/grain/data/local-api-token)" ` +
		`-H 'Content-Type: application/json' -d '{"sandboxMemoryMb":6144,"sandboxMemoryReserveMb":1792}'`
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

// The 10-28 walkthrough's 2 vCPU / 7.8 GiB e2-standard-2: half the
// memory reserved and both cores eaten by the controller's reserve
// (docs/test-runs/2026-10-28-install-upgrade-walkthrough.md). setup.sh
// prints SettingsCommand, so it has to name `grain settings` flags that
// exist and apply all three changes.
func TestCheckSandboxBudgetSettingsCommandForTheTwoCoreHost(t *testing.T) {
	v, err := CheckSandboxBudget([]byte(`{"sandboxCommitment":{"cpusPerSandbox":2,"memoryMbPerSandbox":8192,"hostCpus":2,"hostMemoryMb":7987,
		"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":3993,"cpus":0,"cpusBounded":true,"memoryMb":3994,"memoryBounded":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Checked || v.Fits {
		t.Fatalf("verdict = %+v, want checked and not fitting", v)
	}
	want := "grain settings -sandbox-cpu-overcommit 0 -sandbox-memory-mb 6144 -sandbox-memory-reserve-mb 1792"
	if got := v.SettingsCommand(); got != want {
		t.Errorf("SettingsCommand() = %q\nwant              %q", got, want)
	}
}

func TestCheckSandboxBudgetSettingsCommandEmptyWhenNothingToSet(t *testing.T) {
	for name, settings := range map[string]string{
		"fits":      roomySettingsJSON,
		"too small": `{"sandboxCommitment":{"cpusPerSandbox":1,"memoryMbPerSandbox":2048,"hostCpus":4,"hostMemoryMb":1200,"budget":{"cpuOvercommit":2,"memoryOvercommit":1,"reserveMemoryMb":8192,"cpus":4,"cpusBounded":true,"memoryMb":0,"memoryBounded":true}}}`,
		"unchecked": `{}`,
	} {
		v, err := CheckSandboxBudget([]byte(settings))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cmd := v.SettingsCommand(); cmd != "" {
			t.Errorf("%s: SettingsCommand() = %q, want none", name, cmd)
		}
	}
}

// A beefy 16 vCPU / 64 GiB host with max_workers=10 whose grain and docker
// directories sit on a small 30 GiB boot partition: 30 GiB less the 5 GiB
// floor is 25 GiB usable, 50 GiB at 2x overcommit, and holding 1 sandbox
// (30 GiB) back for repair leaves 20 GiB for ordinary work -- so only the
// sole first run is admitted and a second can never start alongside it.
func TestCheckSandboxBudgetFlagsSmallDiskRestrictingConcurrencyToOneTask(t *testing.T) {
	const beefyHostSmallDiskJSON = `{
		"maxWorkers": 10,
		"sandboxDiskGbDefault": 30,
		"sandboxDiskOvercommit": 2,
		"sandboxDiskFloors": [
			{"holds": ["store", "docker"], "path": "/var/lib/grain/data", "usedMB": 4096, "totalMB": 30720, "floorMB": 5120}
		],
		"sandboxCommitment": {
			"workers": 10, "mergers": 2, "ciJobs": 5, "runs": 17,
			"cpusPerSandbox": 2, "memoryMbPerSandbox": 8192, "diskGbPerSandbox": 30,
			"hostCpus": 16, "hostMemoryMb": 65536,
			"budget": {
				"cpuOvercommit": 2, "memoryOvercommit": 1, "diskOvercommit": 2,
				"reserveMemoryMb": 8192, "cpus": 28, "cpusBounded": true, "memoryMb": 57344, "memoryBounded": true
			}
		}
	}`
	v, err := CheckSandboxBudget([]byte(beefyHostSmallDiskJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Checked || !v.Fits || !v.UnderResourced {
		t.Fatalf("verdict = %+v, want Checked, Fits, and UnderResourced", v)
	}
	if v.ConfiguredWorkers != 10 || v.MaxConcurrent != 1 {
		t.Errorf("ConfiguredWorkers = %d, MaxConcurrent = %d, want 10 and 1", v.ConfiguredWorkers, v.MaxConcurrent)
	}
	for _, want := range []string{
		"one 8192 MiB sandbox fits in 57344 MiB of sandbox memory",
		"configured for 10 workers (max_workers) while its sandbox budget restricts it to 1 task at a time",
		"/var/lib/grain/data (30.0 GiB holding store, docker, 5.0 GiB floor, 50 GiB sandbox disk budget at 2.00x)",
		"Move the grain data directory",
		"Docker's data-root",
	} {
		if !strings.Contains(v.Detail, want) {
			t.Errorf("Detail = %q, want it to mention %q", v.Detail, want)
		}
	}
	if got, want := v.SettingsCommand(), "grain settings -max-workers 1"; got != want {
		t.Errorf("SettingsCommand() = %q, want %q", got, want)
	}

	step := runCheckSandboxBudget(t, beefyHostSmallDiskJSON)
	if step.Status != StepDone {
		t.Errorf("Step.Status = %s, want %s (the host still admits a run)", step.Status, StepDone)
	}
	if !strings.Contains(step.Detail, "restricts it to 1 task at a time") {
		t.Errorf("Step.Detail = %q, want concurrency warning in step detail", step.Detail)
	}
}

func TestCheckSandboxBudgetPassesWhenDiskAndHostMatchConcurrency(t *testing.T) {
	const beefyHostLargeDiskJSON = `{
		"maxWorkers": 10,
		"sandboxDiskGbDefault": 30,
		"sandboxDiskOvercommit": 2,
		"sandboxDiskFloors": [
			{"holds": ["store", "docker"], "path": "/var/lib/grain/data", "usedMB": 10240, "totalMB": 204800, "floorMB": 20480}
		],
		"sandboxCommitment": {
			"workers": 10, "mergers": 2, "ciJobs": 5, "runs": 17,
			"cpusPerSandbox": 2, "memoryMbPerSandbox": 8192, "diskGbPerSandbox": 30,
			"hostCpus": 16, "hostMemoryMb": 65536,
			"budget": {
				"cpuOvercommit": 2, "memoryOvercommit": 1, "diskOvercommit": 2,
				"reserveMemoryMb": 8192, "cpus": 28, "cpusBounded": true, "memoryMb": 57344, "memoryBounded": true
			}
		}
	}`
	v, err := CheckSandboxBudget([]byte(beefyHostLargeDiskJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Checked || !v.Fits || v.UnderResourced {
		t.Fatalf("verdict = %+v, want Checked, Fits, and not UnderResourced", v)
	}
	if cmd := v.SettingsCommand(); cmd != "" {
		t.Errorf("SettingsCommand() = %q, want empty", cmd)
	}
}
