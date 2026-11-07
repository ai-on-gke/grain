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
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatchcondition"
)

// DispatchConditionReader is Config.DispatchConditions' shape: the
// conditions holding dispatch now, and every set and clear this process
// has seen. *dispatchcondition.Set is one.
type DispatchConditionReader interface {
	Active() []dispatchcondition.Condition
	History() []dispatchcondition.Event
	// Version moves on every set and clear (dispatchcondition.Set.Version).
	Version() uint64
}

// dispatchConditionVersion is what the task caches fold into the
// store's change token: a condition set or cleared changes every queued
// task's reason without a store write, and a cache keyed on the store
// alone would serve the old reason until something else was committed.
func (c *Client) dispatchConditionVersion() uint64 {
	if c.Config.DispatchConditions == nil {
		return 0
	}
	return c.Config.DispatchConditions.Version()
}

// DispatchCondition is one condition as the API reports it: the
// condition itself and, so a reader need not do the arithmetic, how
// long it has held.
type DispatchCondition struct {
	dispatchcondition.Condition
	// Held is how long the condition has been in force, as a duration
	// string ("12m30s").
	Held string `json:"held"`
	// Check is the condition as a failing health check's one line, the
	// same sentence a queued task's reason carries.
	Check string `json:"check"`
}

// dispatchBlocked is the conditions in force, for the health and config
// responses; nil when there are none or nothing is wired.
func (c *Client) dispatchBlocked() []DispatchCondition {
	if c.Config.DispatchConditions == nil {
		return nil
	}
	active := c.Config.DispatchConditions.Active()
	if len(active) == 0 {
		return nil
	}
	now := c.now()
	out := make([]DispatchCondition, 0, len(active))
	for _, cond := range active {
		out = append(out, DispatchCondition{
			Condition: cond,
			Held:      now.Sub(cond.Since).Round(time.Second).String(),
			Check:     "dispatch blocked -- " + cond.Sentence(),
		})
	}
	return out
}

// dispatchChanges is every set and clear as a pipeline-feed entry
// (PipelineChangeDispatch): ID is the condition's name, To "blocked"
// or "dispatching", and Detail the error -- or, on a clear, how long it
// held.
func (c *Client) dispatchChanges() []PipelineChange {
	if c.Config.DispatchConditions == nil {
		return nil
	}
	var out []PipelineChange
	for _, e := range c.Config.DispatchConditions.History() {
		ch := PipelineChange{At: e.At, ID: e.Condition.Name, Kind: PipelineChangeDispatch}
		if e.Set {
			ch.To, ch.Detail = "blocked", e.Condition.Error
		} else {
			ch.From, ch.To = "blocked", "dispatching"
			ch.Detail = "cleared after " + e.At.Sub(e.Condition.Since).Round(time.Second).String()
		}
		out = append(out, ch)
	}
	return out
}
