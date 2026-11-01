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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

type recordingProposer struct {
	got []ConfigChangeProposal
}

func (r *recordingProposer) ProposeConfigChange(_ context.Context, p ConfigChangeProposal) (*model.PendingConfigChange, error) {
	r.got = append(r.got, p)
	return &model.PendingConfigChange{Branch: p.Branch, HeadSHA: p.HeadSHA, Files: []string{"settings/x.yml"},
		Validation: model.ConfigChangeValidation{OK: true}}, nil
}

func proposalGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestProposeConfigChangeCommitsAndPushesTheProposalBranch(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	remote := filepath.Join(t.TempDir(), "config.git")
	proposalGit(t, root, "init", "--quiet", "--bare", "-b", "main", remote)
	seed := t.TempDir()
	proposalGit(t, seed, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "x.yml"), []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proposalGit(t, seed, "add", "-A")
	proposalGit(t, seed, "commit", "--quiet", "-m", "seed")
	proposalGit(t, seed, "push", "--quiet", remote, "main")
	proposalGit(t, root, "clone", "--quiet", remote, ConfigStateDir)
	state := filepath.Join(root, ConfigStateDir)
	proposalGit(t, state, "config", "user.name", "grain director")
	proposalGit(t, state, "config", "user.email", "grain@localhost")

	proposer := &recordingProposer{}
	tool := NewProposeConfigChangeTools(proposer, "42", NewSandboxTools(root))[0]

	// Nothing edited: nothing to propose.
	if res := tool.Handler(context.Background(), map[string]any{"summary": "Nothing"}); !res.IsError {
		t.Fatalf("an unchanged checkout was proposed: %s", res.Text)
	}

	if err := os.WriteFile(filepath.Join(state, "x.yml"), []byte("a: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := tool.Handler(context.Background(), map[string]any{"summary": "Raise a to 2", "description": "Because."})
	if res.IsError {
		t.Fatalf("propose_config_change: %s", res.Text)
	}
	if len(proposer.got) != 1 {
		t.Fatalf("proposals = %+v", proposer.got)
	}
	got := proposer.got[0]
	if got.Branch != "grain/config/42/raise-a-to-2" {
		t.Fatalf("branch = %q", got.Branch)
	}
	if pushed := proposalGit(t, remote, "rev-parse", "refs/heads/"+got.Branch); pushed != got.HeadSHA {
		t.Fatalf("remote has %s, proposal names %s", pushed, got.HeadSHA)
	}
	if main := proposalGit(t, remote, "show", "main:x.yml"); main != "a: 1" {
		t.Fatalf("main moved: %q", main)
	}
	if msg := proposalGit(t, remote, "log", "-1", "--format=%B", got.HeadSHA); !strings.Contains(msg, "Raise a to 2") || !strings.Contains(msg, "Because.") {
		t.Fatalf("commit message = %q", msg)
	}
}

func TestConfigBranchSlug(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Raise max_parallel to 4!": "raise-max-parallel-to-4",
		"  ../../main  ":           "main",
		"":                         "",
	} {
		if got := configBranchSlug(in); got != want {
			t.Errorf("configBranchSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
