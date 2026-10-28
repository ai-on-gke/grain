package cli

// The controller-owned config repository participates in the same task and
// merge-queue lifecycle as an external repository, but none of its repository
// operations belong on GitHub. stateRepoPullRequestClient overlays the subset
// of github.Client used by that lifecycle with local git operations and falls
// through to the embedded client for every ordinary repository.
//
// Keeping the adapter at this boundary lets the mature pull-request state
// machine remain shared: a config change still has a fixes link, can await
// Submit, observes conflicts, and auto-merges in queue order. Its synthetic PR
// number is the task's numeric ID; all durable state already lives on that task
// and its branch, so there is no second PR table to reconcile.
