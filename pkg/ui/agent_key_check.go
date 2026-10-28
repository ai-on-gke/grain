package ui

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/credcheck"
	"github.com/bwsalmon/grain/pkg/model"
)

// The agent-credential half of what capability_check.go already does for
// capabilities: POST /api/agent-keys/{framework}/check, which
// authenticates as the credential that framework's runs are dispatched
// with and makes one cheap, harmless call with it.
//
// agent_keys.go beside this can say a credential is *set* -- the secret
// exists and resolves -- and that is the whole of what a pane reading
// grain's own store can see. It is the same gap **Ready** had on the
// Capabilities tab before grain/task-172: a key revoked, rotated or
// expired at the far end leaves every fact grain stores unchanged, so
// the chip reads "set" and stays that way until a dispatched run fails
// to authenticate several minutes into somebody's task. Only the vendor
// knows, and only when something authenticates with it.
//
// Everything below therefore mirrors CapabilityCheck deliberately, down
// to the shape of the wire type and the split between answers and
// errors: an operator reading one pane should not have to learn a second
// idea to read the other.

// AgentKeyCheck is one such test as the API reports it: what happened
// when grain authenticated as this framework's stored credential.
//
// A point-in-time answer, stored nowhere, folded into no chip, and true
// only at CheckedAt -- see Client.CheckAgentKey for why that is the
// honest shape for a question only somebody else's API can answer.
type AgentKeyCheck struct {
	// Framework is the framework whose credential was checked, after
	// normalization -- "gemini" is reported back as "antigravity", the
	// name every other part of this API uses.
	Framework string `json:"framework"`
	// Secret names the secret the credential was read from
	// (secrets.GeminiAPIKeySecret and friends). Reported even when the
	// check failed, since a refusal's whole remedy is replacing what is
	// in that secret -- the same reason CapabilityCheck.Credentials is.
	Secret string `json:"secret,omitempty"`
	// OK is whether the vendor accepted the credential and answered the
	// call.
	OK bool `json:"ok"`
	// Detail is one sentence a human can act on: on success, what came
	// back (which models the credential can see), the evidence that it
	// is live rather than merely present; on failure, who refused it and
	// what they said, naming the secret to paste a new value into.
	Detail string `json:"detail"`
	// CheckedAt is when the call was made, so an answer read later says
	// how old it is rather than looking current.
	CheckedAt time.Time `json:"checkedAt"`
}

// AgentKeyCheckResult is what pkg/cli/daemon.go's adapter carries back
// out of a real check, before this package stamps it with a time and
// turns it into the wire shape above.
//
// Deliberately not credcheck.Result itself, for the same reason
// CapabilityCheckResult is not model.CredentialCheck: nothing in the
// wire shapes this package sends is a type from a package it would
// otherwise not import, and the conversion has exactly one home.
type AgentKeyCheckResult struct {
	Secret string
	Detail string
}

// AgentKeyChecker is implemented by whatever can actually read this
// deployment's stored agent credential and make a call with it --
// pkg/cli/daemon.go's own adapter over the same secrets store and the
// same key files a dispatch resolves through.
//
// An error is an answer, not a failure of this API: a credential the
// vendor refused, a credential that is not set at all, and a vendor that
// could not be reached all arrive here as errors and are reported as an
// AgentKeyCheck with OK false carrying the error's own sentence. Only
// grain's own gaps -- an unwired UI, a framework no build of grain has
// -- are errors out of Client.CheckAgentKey.
type AgentKeyChecker interface {
	CheckAgentKey(ctx context.Context, framework string) (AgentKeyCheckResult, error)
}

// errAgentKeyCheckUnavailable is what this answers with when
// Config.AgentKeyChecks is nil -- see that field's own doc comment.
var errAgentKeyCheckUnavailable = errors.New(
	"testing an agent credential is not available: this deployment's UI is not wired to the daemon that holds them")

// CheckAgentKey authenticates as one framework's stored credential and
// lists the models it can see -- Google's for the Gemini key agy runs
// on, Anthropic's for the Claude Code OAuth token, OpenAI's for the
// codex key -- and reports what came back.
//
// A listing is the cheapest authenticated call each of those APIs has:
// it creates nothing, spends no tokens, and is safe to press twice,
// which matters because this is a button an operator presses while
// pasting a key in. It deliberately does not run the framework's own CLI
// (see package credcheck): that would cost a real model call, need the
// binary installed, and answer two questions at once -- leaving nobody
// sure which half failed.
//
// **On demand, not on a timer**, for the reasons CheckCapability's own
// doc comment sets out at length: a poll would be a request to somebody
// else's API per framework forever whether or not anybody is looking,
// and would make the chip beside the field change with nobody touching
// Settings.
//
// A framework grain cannot dispatch with at all is a ValidationError (a
// 400) -- a mistake about grain rather than an answer about this
// deployment. Everything else, refusals included, comes back as an
// ordinary AgentKeyCheck with OK false.
func (c *Client) CheckAgentKey(ctx context.Context, framework string) (AgentKeyCheck, error) {
	if c.Config.AgentKeyChecks == nil {
		return AgentKeyCheck{}, errAgentKeyCheckUnavailable
	}
	name := model.NormalizeAgentFrameworkName(framework)
	secret, ok := credcheck.SecretFor(name)
	if !ok {
		return AgentKeyCheck{}, validationErrorf("no agent framework named %q", framework)
	}
	// Secret is filled in before the call, not from its result, so a
	// refusal names the field to fix even when the adapter answered with
	// an error and nothing else.
	check := AgentKeyCheck{Framework: name, Secret: secret, CheckedAt: c.now()}
	result, err := c.Config.AgentKeyChecks.CheckAgentKey(ctx, name)
	if result.Secret != "" {
		check.Secret = result.Secret
	}
	if err != nil {
		check.Detail = err.Error()
		return check, nil
	}
	check.OK = true
	check.Detail = result.Detail
	return check, nil
}

// handleCheckAgentKey answers POST /api/agent-keys/{framework}/check.
//
// POST for a call that stores nothing, exactly as handleCheckCapability
// is: it is not a read of state grain holds, it is grain going and
// making a request to a third party, which is not something a browser, a
// proxy or a prefetch may be free to repeat on its own.
//
// The nil check is here as well as in CheckAgentKey so an unwired
// deployment answers 404 -- "this deployment does not offer that" --
// rather than turning a missing feature into a 500 that reads like a
// failure.
func (s *Server) handleCheckAgentKey(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.AgentKeyChecks == nil {
		writeError(w, http.StatusNotFound, errAgentKeyCheckUnavailable)
		return
	}
	check, err := s.tasks.CheckAgentKey(r.Context(), r.PathValue("framework"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, check)
}
