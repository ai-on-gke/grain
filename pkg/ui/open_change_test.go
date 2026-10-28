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

package ui_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeCLs is a change provider for the "fakecl" target kind: each Open
// creates a CL unless the task already has a primary one.
type fakeCLs struct {
	next  int
	opens []change.OpenRequest
	prim  []string
	err   error
}

func (f *fakeCLs) Kind() model.LinkKind { return "fakecl-link" }
func (f *fakeCLs) TargetKind() string   { return "fakecl" }
func (f *fakeCLs) Open(_ context.Context, task model.Task, req change.OpenRequest) (change.OpenResult, error) {
	if f.err != nil {
		return change.OpenResult{}, f.err
	}
	f.opens = append(f.opens, req)
	f.prim = append(f.prim, change.Primary(task))
	if p := change.Primary(task); p != "" {
		return change.OpenResult{Target: p, URL: "http://" + p, Snapshot: 2, Presubmit: "running"}, nil
	}
	f.next++
	return change.OpenResult{Target: "cl/" + string(rune('0'+f.next)), Created: true, Snapshot: 1, Presubmit: "none"}, nil
}
func (f *fakeCLs) Observe(context.Context, model.Task, string) (change.Observation, error) {
	return change.Observation{}, nil
}
func (f *fakeCLs) Threads(context.Context, model.Task, string) ([]change.Thread, error) {
	return nil, nil
}
func (f *fakeCLs) Submit(context.Context, model.Task, string, change.Person) (change.Result, error) {
	return change.Result{}, nil
}
func (f *fakeCLs) Sync(context.Context, model.Task, string) (change.SyncResult, error) {
	return change.SyncResult{}, nil
}

func TestOpenPullRequestOnAChangeTargetCreatesThenUpdatesItsCL(t *testing.T) {
	client, store, ctx := testClient(t)
	fake := &fakeCLs{}
	change.Register(fake)
	client.Config.ExternalBase = "https://grain.example/"
	client.Config.LogRedactor = func() (*grainlog.Redactor, error) {
		return grainlog.NewRedactor([]grainlog.KnownValue{{Label: "secret token", Value: "s3cr3t-deployment-value"}}), nil
	}
	tk := model.Task{ID: "77", Title: "t", Intent: model.IntentImplement, ChangeTarget: &model.ChangeTarget{Kind: "fakecl"},
		WorkOrigin: "task 831", Links: []model.Link{{Kind: model.LinkChildOf, Target: "1401"}}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	if _, err := client.OpenChange(ctx, "77", ui.OpenChangeRequest{Summary: "Fix", Body: "token s3cr3t-deployment-value"}); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("a description with a deployment secret was not refused: %v", err)
	}
	if _, err := client.OpenChange(ctx, "77", ui.OpenChangeRequest{}); err == nil {
		t.Fatal("an open with no summary was not refused")
	}
	if len(fake.opens) != 0 {
		t.Fatalf("a refused description reached the provider: %+v", fake.opens)
	}

	st, err := client.OpenChange(ctx, "77", ui.OpenChangeRequest{Summary: "Fix the thing", Body: "Because."})
	if err != nil {
		t.Fatal(err)
	}
	if st.Number != 1 || !strings.Contains(st.Note, "created") || !strings.Contains(st.Note, "not been mailed") {
		t.Fatalf("first open = %+v", st)
	}
	req := fake.opens[0]
	if req.TaskURL != "https://grain.example/tasks/77" || !strings.Contains(strings.Join(req.Trailers, "\n"), "Origin: task 831") {
		t.Fatalf("open request = %+v", req)
	}
	if _, err := client.OpenChange(ctx, "77", ui.OpenChangeRequest{Summary: "Fix the thing", Body: "Better."}); err != nil {
		t.Fatal(err)
	}
	if fake.prim[1] != "cl/1" {
		t.Fatalf("second open saw primary %q, want cl/1", fake.prim[1])
	}
	got, err := store.GetTask(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	var cls []string
	for _, l := range got.Links {
		if l.Kind == model.LinkChangelist {
			cls = append(cls, l.Target)
		}
	}
	if len(cls) != 1 || cls[0] != "cl/1" {
		t.Fatalf("changelist links = %v, want just cl/1", cls)
	}
	if !model.AwaitsSubmit(*got) {
		t.Fatal("a task with a CL does not await submit")
	}

	// Expired credentials are a typed refusal, and block the kind.
	fake.err = &change.CredentialsExpiredError{Kind: "fakecl", Detail: "credentialcheck: CertExpired"}
	_, err = client.OpenChange(ctx, "77", ui.OpenChangeRequest{Summary: "Fix", Body: "x"})
	var ce *change.CredentialsExpiredError
	if err == nil || !(errors.As(err, &ce) || strings.Contains(err.Error(), "credentials_expired")) || !strings.Contains(err.Error(), "resume_later") {
		t.Fatalf("expired credentials = %v", err)
	}
	if change.HealthOf("fakecl").Blocked != change.BlockedCredentials {
		t.Fatalf("health = %+v, want blocked: credentials", change.HealthOf("fakecl"))
	}
	change.ClearBlocked("fakecl")
}
