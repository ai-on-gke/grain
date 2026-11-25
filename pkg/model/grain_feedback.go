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
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// GrainFeedback is one proposed improvement to grain itself, gathered by
// the per-repository reflection jobs (RepoReflectionTemplatePrefix) or
// recorded from the CLI when runs lose turns to grain's own CLI, tools,
// capabilities, prompts or sandbox rather than to the target repository.
//
// Stored in its own table rather than in memory_document for three reasons:
//
//   - Prompt cost. MemoryIndex injects every memory document's summary into
//     every task prompt targeting that scope. An operator triage queue is
//     never instruction for coding tasks, so coding runs should pay zero
//     tokens for it.
//   - Isolation from the memory agent. The nightly memory agent audits and
//     rewrites agent-authored memory documents, and granting api#memory to
//     a proposing job lets a confused run touch standing prompt memory.
//     A narrow api#feedback scope avoids both.
//   - Cross-repository deduplication without clobbering human triage. Two
//     repositories hitting the same missing grain flag upsert the same slug,
//     unioning their repos and task IDs and bumping OccurrenceCount while
//     preserving a human-set Status (triaged, filed, rejected).

// GrainFeedbackStatus is the triage lifecycle of one feedback entry.
type GrainFeedbackStatus string

const (
	GrainFeedbackProposed GrainFeedbackStatus = "proposed"
	GrainFeedbackTriaged  GrainFeedbackStatus = "triaged"
	GrainFeedbackFiled    GrainFeedbackStatus = "filed"
	GrainFeedbackRejected GrainFeedbackStatus = "rejected"
)

// ParseGrainFeedbackStatus normalizes s, defaulting "" to proposed.
func ParseGrainFeedbackStatus(s string) (GrainFeedbackStatus, error) {
	switch v := GrainFeedbackStatus(strings.ToLower(strings.TrimSpace(s))); v {
	case "":
		return GrainFeedbackProposed, nil
	case GrainFeedbackProposed, GrainFeedbackTriaged, GrainFeedbackFiled, GrainFeedbackRejected:
		return v, nil
	default:
		return "", fmt.Errorf("status %q: want proposed, triaged, filed or rejected", s)
	}
}

// Open reports whether an entry still awaits upstream filing or dismissal.
func (s GrainFeedbackStatus) Open() bool {
	return s == GrainFeedbackProposed || s == GrainFeedbackTriaged
}

// GrainFeedbackSurface names which part of grain caused the friction.
type GrainFeedbackSurface string

const (
	GrainFeedbackSurfaceCLI        GrainFeedbackSurface = "cli"
	GrainFeedbackSurfaceTool       GrainFeedbackSurface = "tool"
	GrainFeedbackSurfaceCapability GrainFeedbackSurface = "capability"
	GrainFeedbackSurfacePrompt     GrainFeedbackSurface = "prompt"
	GrainFeedbackSurfaceSandbox    GrainFeedbackSurface = "sandbox"
	GrainFeedbackSurfaceOther      GrainFeedbackSurface = "other"
)

// ParseGrainFeedbackSurface normalizes s, defaulting "" to other.
func ParseGrainFeedbackSurface(s string) (GrainFeedbackSurface, error) {
	switch v := GrainFeedbackSurface(strings.ToLower(strings.TrimSpace(s))); v {
	case "":
		return GrainFeedbackSurfaceOther, nil
	case GrainFeedbackSurfaceCLI, GrainFeedbackSurfaceTool, GrainFeedbackSurfaceCapability,
		GrainFeedbackSurfacePrompt, GrainFeedbackSurfaceSandbox, GrainFeedbackSurfaceOther:
		return v, nil
	default:
		return "", fmt.Errorf("surface %q: want cli, tool, capability, prompt, sandbox or other", s)
	}
}

const (
	MaxGrainFeedbackSlugLength  = 64
	MaxGrainFeedbackTitleLength = 200
	MaxGrainFeedbackBodyLength  = 32 * 1024
	MaxGrainFeedbackURLLength   = 512
	MaxGrainFeedbackRefs        = 50
)

// ValidateGrainFeedbackSlug enforces lower-case kebab-case handles, matching
// ValidateMemoryDocumentName so slugs round-trip cleanly in URLs and CLI args.
func ValidateGrainFeedbackSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("a feedback entry needs a slug")
	}
	if len(slug) > MaxGrainFeedbackSlugLength {
		return fmt.Errorf("%q is %d characters; a feedback slug is at most %d",
			slug, len(slug), MaxGrainFeedbackSlugLength)
	}
	if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") || strings.Contains(slug, "--") {
		return fmt.Errorf("%q: hyphens in a feedback slug separate words, so it starts and ends with a letter or digit", slug)
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("%q: a feedback slug must be lower-case letters, digits and single hyphens", slug)
		}
	}
	return nil
}

// ValidateGrainFeedbackURL validates an optional http(s) link recorded when an
// operator marks an entry as filed upstream.
func ValidateGrainFeedbackURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", nil
	}
	if len(u) > MaxGrainFeedbackURLLength {
		return "", fmt.Errorf("upstream URL is %d characters; at most %d allowed", len(u), MaxGrainFeedbackURLLength)
	}
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("upstream URL %q must be an http:// or https:// URL", raw)
	}
	return u, nil
}

// GrainFeedback is one stored Grain improvement proposal.
type GrainFeedback struct {
	Slug            string               `json:"slug"`
	Title           string               `json:"title"`
	Surface         GrainFeedbackSurface `json:"surface"`
	Status          GrainFeedbackStatus  `json:"status"`
	Body            string               `json:"body,omitempty"`
	Length          int                  `json:"length"`
	UpstreamURL     string               `json:"upstreamUrl,omitempty"`
	Repos           []string             `json:"repos,omitempty"`
	TaskIDs         []string             `json:"taskIds,omitempty"`
	OccurrenceCount int                  `json:"occurrenceCount"`
	FirstSeenAt     time.Time            `json:"firstSeenAt"`
	LastSeenAt      time.Time            `json:"lastSeenAt"`
	UpdatedAt       time.Time            `json:"updatedAt"`
	UpdatedBy       Principal            `json:"updatedBy"`
}

// GrainFeedbackRecord is an agent or CLI submission recording an observation
// of a Grain-surface friction item.
type GrainFeedbackRecord struct {
	Slug    string
	Title   string
	Surface GrainFeedbackSurface
	Body    string
	Repos   []string
	TaskIDs []string
	Now     time.Time
	By      Principal
}

// GrainFeedbackPatch is a human triage edit on an existing entry.
type GrainFeedbackPatch struct {
	Title       *string
	Surface     *GrainFeedbackSurface
	Status      *GrainFeedbackStatus
	Body        *string
	UpstreamURL *string
	Now         time.Time
	By          Principal
}

// normalizeFeedbackRefs cleans, deduplicates, sorts and caps a string slice
// (repository names or task IDs) so repeated observations stay bounded.
func normalizeFeedbackRefs(existing, incoming []string, norm func(string) string) []string {
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, raw := range existing {
		v := strings.TrimSpace(raw)
		if norm != nil {
			v = norm(v)
		}
		if v != "" && !slices.Contains(merged, v) {
			merged = append(merged, v)
		}
	}
	for _, raw := range incoming {
		v := strings.TrimSpace(raw)
		if norm != nil {
			v = norm(v)
		}
		if v != "" && !slices.Contains(merged, v) {
			merged = append(merged, v)
		}
	}
	if len(merged) > MaxGrainFeedbackRefs {
		merged = merged[len(merged)-MaxGrainFeedbackRefs:]
	}
	slices.Sort(merged)
	return merged
}

const grainFeedbackTable = `CREATE TABLE IF NOT EXISTS ` + "`grain_feedback`" + ` (
  ` + "`slug`" + `              TEXT     NOT NULL,
  ` + "`title`" + `             TEXT     NOT NULL DEFAULT '',
  ` + "`surface`" + `           TEXT     NOT NULL DEFAULT 'other',
  ` + "`status`" + `            TEXT     NOT NULL DEFAULT 'proposed',
  ` + "`body`" + `              TEXT     NOT NULL DEFAULT '',
  ` + "`upstream_url`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`repos_json`" + `        TEXT     NOT NULL DEFAULT '[]',
  ` + "`task_ids_json`" + `     TEXT     NOT NULL DEFAULT '[]',
  ` + "`occurrence_count`" + `  INTEGER  NOT NULL DEFAULT 1,
  ` + "`first_seen_at`" + `     DATETIME NOT NULL,
  ` + "`last_seen_at`" + `      DATETIME NOT NULL,
  ` + "`updated_at`" + `        DATETIME NOT NULL,
  ` + "`updated_by_kind`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`updated_by_id`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`updated_by_source`" + ` TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`slug`" + `)
)`
