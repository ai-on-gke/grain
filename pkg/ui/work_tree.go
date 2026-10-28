package ui

import (
	"context"
	"net/http"

	"github.com/ai-on-gke/grain/pkg/model"
)

// WorkTree is GET /api/tasks/{id}/tree's body: the tree of work task id
// belongs to (model.WorkOrigin), nested by which task filed which, with
// the same status words and the same "n of m done" the progress comment
// on a GitHub origin carries -- both are drawn from model.Store.WorkTree,
// so the two cannot disagree (docs/work-trees.md).
//
// The tree is the one the task's own origin names, or, for a task with
// none, the one rooted at the task itself: a chat that has filed work is
// the root of that work's tree. Total is 0 for a task with no tree to
// show -- nothing filed under it, and no origin -- whose Nodes is then
// the task alone.
type WorkTree struct {
	Origin string `json:"origin"`
	// RootTask is the grain task at the tree's root, "" for a tree whose
	// origin is a GitHub pull request or issue.
	RootTask string         `json:"rootTask,omitempty"`
	Done     int            `json:"done"`
	Total    int            `json:"total"`
	Nodes    []WorkTreeNode `json:"nodes"`
}

// WorkTreeNode is one task in a WorkTree.
type WorkTreeNode struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Status is model.WorkTreeStatus's word: planned, queued, running,
	// "PR open", merged, closed, done, and the rarer ones beside them.
	Status string `json:"status"`
	// PullRequest is the task's pull request as owner/repo#N, "" for none.
	PullRequest string `json:"pullRequest,omitempty"`
	Done        bool   `json:"done"`
	// Root marks the task the tree is rooted at, which is not counted in
	// Done/Total (model.WorkTreeNode.Root).
	Root     bool           `json:"root,omitempty"`
	Children []WorkTreeNode `json:"children,omitempty"`
}

// WorkTree is task id's tree, drawn with only the tasks include lets
// through (nil for all of them). A task that does not exist is a
// NotFoundError.
func (c *Client) WorkTree(ctx context.Context, id string, include func(model.Task) bool) (WorkTree, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return WorkTree{}, err
	}
	if task == nil {
		return WorkTree{}, &NotFoundError{ID: id}
	}
	// A task with no origin of its own is a root only when nobody filed
	// it: one from before origins existed, filed part way down a tree,
	// is drawn in its filer's tree, not as the root of one.
	origin := task.WorkOrigin
	if origin == "" {
		if origin, err = c.Store.TreeOrigin(ctx, task.ID); err != nil {
			return WorkTree{}, err
		}
	}
	tree, err := c.Store.WorkTree(ctx, origin, include)
	if err != nil {
		return WorkTree{}, err
	}
	out := WorkTree{Origin: string(origin), RootTask: origin.Task(), Nodes: []WorkTreeNode{}}
	if tree == nil {
		return out, nil
	}
	out.Done, out.Total = tree.Done, tree.Total
	out.Nodes = workTreeNodes(tree.Roots)
	return out, nil
}

func workTreeNodes(nodes []*model.WorkTreeNode) []WorkTreeNode {
	out := make([]WorkTreeNode, 0, len(nodes))
	for _, n := range nodes {
		v := WorkTreeNode{
			ID:     n.Task.ID,
			Title:  n.Task.Title,
			Status: n.Status,
			Done:   n.Done,
			Root:   n.Root,
		}
		if n.PullRequest != nil {
			v.PullRequest = n.PullRequest.String()
		}
		if len(n.Children) > 0 {
			v.Children = workTreeNodes(n.Children)
		}
		out = append(out, v)
	}
	return out
}

// handleGetTaskTree serves GET /api/tasks/{id}/tree (model.Store.WorkTree).
func (s *Server) handleGetTaskTree(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tree, err := s.tasks.WorkTree(r.Context(), id, nil)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}
