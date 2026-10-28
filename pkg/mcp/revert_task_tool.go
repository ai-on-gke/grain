package mcp

import (
	"context"
	"fmt"
	"strings"
)

// TaskReverter is what revert_task and revert_merge call to create a clean git revert commit.
type TaskReverter interface {
	RevertTask(ctx context.Context, req RevertTaskRequest) (RevertTaskResult, error)
}

// RevertTaskRequest specifies the task ID or merge commit SHA to revert and the reason.
type RevertTaskRequest struct {
	TaskID    string
	CommitSHA string
	Reason    string
}

// RevertTaskResult holds details of the created revert commit.
type RevertTaskResult struct {
	NewCommitSHA string
	TaskID       string
	CommitSHA    string
	Reason       string
	Summary      string
}

const noTaskReverter = "This run cannot revert tasks or commits: it was dispatched with no git reverter " +
	"available for the sandbox workspace. Say so if it matters to what you were asked to do; " +
	"nothing you can do from here will change it."

var revertInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []any{"reason"},
	"properties": map[string]any{
		"task_id": map[string]any{
			"type":        "string",
			"description": "Optional ID of the task to revert (e.g. \"123\" or \"grain/task-123\").",
		},
		"commit_sha": map[string]any{
			"type":        "string",
			"description": "Optional merge commit or commit SHA to revert (4 to 64 hex digits).",
			"pattern":     "^[0-9a-fA-F]{4,64}$",
		},
		"reason": map[string]any{
			"type":        "string",
			"description": "Required explanation of why this task/commit is being reverted.",
		},
	},
}

// NewRevertTools returns the revert_task and revert_merge tools.
func NewRevertTools(reverter TaskReverter) []Tool {
	return []Tool{
		revertTaskTool(reverter),
		revertMergeTool(reverter),
	}
}

func revertTaskTool(reverter TaskReverter) Tool {
	return Tool{
		Name:        "revert_task",
		Description: "Creates a clean git revert commit on the current branch for a specified task ID or commit SHA, attributing the revert to the origin task.",
		InputSchema: revertInputSchema,
		Handler:     makeRevertHandler(reverter, "revert_task"),
	}
}

func revertMergeTool(reverter TaskReverter) Tool {
	return Tool{
		Name:        "revert_merge",
		Description: "Creates a clean git revert commit on the current branch for a specified task ID or merge commit SHA, attributing the revert to the origin task.",
		InputSchema: revertInputSchema,
		Handler:     makeRevertHandler(reverter, "revert_merge"),
	}
}

func makeRevertHandler(reverter TaskReverter, toolName string) func(ctx context.Context, args map[string]any) Result {
	return func(ctx context.Context, args map[string]any) Result {
		if reverter == nil {
			return Result{Text: noTaskReverter, IsError: true}
		}
		taskID, _ := args["task_id"].(string)
		commitSHA, _ := args["commit_sha"].(string)
		reason, _ := args["reason"].(string)

		taskID = strings.TrimSpace(taskID)
		commitSHA = strings.TrimSpace(commitSHA)
		reason = strings.TrimSpace(reason)

		if reason == "" {
			return Result{Text: fmt.Sprintf("%s failed: reason is required and cannot be empty", toolName), IsError: true}
		}
		if taskID == "" && commitSHA == "" {
			return Result{Text: fmt.Sprintf("%s failed: either task_id or commit_sha must be provided", toolName), IsError: true}
		}
		if err := validateRevertTarget(taskID, commitSHA); err != nil {
			return Result{Text: fmt.Sprintf("%s failed: %v", toolName, err), IsError: true}
		}

		res, err := reverter.RevertTask(ctx, RevertTaskRequest{
			TaskID:    taskID,
			CommitSHA: commitSHA,
			Reason:    reason,
		})
		if err != nil {
			return Result{Text: fmt.Sprintf("%s failed: %v", toolName, err), IsError: true}
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("Successfully created revert commit `%s`.\n", res.NewCommitSHA))
		if res.TaskID != "" {
			b.WriteString(fmt.Sprintf("- Origin Task: %s\n", res.TaskID))
		}
		if res.CommitSHA != "" {
			b.WriteString(fmt.Sprintf("- Target Commit: `%s`\n", res.CommitSHA))
		}
		b.WriteString(fmt.Sprintf("- Reason: %s\n", res.Reason))
		if res.Summary != "" {
			b.WriteString(fmt.Sprintf("- Reverted Change: %s\n", res.Summary))
		}
		return Result{Text: b.String()}
	}
}
