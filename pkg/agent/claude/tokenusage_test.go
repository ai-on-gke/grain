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

package claude

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// Each exec's terminal result event carries its own usage, and a capture
// holding two execs (a re-exec after a transient refusal) spent both.
func TestParseTranscriptSumsResultUsageAcrossExecs(t *testing.T) {
	stdout := `{"type":"system","subtype":"init","session_id":"s1"}
{"type":"result","subtype":"error_during_execution","is_error":true,"result":"overloaded","num_turns":2,"total_cost_usd":0.25,"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50}}
{"type":"system","subtype":"init","session_id":"s1"}
{"type":"result","subtype":"success","result":"done","num_turns":3,"total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0}}
`
	res, err := parseTranscript(stdout)
	if err != nil {
		t.Fatal(err)
	}
	want := agent.TokenUsage{InputTokens: 110, OutputTokens: 25, CacheReadTokens: 3000, CacheWriteTokens: 50, CostUSD: 0.75, Turns: 5}
	if res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
	if got := res.Usage.Billable(); got != 185 {
		t.Fatalf("billable = %d, want 185", got)
	}
}
