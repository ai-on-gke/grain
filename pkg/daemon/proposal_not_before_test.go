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

package daemon

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A proposal filed deferred (propose_task's not_before) reads "deferred"
// whether or not it is approved, so the live filer has to tell the agent
// which from its approval. It used to read the state alone, and told the
// agent that a proposal still waiting for a human was "queued already
// ... without anybody approving it" (docs/test-runs/
// 2026-10-02-merge-batch-not-before.md, finding 2).
func TestLiveProposalWithNotBeforeSaysWhetherItStillNeedsAHuman(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	srv := httptest.NewServer(ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator"),
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: testAccessCode,
	}, store))
	t.Cleanup(srv.Close)
	client := accessCodeClient(srv.URL)
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02")

	for _, tc := range []struct {
		name      string
		autoMerge bool
		want      string
		approved  bool
	}{
		{"an auto-merge parent's proposal is filed approved", true, "filed approved", true},
		{"any other parent's proposal waits for a human", false, "filed unapproved because your permission does not cover it", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "parent", Description: "files a proposal for later",
				Repo: "acme/widgets", AutoMerge: tc.autoMerge, Approved: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			tool := mcp.NewLiveProposalTools(&mcp.MockSink{}, nil, parent.ID,
				&daemonProposalFiler{client: client, parent: parent.ID, baseURL: srv.URL, filed: map[string]string{}})[0]
			res := tool.Handler(ctx, map[string]any{
				"title": "later", "body": "not before tomorrow", "not_before": tomorrow,
			})
			if res.IsError {
				t.Fatalf("propose_task refused: %s", res.Text)
			}
			if !strings.Contains(res.Text, tc.want) || !strings.Contains(res.Text, "filed deferred") {
				t.Fatalf("propose_task answered %q, want it to say %q and that it is filed deferred", res.Text, tc.want)
			}
			tasks, err := client.ListTasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, task := range tasks {
				if task.Title == "later" && task.GeneratedFrom == parent.ID {
					if task.State != model.StateDeferred || task.Approved != tc.approved {
						t.Fatalf("the proposal is %s, approved %v; want deferred, approved %v",
							task.State, task.Approved, tc.approved)
					}
					return
				}
			}
			t.Fatal("the proposal was not filed")
		})
	}
}

func TestDaemonProposalFilerEmitsRelativeTaskLink(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	srv := httptest.NewServer(ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator"),
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: testAccessCode,
	}, store))
	t.Cleanup(srv.Close)
	client := accessCodeClient(srv.URL)

	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "parent", Description: "files a proposal",
		Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	filer := &daemonProposalFiler{client: client, parent: parent.ID, baseURL: srv.URL, filed: map[string]string{}}
	filed, err := filer.FileProposal(ctx, mcp.ProposedTask{Title: "follow-up", Body: "details"}, "proposal:1")
	if err != nil {
		t.Fatalf("FileProposal: %v", err)
	}
	wantURL := "/tasks/" + filed.ID
	if filed.URL != wantURL {
		t.Fatalf("filed.URL = %q, want relative path %q (not loopback %q)", filed.URL, wantURL, srv.URL)
	}
	tool := mcp.NewLiveProposalTools(&mcp.MockSink{}, nil, parent.ID, filer)[0]
	res := tool.Handler(ctx, map[string]any{"title": "second follow-up", "body": "details"})
	if res.IsError {
		t.Fatalf("propose_task refused: %s", res.Text)
	}
	if strings.Contains(res.Text, srv.URL) || strings.Contains(res.Text, "127.0.0.1") || strings.Contains(res.Text, "localhost") {
		t.Fatalf("propose_task output contains loopback URL: %q", res.Text)
	}
	if !strings.Contains(res.Text, "is visible now: /tasks/") {
		t.Fatalf("propose_task output = %q, want relative /tasks/<id> link", res.Text)
	}
}

// A proposal for a repo off the deployment's target repo list is filed
// and parked awaiting a reply (ui.Client.parkOffAllowlist), whatever its
// approval. The filer read every state but proposed and deferred as
// queued, so a director that held three such proposals was told they
// were "queued already and dispatched in its turn without anybody
// approving it", and told its operator hold_for_approval was broken
// (grain/task-2050). Held, it says held and parked; approved, it says
// parked and never queued.
func TestLiveProposalOffTheTargetRepoListSaysItIsParked(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		if cfg.PollInterval == 0 {
			cfg.PollInterval = model.DefaultPollInterval
		}
		cfg.TargetRepos = []string{"acme/widgets"}
		cfg.ConsoleSessions = []model.ConsoleSession{
			{ID: model.DirectorSessionID, Title: "Director", TasksApproved: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator"),
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: testAccessCode,
	}, store))
	t.Cleanup(srv.Close)
	client := accessCodeClient(srv.URL)

	for _, tc := range []struct {
		name     string
		hold     bool
		want     []string
		approved bool
	}{
		{"held", true, []string{"filed unapproved because you asked to hold it (hold_for_approval); your permission would otherwise have queued it",
			"also parked awaiting a reply", "a reply alone does not approve it"}, false},
		{"approved", false, []string{"filed approved", "not queued: filing parked it"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The director session of grain/task-1933, whose deployment
			// lets it queue its own work in any repo.
			parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Director", Interactive: true, NoRepo: true})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := store.GetTask(ctx, parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			stored.Tags = append(stored.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
			if err := store.PutTask(ctx, *stored); err != nil {
				t.Fatal(err)
			}
			tool := mcp.NewLiveProposalTools(&mcp.MockSink{}, nil, parent.ID,
				&daemonProposalFiler{client: client, parent: parent.ID, baseURL: srv.URL, filed: map[string]string{}})[0]
			res := tool.Handler(ctx, map[string]any{
				"title": "publish " + tc.name, "body": "b", "repo": "sklodowska-dev/skill-publisher",
				"hold_for_approval": tc.hold,
			})
			if res.IsError {
				t.Fatalf("propose_task refused: %s", res.Text)
			}
			for _, w := range append(tc.want, "`sklodowska-dev/skill-publisher` is not on this deployment's target repo list") {
				if !strings.Contains(res.Text, w) {
					t.Errorf("propose_task answered %q, want it to say %q", res.Text, w)
				}
			}
			if strings.Contains(res.Text, "queued already") {
				t.Errorf("propose_task answered %q, want no claim that a parked proposal is queued", res.Text)
			}
			tasks, err := client.ListTasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, task := range tasks {
				if task.Title == "publish "+tc.name {
					if task.State != model.StateAwaitingReply || task.Approved != tc.approved {
						t.Fatalf("the proposal is %s, approved %v; want awaiting_reply, approved %v",
							task.State, task.Approved, tc.approved)
					}
					return
				}
			}
			t.Fatal("the proposal was not filed")
		})
	}
}
