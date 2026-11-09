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

// ConsoleCapability is the bare name of the retired in-process console
// capability, kept as a one-release input-only alias for HostingConsole
// (design/design/unified-grants.design.md §4.1).
const ConsoleCapability = "console"

// Hosting says how a task's run is driven at dispatch, replacing the retired
// in-process console grant (design/design/unified-grants.design.md §4.1).
//
// Hosting stays separate from Admission (which says when a task is let in) and
// from Task.Interactive (which marks a task filed as a live chat and changes
// nothing about how its run is driven).
type Hosting string

const (
	// HostingUnattended hands the agent CLI a prompt and reads its answer back.
	HostingUnattended Hosting = "unattended"
	// HostingConsole starts the agent CLI on a pseudo-terminal in the UI
	// (pkg/console) for somebody to type into. It replaces the console grant.
	HostingConsole Hosting = "console"
)

// Rank returns the ordered rank of h: 0 for "" or unattended, 1 for console,
// and -1 for an unrecognised value.
func (h Hosting) Rank() int {
	switch Hosting(strings.TrimSpace(string(h))) {
	case "", HostingUnattended:
		return 0
	case HostingConsole:
		return 1
	default:
		return -1
	}
}

// NormalizeHosting canonicalises an empty hosting to HostingUnattended.
func NormalizeHosting(h Hosting) Hosting {
	s := strings.TrimSpace(string(h))
	if s == "" {
		return HostingUnattended
	}
	return Hosting(s)
}

// ValidHosting reports whether h is one of unattended or console.
func ValidHosting(h Hosting) bool {
	switch h {
	case HostingUnattended, HostingConsole:
		return true
	default:
		return false
	}
}

// ValidOptionalHosting reports whether h is unset ("") or a ValidHosting.
func ValidOptionalHosting(h Hosting) bool {
	return h == "" || ValidHosting(h)
}

// ParseHosting parses a task's hosting setting, treating "" as unattended.
func ParseHosting(raw string) (Hosting, error) {
	s := Hosting(strings.TrimSpace(raw))
	if s == "" {
		return HostingUnattended, nil
	}
	if !ValidHosting(s) {
		return "", fmt.Errorf("unknown hosting %q: want unattended or console", raw)
	}
	return s, nil
}

// ParseOptionalHosting parses an optional hosting setting (for templates,
// schedules and defaults), keeping "" as unset.
func ParseOptionalHosting(raw string) (Hosting, error) {
	s := Hosting(strings.TrimSpace(raw))
	if s == "" {
		return "", nil
	}
	if !ValidHosting(s) {
		return "", fmt.Errorf("unknown hosting %q: want unattended or console", raw)
	}
	return s, nil
}

// MaxHosting returns the higher of a and b in the unattended < console order,
// preserving an explicit "unattended" over an unset "". It implements the
// union rule for deployment and repository defaults and legacy capability
// folding.
func MaxHosting(a, b Hosting) Hosting {
	a = Hosting(strings.TrimSpace(string(a)))
	b = Hosting(strings.TrimSpace(string(b)))
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// HasConsole reports whether t is hosted on an interactive pseudo-terminal
// (HostingConsole).
func (t Task) HasConsole() bool {
	return NormalizeHosting(t.Hosting) == HostingConsole
}

// LegacyHostingAlias maps the retired console grant name to its Hosting setting
// value (one-release input compatibility).
func LegacyHostingAlias(name string) (Hosting, bool) {
	switch strings.TrimSpace(name) {
	case ConsoleCapability:
		return HostingConsole, true
	default:
		return "", false
	}
}

// ExtractLegacyHostingStrings strips bare console capability names from caps,
// returning the remaining capability strings, the Hosting named (or "" if none
// appeared), and the distinct legacy names seen.
func ExtractLegacyHostingStrings(caps []string) (kept []string, mapped Hosting, aliases []string) {
	if caps != nil {
		kept = make([]string, 0, len(caps))
	}
	for _, raw := range caps {
		if h, ok := LegacyHostingAlias(raw); ok {
			mapped = MaxHosting(mapped, h)
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

// ExtractLegacyHostingGrants strips bare console grants from grants, returning
// the remaining grants, the Hosting named (or "" if none appeared), and the
// distinct legacy names seen.
func ExtractLegacyHostingGrants(grants []Grant) (kept []Grant, mapped Hosting, aliases []string) {
	if grants != nil {
		kept = make([]Grant, 0, len(grants))
	}
	for _, g := range grants {
		if h, ok := LegacyHostingAlias(g.Capability); ok {
			mapped = MaxHosting(mapped, h)
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
	// TaskEventHostingChanged records a change to an existing task's hosting
	// setting with the old value, new value and actor.
	TaskEventHostingChanged TaskEventKind = "hosting-changed"
	// TransitionHostingChanged is MergeTaskEvents' timeline step for
	// TaskEventHostingChanged.
	TransitionHostingChanged State = "hosting_changed"
)

type hostingChangeContextKey struct{}

// WithHostingChange attaches the actor changing a task's hosting setting to ctx
// so putTask records a TaskEventHostingChanged in the same transaction.
func WithHostingChange(ctx context.Context, by Attribution) context.Context {
	return context.WithValue(ctx, hostingChangeContextKey{}, by)
}

func hostingChangeFrom(ctx context.Context) (Attribution, bool) {
	if v, ok := ctx.Value(hostingChangeContextKey{}).(Attribution); ok {
		return v, true
	}
	if v, ok := ownerChangeFrom(ctx); ok {
		return v, true
	}
	return Attribution{}, false
}

// FormatHostingChangeDetail formats the old and new hosting values for
// TaskEvent.Detail.
func FormatHostingChangeDetail(oldVal, newVal Hosting) string {
	return fmt.Sprintf("hosting changed from %s to %s", NormalizeHosting(oldVal), NormalizeHosting(newVal))
}

// ParseHostingChangeReason splits a TaskEventHostingChanged Reason
// ("old -> new").
func ParseHostingChangeReason(reason string) (oldVal, newVal Hosting, ok bool) {
	left, right, ok := strings.Cut(reason, " -> ")
	if !ok {
		return "", "", false
	}
	oldVal = Hosting(strings.TrimSpace(left))
	newVal = Hosting(strings.TrimSpace(right))
	return oldVal, newVal, ValidHosting(oldVal) && ValidHosting(newVal)
}

// guardTaskHosting validates and normalises t.Hosting and returns an audit
// TaskEvent when an existing task's hosting changes.
func guardTaskHosting(ctx context.Context, tx *sql.Tx, existing int, t *Task) (*TaskEvent, error) {
	norm := NormalizeHosting(t.Hosting)
	if !ValidHosting(norm) {
		return nil, fmt.Errorf("unknown task hosting %q: want unattended or console", t.Hosting)
	}
	t.Hosting = norm

	if existing <= 0 {
		return nil, nil
	}

	var prevRaw string
	err := tx.QueryRowContext(ctx, "SELECT `hosting` FROM `task` WHERE `id` = ?", t.ID).Scan(&prevRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s's hosting: %w", t.ID, err)
	}
	prev := NormalizeHosting(Hosting(prevRaw))
	if prev == norm {
		return nil, nil
	}

	who, ok := hostingChangeFrom(ctx)
	if !ok || (who.Actor.Kind == "" && strings.TrimSpace(who.Actor.ID) == "") {
		who = Attribution{Actor: GrainPrincipal}
	}
	return &TaskEvent{
		TaskID:     t.ID,
		Kind:       TaskEventHostingChanged,
		At:         time.Now().UTC(),
		By:         who.Actor,
		OnBehalfOf: who.OnBehalfOf,
		Reason:     string(prev) + " -> " + string(norm),
		Detail:     FormatHostingChangeDetail(prev, norm),
	}, nil
}
