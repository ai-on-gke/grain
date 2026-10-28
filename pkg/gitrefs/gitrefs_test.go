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

package gitrefs_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitrefs"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestFormatReadsTheConfigOfEveryRepositoryShape(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	work := filepath.Join(root, "work")
	git(t, root, "init", "-q", work)
	git(t, work, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "--allow-empty", "-m", "x")
	bare := filepath.Join(root, "bare.git")
	git(t, root, "init", "-q", "--bare", bare)
	linked := filepath.Join(root, "linked")
	git(t, work, "-c", "user.name=a", "-c", "user.email=a@b", "worktree", "add", "-q", linked)
	plain := filepath.Join(root, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{work, bare, linked, plain} {
		if got, err := gitrefs.Format(ctx, dir); err != nil || got != "" {
			t.Errorf("Format(%s) = %q, %v; want \"\", nil", dir, got, err)
		}
	}
	git(t, work, "config", "extensions.refStorage", "Reftable")
	git(t, bare, "config", "extensions.refstorage", "reftable")
	for _, dir := range []string{work, bare, linked} {
		if got, err := gitrefs.Format(ctx, dir); err != nil || got != "reftable" {
			t.Errorf("Format(%s) = %q, %v; want reftable", dir, got, err)
		}
	}
}

func TestCheckLeavesAFilesRepositoryAlone(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if err := gitrefs.Check(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "config", "extensions.refstorage", "files")
	if err := gitrefs.Check(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if err := gitrefs.Check(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("a directory with no repository: %v", err)
	}
}

func TestTheConversionCommandIsQuotedForAShell(t *testing.T) {
	err := &gitrefs.UnreadableError{Dir: "/var/lib/grain/it's here", Format: "reftable"}
	want := `git -C '/var/lib/grain/it'\''s here' refs migrate --ref-format=files`
	if msg := err.Error(); !strings.Contains(msg, want) {
		t.Fatalf("the message does not offer %q:\n%s", want, msg)
	}
}
