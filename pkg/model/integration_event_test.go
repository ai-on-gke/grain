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

package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openEventTestStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("initializing store: %v", err)
	}
	return store
}

func TestIntegrationEventExternalRefResolutionOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openEventTestStore(t)
	now := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	cases := []struct {
		id      string
		task    model.Task
		wantRef string
	}{
		{
			id: "ref-explicit",
			task: model.Task{
				Target:       &repo,
				ExternalRef:  "CA-204",
				Links:        []model.Link{{Kind: model.LinkGoogleChat, Target: "spaces/CHAT0"}},
				CustomFields: map[string][]string{"external_ref": {"JIRA-0"}},
				Issue:        1,
			},
			wantRef: "CA-204",
		},
		{
			id: "ref-explicit-child",
			task: model.Task{
				Target: &repo,
				Links:  []model.Link{{Kind: model.LinkProposedBy, Target: "ref-explicit"}},
			},
			wantRef: "CA-204",
		},
		{
			id: "ref-chat",
			task: model.Task{
				Target:       &repo,
				Links:        []model.Link{{Kind: model.LinkGoogleChat, Target: "spaces/CHAT1"}},
				CustomFields: map[string][]string{"external_ref": {"JIRA-1"}},
				Issue:        9,
			},
			wantRef: "spaces/CHAT1",
		},
		{
			id: "ref-custom",
			task: model.Task{
				Target:       &repo,
				CustomFields: map[string][]string{"external_ref": {"LINEAR-42"}},
				Issue:        9,
			},
			wantRef: "LINEAR-42",
		},
		{
			id: "ref-issue",
			task: model.Task{
				Target:     &repo,
				WorkOrigin: "task:ref-chat",
				Issue:      17,
			},
			wantRef: "acme/widgets#17",
		},
		{
			id: "ref-external-pr",
			task: model.Task{
				Target:     &repo,
				WorkOrigin: "task:ref-chat",
				Links:      []model.Link{{Kind: model.LinkExternalPR, Target: "upstream/lib#99"}},
			},
			wantRef: "upstream/lib#99",
		},
		{
			id: "ref-inherited-origin",
			task: model.Task{
				Target:     &repo,
				WorkOrigin: "task:ref-chat",
			},
			wantRef: "acme/widgets#9",
		},
	}

	for _, tc := range cases {
		tk := tc.task
		tk.ID = tc.id
		tk.Title = tc.id
		tk.CreatedAt = &now
		tk.Origin = model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", tc.id, err)
		}
	}

	events, err := store.ListIntegrationEvents(ctx, model.IntegrationEventFilter{Limit: 20})
	if err != nil {
		t.Fatalf("ListIntegrationEvents: %v", err)
	}
	if len(events) != len(cases) {
		t.Fatalf("got %d events, want %d", len(events), len(cases))
	}
	for i, tc := range cases {
		if events[i].TaskID != tc.id || events[i].ExternalRef != tc.wantRef {
			t.Errorf("event[%d] (%s): ExternalRef = %q, want %q",
				i, events[i].TaskID, events[i].ExternalRef, tc.wantRef)
		}
	}
}
