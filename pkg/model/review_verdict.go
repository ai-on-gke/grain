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
	"strings"
	"time"
)

// PathTriggerHeadTagPrefix starts the tag a gating trigger's review
// carries for the head it was filed to read ("path-trigger-head:<sha>",
// orchestrator's reviewHeadTag). ReviewedHead falls back to it for a
// review filed before LinkReviewedHead was written.
const PathTriggerHeadTagPrefix = "path-trigger-head:"

// ReviewedHead is the head of the change under review that review t was
// filed to read (LinkReviewedHead), or "" for a task that records none --
// a task that is not a review, or a review filed before the head was
// recorded.
func ReviewedHead(t Task) string {
	for _, l := range t.Links {
		if l.Kind == LinkReviewedHead && l.Target != "" {
			return l.Target
		}
	}
	for _, tag := range t.Tags {
		if sha, ok := strings.CutPrefix(tag, PathTriggerHeadTagPrefix); ok && sha != "" {
			return sha
		}
	}
	return ""
}

// SetReviewedHead records sha as the head review t read, replacing any
// head recorded before. An empty sha records nothing.
func SetReviewedHead(t *Task, sha string) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if sha == "" {
		return
	}
	links := t.Links[:0:0]
	for _, l := range t.Links {
		if l.Kind != LinkReviewedHead {
			links = append(links, l)
		}
	}
	t.Links = append(links, Link{Kind: LinkReviewedHead, Target: sha})
}

// ReviewVerdict is how a review's run ended, as far as the change under
// review is concerned.
type ReviewVerdict string

const (
	// VerdictClean is a review that left nothing for anybody: no finding,
	// no question, no fix of its own.
	VerdictClean ReviewVerdict = "clean"
	// VerdictFindings is a review that found something: it left findings
	// on the change (add_review_comment), or pushed fixes of its own.
	VerdictFindings ReviewVerdict = "findings"
	// VerdictQuestions is a review that ended asking a person to decide
	// (ask_question, or a finding it flagged as a question).
	VerdictQuestions ReviewVerdict = "questions"
)

// The categories a review finding is filed under (add_review_comment's
// `category`). A finding that names none, or one not on this list, is
// FindingOther.
const (
	FindingCorrectness   = "correctness"
	FindingTests         = "tests"
	FindingSecurity      = "security"
	FindingCompatibility = "compatibility"
	FindingOther         = "other"
)

// ReviewFindingCategories is every category a finding may name, in the
// order tools and reports list them.
var ReviewFindingCategories = []string{
	FindingCorrectness, FindingTests, FindingSecurity, FindingCompatibility, FindingOther,
}

// NormalizeReviewFindingCategory is category as ReviewFindingCategories
// spells it, or FindingOther for one that is empty or not on the list.
func NormalizeReviewFindingCategory(category string) string {
	category = strings.ToLower(strings.TrimSpace(category))
	for _, c := range ReviewFindingCategories {
		if c == category {
			return c
		}
	}
	return FindingOther
}

// ReviewVerdictRecord is a review's structured verdict
// (LinkReviewVerdict), recorded when a run of the review finishes.
type ReviewVerdictRecord struct {
	Verdict ReviewVerdict `json:"verdict"`
	// FindingCount is how many findings the run left (its
	// add_review_comment calls); Categories has one entry per finding,
	// in the order the run left them.
	FindingCount int      `json:"findingCount"`
	Categories   []string `json:"categories,omitempty"`
	// FixesPushed is whether the run pushed fixes of its own -- to the
	// reviewed branch, or to a stacked branch of its own.
	FixesPushed bool `json:"fixesPushed,omitempty"`
	// Head is the head the review read (ReviewedHead), when it recorded
	// one.
	Head string `json:"head,omitempty"`
	// RunID is the run that reached the verdict.
	RunID string    `json:"runId,omitempty"`
	At    time.Time `json:"at"`
}

// Link encodes r as a LinkReviewVerdict link.
func (r ReviewVerdictRecord) Link() Link {
	r.At = r.At.UTC().Truncate(time.Second)
	b, _ := json.Marshal(r)
	return Link{Kind: LinkReviewVerdict, Target: string(b)}
}

// ReviewVerdictOf is the verdict recorded on review t, if any.
func ReviewVerdictOf(t Task) (ReviewVerdictRecord, bool) {
	for _, l := range t.Links {
		if l.Kind != LinkReviewVerdict {
			continue
		}
		var r ReviewVerdictRecord
		if err := json.Unmarshal([]byte(l.Target), &r); err == nil && r.Verdict != "" {
			return r, true
		}
	}
	return ReviewVerdictRecord{}, false
}

// SetReviewVerdict records r on t, replacing any verdict recorded before.
func SetReviewVerdict(t *Task, r ReviewVerdictRecord) {
	links := t.Links[:0:0]
	for _, l := range t.Links {
		if l.Kind != LinkReviewVerdict {
			links = append(links, l)
		}
	}
	t.Links = append(links, r.Link())
}
