package ui_test

// One repository's task lifecycle over the operator API
// (docs/task-lifecycle-sandbox.md, "Grain's own API").

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/lifecycle"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The interface is here rather than in pkg/lifecycle so that serving a
// settings pane does not depend on a sandbox pool. This is what keeps
// that from becoming two shapes that drift.
var _ ui.Lifecycles = (*lifecycle.Registry)(nil)

type fakeLifecycles struct {
	status    model.LifecycleStatus
	refreshed int
	err       error
	asked     model.RepoRef
}

func (f *fakeLifecycles) Status(_ context.Context, repo model.RepoRef) model.LifecycleStatus {
	f.asked = repo
	return f.status
}

func (f *fakeLifecycles) Refresh(_ context.Context, repo model.RepoRef) (model.LifecycleStatus, error) {
	f.asked = repo
	f.refreshed++
	return f.status, f.err
}

func ready() model.LifecycleStatus {
	at := time.Date(2026, 9, 16, 9, 12, 0, 0, time.UTC)
	since := time.Date(2026, 9, 17, 2, 0, 0, 0, time.UTC)
	return model.LifecycleStatus{
		Declared: true, Name: "acme task lifecycle",
		Image:      "ghcr.io/acme/widgets/grain-lifecycle",
		Version:    "acme/widgets@sha256:9f2c",
		State:      model.LifecycleReady,
		ResolvedAt: &at,
		Sandbox:    &model.LifecycleSandboxInfo{State: "serving", Since: &since},
		Instances:  model.LifecycleInstances{PinnedHere: 14, OnOlderVersions: 3},
	}
}

func TestGetRepoLifecycleReportsWhatIsPinnedAndWhatIsServing(t *testing.T) {
	t.Parallel()
	c, _, _ := testClient(t)
	fake := &fakeLifecycles{status: ready()}
	c.Lifecycles = fake
	srv := ui.NewServerWithClient(c)

	rec := do(t, srv, http.MethodGet, "/api/repos/acme/widgets/lifecycle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"declared": true,
		"image":    "ghcr.io/acme/widgets/grain-lifecycle",
		"version":  "acme/widgets@sha256:9f2c",
		"state":    "ready",
	} {
		if body[key] != want {
			t.Errorf("%s = %v, want %v", key, body[key], want)
		}
	}
	instances, _ := body["instances"].(map[string]any)
	if instances["pinned_here"] != float64(14) || instances["on_older_versions"] != float64(3) {
		t.Errorf("instances = %v", body["instances"])
	}
	sandbox, _ := body["sandbox"].(map[string]any)
	if sandbox["state"] != "serving" {
		t.Errorf("sandbox = %v", body["sandbox"])
	}
	if fake.asked != (model.RepoRef{Owner: "acme", Name: "widgets"}) {
		t.Errorf("asked about %+v", fake.asked)
	}
}

// A deployment with no registry wired up answers `none` for every
// repository, which is exactly what a repository with no declaration
// gets -- and what every repository gets today.
func TestGetRepoLifecycleWithoutARegistryIsTheDeploymentDefault(t *testing.T) {
	t.Parallel()
	c, _, _ := testClient(t)
	srv := ui.NewServerWithClient(c)
	rec := do(t, srv, http.MethodGet, "/api/repos/acme/widgets/lifecycle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d: %s", rec.Code, rec.Body)
	}
	var status model.LifecycleStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != model.LifecycleNone || status.Declared {
		t.Errorf("status = %+v", status)
	}
}

// Refresh re-resolves the tag and pulls, and is not a build: a build is a
// release run, which POST .../release-runs already starts.
func TestRefreshRepoLifecycleResolvesAgain(t *testing.T) {
	t.Parallel()
	c, _, _ := testClient(t)
	fake := &fakeLifecycles{status: ready()}
	c.Lifecycles = fake
	srv := ui.NewServerWithClient(c)

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/lifecycle/refresh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d: %s", rec.Code, rec.Body)
	}
	if fake.refreshed != 1 {
		t.Errorf("refreshed %d times", fake.refreshed)
	}
}

// A refusal comes back as the state somebody can act on, with the reason
// on the status's own error field, rather than as a code they have to
// interpret.
func TestRefreshRepoLifecycleReportsARefusalAsAState(t *testing.T) {
	t.Parallel()
	c, _, _ := testClient(t)
	refused := ready()
	refused.State, refused.Error = model.LifecycleUnpublished, "manifest unknown"
	c.Lifecycles = &fakeLifecycles{status: refused, err: errors.New("resolving: manifest unknown")}
	srv := ui.NewServerWithClient(c)

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/lifecycle/refresh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d: %s", rec.Code, rec.Body)
	}
	var status model.LifecycleStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != model.LifecycleUnpublished || status.Error == "" {
		t.Errorf("status = %+v", status)
	}
}

func TestRepoLifecycleRefusesARepoItCannotParse(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Lifecycles = &fakeLifecycles{status: ready()}
	if _, err := c.RepoLifecycle(ctx, "not-a-repo"); err == nil {
		t.Fatal("accepted")
	}
	if _, err := c.RefreshRepoLifecycle(ctx, "not-a-repo"); err == nil {
		t.Fatal("accepted")
	}
}
