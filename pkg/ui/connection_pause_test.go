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

// grain/task-1259: a usage-limit pause survives a restart now, so editing
// the connection in Settings is what lifts it early -- the operator's fix
// takes effect at once, as it does for the pool's unavailable marks.

import (
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func connectionPauses(until time.Time) *fakePause {
	return &fakePause{pauses: []model.UsageLimitPause{
		{Connection: "infra-claude", Model: "claude-opus-5-5", Team: model.DefaultTeamID, Framework: "claude", Until: until},
		{Connection: "other", Team: model.DefaultTeamID, Framework: "claude", Until: until},
	}}
}

func pausedConnections(p *fakePause) map[string]bool {
	out := map[string]bool{}
	for _, pause := range p.pauses {
		out[pause.Connection] = true
	}
	return out
}

func TestEditingAConnectionLiftsItsPause(t *testing.T) {
	srv, client := cliPathTeam(t)
	until := time.Now().Add(6 * time.Hour)
	body := `{"framework":"claude","models":["claude-opus-5-5"]}`
	if code, resp := putConnection(t, srv, "", body); code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, resp)
	}

	// Saving the connection unchanged is no edit: the pause stands.
	pause := connectionPauses(until)
	client.Config.AgentPause = pause
	if code, resp := putConnection(t, srv, "", body); code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, resp)
	}
	if got := pausedConnections(pause); !got["infra-claude"] || !got["other"] {
		t.Fatalf("an unchanged save lifted pauses; left %v", got)
	}

	// Editing it lifts its own pause, and nobody else's.
	if code, resp := putConnection(t, srv, "", `{"framework":"claude","models":["claude-opus-5-5","claude-sonnet-5-5"]}`); code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, resp)
	}
	if got := pausedConnections(pause); got["infra-claude"] || !got["other"] {
		t.Fatalf("after editing infra-claude the pauses left are %v, want other's alone", got)
	}
}

func TestSettingAConnectionsCredentialLiftsItsPause(t *testing.T) {
	srv, client := cliPathTeam(t)
	pause := connectionPauses(time.Now().Add(6 * time.Hour))
	client.Config.AgentPause = pause
	rec := do(t, srv, http.MethodPut, "/api/agent-backends/infra-claude/credential", `{"value":"sk-new"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT credential = %d %s", rec.Code, rec.Body.String())
	}
	if got := pausedConnections(pause); got["infra-claude"] || !got["other"] {
		t.Fatalf("after a new key for infra-claude the pauses left are %v, want other's alone", got)
	}
}
