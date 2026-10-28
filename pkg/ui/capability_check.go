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
	"time"
)

// CapabilityCheck is one credential test as
// POST /api/capabilities/{id}/check reports it: what happened when grain
// authenticated as this capability's standing credential and made one
// cheap, harmless call with it.
//
// It is a point-in-time answer and nothing more. Nothing here is stored,
// nothing here changes CapabilityStatus.Ready, and a second call a
// minute later may say something else -- which is the honest shape for a
// question only somebody else's API can answer. See Client.
// CheckCapability for why that is on demand rather than on a timer.
type CapabilityCheck struct {
	// ID is the capability that was checked.
	ID string `json:"id"`
	// Credentials names the standing credentials the check
	// authenticated as (model.CredentialCheck.Credentials) -- one for
	// gcp-key/gemini-key, two for github-sandbox's App. Reported even
	// when the check failed, since a refusal's whole remedy is
	// "replace what is in this secret", and it is filled in from the
	// provider's own Requires rather than from anything a human typed.
	Credentials []string `json:"credentials,omitempty"`
	// OK is whether the far end accepted the credential and answered
	// the call.
	//
	// False is a third state beside Ready and Grantable, and reads as
	// this deployment's problem the way an unready capability does
	// rather than as grain being broken: the deployment is configured,
	// the check ran, and the credential was refused. Detail says by
	// whom and what to do about it.
	OK bool `json:"ok"`
	// Detail is one sentence a human can act on: on success, what came
	// back (how many keys the account has, which installation
	// answered), which is the evidence that this credential is live
	// rather than merely present; on failure, the provider's own
	// explained error -- gcpkey.explainRefusedCredential's sentence
	// naming the dead secret and the two places a current key is
	// pasted, not GCP's bare `invalid_grant`.
	Detail string `json:"detail"`
	// CheckedAt is when the call was made, so an answer read later says
	// how old it is rather than looking current.
	CheckedAt time.Time `json:"checkedAt"`
}

// CapabilityCheckResult is one provider's own answer, before this
// package turns it into the CapabilityCheck above -- what
// pkg/cli/daemon.go's adapter carries back out of
// model.CredentialChecker.
//
// Deliberately not model.CredentialCheck itself, for the same reason
// SandboxRecreation is not orchestrator's own type: nothing in the wire
// shapes this package sends is a type from a package it does not
// import, and the conversion has exactly one home.
type CapabilityCheckResult struct {
	Credentials []string
	Detail      string
}

// CapabilityChecker is implemented by whatever can actually make a call
// as a capability's standing credential -- pkg/cli/daemon.go's own
// adapter over the same providers a dispatch resolves through, in a real
// deployment. See Config.CapabilityChecks' doc comment for the
// nil-means-unavailable contract this interface's absence satisfies.
//
// An error is an answer, not a failure of this API: a credential the far
// end refused, and a capability this deployment never configured, both
// arrive here as errors and are reported as a CapabilityCheck with OK
// false carrying the error's own text. Only grain's own gaps -- an
// unwired UI, an unknown id, a capability with no check written for it
// -- are errors out of Client.CheckCapability.
type CapabilityChecker interface {
	CheckCapability(ctx context.Context, id string) (CapabilityCheckResult, error)
}

// errCapabilityCheckUnavailable is what handleCheckCapability reports
// when Config.CapabilityChecks is nil -- see that field's own doc
// comment.
var errCapabilityCheckUnavailable = errors.New(
	"testing a capability's credential is not available: this deployment's UI is not wired to the daemon that holds them")

// CheckCapability authenticates as id's standing credential and makes
// one cheap, harmless call with it -- gcp-key lists the agent service
// account's own keys, exactly as Reap does; gemini-key lists the
// project's API keys; github-sandbox asks GitHub which installation its
// App has; a named GitHub token asks GitHub for its own rate limit and
// then which of this deployment's target repos it can still reach -- and
// reports what came back.
//
// This is the one question Settings could not previously ask. "Ready"
// there means *configured*: a project, an agent account and a
// `gcp-key-minter` secret are all set. The key inside that secret can
// have been deleted or rotated away in GCP months ago and every one of
// those three facts stays true, so the pane agrees with itself while
// every mint fails with `invalid_grant` (README.md's "Debugging
// `gcp-key` again"). Only the far end knows, and only when something
// authenticates with it.
//
// **On demand, not on a schedule**, and that is the design rather than
// the easy half of it. A background poll would keep the badge itself
// truthful, at the price of two things worth more: every poll is a real
// request to somebody else's API, per capability, forever, whether or
// not anybody is looking; and "Ready" would become a thing that changes
// with nobody touching Settings, so a badge that went red overnight
// would carry no answer to "compared to when, and did anything here
// change?". A button costs one round trip that a human asked for and
// gives an answer stamped with the moment it was true.
//
// **It never mints, writes or deletes anything.** A check somebody is
// expected to press repeatedly must not leave resources behind for a
// reaper to collect, and must not need permission beyond what an
// ordinary Materialize already has -- see model.CredentialChecker.
//
// An unknown id, or one grain ships no check for, is a ValidationError
// (a 400): those are mistakes about grain rather than answers about this
// deployment. Everything the provider itself says -- refused, not
// configured, API disabled -- comes back as an ordinary CapabilityCheck
// with OK false, because each of those is a real answer to the question
// asked, with a remedy on this deployment.
func (c *Client) CheckCapability(ctx context.Context, id string) (CapabilityCheck, error) {
	if c.Config.CapabilityChecks == nil {
		return CapabilityCheck{}, errCapabilityCheckUnavailable
	}
	// Told apart deliberately: an id no build of grain has heard of is a
	// different mistake from a real capability that holds no standing
	// credential to test (self-debug and friends place files and grant
	// tools; there is nothing there to go stale behind grain's back).
	// Known covers both listings, since a capability grain ships a
	// provider for but the picker does not offer is a real capability
	// with a real credential -- ungrantable is Grantable's answer to
	// give, not this one's. A github-credential: id this deployment does
	// not offer falls through to the same "unknown capability": there is
	// no such token here, and no build of grain could have one, since
	// which tokens exist is an operator's own files.
	if !c.checkable(id) {
		known := capabilityShipped(id)
		if !known {
			_, known = c.capabilityByID(id)
		}
		if !known {
			return CapabilityCheck{}, validationErrorf("unknown capability %q", id)
		}
		return CapabilityCheck{}, validationErrorf(
			"capability %q holds no standing credential to test", id)
	}
	check := CapabilityCheck{ID: id, CheckedAt: c.now()}
	result, err := c.Config.CapabilityChecks.CheckCapability(ctx, id)
	check.Credentials = result.Credentials
	if err != nil {
		check.Detail = err.Error()
		return check, nil
	}
	check.OK = true
	check.Detail = result.Detail
	return check, nil
}

// handleCheckCapability answers POST /api/capabilities/{id}/check.
//
// POST rather than GET, for a call that stores nothing: it is not a read
// of state grain holds, it is grain going and doing something with a
// credential -- one request to a third party per call, which is exactly
// the kind of thing a browser, a proxy or a prefetch must not be free to
// repeat on its own.
//
// The nil check is here as well as in CheckCapability so an unwired
// deployment answers 404 -- "this deployment does not offer that" --
// rather than turning a missing feature into a 500 that reads like a
// failure, the same shape handleRecreateSandbox uses.
func (s *Server) handleCheckCapability(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.CapabilityChecks == nil {
		writeError(w, http.StatusNotFound, errCapabilityCheckUnavailable)
		return
	}
	check, err := s.tasks.CheckCapability(r.Context(), r.PathValue("id"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, check)
}
