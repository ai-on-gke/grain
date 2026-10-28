package orchestrator

// A repository whose base branch requires GitHub's own merge queue -- a
// ruleset's "Require merge queue", which an organisation may set as policy
// and nobody on the grain side can lift -- refuses every direct merge with
// "Changes must be made through the merge queue". grain's queue still
// decides *when* a change may land (its own CI, its order, its review);
// what changes is only the last step. Instead of the merge call, the pull
// request is handed to GitHub's queue (github.MergeQueuer), GitHub builds
// its merge group and merges it, and the task is closed out the way any
// merge is: on the cycle that reads the pull request merged.
//
// Between the hand-off and that read the entry is "github_merge_queue"
// (mergeStateGitHubQueue). It holds its repository's slot, because the
// entry is still landing and the one behind it would otherwise be merged
// -- or queued -- past it; GitHub keeps its own order from there. And it
// is polled, not re-merged: GitHub's queue can drop a pull request without
// merging it (its checks failed in the merge group, it conflicted with
// what landed ahead of it, somebody removed it), which the poll notices
// and answers by queuing it again, a bounded number of times per head.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// githubQueuePollInterval is how often a pull request handed to GitHub's
// merge queue is asked about, for whether it is still in it. The merge
// itself is noticed sooner -- every cycle reads the pull request anyway --
// so this bounds only how quickly a drop from the queue is answered.
const githubQueuePollInterval = 2 * time.Minute

// maxGitHubEnqueues is how many times one head is put into GitHub's merge
// queue before the queue's dropping it is taken as an answer rather than
// as bad luck: past that it is reported as a refused merge, which a
// person retries (Retry merge) once whatever GitHub's queue is failing on
// is fixed.
const maxGitHubEnqueues = 3

// githubQueued is what grain knows about one pull request it has handed
// to GitHub's merge queue.
type githubQueued struct {
	head     string
	entry    github.MergeQueueEntry
	enqueues int
	polled   time.Time
}

var (
	githubQueuedMu sync.Mutex
	// githubQueuedPRs is keyed by model.PullRequestRef.String(). In memory,
	// like the merge-refusal record beside it: a restart forgets it, and
	// the next merge call is refused the same way and hands the pull
	// request over again -- EnqueuePullRequest leaving one already in the
	// queue where it is.
	githubQueuedPRs = map[string]githubQueued{}
	// githubQueueBranches is every base branch GitHub has said requires
	// its merge queue, keyed "owner/name:branch", so that the batcher stops
	// building batches it would only be refused the merge of.
	githubQueueBranches = map[string]bool{}
)

func githubQueueBranchKey(repo model.RepoRef, branch string) string {
	return repo.String() + ":" + branch
}

// lastGitHubQueued is the record of ref's head having been handed to
// GitHub's merge queue. A record about another head is a push since,
// which GitHub answers by taking the pull request out of its queue; it is
// dropped here and reported as no record.
func lastGitHubQueued(ref model.PullRequestRef, head string) (githubQueued, bool) {
	githubQueuedMu.Lock()
	defer githubQueuedMu.Unlock()
	q, ok := githubQueuedPRs[ref.String()]
	if !ok {
		return githubQueued{}, false
	}
	if q.head != head {
		delete(githubQueuedPRs, ref.String())
		return githubQueued{}, false
	}
	return q, true
}

func putGitHubQueued(ref model.PullRequestRef, q githubQueued) {
	githubQueuedMu.Lock()
	defer githubQueuedMu.Unlock()
	githubQueuedPRs[ref.String()] = q
}

func forgetGitHubQueued(ref model.PullRequestRef) {
	githubQueuedMu.Lock()
	defer githubQueuedMu.Unlock()
	delete(githubQueuedPRs, ref.String())
}

func noteGitHubQueueBranch(repo model.RepoRef, branch string) {
	githubQueuedMu.Lock()
	defer githubQueuedMu.Unlock()
	githubQueueBranches[githubQueueBranchKey(repo, branch)] = true
}

// requiresGitHubQueue reports whether GitHub has told this process that
// branch only takes changes through its merge queue.
func requiresGitHubQueue(repo model.RepoRef, branch string) bool {
	githubQueuedMu.Lock()
	defer githubQueuedMu.Unlock()
	return githubQueueBranches[githubQueueBranchKey(repo, branch)]
}

// errGitHubQueueDropped is a pull request GitHub's merge queue has removed
// without merging more times than maxGitHubEnqueues allows. Said as a 405
// in GitHub's own shape so that refusedMerge files it with the rule
// refusals (github.ClassifyMergeRefusal reads "merge queue" as a rule):
// held, not retried blind, until a person presses Retry merge.
func errGitHubQueueDropped(ref model.PullRequestRef, n int) error {
	body, _ := json.Marshal(map[string]string{"message": fmt.Sprintf(
		"GitHub's merge queue took %s out %d times without merging it. The merge group's own checks, "+
			"or a conflict with what landed ahead of it, are what to look at on GitHub.", ref, n)})
	return &github.Error{Status: http.StatusMethodNotAllowed, Body: body}
}

// enqueueOnGitHub hands ref's head to GitHub's merge queue, or -- for a
// head already handed over -- asks whether it is still there and hands it
// over again if GitHub has dropped it. It returns the entry as GitHub
// last described it, or an error for refusedMerge: the client cannot
// enqueue (refused, the original merge refusal standing), GitHub would
// not queue it, or the queue has dropped it too often.
//
// refused is the merge refusal that brought the pull request here, nil
// where the record says it was handed over already.
func enqueueOnGitHub(client github.Client, ref model.PullRequestRef, base, head string,
	refused error, now time.Time) (github.MergeQueueEntry, error) {

	queuer, ok := client.(github.MergeQueuer)
	if !ok {
		forgetGitHubQueued(ref)
		if refused == nil {
			refused = errors.New("this GitHub client cannot add pull requests to GitHub's merge queue")
		}
		return github.MergeQueueEntry{}, refused
	}
	noteGitHubQueueBranch(ref.Repo, base)
	q, known := lastGitHubQueued(ref, head)
	if known {
		if now.Sub(q.polled) < githubQueuePollInterval {
			return q.entry, nil
		}
		entry, err := queuer.MergeQueueEntryOf(ref.Repo.Owner, ref.Repo.Name, ref.Number)
		if err != nil {
			return q.entry, err
		}
		q.polled = now
		if entry != nil {
			q.entry = *entry
			putGitHubQueued(ref, q)
			return q.entry, nil
		}
		// Not in the queue and -- since the caller read the pull request
		// open a moment ago -- not merged: GitHub dropped it.
		if q.enqueues >= maxGitHubEnqueues {
			forgetGitHubQueued(ref)
			return github.MergeQueueEntry{}, errGitHubQueueDropped(ref, q.enqueues)
		}
	} else {
		q = githubQueued{head: head}
	}
	entry, err := queuer.EnqueuePullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number, head)
	if err != nil {
		forgetGitHubQueued(ref)
		return github.MergeQueueEntry{}, err
	}
	if !entry.AlreadyQueued {
		q.enqueues++
	}
	q.entry, q.polled = entry, now
	putGitHubQueued(ref, q)
	return entry, nil
}

// landThroughGitHubQueue is syncEntry's merge arm for a pull request
// GitHub will only merge through its own queue. handled is false where
// this has nothing to do with it -- the merge was not refused for the
// queue and no hand-off is on record -- and err is then the merge's own.
// Otherwise err is nil once the pull request is in GitHub's queue, with
// the row saying so, and is for refusedMerge when it could not be put
// there.
func landThroughGitHubQueue(ctx context.Context, store *model.Store, client github.Client, e queueEntry,
	ref model.PullRequestRef, detail github.PullRequestDetail, mergeErr error, now time.Time) (handled bool, err error) {

	_, known := lastGitHubQueued(ref, detail.HeadSHA)
	if !known && !github.IsMergeQueueRequired(mergeErr) {
		return false, mergeErr
	}
	entry, err := enqueueOnGitHub(client, ref, detail.BaseRef, detail.HeadSHA, mergeErr, now)
	if err != nil {
		return true, err
	}
	// The hand-off is a merge as far as the refusal record is concerned:
	// the run of refusals that led here is over.
	forgetMergeRefusals(ref)
	return true, setMergeQueueState(ctx, store, e.obs, e.task.ID, githubQueueStatus(entry, detail.BaseRef), now)
}

// githubQueueStatus is the row for a pull request in GitHub's merge
// queue, in GitHub's own words for where it stands.
func githubQueueStatus(entry github.MergeQueueEntry, base string) queueStatus {
	note := fmt.Sprintf("%s only takes changes through GitHub's merge queue, so grain has added this "+
		"pull request to it; GitHub merges it once its merge group passes", base)
	switch entry.State {
	case "":
	case "AWAITING_CHECKS":
		note += " -- its merge group's checks are running"
	case "UNMERGEABLE":
		note += " -- GitHub says its merge group cannot merge, and will take it out of the queue"
	case "MERGEABLE", "LOCKED":
		note += " -- GitHub is merging it now"
	default:
		if entry.Position > 0 {
			note += fmt.Sprintf(" -- position %d in the queue", entry.Position)
		}
	}
	return queueStatus{state: mergeStateGitHubQueue, note: note}
}
