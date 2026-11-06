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

package grainclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/grain-plc/pkg/grainclient"
	"github.com/ai-on-gke/grain/grain-plc/pkg/perf"
	"github.com/ai-on-gke/grain/grain-plc/pkg/plcseams"
)

func TestEntryKindValidationAndRouting(t *testing.T) {
	if err := grainclient.ValidateEntryKind(grainclient.EntryKindInstance); err != nil {
		t.Fatalf("ValidateEntryKind(instance): %v", err)
	}
	if err := grainclient.ValidateEntryKind(grainclient.EntryKindConnected); err != nil {
		t.Fatalf("ValidateEntryKind(connected): %v", err)
	}
	if err := grainclient.ValidateEntryKind(grainclient.EntryKind("redirect")); !errors.Is(err, grainclient.ErrUnsupportedEntryKind) {
		t.Fatalf("expected ErrUnsupportedEntryKind for redirect, got %v", err)
	}

	// Plain HTTP backend_url for hosted VM instance (per v1 registry decision).
	u, err := grainclient.ResolveEntryURL(grainclient.Entry{
		Name:       "alpha",
		Kind:       grainclient.EntryKindInstance,
		BackendURL: "http://10.128.0.15:8080/",
	}, "https://registry.example.com")
	if err != nil || u != "http://10.128.0.15:8080" {
		t.Fatalf("ResolveEntryURL(instance) = %q, err=%v", u, err)
	}
}

func TestRegistryAndDaemonAPIsEndToEnd(t *testing.T) {
	defer plcseams.ResetForTest()

	var (
		gotOnBehalfOf string
		gotSubjToken  string
		gotClientHdr  string
		gotAssertion  string
		gotCreatedReq grainclient.CreateTaskRequest
	)

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOnBehalfOf = r.Header.Get(plcseams.HeaderOnBehalfOf)
		gotSubjToken = r.Header.Get(plcseams.HeaderSubjectToken)
		gotClientHdr = r.Header.Get(plcseams.HeaderClient)
		gotAssertion = r.Header.Get(plcseams.HeaderRegistryAssertion)

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks":
			if err := json.NewDecoder(r.Body).Decode(&gotCreatedReq); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(grainclient.Task{
				ID:          "101",
				Title:       gotCreatedReq.Title,
				Body:        gotCreatedReq.Body,
				State:       "queued",
				Repo:        gotCreatedReq.Repo,
				ExternalRef: gotCreatedReq.ExternalRef,
				Context:     gotCreatedReq.Context,
				Designs:     gotCreatedReq.Designs,
				Reads:       gotCreatedReq.Reads,
				FiledBy:     gotOnBehalfOf,
				FiledVia:    gotClientHdr,
				ContextManifest: &grainclient.ContextManifest{
					Schema:      1,
					ExternalRef: gotCreatedReq.ExternalRef,
					Files: []grainclient.ContextManifestFile{
						{Path: "prd.md", Role: "prd"},
					},
				},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/101":
			_ = json.NewEncoder(w).Encode(grainclient.Task{
				ID:          "101",
				Title:       "Implement launch step",
				State:       "in_progress",
				ExternalRef: "plc:proj/alpha",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks":
			_ = json.NewEncoder(w).Encode([]grainclient.Task{
				{
					ID:          "101",
					Title:       "Implement launch step",
					State:       "in_progress",
					ExternalRef: r.URL.Query().Get("externalRef"),
				},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks/101/comments":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(grainclient.Comment{
				ID:     1,
				TaskID: "101",
				Author: gotOnBehalfOf,
				Body:   "Looking good",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks/101/reply":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "queued"})

		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks/101/approve":
			_ = json.NewEncoder(w).Encode(grainclient.Task{
				ID:         "101",
				State:      "queued",
				ApprovedBy: gotOnBehalfOf,
			})

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.EscapedPath(), "/api/refs/") && strings.HasSuffix(r.URL.EscapedPath(), "/summary"):
			_ = json.NewEncoder(w).Encode(grainclient.RefSummary{
				ExternalRef: "plc:proj/alpha",
				Counts: grainclient.RefRollupCounts{
					Total:  2,
					Active: 1,
					Merged: 1,
				},
				Tasks: []grainclient.RefTaskSummary{
					{ID: "101", Title: "Implement launch step", State: "in_progress", Repo: "acme/widgets"},
					{ID: "100", Title: "Bootstrap schema", State: "merged", Repo: "acme/widgets", PRURL: "https://example.com/pr/100"},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer daemonSrv.Close()

	registrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/directory":
			_ = json.NewEncoder(w).Encode(grainclient.DirectoryResponse{
				Entries: []grainclient.DirectoryEntry{
					{
						Name:  "widgets-team",
						Kind:  grainclient.EntryKindInstance,
						URL:   daemonSrv.URL,
						Owner: "alice@example.com",
					},
					{
						Name:  "legacy-redirect",
						Kind:  grainclient.EntryKind("redirect"),
						URL:   "https://legacy.example.com",
						Owner: "alice@example.com",
					},
				},
			})
		case "/api/route":
			cand := grainclient.RouteCandidate{
				Name:  "widgets-team",
				Kind:  grainclient.EntryKindInstance,
				URL:   daemonSrv.URL,
				Owner: "alice@example.com",
			}
			_ = json.NewEncoder(w).Encode(grainclient.RouteResponse{
				Repo:       r.URL.Query().Get("repo"),
				Primary:    &cand,
				Candidates: []grainclient.RouteCandidate{cand},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer registrySrv.Close()

	recorder := perf.NewRecorder(64)
	client := grainclient.New(grainclient.Config{
		RegistryURL:       registrySrv.URL,
		ClientName:        grainclient.ClientPLCStudio,
		RegistryAssertion: "jwt-assertion-xyz",
		Perf:              recorder,
	})

	// 1. Directory filters out non-v1 redirect entries and caches response.
	dir, err := client.Directory(context.Background())
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if len(dir.Entries) != 1 || dir.Entries[0].Name != "widgets-team" {
		t.Fatalf("Directory entries = %+v, want [widgets-team]", dir.Entries)
	}
	// Second call hits directory cache.
	if _, err := client.Directory(context.Background()); err != nil {
		t.Fatalf("Directory cached: %v", err)
	}

	// 2. ResolveInstance by repo and call CreateTask with delegated principal.
	ctx := plcseams.WithPrincipal(context.Background(), &plcseams.Principal{
		Email:        "alice@example.com",
		SubjectToken: "subj-tok-789",
	})
	inst, entry, err := client.ResolveInstance(ctx, "", "acme/widgets")
	if err != nil {
		t.Fatalf("ResolveInstance: %v", err)
	}
	if entry == nil || entry.Name != "widgets-team" {
		t.Fatalf("resolved entry = %+v, want widgets-team", entry)
	}

	created, err := inst.CreateTask(ctx, grainclient.CreateTaskRequest{
		Title:       "[PLC] Implement launch step",
		Body:        "Follow the committed context bundle.",
		Repo:        "acme/widgets",
		Reads:       []string{"acme/widgets-read"},
		ExternalRef: "plc:proj/alpha",
		Context: []grainclient.ContextRef{
			{
				Repo: "acme/widgets-read",
				Path: ".grain/context/plc/proj-alpha",
				Rev:  "0123456789abcdef0123456789abcdef01234567",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.ID != "101" || created.ContextManifest == nil || len(created.ContextManifest.Files) != 1 {
		t.Fatalf("created task = %+v", created)
	}
	if gotOnBehalfOf != "alice@example.com" || gotSubjToken != "subj-tok-789" {
		t.Fatalf("delegation headers: on-behalf-of=%q subject-token=%q", gotOnBehalfOf, gotSubjToken)
	}
	if gotClientHdr != grainclient.ClientPLCStudio || gotAssertion != "jwt-assertion-xyz" {
		t.Fatalf("client=%q assertion=%q", gotClientHdr, gotAssertion)
	}

	// 3. RefSummary rollup for externalRef containing slashes.
	summary, err := inst.RefSummary(ctx, "plc:proj/alpha")
	if err != nil {
		t.Fatalf("RefSummary: %v", err)
	}
	if summary.Counts.Total != 2 || summary.Counts.Merged != 1 || len(summary.Tasks) != 2 {
		t.Fatalf("RefSummary = %+v", summary)
	}

	// 4. ReplyTask, PostComment, ApproveTask.
	if _, err := inst.PostComment(ctx, "101", "Looking good", "alice@example.com"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if err := inst.ReplyTask(ctx, "101", "Proceed with v1"); err != nil {
		t.Fatalf("ReplyTask: %v", err)
	}
	if _, err := inst.ApproveTask(ctx, "101"); err != nil {
		t.Fatalf("ApproveTask: %v", err)
	}
}

func TestPollAndStreamEvents(t *testing.T) {
	var gotLastEventID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			gotLastEventID = r.Header.Get("Last-Event-ID")
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			_, _ = fmt.Fprint(w, "id: 11\nevent: task.state\ndata: {\"id\":11,\"type\":\"task.state\",\"taskId\":\"101\",\"externalRef\":\"bug:42\",\"state\":\"in_progress\"}\n\n")
			_, _ = fmt.Fprint(w, "id: 12\nevent: task.question\ndata: {\"id\":12,\"type\":\"task.question\",\"taskId\":\"101\",\"externalRef\":\"bug:42\",\"summary\":\"Which region?\",\"question\":\"Deploy to us-central1 or europe-west1?\"}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(grainclient.EventsPage{
			Events: []grainclient.IntegrationEvent{
				{ID: 10, Type: grainclient.EventTaskState, TaskID: "101", State: "queued"},
			},
			NextCursor: 10,
		})
	}))
	defer srv.Close()

	client := grainclient.New(grainclient.Config{
		GrainURL:   srv.URL,
		ClientName: grainclient.ClientPLCChatGateway,
	})
	inst, err := client.DefaultInstance(context.Background())
	if err != nil {
		t.Fatalf("DefaultInstance: %v", err)
	}

	page, err := inst.PollEvents(context.Background(), grainclient.EventsFilter{
		Since: 5,
		Types: []string{grainclient.EventTaskState},
		Wait:  10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("PollEvents: %v", err)
	}
	if len(page.Events) != 1 || page.NextCursor != 10 {
		t.Fatalf("PollEvents = %+v", page)
	}

	var streamed []grainclient.IntegrationEvent
	err = inst.StreamEvents(context.Background(), grainclient.EventsFilter{
		Since:       10,
		ExternalRef: "bug:42",
	}, func(ev grainclient.IntegrationEvent) error {
		streamed = append(streamed, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	if gotLastEventID != "10" {
		t.Fatalf("Last-Event-ID = %q, want 10", gotLastEventID)
	}
	if len(streamed) != 2 || streamed[0].ID != 11 || streamed[1].Type != grainclient.EventTaskQuestion {
		t.Fatalf("streamed events = %+v", streamed)
	}
}
