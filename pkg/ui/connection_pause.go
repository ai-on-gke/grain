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
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// An operator who edits a model connection in Settings -- a new key, a
// different auth mode, other models, the connection re-enabled -- has
// usually done so because it stopped working, and that fix takes effect
// at once: the connection's usage-limit pauses (orchestrator.Pause) are
// lifted with the save, the way the daemon's pool forgets a connection
// it marked unavailable once its saved form changes. This mattered less
// while a pause was in-process only and a restart forgot it anyway; now
// a pause survives the restart, an edit is how an operator says the old
// refusal no longer applies.
//
// What is lifted is the pauses on the connection itself, keyed as a
// pause is by the connection owning the credential
// (model.AgentBackend.CredentialOwnerID): editing a connection that
// borrows another's key does not lift that key's pause, since the key
// it is still spending is the one that refused.

// liftEditedConnectionPauses lifts the pauses on every connection whose
// saved form differs between before and after, or that after no longer
// has at all.
func (c *Client) liftEditedConnectionPauses(before, after []model.AgentBackend) {
	if c.Config.AgentPause == nil {
		return
	}
	next := make(map[string]model.AgentBackend, len(after))
	for _, b := range after {
		next[b.ID] = b
	}
	var edited []string
	for _, prev := range before {
		cur, ok := next[prev.ID]
		if !ok || connectionEdited(prev, cur) {
			edited = append(edited, prev.ID)
		}
	}
	c.liftConnectionPauses(edited...)
}

// liftConnectionPauses lifts every pause on the connections ids.
func (c *Client) liftConnectionPauses(ids ...string) {
	if c.Config.AgentPause == nil || len(ids) == 0 {
		return
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	lifted := c.Config.AgentPause.LiftWhere(func(p model.UsageLimitPause) bool {
		return p.Connection != "" && want[p.Connection]
	})
	for _, p := range lifted {
		log.Printf("ui: connection %s was edited in Settings; its usage-limit pause until %s is lifted", p.Connection, p.Until.Format(time.RFC3339))
	}
}

// connectionEdited reports whether a and b, two saved forms of one
// connection, differ in anything an operator would edit to fix it:
// everything but its display name and the models discovery last found
// on it, which grain writes itself.
func connectionEdited(a, b model.AgentBackend) bool {
	a.Name, b.Name = "", ""
	a.Discovered, b.Discovered = nil, nil
	// As JSON, as the store holds it, so a nil and an empty list are
	// the same saved form.
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA != nil || errB != nil || !bytes.Equal(ja, jb)
}
