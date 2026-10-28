package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The startup guard of the single-tenant reset (docs/single-tenant-reset.md,
// stage 5).
//
// This build serves one tenant. A data dir a multi-tenant deployment wrote
// -- accounts, team members, several teams, an allowlist, cross-team
// approvals, capabilities granted to teams -- is one it cannot serve, and
// there is no upgrade path for it: the owner retired that deployment
// rather than migrating it. So the store refuses such a data dir before
// anything reads a row out of it, and says what to do instead.
//
// It refuses and does nothing else. It never drops, rewrites or
// reinterprets a row, so the build the data dir came from can still run on
// it, and an operator who wants the rows has them exactly as they were.
//
// It runs in three places, all of them before the data is served:
//
//   - Store.Init, so a daemon started on such a data dir stops there;
//   - staterepo.CheckMigration, which is `grain state check -migrate` and
//     the rehearsal the Upgrade button and scripts/setup.sh run in the new
//     build before cutting over (pkg/upgrade/schema.go). An upgrade onto
//     this build therefore fails there, with the deployment still on the
//     build it runs. SchemaVersion's bump is what makes that rehearsal run
//     at all;
//   - the daemon's start, after the rows a schema upgrade or the state
//     repository brought in have been imported, since Init ran on the
//     empty store before they arrived (pkg/daemon's run).
//
// What it does not count. task_read, task_write and the template and
// schedule equivalents are a task's read and write targets, and
// task_grant, template_grant and schedule_grant are the capabilities a
// task, template or schedule holds: every single-tenant deployment has
// them, so they are not the access lists and team grants this refuses.
// One team is not evidence either: it is the default team, whose row held
// the deployment's settings until schema 24. Nor are that team's
// members, which every build before the single-tenant reset's
// 3e-iv-b2c-iii wrote on a single-tenant daemon -- putTeam seeded them
// from admin_accounts whenever it wrote the default team (task 4553). A
// second team, and another team's members, are dump checks now (below).
//
// It also refuses a store stamped with a schema version from before the
// reset (BaselineSchemaVersion), whatever rows it holds: every such store
// was written by a build that could serve several tenants, and this
// build carries none of the migrations that would read it. That is what
// keeps a multi-tenant data dir whose rows this no longer counts -- the
// access list, cross-team approvals and team grants, whose schema went
// in stage 6 -- from reaching a build that has no tables to count them
// in. That refusal is ErrBeforeBaseline, not ErrMultiTenantData, and its
// words do not say multi-tenant: a single-tenant install from before the
// reset is stamped the same way and refused the same way, and its owner
// is told only what is true of it. staterepo refuses a dump stamped below
// the baseline in the same words, naming the state repository
// (staterepo's Migratable).
//
// Nothing multi-tenant is counted in a store any more: schema 20 dropped
// the `user` table, 21 `team_user` and 24 `team`, so a store at this
// build's schema has nowhere to keep sign-in accounts, team members or a
// second team, and a store at an older one is refused as old before it
// is read. Their rows only reach this build through a state-repository
// dump, and staterepo asks the dump instead (pkg/staterepo/retired.go),
// in the words of MultiTenantRefusal.

// ErrMultiTenantData is returned when a data dir, or a state repository,
// holds rows only a multi-tenant deployment writes. Nothing in it has been
// changed when it is returned.
var ErrMultiTenantData = errors.New("multi-tenant data")

// ErrBeforeBaseline is returned when a data dir, or a state repository, is
// stamped with a schema from before the single-tenant baseline. It says
// nothing about whether the rows are a multi-tenant deployment's: a
// single-tenant install from before the reset is refused with it too,
// since grain has no migration from that schema either way. Nothing in it
// has been changed when it is returned.
var ErrBeforeBaseline = errors.New("schema from before the single-tenant baseline")

// A Holder is what the guard was asked about, in the words its refusal
// names it by.
type Holder string

const (
	// DataDir is a daemon's store: Store.Init, and the daemon's start.
	DataDir Holder = "data dir"
	// StateRepository is a dump of one, which is what `grain state check
	// -migrate`, an upgrade's rehearsal and staterepo.Import read.
	StateRepository Holder = "state repository"
)

// refusal is the guard's error: its own words, wrapping the sentinel
// callers ask for with errors.Is.
type refusal struct {
	sentinel error
	msg      string
}

func (r *refusal) Error() string { return r.msg }
func (r *refusal) Unwrap() error { return r.sentinel }

// refuse is the guard's refusal of in, which is what, with what to do
// instead.
func refuse(sentinel error, in Holder, what string) error {
	return &refusal{sentinel, fmt.Sprintf("this build of grain is single-tenant only, and this %s %s. "+
		"It needs a fresh data dir: start this build on an empty one, seed it with "+
		"`grain connections import <file>`, and add its repositories again. Nothing in this %s has "+
		"been changed, so a build from before the single-tenant reset can still run on it",
		in, what, in)}
}

// CheckSingleTenant refuses a store stamped from before the reset, with
// ErrBeforeBaseline. It only reads. A store at this build's schema has no
// table only a multi-tenant deployment fills (this file's header), so
// there is nothing else to count in one.
func (s *Store) CheckSingleTenant(ctx context.Context) error {
	return s.CheckSingleTenantIn(ctx, DataDir)
}

// CheckSingleTenantIn is CheckSingleTenant, its refusal naming the store
// as in: StateRepository for a scratch store a dump was imported into to
// be checked (staterepo.CheckMigration).
func (s *Store) CheckSingleTenantIn(ctx context.Context, in Holder) error {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	return CheckSchemaBaseline(version, in)
}

// MultiTenantRefusal is the guard's refusal of in, with
// ErrMultiTenantData, naming found, each item a count and what it
// counts, or nil when found is empty.
func MultiTenantRefusal(found []string, in Holder) error {
	if len(found) == 0 {
		return nil
	}
	return refuse(ErrMultiTenantData, in, "holds multi-tenant data: "+strings.Join(found, ", "))
}

// CheckSchemaBaseline refuses in, with ErrBeforeBaseline, when version is
// a schema stamped by a build from before the single-tenant reset: one
// above zero (a store or dump nothing has stamped) and below
// BaselineSchemaVersion. It does not say multi-tenant: a single-tenant
// install from before the reset is stamped the same way, and refused for
// the same reason, that grain has no migration from it.
func CheckSchemaBaseline(version int, in Holder) error {
	if version <= 0 || version >= BaselineSchemaVersion {
		return nil
	}
	return refuse(ErrBeforeBaseline, in, fmt.Sprintf(
		"was written by a build from before the single-tenant reset: schema %d, from before the "+
			"single-tenant baseline (schema %d), which grain has no migration from",
		version, BaselineSchemaVersion))
}
