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

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
)

func findToolByName(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return Tool{}
}

func TestNamespaceMCPToolsLsReadGrepAndElisionCap(t *testing.T) {
	ciStore := ci.NewRunStore(10)
	started := time.Now().Add(-2 * time.Minute)
	finished := time.Now()
	bigLog := "=== RUN   TestFlaky\n    flaky_test.go:19: attempt 1 failed\n" +
		strings.Repeat("    flaky_test.go:20: diagnostic line padding to exceed tool cap\n", 40) +
		"--- FAIL: TestFlaky (0.05s)\nFAIL\tgithub.com/ai-on-gke/grain/pkg/ci\t0.100s\n"

	ciStore.Put(ci.Run{
		ID:         "run-796",
		Repo:       "bwsalmon/grain",
		Conclusion: ci.ConclusionCancelled,
		StartedAt:  &started,
		FinishedAt: &finished,
		Jobs: []ci.JobRun{
			{
				Name:       "go",
				Conclusion: ci.ConclusionCancelled,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionCancelled,
						Output:     "cancelled on retry\n",
					},
				},
				Attempts: []ci.JobAttempt{
					{
						Attempt:    1,
						Conclusion: ci.ConclusionFailure,
						StartedAt:  &started,
						FinishedAt: &finished,
						Steps: []ci.StepRun{
							{
								Name:       "Test",
								Conclusion: ci.ConclusionFailure,
								Output:     bigLog,
							},
						},
						GoTestLog: bigLog,
					},
					{
						Attempt:    2,
						Conclusion: ci.ConclusionCancelled,
						StartedAt:  &started,
						FinishedAt: &finished,
						Steps: []ci.StepRun{
							{
								Name:       "Test",
								Conclusion: ci.ConclusionCancelled,
								Output:     "cancelled on retry\n",
							},
						},
					},
				},
			},
		},
	})

	srcDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcDir, "pkg", "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "pkg", "ci", "engine.go"), []byte("package ci\n// Engine runs jobs\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &namespace.Server{
		CIStore:  ciStore,
		SrcDir:   srcDir,
		Redactor: redact.New(nil),
	}
	tools := NewNamespaceTools(NamespaceToolsConfig{
		Server: srv,
		Scope: namespace.Scope{
			TaskRepo:     "bwsalmon/grain",
			MayReadCI:    true,
			MayReadAllCI: true,
			MayReadSrc:   true,
		},
	})

	ctx := context.Background()

	// 1. grain_ls on attempts
	lsTool := findToolByName(t, tools, "grain_ls")
	res := lsTool.Handler(ctx, map[string]any{"path": "ci://bwsalmon/grain/run-796/go/attempts"})
	if res.IsError {
		t.Fatalf("grain_ls error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "1/") || !strings.Contains(res.Text, "2/") {
		t.Fatalf("grain_ls missing attempts 1/ and 2/:\n%s", res.Text)
	}

	// 2. grain_grep on attempt 1 per-test output
	grepTool := findToolByName(t, tools, "grain_grep")
	res = grepTool.Handler(ctx, map[string]any{
		"path":    "ci://bwsalmon/grain/run-796/go/attempts/1/tests/github.com/ai-on-gke/grain/pkg/ci/TestFlaky.log",
		"pattern": "attempt 1 failed",
	})
	if res.IsError {
		t.Fatalf("grain_grep error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "flaky_test.go:19: attempt 1 failed") {
		t.Fatalf("grain_grep missing match:\n%s", res.Text)
	}

	// 3. Shrink maxToolResultBytes and verify grain_read is cut at the cap and points at REST + grain_copy
	oldCap := maxToolResultBytes.Swap(600)
	defer maxToolResultBytes.Store(oldCap)

	readTool := findToolByName(t, tools, "grain_read")
	res = readTool.Handler(ctx, map[string]any{
		"path": "ci://bwsalmon/grain/run-796/go/attempts/1/gotest.log",
	})
	if res.IsError {
		t.Fatalf("grain_read error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "http://127.0.0.1:9728/fs/ci/bwsalmon/grain/run-796/go/attempts/1/gotest.log") {
		t.Fatalf("capped grain_read did not point to REST URL:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "grain_copy") {
		t.Fatalf("capped grain_read did not mention grain_copy:\n%s", res.Text)
	}
}

func TestNamespaceGrainCopyOnHostGranuleAndDockerBackends(t *testing.T) {
	ciStore := ci.NewRunStore(10)
	started := time.Now().Add(-time.Minute)
	finished := time.Now()
	ciStore.Put(ci.Run{
		ID:         "run-copy",
		Repo:       "bwsalmon/grain",
		Conclusion: ci.ConclusionFailure,
		StartedAt:  &started,
		FinishedAt: &finished,
		Jobs: []ci.JobRun{
			{
				Name:       "go",
				Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionFailure,
						Output:     "=== RUN   TestCopy\n    copy_test.go:9: failed\n--- FAIL: TestCopy (0.01s)\nFAIL\tgithub.com/ai-on-gke/grain/pkg/copy\t0.02s\n",
					},
				},
			},
		},
	})

	srcDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "b.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &namespace.Server{
		CIStore:  ciStore,
		SrcDir:   srcDir,
		Redactor: redact.New(nil),
	}

	backends := []struct {
		name  string
		setup func(t *testing.T) (dir string, sandboxTools []Tool)
	}{
		{
			name: "host",
			setup: func(t *testing.T) (string, []Tool) {
				dir := t.TempDir()
				return dir, NewHostSandboxTools(dir, nil)
			},
		},
		{
			name: "granule",
			setup: func(t *testing.T) (string, []Tool) {
				dir := t.TempDir()
				return dir, NewSSHSandboxTools(localExecRunner{dir: dir}, dir)
			},
		},
		{
			name: "docker",
			setup: func(t *testing.T) (string, []Tool) {
				dir := t.TempDir()
				return dir, NewSSHSandboxTools(localExecRunner{dir: dir}, dir)
			},
		},
	}

	for _, tc := range backends {
		t.Run(tc.name, func(t *testing.T) {
			dir, sbTools := tc.setup(t)
			nsTools := NewNamespaceTools(NamespaceToolsConfig{
				Server: srv,
				Scope: namespace.Scope{
					TaskRepo:     "bwsalmon/grain",
					MayReadCI:    true,
					MayReadAllCI: true,
					MayReadSrc:   true,
				},
				WriteFile: SandboxWriteFile(sbTools),
			})
			copyTool := findToolByName(t, nsTools, "grain_copy")
			ctx := context.Background()

			// Copy a single per-test CI log into the sandbox
			res := copyTool.Handler(ctx, map[string]any{
				"path": "ci://bwsalmon/grain/run-copy/go/tests/github.com/ai-on-gke/grain/pkg/copy/TestCopy.log",
				"dest": "logs/TestCopy.log",
			})
			if res.IsError {
				t.Fatalf("grain_copy file error on %s: %s", tc.name, res.Text)
			}
			gotFile, err := os.ReadFile(filepath.Join(dir, "logs", "TestCopy.log"))
			if err != nil {
				t.Fatalf("reading copied file on %s: %v", tc.name, err)
			}
			if !strings.Contains(string(gotFile), "--- FAIL: TestCopy") {
				t.Fatalf("copied file content mismatch on %s:\n%s", tc.name, gotFile)
			}

			// Copy a directory tree from src:// into the sandbox
			res = copyTool.Handler(ctx, map[string]any{
				"path": "src://sub",
				"dest": "copied-sub",
			})
			if res.IsError {
				t.Fatalf("grain_copy tree error on %s: %s", tc.name, res.Text)
			}
			gotA, err := os.ReadFile(filepath.Join(dir, "copied-sub", "a.txt"))
			if err != nil || string(gotA) != "alpha\n" {
				t.Fatalf("copied tree a.txt on %s = %q, err = %v", tc.name, gotA, err)
			}
			gotB, err := os.ReadFile(filepath.Join(dir, "copied-sub", "b.txt"))
			if err != nil || string(gotB) != "beta\n" {
				t.Fatalf("copied tree b.txt on %s = %q, err = %v", tc.name, gotB, err)
			}
		})
	}
}

// grain_copy moves namespace content into the sandbox without going over
// ServeHTTP, so it has to apply the same redaction a REST read does: a
// secret the daemon knows must not land in the workspace in the clear.
func TestNamespaceGrainCopyRedactsKnownSecrets(t *testing.T) {
	const secret = "s3cr3t-value-the-daemon-holds"
	ciStore := ci.NewRunStore(10)
	ciStore.Put(ci.Run{
		ID:         "run-secret",
		Repo:       "bwsalmon/grain",
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{{
			Name:       "go",
			Conclusion: ci.ConclusionFailure,
			Steps: []ci.StepRun{{
				Name:       "Test",
				Conclusion: ci.ConclusionFailure,
				Output:     "token=" + secret + "\nFAIL\n",
			}},
		}},
	})
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "leak.txt"), []byte("x "+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &namespace.Server{
		CIStore:  ciStore,
		SrcDir:   srcDir,
		Redactor: redact.New([]redact.KnownValue{{Label: "secret test", Value: secret}}),
	}
	dir := t.TempDir()
	copyTool := findToolByName(t, NewNamespaceTools(NamespaceToolsConfig{
		Server:    srv,
		Scope:     namespace.Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true, MayReadSrc: true},
		WriteFile: SandboxWriteFile(NewHostSandboxTools(dir, nil)),
	}), "grain_copy")

	for _, tc := range []struct{ path, dest string }{
		{"ci://bwsalmon/grain/run-secret/go/Test.log", "ci.log"},
		{"src://leak.txt", "src.txt"},
	} {
		res := copyTool.Handler(context.Background(), map[string]any{"path": tc.path, "dest": tc.dest})
		if res.IsError {
			t.Fatalf("grain_copy %s: %s", tc.path, res.Text)
		}
		got, err := os.ReadFile(filepath.Join(dir, tc.dest))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(got), secret) {
			t.Fatalf("grain_copy %s wrote the secret unredacted:\n%s", tc.path, got)
		}
		if !strings.Contains(string(got), "[redacted: secret test]") {
			t.Fatalf("grain_copy %s did not redact:\n%s", tc.path, got)
		}
	}
}
