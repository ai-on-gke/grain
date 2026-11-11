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

// restAttempts is how many times get asks before it gives up on a
// response it counts as transient (restTransient); restBackoff is the
// wait before the second attempt, doubled before each one after it.
// A single 503 from the API used to refuse a release outright
// (run 43048355272), so the gate now rides out a brief outage the way
// a person re-running the job would -- and still refuses, with the
// last error, once the outage outlasts every attempt.
var (
	restAttempts = 5
	restBackoff  = 2 * time.Second
)

// restTransient reports whether a response status is worth asking
// again for: rate limiting and the gateway and server errors GitHub
// returns while it is degraded. Anything else -- 401, 403, 404, 422 --
// says the same thing on every attempt.
func restTransient(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (r REST) get(ctx context.Context, p string, into any) error {
	wait := restBackoff
	var err error
	for attempt := 1; ; attempt++ {
		var retry bool
		retry, err = r.getOnce(ctx, p, into)
		if err == nil || !retry || attempt >= restAttempts || ctx.Err() != nil {
			break
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w (giving up after %d attempts: %v)", err, attempt, ctx.Err())
		case <-t.C:
		}
		wait *= 2
	}
	return err
}

// getOnce makes one request; retry reports whether its error is one a
// second attempt might not meet.
func (r REST) getOnce(ctx context.Context, p string, into any) (retry bool, err error) {
	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+p, nil)
	if err != nil {
		return false, err
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
		// A connection reset or a timeout talking to the API is as
		// passing as a 503 from it.
		return true, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return true, err
	}
	if resp.StatusCode != http.StatusOK {
		return restTransient(resp.StatusCode), fmt.Errorf("GET %s: %s: %s", p, resp.Status, strings.TrimSpace(string(body)))
	}
	return false, json.Unmarshal(body, into)
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
	for page := 1; ; page++ {
		if page > 10 {
			// A change request on a later page would go unseen.
			return nil, fmt.Errorf("pull request #%d has more than 1000 reviews, too many to read", number)
		}
		var items []struct {
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			State             string `json:"state"`
			CommitID          string `json:"commit_id"`
			AuthorAssociation string `json:"author_association"`
		}
		if err := r.get(ctx, fmt.Sprintf("%s/pulls/%d/reviews?per_page=100&page=%d", rp, number, page), &items); err != nil {
			return nil, err
		}
		for _, it := range items {
			out = append(out, Review{Login: it.User.Login, State: it.State, CommitID: it.CommitID, Association: it.AuthorAssociation})
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

// Comparison is the forge's comparison of two commits.
type Comparison struct {
	// Status is "ahead" (head descends from base), "behind", "identical"
	// or "diverged".
	Status string
	// MergeBase is the newest commit both descend from.
	MergeBase string
}

// Compare is the forge's comparison of base and head.
func (r REST) Compare(ctx context.Context, repo, base, head string) (Comparison, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return Comparison{}, err
	}
	var cmp struct {
		Status    string `json:"status"`
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	if err := r.get(ctx, rp+"/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head)+"?per_page=1", &cmp); err != nil {
		return Comparison{}, err
	}
	return Comparison{Status: cmp.Status, MergeBase: cmp.MergeBase.SHA}, nil
}
