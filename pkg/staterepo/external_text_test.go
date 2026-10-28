package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func TestEditableTextLivesInNaturalFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO repo_config(owner,name,default_capabilities,setup_command) VALUES ('acme','widget','','make setup')`,
		`INSERT INTO repo_workflow(owner,name,path,text) VALUES ('acme','widget','ci/build.yml','jobs:
  build: {}
')`,
		`INSERT INTO memory(owner,name,text) VALUES ('acme','widget','# Repository memory
')`,
		`INSERT INTO memory_document(owner,name,document,summary,text) VALUES ('acme','widget','debugging','tips','# Debugging
')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"scripts/repos/acme/widget/setup.sh":              "make setup",
		"workflows/acme/widget/ci/build.yml":              "jobs:\n  build: {}\n",
		"memory/repos/acme/widget.md":                     "# Repository memory\n",
		"memory/documents/repos/acme/widget/debugging.md": "# Debugging\n",
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, table := range []string{"grain_config", "repo_config", "repo_workflow", "memory", "memory_document"} {
		data, err := os.ReadFile(filepath.Join(dir, staterepo.TablesDir, table+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "# Repository memory") ||
			strings.Contains(string(data), "make setup") || strings.Contains(string(data), "jobs:") ||
			strings.Contains(string(data), "# Debugging") {
			t.Errorf("%s.json still embeds editable text:\n%s", table, data)
		}
	}
}

// grain_config.sandbox_image is an OCI reference, not a file: it lives
// inline in tables/grain_config.json. A dump an older build wrote still
// carries sandbox_setup_script as a reference to
// scripts/guest-customization.sh, and imports cleanly whether or not
// that file is still beside it -- the column is one this build does not
// have, so neither it nor the file it names is read.
func TestTheSandboxImageIsInlineAndAnOldSetupScriptIsIgnored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultConfig()
	cfg.SandboxImage = "registry.example/team/guest:v1"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, staterepo.TablesDir, "grain_config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"sandbox_image": "registry.example/team/guest:v1"`) {
		t.Fatalf("grain_config.json does not carry the sandbox image inline:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "scripts", "guest-customization.sh")); !os.IsNotExist(err) {
		t.Fatalf("the export still writes scripts/guest-customization.sh (%v)", err)
	}

	// The shape an older build dumped: the script as a file reference,
	// and the file itself gone.
	old := strings.Replace(string(data), `"sandbox_image": "registry.example/team/guest:v1"`,
		`"sandbox_image": "registry.example/team/guest:v2", "sandbox_setup_script": {"file": "scripts/guest-customization.sh"}`, 1)
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.ImportTables(ctx, db, dir, []string{"grain_config"}); err != nil {
		t.Fatalf("a dump carrying the retired setup script does not import: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxImage != "registry.example/team/guest:v2" {
		t.Fatalf("sandbox image = %q, want the edited one", got.SandboxImage)
	}
}

func TestOffHostRepoExternalText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []string{
		`INSERT INTO repo_config(owner,name,default_capabilities,setup_command) VALUES ('git.forge.example/experimental','test-repo','','make build')`,
		`INSERT INTO repo_workflow(owner,name,path,text) VALUES ('git.forge.example/experimental','test-repo','ci/check.yml','jobs:
  check: {}
')`,
		`INSERT INTO memory(owner,name,text) VALUES ('git.forge.example/experimental','test-repo','# Off-host repo memory
')`,
		`INSERT INTO memory_document(owner,name,document,summary,text) VALUES ('git.forge.example/experimental','test-repo','runbook','notes','# Runbook
')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatalf("exporting config with off-host repo: %v", err)
	}

	files := map[string]string{
		"scripts/repos/git.forge.example/experimental/test-repo/setup.sh":            "make build",
		"workflows/git.forge.example/experimental/test-repo/ci/check.yml":            "jobs:\n  check: {}\n",
		"memory/repos/git.forge.example/experimental/test-repo.md":                   "# Off-host repo memory\n",
		"memory/documents/repos/git.forge.example/experimental/test-repo/runbook.md": "# Runbook\n",
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	for _, table := range []string{"repo_config", "repo_workflow", "memory", "memory_document"} {
		data, err := os.ReadFile(filepath.Join(dir, staterepo.TablesDir, table+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "# Off-host repo memory") || strings.Contains(string(data), "make build") ||
			strings.Contains(string(data), "jobs:") || strings.Contains(string(data), "# Runbook") {
			t.Errorf("%s.json still embeds editable text:\n%s", table, data)
		}
	}

	newSetup := "make build-all"
	if err := os.WriteFile(filepath.Join(dir, "scripts", "repos", "git.forge.example", "experimental", "test-repo", "setup.sh"), []byte(newSetup), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.ImportTables(ctx, db, dir, []string{"repo_config"}); err != nil {
		t.Fatalf("importing repo_config: %v", err)
	}
	var gotSetup string
	if err := db.QueryRowContext(ctx, "SELECT setup_command FROM repo_config WHERE owner = 'git.forge.example/experimental' AND name = 'test-repo'").Scan(&gotSetup); err != nil {
		t.Fatal(err)
	}
	if gotSetup != newSetup {
		t.Fatalf("setup_command = %q, want %q", gotSetup, newSetup)
	}
}

func TestExternalTextRejectsUnsafeComponents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, unsafeOwner := range []string{
		"..",
		".",
		"",
		"../escape",
		"escape/..",
		"escape/../other",
		"/leading",
		"trailing/",
		"double//slash",
		"dot/./slash",
		"back\\slash",
	} {
		t.Run("owner="+unsafeOwner, func(t *testing.T) {
			t.Parallel()
			db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := model.New(db)
			if err := store.Init(ctx); err != nil {
				t.Fatal(err)
			}

			if _, err := db.ExecContext(ctx, "INSERT INTO repo_config(owner,name,default_capabilities,setup_command) VALUES (?, 'widget', '', 'echo hi')", unsafeOwner); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := staterepo.ExportConfig(ctx, db, dir); err == nil {
				t.Errorf("ExportConfig with unsafe owner %q succeeded, want error", unsafeOwner)
			}
		})
	}
}
