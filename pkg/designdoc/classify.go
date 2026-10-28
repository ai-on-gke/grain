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

package designdoc

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Commit is one commit on the branch a doc is implemented on: its sha and
// its full message. Newest first, the order `git log` gives.
type Commit struct {
	SHA     string
	Message string
}

// Built is what the history says about one requirement of a doc.
type Built struct {
	// Commits are the commits whose `Design-Item:` trailer names the
	// item, newest first.
	Commits []string
	// Rev is the doc revision the newest of them implemented (its
	// `Design-Rev:`), "" when it named none.
	Rev string
	// Removed is the commit whose `Design-Removed:` trailer took the item
	// back out, newer than every implementing commit, or "".
	Removed string
}

// History reads which requirements of doc the commits implement, keyed by
// ID. A commit with several `Design-Item:` trailers counts for each, and
// its `Design-Rev:` applies to all of them.
func History(doc string, commits []Commit) map[string]Built {
	out := map[string]Built{}
	for _, c := range commits {
		var items, removed []string
		rev := ""
		for _, kv := range Trailers(c.Message) {
			switch {
			case strings.EqualFold(kv[0], TrailerKey):
				if id, ok := itemOf(doc, kv[1]); ok {
					items = append(items, id)
				}
			case strings.EqualFold(kv[0], RemovedKey):
				if id, ok := itemOf(doc, kv[1]); ok {
					removed = append(removed, id)
				}
			case strings.EqualFold(kv[0], RevKey):
				rev = kv[1]
			}
		}
		for _, id := range removed {
			b := out[id]
			if len(b.Commits) == 0 && b.Removed == "" {
				b.Removed = c.SHA
			}
			out[id] = b
		}
		for _, id := range items {
			b := out[id]
			if len(b.Commits) == 0 && b.Removed == "" {
				b.Rev = rev
			}
			b.Commits = append(b.Commits, c.SHA)
			out[id] = b
		}
	}
	return out
}

func itemOf(doc, value string) (string, bool) {
	d, id, ok := strings.Cut(strings.TrimSpace(value), "#")
	if !ok || strings.Trim(d, "/") != strings.Trim(doc, "/") || !idPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

// OpenTask is a task still open for an item: one whose body names
// `<doc>#<ID>`, with the doc revision it was filed against (the
// `Design-Rev:` line the planner writes into every task body).
type OpenTask struct {
	ID  string
	Rev string
}

// Class is what the planner does about one requirement.
type Class string

const (
	// ClassNew is in the doc, unbuilt, and nothing is open for it.
	ClassNew Class = "new"
	// ClassOpen is in the doc and unbuilt, with tasks open against the
	// revision it still has.
	ClassOpen Class = "open"
	// ClassChangedOpen is unbuilt, with tasks open against an older
	// revision of its section.
	ClassChangedOpen Class = "changed, open"
	// ClassChangedDone was built from a revision whose section differs
	// from today's.
	ClassChangedDone Class = "changed, done"
	// ClassDone is built from the section the doc still has.
	ClassDone Class = "done"
	// ClassRemovedOpen left the doc while tasks were open for it.
	ClassRemovedOpen Class = "removed, open"
	// ClassRemovedDone left the doc after it was built, and nothing has
	// taken it out again.
	ClassRemovedDone Class = "removed, done"
	// ClassGone left the doc and has nothing built or open: nothing to do.
	ClassGone Class = "gone"
)

// Item is one requirement's classification.
type Item struct {
	ID    string
	Title string
	Class Class
	// Built is the history for it, Open the tasks open for it.
	Built Built
	Open  []OpenTask
}

// RevisionReader returns the doc as of a commit.
type RevisionReader func(rev string) ([]byte, error)

// Classify sorts every requirement the doc has now, or the history or the
// open tasks name, into one Class -- docs/design-docs.md's table. A
// section counts as changed when its text differs between the revision
// the work was done (or filed) against and current, ignoring whitespace
// and the `{#ID}` marker; work that named no revision is taken to match.
func Classify(current []byte, history map[string]Built, open map[string][]OpenTask, at RevisionReader) ([]Item, error) {
	reqs, err := Requirements(current)
	if err != nil {
		return nil, err
	}
	cache := map[string][]byte{}
	sectionAt := func(rev, id string) (string, bool, error) {
		doc, ok := cache[rev]
		if !ok {
			b, err := at(rev)
			if err != nil {
				return "", false, fmt.Errorf("designdoc: reading the doc at %s: %w", rev, err)
			}
			cache[rev], doc = b, b
		}
		s, ok := Section(doc, id)
		return s, ok, nil
	}
	changed := func(rev, id, now string) (bool, error) {
		if rev == "" {
			return false, nil
		}
		then, ok, err := sectionAt(rev, id)
		if err != nil {
			return false, err
		}
		return !ok || then != now, nil
	}

	var out []Item
	inDoc := map[string]bool{}
	for _, r := range reqs {
		inDoc[r.ID] = true
		now, _ := Section(current, r.ID)
		it := Item{ID: r.ID, Title: r.Title, Built: history[r.ID], Open: open[r.ID]}
		built := len(it.Built.Commits) > 0 && it.Built.Removed == ""
		switch {
		case built:
			c, err := changed(it.Built.Rev, r.ID, now)
			if err != nil {
				return nil, err
			}
			it.Class = ClassDone
			if c {
				it.Class = ClassChangedDone
			}
		case len(it.Open) > 0:
			it.Class = ClassOpen
			for _, t := range it.Open {
				c, err := changed(t.Rev, r.ID, now)
				if err != nil {
					return nil, err
				}
				if c {
					it.Class = ClassChangedOpen
				}
			}
		default:
			it.Class = ClassNew
		}
		out = append(out, it)
	}

	var gone []string
	for id := range history {
		if !inDoc[id] {
			gone = append(gone, id)
		}
	}
	for id := range open {
		if !inDoc[id] && history[id].Commits == nil && history[id].Removed == "" {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	seen := map[string]bool{}
	for _, id := range gone {
		if seen[id] {
			continue
		}
		seen[id] = true
		it := Item{ID: id, Built: history[id], Open: open[id]}
		switch {
		case len(it.Open) > 0:
			it.Class = ClassRemovedOpen
		case len(it.Built.Commits) > 0 && it.Built.Removed == "":
			it.Class = ClassRemovedDone
		default:
			it.Class = ClassGone
		}
		out = append(out, it)
	}
	return out, nil
}

var idMarker = regexp.MustCompile(`\s*\{#[^}\s]+\}`)

// Section is requirement id's text in doc -- its heading through the next
// heading of the same or a higher level -- with the `{#ID}` markers taken
// out and whitespace collapsed, so that two revisions compare equal when
// only an ID was added or the text rewrapped. ok is false when the doc
// has no heading with that ID.
func Section(doc []byte, id string) (string, bool) {
	lines := strings.Split(string(doc), "\n")
	headings := Headings(doc)
	for i, h := range headings {
		if h.ID != id {
			continue
		}
		end := len(lines)
		for _, next := range headings[i+1:] {
			if next.Level <= h.Level {
				end = next.Line - 1
				break
			}
		}
		text := strings.Join(lines[h.Line-1:end], "\n")
		text = idMarker.ReplaceAllString(text, "")
		return strings.Join(strings.Fields(text), " "), true
	}
	return "", false
}
