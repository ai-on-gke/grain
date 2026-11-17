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
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/claude/stophook"
	"github.com/ai-on-gke/grain/pkg/agent/hookpolicy"
	"github.com/ai-on-gke/grain/pkg/agent/stoprule"
)

// StopHookSubcommand is the argv[1] cmd/grain matches to run ServeStopHook:
// Claude Code's own Stop hook (stophook.Subcommand), which grain installs
// in every run's settings (stopHookSettings).
const StopHookSubcommand = stophook.Subcommand

// ServeStopHook answers one Claude Code Stop hook (stophook.Run). The
// hook itself lives in the leaf package pkg/agent/claude/stophook so that a
// binary as small as grain-agentd can answer it without linking this one.
func ServeStopHook(args []string, stdin io.Reader, stdout, stderr io.Writer) {
	stophook.Run(args, stdin, stdout, stderr)
}

// stopHookSettings is the content of the --settings file that installs
// the hook for one run on a host without hook_policy.
func stopHookSettings(grainBinaryPath string, interactive bool, recordPath string) (string, error) {
	return stopHookSettingsWithPolicy(grainBinaryPath, interactive, recordPath, "")
}

// stopHookSettingsWithPolicy is stopHookSettings with -policy policyPath
// appended when policyPath is non-empty (hosts with FeatureHookPolicy). A
// file rather than the inline JSON --settings also takes, because hostd
// admits a path-like argument only as a clean absolute path that exists
// under its roots (hostd.checkArg), and a JSON command line is neither.
func stopHookSettingsWithPolicy(grainBinaryPath string, interactive bool, recordPath, policyPath string) (string, error) {
	command := hookQuote(grainBinaryPath) + " " + StopHookSubcommand
	if interactive {
		command += " -interactive"
	}
	if recordPath != "" {
		command += " -record " + hookQuote(recordPath)
	}
	if policyPath != "" {
		command += " " + stophook.FlagPolicy + " " + hookQuote(policyPath)
	}
	data, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"Stop": []any{map[string]any{
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": command,
					"timeout": 30,
				}},
			}},
		},
	})
	return string(data), err
}

// hookQuote quotes s for the shell Claude Code runs a hook command with.
func hookQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeStopSettings stages this run's --settings file on host, 0600, the
// way writeMCPConfigJSON stages its --mcp-config.
func writeStopSettings(ctx context.Context, host agent.Host, grainBinaryPath string, interactive bool, recordPath string) (string, error) {
	return writeStopSettingsWithPolicy(ctx, host, grainBinaryPath, interactive, recordPath, "")
}

func writeStopSettingsWithPolicy(ctx context.Context, host agent.Host, grainBinaryPath string, interactive bool, recordPath, policyPath string) (string, error) {
	settings, err := stopHookSettingsWithPolicy(grainBinaryPath, interactive, recordPath, policyPath)
	if err != nil {
		return "", err
	}
	path, err := host.CreateTemp(ctx, "", "grain-claude-settings-*.json")
	if err != nil {
		return "", err
	}
	if err := host.WriteFile(ctx, path, []byte(settings), 0o600); err != nil {
		host.RemoveAll(ctx, path)
		return "", err
	}
	return path, nil
}

// stageStopPolicy stages this run's stop-rule policy file on host, 0600,
// when host advertises FeatureHookPolicy, and returns "" without staging
// anything for older hosts.
func stageStopPolicy(ctx context.Context, host agent.Host, interactive bool) (string, error) {
	return stageStopPolicyForRun(ctx, host, interactive, false)
}

func stageStopPolicyForRun(ctx context.Context, host agent.Host, interactive, requireOpenPR bool) (string, error) {
	if !agent.SupportsHookPolicy(host) {
		return "", nil
	}
	sp := stoprule.DefaultPolicy()
	if requireOpenPR && !interactive {
		sp = stoprule.QATreatmentPolicy()
	}
	sp.Interactive = &interactive
	data, err := hookpolicy.Marshal(hookpolicy.File{Version: hookpolicy.Version, Stop: &sp})
	if err != nil {
		return "", err
	}
	path, err := host.CreateTemp(ctx, "", "grain-claude-hook-policy-*.json")
	if err != nil {
		return "", err
	}
	if err := host.WriteFile(ctx, path, data, 0o600); err != nil {
		host.RemoveAll(ctx, path)
		return "", err
	}
	return path, nil
}

// stageStopRecord creates the empty file this run's hook appends its
// blocks to, on the host the CLI runs on.
func stageStopRecord(ctx context.Context, host agent.Host) (string, error) {
	path, err := host.CreateTemp(ctx, "", "grain-claude-stop-*.jsonl")
	if err != nil {
		return "", err
	}
	if err := host.WriteFile(ctx, path, nil, 0o600); err != nil {
		host.RemoveAll(ctx, path)
		return "", err
	}
	return path, nil
}

// readStopBlocks reads back what the hook recorded for this run.
func readStopBlocks(ctx context.Context, host agent.Host, path string) []agent.StopBlock {
	if path == "" {
		return nil
	}
	data, _, err := host.ReadTail(ctx, path, 1<<20)
	if err != nil {
		return nil
	}
	var blocks []agent.StopBlock
	for _, line := range strings.Split(string(data), "\n") {
		var b agent.StopBlock
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &b) == nil && b.Reason != "" {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

// withStopBlocks puts what the hook recorded on result: the blocks
// themselves, for the census, and a line in the transcript for each.
func withStopBlocks(result *agent.Result, blocks []agent.StopBlock) {
	if result == nil || len(blocks) == 0 {
		return
	}
	result.StopBlocks = append(result.StopBlocks, blocks...)
	for _, b := range blocks {
		result.Transcript = appendNote(result.Transcript, agent.StopNote(b))
	}
}

// EnforcesStopRule implements agent.StopRuleFramework: every run this
// Framework drives carries the Stop hook, so the orchestrator's fallback
// must not apply the rule again.
func (f *Framework) EnforcesStopRule() bool { return true }
