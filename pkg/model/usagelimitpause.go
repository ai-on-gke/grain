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

package model

import (
	"strings"
	"time"
)

// CredentialOwnerID is the connection whose credential b spends: the
// connection it borrows its key from (CredentialBackendID) when it
// borrows one, and b itself otherwise. It is what a provider's usage
// limit belongs to, so it is what a usage-limit pause is keyed by
// (orchestrator.Pause): two connections spending the same key meet the
// same refusal, and two spending different keys -- even of the same
// framework, even in the same team -- do not.
func (b AgentBackend) CredentialOwnerID() string {
	if id := strings.TrimSpace(b.CredentialBackendID); id != "" {
		return id
	}
	return b.ID
}

// UsageLimitPause is one usage-limit pause in force (orchestrator.Pause),
// as the code outside the orchestrator reads it -- the UI's banner and
// GET /api/pause, which may show it only to the team it belongs to.
//
// It names the credential that met the limit rather than the run that
// did: Connection is the connection that owns that credential
// (AgentBackend.CredentialOwnerID), and Team is that connection's team.
// Connection is empty for a pause on a run that spent no connection's
// key -- a deployment-level credential (the legacy single-framework
// settings), or a run whose framework could not say which connection it
// was on -- which covers Team's runs on Framework, or every run of Team
// when Framework is empty too. No pause covers more than one team.
//
// Model narrows a connection's pause to one model on it: the model the
// refused run was on (lower-cased), or the one the provider's error
// named. Labels are the other model names grain passed runs that model
// refused -- "auto", for a provider-chosen run the CLI reported was
// refused on Model -- which the pause covers too. A connection's pause
// with no Model covers the whole connection; Escalated says why, when it
// grew from a model's pause (an account-wide refusal, or a second model
// spent while the first was still paused), and is empty for one met by a
// run whose model grain could not name.
type UsageLimitPause struct {
	Connection string    `json:"connection,omitempty"`
	Model      string    `json:"model,omitempty"`
	Labels     []string  `json:"labels,omitempty"`
	Team       string    `json:"team"`
	Framework  string    `json:"framework,omitempty"`
	Until      time.Time `json:"until"`
	Reason     string    `json:"reason,omitempty"`
	Escalated  string    `json:"escalated,omitempty"`
}

// ModelScoped reports whether p pauses one model on its connection,
// rather than the whole connection or a team's runs on no connection.
func (p UsageLimitPause) ModelScoped() bool {
	return p.Connection != "" && p.Model != ""
}
