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

package model_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A session given propose_config_change holds grain/config in its
// Writes, which UngrantedWrites would otherwise accept as a write its
// children may inherit. A child that targets or writes grain/config is
// refused auto-approval anyway, whatever the session's permission says
// (director-capabilities spec §3.1.3, gap G5); a child that names it
// nowhere is not.
func TestAutoApprovalRefusalAlwaysHoldsConfigRepoProposals(t *testing.T) {
	config := model.ConfigRepo()
	session := model.Task{
		ID:     "director",
		Tags:   []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Writes: []model.WriteTarget{{Repo: config}},
	}
	other := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, tc := range []struct {
		name  string
		child model.Task
		want  string
	}{
		{"targets grain/config", model.Task{Target: &config}, "it targets grain/config"},
		{"writes grain/config", model.Task{Writes: []model.WriteTarget{{Repo: config}}}, "it writes grain/config"},
		{"both", model.Task{Target: &config, Writes: []model.WriteTarget{{Repo: config}}}, "it targets and writes grain/config"},
		{"case-folded name", model.Task{Target: &model.RepoRef{Owner: "Grain", Name: "Config"}}, "it targets grain/config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := model.UngrantedWrites(tc.child, session); len(got) != 0 {
				t.Fatalf("UngrantedWrites = %v, want none -- the session holds grain/config, which is the case this guards", got)
			}
			got := model.AutoApprovalRefusal(tc.child, session, true, nil)
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "always waits for a person") {
				t.Errorf("AutoApprovalRefusal = %q, want it to say %q and that it always waits for a person", got, tc.want)
			}
			if hold := model.ConfigRepoProposalHold(tc.child); hold != got {
				t.Errorf("ConfigRepoProposalHold = %q, want the refusal %q", hold, got)
			}
		})
	}

	elsewhere := model.Task{Target: &other}
	if got := model.ConfigRepoProposalHold(elsewhere); got != "" {
		t.Errorf("ConfigRepoProposalHold(acme/widgets) = %q, want none", got)
	}
	if got := model.AutoApprovalRefusal(model.Task{}, session, true, nil); got != "" {
		t.Errorf("AutoApprovalRefusal of a repo-less child from a queueing session = %q, want none", got)
	}
}

// The shipped director is offered propose_config_change -- it holds
// grain/config in its Writes and nothing else -- without its children
// inheriting that: a TasksApproved director's proposal naming
// grain/config is still refused.
func TestShippedDirectorWritesOnlyTheConfigRepo(t *testing.T) {
	sessions := model.DefaultConsoleSessions()
	if len(sessions) != 1 || sessions[0].ID != model.DirectorSessionID {
		t.Fatalf("DefaultConsoleSessions = %+v, want the director alone", sessions)
	}
	director := sessions[0]
	if len(director.Writes) != 1 || !model.IsConfigRepo(director.Writes[0].Repo) || director.Writes[0].Base != "" {
		t.Fatalf("director Writes = %v, want grain/config alone", director.Writes)
	}
	director.TasksApproved = true
	parent := model.Task{
		ID:     "director",
		Tags:   []string{model.ConsoleSessionTag(director.ID)},
		Grants: model.ConsoleSessionGrants(director),
		Writes: slices.Clone(director.Writes),
	}
	if !model.ConsoleSessionQueuesProposals([]model.ConsoleSession{director}, parent) {
		t.Fatal("a TasksApproved director does not queue its proposals; the test below would prove nothing")
	}
	child := model.Task{Writes: []model.WriteTarget{{Repo: model.ConfigRepo()}}}
	if got := model.AutoApprovalRefusal(child, parent, true, nil); got == "" {
		t.Error("a TasksApproved director's proposal writing grain/config was not refused auto-approval")
	}
}

// sessionProposal files one task proposed by parent at the given time,
// approved by approver (nil for one waiting on a person).
func sessionProposal(t *testing.T, store *model.Store, ctx context.Context,
	id, parent string, at time.Time, approver *model.Principal) {

	t.Helper()
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: id,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonDirect,
		},
		Binding:   model.BindingDirective,
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: parent}},
		CreatedAt: &at,
	}
	if approver != nil {
		task.Approval = &model.Attribution{Actor: *approver}
		task.ApprovedAt = &at
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
}

func sessionMessage(t *testing.T, store *model.Store, ctx context.Context, taskID string, author model.Principal, at time.Time) {
	t.Helper()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID, Author: model.Attribution{Actor: author}, Body: "please", CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

// A standing session's own permission is bounded per conversation turn
// (gap G12): it counts the proposals queued on ConsoleSessionPrincipal
// since the last message a person sent the session, and a new message
// starts a new count. Proposals a person still has to approve, and ones
// queued on another permission, are not the session's to count; nor does
// the session's own reply start a turn.
func TestExceededSessionTurnLimitCountsSinceThePersonsLastMessage(t *testing.T) {
	store, ctx := open(t)
	limits := model.AutoSubmitLimits{TurnFanOut: 2}
	session := model.ConsoleSessionPrincipal
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "director"}
	start := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)

	proposal(t, store, ctx, "director", "", &human)
	check := func(want model.AutoSubmitLimit, why string) {
		t.Helper()
		got, err := store.ExceededSessionTurnLimit(ctx, "director", limits)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: limit = %q, want %q", why, got, want)
		}
	}

	sessionMessage(t, store, ctx, "director", human, start)
	sessionProposal(t, store, ctx, "turn1-a", "director", start.Add(time.Second), &session)
	sessionProposal(t, store, ctx, "turn1-waiting", "director", start.Add(2*time.Second), nil)
	sessionProposal(t, store, ctx, "turn1-auto", "director", start.Add(3*time.Second), &model.AutoSubmitPrincipal)
	sessionProposal(t, store, ctx, "elsewhere", "other", start.Add(3*time.Second), &session)
	check(model.AutoSubmitWithinLimits, "one queued this turn")

	sessionProposal(t, store, ctx, "turn1-b", "director", start.Add(4*time.Second), &session)
	check(model.AutoSubmitTurnFanOutReached, "two queued this turn")

	// The session talking to itself is not a person asking again.
	sessionMessage(t, store, ctx, "director", agent, start.Add(5*time.Second))
	check(model.AutoSubmitTurnFanOutReached, "after the session's own reply")

	// A person's next message is a fresh turn.
	next := start.Add(time.Minute)
	sessionMessage(t, store, ctx, "director", human, next)
	check(model.AutoSubmitWithinLimits, "a new turn")
	sessionProposal(t, store, ctx, "turn2-a", "director", next.Add(time.Second), &session)
	sessionProposal(t, store, ctx, "turn2-b", "director", next.Add(2*time.Second), &session)
	check(model.AutoSubmitTurnFanOutReached, "two queued in the new turn")
}

// A session nobody has written to yet counts from its beginning, and an
// unset bound is grain's own default rather than none.
func TestExceededSessionTurnLimitWithoutAMessageUsesTheDefault(t *testing.T) {
	store, ctx := open(t)
	session := model.ConsoleSessionPrincipal
	start := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	proposal(t, store, ctx, "director", "", &human)
	for i := range model.DefaultConsoleSessionTurnFanOut {
		sessionProposal(t, store, ctx, "piece-"+string(rune('a'+i)), "director", start.Add(time.Duration(i)*time.Second), &session)
	}
	got, err := store.ExceededSessionTurnLimit(ctx, "director", model.AutoSubmitLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got != model.AutoSubmitTurnFanOutReached {
		t.Errorf("limit = %q at the default turn fan-out, want %q", got, model.AutoSubmitTurnFanOutReached)
	}
}

func TestAutoSubmitLimitNoteForASessionTurn(t *testing.T) {
	note := model.AutoSubmitLimitNote("director-1", model.AutoSubmitTurnFanOutReached, model.AutoSubmitLimits{TurnFanOut: 7})
	for _, want := range []string{"director-1", "standing session", "7 proposals since the last message a person sent it", "Approve it"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to say %q", note, want)
		}
	}
	if strings.Contains(note, "auto-merge job") {
		t.Errorf("note = %q calls a session an auto-merge job", note)
	}
}
