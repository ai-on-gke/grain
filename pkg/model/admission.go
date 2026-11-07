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
	"database/sql/driver"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
)

// Admission is how a task's runs are let in
// (design/design/unified-grants.design.md §4.1): a task setting, stored in
// the `admission` column of task, template and schedule (and as
// `default_admission` on grain_config and repo_config), that replaced the
// retired priority-dispatch and durable grants. The values are ordered and
// each includes the one before it, so `t.Admission >= AdmissionBypass` is
// "admitted the way a console session is" and `t.Admission ==
// AdmissionDurable` is "durable".
//
// It is not the numeric Task.Priority, which orders the queue within an
// admission level and nothing else.
type Admission int

const (
	// AdmissionStandard is ordinary admission: worker and merger counts,
	// the pipeline pause, base-repair parking, the per-connection
	// concurrency limit and CI preemption all apply. The zero value.
	AdmissionStandard Admission = iota
	// AdmissionBypass is admitted outside the worker and merger counts,
	// ordered ahead of mergers and ordinary work, passes a pipeline pause
	// other than an upgrade's own hold, is never parked behind a red base
	// branch's repair, is not held by a connection's concurrency limit and
	// is never preempted by CI. It is still charged to the sandbox budget
	// under the priority policy. What priority-dispatch was.
	AdmissionBypass
	// AdmissionDurable is AdmissionBypass plus no wall-clock deadline and
	// no wait-credit cap (an idle watchdog suspends the run instead),
	// counted under Limits.Durable rather than max_workers, with setup
	// failures suspended rather than counted
	// (design/design/durable-tasks.design.md#DT-1). What durable was.
	AdmissionDurable
)

// String is the stored and wire spelling: "standard", "bypass" or
// "durable".
func (a Admission) String() string {
	switch a {
	case AdmissionBypass:
		return "bypass"
	case AdmissionDurable:
		return "durable"
	default:
		return "standard"
	}
}

// Label is the UI's name for a: Standard, Bypass queue or Durable.
func (a Admission) Label() string {
	switch a {
	case AdmissionBypass:
		return "Bypass queue"
	case AdmissionDurable:
		return "Durable"
	default:
		return "Standard"
	}
}

// ParseAdmission reads a stored or requested admission. The empty string
// is AdmissionStandard.
func ParseAdmission(s string) (Admission, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "standard":
		return AdmissionStandard, nil
	case "bypass":
		return AdmissionBypass, nil
	case "durable":
		return AdmissionDurable, nil
	}
	return AdmissionStandard, fmt.Errorf("admission %q: want standard, bypass or durable", s)
}

// MarshalText writes String, so JSON and YAML carry the word.
func (a Admission) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText is ParseAdmission.
func (a *Admission) UnmarshalText(b []byte) error {
	v, err := ParseAdmission(string(b))
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// Value stores String in the `admission` column.
func (a Admission) Value() (driver.Value, error) { return a.String(), nil }

// Scan reads the `admission` column. A value this build does not know
// reads as standard rather than failing the whole row.
func (a *Admission) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("admission: cannot scan %T", src)
	}
	parsed, err := ParseAdmission(s)
	if err != nil {
		parsed = AdmissionStandard
	}
	*a = parsed
	return nil
}

// PriorityDispatchCapability and DurableCapability are the retired grant
// names admission replaced. Neither is a capability any more: no provider
// offers them and nothing stores them as a grant. They are named only so
// input still carrying them -- POST /api/tasks, -grant, .grain/
// declarations, a grain/config import -- is turned into Admission for one
// release (FoldAdmissionGrants), with a deprecation warning.
const (
	PriorityDispatchCapability = "priority-dispatch"
	DurableCapability          = "durable"
)

// IsRetiredAdmissionGrant reports whether name is one of the two retired
// admission grant names.
func IsRetiredAdmissionGrant(name string) bool {
	name = strings.TrimSpace(name)
	return name == PriorityDispatchCapability || name == DurableCapability
}

// AdmissionOfGrantName is the admission a retired grant name asks for:
// bypass for priority-dispatch, durable for durable, standard otherwise.
func AdmissionOfGrantName(name string) Admission {
	switch strings.TrimSpace(name) {
	case PriorityDispatchCapability:
		return AdmissionBypass
	case DurableCapability:
		return AdmissionDurable
	}
	return AdmissionStandard
}

// FoldAdmissionGrants takes the retired admission grants out of grants
// and returns the rest with the admission they ask for raised onto a (the
// higher of the two wins, so a task holding both is durable). folded is
// whether any were found.
func FoldAdmissionGrants(grants []Grant, a Admission) (rest []Grant, out Admission, folded bool) {
	out = a
	for _, g := range grants {
		if IsRetiredAdmissionGrant(g.Capability) {
			folded = true
			out = max(out, AdmissionOfGrantName(g.Capability))
			continue
		}
		rest = append(rest, g)
	}
	if !folded {
		return grants, a, false
	}
	return rest, out, true
}

// FoldAdmissionNames is FoldAdmissionGrants over a plain list of
// capability names (default capabilities, a console session's list, a
// -grant flag's values).
func FoldAdmissionNames(names []string, a Admission) (rest []string, out Admission, folded bool) {
	out = a
	for _, n := range names {
		if IsRetiredAdmissionGrant(n) {
			folded = true
			out = max(out, AdmissionOfGrantName(n))
			continue
		}
		rest = append(rest, n)
	}
	if !folded {
		return names, a, false
	}
	return rest, out, true
}

// WarnAdmissionGrants logs the one-release deprecation warning for input
// still naming a retired admission grant.
func WarnAdmissionGrants(what, id string, a Admission) {
	log.Printf("deprecated: %s %s named the retired priority-dispatch/durable grant; stored as admission: %s instead "+
		"(design/design/unified-grants.design.md §4.1); the grant names stop being accepted next release", what, id, a)
}

// AdmissionDeprecationWarning is the warning an API response, declaration
// check or CLI prints when its input named a retired admission grant.
func AdmissionDeprecationWarning(a Admission) string {
	return fmt.Sprintf("the priority-dispatch and durable grants are retired: set admission: %s instead "+
		"(accepted this release only)", a)
}

// TaskEventAdmissionChanged records a change to a task's admission: the
// old value, the new one, and who made it.
const TaskEventAdmissionChanged TaskEventKind = "admission-changed"

// TransitionAdmissionChanged is the timeline step for a recorded
// admission change.
const TransitionAdmissionChanged State = "admission_changed"

type admissionChangeKey struct{}

// WithAdmissionChange attaches the actor changing a task's admission to
// ctx, for the event putTask records.
func WithAdmissionChange(ctx context.Context, by Attribution) context.Context {
	return context.WithValue(ctx, admissionChangeKey{}, by)
}

func admissionChangeFrom(ctx context.Context) Attribution {
	if by, ok := ctx.Value(admissionChangeKey{}).(Attribution); ok && (by.Actor.Kind != "" || by.Actor.ID != "") {
		return by
	}
	return Attribution{Actor: GrainPrincipal}
}

// FormatAdmissionChangeDetail formats the old and new admissions for
// TaskEvent.Detail.
func FormatAdmissionChangeDetail(oldAdmission, newAdmission Admission) string {
	return fmt.Sprintf("admission changed from %s to %s", oldAdmission, newAdmission)
}

// ParseAdmissionChangeReason splits a TaskEventAdmissionChanged Reason
// ("old -> new").
func ParseAdmissionChangeReason(reason string) (oldAdmission, newAdmission Admission, ok bool) {
	rawOld, rawNew, cut := strings.Cut(reason, " -> ")
	if !cut {
		return AdmissionStandard, AdmissionStandard, false
	}
	var err1, err2 error
	oldAdmission, err1 = ParseAdmission(rawOld)
	newAdmission, err2 = ParseAdmission(rawNew)
	if err1 != nil || err2 != nil || strings.TrimSpace(rawOld) == "" || strings.TrimSpace(rawNew) == "" {
		return AdmissionStandard, AdmissionStandard, false
	}
	return oldAdmission, newAdmission, true
}

// guardAdmission is putTask's admission step, run before the row is
// replaced: it folds any retired admission grant still on t into
// t.Admission (never storing it as a grant) and returns the event to
// record when an existing task's admission changes (nil for none, and
// for a new task, which carries what it was filed with).
func guardAdmission(ctx context.Context, tx querier, t *Task) (*TaskEvent, error) {
	if t.IsConsoleSession() {
		t.Admission = AdmissionDurable
	}
	if rest, a, folded := FoldAdmissionGrants(t.Grants, t.Admission); folded {
		t.Grants, t.Admission = rest, a
		WarnAdmissionGrants("task", t.ID, a)
	}
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT `admission` FROM `task` WHERE `id` = ?", t.ID).Scan(&stored)
	if err != nil {
		// No row (a new task) or nothing to compare against.
		return nil, nil
	}
	var before Admission
	_ = before.Scan(stored)
	if before == t.Admission {
		return nil, nil
	}
	who := admissionChangeFrom(ctx)
	return &TaskEvent{
		TaskID:     t.ID,
		Kind:       TaskEventAdmissionChanged,
		At:         time.Now().UTC(),
		By:         who.Actor,
		OnBehalfOf: who.OnBehalfOf,
		Reason:     before.String() + " -> " + t.Admission.String(),
		Detail:     FormatAdmissionChangeDetail(before, t.Admission),
	}, nil
}

// EffectiveDefaultAdmission returns the default admission a newly filed
// task against target starts with: the higher of Config.DefaultAdmission
// and RepoConfig.DefaultAdmission (plus any retired admission grant name
// still in DefaultCapabilities before a store write cleaned it).
func (s *Store) EffectiveDefaultAdmission(ctx context.Context, target *RepoRef) (Admission, error) {
	var out Admission
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return AdmissionStandard, err
	}
	if cfg != nil {
		_, a, _ := FoldAdmissionNames(cfg.DefaultCapabilities, cfg.DefaultAdmission)
		out = max(out, a)
	}
	if target != nil {
		repoCfg, err := s.GetRepoConfig(ctx, *target)
		if err != nil {
			return AdmissionStandard, err
		}
		if repoCfg != nil {
			_, a, _ := FoldAdmissionNames(repoCfg.DefaultCapabilities, repoCfg.DefaultAdmission)
			out = max(out, a)
		}
	}
	return out, nil
}

// admissionNames lists the values in order, for help text and the UI.
var admissionNames = []string{"standard", "bypass", "durable"}

// AdmissionNames is admissionNames, copied.
func AdmissionNames() []string { return slices.Clone(admissionNames) }
