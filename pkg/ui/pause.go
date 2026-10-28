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

// The "this model connection's credential has no budget left in this
// window" gate (orchestrator.Pause), as an operator sees it: GET
// /api/config carries it so a banner can stand on every page, GET
// /api/pause is the same answer on its own for anything polling it, and
// DELETE /api/pause is the operator's own override.
//
// A pause belongs to the credential that met the limit -- the model
// connection owning the key -- and so to that connection's team. Each
// reading lists the pauses one by one, naming connection and team, and
// lists only those of the teams the caller can see: one team exhausting
// its key is that team's news, not every other team's (a system admin
// sees them all). Lifting is the same: a team's admins
// lift their own team's pauses, and only a system admin lifts another
// team's, or all of them at once.
//
// It is deliberately *not* a section of GET /api/metrics. That report is
// computed over rows for a window that has ended -- nothing in it
// changes between two calls a second apart with no work in between --
// and a pause is a gauge of what dispatch is doing right now (its row in
// usage_limit_pause only carries it across a restart). The one part of that report already in
// that position, the cycles section, is there because it is an input to
// the same metrics.Compute the rest comes from (Config.Cycles); a pause
// is not, and putting it there would only mean an operator learning to
// look for "why is nothing dispatching?" in a latency report.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// AgentPause is implemented by the dispatch gate an agent's own usage
// limit closes -- *orchestrator.Pause itself, in a real
// deployment, satisfied structurally rather than named there: this
// package does not import pkg/orchestrator (see sandbox_health.go's own
// doc comment on why), and unlike SandboxHealth there is no shape to
// convert here, only the methods this API forwards. The record both
// sides share is model.UsageLimitPause.
type AgentPause interface {
	// Pauses is every pause in force at now, one per credential
	// (Pause.Pauses).
	//
	// This is not Pause.Blocked on purpose: Blocked clears a pause whose
	// instant has passed, as the reconcile loop's own read, and a UI poll
	// must not be able to open the gate out from under the loop that owns
	// it. A pause whose instant is in the past is one nothing has asked
	// about since it expired, and is left out here.
	Pauses(now time.Time) []model.UsageLimitPause
	// LiftWhere clears every pause match accepts before its window has
	// run out, and reports the ones it cleared. See Pause.LiftWhere for
	// what an operator is actually buying with it; who may lift which is
	// decided here (Client.LiftAgentPause).
	LiftWhere(match func(model.UsageLimitPause) bool) []model.UsageLimitPause
}

// AgentPauseStatus is one caller's reading of that gate: what GET
// /api/pause reports, and what rides on GET /api/config as agentPause.
// It holds only the pauses of teams that caller can see.
//
// SecondsRemaining is derived rather than left to the caller so that a
// browser with a wrong clock -- the usual case for "in about four hours"
// -- still counts down from the right number. Until is sent beside it
// because a wall-clock time is what an operator plans around, and a
// remaining duration alone cannot be turned back into one.
type AgentPauseStatus struct {
	// Paused is whether any pause the caller can see is in force.
	Paused bool `json:"paused"`
	// Until, Reason and SecondsRemaining are the last of those pauses to
	// lift, and empty when Paused is false. Pauses is what says whose
	// each one is.
	Until            time.Time `json:"until"`
	Reason           string    `json:"reason,omitempty"`
	SecondsRemaining float64   `json:"secondsRemaining"`
	// Pauses is each pause in force the caller can see, sorted by team
	// and connection: whose credential met a limit, so a banner can say
	// which connection of which team has stopped rather than implying the
	// deployment has.
	Pauses []AgentPauseEntry `json:"pauses,omitempty"`
}

// AgentPauseEntry is one credential's pause, as AgentPauseStatus lists
// it.
type AgentPauseEntry struct {
	// Connection is the model connection owning the credential that met
	// the limit (model.AgentBackend.CredentialOwnerID); every connection
	// borrowing its key is paused with it. Empty for a pause on runs that
	// spent no connection's key -- the deployment-level credentials --
	// which covers Team's runs on Framework.
	Connection string `json:"connection,omitempty"`
	// ConnectionName is the connection's display name, where it has one.
	ConnectionName string `json:"connectionName,omitempty"`
	// Model is the one model on Connection the pause holds, and Labels
	// the other names grain passed runs it refused ("auto"), which it
	// holds too; Connection's other models keep dispatching. Empty for a
	// pause on the whole connection, which Escalated says the reason for
	// when it grew from a model's pause (model.UsageLimitPause).
	Model     string   `json:"model,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Escalated string   `json:"escalated,omitempty"`
	// Team is the team whose work the pause holds: the connection's own.
	Team             string    `json:"team"`
	Framework        string    `json:"framework,omitempty"`
	Until            time.Time `json:"until"`
	Reason           string    `json:"reason,omitempty"`
	SecondsRemaining float64   `json:"secondsRemaining"`
	// CanLift is whether the caller may lift this pause by hand -- a
	// team admin of Team, or a system admin -- so the banner offers the
	// button only to somebody it would work for.
	CanLift bool `json:"canLift"`
}

// agentPauseResponse is GET /api/pause's and DELETE /api/pause's whole
// body. Enabled is false, with no status at all, when this UI was handed
// no gate to ask (Config.AgentPause nil) -- the same
// nil-means-unavailable convention sandboxHealthResponse.Enabled and
// MetricsCycles.Enabled already give, and a different answer from a
// deployment that simply is not paused.
type agentPauseResponse struct {
	Enabled bool              `json:"enabled"`
	Pause   *AgentPauseStatus `json:"pause,omitempty"`
	// Lifted is DELETE /api/pause's own report: whether that call is what
	// ended a pause, as opposed to finding none to end. Absent on a GET.
	Lifted *bool `json:"lifted,omitempty"`
}

// AgentPauseLift is which pauses a DELETE /api/pause asks to lift. Every
// field empty is every pause the caller may lift: all of them for a
// system admin, their own teams' for a team admin.
type AgentPauseLift struct {
	// Connection lifts that connection's pauses: the whole connection's
	// and each of its models' -- with Model as well, only the pause on
	// that model there.
	Connection string
	Model      string
	// Team lifts that team's pauses -- with Framework as well, only its
	// pause on Framework runs on no connection.
	Team      string
	Framework string
}

func (l AgentPauseLift) matches(p model.UsageLimitPause) bool {
	if l.Connection != "" && l.Connection != p.Connection {
		return false
	}
	if l.Model != "" && (p.Model == "" || !strings.EqualFold(strings.TrimSpace(l.Model), p.Model)) {
		return false
	}
	if l.Team != "" && !strings.EqualFold(teamOrDefault(l.Team), teamOrDefault(p.Team)) {
		return false
	}
	if l.Framework != "" && (p.Connection != "" ||
		model.NormalizeAgentFrameworkName(l.Framework) != model.NormalizeAgentFrameworkName(p.Framework)) {
		return false
	}
	return true
}

// errAgentPauseUnavailable is what the pause routes report when
// Config.AgentPause is nil -- see that field's own doc comment.
var errAgentPauseUnavailable = errors.New(
	"the agent usage-limit pause is not available: this deployment's UI is not wired to a reconcile loop that has one")

// AgentPause reports the usage-limit pauses in force that ctx's caller
// can see, and until when. ok is false when this UI has no gate to ask
// at all, which is not the same as an unpaused one.
func (c *Client) AgentPause(ctx context.Context) (status AgentPauseStatus, ok bool, err error) {
	if c.Config.AgentPause == nil {
		return AgentPauseStatus{}, false, nil
	}
	status, err = c.agentPauseStatus(ctx)
	return status, err == nil, err
}

// LiftAgentPause clears by hand the pauses lift names that ctx's caller
// may lift, reporting the state it left behind and whether there was a
// pause to clear. An operator who has topped a plan up, or moved a team
// onto another connection, is holding information this process cannot
// have -- see Pause.LiftWhere.
func (c *Client) LiftAgentPause(ctx context.Context, lift AgentPauseLift) (status AgentPauseStatus, lifted bool, err error) {
	if c.Config.AgentPause == nil {
		return AgentPauseStatus{}, false, errAgentPauseUnavailable
	}
	cleared := c.Config.AgentPause.LiftWhere(func(p model.UsageLimitPause) bool {
		return lift.matches(p)
	})
	status, err = c.agentPauseStatus(ctx)
	return status, len(cleared) > 0, err
}

// agentPauseStatus turns the gate's pauses into ctx's caller's reading:
// those of the teams they can see, each said with whether they may lift
// it. A pause whose instant has already passed is left out: it is one
// the reconcile loop has not looked at since it expired (Pause.Blocked
// is what clears it, and only that loop calls it), and reporting it
// would tell an operator dispatch is stopped when the very next tick
// resumes it.
func (c *Client) agentPauseStatus(ctx context.Context) (AgentPauseStatus, error) {
	now := c.now()
	pauses := c.Config.AgentPause.Pauses(now)
	if len(pauses) == 0 {
		return AgentPauseStatus{}, nil
	}
	names := c.connectionNames(ctx)
	var status AgentPauseStatus
	for _, p := range pauses {
		if !now.Before(p.Until) {
			continue
		}
		entry := AgentPauseEntry{
			Connection:       p.Connection,
			ConnectionName:   names[p.Connection],
			Model:            p.Model,
			Labels:           p.Labels,
			Escalated:        p.Escalated,
			Team:             p.Team,
			Framework:        p.Framework,
			Until:            p.Until.UTC(),
			Reason:           p.Reason,
			SecondsRemaining: p.Until.Sub(now).Seconds(),
			CanLift:          true,
		}
		status.Pauses = append(status.Pauses, entry)
		if !status.Paused || entry.Until.After(status.Until) {
			status.Paused = true
			status.Until, status.Reason, status.SecondsRemaining = entry.Until, entry.Reason, entry.SecondsRemaining
		}
	}
	return status, nil
}

// connectionNames is each configured connection's display name by ID,
// for labelling a pause; empty when the store cannot say.
func (c *Client) connectionNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	if c.Store == nil {
		return names
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return names
	}
	for _, b := range cfg.AgentBackends {
		if name := strings.TrimSpace(b.Name); name != "" {
			names[b.ID] = name
		}
	}
	return names
}

func (s *Server) handleGetAgentPause(w http.ResponseWriter, r *http.Request) {
	status, ok, err := s.tasks.AgentPause(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, agentPauseResponse{Enabled: false})
		return
	}
	writeJSON(w, http.StatusOK, agentPauseResponse{Enabled: true, Pause: &status})
}

// handleLiftAgentPause answers DELETE /api/pause: ?connection= lifts
// that connection's pauses -- with &model=, only that model's pause on
// it -- ?team= (and &framework=) that team's, and none of them lifts
// every pause the caller may lift. Who may is
// Client.LiftAgentPause's, and asked first.
//
// An unwired deployment answers 404 -- "this deployment does not offer
// that" -- rather than the 200-with-enabled-false a GET gives: reporting
// success on an action that did nothing is worse than saying there is
// nothing here to act on, which is the same line handleRecreateSandbox
// draws for its own missing feature.
//
// Lifting a pause that is not there is not an error, though. The button
// that sends this is on a banner an operator and an expiring window are
// both racing to clear, and "there was nothing to lift" is a perfectly
// good outcome for whoever wanted dispatch running again.
func (s *Server) handleLiftAgentPause(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, lifted, err := s.tasks.LiftAgentPause(r.Context(), AgentPauseLift{
		Connection: strings.TrimSpace(q.Get("connection")),
		Model:      strings.TrimSpace(q.Get("model")),
		Team:       strings.TrimSpace(q.Get("team")),
		Framework:  strings.TrimSpace(q.Get("framework")),
	})
	if errors.Is(err, errAgentPauseUnavailable) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agentPauseResponse{Enabled: true, Pause: &status, Lifted: &lifted})
}
