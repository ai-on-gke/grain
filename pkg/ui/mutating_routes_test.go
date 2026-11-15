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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// attributedRoute is one write path's row in operator-attribution spec
// §7.2's matrix: a request that exercises it against what setup
// prepared, and what it recorded as its actor.
type attributedRoute struct {
	// client builds the deployment the route is exercised on. Nil for
	// testClient's.
	client func(t *testing.T) *ui.Client
	// setup prepares what the request acts on and returns the task it
	// acts on, if any. Nil for none.
	setup func(t *testing.T, client *ui.Client, send sender) string
	// path is the request's path, with {id} standing for what setup
	// returned. "" for the pattern's own.
	path string
	// body is the request's JSON body.
	body string
	// agentGrants are what the acting task of the agent API gate's row
	// holds for the call. Nil for a route the gate refuses to any agent:
	// that row then checks the refusal, and that nothing was recorded.
	agentGrants []string
	// idOnly is a write that keeps its principal as a bare ID (spec
	// §1.3.2): only its kind and ID are compared.
	idOnly bool
	// bareID is a write that keeps its principal as one free string
	// (spec §1.3.2), ci_run.cancelled_by: only its ID is compared.
	bareID bool
	// recorded reads back who the write recorded. created is the
	// response's task id, for a route that files one.
	recorded func(t *testing.T, ctx context.Context, client *ui.Client, id, created string) model.Principal
	// serial is a route whose client or setup swaps package-level state
	// (figmaOAuthClient's endpoint), so its rows run one at a time
	// rather than in parallel with the rest of the matrix.
	serial bool
}

// sender issues one request the way a row of the matrix asks.
type sender func(t *testing.T, method, path, body string) *httptest.ResponseRecorder

func fileTask(approved bool) func(*testing.T, *ui.Client, sender) string {
	return func(t *testing.T, client *ui.Client, _ sender) string {
		t.Helper()
		task, err := client.CreateTask(context.Background(), ui.CreateTaskRequest{Title: "attributed", Approved: approved, NoRepo: true})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task.ID
	}
}

// fileDurableTask is an approved durable task: what
// POST /api/tasks/{id}/rollover rolls over.
func fileDurableTask(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	task, err := client.CreateTask(context.Background(), ui.CreateTaskRequest{Title: "attributed", Approved: true,
		NoRepo: true, Admission: string(model.AdmissionDurable)})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task.ID
}

// taskParkedOnQuestion is a task whose run parked it on a question
// (ask_question): what POST /api/tasks/{id}/reply answers.
func taskParkedOnQuestion(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	ctx := context.Background()
	id := fileTask(true)(t, client, nil)
	qid, err := client.Store.AddComment(ctx, model.Comment{TaskID: id, Body: "which one?",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}}, CreatedAt: baseTime})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Store.Observe(ctx, model.Observation{TaskID: id, PendingQuestionCommentID: &qid}); err != nil {
		t.Fatal(err)
	}
	return id
}

// taskHeldForHumanReview is a task a review held for a person's decision:
// what POST /api/tasks/{id}/human-review/release acts on.
func taskHeldForHumanReview(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	id := fileTask(true)(t, client, nil)
	if _, err := client.Store.RaiseHumanReviewHold(context.Background(), id, model.HumanReviewRequest{
		Reason: model.HumanReviewReason{Source: model.HumanReviewRequested, Reason: "a person must decide", ReviewTask: "r1"},
		By:     model.Principal{Kind: model.PrincipalAgent, ID: "r1"},
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// taskWithPullRequest is a task whose pull request the merge queue has
// given up on: what retry-merge and force-merge act on.
func taskWithPullRequest(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	ctx := context.Background()
	id := fileTask(true)(t, client, nil)
	if err := client.Store.UpdateTask(ctx, id, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := baseTime.Add(time.Hour)
	if err := client.Store.Observe(ctx, model.Observation{
		TaskID: id, CompletedAt: &done, MergeQueueBlockedAt: &done, MergeQueueState: "merge_failed",
		CI: &model.CIStatus{RunID: "run-7", Commit: stuckHead, Conclusion: "failure", FinishedAt: &done},
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func lastEvent(t *testing.T, ctx context.Context, client *ui.Client, id, _ string) model.Principal {
	t.Helper()
	events, err := client.Store.TaskEvents(ctx, id)
	if err != nil || len(events) == 0 {
		t.Fatalf("task events of %s: %v, %v", id, events, err)
	}
	return events[len(events)-1].By
}

// newestComment is who wrote the newest comment on the task, leaving out
// grain's own record of an agent's call (agentAPIGate), which follows it.
func newestComment(t *testing.T, ctx context.Context, client *ui.Client, id, _ string) model.Principal {
	t.Helper()
	comments, err := client.Store.Comments(ctx, id)
	if err != nil {
		t.Fatalf("comments on %s: %v", id, err)
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if comments[i].Author.Actor.Kind != model.PrincipalAutomation {
			return comments[i].Author.Actor
		}
	}
	t.Fatalf("no comment on %s: %+v", id, comments)
	return model.Principal{}
}

// lastConfigAudit is who made the newest config_audit row (spec §4.4).
func lastConfigAudit(t *testing.T, ctx context.Context, client *ui.Client, _, _ string) model.Principal {
	t.Helper()
	audit, err := client.Store.ConfigAudit(ctx, 1)
	if err != nil || len(audit) == 0 {
		t.Fatalf("the config audit log: %v, %v", audit, err)
	}
	return audit[0].By
}

// registerHostd puts hostd ws-1 in the registry, as what the hostd
// routes act on.
func registerHostd(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	if _, _, err := client.Store.CreateHostd(context.Background(), model.Hostd{Name: "ws-1"}); err != nil {
		t.Fatalf("registering ws-1: %v", err)
	}
	return "ws-1"
}

func registerEnrollmentCode(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	if _, _, err := client.Store.CreateHostEnrollmentCode(context.Background(), model.HostEnrollmentCode{Name: "team-invite"}); err != nil {
		t.Fatalf("creating enrollment code: %v", err)
	}
	return "team-invite"
}

func registerPendingHostJoin(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	ctx := context.Background()
	_, raw, err := client.Store.CreateHostEnrollmentCode(ctx, model.HostEnrollmentCode{Name: "pending-invite"})
	if err != nil {
		t.Fatalf("creating enrollment code: %v", err)
	}
	jr, err := client.Store.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		PublicKey:  "ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Hostname:   "devbox-1",
		OS:         "linux",
		Arch:       "amd64",
		EnrolledBy: "alice@example.com",
	}, raw, time.Now().UTC())
	if err != nil {
		t.Fatalf("creating pending join request: %v", err)
	}
	return jr.ID
}

// registerRevokedHostd puts hostd ws-1 in the registry, revoked, as what
// deleting a hostd acts on.
func registerRevokedHostd(t *testing.T, client *ui.Client, s sender) string {
	t.Helper()
	name := registerHostd(t, client, s)
	if _, err := client.Store.RevokeHostd(context.Background(), name); err != nil {
		t.Fatalf("revoking ws-1: %v", err)
	}
	return name
}

// releaseClient is approvalServer's deployment: grain CI on
// ai-on-gke/grain, with a fake runner recording what is started.
func releaseClient(t *testing.T) *ui.Client {
	t.Helper()
	_, client, _, _ := approvalServer(t)
	return client
}

// releaseRequester is who the last release run the fake runner started
// was asked for by.
func releaseRequester(t *testing.T, _ context.Context, client *ui.Client, _, _ string) model.Principal {
	t.Helper()
	runner := client.Config.CI.(*fakeCIRunner)
	if len(runner.started) == 0 {
		t.Fatal("no release run started")
	}
	last := runner.started[len(runner.started)-1]
	return model.Principal{Kind: model.PrincipalKind(last.RequestedByKind), ID: last.RequestedBy}
}

// consoleSession adds an ops-director session for the session routes to
// act on.
func consoleSession(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	if _, err := client.CreateConsoleSession(context.Background(), ui.CreateConsoleSessionRequest{ID: "ops-director", Title: "Ops Director"}); err != nil {
		t.Fatalf("creating a console session: %v", err)
	}
	return "ops-director"
}

// noted is who the newest note of kind on the task says took that action
// (model.TaskEventSubmitted and the rest).
func noted(kind model.TaskEventKind) func(*testing.T, context.Context, *ui.Client, string, string) model.Principal {
	return func(t *testing.T, ctx context.Context, client *ui.Client, id, _ string) model.Principal {
		t.Helper()
		events, err := client.Store.TaskEvents(ctx, id)
		if err != nil {
			t.Fatalf("task events of %s: %v", id, err)
		}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Kind == kind {
				return events[i].By
			}
		}
		t.Fatalf("no %s note on %s: %+v", kind, id, events)
		return model.Principal{}
	}
}

// seededTasks are tasks 801 and 802 in the agent's repository, for a
// route whose body names the tasks it acts on: 801 is the one returned.
func seededTasks(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	seedAPITask(t, client, "801", "acme/widgets")
	seedAPITask(t, client, "802", "acme/widgets")
	return "801"
}

// secretsClient is testClient's deployment with a secret store.
func secretsClient(t *testing.T) *ui.Client {
	t.Helper()
	client, _, _ := testClient(t)
	client.Config.Secrets = secrets.New(t.TempDir())
	return client
}

// figmaOAuthClient is secretsClient's deployment with the figma
// capability server configured for one oauth account, "design", whose
// code exchange goes to a fake Figma token endpoint.
func figmaOAuthClient(t *testing.T) *ui.Client {
	t.Helper()
	client := secretsClient(t)
	figma := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oauth/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"user_id_string":"42"}`))
	}))
	t.Cleanup(figma.Close)
	prevBase, prevClient := ui.FigmaOAuthAPIBase, ui.FigmaOAuthHTTPClient
	ui.FigmaOAuthAPIBase, ui.FigmaOAuthHTTPClient = figma.URL, figma.Client()
	t.Cleanup(func() { ui.FigmaOAuthAPIBase, ui.FigmaOAuthHTTPClient = prevBase, prevClient })
	if err := client.Config.Secrets.Set("figma-oauth", "client_secrets", []byte(`{"design":"shh"}`)); err != nil {
		t.Fatal(err)
	}
	mc := model.DefaultConfig()
	mc.MCPServers = map[string]protocol.ServerConfig{
		"https://grain.dev/capabilities/figma": {
			Command: "grain",
			Args:    []string{"capability", "figma"},
			Builtin: "figma",
			Config: map[string]any{
				"accounts":       []any{map[string]any{"name": "design", "auth_mode": "oauth", "client_id": "cid"}},
				"client_secrets": protocol.SecretRef("figma-oauth/client_secrets"),
			},
		},
	}
	if err := client.Store.PutConfig(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	return client
}

// externalToken issues an external agent credential for the token
// routes to act on, and returns its id.
func externalToken(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	ctx := ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"})
	issued, err := client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{Name: "laptop", ScopeSet: "grain/task-admin"})
	if err != nil {
		t.Fatalf("issuing an external agent credential: %v", err)
	}
	return issued.ID
}

// releaseSecret stores release secret NPM_TOKEN on acme/widgets.
func releaseSecret(t *testing.T, client *ui.Client, _ sender) string {
	t.Helper()
	if _, err := client.SetRepoReleaseSecret(context.Background(), "acme/widgets", "NPM_TOKEN", "v"); err != nil {
		t.Fatalf("storing a release secret: %v", err)
	}
	return ""
}

// adopterClient is testClient's deployment with a fake pull-request
// adopter, and adoptedFor who its newest filing was for.
func adopterClient(t *testing.T) *ui.Client {
	t.Helper()
	client, _, _ := testClient(t)
	client.Config.PullRequests = &fakeAdopter{}
	return client
}

func adoptedFor(t *testing.T, _ context.Context, client *ui.Client, _, _ string) model.Principal {
	t.Helper()
	return client.Config.PullRequests.(*fakeAdopter).lastRequester()
}

// ciClient is testClient's deployment with a fake grain CI runner whose
// every cancel succeeds.
func ciClient(t *testing.T) *ui.Client {
	t.Helper()
	client, _, _ := testClient(t)
	client.Config.CI = &fakeCIRunner{cancelOK: true}
	return client
}

// cancelledBy is who the fake runner's last cancel was asked for by.
func cancelledBy(t *testing.T, _ context.Context, client *ui.Client, _, _ string) model.Principal {
	t.Helper()
	runner := client.Config.CI.(*fakeCIRunner)
	if len(runner.sources) == 0 {
		t.Fatal("no CI run cancelled")
	}
	return model.Principal{ID: string(runner.sources[len(runner.sources)-1])}
}

// attributedRoutes are the write paths the matrix covers: spec §3's
// inventory for tasks, the conversation, releases and the merge queue,
// settings, sessions, and through the agent row the agent API.
var attributedRoutes = map[string]attributedRoute{
	"POST /api/pull-requests/adopt": {
		client:      adopterClient,
		body:        `{"pr":"acme/widgets#7"}`,
		agentGrants: []string{"api:merge:write", "api:tasks:write"},
		recorded:    adoptedFor,
	},
	"POST /api/pull-requests/review": {
		client:      adopterClient,
		body:        `{"pr":"acme/widgets#7","design":true}`,
		agentGrants: []string{"api:tasks:write", "api:repos:read"},
		recorded:    adoptedFor,
	},
	"POST /api/pull-requests/submit": {
		setup:    taskWithPullRequest,
		body:     `{"pr":"acme/widgets#42"}`,
		recorded: noted(model.TaskEventSubmitted),
	},
	"POST /api/tasks": {
		body:        `{"title":"filed","noRepo":true}`,
		agentGrants: []string{"api:tasks:write"},
		recorded: func(t *testing.T, ctx context.Context, client *ui.Client, _, created string) model.Principal {
			task, err := client.Store.GetTask(ctx, created)
			if err != nil || task == nil {
				t.Fatalf("the filed task %q: %v, %v", created, task, err)
			}
			return task.Origin.Attribution.Actor
		},
	},
	"PATCH /api/tasks/{id}": {
		setup:       fileTask(true),
		body:        `{"title":"renamed"}`,
		agentGrants: []string{"api:tasks:write"},
		recorded:    newestComment,
	},
	"POST /api/tasks/{id}/comments": {
		setup:       fileTask(true),
		body:        `{"body":"a word"}`,
		agentGrants: []string{"api:tasks:write"},
		recorded:    newestComment,
	},
	"POST /api/tasks/{id}/reply": {
		setup:       taskParkedOnQuestion,
		body:        `{"body":"an answer"}`,
		agentGrants: []string{"api:tasks:write"},
		recorded:    newestComment,
	},
	"POST /api/tasks/{id}/approve": {
		setup: fileTask(false),
		recorded: func(t *testing.T, ctx context.Context, client *ui.Client, id, _ string) model.Principal {
			task, err := client.Store.GetTask(ctx, id)
			if err != nil || task == nil || task.Approval == nil {
				t.Fatalf("the approved task %s: %+v, %v", id, task, err)
			}
			return task.Approval.Actor
		},
	},
	"POST /api/tasks/{id}/pause": {
		setup:       fileTask(true),
		agentGrants: []string{"api:tasks:write"},
		recorded: func(t *testing.T, ctx context.Context, client *ui.Client, id, _ string) model.Principal {
			pause, err := client.Store.GetPause(ctx, id)
			if err != nil || pause == nil {
				t.Fatalf("the pause of %s: %v, %v", id, pause, err)
			}
			return pause.PausedBy
		},
	},
	"POST /api/tasks/{id}/close": {
		setup:       fileTask(true),
		agentGrants: []string{"api:tasks:write"},
		recorded:    lastEvent,
	},
	"POST /api/tasks/{id}/rollover": {
		setup:       fileDurableTask,
		agentGrants: []string{"api:dispatch:write"},
		recorded:    lastEvent,
	},
	"POST /api/tasks/{id}/reopen": {
		setup: func(t *testing.T, client *ui.Client, send sender) string {
			id := fileTask(true)(t, client, send)
			if err := client.Close(context.Background(), id, ui.CloseOptions{}); err != nil {
				t.Fatalf("closing %s: %v", id, err)
			}
			return id
		},
		agentGrants: []string{"api:tasks:write"},
		recorded:    lastEvent,
	},
	"POST /api/tasks/{id}/retry-merge": {
		setup:       taskWithPullRequest,
		agentGrants: []string{"api:merge:write"},
		recorded:    newestComment,
	},
	"POST /api/tasks/{id}/force-merge": {
		setup:    taskWithPullRequest,
		body:     `{"reason":"the fix for a broken release"}`,
		recorded: newestComment,
	},
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/approve": {
		client:   releaseClient,
		path:     "/api/repos/ai-on-gke/grain/pending-releases/v2.4.0/approve",
		idOnly:   true,
		recorded: releaseRequester,
	},
	"POST /api/releases/{version}/rollback": {
		client:   releaseClient,
		path:     "/api/releases/v2.4.0/rollback",
		idOnly:   true,
		recorded: releaseRequester,
	},
	"POST /api/tasks/{id}/withdraw-approval": {
		setup:       fileTask(true),
		agentGrants: []string{"api:tasks:write"},
		recorded:    noted(model.TaskEventApprovalWithdrawn),
	},
	"POST /api/tasks/{id}/submit": {
		setup:    taskWithPullRequest,
		recorded: noted(model.TaskEventSubmitted),
	},
	"POST /api/tasks/{id}/human-review/release": {
		setup:    taskHeldForHumanReview,
		recorded: noted(model.TaskEventHumanReviewReleased),
	},
	"POST /api/tasks/{id}/retry": {
		setup:       fileTask(true),
		agentGrants: []string{"api:tasks:write"},
		recorded:    noted(model.TaskEventRetried),
	},
	"POST /api/tasks/{id}/defer": {
		setup:       fileTask(true),
		body:        `{"deferredUntil":"2026-09-01T00:00:00Z"}`,
		agentGrants: []string{"api:tasks:write"},
		recorded:    noted(model.TaskEventDeferred),
	},
	"POST /api/tasks/{id}/undefer": {
		setup: func(t *testing.T, client *ui.Client, send sender) string {
			id := fileTask(true)(t, client, send)
			if err := client.Defer(context.Background(), id, ui.DeferOptions{}); err != nil {
				t.Fatalf("deferring %s: %v", id, err)
			}
			return id
		},
		agentGrants: []string{"api:tasks:write"},
		recorded:    noted(model.TaskEventUndeferred),
	},
	"POST /api/tasks/{id}/park-until": {
		setup:       seededTasks,
		body:        `{"until":"2026-08-28T13:00:00Z"}`,
		agentGrants: []string{"api:tasks:write"},
		recorded:    noted(model.TaskEventParked),
	},
	"POST /api/tasks/{id}/wait-on": {
		setup:       seededTasks,
		body:        `{"ids":["802"],"park":true}`,
		agentGrants: []string{"api:tasks:write"},
		recorded:    noted(model.TaskEventParked),
	},
	"POST /api/tasks/{id}/depends-on": {
		setup:       seededTasks,
		body:        `{"id":"802","attach":true}`,
		agentGrants: []string{"api:tasks:write"},
		recorded:    noted(model.TaskEventDependencies),
	},
	"POST /api/tasks/reorder": {
		setup:       seededTasks,
		body:        `{"ids":["801"],"afterId":"802"}`,
		agentGrants: []string{"api:tasks:write"},
		recorded:    noted(model.TaskEventReordered),
	},
	"PUT /api/tasks/{id}/secret": {
		client: secretsClient,
		setup: func(t *testing.T, client *ui.Client, send sender) string {
			id := fileTask(true)(t, client, send)
			if err := client.Store.ObserveField(context.Background(), id, baseTime, func(o *model.Observation) {
				o.PendingSecret = "stripe-api-key"
			}); err != nil {
				t.Fatal(err)
			}
			return id
		},
		body:        `{"value":"sk_test_not_logged"}`,
		agentGrants: []string{"api:secrets:write"},
		recorded:    noted(model.TaskEventSecretSet),
	},
	"PUT /api/repos/{owner}/{name}/release-secrets/{secret}": {
		// Over a secret already listed, which writes no repo config for
		// guardConfigWrite to see: only the value changes.
		client:   secretsClient,
		setup:    releaseSecret,
		path:     "/api/repos/acme/widgets/release-secrets/NPM_TOKEN",
		body:     `{"value":"not-logged"}`,
		recorded: lastConfigAudit,
	},
	"DELETE /api/repos/{owner}/{name}/release-secrets/{secret}": {
		client:   secretsClient,
		setup:    releaseSecret,
		path:     "/api/repos/acme/widgets/release-secrets/NPM_TOKEN",
		recorded: lastConfigAudit,
	},
	"POST /api/external-agents/tokens/{id}/revoke": {
		setup:    externalToken,
		recorded: lastConfigAudit,
	},
	"DELETE /api/external-agents/tokens/{id}": {
		setup:    externalToken,
		recorded: lastConfigAudit,
	},
	"POST /api/external-agents/tokens/revoke-all": {
		setup:    externalToken,
		recorded: lastConfigAudit,
	},
	"DELETE /api/ci/runs/{id}": {
		client:   ciClient,
		path:     "/api/ci/runs/run-7",
		bareID:   true,
		recorded: cancelledBy,
	},
	"PUT /api/memory": {
		body:        `{"text":"remember this"}`,
		agentGrants: []string{"api:memory"},
		recorded: func(t *testing.T, ctx context.Context, client *ui.Client, _, _ string) model.Principal {
			mem, err := client.Store.GetMemory(ctx, nil)
			if err != nil || mem == nil {
				t.Fatalf("the memory: %v, %v", mem, err)
			}
			return mem.UpdatedBy
		},
	},
	"PUT /api/release-notes": {
		body:        `{"period":"2026-W43","title":"t","text":"x"}`,
		agentGrants: []string{"api:release-notes"},
		recorded: func(t *testing.T, ctx context.Context, client *ui.Client, _, _ string) model.Principal {
			notes, err := client.Store.ListReleaseNotes(ctx)
			if err != nil || len(notes) == 0 {
				t.Fatalf("the release notes: %v, %v", notes, err)
			}
			return notes[0].UpdatedBy
		},
	},
	// Figma OAuth code exchange stores the account's refresh token in the
	// secret store and writes a config_audit row naming who connected it.
	"POST /api/capability-servers/figma-oauth-exchange": {
		client:      figmaOAuthClient,
		serial:      true,
		body:        `{"id":"https://grain.dev/capabilities/figma","account":"design","code":"c0de","redirectUri":"https://grain.example/settings/capabilities"}`,
		agentGrants: []string{"api:capabilities:admin"},
		recorded:    lastConfigAudit,
	},
	// The hostd registry (Settings -> Hostds): each change is a
	// config_audit entry about hostd:<name>.
	"POST /api/hostds": {
		body:        `{"name":"ws-1"}`,
		agentGrants: []string{"api:capabilities:admin"},
		recorded:    lastConfigAudit,
	},
	"POST /api/hostds/enrollment-codes": {
		body:     `{"name":"team-hosts"}`,
		recorded: lastConfigAudit,
	},
	"POST /api/hostds/enrollment-codes/{name}/rotate": {
		setup:    registerEnrollmentCode,
		path:     "/api/hostds/enrollment-codes/{id}/rotate",
		recorded: lastConfigAudit,
	},
	"POST /api/hostds/enrollment-codes/{name}/revoke": {
		setup:    registerEnrollmentCode,
		path:     "/api/hostds/enrollment-codes/{id}/revoke",
		recorded: lastConfigAudit,
	},
	"POST /api/hostds/pending/{id}/accept": {
		setup:    registerPendingHostJoin,
		body:     `{"name":"devbox-1"}`,
		recorded: lastConfigAudit,
	},
	"POST /api/hostds/pending/{id}/reject": {
		setup:    registerPendingHostJoin,
		recorded: lastConfigAudit,
	},
	"PATCH /api/hostds/{name}": {
		setup:       registerHostd,
		path:        "/api/hostds/{id}",
		body:        `{"owner":"bob@example.com"}`,
		agentGrants: []string{"api:capabilities:admin"},
		recorded:    lastConfigAudit,
	},
	"POST /api/hostds/{name}/rename": {
		setup:       registerHostd,
		path:        "/api/hostds/{id}/rename",
		body:        `{"name":"ws-2"}`,
		agentGrants: []string{"api:capabilities:admin"},
		recorded:    lastConfigAudit,
	},
	"POST /api/hostds/{name}/rotate": {
		setup:       registerHostd,
		path:        "/api/hostds/{id}/rotate",
		agentGrants: []string{"api:capabilities:admin"},
		recorded:    lastConfigAudit,
	},
	"POST /api/hostds/{name}/revoke": {
		setup:       registerHostd,
		path:        "/api/hostds/{id}/revoke",
		agentGrants: []string{"api:capabilities:admin"},
		recorded:    lastConfigAudit,
	},
	"DELETE /api/hostds/{name}": {
		setup:       registerRevokedHostd,
		path:        "/api/hostds/{id}",
		agentGrants: []string{"api:capabilities:admin"},
		recorded:    lastConfigAudit,
	},
	"POST /api/hostds/delete-revoked": {
		setup:       registerRevokedHostd,
		agentGrants: []string{"api:capabilities:admin"},
		recorded:    lastConfigAudit,
	},
	// Agents never change who signs in (docs/design/access-rbac.md §3),
	// and a verified operator assigns only roles with no dangerous scope.
	"POST /api/access/principals": {
		body:     `{"entry":"user:newbie@example.com","role":"grain/viewer"}`,
		recorded: lastConfigAudit,
	},
	"PATCH /api/access/principals/{entry}": {
		setup: func(t *testing.T, client *ui.Client, _ sender) string {
			t.Helper()
			if _, err := client.AllowPrincipal(context.Background(), ui.AllowPrincipalRequest{Entry: "user:temp@example.com", Role: "grain/viewer"}); err != nil {
				t.Fatalf("seeding allowed principal: %v", err)
			}
			return "user:temp@example.com"
		},
		path:     "/api/access/principals/{id}",
		body:     `{"role":"grain/contributor"}`,
		recorded: lastConfigAudit,
	},
	"DELETE /api/access/principals/{entry}": {
		setup: func(t *testing.T, client *ui.Client, _ sender) string {
			t.Helper()
			if _, err := client.AllowPrincipal(context.Background(), ui.AllowPrincipalRequest{Entry: "user:temp@example.com", Role: "grain/viewer"}); err != nil {
				t.Fatalf("seeding allowed principal: %v", err)
			}
			return "user:temp@example.com"
		},
		path:     "/api/access/principals/{id}",
		recorded: lastConfigAudit,
	},
	"POST /api/pipeline-pause": {
		body: `{"reason":"maintenance"}`,
		recorded: func(t *testing.T, ctx context.Context, client *ui.Client, _, _ string) model.Principal {
			p, err := client.Store.GetPipelinePause(ctx)
			if err != nil || p == nil {
				t.Fatalf("the pipeline pause: %v, %v", p, err)
			}
			return p.By
		},
	},
	"DELETE /api/upgrade/current": {
		client: func(t *testing.T) *ui.Client {
			t.Helper()
			client, _, _ := testClient(t)
			dir := t.TempDir()
			up := upgrade.New(upgrade.Config{
				GraceWindow: 30 * time.Second,
				Image: &upgrade.ImageConfig{
					Repository: "docker.io/grainai/grain",
					RefFile:    dir + "/image.env",
					PullCmd:    []string{"true"},
				},
				StatusFile: dir + "/upgrade-status.json",
			})
			t.Cleanup(func() { _, _ = up.Cancel("test-cleanup", "") })
			client.Config.Upgrader = up
			return client
		},
		setup: func(t *testing.T, client *ui.Client, _ sender) string {
			t.Helper()
			up := client.Config.Upgrader.(*upgrade.Upgrader)
			_, _ = up.Cancel("test-reset", "")
			if err := up.Start("main"); err != nil {
				t.Fatalf("starting upgrade for cancel test: %v", err)
			}
			return ""
		},
		body:        `{"reason":"started by mistake"}`,
		agentGrants: []string{"api:deployment:upgrade"},
		recorded:    lastConfigAudit,
	},
	"PUT /api/settings": {
		setup: func(t *testing.T, client *ui.Client, _ sender) string {
			storeSettingsRow(t, client)
			return ""
		},
		body:        `{"maxWorkers":4}`,
		agentGrants: []string{"api:settings:write"},
		recorded:    lastConfigAudit,
	},
	"PUT /api/usage/settings": {
		body:        `{"mode":"auto","minWorkers":2}`,
		agentGrants: []string{"api:settings:write"},
		recorded:    lastConfigAudit,
	},
	"PUT /api/repos/{owner}/{name}/merge-on-green": {
		path:        "/api/repos/acme/widgets/merge-on-green",
		body:        `{"enabled":true}`,
		agentGrants: []string{"api:repos:write"},
		recorded:    lastConfigAudit,
	},
	"POST /api/templates": {
		body:        `{"name":"T","title":"Fresh title"}`,
		agentGrants: []string{"api:templates:write"},
		recorded:    lastConfigAudit,
	},
	"POST /api/console-sessions": {
		body:     `{"id":"ops-director","title":"Ops Director"}`,
		recorded: lastConfigAudit,
	},
	"PUT /api/console-sessions/{id}": {
		setup:    consoleSession,
		body:     `{"title":"Ops"}`,
		recorded: lastConfigAudit,
	},
	"DELETE /api/console-sessions/{id}": {
		setup:    consoleSession,
		recorded: lastConfigAudit,
	},
	// Accepting a declaration's grants is a person's alone, so the agent
	// row checks the refusal (task 3971).
	"POST /api/declarations/accept": {
		setup: func(t *testing.T, client *ui.Client, _ sender) string {
			t.Helper()
			if _, _, err := client.Store.NoteCIJobDeclaration(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"},
				"publish", "abc123", []string{`gcp#gcp-key={"permission_set":"publisher"}`}, time.Now()); err != nil {
				t.Fatal(err)
			}
			return ""
		},
		body:   `{"id":"ci:acme/widgets:publish","seen":["gcp#gcp-key={\"permission_set\":\"publisher\"}"]}`,
		idOnly: true,
		recorded: func(t *testing.T, ctx context.Context, client *ui.Client, _, _ string) model.Principal {
			acc, err := client.Store.GetDeclarationAcceptance(ctx, "ci:acme/widgets:publish")
			if err != nil || acc == nil || acc.AcceptedAt == nil {
				t.Fatalf("the acceptance: %+v, %v", acc, err)
			}
			return model.Principal{Kind: model.PrincipalHuman, ID: acc.AcceptedBy}
		},
	},
	// Revoking is a person's alone too; the revocation clears who
	// accepted, so who revoked is read from the config audit log.
	"POST /api/declarations/revoke": {
		setup: func(t *testing.T, client *ui.Client, _ sender) string {
			t.Helper()
			ctx := context.Background()
			gcp := `gcp#gcp-key={"permission_set":"publisher"}`
			if _, _, err := client.Store.NoteCIJobDeclaration(ctx, model.RepoRef{Owner: "acme", Name: "widgets"},
				"publish", "abc123", []string{gcp}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Store.AcceptDeclaration(ctx, "ci:acme/widgets:publish", "alice@example.com",
				[]string{"capabilities:admin"}, []string{gcp}, time.Now()); err != nil {
				t.Fatal(err)
			}
			return ""
		},
		body:     `{"id":"ci:acme/widgets:publish"}`,
		recorded: lastConfigAudit,
	},
}

// neverAttributedRoutes are non-GET routes that write nothing a person
// is credited with: signing in and out, and requests that only check,
// plan or preview a change without making it.
var neverAttributedRoutes = map[string]bool{
	// A review's request for human review is recorded as that review's
	// agent, whoever relays it (its run's MCP server reaches the daemon
	// as the daemon): the hold is the review's, and the request's own
	// principal is checked rather than recorded (ui.Client.RequestHumanReview).
	"POST /api/tasks/{id}/human-review": true,
	// Signing in and out: there is nobody to credit until it succeeds,
	// and nothing is written that names them.
	"POST /api/auth/access-code": true,
	"POST /api/auth/logout":      true,
	// A read that is a POST only because its input is a body of lines:
	// it writes nothing (handleRedactSecrets).
	"POST /api/secrets/redact": true,
	// Re-running a GitHub Actions run writes nothing grain keeps: the new
	// attempt is GitHub's own, and the daemon logs who asked for it
	// (handleRerunCIRun).
	"POST /api/ci/runs/{id}/rerun": true,
	// Self-authenticating Ed25519 host handshake and silent credential
	// renewal protocol endpoints; operator approval is attributed on
	// POST /api/hostds/pending/{id}/accept and .../reject.
	"POST /api/hostd/join":           true,
	"POST /api/hostd/join/{id}/poll": true,
	"POST /api/hostd/renew":          true,
	// Checks, tests and previews: they report what a change would do and
	// write nothing.
	"POST /api/agent-keys/{framework}/check": true,
	"POST /api/capability-grants/check":      true,
	"POST /api/capability-policy/preview":    true,
	"POST /api/capability-servers/test":      true,
	// Figma's account check calls Figma's /v1/me; authorize only issues an
	// in-memory, single-use OAuth state. Neither persists anything.
	"POST /api/capability-servers/figma-test":            true,
	"POST /api/capability-servers/figma-oauth-authorize": true,
	"POST /api/deployment/git-connections/check":         true,
	"POST /api/registry/heartbeat":                       true,
	"POST /api/settings/plan":                            true,
	"POST /api/repos/plan":                               true,
	// Refreshing a connection's discovered models writes only grain's
	// cache of what the provider said (model.AgentBackend.Discovered),
	// which RunModelDiscovery rewrites on its own tick with no actor;
	// nothing a person configured changes.
	"POST /api/deployment/agent-backends/{backendId}/discover": true,
	// The MCP endpoints are transports: each tool they serve calls one of
	// the routes here, and is attributed there. POST /api/mcp's
	// grain_invoke dispatches the route through s.mux with the outer
	// request's principal and acting task (dispatchSubrequest).
	"POST /mcp":     true,
	"POST /api/mcp": true,
}

// notYetAttributedRoutes are the rest of spec §3's write paths, exempt
// until the matrix has a case for them. Each is one of: a path that
// already records RequestActor (§3 "actor") with no row here yet; one
// that records the operator as a bare string (§3 "coarse"); or one that
// records no actor at all (§3 "none"), which §3's Target column gives a
// note or a config_audit row. This list only shrinks: a route added to
// Server goes into attributedRoutes, or onto neverAttributedRoutes with
// its reason, never here.
var notYetAttributedRoutes = map[string]bool{
	"DELETE /api/agent-backends/{id}/codex-auth":                           true,
	"DELETE /api/agent-backends/{id}/credential":                           true,
	"DELETE /api/agent-keys/{framework}":                                   true,
	"DELETE /api/boards/{id}":                                              true,
	"DELETE /api/ci/cache":                                                 true,
	"DELETE /api/codex-auth":                                               true,
	"DELETE /api/console/{id}":                                             true,
	"DELETE /api/deployment/agent-backends/{backendId}":                    true,
	"DELETE /api/deployment/git-connections/{cred}":                        true,
	"DELETE /api/gcp-keys/{name}":                                          true,
	"DELETE /api/github-tokens/{name}":                                     true,
	"DELETE /api/pause":                                                    true,
	"DELETE /api/pipeline-pause":                                           true,
	"DELETE /api/repos/{owner}/{name}":                                     true,
	"DELETE /api/repos/{owner}/{name}/releases/{release}":                  true,
	"DELETE /api/schedules/{id}":                                           true,
	"DELETE /api/secrets/{secret}":                                         true,
	"DELETE /api/secrets/{secret}/{key}":                                   true,
	"DELETE /api/suites/{id}":                                              true,
	"DELETE /api/tasks/{id}/goal":                                          true,
	"DELETE /api/templates/{id}":                                           true,
	"DELETE /api/upgrade/scheduled":                                        true,
	"PATCH /api/boards/{id}":                                               true,
	"PATCH /api/schedules/{id}":                                            true,
	"PATCH /api/suites/{id}":                                               true,
	"PATCH /api/templates/{id}":                                            true,
	"POST /api/actions-cache/flush":                                        true,
	"POST /api/api-scope-sets/delete":                                      true,
	"POST /api/boards":                                                     true,
	"POST /api/capability-servers/bind-secret":                             true,
	"POST /api/capability-servers/disable":                                 true,
	"POST /api/capability-servers/dismiss-notice":                          true,
	"POST /api/capability-servers/enable":                                  true,
	"POST /api/capability-servers/gchat-test":                              true,
	"POST /api/capability-servers/remove":                                  true,
	"POST /api/capability-servers/rotate-managed":                          true,
	"POST /api/capability-servers/test-script":                             true,
	"POST /api/ci/runs":                                                    true,
	"POST /api/console-sessions/{id}/cancel":                               true,
	"POST /api/console-sessions/{id}/restart":                              true,
	"POST /api/console/{id}/input":                                         true,
	"POST /api/console/{id}/resize":                                        true,
	"POST /api/deployment/broadcast":                                       true,
	"POST /api/deployment/git-connections":                                 true,
	"POST /api/external-agents/tokens":                                     true,
	"POST /api/external-agents/tokens/{id}/rotate":                         true,
	"POST /api/gcp-keys":                                                   true,
	"POST /api/host/reboot":                                                true,
	"POST /api/host/restart":                                               true,
	"POST /api/host/shutdown":                                              true,
	"POST /api/notifications":                                              true,
	"POST /api/notifications/{id}/ack":                                     true,
	"POST /api/notifications/{id}/clear":                                   true,
	"POST /api/notifications/{id}/investigate":                             true,
	"POST /api/package-cache/flush":                                        true,
	"POST /api/persistent-sandboxes/{name}/{action}":                       true,
	"POST /api/repos":                                                      true,
	"POST /api/repos/{owner}/{name}/base-branch-runs":                      true,
	"POST /api/repos/{owner}/{name}/branches":                              true,
	"POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve": true,
	"POST /api/repos/{owner}/{name}/import-issues":                         true,
	"POST /api/repos/{owner}/{name}/lifecycle/refresh":                     true,
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/reject":     true,
	"POST /api/repos/{owner}/{name}/release-runs":                          true,
	"POST /api/repos/{owner}/{name}/releases":                              true,
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates":         true,
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote": true,
	"POST /api/repos/{owner}/{name}/releases/{release}/merge":              true,
	"POST /api/sandbox-probe":                                              true,
	"POST /api/schedules":                                                  true,
	"POST /api/schedules/composition":                                      true,
	"POST /api/schedules/{id}/run":                                         true,
	"POST /api/settings":                                                   true,
	"POST /api/state-repo/import-key":                                      true,
	"POST /api/state-repo/pull-requests":                                   true,
	"POST /api/state-repo/remote":                                          true,
	"POST /api/state-repo/reset-notice":                                    true,
	"POST /api/state-repo/secrets-key":                                     true,
	"POST /api/state-repo/secrets-key/unwrap":                              true,
	"POST /api/state-repo/secrets-key/wrap":                                true,
	"POST /api/state-repo/secrets-repo":                                    true,
	"POST /api/state-repo/sync":                                            true,
	"POST /api/suite-runs":                                                 true,
	"POST /api/suites":                                                     true,
	"POST /api/suites/composition":                                         true,
	"POST /api/tasks/from-template":                                        true,
	"POST /api/tasks/{id}/activity":                                        true,
	"POST /api/tasks/{id}/agent-messages":                                  true,
	"POST /api/tasks/{id}/capabilities":                                    true,
	"POST /api/tasks/{id}/change-checks":                                   true,
	"POST /api/tasks/{id}/change-mail":                                     true,
	"POST /api/tasks/{id}/change-publish":                                  true,
	"POST /api/tasks/{id}/change-review":                                   true,
	"POST /api/tasks/{id}/change-sync":                                     true,
	"POST /api/tasks/{id}/config-change":                                   true,
	"POST /api/tasks/{id}/config-change/approve":                           true,
	"POST /api/tasks/{id}/config-change/reject":                            true,
	"POST /api/tasks/{id}/access-request/check":                            true,
	"POST /api/tasks/{id}/access-request/grant":                            true,
	"POST /api/tasks/{id}/access-request/deny":                             true,
	"POST /api/tasks/{id}/db/execute":                                      true,
	"POST /api/tasks/{id}/db/query":                                        true,
	"POST /api/tasks/{id}/delivered":                                       true,
	"POST /api/tasks/{id}/local-script-approvals":                          true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/answer":        true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/expire":        true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/wait":          true,
	"POST /api/tasks/{id}/messages":                                        true,
	"POST /api/tasks/{id}/notes":                                           true,
	"POST /api/tasks/{id}/compact":                                         true,
	"POST /api/tasks/{id}/brief":                                           true,
	"POST /api/tasks/{id}/notify":                                          true,
	"POST /api/tasks/{id}/pull-request":                                    true,
	"POST /api/tasks/{id}/resume":                                          true,
	"POST /api/tasks/{id}/resume-later":                                    true,
	"POST /api/tasks/{id}/run-now":                                         true,
	"POST /api/tasks/{id}/sandbox/recreate":                                true,
	"POST /api/tasks/{id}/staged-attachments":                              true,
	"POST /api/tasks/{id}/stop":                                            true,
	"POST /api/tasks/{id}/wait-credit":                                     true,
	"POST /api/tests/quarantine":                                           true,
	"POST /api/upgrade":                                                    true,
	"PUT /api/agent-backends/{id}/codex-auth":                              true,
	"PUT /api/agent-backends/{id}/credential":                              true,
	"PUT /api/agent-keys/{framework}":                                      true,
	"PUT /api/api-scope-sets":                                              true,
	"PUT /api/capability-policy":                                           true,
	"PUT /api/capability-servers":                                          true,
	"PUT /api/codex-auth":                                                  true,
	"PUT /api/deployment":                                                  true,
	"PUT /api/deployment/agent-backends/{backendId}":                       true,
	"PUT /api/deployment/repos/{owner}/{name}/config":                      true,
	"PUT /api/deployment/repos/{owner}/{name}/git-connection":              true,
	"PUT /api/gcp-keys/{name}":                                             true,
	"PUT /api/github-tokens/{name}":                                        true,
	"PUT /api/memory/documents":                                            true,
	"PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates":       true,
	"PUT /api/repos/{owner}/{name}/capabilities":                           true,
	"PUT /api/repos/{owner}/{name}/ci-capabilities":                        true,
	"PUT /api/repos/{owner}/{name}/ci-mode":                                true,
	"PUT /api/repos/{owner}/{name}/declarations":                           true,
	"PUT /api/repos/{owner}/{name}/ignore-github-ci":                       true,
	"PUT /api/repos/{owner}/{name}/prompt-extension":                       true,
	"PUT /api/repos/{owner}/{name}/qualification-plan":                     true,
	"PUT /api/repos/{owner}/{name}/release":                                true,
	"PUT /api/repos/{owner}/{name}/require-branch-up-to-date":              true,
	"PUT /api/repos/{owner}/{name}/settings":                               true,
	"PUT /api/repos/{owner}/{name}/setup-command":                          true,
	"PUT /api/repos/{owner}/{name}/unfixable-checks":                       true,
	"PUT /api/repos/{owner}/{name}/watch-default-branch":                   true,
	"PUT /api/repos/{owner}/{name}/work-ownership":                         true,
	"PUT /api/repos/{owner}/{name}/workflows":                              true,
	"PUT /api/secrets/{secret}/{key}":                                      true,
	"PUT /api/settings/cli-run":                                            true,
	"PUT /api/tasks/{id}/goal":                                             true,
	"PUT /api/tasks/{id}/hostd":                                            true,
	"PUT /api/tasks/{id}/session-title":                                    true,
}

// matrixAgent is the task the agent API gate's row acts as.
const matrixAgent = "900"

// matrixWay is one of the ways spec §7.2 issues every write: who the
// request comes from, and who it must be recorded as.
type matrixWay struct {
	name string
	// headers are what the request carries besides its body's.
	headers func(t *testing.T) map[string]string
	// remoteAddr is where it comes from; "" for in-process.
	remoteAddr string
	// agent is the agent API gate's row.
	agent bool
	want  model.Principal
}

// matrixWays are spec §7.2's five rows, after the in-process caller
// step 1 of the rollout already checked. The fake verifier's row is the
// contract a wrapper distribution's signed-proxy verifier relies on
// (spec §8.3).
func matrixWays(t *testing.T) []matrixWay {
	t.Helper()
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, true)
	ui.SetIAPAudience(tunnelAudience)
	t.Cleanup(ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name: "fake", headers: []string{"X-Fake-Signed-Identity"}, verifyFn: parseSimpleSignedHeader,
	}))
	operator := ui.DefaultActor("alice")
	withSource := func(p model.Principal, s model.AuthSource) model.Principal {
		p.Source = s
		return p
	}
	spoofed := map[string]string{
		"X-Goog-Authenticated-User-Email": "accounts.google.com:mallory@example.com",
		"X-Grain-User":                    "mallory",
		"X-Forwarded-User":                "mallory@example.com",
	}
	return []matrixWay{
		{name: "in-process", want: withSource(operator, model.SourceInProcess)},
		{
			name: "IAP assertion", remoteAddr: "203.0.113.20:443",
			headers: func(t *testing.T) map[string]string {
				exp := time.Now().Add(5 * time.Minute)
				return map[string]string{ui.IAPAssertionHeader: signIAPToken(t, priv, tunnelKid, tunnelAudience, "alice@example.com", "s", ui.GoogleIAPIssuer, exp)}
			},
			want: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP},
		},
		{
			name: "fake verifier", remoteAddr: "203.0.113.21:443",
			headers: func(*testing.T) map[string]string {
				return map[string]string{"X-Fake-Signed-Identity": "ok:Bob@Example.com"}
			},
			want: model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com", Source: "fake"},
		},
		{
			name: "access code", remoteAddr: "198.51.100.7:4242",
			headers: func(*testing.T) map[string]string {
				return map[string]string{"Authorization": "Bearer " + testAccessCode}
			},
			want: withSource(operator, model.SourceAccessCode),
		},
		{
			// An agent never claims a person (spec §6.2), whatever it
			// names: the frontend's own unsigned header rides along.
			name: "agent API gate", agent: true,
			headers: func(*testing.T) map[string]string {
				return map[string]string{ui.ActingTaskHeader: matrixAgent, ui.ActingReasonHeader: "the attribution matrix", "X-Grain-User": "mallory"}
			},
			want: model.Principal{Kind: model.PrincipalAgent, ID: matrixAgent, Source: model.SourceSandbox},
		},
		{
			// Unsigned identity headers are logged and ignored (spec
			// §6.1): the shared access code stays the operator's.
			name: "access code with spoofed headers", remoteAddr: "198.51.100.7:4242",
			headers: func(*testing.T) map[string]string {
				h := map[string]string{"Authorization": "Bearer " + testAccessCode}
				for k, v := range spoofed {
					h[k] = v
				}
				return h
			},
			want: withSource(operator, model.SourceAccessCode),
		},
	}
}

// TestEveryMutatingRouteIsAttributed is spec §7.2 and §7.3: every
// non-GET route Server registers is covered by the matrix or on a
// commented exemption list, and each covered one records the principal
// each of the matrix's ways must be recorded as.
func TestEveryMutatingRouteIsAttributed(t *testing.T) {
	srv := ui.NewServerWithClient(nil)
	registered := map[string]bool{}
	for _, pattern := range srv.Routes() {
		method, _, ok := strings.Cut(pattern, " ")
		if !ok || method == http.MethodGet {
			continue
		}
		registered[pattern] = true
		lists := 0
		for _, on := range []bool{attributedRoutes[pattern].recorded != nil, neverAttributedRoutes[pattern], notYetAttributedRoutes[pattern]} {
			if on {
				lists++
			}
		}
		switch lists {
		case 0:
			t.Errorf("%s is neither attributed (attributedRoutes) nor exempt (neverAttributedRoutes, with its reason): "+
				"a write must record ui.RequestActor", pattern)
		case 1:
		default:
			t.Errorf("%s is on more than one of attributedRoutes, neverAttributedRoutes and notYetAttributedRoutes", pattern)
		}
	}
	for _, list := range []map[string]bool{neverAttributedRoutes, notYetAttributedRoutes} {
		for pattern := range list {
			if !registered[pattern] {
				t.Errorf("%s is exempt, but Server registers no such non-GET route", pattern)
			}
		}
	}

	ways := matrixWays(t)
	for pattern, route := range attributedRoutes {
		if !registered[pattern] {
			t.Errorf("attributedRoutes covers %s, which Server does not register", pattern)
			continue
		}
		for _, way := range ways {
			t.Run(pattern+"/"+way.name, func(t *testing.T) {
				// Each row builds its own store and server, so the
				// matrix's few hundred rows run in parallel: run one
				// after another they took most of the go job's budget.
				if !route.serial {
					t.Parallel()
				}
				var client *ui.Client
				if route.client != nil {
					client = route.client(t)
				} else {
					client, _, _ = testClient(t)
				}
				client.Config.LocalAPIToken = testAccessCode
				client.Config.AllowedPrincipals = []string{"user:alice@example.com", "user:bob@example.com"}
				srv := ui.NewServerWithClient(client)
				ctx := context.Background()
				if way.agent {
					seedAPITask(t, client, matrixAgent, "acme/widgets", route.agentGrants...)
				}
				send := func(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
					t.Helper()
					h := map[string]string{"Content-Type": "application/json"}
					if way.headers != nil {
						for k, v := range way.headers(t) {
							h[k] = v
						}
					}
					if way.remoteAddr == "" {
						return doWithHeaders(t, srv, method, path, body, h)
					}
					return serveFrom(srv, way.remoteAddr, method, path, body, h)
				}
				var id string
				if route.setup != nil {
					id = route.setup(t, client, send)
				}
				method, path, _ := strings.Cut(pattern, " ")
				if route.path != "" {
					path = route.path
				}
				rec := send(t, method, strings.ReplaceAll(path, "{id}", id), route.body)
				if way.agent && route.agentGrants == nil {
					if rec.Code < 400 {
						t.Fatalf("%s by an agent: %d %s, want it refused: the route is a person's", pattern, rec.Code, rec.Body)
					}
					return
				}
				if rec.Code >= 300 {
					t.Fatalf("%s by %s: %d %s", pattern, way.name, rec.Code, rec.Body)
				}
				var created struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &created)
				got, want := route.recorded(t, ctx, client, id, created.ID), way.want
				if route.idOnly || route.bareID {
					got.Source, want.Source = "", ""
				}
				if route.bareID {
					got.Kind, want.Kind = "", ""
				}
				if got != want {
					t.Errorf("%s by %s recorded %+v, want %+v", pattern, way.name, got, want)
				}
			})
		}
	}
}

// TestABodyNeverNamesAPerson is spec §2.3 and §6.1: an author a request
// body names is no verified identity. A verified caller is recorded as
// themselves whatever the body says, and a caller nobody named may not
// name a person at all.
func TestABodyNeverNamesAPerson(t *testing.T) {
	ways := matrixWays(t)
	client, _, ctx := testClient(t)
	client.Config.LocalAPIToken = testAccessCode
	client.Config.AllowedPrincipals = []string{"user:alice@example.com", "user:bob@example.com"}
	srv := ui.NewServerWithClient(client)
	send := func(way matrixWay, body string) *httptest.ResponseRecorder {
		h := map[string]string{"Content-Type": "application/json"}
		for k, v := range way.headers(t) {
			h[k] = v
		}
		return serveFrom(srv, way.remoteAddr, http.MethodPut, "/api/release-notes", body, h)
	}
	claim := `{"period":"2026-W43","title":"t","text":"x","author":"mallory@example.com","authorKind":"human"}`
	for _, way := range ways {
		switch way.name {
		case "IAP assertion", "fake verifier":
			if rec := send(way, claim); rec.Code != http.StatusOK {
				t.Fatalf("%s naming mallory: %d %s", way.name, rec.Code, rec.Body)
			}
			note, err := client.Store.GetReleaseNote(ctx, "2026-W43")
			if err != nil || note == nil || note.UpdatedBy != way.want {
				t.Errorf("%s naming mallory recorded %+v (%v), want %+v", way.name, note, err, way.want)
			}
		case "access code", "access code with spoofed headers":
			if rec := send(way, claim); rec.Code != http.StatusBadRequest {
				t.Errorf("%s naming mallory: %d %s, want 400", way.name, rec.Code, rec.Body)
			}
		}
	}
}

func TestPutDeploymentAgentBackendMismatchID(t *testing.T) {
	srv, _ := testServer(t)
	body := `{"id":"body-id","framework":"antigravity"}`
	rec := do(t, srv, "PUT", "/api/deployment/agent-backends/path-id", body)
	if rec.Code != 400 {
		t.Errorf("mismatched id status = %d, want 400", rec.Code)
	}
}

func TestAddTargetRepoInvalidCloneUrl(t *testing.T) {
	srv, _ := testServer(t)
	body := `{"repo":"acme/widgets","cloneUrl":"invalid-url"}`
	rec := do(t, srv, "POST", "/api/repos", body)
	if rec.Code != 400 {
		t.Errorf("invalid cloneUrl status = %d, want 400", rec.Code)
	}
}
