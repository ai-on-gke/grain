package ui

// The operator surface for model.WorkOwnership (grain/task-486,
// grain/task-542): what this deployment says about one repository's
// standing work, at both of the levels that record says it at.
//
// Only one of the two had a surface before this. The release item's
// override is written by the Repos -> Release pane's switch, and the
// repository default and every other item's override could be reached
// only by editing `work_ownership` / `work_overrides` in the exported
// settings table and applying it -- a real route, on a deployment whose
// settings are a git repository, but not a discoverable one and not one
// that can *show* anything: a diff of two columns cannot say which of a
// repository's schedules this deployment is the one running.
//
// What this adds is deliberately a reading rather than a set of
// switches. The whole point of a three-valued model.Ownership is that
// "nobody has said", "explicitly on" and "explicitly off" are three
// states and not two, and a checkbox has no way to draw the first: an
// unticked box is a claim, and the claim it makes -- somebody decided no
// -- is exactly the one the type exists to keep separate from silence.
// So every row here carries both halves, what was said and what it
// resolves to, and neither is inferred from the other by a reader.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Work item kinds, as RepoWorkItem.Kind reports them. They are what a
// row *is* rather than what it says, and they exist because the three
// are drawn and explained differently: a release has a pane of its own
// to link to, a schedule has a cadence and a pause switch of its own
// that this setting is not, and an item grain cannot place is a stale
// override somebody should be able to see and clear.
const (
	WorkItemKindRelease  = "release"
	WorkItemKindSchedule = "schedule"
	WorkItemKindUnknown  = "unknown"
	// WorkItemKindStanding is one of the other reserved items
	// (model.StandingWorkItems): the base-branch check, post-merge jobs,
	// automatic ports and grain's own triage and repair tasks
	// (grain/task-3332). Each has no row of its own to link to, so its
	// Title says what it is.
	WorkItemKindStanding = "standing"
)

// standingWorkTitles is what each reserved item is called on the
// repository's page. The release keeps its own kind, and pane.
var standingWorkTitles = map[model.WorkItem]string{
	model.ReleaseItem:      "Release",
	model.BaseBranchCIItem: "Base-branch check (grain CI on new tips, its retries and repairs)",
	model.PostMergeItem:    "Post-merge dispatch jobs (including the guest cache job)",
	model.PortsItem:        "Automatic ports to paired branches",
	model.TriageItem:       "Triage and repair tasks grain files on its own",
}

// RepoWorkOwnership is one repository's whole ownership record as an
// operator reads it: the repository default, what that default resolves
// to, and one row per work item.
type RepoWorkOwnership struct {
	Repo string `json:"repo"`
	// Default is model.WorkOwnership.Default verbatim -- "on", "off", or
	// empty for "nobody has said". Sent as the stored string rather than
	// as a bool with a flag beside it, so that a client cannot represent
	// a state the store cannot.
	Default string `json:"default"`
	// DefaultRuns is what Default resolves to for an item no override
	// names (model.WorkOwnership.RunsByDefault). It is the server's
	// answer rather than the client's arithmetic: empty resolving to
	// "yes" is a decision of the model's, and a pane that re-derived it
	// would be free to disagree with the reconciler.
	DefaultRuns bool `json:"defaultRuns"`
	// Items is the release and the other reserved items, every schedule
	// that is part of this repository's standing work, and every item an override names that
	// is neither -- see repoWorkItems for why those three and in that
	// order.
	Items []RepoWorkItem `json:"items"`
}

// RepoWorkItem is one item of a repository's standing work, with what
// this deployment has said about it and what that comes to.
type RepoWorkItem struct {
	// Item is the work item id -- a schedule id, or model.ReleaseItem --
	// and is what the write endpoint takes back.
	Item string `json:"item"`
	// Kind is one of the WorkItemKind constants above.
	Kind string `json:"kind"`
	// Title is what to call this row when it has a name of its own: a
	// schedule's title. Empty for a row whose id is the whole of what is
	// known about it.
	Title string `json:"title,omitempty"`
	// Override is what has been said about this item specifically --
	// "on", "off", or empty for "follows the repository default". The
	// empty case is a state in its own right and is why this is not a
	// bool.
	Override string `json:"override"`
	// Runs is model.RunsWorkItem for this item: the answer after the
	// override and the default have been resolved against each other.
	// Every row carries it, including the rows that say nothing, because
	// "follows the default" is not an answer to "is this deployment
	// running it".
	Runs bool `json:"runs"`
	// Paused is a schedule that is switched off on its own row
	// (model.Schedule.Enabled), which is a different fact from this
	// deployment not owning it and has to be drawn as one: a paused
	// schedule fires nothing however this reads, and un-pausing it is an
	// edit on the schedule rather than here.
	Paused bool `json:"paused"`
	// Declaration is where a declared schedule comes from -- the
	// repository's own `.grain` directory, and the file in it -- and is
	// nil for a schedule somebody created on this deployment. It is the
	// distinction this pane is about: the repository declares that the
	// work exists, and this deployment decides whether it is the one
	// doing it.
	Declaration *Declaration `json:"declaration,omitempty"`
}

// RepoWorkOwnership reads the whole record for one repository.
func (c *Client) RepoWorkOwnership(ctx context.Context, repo string) (RepoWorkOwnership, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoWorkOwnership{}, validationErrorf("repo: %v", err)
	}
	cfg, err := c.Store.GetRepoConfig(ctx, parsed)
	if err != nil {
		return RepoWorkOwnership{}, err
	}
	// Read for a repository with no row of its own as well, rather than
	// answering 404: a repository nobody has configured is the common
	// case and has a perfectly good answer -- this deployment runs its
	// work, nobody having said otherwise -- and that is the state an
	// operator arriving here to change something is in.
	var work model.WorkOwnership
	if cfg != nil {
		work = cfg.Work
	}
	schedules, err := c.Store.ListSchedules(ctx)
	if err != nil {
		return RepoWorkOwnership{}, fmt.Errorf("listing schedules: %w", err)
	}
	declared, err := c.declaredIn(ctx)
	if err != nil {
		return RepoWorkOwnership{}, err
	}
	return RepoWorkOwnership{
		Repo:        parsed.String(),
		Default:     string(work.Default),
		DefaultRuns: work.RunsByDefault(),
		Items:       repoWorkItems(cfg, parsed, schedules, declared),
	}, nil
}

// repoWorkItems is the item list: the release, then this repository's
// schedules, then whatever else an override names.
//
// A schedule belongs to a repository here if the repository declares it
// or if its firings target the repository. Both, rather than only the
// first, because both are standing work that runs *in* this repository
// and that an operator handing a repository to another deployment means
// to stop -- a schedule this deployment's own UI created against the
// repository is no less that repository's nightly work for having been
// written here. Declaration says which of the two a row is, so the
// difference is drawn rather than flattened.
//
// The third group is the reason this list is not simply the schedules.
// An override outlives whatever it names: a declared schedule whose file
// is withdrawn takes its row with it and leaves `item=off` behind in the
// settings, and an operator who cannot see that entry cannot clear it --
// and would find it again, still deciding things, the day the file comes
// back.
func repoWorkItems(cfg *model.RepoConfig, repo model.RepoRef, schedules []model.Schedule,
	declared map[string]model.RepoDeclaration) []RepoWorkItem {

	item := func(id model.WorkItem, kind string) RepoWorkItem {
		state, _ := workOwnershipOf(cfg).Item(id)
		return RepoWorkItem{
			Item: string(id), Kind: kind,
			Override: string(state), Runs: model.RunsWorkItem(cfg, id),
		}
	}
	// The release first and always, whatever else is here: it is the one
	// item every repository has, it is the one already being resolved
	// through this record today, and the pane that writes it is a click
	// away on the same page.
	//
	// The other reserved items follow it, in model.StandingWorkItems'
	// order, so every piece of standing work the repository default
	// covers is on the page with what the default resolves it to.
	var out []RepoWorkItem
	listed := map[model.WorkItem]bool{}
	for _, id := range model.StandingWorkItems {
		kind := WorkItemKindStanding
		if id == model.ReleaseItem {
			kind = WorkItemKindRelease
		}
		row := item(id, kind)
		row.Title = standingWorkTitles[id]
		out = append(out, row)
		listed[id] = true
	}
	rows := make([]RepoWorkItem, 0, len(schedules))
	for _, sched := range schedules {
		decl, isDeclared := declared[sched.ID]
		if !(isDeclared && decl.Repo.Same(repo)) && !sched.Target.Same(repo) {
			continue
		}
		id := model.ScheduleItem(sched.ID)
		row := item(id, WorkItemKindSchedule)
		row.Title, row.Paused = sched.Title, !sched.Enabled
		if isDeclared {
			row.Declaration = &Declaration{Repo: decl.Repo.String(), Path: decl.Path}
		}
		rows = append(rows, row)
		listed[id] = true
	}
	// By title, and by id where two share one or where a schedule has
	// none: ListSchedules' own order is newest first, which is the right
	// order for a list of what has been created lately and the wrong one
	// for a list somebody is looking a known name up in.
	slices.SortFunc(rows, func(a, b RepoWorkItem) int {
		if n := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); n != 0 {
			return n
		}
		return strings.Compare(a.Item, b.Item)
	})
	out = append(out, rows...)
	for _, id := range workOwnershipOf(cfg).OverriddenItems() {
		if listed[id] {
			continue
		}
		out = append(out, item(id, WorkItemKindUnknown))
	}
	return out
}

// workOwnershipOf is the record on a config row that may not exist --
// the nil check every reader of a repository with no settings needs, in
// one place, the way model.RunsWorkItem is the nil check for resolving
// one item.
func workOwnershipOf(cfg *model.RepoConfig) model.WorkOwnership {
	if cfg == nil {
		return model.WorkOwnership{}
	}
	return cfg.Work
}

// SetRepoWorkDefault writes this deployment's answer for the repository
// as a whole: "on", "off", or empty to go back to having said nothing.
//
// Empty is a write and not a no-op. Going back to silence is how a
// repository stops being one this deployment has an opinion about, and
// it is a different state from "on" for exactly the reason the whole
// type is three-valued: silence follows whatever the resolution rule
// says next, and "on" is a decision that outlives a change to it.
func (c *Client) SetRepoWorkDefault(ctx context.Context, repo, state string) (RepoWorkOwnership, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoWorkOwnership{}, validationErrorf("repo: %v", err)
	}
	ownership, err := model.ParseOwnership(state)
	if err != nil {
		return RepoWorkOwnership{}, validationErrorf("default: %v", err)
	}
	if _, err := c.putRepoConfig(ctx, parsed, func(cfg *model.RepoConfig) {
		cfg.Work.Default = ownership
	}); err != nil {
		return RepoWorkOwnership{}, err
	}
	return c.RepoWorkOwnership(ctx, parsed.String())
}

// SetRepoWorkItemOwnership writes one item's override, or clears it with
// an empty state so the item follows the repository default again.
//
// The item is not checked against the schedules this deployment holds,
// and deliberately. A declared schedule's row appears and disappears
// with a file in a repository grain polls every couple of minutes, so a
// check here would refuse an override for a schedule that is about to
// exist and would refuse to *clear* one whose schedule has just gone --
// which is the case an operator most needs this call for. What it does
// check is that the id could survive the storage encoding
// (model.ValidateWorkItem), since an id holding the separators would
// come back as a different id or not at all.
func (c *Client) SetRepoWorkItemOwnership(ctx context.Context, repo, item, state string) (RepoWorkOwnership, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoWorkOwnership{}, validationErrorf("repo: %v", err)
	}
	id := model.WorkItem(strings.TrimSpace(item))
	if err := model.ValidateWorkItem(id); err != nil {
		return RepoWorkOwnership{}, validationErrorf("item: %v", err)
	}
	ownership, err := model.ParseOwnership(state)
	if err != nil {
		return RepoWorkOwnership{}, validationErrorf("state: %v", err)
	}
	if _, err := c.putRepoConfig(ctx, parsed, func(cfg *model.RepoConfig) {
		cfg.Work = cfg.Work.With(id, ownership)
	}); err != nil {
		return RepoWorkOwnership{}, err
	}
	return c.RepoWorkOwnership(ctx, parsed.String())
}

// SetRepoWorkOwnershipRequest is PUT /api/repos/{owner}/{name}/
// work-ownership's body: one level per request.
//
// Every field is a pointer so that the empty string -- "go back to
// having said nothing", which is a real write and the only way to
// reach the third state -- is tellable from a request that is not
// about that field at all.
type SetRepoWorkOwnershipRequest struct {
	// Default is the repository default: "on", "off" or "".
	Default *string `json:"default,omitempty"`
	// Item and State are one item's override, and go together: State
	// without Item says nothing about which item, and Item without
	// State is refused rather than read as a clear, since "clear it"
	// and "I forgot the field" are not the same request.
	Item  *string `json:"item,omitempty"`
	State *string `json:"state,omitempty"`
}

func (s *Server) handleGetRepoWorkOwnership(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.RepoWorkOwnership(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetRepoWorkOwnership writes one of the two levels per request:
// the repository default, or one item's override.
//
// One endpoint rather than two, and the item in the body rather than in
// the path, because a work item id is a schedule id and a declared
// schedule's id holds colons and the repository's own name
// (model.RepoDeclaredID). A path segment would have to be escaped by
// every client that ever writes one, and the first one that forgot would
// write an override under a mangled id that resolves nothing and that
// this pane would then list as unknown.
func (s *Server) handleSetRepoWorkOwnership(w http.ResponseWriter, r *http.Request) {
	parsed, err := s.tasks.parseRepo(r.PathValue("owner") + "/" + r.PathValue("name"))
	if err != nil {
		writeClientError(w, validationErrorf("repo: %v", err))
		return
	}
	if err := s.tasks.requireRepoPerson(r.Context(), parsed); err != nil {
		writeClientError(w, err)
		return
	}
	var req SetRepoWorkOwnershipRequest
	if !readJSON(w, r, &req) {
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("name")
	switch {
	case req.Item != nil:
		if req.State == nil {
			writeError(w, http.StatusBadRequest,
				fmt.Errorf(`item %q needs a state: "on", "off", or "" to follow the repository default`, *req.Item))
			return
		}
		out, err := s.tasks.SetRepoWorkItemOwnership(r.Context(), repo, *req.Item, *req.State)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case req.Default != nil:
		out, err := s.tasks.SetRepoWorkDefault(r.Context(), repo, *req.Default)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("default, or item and state, is required"))
	}
}
