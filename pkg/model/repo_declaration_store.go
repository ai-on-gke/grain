package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SyncRepoDeclarations makes what repo's `.grain` directory declares at
// commit be exactly what this store holds for repo: every declared
// template, suite and schedule written, and every row this repository
// declared before and no longer does removed.
//
// One transaction, for the reason a suite is written in one: a pass
// halfway through leaves a suite pointing at a template that is not
// there yet, or a schedule pointing at a suite that has just been
// deleted. The whole of one repository's declarations arrives or none of
// it does.
//
// What is preserved across a sync is as much the point as what is
// written. A schedule's NextRunAt and LastRunAt are grain's own record
// of a cadence already running, not something the file says, so re-
// reading a repository whose schedule file has not changed must not
// retime it -- otherwise a repository polled every couple of minutes
// would hold a daily job permanently a day away. They are carried over
// from the row that is already there, and the cadence is re-timed only
// when the file's Recurrence differs from the one the row is running
// (which is exactly what ui.UpdateSchedule does when a human edits one).
// CreatedAt is carried over for the same reason: a declaration created
// last month is not created afresh every time somebody edits its title.
//
// A settings declaration cannot take back a capability the row holds,
// turn publishing back on, or change any other field an operator wrote,
// because there is no longer anything in a settings file that says those
// things: since grain/task-340 a declaration writes two columns of its
// own and the rest of the row is not in its reach at all (RepoSettings,
// and pkg/declschema's RepoSettingsSchema for which settings those are
// and why the others are refused by name). That replaces the check this
// sync used to make against the row before applying one -- a guard on a
// loss that can no longer be expressed.
//
// What a settings declaration does write is returned: the columns of
// repo_config this sync actually changed, in the form
// RepoSettings.Changes renders them, for the caller to log
// (orchestrator.syncDeclarations). The two of them are the prompt every
// agent dispatched to this repository is handed and the command every
// checkout of it runs, and a change to either shows up as an agent
// behaving differently rather than as anything that names the merge that
// did it, so "which sync did this?" is worth being able to answer after
// the fact (grain/task-476). The list is empty for a sync that declares
// no settings, and for one whose file already says what the row says --
// which is every sync after the file's own last edit.
//
// A repository whose files no longer declare anything is a legitimate
// sync with an empty RepoDeclarations, and removes everything it used to
// declare. A repository that could not be read at all is not a sync and
// must not reach here -- see RecordDeclarationFailure, which records the
// failure and leaves the last good commit's rows running.
//
// Declarations in a repo this deployment may not work in are refused, and
// with them the whole sync (checkDeclaredTargets has why).
//
// loc is the deployment's own wall clock, the same one a firing is timed
// against (orchestrator.reconcileSchedule) -- nil is UTC, which is what
// Recurrence.Next means by one.
func (s *Store) SyncRepoDeclarations(ctx context.Context, repo RepoRef, commit string,
	declared RepoDeclarations, now time.Time, loc *time.Location) ([]SettingsChange, error) {
	now = now.UTC()
	var changed []SettingsChange
	err := s.write(ctx, "sync declarations for "+repo.String(), func(tx *sql.Tx) error {
		// write may run this more than once, on a fresh transaction each
		// time, so anything accumulated across it starts again with it.
		changed = nil
		if err := checkDeclaredTargets(ctx, tx, repo, declared); err != nil {
			return err
		}
		cap := len(declared.Templates) + len(declared.Suites) + len(declared.Schedules)
		if declared.Settings != nil {
			cap++
		}
		keep := make(map[string]bool, cap)

		write := func(kind DeclarationKind, id, path string) error {
			keep[id] = true
			_, err := tx.ExecContext(ctx,
				"REPLACE INTO `repo_declaration` (`id`,`kind`,`owner`,`name`,`path`,`commit`,`synced_at`) VALUES (?,?,?,?,?,?,?)",
				id, string(kind), repo.StoredOwner(), repo.Name, path, commit, now)
			return err
		}

		if declared.Settings != nil {
			cfg, err := getRepoConfig(ctx, tx, repo)
			if err != nil {
				return err
			}
			if cfg == nil {
				cfg = &RepoConfig{Repo: repo}
			}
			// The two columns the declaration owns, and nothing else on
			// the row: RepoSettings.ApplyTo has why, and the Declarations
			// switch in particular is not touched here. A sync only
			// happens for a repository whose switch an operator has
			// already turned on, so writing it back would be at best a
			// no-op -- and a file that could turn on the reading of
			// itself is exactly the bootstrap pkg/declschema refuses.
			//
			// What those two columns are about to become is worked out
			// here, against the row this transaction is holding, and
			// handed back for the caller to log.
			changed = declared.Settings.Settings.Changes(*cfg)
			declared.Settings.Settings.ApplyTo(cfg)
			if err := putRepoConfig(ctx, tx, *cfg); err != nil {
				return err
			}
			settingsID := RepoDeclaredID(DeclaredSettings, repo, "settings")
			if err := write(DeclaredSettings, settingsID, declared.Settings.Path); err != nil {
				return err
			}
		}

		for _, file := range declared.Templates {
			t := file.Template
			if existing, err := getTemplate(ctx, tx, t.ID); err != nil {
				return err
			} else if existing != nil {
				t.CreatedAt = existing.CreatedAt
			} else {
				t.CreatedAt = now
			}
			if err := putTemplate(ctx, tx, t); err != nil {
				return err
			}
			if err := write(DeclaredTemplate, t.ID, file.Path); err != nil {
				return err
			}
			if err := putDeclarationPush(ctx, tx, t.ID, file.Push); err != nil {
				return err
			}
		}
		for _, file := range declared.Suites {
			suite := file.Suite
			if existing, err := getSuite(ctx, tx, suite.ID); err != nil {
				return err
			} else if existing != nil {
				suite.CreatedAt = existing.CreatedAt
			} else {
				suite.CreatedAt = now
			}
			if err := putSuite(ctx, tx, suite); err != nil {
				return err
			}
			if err := write(DeclaredSuite, suite.ID, file.Path); err != nil {
				return err
			}
		}
		for _, file := range declared.Schedules {
			sched := file.Schedule
			existing, err := getSchedule(ctx, tx, sched.ID)
			if err != nil {
				return err
			}
			schedLoc := sched.Location()
			if schedLoc == time.UTC && loc != nil {
				schedLoc = loc
			}
			switch {
			case existing == nil:
				sched.CreatedAt = now
				sched.NextRunAt = sched.Recurrence.Next(now, schedLoc)
			default:
				sched.CreatedAt = existing.CreatedAt
				sched.LastRunAt = existing.LastRunAt
				sched.NextRunAt = existing.NextRunAt
				if sched.Recurrence != existing.Recurrence || sched.NextRunAt.IsZero() {
					sched.NextRunAt = sched.Recurrence.Next(now, schedLoc)
				}
			}
			if err := putSchedule(ctx, tx, sched); err != nil {
				return err
			}
			if err := write(DeclaredSchedule, sched.ID, file.Path); err != nil {
				return err
			}
		}

		withdrawn, err := declaredIDs(ctx, tx, repo)
		if err != nil {
			return err
		}
		for _, row := range withdrawn {
			if keep[row.ID] {
				continue
			}
			if row.Kind == DeclaredSettings {
				// A withdrawn settings file takes its two columns back to
				// blank (deleteDeclared), which is as much a change to
				// what an agent dispatched here is handed as writing them
				// was, and deserves the same line in the log.
				cfg, err := getRepoConfig(ctx, tx, repo)
				if err != nil {
					return err
				}
				if cfg != nil {
					changed = RepoSettings{}.Changes(*cfg)
				}
			}
			if err := deleteDeclared(ctx, tx, row); err != nil {
				return err
			}
		}
		if err := putPathTriggers(ctx, tx, repo, commit, declared.Triggers); err != nil {
			return err
		}
		if err := putMergeRules(ctx, tx, repo, commit, declared.MergeRules); err != nil {
			return err
		}
		return recordDeclarationSync(ctx, tx, repo, commit, now, now, "")
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// deleteDeclared removes one withdrawn declaration: the row in whichever
// table it lives in, and its provenance.
//
// A withdrawn schedule is deleted rather than disabled. It is the same
// answer DeleteSchedule gives -- a schedule is a standing declaration
// and there is no history on the row worth keeping -- and the same one
// the state repository gives a deleted settings row: "a deleted row is a
// deleted row". Tasks it already filed are untouched; they are tasks.
func deleteDeclared(ctx context.Context, tx *sql.Tx, row RepoDeclaration) error {
	var table string
	switch row.Kind {
	case DeclaredSettings:
		// A withdrawn settings file takes back the layer it added and
		// leaves the rest of the row exactly as an operator wrote it.
		// Blanking the whole row would make deleting `.grain/settings.yml`
		// -- or merely breaking its parse on a branch that then merges --
		// a way to clear a repository's capabilities, its release secrets
		// and its queue rules, which is the same authority the file was
		// never allowed to write in the first place.
		cfg, err := getRepoConfig(ctx, tx, row.Repo)
		if err != nil {
			return err
		}
		if cfg != nil {
			cfg.DeclaredPromptExtension = ""
			cfg.DeclaredSetupCommand = ""
			cfg.DeclaredPortBranches = nil
			cfg.DeclaredPortTypes = nil
			if err := putRepoConfig(ctx, tx, *cfg); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM `repo_declaration` WHERE `id` = ?", row.ID)
		return err
	case DeclaredTemplate:
		if _, err := tx.ExecContext(ctx, "DELETE FROM `repo_declaration_push` WHERE `id` = ?", row.ID); err != nil {
			return fmt.Errorf("removing withdrawn template %s's push set: %w", row.ID, err)
		}
		table = "template"
	case DeclaredSuite:
		table = "suite"
	case DeclaredSchedule:
		table = "schedule"
	default:
		return fmt.Errorf("unknown declaration kind %q for %s", row.Kind, row.ID)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM `"+table+"` WHERE `id` = ?", row.ID); err != nil {
		return fmt.Errorf("removing withdrawn %s %s: %w", row.Kind, row.ID, err)
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM `repo_declaration` WHERE `id` = ?", row.ID)
	return err
}

func declaredIDs(ctx context.Context, q querier, repo RepoRef) ([]RepoDeclaration, error) {
	var out []RepoDeclaration
	err := each(ctx, q,
		"SELECT `id`,`kind`,`path`,`commit`,`synced_at` FROM `repo_declaration` WHERE `owner` = ? AND `name` = ? ORDER BY `id`",
		[]any{repo.StoredOwner(), repo.Name},
		func(rows *sql.Rows) error {
			row := RepoDeclaration{Repo: repo}
			var kind string
			if err := rows.Scan(&row.ID, &kind, &row.Path, &row.Commit, &row.SyncedAt); err != nil {
				return err
			}
			row.Kind = DeclarationKind(kind)
			out = append(out, row)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing %s's declarations: %w", repo, err)
	}
	if err := hydrateDeclarationPushes(ctx, q, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetRepoDeclaration returns where id was declared, or nil for an id
// nothing declared -- which is every id allocated from a sequence, and
// what pkg/ui reads before refusing an edit.
func (s *Store) GetRepoDeclaration(ctx context.Context, id string) (*RepoDeclaration, error) {
	var row RepoDeclaration
	var kind string
	err := s.db.QueryRowContext(ctx,
		"SELECT `id`,`kind`,`owner`,`name`,`path`,`commit`,`synced_at` FROM `repo_declaration` WHERE `id` = ?", id).
		Scan(&row.ID, &kind, &row.Repo.Owner, &row.Repo.Name, &row.Path, &row.Commit, &row.SyncedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the declaration of %s: %w", id, err)
	}
	row.Repo = RepoFromStored(row.Repo.Owner, row.Repo.Name)
	row.Kind = DeclarationKind(kind)
	if row.Push, err = declarationPushOf(ctx, s.db, row.ID); err != nil {
		return nil, err
	}
	return &row, nil
}

// ListRepoDeclarations returns every declared row's provenance, ordered
// by repository and then by path -- what a listing marks its declared
// entries from with one query rather than one per row.
func (s *Store) ListRepoDeclarations(ctx context.Context) ([]RepoDeclaration, error) {
	var out []RepoDeclaration
	err := each(ctx, s.db,
		"SELECT `id`,`kind`,`owner`,`name`,`path`,`commit`,`synced_at` FROM `repo_declaration` ORDER BY `owner`,`name`,`path`,`id`",
		nil,
		func(rows *sql.Rows) error {
			var row RepoDeclaration
			var kind string
			if err := rows.Scan(&row.ID, &kind, &row.Repo.Owner, &row.Repo.Name, &row.Path,
				&row.Commit, &row.SyncedAt); err != nil {
				return err
			}
			row.Repo = RepoFromStored(row.Repo.Owner, row.Repo.Name)
			row.Kind = DeclarationKind(kind)
			out = append(out, row)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing declarations: %w", err)
	}
	if err := hydrateDeclarationPushes(ctx, s.db, out); err != nil {
		return nil, err
	}
	return out, nil
}

// RepoDeclarationsOf returns just repo's own declarations.
func (s *Store) RepoDeclarationsOf(ctx context.Context, repo RepoRef) ([]RepoDeclaration, error) {
	return declaredIDs(ctx, s.db, repo)
}

// GetRepoDeclarationSync returns grain's record of reading repo's
// declarations, or nil for a repository it has never read.
func (s *Store) GetRepoDeclarationSync(ctx context.Context, repo RepoRef) (*RepoDeclarationSync, error) {
	row := RepoDeclarationSync{Repo: repo}
	var syncedAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `commit`,`checked_at`,`synced_at`,`last_error` FROM `repo_declaration_sync` WHERE `owner` = ? AND `name` = ?",
		repo.StoredOwner(), repo.Name).Scan(&row.Commit, &row.CheckedAt, &syncedAt, &row.LastError)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s's declaration sync: %w", repo, err)
	}
	if syncedAt.Valid {
		row.SyncedAt = syncedAt.Time
	}
	return &row, nil
}

// ListRepoDeclarationSyncs returns every repository grain has looked at,
// ordered by repository.
func (s *Store) ListRepoDeclarationSyncs(ctx context.Context) ([]RepoDeclarationSync, error) {
	var out []RepoDeclarationSync
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`commit`,`checked_at`,`synced_at`,`last_error` FROM `repo_declaration_sync` ORDER BY `owner`,`name`",
		nil,
		func(rows *sql.Rows) error {
			var row RepoDeclarationSync
			var syncedAt sql.NullTime
			if err := rows.Scan(&row.Repo.Owner, &row.Repo.Name, &row.Commit, &row.CheckedAt,
				&syncedAt, &row.LastError); err != nil {
				return err
			}
			row.Repo = RepoFromStored(row.Repo.Owner, row.Repo.Name)
			if syncedAt.Valid {
				row.SyncedAt = syncedAt.Time
			}
			out = append(out, row)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing declaration syncs: %w", err)
	}
	return out, nil
}

// RecordDeclarationCheck notes that repo was looked at and had nothing
// new to say: its tip is still the commit whose declarations are live.
// It is the ordinary outcome of a poll, and the reason polling is cheap
// (RepoDeclarationSync's own doc comment).
func (s *Store) RecordDeclarationCheck(ctx context.Context, repo RepoRef, now time.Time) error {
	return s.write(ctx, "record declaration check for "+repo.String(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `repo_declaration_sync` SET `checked_at` = ?, `last_error` = '' WHERE `owner` = ? AND `name` = ?",
			now.UTC(), repo.StoredOwner(), repo.Name)
		return err
	})
}

// RecordDeclarationFailure records that this look at repo could not be
// used, and why, without touching the rows the last good commit left
// running.
//
// Leaving them running is the decision here. A repository whose
// `.grain/schedules/nightly.yml` stops parsing on somebody's branch has
// not asked for its nightly job to be deleted, and a format error that
// silently switched off a deployment's standing work would be a far
// worse failure than one that shows up as an error beside the
// repository. The commit is deliberately not advanced either: the next
// poll sees the same tip, tries again, and keeps saying so until
// somebody fixes the file.
func (s *Store) RecordDeclarationFailure(ctx context.Context, repo RepoRef, reason string, now time.Time) error {
	return s.write(ctx, "record declaration failure for "+repo.String(), func(tx *sql.Tx) error {
		return recordDeclarationSync(ctx, tx, repo, "", now.UTC(), time.Time{}, reason)
	})
}

// recordDeclarationSync writes the sync row, keeping whatever the caller
// does not have an answer for: a failure knows when it happened and what
// went wrong but not a commit, and must not blank the commit whose
// declarations are still live.
func recordDeclarationSync(ctx context.Context, tx *sql.Tx, repo RepoRef,
	commit string, checkedAt, syncedAt time.Time, reason string) error {
	var existingCommit string
	var existingSynced sql.NullTime
	err := tx.QueryRowContext(ctx,
		"SELECT `commit`,`synced_at` FROM `repo_declaration_sync` WHERE `owner` = ? AND `name` = ?",
		repo.StoredOwner(), repo.Name).Scan(&existingCommit, &existingSynced)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if commit == "" {
		commit = existingCommit
	}
	synced := sql.NullTime{Time: syncedAt.UTC(), Valid: !syncedAt.IsZero()}
	if !synced.Valid && existingSynced.Valid {
		synced = existingSynced
	}
	_, err = tx.ExecContext(ctx,
		"REPLACE INTO `repo_declaration_sync` (`owner`,`name`,`commit`,`checked_at`,`synced_at`,`last_error`) VALUES (?,?,?,?,?,?)",
		repo.StoredOwner(), repo.Name, commit, checkedAt.UTC(), synced, reason)
	return err
}

// ForgetRepoDeclarations removes everything repo declared, and grain's
// record of ever having read it -- what happens when an operator turns
// RepoConfig.Declarations off again (orchestrator.reconcileDeclarations).
//
// The switch is what an operator reaches for when a repository's
// standing work has to stop, so it has to actually stop it: leaving the
// rows behind would leave the schedules firing with nothing left to
// explain where they came from or how to edit them.
func (s *Store) ForgetRepoDeclarations(ctx context.Context, repo RepoRef) error {
	return s.write(ctx, "forget declarations for "+repo.String(), func(tx *sql.Tx) error {
		rows, err := declaredIDs(ctx, tx, repo)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := deleteDeclared(ctx, tx, row); err != nil {
				return err
			}
		}
		if err := forgetPathTriggers(ctx, tx, repo); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"DELETE FROM `repo_declaration_sync` WHERE `owner` = ? AND `name` = ?",
			repo.StoredOwner(), repo.Name)
		return err
	})
}

// ForgetRepo removes every record grain stores specifically for repo:
// its repo_config row, its repo_workflow rows, its base_branch_* rows,
// and its declared templates, suites, schedules and sync record.
func (s *Store) ForgetRepo(ctx context.Context, repo RepoRef) error {
	return s.write(ctx, "forget repo "+repo.String(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `repo_config` WHERE `owner` = ? AND `name` = ?",
			repo.StoredOwner(), repo.Name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `repo_workflow` WHERE `owner` = ? AND `name` = ?",
			repo.StoredOwner(), repo.Name); err != nil {
			return err
		}
		for _, table := range []string{"base_branch_ci", "base_branch_release", "base_branch_dispatch"} {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM `"+table+"` WHERE `owner` = ? AND `name` = ?",
				repo.StoredOwner(), repo.Name); err != nil {
				return err
			}
		}
		rows, err := declaredIDs(ctx, tx, repo)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := deleteDeclared(ctx, tx, row); err != nil {
				return err
			}
		}
		if err := forgetPathTriggers(ctx, tx, repo); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"DELETE FROM `repo_declaration_sync` WHERE `owner` = ? AND `name` = ?",
			repo.StoredOwner(), repo.Name)
		return err
	})
}

// DeclaringRepos returns every repository whose declarations grain has
// rows for, whether or not it is still configured to read them -- the
// list reconcileDeclarations walks to find repositories whose switch has
// since been turned off.
func (s *Store) DeclaringRepos(ctx context.Context) ([]RepoRef, error) {
	seen := map[RepoRef]bool{}
	add := func(r RepoRef) { seen[r] = true }
	syncs, err := s.ListRepoDeclarationSyncs(ctx)
	if err != nil {
		return nil, err
	}
	for _, sync := range syncs {
		add(sync.Repo)
	}
	declared, err := s.ListRepoDeclarations(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range declared {
		add(row.Repo)
	}
	out := make([]RepoRef, 0, len(seen))
	for repo := range seen {
		out = append(out, repo)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StoredOwner() != out[j].StoredOwner() {
			return out[i].StoredOwner() < out[j].StoredOwner()
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// checkDeclaredTargets refuses declarations in a repo this deployment
// may not work in: one Config.TargetRepos leaves out (RepoAllowed, the
// same question orchestrator.reconcileTargetRepos asks of every task
// before it is dispatched), and declared templates whose capabilities
// reach such a repository (checkDeclaredRepoAccess).
//
// A `.grain` file is written in a repository every dispatched agent can
// rewrite, and TargetRepos is the bound an operator set on where this
// deployment's work goes: a file in one repository does not get to step
// past it, by declaring standing work in a repository that is off the
// list or by reaching from one into another with `capabilities:
// [repo-read:..., repo-write:...]` (grain/task-1780). The git credential
// is what finally says whether a task may fetch or push there.
//
// The whole sync is refused rather than the one file, the way a file
// that does not parse refuses it (RecordDeclarationFailure): the last good
// commit's declarations stay running and the reason is recorded against
// the repository, where whoever wrote the file can read it.
func checkDeclaredTargets(ctx context.Context, q querier, repo RepoRef, declared RepoDeclarations) error {
	var paths []string
	for _, f := range declared.Templates {
		paths = append(paths, f.Path)
		if err := checkDeclaredRepoAccess(ctx, q, f.Template, f.Path); err != nil {
			return err
		}
	}
	for _, f := range declared.Suites {
		paths = append(paths, f.Path)
	}
	for _, f := range declared.Schedules {
		paths = append(paths, f.Path)
	}
	if len(paths) == 0 {
		return nil
	}
	targets, err := targetReposIn(ctx, q)
	if err != nil {
		return err
	}
	if !RepoAllowed(targets, repo) {
		return fmt.Errorf("%s: this deployment may not work in %s (its target repos are %s), "+
			"so a declaration in %s cannot run", paths[0], repo, strings.Join(targets, ", "), repo)
	}
	return nil
}

// checkDeclaredRepoAccess refuses a declared template whose Reads or
// Writes name a repository this deployment may not work in
// (checkDeclaredTargets has why).
func checkDeclaredRepoAccess(ctx context.Context, q querier, tmpl Template, path string) error {
	reach := append([]RepoRef(nil), tmpl.Reads...)
	for _, w := range tmpl.Writes {
		reach = append(reach, w.Repo)
	}
	if len(reach) == 0 {
		return nil
	}
	targets, err := targetReposIn(ctx, q)
	if err != nil {
		return err
	}
	for _, r := range reach {
		if !RepoAllowed(targets, r) {
			return fmt.Errorf("%s: this deployment may not work in %s (its target repos are %s), "+
				"so this template's capabilities cannot reach it", path, r, strings.Join(targets, ", "))
		}
	}
	return nil
}

// putDeclarationPush replaces one declared template's push set
// (RepoDeclaration.Push) with what the file read at this sync says.
func putDeclarationPush(ctx context.Context, tx *sql.Tx, id string, push RefPushSet) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM `repo_declaration_push` WHERE `id` = ?", id); err != nil {
		return fmt.Errorf("replacing %s's push set: %w", id, err)
	}
	for _, r := range push {
		if _, err := tx.ExecContext(ctx,
			"REPLACE INTO `repo_declaration_push` (`id`,`pattern`,`allow_force`,`allow_delete`) VALUES (?,?,?,?)",
			id, r.Pattern, boolInt(r.Force), boolInt(r.Delete)); err != nil {
			return fmt.Errorf("writing %s's push set: %w", id, err)
		}
	}
	return nil
}

// declarationPushOf is one declaration's push set, in pattern order.
func declarationPushOf(ctx context.Context, q querier, id string) (RefPushSet, error) {
	var out RefPushSet
	err := each(ctx, q,
		"SELECT `pattern`,`allow_force`,`allow_delete` FROM `repo_declaration_push` WHERE `id` = ? ORDER BY `pattern`",
		id, func(rows *sql.Rows) error {
			var r RefPush
			if err := rows.Scan(&r.Pattern, &r.Force, &r.Delete); err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading %s's push set: %w", id, err)
	}
	return out, nil
}

// hydrateDeclarationPushes fills Push on every template row of rows.
func hydrateDeclarationPushes(ctx context.Context, q querier, rows []RepoDeclaration) error {
	for i := range rows {
		if rows[i].Kind != DeclaredTemplate {
			continue
		}
		push, err := declarationPushOf(ctx, q, rows[i].ID)
		if err != nil {
			return err
		}
		rows[i].Push = push
	}
	return nil
}
