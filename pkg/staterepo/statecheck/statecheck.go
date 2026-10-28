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

// Package statecheck is what `grain state check` asks of a state
// repository's dump once staterepo.Check has loaded it: questions about
// what the rows mean -- custom field declarations grain cannot offer,
// values nothing can hold, values a declaration has moved on from,
// settings a daemon cannot run on -- which need pkg/model, and so live
// beside pkg/staterepo rather than in it.
//
// Each check reads the database the dump was imported into (ScratchStore)
// and returns either an error, for what fails the check, or one warning
// line per finding, for the caller to print.
package statecheck

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// CustomFields is the one thing this command asks of a dump beyond
// "does it load": that every custom field declared in it is one grain
// could actually offer (model.CustomField.Validate).
//
// It is here rather than in staterepo.Check because it is a question
// about what a row *means*, and that package deliberately knows nothing
// about pkg/model -- it takes even the schema version as a parameter.
// This package imports both.
//
// It is worth asking at all because these declarations are the one
// settings table written by hand, by an agent, in a pull request, with
// no form and no Put path to validate them: an enum with no options or
// a type nobody has heard of imports perfectly happily and then renders
// as a field nobody can fill in. Failing the pull request is the whole
// point of this step.
func CustomFields(ctx context.Context, db *sql.DB) error {
	fields, err := model.New(db).ListCustomFields(ctx)
	if err != nil {
		return fmt.Errorf("reading the custom fields this dump declares: %w", err)
	}
	var bad []string
	for _, f := range fields {
		if err := f.Validate(); err != nil {
			bad = append(bad, err.Error())
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("tables/custom_field.json declares %d field(s) grain cannot offer:\n  %s",
		len(bad), strings.Join(bad, "\n  "))
}

// UnusableCustomFieldValues is one warning per schedule or template in
// this dump carrying values (grain/task-105) no declaration in it can
// hold -- a name nothing declares, or a value its own declaration would
// refuse.
//
// A warning rather than a failure, deliberately, because it is the same
// question at review time that a firing answers at three in the morning
// and gives the same answer to: the value is dropped and the task is
// filed (model.FilterCustomFieldValues). Failing here would instead make
// retiring a declaration impossible without hunting down every schedule
// that ever named it, in the same pull request, which is exactly the
// coupling that rule exists to avoid.
//
// It is worth saying at all for CustomFields' own reason: these
// tables are written by hand, in a pull request, with no form and no Put
// path to catch a typo -- and a value silently dropped from every firing
// is a field a human keeps expecting to see and never does. Here it is
// one line in the check that pull request already runs.
func UnusableCustomFieldValues(ctx context.Context, db *sql.DB) ([]string, error) {
	store := model.New(db)
	fields, err := store.ListCustomFields(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the custom fields this dump declares: %w", err)
	}
	schedules, err := store.ListSchedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the schedules this dump carries: %w", err)
	}
	templates, err := store.ListTemplates(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the templates this dump carries: %w", err)
	}
	var out []string
	warn := func(what string, values map[string][]string) {
		_, dropped := model.FilterCustomFieldValues(fields, values)
		if len(dropped) == 0 {
			return
		}
		out = append(out, fmt.Sprintf("%s sets %s, which no declared field can hold -- "+
			"every task it files will be filed without them",
			what, strings.Join(dropped, ", ")))
	}
	for _, s := range schedules {
		warn("schedule "+s.ID, s.CustomFields)
	}
	for _, t := range templates {
		warn("template "+t.ID, t.CustomFields)
	}
	return out, nil
}

// StrandedCustomFieldValues is one warning per field whose declaration
// in this dump has moved on from values its tasks are still carrying
// (grain/task-201): how many tasks carry each retired value, and which
// tasks they are.
//
// This is the moment the count is worth having. Retiring an enum option
// or taking `multiple` off a field is a merged pull request against
// tables/custom_field.json, and until it is merged nobody can say what
// it would cost: the values stay on the tasks (model.
// NormalizeCustomFieldValuesFrom on why nothing rewrites them), shown
// as leftovers on every form that renders one, until somebody edits
// each of them off. "Three tasks carry this and here they are" is what
// the decision wants, said to whoever is reviewing the diff rather than
// to whoever opens one of those tasks a month later.
//
// A warning rather than a failure, for UnusableCustomFieldValues' own
// reason and one more: nothing is broken -- grandfathering is the
// designed behaviour, and a deployment may perfectly well retire an
// option and leave the tasks that had it alone -- and failing here
// would make retiring one impossible without clearing every task that
// carries it in the same pull request, which is the coupling
// grandfathering exists to avoid.
//
// It reads the tasks out of the imported dump, so it says something
// only where the dump carries them: a state repository exporting the
// whole database answers in full, and a config-only one (staterepo.
// ExportConfig, which is what a deployment whose repository holds
// settings and memory alone has) carries no task rows and so reports
// nothing. There is no dishonesty in that -- it cannot see tasks it was
// not given -- but it is why the same question is askable of a running
// deployment through its own listing: `grain list -filter
// custom.priority:legacy`.
func StrandedCustomFieldValues(ctx context.Context, db *sql.DB) ([]string, error) {
	store := model.New(db)
	fields, err := store.ListCustomFields(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the custom fields this dump declares: %w", err)
	}
	uses, err := store.LegacyCustomFieldUses(ctx, fields)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, use := range uses {
		// One warning per kind rather than one per field, because the
		// two say different things to whoever is reading the diff: a
		// value that is gone, and a *set* on a field that no longer
		// holds one (where every value is still offered and there being
		// two of them is what is not).
		if len(use.Values) > 0 {
			var what []string
			carrying := map[string]bool{}
			for _, v := range use.Values {
				what = append(what, fmt.Sprintf("%q on %s", v.Value, strandedTasks(v.Tasks)))
				for _, id := range v.Tasks {
					carrying[id] = true
				}
			}
			if use.Declared {
				out = append(out, fmt.Sprintf(
					"%s %s a %q value this dump's declaration no longer offers, and keep it until "+
						"somebody edits it off: %s",
					tasksSaid(len(carrying)), carrySaid(len(carrying)), use.Field, strings.Join(what, "; ")))
			} else {
				out = append(out, fmt.Sprintf(
					"%s %s %s for %q, which nothing in this dump declares -- so nothing can "+
						"render, offer or explain one: %s",
					tasksSaid(len(carrying)), carrySaid(len(carrying)), valuesSaid(len(carrying)),
					use.Field, strings.Join(what, "; ")))
			}
		}
		if len(use.Sets) > 0 {
			out = append(out, fmt.Sprintf(
				"%s %s more than one %q value, which this dump's declaration no longer holds: %s -- "+
					"the set stays until it is pared down to one",
				tasksSaid(len(use.Sets)), carrySaid(len(use.Sets)), use.Field, taskIDList(use.Sets)))
		}
	}
	return out, nil
}

// tasksSaid is "3 tasks", "1 task" -- the number the decision turns on.
func tasksSaid(n int) string {
	if n == 1 {
		return "1 task"
	}
	return fmt.Sprintf("%d tasks", n)
}

// valuesSaid agrees the noun with it, the way carrySaid agrees the verb.
func valuesSaid(n int) string {
	if n == 1 {
		return "a value"
	}
	return "values"
}

// carrySaid agrees the verb with it, since these lines are read in a
// CI log by somebody deciding something.
func carrySaid(n int) string {
	if n == 1 {
		return "carries"
	}
	return "carry"
}

// strandedTasks is a count and the tasks themselves.
func strandedTasks(ids []string) string {
	return fmt.Sprintf("%s (%s)", tasksSaid(len(ids)), taskIDList(ids))
}

// taskIDList is the tasks by id, so somebody can go and look at them.
// Capped, since a field retired out from under a whole backlog would
// otherwise print the backlog.
func taskIDList(ids []string) string {
	shown, more := ids, ""
	if len(shown) > strandedTasksShown {
		shown, more = shown[:strandedTasksShown], fmt.Sprintf(" and %d more", len(ids)-strandedTasksShown)
	}
	return strings.Join(shown, ", ") + more
}

const strandedTasksShown = 10

// UnrunnableSettings is one warning per value in this dump's settings
// row that grain cannot actually run on: a poll interval no reconcile
// loop can be paced by, a worker count nothing can be dispatched into.
//
// It exists because grain_config is a settings table like every other
// one here -- changed by merging a pull request, written as rows rather
// than through the form and the Put path that validate them. The route
// in is ordinary: ui.UpdateSettings refuses a non-positive interval, and
// an import of a hand-edited file never asks it. `grain state check`
// answering "ok: grain can load this" is then perfectly true and
// perfectly useless, because loading it is not the part that goes wrong
// -- the daemon that reads such a row starts, serves its UI, and paces
// its reconcile loop by nothing at all (cmd/grain's runnableStoredConfig,
// which is what stops that being fatal now, and grain/task-316 for what
// it looked like when it was).
//
// A warning rather than a failure, the same call UnusableCustomFieldValues
// above makes and for the same reason: the value does not stop the
// deployment any more, it is quietly replaced by the flag the daemon was
// started with, and what a reviewer needs is to know that the number in
// the diff is not the number that will be in force. Failing the pull
// request would also make a deployment whose row is already like this
// unable to merge any settings change at all until it fixed one it may
// not have written.
//
// It reads the imported throwaway database rather than the file, so a
// dump whose settings row is missing the column entirely gets whatever
// the schema defaults it to -- which is the value the deployment would
// really run with, and the only reading of it worth reporting.
func UnrunnableSettings(ctx context.Context, db *sql.DB) ([]string, error) {
	cfg, err := model.New(db).GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the settings this dump carries: %w", err)
	}
	// No row at all is not a fault: it is a deployment that has never
	// been configured, and the daemon seeds one from its own flags the
	// first time it starts (cmd/grain's loadConfig).
	if cfg == nil {
		return nil, nil
	}
	var out []string
	if cfg.PollInterval <= 0 {
		out = append(out, fmt.Sprintf("the settings row's poll interval is %s, which no reconcile loop can "+
			"be paced by -- a daemon reading this will run at its own -poll-interval instead of at anything "+
			"this repository says", cfg.PollInterval))
	}
	if cfg.MaxWorkers < 1 {
		out = append(out, fmt.Sprintf("the settings row's max workers is %d, which is no run at all -- "+
			"a daemon reading this will run at its own -max-workers instead of at anything this repository says",
			cfg.MaxWorkers))
	}
	if cfg.MaxMergers < 0 {
		out = append(out, fmt.Sprintf("the settings row's max mergers is %d, which is not a number of runs -- "+
			"a daemon reading this will run at its own -max-mergers instead of at anything this repository says",
			cfg.MaxMergers))
	}
	// The budget's two ratios, where a negative number is not a ratio and
	// is read by model.Budget as the zero that leaves a dimension
	// unbounded -- so a settings pull request lowering one past zero
	// would take the bound off rather than tighten it, and nobody would
	// be told. The reserve is deliberately not checked here: whether it
	// is too large is a question about the host this dump would be
	// merged onto, and this runs against a throwaway database (the daemon
	// that does know says so, in runnableStoredConfig).
	if cfg.SandboxCPUOvercommit < 0 {
		out = append(out, fmt.Sprintf("the settings row's sandbox cpu overcommit is %v, which is not a ratio -- "+
			"a daemon reading this will run at its own instead of at anything this repository says",
			cfg.SandboxCPUOvercommit))
	}
	if cfg.SandboxMemoryOvercommit < 0 {
		out = append(out, fmt.Sprintf("the settings row's sandbox memory overcommit is %v, which is not a ratio -- "+
			"a daemon reading this will run at its own instead of at anything this repository says",
			cfg.SandboxMemoryOvercommit))
	}
	if cfg.SandboxDiskOvercommit < 0 {
		out = append(out, fmt.Sprintf("the settings row's sandbox disk overcommit is %v, which is not a ratio -- "+
			"a daemon reading this will run at its own instead of at anything this repository says",
			cfg.SandboxDiskOvercommit))
	}
	if err := cfg.SandboxOvercommit.Validate(); err != nil {
		out = append(out, fmt.Sprintf("the settings row's sandbox_overcommit is not usable (%v) -- "+
			"a daemon reading this will hold its overcommit ratios fixed", err))
	}
	if cfg.SandboxBackend != "" && !model.ValidSandboxBackend(cfg.SandboxBackend) {
		out = append(out, fmt.Sprintf("the settings row's sandbox backend is %q, which grain does not know -- "+
			"a daemon reading this will keep using the backend selected by its startup flags", cfg.SandboxBackend))
	}
	for _, entry := range cfg.K8sNodeSelector {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			out = append(out, fmt.Sprintf("the settings row's k8s_node_selector entry %q is not a \"key=value\" pair -- "+
				"a daemon reading this will run at its own -k8s-node-selector instead of at anything this repository says", entry))
		}
	}
	out = append(out, mcpServerWarnings(cfg.MCPServers)...)
	return out, nil
}

// mcpServerWarnings checks the settings row's capability servers
// (model.Config.MCPServers): an entry protocol.ServerConfig.Validate
// refuses is one the daemon will not attach, and an env or headers value
// that looks like a credential but is not a $secret:<name> reference is
// one this repository is carrying in cleartext (docs/capabilities-spec.md
// §3.1.2). Neither value is ever repeated in the warning.
func mcpServerWarnings(servers map[string]protocol.ServerConfig) []string {
	ids := make([]string, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []string
	for _, id := range ids {
		sc := servers[id]
		if err := sc.Validate(id); err != nil {
			out = append(out, fmt.Sprintf("the settings row's capability server %s is not valid (%v) -- "+
				"a daemon reading this will refuse to attach it", id, err))
		}
		for _, kv := range []struct {
			kind string
			m    map[string]string
		}{{"env", sc.Env}, {"header", sc.Headers}} {
			names := make([]string, 0, len(kv.m))
			for name := range kv.m {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if looksLikeCredential(name) && !strings.Contains(kv.m[name], "$secret:") {
					out = append(out, fmt.Sprintf("the settings row's capability server %s has %s %s in plain text -- "+
						"a credential belongs in the secrets store, referenced as $secret:<name>", id, kv.kind, name))
				}
			}
		}
	}
	return out
}

func looksLikeCredential(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"authorization", "token", "secret", "password", "passwd", "api_key", "apikey", "api-key", "private_key", "credential"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return strings.HasSuffix(n, "_key") || strings.HasSuffix(n, "-key")
}

// ScratchStore is an empty SQLite database at this build's schema, in a
// temporary directory, for staterepo.Check to import a dump into and the
// checks here to read back. The cleanup closes it and removes the
// directory.
func ScratchStore() (*sql.DB, func(), error) {
	dir, err := os.MkdirTemp("", "grain-state-check-")
	if err != nil {
		return nil, nil, err
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	cleanup := func() {
		db.Close()
		os.RemoveAll(dir)
	}
	if err := model.New(db).Init(context.Background()); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("applying schema: %w", err)
	}
	return db, cleanup, nil
}
