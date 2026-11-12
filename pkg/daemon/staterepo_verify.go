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

package daemon

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// require_verified mode's wiring (staterepo/verify.go, task 3973): the
// Verifier an external config repository is pulled through, built from
// <data-dir>/state-repo.json -- this host's file, never the repository's.

// stateRepoVerifier is settings' Verifier, or nil when require_verified is
// off or the repository is not external.
func stateRepoVerifier(dataDir string, settings staterepo.Settings) *staterepo.Verifier {
	if !settings.RequireVerified || !settings.IsExternal() {
		return nil
	}
	v := &staterepo.Verifier{
		AllowedSignersFile: settings.AllowedSignersFile,
		RequiredApprovals:  settings.RequiredApprovals,
		Mergers:            splitList(settings.VerifiedMergers),
		Accepted:           splitList(settings.AcceptedCommits),
	}
	// Forge reviews are read from GitHub -- github.com, or the forge this
	// deployment names with -github-host -- and only with a credential
	// this deployment would send there: the same rule RepoToken applies.
	if owner, repo, ok := repoFromRemote(settings.Remote); ok {
		transport := forgeReviewTransport(settings.Remote)
		if transport == nil {
			return v
		}
		branch := settings.Branch
		if branch == "" {
			branch = staterepo.DefaultBranch
		}
		client := github.NewClient(transport,
			stateRepoTokens{RepoToken(dataDir, settings.Remote, settings.TokenFile)})
		v.Reviews = forgeReviews{client: client, owner: owner, repo: repo, branch: branch}
	}
	return v
}

// stateRepoForge is this deployment's own forge host, the daemon's
// -github-host and -github-insecure-http, which Run sets before the state
// repository opens and again once the stored configuration is loaded.
var stateRepoForge atomic.Pointer[forgeHost]

type forgeHost struct {
	host         string
	insecureHTTP bool
}

// setStateRepoForge records the deployment's forge host for
// forgeReviewTransport.
func setStateRepoForge(host string, insecureHTTP bool) {
	stateRepoForge.Store(&forgeHost{host: strings.TrimSpace(host), insecureHTTP: insecureHTTP})
}

// initialStateRepoForge is the forge host in effect before loadStateRepo pulls
// an external config repository: the stored grain_config row's when one
// already exists (the same precedence LoadConfig applies once a deployment has
// been seeded, since -github-host and -github-insecure-http are seed-only
// flags), and flagCfg's otherwise.
func initialStateRepoForge(ctx context.Context, store *model.Store, flagCfg Config) (string, bool) {
	if store != nil {
		if stored, err := store.GetConfig(ctx); err == nil && stored != nil && !stored.ConnectionsOnly() {
			return stored.GitHubHost, stored.GitHubInsecureHTTP
		}
	}
	return flagCfg.GithubHost, flagCfg.GithubInsecureHTTP
}

// forgeReviewTransport is what require_verified reads a remote's pull
// requests over, or nil when grain cannot ask anybody about them: GitHub's
// API for an https://github.com remote, and the deployment's own
// -github-host (when its REST API is served at the host root, as github.APIHost
// assumes) for a remote on exactly that host -- spoken over plain HTTP only
// on loopback when -github-insecure-http says so (ValidateForwardTarget), and
// never downgrading an https:// remote to plain HTTP. Before this, every
// remote off github.com was nil, so a deployment pointed at its own forge
// could never verify an approved or grain-merged pull request, only signatures
// and acceptances (docs/test-runs/2026-11-11-external-config-verify.md, F1).
// A remote on any other host is still nil: nothing is asked of a host the
// operator did not name.
func forgeReviewTransport(remote string) *github.RealTransport {
	if _, _, ok := repoFromRemote(remote); !ok {
		return nil
	}
	if ladderMaySendTo(remote) {
		return github.NewRealTransport("github.com")
	}
	f := stateRepoForge.Load()
	if f == nil || f.host == "" || strings.EqualFold(f.host, "github.com") {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || u.User != nil || !strings.EqualFold(u.Host, f.host) {
		return nil
	}
	var useTLS bool
	switch strings.ToLower(u.Scheme) {
	case "https":
		useTLS = true
	case "http":
		if !f.insecureHTTP {
			return nil
		}
	default:
		return nil
	}
	if gitproxy.ValidateForwardTarget(f.host, useTLS) != nil {
		return nil
	}
	t := github.NewRealTransport(f.host)
	t.UseTLS = useTLS
	return t
}

// stateRepoTokens is RepoToken as a github.TokenSource.
type stateRepoTokens struct {
	token func(context.Context) (string, error)
}

func (s stateRepoTokens) TokenFor(_, _ string) *string {
	t, err := s.token(context.Background())
	if err != nil || t == "" {
		return nil
	}
	return &t
}

// forgeReviews is staterepo.ForgeReviews over the GitHub API.
type forgeReviews struct {
	client              *github.RESTClient
	owner, repo, branch string
}

func (f forgeReviews) MergedReview(_ context.Context, sha string) (*staterepo.ForgeReview, error) {
	r, err := f.client.MergedPullRequestReview(f.owner, f.repo, f.branch, sha)
	if err != nil || r == nil {
		return nil, err
	}
	return &staterepo.ForgeReview{Number: r.Number, URL: r.URL, Author: r.Author, MergedBy: r.MergedBy, Approvers: r.Approvers}, nil
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		out = append(out, f)
	}
	return out
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// maxAcceptedCommits bounds the list an operator's acceptances build up.
const maxAcceptedCommits = 20

// applyVerification writes req's require_verified fields into settings.
// remoteChanged drops acceptances, which named another repository's
// commits.
func applyVerification(dataDir string, settings *staterepo.Settings, req ui.StateRepoRemoteRequest, remoteChanged bool) error {
	if remoteChanged {
		settings.AcceptedCommits = ""
	}
	if req.RequireVerified != nil {
		settings.RequireVerified = *req.RequireVerified
	}
	if req.RequiredApprovals != nil {
		if n := *req.RequiredApprovals; n < 0 || n > 20 {
			return fmt.Errorf("requiredApprovals must be between 0 and 20, not %d", n)
		}
		settings.RequiredApprovals = *req.RequiredApprovals
	}
	if req.VerifiedMergers != nil {
		settings.VerifiedMergers = strings.Join(splitList(*req.VerifiedMergers), ",")
	}
	if req.AllowedSigners != nil {
		text := strings.TrimSpace(*req.AllowedSigners)
		if text == "" {
			settings.AllowedSignersFile = ""
		} else {
			for i, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") && len(strings.Fields(line)) < 3 {
					return fmt.Errorf("allowedSigners line %d is not an ssh allowed_signers entry "+
						"(principal, then key type and key, as `ssh-keygen -Y verify` reads them): %q", i+1, line)
				}
			}
			path := filepath.Join(dataDir, "state-repo-allowed-signers")
			if err := os.WriteFile(path, []byte(text+"\n"), 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}
			settings.AllowedSignersFile = path
		}
	}
	if sha := strings.TrimSpace(req.AcceptCommit); sha != "" {
		if !commitSHA.MatchString(sha) {
			return fmt.Errorf("acceptCommit must be a full 40-character commit SHA, not %q", sha)
		}
		accepted := splitList(settings.AcceptedCommits)
		accepted = append(accepted, sha)
		if len(accepted) > maxAcceptedCommits {
			accepted = accepted[len(accepted)-maxAcceptedCommits:]
		}
		settings.AcceptedCommits = strings.Join(accepted, ",")
	}
	return nil
}

// verificationStatus is the State pane's account of require_verified mode.
func verificationStatus(settings staterepo.Settings, repo *staterepo.Repo) *ui.StateRepoVerification {
	if !settings.IsExternal() {
		return nil
	}
	out := &ui.StateRepoVerification{
		RequireVerified:   settings.RequireVerified,
		RequiredApprovals: max(settings.RequiredApprovals, 1),
		VerifiedMergers:   splitList(settings.VerifiedMergers),
		ForgeReviews:      forgeReviewTransport(settings.Remote) != nil,
	}
	if settings.AllowedSignersFile != "" {
		if data, err := os.ReadFile(settings.AllowedSignersFile); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
					out.AllowedSigners++
				}
			}
		}
	}
	if repo != nil && settings.RequireVerified {
		if h := repo.Held(); h != nil {
			out.Held = &ui.StateRepoHeldCommit{SHA: h.SHA, Subject: h.Subject, Reason: h.Reason, Since: h.Since}
		}
	}
	return out
}
