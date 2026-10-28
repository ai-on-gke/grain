package ci

import (
	"strings"
	"testing"
)

func TestExtractFailureSummary_GoTests(t *testing.T) {
	t.Run("basic go test failure", func(t *testing.T) {
		output := `=== RUN   TestCalculatorAdd
    calculator_test.go:42: Add(2, 3) = 6, want 5
--- FAIL: TestCalculatorAdd (0.00s)
FAIL
exit status 1`
		summary := ExtractFailureSummary(output)
		if summary == nil || summary.Empty() {
			t.Fatalf("expected non-empty FailureSummary, got %+v", summary)
		}
		if len(summary.Tests) != 1 {
			t.Fatalf("expected 1 test failure, got %d", len(summary.Tests))
		}
		tf := summary.Tests[0]
		if tf.Name != "TestCalculatorAdd" {
			t.Errorf("expected test name 'TestCalculatorAdd', got %q", tf.Name)
		}
		if tf.Location != "calculator_test.go:42" {
			t.Errorf("expected location 'calculator_test.go:42', got %q", tf.Location)
		}
		if !strings.Contains(tf.Message, "Add(2, 3) = 6, want 5") {
			t.Errorf("expected message to contain 'Add(2, 3) = 6, want 5', got %q", tf.Message)
		}
	})

	t.Run("go test subtest failure with diff", func(t *testing.T) {
		output := `=== RUN   TestSuite
=== RUN   TestSuite/SubTestA
    suite_test.go:120: mismatch in config:
        diff:
        - {"timeout": 30}
        + {"timeout": 60}
--- FAIL: TestSuite (0.05s)
    --- FAIL: TestSuite/SubTestA (0.01s)
FAIL`
		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Tests) < 1 {
			t.Fatalf("expected test failures, got %+v", summary)
		}
		var foundSubtest bool
		for _, tf := range summary.Tests {
			if tf.Name == "TestSuite/SubTestA" {
				foundSubtest = true
				if tf.Location != "suite_test.go:120" {
					t.Errorf("expected location 'suite_test.go:120', got %q", tf.Location)
				}
				if !strings.Contains(tf.Diff, `{"timeout": 30}`) {
					t.Errorf("expected diff to contain timeout, got %q", tf.Diff)
				}
			}
		}
		if !foundSubtest {
			t.Errorf("did not find TestSuite/SubTestA in failures: %+v", summary.Tests)
		}
	})

	t.Run("go test with panic", func(t *testing.T) {
		output := `=== RUN   TestNilDeref
--- FAIL: TestNilDeref (0.00s)
panic: runtime error: invalid memory address or nil pointer dereference [recovered]
	panic: runtime error: invalid memory address or nil pointer dereference
goroutine 19 [running]:
github.com/bwsalmon/grain/pkg/ci.TestNilDeref(0xc00010c780)
	/home/debian/work/pkg/ci/engine_test.go:88 +0x3c
FAIL	github.com/bwsalmon/grain/pkg/ci	0.015s`
		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Tests) != 1 {
			t.Fatalf("expected 1 test failure, got %+v", summary)
		}
		tf := summary.Tests[0]
		if tf.Name != "TestNilDeref" {
			t.Errorf("expected name TestNilDeref, got %q", tf.Name)
		}
		if !strings.Contains(tf.Message, "panic: runtime error: invalid memory address or nil pointer dereference") {
			t.Errorf("expected panic message, got %q", tf.Message)
		}
		if !strings.Contains(tf.Location, "engine_test.go:88") {
			t.Errorf("expected location from stack trace, got %q", tf.Location)
		}
	})
}

func TestExtractFailureSummary_PlaywrightJest(t *testing.T) {
	t.Run("playwright failure", func(t *testing.T) {
		output := `
Running 15 tests using 4 workers
  1) [chromium] › tests/e2e/auth.spec.ts:18:5 › Authentication › user can login with 2FA ───

    Error: expect(received).toBe(expected) // Object.is equality

    Expected: 200
    Received: 401

      22 |   await page.goto('/login');
    > 23 |   expect(response.status()).toBe(200);
         |                             ^
      24 | });

        at /home/debian/work/tests/e2e/auth.spec.ts:23:29

  1 failed
    [chromium] › tests/e2e/auth.spec.ts:18:5 › Authentication › user can login with 2FA
`
		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Tests) != 1 {
			t.Fatalf("expected 1 test failure, got %+v", summary)
		}
		tf := summary.Tests[0]
		if !strings.Contains(tf.Name, "user can login with 2FA") {
			t.Errorf("expected test name to contain 'user can login with 2FA', got %q", tf.Name)
		}
		if !strings.Contains(tf.Location, "auth.spec.ts:18:5") && !strings.Contains(tf.Location, "auth.spec.ts:23:29") {
			t.Errorf("expected location in auth.spec.ts, got %q", tf.Location)
		}
		if !strings.Contains(tf.Message, "Error: expect(received).toBe(expected)") {
			t.Errorf("expected Error: expect(received)... message, got %q", tf.Message)
		}
		if !strings.Contains(tf.Diff, "Expected: 200") || !strings.Contains(tf.Diff, "Received: 401") {
			t.Errorf("expected Expected/Received diff, got %q", tf.Diff)
		}
	})

	t.Run("jest test failure", func(t *testing.T) {
		output := `
FAIL src/components/Button.test.tsx
  ● Button Component › renders correctly with primary variant

    expect(received).toEqual(expected) // deep equality

    - Expected  - 1
    + Received  + 1

    - <button class="btn-primary">Submit</button>
    + <button class="btn-secondary">Submit</button>

      15 |   render(<Button variant="primary">Submit</Button>);
    > 16 |   expect(screen.getByRole('button')).toEqual(expected);
         |                                      ^
      17 | });

      at Object.<anonymous> (src/components/Button.test.tsx:16:38)

Test Suites: 1 failed, 1 total
Tests:       1 failed, 4 passed, 5 total
`
		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Tests) != 1 {
			t.Fatalf("expected 1 test failure, got %+v", summary)
		}
		tf := summary.Tests[0]
		if !strings.Contains(tf.Name, "Button Component › renders correctly with primary variant") {
			t.Errorf("expected test name, got %q", tf.Name)
		}
		if !strings.Contains(tf.Location, "Button.test.tsx:16:38") {
			t.Errorf("expected location Button.test.tsx:16:38, got %q", tf.Location)
		}
		if !strings.Contains(tf.Diff, "- Expected") || !strings.Contains(tf.Diff, "+ Received") {
			t.Errorf("expected diff, got %q", tf.Diff)
		}
	})
}

func TestExtractFailureSummary_Diagnostics(t *testing.T) {
	t.Run("golangci-lint / go compiler diagnostics", func(t *testing.T) {
		output := `
pkg/ci/results.go:120:5: undefined: SomeUndefinedType (typecheck)
pkg/ci/engine.go:45:10: S1000: should use for range instead (gosimple)
`
		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Diagnostics) != 2 {
			t.Fatalf("expected 2 diagnostics, got %+v", summary)
		}
		d1 := summary.Diagnostics[0]
		if d1.File != "pkg/ci/results.go" || d1.Line != 120 || d1.Column != 5 {
			t.Errorf("unexpected d1 location: %s:%d:%d", d1.File, d1.Line, d1.Column)
		}
		if !strings.Contains(d1.Message, "undefined: SomeUndefinedType") {
			t.Errorf("unexpected d1 message: %q", d1.Message)
		}
		if d1.Rule != "typecheck" {
			t.Errorf("unexpected d1 rule: %q", d1.Rule)
		}

		d2 := summary.Diagnostics[1]
		if d2.File != "pkg/ci/engine.go" || d2.Line != 45 || d2.Column != 10 {
			t.Errorf("unexpected d2 location: %s:%d:%d", d2.File, d2.Line, d2.Column)
		}
		if d2.Rule != "gosimple" {
			t.Errorf("unexpected d2 rule: %q", d2.Rule)
		}
	})

	t.Run("typescript tsc diagnostics", func(t *testing.T) {
		output := `
src/api/client.ts(45,12): error TS2339: Property 'token' does not exist on type 'Session'.
`
		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Diagnostics) != 1 {
			t.Fatalf("expected 1 diagnostic, got %+v", summary)
		}
		d := summary.Diagnostics[0]
		if d.File != "src/api/client.ts" || d.Line != 45 || d.Column != 12 {
			t.Errorf("unexpected location: %s:%d:%d", d.File, d.Line, d.Column)
		}
		if d.Rule != "TS2339" {
			t.Errorf("unexpected rule: %q", d.Rule)
		}
		if !strings.Contains(d.Message, "Property 'token' does not exist") {
			t.Errorf("unexpected message: %q", d.Message)
		}
	})

	t.Run("eslint diagnostics", func(t *testing.T) {
		output := `
/home/debian/work/src/index.js
  15:10  error  'unusedVar' is defined but never used  no-unused-vars
  22:5   warning  Unexpected console.log statement     no-console
`
		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Diagnostics) != 2 {
			t.Fatalf("expected 2 diagnostics, got %+v", summary)
		}
		if summary.Diagnostics[0].Line != 15 || summary.Diagnostics[0].Rule != "no-unused-vars" {
			t.Errorf("unexpected diagnostic 0: %+v", summary.Diagnostics[0])
		}
		if summary.Diagnostics[1].Line != 22 || summary.Diagnostics[1].Severity != "warning" {
			t.Errorf("unexpected diagnostic 1: %+v", summary.Diagnostics[1])
		}
	})
}

func TestExtractFailureSummary_ANSIStrippingAndClean(t *testing.T) {
	t.Run("ansi escape codes stripped", func(t *testing.T) {
		output := "\x1b[31m--- FAIL: TestColored (0.00s)\x1b[0m\n    \x1b[32mcolored_test.go:10:\x1b[0m failed assertion"
		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Tests) != 1 {
			t.Fatalf("expected 1 test failure, got %+v", summary)
		}
		if summary.Tests[0].Name != "TestColored" {
			t.Errorf("expected TestColored, got %q", summary.Tests[0].Name)
		}
		if summary.Tests[0].Location != "colored_test.go:10" {
			t.Errorf("expected colored_test.go:10, got %q", summary.Tests[0].Location)
		}
	})

	t.Run("clean output returns nil", func(t *testing.T) {
		output := `=== RUN   TestPassing
--- PASS: TestPassing (0.01s)
PASS
ok  	github.com/bwsalmon/grain/pkg/ci	0.123s`
		summary := ExtractFailureSummary(output)
		if summary != nil && !summary.Empty() {
			t.Errorf("expected nil or empty summary for passing output, got %+v", summary)
		}
	})
}

func TestFailureSummary_Render(t *testing.T) {
	fs := &FailureSummary{
		Tests: []TestFailure{
			{
				Name:     "TestUserAuth",
				Location: "pkg/auth/auth_test.go:50",
				Message:  "expected status 200, got 401",
				Diff:     "- 200\n+ 401",
			},
		},
		Diagnostics: []Diagnostic{
			{
				File:     "pkg/auth/auth.go",
				Line:     25,
				Column:   8,
				Severity: "error",
				Message:  "undefined: ValidateToken",
				Rule:     "typecheck",
			},
		},
	}
	rendered := fs.Render()
	if !strings.Contains(rendered, "Failure summary:") {
		t.Errorf("rendered output missing header:\n%s", rendered)
	}
	if !strings.Contains(rendered, "• TestUserAuth (pkg/auth/auth_test.go:50): expected status 200, got 401") {
		t.Errorf("rendered output missing test line:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Diff:\n        - 200\n        + 401") {
		t.Errorf("rendered output missing diff block:\n%s", rendered)
	}
	if !strings.Contains(rendered, "• pkg/auth/auth.go:25:8: undefined: ValidateToken (typecheck)") {
		t.Errorf("rendered output missing diagnostic line:\n%s", rendered)
	}
}

func TestExtractFailureSummary_RunGoTestsIntegration(t *testing.T) {
	t.Run("run-go-tests.sh panic output with blank line and raw tail deduplicates and picks test location", func(t *testing.T) {
		output := `[go test] --- FAIL: TestPanic (0.00s) [pkg/foo]
panic: boom [recovered, repanicked] [pkg/foo]
FAIL	github.com/bwsalmon/grain/pkg/foo	0.002s
[go test] done in 0s: 0 pkg ok, 1 pkg fail, 0 pkg skip (0 tests pass, 1 fail)

=== failing tests ===
--- FAIL: TestPanic (0.00s) [pkg/foo]
panic: boom [recovered, repanicked]

goroutine 7 [running]:
testing.tRunner.func1.2({0x58bd60, 0x5c2dc0})
	/usr/local/go/src/testing/testing.go:1872 +0x23c
testing.tRunner.func1()
	/usr/local/go/src/testing/testing.go:1875 +0x358
panic({0x58bd60?, 0x5c2dc0?})
	/usr/local/go/src/runtime/panic.go:783 +0x120
github.com/bwsalmon/grain/pkg/foo.TestPanic(0xc0000c8380?)
	/home/debian/work/pkg/foo/panic_test.go:16 +0x2c
testing.tRunner(0xc0000c8380, 0x5c2aa8)
	/usr/local/go/src/testing/testing.go:1934 +0xe8

=== panics ===
panic: boom [recovered, repanicked] [pkg/foo]

goroutine 7 [running]:
testing.tRunner.func1.2({0x58bd60, 0x5c2dc0})
	/usr/local/go/src/testing/testing.go:1872 +0x23c
github.com/bwsalmon/grain/pkg/foo.TestPanic(0xc0000c8380?)
	/home/debian/work/pkg/foo/panic_test.go:16 +0x2c

=== last 200 lines of the raw run ===
=== RUN   TestPanic
--- FAIL: TestPanic (0.00s)
panic: boom [recovered, repanicked]

goroutine 7 [running]:
testing.tRunner.func1.2({0x58bd60, 0x5c2dc0})
	/usr/local/go/src/testing/testing.go:1872 +0x23c
github.com/bwsalmon/grain/pkg/foo.TestPanic(0xc0000c8380?)
	/home/debian/work/pkg/foo/panic_test.go:16 +0x2c
FAIL	github.com/bwsalmon/grain/pkg/foo	0.002s`

		summary := ExtractFailureSummary(output)
		if summary == nil || summary.Empty() {
			t.Fatalf("expected non-empty FailureSummary, got %+v", summary)
		}
		if len(summary.Tests) != 1 {
			t.Fatalf("expected 1 deduplicated test failure, got %d: %+v", len(summary.Tests), summary.Tests)
		}
		tf := summary.Tests[0]
		if tf.Name != "TestPanic" {
			t.Errorf("tf.Name = %q, want TestPanic", tf.Name)
		}
		if !strings.Contains(tf.Location, "panic_test.go:16") {
			t.Errorf("tf.Location = %q, want it to contain panic_test.go:16 (not stdlib)", tf.Location)
		}
		if len(summary.RawErrors) != 0 {
			t.Errorf("expected 0 RawErrors when panic is already attributed to TestPanic, got %v", summary.RawErrors)
		}
		if len(summary.Diagnostics) != 0 {
			t.Errorf("expected 0 Diagnostics for test panic, got %+v", summary.Diagnostics)
		}
	})

	t.Run("run-go-tests.sh data race output extracts race location and avoids duplicate tests", func(t *testing.T) {
		output := `[go test] --- FAIL: TestRace (0.01s) [pkg/foo]
WARNING: DATA RACE [pkg/foo]
FAIL	github.com/bwsalmon/grain/pkg/foo	0.015s

=== failing tests ===
--- FAIL: TestRace (0.01s) [pkg/foo]
==================
WARNING: DATA RACE
Write at 0x00c0000140b8 by goroutine 8:
  github.com/bwsalmon/grain/pkg/foo.TestRace.func1()
      /home/debian/work/pkg/foo/race_test.go:11 +0x34

Previous write at 0x00c0000140b8 by main goroutine:
  github.com/bwsalmon/grain/pkg/foo.TestRace()
      /home/debian/work/pkg/foo/race_test.go:13 +0x8c
  testing.tRunner()
      /usr/local/go/src/testing/testing.go:1934 +0x21c
==================
    testing.go:1712: race detected during execution of test

=== data races ===
WARNING: DATA RACE [pkg/foo]
Write at 0x00c0000140b8 by goroutine 8:
  github.com/bwsalmon/grain/pkg/foo.TestRace.func1()
      /home/debian/work/pkg/foo/race_test.go:11 +0x34

=== last 200 lines of the raw run ===
=== RUN   TestRace
==================
WARNING: DATA RACE
Write at 0x00c0000140b8 by goroutine 8:
  github.com/bwsalmon/grain/pkg/foo.TestRace.func1()
      /home/debian/work/pkg/foo/race_test.go:11 +0x34
==================
    testing.go:1712: race detected during execution of test
--- FAIL: TestRace (0.01s)
FAIL`

		summary := ExtractFailureSummary(output)
		if summary == nil || len(summary.Tests) != 1 {
			t.Fatalf("expected 1 test failure, got %+v", summary)
		}
		tf := summary.Tests[0]
		if !strings.Contains(tf.Location, "race_test.go:11") {
			t.Errorf("tf.Location = %q, want race_test.go:11 instead of testing.go", tf.Location)
		}
		if !strings.Contains(tf.Message, "WARNING: DATA RACE") {
			t.Errorf("tf.Message = %q, want WARNING: DATA RACE", tf.Message)
		}
		if len(summary.Diagnostics) != 0 {
			t.Errorf("expected 0 Diagnostics, got %+v", summary.Diagnostics)
		}
	})

	t.Run("noisy test failure with >45 lines and multiple t.Errorf/t.Logf lines", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("=== failing tests ===\n")
		b.WriteString("--- FAIL: TestNoisy (0.05s) [pkg/foo]\n")
		b.WriteString("    noisy_test.go:15: initial check failed\n")
		for i := 0; i < 45; i++ {
			b.WriteString("2026/10/22 00:00:00 daemon log line\n")
		}
		b.WriteString("    noisy_test.go:99: final fatal failure after daemon logs\n")
		b.WriteString("\n=== last 200 lines of the raw run ===\n")
		b.WriteString("=== RUN   TestOtherPassing\n")
		b.WriteString("    other_test.go:55: informative log from passing test\n")
		b.WriteString("--- PASS: TestOtherPassing (0.01s)\n")

		summary := ExtractFailureSummary(b.String())
		if summary == nil || len(summary.Tests) != 1 {
			t.Fatalf("expected 1 test failure, got %+v", summary)
		}
		tf := summary.Tests[0]
		if tf.Location != "noisy_test.go:15" {
			t.Errorf("tf.Location = %q, want noisy_test.go:15", tf.Location)
		}
		if !strings.Contains(tf.Message, "initial check failed") || !strings.Contains(tf.Message, "final fatal failure after daemon logs") {
			t.Errorf("tf.Message missing expected assertion messages, got:\n%s", tf.Message)
		}
		if len(summary.Diagnostics) != 0 {
			t.Errorf("expected 0 Diagnostics (t.Logf/t.Errorf must not be parsed as compiler diagnostics), got %+v", summary.Diagnostics)
		}
	})
}
