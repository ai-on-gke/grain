package ui_test

import (
	"net/http"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// Auto overcommit: the Settings pane shows the budget the daemon actually
// admits against, which in auto mode is the controller's, and the
// controller's own state beside it. Fixed mode is unchanged.
func TestSettingsShowsTheAutoOvercommitBudget(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	ctrl := model.NewOvercommitController(model.OvercommitSettings{Mode: model.OvercommitAuto}, 4, 1.5)
	c.Config.Overcommit = ctrl.State

	cpu, mem := 2.0, 1.0
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxCPUOvercommit: &cpu, SandboxMemoryOvercommit: &mem})
	if err != nil {
		t.Fatal(err)
	}
	b := got.SandboxCommitment.Budget
	if b == nil || b.Mode != model.OvercommitFixed || b.CPUOvercommit != 2 || b.MemoryOvercommit != 1 {
		t.Fatalf("fixed mode budget = %+v, want the stored 2/1", b)
	}

	got, err = c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxOvercommit: &model.OvercommitSettings{Mode: model.OvercommitAuto, CPUMax: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxOvercommit.Mode != model.OvercommitAuto || got.SandboxOvercommit.CPUMax != 5 ||
		got.SandboxOvercommit.MemoryMax != model.DefaultOvercommitMemoryMax {
		t.Errorf("stored overcommit settings read back as %+v", got.SandboxOvercommit)
	}
	b = got.SandboxCommitment.Budget
	if b == nil || b.Mode != model.OvercommitAuto || b.CPUOvercommit != 4 || b.MemoryOvercommit != 1.5 || b.Overcommit == nil {
		t.Fatalf("auto mode budget = %+v, want the controller's 4/1.5 and its state", b)
	}

	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxOvercommit: &model.OvercommitSettings{Mode: model.OvercommitAuto, CPUMin: 3, CPUMax: 2},
	}); err == nil {
		t.Error("a cpu minimum above its maximum was saved")
	}
}

func TestSandboxHealthIncludesOvercommitState(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctrl := model.NewOvercommitController(model.OvercommitSettings{Mode: model.OvercommitAuto, CPUMax: 5}, 3.5, 1.4)
	client.Config.Overcommit = ctrl.State

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)
	if !got.Enabled {
		t.Fatal("Enabled = false, want true when Overcommit is configured")
	}
	if got.Overcommit == nil {
		t.Fatal("Overcommit = nil, want controller state")
	}
	if got.Overcommit.Mode != model.OvercommitAuto || got.Overcommit.CPUOvercommit != 3.5 || got.Overcommit.MemoryOvercommit != 1.4 {
		t.Fatalf("Overcommit = %+v, want auto mode with 3.5/1.4", got.Overcommit)
	}
}
