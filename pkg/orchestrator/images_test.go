package orchestrator_test

import (
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// pngBytes is enough of a PNG for content sniffing to call it one.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR screenshots")

// publishedImageRefs is every ref the images were published on, with
// the commit each points at.
func publishedImageRefs(t *testing.T, bare string) map[string]string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "for-each-ref",
		"--format=%(refname) %(objectname)", "refs/grain-images/").Output()
	if err != nil {
		t.Fatalf("listing published refs: %v", err)
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok {
			refs[name] = sha
		}
	}
	return refs
}

// The images a run attaches to its closing note reach the pull request
// it pushed: committed to the repository off every branch, and linked
// from the description above grain's footer. What is not an image stays
// in grain's conversation only.
func TestAClosingNotesImagesAreShownInThePullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "Centre the tab counts\n\nThey sat below their labels.")

	result := toolResult(agent.ToolCall{
		Name: "comment_on_issue", Arguments: map[string]any{
			"comment": "Done; screenshots attached.",
			"attachments": []any{
				map[string]any{"filename": "contact-sheet.png", "content_type": "image/png",
					"content": base64.StdEncoding.EncodeToString(pngBytes)},
				// No content type, which defaults to octet-stream: the
				// name and the bytes are what make it an image.
				map[string]any{"filename": "contact-sheet.png",
					"content": base64.StdEncoding.EncodeToString(pngBytes)},
				map[string]any{"filename": "notes.txt", "content_type": "text/plain",
					"content": base64.StdEncoding.EncodeToString([]byte("not an image"))},
			},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	refs := publishedImageRefs(t, sim.BareRepo)
	if len(refs) != 1 {
		t.Fatalf("published refs = %v, want one for the closing note", refs)
	}
	var sha string
	for name, s := range refs {
		if !strings.HasPrefix(name, "refs/grain-images/"+branch+"/") {
			t.Fatalf("published on %s, want a ref under the task's branch name", name)
		}
		sha = s
	}
	files, err := exec.Command("git", "--git-dir", sim.BareRepo, "ls-tree", "--name-only", sha).Output()
	if err != nil || strings.Fields(string(files))[0] != "2-contact-sheet.png" || len(strings.Fields(string(files))) != 2 {
		t.Fatalf("published tree = %q (%v), want both images under distinct names and nothing else", files, err)
	}

	body := prByHead(t, sim, branch).Body
	if !strings.HasPrefix(body, "Centre the tab counts") {
		t.Fatalf("description = %q, want the commit message still leading it", body)
	}
	for _, want := range []string{"### Images", "/blob/" + sha + "/contact-sheet.png?raw=true)", "/blob/" + sha + "/2-contact-sheet.png?raw=true)"} {
		if !strings.Contains(body, want) {
			t.Fatalf("description = %q, want it to contain %q", body, want)
		}
	}
	if strings.Contains(body, "notes.txt") {
		t.Fatalf("description = %q, want the text file left out", body)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if lines[len(lines)-1] != "Automated change for grain task t1." {
		t.Fatalf("description = %q, want grain's footer still its last line", body)
	}

	// Another finish -- the branch moved on, and this run left no images
	// -- rewrites the description from the commits and keeps the images,
	// which no commit message could have brought back.
	pushCommitSaying(t, sim.BareRepo, branch, "Fix the lint")
	if err := orchestrator.ProcessResult(ctx, store, client, task,
		toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"}), "t1-2", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	again := prByHead(t, sim, branch).Body
	if !strings.Contains(again, "- Fix the lint") || !strings.Contains(again, "/blob/"+sha+"/contact-sheet.png?raw=true)") {
		t.Fatalf("description after another finish = %q, want the new commit listed and the images kept", again)
	}
	if strings.Count(again, "### Images") != 1 {
		t.Fatalf("description = %q, want the images section exactly once", again)
	}
	if got := publishedImageRefs(t, sim.BareRepo); len(got) != 1 {
		t.Fatalf("published refs = %v, want nothing published twice", got)
	}
}

// A note with no images publishes nothing and leaves the description
// as the commits wrote it.
func TestANoteWithoutImagesPublishesNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "Teach the parser about headers\n\nAn explanation.")

	result := toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := publishedImageRefs(t, sim.BareRepo); len(got) != 0 {
		t.Fatalf("published refs = %v, want none", got)
	}
	if body := prByHead(t, sim, branch).Body; strings.Contains(body, "grain:images") {
		t.Fatalf("description = %q, want no images section", body)
	}
}
