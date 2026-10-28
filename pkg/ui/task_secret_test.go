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

// The task-pane half of mcp's request_secret escape hatch
// (grain/task-230): a task parked on a credential offers where to write
// it, the write lands in the secrets store, the task queues again -- and
// the value appears in no response, no comment and no prompt.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// parkedOnASecret is the state orchestrator.relayParkingCalls leaves
// behind for a run that called request_secret: the request relayed as a
// comment, the task parked on that comment, and the secret's own name
// recorded beside it.
func parkedOnASecret(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context, name string) ui.Task {
	t.Helper()
	task := create(t, c, ctx)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      "This run asked for the secret `" + name + "`.",
		CreatedAt: baseTime,
	})
	if err != nil {
		t.Fatalf("relaying the request: %v", err)
	}
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
		o.PendingSecret = name
	}); err != nil {
		t.Fatalf("parking the task: %v", err)
	}
	return task
}

// withSecrets gives a test client somewhere to write to -- the
// colocation `grain daemon` always has and `grain demo` never does.
func withSecrets(t *testing.T, c *ui.Client) *secrets.Store {
	t.Helper()
	store := secrets.New(t.TempDir())
	c.Config.Secrets = store
	return store
}

func TestGetTaskReportsWhereAPendingSecretWouldBeWritten(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	withSecrets(t, c)
	task := parkedOnASecret(t, c, store, ctx, "stripe-api-key")

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply -- a secret request parks like a question", detail.State)
	}
	if detail.PendingSecret == nil {
		t.Fatal("PendingSecret = nil, want the credential the run asked for")
	}
	// A bare name is a secret holding one key, and that key is where a
	// write goes -- the same resolution capabilitySecretsFor already does
	// for a capability's declared credential.
	if got := *detail.PendingSecret; got.Name != "stripe-api-key" || got.Secret != "stripe-api-key" ||
		got.Key != secrets.AgentCredentialKey || got.Set {
		t.Fatalf("PendingSecret = %+v, want stripe-api-key/%s, unset", got, secrets.AgentCredentialKey)
	}
}

func TestGetTaskReportsNoPendingSecretForAnOrdinaryTask(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	withSecrets(t, c)
	task := create(t, c, ctx)

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.PendingSecret != nil {
		t.Fatalf("PendingSecret = %+v, want nil for a task nobody asked a secret of", detail.PendingSecret)
	}
}

// The whole point of the feature: the value reaches the secrets store
// and nothing else. It is not in the conversation the next run is handed
// (that thread is the prompt, orchestrator's commentThreadSection), and
// it is not in the response either -- nothing in this package can read a
// stored secret back out.
func TestSetPendingSecretStoresTheValueAndKeepsItOutOfTheThread(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	secretStore := withSecrets(t, c)
	task := parkedOnASecret(t, c, store, ctx, "stripe-api-key")

	if err := c.SetPendingSecret(ctx, task.ID, "sk_live_secret"); err != nil {
		t.Fatalf("SetPendingSecret: %v", err)
	}

	value, err := secretStore.Resolve(ctx, "stripe-api-key")
	if err != nil {
		t.Fatalf("Resolve after setting: %v", err)
	}
	if value != "sk_live_secret" {
		t.Fatalf("stored value = %q, want what was typed", value)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, cm := range comments {
		if strings.Contains(cm.Body, "sk_live_secret") {
			t.Fatalf("comment %q carries the value -- the thread is the next run's prompt", cm.Body)
		}
	}
	if len(comments) != 2 || !strings.Contains(comments[1].Body, "stripe-api-key") {
		t.Fatalf("conversation = %+v, want a note naming the secret that was set", comments)
	}
	if comments[1].Author.Actor.Kind != model.PrincipalHuman {
		t.Fatalf("note author = %+v, want the human who set it", comments[1].Author)
	}
}

// Setting the secret is an answer, so it un-parks the task exactly as a
// reply would -- and stops offering the box, since nothing is waiting on
// one any more.
func TestSetPendingSecretQueuesTheTaskAgain(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	withSecrets(t, c)
	task := parkedOnASecret(t, c, store, ctx, "stripe-api-key")

	if err := c.SetPendingSecret(ctx, task.ID, "sk_live_secret"); err != nil {
		t.Fatalf("SetPendingSecret: %v", err)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateQueued {
		t.Fatalf("state = %q, want queued once the secret is set", detail.State)
	}
	if detail.PendingSecret != nil {
		t.Fatalf("PendingSecret = %+v, want nothing left to ask for", detail.PendingSecret)
	}
	// PendingSecret now reads "set", which is the other half: a second
	// request for the same name would offer to replace it rather than
	// claim the deployment has nothing.
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingSecret != "" || obs.PendingQuestionCommentID != nil {
		t.Fatalf("observation = %+v, want both the parking and the request cleared", obs)
	}
}

// Answering in words is a legitimate answer to a request for a
// credential ("use the staging key instead"), and it un-parks the task
// -- so the box must stop being offered with it, rather than sitting on
// a queued task waiting to write a value nothing asked for.
func TestReplyingInWordsClearsAPendingSecret(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	withSecrets(t, c)
	task := parkedOnASecret(t, c, store, ctx, "stripe-api-key")

	if err := c.AddComment(ctx, task.ID, "use the staging key, it is already set", nil); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateQueued {
		t.Fatalf("state = %q, want queued", detail.State)
	}
	if detail.PendingSecret != nil {
		t.Fatalf("PendingSecret = %+v, want it cleared by the reply", detail.PendingSecret)
	}
}

// The name is the task's, never the request's: there is no way to spend
// this endpoint on a secret no run asked for.
func TestSetPendingSecretRefusesATaskThatIsNotWaitingOnOne(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	withSecrets(t, c)
	task := create(t, c, ctx)

	err := c.SetPendingSecret(ctx, task.ID, "sk_live_secret")
	var ve *ui.ValidationError
	if err == nil || !errors.As(err, &ve) {
		t.Fatalf("SetPendingSecret on an unparked task = %v, want a validation error", err)
	}
}

func TestSetPendingSecretNeedsAValue(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	withSecrets(t, c)
	task := parkedOnASecret(t, c, store, ctx, "stripe-api-key")

	err := c.SetPendingSecret(ctx, task.ID, "")
	var ve *ui.ValidationError
	if err == nil || !errors.As(err, &ve) {
		t.Fatalf("SetPendingSecret with no value = %v, want a validation error", err)
	}
}

// A UI with no secrets directory of its own (`grain demo`) can still
// show the request -- it is a real fact about the task -- but has
// nowhere to put a value, and says so rather than pretending to store
// one.
func TestSetPendingSecretWithoutASecretsStoreIsNotFound(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := parkedOnASecret(t, c, store, ctx, "stripe-api-key")

	err := c.SetPendingSecret(ctx, task.ID, "sk_live_secret")
	var nf *ui.NotFoundError
	if err == nil || !errors.As(err, &nf) {
		t.Fatalf("SetPendingSecret with no secrets store = %v, want a not-found error", err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.PendingSecret == nil {
		t.Fatal("PendingSecret = nil, want the request still reported on a UI that cannot write it")
	}
}

// The HTTP surface, once: the endpoint is a PUT on the task, its
// response is the task (queued again, nothing pending), and the value is
// nowhere in it.
func TestSetTaskSecretEndpointAnswersWithTheUnparkedTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	withSecrets(t, c)
	task := parkedOnASecret(t, c, store, ctx, "stripe-api-key")
	srv := ui.NewServerWithClient(c)

	rec := do(t, srv, http.MethodPut, "/api/tasks/"+task.ID+"/secret", `{"value":"sk_live_secret"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk_live_secret") {
		t.Fatalf("response leaked the value: %s", rec.Body)
	}
	got := decode[ui.TaskDetail](t, rec)
	if got.State != model.StateQueued {
		t.Fatalf("state = %q, want queued", got.State)
	}
	if got.PendingSecret != nil {
		t.Fatalf("pendingSecret = %+v, want nothing pending", got.PendingSecret)
	}
}
