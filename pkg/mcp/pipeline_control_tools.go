package mcp

import (
	"context"
	"fmt"
	"strings"
)

// PipelineController is what pause_pipeline and resume_pipeline call to
// pause or resume dispatch.
//
// Each reports what it reached: the team whose own dispatch it paused or
// resumed, for a run whose pipeline-control grant is team-scoped
// (grain/task-2555), or "" for one whose grant reaches the whole
// deployment. The controller decides, not the agent -- the tools take no
// team argument, so a team-scoped run cannot name another team's queue.
type PipelineController interface {
	PausePipeline(ctx context.Context, repo, reason string) (team string, err error)
	ResumePipeline(ctx context.Context, repo, reason string) (team string, err error)
}

const noPipelineController = "This run cannot control the pipeline: it holds the pipeline-control grant " +
	"but was dispatched with no route back to the grain daemon that controls the pipeline. Say so if it " +
	"matters to what you were asked to do; nothing you can do from here will change it."

var pausePipelineInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []any{"repo", "reason"},
	"properties": map[string]any{
		"repo": map[string]any{
			"type":        "string",
			"description": "Repository (owner/name) to pause automated merges and pipeline dispatch on.",
		},
		"reason": map[string]any{
			"type":        "string",
			"description": "Reason for pausing the pipeline.",
		},
	},
}

var resumePipelineInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []any{"repo", "reason"},
	"properties": map[string]any{
		"repo": map[string]any{
			"type":        "string",
			"description": "Repository (owner/name) to resume automated merges and pipeline dispatch on.",
		},
		"reason": map[string]any{
			"type":        "string",
			"description": "Reason for resuming the pipeline.",
		},
	},
}

// NewPipelineControlTools returns the pause_pipeline and resume_pipeline tools.
func NewPipelineControlTools(controller PipelineController) []Tool {
	return []Tool{
		pausePipelineTool(controller),
		resumePipelineTool(controller),
	}
}

func pausePipelineTool(controller PipelineController) Tool {
	return Tool{
		Name: "pause_pipeline",
		Description: "Emergency control to stop new work being dispatched. Protected by the pipeline-control capability. " +
			"If this run's team holds that capability deployment-wide it pauses the whole deployment; otherwise it " +
			"pauses only this run's own team's dispatch, and every other team keeps dispatching. Runs already in " +
			"flight finish either way.",
		InputSchema: pausePipelineInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if controller == nil {
				return Result{Text: noPipelineController, IsError: true}
			}
			repo, _ := args["repo"].(string)
			reason, _ := args["reason"].(string)
			repo = strings.TrimSpace(repo)
			reason = strings.TrimSpace(reason)
			if repo == "" {
				return Result{Text: "pause_pipeline failed: repo is required and cannot be empty", IsError: true}
			}
			if reason == "" {
				return Result{Text: "pause_pipeline failed: reason is required and cannot be empty", IsError: true}
			}
			team, err := controller.PausePipeline(ctx, repo, reason)
			if err != nil {
				return Result{Text: fmt.Sprintf("Failed to pause pipeline for repository %s: %v", repo, err), IsError: true}
			}
			if team != "" {
				return Result{Text: fmt.Sprintf("Team %s's dispatch is paused (%s: %s). This run's pipeline-control "+
					"is scoped to its own team, so every other team keeps dispatching, and runs already in flight "+
					"finish.", team, repo, reason)}
			}
			return Result{Text: fmt.Sprintf("Pipeline successfully paused for repository %s: %s", repo, reason)}
		},
	}
}

func resumePipelineTool(controller PipelineController) Tool {
	return Tool{
		Name: "resume_pipeline",
		Description: "Emergency control to let new work be dispatched again, lifting what pause_pipeline paused: " +
			"the whole deployment if this run's team holds pipeline-control deployment-wide, otherwise only this " +
			"run's own team's pause.",
		InputSchema: resumePipelineInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if controller == nil {
				return Result{Text: noPipelineController, IsError: true}
			}
			repo, _ := args["repo"].(string)
			reason, _ := args["reason"].(string)
			repo = strings.TrimSpace(repo)
			reason = strings.TrimSpace(reason)
			if repo == "" {
				return Result{Text: "resume_pipeline failed: repo is required and cannot be empty", IsError: true}
			}
			if reason == "" {
				return Result{Text: "resume_pipeline failed: reason is required and cannot be empty", IsError: true}
			}
			team, err := controller.ResumePipeline(ctx, repo, reason)
			if err != nil {
				return Result{Text: fmt.Sprintf("Failed to resume pipeline for repository %s: %v", repo, err), IsError: true}
			}
			if team != "" {
				return Result{Text: fmt.Sprintf("Team %s's dispatch is resumed (%s: %s).", team, repo, reason)}
			}
			return Result{Text: fmt.Sprintf("Pipeline successfully resumed for repository %s: %s", repo, reason)}
		},
	}
}
