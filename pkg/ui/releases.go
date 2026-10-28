package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Release is one named release branch set's JSON shape --
// bwsalmon/agents#571's own latest/rc/prod branches, derived from Name
// rather than configured separately: LatestBranch and ProdBranch are
// always Name+".latest" and Name itself.
type Release struct {
	Repo           string     `json:"repo"`
	Name           string     `json:"name"`
	LatestBranch   string     `json:"latestBranch"`
	ProdBranch     string     `json:"prodBranch"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"createdAt"`
	MergedAt       *time.Time `json:"mergedAt,omitempty"`
	PullRequestURL string     `json:"pullRequestUrl,omitempty"`
	// MergeNote is why a merged release has no PullRequestURL: its prod
	// branch carried nothing the default branch did not already have, so
	// there was no pull request to open (model.Release.MergeNote). Set
	// instead of PullRequestURL, never alongside it, and not an error --
	// nothing went wrong with such a release.
	MergeNote string `json:"mergeNote,omitempty"`
	// Error is the releases reconciler's own account of why Status hasn't
	// advanced yet, if anything has gone wrong.
	Error string `json:"error,omitempty"`
}

func releaseFrom(r model.Release) Release {
	return Release{
		Repo: r.Repo.String(), Name: r.Name,
		LatestBranch: r.LatestBranch(), ProdBranch: r.ProdBranch(),
		Status: string(r.Status), CreatedAt: r.CreatedAt, MergedAt: r.MergedAt,
		PullRequestURL: r.PullRequestURL, MergeNote: r.MergeNote, Error: r.LastError,
	}
}

func releasesFrom(rs []model.Release) []Release {
	out := make([]Release, len(rs))
	for i, r := range rs {
		out[i] = releaseFrom(r)
	}
	return out
}

// Candidate is one rc cut for a release, over the wire.
type Candidate struct {
	ID      int64  `json:"id"`
	Repo    string `json:"repo"`
	Release string `json:"release"`
	Number  int    `json:"number"`
	Branch  string `json:"branch"`
	// Status is one of "cutting", "active", "promoting", "promoted" --
	// model.CandidateStatus's own vocabulary, unchanged, since it is
	// already the plain English a UI wants to show.
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	PromotedAt *time.Time `json:"promotedAt,omitempty"`
	// Error is the releases reconciler's own account of why Status hasn't
	// advanced yet, if anything has gone wrong -- model.Candidate's own
	// LastError.
	Error string `json:"error,omitempty"`
}

// candidateFrom renders c over the wire against releaseName -- the name
// of the release c.ReleaseID names, which every caller here already has
// in hand from the request path, so model.Candidate itself carries no
// redundant copy of it.
func candidateFrom(c model.Candidate, releaseName string) Candidate {
	return Candidate{
		ID: c.ID, Repo: c.Repo.String(), Release: releaseName,
		Number: c.Number, Branch: c.Branch, Status: string(c.Status),
		CreatedAt: c.CreatedAt, PromotedAt: c.PromotedAt, Error: c.LastError,
	}
}

func candidatesFrom(cs []model.Candidate, releaseName string) []Candidate {
	out := make([]Candidate, len(cs))
	for i, c := range cs {
		out[i] = candidateFrom(c, releaseName)
	}
	return out
}

// parseRepoPath reads the {owner}/{name} pair out of a route's path --
// what every repo-scoped route here, and the branch and qualification
// routes beside them, are keyed by.
//
// Spelled this deployment's way (Client.parseRepo) rather than as the
// URL happened to capitalise it, since these are keys: a release, a
// candidate, a branch row and a qualification plan all belong to a repo
// by (owner, name), and a link typed or pasted in another case would
// otherwise open a second, empty repo beside the one it names.
func (s *Server) parseRepoPath(r *http.Request) (model.RepoRef, error) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	if owner == "" || name == "" {
		return model.RepoRef{}, validationErrorf("repo owner and name are required")
	}
	return s.tasks.canonicalRepo(model.RepoFromStored(owner, name)), nil
}

func parseReleaseName(r *http.Request) (string, error) {
	name := r.PathValue("release")
	if name == "" {
		return "", validationErrorf("release name is required")
	}
	return name, nil
}

// releaseNotFoundError marks a repo and release name with no release
// behind it -- writeClientError maps it to a 404, scheduleNotFoundError's
// own reasoning for why this is its own type rather than NotFoundError
// itself.
type releaseNotFoundError struct {
	Repo model.RepoRef
	Name string
}

func (e *releaseNotFoundError) Error() string {
	return fmt.Sprintf("%s has no release %q", e.Repo, e.Name)
}

// releaseErrorf maps one of model's own release sentinel errors to the
// ValidationError Server maps to a 400 -- every one of them is a caller
// mistake (unknown or inactive release, wrong sequencing), never a store
// fault.
func releaseErrorf(err error) error {
	switch {
	case errors.Is(err, model.ErrInvalidReleaseName):
		// The rule itself, not just the verdict: "v3.0..x" and "v3.lock"
		// look like any other name until someone says why git refuses them.
		return validationErrorf(`invalid release name: use letters, digits, ".", "_" and "-", starting with a letter or digit; ` +
			`a name cannot contain "..", end in "." or ".lock", be "HEAD", or end in ".latest" or ".rc.<number>"`)
	case errors.Is(err, model.ErrReleaseNameInUse):
		return validationErrorf("this repo already has an unmerged release with this name")
	case errors.Is(err, model.ErrNoRelease):
		return validationErrorf("no such release")
	case errors.Is(err, model.ErrReleaseNotActive):
		return validationErrorf("this release is not active yet, or its merge back has already been requested")
	case errors.Is(err, model.ErrReleaseAlreadyMergeRequested):
		return validationErrorf("this release's merge back to the default branch was already requested")
	case errors.Is(err, model.ErrCandidateActive):
		return validationErrorf("this release already has an unpromoted candidate")
	case errors.Is(err, model.ErrNoCandidate):
		return validationErrorf("this release has no candidate yet")
	case errors.Is(err, model.ErrCandidateNotReady):
		return validationErrorf("the current candidate has not finished cutting yet")
	case errors.Is(err, model.ErrAlreadyPromoted):
		return validationErrorf("the current candidate was already promoted")
	default:
		return err
	}
}

// CreateReleaseRequest is a new release's user-given name -- the issue's
// own "the branch should have a user given name."
type CreateReleaseRequest struct {
	Name string `json:"name"`
}

// errConfigRepoReleases is why the config repository has no releases.
var errConfigRepoReleases = errors.New("releases are not available for the internal config repository: " +
	"it is served by grain's own git proxy, not GitHub, and a release's branches are cut on GitHub")

// CreateRelease is the issue's own "create a new release branch": it
// records a fresh model.Release declaring the intent, and returns
// immediately -- the releases reconciler (pkg/orchestrator.SyncReleases)
// is what actually creates LatestBranch on GitHub.
//
// A release is the standing instruction for the releases reconciler to
// create and push its latest, rc and prod branches in repo, so a repo
// off Config.TargetRepos or the owning team's target_repos is refused
// here, before anything is written, rather than left for the reconciler
// to push branches into a repository this deployment was never allowed
// to touch -- the same boundary PutQualificationPlan draws
// (refuseOffRepoAllowlist). Only creation is refused: cutting,
// promoting and merging back a release that already exists stay
// possible after the list narrows, so one already under way can still
// be finished.
//
// Who may ask is the line StartRepoRelease already draws: repo's own
// admins, an admin of a team attached to it, or a system admin
// (requireRepoPerson), asked first so a caller who may not is told that,
// with a 403, and nothing else. The same goes for cutting, promoting and
// merging back: each moves branches on GitHub, promote and merge back
// the ones production runs from.
func (c *Client) CreateRelease(ctx context.Context, repo model.RepoRef, req CreateReleaseRequest) (Release, error) {
	if err := c.requireRepoPerson(ctx, repo); err != nil {
		return Release{}, err
	}
	if refusal := c.ReleaseCreation(ctx, repo); !refusal.Allowed {
		return Release{}, validationErrorf("%s", refusal.Reason)
	}
	name := strings.TrimSpace(req.Name)
	r, err := c.Store.CreateRelease(ctx, repo, name, c.now())
	if err != nil {
		return Release{}, releaseErrorf(err)
	}
	return releaseFrom(r), nil
}

// ReleaseCreation is whether a new release may be created in a repo,
// and if not, why -- what the releases pane reads to show the refusal
// in place of a create form, rather than offer a form whose every
// submission CreateRelease would turn away.
type ReleaseCreation struct {
	Allowed bool `json:"allowed"`
	// Reason is CreateRelease's own refusal, word for word, when Allowed
	// is false.
	Reason string `json:"reason,omitempty"`
	// Manageable is whether the caller may cut, promote and merge back
	// repo's releases at all (requireRepoPerson), so the pane can leave
	// those buttons off for somebody every press would be refused.
	// Unlike Allowed it does not move with the target repo list: a
	// release already under way can still be finished.
	Manageable bool `json:"manageable"`
}

// ReleaseCreation answers the questions CreateRelease asks first, without
// writing anything, so that the pane and the write can never disagree
// about them: both ask requireRepoPerson, then refuseOffRepoAllowlist,
// then whether repo is the controller-local config repository.
func (c *Client) ReleaseCreation(ctx context.Context, repo model.RepoRef) ReleaseCreation {
	if err := c.requireRepoPerson(ctx, repo); err != nil {
		return ReleaseCreation{Reason: err.Error()}
	}
	if err := c.refuseOffRepoAllowlist(ctx, repo, "a release cannot be created in it"); err != nil {
		return ReleaseCreation{Reason: err.Error(), Manageable: true}
	}
	if model.IsConfigRepo(repo) {
		// A release's branches are cut on GitHub by the releases
		// reconciler, and the controller-local config repository is not
		// on GitHub: refused here rather than left pending forever.
		return ReleaseCreation{Reason: errConfigRepoReleases.Error()}
	}
	return ReleaseCreation{Allowed: true, Manageable: true}
}

// ListReleases returns every release ever created for repo, newest
// first.
func (c *Client) ListReleases(ctx context.Context, repo model.RepoRef) ([]Release, error) {
	rs, err := c.Store.ListReleases(ctx, repo)
	if err != nil {
		return nil, err
	}
	return releasesFrom(rs), nil
}

// GetRelease returns repo's current release named name.
func (c *Client) GetRelease(ctx context.Context, repo model.RepoRef, name string) (Release, error) {
	r, err := c.Store.GetRelease(ctx, repo, name)
	if err != nil {
		return Release{}, err
	}
	if r == nil {
		return Release{}, &releaseNotFoundError{Repo: repo, Name: name}
	}
	return releaseFrom(*r), nil
}

// RequestReleaseMerge is the issue's own "the prod branch can be merged
// back into the default branch when ready": it records the request
// against repo's release named name and returns immediately, the same
// declarative handoff CutCandidate and PromoteCandidate already make.
func (c *Client) RequestReleaseMerge(ctx context.Context, repo model.RepoRef, name string) (Release, error) {
	if err := c.requireRepoPerson(ctx, repo); err != nil {
		return Release{}, err
	}
	r, err := c.Store.RequestReleaseMerge(ctx, repo, name)
	if err != nil {
		return Release{}, releaseErrorf(err)
	}
	return releaseFrom(r), nil
}

// DeleteRelease removes repo's release named name and any candidates cut
// for it.
func (c *Client) DeleteRelease(ctx context.Context, repo model.RepoRef, name string) error {
	if err := c.requireRepoPerson(ctx, repo); err != nil {
		return err
	}
	if err := c.Store.DeleteRelease(ctx, repo, name); err != nil {
		return releaseErrorf(err)
	}
	return nil
}

// ListCandidates returns repo's release named releaseName's own rc
// history, newest first.
func (c *Client) ListCandidates(ctx context.Context, repo model.RepoRef, releaseName string) ([]Candidate, error) {
	cs, err := c.Store.ListCandidates(ctx, repo, releaseName)
	if err != nil {
		return nil, err
	}
	return candidatesFrom(cs, releaseName), nil
}

// CutCandidate is the issue's own "create a new rc": it records a fresh
// model.Candidate declaring the cut, and returns immediately -- the
// releases reconciler is what actually creates the branch on GitHub.
func (c *Client) CutCandidate(ctx context.Context, repo model.RepoRef, releaseName string) (Candidate, error) {
	if err := c.requireRepoPerson(ctx, repo); err != nil {
		return Candidate{}, err
	}
	candidate, err := c.Store.CutCandidate(ctx, repo, releaseName, c.now())
	if err != nil {
		return Candidate{}, releaseErrorf(err)
	}
	return candidateFrom(candidate, releaseName), nil
}

// PromoteCandidate is the issue's own "promote the current rc": it
// records the promotion against releaseName's current candidate and
// returns immediately, the same declarative handoff CutCandidate makes.
func (c *Client) PromoteCandidate(ctx context.Context, repo model.RepoRef, releaseName string) (Candidate, error) {
	if err := c.requireRepoPerson(ctx, repo); err != nil {
		return Candidate{}, err
	}
	candidate, err := c.Store.PromoteCandidate(ctx, repo, releaseName)
	if err != nil {
		return Candidate{}, releaseErrorf(err)
	}
	return candidateFrom(candidate, releaseName), nil
}

// --- handlers ------------------------------------------------------------

func (s *Server) handleListReleases(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	releases, err := s.tasks.ListReleases(r.Context(), repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, releases)
}

func (s *Server) handleCreateRelease(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	var req CreateReleaseRequest
	if !readJSON(w, r, &req) {
		return
	}
	release, err := s.tasks.CreateRelease(r.Context(), repo, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, release)
}

func (s *Server) handleGetReleaseCreation(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.tasks.ReleaseCreation(r.Context(), repo))
}

func (s *Server) handleGetRelease(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	name, err := parseReleaseName(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	release, err := s.tasks.GetRelease(r.Context(), repo, name)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, release)
}

func (s *Server) handleDeleteRelease(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	name, err := parseReleaseName(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if err := s.tasks.DeleteRelease(r.Context(), repo, name); err != nil {
		writeClientError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRequestReleaseMerge(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	name, err := parseReleaseName(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	release, err := s.tasks.RequestReleaseMerge(r.Context(), repo, name)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, release)
}

func (s *Server) handleListCandidates(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	name, err := parseReleaseName(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	candidates, err := s.tasks.ListCandidates(r.Context(), repo, name)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, candidates)
}

func (s *Server) handleCutCandidate(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	name, err := parseReleaseName(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	candidate, err := s.tasks.CutCandidate(r.Context(), repo, name)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, candidate)
}

func (s *Server) handlePromoteCandidate(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	name, err := parseReleaseName(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	candidate, err := s.tasks.PromoteCandidate(r.Context(), repo, name)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, candidate)
}
