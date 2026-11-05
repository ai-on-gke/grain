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
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// #XC-20: POST /api/tasks/{id}/reply answers a parked question, and
// refuses -- 409, posting nothing -- a task with no question parked and
// one parked on a secret request.
func TestGrainReplyAnswersQuestionRefusesIdleAndRefusesSecretRequest(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	comments := func(id string) int {
		t.Helper()
		cs, err := client.Store.Comments(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}
	park := func(id, secret string) {
		t.Helper()
		qid, err := client.Store.AddComment(ctx, model.Comment{TaskID: id, Body: "which branch?",
			Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}}, CreatedAt: baseTime})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Store.Observe(ctx, model.Observation{TaskID: id, PendingQuestionCommentID: &qid, PendingSecret: secret}); err != nil {
			t.Fatal(err)
		}
	}
	seedAPITask(t, client, "300", "acme/widgets")
	seedAPITask(t, client, "301", "acme/widgets")
	seedAPITask(t, client, "302", "acme/widgets")
	park("300", "")
	park("302", "stripe-api-key")

	before := comments("301")
	code, body := serve(t, srv, http.MethodPost, "/api/tasks/301/reply", `{"body":"main"}`)
	if code != http.StatusConflict || !strings.Contains(body, "not parked on a question") {
		t.Errorf("reply to an idle task: %d %s, want 409 naming why", code, body)
	}
	if comments("301") != before {
		t.Error("a refused reply still posted a comment")
	}

	before = comments("302")
	code, body = serve(t, srv, http.MethodPost, "/api/tasks/302/reply", `{"body":"sk_live_secret"}`)
	if code != http.StatusConflict || !strings.Contains(body, "/tasks/302") || !strings.Contains(body, "secret") {
		t.Errorf("reply to a secret request: %d %s, want 409 pointing at /tasks/302", code, body)
	}
	if comments("302") != before {
		t.Error("a reply to a secret request posted the secret into the conversation")
	}
	cs, _ := client.Store.Comments(ctx, "302")
	for _, c := range cs {
		if strings.Contains(c.Body, "sk_live_secret") {
			t.Fatal("the secret landed in the conversation")
		}
	}

	before = comments("300")
	code, body = serve(t, srv, http.MethodPost, "/api/tasks/300/reply", `{"body":"main, please"}`)
	if code != http.StatusOK {
		t.Fatalf("reply to a parked question: %d %s", code, body)
	}
	if comments("300") != before+1 {
		t.Error("the reply was not recorded in the conversation")
	}
	if code, _ := serve(t, srv, http.MethodPost, "/api/tasks/404/reply", `{"body":"x"}`); code != http.StatusNotFound {
		t.Errorf("reply to no task answered %d, want 404", code)
	}
}
