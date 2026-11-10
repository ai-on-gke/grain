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
	"encoding/json"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// #XC-21: grain inbox prints every actionable kind with its UI path,
// narrows by -kind and -repo, and refuses an unknown kind with exit 2.
func TestGrainInboxMatchesActionableCategoriesWithinRepoGrants(t *testing.T) {
	client, c := externalCLIServer(t)
	ctx := context.Background()
	file := func(title string, approved bool) string {
		t.Helper()
		task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: title, NoRepo: true, Approved: approved})
		if err != nil {
			t.Fatal(err)
		}
		return task.ID
	}
	asked := file("asked", true)
	secret := file("secret", true)
	proposal := file("proposal", false)
	file("queued", true)
	parkOnQuestion(t, client, asked, "")
	parkOnQuestion(t, client, secret, "stripe-api-key")
	if _, err := client.RaiseNotification(ctx, ui.RaiseNotificationRequest{Subject: "ci:main", Title: "main is red", Severity: "critical"}); err != nil {
		t.Fatal(err)
	}

	text := captureStdout(t, func() {
		if err := cmdInbox(ctx, c, &printer{json: true}, nil); err != nil {
			t.Fatal(err)
		}
	})
	var resp ui.InboxResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("decoding %q: %v", text, err)
	}
	got := map[string]string{}
	for _, it := range resp.Items {
		got[it.Kind] = it.Path
	}
	// A question's item links to the comment it is parked on.
	obs, oerr := client.Store.GetObservation(context.Background(), asked)
	if oerr != nil || obs == nil || obs.PendingQuestionCommentID == nil {
		t.Fatalf("observation of %s = %+v, %v", asked, obs, oerr)
	}
	want := map[string]string{
		ui.InboxQuestion: model.CommentPath(asked, *obs.PendingQuestionCommentID), ui.InboxSecret: "/tasks/" + secret,
		ui.InboxProposal: "/tasks/" + proposal, ui.InboxNotification: "/inbox",
	}
	for kind, path := range want {
		if got[kind] != path {
			t.Errorf("%s item path = %q, want %q (inbox %+v)", kind, got[kind], path, resp.Items)
		}
	}
	if len(resp.Items) != len(want) {
		t.Errorf("inbox listed %d items, want %d: %+v", len(resp.Items), len(want), resp.Items)
	}

	text = captureStdout(t, func() {
		if err := cmdInbox(ctx, c, &printer{}, []string{"-kind", "secret"}); err != nil {
			t.Fatal(err)
		}
	})
	if lines := strings.Split(strings.TrimSpace(text), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "secret") ||
		!strings.HasSuffix(lines[0], "/tasks/"+secret) {
		t.Errorf("-kind secret printed %q", text)
	}
	text = captureStdout(t, func() {
		if err := cmdInbox(ctx, c, &printer{json: true}, []string{"-repo", "acme/elsewhere"}); err != nil {
			t.Fatal(err)
		}
	})
	if err := json.Unmarshal([]byte(text), &resp); err != nil || len(resp.Items) != 0 {
		t.Errorf("-repo acme/elsewhere listed %q (%v), want nothing", text, err)
	}
	err := cmdInbox(ctx, c, &printer{}, []string{"-kind", "everything"})
	if exit, _, _ := classifyError(err); exit != exitUsage {
		t.Errorf("-kind everything: %v (exit %d), want exit 2", err, exit)
	}
}
