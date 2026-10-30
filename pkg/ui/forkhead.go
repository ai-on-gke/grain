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
	"errors"
	"strings"

	"github.com/ai-on-gke/grain/pkg/forkhead"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ForkPullRequests is the slice of the daemon's GitHub client that turns
// a fork head label ("andrzejfudro:chore/bump-agy-1.4.9", model.ForkHead)
// typed as a task's branch into the pull request it heads.
type ForkPullRequests = forkhead.Finder

// splitForkHead reads value, a branch field of a request, as a fork head
// label. ok is false for a plain branch name, which the caller validates
// as it always has; a value with a ':' that is not a well-formed label is
// refused here, with what is wrong with it rather than the generic "not a
// usable branch name".
func splitForkHead(field, value string) (model.ForkHead, bool, error) {
	head, ok, err := model.ParseForkHead(strings.TrimSpace(value))
	if err != nil {
		return model.ForkHead{}, true, validationErrorf("%s: %v", field, err)
	}
	return head, ok, nil
}

// resolveForkHead resolves head against repo (forkhead.Resolve). No open
// pull request is a validation error naming the label; so is a task with
// no repo, or a deployment with no GitHub client to ask.
func (c *Client) resolveForkHead(field string, repo *model.RepoRef, head model.ForkHead) (*forkhead.PR, error) {
	if repo == nil {
		return nil, validationErrorf("%s %q is a GitHub fork head (<owner>:<branch>), which names a pull request "+
			"on a repository: this task has none", field, head)
	}
	if c.Config.GitHubForkPullRequests == nil {
		return nil, validationErrorf("%s %q is a GitHub fork head (<owner>:<branch>), but this deployment cannot read "+
			"%s's pull requests to find the one it heads: name a branch of %s instead", field, head, repo, repo)
	}
	r, err := forkhead.Resolve(c.Config.GitHubForkPullRequests, *repo, head)
	if errors.Is(err, forkhead.ErrNoPullRequest) {
		return nil, validationErrorf("%s %v", field, err)
	}
	return r, err
}

// hasLinkTo reports whether links already holds want.
func hasLinkTo(links []model.Link, want model.Link) bool {
	for _, l := range links {
		if l.Kind == want.Kind && l.Target == want.Target {
			return true
		}
	}
	return false
}
