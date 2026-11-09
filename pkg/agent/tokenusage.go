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

// TokenUsage is what one run (or one turn of one) spent at its model
// provider, as the framework's own CLI reported it: claude's terminal
// "result" event's usage block, codex's "turn.completed" usage, agy's
// "result" usage. Zero is "the CLI reported nothing", not "the run was
// free" -- see Reported.
//
// The counts are the provider's own, kept apart rather than summed so a
// reader can weigh them the way its provider bills them:
//
//   - InputTokens is uncached prompt input. claude's input_tokens already
//     excludes cache reads; codex and agy report input including what
//     they read from cache, so their parsers subtract CacheReadTokens.
//   - OutputTokens is generated output, thinking included where the CLI
//     reports thinking separately (agy's thinking_tokens).
//   - CacheReadTokens is prompt input served from the provider's cache.
//   - CacheWriteTokens is prompt input written to it (claude's
//     cache_creation_input_tokens).
//
// CostUSD is the provider's own cost figure where it reports one
// (claude's total_cost_usd); grain does not price tokens itself.
type TokenUsage struct {
	InputTokens      int64   `json:"inputTokens,omitempty"`
	OutputTokens     int64   `json:"outputTokens,omitempty"`
	CacheReadTokens  int64   `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64   `json:"cacheWriteTokens,omitempty"`
	CostUSD          float64 `json:"costUsd,omitempty"`
	// Turns is how many model turns the counts cover, where the CLI
	// says (one per usage record otherwise).
	Turns int `json:"turns,omitempty"`
}

// Add is u and v summed: a run that re-exec'd its CLI, or was resumed
// for the stop rule or a session turn, spent both.
func (u TokenUsage) Add(v TokenUsage) TokenUsage {
	return TokenUsage{
		InputTokens:      u.InputTokens + v.InputTokens,
		OutputTokens:     u.OutputTokens + v.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + v.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + v.CacheWriteTokens,
		CostUSD:          u.CostUSD + v.CostUSD,
		Turns:            u.Turns + v.Turns,
	}
}

// Reported says whether the CLI reported any usage at all.
func (u TokenUsage) Reported() bool {
	return u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheReadTokens != 0 || u.CacheWriteTokens != 0 || u.CostUSD != 0
}

// Billable is the figure a budget is counted in: input, output and
// cache writes. Cache reads are left out -- every provider grain drives
// discounts them heavily, and counting them at par would make a
// long-context run look several times its real cost.
func (u TokenUsage) Billable() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheWriteTokens
}
