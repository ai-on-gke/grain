package staterepo

import (
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The startup guard's questions about schema this build no longer has,
// asked of a dump instead of a store (stage 6 of
// docs/single-tenant-reset.md).
//
// Import passes over a table the database does not have, which is what
// lets a dump one build along load at all. For a table that only a
// multi-tenant deployment ever filled, that tolerance would be a hole in
// the guard: a dump from schema 19 holding a multi-tenant deployment's
// rows would import cleanly, those rows left behind on the floor, and
// model.Store.CheckSingleTenant would then find nothing to refuse in the
// store it went into. So importInto asks here first, of the files, and
// refuses in the guard's own words.

// retiredMultiTenantChecks are the guard's questions about the retired
// schema, each one a table, what to call its rows, and which of them
// count. A nil counts counts every row: a single-tenant deployment never
// wrote any.
var retiredMultiTenantChecks = []struct {
	what, table string
	counts      func(row map[string]any) bool
}{
	// Dropped at schema 20.
	{"sign-in accounts (user)", "user", nil},
	// Dropped at schema 21. Not the default team's members: every build
	// before 3e-iv-b2c-iii seeded them from admin_accounts on a
	// single-tenant daemon too (model's single_tenant_guard.go).
	{"members of teams besides the default one (team_user)", "team_user", func(row map[string]any) bool {
		return fmt.Sprint(row["team_id"]) != model.DefaultTeamID
	}},
	// Dropped at schema 23. The default team's rows are the
	// deployment's, and the fixup past 22 carries them into repo_config
	// and gcp_key (team_repo_settings.go); it leaves another team's here
	// to be counted.
	{"repository settings of teams besides the default one (team_repo_config)", "team_repo_config", notDefaultTeamRow},
	{"GCP keys of teams besides the default one (team_gcp_key)", "team_gcp_key", notDefaultTeamRow},
	// Dropped at schema 24, and until then the one question the guard
	// asked of a store (model's single_tenant_guard.go). The default
	// team's row is the deployment's, and the fixup past 23 carries it
	// onto grain_config (default_team_row.go).
	{"teams besides the default one (team)", "team", func(row map[string]any) bool {
		return fmt.Sprint(row["id"]) != model.DefaultTeamID
	}},
}

// notDefaultTeamRow counts a row whose team_id is any team but the
// default one.
func notDefaultTeamRow(row map[string]any) bool {
	return fmt.Sprint(row["team_id"]) != model.DefaultTeamID
}

// checkRetiredMultiTenantRows refuses a dump in dir that holds a
// multi-tenant deployment's rows in the retired schema, with
// model.ErrMultiTenantData, and is nil for every other dump.
func checkRetiredMultiTenantRows(dir string) error {
	var found []string
	for _, c := range retiredMultiTenantChecks {
		rows, present, err := dumpRowsOf(dir, c.table)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		n := 0
		for _, row := range rows {
			if c.counts == nil || c.counts(row) {
				n++
			}
		}
		if n > 0 {
			found = append(found, fmt.Sprintf("%d %s", n, c.what))
		}
	}
	return model.MultiTenantRefusal(found, model.StateRepository)
}
