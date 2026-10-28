package trustboundary

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Every table a merged change is applied from has been decided about:
// one that grants nothing, or one a person has to look at. A new
// settings table fails here until somebody says which.
func TestEveryStateTableIsClassified(t *testing.T) {
	privileged := map[string]bool{}
	for _, table := range StatePrivilegedTables {
		privileged[table] = true
	}
	benign := map[string]bool{}
	for _, table := range StateBenignTables {
		if privileged[table] {
			t.Errorf("%s is listed as both privileged and benign", table)
		}
		benign[table] = true
	}
	for _, table := range staterepo.SettingsTables {
		if !privileged[table] && !benign[table] {
			t.Errorf("settings table %s is on neither StatePrivilegedTables nor StateBenignTables: "+
				"decide whether a merged change to it grants anything", table)
		}
	}
	settings := map[string]bool{}
	for _, table := range staterepo.SettingsTables {
		settings[table] = true
	}
	for _, table := range StatePrivilegedTables {
		if !settings[table] {
			t.Errorf("%s is privileged but not a settings table: drop it, or say why it is here", table)
		}
	}
}

// Wherever the export writes a privileged table -- its own dump file, or
// the settings document that carries it -- a change there is held. A
// benign table with a file of its own is not.
func TestStateDumpPathsAreClassifiedByTheirTables(t *testing.T) {
	for _, table := range StatePrivilegedTables {
		p := staterepo.DumpPath(table)
		if got := ClassifyState([]string{p}); len(got) != 1 || got[0].Category.ID != StatePrivilege.ID {
			t.Errorf("%s (%s) = %v, want state-privilege", table, p, categoryIDs(got))
		}
		// A repository written by an older build still carries its own
		// dump file, and a restore reads it.
		if got := ClassifyState([]string{"tables/" + table + ".json"}); len(got) != 1 {
			t.Errorf("tables/%s.json was not held", table)
		}
	}
	for _, table := range StateBenignTables {
		p := staterepo.DumpPath(table)
		if !strings.HasPrefix(p, staterepo.TablesDir+"/") {
			// Carried by a settings document beside privileged tables.
			continue
		}
		if got := ClassifyState([]string{p}); got != nil {
			t.Errorf("benign %s (%s) = %v, want nothing", table, p, categoryIDs(got))
		}
	}
}

func TestClassifyStateFailsClosed(t *testing.T) {
	held := []string{
		"tables/template_grant.json", "tables/grain_config.json", "tables/standing_sandbox_secret.json",
		"tables/gcp_key.json", "settings/templates.yml", "settings/schedules.yml", "settings/suites.yml",
		"scripts/guest-customization.sh", "scripts/repos/acme/widgets/setup.sh",
		"workflows/acme/widgets/ci.yml", ".github/workflows/grain-state-check.yml",
		"tables/task.json", "tables/some_table_added_later.json", "./tables/../tables/gcp_key.json",
	}
	got := ClassifyState(held)
	// The last path cleans to one already listed.
	if len(got) != 1 || !got[0].Category.RequiresHuman || len(got[0].Paths) != len(held)-1 {
		t.Fatalf("ClassifyState(%v) = %+v, want every path under one human-required category", held, got)
	}
	if !RequiresHuman(got) {
		t.Error("state-privilege must not be releasable by a review")
	}

	benign := []string{
		"tables/board.json", "tables/custom_field.json", "memory/deployment.md",
		"memory/documents/teams/default/notes.md", "release-notes/2026-W43.md", "README.md",
	}
	if got := ClassifyState(benign); got != nil {
		t.Errorf("ClassifyState(%v) = %+v, want nothing", benign, got)
	}
}

// The state repository's layout means nothing anywhere else: the same
// paths in an ordinary repository are judged by Classify alone.
func TestClassifyIgnoresStatePaths(t *testing.T) {
	if got := Classify([]string{"tables/template_grant.json", "settings/templates.yml"}); got != nil {
		t.Errorf("Classify = %v, want nothing", categoryIDs(got))
	}
}
