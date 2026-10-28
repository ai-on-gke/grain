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
)

// requireNoHostWork skips a test whose subject is a "/work/..." path if
// this machine happens to have a real /work directory: the retry these
// tests exercise is about a path that is *not* there, and a host that
// has one is not the sandbox any of this is about.
func requireNoHostWork(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/work"); err == nil {
		t.Skip("this machine has a real /work; the guess under test is about one that does not exist")
	}
}

// The guess itself, against both transports: a run whose framework told
// it that it is sitting in "/" turns the prompt's "./work" into an
// absolute "/work/...", which is a path no sandbox has. Rather than
// spending the run a turn on "No such file or directory", the tools read
// the file it meant -- and say so, since a silent correction leaves the
// next call in the same run wrong in the same way.
func TestReadFileRecoversAnAbsolutePathUnderTheWorkspace(t *testing.T) {
	requireNoHostWork(t)
	for _, tc := range []struct {
		name   string
		client func(*testing.T, string) *Client
	}{
		{"local", newTestClient},
		{"ssh", newSSHTestClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.MkdirAll(filepath.Join(workspace, "work", "pkg"), 0o755); err != nil {
				t.Fatal(err)
			}
			real := filepath.Join(workspace, "work", "pkg", "task.go")
			if err := os.WriteFile(real, []byte("package model\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			res, err := tc.client(t, workspace).CallTool(context.Background(), "read_file",
				map[string]any{"file_path": "/work/pkg/task.go"})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatalf("read_file did not recover the path: %s", res.Text())
			}
			if !strings.Contains(res.Text(), "package model") {
				t.Errorf("read_file did not return the file's content: %q", res.Text())
			}
			// Both paths, out loud: what was asked for, and what was
			// actually read.
			for _, want := range []string{"/work/pkg/task.go", real, workspace} {
				if !strings.Contains(res.Text(), want) {
					t.Errorf("read_file corrected the path without saying %q: %q", want, res.Text())
				}
			}
		})
	}
}

// The same for an edit, which has the further requirement that the file
// it writes back is the file it read.
func TestEditFileRecoversAnAbsolutePathAndEditsThatFile(t *testing.T) {
	requireNoHostWork(t)
	for _, tc := range []struct {
		name   string
		client func(*testing.T, string) *Client
	}{
		{"local", newTestClient},
		{"ssh", newSSHTestClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.MkdirAll(filepath.Join(workspace, "work"), 0o755); err != nil {
				t.Fatal(err)
			}
			real := filepath.Join(workspace, "work", "main.go")
			if err := os.WriteFile(real, []byte("func old() {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			res, err := tc.client(t, workspace).CallTool(context.Background(), "edit_file", map[string]any{
				"file_path": "/work/main.go", "old_string": "old", "new_string": "new",
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatalf("edit_file did not recover the path: %s", res.Text())
			}
			if !strings.Contains(res.Text(), "/work/main.go") || !strings.Contains(res.Text(), real) {
				t.Errorf("edit_file corrected the path without naming both: %q", res.Text())
			}
			got, err := os.ReadFile(real)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "func new() {}\n" {
				t.Errorf("the file under the workspace reads %q, want the edit applied to it", got)
			}
		})
	}
}

// A write has no failed read to trigger the retry off, so it asks about
// the directory instead: one that is already there under the workspace
// is where the file goes.
func TestWriteFileRecoversAnAbsolutePathIntoAnExistingDirectory(t *testing.T) {
	requireNoHostWork(t)
	for _, tc := range []struct {
		name   string
		client func(*testing.T, string) *Client
	}{
		{"local", newTestClient},
		{"ssh", newSSHTestClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.MkdirAll(filepath.Join(workspace, "work", "pkg"), 0o755); err != nil {
				t.Fatal(err)
			}

			res, err := tc.client(t, workspace).CallTool(context.Background(), "write_file", map[string]any{
				"file_path": "/work/pkg/new.go", "content": "package new\n",
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatalf("write_file did not recover the path: %s", res.Text())
			}
			real := filepath.Join(workspace, "work", "pkg", "new.go")
			got, err := os.ReadFile(real)
			if err != nil {
				t.Fatalf("nothing was written under the workspace: %v", err)
			}
			if string(got) != "package new\n" {
				t.Errorf("wrote %q, want the content the call carried", got)
			}
			// The answer reports the path it really wrote, not the one
			// the notice above it has just called impossible.
			if !strings.Contains(res.Text(), "Wrote "+real) {
				t.Errorf("write_file does not report the path it wrote: %q", res.Text())
			}
		})
	}
}

// The retry is a recovery, not a redirection service: an absolute path
// whose directory does not exist under the workspace either is a path
// nobody here can make sense of, and inventing a tree for it under the
// sandbox would turn one wrong path into a file the run can never find
// again.
func TestWriteFileDoesNotInventADirectoryUnderTheWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client func(*testing.T, string) *Client
	}{
		{"local", newTestClient},
		{"ssh", newSSHTestClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			// Under /proc deliberately: the write has to fail on the
			// path as given for the retry to be reached at all, and
			// /proc refuses a mkdir even to root -- so this test cannot
			// leave a directory behind on the machine running it,
			// whichever account that is.
			res, err := tc.client(t, workspace).CallTool(context.Background(), "write_file", map[string]any{
				"file_path": "/proc/grain-not-a-real-dir/x.txt", "content": "x\n",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Errorf("write_file to a path nothing can make sense of reported success: %s", res.Text())
			}
			if _, err := os.Stat(filepath.Join(workspace, "proc")); err == nil {
				t.Errorf("write_file built %s under the workspace for a path nobody asked to create there",
					filepath.Join(workspace, "proc"))
			}
		})
	}
}

// A path that is nowhere at all comes back as the error it always did,
// naming the path the caller asked about -- not a second path it never
// mentioned, and not a paragraph of explanation for a mistyped filename.
func TestReadFileReportsTheAskedPathWhenNothingRecoversIt(t *testing.T) {
	requireNoHostWork(t)
	for _, tc := range []struct {
		name   string
		client func(*testing.T, string) *Client
	}{
		{"local", newTestClient},
		{"ssh", newSSHTestClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			res, err := tc.client(t, workspace).CallTool(context.Background(), "read_file",
				map[string]any{"file_path": "/work/pkg/task.go"})
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Fatalf("read_file of a path that exists nowhere reported success: %s", res.Text())
			}
			if !strings.Contains(res.Text(), "/work/pkg/task.go") {
				t.Errorf("the error does not name the path that was asked for: %q", res.Text())
			}
			if strings.Contains(res.Text(), "[grain]") {
				t.Errorf("a failed read claims a path was recovered: %q", res.Text())
			}
		})
	}
}

// An ordinary relative call is untouched by any of this: it already
// lands under the workspace, and nothing about it is a guess.
func TestRelativePathsAreNeverRewritten(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client func(*testing.T, string) *Client
	}{
		{"local", newTestClient},
		{"ssh", newSSHTestClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("hello\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := tc.client(t, workspace).CallTool(context.Background(), "read_file",
				map[string]any{"file_path": "notes.md"})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError || !strings.Contains(res.Text(), "hello") {
				t.Fatalf("read_file of a relative path: %s", res.Text())
			}
			if strings.Contains(res.Text(), "[grain]") {
				t.Errorf("an ordinary read carries a correction notice: %q", res.Text())
			}
		})
	}
}

func TestWorkspaceRetryOnlyAnswersForAPathThatCouldHaveMeantSomethingElse(t *testing.T) {
	for _, tc := range []struct {
		name, workspace, path string
		want                  string // "" for no retry at all
	}{
		{"absolute outside the workspace", "/home/debian", "/work/pkg/x.go", "/home/debian/work/pkg/x.go"},
		{"relative", "/home/debian", "work/pkg/x.go", ""},
		{"already inside the workspace", "/home/debian", "/home/debian/work/x.go", ""},
		{"the workspace itself", "/home/debian", "/home/debian", ""},
		{"no workspace known", "", "/work/x.go", ""},
		{"a workspace of /", "/", "/work/x.go", ""},
		{"a relative workspace", "sandbox", "/work/x.go", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := workspaceRetry(tc.workspace, tc.path)
			if tc.want == "" {
				if ok {
					t.Errorf("workspaceRetry(%q, %q) = %q, want no retry", tc.workspace, tc.path, got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Errorf("workspaceRetry(%q, %q) = %q, %v, want %q", tc.workspace, tc.path, got, ok, tc.want)
			}
		})
	}
}
