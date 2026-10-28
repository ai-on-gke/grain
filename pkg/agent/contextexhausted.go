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

package agent

import (
	"errors"
	"strings"
)

// ErrContextExhausted is a Framework's report that the run's conversation
// no longer fits the model's context window -- the CLI's "prompt is too
// long", or a compaction that gave up. A Framework that can tell wraps
// it; ContextExhausted also reads the words the CLIs use, for the ones
// that only say so in their error text.
//
// orchestrator.classifyRunEnd records such a run as a `context`
// suspension (docs/design/durable-tasks.md#DT-2): not the task's failure,
// and resumed in a fresh CLI session from the conversation plus a
// handover, since the session it was in is the thing that is full.
var ErrContextExhausted = errors.New("agent: the conversation no longer fits the model's context window")

// contextExhaustedMarkers are the phrases agent CLIs and their providers
// end a run with when its context is full, lower-cased.
var contextExhaustedMarkers = []string{
	"prompt is too long",
	"context_length_exceeded",
	"maximum context length",
	"exceeds the context window",
	"exceeded the context window",
	"context window exceeded",
	"input is too long for the model",
}

// ContextExhausted reports whether err says the run's context is full.
func ContextExhausted(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrContextExhausted) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, m := range contextExhaustedMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}
