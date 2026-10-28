package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// GitHubBranchLister is the read-only part of pkg/github.Client this UI uses.
// Keeping the seam narrow lets the API cache and failure behavior be tested
// without teaching pkg/ui how GitHub authentication works.
type GitHubBranchLister interface {
	ListBranches(owner, repo string) ([]string, error)
}

// RepoBranchSource lists a repository's branches without GitHub's API
// (Config.RepoBranches). ok is false for a repository it does not serve,
// which is then asked of GitHub; an error with ok true is that source
// failing, and is never retried against GitHub, which would only answer
// a 404 about a repository it does not have.
type RepoBranchSource interface {
	RepoBranches(ctx context.Context, repo model.RepoRef) (names []string, ok bool, err error)
}

// PullRequestReader is the one read of a pull request this UI makes for
// itself: its current head, for Client.ForceMerge. Narrow for the same
// reason GitHubBranchLister is.
type PullRequestReader interface {
	GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error)
}

// RepoComparer renders the change head would bring to base. An empty base asks
// the implementation to use the repository's default branch. Implementations
// may read a local config repository, a Git proxy, or any other Git remote;
// the UI does not need to know which forge (if any) owns it.
type RepoComparer interface {
	Compare(ctx context.Context, repo model.RepoRef, base, head string) (BranchComparison, error)
}

// BranchComparison is one comparison as the pull-request view reads it:
// which two refs were compared, the commit the diff is actually taken
// from, and whether base has the change already.
//
// MergeBase is that commit -- where head last forked from base -- and it
// is reported rather than left implicit because it is the whole of what
// makes this view survive a merge. A diff of base against a head base
// already contains is empty, so a comparison that only ever named two
// branches went blank the moment the branch landed (grain/task-375);
// naming the fork point says which change is on screen, and for a merged
// branch it is a commit no branch name points at any more.
//
// Merged is base already containing head -- a branch that has landed.
// The diff beside it is the change it brought, reconstructed from the
// merge, so it says "this is history" rather than "this is outstanding".
type BranchComparison struct {
	Base      string `json:"base"`
	Head      string `json:"head"`
	MergeBase string `json:"mergeBase,omitempty"`
	Merged    bool   `json:"merged,omitempty"`
	Diff      string `json:"diff"`
}

const (
	githubBranchCacheTTL      = 5 * time.Minute
	githubBranchErrorCacheTTL = 15 * time.Second
)

type githubBranchCacheEntry struct {
	ready     chan struct{}
	names     []string
	err       error
	expiresAt time.Time
	fetchedAt time.Time
}

var (
	errGitHubBranchesUnavailable = errors.New("GitHub branch listing is not available on this UI server")
	errNoGitHubRepoCredential    = errors.New("no GitHub credential is configured for this repository")
	// errConfigRepoBranchesUnavailable is the config repository's answer
	// on a UI server with no local source for its branches: it is not on
	// GitHub, so asking GitHub could only ever be a 404 about some other
	// repository.
	errConfigRepoBranchesUnavailable = errors.New("branch listing is not available for the internal config repository on this UI server")
)

// localBranchesError is Config.RepoBranches failing: grain's own read of
// a repository it serves or reaches over git, and so this server's
// failure (500) rather than an upstream API's (502).
type localBranchesError struct{ err error }

func (e localBranchesError) Error() string { return e.err.Error() }
func (e localBranchesError) Unwrap() error { return e.err }

// Branch is one requested branch's own JSON shape -- bwsalmon/
// agents#638's own "create a new branch on a repo from the repo page,"
// and grain/task-176's own "add a branch that already exists."
type Branch struct {
	Repo string `json:"repo"`
	Name string `json:"name"`
	// From is the branch Name is cut from, "" for the repo's default
	// branch (model.Branch.From).
	From string `json:"from,omitempty"`
	// Status is model.BranchStatus as a string: "pending" until the
	// branches reconciler has been to GitHub, then "created" if it cut
	// the ref or "adopted" if the name was already there.
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	// Error is the branches reconciler's own account of why this branch
	// isn't on grain's books yet, if anything has gone wrong.
	Error string `json:"error,omitempty"`
	// AlreadyRequested is set only on CreateBranch's answer, when repo
	// already had a row for Name and that row is what came back rather
	// than a new one -- so the form can say "already added" instead of
	// leaving somebody to wonder why the list did not grow.
	AlreadyRequested bool `json:"alreadyRequested,omitempty"`
}

func branchFrom(b model.Branch) Branch {
	return Branch{
		Repo: b.Repo.String(), Name: b.Name, From: b.From, Status: string(b.Status),
		CreatedAt: b.CreatedAt, Error: b.LastError,
	}
}

func branchesFrom(bs []model.Branch) []Branch {
	out := make([]Branch, len(bs))
	for i, b := range bs {
		out[i] = branchFrom(b)
	}
	return out
}

// CreateBranchRequest is a branch's user-given name -- the issue's own
// "create the given branch", or the name of one already on GitHub.
type CreateBranchRequest struct {
	Name string `json:"name"`
	// From is the branch to cut Name from, "" for the repo's default
	// branch (model.Branch.From).
	From string `json:"from,omitempty"`
}

// CreateBranch is the issue's own "Add the ability to create a new branch
// on a repo from the repo page. This should just create the given branch
// in github", and grain/task-176's "if a branch already exists on a repo,
// we should be able to add it to grain": it records a fresh model.Branch
// declaring the intent, and returns immediately -- the branches
// reconciler (pkg/orchestrator.SyncBranches) is what goes to GitHub, and
// either creates the branch at repo's current default branch tip or
// adopts the ref already there, untouched.
//
// There is one request for both because the caller cannot know which it
// is asking for -- see model.(*Store).CreateBranch -- so a fresh branch
// always reads "pending", and its status says which happened only once
// the reconciler has been. Asking for a name repo already has answers
// with that row as it stands, AlreadyRequested set, and records nothing
// new (model.(*Store).RequestBranch).
//
// Only a system admin or one of repo's admins may ask (requireRepoPerson):
// anybody else is refused with a 403 before anything is recorded.
func (c *Client) CreateBranch(ctx context.Context, repo model.RepoRef, req CreateBranchRequest) (Branch, error) {
	if err := c.requireRepoPerson(ctx, repo); err != nil {
		return Branch{}, err
	}
	if model.IsConfigRepo(repo) {
		// The branches reconciler cuts and adopts branches on GitHub, which
		// has no grain/config: a row recorded here would sit pending
		// forever, asking GitHub about it every cycle.
		return Branch{}, validationErrorf("adding branches from the repo page is not available for the internal " +
			"config repository: push the branch to grain/config through grain's git proxy instead")
	}
	name := strings.TrimSpace(req.Name)
	b, fresh, err := c.Store.RequestBranchFrom(ctx, repo, name, strings.TrimSpace(req.From), c.now())
	if err != nil {
		if errors.Is(err, model.ErrInvalidBranchName) {
			// The rule itself, not just the verdict, as releaseErrorf
			// does for a release name: "bad branch..name" looks like a
			// name until someone says why git refuses it.
			bad := name
			if model.ValidBranchName(name) {
				bad = strings.TrimSpace(req.From)
			}
			return Branch{}, validationErrorf(`invalid branch name %q: use letters, digits, ".", "_", "-" and "/", `+
				`starting with a letter or digit; a name cannot contain ".." or "//" or end in "/"`, bad)
		}
		return Branch{}, err
	}
	out := branchFrom(b)
	out.AlreadyRequested = !fresh
	return out, nil
}

// ListBranches returns every branch ever requested for repo, newest
// first.
func (c *Client) ListBranches(ctx context.Context, repo model.RepoRef) ([]Branch, error) {
	bs, err := c.Store.ListBranches(ctx, repo)
	if err != nil {
		return nil, err
	}
	return branchesFrom(bs), nil
}

// ListGitHubBranches returns the branch refs repo's remote actually has.
//
// A repository GitHub's API does not serve is read from where it lives
// instead (Config.RepoBranches): the controller-local config repository
// from its own checkout, and a repository on a plain git host over git.
// The config repository is never asked of GitHub at all -- grain/config
// is a name grain's own git proxy gives it, and on GitHub it is either
// nothing (a 404 the branch pickers surfaced as a 502) or somebody else's.
//
// Everything else is GitHub's listing, through a short-lived,
// per-repository cache. Concurrent first readers share one request, and a
// brief error cache prevents a focused field from hammering a failing API.
func (c *Client) ListGitHubBranches(ctx context.Context, repo model.RepoRef) ([]string, error) {
	if src := c.Config.RepoBranches; src != nil {
		names, ok, err := src.RepoBranches(ctx, repo)
		if ok {
			if err != nil {
				return nil, localBranchesError{err}
			}
			return names, nil
		}
	}
	if model.IsConfigRepo(repo) {
		return nil, errConfigRepoBranchesUnavailable
	}
	if c.Config.GitHubBranches == nil {
		return nil, errGitHubBranchesUnavailable
	}
	credentialAvailable := false
	if c.Config.GitHubCredentialAvailable != nil {
		credentialAvailable = c.Config.GitHubCredentialAvailable(repo.Owner, repo.Name)
	} else if c.Config.Credentials != nil {
		// The deployment's git connection for the repository, as the git
		// proxy resolves it, naming a credential that is stored here.
		if c.Store != nil {
			if name, err := c.Store.GitConnectionOn(ctx, repo.Host, repo.Owner, repo.Name); err == nil && name != "" {
				_, credentialAvailable = c.Config.Credentials.Get(name)
			}
		}
		if !credentialAvailable {
			_, credentialAvailable = c.Config.Credentials.Select(repo.Owner, repo.Name)
		}
	}
	if !credentialAvailable {
		return nil, errNoGitHubRepoCredential
	}

	key := strings.ToLower(repo.String())
	now := c.now()
	c.githubBranchesMu.Lock()
	if c.githubBranches == nil {
		c.githubBranches = make(map[string]*githubBranchCacheEntry)
	}
	if hit := c.githubBranches[key]; hit != nil {
		if hit.expiresAt.IsZero() || now.Before(hit.expiresAt) {
			ready := hit.ready
			c.githubBranchesMu.Unlock()
			select {
			case <-ready:
				return append([]string(nil), hit.names...), hit.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	entry := &githubBranchCacheEntry{ready: make(chan struct{})}
	c.githubBranches[key] = entry
	c.githubBranchesMu.Unlock()

	names, err := c.Config.GitHubBranches.ListBranches(repo.Owner, repo.Name)
	c.githubBranchesMu.Lock()
	entry.names, entry.err = append([]string(nil), names...), err
	entry.fetchedAt = now
	entry.expiresAt = now.Add(githubBranchCacheTTL)
	if err != nil {
		entry.expiresAt = now.Add(githubBranchErrorCacheTTL)
	}
	close(entry.ready)
	c.githubBranchesMu.Unlock()
	return names, err
}

// githubBranchRecheckAfter is how old a cached listing has to be before
// a base it does not name is asked about again rather than refused. The
// cache is five minutes long, and somebody who pushes a branch and then
// files a task on it straight away must not be told it is not there for
// the rest of those five minutes; asking again only when the answer
// would be a refusal keeps that to one extra listing per refusal.
const githubBranchRecheckAfter = 10 * time.Second

// forgetGitHubBranches drops repo's cached listing if it was read longer
// ago than olderThan, so the next ListGitHubBranches goes to GitHub.
func (c *Client) forgetGitHubBranches(repo model.RepoRef, olderThan time.Duration) bool {
	key := strings.ToLower(repo.String())
	c.githubBranchesMu.Lock()
	defer c.githubBranchesMu.Unlock()
	hit := c.githubBranches[key]
	if hit == nil {
		return false
	}
	select {
	case <-hit.ready:
	default:
		return false // still being read: that answer is as fresh as any
	}
	if c.now().Sub(hit.fetchedAt) < olderThan {
		return false
	}
	delete(c.githubBranches, key)
	return true
}

// baseBranchMissing reports whether base is known not to exist on repo:
// GitHub's own listing was read and does not name it, and grain has no
// request on record to create it (a pending row the branches reconciler
// will cut). Anything short of that is "not known to be missing" -- no
// GitHub lister, no credential for the repo, GitHub failing -- and
// answers false, because grain being unable to tell is not evidence that
// the branch is absent, and dispatch still refuses a base it cannot
// check out.
func (c *Client) baseBranchMissing(ctx context.Context, repo model.RepoRef, base string) (bool, error) {
	if base == "" {
		return false, nil // the repo's default branch
	}
	recorded, err := c.Store.ListBranches(ctx, repo)
	if err != nil {
		return false, err
	}
	for _, b := range recorded {
		if b.Name == base {
			return false, nil
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		names, err := c.ListGitHubBranches(ctx, repo)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return false, ctxErr
			}
			return false, nil
		}
		for _, name := range names {
			if name == base {
				return false, nil
			}
		}
		if attempt == 0 && !c.forgetGitHubBranches(repo, githubBranchRecheckAfter) {
			break
		}
	}
	return true, nil
}

// refuseMissingBases turns away a one-shot task whose base -- its own, or
// one of its write targets' -- GitHub says does not exist, the write-time
// half of grain/task-1620. It is only asked of a task nothing holds back:
// one waiting on another task may be waiting for exactly the work that
// cuts its branch. Schedules and templates are never asked it, since the
// branch they name is often one a later release will cut; the forms
// warn about those instead (BranchField's warnMissing).
func (c *Client) refuseMissingBases(ctx context.Context, target *model.RepoRef, base string, writes []model.WriteTarget) error {
	if target != nil {
		missing, err := c.baseBranchMissing(ctx, *target, base)
		if err != nil {
			return err
		}
		if missing {
			return validationErrorf("base %q does not exist on %s: push it first, "+
				"or have the task depend on the one that creates it", base, target)
		}
	}
	for _, w := range writes {
		missing, err := c.baseBranchMissing(ctx, w.Repo, w.Base)
		if err != nil {
			return err
		}
		if missing {
			return validationErrorf("base for %s %q does not exist on %s: push it first, "+
				"or have the task depend on the one that creates it", w.Repo, w.Base, w.Repo)
		}
	}
	return nil
}

// blockedByLinks reports whether any of links holds its task back from
// dispatch (model.LinkKind.Blocks).
func blockedByLinks(links []model.Link) bool {
	for _, l := range links {
		if l.Kind.Blocks() {
			return true
		}
	}
	return false
}

func hasWriteTarget(writes []model.WriteTarget, w model.WriteTarget) bool {
	for _, have := range writes {
		if have.Repo.Same(w.Repo) && have.Base == w.Base {
			return true
		}
	}
	return false
}

// --- handlers ------------------------------------------------------------

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	branches, err := s.tasks.ListBranches(r.Context(), repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, branches)
}

func (s *Server) handleListGitHubBranches(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	names, err := s.tasks.ListGitHubBranches(r.Context(), repo)
	if err != nil {
		status := http.StatusBadGateway
		var local localBranchesError
		if errors.As(err, &local) {
			status = http.StatusInternalServerError
		}
		if errors.Is(err, errGitHubBranchesUnavailable) || errors.Is(err, errNoGitHubRepoCredential) ||
			errors.Is(err, errConfigRepoBranchesUnavailable) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleCompareBranches(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if s.tasks.Config.RepoComparer == nil {
		writeError(w, http.StatusNotFound, errors.New("repository comparison is not available on this UI server"))
		return
	}
	base, head := strings.TrimSpace(r.URL.Query().Get("base")), strings.TrimSpace(r.URL.Query().Get("head"))
	if head == "" {
		writeClientError(w, validationErrorf("a head branch is required"))
		return
	}
	comparison, err := s.tasks.Config.RepoComparer.Compare(r.Context(), repo, base, head)
	if github.IsNotFound(err) {
		// GitHub's 404 is the answer for a branch that does not exist --
		// a typo in the compare form, a deleted branch, a stale link --
		// and for two branches with no common ancestor. Reading it as an
		// outage left the page saying "an upstream service returned an
		// error" about a name somebody can fix. Said
		// in grain's own words, naming only what the caller sent; the
		// upstream's text still goes nowhere but the log (#2424).
		writeError(w, http.StatusNotFound, fmt.Errorf(
			"GitHub has nothing to compare for %s...%s in %s: one of the two branches does not exist, "+
				"the two share no history, or this deployment's credential cannot see %s",
			compareBaseLabel(base), head, repo, repo))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, comparison)
}

func (s *Server) handleCreateBranch(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	var req CreateBranchRequest
	if !readJSON(w, r, &req) {
		return
	}
	branch, err := s.tasks.CreateBranch(r.Context(), repo, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if branch.AlreadyRequested {
		writeJSON(w, http.StatusOK, branch)
		return
	}
	writeJSON(w, http.StatusCreated, branch)
}

// compareBaseLabel is the base a comparison sentence names: the one
// the caller sent, or the repo's default branch when it sent none.
func compareBaseLabel(base string) string {
	if base == "" {
		return "the default branch"
	}
	return base
}

// branchBaseLabel is how a sentence names the branch a request is cut
// from: model.Branch.From, or the repo's default branch when that is
// empty.
func branchBaseLabel(from string) string {
	if from == "" {
		return "the default branch"
	}
	return `"` + from + `"`
}
