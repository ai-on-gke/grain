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

package githubsim

import (
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// checkRunPayload is the body of POST .../check-runs and PATCH
// .../check-runs/{id}: the fields grain writes (github.CheckRunWrite).
type checkRunPayload struct {
	Name        string     `json:"name"`
	HeadSHA     string     `json:"head_sha"`
	ExternalID  string     `json:"external_id"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	DetailsURL  string     `json:"details_url"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	Output      *struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"output"`
}

// apply writes p onto cr the way GitHub does: a conclusion completes the
// check run, and a completed one without a completed_at is given now.
func (p checkRunPayload) apply(cr *github.CheckRun, now time.Time) {
	if p.ExternalID != "" {
		cr.ExternalID = p.ExternalID
	}
	if p.Status != "" {
		cr.Status = p.Status
	}
	if p.Conclusion != "" {
		conc := p.Conclusion
		cr.Conclusion = &conc
		cr.Status = "completed"
	}
	if p.DetailsURL != "" {
		cr.HTMLURL = p.DetailsURL
	}
	if p.StartedAt != nil {
		at := p.StartedAt.UTC()
		cr.StartedAt = &at
	}
	if cr.StartedAt == nil {
		at := now.UTC().Truncate(time.Second)
		cr.StartedAt = &at
	}
	if p.CompletedAt != nil {
		at := p.CompletedAt.UTC()
		cr.CompletedAt = &at
	}
	if cr.Status == "completed" && cr.CompletedAt == nil {
		at := now.UTC().Truncate(time.Second)
		cr.CompletedAt = &at
	}
	if p.Output != nil {
		// Served back as output.title and output.summary (checkRunsJSON).
		cr.FailureReason = p.Output.Title
		cr.Output = p.Output.Summary
	}
}
