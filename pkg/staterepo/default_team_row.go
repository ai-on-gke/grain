package staterepo

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// foldDefaultTeamRow is dumpFixups' step past schema 23: what the default
// team's row still held that anything acted on moves onto grain_config,
// and the team table goes (docs/single-tenant-reset.md, 6-ii-b4).
//
// Import would pass over the team table as one this build does not have,
// so without this a deployment would come up running on the default
// framework, committing under the sandbox's identity, dispatching into
// every repository rather than the ones it listed, and on whatever model
// its legacy per-framework fields no longer named.
//
//   - agent_framework, agent_git_name, agent_git_email and target_repos
//     are new to grain_config at 24, so the team's value is the value.
//   - repair_template_id and merge_batch_size were on grain_config
//     already. The team's wins where it chose one: base-branch repair
//     asked the team's template before the deployment's, and a team's
//     merge batch size stood in for the deployment's.
//   - claude_model, codex_model, gemini_model, codex_effort and
//     gemini_effort are folded into default_model and model_settings
//     exactly as dispatch folded them on every run
//     (model.Config.WithLegacyFrameworkModels), so they are folded once
//     here instead.
//
// time_zone, name, description, personal, anyone_may_propose,
// onboarding_dismissed, archived and created_at are left behind: only
// the API's team shape showed them, and nothing acted on them.
//
// Another team's row is left in the team file, for the startup guard's
// question about it (retired.go) to refuse the dump. The default team's
// is taken out of it, and the file with it when no other is there, so
// the migration check does not report the row this carried as ignored.
func foldDefaultTeamRow(dir string) error {
	teamPath := filepath.Join(dir, filepath.FromSlash(DumpPath("team")))
	teams, err := readRowsIfPresent(teamPath)
	if err != nil {
		return err
	}
	var team map[string]any
	var others []map[string]any
	for _, row := range teams {
		if fmt.Sprint(row["id"]) == model.DefaultTeamID {
			team = row
		} else {
			others = append(others, row)
		}
	}
	if team == nil {
		return writeLeftBehind(teamPath, others)
	}
	moved := map[string]any{}
	for _, col := range foldedTeamColumns {
		if v, ok := team[col]; ok && chosen(v) {
			moved[col] = v
		}
	}
	legacy := model.LegacyFrameworkModels{
		AgentFramework: dumpText(team["agent_framework"]),
		ClaudeModel:    dumpText(team["claude_model"]),
		CodexModel:     dumpText(team["codex_model"]),
		GeminiModel:    dumpText(team["gemini_model"]),
		CodexEffort:    dumpText(team["codex_effort"]),
		GeminiEffort:   dumpText(team["gemini_effort"]),
	}
	hasLegacy := legacy.ClaudeModel != "" || legacy.CodexModel != "" || legacy.GeminiModel != "" ||
		legacy.CodexEffort != "" || legacy.GeminiEffort != ""
	if len(moved) == 0 && !hasLegacy {
		return writeLeftBehind(teamPath, others)
	}

	configPath := filepath.Join(dir, filepath.FromSlash(DumpPath("grain_config")))
	configs, err := readRowsIfPresent(configPath)
	if err != nil {
		return err
	}
	if len(configs) == 0 {
		// As moveDefaultTeamSettings: a dump with settings on the team's
		// row and no grain_config row is one nobody ran, and making a row
		// up would stand in for every setting it does not mention.
		return fmt.Errorf("the default team's row holds the deployment's settings, but the dump has no grain_config row to move them to")
	}
	for _, cfg := range configs {
		for col, v := range moved {
			cfg[col] = v
		}
		if hasLegacy {
			folded := model.Config{
				DefaultModel:  dumpText(cfg["default_model"]),
				ModelSettings: model.DecodeModelSettings(dumpText(cfg["model_settings"])),
			}.WithLegacyFrameworkModels(legacy)
			cfg["default_model"] = folded.DefaultModel
			cfg["model_settings"] = model.EncodeModelSettings(folded.ModelSettings)
		}
	}
	if err := writeRows(configPath, configs); err != nil {
		return err
	}
	return writeLeftBehind(teamPath, others)
}

// foldedTeamColumns are the team columns schema 24 carried onto
// grain_config, each under its own name there.
var foldedTeamColumns = []string{
	"agent_framework",
	"agent_git_name",
	"agent_git_email",
	"target_repos",
	"repair_template_id",
	"merge_batch_size",
}

// chosen reports whether a dumped value says something: not null, not
// blank text and not a zero.
func chosen(v any) bool {
	if v == nil {
		return false
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	return s != "" && s != "0"
}

// dumpText is a dumped text column's value, "" for null.
func dumpText(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
