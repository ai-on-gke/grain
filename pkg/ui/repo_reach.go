package ui

// What a team's GitHub credential can reach, asked when a task is filed
// rather than learnt when its clone is refused (grain/task-1850). A task
// a human files against a repository the team cannot reach is refused,
// naming the team, the repository and where its access is changed; one
// filed by automation -- an agent's propose_task, which would otherwise
// lose the work it describes -- is filed and parked on the same message.
// And GET /api/deployment/reachable-repos is what the New task pickers
// offer a team that has no repositories attached.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
)

// reachRefusal is the sentence a task is refused (or parked) with when
// its team's credential cannot reach one of the repositories it names,
// and "" when it can or when nothing can say -- gitproxy.Reach.Refusal,
// which the orchestrator's own filing paths (end-of-run proposals,
// schedules, suite passes) ask too.
func (c *Client) reachRefusal(ctx context.Context, team string, target *model.RepoRef, reads []model.RepoRef,
	writes []model.WriteTarget) (string, error) {

	return c.Config.GitReach.Refusal(ctx, c.Store, team, target, reads, writes)
}

// reachParkComment is gitproxy.ReachParkComment, the wording every
// filing path parks an unreachable task on.
func reachParkComment(refusal string) string { return gitproxy.ReachParkComment(refusal) }

// parkUnreached parks each of ids -- tasks a schedule or suite run just
// filed, for which nobody filled in a form that could be refused -- whose
// team's credential cannot reach a repository it names. A task already
// parked (off the target repo list, say) is left on what it was parked
// on.
func (c *Client) parkUnreached(ctx context.Context, ids []string) error {
	if c.Config.GitReach == nil {
		return nil
	}
	for _, id := range ids {
		task, err := c.Store.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if task == nil {
			continue
		}
		if obs, err := c.Store.GetObservation(ctx, id); err != nil {
			return err
		} else if obs != nil && obs.PendingQuestionCommentID != nil {
			continue
		}
		refusal, err := c.Config.GitReach.TaskRefusal(ctx, c.Store, *task)
		if err != nil {
			return err
		}
		if refusal == "" {
			continue
		}
		if err := c.park(ctx, id, reachParkComment(refusal), c.now()); err != nil {
			return err
		}
	}
	return nil
}

// ReachConnection is how one of a team's Git connections fared when
// ReachableRepos asked it what it reaches: the credential and the host it
// was listed on (the deployment's default host spelled out rather than
// left empty), whether a listing was had, why not when it was not, how
// many repositories it listed, and whether the fix is the host's own login
// (a gitproxy.LoginRedirectError) rather than the credential itself. The
// Add repo picker shows these under itself, so an empty list says which
// connection came back empty and why rather than reading as "nothing".
type ReachConnection struct {
	Credential  string `json:"credential"`
	Host        string `json:"host"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	RepoCount   int    `json:"repoCount"`
	LoginNeeded bool   `json:"loginNeeded,omitempty"`
}

// ConnectionReachError is one of a team's Git connections whose listing
// failed: which credential on which host, and the sentence saying why --
// what to run, as authfix.ClassifyRedirect words it, for a host whose
// front-end login has lapsed (gitproxy.LoginRedirectError) -- so a picker
// can show it beside whatever the other connections did list.
type ConnectionReachError struct {
	Credential string `json:"credential"`
	Host       string `json:"host,omitempty"`
	Error      string `json:"error"`
	// LoginNeeded is set when the fix is the host's own login rather
	// than the credential.
	LoginNeeded bool `json:"loginNeeded,omitempty"`
}

// ReachableReposResult is GET /api/deployment/reachable-repos' answer.
// Available, Reason and Repos are what the endpoint has always said;
// Errors lists each connection whose listing failed, even when another's
// succeeded; Connections is every connection's status, failed or not.
type ReachableReposResult struct {
	Available   bool                     `json:"available"`
	Reason      string                   `json:"reason"`
	Repos       []gitproxy.ReachableRepo `json:"repos"`
	Errors      []ConnectionReachError   `json:"errors"`
	Connections []ReachConnection        `json:"connections"`
}

// ReachableRepos is what the team's Git credentials can reach, for the
// New task and Add repo pickers: every repository its configured Git
// connections list (on both the default host and any other host a
// connection names), with push marked, and how each connection
// fared. Available is false when no list can be had -- no GitReach, no
// credential, GitHub refusing every listing -- and Reason says why, for
// the picker to show rather than an empty list that reads as "nothing".
func (c *Client) ReachableRepos(ctx context.Context, team string) (ReachableReposResult, error) {
	out := ReachableReposResult{Repos: []gitproxy.ReachableRepo{}, Errors: []ConnectionReachError{},
		Connections: []ReachConnection{}}
	if team == "" {
		team = model.DefaultTeamID
	}
	if c.Config.GitReach == nil {
		out.Reason = "this deployment cannot list what a GitHub credential reaches"
		return out, nil
	}

	// A connection is keyed on its credential and the host it resolves
	// to, so the team's default credential and a connection naming the
	// same credential on the default host by name are asked once.
	defaultHost := c.Config.GitReach.Host
	if defaultHost == "" {
		defaultHost = model.DefaultGitHost
	}
	creds := c.Config.GitReach.Credentials
	type connTarget struct {
		name  string
		host  string // as configured; empty is the credential's own host
		shown string // where it resolves, for the status
	}
	var targets []connTarget
	seen := map[[2]string]bool{}
	addTarget := func(name, host string) {
		name = strings.TrimSpace(name)
		if name == "" || name == "anonymous" {
			return
		}
		shown := host
		if shown == "" && creds != nil {
			shown = creds.HostOf(name).Host
		}
		if shown == "" {
			shown = defaultHost
		}
		if h, err := model.NormalizeGitHost(shown); err == nil && h != "" {
			shown = h
		}
		key := [2]string{name, strings.ToLower(shown)}
		if !seen[key] {
			seen[key] = true
			targets = append(targets, connTarget{name: name, host: host, shown: shown})
		}
	}

	if defaultCred, err := c.Store.TeamGitCredential(ctx, team, "", ""); err == nil {
		addTarget(defaultCred, "")
	}

	if cfg, err := c.Store.GetConfig(ctx); err == nil && cfg != nil {
		for _, conn := range cfg.EffectiveGitConnections() {
			addTarget(conn.Credential, conn.Host)
		}
	}

	if len(targets) == 0 {
		out.Reason = model.ErrNoTeamGitCredential.Error()
		return out, nil
	}

	repoMap := map[string]bool{}
	var anyOK bool
	var lastErr error
	for _, target := range targets {
		var listing []gitproxy.ReachableRepo
		var rOk bool
		var rErr error
		conn := ReachConnection{Credential: target.name, Host: target.shown}
		if target.host != "" {
			listing, rOk, rErr = c.Config.GitReach.ReposForHost(target.name, target.host)
		} else {
			listing, rOk, rErr = c.Config.GitReach.Repos(target.name)
		}
		switch {
		case rErr != nil:
			lastErr = rErr
			conn.Error = rErr.Error()
			_, conn.LoginNeeded = gitproxy.AsLoginRedirect(rErr)
			out.Errors = append(out.Errors, ConnectionReachError{Credential: target.name, Host: target.shown,
				Error: conn.Error, LoginNeeded: conn.LoginNeeded})
		case !rOk:
			conn.Error = fmt.Sprintf("the %q credential cannot list the repositories it reaches on %s", target.name, conn.Host)
		default:
			anyOK = true
			conn.OK = true
			conn.RepoCount = len(listing)
			for _, r := range listing {
				if r.Push {
					repoMap[r.Repo] = true
				} else if _, exists := repoMap[r.Repo]; !exists {
					repoMap[r.Repo] = false
				}
			}
		}
		out.Connections = append(out.Connections, conn)
	}

	if !anyOK {
		switch {
		case lastErr != nil:
			out.Reason = lastErr.Error()
		case len(targets) == 1:
			out.Reason = fmt.Sprintf("the %q credential lists no repositories", targets[0].name)
		default:
			out.Reason = "the credentials list no repositories"
		}
		return out, nil
	}

	for repo, push := range repoMap {
		out.Repos = append(out.Repos, gitproxy.ReachableRepo{Repo: repo, Push: push})
	}
	sort.Slice(out.Repos, func(i, j int) bool {
		return strings.ToLower(out.Repos[i].Repo) < strings.ToLower(out.Repos[j].Repo)
	})
	out.Available = true
	return out, nil
}
