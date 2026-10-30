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
	"context"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// IssueImporter imports one repository's GitHub issues now, the manual
// trigger beside the orchestrator's periodic one (orchestrator.ImportIssues,
// which the daemon wires in as Config.IssueImporter).
type IssueImporter func(ctx context.Context, repo model.RepoRef) (model.IssueImportResult, error)

// cleanIssueImportRepo is the issue importer's source repository as it is
// stored: "" for the repository itself, and otherwise owner/name in its one
// written form. It is not spelled the way the deployment's target repos
// spell it (parseRepo), since the source is usually not one of them.
func (c *Client) cleanIssueImportRepo(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	parsed, err := model.ParseRepo(text)
	if err != nil {
		return "", validationErrorf("issueImportRepo: %v", err)
	}
	return parsed.String(), nil
}

// ImportIssues imports repo's open GitHub issues now
// (POST /api/repos/{owner}/{name}/import-issues). It needs the importer
// switched on for repo, and files what it imports exactly as the periodic
// pass would: unapproved unless the repository's settings say approved.
func (c *Client) ImportIssues(ctx context.Context, repo string) (model.IssueImportResult, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return model.IssueImportResult{}, validationErrorf("repo: %v", err)
	}
	if err := c.checkRepoPerson(ctx, parsed); err != nil {
		return model.IssueImportResult{}, err
	}
	if c.Config.IssueImporter == nil {
		return model.IssueImportResult{}, validationErrorf("this deployment cannot import GitHub issues: it has no GitHub client")
	}
	cfg, err := c.Store.GetRepoConfig(ctx, parsed)
	if err != nil {
		return model.IssueImportResult{}, err
	}
	if cfg == nil || !cfg.IssueImport.Enabled {
		return model.IssueImportResult{}, validationErrorf("issue import is not switched on for %s: turn it on in the repository's settings first", parsed)
	}
	return c.Config.IssueImporter(ctx, parsed)
}

func (s *Server) handleImportIssues(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.ImportIssues(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"))
	if err != nil && out.Source == "" {
		writeClientError(w, err)
		return
	}
	// A pass that filed some issues and failed on others answers what it
	// did, with the failure beside it, rather than hiding the tasks it
	// filed behind a status code.
	resp := struct {
		model.IssueImportResult
		Error string `json:"error,omitempty"`
	}{IssueImportResult: out}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
