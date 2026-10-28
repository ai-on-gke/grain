// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package staterepo

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// foldTeamRepoSettings is dumpFixups' step past schema 22: the default
// team's per-repository settings (team_repo_config) fold into repo_config,
// and the deployment's GCP keys move from team_gcp_key to gcp_key
// (docs/single-tenant-reset.md, 6-ii-b3).
//
// Rows moving between tables is not something Import absorbs: it would
// pass over both old tables as tables this build does not have, and a
// deployment would come up with its GCP keys gone and every repository
// override it had set -- a clone URL, a git credential, the CI
// capabilities a release runs with -- quietly back to repo_config's.
//
// Each team_repo_config row of the default team lands on the repo_config
// row of the same repository, made when there is none. Where both say
// something, the team's wins, because it was the layer dispatch and CI
// read first (model.EffectiveRepoConfigFor before 23), with one
// exception: default_capabilities is the union of the two, which is what
// a task targeting the repository was filed holding. A {"file": ...}
// reference comes across as the text it names, since repo_config's own
// file references live at a path of their own.
//
// Another team's rows are left where they are, so that the startup
// guard's question about them (retired.go) refuses the dump rather than
// leaving them behind. A table with none is removed from the copy, so
// the migration check does not report as ignored rows this carried.
func foldTeamRepoSettings(dir string) error {
	if err := foldTeamRepoConfig(dir); err != nil {
		return err
	}
	return moveTeamGCPKeys(dir)
}

// foldTeamRepoConfig is foldTeamRepoSettings' half for team_repo_config.
func foldTeamRepoConfig(dir string) error {
	teamPath := filepath.Join(dir, filepath.FromSlash(DumpPath("team_repo_config")))
	teamRows, err := readRowsIfPresent(teamPath)
	if err != nil {
		return err
	}
	if len(teamRows) == 0 {
		return writeLeftBehind(teamPath, nil)
	}
	configPath := filepath.Join(dir, filepath.FromSlash(DumpPath("repo_config")))
	configs, err := readRowsIfPresent(configPath)
	if err != nil {
		return err
	}
	var others []map[string]any
	for _, row := range teamRows {
		if fmt.Sprint(row["team_id"]) != model.DefaultTeamID {
			others = append(others, row)
			continue
		}
		owner, name := fmt.Sprint(row["owner"]), fmt.Sprint(row["name"])
		var target map[string]any
		for _, cfg := range configs {
			if strings.EqualFold(fmt.Sprint(cfg["owner"]), owner) && strings.EqualFold(fmt.Sprint(cfg["name"]), name) {
				target = cfg
				break
			}
		}
		if target == nil {
			target = map[string]any{"owner": owner, "name": name, "default_capabilities": ""}
			configs = append(configs, target)
		}
		for _, col := range foldedTeamRepoColumns {
			v, err := resolveFileValue(dir, row[col])
			if err != nil {
				return fmt.Errorf("team_repo_config %s/%s, column %s: %w", owner, name, col, err)
			}
			if !saysSomething(v) {
				continue
			}
			if col == "default_capabilities" {
				existing, err := resolveFileValue(dir, target[col])
				if err != nil {
					return fmt.Errorf("repo_config %s/%s, column %s: %w", owner, name, col, err)
				}
				v = unionCapabilityCSV(existing, v)
			}
			target[col] = v
		}
	}
	if err := writeRows(configPath, configs); err != nil {
		return err
	}
	return writeLeftBehind(teamPath, others)
}

// foldedTeamRepoColumns are team_repo_config's setting columns, each
// under its own name on repo_config.
var foldedTeamRepoColumns = []string{
	"clone_url",
	"default_capabilities",
	"ci_capabilities",
	"prompt_extension",
	"setup_command",
	"ci_mode",
	"git_credential",
	"repair_template_id",
	"require_branch_up_to_date",
	"watch_default_branch",
	"release_secrets",
}

// saysSomething is whether a dump value sets anything: not null (the
// team row's "inherit" for its two switches) and not blank text.
func saysSomething(v any) bool {
	if v == nil {
		return false
	}
	s, isText := v.(string)
	return !isText || strings.TrimSpace(s) != ""
}

// unionCapabilityCSV is two comma-separated capability lists as one,
// first's ids first, without repeats.
func unionCapabilityCSV(first, second any) string {
	var out []string
	seen := map[string]bool{}
	for _, v := range []any{first, second} {
		s, _ := v.(string)
		for _, id := range splitCSV(s) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return joinCSV(out)
}

// moveTeamGCPKeys is foldTeamRepoSettings' half for team_gcp_key: the
// default team's rows, without their team, become gcp_key's.
func moveTeamGCPKeys(dir string) error {
	oldPath := filepath.Join(dir, filepath.FromSlash(DumpPath("team_gcp_key")))
	rows, err := readRowsIfPresent(oldPath)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return writeLeftBehind(oldPath, nil)
	}
	newPath := filepath.Join(dir, filepath.FromSlash(DumpPath("gcp_key")))
	keys, err := readRowsIfPresent(newPath)
	if err != nil {
		return err
	}
	var others []map[string]any
	for _, row := range rows {
		if fmt.Sprint(row["team_id"]) != model.DefaultTeamID {
			others = append(others, row)
			continue
		}
		delete(row, "team_id")
		keys = append(keys, row)
	}
	if err := writeRows(newPath, keys); err != nil {
		return err
	}
	return writeLeftBehind(oldPath, others)
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func joinCSV(items []string) string {
	return strings.Join(items, ",")
}
