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

package credhealth_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/credhealth"
	"github.com/ai-on-gke/grain/pkg/model"
)

type notifier struct {
	raised  []string
	bodies  []string
	cleared []string
}

func (n *notifier) RaiseNotification(_ context.Context, no model.Notification) (model.NotificationRaise, error) {
	n.raised = append(n.raised, no.Subject)
	n.bodies = append(n.bodies, no.Body)
	return model.NotificationRaise{Notification: no, New: true}, nil
}

func (n *notifier) ClearNotification(_ context.Context, subject, _ string) (bool, error) {
	n.cleared = append(n.cleared, subject)
	return true, nil
}

// A source failing twice raises once, and its clear clears it once.
func TestFailRaisesOnceAndClearClears(t *testing.T) {
	credhealth.Reset()
	t.Cleanup(credhealth.Reset)
	n := &notifier{}
	credhealth.Bind(n)
	ctx, now := context.Background(), time.Now()

	if !credhealth.Fail(ctx, "host-tool:x", "t", "d1", now) {
		t.Error("first Fail is not new")
	}
	if credhealth.Fail(ctx, "host-tool:x", "t", "d2", now.Add(time.Minute)) {
		t.Error("second Fail is new")
	}
	f := credhealth.Failures()
	if len(f) != 1 || f[0].Detail != "d2" || f[0].Count != 2 || !f[0].Since.Equal(now) {
		t.Errorf("failures = %+v, want one spell since the first, saying d2", f)
	}
	if want := []string{credhealth.Subject("host-tool:x")}; !slices.Equal(n.raised, want) {
		t.Errorf("raised = %q, want %q", n.raised, want)
	}
	if !credhealth.Clear(ctx, "host-tool:x") || credhealth.Clear(ctx, "host-tool:x") {
		t.Error("Clear did not report the spell exactly once")
	}
	if want := []string{credhealth.Subject("host-tool:x")}; !slices.Equal(n.cleared, want) {
		t.Errorf("cleared = %q, want %q", n.cleared, want)
	}
}

// A registered check that fails is recorded under check:<name>, and one
// that passes again clears it.
func TestRunChecks(t *testing.T) {
	credhealth.Reset()
	t.Cleanup(credhealth.Reset)
	n := &notifier{}
	credhealth.Bind(n)
	var err error
	unregister := credhealth.RegisterCheck("cert", credhealth.Check{
		Title: "cert expiring",
		Run:   func(context.Context) error { return err },
	})
	defer unregister()

	err = errors.New("expires in 40m: run renew-cert against https://user:secretpass@auth.example.test/renew")
	credhealth.RunChecks(context.Background(), time.Now())
	f := credhealth.Failures()
	if len(f) != 1 || f[0].Source != "check:cert" || !strings.Contains(f[0].Detail, "run renew-cert") {
		t.Errorf("failures = %+v, want check:cert", f)
	}
	if len(f) == 1 && strings.Contains(f[0].Detail, "secretpass") {
		t.Errorf("check failure detail leaked credential shape: %q", f[0].Detail)
	}
	if len(n.bodies) != 1 || strings.Contains(n.bodies[0], "secretpass") {
		t.Errorf("notification body leaked credential shape: %q", n.bodies)
	}
	err = nil
	credhealth.RunChecks(context.Background(), time.Now())
	if f := credhealth.Failures(); len(f) != 0 {
		t.Errorf("failures after a passing check = %+v", f)
	}
	if len(n.raised) != 1 || len(n.cleared) != 1 {
		t.Errorf("raised %q, cleared %q; want one each", n.raised, n.cleared)
	}

	// Context cancellation on daemon shutdown is not a credential failure.
	ctx, cancel := context.WithCancel(context.Background())
	err = context.Canceled
	cancel()
	credhealth.RunChecks(ctx, time.Now())
	if f := credhealth.Failures(); len(f) != 0 {
		t.Errorf("failures after cancelled check = %+v, want none", f)
	}
	if len(n.raised) != 1 {
		t.Errorf("raised after cancelled check = %q, want no new raise", n.raised)
	}
}

type persistentNotifier struct {
	notifier
	open    map[string]*model.Notification
	openOps int
	failErr error
}

func (p *persistentNotifier) RaiseNotification(ctx context.Context, no model.Notification) (model.NotificationRaise, error) {
	if p.failErr != nil {
		return model.NotificationRaise{}, p.failErr
	}
	if p.open == nil {
		p.open = map[string]*model.Notification{}
	}
	cp := no
	p.open[no.Subject] = &cp
	return p.notifier.RaiseNotification(ctx, no)
}

func (p *persistentNotifier) ClearNotification(ctx context.Context, subject, by string) (bool, error) {
	_, had := p.open[subject]
	delete(p.open, subject)
	_, _ = p.notifier.ClearNotification(ctx, subject, by)
	return had, nil
}

func (p *persistentNotifier) OpenNotification(_ context.Context, subject string) (*model.Notification, error) {
	p.openOps++
	return p.open[subject], nil
}

// Clear clears an open notification persisted before a daemon restart once,
// without re-querying the store on every subsequent healthy pass, and Fail
// retries raising a notification when an earlier attempt failed or preceded Bind.
func TestPersistedNotificationClearsAfterRestartAndUnnotifiedSpellRetries(t *testing.T) {
	credhealth.Reset()
	t.Cleanup(credhealth.Reset)
	ctx, now := context.Background(), time.Now()

	store := &persistentNotifier{
		open: map[string]*model.Notification{
			credhealth.Subject("check:cert"): {Subject: credhealth.Subject("check:cert")},
		},
	}
	credhealth.Bind(store)

	if !credhealth.Clear(ctx, "check:cert") {
		t.Error("first Clear after restart did not clear the persisted notification")
	}
	if credhealth.Clear(ctx, "check:cert") {
		t.Error("second Clear reported clearing again")
	}
	if store.openOps != 1 || len(store.cleared) != 1 {
		t.Errorf("openOps=%d cleared=%q, want 1 open check and 1 clear", store.openOps, store.cleared)
	}

	// Transient RaiseNotification error is retried on the next Fail in the spell.
	store.failErr = errors.New("db busy")
	if !credhealth.Fail(ctx, "host-tool:x", "t", "d1", now) {
		t.Error("first Fail is not new")
	}
	if len(store.raised) != 0 {
		t.Fatalf("raised = %q while store errored", store.raised)
	}
	store.failErr = nil
	if credhealth.Fail(ctx, "host-tool:x", "t", "d2", now.Add(time.Minute)) {
		t.Error("second Fail in same spell reported new")
	}
	if want := []string{credhealth.Subject("host-tool:x")}; !slices.Equal(store.raised, want) {
		t.Errorf("raised after retry = %q, want %q", store.raised, want)
	}
}
