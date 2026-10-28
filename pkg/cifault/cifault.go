// Package cifault is the second half of a shared CI fault: given one
// failure showing up on several branches at once (ci.SharedFault, where
// the judgement about what counts as one lives), which open pull request
// -- if any -- already fixes it.
//
// The afternoon it was written for is in pkg/ci/sharedfault.go. The
// short of it: six branches red with the same proxy error, four tasks in
// the merge lane being repaired one by one against a cause none of them
// owned, and the pull request that fixed it sitting finished and
// unmerged the whole time. Nothing on any row connected any of it.
//
// It is a package of its own because two unrelated halves of grain have
// to say the same sentence and must not say different ones. pkg/ui
// serves it on GET /api/ci/runs and GET /api/ci/faults, which is what
// the CI pane draws and what an agent's wait_for_checks reads back
// through the daemon; pkg/orchestrator says it in the verdict comment it
// posts on a red pull request, which is what a human reviewer reads
// first. Before this package the lookup lived in pkg/ui, which the
// orchestrator cannot import, so the comment could only point at the
// pane and hope somebody went and looked.
package cifault

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// FileLister is the read-only slice of pkg/github.Client this package
// uses to find out what an open pull request touches -- a seam of its
// own so that the cache and the failure behaviour can be tested without
// teaching anything here how GitHub authentication works.
//
// nil is a deployment that cannot ask. It costs a fault its second half
// (which pull request fixes this) and nothing else: the fault itself is
// computed from grain's own runs.
type FileLister interface {
	ListPullRequestFiles(owner, repo string, number int) ([]string, error)
}

// Store is the half of *model.Store this package needs: which pull
// requests are open and grain's own, and the task each one belongs to.
//
// Narrowed to two methods for the same reason FileLister is narrow --
// these are the only two questions asked, and a fake answering them is
// the whole of what a test of this judgement needs.
type Store interface {
	OpenPullRequestLinks(ctx context.Context) ([]model.TaskPullRequestLink, error)
	GetTask(ctx context.Context, id string) (*model.Task, error)
}

// Fix is one open pull request grain is willing to name as fixing a
// fault, and why it is being named.
type Fix struct {
	// PullRequest is model.PullRequestRef's own spelling, owner/name#n --
	// what pullRequestUrl (ui/src/state.js) turns into a link.
	PullRequest string
	// Task is the grain task that opened it, where one did.
	Task string
	// Title is that task's title, so a reader can say what the change is
	// rather than only its number.
	Title string
	// Files are the paths this pull request touches that the failure
	// named -- the evidence for the claim, said out loud, so that a
	// reader who thinks it is wrong can see immediately why it was made.
	Files []string
}

// Where is why this pull request is being named, in the half of a
// sentence a line has room for. ui/src/ciFaults.js's fixWhere and
// pkg/mcp's faultFixFiles say the same thing to their own readers.
func (f Fix) Where() string {
	switch len(f.Files) {
	case 0:
		return "a file this failure names"
	case 1:
		return f.Files[0]
	default:
		return fmt.Sprintf("%d of the files this failure names", len(f.Files))
	}
}

// MaxFixes is how many pull requests one fault may name before it names
// none.
//
// A fault whose file four open changes all touch (go.mod, a shared
// workflow) has not identified a fix: it has identified a popular file,
// and an answer offering four candidates has moved the reader's problem
// rather than solved it. Silence is the better answer, and the fault
// itself -- "this is failing on five other branches" -- is still said.
const MaxFixes = 3

// Fixes is the open pull requests that touch a file this fault's output
// named, newest task first.
//
// Three conditions, and all of them are about not naming the wrong
// change. The pull request has to be open and grain's own
// (Store.OpenPullRequestLinks -- a run that is over, a pull request
// nothing has closed); it has to touch a file *every* branch in the
// fault printed (ci.SharedFault.Paths is already the intersection); and
// it must not be open from a branch the fault itself is on, because a
// branch that is red with this fault plainly does not fix it.
//
// Empty is the ordinary case and the honest one: most faults name no
// file, and a fault whose file nobody has an open change against has no
// fix to point at yet. Errors are logged and swallowed for the same
// reason -- this annotates an answer that is worth giving without it.
func Fixes(ctx context.Context, store Store, files FileLister, fault ci.SharedFault) []Fix {
	if len(fault.Paths) == 0 || store == nil || files == nil {
		return nil
	}
	repo, err := model.ParseRepo(fault.Repo)
	if err != nil {
		return nil
	}
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		log.Printf("ci faults: reading open pull requests: %v", err)
		return nil
	}
	failing := map[string]bool{}
	for _, ref := range fault.Refs {
		failing[ref] = true
	}
	var fixes []Fix
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			log.Printf("ci faults: reading open pull request files stopped: %v", err)
			break
		}
		ref, err := model.ParsePullRequestRef(link.PullRequest)
		if err != nil || ref.Repo != repo {
			continue
		}
		task, err := store.GetTask(ctx, link.TaskID)
		if err != nil || task == nil {
			continue
		}
		if failing[task.BranchName()] {
			continue
		}
		paths, err := listPullRequestFilesWithContext(ctx, files, repo.Owner, repo.Name, ref.Number)
		if err != nil {
			log.Printf("ci faults: reading files of %s: %v", link.PullRequest, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		var matched []string
		for _, path := range fault.Paths {
			for _, file := range paths {
				if ci.SameFile(path, file) {
					matched = append(matched, file)
					break
				}
			}
		}
		if len(matched) == 0 {
			continue
		}
		fixes = append(fixes, Fix{
			PullRequest: link.PullRequest,
			Task:        link.TaskID,
			Title:       task.Title,
			Files:       matched,
		})
	}
	if len(fixes) > MaxFixes {
		return nil
	}
	sort.SliceStable(fixes, func(i, j int) bool { return newerTask(fixes[i].Task, fixes[j].Task) })
	return fixes
}

func listPullRequestFilesWithContext(ctx context.Context, files FileLister, owner, repo string, number int) ([]string, error) {
	if ctx.Done() == nil {
		return files.ListPullRequestFiles(owner, repo, number)
	}
	type result struct {
		paths []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		p, err := files.ListPullRequestFiles(owner, repo, number)
		ch <- result{paths: p, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.paths, r.err
	}
}

// newerTask orders task ids newest first. The store allocates them as
// increasing decimal strings, so a longer id is a newer one -- compared
// as plain strings, task 999 sorted ahead of task 1000.
func newerTask(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// OwnerStore is the optional slice of *model.Store used by ResolveOwner
// to check task state and open base-repair tags.
type OwnerStore interface {
	Store
	State(ctx context.Context, id string) (model.State, error)
	OpenTasksWithTag(ctx context.Context, tag string) ([]string, error)
}

type ownerObservationStore interface {
	GetObservation(ctx context.Context, taskID string) (*model.Observation, error)
}

// ResolveOwner determines the single task responsible for fixing fault in
// its repository/base-branch lane:
//  1. An open, approved base-repair / FixesBaseBranch task for the lane, if any.
//  2. Otherwise, the first open, unblocked task in queue order (OrderKey, then ID)
//     among the branches hitting fault.
func ResolveOwner(ctx context.Context, store Store, fault ci.SharedFault) (ownerTaskID, ownerRef string) {
	return ResolveOwnerForBase(ctx, store, fault, "")
}

// ResolveOwnerForBase is ResolveOwner scoped to baseBranch when baseBranch is
// non-empty, so paired base-branch lanes (e.g. main vs candidate) resolve their
// own fault owner independently.
func ResolveOwnerForBase(ctx context.Context, store Store, fault ci.SharedFault, baseBranch string) (ownerTaskID, ownerRef string) {
	ownerTaskID, ownerRef = fault.OwnerTaskID, fault.OwnerRef
	if store == nil {
		return ownerTaskID, ownerRef
	}
	os, hasOwnerStore := store.(OwnerStore)
	obsStore, hasObsStore := store.(ownerObservationStore)
	eligible := func(task *model.Task) bool {
		if task == nil {
			return false
		}
		if baseBranch != "" {
			tb := task.Base
			if tb == "" {
				tb = "main"
			}
			if tb != baseBranch {
				return false
			}
		}
		if hasOwnerStore {
			if st, err := os.State(ctx, task.ID); err == nil {
				if st == model.StateClosed || st == model.StateFailed {
					return false
				}
				if st == model.StateProposed && (task.Approval == nil || task.DeferredAt != nil) {
					return false
				}
			}
		}
		if hasObsStore {
			if obs, err := obsStore.GetObservation(ctx, task.ID); err == nil && obs != nil && obs.MergeQueueBlockedAt != nil {
				return false
			}
		}
		return true
	}

	type candidate struct {
		task model.Task
		ref  string
	}
	var candidates []candidate
	inferredBase := baseBranch
	if inferredBase == "" {
		inferredBase = "main"
	}
	for _, r := range fault.Runs {
		tid := r.TaskID
		if tid == "" {
			tid = ci.TaskIDFromRef(r.Ref)
		}
		if tid == "" {
			continue
		}
		task, err := store.GetTask(ctx, tid)
		if err != nil || !eligible(task) {
			continue
		}
		if baseBranch == "" && len(candidates) == 0 && task.Base != "" {
			inferredBase = task.Base
		}
		ref := r.Ref
		if ref == "" {
			ref = task.BranchName()
		}
		candidates = append(candidates, candidate{task: *task, ref: ref})
	}
	if repo, err := model.ParseRepo(fault.Repo); err == nil && hasOwnerStore {
		tag := model.BaseRepairBranchTag(repo, inferredBase)
		if ids, err := os.OpenTasksWithTag(ctx, tag); err == nil {
			for i := len(ids) - 1; i >= 0; i-- {
				task, err := store.GetTask(ctx, ids[i])
				if err != nil || !eligible(task) {
					continue
				}
				if model.GrainBaseRepair(*task) || (task.Approval != nil && task.DeferredAt == nil && !slices.Contains(task.Tags, "base-ci-green-noted")) {
					return task.ID, task.BranchName()
				}
			}
		}
	}
	if len(candidates) > 0 {
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].task.OrderKey != candidates[j].task.OrderKey {
				return candidates[i].task.OrderKey < candidates[j].task.OrderKey
			}
			return ci.CompareTaskIDs(candidates[i].task.ID, candidates[j].task.ID) < 0
		})
		return candidates[0].task.ID, candidates[0].ref
	}
	if len(fault.Runs) > 0 {
		return "", ""
	}
	if ownerTaskID != "" {
		if task, err := store.GetTask(ctx, ownerTaskID); err == nil && task != nil && !eligible(task) {
			return "", ""
		}
	}
	return ownerTaskID, ownerRef
}
