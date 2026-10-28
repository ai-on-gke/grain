package ui

import (
	"context"
	"net/http"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Editing the grain CI workflows this deployment declares for a repo it
// does not control (grain/task-245).
//
// pkg/ci reads a repository's workflows out of its own
// `.grain/workflows`, which works for the repositories grain works in
// and for nothing else: a grain-specific CI directory in somebody
// else's repository is a pull request they have to accept first. These
// routes are the other side grain always meant to have -- the same
// workflow text, in the same format, stored against the repo
// (model.RepoWorkflow) and read back through ci.Source (ci.Layered over
// ci.DeclaredSource, wired in cmd/grain's buildCIService).
//
// The one thing these routes do that the store does not is parse. A
// workflow is refused here, on the request that saves it, so a file
// that does not parse is an error under the form somebody is still
// looking at rather than a run that fails minutes later for a reason
// nobody is watching for -- and ci.Parse is the parse the run itself
// uses, not a second, laxer one.

// RepoWorkflow is one workflow this deployment declares for a repo: the
// path it would have had in the repository, and the file.
//
// Path and Text are the whole of what a caller sends. Name is filled on
// the way out only -- the workflow's own `name:`, as ci.Parse read it,
// so a listing can show what each file calls itself without the browser
// having to parse YAML to find out. It is ignored on the way in, since
// the file is what says what it is called.
type RepoWorkflow struct {
	Path string `json:"path"`
	Text string `json:"text"`
	Name string `json:"name,omitempty"`
}

// RepoWorkflowsResponse is what GET and PUT /api/repos/{owner}/{name}/
// workflows both answer with: the whole of what this deployment
// declares for the repo, after the write in PUT's case, so a form has
// the new state without a second request.
type RepoWorkflowsResponse struct {
	Repo string `json:"repo"`
	// Dir is where a workflow lives (ci.WorkflowDir), reported so the
	// form can say it rather than hard-coding grain's own directory
	// layout in a browser.
	Dir string `json:"dir"`
	// Workflows are this repo's declared workflows in path order, each
	// exactly as stored.
	//
	// What the *repository itself* declares in its own tree is not here
	// and cannot be: reading it means cloning a branch, which is a thing
	// a run does and not a thing a settings form should wait on. So this
	// pane shows what grain declares, and the run's own job list is
	// where the union of the two is visible.
	Workflows []RepoWorkflow `json:"workflows"`
}

// SetRepoWorkflowsRequest is PUT /api/repos/{owner}/{name}/workflows'
// body: the whole set, replaced rather than added to, the same way
// SetRepoCapabilitiesRequest replaces a repo's whole capability set.
//
// Replacing the set is also how one is deleted -- a workflow left out
// of the list is gone -- which is why there is no DELETE route beside
// this and no per-path PUT. One request is one decision about what this
// repo's CI is.
type SetRepoWorkflowsRequest struct {
	Workflows []RepoWorkflow `json:"workflows"`
}

// RepoWorkflows reads the workflows this deployment declares for repo.
// A repo that declares none is not an error: it reports an empty list,
// which is exactly what it contributes to a run.
func (c *Client) RepoWorkflows(ctx context.Context, repo string) (RepoWorkflowsResponse, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoWorkflowsResponse{}, validationErrorf("repo: %v", err)
	}
	return c.repoWorkflows(ctx, parsed)
}

func (c *Client) repoWorkflows(ctx context.Context, repo model.RepoRef) (RepoWorkflowsResponse, error) {
	stored, err := c.Store.GetRepoWorkflows(ctx, repo)
	if err != nil {
		return RepoWorkflowsResponse{}, err
	}
	out := RepoWorkflowsResponse{Repo: repo.String(), Dir: ci.WorkflowDir, Workflows: []RepoWorkflow{}}
	for _, w := range stored {
		// A stored workflow that no longer parses -- this build tightened
		// the format, or a settings file arrived from a state repository
		// that was never sent through this route -- is still listed, with
		// no name, rather than failing the read. Somebody has to be able
		// to see the file in order to fix it.
		name := ""
		if parsed, err := ci.Parse(w.Path, []byte(w.Text)); err == nil {
			name = parsed.Name
		}
		out.Workflows = append(out.Workflows, RepoWorkflow{Path: w.Path, Text: w.Text, Name: name})
	}
	return out, nil
}

// SetRepoWorkflows replaces the whole of what this deployment declares
// for repo, and is where a workflow file is checked.
//
// Three things happen before anything is written, and each is a thing
// somebody is better told now than later:
//
//   - a bare file name is filled out to a path under ci.WorkflowDir
//     (model.RepoWorkflowPath), because the directory is not a choice
//     whoever names a workflow gets to make, and anything else is
//     checked against model.ValidateRepoWorkflowPath;
//   - the file is parsed, with ci.Parse -- the run's own parse, naming
//     the same key in the same words -- so a workflow that could never
//     run is refused while its author is still looking at it;
//   - two workflows at one path are refused rather than silently
//     collapsed, since the path is the key both the store and ci.Layered
//     identify a workflow by.
//
// It does not check repo against Config.TargetRepos, for the reason
// SetRepoSetupCommand does not: a repo may be configured before it is
// allow-listed, and nothing here is granted by the row alone. Nor does
// it check the `grain-ci` capability. That is deliberate and is the
// line this whole feature has to keep: whether a repository may have CI
// run against it at all is decided outside any workflow, per task or
// per repo (pkg/capability/grainci), and declaring a file that would
// run if it ever were allowed grants nothing. A workflow declared here
// widens nothing that pkg/ci's "What a workflow file is trusted with"
// does not already leave open to a `.grain/workflows` file in the
// repository itself -- this route decides *what runs*, never what a job
// may reach.
func (c *Client) SetRepoWorkflows(ctx context.Context, repo string, workflows []RepoWorkflow) (RepoWorkflowsResponse, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoWorkflowsResponse{}, validationErrorf("repo: %v", err)
	}
	if err := c.checkRepoPerson(ctx, parsed); err != nil {
		return RepoWorkflowsResponse{}, err
	}
	if err := c.checkSettingsWritable(ctx, "repo_workflow"); err != nil {
		return RepoWorkflowsResponse{}, err
	}
	seen := make(map[string]bool, len(workflows))
	rows := make([]model.RepoWorkflow, 0, len(workflows))
	for _, w := range workflows {
		path := model.RepoWorkflowPath(w.Path)
		if err := model.ValidateRepoWorkflowPath(path); err != nil {
			return RepoWorkflowsResponse{}, validationErrorf("workflows: %v", err)
		}
		if seen[path] {
			return RepoWorkflowsResponse{}, validationErrorf("workflows: %s is declared twice", path)
		}
		seen[path] = true
		if _, err := ci.Parse(path, []byte(w.Text)); err != nil {
			return RepoWorkflowsResponse{}, validationErrorf("workflows: %v", err)
		}
		rows = append(rows, model.RepoWorkflow{Repo: parsed, Path: path, Text: w.Text})
	}
	if err := c.Store.PutRepoWorkflows(ctx, parsed, rows); err != nil {
		return RepoWorkflowsResponse{}, err
	}
	return c.repoWorkflows(ctx, parsed)
}

func (s *Server) handleGetRepoWorkflows(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.RepoWorkflows(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetRepoWorkflows(w http.ResponseWriter, r *http.Request) {
	var req SetRepoWorkflowsRequest
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.SetRepoWorkflows(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.Workflows)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
