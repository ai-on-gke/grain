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
	"net/url"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// provenanceLinks turns CreateTaskRequest's Designs and DerivedFrom into
// the links they record. A design is a repository-relative path under
// design/ (or docs/); a derived-from reference is owner/name#number, or
// a pull request URL, which is stored in that same short form so one
// spelling filters (`grain list -filter derived_from:eq:...`).
func provenanceLinks(designs, derivedFrom []string) ([]model.Link, error) {
	var links []model.Link
	for _, raw := range designs {
		p := strings.TrimPrefix(strings.TrimSpace(raw), "./")
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, "\n\r") {
			return nil, validationErrorf("design %q is not a repository-relative path such as design/spec/x/x.spec.md", raw)
		}
		links = append(links, model.Link{Kind: model.LinkDesign, Target: p})
	}
	for _, raw := range derivedFrom {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ref, ok := NormalizePullRequestRef(raw)
		if !ok {
			return nil, validationErrorf("derived-from %q is not a pull request: write owner/name#number or its URL", raw)
		}
		links = append(links, model.Link{Kind: model.LinkDerivedFrom, Target: ref})
	}
	return links, nil
}

// NormalizePullRequestRef reads a pull request named as owner/name#number,
// host/owner/name#number or https://host/owner/name/pull/number, and
// answers it as owner/name#number (host/owner/name#number off the default
// github.com), the form model.LinkDerivedFrom stores.
func NormalizePullRequestRef(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		u, err := url.Parse(text)
		if err != nil {
			return "", false
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 4 || (parts[2] != "pull" && parts[2] != "pulls") {
			return "", false
		}
		if _, err := strconv.Atoi(parts[3]); err != nil {
			return "", false
		}
		repo := parts[0] + "/" + parts[1]
		if host := strings.ToLower(u.Host); host != "" && host != "github.com" && host != "www.github.com" {
			repo = host + "/" + repo
		}
		return repo + "#" + parts[3], true
	}
	repo, num, ok := strings.Cut(text, "#")
	if !ok || strings.Count(repo, "/") < 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", false
	}
	if _, err := strconv.Atoi(num); err != nil {
		return "", false
	}
	repo = strings.TrimPrefix(strings.TrimPrefix(repo, "github.com/"), "www.github.com/")
	return repo + "#" + num, true
}
