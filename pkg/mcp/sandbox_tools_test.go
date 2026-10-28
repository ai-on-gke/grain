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
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/procgroup"
)

func TestSandboxToolsRejectSymlinksPointingOutsideWorkspace(t *testing.T) {
	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideSecret, []byte("super-secret-outside-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideSub := filepath.Join(outsideDir, "outside_sub")
	if err := os.MkdirAll(outsideSub, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideSubSecret := filepath.Join(outsideSub, "nested.txt")
	if err := os.WriteFile(outsideSubSecret, []byte("nested-secret-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	// 1. Absolute symlink to outside file.
	symlinkFile := filepath.Join(root, "symlink_secret.txt")
	if err := os.Symlink(outsideSecret, symlinkFile); err != nil {
		t.Fatal(err)
	}

	// 2. Relative symlink to outside file.
	relSymlinkFile := filepath.Join(root, "rel_symlink_secret.txt")
	relTarget, err := filepath.Rel(root, outsideSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relTarget, relSymlinkFile); err != nil {
		t.Fatal(err)
	}

	// 3. Symlink to outside directory.
	symlinkDir := filepath.Join(root, "outside_dir_link")
	if err := os.Symlink(outsideDir, symlinkDir); err != nil {
		t.Fatal(err)
	}

	// 4. Broken symlink to outside non-existent file.
	brokenSymlinkFile := filepath.Join(root, "broken_symlink.txt")
	if err := os.Symlink(filepath.Join(outsideDir, "nonexistent.txt"), brokenSymlinkFile); err != nil {
		t.Fatal(err)
	}

	// 5. Broken symlink to outside non-existent directory.
	brokenSymlinkDir := filepath.Join(root, "broken_dir_link")
	if err := os.Symlink(filepath.Join(outsideDir, "nonexistent_dir"), brokenSymlinkDir); err != nil {
		t.Fatal(err)
	}

	// 6. Chain of symlinks leading outside.
	chainSymlink := filepath.Join(root, "chain_link.txt")
	if err := os.Symlink("symlink_secret.txt", chainSymlink); err != nil {
		t.Fatal(err)
	}

	// 7. Legitimate internal symlink to file.
	internalFile := filepath.Join(root, "internal_real.txt")
	if err := os.WriteFile(internalFile, []byte("hello internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	internalSymlinkFile := filepath.Join(root, "internal_link.txt")
	if err := os.Symlink("internal_real.txt", internalSymlinkFile); err != nil {
		t.Fatal(err)
	}

	// 8. Legitimate internal symlink to directory.
	internalDir := filepath.Join(root, "internal_dir")
	if err := os.MkdirAll(internalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	internalSubFile := filepath.Join(internalDir, "sub.txt")
	if err := os.WriteFile(internalSubFile, []byte("hello internal sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	internalSymlinkDir := filepath.Join(root, "internal_dir_link")
	if err := os.Symlink("internal_dir", internalSymlinkDir); err != nil {
		t.Fatal(err)
	}

	t.Run("read_file rejects symlinks escaping workspace", func(t *testing.T) {
		escapePaths := []string{
			"symlink_secret.txt",
			"rel_symlink_secret.txt",
			"outside_dir_link/secret.txt",
			"outside_dir_link/outside_sub/nested.txt",
			"chain_link.txt",
			"broken_symlink.txt",
			"broken_dir_link/anything.txt",
		}
		for _, p := range escapePaths {
			res, err := client.CallTool(ctx, "read_file", map[string]any{"file_path": p})
			if err != nil {
				t.Fatalf("unexpected call error on read_file(%q): %v", p, err)
			}
			if !res.IsError {
				t.Errorf("read_file(%q): expected error rejecting symlink escape, got success: %s", p, res.Text())
			}
			if strings.Contains(res.Text(), "super-secret-outside-content") || strings.Contains(res.Text(), "nested-secret-content") {
				t.Errorf("read_file(%q): secret leaked in output: %s", p, res.Text())
			}
		}

		// Valid internal symlinks should succeed.
		res, err := client.CallTool(ctx, "read_file", map[string]any{"file_path": "internal_link.txt"})
		if err != nil || res.IsError {
			t.Fatalf("read_file on internal symlink failed: %v, text: %s", err, res.Text())
		}
		if !strings.Contains(res.Text(), "hello internal") {
			t.Errorf("read_file on internal symlink did not return content: %s", res.Text())
		}

		res, err = client.CallTool(ctx, "read_file", map[string]any{"file_path": "internal_dir_link/sub.txt"})
		if err != nil || res.IsError {
			t.Fatalf("read_file on internal dir symlink failed: %v, text: %s", err, res.Text())
		}
		if !strings.Contains(res.Text(), "hello internal sub") {
			t.Errorf("read_file on internal dir symlink did not return content: %s", res.Text())
		}
	})

	t.Run("write_file rejects symlinks escaping workspace", func(t *testing.T) {
		escapePaths := []string{
			"symlink_secret.txt",
			"rel_symlink_secret.txt",
			"outside_dir_link/secret.txt",
			"outside_dir_link/new_file.txt",
			"outside_dir_link/sub/new_file.txt",
			"chain_link.txt",
			"broken_symlink.txt",
			"broken_dir_link/new_file.txt",
		}
		for _, p := range escapePaths {
			res, err := client.CallTool(ctx, "write_file", map[string]any{
				"file_path": p,
				"content":   "overwritten-payload\n",
			})
			if err != nil {
				t.Fatalf("unexpected call error on write_file(%q): %v", p, err)
			}
			if !res.IsError {
				t.Errorf("write_file(%q): expected error rejecting symlink escape, got success: %s", p, res.Text())
			}
		}

		// Verify outside files were NOT modified or created.
		secretData, err := os.ReadFile(outsideSecret)
		if err != nil || string(secretData) != "super-secret-outside-content\n" {
			t.Fatalf("outside secret was modified: %v, content: %q", err, string(secretData))
		}
		if _, err := os.Stat(filepath.Join(outsideDir, "new_file.txt")); err == nil {
			t.Fatalf("write_file created new_file.txt in outside directory")
		}
		if _, err := os.Stat(filepath.Join(outsideDir, "nonexistent.txt")); err == nil {
			t.Fatalf("write_file created nonexistent.txt via broken symlink")
		}

		// Valid internal symlinks should succeed.
		res, err := client.CallTool(ctx, "write_file", map[string]any{
			"file_path": "internal_link.txt",
			"content":   "updated internal\n",
		})
		if err != nil || res.IsError {
			t.Fatalf("write_file on internal symlink failed: %v, text: %s", err, res.Text())
		}
		updated, err := os.ReadFile(internalFile)
		if err != nil || string(updated) != "updated internal\n" {
			t.Errorf("internal file was not updated: %v, content: %q", err, string(updated))
		}

		res, err = client.CallTool(ctx, "write_file", map[string]any{
			"file_path": "internal_dir_link/new_internal.txt",
			"content":   "created via internal dir symlink\n",
		})
		if err != nil || res.IsError {
			t.Fatalf("write_file on internal dir symlink failed: %v, text: %s", err, res.Text())
		}
		created, err := os.ReadFile(filepath.Join(internalDir, "new_internal.txt"))
		if err != nil || string(created) != "created via internal dir symlink\n" {
			t.Errorf("file created via internal dir symlink incorrect: %v, content: %q", err, string(created))
		}
	})

	t.Run("edit_file rejects symlinks escaping workspace", func(t *testing.T) {
		escapePaths := []string{
			"symlink_secret.txt",
			"rel_symlink_secret.txt",
			"outside_dir_link/secret.txt",
			"outside_dir_link/outside_sub/nested.txt",
			"chain_link.txt",
			"broken_symlink.txt",
			"broken_dir_link/anything.txt",
		}
		for _, p := range escapePaths {
			res, err := client.CallTool(ctx, "edit_file", map[string]any{
				"file_path":  p,
				"old_string": "secret",
				"new_string": "pwned",
			})
			if err != nil {
				t.Fatalf("unexpected call error on edit_file(%q): %v", p, err)
			}
			if !res.IsError {
				t.Errorf("edit_file(%q): expected error rejecting symlink escape, got success: %s", p, res.Text())
			}
		}

		// Verify outside file was NOT edited.
		secretData, err := os.ReadFile(outsideSecret)
		if err != nil || string(secretData) != "super-secret-outside-content\n" {
			t.Fatalf("outside secret was modified by edit_file: %v, content: %q", err, string(secretData))
		}

		// Valid internal symlink should succeed.
		res, err := client.CallTool(ctx, "edit_file", map[string]any{
			"file_path":  "internal_link.txt",
			"old_string": "updated",
			"new_string": "edited",
		})
		if err != nil || res.IsError {
			t.Fatalf("edit_file on internal symlink failed: %v, text: %s", err, res.Text())
		}
		edited, err := os.ReadFile(internalFile)
		if err != nil || string(edited) != "edited internal\n" {
			t.Errorf("internal file was not edited: %v, content: %q", err, string(edited))
		}
	})
}

// resolvePath opens the path safepath.Within resolved, but what a tool
// says back names the path as the caller spelled it: a workspace reached
// through a symlink must not have its retry notice or "Wrote" line
// rewritten into the directory the link points at.
func TestSandboxToolsNameThePathAskedForNotTheResolvedOne(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "link")
	if err := os.Symlink(real, root); err != nil {
		t.Fatal(err)
	}
	tools := map[string]Tool{}
	for _, tool := range NewSandboxTools(root) {
		tools[tool.Name] = tool
	}
	ctx := context.Background()

	res := tools["write_file"].Handler(ctx, map[string]any{"file_path": "/sub/a.txt", "content": "hi"})
	if res.IsError {
		t.Fatalf("write_file(/sub/a.txt) failed: %s", res.Text)
	}
	want := filepath.Join(root, "sub", "a.txt")
	if !strings.Contains(res.Text, want+" was used instead") || !strings.HasSuffix(res.Text, "Wrote "+want) {
		t.Errorf("write_file(/sub/a.txt) should name %s: %s", want, res.Text)
	}
	if strings.Contains(res.Text, real+string(filepath.Separator)) {
		t.Errorf("write_file(/sub/a.txt) named the resolved path: %s", res.Text)
	}
	if got, err := os.ReadFile(filepath.Join(real, "sub", "a.txt")); err != nil || string(got) != "hi" {
		t.Fatalf("file not written under the workspace: %q, %v", got, err)
	}
	res = tools["read_file"].Handler(ctx, map[string]any{"file_path": "/sub/a.txt"})
	if res.IsError || !strings.Contains(res.Text, want+" was used instead") || !strings.Contains(res.Text, "hi") {
		t.Errorf("read_file(/sub/a.txt): %s", res.Text)
	}

	// A dangling link is refused even when its target would be inside
	// the workspace: Within cannot vouch for where it would write.
	if err := os.Symlink(filepath.Join(real, "missing.txt"), filepath.Join(real, "dangling")); err != nil {
		t.Fatal(err)
	}
	if res := tools["write_file"].Handler(ctx, map[string]any{"file_path": "dangling", "content": "x"}); !res.IsError {
		t.Errorf("write_file through a dangling link was allowed: %s", res.Text)
	}
}

func TestRunCommandResetsChildOOMScoreAdjToPositive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("oom_score_adj is Linux-specific")
	}
	root := t.TempDir()
	var runCmd Tool
	for _, tool := range NewHostSandboxTools(root, nil) {
		if tool.Name == "run_command" {
			runCmd = tool
			break
		}
	}
	res := runCmd.Handler(context.Background(), map[string]any{
		"command": "sleep 0.05 && cat /proc/self/oom_score_adj",
	})
	if res.IsError {
		t.Fatalf("run_command failed: %s", res.Text)
	}
	want := strconv.Itoa(procgroup.ChildOOMScoreAdj)
	if !strings.Contains(res.Text, "\n"+want+"\n") {
		t.Errorf("run_command output = %q, want oom_score_adj %q", res.Text, want)
	}
}
