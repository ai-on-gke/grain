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

package checks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runRealCmdInDir(baseDir string) func(ctx context.Context, dir string, argv []string, timeout time.Duration) (string, int, error) {
	return func(ctx context.Context, dir string, argv []string, timeout time.Duration) (string, int, error) {
		cctx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			cctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
		cmd.Dir = filepath.Join(baseDir, dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return string(out), exitErr.ExitCode(), nil
			}
			return string(out), 1, err
		}
		return string(out), 0, nil
	}
}

func TestMutationHardeningRealGoTest(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/testmut\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "package testmut\n\nfunc ClampNonNegative(v int) int {\n\tif v < 0 {\n\t\treturn 0\n\t}\n\treturn v\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "math.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// Weak test: never checks v == 0 boundary, so `<` -> `<=` survives, AND never asserts return value of ClampNonNegative(5)!
	weakTest := "package testmut\n\nimport \"testing\"\n\nfunc TestWeak(t *testing.T) {\n\t_ = ClampNonNegative(5)\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "math_test.go"), []byte(weakTest), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := &SandboxWorkspace{
		CheckoutDir: tmpDir,
		ReadFile: func(_ context.Context, p string, _ int) ([]byte, error) {
			return os.ReadFile(p)
		},
	}
	pd := ParsedDiff{
		Files: []DiffFile{{
			NewPath: "math.go", OldPath: "math.go",
			AddedLines: []NumberedLine{
				{Line: 4, Text: "\tif v < 0 {"},
			},
		}},
	}

	// 1. Without ExecInSandbox -> silent skip (0 findings).
	skipped, err := CheckMutationHardening(context.Background(), ws, pd, RunOptions{}, nil)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("expected silent skip without sandbox runner, got findings=%v err=%v", skipped, err)
	}

	// 2. With real go test and weak test -> mutant `<` -> `<=` survives!
	opts := RunOptions{ExecInSandbox: runRealCmdInDir(tmpDir)}
	findings, err := CheckMutationHardening(context.Background(), ws, pd, opts, map[string]any{"mutants": 10, "max-seconds": 30})
	if err != nil {
		t.Fatalf("CheckMutationHardening: %v", err)
	}
	if len(findings) == 0 || !strings.Contains(findings[0].Summary, "<=") {
		t.Fatalf("expected surviving `<` -> `<=` mutant finding, got %+v", findings)
	}

	// Verify original file was restored intact.
	after, err := os.ReadFile(filepath.Join(tmpDir, "math.go"))
	if err != nil || string(after) != src {
		t.Fatalf("math.go was not restored cleanly: %q", string(after))
	}

	// 3. Upgrade test to assert exact boundary v == 0 is NOT clamped unless needed (or test v := 0 returns 0? Wait: for `v < 0 { return -1 }`, `v == 0` distinguishes `<` from `<=`!).
	src2 := "package testmut\n\nfunc Sign(v int) int {\n\tif v < 0 {\n\t\treturn -1\n\t}\n\treturn 1\n}\n"
	strongTest := "package testmut\n\nimport \"testing\"\n\nfunc TestStrong(t *testing.T) {\n\tif got := Sign(-1); got != -1 { t.Fatalf(\"got %d\", got) }\n\tif got := Sign(0); got != 1 { t.Fatalf(\"got %d\", got) }\n\tif got := Sign(2); got != 1 { t.Fatalf(\"got %d\", got) }\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "math.go"), []byte(src2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "math_test.go"), []byte(strongTest), 0o644); err != nil {
		t.Fatal(err)
	}
	killed, err := CheckMutationHardening(context.Background(), ws, pd, opts, map[string]any{"mutants": 10, "max-seconds": 30})
	if err != nil {
		t.Fatalf("CheckMutationHardening strong: %v", err)
	}
	if len(killed) != 0 {
		t.Fatalf("expected 0 surviving mutants with boundary assertion, got %+v", killed)
	}
}
