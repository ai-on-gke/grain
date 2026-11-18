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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundaryFuzzRealGoTest(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/testfuzz\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "package testfuzz\n\nfunc BadSliceAt(data []byte, offset int) byte {\n\treturn data[offset]\n}\n\nfunc SafeSliceAt(data []byte, offset int) byte {\n\tif offset < 0 || offset >= len(data) {\n\t\treturn 0\n\t}\n\treturn data[offset]\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "slice.go"), []byte(src), 0o644); err != nil {
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
			NewPath: "slice.go", OldPath: "slice.go",
			AddedLines: []NumberedLine{
				{Line: 3, Text: "func BadSliceAt(data []byte, offset int) byte {"},
				{Line: 7, Text: "func SafeSliceAt(data []byte, offset int) byte {"},
			},
		}},
	}

	// 1. Silent skip when no sandbox runner is present.
	skipped, err := CheckBoundaryFuzz(context.Background(), ws, pd, RunOptions{}, nil)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("expected silent skip without runner, got %v, %v", skipped, err)
	}

	// 2. Real execution catches BadSliceAt panic and passes SafeSliceAt!
	opts := RunOptions{ExecInSandbox: runRealCmdInDir(tmpDir)}
	findings, err := CheckBoundaryFuzz(context.Background(), ws, pd, opts, map[string]any{"max-seconds": 15})
	if err != nil {
		t.Fatalf("CheckBoundaryFuzz: %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Summary, "BadSliceAt") {
		t.Fatalf("expected exactly 1 finding for BadSliceAt, got %+v", findings)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "zz_grain_qa_fuzz_test.go")); !os.IsNotExist(statErr) {
		t.Fatalf("zz_grain_qa_fuzz_test.go was not cleaned up: %v", statErr)
	}
}
