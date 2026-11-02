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

// The person-only last step of a trunk release
// (docs/design/release-from-trunk.md, §4): the release train stops a
// release whose soak passed at vX.Y.0-pending, and a person approves or
// rejects it here. Approving runs .grain/workflows/promote-release.yml's
// `promote` job (which needs `approve`), rejecting its `reject` job; both
// are workflow_dispatch runs of the repository's trunk that carry
// GRAIN_CI_REQUESTED_BY_KIND=human and GRAIN_CI_RELEASE_VERSION, which no
// API caller can set, and every job of that workflow refuses to run
// without them.
//
// The routes are under releases:write, the release-manager scope, but a
// grant is not enough: the caller has to be a person. An agent holding
// the scope -- a run's token, an executor, a pipeline steward -- is
// refused before anything is started, and so is a schedule, which acts
// as no person at all.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// PromoteReleaseWorkflow is the workflow a person's answer runs.
const PromoteReleaseWorkflow = ci.WorkflowDir + "/promote-release.yml"

// releaseTrunk is the branch the release train cuts from and the
// workflow is read at: releases are cut from trunk and nowhere else.
const releaseTrunk = "main"

// pendingReleasePattern is a release a person can be asked about: vX.Y.0.
var pendingReleasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.0$`)

// ReleaseDecision is what approving or rejecting a pending release
// answers: the CI run carrying it out.
type ReleaseDecision struct {
	Version string `json:"version"`
	Approve bool   `json:"approve"`
	Run     ci.Run `json:"run"`
}

// DecideRelease starts the promote-release run that approves (approve)
// or rejects version, a pending release of repo. Only a person may: any
// other caller is refused with a ForbiddenError and nothing is started.
// On a run started, the release's "ready to promote" notification is
// cleared -- the run is what to watch now, and a run that fails leaves
// the -pending tag, which the next daily train run raises again.
func (c *Client) DecideRelease(ctx context.Context, repo, version string, approve bool) (ReleaseDecision, error) {
	verb := "reject"
	if approve {
		verb = "approve"
	}
	// Who first, before anything about the release is looked at.
	actor := c.requestActor(ctx).Actor
	if actor.Kind != model.PrincipalHuman || actor.ID == "" {
		return ReleaseDecision{}, forbiddenErrorf("only a person may %s a release: %q is a %s, and a release "+
			"is made official by a person, never by an agent or a schedule", verb, actor.ID, kindName(actor.Kind))
	}
	if c.Config.CI == nil {
		return ReleaseDecision{}, fmt.Errorf("this deployment has no grain CI wired")
	}
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return ReleaseDecision{}, validationErrorf("repo: %v", err)
	}
	if !pendingReleasePattern.MatchString(version) {
		return ReleaseDecision{}, validationErrorf("%q is not a release a person is asked about: name it vX.Y.0", version)
	}
	enabled, err := c.ciEnabledFor(ctx, parsed)
	if err != nil {
		return ReleaseDecision{}, err
	}
	if !enabled {
		return ReleaseDecision{}, forbiddenErrorf("%s", ciNotEnabledMessage(parsed))
	}
	job := PromoteReleaseWorkflow + ":reject"
	if approve {
		job = PromoteReleaseWorkflow + ":promote"
	}
	run, err := c.Config.CI.Start(ctx, ci.Request{
		Repo: parsed.String(), Ref: releaseTrunk,
		Event: ci.EventManual, Workflow: PromoteReleaseWorkflow, Jobs: []string{job},
		RequestedBy: actor.ID, RequestedByKind: string(model.PrincipalHuman), ReleaseVersion: version,
	})
	if err != nil {
		return ReleaseDecision{}, &ValidationError{err: err}
	}
	if c.Store != nil {
		subject := model.NotificationSubject(model.FamilyReleasePending, version)
		if _, err := c.Store.ClearNotification(ctx, subject, model.PrincipalString(actor)); err != nil {
			log.Printf("grain: clearing %s after %s's %s: %v", subject, actor.ID, verb, err)
		}
	}
	return ReleaseDecision{Version: version, Approve: approve, Run: run}, nil
}

func kindName(k model.PrincipalKind) string {
	if k == "" {
		return "caller nobody identified"
	}
	return string(k)
}

func (s *Server) handleApproveRelease(w http.ResponseWriter, r *http.Request) {
	s.decideRelease(w, r, true)
}

func (s *Server) handleRejectRelease(w http.ResponseWriter, r *http.Request) {
	s.decideRelease(w, r, false)
}

func (s *Server) decideRelease(w http.ResponseWriter, r *http.Request, approve bool) {
	out, err := s.tasks.DecideRelease(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"),
		r.PathValue("version"), approve)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}
