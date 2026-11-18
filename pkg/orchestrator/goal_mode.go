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

package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// AutonomousGoalInstruction is appended to BuildPrompt when a task has
// GoalMode.Enabled == true.
const AutonomousGoalInstruction = "\n\n## Autonomous Goal Posture\n" +
	"You are running in Autonomous Goal Mode. Work autonomously end-to-end without waiting on human input:\n" +
	"- Resolve ambiguities using repository idioms and pragmatic engineering judgment. Call `record_assumption` for non-trivial engineering or UX decisions so your choices and rationale are recorded.\n" +
	"- Build, test, commit, and push your changes cleanly to the task branch.\n" +
	"- Finish by calling `complete_goal(summary, deliverables)` where every deliverable cites concrete observed command or test output (`verified: true`), and only exit after calling `complete_goal` and opening the pull request."

const (
	goalScorecardStart = "<!-- grain:goal-scorecard:start -->"
	goalScorecardEnd   = "<!-- grain:goal-scorecard:end -->"
	goalHeadMarker     = "__GRAIN_GOAL_HEAD__"
	goalDirtyMarker    = "__GRAIN_GOAL_DIRTY__"
)

func autonomousGoalSection(gm model.GoalModeConfig) string {
	section := AutonomousGoalInstruction
	if gm.MockMissingSecrets {
		section += "\n- External credentials/secrets posture: `mockMissingSecrets` is enabled. If an external credential or third-party integration is absent, mock or stub it locally with deterministic tests rather than stopping or requesting secrets."
	}
	return section
}

// goalModeDispatch bounds cfg.MaxRunRuntime by gm.WallClockMinutes when GoalMode
// is enabled on task.
func goalModeDispatch(task model.Task, cfg Config) Config {
	gm, ok := model.GoalModeOf(task)
	if !ok || !gm.Enabled || gm.WallClockMinutes <= 0 {
		return cfg
	}
	wallClock := time.Duration(gm.WallClockMinutes) * time.Minute
	if maxRuntime := cfg.maxRunRuntime(); maxRuntime <= 0 || wallClock < maxRuntime {
		cfg.MaxRunRuntime = wallClock
	}
	return cfg
}

type goalVerification struct {
	verified     bool
	summary      string
	deliverables []model.GoalDeliverable
	missing      []string
	reason       string
}

// verifyGoalPass inspects the latest complete_goal tool call in passResult and
// verifies that summary is non-empty, deliverables is non-empty, every
// deliverable has non-empty Requirement and Evidence with Verified == true, and
// treeClean is true.
func verifyGoalPass(passResult *agent.Result, gitHeadChanged, treeClean bool) goalVerification {
	_ = gitHeadChanged
	var out goalVerification
	if passResult == nil {
		out.missing = append(out.missing, "no agent result produced")
		out.reason = "missing complete_goal"
		return out
	}

	var foundCall *agent.ToolCall
	for i := len(passResult.ToolCalls) - 1; i >= 0; i-- {
		tc := passResult.ToolCalls[i]
		if tc.IsError {
			continue
		}
		if bareGoalToolName(tc.Name) == "complete_goal" {
			foundCall = &passResult.ToolCalls[i]
			break
		}
	}

	if foundCall == nil {
		out.missing = append(out.missing, "complete_goal was not called")
	} else {
		summary, _ := foundCall.Arguments["summary"].(string)
		out.summary = strings.TrimSpace(summary)
		if out.summary == "" {
			out.missing = append(out.missing, "complete_goal summary is empty")
		}
		out.deliverables = extractGoalDeliverables(foundCall.Arguments["deliverables"])
		if len(out.deliverables) == 0 {
			out.missing = append(out.missing, "complete_goal deliverables list is empty")
		}
		for idx, d := range out.deliverables {
			req := strings.TrimSpace(d.Requirement)
			ev := strings.TrimSpace(d.Evidence)
			if req == "" {
				out.missing = append(out.missing, fmt.Sprintf("deliverable %d requirement is empty", idx+1))
			}
			if ev == "" {
				label := req
				if label == "" {
					label = fmt.Sprintf("#%d", idx+1)
				}
				out.missing = append(out.missing, fmt.Sprintf("deliverable %q is missing concrete verification evidence", label))
			}
			if !d.Verified {
				label := req
				if label == "" {
					label = fmt.Sprintf("#%d", idx+1)
				}
				out.missing = append(out.missing, fmt.Sprintf("deliverable %q has verified=false", label))
			}
		}
	}

	if !treeClean {
		out.missing = append(out.missing, "working tree has uncommitted changes (git status --porcelain is not clean)")
	}

	if len(out.missing) == 0 {
		out.verified = true
		return out
	}
	if foundCall == nil && treeClean {
		out.reason = "missing complete_goal"
	} else {
		out.reason = strings.Join(out.missing, "; ")
	}
	return out
}

func extractGoalDeliverables(raw any) []model.GoalDeliverable {
	switch v := raw.(type) {
	case []model.GoalDeliverable:
		out := make([]model.GoalDeliverable, len(v))
		copy(out, v)
		return out
	case []any:
		out := make([]model.GoalDeliverable, 0, len(v))
		for _, item := range v {
			switch elem := item.(type) {
			case model.GoalDeliverable:
				out = append(out, elem)
			case map[string]any:
				req, _ := elem["requirement"].(string)
				ev, _ := elem["evidence"].(string)
				ver, _ := elem["verified"].(bool)
				out = append(out, model.GoalDeliverable{
					Requirement: strings.TrimSpace(req),
					Evidence:    strings.TrimSpace(ev),
					Verified:    ver,
				})
			}
		}
		return out
	default:
		return nil
	}
}

func extractPassAssumptions(passResult *agent.Result, pass int, now time.Time) []model.GoalAssumption {
	if passResult == nil {
		return nil
	}
	var out []model.GoalAssumption
	for _, tc := range passResult.ToolCalls {
		if tc.IsError || bareGoalToolName(tc.Name) != "record_assumption" {
			continue
		}
		topic, _ := tc.Arguments["topic"].(string)
		decision, _ := tc.Arguments["decision"].(string)
		rationale, _ := tc.Arguments["rationale"].(string)
		alternatives, _ := tc.Arguments["alternatives"].(string)
		topic = strings.TrimSpace(topic)
		decision = strings.TrimSpace(decision)
		if topic == "" || decision == "" {
			continue
		}
		out = append(out, model.GoalAssumption{
			Topic:        topic,
			Decision:     decision,
			Rationale:    strings.TrimSpace(rationale),
			Alternatives: strings.TrimSpace(alternatives),
			Pass:         pass,
			CreatedAt:    now,
		})
	}
	return out
}

func bareGoalToolName(name string) string {
	for _, prefix := range []string{"mcp__grain-sandbox__", "mcp_grain-sandbox_"} {
		if stripped, ok := strings.CutPrefix(name, prefix); ok {
			return stripped
		}
	}
	return name
}

var readOnlyToolNames = map[string]bool{
	"read_file":                  true,
	"grain_read":                 true,
	"grain_ls":                   true,
	"grain_grep":                 true,
	"list_grain_tasks":           true,
	"read_grain_task":            true,
	"read_grain_task_prompt":     true,
	"read_grain_task_transcript": true,
	"read_grain_metrics":         true,
	"read_grain_log":             true,
	"read_deployment_health":     true,
	"read_deployment_state":      true,
	"list_issues":                true,
	"read_issue":                 true,
	"pull_request_status":        true,
	"base_branch_status":         true,
	"list_ci_runs":               true,
	"get_ci_log":                 true,
	"query_test_history":         true,
	"query_merge_queue":          true,
	"query_grain_db":             true,
	"record_assumption":          true,
	"complete_goal":              true,
}

func hasMutatingToolCalls(calls []agent.ToolCall) bool {
	for _, tc := range calls {
		if tc.IsError {
			continue
		}
		name := bareGoalToolName(tc.Name)
		if name != "" && !readOnlyToolNames[name] {
			return true
		}
	}
	return false
}

func inspectGoalCheckout(ctx context.Context, tools []mcp.Tool) (headSHA string, treeClean bool) {
	run, ok := runCommandTool(tools)
	if !ok {
		return "", true
	}
	script := fmt.Sprintf(`cd '%s' 2>/dev/null || exit 0
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 0
head_sha=$(git rev-parse --verify HEAD 2>/dev/null || true)
echo "%s${head_sha}"
if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  echo "%s1"
fi
`, CheckoutDir, goalHeadMarker, goalDirtyMarker)
	res := run(ctx, map[string]any{"command": script})
	if res.IsError {
		return "", true
	}
	treeClean = true
	for _, line := range strings.Split(res.Text, "\n") {
		trimmed := strings.TrimSpace(line)
		if sha, ok := strings.CutPrefix(trimmed, goalHeadMarker); ok {
			headSHA = strings.TrimSpace(sha)
		}
		if _, ok := strings.CutPrefix(trimmed, goalDirtyMarker); ok {
			treeClean = false
		}
	}
	return headSHA, treeClean
}

func buildGoalContinuePrompt(task model.Task, nextPass, maxPasses int, missing []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Autonomous Goal Verifier — Pass %d/%d for task %q.\n", nextPass, maxPasses, task.Title)
	b.WriteString("Verification of your previous pass is incomplete. Missing items:\n")
	for _, item := range missing {
		fmt.Fprintf(&b, "- %s\n", item)
	}
	b.WriteString("\nComplete all remaining work, ensure `git status --porcelain` is clean and your commits are pushed, and call `complete_goal(summary, deliverables)` with `verified: true` and concrete observed command/test evidence for every deliverable before stopping.")
	return b.String()
}

func persistGoalPassState(ctx context.Context, store *model.Store, task *model.Task, assumptions []model.GoalAssumption, sc model.GoalScorecard) {
	apply := func(t *model.Task) error {
		for _, a := range assumptions {
			if err := model.AppendGoalAssumption(t, a); err != nil {
				return err
			}
		}
		return model.SetGoalScorecard(t, sc)
	}
	if task != nil {
		_ = apply(task)
	}
	if store != nil && task != nil && task.ID != "" {
		_ = store.UpdateTask(context.WithoutCancel(ctx), task.ID, apply)
	}
}

func runGoalVerifierPasses(
	hostCtx context.Context,
	storeCtx context.Context,
	store *model.Store,
	cfg Config,
	framework agent.Framework,
	runCfg agent.RunConfig,
	tools []mcp.Tool,
	task *model.Task,
	runID string,
	initialGitHead string,
	pass1Span *GoalPerfSpan,
	result *agent.Result,
	runErr error,
) (*agent.Result, error) {
	if task == nil {
		return result, runErr
	}
	gm, ok := model.GoalModeOf(*task)
	if !ok || !gm.Enabled {
		return result, runErr
	}

	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = cfg.GrainStateDir
	}
	profiler := NewGoalPerfProfiler(dataDir)
	prevHead := initialGitHead
	passResult := result
	span := pass1Span
	if span == nil {
		span = profiler.Start(task.ID, runID, 1, "verifier-pass")
	}

	stalledPasses := 0
	for pass := 1; pass <= gm.MaxPasses; pass++ {
		currHead, treeClean := inspectGoalCheckout(hostCtx, tools)
		gitHeadChanged := currHead != "" && currHead != prevHead
		if currHead != "" {
			prevHead = currHead
		}

		ver := verifyGoalPass(passResult, gitHeadChanged, treeClean)
		span.End(ver.verified)

		now := cfg.now()
		assumptions := extractPassAssumptions(passResult, pass, now)

		// Check if GoalMode was disabled live in the store mid-run.
		if store != nil && task.ID != "" {
			if fresh, err := store.GetTask(context.WithoutCancel(storeCtx), task.ID); err == nil && fresh != nil {
				freshGM, stillEnabled := model.GoalModeOf(*fresh)
				if !stillEnabled {
					sc := model.GoalScorecard{
						PassesUsed:   pass,
						MaxPasses:    gm.MaxPasses,
						Verified:     ver.verified,
						StoppedAtCap: false,
						StopReason:   "switch to supervised",
						Summary:      ver.summary,
						Deliverables: ver.deliverables,
						UpdatedAt:    now,
					}
					persistGoalPassState(storeCtx, store, task, assumptions, sc)
					// Mirror the disabled GoalMode onto task as well.
					_ = model.SetGoalMode(task, &model.GoalModeConfig{Enabled: false})
					return result, runErr
				}
				gm = freshGM
			}
		}

		if ver.verified {
			sc := model.GoalScorecard{
				PassesUsed:   pass,
				MaxPasses:    gm.MaxPasses,
				Verified:     true,
				StoppedAtCap: false,
				StopReason:   "",
				Summary:      ver.summary,
				Deliverables: ver.deliverables,
				UpdatedAt:    now,
			}
			persistGoalPassState(storeCtx, store, task, assumptions, sc)
			return result, runErr
		}

		// Stop immediately if context cancelled, run errored, or session is not resumable.
		if hostCtx.Err() != nil || storeCtx.Err() != nil || runErr != nil || result == nil || result.ResumableSession == "" {
			reason := ver.reason
			if runErr != nil {
				reason = runErr.Error()
			} else if hostCtx.Err() != nil || storeCtx.Err() != nil {
				reason = "context cancelled"
			}
			sc := model.GoalScorecard{
				PassesUsed:   pass,
				MaxPasses:    gm.MaxPasses,
				Verified:     false,
				StoppedAtCap: pass >= gm.MaxPasses,
				StopReason:   reason,
				Summary:      ver.summary,
				Deliverables: ver.deliverables,
				UpdatedAt:    now,
			}
			persistGoalPassState(storeCtx, store, task, assumptions, sc)
			return result, runErr
		}

		if pass >= gm.MaxPasses {
			sc := model.GoalScorecard{
				PassesUsed:   pass,
				MaxPasses:    gm.MaxPasses,
				Verified:     false,
				StoppedAtCap: true,
				StopReason:   ver.reason,
				Summary:      ver.summary,
				Deliverables: ver.deliverables,
				UpdatedAt:    now,
			}
			persistGoalPassState(storeCtx, store, task, assumptions, sc)
			return result, runErr
		}

		if (passResult == nil || !hasMutatingToolCalls(passResult.ToolCalls)) && !gitHeadChanged {
			stalledPasses++
		} else {
			stalledPasses = 0
		}
		if stalledPasses >= 2 {
			sc := model.GoalScorecard{
				PassesUsed:   pass,
				MaxPasses:    gm.MaxPasses,
				Verified:     false,
				StoppedAtCap: true,
				StopReason:   "stalled without progress",
				Summary:      ver.summary,
				Deliverables: ver.deliverables,
				UpdatedAt:    now,
			}
			persistGoalPassState(storeCtx, store, task, assumptions, sc)
			return result, runErr
		}

		// Persist intermediate pass state before continuing.
		persistGoalPassState(storeCtx, store, task, assumptions, model.GoalScorecard{
			PassesUsed:   pass,
			MaxPasses:    gm.MaxPasses,
			Verified:     false,
			StoppedAtCap: false,
			StopReason:   ver.reason,
			Summary:      ver.summary,
			Deliverables: ver.deliverables,
			UpdatedAt:    now,
		})

		continuePrompt := buildGoalContinuePrompt(*task, pass+1, gm.MaxPasses, ver.missing)
		runCfg.ResumeSession = result.ResumableSession
		runCfg.ContinuePrompt = continuePrompt
		span = profiler.Start(task.ID, runID, pass+1, "verifier-pass")

		next, nextErr := framework.Run(hostCtx, runCfg)
		block := agent.StopBlock{Via: agent.StopViaResume, Reason: continuePrompt}
		result = agent.ContinueResult(result, next, block)
		passResult = next
		runErr = nextErr
	}
	return result, runErr
}

// FormatGoalScorecardMarkdown renders the Markdown "Autonomous Goal Scorecard &
// Assumptions" section when task (or its fresh store copy) carries a
// GoalScorecard or GoalAssumptions. Returns "" if neither is present.
func FormatGoalScorecardMarkdown(task model.Task) string {
	sc, hasSC := model.GoalScorecardOf(task)
	assumptions := model.GoalAssumptionsOf(task)
	if !hasSC && len(assumptions) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(goalScorecardStart + "\n")
	b.WriteString("## Autonomous Goal Scorecard & Assumptions\n\n")
	if hasSC && sc != nil {
		status := "Incomplete"
		if sc.Verified {
			status = "Verified"
		} else if sc.StoppedAtCap {
			status = "Stopped at cap"
		}
		fmt.Fprintf(&b, "- **Status**: %s (%d/%d passes)\n", status, sc.PassesUsed, sc.MaxPasses)
		if sc.StopReason != "" {
			fmt.Fprintf(&b, "- **Stop reason**: %s\n", sc.StopReason)
		}
		if sc.Summary != "" {
			fmt.Fprintf(&b, "- **Summary**: %s\n", sc.Summary)
		}
		if len(sc.Deliverables) > 0 {
			b.WriteString("\n### Deliverables\n\n")
			for _, d := range sc.Deliverables {
				mark := "[ ]"
				if d.Verified {
					mark = "[x]"
				}
				fmt.Fprintf(&b, "- %s **%s** — %s\n", mark, d.Requirement, d.Evidence)
			}
		}
	}
	if len(assumptions) > 0 {
		b.WriteString("\n### Recorded Assumptions\n\n")
		for _, a := range assumptions {
			line := fmt.Sprintf("- **%s**: %s", a.Topic, a.Decision)
			if a.Rationale != "" {
				line += fmt.Sprintf(" _(Rationale: %s)_", a.Rationale)
			}
			if a.Alternatives != "" {
				line += fmt.Sprintf(" [Alternatives: %s]", a.Alternatives)
			}
			b.WriteString(line + "\n")
		}
	}
	b.WriteString(goalScorecardEnd)
	return b.String()
}

func goalMarkdownSection(ctx context.Context, store *model.Store, task model.Task) string {
	if store != nil && task.ID != "" {
		if fresh, err := store.GetTask(ctx, task.ID); err == nil && fresh != nil {
			if s := FormatGoalScorecardMarkdown(*fresh); s != "" {
				return s
			}
		}
	}
	return FormatGoalScorecardMarkdown(task)
}

func withGoalSummary(body, section string) string {
	if section == "" {
		return body
	}
	if start := strings.Index(body, goalScorecardStart); start >= 0 {
		if end := strings.Index(body[start:], goalScorecardEnd); end >= 0 {
			return body[:start] + section + body[start+end+len(goalScorecardEnd):]
		}
	}
	if i := strings.LastIndex(body, "\n---\n"); i >= 0 {
		return body[:i] + "\n\n" + section + "\n" + body[i:]
	}
	return body + "\n\n" + section
}

func appendGoalMarkdownToComment(comment, section string) string {
	if section == "" || strings.Contains(comment, "Autonomous Goal Scorecard & Assumptions") {
		return comment
	}
	trimmed := strings.TrimSpace(comment)
	if trimmed == "" {
		return section
	}
	return trimmed + "\n\n" + section
}
