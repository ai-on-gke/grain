package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// template.interactive (model.Template.Interactive) goes out to the
// state repository under the same `interactive` key a repository's own
// .grain templates use, and comes back through the settings import --
// the path a merged pull request against grain/config takes -- with the
// switch intact, on and off.
func TestATemplatesInteractiveSwitchRoundTripsThroughTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	on := model.Template{
		ID: "template-1", Name: "GKE cluster", Title: "Set up a cluster", Body: "ask first",
		Interactive: true, CreatedAt: now,
	}
	off := model.Template{
		ID: "template-2", Name: "Sweep", Title: "Sweep", Body: "sweep it", CreatedAt: now,
	}
	for _, tmpl := range []model.Template{on, off} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatalf("putting %s: %v", tmpl.ID, err)
		}
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	path := staterepo.DumpPath("template")
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "interactive: true") {
		t.Errorf("%s does not carry interactive: true:\n%s", path, raw)
	}

	other, otherDB := openDB(t)
	if err := staterepo.ImportTables(ctx, otherDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing the settings tables: %v", err)
	}
	for _, want := range []model.Template{on, off} {
		got, err := other.GetTemplate(ctx, want.ID)
		if err != nil || got == nil {
			t.Fatalf("reading the imported %s: %v, %v", want.ID, got, err)
		}
		if got.Interactive != want.Interactive {
			t.Errorf("%s: Interactive after the round trip = %v, want %v", want.ID, got.Interactive, want.Interactive)
		}
	}
}
