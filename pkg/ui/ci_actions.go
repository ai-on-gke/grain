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

// ci_actions.go is ci:read for a repository whose CI is GitHub Actions
// (ci_mode: github, or Actions not ignored): its workflow runs listed
// beside grain's own in GET /api/ci/runs, one run's jobs and steps in GET
// /api/ci/runs/{id}, one job's log in GET /api/ci/runs/{id}/log, and its
// artifacts listed in GET /api/ci/runs/{id}/artifacts.
//
// No credential reaches a sandbox for any of it. The reads go through
// the daemon's own forge connection (github.ActionsReader on the client
// Config.GitHubPullRequests is), charged to ciActionsCaller in the budget
// ledger and cached here for a little while, and when that connection is
// missing, failing or short of budget the list falls back to grain's own
// polled record of the repository's Actions jobs (model.GitHubCIJob).
//
// An Actions run's id is namespaced -- gha:<owner>:<name>:<run id>, the
// repository's slashes written as colons so the id is one path segment
// -- so it never collides with a grain run's and says which repository
// it is read from, which is what the per-repo read scope is checked
// against (Server.agentCallRepo).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ActionsRunIDPrefix starts every GitHub Actions run id ci:read serves.
const ActionsRunIDPrefix = ci.ActionsRunIDPrefix

// ciActionsCaller is the budget ledger's label for these reads.
const ciActionsCaller = "ci-read"

// MaxActionsLogBytes bounds one job log GET /api/ci/runs/{id}/log
// answers with -- the same bound a grain CI step's output has
// (ci.MaxStepOutput) -- keeping the end, which is where a failure is.
const MaxActionsLogBytes = ci.MaxStepOutput

// ActionsRunID is the ci:read id of run runID of repo.
func ActionsRunID(repo string, runID int64) string { return ci.ActionsRunID(repo, runID) }

// ParseActionsRunID reads an id ActionsRunID made back into its
// repository and GitHub run id (ci.ParseActionsRunID).
func ParseActionsRunID(id string) (repo string, runID int64, ok bool) {
	return ci.ParseActionsRunID(id)
}

// IsActionsRunID reports whether id names a GitHub Actions run.
func IsActionsRunID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), ActionsRunIDPrefix)
}

// actionsReader is the forge connection Actions are read through, its
// calls charged to ciActionsCaller, or nil when this deployment has none
// that can.
func (c *Client) actionsReader() github.ActionsReader {
	reader, _ := forgeReader[github.ActionsReader](c)
	return reader
}

// forgeReader is the first of this client's forge connections that is a
// T, labelled as ci:read's caller.
func forgeReader[T any](c *Client) (T, bool) {
	var candidates []any
	if c.Config.Actions != nil {
		candidates = append(candidates, c.Config.Actions)
	}
	if r, ok := c.Config.PullRequests.(PullRequestCheckReader); ok {
		if reader := r.GitHubPullRequestReader(); reader != nil {
			candidates = append(candidates, reader)
		}
	}
	if c.Config.GitHubPullRequests != nil {
		candidates = append(candidates, c.Config.GitHubPullRequests)
	}
	for _, cand := range candidates {
		if l, ok := cand.(github.CallerLabeler); ok {
			cand = l.WithCaller(ciActionsCaller)
		}
		if reader, ok := cand.(T); ok {
			return reader, true
		}
	}
	var none T
	return none, false
}

// actionsListedFor reports whether repo's Actions runs are listed: a
// repository gated by GitHub (ci_mode: github), or one whose GitHub CI is
// not ignored. The config repository grain CI always runs, and a repo on
// grain CI or none, are not.
func (c *Client) actionsListedFor(ctx context.Context, repo model.RepoRef) bool {
	return c.actionsRefusal(ctx, repo) == nil
}

// actionsRefusal is why repo's Actions runs are not listed, or nil when
// they are (actionsListedFor).
func (c *Client) actionsRefusal(ctx context.Context, repo model.RepoRef) *actionsRefused {
	if c.Store == nil {
		return &actionsRefused{reason: "this deployment has no store to read a repository's CI mode from"}
	}
	if grainci.RequiredFor(repo) {
		return &actionsRefused{byMode: true, reason: fmt.Sprintf("%s is grain's config repository, which grain CI always runs", repo)}
	}
	if err := c.actionsRepoServed(ctx, repo); err != nil {
		return &actionsRefused{reason: err.Error()}
	}
	eff, err := c.Store.EffectiveRepoConfig(ctx, repo)
	if err != nil {
		return &actionsRefused{reason: fmt.Sprintf("reading %s's CI mode failed (%v), so grain lists none of its GitHub Actions runs", repo, err)}
	}
	switch eff.CIMode {
	case model.CIModeGrain, model.CIModeNone:
		return &actionsRefused{byMode: true,
			reason: fmt.Sprintf("%s's CI mode is %q, so grain lists none of its GitHub Actions runs", repo, eff.CIMode)}
	}
	return nil
}

// actionsRefused is actionsRefusal's answer. byMode is a refusal that is
// the repository's own setting -- its CI is grain's, or none -- which a
// listing that did not ask for Actions by name need not mention.
type actionsRefused struct {
	reason string
	byMode bool
}

// actionsRepoServed refuses repo unless this deployment works in it
// (Config.TargetRepos; every repository while that list is empty). The
// reads go through the daemon's own forge credential, which may reach
// repositories grain was never pointed at, and the repository of a gha:
// id is whatever its caller wrote -- so a reader whose grant covers
// "every repository" still means every repository grain serves, as it
// does for grain's own runs, and not every one the credential can see.
func (c *Client) actionsRepoServed(ctx context.Context, repo model.RepoRef) error {
	if targetAllowed(c.teamTargetRepos(ctx, ""), repo) {
		return nil
	}
	return &NotFoundError{ID: repo.String(),
		message: fmt.Sprintf("%s is not a repository this deployment works in, so grain reads none of its GitHub Actions runs", repo)}
}

// actionsCache holds what the forge answered for a little while, so an
// agent polling a run and the next one asking about the same run cost
// GitHub one call between them.
type actionsCache struct {
	mu      sync.Mutex
	entries map[string]actionsCacheEntry
}

type actionsCacheEntry struct {
	value   any
	expires time.Time
}

// actionsCacheMax bounds the entries kept; past it the expired go first,
// then everything.
const actionsCacheMax = 512

// TTLs: a list is fresh for half a minute; a finished run, its jobs and
// a finished job's log do not change, and keep for longer.
const (
	actionsListTTL     = 30 * time.Second
	actionsLiveTTL     = 15 * time.Second
	actionsFinishedTTL = 10 * time.Minute
)

var ciActionsCaches sync.Map // *Client -> *actionsCache

func (c *Client) actionsCache() *actionsCache {
	v, _ := ciActionsCaches.LoadOrStore(c, &actionsCache{entries: map[string]actionsCacheEntry{}})
	return v.(*actionsCache)
}

func (a *actionsCache) get(key string, now time.Time) (any, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[key]
	if !ok || now.After(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (a *actionsCache) put(key string, value any, ttl time.Duration, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.entries) >= actionsCacheMax {
		for k, e := range a.entries {
			if now.After(e.expires) {
				delete(a.entries, k)
			}
		}
		if len(a.entries) >= actionsCacheMax {
			a.entries = map[string]actionsCacheEntry{}
		}
	}
	a.entries[key] = actionsCacheEntry{value: value, expires: now.Add(ttl)}
}

// invalidateRun drops cached entries for runID (and any jobIDs) and repo's
// run listings so reads after a re-run fetch the new attempt rather than
// serving the finished attempt for actionsFinishedTTL.
func (a *actionsCache) invalidateRun(repo model.RepoRef, runID int64, jobIDs ...int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.entries, fmt.Sprintf("run|%s|%d", repo, runID))
	delete(a.entries, fmt.Sprintf("artifacts|%s|%d", repo, runID))
	for _, jid := range jobIDs {
		delete(a.entries, fmt.Sprintf("log|%s|%d", repo, jid))
	}
	prefix := fmt.Sprintf("runs|%s|", repo)
	for k := range a.entries {
		if strings.HasPrefix(k, prefix) {
			delete(a.entries, k)
		}
	}
}

// errActionsBudgetLow is a forge read declined because the hour's
// budget is low (github.BudgetLow): what is left goes to the merge queue.
var errActionsBudgetLow = errors.New("the GitHub API budget is low, so grain is not spending it on reading Actions for an agent " +
	"right now; what grain already recorded is served, and the rest can be asked again later")

// errNoActionsReader is a deployment with no forge connection that reads Actions.
var errNoActionsReader = errors.New("this deployment has no forge connection that reads GitHub Actions")

// cachedActions is fetch's answer for key, from the cache when it is
// fresh, otherwise from the forge -- unless the budget is low.
func cachedActions[T any](c *Client, key string, ttl func(T) time.Duration, fetch func() (T, error)) (T, error) {
	var zero T
	now := time.Now()
	cache := c.actionsCache()
	if v, ok := cache.get(key, now); ok {
		return v.(T), nil
	}
	if github.BudgetLow(now) {
		return zero, errActionsBudgetLow
	}
	v, err := fetch()
	if err != nil {
		return zero, err
	}
	cache.put(key, v, ttl(v), now)
	return v, nil
}

func listTTL[T any](T) time.Duration { return actionsListTTL }

// wantsActions reports whether a GET /api/ci/runs answers with Actions
// rows: a request naming one repository whose Actions are listed, that
// either asked for a provider or comes from an agent rather than the CI
// pane (which lists grain's own runs, and polls every few seconds).
func (s *Server) wantsActions(r *http.Request, filter ci.RunFilter) (model.RepoRef, bool) {
	if s.tasks == nil || filter.Repo == "" {
		return model.RepoRef{}, false
	}
	if filter.Provider != "" && !strings.EqualFold(filter.Provider, ci.ProviderGitHubActions) {
		return model.RepoRef{}, false
	}
	if filter.Provider == "" && s.tasks.requestActor(r.Context()).Actor.Kind == model.PrincipalHuman {
		return model.RepoRef{}, false
	}
	repo, err := s.tasks.parseRepo(filter.Repo)
	if err != nil || !s.tasks.actionsListedFor(r.Context(), repo) {
		return model.RepoRef{}, false
	}
	return repo, true
}

func (c *Client) listActionsRunsCounted(ctx context.Context, repo model.RepoRef, filter ci.RunFilter, limit int) (runs []ci.Run, matched int, note string) {
	if limit <= 0 {
		limit = ci.MaxListedRuns
	}
	recorded, rec := c.recordedActionsRuns(ctx, repo, filter)
	byID := map[string]ci.Run{}
	var out []ci.Run
	add := func(run ci.Run) {
		if _, dup := byID[run.ID]; dup {
			return
		}
		byID[run.ID] = run
		out = append(out, run)
	}
	forge, forgeTotal, serverExact, notFound, err := c.forgeActionsRuns(repo, filter, limit)
	switch {
	case err == nil && notFound:
		note = fmt.Sprintf("the forge answered 404 for %s's Actions runs on %s (Actions off there, or not readable "+
			"with this deployment's credential); listing what grain recorded of them", repo, repo.GitHost())
	case err == nil:
		for _, run := range forge {
			if rec, ok := recorded[run.ID]; ok {
				run.Actions.FailedJobs = rec.Actions.FailedJobs
				if len(run.Jobs) == 0 {
					run.Jobs = rec.Jobs
				}
			}
			add(run)
		}
	case errors.Is(err, errNoActionsReader), errors.Is(err, errActionsBudgetLow):
		note = err.Error()
	default:
		note = fmt.Sprintf("reading %s's Actions runs from the forge failed (%v); listing what grain recorded of them", repo, err)
	}
	for _, run := range recorded {
		add(run)
	}
	kept := out[:0]
	for _, run := range out {
		f := filter
		f.Provider = ""
		if f.Matches(run) {
			kept = append(kept, run)
		}
	}
	sortRunsNewestFirst(kept)
	if len(kept) == 0 {
		note = emptyActionsNote(repo, note, err == nil && !notFound, rec)
	}
	matched = len(kept)
	if err == nil && serverExact && forgeTotal > matched {
		matched = forgeTotal
	}
	if len(kept) > limit {
		kept = kept[:limit]
	}
	return kept, matched, note
}

// forgeActionsRuns is the forge's pages of repo's runs, as rows. A ref is
// asked of the forge as a branch first, since GitHub matches only an
// exact one, and then unnarrowed for the substring match ?ref= means.
func (c *Client) forgeActionsRuns(repo model.RepoRef, filter ci.RunFilter, limit int) (out []ci.Run, total int, serverExact, notFound bool, err error) {
	reader := c.actionsReader()
	if reader == nil {
		return nil, 0, false, false, errNoActionsReader
	}
	clientFilters := len(filter.States) > 0 || strings.TrimSpace(filter.Job) != "" || !filter.Until.IsZero()
	fetchLimit := max(limit, 100)
	if clientFilters {
		fetchLimit = github.MaxActionsRunsLimit
	}
	fetchLimit = min(fetchLimit, github.MaxActionsRunsLimit)

	ask := func(branch string, reqLimit int) (github.ActionsRunsPage, error) {
		q := github.ActionsRunsQuery{
			Branch:   branch,
			Workflow: strings.TrimSpace(filter.Workflow),
			Since:    filter.Since,
			Until:    filter.Until,
			Limit:    reqLimit,
		}
		key := fmt.Sprintf("runs|%s|%s|%s|%d|%d|%d", repo, branch, q.Workflow, q.Since.Unix(), q.Until.Unix(), q.Limit)
		return cachedActions(c, key, listTTL[github.ActionsRunsPage], func() (github.ActionsRunsPage, error) {
			if pager, ok := reader.(github.ActionsPageReader); ok {
				return pager.ListActionsRunsPage(repo.StoredOwner(), repo.Name, q)
			}
			runs, err := reader.ListActionsRuns(repo.StoredOwner(), repo.Name, q)
			if err != nil {
				return github.ActionsRunsPage{}, err
			}
			return github.ActionsRunsPage{
				Runs:             runs,
				TotalCount:       len(runs),
				WorkflowFiltered: q.Workflow == "",
			}, nil
		})
	}
	var page github.ActionsRunsPage
	branchMatched := filter.Ref == ""
	if filter.Ref != "" {
		if page, err = ask(filter.Ref, fetchLimit); err != nil {
			return nil, 0, false, false, err
		}
		branchMatched = len(page.Runs) > 0
	}
	if len(page.Runs) == 0 {
		fallbackLimit := fetchLimit
		if filter.Ref != "" {
			fallbackLimit = github.MaxActionsRunsLimit
		}
		if page, err = ask("", fallbackLimit); err != nil {
			return nil, 0, false, false, err
		}
	}
	if strings.TrimSpace(filter.Workflow) != "" && !page.WorkflowFiltered && fetchLimit < github.MaxActionsRunsLimit && len(page.Runs) >= fetchLimit {
		// Workflow was a display name rather than a workflow file, so widen to
		// MaxActionsRunsLimit before client-side workflow matching.
		branch := ""
		if branchMatched && filter.Ref != "" {
			branch = filter.Ref
		}
		if wider, werr := ask(branch, github.MaxActionsRunsLimit); werr == nil {
			page = wider
		}
	}
	exact := !clientFilters && branchMatched && (strings.TrimSpace(filter.Workflow) == "" || page.WorkflowFiltered)
	out = make([]ci.Run, 0, len(page.Runs))
	for _, r := range page.Runs {
		out = append(out, actionsRunRow(repo, r))
	}
	return out, page.TotalCount, exact, page.NotFound, nil
}

// actionsRunRow is a forge workflow run as a listed run.
func actionsRunRow(repo model.RepoRef, r github.WorkflowRun) ci.Run {
	run := ci.Run{
		ID:         ActionsRunID(repo.String(), r.ID),
		Source:     ci.FaultSourceGitHub,
		Provider:   ci.ProviderGitHubActions,
		Repo:       repo.String(),
		Ref:        r.HeadBranch,
		Commit:     r.HeadSHA,
		Event:      ci.Event(r.Event),
		StartedAt:  r.StartedAt,
		Conclusion: actionsConclusion(r.Status, r.Conclusion),
		Jobs:       []ci.JobRun{},
		Actions: &ci.ActionsRun{
			RunID: r.ID, RunAttempt: r.RunAttempt, Workflow: r.Path, WorkflowName: r.Name, HeadRepo: r.HeadRepo,
			Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL,
		},
	}
	if r.Status == "completed" {
		run.FinishedAt = r.UpdatedAt
	}
	return run
}

// actionsConclusion is GitHub's status and conclusion in grain's
// vocabulary: "" for a run or job that has not finished.
func actionsConclusion(status, conclusion string) ci.Conclusion {
	if status != "" && status != "completed" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "success":
		return ci.ConclusionSuccess
	case "cancelled":
		return ci.ConclusionCancelled
	case "skipped", "neutral":
		return ci.ConclusionSkipped
	case "":
		return ""
	default:
		return ci.ConclusionFailure
	}
}

// recordedActionsRuns is grain's own polled record of repo's Actions
// jobs (model.GitHubCIJob), as runs keyed by their ci:read id, and what
// was read to make them.
//
// The filter's window and ref are asked of the store rather than of what
// it returns: the read is bounded (recordedActionsJobsLimit), and the
// newest jobs of every branch narrowed afterwards to one branch, or to a
// window weeks back, would be none of them -- which is how a listing of
// main's runs before the forge's newest 500 used to answer nothing
// (grain/task-5274).
func (c *Client) recordedActionsRuns(ctx context.Context, repo model.RepoRef, filter ci.RunFilter) (map[string]ci.Run, actionsRecord) {
	out := map[string]ci.Run{}
	var rec actionsRecord
	if c.Store == nil {
		rec.err = errors.New("this deployment has no store")
		return out, rec
	}
	jobs, err := c.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
		Repo: repo.String(), Since: filter.Since, Until: filter.Until, RefContains: filter.Ref,
		Limit: recordedActionsJobsLimit,
	})
	if err != nil {
		rec.err = err
		return out, rec
	}
	rec.jobs = len(jobs)
	byRun := map[int64][]model.GitHubCIJob{}
	for _, j := range jobs {
		if id := recordedRunID(j); id != 0 {
			j.RunID = id
			byRun[id] = append(byRun[id], j)
		} else {
			rec.noRun++
		}
	}
	for runID, js := range byRun {
		if run, ok := recordedActionsRun(repo, runID, js); ok {
			out[run.ID] = run
		}
	}
	return out, rec
}

// recordedActionsJobsLimit bounds one read of the recorded jobs behind
// a listing: some three hundred runs of a twenty-job workflow.
const recordedActionsJobsLimit = 5000

// actionsRecord is what recordedActionsRuns read: how many recorded jobs,
// how many of them it could not place in a workflow run, and why it read
// none at all.
type actionsRecord struct {
	jobs, noRun int
	err         error
}

// recordedRunID is the workflow run a recorded job belongs to: its own
// run id, or for a row recorded before that was filled in from the job's
// page (model.PutGitHubCIJob does it now), the one its page names.
func recordedRunID(j model.GitHubCIJob) int64 {
	if j.RunID != 0 {
		return j.RunID
	}
	if id := model.ActionsRunIDFromURL(j.JobURL); id != 0 {
		return id
	}
	return model.ActionsRunIDFromURL(j.RunURL)
}

// emptyActionsNote is the note for a listing of repo's Actions runs that
// came back empty: note, what the forge said (asked is whether it
// answered, and with no runs), and what grain's record held.
func emptyActionsNote(repo model.RepoRef, note string, asked bool, rec actionsRecord) string {
	var parts []string
	if note != "" {
		parts = append(parts, note)
	} else if asked {
		parts = append(parts, fmt.Sprintf("the forge listed no Actions runs of %s matching this", repo))
	}
	switch {
	case rec.err != nil:
		parts = append(parts, fmt.Sprintf("reading grain's record of its Actions jobs failed (%v)", rec.err))
	case rec.jobs == 0:
		parts = append(parts, "grain has recorded none of its Actions jobs in this window")
	case rec.noRun == rec.jobs:
		parts = append(parts, fmt.Sprintf("grain recorded %d of its jobs, none of them naming a workflow run", rec.jobs))
	default:
		parts = append(parts, fmt.Sprintf("none of the %d jobs grain recorded of it match this", rec.jobs))
	}
	return strings.Join(parts, "; ")
}

// recordedActionsRun is one run out of grain's record of its jobs.
func recordedActionsRun(repo model.RepoRef, runID int64, jobs []model.GitHubCIJob) (ci.Run, bool) {
	run, ok := ci.GitHubCIRun(jobs)
	if !ok {
		return ci.Run{}, false
	}
	run.ID = ActionsRunID(repo.String(), runID)
	run.Provider = ci.ProviderGitHubActions
	run.Repo = repo.String()
	actions := &ci.ActionsRun{RunID: runID, Status: "completed"}
	for _, j := range jobs {
		if actions.WorkflowName == "" {
			actions.WorkflowName = j.Workflow
		}
		if actions.Workflow == "" {
			lw := strings.ToLower(strings.TrimSpace(j.Workflow))
			if strings.HasSuffix(lw, ".yml") || strings.HasSuffix(lw, ".yaml") || strings.HasPrefix(lw, ".github/workflows/") {
				actions.Workflow = strings.TrimSpace(j.Workflow)
			}
		}
		if actions.URL == "" {
			actions.URL = j.RunURL
		}
		if j.RunAttempt > actions.RunAttempt {
			actions.RunAttempt = j.RunAttempt
		}
		if model.GitHubCIFailed(j.Conclusion) {
			actions.FailedJobs = append(actions.FailedJobs, j.JobName)
		}
	}
	sort.Strings(actions.FailedJobs)
	run.Actions = actions
	return run, true
}

// sortRunsNewestFirst orders runs by when they started, newest first,
// a run not started yet first of all.
func sortRunsNewestFirst(runs []ci.Run) {
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i].StartedAt, runs[j].StartedAt
		switch {
		case a == nil && b == nil:
			return runs[i].ID > runs[j].ID
		case a == nil:
			return true
		case b == nil:
			return false
		}
		return a.After(*b)
	})
}

// actionsRunDetail is one Actions run with its jobs and their steps, from
// the forge, or from grain's record of it when the forge cannot say.
func (c *Client) actionsRunDetail(ctx context.Context, repo model.RepoRef, runID int64) (ci.Run, []github.ActionsRunJob, error) {
	if err := c.actionsRepoServed(ctx, repo); err != nil {
		return ci.Run{}, nil, err
	}
	reader := c.actionsReader()
	var forgeErr error
	if reader != nil {
		key := fmt.Sprintf("run|%s|%d", repo, runID)
		type detail struct {
			run  *github.WorkflowRun
			jobs []github.ActionsRunJob
		}
		d, err := cachedActions(c, key, func(d detail) time.Duration {
			if d.run != nil && d.run.Status == "completed" {
				return actionsFinishedTTL
			}
			return actionsLiveTTL
		}, func() (detail, error) {
			run, err := reader.GetActionsRun(repo.StoredOwner(), repo.Name, runID)
			if err != nil || run == nil {
				return detail{}, err
			}
			jobs, err := reader.ListActionsJobs(repo.StoredOwner(), repo.Name, runID)
			return detail{run: run, jobs: jobs}, err
		})
		if err == nil && d.run != nil {
			run := actionsRunWithJobs(repo, *d.run, d.jobs)
			if c.Store != nil && len(run.Actions.FailedJobs) > 0 {
				if recorded, ok := c.recordedActionsRunByID(ctx, repo, runID); ok {
					foldRecordedTails(&run, recorded)
				}
			}
			return run, d.jobs, nil
		}
		forgeErr = err
	} else {
		forgeErr = errNoActionsReader
	}
	if c.Store != nil {
		if run, ok := c.recordedActionsRunByID(ctx, repo, runID); ok {
			return run, nil, nil
		}
	}
	if forgeErr == nil {
		forgeErr = &NotFoundError{ID: ActionsRunID(repo.String(), runID),
			message: fmt.Sprintf("no GitHub Actions run %d in %s", runID, repo)}
	}
	return ci.Run{}, nil, forgeErr
}

// recordedActionsRunByID is run runID of repo out of grain's record: the
// jobs stored under that run id, or failing those the ones whose pages
// name it (recordedRunID).
func (c *Client) recordedActionsRunByID(ctx context.Context, repo model.RepoRef, runID int64) (ci.Run, bool) {
	jobs, err := c.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{Repo: repo.String(), RunID: runID})
	if err != nil {
		return ci.Run{}, false
	}
	if len(jobs) == 0 {
		all, err := c.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{Repo: repo.String(), Limit: 5000})
		if err != nil {
			return ci.Run{}, false
		}
		for _, j := range all {
			if j.RunID == 0 && recordedRunID(j) == runID {
				j.RunID = runID
				jobs = append(jobs, j)
			}
		}
	}
	return recordedActionsRun(repo, runID, jobs)
}

// foldRecordedTails puts the failing log tail grain recorded of each of
// run's failed jobs (recordedActionsRun) on that job's failing step: the
// forge's account of a run has its steps' verdicts but none of their
// output, and the tail is where a failed test's name is
// (`grain ci test-history`, `grain ci show`).
func foldRecordedTails(run *ci.Run, recorded ci.Run) {
	byName := map[string]ci.JobRun{}
	tails := map[string]ci.StepRun{}
	for _, job := range recorded.Jobs {
		byName[job.Name] = job
		if job.Conclusion == ci.ConclusionFailure && len(job.Steps) > 0 && job.Steps[0].Output != "" {
			tails[job.Name] = job.Steps[0]
		}
	}
	for i := range run.Jobs {
		job := &run.Jobs[i]
		if recJob, ok := byName[job.Name]; ok {
			if job.StartedAt == nil {
				job.StartedAt = recJob.StartedAt
			}
			if job.FinishedAt == nil {
				job.FinishedAt = recJob.FinishedAt
			}
		}
		tail, ok := tails[job.Name]
		if !ok || job.Conclusion != ci.ConclusionFailure {
			continue
		}
		at := -1
		for j, st := range job.Steps {
			if st.Output != "" {
				at = -2
				break
			}
			if st.Name == tail.Name || (at == -1 && st.Conclusion == ci.ConclusionFailure) {
				at = j
			}
		}
		switch {
		case at >= 0:
			job.Steps[at].Output = tail.Output
		case at == -1:
			job.Steps = append(job.Steps, tail)
		}
	}
}

// actionsRunWithJobs is a forge run and its jobs as one run in full.
func actionsRunWithJobs(repo model.RepoRef, r github.WorkflowRun, jobs []github.ActionsRunJob) ci.Run {
	run := actionsRunRow(repo, r)
	for _, j := range jobs {
		job := ci.JobRun{
			Workflow: r.Name, WorkflowPath: r.Path,
			JobID: strconv.FormatInt(j.ID, 10), Name: j.Name,
			StartedAt: j.StartedAt, FinishedAt: j.CompletedAt,
			Conclusion: actionsConclusion(j.Status, j.Conclusion),
			Status:     j.Status,
		}
		for _, st := range j.Steps {
			job.Steps = append(job.Steps, ci.StepRun{Name: st.Name, Conclusion: actionsConclusion(st.Status, st.Conclusion),
				StartedAt: st.StartedAt, FinishedAt: st.CompletedAt})
		}
		if job.Conclusion == ci.ConclusionFailure {
			run.Actions.FailedJobs = append(run.Actions.FailedJobs, j.Name)
		}
		run.Jobs = append(run.Jobs, job)
	}
	return run
}

// CIRunLogResponse is GET /api/ci/runs/{id}/log: one GitHub Actions job's
// log, its last MaxActionsLogBytes at most, or its last ?tail= lines.
type CIRunLogResponse struct {
	Run   string `json:"run"`
	Job   string `json:"job"`
	JobID int64  `json:"jobId"`
	URL   string `json:"url,omitempty"`
	// Bytes is the size of the whole log as the forge served it, and
	// Truncated whether Log is less than all of it.
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
	Log       string `json:"log"`
}

// handleGetCIRunLog is GET /api/ci/runs/{id}/log?job=<name or id>&tail=<lines>.
func (s *Server) handleGetCIRunLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	repoText, runID, ok := ParseActionsRunID(id)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a GitHub Actions run (gha:<owner>:<name>:<run id>): "+
			"a grain CI run's log is its steps' output, in GET /api/ci/runs/%s", id, url.PathEscape(id)))
		return
	}
	repo, err := s.tasks.parseRepo(repoText)
	if err != nil {
		writeClientError(w, err)
		return
	}
	tail := 0
	if text := strings.TrimSpace(r.URL.Query().Get("tail")); text != "" {
		if tail, err = strconv.Atoi(text); err != nil || tail < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a tail: it is how many lines from the end to read", text))
			return
		}
	}
	job, text, status, err := s.tasks.actionsJobLog(r.Context(), repo, runID, r.URL.Query().Get("job"))
	if err != nil {
		if status == 0 {
			writeClientError(w, err)
			return
		}
		if status == http.StatusBadGateway {
			// Why the forge served no log is what the agent asking needs
			// to act on, and it is already trusted with the log itself.
			err = apierror.Expose(err)
		}
		writeError(w, status, err)
		return
	}
	resp := CIRunLogResponse{Run: id, Job: job.Name, JobID: job.ID, URL: job.HTMLURL, Bytes: len(text)}
	resp.Log, resp.Truncated = tailActionsLog(text, tail)
	writeJSON(w, http.StatusOK, resp)
}

// actionsJobLog is one job of run runID's whole log, the job picked by
// name or id (pickActionsJob). A failure carries the HTTP status it
// answers with, 0 for writeClientError to decide.
func (c *Client) actionsJobLog(ctx context.Context, repo model.RepoRef, runID int64, name string) (github.ActionsRunJob, string, int, error) {
	_, jobs, err := c.actionsRunDetail(ctx, repo, runID)
	if err != nil {
		if errors.Is(err, errActionsBudgetLow) {
			return github.ActionsRunJob{}, "", http.StatusServiceUnavailable, err
		}
		return github.ActionsRunJob{}, "", 0, err
	}
	job, err := pickActionsJob(jobs, name)
	if err != nil {
		return job, "", http.StatusNotFound, err
	}
	reader := c.actionsReader()
	if reader == nil {
		return job, "", http.StatusNotFound, errNoActionsReader
	}
	key := fmt.Sprintf("log|%s|%d", repo, job.ID)
	text, err := cachedActions(c, key, func(string) time.Duration {
		if job.Status == "completed" {
			return actionsFinishedTTL
		}
		return actionsLiveTTL
	}, func() (string, error) {
		text, err := reader.ActionsJobLog(repo.StoredOwner(), repo.Name, job.ID)
		if err == nil && text == "" && job.Status == "completed" {
			// An error rather than a log, so it is not cached: the next
			// read asks the forge again instead of being handed the same
			// nothing for actionsFinishedTTL.
			return "", errEmptyActionsLog
		}
		return text, err
	})
	if err != nil {
		var gh *github.Error
		switch {
		case errors.Is(err, errActionsBudgetLow):
			return job, "", http.StatusServiceUnavailable, err
		case errors.Is(err, errEmptyActionsLog), errors.Is(err, github.ErrEmptyJobLog):
			// A finished job always wrote something; nothing back is a log
			// that went missing on its way here, not one that is empty.
			msg := fmt.Sprintf("the forge answered job %q's log (%d) with no bytes at all", job.Name, job.ID)
			if errors.As(err, &gh) {
				msg += ", " + forgeAnswer(gh)
				if gh.Note != "" {
					msg += " (" + gh.Note + ")"
				}
			}
			return job, "", http.StatusBadGateway, errors.New(msg)
		case errors.As(err, &gh) && (gh.Status == http.StatusNotFound || gh.Status == http.StatusGone):
			return job, "", http.StatusNotFound, fmt.Errorf("the forge has no log for job %q (%d), %s: it is past its retention window, or not written yet", job.Name, job.ID, forgeAnswer(gh))
		case errors.As(err, &gh) && gh.Status == http.StatusForbidden:
			return job, "", http.StatusForbidden, fmt.Errorf("the forge refused job %q's log to this deployment's credential (it needs actions:read), %s", job.Name, forgeAnswer(gh))
		case errors.As(err, &gh):
			// The status and hosts, not the body: that is whatever page
			// the forge or its blob store answered with.
			return job, "", http.StatusBadGateway, fmt.Errorf("reading job %q's log from the forge: %s", job.Name, forgeAnswer(gh))
		default:
			return job, "", http.StatusBadGateway, fmt.Errorf("reading job %q's log from the forge: %w", job.Name, err)
		}
	}
	return job, text, http.StatusOK, nil
}

// errEmptyActionsLog is a finished job's log read back as nothing from a
// reader that said no more than that.
var errEmptyActionsLog = errors.New("the forge answered a finished job's log with no bytes at all")

// forgeAnswer is gh's status and the host or hosts it came from ("HTTP
// 404 from ghe.example.com"), for a sentence saying why a log is not
// here. The URL's path and query are left out: a log's redirect target
// is a signed URL.
func forgeAnswer(gh *github.Error) string {
	out := fmt.Sprintf("HTTP %d", gh.Status)
	var hosts []string
	for _, field := range strings.Fields(gh.URL) {
		if u, err := url.Parse(field); err == nil && u.Host != "" && !slices.Contains(hosts, u.Host) {
			hosts = append(hosts, u.Host)
		}
	}
	if len(hosts) > 0 {
		out += " from " + strings.Join(hosts, ", redirected to ")
	}
	return out
}

// ExternalCIRuns is this client's GitHub Actions runs as ci.ExternalRuns,
// for the sandbox namespace and CLI.
func (c *Client) ExternalCIRuns() ci.ExternalRuns { return actionsExternalRuns{c} }

type actionsExternalRuns struct{ c *Client }

func (a actionsExternalRuns) FindExternalRuns(ctx context.Context, f ci.RunFilter, limit int) ([]ci.Run, string) {
	runs, _, note := a.FindExternalRunsCounted(ctx, f, limit)
	return runs, note
}

func (a actionsExternalRuns) FindExternalRunsCounted(ctx context.Context, f ci.RunFilter, limit int) ([]ci.Run, int, string) {
	if f.Provider != "" && !strings.EqualFold(f.Provider, ci.ProviderGitHubActions) {
		return nil, 0, ""
	}
	repo, err := a.c.parseRepo(f.Repo)
	if err != nil {
		return nil, 0, fmt.Sprintf("%q is not a repository grain can read GitHub Actions runs of: %v", f.Repo, err)
	}
	if refused := a.c.actionsRefusal(ctx, repo); refused != nil {
		if refused.byMode && f.Provider == "" {
			// A listing of whatever CI the repository has: a repository on
			// grain CI having no Actions runs is not news.
			return nil, 0, ""
		}
		return nil, 0, refused.reason
	}
	runs, matched, note := a.c.listActionsRunsCounted(ctx, repo, f, limit)
	if repair := a.c.openBaseRepairNote(ctx, repo, f.Ref); repair != "" {
		if note != "" {
			note += "; "
		}
		note += repair
	}
	return runs, matched, note
}

// FindExternalTestFailures is every run in grain's record of f.Repo's
// Actions jobs with a failed job naming test among its FailedTests,
// newest first, each with its failed jobs alone: what `grain ci
// test-history` counts a test's failures over, which the record keeps for
// weeks (the orchestrator's gitHubCIVerdictRetention), beside the newest
// hundred runs it lists.
func (a actionsExternalRuns) FindExternalTestFailures(ctx context.Context, f ci.RunFilter, test string) ([]ci.Run, string) {
	repo, err := a.c.parseRepo(f.Repo)
	if err != nil {
		return nil, fmt.Sprintf("%q is not a repository grain can read GitHub Actions runs of: %v", f.Repo, err)
	}
	if refused := a.c.actionsRefusal(ctx, repo); refused != nil {
		return nil, refused.reason
	}
	if a.c.Store == nil || strings.TrimSpace(test) == "" {
		return nil, ""
	}
	jobs, err := a.c.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
		Repo: repo.String(), FailedTest: strings.TrimSpace(test),
		Since: f.Since, Until: f.Until, RefContains: f.Ref, Limit: recordedActionsJobsLimit,
	})
	if err != nil {
		return nil, fmt.Sprintf("reading grain's record of %s's failed Actions jobs failed (%v)", repo, err)
	}
	byRun := map[int64][]model.GitHubCIJob{}
	for _, j := range jobs {
		if id := recordedRunID(j); id != 0 {
			j.RunID = id
			byRun[id] = append(byRun[id], j)
		}
	}
	var out []ci.Run
	for runID, js := range byRun {
		if run, ok := recordedActionsRun(repo, runID, js); ok {
			out = append(out, run)
		}
	}
	sortRunsNewestFirst(out)
	return out, ""
}

// openBaseRepairNote is what a listing of ref's runs says about repo's
// open default-branch repair (orchestrator's githubbaserepair.go), or "":
// "repair task <id> is open for the red <ref>", which the release train
// repeats when it has no known-good commit to cut (release-from-trunk.sh).
func (c *Client) openBaseRepairNote(ctx context.Context, repo model.RepoRef, ref string) string {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	if c.Store == nil || (ref != "main" && ref != "master") {
		return ""
	}
	ids, err := c.Store.OpenTasksWithTag(ctx, model.GitHubBaseRepairTag(repo))
	if err != nil || len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return fmt.Sprintf("repair task %s is open for the red %s", ids[0], ref)
}

var _ ci.ExternalRunsRepos = actionsExternalRuns{}

// ServesRepo reports whether repoText's Actions runs are listed: its CI
// is GitHub Actions (actionsListedFor).
func (a actionsExternalRuns) ServesRepo(ctx context.Context, repoText string) bool {
	repo, err := a.c.parseRepo(repoText)
	return err == nil && a.c.actionsListedFor(ctx, repo)
}

func (a actionsExternalRuns) ExternalRun(ctx context.Context, id string) (ci.Run, bool, error) {
	repoText, runID, ok := ParseActionsRunID(id)
	if !ok {
		return ci.Run{}, false, nil
	}
	repo, err := a.c.parseRepo(repoText)
	if err != nil {
		return ci.Run{}, false, err
	}
	run, _, err := a.c.actionsRunDetail(ctx, repo, runID)
	if err != nil {
		return ci.Run{}, false, err
	}
	return run, true, nil
}

var _ ci.ExternalAttempts = actionsExternalRuns{}

func (a actionsExternalRuns) ExternalJobAttempts(ctx context.Context, id string) ([]ci.GitHubJob, error) {
	repoText, runID, ok := ParseActionsRunID(id)
	if !ok {
		return nil, fmt.Errorf("%q is not a GitHub Actions run", id)
	}
	repo, err := a.c.parseRepo(repoText)
	if err != nil {
		return nil, err
	}
	if err := a.c.actionsRepoServed(ctx, repo); err != nil {
		return nil, err
	}
	reader, ok := forgeReader[github.ActionsJobAttemptsReader](a.c)
	if !ok {
		return nil, errNoActionsReader
	}
	jobs, err := reader.ListActionsJobAttempts(repo.StoredOwner(), repo.Name, runID)
	if err != nil {
		return nil, fmt.Errorf("reading every attempt of the jobs of %s from the forge: %w", id, err)
	}
	out := make([]ci.GitHubJob, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, ci.GitHubJob{
			ID: j.ID, RunID: j.RunID, RunAttempt: j.RunAttempt, WorkflowName: j.WorkflowName, HeadSHA: j.HeadSHA,
			Name: j.Name, Status: j.Status, Conclusion: ci.GitHubConclusion(j.Conclusion),
			CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, CompletedAt: j.CompletedAt, HTMLURL: j.HTMLURL,
		})
	}
	return out, nil
}

func (a actionsExternalRuns) ExternalCheckRuns(ctx context.Context, repoText, sha, name string) ([]ci.GitHubCheckRun, string, error) {
	repo, err := a.c.parseRepo(repoText)
	if err != nil {
		return nil, "", err
	}
	if err := a.c.actionsRepoServed(ctx, repo); err != nil {
		return nil, "", err
	}
	reader, ok := forgeReader[github.CheckRunAttemptsReader](a.c)
	if !ok {
		return nil, "", errors.New("this deployment's forge connection cannot list a commit's check runs")
	}
	runs, err := reader.ListCheckRunAttempts(repo.StoredOwner(), repo.Name, sha, name)
	if err != nil {
		return nil, "", fmt.Errorf("reading the check runs on %s of %s from the forge: %w", sha, repo, err)
	}
	app, err := reader.CheckRunApp(repo.StoredOwner(), repo.Name)
	if err != nil {
		app = ""
	}
	out := make([]ci.GitHubCheckRun, 0, len(runs))
	for _, r := range runs {
		run := ci.GitHubCheckRun{
			ID: r.ID, Name: r.Name, HeadSHA: r.HeadSHA, ExternalID: r.ExternalID, Status: r.Status,
			StartedAt: r.StartedAt, CompletedAt: r.CompletedAt, HTMLURL: r.HTMLURL,
		}
		if r.Conclusion != nil {
			run.Conclusion = ci.GitHubConclusion(*r.Conclusion)
		}
		if r.App != "" {
			run.App = &ci.GitHubApp{Slug: r.App}
		}
		out = append(out, run)
	}
	return out, app, nil
}

func (a actionsExternalRuns) ExternalJobLog(ctx context.Context, id, job string) (string, error) {
	repoText, runID, ok := ParseActionsRunID(id)
	if !ok {
		return "", fmt.Errorf("%q is not a GitHub Actions run", id)
	}
	repo, err := a.c.parseRepo(repoText)
	if err != nil {
		return "", err
	}
	_, text, _, err := a.c.actionsJobLog(ctx, repo, runID, job)
	if err != nil {
		return "", err
	}
	text, _ = tailActionsLog(text, 0)
	return text, nil
}

// tailActionsLog is the last lines lines of text (all of it for 0),
// and at most MaxActionsLogBytes of that, cut at a line boundary.
func tailActionsLog(text string, lines int) (string, bool) {
	out := text
	if lines > 0 {
		trimmed := strings.TrimRight(out, "\n")
		cut := len(trimmed)
		for i := 0; i < lines && cut > 0; i++ {
			cut = strings.LastIndexByte(trimmed[:cut], '\n')
			if cut < 0 {
				cut = 0
				break
			}
		}
		if cut > 0 {
			out = trimmed[cut+1:] + "\n"
		}
	}
	if len(out) > MaxActionsLogBytes {
		out = out[len(out)-MaxActionsLogBytes:]
		if i := strings.IndexByte(out, '\n'); i >= 0 && i < len(out)-1 {
			out = out[i+1:]
		}
		out = strings.ToValidUTF8(out, "")
	}
	return out, len(out) < len(text)
}

// pickActionsJob is the job named (by name or id), or with none named
// the first that failed -- or the only one there is.
func pickActionsJob(jobs []github.ActionsRunJob, name string) (github.ActionsRunJob, error) {
	name = strings.TrimSpace(name)
	var names []string
	for _, j := range jobs {
		names = append(names, j.Name)
		if name != "" && (strings.EqualFold(j.Name, name) || strconv.FormatInt(j.ID, 10) == name) {
			return j, nil
		}
	}
	if name == "" {
		for _, j := range jobs {
			if actionsConclusion(j.Status, j.Conclusion) == ci.ConclusionFailure {
				return j, nil
			}
		}
		if len(jobs) == 1 {
			return jobs[0], nil
		}
		if len(jobs) == 0 {
			return github.ActionsRunJob{}, errors.New("this run has no jobs the forge can name")
		}
		return github.ActionsRunJob{}, fmt.Errorf("no job failed in this run: name one with ?job= (%s)", strings.Join(names, ", "))
	}
	return github.ActionsRunJob{}, fmt.Errorf("no job %q in this run (its jobs: %s)", name, strings.Join(names, ", "))
}

// handleGetActionsRun is GET /api/ci/runs/{id} for an Actions run.
func (s *Server) handleGetActionsRun(w http.ResponseWriter, r *http.Request, id string) {
	repoText, runID, ok := ParseActionsRunID(id)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a GitHub Actions run id: they are gha:<owner>:<name>:<run id>", id))
		return
	}
	repo, err := s.tasks.parseRepo(repoText)
	if err != nil {
		writeClientError(w, err)
		return
	}
	run, _, err := s.tasks.actionsRunDetail(r.Context(), repo, runID)
	if err != nil {
		if errors.Is(err, errActionsBudgetLow) {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleListActionsArtifacts is GET /api/ci/runs/{id}/artifacts for an
// Actions run. Listed only: an Actions artifact downloads as a zip from
// a redirect, sometimes gigabytes, and is fetched from its run's page
// rather than through grain.
func (s *Server) handleListActionsArtifacts(w http.ResponseWriter, r *http.Request, id string, list listQuery) {
	repoText, runID, ok := ParseActionsRunID(id)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a GitHub Actions run id", id))
		return
	}
	repo, err := s.tasks.parseRepo(repoText)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if err := s.tasks.actionsRepoServed(r.Context(), repo); err != nil {
		writeClientError(w, err)
		return
	}
	reader := s.tasks.actionsReader()
	if reader == nil {
		writeJSON(w, http.StatusOK, CIRunArtifactsResponse{Artifacts: []ci.Artifact{}})
		return
	}
	arts, err := cachedActions(s.tasks, fmt.Sprintf("artifacts|%s|%d", repo, runID), listTTL[[]github.ActionsArtifact],
		func() ([]github.ActionsArtifact, error) {
			return reader.ListActionsArtifacts(repo.StoredOwner(), repo.Name, runID)
		})
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("listing the run's artifacts on the forge: %w", err))
		return
	}
	out := make([]ci.Artifact, 0, len(arts))
	for _, a := range arts {
		note := actionsArtifactNote
		if a.Expired {
			note = "expired on the forge; " + note
		}
		out = append(out, ci.Artifact{Run: id, Name: a.Name, Bytes: a.Bytes, CollectedAt: a.CreatedAt, Note: note})
	}
	writeCIRunArtifacts(w, true, true, out, list)
}

// actionsArtifactNote is why an Actions artifact is listed and not served.
const actionsArtifactNote = "a GitHub Actions artifact is listed only: grain does not download it (it is a zip behind a redirect, " +
	"sometimes gigabytes); fetch it from the run's page"
