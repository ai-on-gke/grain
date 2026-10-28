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

package deploy

// scripts/run-go-tests.sh, run rather than read.
//
// The script sits between `go test` and the step log in
// .grain/workflows/tests.yml (`go`, `postgres`, `run-host`,
// `package-cache`), solving two opposite failure modes at once:
//
//   - redirecting `go test > /tmp/gotest.log 2>&1` produced no live
//     output at all for 15-35 minutes while the suite ran, and neither
//     did single-package steps invoked without `-v` or `-json`;
//   - dumping raw `go test` output directly to stdout let noisy passing
//     tests (such as cmd/grain's CLI/daemon output) exceed the 256 KiB
//     ci.maxStepOutput ceiling (pkg/grit/steps/live.go), eliding the middle of
//     the step log where `--- FAIL:` lines lived.
//
// These tests drive the script against stub `go` processes so that
// timing, noisy passing output, failures, panics, data races and exit
// codes can be exercised deterministically in milliseconds.

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeGoStub(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	stub := "#!/usr/bin/env bash\nset -euo pipefail\n" + body + "\n"
	path := filepath.Join(dir, "go")
	if err := os.WriteFile(path, []byte(stub), 0o755); err != nil {
		t.Fatalf("writing go stub: %v", err)
	}
	return dir
}

func runGoTestsScript(t *testing.T, stubDir string, env []string, args ...string) (int, string, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "gotest.log")
	for attempt := 1; ; attempt++ {
		cmd := exec.Command(filepath.Join(repoRoot(t), "scripts", "run-go-tests.sh"), args...)
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(),
			"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GRAIN_GOTEST_LOG="+logPath,
		)
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		if strings.Contains(string(out), "Text file busy") && attempt < 5 {
			time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
			continue
		}
		status := 0
		if exit, ok := err.(*exec.ExitError); ok {
			status = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("running run-go-tests.sh: %v\n%s", err, out)
		}
		rawLog, _ := os.ReadFile(logPath)
		return status, string(out), string(rawLog)
	}
}

func TestRunGoTestsScriptIsExecutableAndValidBash(t *testing.T) {
	t.Parallel()
	executable(t, "scripts", "run-go-tests.sh")
	script := filepath.Join(repoRoot(t), "scripts", "run-go-tests.sh")
	if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n scripts/run-go-tests.sh: %v\n%s", err, out)
	}
}

// Live progress lines and heartbeats arrive incrementally *while* `go
// test` is still running, rather than in one batch when it exits.
func TestRunGoTestsStreamsLiveProgressAndHeartbeatsIncrementally(t *testing.T) {
	t.Parallel()
	stubDir := writeGoStub(t, `
cat <<'JSON'
{"Action":"start","Package":"github.com/ai-on-gke/grain/pkg/fast"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/pkg/fast","Test":"TestFast"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/fast","Test":"TestFast","Output":"=== RUN   TestFast\n"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/fast","Test":"TestFast","Output":"--- PASS: TestFast (0.01s)\n"}
{"Action":"pass","Package":"github.com/ai-on-gke/grain/pkg/fast","Test":"TestFast"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/fast","Output":"ok  \tgithub.com/ai-on-gke/grain/pkg/fast\t0.012s\n"}
{"Action":"pass","Package":"github.com/ai-on-gke/grain/pkg/fast"}
{"Action":"start","Package":"github.com/ai-on-gke/grain/pkg/ui"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/pkg/ui","Test":"TestSlowDashboard"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/ui","Test":"TestSlowDashboard","Output":"=== RUN   TestSlowDashboard\n"}
JSON
sleep 0.45
cat <<'JSON'
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/ui","Test":"TestSlowDashboard","Output":"--- PASS: TestSlowDashboard (0.45s)\n"}
{"Action":"pass","Package":"github.com/ai-on-gke/grain/pkg/ui","Test":"TestSlowDashboard"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/ui","Output":"ok  \tgithub.com/ai-on-gke/grain/pkg/ui\t0.455s\n"}
{"Action":"pass","Package":"github.com/ai-on-gke/grain/pkg/ui"}
JSON
`)

	logPath := filepath.Join(t.TempDir(), "gotest.log")
	var cmd *exec.Cmd
	for attempt := 1; ; attempt++ {
		cmd = exec.Command(filepath.Join(repoRoot(t), "scripts", "run-go-tests.sh"), "go", "test", "./...")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(),
			"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GRAIN_GOTEST_LOG="+logPath,
			"GRAIN_GOTEST_HEARTBEAT_SECONDS=0.15",
		)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("StdoutPipe: %v", err)
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			if strings.Contains(err.Error(), "text file busy") && attempt < 5 {
				time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
				continue
			}
			t.Fatalf("Start: %v", err)
		}

		type timedLine struct {
			at   time.Time
			text string
		}
		var lines []timedLine
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines = append(lines, timedLine{at: time.Now(), text: scanner.Text()})
		}
		err = cmd.Wait()
		if err != nil {
			t.Fatalf("run-go-tests.sh exited with error: %v", err)
		}

		var fastAt, heartbeatAt, slowAt time.Time
		for _, l := range lines {
			if strings.HasPrefix(l.text, "ok  \tgithub.com/ai-on-gke/grain/pkg/fast") && fastAt.IsZero() {
				fastAt = l.at
			}
			if strings.HasPrefix(l.text, "[go test +") && strings.Contains(l.text, "pkg/ui:TestSlowDashboard") && heartbeatAt.IsZero() {
				heartbeatAt = l.at
			}
			if strings.HasPrefix(l.text, "ok  \tgithub.com/ai-on-gke/grain/pkg/ui") && slowAt.IsZero() {
				slowAt = l.at
			}
		}
		if fastAt.IsZero() {
			t.Fatalf("did not see live completion line for pkg/fast; lines:\n%+v", lines)
		}
		if heartbeatAt.IsZero() {
			t.Fatalf("did not see heartbeat naming pkg/ui:TestSlowDashboard while in flight; lines:\n%+v", lines)
		}
		if slowAt.IsZero() {
			t.Fatalf("did not see completion line for pkg/ui; lines:\n%+v", lines)
		}
		if !fastAt.Before(slowAt) || slowAt.Sub(fastAt) < 200*time.Millisecond {
			t.Errorf("pkg/fast line (%v) and pkg/ui line (%v) arrived together (%v apart) instead of streaming incrementally",
				fastAt, slowAt, slowAt.Sub(fastAt))
		}
		if !heartbeatAt.Before(slowAt) {
			t.Errorf("heartbeat (%v) did not arrive before pkg/ui finished (%v)", heartbeatAt, slowAt)
		}
		break
	}
}

// A passing run whose tests write >300 KiB of noisy stdout stays compact
// on stdout (a few KiB, well below the 256 KiB ci.maxStepOutput ceiling)
// while still writing every byte of raw output to GRAIN_GOTEST_LOG.
func TestRunGoTestsStaysCompactOnNoisyPassingRun(t *testing.T) {
	t.Parallel()
	// 4000 noisy output events of ~100 bytes each (>400 KiB of raw test output).
	stubDir := writeGoStub(t, `
python3 - <<'PY'
import json, sys
print(json.dumps({"Action":"start","Package":"github.com/ai-on-gke/grain/cmd/grain"}))
print(json.dumps({"Action":"run","Package":"github.com/ai-on-gke/grain/cmd/grain","Test":"TestNoisyCLI"}))
print(json.dumps({"Action":"output","Package":"github.com/ai-on-gke/grain/cmd/grain","Test":"TestNoisyCLI","Output":"=== RUN   TestNoisyCLI\n"}))
noisy = "noisy cli stdout dump line with plenty of characters to exceed 256 KiB when repeated 4000 times\n"
for i in range(4000):
    print(json.dumps({"Action":"output","Package":"github.com/ai-on-gke/grain/cmd/grain","Test":"TestNoisyCLI","Output":f"{i}: {noisy}"}))
print(json.dumps({"Action":"output","Package":"github.com/ai-on-gke/grain/cmd/grain","Test":"TestNoisyCLI","Output":"--- PASS: TestNoisyCLI (0.10s)\n"}))
print(json.dumps({"Action":"pass","Package":"github.com/ai-on-gke/grain/cmd/grain","Test":"TestNoisyCLI"}))
print(json.dumps({"Action":"output","Package":"github.com/ai-on-gke/grain/cmd/grain","Output":"ok  \tgithub.com/ai-on-gke/grain/cmd/grain\t0.105s\n"}))
print(json.dumps({"Action":"pass","Package":"github.com/ai-on-gke/grain/cmd/grain"}))
PY
`)

	status, out, rawLog := runGoTestsScript(t, stubDir, nil, "go", "test", "-race", "./...")
	if status != 0 {
		t.Fatalf("expected exit 0 on passing run, got %d:\n%s", status, out)
	}
	if len(rawLog) < 300*1024 {
		t.Errorf("expected GRAIN_GOTEST_LOG to capture >300 KiB of raw output, got %d bytes", len(rawLog))
	}
	if len(out) > 4*1024 {
		t.Errorf("expected passing stdout to stay compact (<4 KiB), got %d bytes:\n%s", len(out), out)
	}
	if strings.Contains(out, "noisy cli stdout dump line") {
		t.Errorf("passing stdout leaked noisy test output:\n%s", out)
	}
	if strings.Contains(out, "=== last 200 lines of the raw run ===") {
		t.Errorf("passing stdout included the raw 200-line tail, which belongs only on failure:\n%s", out)
	}
	for _, want := range []string{
		"ok  \tgithub.com/ai-on-gke/grain/cmd/grain\t0.105s",
		"=== failing tests ===",
		"=== panics ===",
		"=== data races ===",
		"=== per-package results ===",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

// Failures, panics, and data races are surfaced both in the live stream
// and in the structured summary sections, even when surrounded by >300 KiB
// of noisy passing test output, and the non-zero exit status is preserved.
func TestRunGoTestsSurfacesFailuresPanicsRacesAndPreservesExitStatus(t *testing.T) {
	t.Parallel()
	stubDir := writeGoStub(t, `
python3 - <<'PY'
import json, sys
pkg = "github.com/ai-on-gke/grain/cmd/grain"
print(json.dumps({"Action":"start","Package":pkg}))
print(json.dumps({"Action":"run","Package":pkg,"Test":"TestChattyPass"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestChattyPass","Output":"=== RUN   TestChattyPass\n"}))
for i in range(3500):
    print(json.dumps({"Action":"output","Package":pkg,"Test":"TestChattyPass","Output":f"chatty line {i}: " + ("x" * 80) + "\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestChattyPass","Output":"--- PASS: TestChattyPass (0.05s)\n"}))
print(json.dumps({"Action":"pass","Package":pkg,"Test":"TestChattyPass"}))

print(json.dumps({"Action":"run","Package":pkg,"Test":"TestBrokenFeature"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"=== RUN   TestBrokenFeature\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"    feature_test.go:42: expected status 200, got 500\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"==================\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"WARNING: DATA RACE\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"Write at 0x00c000123456 by goroutine 17:\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"  github.com/ai-on-gke/grain/cmd/grain.TestBrokenFeature.func1()\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"panic: unexpected nil pointer in handler\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"goroutine 17 [running]:\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestBrokenFeature","Output":"--- FAIL: TestBrokenFeature (0.03s)\n"}))
print(json.dumps({"Action":"fail","Package":pkg,"Test":"TestBrokenFeature"}))
print(json.dumps({"Action":"output","Package":pkg,"Output":"FAIL\tgithub.com/ai-on-gke/grain/cmd/grain\t0.080s\n"}))
print(json.dumps({"Action":"fail","Package":pkg}))
sys.exit(1)
PY
`)

	status, out, _ := runGoTestsScript(t, stubDir, nil, "go", "test", "-race", "./...")
	if status != 1 {
		t.Fatalf("expected exit status 1, got %d:\n%s", status, out)
	}
	if len(out) > 32*1024 {
		t.Errorf("expected failure stdout to stay well under 256 KiB (<32 KiB), got %d bytes", len(out))
	}

	failingSection := between(t, out, "=== failing tests ===\n", "=== panics ===")
	for _, want := range []string{
		"--- FAIL: TestBrokenFeature (0.03s) [cmd/grain]",
		"feature_test.go:42: expected status 200, got 500",
	} {
		if !strings.Contains(failingSection, want) {
			t.Errorf("=== failing tests === section missing %q:\n%s", want, failingSection)
		}
	}

	panicsSection := between(t, out, "=== panics ===\n", "=== data races ===")
	for _, want := range []string{
		"panic: unexpected nil pointer in handler",
		"goroutine 17 [running]:",
	} {
		if !strings.Contains(panicsSection, want) {
			t.Errorf("=== panics === section missing %q:\n%s", want, panicsSection)
		}
	}

	racesSection := between(t, out, "=== data races ===\n", "=== per-package results ===")
	for _, want := range []string{
		"WARNING: DATA RACE",
		"Write at 0x00c000123456 by goroutine 17:",
	} {
		if !strings.Contains(racesSection, want) {
			t.Errorf("=== data races === section missing %q:\n%s", want, racesSection)
		}
	}

	pkgSection := between(t, out, "=== per-package results ===\n", "=== last 200 lines of the raw run ===")
	if !strings.Contains(pkgSection, "FAIL\tgithub.com/ai-on-gke/grain/cmd/grain\t0.080s") {
		t.Errorf("=== per-package results === section missing package failure line:\n%s", pkgSection)
	}
}

// Pre-compiled test binaries (such as /var/tmp/grain-intercept.test in the
// package-cache job) emit plain `go test -v` text rather than `go test
// -json` events; run-go-tests.sh accepts the binary directly and still
// writes the raw log for post-run `--- PASS:` assertions, while surfacing
// `t.Skipf(...)` diagnostic lines if a test skips.
func TestRunGoTestsSupportsCompiledTestBinaryPlainOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake.test")
	script := "#!/usr/bin/env bash\n" +
		"echo '=== RUN   TestStandInArchive'\n" +
		"echo '    intercept_test.go:19: serving stand-in archive'\n" +
		"echo '--- PASS: TestStandInArchive (0.01s)'\n" +
		"echo 'PASS'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake test binary: %v", err)
	}

	status, out, rawLog := runGoTestsScript(t, dir, nil, bin, "-test.v", "-test.run", "TestStandInArchive")
	if status != 0 {
		t.Fatalf("expected exit 0, got %d:\n%s", status, out)
	}
	if !strings.Contains(rawLog, "--- PASS: TestStandInArchive (0.01s)") {
		t.Errorf("expected GRAIN_GOTEST_LOG to contain `--- PASS: TestStandInArchive`, got:\n%s", rawLog)
	}
}

// When a step runs with -v or -test.v (such as package-cache's two steps)
// and a test calls t.Skipf(...), the skip diagnostic line and `--- SKIP:`
// verdict must be surfaced in stdout even though `go test` exits 0, so a
// post-run `--- PASS:` check that rejects skips still shows why the test
// skipped.
func TestRunGoTestsSurfacesSkipDiagnosticsInVerboseAndBinaryModes(t *testing.T) {
	t.Parallel()
	stubDir := writeGoStub(t, `
cat <<'JSON'
{"Action":"start","Package":"github.com/ai-on-gke/grain/pkg/orchestrator"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Test":"TestPackageCachePullsThroughARealDockerd"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Test":"TestPackageCachePullsThroughARealDockerd","Output":"=== RUN   TestPackageCachePullsThroughARealDockerd\n"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Test":"TestPackageCachePullsThroughARealDockerd","Output":"    packagecache_real_test.go:88: docker daemon is not reachable on this host\n"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Test":"TestPackageCachePullsThroughARealDockerd","Output":"--- SKIP: TestPackageCachePullsThroughARealDockerd (0.01s)\n"}
{"Action":"skip","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Test":"TestPackageCachePullsThroughARealDockerd"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Output":"ok  \tgithub.com/ai-on-gke/grain/pkg/orchestrator\t0.015s\n"}
{"Action":"pass","Package":"github.com/ai-on-gke/grain/pkg/orchestrator"}
JSON
`)

	status, out, rawLog := runGoTestsScript(t, stubDir, nil, "go", "test", "./pkg/orchestrator/", "-v")
	if status != 0 {
		t.Fatalf("expected exit 0, got %d:\n%s", status, out)
	}
	for _, want := range []string{
		"packagecache_real_test.go:88: docker daemon is not reachable on this host",
		"--- SKIP: TestPackageCachePullsThroughARealDockerd (0.01s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("verbose stdout missing skip diagnostic %q:\n%s", want, out)
		}
		if !strings.Contains(rawLog, want) {
			t.Errorf("GRAIN_GOTEST_LOG missing skip diagnostic %q:\n%s", want, rawLog)
		}
	}
}

// A noisy failing test that writes >100 lines of log output before failing
// still preserves both early file.go:line: error lines and the final
// t.Fatalf diagnostic at the end of the test, and concurrent failing tests
// in the same package do not bleed output lines into each other's blocks.
func TestRunGoTestsPreservesTailDiagnosticsAndIsolatesConcurrentFailingTests(t *testing.T) {
	t.Parallel()
	stubDir := writeGoStub(t, `
python3 - <<'PY'
import json, sys

pkg = "github.com/ai-on-gke/grain/pkg/hostinit"
print(json.dumps({"Action":"start","Package":pkg}))
print(json.dumps({"Action":"run","Package":pkg,"Test":"TestNoisyFailure"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestNoisyFailure","Output":"=== RUN   TestNoisyFailure\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestNoisyFailure","Output":"    hostinit_test.go:15: initial check failed\n"}))
for i in range(120):
    print(json.dumps({"Action":"output","Package":pkg,"Test":"TestNoisyFailure","Output":f"2026/10/21 00:00:00 noisy daemon log line {i}\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestNoisyFailure","Output":"    hostinit_test.go:99: final fatal failure after 120 log lines\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestNoisyFailure","Output":"--- FAIL: TestNoisyFailure (0.05s)\n"}))
print(json.dumps({"Action":"fail","Package":pkg,"Test":"TestNoisyFailure"}))

print(json.dumps({"Action":"run","Package":pkg,"Test":"TestOtherParallelFailure"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestOtherParallelFailure","Output":"=== RUN   TestOtherParallelFailure\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestOtherParallelFailure","Output":"    other_test.go:202: distinct failure in second test\n"}))
print(json.dumps({"Action":"output","Package":pkg,"Test":"TestOtherParallelFailure","Output":"--- FAIL: TestOtherParallelFailure (0.04s)\n"}))
print(json.dumps({"Action":"fail","Package":pkg,"Test":"TestOtherParallelFailure"}))
print(json.dumps({"Action":"output","Package":pkg,"Output":"FAIL\tgithub.com/ai-on-gke/grain/pkg/hostinit\t0.090s\n"}))
print(json.dumps({"Action":"fail","Package":pkg}))
sys.exit(1)
PY
`)

	status, out, _ := runGoTestsScript(t, stubDir, nil, "go", "test", "./...")
	if status != 1 {
		t.Fatalf("expected exit 1, got %d:\n%s", status, out)
	}
	failingSection := between(t, out, "=== failing tests ===\n", "=== panics ===")
	for _, want := range []string{
		"--- FAIL: TestNoisyFailure (0.05s) [pkg/hostinit]",
		"hostinit_test.go:15: initial check failed",
		"hostinit_test.go:99: final fatal failure after 120 log lines",
		"--- FAIL: TestOtherParallelFailure (0.04s) [pkg/hostinit]",
		"other_test.go:202: distinct failure in second test",
	} {
		if !strings.Contains(failingSection, want) {
			t.Errorf("=== failing tests === section missing %q:\n%s", want, failingSection)
		}
	}
	firstBlock := between(t, failingSection, "--- FAIL: TestNoisyFailure (0.05s) [pkg/hostinit]\n", "--- FAIL: TestOtherParallelFailure")
	if strings.Contains(firstBlock, "other_test.go:202") {
		t.Errorf("TestNoisyFailure block bled into TestOtherParallelFailure's output:\n%s", firstBlock)
	}
}

// A test binary that exits non-zero or is killed mid-test before
// printing `--- FAIL:` still has the in-flight test and its buffered
// output surfaced under `=== failing tests ===`.
func TestRunGoTestsSurfacesIncompleteInFlightTestOnAbruptFailure(t *testing.T) {
	t.Parallel()
	stubDir := writeGoStub(t, `
cat <<'JSON'
{"Action":"start","Package":"github.com/ai-on-gke/grain/pkg/orchestrator"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Test":"TestAbruptExit"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Test":"TestAbruptExit","Output":"=== RUN   TestAbruptExit\n"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/orchestrator","Test":"TestAbruptExit","Output":"    reconcile_test.go:99: waiting for condition before crash\n"}
JSON
exit 2
`)

	status, out, _ := runGoTestsScript(t, stubDir, nil, "go", "test", "./...")
	if status != 2 {
		t.Fatalf("expected exit status 2, got %d:\n%s", status, out)
	}
	failingSection := between(t, out, "=== failing tests ===\n", "=== panics ===")
	for _, want := range []string{
		"--- FAIL: TestAbruptExit (incomplete) [pkg/orchestrator]",
		"reconcile_test.go:99: waiting for condition before crash",
	} {
		if !strings.Contains(failingSection, want) {
			t.Errorf("=== failing tests === section missing %q:\n%s", want, failingSection)
		}
	}
}

// Data race blocks stop at `--- FAIL:` verdicts so `=== data races ===` does
// not swallow subsequent test verdict lines, and repeated runs (`-count=2`)
// record every failure of the same test.
func TestRunGoTestsRaceBoundaryAndRepeatedTestFailures(t *testing.T) {
	t.Parallel()
	stubDir := writeGoStub(t, `
cat <<'JSON'
{"Action":"start","Package":"github.com/ai-on-gke/grain/pkg/ci"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRace"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRace","Output":"=== RUN   TestRace\n==================\nWARNING: DATA RACE\nWrite at 0x00c0000140b8 by goroutine 8:\n  github.com/ai-on-gke/grain/pkg/ci.TestRace.func1()\n      /work/pkg/ci/race_test.go:11 +0x34\n==================\n    testing.go:1712: race detected during execution of test\n--- FAIL: TestRace (0.01s)\n"}
{"Action":"fail","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRace"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRepeat"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRepeat","Output":"=== RUN   TestRepeat\n    repeat_test.go:10: failure one\n"}
{"Action":"fail","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRepeat"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRepeat"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRepeat","Output":"=== RUN   TestRepeat\n    repeat_test.go:10: failure two\n"}
{"Action":"fail","Package":"github.com/ai-on-gke/grain/pkg/ci","Test":"TestRepeat"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/ci","Output":"FAIL\tgithub.com/ai-on-gke/grain/pkg/ci\t0.020s\n"}
{"Action":"fail","Package":"github.com/ai-on-gke/grain/pkg/ci"}
JSON
exit 1
`)

	status, out, _ := runGoTestsScript(t, stubDir, nil, "go", "test", "-race", "-count=2", "./pkg/ci/...")
	if status != 1 {
		t.Fatalf("expected exit status 1, got %d:\n%s", status, out)
	}
	raceSection := between(t, out, "=== data races ===\n", "=== last ")
	if strings.Contains(raceSection, "--- FAIL: TestRace") {
		t.Errorf("expected === data races === to stop before `--- FAIL: TestRace`, got:\n%s", raceSection)
	}
	failingSection := between(t, out, "=== failing tests ===\n", "=== data races ===")
	for _, want := range []string{
		"repeat_test.go:10: failure one",
		"repeat_test.go:10: failure two",
	} {
		if !strings.Contains(failingSection, want) {
			t.Errorf("=== failing tests === missing repeated failure %q:\n%s", want, failingSection)
		}
	}
}

// Multiple invocations of run-go-tests.sh in the same CI job (such as the unit
// test step followed by ./tests/e2e) append to GRAIN_GOTEST_LOG rather than
// overwriting the earlier step's raw test log.
func TestRunGoTestsAppendsAcrossMultipleStepsInJob(t *testing.T) {
	t.Parallel()
	logPath := filepath.Join(t.TempDir(), "gotest.log")

	stubDir1 := writeGoStub(t, `
cat <<'JSON'
{"Action":"start","Package":"github.com/ai-on-gke/grain/pkg/unit"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/pkg/unit","Test":"TestUnitStep"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/unit","Test":"TestUnitStep","Output":"=== RUN   TestUnitStep\n--- PASS: TestUnitStep (0.01s)\n"}
{"Action":"pass","Package":"github.com/ai-on-gke/grain/pkg/unit","Test":"TestUnitStep"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/pkg/unit","Output":"ok  \tgithub.com/ai-on-gke/grain/pkg/unit\t0.010s\n"}
{"Action":"pass","Package":"github.com/ai-on-gke/grain/pkg/unit"}
JSON
`)
	stubDir2 := writeGoStub(t, `
cat <<'JSON'
{"Action":"start","Package":"github.com/ai-on-gke/grain/tests/e2e"}
{"Action":"run","Package":"github.com/ai-on-gke/grain/tests/e2e","Test":"TestE2EStep"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/tests/e2e","Test":"TestE2EStep","Output":"=== RUN   TestE2EStep\n    e2e_test.go:42: e2e assertion failed\n--- FAIL: TestE2EStep (0.02s)\n"}
{"Action":"fail","Package":"github.com/ai-on-gke/grain/tests/e2e","Test":"TestE2EStep"}
{"Action":"output","Package":"github.com/ai-on-gke/grain/tests/e2e","Output":"FAIL\tgithub.com/ai-on-gke/grain/tests/e2e\t0.020s\n"}
{"Action":"fail","Package":"github.com/ai-on-gke/grain/tests/e2e"}
JSON
exit 1
`)

	for _, tc := range []struct {
		stubDir    string
		wantStatus int
	}{
		{stubDir: stubDir1, wantStatus: 0},
		{stubDir: stubDir2, wantStatus: 1},
	} {
		cmd := exec.Command(filepath.Join(repoRoot(t), "scripts", "run-go-tests.sh"), "go", "test", "./...")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(),
			"PATH="+tc.stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GRAIN_GOTEST_LOG="+logPath,
		)
		out, err := cmd.CombinedOutput()
		status := 0
		if exit, ok := err.(*exec.ExitError); ok {
			status = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("running run-go-tests.sh: %v\n%s", err, out)
		}
		if status != tc.wantStatus {
			t.Fatalf("status = %d, want %d:\n%s", status, tc.wantStatus, out)
		}
	}

	rawBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	rawLog := string(rawBytes)
	for _, want := range []string{
		"=== PACKAGE github.com/ai-on-gke/grain/pkg/unit",
		"--- PASS: TestUnitStep (0.01s)",
		"=== PACKAGE github.com/ai-on-gke/grain/tests/e2e",
		"e2e_test.go:42: e2e assertion failed",
		"--- FAIL: TestE2EStep (0.02s)",
	} {
		if !strings.Contains(rawLog, want) {
			t.Errorf("combined GRAIN_GOTEST_LOG missing %q:\n%s", want, rawLog)
		}
	}
}
