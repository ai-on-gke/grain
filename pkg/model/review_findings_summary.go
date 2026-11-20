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
	"fmt"
	"regexp"
	"strings"
)

// FindingSummaryItem is one review finding as a findings summary reads
// it: where it is, and what it says.
type FindingSummaryItem struct {
	Path string
	Line int
	Body string
}

// maxFindingSentence bounds the first finding's sentence in a summary,
// which is one line of a merge gate.
const maxFindingSentence = 120

// FormatFindingsSummary is a review's findings on one line, the way a
// merge gate's detail and ReviewFindingsRecord.Summary carry them: the
// count, then the first finding's location and first sentence --
// "3 findings: `pkg/x.go:12` The lock is never released." -- so the line
// says what was found rather than only that something was. Empty for no
// findings, or none with any text.
func FormatFindingsSummary(findings []FindingSummaryItem) string {
	var first *FindingSummaryItem
	n := 0
	for i := range findings {
		if strings.TrimSpace(findings[i].Body) == "" {
			continue
		}
		n++
		if first == nil {
			first = &findings[i]
		}
	}
	if first == nil {
		return ""
	}
	var b strings.Builder
	if n == 1 {
		b.WriteString("1 finding: ")
	} else {
		fmt.Fprintf(&b, "%d findings: ", n)
	}
	if p := strings.TrimSpace(first.Path); p != "" {
		if first.Line > 0 {
			p = fmt.Sprintf("%s:%d", p, first.Line)
		}
		b.WriteString("`" + p + "` ")
	}
	b.WriteString(FirstSentence(first.Body))
	return b.String()
}

// SummaryFromClosingNote is a findings summary written from a review's
// closing note, for a review whose structured findings were never
// recorded: "review 5381: <the note's first sentence>". Empty when the
// note says nothing.
func SummaryFromClosingNote(reviewTask, note string) string {
	s := FirstSentence(note)
	if s == "" {
		return ""
	}
	if reviewTask == "" {
		return s
	}
	return "review " + reviewTask + ": " + s
}

// bareFindingsSummary matches the summaries grain used to record with no
// finding's text in them at all.
var bareFindingsSummary = regexp.MustCompile(`^review(?: \S+)? left findings$`)

// IsBareFindingsSummary reports whether s is one of the placeholder
// summaries ("review 5381 left findings") that name a review but none of
// what it found -- the ones BackfillFindingsSummary replaces.
func IsBareFindingsSummary(s string) bool {
	return bareFindingsSummary.MatchString(strings.TrimSpace(s))
}

// FirstSentence is text's first sentence on one line: the first line
// with words on it (a markdown heading is skipped when anything follows
// it), with list and emphasis markers trimmed, cut at the end of its
// first sentence and bounded to maxFindingSentence runes.
func FirstSentence(text string) string {
	var line, heading string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "```") {
			continue
		}
		if strings.HasPrefix(l, "#") {
			if heading == "" {
				heading = strings.TrimSpace(strings.TrimLeft(l, "#"))
			}
			continue
		}
		line = l
		break
	}
	if line == "" {
		line = heading
	}
	for _, marker := range []string{"- ", "* ", "+ ", "> "} {
		line = strings.TrimSpace(strings.TrimPrefix(line, marker))
	}
	if line == "" {
		return ""
	}
	for i := 0; i+1 < len(line); i++ {
		if (line[i] == '.' || line[i] == '?' || line[i] == '!') && line[i+1] == ' ' {
			line = line[:i+1]
			break
		}
	}
	if r := []rune(line); len(r) > maxFindingSentence {
		line = string(r[:maxFindingSentence-1]) + "…"
	}
	return line
}

// LatestClosingNote is the newest comment taskID's own runs posted in its
// conversation, bounded the way a wait target's closing note is -- the
// agent's own words, never a person's.
func (s *Store) LatestClosingNote(ctx context.Context, taskID string) (string, error) {
	return s.latestClosingNote(ctx, taskID)
}

// ReplaceReviewFindingsSummary rewrites the summary of every findings
// record on t that review reviewTask left with a bare placeholder summary
// (IsBareFindingsSummary), keeping everything else about the record, and
// reports whether any changed. Unlike AddReviewFindings it touches
// nothing else on t: it corrects what a record says, it does not record
// new findings.
func ReplaceReviewFindingsSummary(t *Task, reviewTask, summary string) bool {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false
	}
	changed := false
	for i, l := range t.Links {
		if l.Kind != LinkReviewFindings {
			continue
		}
		r, ok := ParseReviewFindingsRecord(l.Target)
		if !ok || !IsBareFindingsSummary(r.Summary) || BareFindingsReviewTask(r) != reviewTask {
			continue
		}
		r.Summary = summary
		t.Links[i] = r.Link()
		changed = true
	}
	return changed
}

// BareFindingsReviewTask is the review a findings record is from: its
// ReviewTask, or for an older record that left that empty, the one its
// placeholder summary names ("review 5381 left findings").
func BareFindingsReviewTask(r ReviewFindingsRecord) string {
	if r.ReviewTask != "" {
		return r.ReviewTask
	}
	f := strings.Fields(strings.TrimSpace(r.Summary))
	if len(f) == 4 && f[0] == "review" {
		return f[1]
	}
	return ""
}
