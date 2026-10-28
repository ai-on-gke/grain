package ui_test

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestTaskCreationDirectEmptyCreatorValidation(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Actor = model.Principal{} // clear default actor

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "no creator task",
	})
	if err == nil || !strings.Contains(err.Error(), "creator is required") {
		t.Fatalf("expected creator is required error, got %v", err)
	}
}

func TestTaskCreationCapabilityPermissions(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// 1. Requesting CI-only capability (e.g. package-publish) on task creation is rejected
	caps := []string{"package-publish"}
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "ci cap task",
		Creator:      "alice",
		Capabilities: &caps,
	})
	if err == nil || !strings.Contains(err.Error(), "package-publish is a CI capability, not a task grant") {
		t.Fatalf("expected CI capability rejection, got %v", err)
	}

	// 2. Allowed capability creates task successfully
	allowedCaps := []string{"self-debug"}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "valid cap task",
		Creator:      "alice",
		Capabilities: &allowedCaps,
	})
	if err != nil {
		t.Fatalf("CreateTask with self-debug failed: %v", err)
	}

	// 3. SetCapability with CI capability is rejected
	err = c.SetCapability(ctx, task.ID, "package-publish", true)
	if err == nil || !strings.Contains(err.Error(), "package-publish is a CI capability, not a task grant") {
		t.Fatalf("expected SetCapability CI rejection, got %v", err)
	}
}
