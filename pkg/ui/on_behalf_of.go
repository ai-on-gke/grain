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

package ui

import (
	"context"
	"log"

	"github.com/ai-on-gke/grain/pkg/model"
)

// agentOnBehalfOf is the person a call acting as task acting is made for
// (operator-attribution spec §2.4), read from grain's own records and
// never from the request:
//
//   - a console session acts for whoever wrote the most recent person's
//     message its current turn was shown (sessionTurnAuthor);
//   - a dispatched run acts for the person who approved its task.
//
// Either way only a verified person counts: the access code is shared,
// so a message or an approval made with it names nobody (§6.3), and the
// call has no on-behalf-of. nil means none.
func agentOnBehalfOf(ctx context.Context, store *model.Store, acting model.Task) *model.Principal {
	var who *model.Principal
	if acting.IsConsoleSession() {
		who = sessionTurnAuthor(ctx, store, acting.ID)
	} else if a := acting.Approval; a != nil {
		who = attributedHuman(*a)
	}
	if who == nil || who.Kind != model.PrincipalHuman || !who.Verified() {
		return nil
	}
	return who
}

// attributedHuman is the human principal a recorded attribution names:
// Actor when a human acted directly, or OnBehalfOf when a delegated
// service principal acted on a human's behalf (AU-26). Plain service
// principals, agents and automations return nil (AU-26.4).
func attributedHuman(a model.Attribution) *model.Principal {
	if a.Actor.Kind == model.PrincipalHuman {
		p := a.Actor
		return &p
	}
	if a.Actor.Kind == model.PrincipalService && a.OnBehalfOf != nil && a.OnBehalfOf.Kind == model.PrincipalHuman {
		p := *a.OnBehalfOf
		return &p
	}
	return nil
}

// sessionTurnAuthor is the author of the most recent person's message
// the live turn of session taskID has been shown, which is the message
// it is answering. A turn has been shown every comment up to
// Observation.DeliveredCommentID -- a continuing turn's prompt, and each
// message handed to it since as an addendum -- and, on a turn its run
// was dispatched for, everything in the conversation when that run
// started, which the orchestrator only marks delivered once the run
// ends. A person who writes while the director is still answering
// somebody else is therefore not who that answer is for: several people
// talking to one session each get the turns that answer them. With no
// live run there is no turn to bound, and the most recent person's
// message of all is the one.
func sessionTurnAuthor(ctx context.Context, store *model.Store, taskID string) *model.Principal {
	if store == nil {
		return nil
	}
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		log.Printf("grain api: reading session %s's conversation for who it acts for: %v", taskID, err)
		return nil
	}
	shown := func(model.Comment) bool { return true }
	if live := liveRun(ctx, store, taskID); live != nil {
		var through int64 = -1
		if obs, err := store.GetObservation(ctx, taskID); err != nil {
			log.Printf("grain api: reading session %s's delivered messages for who it acts for: %v", taskID, err)
		} else if obs != nil && obs.DeliveredCommentID != nil {
			through = *obs.DeliveredCommentID
		}
		started := live.StartedAt
		shown = func(c model.Comment) bool { return c.ID <= through || !c.CreatedAt.After(started) }
	}
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		who := attributedHuman(c.Author)
		if who == nil || !shown(c) {
			continue
		}
		return who
	}
	return nil
}

// liveRun is taskID's run that has not finished, the latest started if
// somehow there are several, and nil for none.
func liveRun(ctx context.Context, store *model.Store, taskID string) *model.Run {
	runs, err := store.Runs(ctx, taskID)
	if err != nil {
		log.Printf("grain api: reading session %s's runs for who it acts for: %v", taskID, err)
		return nil
	}
	var live *model.Run
	for i := range runs {
		if runs[i].FinishedAt == nil && (live == nil || runs[i].StartedAt.After(live.StartedAt)) {
			live = &runs[i]
		}
	}
	return live
}

// sessionTitles is a title lookup for sessionLabel over c's store, which
// reads each session task once.
func (c *Client) sessionTitles(ctx context.Context) func(id string) string {
	seen := map[string]string{}
	return func(id string) string {
		if title, ok := seen[id]; ok {
			return title
		}
		var title string
		if t, err := c.Store.GetTask(ctx, id); err == nil && t != nil && t.IsConsoleSession() {
			title = t.Title
		}
		seen[id] = title
		return title
	}
}

// nameSessions relabels every principal of detail that is a console
// session's agent by its session's title (sessionLabel). comments and
// transitions are what detail's Comments and Transitions were built from,
// in the same order.
func (c *Client) nameSessions(ctx context.Context, detail *TaskDetail, t model.Task, comments []model.Comment, transitions []model.Transition) {
	title := c.sessionTitles(ctx)
	if a := t.Origin.Attribution; a.Actor.Source == model.SourceSession {
		detail.Task.AuthorLabel = sessionLabel(a.Actor, a.OnBehalfOf, title)
	}
	if a := t.Approval; a != nil && a.Actor.Source == model.SourceSession && detail.Task.ApprovedByLabel != "" {
		detail.Task.ApprovedByLabel = sessionLabel(a.Actor, a.OnBehalfOf, title)
	}
	for i, cm := range comments {
		if i < len(detail.Comments) && cm.Author.Actor.Source == model.SourceSession && detail.Comments[i].AuthorLabel != "" {
			detail.Comments[i].AuthorLabel = sessionLabel(cm.Author.Actor, cm.Author.OnBehalfOf, title)
		}
	}
	for i, tr := range transitions {
		if i < len(detail.Transitions) && tr.By != nil && tr.By.Source == model.SourceSession {
			label := sessionLabel(*tr.By, tr.OnBehalfOf, title)
			detail.Transitions[i].ActorLabel, detail.Transitions[i].By, detail.Transitions[i].ByLabel = label, label, label
		}
	}
}
