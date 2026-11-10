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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// A repository's own `.grain/` declarations are written by whoever can
// merge there, with no grain access of their own, so what one asks for
// that only a person may give (DeclaredGrantNeedsPerson, an admission
// above standard, open network access) is not granted by being written
// down. Task 3965 held every such firing for a person; this is the end
// state (task 3971): a person holding the scope each item needs accepts
// a declaration's set once (DeclarationAcceptance), and its work lands
// approved for as long as what it asks for stays within that set. A
// declaration that widens its set past what was accepted falls back to
// held, and shows as awaiting acceptance until a person accepts the new
// set.
//
// One record per declaration, keyed by what it is: a declared schedule's,
// template's or suite's id (IsRepoDeclaredID), or a CI job's
// CIJobDeclarationID.

// DeclarationKindCIJob is a `.grain/workflows` job's `capabilities:`,
// the one kind of declaration acceptance knows that is not a
// RepoDeclaration row.
const DeclarationKindCIJob DeclarationKind = "ci-job"

// Hold items beside grants: what else a declaration may ask for that only
// a person may accept.
const (
	// DeclaredNetworkOpenItem is a declaration's `network_access: open`:
	// a sandbox that can reach anything, which the deployment's own
	// default may not give.
	DeclaredNetworkOpenItem = "network_access: open"
	// DeclaredAutoApprovedSuiteItem is a declared suite's
	// `require_approval: false` when its items ask for anything else
	// here: the suite files its tasks approved, so their grants are
	// held exactly as a schedule firing's are.
	DeclaredAutoApprovedSuiteItem = "require_approval: false"
)

// declaredAdmissionItem is the hold item for an admission above standard.
func declaredAdmissionItem(adm Admission) string { return "admission: " + string(adm) }

// CIJobDeclarationID is the declaration id of job (a workflow job's
// name, as grain CI reports it) in repo, the key its acceptance is kept
// under. The job's name rather than its file: a job moved between files
// asks for the same thing, and a job renamed is a new declaration.
func CIJobDeclarationID(repo RepoRef, job string) string {
	return "ci:" + repo.String() + ":" + strings.TrimSpace(job)
}

// DeclaredCompositionHold is every item in comp that a repository's
// declaration may not grant on its own: each grant
// DeclaredGrantNeedsPerson, an admission above standard and open network
// access, each once, in that order.
func DeclaredCompositionHold(comp Composition) []string {
	var held []string
	for _, g := range comp.Grants {
		if DeclaredGrantNeedsPerson(g.Capability) && !slices.Contains(held, g.Capability) {
			held = append(held, g.Capability)
		}
	}
	if adm := NormalizeAdmission(comp.Admission); adm.AtLeast(AdmissionBypass) {
		held = append(held, declaredAdmissionItem(adm))
	}
	if comp.NetworkAccess == NetworkOpen {
		held = append(held, DeclaredNetworkOpenItem)
	}
	return held
}

// DeclaredCIJobHold is the capabilities a CI job declares that need a
// person to accept them (DeclaredGrantNeedsPerson), each once.
func DeclaredCIJobHold(capabilities []string) []string {
	var held []string
	for _, c := range capabilities {
		c = strings.TrimSpace(c)
		if DeclaredGrantNeedsPerson(c) && !slices.Contains(held, c) {
			held = append(held, c)
		}
	}
	return held
}

// DeclaredItemScopes is the API scopes a person must hold to accept item:
// a dangerous api grant's own scopes, and capabilities:admin for every
// other item -- a capability server's grant, an admission, open network
// access, a suite filing approved -- since each is what that scope
// governs for a task.
func DeclaredItemScopes(item string) []APIScope {
	if g, ok, err := ParseAPIGrant(item); ok && err == nil && len(g.Scopes) > 0 {
		return slices.Clone(g.Scopes)
	}
	if preset, ok := apiAliasCapabilities[strings.TrimSpace(item)]; ok {
		if g, ok, err := ParseAPIGrant(APIGrantPrefix + preset); ok && err == nil && len(g.Scopes) > 0 {
			return slices.Clone(g.Scopes)
		}
	}
	return []APIScope{"capabilities:admin"}
}

// DeclarationAcceptance is one declaration's record: what it last asked
// for that needs a person, and what a person last accepted.
type DeclarationAcceptance struct {
	ID   string
	Kind DeclarationKind
	Repo RepoRef
	// Requested is the declaration's held items (DeclaredCompositionHold,
	// DeclaredCIJobHold) as of its last firing, sorted, and
	// RequestedCommit the declaration's commit then.
	Requested       []string
	RequestedCommit string
	RequestedAt     time.Time
	// Accepted is the set a person accepted, sorted; empty for none yet.
	Accepted []string
	// AcceptedBy is who accepted it, AcceptedScopes the scopes they held
	// that the set needs (DeclaredItemScopes), and AcceptedCommit the
	// declaration's commit they accepted it at.
	AcceptedBy     string
	AcceptedScopes []string
	AcceptedCommit string
	AcceptedAt     *time.Time
}

// Covers reports whether every one of items is within the accepted set.
func (a *DeclarationAcceptance) Covers(items []string) bool {
	if a == nil || a.AcceptedAt == nil {
		return len(items) == 0
	}
	for _, it := range items {
		if !slices.Contains(a.Accepted, it) {
			return false
		}
	}
	return true
}

// Awaiting reports whether the declaration last asked for more than a
// person has accepted: what the UI shows as awaiting acceptance.
func (a DeclarationAcceptance) Awaiting() bool {
	return len(a.Requested) > 0 && !a.Covers(a.Requested)
}

// Unaccepted is the items of Requested outside Accepted.
func (a DeclarationAcceptance) Unaccepted() []string {
	var out []string
	for _, it := range a.Requested {
		if a.AcceptedAt == nil || !slices.Contains(a.Accepted, it) {
			out = append(out, it)
		}
	}
	return out
}

// DeclarationHold is what a composition from a repository's declaration
// asked for that needs a person (Composition.DeclarationHold): the
// declaration it is accepted under, the items, and whether an acceptance
// covers them.
type DeclarationHold struct {
	ID       string
	Kind     DeclarationKind
	Items    []string
	Accepted bool
	// AcceptedBy is who accepted the set, when Accepted.
	AcceptedBy string
}

// DeclarationAcceptanceNote is the line a firing held for want of an
// acceptance carries in its body: what it asks for, and the two ways a
// person can release it.
func DeclarationAcceptanceNote(id string, items []string, widened bool) string {
	why := "Nobody has accepted that set yet"
	if widened {
		why = "That is more than a person last accepted for it"
	}
	return fmt.Sprintf("\n\n---\n\ngrain: filed for a person's approval rather than approved: this work comes from "+
		"a repository's own declaration (`%s`), which asks for %s. %s. Approve this task to run it once, or "+
		"accept the declaration's set on [Settings → Declarations](/settings/declarations) (it takes a person "+
		"holding the scope each item needs) and its later firings land approved.",
		id, strings.Join(items, ", "), why)
}

// declarationHoldFor resolves the hold of a composition declared as id
// (kind) that asks for items -- Accepted when a person's acceptance covers
// them -- and returns the declaration's record beside it. Reads through q
// so a suite pass can ask inside its transaction.
func declarationHoldFor(ctx context.Context, q querier, id string, kind DeclarationKind, items []string) (*DeclarationHold, *DeclarationAcceptance, error) {
	if len(items) == 0 || id == "" {
		return nil, nil, nil
	}
	acc, err := getDeclarationAcceptance(ctx, q, id)
	if err != nil {
		return nil, nil, err
	}
	hold := &DeclarationHold{ID: id, Kind: kind, Items: items}
	if acc.Covers(items) {
		hold.Accepted, hold.AcceptedBy = true, acc.AcceptedBy
	}
	return hold, acc, nil
}

// applyDeclarationHold files comp unapproved, with a note saying why,
// when its declaration's hold is not accepted; prior is the
// declaration's record, which says whether this is a widening.
func applyDeclarationHold(comp *Composition, hold *DeclarationHold, prior *DeclarationAcceptance) {
	comp.DeclarationHold = hold
	if hold == nil || hold.Accepted || !comp.Approved {
		return
	}
	comp.Approved = false
	comp.Body += DeclarationAcceptanceNote(hold.ID, hold.Items, prior != nil && prior.AcceptedAt != nil)
}

const declarationAcceptanceColumns = "`id`,`kind`,`owner`,`name`,`requested`,`requested_commit`,`requested_at`," +
	"`accepted`,`accepted_by`,`accepted_scopes`,`accepted_commit`,`accepted_at`"

func scanDeclarationAcceptance(scan func(...any) error) (DeclarationAcceptance, error) {
	var a DeclarationAcceptance
	var kind, requested, accepted, scopes string
	var acceptedAt sql.NullTime
	if err := scan(&a.ID, &kind, &a.Repo.Owner, &a.Repo.Name, &requested, &a.RequestedCommit, &a.RequestedAt,
		&accepted, &a.AcceptedBy, &scopes, &a.AcceptedCommit, &acceptedAt); err != nil {
		return a, err
	}
	a.Kind = DeclarationKind(kind)
	a.Repo = RepoFromStored(a.Repo.Owner, a.Repo.Name)
	for _, pair := range []struct {
		text string
		into *[]string
	}{{requested, &a.Requested}, {accepted, &a.Accepted}, {scopes, &a.AcceptedScopes}} {
		if pair.text == "" {
			continue
		}
		if err := json.Unmarshal([]byte(pair.text), pair.into); err != nil {
			return a, fmt.Errorf("declaration acceptance %s: %w", a.ID, err)
		}
	}
	if acceptedAt.Valid {
		t := acceptedAt.Time.UTC()
		a.AcceptedAt = &t
	}
	a.RequestedAt = a.RequestedAt.UTC()
	return a, nil
}

func getDeclarationAcceptance(ctx context.Context, q querier, id string) (*DeclarationAcceptance, error) {
	row := q.QueryRowContext(ctx, "SELECT "+declarationAcceptanceColumns+" FROM `declaration_acceptance` WHERE `id` = ?", id)
	a, err := scanDeclarationAcceptance(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the acceptance of %s: %w", id, err)
	}
	return &a, nil
}

// GetDeclarationAcceptance returns id's record, nil when it has never
// asked for anything a person must accept.
func (s *Store) GetDeclarationAcceptance(ctx context.Context, id string) (*DeclarationAcceptance, error) {
	return getDeclarationAcceptance(ctx, s.db, id)
}

// ListDeclarationAcceptances returns every declaration's record, those
// awaiting acceptance first, then by id.
func (s *Store) ListDeclarationAcceptances(ctx context.Context) ([]DeclarationAcceptance, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+declarationAcceptanceColumns+" FROM `declaration_acceptance` ORDER BY `id`")
	if err != nil {
		return nil, fmt.Errorf("listing declaration acceptances: %w", err)
	}
	defer rows.Close()
	var out []DeclarationAcceptance
	for rows.Next() {
		a, err := scanDeclarationAcceptance(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Awaiting() && !out[j].Awaiting() })
	return out, nil
}

func encodeItems(items []string) string {
	sorted := slices.Clone(items)
	sort.Strings(sorted)
	sorted = slices.Compact(sorted)
	if sorted == nil {
		sorted = []string{}
	}
	b, _ := json.Marshal(sorted)
	return string(b)
}

// noteDeclarationRequest records that id (kind, declared in repo at
// commit) asked for items now: what Settings → Declarations shows and what
// a person accepts. The accepted half of the row is left as it is.
func noteDeclarationRequest(ctx context.Context, q querier, id string, kind DeclarationKind, repo RepoRef,
	commit string, items []string, now time.Time) error {

	if len(items) == 0 || id == "" {
		return nil
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO `declaration_acceptance` (`id`,`kind`,`owner`,`name`,`requested`,`requested_commit`,`requested_at`) "+
			"VALUES (?,?,?,?,?,?,?) ON CONFLICT (`id`) DO UPDATE SET `kind` = excluded.`kind`, `owner` = excluded.`owner`, "+
			"`name` = excluded.`name`, `requested` = excluded.`requested`, `requested_commit` = excluded.`requested_commit`, "+
			"`requested_at` = excluded.`requested_at`",
		id, string(kind), repo.StoredOwner(), repo.Name, encodeItems(items), commit, now.UTC())
	if err != nil {
		return fmt.Errorf("recording what %s asks for: %w", id, err)
	}
	return nil
}

// noteDeclarationHold is noteDeclarationRequest for a composition's hold,
// with the repository and commit read from the declaration's own
// provenance (repo_declaration).
func noteDeclarationHold(ctx context.Context, q querier, hold *DeclarationHold, now time.Time) error {
	if hold == nil {
		return nil
	}
	var repo RepoRef
	var commit string
	err := q.QueryRowContext(ctx, "SELECT `owner`,`name`,`commit` FROM `repo_declaration` WHERE `id` = ?", hold.ID).
		Scan(&repo.Owner, &repo.Name, &commit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading the declaration of %s: %w", hold.ID, err)
	}
	return noteDeclarationRequest(ctx, q, hold.ID, hold.Kind, RepoFromStored(repo.Owner, repo.Name), commit, hold.Items, now)
}

// NoteCIJobDeclaration records what CI job job of repo at commit asks for
// (DeclaredCIJobHold) and reports whether a person's acceptance covers it.
// A job asking for nothing held is accepted without a record.
func (s *Store) NoteCIJobDeclaration(ctx context.Context, repo RepoRef, job, commit string, capabilities []string, now time.Time) (bool, []string, error) {
	items := DeclaredCIJobHold(capabilities)
	if len(items) == 0 {
		return true, nil, nil
	}
	id := CIJobDeclarationID(repo, job)
	accepted := false
	err := s.write(ctx, "note what CI job "+id+" asks for", func(tx *sql.Tx) error {
		if err := noteDeclarationRequest(ctx, tx, id, DeclarationKindCIJob, repo, commit, items, now); err != nil {
			return err
		}
		acc, err := getDeclarationAcceptance(ctx, tx, id)
		if err != nil {
			return err
		}
		accepted = acc.Covers(items)
		return nil
	})
	return accepted, items, err
}

// ErrDeclarationMoved is AcceptDeclaration's refusal when the set the
// person saw is no longer what the declaration asks for.
var ErrDeclarationMoved = errors.New("the declaration's requested set has changed since it was shown")

// AcceptDeclaration records by's acceptance of what id last asked for,
// holding scopes (the scopes that set needs, DeclaredItemScopes, which the
// caller has checked by holds). seen, when not nil, is the set the person
// was shown: an acceptance of anything else is refused with
// ErrDeclarationMoved, so a declaration widened while the page was open
// is not accepted unseen. The commit recorded is the declaration's own as
// of now (repo_declaration), or the one it last asked at.
func (s *Store) AcceptDeclaration(ctx context.Context, id, by string, scopes []string, seen []string, now time.Time) (DeclarationAcceptance, error) {
	var out DeclarationAcceptance
	err := s.write(ctx, "accept declaration "+id, func(tx *sql.Tx) error {
		acc, err := getDeclarationAcceptance(ctx, tx, id)
		if err != nil {
			return err
		}
		if acc == nil {
			return fmt.Errorf("declaration %s has asked for nothing to accept", id)
		}
		if seen != nil && encodeItems(seen) != encodeItems(acc.Requested) {
			return ErrDeclarationMoved
		}
		commit := acc.RequestedCommit
		var current string
		if err := tx.QueryRowContext(ctx, "SELECT `commit` FROM `repo_declaration` WHERE `id` = ?", id).Scan(&current); err == nil && current != "" {
			commit = current
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `declaration_acceptance` SET `accepted` = ?, `accepted_by` = ?, `accepted_scopes` = ?, "+
				"`accepted_commit` = ?, `accepted_at` = ? WHERE `id` = ?",
			encodeItems(acc.Requested), by, encodeItems(scopes), commit, now.UTC(), id); err != nil {
			return fmt.Errorf("recording the acceptance of %s: %w", id, err)
		}
		got, err := getDeclarationAcceptance(ctx, tx, id)
		if err != nil {
			return err
		}
		out = *got
		return nil
	})
	return out, err
}

// RevokeDeclarationAcceptance clears id's accepted set: its next firing
// is held again.
func (s *Store) RevokeDeclarationAcceptance(ctx context.Context, id string) error {
	return s.write(ctx, "revoke the acceptance of "+id, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `declaration_acceptance` SET `accepted` = '', `accepted_by` = '', `accepted_scopes` = '', "+
				"`accepted_commit` = '', `accepted_at` = NULL WHERE `id` = ?", id)
		return err
	})
}

// PersonStandsBehind reports whether a person stands behind task's
// approval for grants, the grants the capability catalogue says only a
// person may give (CapabilityCatalogue.ValidateGrants' RequiresPerson):
// a person approved it themselves, or it is the firing of a schedule an
// operator wrote (a person's standing approval, DeclaredFiringHold's own
// reason), or of a repository's declared schedule or template whose
// accepted set (DeclarationAcceptance) covers grants. orchestrator's
// catalogueHold asks it before dispatch (task 3971).
func (s *Store) PersonStandsBehind(ctx context.Context, task Task, grants []string) (bool, error) {
	if task.Approval == nil {
		return false, nil
	}
	a := task.Approval
	if a.Actor.Kind == PrincipalHuman || (a.OnBehalfOf != nil && a.OnBehalfOf.Kind == PrincipalHuman) {
		return true, nil
	}
	if !a.Actor.Same(SchedulePrincipal) {
		return false, nil
	}
	for _, tag := range task.Tags {
		id, ok := strings.CutPrefix(tag, ScheduleFiringTag(""))
		if !ok || id == "" {
			continue
		}
		declared := id
		if !IsRepoDeclaredID(declared) {
			if !IsRepoDeclaredID(task.TemplateID) {
				return true, nil
			}
			declared = task.TemplateID
		}
		acc, err := s.GetDeclarationAcceptance(ctx, declared)
		if err != nil {
			return false, err
		}
		return acc.coversGrants(grants), nil
	}
	return false, nil
}

// coversGrants is Covers for grants however each is spelled: the
// catalogue names a task's grants as the task stores them, which may not
// be the spelling the declaration was accepted in.
func (a *DeclarationAcceptance) coversGrants(grants []string) bool {
	if a == nil || a.AcceptedAt == nil {
		return len(grants) == 0
	}
	for _, g := range grants {
		key := CanonicalGrantKey(g)
		if !slices.ContainsFunc(a.Accepted, func(it string) bool { return CanonicalGrantKey(it) == key }) {
			return false
		}
	}
	return true
}
