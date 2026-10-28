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
	"fmt"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The kind tags a task can carry (docs/design/durable-tasks.md#DT-13):
// the small fixed vocabulary a judgement agent describes a task by, and
// the one a mutating call's expect is checked against. Each is read off a
// field the record already has, so an agent saying "port" about a task
// can be checked against the same rule that drew the tag.
const (
	TaskKindPort     = "port"
	TaskKindRepair   = "repair"
	TaskKindSession  = "session"
	TaskKindProposal = "proposal"
	TaskKindDurable  = "durable"
)

// TaskKinds lists the vocabulary in its documented order.
var TaskKinds = []string{TaskKindPort, TaskKindRepair, TaskKindSession, TaskKindProposal, TaskKindDurable}

// DurableCapability is the grant that makes a task durable (DT-1). It is
// named here before its provider lands so the tag reads it from day one.
const DurableCapability = model.DurableCapability

// TaskKindTags is t's kind tags, in TaskKinds' order.
func TaskKindTags(t Task) []string {
	var out []string
	if t.PortOf != "" {
		out = append(out, TaskKindPort)
	}
	if t.FixesBaseBranch || t.GrainBaseRepair {
		out = append(out, TaskKindRepair)
	}
	if t.Interactive || t.ConsoleSession != "" {
		out = append(out, TaskKindSession)
	}
	if t.State == model.StateProposed {
		out = append(out, TaskKindProposal)
	}
	if slices.Contains(t.Capabilities, DurableCapability) {
		out = append(out, TaskKindDurable)
	}
	return out
}

// TaskExpectation is what a mutating call says it believes about the task
// it acts on. Every field is optional; an empty one is not checked.
type TaskExpectation struct {
	State       string
	TitlePrefix string
	// Kind is one tag the task must carry, or "none" for a task that
	// carries no tag at all.
	Kind string
}

// Empty reports whether the expectation checks nothing.
func (e TaskExpectation) Empty() bool {
	return e.State == "" && e.TitlePrefix == "" && e.Kind == ""
}

// Check is nil when t matches every field of e, and otherwise names each
// field that disagrees, with both versions, so a refusal says what the
// record holds as well as what was believed.
func (e TaskExpectation) Check(t Task) error {
	var wrong []string
	if e.State != "" && !strings.EqualFold(e.State, string(t.State)) {
		wrong = append(wrong, fmt.Sprintf("state is %s, not %s", t.State, e.State))
	}
	if e.TitlePrefix != "" && !strings.HasPrefix(strings.ToLower(t.Title), strings.ToLower(e.TitlePrefix)) {
		wrong = append(wrong, fmt.Sprintf("title is %q, which does not start %q", t.Title, e.TitlePrefix))
	}
	if e.Kind != "" {
		tags := TaskKindTags(t)
		switch {
		case strings.EqualFold(e.Kind, "none"):
			if len(tags) > 0 {
				wrong = append(wrong, fmt.Sprintf("kind is %s, not none", strings.Join(tags, ", ")))
			}
		case !slices.Contains(tags, strings.ToLower(e.Kind)):
			have := "none"
			if len(tags) > 0 {
				have = strings.Join(tags, ", ")
			}
			wrong = append(wrong, fmt.Sprintf("kind is %s, not %s", have, e.Kind))
		}
	}
	if len(wrong) == 0 {
		return nil
	}
	return fmt.Errorf("task %s does not match what this call expected: %s. Nothing was changed -- "+
		"re-read it with read_grain_task before deciding what to do", t.ID, strings.Join(wrong, "; "))
}
