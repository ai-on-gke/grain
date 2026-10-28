package ui_test

// GET /api/sandboxes' standingSandboxes: what the daemon's standing
// sandbox supervisor holds, reported without ever reaching a secret value.

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestSandboxHealthReportsStandingSandboxes(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	started := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	client.Config.StandingSandboxes = func() []ui.StandingSandboxStatus {
		return []ui.StandingSandboxStatus{
			{Name: "indexer", Image: "ghcr.io/x/indexer:1", StartedAt: started, Restarts: 2},
			{Name: "web", Image: "ghcr.io/x/web:1", StartedAt: started, Failed: true,
				LastError: "serve: exit 1: token=plain-shapeless-value"},
		}
	}
	client.Config.LogRedactor = func() (*grainlog.Redactor, error) {
		return grainlog.NewRedactor([]grainlog.KnownValue{{Label: "secret web-token", Value: "plain-shapeless-value"}}), nil
	}

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "plain-shapeless-value") {
		t.Fatalf("a secret value reached the response: %s", rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)
	if !got.Enabled {
		t.Error("a UI with only a standing sandbox supervisor reported itself disabled")
	}
	if len(got.StandingSandboxes) != 2 {
		t.Fatalf("standingSandboxes = %+v, want two", got.StandingSandboxes)
	}
	indexer, web := got.StandingSandboxes[0], got.StandingSandboxes[1]
	if indexer.Name != "indexer" || indexer.Image != "ghcr.io/x/indexer:1" || indexer.Restarts != 2 ||
		!indexer.StartedAt.Equal(started) || indexer.Failed || indexer.LastError != "" {
		t.Errorf("indexer = %+v", indexer)
	}
	if !web.Failed || !strings.HasPrefix(web.LastError, "serve: exit 1: token=") {
		t.Errorf("web = %+v, want failed with its redacted launch error", web)
	}
}

// A redaction list that cannot be read withholds the error rather than
// serving it unredacted.
func TestSandboxHealthWithholdsStandingErrorsItCannotRedact(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.StandingSandboxes = func() []ui.StandingSandboxStatus {
		return []ui.StandingSandboxStatus{{Name: "web", Failed: true, LastError: "token=plain-shapeless-value"}}
	}
	client.Config.LogRedactor = func() (*grainlog.Redactor, error) { return nil, errors.New("store unreadable") }

	got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if len(got.StandingSandboxes) != 1 || strings.Contains(got.StandingSandboxes[0].LastError, "plain-shapeless-value") {
		t.Fatalf("standingSandboxes = %+v, want the error withheld", got.StandingSandboxes)
	}
}
