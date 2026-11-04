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

package cli

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type recordingCIRunner struct {
	ci.NopRunner
	started []ci.Request
}

func (r *recordingCIRunner) Start(_ context.Context, req ci.Request) (ci.Run, error) {
	r.started = append(r.started, req)
	return ci.Run{ID: "run-rollback-1", Repo: req.Repo, Ref: req.Ref, Event: req.Event}, nil
}

func TestGrainReleaseRollbackCLI(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"ai-on-gke/grain", "example/widgets"}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []model.RepoRef{model.GrainSourceRepo(), {Owner: "example", Name: "widgets"}} {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
			t.Fatal(err)
		}
	}

	runner := &recordingCIRunner{}
	client := ui.NewClient(ui.Config{
		Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"},
		CI:    runner,
	}, store)
	ts := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(ts.Close)

	hc := accessCodeClient(ts.URL)
	var out printer

	if err := cmdRelease(ctx, hc, &out, []string{"rollback", "v2.4.0"}); err != nil {
		t.Fatalf("grain release rollback v2.4.0: %v", err)
	}
	if len(runner.started) != 1 {
		t.Fatalf("started %d runs, want 1", len(runner.started))
	}
	got := runner.started[0]
	if got.Repo != "ai-on-gke/grain" || got.ReleaseVersion != "v2.4.0" || got.RequestedByKind != "human" ||
		len(got.Jobs) != 1 || got.Jobs[0] != ui.PromoteReleaseWorkflow+":rollback" ||
		got.ReleaseRollbackTo != "" || got.ReleaseRollbackReason != "" {
		t.Errorf("plain rollback started %+v", got)
	}

	if err := cmdRelease(ctx, hc, &out, []string{"rollback", "v2.4.0", "--to", "v2.3.0", "--reason", "bad migration", "--repo", "example/widgets"}); err != nil {
		t.Fatalf("grain release rollback with flags: %v", err)
	}
	got = runner.started[1]
	if got.Repo != "example/widgets" || got.ReleaseVersion != "v2.4.0" ||
		got.ReleaseRollbackTo != "v2.3.0" || got.ReleaseRollbackReason != "bad migration" {
		t.Errorf("rollback with flags started %+v", got)
	}

	if err := cmdRelease(ctx, hc, &out, []string{"rollback", "--bad", "v2.5.0", "--to", "v2.4.0"}); err != nil {
		t.Fatalf("grain release rollback --bad: %v", err)
	}
	if runner.started[2].ReleaseVersion != "v2.5.0" || runner.started[2].ReleaseRollbackTo != "v2.4.0" {
		t.Errorf("rollback --bad started %+v", runner.started[2])
	}

	for _, bad := range [][]string{
		{"rollback"},
		{"rollback", "v2.4.0", "v2.3.0"},
		{"rollback", "--bad", "v2.4.0", "v2.4.0"},
		{"rollback", "-short-soak-ok", "v2.4.0"},
		{"rollback", "-allow-breaking", "v2.4.0"},
		{"approve", "-to", "v2.3.0", "v2.4.0"},
		{"approve", "-reason", "why", "v2.4.0"},
		{"reject", "-bad", "v2.4.0", "v2.4.0"},
	} {
		if err := cmdRelease(ctx, hc, &out, bad); err == nil {
			t.Errorf("cmdRelease(%v) succeeded; want error", bad)
		}
	}
	if len(runner.started) != 3 {
		t.Errorf("started %d runs after bad CLI invocations, want 3", len(runner.started))
	}

	// An agent calling the same server is refused even over the CLI.
	agentClient := ui.NewClient(ui.Config{
		Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run-42"},
		CI:    runner,
	}, store)
	agentTS := httptest.NewServer(accessCodeServer(agentClient))
	t.Cleanup(agentTS.Close)
	if err := cmdRelease(ctx, accessCodeClient(agentTS.URL), &out, []string{"rollback", "v2.4.0"}); err == nil ||
		!strings.Contains(err.Error(), "only a person may roll back") {
		t.Errorf("agent CLI rollback: %v, want refused", err)
	}
}
