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
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// The limit, cursor and q every collection read of the agent API takes
// (docs/agent-api.md, "Paging a list"), so no api_* list tool can answer
// with an unbounded body.
//
// An MCP tool result carries about 10 KB before the client elides it, and
// several list routes -- what a credential reaches, a repository's
// branches, the memory index -- grow with the deployment rather than with
// anything the caller chose, so a single call could return far more than
// that with no parameter to narrow it by (grain/task-57). Each of them now
// reads the same three parameters through parseListQuery, and when the
// answer is a page rather than the whole list it says so: {items, total,
// truncated, nextCursor} rather than a bare list silently cut short.
//
// The UI's own reads are kept as they were: a person's call that names
// none of the parameters gets the whole list in the shape it always had,
// since the pickers that read it show everything. An agent's call --
// through the gate, whether from a sandbox, a session or an external
// credential -- is paged by default, listPageDefault at a time, because
// an agent has a budget and a person has a scroll bar.

// A page's size when a paged caller names no limit, by how much a row
// of the list weighs: chosen so a default page of each stays under the
// ~10 KB tool-result cap with room for the rest of the answer.
const (
	// listPageDefault is for lists of short rows -- a repository's name
	// and whether it can be pushed to, a branch's name -- at about 50
	// bytes each.
	listPageDefault = 100
	// listPageMedium is for rows of a dozen fields, such as a memory
	// document's index line or a branch grain cut, at 100-200 bytes.
	listPageMedium = 50
	// listPageLarge is for rows that carry lists of their own -- a CI
	// fault's branches and runs, a suite run's progress, a notification's
	// body -- at up to half a kilobyte.
	listPageLarge = 20
	// listPageSmall is for rows carrying structured schemas or job/step
	// trees -- a full CI run, a capability method with its JSON schema, an
	// inline schedule -- at 500-800 bytes.
	listPageSmall = 10
	// listPageTiny is for rows carrying prose or YAML bodies -- a
	// template's prompt, a standing memory's text, a weekly release note,
	// a declared workflow file -- at 1-1.5 kilobytes.
	listPageTiny = 5
	// listPageMax bounds a limit a caller does name, so a limit with an
	// extra zero in it costs a large answer rather than the daemon's
	// memory.
	listPageMax = 1000
)

// isAgentListCaller reports whether r is a call from an agent or through
// the controller MCP endpoint (grain_invoke), both of which sit behind the
// ~10 KB MCP tool-result cap and page collection reads by default.
func isAgentListCaller(r *http.Request) bool {
	if _, ok := agentCallFromContext(r.Context()); ok {
		return true
	}
	return controllerMCPRequestFromContext(r.Context()) != nil
}

// listQuery is what one collection read's limit, cursor and q resolve to.
type listQuery struct {
	// Limit is how many items the page carries, 0 for every item.
	Limit int
	// Offset is where the page starts, from the cursor.
	Offset int
	// Q is the lower-cased substring an item's natural key must contain,
	// "" for no filter.
	Q string
	// Paged says the answer is a ListPage rather than the bare list: the
	// caller named limit, cursor or q, or is an agent.
	Paged bool
}

// parsePageBounds reads limit and cursor from r, and whether the caller is
// an agent (which pages by default, defaultLimit at a time), leaving q to a
// caller whose route already has a filter grammar of its own.
func parsePageBounds(r *http.Request, defaultLimit int) (listQuery, error) {
	q := r.URL.Query()
	var out listQuery
	agent := isAgentListCaller(r)
	if text := strings.TrimSpace(q.Get("limit")); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 {
			return out, validationErrorf("%q is not a limit: it is how many items to answer with, a whole number (0 means every item)", q.Get("limit"))
		}
		out.Limit, out.Paged = n, true
	} else if agent {
		out.Limit = defaultLimit
	}
	if text := strings.TrimSpace(q.Get("cursor")); text != "" {
		offset, err := decodeListCursor(text)
		if err != nil {
			return out, err
		}
		out.Offset, out.Paged = offset, true
		if out.Limit == 0 {
			out.Limit = defaultLimit
		}
	}
	if out.Limit > listPageMax {
		out.Limit = listPageMax
	}
	if agent {
		out.Paged = true
	}
	return out, nil
}

// parseListQuery reads limit, cursor and q from r, and whether the caller
// is an agent (which pages by default, defaultLimit at a time). A limit
// that is not a whole number, or a cursor this route did not hand out,
// is refused rather than ignored: an argument silently dropped answers
// with something else and says nothing about it.
func parseListQuery(r *http.Request, defaultLimit int) (listQuery, error) {
	out, err := parsePageBounds(r, defaultLimit)
	if err != nil {
		return out, err
	}
	out.Q = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if out.Q != "" {
		out.Paged = true
	}
	return out, nil
}

// matches reports whether q's substring is in any of keys, or there is no
// substring to match.
func (q listQuery) matches(keys ...string) bool {
	if q.Q == "" {
		return true
	}
	for _, key := range keys {
		if strings.Contains(strings.ToLower(key), q.Q) {
			return true
		}
	}
	return false
}

// ListPage is one page of a collection read: the items of this page, how
// many matched across every page, whether this page was cut short by the
// limit, and when it was, the cursor that asks for the next.
type ListPage[T any] struct {
	Items      []T    `json:"items"`
	Total      int    `json:"total"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// pageOf cuts q's page out of items, which are already filtered and in
// the order the route answers in.
func pageOf[T any](items []T, q listQuery) ListPage[T] {
	page := ListPage[T]{Items: []T{}, Total: len(items)}
	start := min(q.Offset, len(items))
	end := len(items)
	if q.Limit > 0 && start+q.Limit < end {
		end = start + q.Limit
	}
	page.Items = append(page.Items, items[start:end]...)
	if end < len(items) {
		page.Truncated = true
		page.NextCursor = encodeListCursor(end)
	}
	return page
}

// writeList answers a collection read with items: the bare list when q
// does not page, and q's page of it when it does.
func writeList[T any](w http.ResponseWriter, items []T, q listQuery) {
	if !q.Paged {
		if items == nil {
			items = []T{}
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	writeJSON(w, http.StatusOK, pageOf(items, q))
}

// filterList keeps the items of list that keep says to, in their order.
func filterList[T any](list []T, keep func(T) bool) []T {
	out := make([]T, 0, len(list))
	for _, item := range list {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

// A list cursor is the offset of the next page, encoded so a caller
// hands it back rather than reading anything into it: the lists it pages
// are sorted by a stable key, so an offset is as good a place to resume
// as any, and a caller that wants the whole list pages until nextCursor
// is empty.
const listCursorPrefix = "o:"

func encodeListCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(listCursorPrefix + strconv.Itoa(offset)))
}

func decodeListCursor(text string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err == nil {
		if after, ok := strings.CutPrefix(string(raw), listCursorPrefix); ok {
			if offset, err := strconv.Atoi(after); err == nil && offset >= 0 {
				return offset, nil
			}
		}
	}
	return 0, validationErrorf("%q is not a cursor this route handed out: pass back a previous answer's nextCursor, or leave it out for the first page", text)
}

// ListCursorFor is the opaque cursor that starts a paged collection read
// at offset, for a caller that pages by hand.
func ListCursorFor(offset int) string {
	return encodeListCursor(max(offset, 0))
}

// describeListQuery is the sentence every paged route's tool description
// ends with, naming the three parameters.
func describeListQuery(what string, defaultLimit int) string {
	return fmt.Sprintf("Paged (default %d): filter with q (%s), limit and cursor; sets truncated and nextCursor.", defaultLimit, what)
}

// listQueryDocs is the routeQueries entries every paged route shares,
// with what for the natural key q is matched against.
func listQueryDocs(what string, defaultLimit int, more map[string]string) map[string]string {
	out := map[string]string{
		"q":      "Substring of " + what + ".",
		"limit":  fmt.Sprintf("Max items (default %d, max %d; 0 for all).", defaultLimit, listPageMax),
		"cursor": "Previous nextCursor.",
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

// listObjectQueryDocs is listQueryDocs for a route whose answer is already
// an object carrying total, truncated and nextCursor beside its own fields.
func listObjectQueryDocs(what string, defaultLimit int, more map[string]string) map[string]string {
	return listQueryDocs(what, defaultLimit, more)
}
