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

package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStageRootDefaultsToItsOwnDirectory: with no StageRoot, staging is
// confined to a directory of its own under ~/.grain, never ~/.grain
// itself, which on a workstation is the local daemon's data directory.
func TestStageRootDefaultsToItsOwnDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".grain"), 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(home, ".grain", "grain.db")
	if err := os.WriteFile(db, []byte("secrets"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	root := s.stageRoot()
	if want := filepath.Join(home, ".grain", "hostd-stage"); root != want {
		t.Fatalf("stageRoot() = %q, want %q", root, want)
	}
	for _, op := range []StageOp{
		{Op: StageReadTail, Path: db},
		{Op: StageReadTail, Path: "~/.grain/grain.db"},
		{Op: StageWriteFile, Path: db, Data: []byte("x")},
		{Op: StageRemoveAll, Path: filepath.Join(home, ".grain")},
	} {
		if r := s.stage(&op); r.Error == "" {
			t.Errorf("stage(%s %s) succeeded outside the staging root", op.Op, op.Path)
		}
	}
	if data, err := os.ReadFile(db); err != nil || string(data) != "secrets" {
		t.Fatalf("the daemon's data directory was touched: %q, %v", data, err)
	}
}

// TestStageRefusesToFollowASymlinkOutOfTheRoot: a link inside the root
// cannot be named as the directory a create or a prune works in.
func TestStageRefusesToFollowASymlinkOutOfTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	s := &Server{StageRoot: root}
	for _, op := range []StageOp{
		{Op: StagePrune, Path: link, MaxAge: 1},
		{Op: StageCreateTemp, Dir: link, Pattern: "x-"},
		{Op: StageWriteFile, Path: filepath.Join(link, "f"), Data: []byte("x")},
		{Op: StageReadTail, Path: link},
	} {
		if r := s.stage(&op); r.Error == "" {
			t.Errorf("stage(%s) through %s succeeded", op.Op, link)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("prune followed the link: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatalf("something was created outside the root: %v", entries)
	}
	// The temp area itself planted as a link out of the root.
	if err := os.Symlink(outside, filepath.Join(root, "tmp")); err != nil {
		t.Fatal(err)
	}
	if r := s.stage(&StageOp{Op: StageMkdirTemp, Pattern: "x-"}); r.Error == "" {
		t.Errorf("stage(%s) through the linked temp area succeeded: %s", StageMkdirTemp, r.Path)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatalf("a temp directory was created outside the root: %v", entries)
	}
}

// TestStageSymlinkTakesOnlyTheCodexLogin: the one link a run stages is
// to this machine's codex login, and no other target is linked in.
func TestStageSymlinkTakesOnlyTheCodexLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	root := t.TempDir()
	s := &Server{StageRoot: root}
	op := StageOp{Op: StageSymlink, Path: filepath.Join(root, "auth.json"), Target: filepath.Join(home, ".codex", "auth.json")}
	if r := s.stage(&op); r.Error == "" {
		t.Fatal("linked a codex login this machine does not have")
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(op.Target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := s.stage(&op); r.Error != "" {
		t.Fatalf("linking the codex login: %s", r.Error)
	}
	for _, target := range []string{"/etc/passwd", filepath.Join(home, ".ssh"), "~/.ssh/id_ed25519"} {
		other := StageOp{Op: StageSymlink, Path: filepath.Join(root, "other"), Target: target}
		if r := s.stage(&other); r.Error == "" || !strings.Contains(r.Error, "codex login") {
			t.Errorf("stage(symlink -> %s) = %+v, want a refusal", target, r)
		}
	}
}
