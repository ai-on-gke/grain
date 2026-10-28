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

// grain/task-132: the agent usage-limit pause as an operator sees it --
// GET /api/pause, DELETE /api/pause, and the copy of the same reading
// GET /api/config carries for the banner.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakePause stands in for *orchestrator.Pause, which pkg/ui cannot
// import (ui.AgentPause's own doc comment). It keeps its own state and
// counts lifts, so a test can assert not just what a read reported but
// that reading left the gate alone -- the property Pauses' not expiring
// anything exists for.
type fakePause struct {
	pauses []model.UsageLimitPause
	lifts  int
}

// pauseOf is a fakePause holding one pause, on the default team's claude
// connection.
func pauseOf(until time.Time, reason string) *fakePause {
	return &fakePause{pauses: []model.UsageLimitPause{{
		Connection: "default-claude", Team: model.DefaultTeamID, Framework: "claude", Until: until, Reason: reason,
	}}}
}

func (p *fakePause) Pauses(now time.Time) []model.UsageLimitPause {
	var out []model.UsageLimitPause
	for _, pause := range p.pauses {
		if now.Before(pause.Until) {
			out = append(out, pause)
		}
	}
	return out
}

func (p *fakePause) LiftWhere(match func(model.UsageLimitPause) bool) []model.UsageLimitPause {
	p.lifts++
	var kept, lifted []model.UsageLimitPause
	for _, pause := range p.pauses {
		if match(pause) {
			lifted = append(lifted, pause)
		} else {
			kept = append(kept, pause)
		}
	}
	p.pauses = kept
	return lifted
}

func (p *fakePause) Lift() bool {
	return len(p.LiftWhere(func(model.UsageLimitPause) bool { return true })) > 0
}

type pauseEntryBody struct {
	Connection     string    `json:"connection"`
	ConnectionName string    `json:"connectionName"`
	Team           string    `json:"team"`
	Framework      string    `json:"framework"`
	Until          time.Time `json:"until"`
	CanLift        bool      `json:"canLift"`
}

type pauseBody struct {
	Enabled bool `json:"enabled"`
	Pause   *struct {
		Paused           bool             `json:"paused"`
		Until            time.Time        `json:"until"`
		Reason           string           `json:"reason"`
		SecondsRemaining float64          `json:"secondsRemaining"`
		Pauses           []pauseEntryBody `json:"pauses"`
	} `json:"pause"`
	Lifted *bool `json:"lifted"`
}

func TestGetPauseReportsUnavailableWithNoGateWired(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/pause", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[pauseBody](t, rec)
	if got.Enabled {
		t.Fatal("enabled = true, want false with no Config.AgentPause")
	}
	if got.Pause != nil {
		t.Fatalf("pause = %+v, want none reported at all", *got.Pause)
	}
}

func TestGetPauseReportsTheCurrentWindow(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	until := baseTime.Add(4 * time.Hour)
	client.Config.AgentPause = pauseOf(until, "claude: usage limit reached; resets at "+until.Format(time.RFC3339))

	rec := do(t, srv, http.MethodGet, "/api/pause", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[pauseBody](t, rec)
	if !got.Enabled || got.Pause == nil || !got.Pause.Paused {
		t.Fatalf("got %+v, want an enabled, paused reading", got)
	}
	if !got.Pause.Until.Equal(until) {
		t.Fatalf("until = %s, want %s", got.Pause.Until, until)
	}
	if got.Pause.SecondsRemaining != (4 * time.Hour).Seconds() {
		t.Fatalf("secondsRemaining = %v, want %v", got.Pause.SecondsRemaining, (4 * time.Hour).Seconds())
	}
	// The provider's own sentence, verbatim: it names the framework and
	// the window, which is the half of this an operator can act on.
	if got.Pause.Reason == "" {
		t.Fatal("reason is empty, want the provider's own sentence")
	}
}

func TestGetPauseReportsAnUnpausedDeployment(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.AgentPause = &fakePause{}

	got := decode[pauseBody](t, do(t, srv, http.MethodGet, "/api/pause", ""))
	if !got.Enabled {
		t.Fatal("enabled = false, want true: a gate is wired, it is simply not shut")
	}
	if got.Pause == nil || got.Pause.Paused {
		t.Fatalf("got %+v, want a reading that says nothing is paused", got)
	}
}

// An expired pause is one the reconcile loop has not looked at since its
// window ran out -- only that loop's own Pause.Blocked clears one. It
// reads here as not paused, and this read leaves it exactly as it found
// it: a UI poll that could clear a pause would open the gate out from
// under the loop that owns it.
func TestGetPauseReadsAnExpiredWindowWithoutClearingIt(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	pause := pauseOf(baseTime.Add(-time.Minute), "gemini: usage limit reached")
	client.Config.AgentPause = pause

	got := decode[pauseBody](t, do(t, srv, http.MethodGet, "/api/pause", ""))
	if got.Pause == nil || got.Pause.Paused {
		t.Fatalf("got %+v, want an expired window to read as not paused", got)
	}
	if pause.lifts != 0 {
		t.Fatalf("lifts = %d, want 0: reading a pause must not clear one", pause.lifts)
	}
	if len(pause.pauses) == 0 {
		t.Fatal("the gate itself was cleared by a read; only the reconcile loop may do that")
	}
}

func TestDeletePauseLiftsItAndSaysSo(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	pause := pauseOf(baseTime.Add(time.Hour), "claude: usage limit reached")
	client.Config.AgentPause = pause

	rec := do(t, srv, http.MethodDelete, "/api/pause", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[pauseBody](t, rec)
	if got.Lifted == nil || !*got.Lifted {
		t.Fatalf("lifted = %v, want true", got.Lifted)
	}
	if got.Pause == nil || got.Pause.Paused {
		t.Fatalf("got %+v, want the state left behind to be unpaused", got)
	}

	// Lifting a pause that is not there is not an error: the banner's
	// own button and an expiring window are racing to do the same thing.
	rec = do(t, srv, http.MethodDelete, "/api/pause", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("second delete status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got = decode[pauseBody](t, rec)
	if got.Lifted == nil || *got.Lifted {
		t.Fatalf("lifted = %v on the second call, want false", got.Lifted)
	}
}

func TestDeletePauseIsNotFoundWithNoGateWired(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)

	rec := do(t, srv, http.MethodDelete, "/api/pause", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// The banner reads this copy rather than polling a second endpoint, so
// the two have to agree -- both come from the same Client.AgentPause.
func TestConfigCarriesThePauseForTheBanner(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	pause := pauseOf(baseTime.Add(90*time.Minute), "claude: usage limit reached")
	client.Config.AgentPause = pause

	got := decode[struct {
		AgentPause *struct {
			Paused           bool      `json:"paused"`
			Until            time.Time `json:"until"`
			Reason           string    `json:"reason"`
			SecondsRemaining float64   `json:"secondsRemaining"`
		} `json:"agentPause"`
	}](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got.AgentPause == nil {
		t.Fatal("agentPause is absent from /api/config while dispatch is paused")
	}
	if !got.AgentPause.Paused || got.AgentPause.SecondsRemaining != (90*time.Minute).Seconds() {
		t.Fatalf("agentPause = %+v, want a paused reading 90 minutes from resuming", *got.AgentPause)
	}

	// And nothing at all once it is lifted: a banner draws only when
	// this field is there, so an unpaused deployment must not send one.
	pause.Lift()
	got = decode[struct {
		AgentPause *struct {
			Paused           bool      `json:"paused"`
			Until            time.Time `json:"until"`
			Reason           string    `json:"reason"`
			SecondsRemaining float64   `json:"secondsRemaining"`
		} `json:"agentPause"`
	}](t, do(t, srv, http.MethodGet, "/api/config", ""))
	if got.AgentPause != nil {
		t.Fatalf("agentPause = %+v on an unpaused deployment, want it omitted", *got.AgentPause)
	}
}

// HTTPClient's own half of the same two routes (grain/task-182): what
// `grain pause` and `grain pause -lift` call, over a real round trip.
// The distinction worth proving here is the one an operator acts on --
// "not paused" and "this daemon has no gate to ask" are different
// answers, and only the first one means dispatch is running.
func pauseHTTPClient(t *testing.T, pause ui.AgentPause) (*ui.HTTPClient, context.Context) {
	t.Helper()
	client, _, ctx := testClient(t)
	storeSettingsRow(t, client)
	client.Config.AgentPause = pause
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)
	return accessCodeClient(client, srv.URL), ctx
}

func TestHTTPClientAgentPauseReadsTheWindowWithoutClearingIt(t *testing.T) {
	t.Parallel()
	until := baseTime.Add(2 * time.Hour)
	pause := pauseOf(until, "claude: usage limit reached; resets at "+until.Format(time.RFC3339))
	c, ctx := pauseHTTPClient(t, pause)

	status, enabled, err := c.AgentPause(ctx)
	if err != nil {
		t.Fatalf("AgentPause: %v", err)
	}
	if !enabled || !status.Paused {
		t.Fatalf("AgentPause = %+v, enabled %t; want an enabled, paused reading", status, enabled)
	}
	if !status.Until.Equal(until) || status.SecondsRemaining != (2*time.Hour).Seconds() {
		t.Fatalf("AgentPause = %+v, want until %s and two hours remaining", status, until)
	}
	if status.Reason == "" {
		t.Error("reason is empty, want the provider's own sentence")
	}
	if pause.lifts != 0 || len(pause.pauses) == 0 {
		t.Errorf("reading through the CLI cleared the gate (lifts = %d): only the reconcile loop may do that", pause.lifts)
	}
}

func TestHTTPClientAgentPauseSeparatesUnpausedFromUnwired(t *testing.T) {
	t.Parallel()
	c, ctx := pauseHTTPClient(t, &fakePause{})
	status, enabled, err := c.AgentPause(ctx)
	if err != nil {
		t.Fatalf("AgentPause: %v", err)
	}
	if !enabled || status.Paused {
		t.Fatalf("AgentPause = %+v, enabled %t; want an enabled reading that says nothing is paused", status, enabled)
	}

	// No gate wired at all: not an error -- the route answers 200 -- but
	// enabled false, so a CLI can say it has been told nothing rather
	// than report a deployment it cannot see into as running.
	unwired, _, ctx := testClient(t)
	srv := httptest.NewServer(ui.NewServerWithClient(unwired))
	t.Cleanup(srv.Close)
	_, enabled, err = accessCodeClient(unwired, srv.URL).AgentPause(ctx)
	if err != nil {
		t.Fatalf("AgentPause with no gate wired: %v", err)
	}
	if enabled {
		t.Error("enabled = true with no Config.AgentPause")
	}
}

func TestHTTPClientLiftAgentPause(t *testing.T) {
	t.Parallel()
	pause := pauseOf(baseTime.Add(time.Hour), "gemini: usage limit reached")
	c, ctx := pauseHTTPClient(t, pause)

	status, lifted, err := c.LiftAgentPause(ctx, ui.AgentPauseLift{})
	if err != nil {
		t.Fatalf("LiftAgentPause: %v", err)
	}
	if !lifted || status.Paused {
		t.Fatalf("LiftAgentPause = %+v, lifted %t; want it lifted and the state left behind unpaused", status, lifted)
	}

	// Lifting again is not an error: an operator's own command and an
	// expiring window are racing to do the same thing.
	if _, lifted, err = c.LiftAgentPause(ctx, ui.AgentPauseLift{}); err != nil {
		t.Fatalf("second LiftAgentPause: %v", err)
	}
	if lifted {
		t.Error("lifted = true on the second call, want false: there was nothing left to lift")
	}
}

// A lift against a deployment with no gate is an error rather than a
// quiet success, so `grain pause -lift` cannot report having resumed a
// deployment it never touched.
func TestHTTPClientLiftAgentPauseErrsWithNoGateWired(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	storeSettingsRow(t, client)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	if _, _, err := accessCodeClient(client, srv.URL).LiftAgentPause(ctx, ui.AgentPauseLift{}); err == nil {
		t.Fatal("LiftAgentPause with no gate wired returned no error")
	}
}

// A pause on one model of a connection says which model, and DELETE
// /api/pause?connection=&model= lifts that model's pause alone -- not the
// same connection's pause on another model, nor its escalated one.
func TestPauseReportsAndLiftsOneModel(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	until := baseTime.Add(time.Hour)
	pause := &fakePause{pauses: []model.UsageLimitPause{
		{Connection: "default-gemini", Model: "gemini-3-pro", Labels: []string{"auto"}, Team: model.DefaultTeamID, Framework: "antigravity", Until: until},
		{Connection: "default-gemini", Model: "gemini-3.8-flash-high", Team: model.DefaultTeamID, Framework: "antigravity", Until: until},
		{Connection: "default-claude", Team: model.DefaultTeamID, Framework: "claude", Until: until, Escalated: "the provider's refusal is account-wide"},
	}}
	client.Config.AgentPause = pause

	type entry struct {
		Connection string   `json:"connection"`
		Model      string   `json:"model"`
		Labels     []string `json:"labels"`
		Escalated  string   `json:"escalated"`
	}
	type body struct {
		Lifted *bool `json:"lifted"`
		Pause  *struct {
			Pauses []entry `json:"pauses"`
		} `json:"pause"`
	}
	got := decode[body](t, do(t, srv, http.MethodGet, "/api/pause", ""))
	if got.Pause == nil || len(got.Pause.Pauses) != 3 {
		t.Fatalf("GET /api/pause = %+v, want three pauses", got.Pause)
	}
	first := got.Pause.Pauses[0]
	if first.Model != "gemini-3-pro" || len(first.Labels) != 1 || first.Labels[0] != "auto" {
		t.Errorf("first pause = %+v, want gemini-3-pro holding auto", first)
	}
	if esc := got.Pause.Pauses[2]; esc.Model != "" || !strings.Contains(esc.Escalated, "account-wide") {
		t.Errorf("escalated pause = %+v, want the whole connection and why", esc)
	}

	rec := do(t, srv, http.MethodDelete, "/api/pause?connection=default-gemini&model=Gemini-3-Pro", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d: %s", rec.Code, rec.Body)
	}
	got = decode[body](t, rec)
	if got.Lifted == nil || !*got.Lifted {
		t.Fatalf("lifted = %v, want true", got.Lifted)
	}
	var left []string
	for _, p := range got.Pause.Pauses {
		left = append(left, p.Connection+"/"+p.Model)
	}
	if strings.Join(left, ",") != "default-gemini/gemini-3.8-flash-high,default-claude/" {
		t.Errorf("pauses left = %v, want flash's and claude's", left)
	}

	// A model named on a connection whose pause is the whole connection
	// lifts nothing: that pause is not the model's.
	got = decode[body](t, do(t, srv, http.MethodDelete, "/api/pause?connection=default-claude&model=claude-opus-4", ""))
	if got.Lifted == nil || *got.Lifted {
		t.Errorf("lifting a model of a connection-wide pause lifted = %v, want false", got.Lifted)
	}
	// And the connection alone lifts every pause on it.
	got = decode[body](t, do(t, srv, http.MethodDelete, "/api/pause?connection=default-gemini", ""))
	if got.Lifted == nil || !*got.Lifted || len(got.Pause.Pauses) != 1 {
		t.Errorf("lifting the connection = %+v, want only claude's pause left", got)
	}
}
