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

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const repoRoot = "../.."

// The real manifest names every test package under tests/security and
// only tests that exist: a new suite there, or a renamed test, fails this
// in the pull request rather than going unrun at night.
func TestTheManifestCoversEveryTestPackageUnderTestsSecurity(t *testing.T) {
	m, err := loadManifest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Check(repoRoot, m) {
		t.Error(p)
	}
	for _, task := range []int{3981, 3982, 3983, 3984, 3965} {
		found := false
		for _, p := range m.Paths {
			found = found || p.Task == task
		}
		if !found {
			t.Errorf("no attack path comes from task %d's suite", task)
		}
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCheckRefusesAnUnlistedSuiteAndAMissingTest(t *testing.T) {
	root := writeTree(t, map[string]string{
		"tests/security/a_test.go":     "package security\nfunc TestA(t *testing.T) {}\n",
		"tests/security/new/b_test.go": "package new\nfunc TestB(t *testing.T) {}\n",
		ManifestPath: `packages: [./tests/security]
paths:
  - {name: a, task: 1, title: a, mode: in-process, package: ./tests/security, tests: [TestA, TestRenamed]}
  - {name: a, task: 1, title: a, mode: sideways, package: ./tests/security, tests: [TestA]}
`,
	})
	m, err := loadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(Check(root, m), "\n")
	for _, want := range []string{
		"./tests/security/new holds tests but",
		"names TestRenamed, which ./tests/security does not declare",
		"path a is listed twice",
		`mode "sideways"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Check did not report %q:\n%s", want, got)
		}
	}
}

// events is a fake go test -json stream for one package.
func events(pkg string, tests map[string][2]string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for name, r := range tests {
		action, out := r[0], r[1]
		enc.Encode(testEvent{Action: "run", Package: pkg, Test: name})
		enc.Encode(testEvent{Action: "run", Package: pkg, Test: name + "/sub"})
		enc.Encode(testEvent{Action: "fail", Package: pkg, Test: name + "/sub"})
		if out != "" {
			enc.Encode(testEvent{Action: "output", Package: pkg, Test: name, Output: "    x_test.go:12: " + out + "\n"})
		}
		if action != "" {
			enc.Encode(testEvent{Action: action, Package: pkg, Test: name})
		}
	}
	return b.String()
}

func TestRunFailsClosed(t *testing.T) {
	root := writeTree(t, map[string]string{
		"tests/security/a_test.go": "package security\n",
		ManifestPath: `packages: [./tests/security]
paths:
  - {name: passes, task: 1, title: t, mode: in-process, package: ./tests/security, tests: [TestPass]}
  - {name: gap, task: 1, title: t, mode: in-process, package: ./tests/security, tests: [TestPass, TestGap]}
  - {name: infra, task: 1, title: t, mode: in-process, package: ./tests/security, tests: [TestInfra]}
  - {name: fails, task: 1, title: t, mode: in-process, package: ./tests/security, tests: [TestPass, TestFail]}
  - {name: skips, task: 1, title: t, mode: in-process, package: ./tests/security, tests: [TestSkip]}
  - {name: missing, task: 1, title: t, mode: in-process, package: ./tests/security, tests: [TestRenamed]}
  - {name: unfinished, task: 1, title: t, mode: in-process, package: ./tests/security, tests: [TestHang]}
  - {name: unbuilt, task: 2, title: t, mode: in-process, package: ./pkg/broken, tests: [TestX]}
  - {name: narrowed, task: 2, title: t, mode: in-process, package: ./pkg/ok, tests: [TestOK]}
  - {name: vm, task: 3, title: t, mode: live-vm, package: ./pkg/orchestrator, tests: [TestVM]}
`,
	})
	m, err := loadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	fake := func(dir string, args []string, out io.Writer) (int, error) {
		pkg := args[len(args)-1]
		calls = append(calls, strings.Join(args, " "))
		switch pkg {
		case "./tests/security":
			io.WriteString(out, events("example/tests/security", map[string][2]string{
				"TestPass":   {"pass", ""},
				"TestGap":    {"skip", "known gap: the shared cache takes writes; see follow-up"},
				"TestInfra":  {"skip", "infrastructure: no /dev/kvm on this runner"},
				"TestFail":   {"fail", "a viewer read the access code"},
				"TestSkip":   {"skip", "initdb not installed"},
				"TestHang":   {"", "still going"},
				"TestOther":  {"pass", ""},
				"TestOther2": {"fail", "an older regression"},
			}))
			return 1, nil
		case "./pkg/broken":
			io.WriteString(out, "# example/pkg/broken\nbroken.go:3:1: syntax error\n")
			json.NewEncoder(out).Encode(testEvent{Action: "fail", Package: "example/pkg/broken"})
			return 1, nil
		default:
			io.WriteString(out, events("example/pkg/ok", map[string][2]string{"TestOK": {"pass", ""}}))
			return 0, nil
		}
	}
	verdicts := RunInProcess(root, m, time.Minute, io.Discard, io.Discard, fake)
	got := map[string]Verdict{}
	for _, v := range verdicts {
		got[v.Path.Name] = v
	}
	for name, want := range map[string]Status{
		"passes": Pass, "gap": KnownGap, "infra": Infra, "fails": Fail, "skips": Fail,
		"missing": Fail, "unfinished": Fail, "unbuilt": Fail, "narrowed": Pass, "vm": Live,
		"./tests/security: other tests": Fail,
	} {
		if got[name].Status != want {
			t.Errorf("path %s = %s (%s), want %s", name, got[name].Status, got[name].Detail, want)
		}
	}
	if d := got["unbuilt"].Detail; !strings.Contains(d, "syntax error") {
		t.Errorf("an unbuilt package's path does not say why: %q", d)
	}
	if d := got["./tests/security: other tests"].Detail; !strings.Contains(d, "TestOther2 failed") || strings.Contains(d, "TestFail") {
		t.Errorf("other tests = %q, want TestOther2 and no test a path already names", d)
	}
	wantCalls := []string{
		"-json -count=1 -timeout 1m0s ./tests/security",
		"-json -count=1 -timeout 1m0s -run ^(TestX)$ ./pkg/broken",
		"-json -count=1 -timeout 1m0s -run ^(TestOK)$ ./pkg/ok",
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("go test calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(wantCalls, "\n"))
	}

	dir := t.TempDir()
	summary, failed := filepath.Join(dir, "summary.md"), filepath.Join(dir, "failed")
	var out strings.Builder
	n, err := report(verdicts, "Security attack paths", &out, summary, failed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Errorf("report counted %d failed paths, want 6:\n%s", n, out.String())
	}
	md, _ := os.ReadFile(summary)
	for _, want := range []string{"| `passes` t | #1 | in-process | **PASS** |", "| `fails` t | #1 | in-process | **FAIL** |", "| `vm` t | #3 | live-vm | **LIVE** |", "6 attack path(s) failed"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("summary lacks %q:\n%s", want, md)
		}
	}
	names, _ := os.ReadFile(failed)
	if want := "fails\nskips\nmissing\nunfinished\nunbuilt\n./tests/security: other tests\n"; string(names) != want {
		t.Errorf("failed paths file = %q, want %q", names, want)
	}
	if !strings.Contains(out.String(), "::error title=security-attacks fails::TestFail failed: a viewer read the access code") ||
		!strings.Contains(out.String(), "FAILED: fails, skips, missing, unfinished, unbuilt, ./tests/security: other tests") {
		t.Errorf("stdout does not name the failed paths:\n%s", out.String())
	}
}

func TestJudgeLiveReadsGoTestVerboseLogs(t *testing.T) {
	root := writeTree(t, map[string]string{
		"vm.log": `=== RUN   TestFence
=== RUN   TestFence/metadata
    --- FAIL: TestFence/metadata (1.00s)
--- PASS: TestFence (12.00s)
--- SKIP: TestIsolation (0.00s)
    real_test.go:109: GRAIN_REAL_VM_TESTS not set
--- PASS: TestK8s (3.00s)
`,
	})
	m := Manifest{Paths: []Path{
		{Name: "fence", Mode: modeLiveVM, Tests: []string{"TestFence"}},
		{Name: "isolation", Mode: modeLiveVM, Tests: []string{"TestIsolation"}},
		{Name: "k8s", Mode: modeLiveK8s, Tests: []string{"TestK8s"}},
	}}
	got := JudgeLive(m, modeLiveVM, []string{filepath.Join(root, "vm.log")})
	if len(got) != 2 || got[0].Status != Pass || got[1].Status != Fail || !strings.Contains(got[1].Detail, "GRAIN_REAL_VM_TESTS not set") {
		t.Errorf("live-vm verdicts = %+v", got)
	}
	// The job's setup failed before the suite wrote its log: not a pass.
	got = JudgeLive(m, modeLiveK8s, []string{filepath.Join(root, "absent.log")})
	if len(got) != 1 || got[0].Status != Fail || !strings.Contains(got[0].Detail, "no log at") {
		t.Errorf("a missing log judged %+v, want a failure naming it", got)
	}
}

func TestMainExitCodes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{
		{nil, 2},
		{[]string{"bogus", "-root", repoRoot}, 2},
		{[]string{"judge", "-root", repoRoot}, 2},
		{[]string{"check", "-root", repoRoot}, 0},
		{[]string{"judge", "-root", repoRoot, "-mode", modeLiveK8s, "-log", "/nonexistent/k8s.log"}, 1},
	} {
		var out, errOut strings.Builder
		if got := Main(tc.args, &out, &errOut); got != tc.want {
			t.Errorf("Main(%q) = %d, want %d\n%s%s", tc.args, got, tc.want, out.String(), errOut.String())
		}
	}
}

func TestOnlyTheSkipsOwnReasonExcusesIt(t *testing.T) {
	for _, c := range []struct {
		output []string
		want   Status
	}{
		{[]string{"    x_test.go:9: known gap: the cache takes writes"}, KnownGap},
		{[]string{"    x_test.go:9: Infrastructure: no /dev/kvm"}, Infra},
		{[]string{"    x_test.go:9: known gaps are listed in the doc"}, Skip},
		{[]string{"    x_test.go:5: known gap: logged earlier", "    x_test.go:9: initdb not installed"}, Skip},
		{[]string{"    x_test.go:5: infrastructure: logged earlier", "    x_test.go:9: initdb not installed"}, Skip},
	} {
		r := &result{status: Skip, output: c.output, own: c.output}
		r.finish()
		if r.status != c.want {
			t.Errorf("skip with output %q = %s, want %s", c.output, r.status, c.want)
		}
	}
}

// What go test really prints for a test that skips for one reason while a
// parallel subtest logs "known gap:" after it, and for an excused skip:
// -v streams a test's messages ahead of its verdict, and -json indents a
// subtest's messages no deeper than its parent's.
func TestASubtestsMessageDoesNotExcuseItsParentsSkip(t *testing.T) {
	verbose := `=== RUN   TestX
=== RUN   TestX/sub
=== PAUSE TestX/sub
=== NAME  TestX
    x_test.go:5: initdb missing
=== CONT  TestX/sub
    x_test.go:4: known gap: sub
--- SKIP: TestX (0.00s)
    --- PASS: TestX/sub (0.00s)
=== RUN   TestY
    x_test.go:7: known gap: real
--- SKIP: TestY (0.00s)
PASS
`
	events := `{"Action":"run","Test":"TestX"}
{"Action":"output","Test":"TestX","Output":"=== RUN   TestX\n"}
{"Action":"run","Test":"TestX/sub"}
{"Action":"output","Test":"TestX/sub","Output":"=== RUN   TestX/sub\n"}
{"Action":"output","Test":"TestX","Output":"    x_test.go:5: initdb missing\n"}
{"Action":"output","Test":"TestX/sub","Output":"    x_test.go:4: known gap: sub\n"}
{"Action":"pass","Test":"TestX/sub"}
{"Action":"output","Test":"TestX","Output":"--- SKIP: TestX (0.00s)\n"}
{"Action":"skip","Test":"TestX"}
{"Action":"run","Test":"TestY"}
{"Action":"output","Test":"TestY","Output":"    x_test.go:7: known gap: real\n"}
{"Action":"output","Test":"TestY","Output":"--- SKIP: TestY (0.00s)\n"}
{"Action":"skip","Test":"TestY"}
`
	for name, run := range map[string]*pkgRun{
		"-v":    parseText(strings.NewReader(verbose)),
		"-json": parseJSON(strings.NewReader(events), io.Discard),
	} {
		if x := run.tests["TestX"]; x == nil || x.status != Skip || x.reason() != "initdb missing" {
			t.Errorf("%s: TestX = %+v, want a skip for initdb missing", name, x)
		}
		if y := run.tests["TestY"]; y == nil || y.status != KnownGap || y.reason() != "known gap: real" {
			t.Errorf("%s: TestY = %+v, want a known gap", name, y)
		}
	}
}

// Go marks a parent test PASS even when its subtests skip; a multi-line
// t.Skip indents continuation lines eight spaces; and code under test can
// print unindented lines between t.Log and t.Skip. None of those may turn an
// unexcused skip into a pass or a known gap.
func TestSkippedSubtestsMultiLineSkipsAndUnindentedLogsFailClosed(t *testing.T) {
	verbose := `=== RUN   TestSubUnexcused
=== RUN   TestSubUnexcused/gap
    x_test.go:4: known gap: sub gap
=== RUN   TestSubUnexcused/bad
    x_test.go:7: initdb missing
--- PASS: TestSubUnexcused (0.00s)
    --- SKIP: TestSubUnexcused/gap (0.00s)
    --- SKIP: TestSubUnexcused/bad (0.00s)
=== RUN   TestSubGap
=== RUN   TestSubGap/ok
=== RUN   TestSubGap/gap
    x_test.go:12: known gap: sub only
--- PASS: TestSubGap (0.00s)
    --- PASS: TestSubGap/ok (0.00s)
    --- SKIP: TestSubGap/gap (0.00s)
=== RUN   TestSubInfra
=== RUN   TestSubInfra/gap
    x_test.go:16: known gap: sub gap
=== RUN   TestSubInfra/infra
    x_test.go:19: infrastructure: no /dev/kvm
--- PASS: TestSubInfra (0.00s)
    --- SKIP: TestSubInfra/gap (0.00s)
    --- SKIP: TestSubInfra/infra (0.00s)
=== RUN   TestMultiLineSkip
    x_test.go:24: initdb missing:
        foo.go:12: known gap: continuation line
--- SKIP: TestMultiLineSkip (0.00s)
=== RUN   TestUnindentedNoise
    x_test.go:29: known gap: logged earlier
2026/11/12 01:55:00 starting helper daemon on 127.0.0.1:0
    x_test.go:32: /dev/kvm not present
--- SKIP: TestUnindentedNoise (0.00s)
PASS
ok  	example.com/sec	0.010s
`
	jsonStream := `{"Action":"run","Test":"TestSubUnexcused"}
{"Action":"run","Test":"TestSubUnexcused/gap"}
{"Action":"output","Test":"TestSubUnexcused/gap","Output":"    x_test.go:4: known gap: sub gap\n"}
{"Action":"skip","Test":"TestSubUnexcused/gap"}
{"Action":"run","Test":"TestSubUnexcused/bad"}
{"Action":"output","Test":"TestSubUnexcused/bad","Output":"    x_test.go:7: initdb missing\n"}
{"Action":"skip","Test":"TestSubUnexcused/bad"}
{"Action":"pass","Test":"TestSubUnexcused"}
{"Action":"run","Test":"TestSubGap"}
{"Action":"run","Test":"TestSubGap/ok"}
{"Action":"pass","Test":"TestSubGap/ok"}
{"Action":"run","Test":"TestSubGap/gap"}
{"Action":"output","Test":"TestSubGap/gap","Output":"    x_test.go:12: known gap: sub only\n"}
{"Action":"skip","Test":"TestSubGap/gap"}
{"Action":"pass","Test":"TestSubGap"}
{"Action":"run","Test":"TestSubInfra"}
{"Action":"run","Test":"TestSubInfra/gap"}
{"Action":"output","Test":"TestSubInfra/gap","Output":"    x_test.go:16: known gap: sub gap\n"}
{"Action":"skip","Test":"TestSubInfra/gap"}
{"Action":"run","Test":"TestSubInfra/infra"}
{"Action":"output","Test":"TestSubInfra/infra","Output":"    x_test.go:19: infrastructure: no /dev/kvm\n"}
{"Action":"skip","Test":"TestSubInfra/infra"}
{"Action":"pass","Test":"TestSubInfra"}
{"Action":"run","Test":"TestMultiLineSkip"}
{"Action":"output","Test":"TestMultiLineSkip","Output":"    x_test.go:24: initdb missing:\n"}
{"Action":"output","Test":"TestMultiLineSkip","Output":"        foo.go:12: known gap: continuation line\n"}
{"Action":"skip","Test":"TestMultiLineSkip"}
{"Action":"run","Test":"TestUnindentedNoise"}
{"Action":"output","Test":"TestUnindentedNoise","Output":"    x_test.go:29: known gap: logged earlier\n"}
{"Action":"output","Test":"TestUnindentedNoise","Output":"2026/11/12 01:55:00 starting helper daemon on 127.0.0.1:0\n"}
{"Action":"output","Test":"TestUnindentedNoise","Output":"    x_test.go:32: /dev/kvm not present\n"}
{"Action":"skip","Test":"TestUnindentedNoise"}
`
	for mode, run := range map[string]*pkgRun{
		"-v":    parseText(strings.NewReader(verbose)),
		"-json": parseJSON(strings.NewReader(jsonStream), io.Discard),
	} {
		for _, tc := range []struct {
			test   string
			status Status
			reason string
		}{
			{"TestSubUnexcused", Skip, "bad: initdb missing"},
			{"TestSubGap", KnownGap, "gap: known gap: sub only"},
			{"TestSubInfra", Infra, "infra: infrastructure: no /dev/kvm"},
			{"TestMultiLineSkip", Skip, "initdb missing:"},
			{"TestUnindentedNoise", Skip, "/dev/kvm not present"},
		} {
			got := run.tests[tc.test]
			if got == nil || got.status != tc.status || got.reason() != tc.reason {
				t.Errorf("%s %s = %+v (reason %q), want status %s reason %q", mode, tc.test, got, func() string {
					if got == nil {
						return ""
					}
					return got.reason()
				}(), tc.status, tc.reason)
			}
		}
		if v := judgePath(Path{Name: "p", Tests: []string{"TestSubUnexcused"}}, run); v.Status != Fail {
			t.Errorf("%s judgePath(TestSubUnexcused) = %+v, want Fail", mode, v)
		}
	}
}
