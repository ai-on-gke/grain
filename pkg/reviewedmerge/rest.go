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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// REST is a Forge over GitHub's REST API. It reads three endpoints and
// nothing else, so a release job needs only `contents: read` and
// `pull-requests: read`, and a deployment only a read token (or none, for
// a public repository and the API's unauthenticated rate limit).
//
// A repository is owner/name on github.com, or host/owner/name on a
// GitHub Enterprise Server, whose API is https://<host>/api/v3
// (SplitRepo, APIBaseURL).
type REST struct {
	// BaseURL is the API root; when empty, the repository's own
	// (APIBaseURL of its host).
	BaseURL string
	// Token, when set, is sent as a bearer token.
	Token string
	// Client is the HTTP client; one with a 30s timeout when nil.
	Client *http.Client
	// Attempts is how many times a read is tried before its failure is
	// reported; DefaultAttempts when zero.
	Attempts int
	// Backoff is the wait before the first retry, doubled before each
	// one after it; DefaultBackoff when zero.
	Backoff time.Duration
}

// DefaultAttempts and DefaultBackoff are how a REST read is retried: four
// tries, 2s, 4s and 8s apart.
const (
	DefaultAttempts = 4
	DefaultBackoff  = 2 * time.Second
)

// maxRetryAfter caps how long a 429's Retry-After is waited out.
const maxRetryAfter = 60 * time.Second

// StatusError is a forge answer other than 200 OK.
type StatusError struct {
	Path       string
	Status     string
	StatusCode int
	Body       string
	// RetryAfter is the answer's Retry-After, zero when it has none.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GET %s: %s: %s", e.Path, e.Status, e.Body)
}

// transient is whether a failed read is worth trying again: a 5xx, a 429,
// or the request never being answered at all (a connection refused or
// reset, a timeout). Any other 4xx is the forge's answer, and asking again
// gets the same one.
func transient(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode >= 500 || se.StatusCode == http.StatusTooManyRequests
	}
	var ne *networkError
	return errors.As(err, &ne)
}

// networkError is a request that got no complete answer.
type networkError struct{ err error }

func (e *networkError) Error() string { return e.err.Error() }
func (e *networkError) Unwrap() error { return e.err }

// get reads p into into, trying again with backoff while the failure is
// transient. Only how the forge is read is retried: what it answers is
// returned as it is.
func (r REST) get(ctx context.Context, rp repoAPI, p string, into any) error {
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	wait := r.Backoff
	if wait <= 0 {
		wait = DefaultBackoff
	}
	for attempt := 1; ; attempt++ {
		err := r.getOnce(ctx, rp, p, into)
		if err == nil || !transient(err) || ctx.Err() != nil {
			return err
		}
		if attempt >= attempts {
			return fmt.Errorf("after %s: %w", plural(attempts, "attempt"), err)
		}
		delay := wait
		var se *StatusError
		if errors.As(err, &se) && se.RetryAfter > delay {
			delay = min(se.RetryAfter, maxRetryAfter)
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("after %s: %w", plural(attempt, "attempt"), err)
		case <-t.C:
		}
		wait *= 2
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func (r REST) getOnce(ctx context.Context, rp repoAPI, p string, into any) error {
	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" {
		base = APIBaseURL(rp.host)
	}
	p = rp.path + p
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
		return &networkError{err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &networkError{err}
	}
	if resp.StatusCode != http.StatusOK {
		se := &StatusError{Path: p, Status: resp.Status, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
			se.RetryAfter = time.Duration(secs) * time.Second
		}
		return se
	}
	return json.Unmarshal(body, into)
}

// DefaultHost is the host an owner/name repository is on.
const DefaultHost = "github.com"

// SplitRepo splits repo, owner/name or host/owner/name, into its parts;
// host is DefaultHost for owner/name.
func SplitRepo(repo string) (host, owner, name string, err error) {
	parts := strings.Split(repo, "/")
	switch len(parts) {
	case 2:
		host, owner, name = DefaultHost, parts[0], parts[1]
	case 3:
		host, owner, name = strings.ToLower(parts[0]), parts[1], parts[2]
	}
	if host == "" || owner == "" || name == "" {
		return "", "", "", fmt.Errorf("repository %q is not owner/name or host/owner/name", repo)
	}
	return host, owner, name, nil
}

// APIBaseURL is host's REST API root: https://api.github.com for
// github.com, and https://<host>/api/v3 for a GitHub Enterprise Server.
func APIBaseURL(host string) string {
	if host == "" || strings.EqualFold(host, DefaultHost) {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// repoAPI is a repository as the API is asked about it: the host whose
// API answers, and the /repos/owner/name path under it.
type repoAPI struct {
	host, path string
}

func repoPath(repo string) (repoAPI, error) {
	host, owner, name, err := SplitRepo(repo)
	if err != nil {
		return repoAPI{}, err
	}
	return repoAPI{host: host, path: "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)}, nil
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
	if err := r.get(ctx, rp, "/commits/"+url.PathEscape(sha)+"/pulls?per_page=100", &items); err != nil {
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
			if err := r.get(ctx, rp, fmt.Sprintf("/pulls/%d", it.Number), &detail); err != nil {
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
		if err := r.get(ctx, rp, fmt.Sprintf("/pulls/%d/reviews?per_page=100&page=%d", number, page), &items); err != nil {
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
	if err := r.get(ctx, rp, "/git/commits/"+url.PathEscape(sha), &commit); err != nil {
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
	if err := r.get(ctx, rp, "/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head)+"?per_page=1", &cmp); err != nil {
		return Comparison{}, err
	}
	return Comparison{Status: cmp.Status, MergeBase: cmp.MergeBase.SHA}, nil
}
