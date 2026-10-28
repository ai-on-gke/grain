package staterepo_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// TestGitConnectionsStateRepoExportImport verifies that:
// 1. Exporting the deployment's GitConnections writes git_connections to tables/grain_config.json.
// 2. Editing git_connections in tables/grain_config.json and importing it updates the store.
// 3. Importing a config row carrying a column this build lacks is cleanly ignored without crash or corruption.
func TestGitConnectionsStateRepoExportImport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)

	// 1. The deployment's git connections, on grain_config since schema 22
	cfg := model.DefaultConfig()
	cfg.GitConnections = []model.TeamGitConnection{
		{Credential: "cred-dotcom", Scope: "acme"},
		{Credential: "cred-ghe", Host: "ghe.example.com"},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	// Export to state repo directory
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("staterepo.Export: %v", err)
	}

	configJSONPath := filepath.Join(dir, staterepo.TablesDir, "grain_config.json")
	data, err := os.ReadFile(configJSONPath)
	if err != nil {
		t.Fatalf("ReadFile grain_config.json: %v", err)
	}

	// Verify git_connections column is present in the table dump
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("Unmarshal grain_config.json: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("grain_config.json holds %d rows, want 1", len(rows))
	}
	foundRowIdx := 0

	rawConns, ok := rows[foundRowIdx]["git_connections"]
	if !ok {
		t.Fatalf("grain_config.json row does not contain git_connections: %+v", rows[foundRowIdx])
	}
	t.Logf("Exported git_connections: %v", rawConns)

	// 2. Edit git_connections in the exported table dump
	editedConns := []model.TeamGitConnection{
		{Credential: "cred-edited", Host: "gitlab.example.com", Scope: "edited-scope"},
	}
	encodedEdited, err := json.Marshal(editedConns)
	if err != nil {
		t.Fatal(err)
	}
	rows[foundRowIdx]["git_connections"] = string(encodedEdited)

	editedBytes, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configJSONPath, editedBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Import into a fresh clean database
	restoredStore, restoredDB := openDB(t)
	if err := staterepo.Import(ctx, restoredDB, dir); err != nil {
		t.Fatalf("staterepo.Import after edit: %v", err)
	}

	restored, err := restoredStore.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig after import: %v", err)
	}
	if restored == nil {
		t.Fatalf("the config row is not there after import")
	}
	if len(restored.GitConnections) != 1 || restored.GitConnections[0].Credential != "cred-edited" || restored.GitConnections[0].Host != "gitlab.example.com" {
		t.Fatalf("restored git connections = %+v, want edited connection", restored.GitConnections)
	}

	// 3. Import a config row carrying a column this build does not have
	for idx := range rows {
		rows[idx]["a_column_from_a_later_build"] = 1
	}
	withUnknownColumnBytes, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configJSONPath, withUnknownColumnBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	ignoredStore, ignoredDB := openDB(t)
	if err := staterepo.Import(ctx, ignoredDB, dir); err != nil {
		t.Fatalf("staterepo.Import with an unknown column failed: %v", err)
	}

	afterIgnore, err := ignoredStore.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig after unknown column import: %v", err)
	}
	if afterIgnore == nil || len(afterIgnore.GitConnections) != 1 || afterIgnore.GitConnections[0].Credential != "cred-edited" {
		t.Fatalf("config row corrupted after unknown column import: %+v", afterIgnore)
	}
}
