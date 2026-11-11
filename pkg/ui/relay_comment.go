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
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// relayAttribution decides whether a POST /api/tasks/{id}/comments with
// relay: true may be written as a person's answer (Client.AddRelayedComment),
// and returns the person and the session carrying it. A request that is
// not an agent's call returns nil, nil, nil: a person's own comment is
// already theirs, and relay changes nothing about it.
//
// Only a console session (a director, or any other standing chat) may
// relay, and only for the verified person whose message its current turn
// is answering (agentOnBehalfOf). A dispatched run acts for whoever
// approved its task, but nobody told it anything to pass on, so what it
// says on another task stays its own -- recorded, and never an answer.
// The person must also be one who could have replied on the task
// themselves (personMayReply): a session is no way round their role.
func (s *Server) relayAttribution(r *http.Request, target string) (person, session *model.Principal, err error) {
	call, ok := agentCallFromContext(r.Context())
	if !ok {
		return nil, nil, nil
	}
	if call.external != nil {
		return nil, nil, forbiddenErrorf("an external agent credential cannot relay a person's answer: relay is for a console session acting for the person who asked it to")
	}
	if !call.session {
		return nil, nil, forbiddenErrorf("task %s is not a console session, so it cannot relay a person's answer: "+
			"only a director or console session acting for a person may, and a run's comment on another task is recorded as its own", call.taskID)
	}
	if call.onBehalfOf == nil {
		return nil, nil, forbiddenErrorf("session %s is acting for no signed-in person right now, so it has nobody's answer to relay: "+
			"ask the person to reply on the task themselves", call.taskID)
	}
	target = model.NormalizeTaskID(target)
	if target == model.NormalizeTaskID(call.taskID) {
		return nil, nil, validationErrorf("a session cannot relay an answer onto its own conversation: the person is already talking to it")
	}
	if s.tasks == nil || s.tasks.Store == nil {
		return nil, nil, errNoAgentAPI
	}
	task, err := s.tasks.Store.GetTask(r.Context(), target)
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, &NotFoundError{ID: target}
	}
	if err := s.personMayReply(r.Context(), *call.onBehalfOf, *task); err != nil {
		return nil, nil, err
	}
	who := *call.onBehalfOf
	via := model.Principal{Kind: model.PrincipalAgent, ID: model.NormalizeTaskID(call.taskID), Source: call.source()}
	return &who, &via, nil
}

// personMayReply is whether person's own role lets them reply on task:
// the role gate's check of POST /api/tasks/{id}/comments, asked of the
// person a session would speak for rather than of the session. A person
// the allowlist does not match while an identity verifier is active has
// no role to reply within, as externalTokenOwnerCap holds a credential's
// owner; with no verifier the list admits nobody, and there is no role to
// ask.
func (s *Server) personMayReply(ctx context.Context, person model.Principal, task model.Task) error {
	email := normalizeIdentityEmail(person.ID)
	roles, err := s.allowedPrincipalRoles(ctx, VerifiedIdentity{Email: email, Sub: email, Kind: model.PrincipalHuman}, true)
	if err != nil {
		var groupErr *groupAllowlistRefusalError
		if errors.As(err, &groupErr) {
			return forbiddenErrorf("%s's role cannot be checked, so their answer cannot be relayed: %s", email, groupErr.public)
		}
		if len(activeVerifiers(ctx)) > 0 {
			return forbiddenErrorf("%s holds no role on this deployment, so their answer cannot be relayed onto task %s", email, task.ID)
		}
		return nil
	}
	a := resolveAccessRoles(email, roles)
	a.matches = s.entryMatcher(model.PrincipalHuman, email)
	if a.Full {
		return nil
	}
	const pattern = "POST /api/tasks/{id}/comments"
	scope := roleScopeOf(pattern)
	repo := taskRepo(&task)
	if !a.allows(scope, repo) {
		where := "here"
		if repo != "" {
			where = "on " + repo
		}
		return forbiddenErrorf("%s may not reply on task %s (%s holds no %s %s), so their answer cannot be relayed",
			email, task.ID, a.label(), scope, where)
	}
	allowedRepo := func(need APIScope, targetRepo string) bool {
		targetRepo = strings.TrimSpace(targetRepo)
		if targetRepo == "" {
			return true
		}
		return a.allows(need, targetRepo)
	}
	if err := s.tasks.roleTaskWithin(ctx, a, task, "reply to", allowedRepo, s.normalizeCapFn()); err != nil {
		return forbiddenErrorf("%v, so their answer cannot be relayed", err)
	}
	return nil
}

// agentCommentNotCounted is whether a comment that has just been written
// on task by this request is an agent's that leaves task waiting for a
// person: the case handleAddComment says so in its answer, rather than
// let an agent believe it answered. An agent's comment on its own task is
// it talking in its own conversation, and is not this.
func agentCommentNotCounted(ctx context.Context, task Task) bool {
	call, ok := agentCallFromContext(ctx)
	if !ok || task.State != model.StateAwaitingReply {
		return false
	}
	return call.external != nil || model.NormalizeTaskID(call.taskID) != model.NormalizeTaskID(task.ID)
}
