package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/githubtoken"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TeamGitHubCredentialCheck is what POST /api/teams/{id}/github-credential/check
// and its per-repo twin answer: GitHub's verdict on a team's token, made
// on demand when a team admin presses Test in Team settings' Git tab
// (grain/task-1849). Nothing here is stored, and nothing here carries
// the token.
type TeamGitHubCredentialCheck struct {
	githubtoken.Report
	// Saved is true when the stored credential was tested, false when
	// the request carried a value that has not been saved.
	Saved bool `json:"saved"`
	// Host is the GitHub host the token was tested against.
	Host string `json:"host"`
	// CheckedAt is when GitHub was asked.
	CheckedAt time.Time `json:"checkedAt"`
	// FromReach is true when the per-repo verdicts came from the
	// listing of what the saved credential reaches (gitproxy.Reach) --
	// the same answer a task naming those repos is filed against --
	// rather than from a lookup of each repo. Repos is then that whole
	// listing when the team has no repos attached.
	FromReach bool `json:"fromReach,omitempty"`
}

// reachVerdicts is the ✓/✗ per repo for saved credential name, taken
// from gitproxy.Reach.Check -- what reachRefusal and parkUnreached ask
// when a task is filed -- so the Test button cannot pass a repo filing
// would refuse, or the reverse. With no repos (a team attached to none)
// it is the listing itself. A repo the listing cannot vouch for either
// way (one past the end of a listing too long to read in full) is asked
// of GitHub directly, as an unsaved token's repos are.
func (c *Client) reachVerdicts(transport github.Transport, name, token string, repos []string,
	listing []gitproxy.ReachableRepo) ([]githubtoken.RepoProbe, int) {

	if len(repos) == 0 {
		sort.Slice(listing, func(i, j int) bool { return strings.ToLower(listing[i].Repo) < strings.ToLower(listing[j].Repo) })
		for _, r := range listing {
			repos = append(repos, r.Repo)
		}
	}
	var out []githubtoken.RepoProbe
	skipped := 0
	for _, entry := range repos {
		repo, err := model.ParseRepo(entry)
		if err != nil || strings.ContainsAny(repo.Owner+repo.Name, "*?[") {
			// A pattern names no one repo to check.
			continue
		}
		if len(out) == githubtoken.MaxProbedRepos {
			skipped++
			continue
		}
		read, err := c.Config.GitReach.Check(name, repo.Owner, repo.Name, false)
		if err != nil || read == gitproxy.ReachUnknown {
			out = append(out, githubtoken.ProbeRepo(transport, token, repo))
			continue
		}
		verdict := githubtoken.RepoProbe{Repo: repo.String()}
		if read == gitproxy.Unreachable {
			verdict.Error = "not among the repositories GitHub lists this credential as reaching, " +
				"so a task naming it is refused when filed (the list is refreshed every few minutes)"
			out = append(out, verdict)
			continue
		}
		verdict.Read = true
		push, err := c.Config.GitReach.Check(name, repo.Owner, repo.Name, true)
		verdict.Push = err == nil && push == gitproxy.Reachable
		out = append(out, verdict)
	}
	return out, skipped
}

// gitHubHost is the GitHub API host this deployment runs against: the
// running configuration where the UI is wired to one, the stored one
// otherwise, and github.com when neither names one.
func (c *Client) gitHubHost(ctx context.Context) (string, bool) {
	var cfg model.Config
	if c.Config.RunningConfig != nil {
		cfg = c.Config.RunningConfig()
	} else if c.Store != nil {
		if stored, err := c.Store.GetConfig(ctx); err == nil && stored != nil {
			cfg = *stored
		}
	}
	host := strings.TrimSpace(cfg.GitHubHost)
	if host == "" {
		host = githubtoken.DefaultHost
	}
	return host, cfg.GitHubInsecureHTTP
}
