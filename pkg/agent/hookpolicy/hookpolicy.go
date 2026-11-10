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

// Package hookpolicy defines the versioned per-run hook policy file that the
// daemon stages beside a run's CLI configuration and that the hook
// subcommands (agy-tool-hook and claude-stop-hook) evaluate on the CLI's
// host.
//
// It imports nothing of grain's so that every binary answering a hook --
// cmd/grain and grain-agentd -- can link it without pulling in daemon, model
// or MCP packages (design/design/agentd-stable-contract.design.md, V1 and V2,
// feature hook_policy).
//
// Because the policy file is staged onto the host before the CLI starts,
// detached runs keep evaluating it locally even while the daemon restarts.
package hookpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Version is the current schema version of the per-run hook policy file.
// Additive optional fields stay at Version 1; unknown fields are ignored by
// decoders (design/design/agentd-stable-contract.design.md, rule 1).
const Version = 1

// Filename is the conventional basename for a staged per-run hook policy file.
const Filename = "hook-policy.json"

// File is the root JSON document staged per run for hosts advertising
// hostd.FeatureHookPolicy.
type File struct {
	Version int         `json:"version"`
	Tool    *ToolPolicy `json:"tool,omitempty"`
	Stop    *StopPolicy `json:"stop,omitempty"`
}

// HostToolGrant is one host-tool grant held by a run, resolved by the daemon
// before staging so the host binary needs no compiled-in grant registrations.
type HostToolGrant struct {
	Capability string   `json:"capability"`
	Tools      []string `json:"tools"`
}

// DenyPattern pairs a wildcard tool-name pattern ('*' matches any sequence of
// characters) with a denial reason.
type DenyPattern struct {
	Pattern string `json:"pattern"`
	Reason  string `json:"reason"`
}

// ToolPolicy is the PreToolUse policy agy-tool-hook evaluates for one run.
// When present on a File, it replaces every compiled-in tool roster and
// host-tool grant registration: only tools matched here (or by
// -capability-tool arguments qualified with CapabilityToolPrefix) are
// admitted.
type ToolPolicy struct {
	// AllowedTools are exact tool names admitted with no opinion (for
	// example grain's qualified MCP tools and "exit_plan_mode").
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// AllowedPatterns are wildcard patterns ('*' matches zero or more
	// characters) admitted with no opinion.
	AllowedPatterns []string `json:"allowed_patterns,omitempty"`
	// CapabilityToolPrefix is prepended before "<server>_<tool>" for any
	// "<server>/<tool>" passed via -capability-tool on the hook's command
	// line ("mcp_" when empty).
	CapabilityToolPrefix string `json:"capability_tool_prefix,omitempty"`
	// CapabilityServerPrefixes are tool-name prefixes (such as "mcp_acme_")
	// admitted with no opinion for capability MCP servers.
	CapabilityServerPrefixes []string `json:"capability_server_prefixes,omitempty"`
	// HostToolGrants are the host-tool grants this run holds. Each admitted
	// call must be recorded to RecordPath before proceeding; failure to
	// record denies the call.
	HostToolGrants []HostToolGrant `json:"host_tool_grants,omitempty"`
	// RecordPath, TaskID and RunID configure host-tool call recording when
	// not overridden by -record, -task or -run on the hook's command line.
	RecordPath string `json:"record_path,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	// DenyReasons maps exact denied tool names (such as "enter_plan_mode" or
	// "schedule") to custom denial messages.
	DenyReasons map[string]string `json:"deny_reasons,omitempty"`
	// DenyPatterns maps wildcard patterns to custom denial messages,
	// checked in order after DenyReasons.
	DenyPatterns []DenyPattern `json:"deny_patterns,omitempty"`
	// DefaultDenySuffix is appended after "<tool> " for any other denied
	// call. When empty, a generic sandbox-bound denial message is used.
	DefaultDenySuffix string `json:"default_deny_suffix,omitempty"`
}

// ClosingCallRule matches a tool call that counts as a closing move on a
// background run. Every non-zero condition on the rule must hold.
type ClosingCallRule struct {
	// Tool is the bare tool name, or a '*' wildcard pattern.
	Tool string `json:"tool"`
	// RequireSuccess requires !call.IsError (for example resume_later or git push).
	RequireSuccess bool `json:"require_success,omitempty"`
	// ArgBoolTrue requires call.Arguments[ArgBoolTrue] == true (for example "park").
	ArgBoolTrue string `json:"arg_bool_true,omitempty"`
	// ArgNonEmpty requires strings.TrimSpace(call.Arguments[ArgNonEmpty]) != ""
	// (for example "resume_at").
	ArgNonEmpty string `json:"arg_non_empty,omitempty"`
	// Arg and Substring require strings.Contains(call.Arguments[Arg], Substring)
	// (for example Arg="command", Substring="git push").
	Arg       string `json:"arg,omitempty"`
	Substring string `json:"substring,omitempty"`
}

// StopPolicy is the Stop-hook policy claude-stop-hook evaluates for one run.
// When present on a File, it replaces every compiled-in closing-tool list,
// promise phrase list, regular expression and nudge string.
type StopPolicy struct {
	// Interactive, when non-nil, sets whether the run is an interactive or
	// console session; otherwise the hook's -interactive flag decides.
	Interactive *bool `json:"interactive,omitempty"`
	// ToolPrefixes are stripped in order from tool_use names in a session
	// transcript to recover bare tool names (for example
	// "mcp__grain-sandbox__" and "mcp_grain-sandbox_").
	ToolPrefixes []string `json:"tool_prefixes,omitempty"`
	// SessionContinueReason is the nudge returned when an interactive
	// session's final message promises more work.
	SessionContinueReason string `json:"session_continue_reason,omitempty"`
	// BackgroundContinueReason is the nudge returned when a background run
	// ends without a closing move.
	BackgroundContinueReason string `json:"background_continue_reason,omitempty"`
	// ClosingTools are bare tool names (or '*' wildcard patterns) whose call
	// closes a background run regardless of IsError.
	ClosingTools []string `json:"closing_tools,omitempty"`
	// ClosingCalls are conditional tool-call rules that close a background
	// run when all of their conditions hold.
	ClosingCalls []ClosingCallRule `json:"closing_calls,omitempty"`
	// TrailingColon, when true, treats a final message ending on ':' (after
	// trimming whitespace and markdown emphasis) as promising more work.
	TrailingColon bool `json:"trailing_colon,omitempty"`
	// LastSentences is how many final sentences of the message are checked
	// for promises (default 2 when <= 0).
	LastSentences int `json:"last_sentences,omitempty"`
	// PromisePhrases are lower-case substrings that mark a final sentence as
	// promising more work.
	PromisePhrases []string `json:"promise_phrases,omitempty"`
	// PromisePrefixes are lower-case sentence prefixes (such as "let me ")
	// that mark a final sentence as promising more work.
	PromisePrefixes []string `json:"promise_prefixes,omitempty"`
	// ProgressivePattern is an optional regular expression matched against a
	// lower-cased final sentence for progressive narration.
	ProgressivePattern string `json:"progressive_pattern,omitempty"`
	// ConditionalPrefixes and ConditionalPhrases exempt offer sentences from
	// matching PromisePhrases, PromisePrefixes and ProgressivePattern.
	ConditionalPrefixes []string `json:"conditional_prefixes,omitempty"`
	ConditionalPhrases  []string `json:"conditional_phrases,omitempty"`
}

// Marshal serialises f as JSON with Version defaulted to Version when zero.
func Marshal(f File) ([]byte, error) {
	if f.Version == 0 {
		f.Version = Version
	}
	return json.Marshal(f)
}

// Parse decodes a JSON policy document. Unknown fields are ignored so a host
// binary can read additive fields written by a newer daemon. Versions outside
// 0..Version return an error.
func Parse(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing hook policy: %w", err)
	}
	if f.Version < 0 || f.Version > Version {
		return nil, fmt.Errorf("unsupported hook policy version %d (supported up to %d)", f.Version, Version)
	}
	if f.Version == 0 {
		f.Version = Version
	}
	return &f, nil
}

// Load reads and parses the policy file at path. When path is empty or does
// not exist on disk, Load returns (nil, nil) so the caller falls back to its
// compiled-in behaviour. Any other read error, malformed JSON, or unsupported
// version returns a non-nil error.
func Load(path string) (*File, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return Parse(data)
}

// MatchWildcard reports whether s matches pattern, where '*' matches any
// sequence of zero or more characters and every other byte matches literally.
func MatchWildcard(pattern, s string) bool {
	p, si := 0, 0
	starP, matchS := -1, 0
	for si < len(s) {
		if p < len(pattern) && pattern[p] == s[si] {
			p++
			si++
		} else if p < len(pattern) && pattern[p] == '*' {
			starP = p
			matchS = si
			p++
		} else if starP != -1 {
			p = starP + 1
			matchS++
			si = matchS
		} else {
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
