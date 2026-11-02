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

package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// propose_config_change (design/design/config-repo-settings.design.md, section
// 4.3): a session holding grain/config in its writes edits its checkout
// of it, grain-state, and calls this. The tool commits and pushes the
// edits to one of the session's own proposal branches
// (model.ConfigProposalBranchPrefix) through run_command, the way the
// session itself would, and then asks the daemon to diff and validate the
// branch and record it on the task (ConfigChangeProposer). When the turn
// ends, the run's finish parks the task on it and the person approves or
// rejects it inline in the chat.

// ConfigChangeProposal is one propose_config_change call, pushed.
type ConfigChangeProposal struct {
	Summary     string
	Description string
	Branch      string
	HeadSHA     string
}

// ConfigChangeProposer records a pushed proposal for this run's task and
// is what validation said about it. pkg/daemon's implementation asks the
// daemon (ui's POST /api/tasks/{id}/config-change).
type ConfigChangeProposer interface {
	ProposeConfigChange(ctx context.Context, proposal ConfigChangeProposal) (*model.PendingConfigChange, error)
}

// ConfigStateDir is the session's checkout of grain/config, relative to
// where run_command starts (orchestrator.DirectorStateDir).
const ConfigStateDir = "grain-state"

// NewProposeConfigChangeTools is propose_config_change for task taskID,
// pushing through tools' run_command. A nil proposer, or a roster with no
// run_command, refuses every call.
func NewProposeConfigChangeTools(proposer ConfigChangeProposer, taskID string, tools []Tool) []Tool {
	var run *Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			run = &tools[i]
		}
	}
	return []Tool{{
		Name: "propose_config_change",
		Description: "Commit and push your changes in " + ConfigStateDir + " (this deployment's " +
			"grain/config checkout) to a branch of grain/config, run config validation, and show " +
			"the person in this session's chat an inline diff with Approve and Reject buttons. " +
			"Nothing takes effect until they approve it. This ends your turn: when they decide, " +
			"you are woken with their decision -- approved and applied, or rejected with their " +
			"reason, in which case revise your edits and call this again. After calling this, " +
			"do not take any further actions.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"summary": questionSummarySchema("what this configuration change does and why"),
				"description": map[string]any{
					"type":        "string",
					"description": "Optional markdown explanation of the change and any trade-offs.",
				},
				"branch": map[string]any{
					"type": "string",
					"description": "Optional branch suffix (defaults to a slug of the summary). Reusing " +
						"one replaces the proposal pushed under it.",
				},
			},
			"required": []string{"summary"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			summary, err := questionSummaryArg(args)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if summary == "" {
				return Result{Text: "summary is required", IsError: true}
			}
			if proposer == nil || run == nil || taskID == "" {
				return Result{Text: "propose_config_change is not available in this run: it needs grain's " +
					"daemon and a sandbox to push from", IsError: true}
			}
			description, _ := argString(args, "description")
			suffix, _ := argString(args, "branch")
			slug := configBranchSlug(suffix)
			if slug == "" {
				slug = configBranchSlug(summary)
			}
			if slug == "" {
				slug = "change"
			}
			branch := model.ConfigProposalBranchPrefix(taskID) + slug
			res := run.Handler(ctx, map[string]any{
				"command":     proposeConfigChangeScript(branch, summary, strings.TrimSpace(description)),
				"description": "committing and pushing the proposed config change",
			})
			head, ok := proposedHead(res.Text)
			if res.IsError || !ok {
				return Result{Text: "Could not commit and push the change in " + ConfigStateDir + ": " +
					strings.TrimSpace(res.Text), IsError: true}
			}
			pending, err := proposer.ProposeConfigChange(ctx, ConfigChangeProposal{
				Summary: summary, Description: strings.TrimSpace(description), Branch: branch, HeadSHA: head,
			})
			if err != nil {
				return Result{Text: fmt.Sprintf("Pushed %s, but grain could not record it for approval: %v", branch, err), IsError: true}
			}
			verdict := "It passed config validation."
			if !pending.Validation.OK {
				verdict = "It FAILED config validation, so the person cannot approve it as it stands: " +
					pending.Validation.Error + ". You may fix it and call propose_config_change again now, " +
					"or end your turn and let the person read it."
			}
			return Result{Text: fmt.Sprintf("Proposed %s (%s, %d file(s)). %s The person sees the diff with "+
				"Approve and Reject in this chat. This ends your turn: stop here and take no further "+
				"actions.", branch, shortSHA(head), len(pending.Files), verdict)}
		},
	}}
}

var configSlugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// configBranchSlug is s as a branch name component: lower case, runs of
// anything else one hyphen, at most 48 characters.
func configBranchSlug(s string) string {
	slug := strings.Trim(configSlugUnsafe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	return slug
}

const proposedHeadMarker = "GRAIN-CONFIG-HEAD:"

// proposeConfigChangeScript commits whatever the checkout holds onto
// branch -- working-tree edits, and any commits the session already made
// -- and force-pushes it there, the one ref of grain/config this session
// may move (the git proxy refuses main). It fails on a checkout with
// nothing to propose.
func proposeConfigChangeScript(branch, summary, description string) string {
	message := summary
	if description != "" {
		message += "\n\n" + description
	}
	return fmt.Sprintf(`set -e
cd %[1]s
git fetch --quiet origin
base="$(git symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null || echo origin/main)"
git checkout --quiet -B %[2]s
git add -A
if ! git diff --cached --quiet; then
  git commit --quiet -m %[3]s
fi
if [ -z "$(git rev-list "$base..HEAD")" ]; then
  echo "nothing to propose: %[1]s has no changes against $base" >&2
  exit 1
fi
git push --quiet --force origin HEAD:refs/heads/%[2]s
echo "%[4]s$(git rev-parse HEAD)"`, shellQuote(ConfigStateDir), shellQuote(branch), shellQuote(message), proposedHeadMarker)
}

var commitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// proposedHead is the commit proposeConfigChangeScript pushed.
func proposedHead(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		if sha, ok := strings.CutPrefix(strings.TrimSpace(line), proposedHeadMarker); ok && commitID.MatchString(sha) {
			return sha, true
		}
	}
	return "", false
}
