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
	"errors"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

var errAgentTaskOwner = errors.New("an agent cannot set or transfer task ownership")

// ProposerLabel renders the actor that proposed a task on the timeline's
// Proposed step:
//   - a person (or operator fallback) via PrincipalLabel;
//   - for tasks proposed by a run: the proposing task and run, plus on whose
//     behalf (the task's owner);
//   - for automation-filed tasks: the specific automation principal
//     ("schedule", "trigger", "review", "auto-submit", ...);
//   - "unknown" only when nothing recorded who or what proposed a legacy task.
func ProposerLabel(t model.Transition, attempt int) string {
	if t.By == nil {
		return ""
	}
	if t.ProposingTaskID != "" {
		noun := "task"
		if t.By.Source == model.SourceSession {
			noun = "session"
		}
		who := fmt.Sprintf("%s #%s", noun, t.ProposingTaskID)
		if attempt > 0 {
			who += fmt.Sprintf(" (run #%d)", attempt)
		} else if strings.TrimSpace(t.ProposingRunID) != "" {
			who += fmt.Sprintf(" (run %s)", strings.TrimSpace(t.ProposingRunID))
		}
		if owner := strings.TrimSpace(t.OnBehalfOfOwner); owner != "" {
			who += " on behalf of " + owner
		} else if t.OnBehalfOf != nil && strings.TrimSpace(t.OnBehalfOf.ID) != "" {
			who += " on behalf of " + strings.TrimSpace(t.OnBehalfOf.ID)
		}
		return who
	}
	switch t.By.Kind {
	case model.PrincipalUnknown:
		return "unknown"
	case model.PrincipalAutomation:
		id := strings.TrimSpace(t.By.ID)
		if id != "" && id != "grain" {
			return id
		}
		return "grain"
	case model.PrincipalAgent:
		label := PrincipalLabel(*t.By, t.OnBehalfOf)
		if t.OnBehalfOf != nil && !t.OnBehalfOf.Verified() && strings.TrimSpace(t.OnBehalfOf.ID) != "" {
			label += " on behalf of " + strings.TrimSpace(t.OnBehalfOf.ID)
		}
		return label
	default:
		return PrincipalLabel(*t.By, t.OnBehalfOf)
	}
}

func (c *Client) defaultDeploymentOwner(_ context.Context) string {
	if p := c.operatorPerson(); p != "" {
		if norm, err := model.NormalizeTaskOwner(p); err == nil {
			return norm
		}
	}
	return model.DefaultTaskOwner
}

// latestSessionHumanSender finds the most recent human or service principal
// who posted a message to a console session task so tasks filed from that
// session inherit that person as owner (attribution.design.md).
func (c *Client) latestSessionHumanSender(ctx context.Context, sessionTaskID string) *model.Principal {
	sessionTaskID = strings.TrimSpace(sessionTaskID)
	if c.Store == nil || sessionTaskID == "" {
		return nil
	}
	var before int64
	for {
		page, more, err := c.Store.CommentsBefore(ctx, sessionTaskID, before, 50)
		if err != nil {
			return nil
		}
		for i := len(page) - 1; i >= 0; i-- {
			if h := attributedHuman(page[i].Author); h != nil && strings.TrimSpace(h.ID) != "" {
				if _, nerr := model.NormalizeTaskOwner(h.ID); nerr == nil {
					return h
				}
			}
			actor := page[i].Author.Actor
			if (actor.Kind == model.PrincipalHuman || actor.Kind == model.PrincipalService) && strings.TrimSpace(actor.ID) != "" {
				if _, nerr := model.NormalizeTaskOwner(actor.ID); nerr == nil {
					cp := actor
					return &cp
				}
			}
		}
		if !more || len(page) == 0 {
			return nil
		}
		before = page[0].ID
	}
}

func (c *Client) resolveNewTaskOwner(ctx context.Context, req CreateTaskRequest, proposedBy *model.Task) (string, *model.Principal, error) {
	who := c.requestActor(ctx)
	if asked := strings.TrimSpace(req.Owner); asked != "" {
		if who.Actor.Kind == model.PrincipalAgent {
			return "", nil, forbiddenErrorf("%s", errAgentTaskOwner)
		}
		norm, err := model.NormalizeTaskOwner(asked)
		if err != nil {
			return "", nil, validationErrorf("%v", err)
		}
		return norm, nil, nil
	}

	// Child of a non-session task always inherits its parent's owner.
	if proposedBy != nil && !proposedBy.IsConsoleSession() {
		if norm, err := model.NormalizeTaskOwner(proposedBy.Owner); err == nil {
			return norm, nil, nil
		}
		if norm, err := model.NormalizeTaskOwner(proposedBy.Creator); err == nil {
			return norm, nil, nil
		}
	}

	// Console session (via ProposedBy or agent REST API): use OnBehalfOf or
	// the session's most recent human sender.
	sessionID := ""
	if proposedBy != nil && proposedBy.IsConsoleSession() {
		sessionID = proposedBy.ID
	} else if who.Actor.Kind == model.PrincipalAgent && who.Actor.Source == model.SourceSession {
		sessionID = who.Actor.ID
	} else if call, ok := agentCallFromContext(ctx); ok && call.session {
		sessionID = call.taskID
	}
	if sessionID != "" {
		if who.OnBehalfOf != nil {
			if norm, err := model.NormalizeTaskOwner(who.OnBehalfOf.ID); err == nil {
				cp := *who.OnBehalfOf
				return norm, &cp, nil
			}
		}
		if sender := c.latestSessionHumanSender(ctx, sessionID); sender != nil {
			if norm, err := model.NormalizeTaskOwner(sender.ID); err == nil {
				return norm, sender, nil
			}
		}
		if proposedBy != nil {
			if norm, err := model.NormalizeTaskOwner(proposedBy.Owner); err == nil {
				return norm, nil, nil
			}
		}
		return c.defaultDeploymentOwner(ctx), nil, nil
	}

	// External agent credential (gma_...) or other agent with OnBehalfOf.
	if who.Actor.Kind == model.PrincipalAgent {
		if who.OnBehalfOf != nil {
			if norm, err := model.NormalizeTaskOwner(who.OnBehalfOf.ID); err == nil {
				cp := *who.OnBehalfOf
				return norm, &cp, nil
			}
		}
		if call, ok := agentCallFromContext(ctx); ok && call.taskID != "" && c.Store != nil {
			if acting, err := c.Store.GetTask(ctx, call.taskID); err == nil && acting != nil {
				if norm, nerr := model.NormalizeTaskOwner(acting.Owner); nerr == nil {
					return norm, nil, nil
				}
			}
		}
		return c.defaultDeploymentOwner(ctx), nil, nil
	}

	// Human or service caller (UI, CLI, API, or in-process).
	if person := requestPerson(ctx); person != "" {
		if norm, err := model.NormalizeTaskOwner(person); err == nil {
			return norm, nil, nil
		}
	}
	for _, candidate := range []string{req.Author, req.Creator} {
		if norm, err := model.NormalizeTaskOwner(candidate); err == nil {
			return norm, nil, nil
		}
	}
	return c.defaultDeploymentOwner(ctx), nil, nil
}

func (c *Client) proposerForNewTask(ctx context.Context, owner string, sessionBehalf *model.Principal, proposedBy *model.Task) model.TaskProposer {
	who := c.requestActor(ctx)
	var runID string
	var callSession bool
	var callTaskID string
	if call, ok := agentCallFromContext(ctx); ok {
		runID = strings.TrimSpace(call.runID)
		callSession = call.session
		callTaskID = strings.TrimSpace(call.taskID)
	}

	ownerPrincipal := &model.Principal{Kind: model.PrincipalHuman, ID: owner}
	if sessionBehalf != nil {
		cp := *sessionBehalf
		ownerPrincipal = &cp
	} else if who.OnBehalfOf != nil && strings.EqualFold(who.OnBehalfOf.ID, owner) {
		cp := *who.OnBehalfOf
		ownerPrincipal = &cp
	}

	if proposedBy != nil {
		src := model.SourceSandbox
		if proposedBy.IsConsoleSession() || callSession {
			src = model.SourceSession
		}
		return model.TaskProposer{
			Attribution: model.Attribution{
				Actor:      model.Principal{Kind: model.PrincipalAgent, ID: proposedBy.ID, Source: src},
				OnBehalfOf: ownerPrincipal,
			},
			TaskID: proposedBy.ID,
			RunID:  runID,
		}
	}

	if who.Actor.Kind == model.PrincipalAgent && (who.Actor.Source == model.SourceSandbox || who.Actor.Source == model.SourceSession || callTaskID != "") && who.Actor.Source != model.SourceExternalToken {
		taskID := strings.TrimSpace(who.Actor.ID)
		if taskID == "" {
			taskID = callTaskID
		}
		src := who.Actor.Source
		if src == "" {
			if callSession {
				src = model.SourceSession
			} else {
				src = model.SourceSandbox
			}
		}
		return model.TaskProposer{
			Attribution: model.Attribution{
				Actor:      model.Principal{Kind: model.PrincipalAgent, ID: taskID, Source: src},
				OnBehalfOf: ownerPrincipal,
			},
			TaskID: taskID,
			RunID:  runID,
		}
	}

	if who.Actor.Kind == model.PrincipalAgent && who.Actor.Source == model.SourceExternalToken {
		behalf := who.OnBehalfOf
		if behalf == nil && owner != "" {
			behalf = ownerPrincipal
		}
		return model.TaskProposer{
			Attribution: model.Attribution{
				Actor:      who.Actor,
				OnBehalfOf: behalf,
			},
		}
	}

	return model.TaskProposer{
		Attribution: who,
	}
}

func (c *Client) resolveOwnerFilter(ctx context.Context, raw string) string {
	val := strings.TrimSpace(raw)
	if val == "" {
		return ""
	}
	if strings.EqualFold(val, "me") {
		if tok := externalTokenFromContext(ctx); tok != nil && strings.TrimSpace(tok.Owner) != "" {
			if norm, err := model.NormalizeTaskOwner(tok.Owner); err == nil {
				return norm
			}
		}
		if p := c.requestPersonOrOperator(ctx); p != "" {
			if norm, err := model.NormalizeTaskOwner(p); err == nil {
				return norm
			}
		}
		return c.defaultDeploymentOwner(ctx)
	}
	if norm, err := model.NormalizeTaskOwner(val); err == nil {
		return norm
	}
	return val
}
