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

package mcp

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ai-on-gke/grain/pkg/model"
)

// proposeTaskSynonyms maps argument spellings borrowed from other Grain task
// surfaces (POST /api/tasks, grain_submit_task, writes entries, CLI flags) to
// the name propose_task declares. Checked before edit-distance matching so
// short words like "base" point to "branch" rather than "body".
var proposeTaskSynonyms = map[string]string{
	"base":             "branch",
	"base_branch":      "branch",
	"target_branch":    "branch",
	"ref":              "branch",
	"description":      "body",
	"desc":             "body",
	"prompt":           "body",
	"fields":           "template_fields",
	"template":         "template_id",
	"review_template":  "review_template_id",
	"approve":          "hold_for_approval",
	"approved":         "hold_for_approval",
	"position":         "at_front",
	"backend":          "model",
	"agent_backend":    "model",
	"agent_backend_id": "model",
	"framework":        "agent_framework",
	"cpu":              "sandbox_cpus",
	"cpus":             "sandbox_cpus",
	"memory":           "sandbox_memory_mb",
	"memory_mb":        "sandbox_memory_mb",
	"disk":             "sandbox_disk_gb",
	"disk_gb":          "sandbox_disk_gb",
	"network":          "network_access",
	"defer":            "not_before",
	"deferred_until":   "not_before",
	"wake_at":          "not_before",
	"resume_at":        "not_before",
	"depends":          "depends_on",
	"dependencies":     "depends_on",
	"blocked_by":       "depends_on",
	"supersede":        "supersedes",
	"replaces":         "supersedes",
	"repository":       "repo",
}

var writeTargetSynonyms = map[string]string{
	"branch":        "base",
	"base_branch":   "base",
	"target_branch": "base",
	"ref":           "base",
	"repository":    "repo",
}

// ValidateProposeTaskArgs checks that every argument in a propose_task call is
// declared by propose_task's InputSchema (and every object in writes carries
// only declared fields). Both proposeTaskTool and orchestrator.relayProposedTasks
// call this so call-time validation and end-of-run replay cannot disagree.
func ValidateProposeTaskArgs(args map[string]any) error {
	if len(args) == 0 {
		return nil
	}
	declared := proposeTaskProperties(nil)
	declaredNames := slices.Sorted(maps.Keys(declared))
	for _, key := range slices.Sorted(maps.Keys(args)) {
		if _, ok := declared[key]; !ok {
			if near := nearestArgName(key, declared, declaredNames, proposeTaskSynonyms); near != "" {
				return fmt.Errorf("propose_task: unknown argument %q (did you mean %q?)", key, near)
			}
			return fmt.Errorf("propose_task: unknown argument %q", key)
		}
	}
	return validateProposeTaskWrites(args["writes"])
}

func validateProposeTaskWrites(raw any) error {
	if raw == nil {
		return nil
	}
	var items []map[string]any
	switch val := raw.(type) {
	case []any:
		for _, entry := range val {
			if m, ok := entry.(map[string]any); ok {
				items = append(items, m)
			} else {
				items = append(items, nil)
			}
		}
	case []map[string]any:
		items = val
	default:
		return nil
	}
	declared := map[string]any{"repo": true, "base": true}
	declaredNames := []string{"base", "repo"}
	for i, item := range items {
		if item == nil {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(item)) {
			if _, ok := declared[key]; !ok {
				if near := nearestArgName(key, declared, declaredNames, writeTargetSynonyms); near != "" {
					return fmt.Errorf("propose_task: writes[%d]: unknown field %q (did you mean %q?)", i, key, near)
				}
				return fmt.Errorf("propose_task: writes[%d]: unknown field %q", i, key)
			}
		}
	}
	return nil
}

// ProposeTaskGrantSpelling reports which legacy grant/target arguments a
// propose_task call sent alongside grants:, for model.DecodeRawRequestGrants.
func ProposeTaskGrantSpelling(args map[string]any) model.GrantSpelling {
	var targetFields []string
	if repo, _ := argString(args, "repo"); strings.TrimSpace(repo) != "" {
		targetFields = append(targetFields, "repo")
	}
	if branch, _ := argString(args, "branch"); strings.TrimSpace(branch) != "" {
		targetFields = append(targetFields, "branch")
	}
	if noRepo, _ := argBool(args, "no_repo"); noRepo {
		targetFields = append(targetFields, "no_repo")
	}
	if ct, err := ProposedChangeTarget(args); err != nil || ct != nil {
		if ct != nil && ct.Kind != "" {
			targetFields = append(targetFields, ct.Kind)
		} else {
			targetFields = append(targetFields, "change_target")
		}
	}
	return model.GrantSpelling{
		Capabilities: args["capabilities"] != nil,
		Reads:        args["reads"] != nil,
		Writes:       args["writes"] != nil,
		Target:       len(targetFields) > 0,
		TargetFields: targetFields,
	}
}

func nearestArgName(raw string, declared map[string]any, declaredNames []string, synonyms map[string]string) string {
	norm := normalizeArgKey(raw)
	if norm == "" {
		return ""
	}
	if _, ok := declared[norm]; ok {
		return norm
	}
	if syn, ok := synonyms[norm]; ok {
		if _, exists := declared[syn]; exists {
			return syn
		}
	}
	maxDist := 0
	switch {
	case len(norm) >= 9:
		maxDist = 3
	case len(norm) >= 5:
		maxDist = 2
	case len(norm) >= 3:
		maxDist = 1
	}
	if maxDist == 0 {
		return ""
	}
	best := ""
	bestDist := maxDist + 1
	for _, cand := range declaredNames {
		d := argEditDistance(norm, cand)
		if d < bestDist {
			bestDist = d
			best = cand
		}
	}
	return best
}

func normalizeArgKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "-")
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range s {
		if r == '-' || r == ' ' {
			b.WriteByte('_')
			continue
		}
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := rune(s[i-1])
				if prev != '_' && prev != '-' && !unicode.IsUpper(prev) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// argEditDistance is the optimal string alignment distance (insertions,
// deletions, substitutions, and adjacent transpositions each costing 1).
func argEditDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}
