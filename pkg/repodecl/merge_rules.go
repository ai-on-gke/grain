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

package repodecl

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// `.grain/merge-rules.yml` (grain/task-1659): per-path CI gate,
// auto-merge and CI stall timeout, and (grain/task-1660) batching,
// automatic repairs, flake rechecks and porting. model's merge_rules.go has what a rule
// means and how rules combine; this is the YAML surface and the checks a
// file has to pass before it is anything at all. An unknown key is an
// error (decodeInto is strict), and so is a value outside a field's
// vocabulary. A grain CI job or a GitHub check nothing declares is only a
// warning: the first may be a workflow added in the same change, and the
// second is GitHub's to know, not the tree's.

// rawMergePolicy is a rule's, or the defaults', optional sections.
type rawMergePolicy struct {
	CI           *rawCIRule `yaml:"ci"`
	AutoMerge    string     `yaml:"auto_merge"`
	StallTimeout string     `yaml:"stall_timeout"`
	// grain/task-1660's: `alone` or a number, 0-2, and two booleans.
	// Read loosely and checked in parseMergePolicy, so a wrong value is
	// refused in the field's own words rather than YAML's.
	Batching     any `yaml:"batching"`
	Repairs      any `yaml:"repairs"`
	FlakeRecheck any `yaml:"flake_recheck"`
	Port         any `yaml:"port"`
}

type rawCIRule struct {
	Mode   string   `yaml:"mode"`
	Grain  []string `yaml:"grain"`
	GitHub []string `yaml:"github"`
}

type rawMergeRule struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Paths       []string `yaml:"paths"`
	// Review is grain/task-1657's planned section. Per-path reviews
	// landed as sensitive `.grain/triggers` instead (grain/task-1714), so
	// it is refused with a pointer there rather than read and ignored.
	Review         any `yaml:"review"`
	rawMergePolicy `yaml:",inline"`
}

type rawMergeRules struct {
	Defaults *rawMergePolicy `yaml:"defaults"`
	Rules    []rawMergeRule  `yaml:"rules"`
}

var mergeRuleID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// LoadMergeRules reads the repository's merge rules, or nil where it has
// none. Where both the file and its old name exist the file wins and the
// other is named in a warning.
func LoadMergeRules(tree fs.FS) (*model.MergeRules, error) {
	var found []file
	for _, p := range model.MergeRulesPaths {
		text, err := fs.ReadFile(tree, p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}
		found = append(found, file{path: p, stem: "merge-rules", text: text})
	}
	if len(found) == 0 {
		return nil, nil
	}
	if len(found) > 1 && found[1].path != model.MergeRulesAliasPath {
		return nil, fmt.Errorf("%s: %s already declares the merge rules -- one file, please", found[1].path, found[0].path)
	}
	rules, err := parseMergeRules(found[0])
	if err != nil {
		return nil, err
	}
	for _, other := range found[1:] {
		rules.Warnings = append(rules.Warnings, fmt.Sprintf(
			"%s and %s both exist; %s is read and %s is ignored -- delete it", found[0].path, other.path,
			found[0].path, other.path))
	}
	if workflows, err := ci.LoadNativeDir(tree); err == nil {
		rules.Warnings = append(rules.Warnings, unknownGrainJobs(rules, workflows)...)
	}
	return rules, nil
}

func parseMergeRules(f file) (*model.MergeRules, error) {
	var raw rawMergeRules
	if err := decodeInto(f, &raw); err != nil {
		return nil, err
	}
	out := &model.MergeRules{Path: f.path}
	var errs []error
	if raw.Defaults != nil {
		p, err := parseMergePolicy(raw.Defaults)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: defaults: %w", f.path, err))
		}
		out.Defaults = p
	}
	seen := map[string]bool{}
	for i, r := range raw.Rules {
		where := fmt.Sprintf("%s: rules[%d]", f.path, i)
		if r.ID != "" {
			where = fmt.Sprintf("%s: rule %q", f.path, r.ID)
		}
		switch {
		case r.ID == "":
			errs = append(errs, fmt.Errorf("%s: id is required", where))
		case !mergeRuleID.MatchString(r.ID):
			errs = append(errs, fmt.Errorf("%s: id must be lower-case letters, digits, '.', '_' and '-'", where))
		case seen[r.ID]:
			errs = append(errs, fmt.Errorf("%s: a rule with this id is already declared", where))
		}
		seen[r.ID] = true
		if r.Review != nil {
			errs = append(errs, fmt.Errorf("%s: review: per-path reviews are declared as a sensitive trigger "+
				"in %s (see docs/declarations.md), not in the merge rules", where, model.TriggerDeclarationDir))
		}
		if len(r.Paths) == 0 {
			errs = append(errs, fmt.Errorf("%s: paths: at least one path is required", where))
		}
		for _, p := range r.Paths {
			if err := model.ValidatePathPattern(p); err != nil {
				errs = append(errs, fmt.Errorf("%s: paths: %w", where, err))
			}
		}
		p, err := parseMergePolicy(&r.rawMergePolicy)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		}
		out.Rules = append(out.Rules, model.MergeRule{
			ID: r.ID, Description: strings.TrimSpace(r.Description), Paths: r.Paths, MergePolicy: p,
		})
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func parseMergePolicy(raw *rawMergePolicy) (model.MergePolicy, error) {
	var out model.MergePolicy
	var errs []error
	if raw.CI != nil {
		c := &model.CIRule{Mode: model.CIGateMode(strings.TrimSpace(raw.CI.Mode))}
		switch c.Mode {
		case "", model.CIGateGrain, model.CIGateGitHub, model.CIGateBoth:
		default:
			errs = append(errs, fmt.Errorf("ci.mode: %q is not one of grain, github, both", raw.CI.Mode))
		}
		var err error
		if c.Grain, err = names("ci.grain", raw.CI.Grain); err != nil {
			errs = append(errs, err)
		}
		if c.GitHub, err = names("ci.github", raw.CI.GitHub); err != nil {
			errs = append(errs, err)
		}
		out.CI = c
	}
	switch a := model.AutoMergePolicy(strings.TrimSpace(raw.AutoMerge)); a {
	case "", model.AutoMergeAllowed, model.AutoMergeNever:
		out.AutoMerge = a
	default:
		errs = append(errs, fmt.Errorf("auto_merge: %q is not one of allowed, never", raw.AutoMerge))
	}
	if s := strings.TrimSpace(raw.StallTimeout); s != "" {
		d, err := time.ParseDuration(s)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("stall_timeout: %q is not a duration like 30m or 2h", s))
		case d < time.Minute:
			errs = append(errs, fmt.Errorf("stall_timeout: %s is shorter than a minute", d))
		default:
			out.StallTimeout = d
		}
	}
	switch b := raw.Batching.(type) {
	case nil:
	case string:
		if strings.TrimSpace(b) != "alone" {
			errs = append(errs, fmt.Errorf("batching: %q is not alone or a maximum batch size", b))
		} else {
			out.Batching = model.BatchAlone
		}
	case int:
		if b < 1 {
			errs = append(errs, fmt.Errorf("batching: %d is not a batch size -- 1 or more, or alone", b))
		} else {
			out.Batching = min(b, model.MaxMergeBatchSize)
		}
	default:
		errs = append(errs, fmt.Errorf("batching: %v is not alone or a maximum batch size", b))
	}
	if raw.Repairs != nil {
		if n, ok := raw.Repairs.(int); !ok || n < 0 || n > model.MaxAutomaticRepairs {
			errs = append(errs, fmt.Errorf("repairs: %v is not 0, 1 or 2", raw.Repairs))
		} else {
			out.Repairs = &n
		}
	}
	var err error
	if out.FlakeRecheck, err = optionalBool("flake_recheck", raw.FlakeRecheck); err != nil {
		errs = append(errs, err)
	}
	if out.Port, err = optionalBool("port", raw.Port); err != nil {
		errs = append(errs, err)
	}
	return out, errors.Join(errs...)
}

func optionalBool(field string, v any) (*bool, error) {
	if v == nil {
		return nil, nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("%s: %v is not true or false", field, v)
	}
	return &b, nil
}

func names(field string, in []string) ([]string, error) {
	var out []string
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n == "" {
			return nil, fmt.Errorf("%s: an empty name", field)
		}
		out = append(out, n)
	}
	return out, nil
}

// unknownGrainJobs warns about each ci.grain name no job, job name or
// workflow in the tree's `.grain/workflows` answers to.
func unknownGrainJobs(rules *model.MergeRules, workflows []ci.Workflow) []string {
	known := map[string]bool{}
	for _, w := range workflows {
		known[w.Name] = true
		known[model.DeclarationStem(w.Path)] = true
		for _, j := range w.Jobs {
			known[j.ID] = true
			known[j.Name] = true
		}
	}
	var out []string
	check := func(where string, p model.MergePolicy) {
		if p.CI == nil {
			return
		}
		for _, n := range p.CI.Grain {
			if !known[n] {
				out = append(out, fmt.Sprintf("%s: %s: ci.grain names %q, which no job or workflow in %s declares",
					rules.Path, where, n, model.DeclarationDir+"/workflows"))
			}
		}
	}
	check("defaults", rules.Defaults)
	for _, r := range rules.Rules {
		check("rule "+r.ID, r.MergePolicy)
	}
	return out
}
