package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

// GET /api/tasks/{id}/tree against the demo's own store: the director's
// chat filed the tasks its conversation lists, and every task in that
// tree -- the chat itself, or any one of its follow-ups -- answers with
// the same tree, nested by who filed what, in the progress comment's own
// words (docs/work-trees.md).
func TestDemoTaskTree(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}
	tasks, err := ui.NewClient(cfg, store).ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]string{}
	for _, task := range tasks {
		byTitle[task.Title] = task.ID
	}
	director := byTitle["Director Session"]
	srv := ui.NewServer(withAccessCode(cfg), store)
	get := func(id string) (int, ui.WorkTree) {
		// Over loopback with the access code, as the deployment's own
		// actor -- the way `grain demo` is read in the e2e specs.
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+id+"/tree", nil)
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var tree ui.WorkTree
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
				t.Fatalf("decoding the tree: %v", err)
			}
		}
		return rec.Code, tree
	}

	code, tree := get(director)
	if code != http.StatusOK {
		t.Fatalf("GET /api/tasks/%s/tree: %d", director, code)
	}
	if tree.Origin != "task:"+director || tree.RootTask != director {
		t.Errorf("origin = %q, root = %q, want the director's chat", tree.Origin, tree.RootTask)
	}
	if tree.Done != 0 || tree.Total != 4 {
		t.Errorf("%d of %d done, want 0 of 4 (the chat itself is not counted)", tree.Done, tree.Total)
	}
	if len(tree.Nodes) != 1 || tree.Nodes[0].ID != director || !tree.Nodes[0].Root {
		t.Fatalf("roots = %+v, want the director's chat alone", tree.Nodes)
	}
	type want struct {
		title, status, pr string
		children          []string
	}
	wants := []want{
		{title: "Add a dark mode toggle", status: model.TreeStatusPlanned},
		{title: "Bump the Go toolchain to 1.24", status: model.TreeStatusRunning},
		{title: "Add pagination to the tasks API", status: model.TreeStatusPROpen, pr: "acme/widgets#104",
			children: []string{byTitle["Extract pagination query helper"]}},
	}
	children := tree.Nodes[0].Children
	if len(children) != len(wants) {
		t.Fatalf("the chat's children = %+v, want %d", children, len(wants))
	}
	for i, w := range wants {
		got := children[i]
		if got.ID != byTitle[w.title] || got.Title != w.title || got.Status != w.status || got.PullRequest != w.pr || got.Done {
			t.Errorf("child %d = %+v, want %q %s %q", i, got, w.title, w.status, w.pr)
		}
		var ids []string
		for _, c := range got.Children {
			ids = append(ids, c.ID)
			if c.Status != model.TreeStatusQueued {
				t.Errorf("%s's status = %q, want queued", c.ID, c.Status)
			}
		}
		if len(ids) != len(w.children) || (len(ids) > 0 && ids[0] != w.children[0]) {
			t.Errorf("%s's children = %v, want %v", got.ID, ids, w.children)
		}
	}

	// A follow-up answers with the same tree: it is the chat's.
	if _, sub := get(byTitle["Extract pagination query helper"]); sub.RootTask != director || sub.Total != 4 {
		t.Errorf("the follow-up's tree = %+v, want the director's", sub)
	}
	// A task in no tree has nothing to draw but itself.
	if _, lone := get(byTitle["Fix flaky retry test"]); lone.Total != 0 || len(lone.Nodes) != 1 {
		t.Errorf("a task in no tree = %+v, want itself and a total of 0", lone)
	}
	if code, _ := get("999999"); code != http.StatusNotFound {
		t.Errorf("an unknown task's tree: %d, want 404", code)
	}
}
