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

package codex

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// turn.completed's usage counts cached input inside input_tokens; the
// parsed usage moves it out so a budget does not count it at par.
func TestParseEventsReadsTurnCompletedUsage(t *testing.T) {
	stdout := `{"type":"thread.started","thread_id":"t1"}
{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"hi"}}
{"type":"turn.completed","usage":{"input_tokens":1200,"cached_input_tokens":800,"output_tokens":90}}
`
	got := parseEvents(stdout).result.Usage
	want := agent.TokenUsage{InputTokens: 400, CacheReadTokens: 800, OutputTokens: 90, Turns: 1}
	if got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}
