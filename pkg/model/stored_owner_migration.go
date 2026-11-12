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

package model

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
)

// qualificationOwnerTables are the tables a repository's qualification
// plan is written to, all keyed by (owner, name) and moved together.
var qualificationOwnerTables = []string{
	"qualification_config",
	"qualification_item",
	"qualification_item_depends_on",
}

// migrateStoredOwners is migration 142: it host-qualifies the owner
// column of the qualification plan tables and scheduled_workflow_run,
// which before it were written with a bare Owner even for a repository
// off the default host (grain/task-4335). Every other repo-keyed table
// already holds StoredOwner, which ForgetRepo matches on.
//
// A bare owner says nothing about which host its row was meant for, so a
// row is only moved where something else does:
//
//   - a scheduled_workflow_run row takes the host of its own schedule's
//     target (schedule.target_owner is already host-qualified), when that
//     target is the same owner and name;
//   - any other row takes the host of the one target repo
//     (Config.TargetRepos) spelled with its owner and name, when exactly
//     one is and it is off the default host.
//
// A row neither settles stays on the default host, where every reader
// before this migration already put it. Those that a target repo off the
// default host could also have meant -- two hosts name the same owner
// and name, or no target names them at all on a deployment that has
// targets off the default host -- are logged, so an operator can re-save
// the plan or let the schedule fire again under the right repository.
//
// Idempotent: a host-qualified owner is never looked at again, so the
// state repository import may run it over rows that already moved.
func migrateStoredOwners(ctx context.Context, tx *sql.Tx) error {
	targets, err := targetHostsByRepo(ctx, tx)
	if err != nil {
		return fmt.Errorf("host-qualifying stored owners: %w", err)
	}
	if err := migrateQualificationOwners(ctx, tx, targets); err != nil {
		return fmt.Errorf("host-qualifying qualification plans: %w", err)
	}
	if err := migrateScheduledWorkflowRunOwners(ctx, tx, targets); err != nil {
		return fmt.Errorf("host-qualifying scheduled workflow runs: %w", err)
	}
	return nil
}

// storedOwnerTargets is what the target repo list says about the hosts a
// bare (owner, name) might be on.
type storedOwnerTargets struct {
	// hosts maps owner/name, as written, to every host a target repo
	// spells it on: OffDefaultHost, so "" is the default host.
	hosts map[string][]string
	// offHost is whether any target repo is off the default host at all.
	// Without one there is nothing a bare owner could have meant but the
	// default host, and nothing to report.
	offHost bool
}

func targetHostsByRepo(ctx context.Context, tx *sql.Tx) (storedOwnerTargets, error) {
	out := storedOwnerTargets{hosts: map[string][]string{}}
	raw, err := targetReposIn(ctx, tx)
	if err != nil {
		return out, err
	}
	for _, text := range raw {
		repo, err := ParseRepo(text)
		if err != nil {
			// A pattern or anything else that is not one repository
			// names no host for a row to take.
			continue
		}
		host := repo.OffDefaultHost()
		if host != "" {
			out.offHost = true
		}
		key := repo.Owner + "/" + repo.Name
		if !slices.Contains(out.hosts[key], host) {
			out.hosts[key] = append(out.hosts[key], host)
		}
	}
	return out, nil
}

// resolve returns the host-qualified repository a bare (owner, name)
// row belongs to, or false to leave it where it is -- with the reason to
// report, when there is one.
func (t storedOwnerTargets) resolve(owner, name string) (RepoRef, bool, string) {
	hosts := t.hosts[owner+"/"+name]
	switch {
	case len(hosts) == 1 && hosts[0] != "":
		return RepoRef{Host: hosts[0], Owner: owner, Name: name}, true, ""
	case len(hosts) > 1:
		var spelled []string
		for _, h := range hosts {
			spelled = append(spelled, RepoRef{Host: h, Owner: owner, Name: name}.String())
		}
		sort.Strings(spelled)
		return RepoRef{}, false, "target repos name it on more than one host (" + strings.Join(spelled, ", ") + ")"
	case len(hosts) == 0 && t.offHost:
		return RepoRef{}, false, "no target repo names it, and some target repos are off the default host"
	}
	return RepoRef{}, false, ""
}

func migrateQualificationOwners(ctx context.Context, tx *sql.Tx, targets storedOwnerTargets) error {
	seen := map[[2]string]bool{}
	var repos [][2]string
	for _, table := range qualificationOwnerTables {
		err := each(ctx, tx, "SELECT DISTINCT `owner`,`name` FROM `"+table+"`", nil, func(rows *sql.Rows) error {
			var owner, name string
			if err := rows.Scan(&owner, &name); err != nil {
				return err
			}
			key := [2]string{owner, name}
			if !strings.Contains(owner, "/") && !seen[key] {
				seen[key] = true
				repos = append(repos, key)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("reading %s: %w", table, err)
		}
	}
	sort.Slice(repos, func(i, j int) bool {
		if repos[i][0] != repos[j][0] {
			return repos[i][0] < repos[j][0]
		}
		return repos[i][1] < repos[j][1]
	})

	for _, key := range repos {
		owner, name := key[0], key[1]
		repo, ok, why := targets.resolve(owner, name)
		if !ok {
			if why != "" {
				log.Printf("schema 142: left the qualification plan stored for %s/%s on the default host: %s", owner, name, why)
			}
			continue
		}
		taken, err := storedOwnerRowExists(ctx, tx, "qualification_config", repo.StoredOwner(), name)
		if err != nil {
			return err
		}
		if taken {
			log.Printf("schema 142: left the qualification plan stored for %s/%s on the default host: %s already has a plan of its own", owner, name, repo)
			continue
		}
		for _, table := range qualificationOwnerTables {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `"+table+"` SET `owner` = ? WHERE `owner` = ? AND `name` = ?",
				repo.StoredOwner(), owner, name); err != nil {
				return fmt.Errorf("moving %s rows for %s/%s to %s: %w", table, owner, name, repo, err)
			}
		}
	}
	return nil
}

func migrateScheduledWorkflowRunOwners(ctx context.Context, tx *sql.Tx, targets storedOwnerTargets) error {
	type firing struct {
		scheduleID, owner, name string
		target                  *RepoRef
	}
	var firings []firing
	err := each(ctx, tx,
		"SELECT `r`.`schedule_id`,`r`.`owner`,`r`.`name`,`s`.`target_owner`,`s`.`target_name` "+
			"FROM `scheduled_workflow_run` AS `r` LEFT JOIN `schedule` AS `s` ON `s`.`id` = `r`.`schedule_id` "+
			"ORDER BY `r`.`schedule_id`", nil,
		func(rows *sql.Rows) error {
			var f firing
			var targetOwner, targetName sql.NullString
			if err := rows.Scan(&f.scheduleID, &f.owner, &f.name, &targetOwner, &targetName); err != nil {
				return err
			}
			if strings.Contains(f.owner, "/") {
				return nil
			}
			if targetOwner.Valid && targetName.Valid {
				t := RepoFromStored(targetOwner.String, targetName.String)
				f.target = &t
			}
			firings = append(firings, f)
			return nil
		})
	if err != nil {
		return fmt.Errorf("reading scheduled_workflow_run: %w", err)
	}

	for _, f := range firings {
		var repo RepoRef
		if f.target != nil && f.target.Owner == f.owner && f.target.Name == f.name {
			// The schedule that fired it says which host it is on.
			if f.target.OffDefaultHost() == "" {
				continue
			}
			repo = *f.target
		} else {
			var ok bool
			var why string
			repo, ok, why = targets.resolve(f.owner, f.name)
			if !ok {
				if why != "" {
					log.Printf("schema 142: left schedule %s's workflow firing for %s/%s on the default host: %s",
						f.scheduleID, f.owner, f.name, why)
				}
				continue
			}
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `scheduled_workflow_run` SET `owner` = ? WHERE `schedule_id` = ?",
			repo.StoredOwner(), f.scheduleID); err != nil {
			return fmt.Errorf("moving schedule %s's workflow firing to %s: %w", f.scheduleID, repo, err)
		}
	}
	return nil
}

func storedOwnerRowExists(ctx context.Context, tx *sql.Tx, table, owner, name string) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `"+table+"` WHERE `owner` = ? AND `name` = ?", owner, name).Scan(&n); err != nil {
		return false, fmt.Errorf("reading %s: %w", table, err)
	}
	return n > 0, nil
}

// migrateReleaseOwners is migration 144: it host-qualifies the owner
// column of release, release_candidate and qualification_run, which
// before it were written with a bare Owner even for a repository off the
// default host (grain/task-4489) -- a candidate read back with no host at
// all, so its qualification plan, stored host-qualified since 142, was
// never found, and cutting and promoting it targeted the default host.
//
// A repository's releases move together, onto the host of the one target
// repo (Config.TargetRepos) spelled with its owner and name, when exactly
// one is and it is off the default host -- the rule 142 placed
// qualification plans by, so a moved candidate finds the plan that moved
// with it. A candidate moves with its release, and a qualification run
// with its candidate. Everything else stays on the default host, with
// what 142 logs for the ambiguous; so is a repository whose destination
// already holds releases of its own, since two releases of one name
// cannot both be current there.
//
// Idempotent: a host-qualified owner is never looked at again.
func migrateReleaseOwners(ctx context.Context, tx *sql.Tx) error {
	targets, err := targetHostsByRepo(ctx, tx)
	if err != nil {
		return fmt.Errorf("host-qualifying release owners: %w", err)
	}
	var repos [][2]string
	err = each(ctx, tx, "SELECT DISTINCT `owner`,`repo` FROM `release` ORDER BY `owner`,`repo`", nil, func(rows *sql.Rows) error {
		var owner, name string
		if err := rows.Scan(&owner, &name); err != nil {
			return err
		}
		if !strings.Contains(owner, "/") {
			repos = append(repos, [2]string{owner, name})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("host-qualifying release owners: reading release: %w", err)
	}

	for _, key := range repos {
		owner, name := key[0], key[1]
		repo, ok, why := targets.resolve(owner, name)
		if !ok {
			if why != "" {
				log.Printf("schema 144: left the releases of %s/%s on the default host: %s", owner, name, why)
			}
			continue
		}
		stored := repo.StoredOwner()
		var n int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `release` WHERE `owner` = ? AND `repo` = ?", stored, name).Scan(&n); err != nil {
			return fmt.Errorf("host-qualifying release owners: reading release: %w", err)
		}
		if n > 0 {
			log.Printf("schema 144: left the releases of %s/%s on the default host: %s already has releases of its own", owner, name, repo)
			continue
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `release` SET `owner` = ? WHERE `owner` = ? AND `repo` = ?",
			stored, owner, name); err != nil {
			return fmt.Errorf("moving the releases of %s/%s to %s: %w", owner, name, repo, err)
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `release_candidate` SET `owner` = ? WHERE `owner` = ? AND `repo` = ? "+
				"AND `release_id` IN (SELECT `id` FROM `release` WHERE `owner` = ? AND `repo` = ?)",
			stored, owner, name, stored, name); err != nil {
			return fmt.Errorf("moving the release candidates of %s/%s to %s: %w", owner, name, repo, err)
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `qualification_run` SET `owner` = ? WHERE `owner` = ? AND `name` = ? "+
				"AND `candidate_id` IN (SELECT `id` FROM `release_candidate` WHERE `owner` = ? AND `repo` = ?)",
			stored, owner, name, stored, name); err != nil {
			return fmt.Errorf("moving the qualification runs of %s/%s to %s: %w", owner, name, repo, err)
		}
	}
	return nil
}
