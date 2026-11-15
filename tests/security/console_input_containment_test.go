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

package security

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/console"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestDelegatePersonCannotDriveAConsoleHoldingMoreThanItsRole is the
// regression test for finding CE-5 of the credential-exposure audit
// (docs/security/audits/2026-11-10-credential-exposure.md).
//
// POST /api/console/{id}/input needs only sessions:write, which
// grain/delegated holds. A live console is a run's terminal: whatever its
// task was granted -- here api:secrets:write -- the run can use, and so can
// anybody typing into it (`cat ~/.gcp-token`, or the run's own MCP tools).
// A person whose role could never be granted api:secrets:write must
// therefore not be able to type into, resize, close or read that console.
// The same person may still drive a console whose task holds nothing past
// their role, so the refusal is the containment, not a missing scope.
func TestDelegatePersonCannotDriveAConsoleHoldingMoreThanItsRole(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	const delegate = "delegate@example.com"

	consoles := console.NewManager()
	t.Cleanup(func() {
		for _, info := range consoles.List() {
			_ = consoles.Close(info.TaskID)
		}
	})
	f := newRouteFixtureWith(t, true, true, func(cfg *ui.Config) {
		cfg.Consoles = consoles
	})
	f.from = publicAddr
	f.accessCookie = ""
	ctx := context.Background()

	if _, _, err := f.store.AddAllowedPrincipalWithRole(ctx, "user:"+delegate, "test", "test",
		model.DefaultDelegateAccessRole, time.Now()); err != nil {
		t.Fatal(err)
	}

	base, err := f.store.GetTask(ctx, f.taskID)
	if err != nil || base == nil {
		t.Fatalf("reading fixture task %s: %v", f.taskID, err)
	}
	privileged := *base
	privileged.Title = "console holding api:secrets:write"
	privileged.Grants = append(append([]model.Grant(nil), base.Grants...),
		model.Grant{Capability: "api:secrets:write", Via: model.GrantByLabel})
	if err := f.store.PutTask(ctx, privileged); err != nil {
		t.Fatalf("granting api:secrets:write to task %s: %v", privileged.ID, err)
	}
	plain := *base
	plain.ID = "9404"
	plain.Branch = ""
	plain.Title = "console holding nothing past grain/delegated"
	plain.Grants = nil
	if err := f.store.PutTask(ctx, plain); err != nil {
		t.Fatalf("filing plain task: %v", err)
	}

	for _, id := range []string{privileged.ID, plain.ID} {
		sess, err := consoles.Start(console.Spec{
			TaskID: id, RunID: "run-" + id, Repo: "acme/widgets", StartedAt: time.Now(),
		}, console.Command{Path: "/bin/cat"})
		if err != nil {
			t.Fatalf("starting console for task %s: %v", id, err)
		}
		t.Cleanup(func() {
			_ = consoles.Close(id)
			<-sess.Done()
		})
	}

	const marker = "typed-by-the-delegate-4004"
	input := fmt.Sprintf(`{"input":%q}`, base64.StdEncoding.EncodeToString([]byte("echo "+marker+"\n")))

	// The finding itself: a grain/delegated person types into the console
	// of a task holding api:secrets:write and is refused.
	rec := f.send(t, http.MethodPost, "/api/console/"+privileged.ID+"/input", input, delegate)
	if rec.code != http.StatusForbidden {
		t.Fatalf("POST /api/console/%s/input as grain/delegated on a task holding api:secrets:write answered %d, want 403: %s",
			privileged.ID, rec.code, rec.excerpt())
	}
	if !strings.Contains(rec.body.String(), "api:secrets:write") {
		t.Errorf("the refusal does not name the grant the role lacks: %s", rec.excerpt())
	}
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/console/" + privileged.ID + "/resize", `{"cols":120,"rows":40}`},
		{http.MethodGet, "/api/console/" + privileged.ID + "/output", ""},
		{http.MethodDelete, "/api/console/" + privileged.ID, ""},
	} {
		if rec := f.send(t, probe.method, probe.path, probe.body, delegate); rec.code != http.StatusForbidden {
			t.Errorf("%s %s as grain/delegated on a task holding api:secrets:write answered %d, want 403: %s",
				probe.method, probe.path, rec.code, rec.excerpt())
		}
	}

	// Positive control: the same person drives a console whose task holds
	// nothing past their role, and what they type reaches it.
	if rec := f.send(t, http.MethodPost, "/api/console/"+plain.ID+"/input", input, delegate); rec.code != http.StatusNoContent && rec.code != http.StatusOK {
		t.Fatalf("POST /api/console/%s/input as grain/delegated on a plain task answered %d, want 2xx: %s",
			plain.ID, rec.code, rec.excerpt())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, _, _ := consoles.Get(plain.ID).Output(0)
		if strings.Contains(string(out), marker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the plain console never echoed what the delegate typed: %q", out)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The refused console is still open, and never saw the input.
	sess := consoles.Get(privileged.ID)
	if sess == nil {
		t.Fatalf("the refused DELETE closed console %s anyway", privileged.ID)
	}
	if out, _, _ := sess.Output(0); strings.Contains(string(out), marker) {
		t.Errorf("console %s carries input the refused request typed: %q", privileged.ID, out)
	}
}
