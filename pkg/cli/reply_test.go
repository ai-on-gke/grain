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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// parkOnQuestion parks task id on a question its run asked, and on a
// secret request when secret is set.
func parkOnQuestion(t *testing.T, client *ui.Client, id, secret string) {
	t.Helper()
	ctx := context.Background()
	qid, err := client.Store.AddComment(ctx, model.Comment{TaskID: id, Body: "which branch?",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Store.Observe(ctx, model.Observation{TaskID: id, PendingQuestionCommentID: &qid, PendingSecret: secret}); err != nil {
		t.Fatal(err)
	}
}

// #XC-20: grain reply answers a parked question, and exits 2 posting
// nothing on a task with none parked and on a secret request -- naming
// /tasks/<id> for the latter -- where grain comment would have posted.
func TestGrainReplyAnswersQuestionRefusesIdleAndRefusesSecretRequest(t *testing.T) {
	client, c := externalCLIServer(t)
	ctx := context.Background()
	file := func(title string) string {
		t.Helper()
		task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: title, NoRepo: true, Approved: true})
		if err != nil {
			t.Fatal(err)
		}
		return task.ID
	}
	asked, idle, secret := file("asked"), file("idle"), file("secret")
	parkOnQuestion(t, client, asked, "")
	parkOnQuestion(t, client, secret, "stripe-api-key")
	count := func(id string) int {
		t.Helper()
		cs, err := client.Store.Comments(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}

	out := &printer{json: true}
	before := count(idle)
	err := cmdReply(ctx, c, out, []string{idle, "use", "main"})
	if exit, code, _ := classifyError(err); err == nil || exit != exitUsage || code != "conflict" {
		t.Errorf("reply to an idle task: %v (exit %d, %s), want a 409 conflict, exit 2", err, exit, code)
	}
	if count(idle) != before {
		t.Error("a refused reply posted a comment")
	}

	before = count(secret)
	err = cmdReply(ctx, c, out, []string{secret, "sk_live_123"})
	if exit, _, _ := classifyError(err); err == nil || exit != exitUsage || !strings.Contains(err.Error(), "/tasks/"+secret) {
		t.Errorf("reply to a secret request: %v (exit %d), want exit 2 pointing at /tasks/%s", err, exit, secret)
	}
	if count(secret) != before {
		t.Error("a reply to a secret request posted into the conversation")
	}

	old := cliStdin
	t.Cleanup(func() { cliStdin = old })
	cliStdin = strings.NewReader("main, please\n")
	before = count(asked)
	captureStdout(t, func() {
		if err := cmdReply(ctx, c, out, []string{"-body-file", "-", asked}); err != nil {
			t.Fatalf("reply to a parked question: %v", err)
		}
	})
	cs, _ := client.Store.Comments(ctx, asked)
	if len(cs) != before+1 || strings.TrimSpace(cs[len(cs)-1].Body) != "main, please" {
		t.Errorf("the reply was not recorded: %+v", cs)
	}

	if err := cmdReply(ctx, c, out, []string{asked}); err == nil {
		t.Error("a reply with no body was sent")
	}
}
