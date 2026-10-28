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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Path-scoped merge rules (grain/task-1659): `.grain/merge-rules.yml`,
// read from the base branch by the declaration sync like every other
// `.grain` file, saying per set of paths which CI system gates the merge,
// which grain CI jobs and GitHub checks have to pass, whether the change
// may merge itself at all, and how long the queue waits on CI before it
// hands the change to a person.
//
// A pull request's effective policy is the strictest combination of the
// file's `defaults` and every rule its changed files match
// (MergeRules.Effective). "Strictest" is per field:
//
//   - ci.mode: both beats either single mode; grain and github together
//     are both. The repository's own CI mode (RepoConfig.EffectiveCIMode)
//     is the floor: a rule can add a CI system, never remove the one the
//     repository already gates on.
//   - ci.grain: the full suite, unless every contributor names a list --
//     only then is the gate narrowed, to the union of the lists. A
//     contributor that names nothing asks for the full suite, so one
//     rule (or the defaults covering an unmatched file) wanting more
//     always wins. Names given beside a full-suite requirement are
//     recorded as required too.
//   - ci.github: the same rule. Every check on the commit gates, as it
//     does today, unless every contributor names a list; named checks
//     must be present and green either way.
//   - auto_merge: never beats allowed.
//   - stall_timeout: the shortest set wins, and none set leaves the
//     deployment's own deadline (forge.CheckStallDeadline).
//
// grain/task-1660 added four more, on the same machinery:
//
//   - batching: `alone` (never batched with another change) beats any
//     size, and otherwise the smallest maximum batch size wins. It caps
//     the batch the change is in, on top of its team's own size
//     (Team.MergeBatchSize).
//   - repairs: the fewest automatic repairs wins (0, 1 or 2; 2 is the
//     queue's own most, and its second is still only given when
//     something changed since the first). 0 sends a failure straight to
//     a person.
//   - flake_recheck: false beats true -- a failing job is not re-run
//     uncached before the change is blamed.
//   - port: false beats true -- a merge is not ported to the paired
//     branch automatically. The task's own opt-out (Task.NoPort) still
//     applies besides.
//
// `defaults` contributes its ci lists only when at least one changed file
// matched no rule (or the file list could not be read in full); every
// other setting it has -- mode, auto_merge, stall_timeout, batching,
// repairs, flake_recheck and port -- applies to every pull request, which
// is what "the PR as a whole" means.

const (
	// MergeRulesPath is where a repository declares its merge rules.
	MergeRulesPath = DeclarationDir + "/merge-rules.yml"
	// MergeRulesAltPath is the same file with the other YAML extension.
	MergeRulesAltPath = DeclarationDir + "/merge-rules.yaml"
	// MergeRulesAliasPath is the name grain/task-1657's plan gave the
	// file, still read so a repository that followed that plan is not
	// silently unruled. Both present is a warning, and merge-rules wins.
	MergeRulesAliasPath = DeclarationDir + "/sensitive.yml"
)

// MergeRulesPaths is every path a merge-rules file may be at, in the
// order they are preferred.
var MergeRulesPaths = []string{MergeRulesPath, MergeRulesAltPath, MergeRulesAliasPath}

// CIGateMode is which CI system gates a merge.
type CIGateMode string

const (
	CIGateGrain  CIGateMode = "grain"
	CIGateGitHub CIGateMode = "github"
	CIGateBoth   CIGateMode = "both"
	// CIGateNone is only ever the repository's own floor (CIModeNone):
	// a rule cannot ask for it.
	CIGateNone CIGateMode = "none"
)

// Grain reports whether grain CI gates under m.
func (m CIGateMode) Grain() bool { return m == CIGateGrain || m == CIGateBoth }

// GitHub reports whether GitHub's checks gate under m.
func (m CIGateMode) GitHub() bool { return m == CIGateGitHub || m == CIGateBoth }

// combineModes is the strictest of a and b.
func combineModes(a, b CIGateMode) CIGateMode {
	grain, github := a.Grain() || b.Grain(), a.GitHub() || b.GitHub()
	switch {
	case grain && github:
		return CIGateBoth
	case grain:
		return CIGateGrain
	case github:
		return CIGateGitHub
	}
	if a == "" {
		return b
	}
	return a
}

// AutoMergePolicy says whether a change may merge itself.
type AutoMergePolicy string

const (
	AutoMergeAllowed AutoMergePolicy = "allowed"
	AutoMergeNever   AutoMergePolicy = "never"
)

// CIRule is a rule's `ci:` section. A nil list is "not said", which is
// different from an empty one only in YAML: both mean the full suite.
type CIRule struct {
	Mode   CIGateMode `json:",omitempty"`
	Grain  []string   `json:",omitempty"`
	GitHub []string   `json:",omitempty"`
}

// MaxAutomaticRepairs is the most automatic repairs the merge queue
// gives one pull request (grain/task-1640), and so the most `repairs:`
// may say.
const MaxAutomaticRepairs = 2

// BatchAlone is the `batching: alone` cap: a batch of one, which is no
// batch at all.
const BatchAlone = 1

// MergePolicy is what one rule, or the defaults, says.
type MergePolicy struct {
	CI           *CIRule         `json:",omitempty"`
	AutoMerge    AutoMergePolicy `json:",omitempty"`
	StallTimeout time.Duration   `json:",omitempty"`
	// Batching is the largest batch a change matching this may be in:
	// BatchAlone for `batching: alone`, 0 where it says nothing.
	Batching int `json:",omitempty"`
	// Repairs, FlakeRecheck and Port are nil where it says nothing.
	Repairs      *int  `json:",omitempty"`
	FlakeRecheck *bool `json:",omitempty"`
	Port         *bool `json:",omitempty"`
}

// empty reports whether p says nothing at all.
func (p MergePolicy) empty() bool {
	return p.CI == nil && p.AutoMerge == "" && p.StallTimeout == 0 && p.Batching == 0 &&
		p.Repairs == nil && p.FlakeRecheck == nil && p.Port == nil
}

// BatchingString is a batch cap in the file's own words.
func BatchingString(n int) string {
	if n == BatchAlone {
		return "alone"
	}
	return fmt.Sprint(n)
}

// MergeRule is one entry in the file.
type MergeRule struct {
	ID          string
	Description string `json:",omitempty"`
	Paths       []string
	MergePolicy
}

// Match is the changed files this rule covers.
func (r MergeRule) Match(changed []string) []string {
	var out []string
	for _, f := range changed {
		if matchesAny(r.Paths, f) {
			out = append(out, f)
		}
	}
	return out
}

// MergeRules is one repository's file, as the base declares it.
type MergeRules struct {
	// Path is the file it was read from, and Commit the base commit.
	Path     string      `json:",omitempty"`
	Commit   string      `json:",omitempty"`
	Defaults MergePolicy `json:",omitempty"`
	Rules    []MergeRule `json:",omitempty"`
	// Warnings are what the sync noticed and did not refuse the file
	// over: a job or check name nothing declares, both file names present.
	Warnings []string `json:",omitempty"`
}

// Empty reports whether the file says nothing at all.
func (r *MergeRules) Empty() bool {
	return r == nil || (len(r.Rules) == 0 && r.Defaults.empty())
}

// Sources a requirement can come from, besides a rule's own id.
const (
	PolicySourceRepo     = "repository setting"
	PolicySourceDefaults = "defaults"
)

// PolicyRequirement is one thing the effective policy asks for and the
// rule (or rules) that asked for it -- what a person reading the task or
// the merge queue is told.
type PolicyRequirement struct {
	// Field is the setting: "ci.mode", "ci.grain", "ci.github",
	// "auto_merge", "stall_timeout", "batching", "repairs",
	// "flake_recheck" or "port".
	Field string
	// Value is what it requires, in the file's own words.
	Value string
	// From is the rules that caused it, by id, PolicySourceDefaults or
	// PolicySourceRepo.
	From []string
}

// EffectiveMergePolicy is a pull request's policy: the strictest
// combination of everything that applied to it.
type EffectiveMergePolicy struct {
	// Head is the commit whose file list this was worked out from, and
	// Rules the base commit of the rules it was worked out under. A head
	// or rules that moved is classified again.
	Head  string `json:",omitempty"`
	Rules string `json:",omitempty"`

	Mode CIGateMode
	// GrainJobs is the grain CI jobs that must pass. With GrainFullSuite
	// the whole declared suite must pass and these are named on top;
	// without it the gate is narrowed to exactly these.
	GrainJobs      []string `json:",omitempty"`
	GrainFullSuite bool
	// GitHubChecks is the GitHub checks that must be present and green.
	// With GitHubAllChecks every check on the commit gates as well.
	GitHubChecks    []string `json:",omitempty"`
	GitHubAllChecks bool
	AutoMergeNever  bool          `json:",omitempty"`
	StallTimeout    time.Duration `json:",omitempty"`
	// BatchLimit is the largest merge batch this change may be in
	// (BatchAlone: none), 0 where no rule caps it.
	BatchLimit int `json:",omitempty"`
	// Repairs is the most automatic repairs it may have, nil where no
	// rule says (the queue's own maximum).
	Repairs *int `json:",omitempty"`
	// NoFlakeRecheck: a failing job is blamed without an uncached recheck.
	NoFlakeRecheck bool `json:",omitempty"`
	// NoPort: a merge is not ported to the paired branch automatically.
	NoPort bool `json:",omitempty"`

	// Matched is the rule ids the change's files matched.
	Matched      []string            `json:",omitempty"`
	Requirements []PolicyRequirement `json:",omitempty"`
}

// listGate folds one CI list across contributors: full is true while any
// contributor named nothing, and names is the union of what they named.
type listGate struct {
	full      bool
	names     []string
	seen      map[string]bool
	fullFrom  []string
	namedFrom map[string][]string
}

func (g *listGate) add(from string, names []string) {
	if len(names) == 0 {
		g.full = true
		g.fullFrom = appendUnique(g.fullFrom, from)
		return
	}
	if g.seen == nil {
		g.seen, g.namedFrom = map[string]bool{}, map[string][]string{}
	}
	for _, n := range names {
		if !g.seen[n] {
			g.seen[n] = true
			g.names = append(g.names, n)
		}
		g.namedFrom[n] = appendUnique(g.namedFrom[n], from)
	}
}

// minGate folds a setting whose strictest value is its smallest --
// batching, repairs, and (as 0 and 1) flake_recheck and port -- keeping
// which contributors said the winning value.
type minGate struct {
	set  bool
	min  int
	from []string
}

func (g *minGate) add(from string, v int) {
	switch {
	case !g.set || v < g.min:
		g.set, g.min, g.from = true, v, []string{from}
	case v == g.min:
		g.from = appendUnique(g.from, from)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RepairLimit is the most automatic repairs p allows, given the queue's
// own most, and the rules that lowered it (none where it is the queue's).
func (p *EffectiveMergePolicy) RepairLimit(queueMax int) (int, []string) {
	if p == nil || p.Repairs == nil || *p.Repairs >= queueMax {
		return queueMax, nil
	}
	return *p.Repairs, p.SourcesOf("repairs")
}

// SourcesOf is the rules that caused field's requirement, for a sentence.
func (p *EffectiveMergePolicy) SourcesOf(field string) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, r := range p.Requirements {
		if r.Field == field {
			for _, f := range r.From {
				out = appendUnique(out, f)
			}
		}
	}
	return out
}

func appendUnique(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
}

// Effective works out the policy for a change touching files, under the
// repository's own CI mode. known false is a file list that could not be
// read in full, which matches every rule and the defaults besides: the
// unread part is exactly where a stricter rule could apply.
func (r *MergeRules) Effective(files []string, known bool, repoMode CIMode) EffectiveMergePolicy {
	out := EffectiveMergePolicy{Mode: repoGateMode(repoMode)}
	req := map[string]*PolicyRequirement{}
	var order []string
	require := func(field, value, from string) {
		key := field + "\x00" + value
		p, ok := req[key]
		if !ok {
			p = &PolicyRequirement{Field: field, Value: value}
			req[key] = p
			order = append(order, key)
		}
		p.From = appendUnique(p.From, from)
	}
	if out.Mode != CIGateNone {
		require("ci.mode", string(out.Mode), PolicySourceRepo)
	}
	if r != nil {
		out.Rules = r.Commit
	}

	var grain, github listGate
	var batching, repairs, flake, port minGate
	stall := time.Duration(0)
	apply := func(from string, p MergePolicy, lists bool) {
		if p.CI != nil {
			if p.CI.Mode != "" {
				out.Mode = combineModes(out.Mode, p.CI.Mode)
				require("ci.mode", string(p.CI.Mode), from)
			}
			if lists {
				grain.add(from, p.CI.Grain)
				github.add(from, p.CI.GitHub)
			}
		} else if lists {
			grain.add(from, nil)
			github.add(from, nil)
		}
		if p.AutoMerge == AutoMergeNever {
			out.AutoMergeNever = true
			require("auto_merge", string(AutoMergeNever), from)
		}
		if p.StallTimeout > 0 && (stall == 0 || p.StallTimeout < stall) {
			stall = p.StallTimeout
		}
		if p.Batching > 0 {
			batching.add(from, p.Batching)
		}
		if p.Repairs != nil {
			repairs.add(from, *p.Repairs)
		}
		if p.FlakeRecheck != nil {
			flake.add(from, boolInt(*p.FlakeRecheck))
		}
		if p.Port != nil {
			port.add(from, boolInt(*p.Port))
		}
	}

	unmatched := !known || len(files) == 0
	covered := map[string]bool{}
	if r != nil {
		for _, rule := range r.Rules {
			hit := rule.Match(files)
			if len(hit) == 0 && known {
				continue
			}
			for _, f := range hit {
				covered[f] = true
			}
			out.Matched = append(out.Matched, rule.ID)
			apply(rule.ID, rule.MergePolicy, true)
		}
	}
	for _, f := range files {
		if !covered[f] {
			unmatched = true
		}
	}
	var defaults MergePolicy
	if r != nil {
		defaults = r.Defaults
	}
	apply(PolicySourceDefaults, defaults, unmatched)

	if stall > 0 {
		out.StallTimeout = stall
		var from []string
		if r != nil {
			for _, rule := range r.Rules {
				if rule.StallTimeout == stall && contains(out.Matched, rule.ID) {
					from = append(from, rule.ID)
				}
			}
			if r.Defaults.StallTimeout == stall {
				from = append(from, PolicySourceDefaults)
			}
		}
		for _, f := range from {
			require("stall_timeout", shortDuration(stall), f)
		}
	}

	if batching.set {
		out.BatchLimit = batching.min
		for _, f := range batching.from {
			require("batching", BatchingString(batching.min), f)
		}
	}
	if repairs.set {
		n := repairs.min
		out.Repairs = &n
		for _, f := range repairs.from {
			require("repairs", fmt.Sprint(n), f)
		}
	}
	if flake.set {
		out.NoFlakeRecheck = flake.min == 0
		for _, f := range flake.from {
			require("flake_recheck", fmt.Sprint(flake.min == 1), f)
		}
	}
	if port.set {
		out.NoPort = port.min == 0
		for _, f := range port.from {
			require("port", fmt.Sprint(port.min == 1), f)
		}
	}

	out.GrainFullSuite, out.GrainJobs = grain.full, grain.names
	out.GitHubAllChecks, out.GitHubChecks = github.full, github.names
	if out.Mode.Grain() {
		if grain.full {
			for _, f := range grain.fullFrom {
				require("ci.grain", "full suite", f)
			}
		}
		for _, n := range grain.names {
			for _, f := range grain.namedFrom[n] {
				require("ci.grain", n, f)
			}
		}
	}
	if out.Mode.GitHub() {
		for _, n := range github.names {
			for _, f := range github.namedFrom[n] {
				require("ci.github", n, f)
			}
		}
	}
	for _, k := range order {
		out.Requirements = append(out.Requirements, *req[k])
	}
	return out
}

// shortDuration is d as a rule would write it: "30m" and "2h" rather
// than Duration.String's "30m0s" and "2h0m0s".
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// repoGateMode is a repository's CI mode as a gate mode: its floor.
func repoGateMode(m CIMode) CIGateMode {
	switch m {
	case CIModeGrain:
		return CIGateGrain
	case CIModeNone:
		return CIGateNone
	case "":
		return CIGateGitHub
	}
	return CIGateGitHub
}

// GateCIMode is the repository CI mode the merge queue has always
// branched on, for an effective mode: grain where grain CI is the only
// gate, github otherwise (both reads GitHub's checks, and grain's verdict
// is required separately).
func (p EffectiveMergePolicy) GateCIMode() CIMode {
	switch p.Mode {
	case CIGateGrain:
		return CIModeGrain
	case CIGateNone:
		return CIModeNone
	}
	return CIModeGitHub
}

// Describe is the policy for a person: what it requires and why, one
// line per requirement.
func (p EffectiveMergePolicy) Describe() string {
	var b strings.Builder
	for _, r := range p.Requirements {
		fmt.Fprintf(&b, "- `%s: %s` -- from %s\n", r.Field, r.Value, describeSources(r.From))
	}
	if p.Mode.Grain() && !p.GrainFullSuite {
		fmt.Fprintf(&b, "- grain CI is narrowed to %s: every rule this change matched names its jobs\n",
			strings.Join(quoteEach(p.GrainJobs), ", "))
	}
	if p.Mode.GitHub() && !p.GitHubAllChecks {
		fmt.Fprintf(&b, "- GitHub CI is narrowed to %s: every rule this change matched names its checks\n",
			strings.Join(quoteEach(p.GitHubChecks), ", "))
	}
	return b.String()
}

func describeSources(from []string) string {
	out := make([]string, len(from))
	for i, f := range from {
		switch f {
		case PolicySourceRepo, PolicySourceDefaults:
			out[i] = f
		default:
			out[i] = "rule `" + f + "`"
		}
	}
	return strings.Join(out, ", ")
}

func quoteEach(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = "`" + s + "`"
	}
	return out
}

// LinkMergePolicy records the effective merge policy a task's pull
// request was classified under, as JSON (EffectiveMergePolicy).
const LinkMergePolicy LinkKind = "merge-policy"

// LinkMergePolicyWithdrawn records the head commit whose auto-merge grain
// withdrew under `auto_merge: never`, so a person's Submit after it is
// not withdrawn again for the same commit.
const LinkMergePolicyWithdrawn LinkKind = "merge-policy-withdrawn"

// MergePolicyLink encodes p for a task's links.
func MergePolicyLink(p EffectiveMergePolicy) Link {
	b, _ := json.Marshal(p)
	return Link{Kind: LinkMergePolicy, Target: string(b)}
}

// MergePolicyOf reads back the policy a task was classified under, or
// false where it has none (or one that no longer decodes, which is
// classified again).
func MergePolicyOf(task Task) (EffectiveMergePolicy, bool) {
	for _, l := range task.Links {
		if l.Kind != LinkMergePolicy {
			continue
		}
		var p EffectiveMergePolicy
		if err := json.Unmarshal([]byte(l.Target), &p); err != nil {
			return EffectiveMergePolicy{}, false
		}
		return p, true
	}
	return EffectiveMergePolicy{}, false
}

// SortedRuleIDs is the rules' ids in file order, for a log line.
func (r *MergeRules) SortedRuleIDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.Rules))
	for _, rule := range r.Rules {
		out = append(out, rule.ID)
	}
	sort.Strings(out)
	return out
}
