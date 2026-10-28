package ui_test

import (
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// A capability toggle naming no capability -- an empty body, or one with
// the wrong field names -- is a client bug, and a 200 that changed
// nothing hid it (grain/task-1613).
func TestACapabilityToggleNamingNothingIsABadRequest(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "toggled", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := ui.NewServerWithClient(c)
	for _, body := range []string{`{}`, `{"capability":"db-read","enabled":true}`, `{"id":"  ","attach":true}`} {
		if rec := do(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/capabilities", body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST capabilities %s: %d %s, want 400", body, rec.Code, rec.Body)
		}
	}
}
