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
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ObservationSelection is a deployment-local allowlist, not a task contract or
// policy. Empty disables collection and display. No elapsed timer changes it.
func ObservationSelection(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	ids := strings.Split(raw, ",")
	if len(ids) > 32 {
		return nil, fmt.Errorf("at most 32 observation task IDs")
	}
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if !isValidTaskSummaryID(id) || slices.Contains(ids[:i], id) {
			return nil, fmt.Errorf("invalid or duplicate observation task ID")
		}
		ids[i] = id
	}
	return ids, nil
}
func observationEnvironment() []string {
	ids, err := ObservationSelection(os.Getenv("GRAIN_GATE_OBSERVATION_TASKS"))
	if err != nil {
		log.Printf("gate observations disabled: %v", err)
		return nil
	}
	return ids
}
func (c *Client) observesGates(id string) bool {
	return slices.Contains(c.Config.GateObservationTaskIDs, id) && len(c.Config.GateObservationTaskIDs) <= 32
}
func (c *Client) gateObservations(ctx context.Context, id string) (*gates.ObservationView, *model.GateObservationDiagnostics) {
	if !c.observesGates(id) {
		return nil, nil
	}
	// A failed diagnostic never becomes a failed ordinary task read.
	readCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	now := c.now()
	v, d, err := c.Store.ObserveTaskGates(readCtx, id, now)
	if err == nil {
		return v, d
	}
	// No raw database errors, comment bodies, principals or credentials escape.
	unavailable := gates.UnavailableObservation(id, now)
	v = &unavailable
	// A fresh short budget can retain a failure even when the collection's
	// own 250ms budget expired. Never delay an already canceled request.
	if ctx.Err() == nil {
		failureCtx, stop := context.WithTimeout(ctx, 50*time.Millisecond)
		_ = c.Store.RecordGateObservationFailure(failureCtx, id, now, v)
		stop()
	}
	return v, nil
}
