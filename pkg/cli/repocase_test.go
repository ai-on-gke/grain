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

// `grain repo-case` from an operator's side: the command reads this
// deployment's own spelling of a repo out of its stored settings, says
// what is stored some other way, and changes nothing until asked.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
)

// divergentStore is a deployment whose allowlist says acme/widgets and
// whose database holds a task filed against ACME/Widgets -- a task filed
// before ui.parseRepo learned to spell a repo the deployment's way.
func divergentStore(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	ctx := context.Background()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer db.Close()
	if err := store.SetTargetRepos(ctx, []string{"acme/widgets"}); err != nil {
		t.Fatalf("writing settings: %v", err)
	}
	created := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{
		ID: "a1b2", Intent: model.IntentImplement, Title: "fix the thing",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
			Reason:      model.ReasonDirect,
		},
		Target:    &model.RepoRef{Owner: "ACME", Name: "Widgets"},
		Binding:   model.BindingDirective,
		CreatedAt: &created,
	}); err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	return dataDir
}

func storedTarget(t *testing.T, dataDir string) string {
	t.Helper()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task, err := store.GetTask(context.Background(), "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if task.Target == nil {
		t.Fatal("the task has no target")
	}
	return task.Target.String()
}

func TestRepoCaseReportsWithoutChangingAnything(t *testing.T) {
	t.Parallel()
	dataDir := divergentStore(t)

	var out bytes.Buffer
	if err := runRepoCase([]string{"-data-dir", dataDir}, &out); err != nil {
		t.Fatalf("reporting: %v", err)
	}
	if !strings.Contains(out.String(), "ACME/Widgets -> acme/widgets") {
		t.Fatalf("output does not name the rewrite it would make:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "-apply") {
		t.Fatalf("output does not say how to make the change:\n%s", out.String())
	}
	if got := storedTarget(t, dataDir); got != "ACME/Widgets" {
		t.Fatalf("target = %q, want it untouched: a report writes nothing", got)
	}
}

func TestRepoCaseApplyRewritesAndIsThenDone(t *testing.T) {
	t.Parallel()
	dataDir := divergentStore(t)

	var out bytes.Buffer
	if err := runRepoCase([]string{"-data-dir", dataDir, "-apply"}, &out); err != nil {
		t.Fatalf("applying: %v", err)
	}
	if got := storedTarget(t, dataDir); got != "acme/widgets" {
		t.Fatalf("target = %q, want acme/widgets", got)
	}

	out.Reset()
	if err := runRepoCase([]string{"-data-dir", dataDir}, &out); err != nil {
		t.Fatalf("reporting again: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Fatalf("a second report still finds something:\n%s", out.String())
	}
}

// The default target is a daemon startup flag rather than stored
// settings, so a deployment that runs with one has to say so here --
// and when it does, it is a canonical spelling like any other.
func TestRepoCaseTakesTheDefaultTargetFromItsFlag(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	ctx := context.Background()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: model.RepoRef{Owner: "ACME", Name: "Widgets"}, DefaultCapabilities: []string{"gcp-key"},
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var out bytes.Buffer
	if err := runRepoCase([]string{"-data-dir", dataDir}, &out); err == nil {
		t.Fatalf("a deployment naming no repo at all reported anyway:\n%s", out.String())
	}

	out.Reset()
	if err := runRepoCase([]string{"-data-dir", dataDir, "-default-target", "acme/widgets", "-apply"}, &out); err != nil {
		t.Fatalf("applying against the default target: %v", err)
	}
	store, db, err = daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg, err := store.GetRepoConfig(ctx, model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("the repo_config row did not move to the default target's own spelling")
	}
}

// A conflict is reported, refused and non-zero: the operator resolves it
// themselves rather than having grain pick one of their two answers.
func TestRepoCaseRefusesWhatItCannotMerge(t *testing.T) {
	t.Parallel()
	dataDir := divergentStore(t)
	ctx := context.Background()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []model.RepoConfig{
		{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, SetupCommand: "make deps"},
		{Repo: model.RepoRef{Owner: "ACME", Name: "Widgets"}, SetupCommand: "npm ci"},
	} {
		if err := store.PutRepoConfig(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	var out bytes.Buffer
	if err := runRepoCase([]string{"-data-dir", dataDir, "-apply"}, &out); err == nil {
		t.Fatalf("applying over a conflict succeeded:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "setup_command") {
		t.Fatalf("output does not say what disagrees:\n%s", out.String())
	}
	if got := storedTarget(t, dataDir); got != "ACME/Widgets" {
		t.Fatalf("target = %q, want it untouched: a refused run writes nothing at all", got)
	}
}
