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

package ui

import (
	"slices"
	"sort"
	"strings"
	"unicode"
)

// How grain_explore_scope finds a route by a word, and how it and
// grain_invoke answer a word that finds nothing: with the routes nearest
// to it, so a guessed name (api_get_logs for api_get_log_lines) costs
// one call rather than a failed call and two explores.

// searchTokens splits s into its lower-case words: a tool name on its
// underscores, a path on its slashes and hyphens, a sentence on its
// spaces and punctuation. The api_ prefix every tool carries is dropped,
// as is anything it would be pointless to match on.
func searchTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if f == "api" || len(f) < 2 {
			continue
		}
		out = append(out, f)
	}
	return out
}

// routeSearchTokens is every word a route can be found by: its tool
// name's, its path's, its scope's, and its summary's.
func routeSearchTokens(rt APIRoute) []string {
	var out []string
	out = append(out, searchTokens(rt.Tool)...)
	out = append(out, searchTokens(rt.Path)...)
	out = append(out, searchTokens(string(rt.Scope))...)
	out = append(out, searchTokens(rt.Summary)...)
	return out
}

// routeNamedByToolQuery reports whether toolQuery is rt's tool name,
// with or without its api_ prefix.
func routeNamedByToolQuery(rt APIRoute, toolQuery string) bool {
	tool := strings.ToLower(rt.Tool)
	return tool == toolQuery || tool == APIToolPrefix+toolQuery
}

// routeMatchesToolQuery reports whether rt is one a tool query finds:
// a substring of its tool name or path, or -- for a query of one or
// more words -- every word of the query found in a word of its tool
// name, path, scope or summary. A query that is exactly a tool's name
// finds that tool alone (routeNamedByToolQuery), which a caller of this
// checks first.
func routeMatchesToolQuery(rt APIRoute, toolQuery string) bool {
	if toolQuery == "" {
		return true
	}
	if routeNamedByToolQuery(rt, toolQuery) ||
		strings.Contains(strings.ToLower(rt.Tool), toolQuery) || strings.Contains(strings.ToLower(rt.Path), toolQuery) {
		return true
	}
	words := searchTokens(toolQuery)
	if len(words) == 0 {
		return false
	}
	haystack := routeSearchTokens(rt)
	for _, w := range words {
		found := false
		for _, h := range haystack {
			if strings.Contains(h, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// editDistance is the optimal string alignment distance between a and b:
// insertions, deletions, substitutions and adjacent transpositions, each
// costing one.
func editDistance(a, b string) int {
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

// wordSimilarity scores how well a query word matches a route's word:
// 1 for the same word, less for one that is a prefix or a near
// misspelling (logs for log, lits for list), 0 for an unrelated one.
func wordSimilarity(query, word string) float64 {
	if query == word {
		return 1
	}
	shorter := min(len(query), len(word))
	if shorter >= 3 && (strings.HasPrefix(word, query) || strings.HasPrefix(query, word)) {
		return 0.8
	}
	if shorter >= 4 && strings.Contains(word, query) {
		return 0.6
	}
	allowed := 0
	switch {
	case shorter >= 7:
		allowed = 2
	case shorter >= 3:
		allowed = 1
	}
	if allowed > 0 && editDistance(query, word) <= allowed {
		return 0.7
	}
	return 0
}

// toolSuggestion is one route nearestTools proposes, with how near it is.
type toolSuggestion struct {
	Tool  string
	Score float64
}

// nearestTools ranks routes by how nearly their words match the words of
// query, and returns the tools of the best few: those matching at least
// half of the query's words, best first, at most n. A query with no
// words, or one nothing comes near, yields none.
func nearestTools(query string, routes []APIRoute, n int) []string {
	words := searchTokens(query)
	if len(words) == 0 || n <= 0 {
		return nil
	}
	var scored []toolSuggestion
	for _, rt := range routes {
		haystack := routeSearchTokens(rt)
		nameWords := searchTokens(rt.Tool)
		total := 0.0
		for _, w := range words {
			best := 0.0
			for i, h := range haystack {
				s := wordSimilarity(w, h)
				// A word of the tool's own name counts for more than
				// the same word deep in its description.
				if i >= len(nameWords) {
					s *= 0.75
				}
				best = max(best, s)
			}
			total += best
		}
		score := total / float64(len(words))
		if score < 0.5 {
			continue
		}
		// Between routes matching equally well, the one whose name has
		// the fewest words beyond the query's is the likelier guess.
		score -= 0.01 * float64(max(0, len(nameWords)-len(words)))
		// And a route that stands alone over an alias of another.
		if rt.AliasOf != "" {
			score -= 0.02
		}
		scored = append(scored, toolSuggestion{Tool: rt.Tool, Score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Tool < scored[j].Tool
	})
	out := make([]string, 0, n)
	for _, s := range scored {
		if len(out) == n {
			break
		}
		out = append(out, s.Tool)
	}
	return out
}

// nearestGroups is the scope groups among groups whose name comes near a
// word of query, for an explore that found no route.
func nearestGroups(query string, groups []string) []string {
	var out []string
	for _, w := range searchTokens(query) {
		for _, g := range groups {
			if wordSimilarity(w, g) > 0 && !slices.Contains(out, g) {
				out = append(out, g)
			}
		}
	}
	sort.Strings(out)
	return out
}
