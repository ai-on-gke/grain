package staterepo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// moveDefaultTeamSettings is dumpFixups' step past schema 21: the
// deployment's git connections, pinned connection and per-model settings
// leave the default team's row for grain_config, and the row's default
// model is folded into grain_config.default_model
// (docs/single-tenant-reset.md, 6-ii-b2).
//
// A column moving between tables is the one schema change Import cannot
// absorb on its own: it would pass over team.git_connections as a column
// this build does not have, and grain_config.git_connections would come
// up empty, so every clone would fail closed on a deployment that had
// connections an hour ago. This copies each value across in the dump
// before Import ever sees it.
//
// The team's default model wins over the one grain_config already held,
// when it names one: it is the one dispatch ran on (a task that chose no
// model ran on the default team's), and grain_config.default_model only
// ever named what the Settings page showed. Every other column is new to
// grain_config at 22, so the team's value is simply the value.
//
// Only the default team's row is read. A dump holding any other team is
// refused by the startup guard's own second-team question once it is
// imported, so there is nothing of another team's to carry.
func moveDefaultTeamSettings(dir string) error {
	teamPath := filepath.Join(dir, filepath.FromSlash(DumpPath("team")))
	teams, err := readRowsIfPresent(teamPath)
	if err != nil || teams == nil {
		return err
	}
	moved := map[string]any{}
	for _, row := range teams {
		if fmt.Sprint(row["id"]) != model.DefaultTeamID {
			continue
		}
		for _, col := range movedTeamSettingColumns {
			if v, ok := row[col]; ok {
				if s, isText := v.(string); !isText || strings.TrimSpace(s) != "" {
					moved[col] = v
				}
				delete(row, col)
			}
		}
	}
	if len(moved) == 0 {
		return writeRows(teamPath, teams)
	}

	configPath := filepath.Join(dir, filepath.FromSlash(DumpPath("grain_config")))
	configs, err := readRowsIfPresent(configPath)
	if err != nil {
		return err
	}
	if len(configs) == 0 {
		// The daemon writes this row on its first start, so a dump
		// with settings on the team's row and none here is one nobody
		// ran; there is no row to carry them onto, and making one up
		// would stand in for every setting it does not mention.
		return fmt.Errorf("the default team's row holds the deployment's %s, but the dump has no grain_config row to move them to",
			strings.Join(movedColumnsIn(moved), ", "))
	}
	for _, cfg := range configs {
		for col, v := range moved {
			cfg[col] = v
		}
	}
	if err := writeRows(configPath, configs); err != nil {
		return err
	}
	return writeRows(teamPath, teams)
}

// movedTeamSettingColumns are the team columns schema 22 moved onto
// grain_config, each under its own name there.
var movedTeamSettingColumns = []string{
	"git_connections",
	"git_credential",
	"agent_backend_id",
	"default_model",
	"model_settings",
}

// readRowsIfPresent is readRows for a table file that may not be there:
// nil rows and no error for a dump with no file for it.
func readRowsIfPresent(path string) ([]map[string]any, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	return readRows(path)
}

// writeRows writes rows back as a table file Import reads. Columns come
// out in encoding/json's alphabetical order rather than the table's own:
// a fixup writes a scratch copy, which Migrated replaces with a fresh
// export once the import is done.
func writeRows(path string, rows []map[string]any) error {
	if rows == nil {
		rows = []map[string]any{}
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return fmt.Errorf("staterepo: encoding %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	return nil
}

// writeLeftBehind writes back what a fixup did not carry out of a table
// this build no longer has, and removes the file when it carried every
// row. A file left holding nothing would still name a table this build
// lacks, and the migration check would report every row in it as one
// that is about to be ignored (check.go) -- telling an operator the
// rows the fixup just carried across are about to be lost.
func writeLeftBehind(path string, rows []map[string]any) error {
	if len(rows) > 0 {
		return writeRows(path, rows)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("staterepo: removing %s: %w", path, err)
	}
	return nil
}

// movedColumnsIn is the moved columns m holds, in movedTeamSettingColumns' order.
func movedColumnsIn(m map[string]any) []string {
	var out []string
	for _, col := range movedTeamSettingColumns {
		if _, ok := m[col]; ok {
			out = append(out, col)
		}
	}
	return out
}
