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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/credhealth"
	"github.com/ai-on-gke/grain/pkg/model"
)

type notifier struct {
	raised  []string
	cleared []string
}

func (n *notifier) RaiseNotification(_ context.Context, no model.Notification) (model.NotificationRaise, error) {
	n.raised = append(n.raised, no.Subject)
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

	err = errors.New("expires in 40m: run gcert (touch your security key)")
	credhealth.RunChecks(context.Background(), time.Now())
	if f := credhealth.Failures(); len(f) != 1 || f[0].Source != "check:cert" || f[0].Detail != err.Error() {
		t.Errorf("failures = %+v, want check:cert", f)
	}
	err = nil
	credhealth.RunChecks(context.Background(), time.Now())
	if f := credhealth.Failures(); len(f) != 0 {
		t.Errorf("failures after a passing check = %+v", f)
	}
	if len(n.raised) != 1 || len(n.cleared) != 1 {
		t.Errorf("raised %q, cleared %q; want one each", n.raised, n.cleared)
	}
}
