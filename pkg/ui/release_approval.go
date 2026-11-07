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
// (design/playbook/releases.playbook.md): the release train stops a
// release whose candidate qualified at vX.Y.Z-pending -- a minor release,
// vX.Y.0, or a patch the train cut from fixes alone, vX.Y.Z -- and a
// person approves or rejects it here. On a repository that publishes
// from GitHub Actions (ci_mode: github) the answer is a signed dispatch
// of .github/workflows/promote-release.yml instead
// (release_approval_github.go). Otherwise approving runs .grain/workflows/promote-release.yml's
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
//
// An approval may carry short_soak_ok: the person's explicit acceptance
// that the release soaked under the minimum, which
// scripts/release-from-trunk.sh approve otherwise refuses. It reaches the
// run as GRAIN_CI_RELEASE_SHORT_SOAK_OK, another variable no API caller
// and no workflow env: can set, and only from a person on this route.
//
// It may carry allow_breaking the same way: the person's acceptance that
// the release carries breaking changes without being the major release
// RELEASE_MAJOR asks for, which `release-notes.sh check-breaking` otherwise
// refuses. It reaches the run as GRAIN_CI_RELEASE_ALLOW_BREAKING, which
// promote-release.yml passes on as RELEASE_ALLOW_BREAKING=<the release>.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// PromoteReleaseWorkflow is the workflow a person's answer runs.
const PromoteReleaseWorkflow = ci.WorkflowDir + "/promote-release.yml"

// releaseTrunk is the branch the release train cuts from and the
// workflow is read at: releases are cut from trunk and nowhere else.
const releaseTrunk = "main"

// pendingReleasePattern is a release a person can be asked about: vX.Y.Z,
// the train staging patch releases (vX.Y.Z-pending, Z >= 1) as well as
// minor ones.
var pendingReleasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// ReleaseDecision is what approving, rejecting, or rolling back a
// release answers: the CI run carrying it out.
type ReleaseDecision struct {
	Version        string `json:"version"`
	Approve        bool   `json:"approve"`
	Rollback       bool   `json:"rollback,omitempty"`
	RollbackTo     string `json:"rollbackTo,omitempty"`
	RollbackReason string `json:"rollbackReason,omitempty"`
	ShortSoakOK    bool   `json:"shortSoakOk,omitempty"`
	AllowBreaking  bool   `json:"allowBreaking,omitempty"`
	// Run is the grain CI run carrying the answer out, on a repository
	// grain CI releases.
	Run ci.Run `json:"run"`
	// Dispatched is the GitHub Actions dispatch carrying it out instead,
	// on a repository that publishes from GitHub Actions (ci_mode:
	// github; release_approval_github.go).
	Dispatched *ReleaseDispatch `json:"dispatched,omitempty"`
}

// ReleaseApproval is the optional body of POST
// .../pending-releases/{version}/approve (and of reject, which refuses
// both of its answers).
type ReleaseApproval struct {
	// ShortSoakOK accepts a release whose recorded staging soak is under
	// the minimum (the -pending tag's Soak-Seconds; the notification says
	// SHORT SOAK). Without it the approve job refuses such a release.
	ShortSoakOK bool `json:"short_soak_ok,omitempty"`
	// AllowBreaking accepts a release that carries breaking changes
	// without being the major release RELEASE_MAJOR asks for. Without it
	// the approve job refuses such a release.
	AllowBreaking bool `json:"allow_breaking,omitempty"`
}

// ReleaseRollback is the optional body of POST /api/releases/{version}/rollback.
type ReleaseRollback struct {
	// Repo is the repository whose promoted release version is rolled
	// back; empty defaults to grain's own source repository.
	Repo string `json:"repo,omitempty"`
	// To is the earlier release (vA.B.C) latest goes back to; empty lets
	// scripts/release-from-trunk.sh pick the newest earlier release
	// neither retracted nor in RELEASE_NEVER_PROMOTE.
	To string `json:"to,omitempty"`
	// Reason is why version is retracted, recorded on its
	// vX.Y.Z-retracted tag.
	Reason string `json:"reason,omitempty"`
}

// DecideRelease starts the promote-release run that approves (approve)
// or rejects version, a pending release of repo; answers, for an
// approval only, accepts a soak under the minimum or breaking changes.
// Only a person may: any
// other caller is refused with a ForbiddenError and nothing is started.
// On a run started, the release's "ready to promote" notification is
// cleared -- the run is what to watch now, and a run that fails leaves
// the -pending tag, which the next daily train run raises again.
func (c *Client) DecideRelease(ctx context.Context, repo, version string, approve bool, answers ReleaseApproval) (ReleaseDecision, error) {
	verb := "reject"
	if approve {
		verb = "approve"
	}
	// Who first, before anything about the release is looked at.
	actor := c.requestActor(ctx).Actor
	if actor.Kind != model.PrincipalHuman || actor.ID == "" {
		return ReleaseDecision{}, forbiddenErrorf("only a person may %s a release: %q is %s, and a release "+
			"is made official by a person, never by an agent or a schedule", verb, actor.ID, kindName(actor.Kind))
	}
	if answers.ShortSoakOK && !approve {
		return ReleaseDecision{}, validationErrorf("short_soak_ok is an approval's answer: rejecting %s needs no soak", version)
	}
	if answers.AllowBreaking && !approve {
		return ReleaseDecision{}, validationErrorf("allow_breaking is an approval's answer: rejecting %s promotes nothing", version)
	}
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return ReleaseDecision{}, validationErrorf("repo: %v", err)
	}
	if !pendingReleasePattern.MatchString(version) {
		return ReleaseDecision{}, validationErrorf("%q is not a release a person is asked about: name it vX.Y.Z", version)
	}
	if onGitHub, err := c.publishesFromGitHub(ctx, parsed); err != nil {
		return ReleaseDecision{}, err
	} else if onGitHub {
		d, err := c.dispatchGitHubRelease(ctx, parsed, version, actor.ID, approve, answers)
		if err != nil {
			return ReleaseDecision{}, err
		}
		c.clearReleasePending(ctx, version, actor, verb)
		return ReleaseDecision{Version: version, Approve: approve, ShortSoakOK: answers.ShortSoakOK,
			AllowBreaking: answers.AllowBreaking, Dispatched: &d}, nil
	}
	if c.Config.CI == nil {
		return ReleaseDecision{}, fmt.Errorf("this deployment has no grain CI wired")
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
		ReleaseShortSoakOK: answers.ShortSoakOK, ReleaseAllowBreaking: answers.AllowBreaking,
	})
	if err != nil {
		return ReleaseDecision{}, &ValidationError{err: err}
	}
	c.clearReleasePending(ctx, version, actor, verb)
	return ReleaseDecision{Version: version, Approve: approve,
		ShortSoakOK: answers.ShortSoakOK, AllowBreaking: answers.AllowBreaking, Run: run}, nil
}

// clearReleasePending clears version's "ready to promote" notification
// once a person's answer is on its way.
func (c *Client) clearReleasePending(ctx context.Context, version string, actor model.Principal, verb string) {
	if c.Store == nil {
		return
	}
	subject := model.NotificationSubject(model.FamilyReleasePending, version)
	if _, err := c.Store.ClearNotification(ctx, subject, model.PrincipalString(actor)); err != nil {
		log.Printf("grain: clearing %s after %s's %s: %v", subject, actor.ID, verb, err)
	}
}

// kindName is k with its article, for "only a person may ...: %q is
// <kindName>": "an agent", "a service", "an automation". Without the
// article chosen here the refusal read `"20" is a agent`
// (docs/test-runs/2026-11-04-release-approval.md, F4).
func kindName(k model.PrincipalKind) string {
	if k == "" {
		return "a caller nobody identified"
	}
	if strings.ContainsRune("aeiou", rune(k[0])) {
		return "an " + string(k)
	}
	return "a " + string(k)
}

func (s *Server) handleApproveRelease(w http.ResponseWriter, r *http.Request) {
	s.decideRelease(w, r, true)
}

func (s *Server) handleRejectRelease(w http.ResponseWriter, r *http.Request) {
	s.decideRelease(w, r, false)
}

func (s *Server) decideRelease(w http.ResponseWriter, r *http.Request, approve bool) {
	var req ReleaseApproval
	if !readOptionalJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.DecideRelease(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"),
		r.PathValue("version"), approve, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// RollbackRelease starts the promote-release run that retracts version,
// a promoted release of repo, and re-points latest to opts.To (or the
// newest earlier non-retracted release). Only a person may: any other
// caller is refused with a ForbiddenError and nothing is started.
func (c *Client) RollbackRelease(ctx context.Context, repo, version string, opts ReleaseRollback) (ReleaseDecision, error) {
	// Who first, before anything about the release is looked at.
	actor := c.requestActor(ctx).Actor
	if actor.Kind != model.PrincipalHuman || actor.ID == "" {
		return ReleaseDecision{}, forbiddenErrorf("only a person may roll back a release: %q is %s, and a release "+
			"is rolled back by a person, never by an agent or a schedule", actor.ID, kindName(actor.Kind))
	}
	if strings.TrimSpace(repo) == "" {
		repo = strings.TrimSpace(opts.Repo)
	}
	if repo == "" {
		repo = model.GrainSourceRepo().String()
	}
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return ReleaseDecision{}, validationErrorf("repo: %v", err)
	}
	if !pendingReleasePattern.MatchString(version) {
		return ReleaseDecision{}, validationErrorf("%q is not a release version: name it vX.Y.Z", version)
	}
	to := strings.TrimSpace(opts.To)
	if to != "" && !pendingReleasePattern.MatchString(to) {
		return ReleaseDecision{}, validationErrorf("to %q is not a release version: name it vX.Y.Z or leave it empty", opts.To)
	}
	if strings.ContainsAny(opts.Reason, "\x00\r\n") {
		return ReleaseDecision{}, validationErrorf("reason must be a single line")
	}
	reason := strings.TrimSpace(opts.Reason)
	cleanOpts := ReleaseRollback{Repo: parsed.String(), To: to, Reason: reason}
	if onGitHub, err := c.publishesFromGitHub(ctx, parsed); err != nil {
		return ReleaseDecision{}, err
	} else if onGitHub {
		d, err := c.dispatchGitHubRollback(ctx, parsed, version, actor.ID, cleanOpts)
		if err != nil {
			return ReleaseDecision{}, err
		}
		return ReleaseDecision{Version: version, Rollback: true, RollbackTo: to,
			RollbackReason: reason, Dispatched: &d}, nil
	}
	if c.Config.CI == nil {
		return ReleaseDecision{}, fmt.Errorf("this deployment has no grain CI wired")
	}
	enabled, err := c.ciEnabledFor(ctx, parsed)
	if err != nil {
		return ReleaseDecision{}, err
	}
	if !enabled {
		return ReleaseDecision{}, forbiddenErrorf("%s", ciNotEnabledMessage(parsed))
	}
	job := PromoteReleaseWorkflow + ":rollback"
	run, err := c.Config.CI.Start(ctx, ci.Request{
		Repo: parsed.String(), Ref: releaseTrunk,
		Event: ci.EventManual, Workflow: PromoteReleaseWorkflow, Jobs: []string{job},
		RequestedBy: actor.ID, RequestedByKind: string(model.PrincipalHuman), ReleaseVersion: version,
		ReleaseRollbackTo: to, ReleaseRollbackReason: reason,
	})
	if err != nil {
		return ReleaseDecision{}, &ValidationError{err: err}
	}
	return ReleaseDecision{Version: version, Rollback: true,
		RollbackTo: to, RollbackReason: reason, Run: run}, nil
}

func (s *Server) handleRollbackRelease(w http.ResponseWriter, r *http.Request) {
	var req ReleaseRollback
	if !readOptionalJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.RollbackRelease(r.Context(), req.Repo, r.PathValue("version"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}
