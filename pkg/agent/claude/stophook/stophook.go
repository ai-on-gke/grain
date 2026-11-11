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

// Package stophook is Claude Code's Stop hook as grain answers it: the
// payload on stdin, the stop rule (pkg/agent/stoprule) applied to the
// session it names, and the decision on stdout.
//
// It is a leaf -- it imports only stoprule and hookpolicy -- so that every
// binary a run may name as its hook can carry it cheaply: cmd/grain, and
// grain-agentd, which runs the CLIs on a machine that is not the daemon's
// and must stay small. When -policy names a staged per-run policy file, the
// hook evaluates that data directly rather than its compiled-in default rule;
// when the file is absent, it falls back to stoprule.DefaultPolicy.
package stophook

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent/hookpolicy"
	"github.com/ai-on-gke/grain/pkg/agent/stoprule"
)

// Subcommand is the argv[1] a grain binary matches to answer with Run: Claude
// Code's own Stop hook, which grain installs in every run's settings so
// that a run which ends its turn while promising more work -- or, on a
// background task, before it has made any closing move -- is sent back
// once to finish (stoprule.Decide).
//
// It is grain forking itself, for the reason the agy hook and mcpserver
// are: the hook must be an executable named in a config written per run,
// and the one a deployment is guaranteed to have is this binary. It runs
// beside the CLI, on the same host, and reads only what Claude Code hands
// it on stdin, the session transcript that names, and the staged per-run
// policy file (-policy); it asks the daemon nothing, holds no credential,
// and can change nothing but whether this one turn ends.
const Subcommand = "claude-stop-hook"

// FlagPolicy is the flag pointing claude-stop-hook at a staged per-run
// hookpolicy.File.
const FlagPolicy = "-policy"

// input is the part of Claude Code's Stop hook payload this hook reads.
// last_assistant_message is only sent by newer builds; an older one is
// read off transcript_path instead.
type input struct {
	HookEventName        string `json:"hook_event_name"`
	StopHookActive       bool   `json:"stop_hook_active"`
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// output is the reply that keeps Claude Code going: "block" with a
// reason, which the CLI hands the model as the reason it may not stop.
type output struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// maxTranscript bounds how much of a session transcript the hook reads:
// the decision needs the run's tool calls and its last message, and a
// pathological transcript is not worth holding a turn up for.
const maxTranscript = 64 << 20

// Run answers one Claude Code Stop hook: the payload on stdin, the
// decision on stdout. Its arguments are what the run's settings wrote --
// -interactive for a task somebody is sitting with, -record naming the
// file each block is appended to for the run's transcript and census, and
// -policy naming the staged per-run hookpolicy.File (when the host supports
// hook_policy).
//
// It never exits non-zero and writes nothing at all for a stop it allows:
// a hook that fails must cost a run nothing, and an empty stdout with exit
// status 0 is Claude Code's "carry on as you would have".
//
// stop_hook_active is what keeps it to one block per turn: Claude Code
// sets it when the stop it is asking about ends a continuation a Stop
// hook already asked for, and that stop is always allowed, so the rule
// can never loop.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) {
	fs := flag.NewFlagSet(Subcommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	interactive := fs.Bool("interactive", false, "the task is a session somebody is sitting with")
	record := fs.String("record", "", "file to append each block to")
	policyPath := fs.String("policy", "", "staged per-run hook policy file")
	if err := fs.Parse(args); err != nil {
		return
	}
	policy := stoprule.DefaultPolicy()
	if file, err := hookpolicy.Load(*policyPath); err != nil {
		return
	} else if file != nil && file.Stop != nil {
		policy = *file.Stop
	}
	var in input
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in); err != nil {
		return
	}
	d, block := decideWith(in, *interactive, policy, readTranscript)
	if !block {
		return
	}
	if *record != "" {
		recordBlock(*record, stoprule.Block{Via: stoprule.ViaHook, Reason: d.Reason})
	}
	json.NewEncoder(stdout).Encode(output{Decision: "block", Reason: d.Reason})
}

// decide is the hook's policy apart from its I/O under DefaultPolicy.
func decide(in input, interactive bool, transcript func(path string) string) (stoprule.Decision, bool) {
	return decideWith(in, interactive, stoprule.DefaultPolicy(), transcript)
}

func decideWith(in input, interactive bool, policy hookpolicy.StopPolicy, transcript func(path string) string) (stoprule.Decision, bool) {
	if in.StopHookActive {
		return stoprule.Decision{}, false
	}
	if in.HookEventName != "" && in.HookEventName != "Stop" {
		return stoprule.Decision{}, false
	}
	session := ""
	if in.TranscriptPath != "" {
		session = transcript(in.TranscriptPath)
	}
	prefixes := policy.ToolPrefixes
	if len(prefixes) == 0 {
		prefixes = stoprule.DefaultToolPrefixes
	}
	calls, last := ParseSessionWithPrefixes(session, prefixes)
	final := in.LastAssistantMessage
	if final == "" {
		final = last
	}
	d := stoprule.DecideWith(policy, interactive, final, calls)
	return d, d.Block
}

// ParseSession reads what the stop rule needs out of a Claude Code session
// transcript using DefaultToolPrefixes.
func ParseSession(session string) (calls []stoprule.Call, lastText string) {
	return ParseSessionWithPrefixes(session, stoprule.DefaultToolPrefixes)
}

// ParseSessionWithPrefixes reads every tool call in session, stripping the
// first matching prefix in prefixes to recover bare tool names, along with
// each call's arguments, error flag, and the session's last text block.
func ParseSessionWithPrefixes(session string, prefixes []string) (calls []stoprule.Call, lastText string) {
	pending := map[string]int{}
	for _, line := range strings.Split(session, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if (ev.Type != "assistant" && ev.Type != "user") || ev.Message == nil {
			continue
		}
		for _, b := range blocks(ev.Message.Content) {
			switch b.Type {
			case "text":
				if b.Text != "" {
					lastText = b.Text
				}
			case "tool_use":
				name := b.Name
				for _, pref := range prefixes {
					if after, ok := strings.CutPrefix(name, pref); ok {
						name = after
						break
					}
				}
				calls = append(calls, stoprule.Call{Name: name, Arguments: b.Input})
				pending[b.ID] = len(calls) - 1
			case "tool_result":
				if idx, ok := pending[b.ToolUseID]; ok {
					calls[idx].IsError = b.IsError
				}
			}
		}
	}
	return calls, lastText
}

type block struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ID        string         `json:"id"`
	ToolUseID string         `json:"tool_use_id"`
	IsError   bool           `json:"is_error"`
}

// blocks reads a message's content field, which is either a plain string
// or a list of typed blocks (text, tool_use, tool_result).
func blocks(raw json.RawMessage) []block {
	var bs []block
	if err := json.Unmarshal(raw, &bs); err == nil {
		return bs
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return []block{{Type: "text", Text: s}}
	}
	return nil
}

func readTranscript(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, maxTranscript))
	return string(data)
}

// recordBlock appends b to the run's record file as one JSON line.
// Best-effort: a block that cannot be recorded still blocks. O_APPEND
// without O_CREATE, so the hook only ever writes to the file grain staged.
func recordBlock(path string, b stoprule.Block) {
	line, err := json.Marshal(b)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}
