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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type sseFrame struct {
	id      string
	event   string
	data    string
	comment string
}

func doEventsJSON(t *testing.T, srv *ui.Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var headers map[string]string
	if body != "" {
		headers = map[string]string{"Content-Type": "application/json"}
	}
	return doWithHeaders(t, srv, method, path, body, headers)
}

func readSSEFrames(ctx context.Context, body *bufio.Reader, out chan<- sseFrame) {
	defer close(out)
	var cur sseFrame
	for {
		line, err := body.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if cur.id != "" || cur.event != "" || cur.data != "" || cur.comment != "" {
				select {
				case out <- cur:
				case <-ctx.Done():
					return
				}
				cur = sseFrame{}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			c := strings.TrimSpace(strings.TrimPrefix(line, ":"))
			if cur.comment == "" {
				cur.comment = c
			} else {
				cur.comment += "\n" + c
			}
			continue
		}
		field, val, _ := strings.Cut(line, ":")
		val = strings.TrimPrefix(val, " ")
		switch field {
		case "id":
			cur.id = val
		case "event":
			cur.event = val
		case "data":
			if cur.data == "" {
				cur.data = val
			} else {
				cur.data += "\n" + val
			}
		}
	}
}

func waitSSEEvent(t *testing.T, ch <-chan sseFrame, timeout time.Duration) ui.IntegrationEvent {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatal("SSE stream closed before expected event")
			}
			if f.data == "" {
				continue
			}
			var ev ui.IntegrationEvent
			if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
				t.Fatalf("decoding SSE event data %q: %v", f.data, err)
			}
			if f.id != ev.ID {
				t.Fatalf("SSE frame id = %q, event.ID = %q", f.id, ev.ID)
			}
			if f.event != string(ev.Type) {
				t.Fatalf("SSE frame event = %q, event.Type = %q", f.event, ev.Type)
			}
			return ev
		case <-deadline.C:
			t.Fatalf("timed out after %s waiting for SSE event", timeout)
			return ui.IntegrationEvent{}
		}
	}
}

func TestEventsLifecycleOrderingPayloadAndNoSecretOrTranscriptLeak(t *testing.T) {
	ui.LogToTest(t)
	ctx := context.Background()
	_, store, _ := testClient(t)
	secretStore := secrets.New(t.TempDir())
	client := ui.NewClient(ui.Config{
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secretStore,
	}, store)
	storeSettingsRow(t, client)
	srv := ui.NewServerWithClient(client)

	const (
		secretDescription = "TASK_DESCRIPTION_DO_NOT_LEAK_771"
		secretPrompt      = "RUN_PROMPT_DO_NOT_LEAK_772"
		secretTranscript  = "RUN_TRANSCRIPT_DO_NOT_LEAK_773"
		secretCommentBody = "COMMENT_MARKDOWN_BODY_DO_NOT_LEAK_774"
		secretQuestion    = "QUESTION_DETAIL_BODY_DO_NOT_LEAK_775"
		secretHandover    = "HANDOVER_INTERNAL_NOTE_DO_NOT_LEAK_776"
		secretValue       = "sk_live_NEVER_LEAK_THIS_CREDENTIAL_777"
		secretCILog       = "GITHUB_CI_RAW_STDOUT_DO_NOT_LEAK_778"
		secretCIFailure   = "GITHUB_CI_FAILURE_TAIL_DO_NOT_LEAK_779"
	)

	createdAt := baseTime
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := client.Store.PutTask(ctx, model.Task{
		ID:         "500",
		Title:      "[PLC] Wire board integration",
		Body:       secretDescription,
		Target:     &target,
		Branch:     "grain/test/task-500",
		CreatedAt:  &createdAt,
		FiledVia:   "plc-studio",
		WorkOrigin: "acme/widgets#500",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}},
		},
		Links: []model.Link{
			{Kind: model.LinkGoogleChat, Target: "spaces/AAA111"},
		},
		CustomFields: map[string][]string{
			"external_ref": {"PLC-500"},
		},
	}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	// Approve -> queued.
	approvedAt := baseTime.Add(time.Minute)
	if err := client.Store.Approve(ctx, "500", model.Attribution{
		Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com"},
	}, approvedAt); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	// Defer -> deferred, then undefer -> queued.
	deferAt := baseTime.Add(2 * time.Minute)
	if err := client.Store.SetDeferred(ctx, "500", &deferAt, nil, deferAt); err != nil {
		t.Fatalf("SetDeferred(defer): %v", err)
	}
	undeferAt := baseTime.Add(3 * time.Minute)
	if err := client.Store.SetDeferred(ctx, "500", nil, nil, undeferAt); err != nil {
		t.Fatalf("SetDeferred(undefer): %v", err)
	}

	// Start run -> running, with prompt and transcript that must never appear on /api/events.
	runStart := baseTime.Add(4 * time.Minute)
	if err := client.Store.StartRun(ctx, model.Run{
		ID:        "500-1",
		TaskID:    "500",
		Sandbox:   "sb-500-1",
		Attempt:   1,
		StartedAt: runStart,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := client.Store.SetRunPrompt(ctx, "500-1", secretPrompt); err != nil {
		t.Fatalf("SetRunPrompt: %v", err)
	}
	if err := client.Store.SetRunTranscript(ctx, "500-1", secretTranscript); err != nil {
		t.Fatalf("SetRunTranscript: %v", err)
	}

	// Ordinary comment -> task.comment (carries commentId, never body).
	commentID, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    "500",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "carol@example.com"}},
		Body:      secretCommentBody,
		CreatedAt: baseTime.Add(5 * time.Minute),
		FiledVia:  "gchat",
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	// Internal handover and turn-stopped notes must NOT emit integration events.
	if _, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    "500",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "500"}},
		Body:      secretHandover,
		Handover:  true,
		CreatedAt: baseTime.Add(5*time.Minute + 10*time.Second),
	}); err != nil {
		t.Fatalf("AddComment(handover): %v", err)
	}
	if _, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    "500",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      model.TurnStoppedComment,
		CreatedAt: baseTime.Add(5*time.Minute + 20*time.Second),
	}); err != nil {
		t.Fatalf("AddComment(turn stopped): %v", err)
	}

	// Finish run and park on a question -> task.question + task.state(awaiting_reply).
	runEnd := baseTime.Add(6 * time.Minute)
	if err := client.Store.FinishRun(ctx, "500-1", runEnd, "succeeded", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	questionID, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    "500",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "500"}},
		Summary:   "Keep v1 webhook bridge or remove it?",
		Body:      secretQuestion,
		CreatedAt: runEnd,
	})
	if err != nil {
		t.Fatalf("AddComment(question): %v", err)
	}
	if err := client.Store.Observe(ctx, model.Observation{
		TaskID:                   "500",
		PendingQuestionCommentID: &questionID,
		ObservedAt:               &runEnd,
	}); err != nil {
		t.Fatalf("Observe(question): %v", err)
	}

	// Human replies via HTTP POST /api/tasks/500/reply -> task.comment + task.state(queued).
	replyRec := doEventsJSON(t, srv, http.MethodPost, "/api/tasks/500/reply", `{"body":"Remove v1."}`)
	if replyRec.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks/500/reply: %d %s", replyRec.Code, replyRec.Body.String())
	}

	// Park on a secret request and answer via PUT /api/tasks/500/secret -> secret value must never leak.
	secCommentID, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    "500",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "500"}},
		Summary:   "Set webhook-signing-key so integration tests can verify signatures",
		Body:      secretQuestion,
		CreatedAt: baseTime.Add(7 * time.Minute),
	})
	if err != nil {
		t.Fatalf("AddComment(secret request): %v", err)
	}
	secAt := baseTime.Add(7 * time.Minute)
	if err := client.Store.Observe(ctx, model.Observation{
		TaskID:                   "500",
		PendingQuestionCommentID: &secCommentID,
		PendingSecret:            "webhook-signing-key",
		ObservedAt:               &secAt,
	}); err != nil {
		t.Fatalf("Observe(secret): %v", err)
	}
	secretRec := doEventsJSON(t, srv, http.MethodPut, "/api/tasks/500/secret", `{"value":"`+secretValue+`"}`)
	if secretRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/tasks/500/secret: %d %s", secretRec.Code, secretRec.Body.String())
	}

	// Record grain CI verdict and GitHub CI verdict (with failure logs that must never leak).
	ciDone := baseTime.Add(8 * time.Minute)
	if err := client.Store.ObserveField(ctx, "500", ciDone, func(o *model.Observation) {
		o.CI = &model.CIStatus{
			RunID:      "ci-run-500",
			Commit:     "deadbeef1234",
			Conclusion: "success",
			FinishedAt: &ciDone,
			Detail:     "unit + lint passed",
		}
	}); err != nil {
		t.Fatalf("ObserveField(CI): %v", err)
	}
	if err := client.Store.PutGitHubCIJob(ctx, model.GitHubCIJob{
		Repo:        "acme/widgets",
		CheckRunID:  9001,
		RunID:       4400,
		RunAttempt:  1,
		Workflow:    "CI",
		JobName:     "e2e",
		Ref:         "refs/heads/grain/test/task-500",
		Commit:      "deadbeef1234",
		Conclusion:  "failure",
		CompletedAt: &ciDone,
		UpdatedAt:   ciDone,
		Output:      secretCILog,
		FailureTail: secretCIFailure,
	}); err != nil {
		t.Fatalf("PutGitHubCIJob: %v", err)
	}

	// Link PR, mark completed (auto_merge=false -> awaiting_submit), then close via HTTP.
	if err := client.Store.UpdateTask(ctx, "500", func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#88"})
		return nil
	}); err != nil {
		t.Fatalf("UpdateTask(PR link): %v", err)
	}
	completedAt := baseTime.Add(9 * time.Minute)
	if err := client.Store.ObserveField(ctx, "500", completedAt, func(o *model.Observation) {
		o.CompletedAt = &completedAt
	}); err != nil {
		t.Fatalf("ObserveField(completed): %v", err)
	}
	closeRec := doEventsJSON(t, srv, http.MethodPost, "/api/tasks/500/close", `{"reason":"shipped"}`)
	if closeRec.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks/500/close: %d %s", closeRec.Code, closeRec.Body.String())
	}

	rec := doEventsJSON(t, srv, http.MethodGet, "/api/events?since=0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/events: %d %s", rec.Code, rec.Body.String())
	}
	rawBody := rec.Body.String()
	for _, forbidden := range []string{
		secretDescription,
		secretPrompt,
		secretTranscript,
		secretCommentBody,
		secretQuestion,
		secretHandover,
		secretValue,
		secretCILog,
		secretCIFailure,
		"Remove v1.",
	} {
		if strings.Contains(rawBody, forbidden) {
			t.Fatalf("GET /api/events leaked %q in response: %s", forbidden, rawBody)
		}
	}

	var resp ui.EventsResponse
	if err := json.Unmarshal([]byte(rawBody), &resp); err != nil {
		t.Fatalf("decoding EventsResponse: %v", err)
	}
	if len(resp.Events) == 0 {
		t.Fatal("expected lifecycle events, got none")
	}

	// Verify strictly increasing integer IDs and expected externalRef / metadata.
	var prevID int64
	var stateSeq []string
	var sawComment, sawQuestion, sawGrainCI, sawGitHubCI bool
	for _, ev := range resp.Events {
		id, err := strconv.ParseInt(ev.ID, 10, 64)
		if err != nil || id <= prevID {
			t.Fatalf("non-monotonic event ID %q after %d", ev.ID, prevID)
		}
		prevID = id
		if ev.TaskID != "500" {
			t.Fatalf("TaskID = %q, want 500", ev.TaskID)
		}
		if ev.Repo != "acme/widgets" {
			t.Fatalf("Repo = %q, want acme/widgets", ev.Repo)
		}
		if ev.ExternalRef != "spaces/AAA111" {
			t.Fatalf("ExternalRef = %q, want spaces/AAA111", ev.ExternalRef)
		}
		if ev.Origin != "acme/widgets#500" {
			t.Fatalf("Origin = %q, want acme/widgets#500", ev.Origin)
		}
		if ev.Actor == "" {
			t.Fatalf("event %s (%s) missing actor", ev.ID, ev.Type)
		}

		switch ev.Type {
		case model.IntegrationEventTaskState:
			stateSeq = append(stateSeq, string(ev.PreviousState)+"->"+string(ev.State))
			if ev.State == model.StateAwaitingReply && ev.QuestionSummary == "" {
				t.Fatalf("awaiting_reply transition %s missing questionSummary", ev.ID)
			}
			if ev.State == model.StateAwaitingSubmit && ev.PullRequest != "acme/widgets#88" {
				t.Fatalf("awaiting_submit PullRequest = %q, want acme/widgets#88", ev.PullRequest)
			}
		case model.IntegrationEventTaskComment:
			if ev.CommentID == commentID {
				sawComment = true
				if ev.FiledVia != "gchat" {
					t.Fatalf("comment FiledVia = %q, want gchat", ev.FiledVia)
				}
			}
		case model.IntegrationEventTaskQuestion:
			if ev.CommentID == questionID {
				sawQuestion = true
				if ev.QuestionSummary != "Keep v1 webhook bridge or remove it?" {
					t.Fatalf("QuestionSummary = %q", ev.QuestionSummary)
				}
			}
		case model.IntegrationEventTaskCI:
			switch ev.CIProvider {
			case "grain":
				sawGrainCI = true
				if ev.CIConclusion != "success" || ev.CIRunID != "ci-run-500" || ev.CICommit != "deadbeef1234" {
					t.Fatalf("unexpected grain CI event: %+v", ev)
				}
			case "github":
				sawGitHubCI = true
				if ev.CIConclusion != "failure" || ev.CIRunID != "gha:acme/widgets:4400" || ev.CIDetail != "CI / e2e" {
					t.Fatalf("unexpected github CI event: %+v", ev)
				}
			}
		}
	}

	wantStateSeq := []string{
		"->proposed",
		"proposed->queued",
		"queued->deferred",
		"deferred->queued",
		"queued->running",
		"running->queued",
		"queued->awaiting_reply",
		"awaiting_reply->queued",
		"queued->awaiting_reply",
		"awaiting_reply->queued",
		"queued->awaiting_submit",
		"awaiting_submit->closed",
	}
	if strings.Join(stateSeq, ",") != strings.Join(wantStateSeq, ",") {
		t.Fatalf("state transition sequence:\n got %v\nwant %v", stateSeq, wantStateSeq)
	}
	if !sawComment || !sawQuestion || !sawGrainCI || !sawGitHubCI {
		t.Fatalf("missing event kinds: comment=%v question=%v grainCI=%v githubCI=%v",
			sawComment, sawQuestion, sawGrainCI, sawGitHubCI)
	}

	// Verify ?types= filter returns only requested event types.
	ciOnlyRec := doEventsJSON(t, srv, http.MethodGet, "/api/events?since=0&types=task.ci", "")
	if ciOnlyRec.Code != http.StatusOK {
		t.Fatalf("GET /api/events?types=task.ci: %d %s", ciOnlyRec.Code, ciOnlyRec.Body.String())
	}
	var ciOnly ui.EventsResponse
	if err := json.Unmarshal(ciOnlyRec.Body.Bytes(), &ciOnly); err != nil {
		t.Fatal(err)
	}
	if len(ciOnly.Events) != 2 {
		t.Fatalf("len(ciOnly.Events) = %d, want 2", len(ciOnly.Events))
	}

	// Verify invalid query parameters return 400.
	for _, bad := range []string{
		"/api/events?since=-1",
		"/api/events?since=bogus",
		"/api/events?wait=-2",
		"/api/events?limit=0",
		"/api/events?types=task.unknown",
		"/api/events?stream=maybe",
	} {
		badRec := doEventsJSON(t, srv, http.MethodGet, bad, "")
		if badRec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", bad, badRec.Code)
		}
	}
}

func TestEventsPaginationLongPollAndSSEResumeFromCursor(t *testing.T) {
	ui.LogToTest(t)
	ctx := context.Background()
	srv, client := testServer(t)
	ui.SetEventsKeepaliveForTest(srv, 40*time.Millisecond)

	seedAPITask(t, client, "601", "acme/widgets")
	approve(t, client, "601")
	seedAPITask(t, client, "602", "acme/widgets")
	approve(t, client, "602")
	seedAPITask(t, client, "603", "acme/widgets")

	// Read full baseline in one shot.
	allRec := doEventsJSON(t, srv, http.MethodGet, "/api/events?since=0", "")
	if allRec.Code != http.StatusOK {
		t.Fatalf("GET /api/events?since=0: %d %s", allRec.Code, allRec.Body.String())
	}
	var all ui.EventsResponse
	if err := json.Unmarshal(allRec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Events) != 5 {
		t.Fatalf("len(all.Events) = %d, want 5", len(all.Events))
	}

	// Walk with limit=2 and verify cursor pagination has no gaps or duplicates.
	var paged []ui.IntegrationEvent
	cursor := "0"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		rec := doEventsJSON(t, srv, http.MethodGet, fmt.Sprintf("/api/events?since=%s&limit=2", cursor), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: %d %s", pages, rec.Code, rec.Body.String())
		}
		var page ui.EventsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page.Events...)
		cursor = page.Cursor
		if !page.HasMore {
			break
		}
	}
	if len(paged) != len(all.Events) {
		t.Fatalf("paged %d events, want %d", len(paged), len(all.Events))
	}
	for i := range all.Events {
		if paged[i].ID != all.Events[i].ID {
			t.Fatalf("paged[%d].ID = %q, all[%d].ID = %q", i, paged[i].ID, i, all.Events[i].ID)
		}
	}
	if cursor != all.Cursor {
		t.Fatalf("final cursor = %q, want %q", cursor, all.Cursor)
	}

	// Long-poll timeout when nothing happens.
	timeoutRec := doEventsJSON(t, srv, http.MethodGet, "/api/events?since=now&wait=1", "")
	if timeoutRec.Code != http.StatusOK {
		t.Fatalf("long-poll timeout: %d %s", timeoutRec.Code, timeoutRec.Body.String())
	}
	var timedOutResp ui.EventsResponse
	if err := json.Unmarshal(timeoutRec.Body.Bytes(), &timedOutResp); err != nil {
		t.Fatal(err)
	}
	if !timedOutResp.TimedOut || len(timedOutResp.Events) != 0 || timedOutResp.Cursor != all.Cursor {
		t.Fatalf("unexpected timeout response: %+v", timedOutResp)
	}

	// Long-poll wakes promptly when a transition commits.
	ui.SetAccessCodeForTest(srv, testAccessCode)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	approveDone := make(chan error, 1)
	go func() {
		time.Sleep(60 * time.Millisecond)
		approveDone <- client.Store.UpdateTask(ctx, "603", func(tk *model.Task) error {
			tk.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
			return nil
		})
	}()
	startWait := time.Now()
	lpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?since="+cursor+"&wait=10", nil)
	if err != nil {
		t.Fatal(err)
	}
	lpReq.Header.Set("Authorization", "Bearer "+testAccessCode)
	lpResp, err := ts.Client().Do(lpReq)
	if err != nil {
		t.Fatalf("long-poll GET: %v", err)
	}
	defer lpResp.Body.Close()
	if err := <-approveDone; err != nil {
		t.Fatalf("approving 603: %v", err)
	}
	if lpResp.StatusCode != http.StatusOK {
		t.Fatalf("long-poll status = %d, want 200", lpResp.StatusCode)
	}
	var woke ui.EventsResponse
	if err := json.NewDecoder(lpResp.Body).Decode(&woke); err != nil {
		t.Fatal(err)
	}
	if woke.TimedOut || len(woke.Events) != 1 || woke.Events[0].TaskID != "603" || woke.Events[0].State != model.StateQueued {
		t.Fatalf("unexpected long-poll wakeup response: %+v", woke)
	}
	if elapsed := time.Since(startWait); elapsed > 3*time.Second {
		t.Fatalf("long-poll took %s, expected immediate wakeup on Store.Changed()", elapsed)
	}
	lastSeenID := woke.Cursor

	// Open live SSE stream, observe ": ready" + ": keepalive" + live event, then disconnect.
	sseCtx1, cancelSSE1 := context.WithCancel(ctx)
	req1, err := http.NewRequestWithContext(sseCtx1, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req1.Header.Set("Authorization", "Bearer "+testAccessCode)
	req1.Header.Set("Accept", "text/event-stream")
	res1, err := ts.Client().Do(req1)
	if err != nil {
		t.Fatalf("opening SSE stream 1: %v", err)
	}
	if got := res1.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q, want text/event-stream", got)
	}
	frames1 := make(chan sseFrame, 32)
	go readSSEFrames(sseCtx1, bufio.NewReader(res1.Body), frames1)

	// Wait for ": ready" and at least one ": keepalive".
	var sawReady, sawKeepalive bool
	waitDeadline := time.After(2 * time.Second)
	for !sawReady || !sawKeepalive {
		select {
		case f, ok := <-frames1:
			if !ok {
				t.Fatal("SSE stream 1 closed early")
			}
			if strings.HasPrefix(f.comment, "ready cursor="+lastSeenID) {
				sawReady = true
			}
			if f.comment == "keepalive" {
				sawKeepalive = true
			}
		case <-waitDeadline:
			t.Fatalf("timed out waiting for SSE ready (%v) / keepalive (%v)", sawReady, sawKeepalive)
		}
	}

	// Trigger a live event while connected.
	seedAPITask(t, client, "604", "acme/widgets")
	liveEv := waitSSEEvent(t, frames1, 2*time.Second)
	if liveEv.TaskID != "604" || liveEv.State != model.StateProposed {
		t.Fatalf("unexpected live SSE event: %+v", liveEv)
	}
	lastSeenID = liveEv.ID

	// Disconnect SSE stream 1.
	cancelSSE1()
	_ = res1.Body.Close()

	// Trigger 3 events while disconnected.
	approve(t, client, "604")
	qid, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    "604",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "604"}},
		Summary:   "Deploy to staging first?",
		Body:      "Full details in task.",
		CreatedAt: baseTime.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	obsAt := baseTime.Add(10 * time.Minute)
	if err := client.Store.Observe(ctx, model.Observation{
		TaskID:                   "604",
		PendingQuestionCommentID: &qid,
		ObservedAt:               &obsAt,
	}); err != nil {
		t.Fatal(err)
	}

	// Reconnect SSE stream 2 with Last-Event-ID and verify all 3 missed events replay in order, followed by a live event.
	sseCtx2, cancelSSE2 := context.WithCancel(ctx)
	defer cancelSSE2()
	req2, err := http.NewRequestWithContext(sseCtx2, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Authorization", "Bearer "+testAccessCode)
	req2.Header.Set("Accept", "text/event-stream")
	req2.Header.Set("Last-Event-ID", lastSeenID)
	res2, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatalf("opening SSE stream 2: %v", err)
	}
	defer res2.Body.Close()
	frames2 := make(chan sseFrame, 32)
	go readSSEFrames(sseCtx2, bufio.NewReader(res2.Body), frames2)

	replayed1 := waitSSEEvent(t, frames2, 2*time.Second)
	replayed2 := waitSSEEvent(t, frames2, 2*time.Second)
	replayed3 := waitSSEEvent(t, frames2, 2*time.Second)
	if replayed1.Type != model.IntegrationEventTaskState || replayed1.State != model.StateQueued {
		t.Fatalf("replayed1 = %+v, want task.state queued", replayed1)
	}
	if replayed2.Type != model.IntegrationEventTaskQuestion || replayed2.QuestionSummary != "Deploy to staging first?" {
		t.Fatalf("replayed2 = %+v, want task.question", replayed2)
	}
	if replayed3.Type != model.IntegrationEventTaskState || replayed3.State != model.StateAwaitingReply ||
		replayed3.QuestionSummary != "Deploy to staging first?" {
		t.Fatalf("replayed3 = %+v, want task.state awaiting_reply with summary", replayed3)
	}

	// And verify live streaming continues seamlessly right after catch-up replay.
	seedAPITask(t, client, "605", "acme/widgets")
	afterReplay := waitSSEEvent(t, frames2, 2*time.Second)
	if afterReplay.TaskID != "605" || afterReplay.State != model.StateProposed {
		t.Fatalf("afterReplay = %+v, want task 605 proposed", afterReplay)
	}
}

func TestEventsAuthorizationFilteringAcrossPrincipalsAndRoles(t *testing.T) {
	ui.LogToTest(t)
	ctx := context.Background()
	h := newRoleHarness(t)
	installScopeSets(t,
		model.APIScopeSet{
			Name:        "ci-only",
			Description: "CI viewer without task read access",
			Scopes:      []string{"ci:read"},
		},
		model.APIScopeSet{
			Name:        "widgets-reader",
			Description: "Task reader restricted to acme/widgets",
			Scopes:      []string{"tasks:read"},
			Repos:       []string{"acme/widgets"},
		},
	)
	h.allow(t, "user:ci@example.com", "ci-only")
	h.allow(t, "user:widgets@example.com", "widgets-reader")
	h.allow(t, "user:boss@example.com", "grain/operator")

	cfg, err := h.client.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{PollInterval: model.DefaultPollInterval}
	}
	cfg.ExternalAgentAPI = true
	if err := h.client.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}

	// Seed events across acme/widgets, acme/gadgets, acme/secret, and a no-repo task.
	seedAPITask(t, h.client, "701", "acme/widgets", "api:tasks:read@acme/widgets")
	seedAPITask(t, h.client, "702", "acme/gadgets")
	seedAPITask(t, h.client, "703", "acme/secret")
	noRepoCreated := baseTime
	if err := h.client.Store.PutTask(ctx, model.Task{
		ID:        "704",
		Title:     "Deployment-wide no-repo chore",
		CreatedAt: &noRepoCreated,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "boss@example.com"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Issue a multi-repo external-agent token covering acme/widgets + acme/gadgets,
	// and a token without tasks:read.
	h.allow(t, "user:operator@example.com", "grain/operator") // behind IAP a token works only while its owner is listed
	multiRepoTok := issueFSToken(t, h.client, "plc-bridge", []string{"tasks:read"}, "acme/widgets", "acme/gadgets")
	ciOnlyTok := issueFSToken(t, h.client, "ci-watcher", []string{"ci:read"}, "acme/widgets")

	// 1. Unauthenticated external caller -> 401.
	recUnauth := serveFrom(h.srv, "203.0.113.10:443", http.MethodGet, "/api/events", "", nil)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/events: %d, want 401", recUnauth.Code)
	}

	// 2. Unlisted IAP caller -> 403.
	codeStranger, _ := h.call("stranger@example.com", http.MethodGet, "/api/events", "")
	if codeStranger != http.StatusForbidden {
		t.Fatalf("unlisted IAP GET /api/events: %d, want 403", codeStranger)
	}

	// 3. RBAC role and external token lacking tasks:read -> 403.
	codeCIUser, _ := h.call("ci@example.com", http.MethodGet, "/api/events", "")
	if codeCIUser != http.StatusForbidden {
		t.Fatalf("ci-only RBAC GET /api/events: %d, want 403", codeCIUser)
	}
	recCITok := serveFrom(h.srv, "203.0.113.10:443", http.MethodGet, "/api/events", "", map[string]string{
		"Authorization": "Bearer " + ciOnlyTok,
	})
	if recCITok.Code != http.StatusForbidden {
		t.Fatalf("ci-only token GET /api/events: %d, want 403", recCITok.Code)
	}

	// 4. RBAC role limited to acme/widgets: disallowed ?repo= gets 403; unfiltered gets ONLY acme/widgets.
	codeWidgetsSecret, _ := h.call("widgets@example.com", http.MethodGet, "/api/events?repo=acme/secret", "")
	if codeWidgetsSecret != http.StatusForbidden {
		t.Fatalf("widgets-reader GET /api/events?repo=acme/secret: %d, want 403", codeWidgetsSecret)
	}
	codeWidgetsAll, bodyWidgetsAll := h.call("widgets@example.com", http.MethodGet, "/api/events?since=0", "")
	if codeWidgetsAll != http.StatusOK {
		t.Fatalf("widgets-reader GET /api/events?since=0: %d %s", codeWidgetsAll, bodyWidgetsAll)
	}
	if strings.Contains(bodyWidgetsAll, "acme/secret") || strings.Contains(bodyWidgetsAll, "acme/gadgets") || strings.Contains(bodyWidgetsAll, `"704"`) {
		t.Fatalf("widgets-reader response leaked unauthorized tasks: %s", bodyWidgetsAll)
	}
	var widgetsResp ui.EventsResponse
	if err := json.Unmarshal([]byte(bodyWidgetsAll), &widgetsResp); err != nil {
		t.Fatal(err)
	}
	if len(widgetsResp.Events) != 1 || widgetsResp.Events[0].TaskID != "701" {
		t.Fatalf("widgets-reader events = %+v, want only task 701", widgetsResp.Events)
	}
	// Cursor for since=now must reflect only the caller's authorized watermark (task 701's event id), not task 704's higher id.
	codeWidgetsNow, bodyWidgetsNow := h.call("widgets@example.com", http.MethodGet, "/api/events?since=now", "")
	if codeWidgetsNow != http.StatusOK {
		t.Fatalf("widgets-reader GET /api/events?since=now: %d %s", codeWidgetsNow, bodyWidgetsNow)
	}
	var widgetsNow ui.EventsResponse
	if err := json.Unmarshal([]byte(bodyWidgetsNow), &widgetsNow); err != nil {
		t.Fatal(err)
	}
	if widgetsNow.Cursor != widgetsResp.Events[0].ID {
		t.Fatalf("widgets-reader since=now cursor = %q, want %q (must not leak cross-repo sequence ids)",
			widgetsNow.Cursor, widgetsResp.Events[0].ID)
	}

	// 5. Multi-repo external-agent token receives events from BOTH granted repos, and 403s on acme/secret.
	recTokSecret := serveFrom(h.srv, "203.0.113.10:443", http.MethodGet, "/api/events?repo=acme/secret", "", map[string]string{
		"Authorization": "Bearer " + multiRepoTok,
	})
	if recTokSecret.Code != http.StatusForbidden {
		t.Fatalf("multi-repo token ?repo=acme/secret: %d, want 403", recTokSecret.Code)
	}
	recTokAll := serveFrom(h.srv, "203.0.113.10:443", http.MethodGet, "/api/events?since=0", "", map[string]string{
		"Authorization": "Bearer " + multiRepoTok,
	})
	if recTokAll.Code != http.StatusOK {
		t.Fatalf("multi-repo token GET /api/events?since=0: %d %s", recTokAll.Code, recTokAll.Body.String())
	}
	var tokResp ui.EventsResponse
	if err := json.Unmarshal(recTokAll.Body.Bytes(), &tokResp); err != nil {
		t.Fatal(err)
	}
	if len(tokResp.Events) != 2 || tokResp.Events[0].TaskID != "701" || tokResp.Events[1].TaskID != "702" {
		t.Fatalf("multi-repo token events = %+v, want [701, 702]", tokResp.Events)
	}

	// 6. Acting task 701 (holding api:tasks:read@acme/widgets) sees only acme/widgets and 403s on acme/secret.
	recActingSecret := doWithHeaders(t, h.srv, http.MethodGet, "/api/events?repo=acme/secret", "", map[string]string{
		ui.ActingTaskHeader: "701",
	})
	if recActingSecret.Code != http.StatusForbidden {
		t.Fatalf("acting task 701 ?repo=acme/secret: %d, want 403", recActingSecret.Code)
	}
	recActingAll := doWithHeaders(t, h.srv, http.MethodGet, "/api/events?since=0", "", map[string]string{
		ui.ActingTaskHeader: "701",
	})
	if recActingAll.Code != http.StatusOK {
		t.Fatalf("acting task 701 GET /api/events?since=0: %d %s", recActingAll.Code, recActingAll.Body.String())
	}
	var actingResp ui.EventsResponse
	if err := json.Unmarshal(recActingAll.Body.Bytes(), &actingResp); err != nil {
		t.Fatal(err)
	}
	if len(actingResp.Events) != 1 || actingResp.Events[0].TaskID != "701" {
		t.Fatalf("acting task 701 events = %+v, want only [701]", actingResp.Events)
	}

	// 7. Full operator sees all 4 tasks, including acme/secret and no-repo task 704.
	codeBoss, bodyBoss := h.call("boss@example.com", http.MethodGet, "/api/events?since=0", "")
	if codeBoss != http.StatusOK {
		t.Fatalf("operator GET /api/events?since=0: %d %s", codeBoss, bodyBoss)
	}
	var bossResp ui.EventsResponse
	if err := json.Unmarshal([]byte(bodyBoss), &bossResp); err != nil {
		t.Fatal(err)
	}
	if len(bossResp.Events) != 4 {
		t.Fatalf("operator events = %d, want 4", len(bossResp.Events))
	}

	// 8. Live SSE stream as widgets@example.com never delivers acme/secret transitions, only acme/widgets.
	ts := httptest.NewServer(h.srv)
	defer ts.Close()

	sseCtx, cancelSSE := context.WithCancel(ctx)
	defer cancelSSE()
	sseReq, err := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h.as("widgets@example.com") {
		sseReq.Header.Set(k, v)
	}
	sseReq.Header.Set("X-Forwarded-For", "203.0.113.10")
	sseReq.Header.Set("Accept", "text/event-stream")
	sseRes, err := ts.Client().Do(sseReq)
	if err != nil {
		t.Fatalf("opening RBAC SSE stream: %v", err)
	}
	defer sseRes.Body.Close()
	if sseRes.StatusCode != http.StatusOK {
		t.Fatalf("RBAC SSE status = %d, want 200", sseRes.StatusCode)
	}
	sseCh := make(chan sseFrame, 16)
	go readSSEFrames(sseCtx, bufio.NewReader(sseRes.Body), sseCh)

	// Wait for ": ready" so the stream is actively tailing.
	readyTimer := time.NewTimer(2 * time.Second)
	for {
		select {
		case f := <-sseCh:
			if strings.HasPrefix(f.comment, "ready ") {
				goto streamReady
			}
		case <-readyTimer.C:
			t.Fatal("timed out waiting for RBAC SSE ready frame")
		}
	}
streamReady:
	readyTimer.Stop()

	// Trigger unauthorized transition on acme/secret first, then authorized transition on acme/widgets.
	approve(t, h.client, "703")
	approve(t, h.client, "701")

	gotLive := waitSSEEvent(t, sseCh, 2*time.Second)
	if gotLive.TaskID != "701" || gotLive.Repo != "acme/widgets" {
		t.Fatalf("RBAC SSE leaked unauthorized event: %+v", gotLive)
	}
}
