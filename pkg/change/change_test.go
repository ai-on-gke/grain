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

package change

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

type fakeProvider struct {
	kind     model.LinkKind
	target   string
	observe  error
	check    error
	observed int
}

func (f *fakeProvider) Kind() model.LinkKind { return f.kind }
func (f *fakeProvider) TargetKind() string   { return f.target }
func (f *fakeProvider) Open(context.Context, model.Task, OpenRequest) (OpenResult, error) {
	return OpenResult{Target: "cl/1"}, nil
}
func (f *fakeProvider) Observe(_ context.Context, _ model.Task, target string) (Observation, error) {
	f.observed++
	return Observation{Target: target, Status: StatusPending}, f.observe
}
func (f *fakeProvider) Threads(context.Context, model.Task, string) ([]Thread, error) {
	return nil, ErrNotSupported
}
func (f *fakeProvider) Submit(context.Context, model.Task, string, Person) (Result, error) {
	return Result{}, ErrNotSupported
}
func (f *fakeProvider) Sync(context.Context, model.Task, string) (SyncResult, error) {
	return SyncResult{}, ErrNotSupported
}
func (f *fakeProvider) Check(context.Context) error { return f.check }
func (f *fakeProvider) CredentialNotice() Notice {
	return Notice{Subject: "fake-expired", Title: "Run fakecert"}
}

type fakeNotifier struct {
	raised  []string
	cleared []string
}

func (n *fakeNotifier) RaiseNotification(_ context.Context, no model.Notification) (model.NotificationRaise, error) {
	n.raised = append(n.raised, no.Subject)
	return model.NotificationRaise{}, nil
}
func (n *fakeNotifier) ClearNotification(_ context.Context, subject, _ string) (bool, error) {
	n.cleared = append(n.cleared, subject)
	return true, nil
}

func TestRegisterMakesTheProviderAnOpenerForItsTarget(t *testing.T) {
	p := &fakeProvider{kind: "fake-link", target: "fake2"}
	Register(p)
	if got, ok := Lookup("fake-link"); !ok || got != p {
		t.Fatalf("Lookup = %v, %v", got, ok)
	}
	if o, ok := OpenerFor("fake2"); !ok || o != Opener(p) {
		t.Fatalf("OpenerFor(fake2) = %v, %v", o, ok)
	}
}

func TestExpiredCredentialsBlockPollingUntilACheckPasses(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{kind: "fake-cred", target: "fake3", observe: errors.New("credentials_expired: LOAS2 certificate has expired")}
	Register(p)
	n := &fakeNotifier{}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	task := model.Task{ID: "7"}

	if _, err := Observe(ctx, task, "fake-cred", "cl/1", n, now); !IsCredentialsExpired(err) {
		t.Fatalf("first observe err = %v, want credentials_expired", err)
	}
	if HealthOf("fake3").Blocked != BlockedCredentials {
		t.Fatalf("health = %+v, want blocked: credentials", HealthOf("fake3"))
	}
	if len(n.raised) != 1 || n.raised[0] != "fake-expired" {
		t.Fatalf("raised = %v, want one fake-expired", n.raised)
	}
	// Blocked: nothing polls the forge, and the notification is not raised again.
	if _, err := Observe(ctx, task, "fake-cred", "cl/1", n, now.Add(time.Minute)); !IsCredentialsExpired(err) {
		t.Fatalf("blocked observe err = %v", err)
	}
	if p.observed != 1 || len(n.raised) != 1 {
		t.Fatalf("observed %d times, raised %v: polling did not stop", p.observed, n.raised)
	}
	// A failing Check leaves it blocked; a passing one clears it and its notification.
	p.check = errors.New("still expired")
	Recheck(ctx, n)
	if HealthOf("fake3").Blocked == "" || len(n.cleared) != 0 {
		t.Fatalf("a failing check cleared the block")
	}
	p.check, p.observe = nil, nil
	Recheck(ctx, n)
	if HealthOf("fake3").Blocked != "" || len(n.cleared) != 1 || n.cleared[0] != "fake-expired" {
		t.Fatalf("after a passing check: health %+v, cleared %v", HealthOf("fake3"), n.cleared)
	}
	if _, err := Observe(ctx, task, "fake-cred", "cl/1", n, now); err != nil || p.observed != 2 {
		t.Fatalf("observe after clearing: %v (observed %d)", err, p.observed)
	}
}

func TestPrimaryIsTheFirstChangelist(t *testing.T) {
	task := model.Task{Links: []model.Link{
		{Kind: model.LinkDependsOn, Target: "3"},
		{Kind: model.LinkChangelist, Target: "cl/10"},
		{Kind: model.LinkChangelist, Target: "cl/11"},
	}}
	if got := Primary(task); got != "cl/10" {
		t.Fatalf("Primary = %q", got)
	}
	if got := Changes(task); len(got) != 2 {
		t.Fatalf("Changes = %v", got)
	}
}
