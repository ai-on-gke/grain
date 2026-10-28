// Package designdoc reads what a design-doc planner works from
// (docs/design-docs.md): the requirement IDs a design doc's headings
// carry, and the commit trailers that record which of them are built.
//
// There is no ledger. What has been built is read from git history:
// every implementing commit carries `Design-Item: <doc>#<ID>` and
// `Design-Rev: <sha>` (the doc revision it implemented), and a commit
// that takes a requirement back out carries `Design-Removed: <doc>#<ID>`.
// Classify turns that history, the open tasks that name an item, and
// the doc as it is now into the new / changed / done / removed answer
// the planner acts on.
package designdoc

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Commit trailers a design doc's implementing commits carry.
const (
	// TrailerKey names the requirement a commit implements:
	// `Design-Item: docs/design/foo/README.md#D-7`.
	TrailerKey = "Design-Item"
	// RevKey names the doc revision that commit implemented, so a later
	// revision whose section differs reads as changed:
	// `Design-Rev: 0123abc...`.
	RevKey = "Design-Rev"
	// RemovedKey names a requirement a commit took back out, after it
	// left the doc: `Design-Removed: docs/design/foo/README.md#D-7`.
	RemovedKey = "Design-Removed"
)

// idPattern is a requirement ID: a capitalised prefix, a dash, a number.
// `D-7`, `API-12`.
var idPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)

// Trailer is the `Design-Item:` trailer for one item of doc.
func Trailer(doc, id string) string {
	return fmt.Sprintf("%s: %s#%s", TrailerKey, doc, id)
}

// Trailers is every `Key: value` trailer line in a commit message's last
// paragraph, in order. Keys are matched case-insensitively by callers;
// they are returned as written.
func Trailers(message string) [][2]string {
	paras := strings.Split(strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n")), "\n\n")
	if len(paras) < 2 {
		return nil
	}
	var out [][2]string
	sc := bufio.NewScanner(strings.NewReader(paras[len(paras)-1]))
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.ContainsAny(key, " \t") || key == "" {
			continue
		}
		out = append(out, [2]string{key, strings.TrimSpace(value)})
	}
	return out
}

// Requirement is one heading of a design doc.
type Requirement struct {
	// ID is the heading's `{#ID}`, or "" for a heading that has none yet.
	ID    string
	Title string
	// Level is the heading's depth: 3 for `###`.
	Level int
	// Line is 1-based.
	Line int
}

var heading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)
var headingID = regexp.MustCompile(`\s*\{#([^}\s]+)\}$`)

// Headings is every ATX heading in a markdown doc outside fenced code,
// with the ID it carries if it carries one. Which headings are
// requirements is the doc's to say -- the planner assigns IDs to those
// that should have one -- so this returns them all and leaves the choice
// to the caller.
func Headings(doc []byte) []Requirement {
	var out []Requirement
	fence := ""
	for i, line := range strings.Split(string(doc), "\n") {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		m := heading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		r := Requirement{Title: strings.TrimRight(m[2], "# "), Level: len(m[1]), Line: i + 1}
		if id := headingID.FindStringSubmatch(r.Title); id != nil {
			r.ID = id[1]
			r.Title = strings.TrimSpace(headingID.ReplaceAllString(r.Title, ""))
		}
		out = append(out, r)
	}
	return out
}

// Requirements is the headings of doc that carry an ID, checked: every
// ID shaped like D-7 and none repeated.
func Requirements(doc []byte) ([]Requirement, error) {
	var out []Requirement
	var errs []error
	seen := map[string]int{}
	for _, h := range Headings(doc) {
		if h.ID == "" {
			continue
		}
		if !idPattern.MatchString(h.ID) {
			errs = append(errs, fmt.Errorf("line %d: {#%s} is not shaped like {#D-7}", h.Line, h.ID))
			continue
		}
		if first, ok := seen[h.ID]; ok {
			errs = append(errs, fmt.Errorf("line %d: {#%s} is already used on line %d", h.Line, h.ID, first))
			continue
		}
		seen[h.ID] = h.Line
		out = append(out, h)
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("designdoc: %w", errors.Join(errs...))
	}
	return out, nil
}

// NextID is the next free ID with prefix (`D`), given the IDs already
// used -- in the doc, and in every `Design-Item:` trailer on the branch
// (an item removed from the doc keeps its ID taken), so an ID is never
// reused for a different requirement.
func NextID(prefix string, used ...[]string) string {
	highest := 0
	for _, ids := range used {
		for _, id := range ids {
			n, ok := strings.CutPrefix(id, prefix+"-")
			if !ok {
				continue
			}
			if v, err := strconv.Atoi(n); err == nil && v > highest {
				highest = v
			}
		}
	}
	return fmt.Sprintf("%s-%d", prefix, highest+1)
}
