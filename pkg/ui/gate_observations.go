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
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	v, d, err := c.Store.ObserveTaskGates(readCtx, id, now, c.Config.GateObservationChildren[id]...)
	if err == nil {
		c.redactGateHistoryScopes(readCtx, id, v, d)
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

// ObservationChildSelection configures comparisons, never requirements or waits.
// The task allowlist independently controls whether anything is collected.
func ObservationChildSelection(raw string) (map[string][]gates.ChildrenConfig, error) {
	out := map[string][]gates.ChildrenConfig{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if len(raw) > 64*1024 {
		return nil, fmt.Errorf("shadow child selection exceeds 64 KiB")
	}
	entries := map[string][]json.RawMessage{}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("child selections must be an object")
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		id, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("invalid parent ID")
		}
		if _, exists := entries[id]; exists {
			return nil, fmt.Errorf("duplicate parent selection")
		}
		var configs []json.RawMessage
		if err = decoder.Decode(&configs); err != nil {
			return nil, err
		}
		entries[id] = configs
		if len(entries) > 32 {
			return nil, fmt.Errorf("too many parents")
		}
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing child selection data")
	}
	if len(entries) > 32 {
		return nil, fmt.Errorf("at most 32 shadow parent selections")
	}
	for id, configs := range entries {
		if !isValidTaskSummaryID(id) || len(configs) > 3 {
			return nil, fmt.Errorf("invalid parent or too many child predicates")
		}
		children := map[string]bool{}
		for _, raw := range configs {
			canonical, err := gates.Validate("children", 1, raw)
			if err != nil {
				return nil, err
			}
			var config gates.ChildrenConfig
			_ = json.Unmarshal(canonical, &config)
			for _, child := range config.Tasks {
				children[child] = true
			}
			if len(children) > 64 {
				return nil, fmt.Errorf("at most 64 distinct selected children")
			}
			out[id] = append(out[id], config)
		}
	}
	return out, nil
}
func observationChildrenEnvironment() map[string][]gates.ChildrenConfig {
	selections, err := ObservationChildSelection(os.Getenv("GRAIN_GATE_OBSERVATION_CHILDREN"))
	if err != nil {
		log.Printf("gate child comparisons disabled: %v", err)
		return nil
	}
	return selections
}

// A child's permissions can change after a history sample was retained. Apply
// current task-read scope to every historical sidecar, not just today's result.
func (c *Client) redactGateHistoryScopes(ctx context.Context, parentID string, v *gates.ObservationView, d *model.GateObservationDiagnostics) {
	parent, err := c.Store.GetTask(ctx, parentID)
	if err != nil || parent == nil {
		return
	}
	allowed := map[string]bool{parentID: true}
	canRead := func(id string) bool {
		if permitted, ok := allowed[id]; ok {
			return permitted
		}
		task, err := c.Store.GetTask(ctx, id)
		permitted := err == nil && task != nil && gates.Digest(task.Target) == gates.Digest(parent.Target) && task.ChangeTarget.Equal(parent.ChangeTarget)
		if call, ok := agentCallFromContext(ctx); ok && permitted {
			permitted = call.grants.Allows("tasks:read", taskRepo(task))
		}
		allowed[id] = permitted
		return permitted
	}
	redact := func(view *gates.ObservationView) {
		for i := range view.Results {
			result := &view.Results[i].Result
			hidden := false
			for _, e := range result.Evidence {
				if e.Subject.Kind == "task" && !canRead(e.Subject.TaskID) {
					hidden = true
				}
			}
			if hidden {
				result.Status = "unknown"
				result.Summary = "Related evidence is unavailable in the current read scope."
				result.Evidence = []gates.Evidence{}
				result.Dependencies = []gates.Dependency{}
				result.Waits = []gates.Wait{}
				result.HumanText = nil
				result.AgentText = nil
			}
		}
	}
	redact(v)
	if d != nil {
		for i := range d.History {
			redact(&d.History[i].View)
		}
	}
}
