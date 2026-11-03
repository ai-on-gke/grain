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
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// RemovedCustomConnectionReason is the DisabledReason of a connection of
// the retired custom command framework (RemovedAgentFrameworkCustom).
const RemovedCustomConnectionReason = "custom command connections were removed in v3.2.0; " +
	"replace it with an Antigravity, Claude or Codex connection"

// IsRemovedCustomConnection reports whether b is a connection of the
// retired custom command framework.
func (b AgentBackend) IsRemovedCustomConnection() bool {
	return strings.TrimSpace(b.Framework) == RemovedAgentFrameworkCustom
}

// DisableRemovedCustomConnections disables every custom command
// connection in backends that is not already disabled for that reason,
// keeping it rather than deleting it, and returns the IDs it changed.
// backends is changed in place.
func DisableRemovedCustomConnections(backends []AgentBackend) []string {
	var changed []string
	for i := range backends {
		b := &backends[i]
		if !b.IsRemovedCustomConnection() || (b.Disabled && b.DisabledReason == RemovedCustomConnectionReason) {
			continue
		}
		b.Disabled = true
		b.DisabledReason = RemovedCustomConnectionReason
		changed = append(changed, b.ID)
	}
	return changed
}

// LogRemovedCustomConnections logs each custom command connection in
// backends, so a deployment that still has one hears about it every time
// it starts until the connection is deleted.
func LogRemovedCustomConnections(backends []AgentBackend) {
	for _, b := range backends {
		if b.IsRemovedCustomConnection() {
			log.Printf("model connection %q (%s) is a custom command connection and is disabled: %s; delete it in Settings -> Models",
				b.ID, b.Name, RemovedCustomConnectionReason)
		}
	}
}

// disableCustomConnections is migration 103: every stored custom command
// connection is disabled and kept, with RemovedCustomConnectionReason, so
// a deployment that had one still starts and can see and delete it. It is
// idempotent, as ApplyDataMigrations needs.
func disableCustomConnections(ctx context.Context, tx *sql.Tx) error {
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT `agent_backends` FROM `grain_config` WHERE `id` = 1").Scan(&stored)
	if err == sql.ErrNoRows || (err == nil && strings.TrimSpace(stored) == "") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("grain_config.agent_backends: reading: %w", err)
	}
	// Unmarshalled here rather than through DecodeAgentBackends, which
	// disables a custom connection as it reads one and so would leave
	// nothing for this to see.
	var backends []AgentBackend
	if err := json.Unmarshal([]byte(stored), &backends); err != nil {
		// Left as it is: DecodeAgentBackends reports it on read, the
		// same with or without this.
		return nil
	}
	changed := DisableRemovedCustomConnections(backends)
	if len(changed) == 0 {
		return nil
	}
	encoded, err := EncodeAgentBackends(backends)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE `grain_config` SET `agent_backends` = ? WHERE `id` = 1", encoded); err != nil {
		return fmt.Errorf("grain_config.agent_backends: rewriting: %w", err)
	}
	for _, id := range changed {
		log.Printf("disabled model connection %q: %s", id, RemovedCustomConnectionReason)
	}
	return nil
}
