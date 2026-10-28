package ui

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/cifault"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A shared fault is one failure showing up on several branches at once
// (ci.SharedFault). This file is the half of it a reader of the API
// sees: the API's own answer to "is this red mine?", and -- where grain
// can honestly say so -- which open pull request already fixes it.
//
// The judgement behind that second half is pkg/cifault's, not this
// package's, and deliberately so: the orchestrator says the same
// sentence in the verdict comment it posts on a red pull request, and
// two readers of the same facts disagreeing about which change fixes a
// fault would be worse than neither of them saying anything.

// PullRequestFileLister is what this package needs of a GitHub client to
// find out what an open pull request touches. An alias rather than a
// declaration of its own, so that Config's field and pkg/cifault's
// argument are the one type: nothing has to convert, and a deployment
// can hand in cifault.FileCache wherever the plain client would do.
type PullRequestFileLister = cifault.FileLister

// CIFault is one ci.SharedFault as the API serves it, plus the open pull
// requests that touch the files the failure names.
type CIFault struct {
	ci.SharedFault
	// Fixes are those pull requests, newest task first. Empty is the
	// ordinary case and the honest one: most faults name no file, and a
	// fault whose file nobody has an open change against has no fix to
	// point at yet.
	Fixes []CIFaultFix `json:"fixes,omitempty"`
}

// CIFaultFix is one such pull request, and why it is being named --
// cifault.Fix with the JSON spelling the browser reads (ui/src/
// ciFaults.js).
type CIFaultFix struct {
	// PullRequest is model.PullRequestRef's own spelling, owner/name#n --
	// what pullRequestUrl (ui/src/state.js) turns into a link.
	PullRequest string `json:"pullRequest"`
	// Task is the grain task that opened it, where one did.
	Task string `json:"task,omitempty"`
	// Title is that task's title, so a row can say what the change is
	// rather than only its number.
	Title string `json:"title,omitempty"`
	// Files are the paths this pull request touches that the failure
	// named -- the evidence for the claim, on the row, so that a reader
	// who thinks it is wrong can see immediately why it was made.
	Files []string `json:"files"`
}

// ciFaultTTL is how long one computation of the fault list is reused.
//
// The CI pane polls every five seconds (CIPage's REFRESH_MS) and this
// costs a read of every recent failing run's output, so it is
// deliberately not recomputed per request. Thirty seconds is far inside
// the window a fault lives in (ci.DefaultFaultWindow, six hours) and far
// outside the poll.
const ciFaultTTL = 30 * time.Second

// ciFaultRunLimit bounds how many failing runs are read back for one
// computation. A deployment that has been red all day has hundreds; the
// fault is provable from the recent ones, and the bound is what keeps a
// bad day from turning a five-second poll into a table scan.
const ciFaultRunLimit = 60

// ciFaultFixesTimeout bounds how long faultFixes waits for GitHub pull
// request file lookups so slow GitHub responses cannot stall /api/ci/runs.
const ciFaultFixesTimeout = 2 * time.Second

type ciFaultCache struct {
	at     time.Time
	key    string
	ghKey  string
	faults []CIFault
}

func failedRunsKey(runs []ci.Run, now time.Time) string {
	var b strings.Builder
	count := 0
	for _, summary := range runs {
		if count >= ciFaultRunLimit {
			break
		}
		if !ci.RecentlyFailed(summary, now, ci.DefaultFaultWindow) {
			continue
		}
		count++
		b.WriteString(summary.ID)
		b.WriteByte(':')
		b.WriteString(string(summary.Conclusion))
		for _, job := range summary.Jobs {
			if job.Conclusion == ci.ConclusionFailure || job.Conclusion == ci.ConclusionErrored {
				b.WriteByte(',')
				b.WriteString(job.JobID)
				b.WriteByte('=')
				b.WriteString(string(job.Conclusion))
			}
		}
		b.WriteByte(';')
	}
	return b.String()
}

func failedGitHubJobsKey(jobs []model.GitHubCIJob) string {
	var b strings.Builder
	count := 0
	for _, j := range jobs {
		if count >= ciFaultRunLimit {
			break
		}
		if !j.Failed() || strings.TrimSpace(j.FailureTail) == "" {
			continue
		}
		count++
		b.WriteString(j.Repo)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(j.CheckRunID, 10))
		b.WriteByte('=')
		b.WriteString(j.Conclusion)
		b.WriteByte(';')
	}
	return b.String()
}

// CISharedFaults is every fault this deployment's CI is showing on more
// than one branch right now, newest first.
//
// It reads whole runs rather than the listing: a listing carries no step
// output (ci.RunStore.List), and output is the whole of the evidence a
// fault is claimed from. Only runs that failed inside the window are
// read back, newest first and bounded -- ci.RecentSharedFaults, which is
// where that reading rule lives now that the orchestrator's own verdict
// comment groups the same faults from the same store.
//
// Errors are logged and swallowed. This is an annotation on a table that
// is worth drawing without it: a GitHub call that fails, a store that
// will not answer, should cost the rows their extra sentence and not the
// pane.
func (c *Client) CISharedFaults(ctx context.Context) []CIFault {
	runner := c.Config.CI
	if runner == nil && c.Store == nil {
		return nil
	}
	now := c.now()
	var githubJobs []model.GitHubCIJob
	if c.Store != nil {
		githubJobs, _ = c.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
			Since: now.Add(-ci.DefaultFaultWindow),
		})
	}
	ghKey := failedGitHubJobsKey(githubJobs)
	c.ciFaultsMu.Lock()
	if c.ciFaults != nil && now.Sub(c.ciFaults.at) < ciFaultTTL && c.ciFaults.ghKey == ghKey {
		cached := make([]CIFault, len(c.ciFaults.faults))
		copy(cached, c.ciFaults.faults)
		c.ciFaultsMu.Unlock()
		return c.resolveFaultOwners(ctx, cached)
	}
	c.ciFaultsMu.Unlock()

	var runs []ci.Run
	if runner != nil {
		runs = runner.List()
	}
	return c.ciSharedFaultsFromRunsWithGitHub(ctx, runs, githubJobs, ghKey, now)
}

func (c *Client) ciSharedFaultsForRuns(ctx context.Context, runs []ci.Run) []CIFault {
	if c.Config.CI == nil && c.Store == nil {
		return nil
	}
	return c.ciSharedFaultsFromRuns(ctx, runs, c.now())
}

func (c *Client) ciSharedFaultsFromRuns(ctx context.Context, runs []ci.Run, now time.Time) []CIFault {
	runner := c.Config.CI
	if runner == nil && c.Store == nil {
		return nil
	}
	var githubJobs []model.GitHubCIJob
	if c.Store != nil {
		githubJobs, _ = c.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
			Since: now.Add(-ci.DefaultFaultWindow),
		})
	}
	return c.ciSharedFaultsFromRunsWithGitHub(ctx, runs, githubJobs, failedGitHubJobsKey(githubJobs), now)
}

func (c *Client) ciSharedFaultsFromRunsWithGitHub(ctx context.Context, runs []ci.Run,
	githubJobs []model.GitHubCIJob, ghKey string, now time.Time) []CIFault {
	runner := c.Config.CI
	if runner == nil && c.Store == nil {
		return nil
	}
	key := failedRunsKey(runs, now)
	c.ciFaultsMu.Lock()
	if c.ciFaults != nil && now.Sub(c.ciFaults.at) < ciFaultTTL && c.ciFaults.key == key && c.ciFaults.ghKey == ghKey {
		cached := make([]CIFault, len(c.ciFaults.faults))
		copy(cached, c.ciFaults.faults)
		c.ciFaultsMu.Unlock()
		return c.resolveFaultOwners(ctx, cached)
	}
	c.ciFaultsMu.Unlock()

	var raw []ci.SharedFault
	if runner != nil {
		get := c.cachedFaultRun(runner, runs)
		raw = ci.RecentSharedFaultsFromRuns(runs, get, now, ci.DefaultFaultWindow, ciFaultRunLimit)
	}
	if len(githubJobs) > 0 {
		raw = append(raw, ci.GitHubSharedFaults(githubJobs, now, ci.DefaultFaultWindow)...)
	}
	sort.SliceStable(raw, func(i, j int) bool {
		if !raw[i].Runs[0].At.Equal(raw[j].Runs[0].At) {
			return raw[i].Runs[0].At.After(raw[j].Runs[0].At)
		}
		if raw[i].Digest != raw[j].Digest {
			return raw[i].Digest < raw[j].Digest
		}
		return raw[i].Source < raw[j].Source
	})
	faults := make([]CIFault, 0, len(raw))
	for _, f := range raw {
		if c.Store != nil {
			f.OwnerTaskID, f.OwnerRef = cifault.ResolveOwner(ctx, c.Store, f)
		}
		faults = append(faults, CIFault{SharedFault: f, Fixes: c.faultFixes(ctx, f)})
	}
	c.ciFaultsMu.Lock()
	c.ciFaults = &ciFaultCache{at: now, key: key, ghKey: ghKey, faults: faults}
	c.ciFaultsMu.Unlock()
	return faults
}

func (c *Client) resolveFaultOwners(ctx context.Context, faults []CIFault) []CIFault {
	if c.Store != nil {
		for i := range faults {
			faults[i].OwnerTaskID, faults[i].OwnerRef = cifault.ResolveOwner(ctx, c.Store, faults[i].SharedFault)
		}
	}
	return faults
}

func (c *Client) cachedFaultRun(runner ci.Runner, listed []ci.Run) func(string) (ci.Run, bool) {
	active := make(map[string]bool, len(listed))
	for _, r := range listed {
		active[r.ID] = true
	}
	c.ciFaultsMu.Lock()
	for id := range c.ciFaultRuns {
		if !active[id] {
			delete(c.ciFaultRuns, id)
		}
	}
	c.ciFaultsMu.Unlock()

	return func(id string) (ci.Run, bool) {
		c.ciFaultsMu.Lock()
		if c.ciFaultRuns != nil {
			if cached, ok := c.ciFaultRuns[id]; ok {
				c.ciFaultsMu.Unlock()
				return cached, true
			}
		}
		c.ciFaultsMu.Unlock()

		run, ok := runner.Get(id)
		if !ok {
			return ci.Run{}, false
		}
		if ci.IsConcludedRun(run) {
			slim := ci.ClassifyFaultRun(run)
			c.ciFaultsMu.Lock()
			if c.ciFaultRuns == nil {
				c.ciFaultRuns = make(map[string]ci.Run)
			}
			c.ciFaultRuns[id] = slim
			c.ciFaultsMu.Unlock()
			return slim, true
		}
		return run, true
	}
}

// faultFixes is cifault.Fixes in this package's own vocabulary: the open
// pull requests grain is willing to name as fixing this fault, on the
// terms that package documents.
//
// The GitHub call behind it goes through a cifault.FileCache held on the
// Client, since the pane asks every five seconds and what a pull request
// touches changes only when somebody pushes to it.
func (c *Client) faultFixes(ctx context.Context, fault ci.SharedFault) []CIFaultFix {
	if c.Config.GitHubPullRequestFiles == nil || c.Store == nil {
		return nil
	}
	fixCtx, cancel := context.WithTimeout(ctx, ciFaultFixesTimeout)
	defer cancel()
	found := cifault.Fixes(fixCtx, c.Store, c.pullRequestFiles(), fault)
	if len(found) == 0 {
		return nil
	}
	fixes := make([]CIFaultFix, 0, len(found))
	for _, fix := range found {
		fixes = append(fixes, CIFaultFix{
			PullRequest: fix.PullRequest,
			Task:        fix.Task,
			Title:       fix.Title,
			Files:       fix.Files,
		})
	}
	return fixes
}

// pullRequestFiles is this Client's memo of what each candidate pull
// request touches, built on first use so that a Config whose GitHub
// client was set after construction -- which is every test, and
// cmd/grain's own wiring order -- still gets one.
func (c *Client) pullRequestFiles() *cifault.FileCache {
	c.prFilesMu.Lock()
	defer c.prFilesMu.Unlock()
	if c.prFiles == nil {
		cache := cifault.NewFileCache(c.Config.GitHubPullRequestFiles)
		if cache == nil {
			return nil
		}
		cache.Now = c.now
		c.prFiles = cache
	}
	return c.prFiles
}
