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

package antigravity

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// agy's result event carries a usage block (docs/agy-surface.md); thinking
// counts as output and cache reads come out of the input.
func TestParseEventsReadsResultUsage(t *testing.T) {
	stdout := `{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"ok","num_turns":4,"usage":{"input_tokens":5000,"output_tokens":300,"thinking_tokens":200,"cache_read_tokens":4000,"total_tokens":5500}}}
`
	got := parseEvents(stdout).result.Usage
	want := agent.TokenUsage{InputTokens: 1000, CacheReadTokens: 4000, OutputTokens: 500, Turns: 4}
	if got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}
