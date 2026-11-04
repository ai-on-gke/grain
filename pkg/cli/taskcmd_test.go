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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// externalCLIServer is a daemon's UI/API over a real embedded store,
// and a CLI client presenting its access code: what the external-cli
// verbs' tests drive.
func externalCLIServer(t *testing.T) (*ui.Client, *ui.HTTPClient) {
	t.Helper()
	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	client := ui.NewClient(ui.Config{SourceDB: db, Actor: ui.DefaultActor("alice")}, store)
	ts := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(ts.Close)
	return client, accessCodeClient(ts.URL)
}

// #XC-19: grain create reads its body from -body-file (stdin for -),
// refuses -body beside it with exit 2, and records -design on the
// task's provenance.
func TestCreateBodyFileStdinConflictAndDesignLink(t *testing.T) {
	_, c := externalCLIServer(t)
	ctx := context.Background()
	old := cliStdin
	t.Cleanup(func() { cliStdin = old })
	cliStdin = strings.NewReader("# From stdin\n\nThe whole body.\n")

	out := &printer{json: true}
	captureStdout(t, func() {
		if err := cmdCreate(ctx, c, out, []string{"-title", "piped", "-no-repo", "-body-file", "-",
			"-design", "design/spec/external-cli/external-cli.spec.md"}); err != nil {
			t.Fatal(err)
		}
	})
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("filed %d tasks, want 1", len(tasks))
	}
	got := tasks[0]
	if got.Description != "# From stdin\n\nThe whole body.\n" {
		t.Errorf("description = %q, want stdin's", got.Description)
	}
	if len(got.Designs) != 1 || got.Designs[0] != "design/spec/external-cli/external-cli.spec.md" {
		t.Errorf("designs = %v, want the -design path on the task's provenance", got.Designs)
	}

	err = cmdCreate(ctx, c, out, []string{"-title", "both", "-no-repo", "-body", "x", "-body-file", "-"})
	if exit, _, _ := classifyError(err); err == nil || exit != exitUsage {
		t.Errorf("-body with -body-file: %v (exit %d), want a usage error, exit 2", err, exit)
	}
	if tasks, _ := c.ListTasks(ctx); len(tasks) != 1 {
		t.Errorf("a refused create still filed a task")
	}
}
