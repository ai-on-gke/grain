package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testPipelineController struct {
	pausedRepo    string
	pausedReason  string
	pauseErr      error
	resumedRepo   string
	resumedReason string
	resumeErr     error
	team          string
}

func (c *testPipelineController) PausePipeline(ctx context.Context, repo, reason string) (string, error) {
	c.pausedRepo = repo
	c.pausedReason = reason
	return c.team, c.pauseErr
}

func (c *testPipelineController) ResumePipeline(ctx context.Context, repo, reason string) (string, error) {
	c.resumedRepo = repo
	c.resumedReason = reason
	return c.team, c.resumeErr
}

// TestPipelineControlToolsSayWhenTheyReachedOnlyTheTeam is grain/task-2555:
// a team-scoped controller's answer tells the agent it paused its own team
// and that the other teams keep dispatching.
func TestPipelineControlToolsSayWhenTheyReachedOnlyTheTeam(t *testing.T) {
	ctrl := &testPipelineController{team: "payments"}
	res := pausePipelineTool(ctrl).Handler(t.Context(), map[string]any{"repo": "acme/pay", "reason": "bad deploy"})
	if res.IsError || !strings.Contains(res.Text, "Team payments's dispatch is paused") ||
		!strings.Contains(res.Text, "every other team keeps dispatching") {
		t.Fatalf("pause_pipeline answered %q, want it to say only team payments was paused", res.Text)
	}
	res = resumePipelineTool(ctrl).Handler(t.Context(), map[string]any{"repo": "acme/pay", "reason": "fixed"})
	if res.IsError || !strings.Contains(res.Text, "Team payments's dispatch is resumed") {
		t.Fatalf("resume_pipeline answered %q, want team payments resumed", res.Text)
	}
}

func TestPipelineControlToolsNilController(t *testing.T) {
	tools := NewPipelineControlTools(nil)
	ctx := t.Context()

	for _, tool := range tools {
		res := tool.Handler(ctx, map[string]any{"repo": "bwsalmon/grain", "reason": "testing"})
		if res.Text != noPipelineController {
			t.Fatalf("%s returned %q, want %q", tool.Name, res.Text, noPipelineController)
		}
	}
}

func TestPausePipelineTool(t *testing.T) {
	ctrl := &testPipelineController{}
	tool := pausePipelineTool(ctrl)
	ctx := t.Context()

	// Missing arguments
	res := tool.Handler(ctx, map[string]any{})
	if !strings.Contains(res.Text, "repo is required") {
		t.Fatalf("expected error message for missing repo, got: %s", res.Text)
	}

	res = tool.Handler(ctx, map[string]any{"repo": "bwsalmon/grain"})
	if !strings.Contains(res.Text, "reason is required") {
		t.Fatalf("expected error message for missing reason, got: %s", res.Text)
	}

	// Successful pause
	res = tool.Handler(ctx, map[string]any{"repo": "bwsalmon/grain", "reason": "investigating failure"})
	if ctrl.pausedRepo != "bwsalmon/grain" || ctrl.pausedReason != "investigating failure" {
		t.Fatalf("controller got repo %q reason %q", ctrl.pausedRepo, ctrl.pausedReason)
	}
	if !strings.Contains(res.Text, "Pipeline successfully paused") {
		t.Fatalf("expected success message, got: %s", res.Text)
	}

	// Controller error
	ctrl.pauseErr = errors.New("database locked")
	res = tool.Handler(ctx, map[string]any{"repo": "bwsalmon/grain", "reason": "testing"})
	if !strings.Contains(res.Text, "database locked") {
		t.Fatalf("expected error from controller, got: %s", res.Text)
	}
}

func TestResumePipelineTool(t *testing.T) {
	ctrl := &testPipelineController{}
	tool := resumePipelineTool(ctrl)
	ctx := t.Context()

	// Missing arguments
	res := tool.Handler(ctx, map[string]any{})
	if !strings.Contains(res.Text, "repo is required") {
		t.Fatalf("expected error message for missing repo, got: %s", res.Text)
	}

	// Successful resume
	res = tool.Handler(ctx, map[string]any{"repo": "bwsalmon/grain", "reason": "incident resolved"})
	if ctrl.resumedRepo != "bwsalmon/grain" || ctrl.resumedReason != "incident resolved" {
		t.Fatalf("controller got repo %q reason %q", ctrl.resumedRepo, ctrl.resumedReason)
	}
	if !strings.Contains(res.Text, "Pipeline successfully resumed") {
		t.Fatalf("expected success message, got: %s", res.Text)
	}

	// Controller error
	ctrl.resumeErr = errors.New("network timeout")
	res = tool.Handler(ctx, map[string]any{"repo": "bwsalmon/grain", "reason": "testing"})
	if !strings.Contains(res.Text, "network timeout") {
		t.Fatalf("expected error from controller, got: %s", res.Text)
	}
}
