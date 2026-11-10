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

package reviewedmerge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// REST is a Forge over GitHub's REST API. It reads three endpoints and
// nothing else, so a release job needs only `contents: read` and
// `pull-requests: read`, and a deployment only a read token (or none, for
// a public repository and the API's unauthenticated rate limit).
type REST struct {
	// BaseURL is the API root; https://api.github.com when empty.
	BaseURL string
	// Token, when set, is sent as a bearer token.
	Token string
	// Client is the HTTP client; one with a 30s timeout when nil.
	Client *http.Client
}

func (r REST) get(ctx context.Context, p string, into any) error {
	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+p, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", p, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, into)
}

func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("repository %q is not owner/name", repo)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

// PullsForCommit implements Forge.
func (r REST) PullsForCommit(ctx context.Context, repo, sha string) ([]Pull, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var items []struct {
		Number         int     `json:"number"`
		State          string  `json:"state"`
		MergedAt       *string `json:"merged_at"`
		MergeCommitSHA string  `json:"merge_commit_sha"`
		Base           struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := r.get(ctx, rp+"/commits/"+url.PathEscape(sha)+"/pulls?per_page=100", &items); err != nil {
		return nil, err
	}
	var pulls []Pull
	for _, it := range items {
		pr := Pull{
			Number: it.Number, State: it.State,
			Merged:         it.MergedAt != nil && *it.MergedAt != "",
			MergeCommitSHA: it.MergeCommitSHA, Base: it.Base.Ref, HeadSHA: it.Head.SHA, Author: it.User.Login,
		}
		if pr.Merged && pr.MergeCommitSHA == sha {
			// The list does not say who merged; the pull request does.
			var detail struct {
				MergedBy *struct {
					Login string `json:"login"`
				} `json:"merged_by"`
			}
			if err := r.get(ctx, fmt.Sprintf("%s/pulls/%d", rp, it.Number), &detail); err != nil {
				return nil, err
			}
			if detail.MergedBy != nil {
				pr.MergedBy = detail.MergedBy.Login
			}
		}
		pulls = append(pulls, pr)
	}
	return pulls, nil
}

// Reviews implements Forge. GitHub returns reviews oldest first.
func (r REST) Reviews(ctx context.Context, repo string, number int) ([]Review, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out []Review
	for page := 1; page <= 10; page++ {
		var items []struct {
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			State    string `json:"state"`
			CommitID string `json:"commit_id"`
		}
		if err := r.get(ctx, fmt.Sprintf("%s/pulls/%d/reviews?per_page=100&page=%d", rp, number, page), &items); err != nil {
			return nil, err
		}
		for _, it := range items {
			out = append(out, Review{Login: it.User.Login, State: it.State, CommitID: it.CommitID})
		}
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}

// FirstParent implements Forge.
func (r REST) FirstParent(ctx context.Context, repo, sha string) (string, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	var commit struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := r.get(ctx, rp+"/git/commits/"+url.PathEscape(sha), &commit); err != nil {
		return "", err
	}
	if len(commit.Parents) == 0 {
		return "", nil
	}
	return commit.Parents[0].SHA, nil
}
