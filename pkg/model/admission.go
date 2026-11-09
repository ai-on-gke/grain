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
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Admission says which dispatch pool a task runs in and whether it bypasses
// worker saturation, replacing the retired in-process priority-dispatch and
// durable grants (design/design/unified-grants.design.md §4.1).
//
// The values form a strict total order: standard < bypass < durable. Note that
// their spellings do NOT sort in that order lexicographically ("bypass" <
// "durable" < "standard"), so callers must compare with Rank, AtLeast or
// MaxAdmission rather than Go's string comparison operators.
type Admission string

const (
	// AdmissionStandard is ordinary queued work, bounded by Limits.MaxWorkers.
	AdmissionStandard Admission = "standard"
	// AdmissionBypass admits a task past worker saturation (and pipeline
	// pause / base-branch CI hold where HasPriorityDispatch applies) without
	// placing it in the durable pool. It replaces priority-dispatch.
	AdmissionBypass Admission = "bypass"
	// AdmissionDurable places a task in Limits.Durable, exempts its runs from
	// the ordinary wall-clock timeout in favour of the per-turn tool budget,
	// and includes AdmissionBypass. It replaces durable.
	AdmissionDurable Admission = "durable"
)

// Rank returns the ordered rank of a: 0 for "" or standard, 1 for bypass,
// 2 for durable, and -1 for an unrecognised value.
func (a Admission) Rank() int {
	switch Admission(strings.TrimSpace(string(a))) {
	case "", AdmissionStandard:
		return 0
	case AdmissionBypass:
		return 1
	case AdmissionDurable:
		return 2
	default:
		return -1
	}
}

// AtLeast reports whether a is at least as privileged as other in the
// standard < bypass < durable order.
func (a Admission) AtLeast(other Admission) bool {
	ar, or := a.Rank(), other.Rank()
	if ar < 0 || or < 0 {
		return false
	}
	return ar >= or
}

// NormalizeAdmission canonicalises an empty admission to AdmissionStandard.
func NormalizeAdmission(a Admission) Admission {
	s := strings.TrimSpace(string(a))
	if s == "" {
		return AdmissionStandard
	}
	return Admission(s)
}

// ValidAdmission reports whether a is one of standard, bypass or durable.
func ValidAdmission(a Admission) bool {
	switch a {
	case AdmissionStandard, AdmissionBypass, AdmissionDurable:
		return true
	default:
		return false
	}
}

// ValidOptionalAdmission reports whether a is unset ("") or a ValidAdmission.
func ValidOptionalAdmission(a Admission) bool {
	return a == "" || ValidAdmission(a)
}

// ParseAdmission parses a task's admission setting, treating "" as standard.
func ParseAdmission(raw string) (Admission, error) {
	s := Admission(strings.TrimSpace(raw))
	if s == "" {
		return AdmissionStandard, nil
	}
	if !ValidAdmission(s) {
		return "", fmt.Errorf("unknown admission %q: want standard, bypass or durable", raw)
	}
	return s, nil
}

// ParseOptionalAdmission parses an optional admission setting (for templates,
// schedules and defaults), keeping "" as unset.
func ParseOptionalAdmission(raw string) (Admission, error) {
	s := Admission(strings.TrimSpace(raw))
	if s == "" {
		return "", nil
	}
	if !ValidAdmission(s) {
		return "", fmt.Errorf("unknown admission %q: want standard, bypass or durable", raw)
	}
	return s, nil
}

// MaxAdmission returns the higher of a and b in the standard < bypass < durable
// order, preserving an explicit "standard" over an unset "".
func MaxAdmission(a, b Admission) Admission {
	a = Admission(strings.TrimSpace(string(a)))
	b = Admission(strings.TrimSpace(string(b)))
	ar, br := a.Rank(), b.Rank()
	if br > ar {
		return b
	}
	if ar > br {
		return a
	}
	if a != "" {
		return a
	}
	return b
}

// LegacyAdmissionAlias maps the retired priority-dispatch and durable grant
// names to their replacement Admission values for the one-release input alias
// window.
func LegacyAdmissionAlias(name string) (Admission, bool) {
	switch strings.TrimSpace(name) {
	case PriorityDispatchCapability:
		return AdmissionBypass, true
	case DurableCapability:
		return AdmissionDurable, true
	default:
		return "", false
	}
}

// ExtractLegacyAdmissionStrings strips bare priority-dispatch and durable
// names from caps, returning the remaining capability strings, the highest
// Admission named (or "" if neither appeared), and the distinct legacy names
// seen in order. A non-nil empty input stays non-nil.
func ExtractLegacyAdmissionStrings(caps []string) (kept []string, mapped Admission, aliases []string) {
	if caps != nil {
		kept = make([]string, 0, len(caps))
	}
	for _, raw := range caps {
		if adm, ok := LegacyAdmissionAlias(raw); ok {
			mapped = MaxAdmission(mapped, adm)
			name := strings.TrimSpace(raw)
			if !slices.Contains(aliases, name) {
				aliases = append(aliases, name)
			}
			continue
		}
		kept = append(kept, raw)
	}
	return kept, mapped, aliases
}

// ExtractLegacyAdmissionGrants strips bare priority-dispatch and durable
// grants from grants, returning the remaining grants, the highest Admission
// named (or "" if neither appeared), and the distinct legacy names seen.
func ExtractLegacyAdmissionGrants(grants []Grant) (kept []Grant, mapped Admission, aliases []string) {
	if grants != nil {
		kept = make([]Grant, 0, len(grants))
	}
	for _, g := range grants {
		if adm, ok := LegacyAdmissionAlias(g.Capability); ok {
			mapped = MaxAdmission(mapped, adm)
			name := strings.TrimSpace(g.Capability)
			if !slices.Contains(aliases, name) {
				aliases = append(aliases, name)
			}
			continue
		}
		kept = append(kept, g)
	}
	return kept, mapped, aliases
}

const (
	// TaskEventAdmissionChanged records a change to an existing task's
	// admission setting with the old value, new value and actor.
	TaskEventAdmissionChanged TaskEventKind = "admission-changed"
	// TransitionAdmissionChanged is MergeTaskEvents' timeline step for
	// TaskEventAdmissionChanged.
	TransitionAdmissionChanged State = "admission_changed"
)

type admissionChangeContextKey struct{}

// WithAdmissionChange attaches the actor changing a task's admission setting
// to ctx so putTask records a TaskEventAdmissionChanged in the same
// transaction.
func WithAdmissionChange(ctx context.Context, by Attribution) context.Context {
	return context.WithValue(ctx, admissionChangeContextKey{}, by)
}

func admissionChangeFrom(ctx context.Context) (Attribution, bool) {
	if v, ok := ctx.Value(admissionChangeContextKey{}).(Attribution); ok {
		return v, true
	}
	if v, ok := ownerChangeFrom(ctx); ok {
		return v, true
	}
	return Attribution{}, false
}

// FormatAdmissionChangeDetail formats the old and new admission values for
// TaskEvent.Detail.
func FormatAdmissionChangeDetail(oldVal, newVal Admission) string {
	return fmt.Sprintf("admission changed from %s to %s", NormalizeAdmission(oldVal), NormalizeAdmission(newVal))
}

// ParseAdmissionChangeReason splits a TaskEventAdmissionChanged Reason
// ("old -> new").
func ParseAdmissionChangeReason(reason string) (oldVal, newVal Admission, ok bool) {
	left, right, ok := strings.Cut(reason, " -> ")
	if !ok {
		return "", "", false
	}
	oldVal = Admission(strings.TrimSpace(left))
	newVal = Admission(strings.TrimSpace(right))
	return oldVal, newVal, ValidAdmission(oldVal) && ValidAdmission(newVal)
}

// guardTaskAdmission validates and normalises t.Admission and returns an audit
// TaskEvent when an existing task's admission changes.
func guardTaskAdmission(ctx context.Context, tx *sql.Tx, existing int, t *Task) (*TaskEvent, error) {
	norm := NormalizeAdmission(t.Admission)
	if !ValidAdmission(norm) {
		return nil, fmt.Errorf("unknown task admission %q: want standard, bypass or durable", t.Admission)
	}
	t.Admission = norm

	if existing <= 0 {
		return nil, nil
	}

	var prevRaw string
	err := tx.QueryRowContext(ctx, "SELECT `admission` FROM `task` WHERE `id` = ?", t.ID).Scan(&prevRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s's admission: %w", t.ID, err)
	}
	prev := NormalizeAdmission(Admission(prevRaw))
	if prev == norm {
		return nil, nil
	}

	who, ok := admissionChangeFrom(ctx)
	if !ok || (who.Actor.Kind == "" && strings.TrimSpace(who.Actor.ID) == "") {
		who = Attribution{Actor: GrainPrincipal}
	}
	return &TaskEvent{
		TaskID:     t.ID,
		Kind:       TaskEventAdmissionChanged,
		At:         time.Now().UTC(),
		By:         who.Actor,
		OnBehalfOf: who.OnBehalfOf,
		Reason:     string(prev) + " -> " + string(norm),
		Detail:     FormatAdmissionChangeDetail(prev, norm),
	}, nil
}
