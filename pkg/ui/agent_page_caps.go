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
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Paged rows that carry a list or a document of their own -- a CI run's
// jobs, a capability server's configure arrays, a method's description
// and argument schema -- grow with the deployment rather than with the
// page, so a default page of ten such rows was 24-240 KB on a deployment
// of a few hundred spaces or workflows
// (docs/test-runs/2026-11-10-agent-api-paging.md, "Derived collections").
// A paged row carries the first few of each and says how many there
// were; a person's unpaged read, and fields=full, carry the whole row.

// Caps on what one paged row carries.
const (
	// pageRowArrayItems is how many items of each array a paged
	// capability server's config, or a paged capability method's
	// argument schema, keeps.
	pageRowArrayItems = 10
	// pageRowDescriptionBytes is how much of a paged capability
	// method's description is kept.
	pageRowDescriptionBytes = 500
	// ciRunPageJobs is how many jobs a paged CI run row (view=full)
	// carries, the ones that did not pass first.
	ciRunPageJobs = 5
	// ciRunPageErrorBytes is how much of a paged CI run's error, and
	// of each of its jobs' errors, is kept.
	ciRunPageErrorBytes = 500
	// ciCompactRunPageJobs is how many job lines a paged compact CI run
	// carries: failures before jobs still waiting.
	ciCompactRunPageJobs = 10
	// ciFaultPageRuns, ciFaultPageRefs and ciFaultPagePaths are how many
	// runs, branches and files a paged CI fault row names.
	ciFaultPageRuns  = 3
	ciFaultPageRefs  = 5
	ciFaultPagePaths = 5
	// ciRunsPageFaults is how many shared faults a paged GET
	// /api/ci/runs (view=full) carries beside its runs; GET
	// /api/ci/faults pages through every one.
	ciRunsPageFaults = 5
	// suiteRunPageTasks is how many of a suite run's tasks a paged row
	// carries; GET /api/suite-runs/{id} has every one.
	suiteRunPageTasks = 3
)

// wantsFullRows reports whether the caller asked for whole rows on a
// paged read (fields=full), which the trimming above leaves alone.
func wantsFullRows(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("fields")), "full")
}

// trimsPagedRows reports whether a read's rows are trimmed: a paged read
// that did not ask for fields=full.
func trimsPagedRows(r *http.Request, list listQuery) bool {
	return list.Paged && !wantsFullRows(r)
}

// writePageTrimmed is writeListBy for a route whose paged rows are
// trimmed to fit the tool-result cap: trim is applied to each row of the
// page after it is cut, never to a person's unpaged list, and not at all
// when the caller asked for fields=full.
func writePageTrimmed[T any](w http.ResponseWriter, r *http.Request, items []T, list listQuery, key func(T) string, trim func(T) T) {
	if !list.Paged {
		writeListBy(w, items, list, key)
		return
	}
	page := pageOfBy(items, list, key)
	if !wantsFullRows(r) {
		for i := range page.Items {
			page.Items[i] = trim(page.Items[i])
		}
	}
	writeJSON(w, http.StatusOK, page)
}

// capJSONArrays returns a copy of v, a JSON-shaped value, with every
// array in it cut to its first n items, and the full length of each array
// it cut keyed by its path ("spaces", "properties.space.enum"). v is
// never modified. A value that is not JSON-shaped (a typed Go slice or
// struct) is round-tripped through encoding/json first, so the copy is
// always map[string]any / []any all the way down.
func capJSONArrays(v any, n int) (any, map[string]int) {
	if v == nil {
		return nil, nil
	}
	if !isJSONShaped(v) {
		raw, err := json.Marshal(v)
		if err != nil {
			return v, nil
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return v, nil
		}
		v = decoded
	}
	cut := map[string]int{}
	out := capJSONValue(v, n, "", cut)
	if len(cut) == 0 {
		cut = nil
	}
	return out, cut
}

// isJSONShaped reports whether v holds only what encoding/json decodes
// into an any: maps of string to any, slices of any, and scalars.
func isJSONShaped(v any) bool {
	switch t := v.(type) {
	case nil, string, bool, float64, json.Number:
		return true
	case map[string]any:
		for _, e := range t {
			if !isJSONShaped(e) {
				return false
			}
		}
		return true
	case []any:
		for _, e := range t {
			if !isJSONShaped(e) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func capJSONValue(v any, n int, path string, cut map[string]int) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = capJSONValue(e, n, joinJSONPath(path, k), cut)
		}
		return out
	case []any:
		keep := t
		if len(t) > n {
			keep = t[:n]
			name := path
			if name == "" {
				name = "."
			}
			cut[name] = len(t)
		}
		out := make([]any, len(keep))
		for i, e := range keep {
			out[i] = capJSONValue(e, n, joinJSONPath(path, strconv.Itoa(i)), cut)
		}
		return out
	default:
		return v
	}
}

func joinJSONPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// capJSONObject is capJSONArrays for a JSON object (a server's config, a
// method's argument schema), answering an object again.
func capJSONObject(m map[string]any, n int) (map[string]any, map[string]int) {
	if len(m) == 0 {
		return m, nil
	}
	out, cut := capJSONArrays(m, n)
	if obj, ok := out.(map[string]any); ok {
		return obj, cut
	}
	return m, nil
}

// truncateText cuts s to at most limit bytes on a rune boundary, and
// says whether it cut anything.
func truncateText(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \n\t") + "…", true
}

// firstOf returns the first n items of list (a copy), and whether it
// cut any.
func firstOf[T any](list []T, n int) ([]T, bool) {
	if len(list) <= n {
		return list, false
	}
	return append([]T(nil), list[:n]...), true
}

// preferredFirst returns at most n items of list: the ones prefer says
// to keep first (in list's own order), then the rest in order until there
// are n, and whether any were left out. A list of n or fewer is returned
// as it is.
func preferredFirst[T any](list []T, n int, prefer func(T) bool) ([]T, bool) {
	if len(list) <= n {
		return list, false
	}
	out := make([]T, 0, n)
	taken := make([]bool, len(list))
	for i, item := range list {
		if len(out) == n {
			break
		}
		if prefer(item) {
			out = append(out, item)
			taken[i] = true
		}
	}
	for i, item := range list {
		if len(out) == n {
			break
		}
		if !taken[i] {
			out = append(out, item)
		}
	}
	return out, true
}
