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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
func ActionsRunID(repo string, runID int64) string {
	return ActionsRunIDPrefix + strings.ReplaceAll(repo, "/", ":") + ":" + strconv.FormatInt(runID, 10)
}

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
		if reader, ok := cand.(github.ActionsReader); ok {
			return reader
		}
	}
	return nil
}

// actionsListedFor reports whether repo's Actions runs are listed: a
// repository gated by GitHub (ci_mode: github), or one whose GitHub CI is
// not ignored. The config repository grain CI always runs, and a repo on
// grain CI or none, are not.
func (c *Client) actionsListedFor(ctx context.Context, repo model.RepoRef) bool {
	if c.Store == nil || grainci.RequiredFor(repo) || c.actionsRepoServed(ctx, repo) != nil {
		return false
	}
	eff, err := c.Store.EffectiveRepoConfig(ctx, repo)
	if err != nil {
		return false
	}
	switch eff.CIMode {
	case model.CIModeGrain, model.CIModeNone:
		return false
	}
	return true
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

// actionsRunListing is what wantsActions decided for a list request.
type actionsRunListing struct {
	repo model.RepoRef
	// note says why the rows came from grain's own record, or why there
	// are none, when the forge could not be asked.
	note string
}

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
	if filter.Provider == "" && s.tasks.actor(r.Context()).Kind == model.PrincipalHuman {
		return model.RepoRef{}, false
	}
	repo, err := s.tasks.parseRepo(filter.Repo)
	if err != nil || !s.tasks.actionsListedFor(r.Context(), repo) {
		return model.RepoRef{}, false
	}
	return repo, true
}

// listActionsRuns is repo's Actions runs matching filter, newest first,
// at most limit of them: the forge's own listing where it can be asked,
// with what grain recorded of each run's jobs folded in, and grain's
// record alone where it cannot. note says which, when it was the record.
func (c *Client) listActionsRuns(ctx context.Context, repo model.RepoRef, filter ci.RunFilter, limit int) (runs []ci.Run, note string) {
	if limit <= 0 {
		limit = ci.MaxListedRuns
	}
	recorded := c.recordedActionsRuns(ctx, repo, filter.Since)
	byID := map[string]ci.Run{}
	var out []ci.Run
	add := func(run ci.Run) {
		if _, dup := byID[run.ID]; dup {
			return
		}
		byID[run.ID] = run
		out = append(out, run)
	}
	forge, err := c.forgeActionsRuns(repo, filter, limit)
	switch {
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
	if len(kept) > limit {
		kept = kept[:limit]
	}
	return kept, note
}

// forgeActionsRuns is the forge's page of repo's runs, as rows. A ref is
// asked of the forge as a branch first, since GitHub matches only an
// exact one, and then unnarrowed for the substring match ?ref= means.
func (c *Client) forgeActionsRuns(repo model.RepoRef, filter ci.RunFilter, limit int) ([]ci.Run, error) {
	reader := c.actionsReader()
	if reader == nil {
		return nil, errNoActionsReader
	}
	ask := func(branch string) ([]github.WorkflowRun, error) {
		q := github.ActionsRunsQuery{Branch: branch, Since: filter.Since, Limit: min(limit, 100)}
		key := fmt.Sprintf("runs|%s|%s|%d|%d", repo, branch, q.Since.Unix(), q.Limit)
		return cachedActions(c, key, listTTL[[]github.WorkflowRun], func() ([]github.WorkflowRun, error) {
			return reader.ListActionsRuns(repo.StoredOwner(), repo.Name, q)
		})
	}
	var runs []github.WorkflowRun
	var err error
	if filter.Ref != "" {
		if runs, err = ask(filter.Ref); err != nil {
			return nil, err
		}
	}
	if len(runs) == 0 {
		if runs, err = ask(""); err != nil {
			return nil, err
		}
	}
	out := make([]ci.Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, actionsRunRow(repo, r))
	}
	return out, nil
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
			RunID: r.ID, RunAttempt: r.RunAttempt, Workflow: r.Path, WorkflowName: r.Name,
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
// jobs (model.GitHubCIJob), as runs keyed by their ci:read id.
func (c *Client) recordedActionsRuns(ctx context.Context, repo model.RepoRef, since time.Time) map[string]ci.Run {
	out := map[string]ci.Run{}
	if c.Store == nil {
		return out
	}
	jobs, err := c.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{Repo: repo.String(), Since: since, Limit: 2000})
	if err != nil {
		return out
	}
	byRun := map[int64][]model.GitHubCIJob{}
	for _, j := range jobs {
		if j.RunID != 0 {
			byRun[j.RunID] = append(byRun[j.RunID], j)
		}
	}
	for runID, js := range byRun {
		if run, ok := recordedActionsRun(repo, runID, js); ok {
			out[run.ID] = run
		}
	}
	return out
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
			return actionsRunWithJobs(repo, *d.run, d.jobs), d.jobs, nil
		}
		forgeErr = err
	} else {
		forgeErr = errNoActionsReader
	}
	if c.Store != nil {
		jobs, err := c.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{Repo: repo.String(), RunID: runID})
		if err == nil {
			if run, ok := recordedActionsRun(repo, runID, jobs); ok {
				return run, nil, nil
			}
		}
	}
	if forgeErr == nil {
		forgeErr = &NotFoundError{ID: ActionsRunID(repo.String(), runID),
			message: fmt.Sprintf("no GitHub Actions run %d in %s", runID, repo)}
	}
	return ci.Run{}, nil, forgeErr
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
			job.Steps = append(job.Steps, ci.StepRun{Name: st.Name, Conclusion: actionsConclusion(st.Status, st.Conclusion)})
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
		return reader.ActionsJobLog(repo.StoredOwner(), repo.Name, job.ID)
	})
	if err != nil {
		var gh *github.Error
		switch {
		case errors.Is(err, errActionsBudgetLow):
			return job, "", http.StatusServiceUnavailable, err
		case errors.As(err, &gh) && (gh.Status == http.StatusNotFound || gh.Status == http.StatusGone):
			return job, "", http.StatusNotFound, fmt.Errorf("the forge has no log for job %q (%d): it is past its retention window, or not written yet", job.Name, job.ID)
		case errors.As(err, &gh) && gh.Status == http.StatusForbidden:
			return job, "", http.StatusForbidden, fmt.Errorf("the forge refused job %q's log to this deployment's credential (it needs actions:read)", job.Name)
		default:
			return job, "", http.StatusBadGateway, fmt.Errorf("reading job %q's log from the forge: %w", job.Name, err)
		}
	}
	return job, text, http.StatusOK, nil
}

// ExternalCIRuns is this client's GitHub Actions runs as ci.ExternalRuns,
// for the sandbox namespace and CLI.
func (c *Client) ExternalCIRuns() ci.ExternalRuns { return actionsExternalRuns{c} }

type actionsExternalRuns struct{ c *Client }

func (a actionsExternalRuns) FindExternalRuns(ctx context.Context, f ci.RunFilter, limit int) ([]ci.Run, string) {
	if f.Provider != "" && !strings.EqualFold(f.Provider, ci.ProviderGitHubActions) {
		return nil, ""
	}
	repo, err := a.c.parseRepo(f.Repo)
	if err != nil || !a.c.actionsListedFor(ctx, repo) {
		return nil, ""
	}
	return a.c.listActionsRuns(ctx, repo, f, limit)
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
func (s *Server) handleListActionsArtifacts(w http.ResponseWriter, r *http.Request, id string) {
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
	writeJSON(w, http.StatusOK, CIRunArtifactsResponse{Enabled: true, Available: true, Artifacts: out})
}

// actionsArtifactNote is why an Actions artifact is listed and not served.
const actionsArtifactNote = "a GitHub Actions artifact is listed only: grain does not download it (it is a zip behind a redirect, " +
	"sometimes gigabytes); fetch it from the run's page"
