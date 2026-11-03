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

package github

// What a release on GitHub Actions needs of the forge connection
// (design/playbook/releases.playbook.md §0.2, gap 4): the deployment
// reads a repository's release tags to notice a vX.Y.Z-pending one --
// the tag is the state -- and, on a person's Approve in the Inbox,
// dispatches the workflow that holds the publishing credentials. Both
// are optional capabilities of a Client, asked for by type assertion, so
// a deployment whose forge connection has neither raises nothing and
// dispatches nothing.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// TagRef is one tag: its name (without refs/tags/) and the object it
// points at -- a tag object for an annotated tag, a commit for a
// lightweight one.
type TagRef struct {
	Name       string
	ObjectSHA  string
	ObjectType string
}

// AnnotatedTag is a tag object: the commit it tags and its message.
type AnnotatedTag struct {
	Commit  string
	Message string
}

// ReleaseTagReader reads a repository's tags.
type ReleaseTagReader interface {
	// ListMatchingTags is every tag whose name starts with prefix.
	ListMatchingTags(owner, repo, prefix string) ([]TagRef, error)
	// GetAnnotatedTag is the tag object sha, or nil when there is none.
	GetAnnotatedTag(owner, repo, sha string) (*AnnotatedTag, error)
}

// WorkflowDispatcher starts a workflow_dispatch run.
type WorkflowDispatcher interface {
	// DispatchWorkflow runs workflow (a file name under
	// .github/workflows, or its id) at ref with inputs.
	DispatchWorkflow(owner, repo, workflow, ref string, inputs map[string]string) error
}

var (
	_ ReleaseTagReader   = (*RESTClient)(nil)
	_ WorkflowDispatcher = (*RESTClient)(nil)
)

// ListMatchingTags reads refs/tags/<prefix>* with the matching-refs API, every
// page of it.
func (c *RESTClient) ListMatchingTags(owner, repo, prefix string) ([]TagRef, error) {
	var out []TagRef
	path := fmt.Sprintf("/repos/%s/%s/git/matching-refs/tags/%s?per_page=100", owner, repo, quoteKeepSlash(prefix))
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != http.StatusOK {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var page []struct {
			Ref    string `json:"ref"`
			Object struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			} `json:"object"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, fmt.Errorf("reading %s/%s's tags: %w", owner, repo, err)
		}
		for _, r := range page {
			name, ok := strings.CutPrefix(r.Ref, "refs/tags/")
			if !ok {
				continue
			}
			out = append(out, TagRef{Name: name, ObjectSHA: r.Object.SHA, ObjectType: r.Object.Type})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return out, nil
}

// GetAnnotatedTag reads tag object sha.
func (c *RESTClient) GetAnnotatedTag(owner, repo, sha string) (*AnnotatedTag, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/git/tags/%s", owner, repo, quoteAll(sha)))
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusNotFound {
		return nil, nil
	}
	if resp.Status != http.StatusOK {
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Message string `json:"message"`
		Object  struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	if data.Object.Type != "" && data.Object.Type != "commit" {
		return nil, fmt.Errorf("tag object %s tags a %s, not a commit", sha, data.Object.Type)
	}
	return &AnnotatedTag{Commit: data.Object.SHA, Message: data.Message}, nil
}

// DispatchWorkflow POSTs .../actions/workflows/{workflow}/dispatches.
// GitHub answers 204 with nothing to say about the run it starts.
func (c *RESTClient) DispatchWorkflow(owner, repo, workflow, ref string, inputs map[string]string) error {
	payload, err := json.Marshal(struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs,omitempty"`
	}{ref, inputs})
	if err != nil {
		return err
	}
	resp, err := c.request("POST", fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches", owner, repo, quoteAll(workflow)),
		c.headers(owner, repo, true), payload)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusNoContent && resp.Status != http.StatusOK {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// TagCommit is the commit tag name points at in owner/repo, peeling an
// annotated tag; "" when there is no such tag.
func TagCommit(r ReleaseTagReader, owner, repo, name string) (string, error) {
	tags, err := r.ListMatchingTags(owner, repo, name)
	if err != nil {
		return "", err
	}
	for _, t := range tags {
		if t.Name != name {
			continue
		}
		if t.ObjectType != "tag" {
			return t.ObjectSHA, nil
		}
		at, err := r.GetAnnotatedTag(owner, repo, t.ObjectSHA)
		if err != nil {
			return "", err
		}
		if at == nil {
			return "", nil
		}
		return at.Commit, nil
	}
	return "", nil
}
