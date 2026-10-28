package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/memory"
	"github.com/bwsalmon/grain/pkg/model"
)

// Memory is only worth storing if it reaches a prompt, and it is only
// safe to store if a run can tell it apart from the instructions beside
// it -- so what these pin is that both blocks arrive, that memory is
// labelled as notes from earlier runs rather than as a decision somebody
// made, and that a deployment with no memory at all (which is every
// deployment until the memory agent has run once) gets a prompt with
// nothing extra in it.

func TestPrepareCapabilitiesCarriesMemoryAndTheExtensionApart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	task := model.Task{ID: "t1", Title: "Do the thing"}
	cc := model.CapabilityContext{Task: task}
	_, prompt, err := prepareCapabilities(ctx, nil, cc, nil, t.TempDir(), nil, nil, History{}, nil,
		checkout{}, model.RepoRef{}, "", false, "always run `make lint`", "the e2e suite is flaky on retry",
		DefaultMaxRunRuntime)
	if err != nil {
		t.Fatalf("prepareCapabilities: %v", err)
	}
	if !strings.Contains(prompt, "the e2e suite is flaky on retry") {
		t.Errorf("the dispatch prompt never carries what grain has learned:\n%s", prompt)
	}
	if !strings.Contains(prompt, "always run `make lint`") {
		t.Errorf("the dispatch prompt lost its standing instructions:\n%s", prompt)
	}
	// The headings, not the text: a run that cannot tell "somebody
	// decided this" from "a run last week noticed this" has no way to
	// weigh one against the other when they disagree.
	memoryAt := strings.Index(prompt, "What grain has learned")
	extensionAt := strings.Index(prompt, "Standing instructions")
	if memoryAt < 0 || extensionAt < 0 {
		t.Fatalf("the two blocks are not both named in the prompt:\n%s", prompt)
	}
	if memoryAt > extensionAt {
		t.Error("memory comes after the standing instructions; what is known should come before what is asked")
	}
}

func TestMemorySectionSaysNothingWhenThereIsNothingToSay(t *testing.T) {
	t.Parallel()
	if got := memorySection(""); got != "" {
		t.Errorf("memorySection(\"\") = %q, want no heading at all for a deployment that has learned nothing", got)
	}
}

// The two layers as a dispatch actually reads them: the deployment's own
// and the target repo's, with a task that targets nothing reading only
// the first. Against a real store, because "which rows does a dispatch
// read" is the whole of what resolveMemory does.
func TestResolveMemoryReadsBothLayers(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	if err := store.PutMemory(ctx, model.Memory{Text: "deployment-wide"}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	if err := store.PutMemory(ctx, model.Memory{Repo: &repo, Text: "repo-specific"}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	got, err := resolveMemory(ctx, store, model.Task{ID: "t1", Target: &repo}, time.Now())
	if err != nil {
		t.Fatalf("resolveMemory: %v", err)
	}
	if !strings.Contains(got, "deployment-wide") || !strings.Contains(got, "repo-specific") {
		t.Errorf("resolveMemory = %q, want both layers", got)
	}

	got, err = resolveMemory(ctx, store, model.Task{ID: "t2"}, time.Now())
	if err != nil {
		t.Fatalf("resolveMemory (no target): %v", err)
	}
	if strings.Contains(got, "repo-specific") {
		t.Errorf("resolveMemory = %q, want no repo layer for a task that targets no repo", got)
	}
	if !strings.Contains(got, "deployment-wide") {
		t.Errorf("resolveMemory = %q, want the deployment's own memory even with no target", got)
	}
}

// The index of what grain keeps in full: named in the prompt of a run
// that could read one, and absent from every other run's -- a line
// pointing at a tool this run does not have could only send it looking
// for one.
func TestResolveMemoryIndexesDocumentsForARunThatCanReadThem(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	written := time.Now().UTC().Add(-72 * time.Hour)
	for _, d := range []model.MemoryDocument{
		{Name: "state-repo-shape", Summary: "how the dump is laid out",
			Text: "one file per table", UpdatedAt: written},
		{Repo: &repo, Name: "flaky-e2e", Summary: "why the third retry fails",
			Text: "the suite reuses the VM", UpdatedAt: written},
		{Repo: &model.RepoRef{Owner: "bwsalmon", Name: "kontur"}, Name: "vm-boot",
			Summary: "how a guest comes up", Text: "slowly", UpdatedAt: written},
	} {
		if err := store.PutMemoryDocument(ctx, d); err != nil {
			t.Fatalf("PutMemoryDocument: %v", err)
		}
	}

	granted := model.Task{ID: "t1", Target: &repo,
		Grants: []model.Grant{{Capability: memory.CapabilityName}}}
	got, err := resolveMemory(ctx, store, granted, time.Now().UTC())
	if err != nil {
		t.Fatalf("resolveMemory: %v", err)
	}
	// This run's own two scopes, with what each note is about and how old
	// it is -- and not a word of any note's text.
	for _, want := range []string{"state-repo-shape", "flaky-e2e", "how the dump is laid out",
		"written 3 days ago", "read_memory_document"} {
		if !strings.Contains(got, want) {
			t.Errorf("the index never mentions %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "the suite reuses the VM") {
		t.Errorf("the index carries a document's text, which is the one thing it exists not to do:\n%s", got)
	}
	// A third repo's note is somebody else's business: the index is the
	// two layers this dispatch reads, exactly as the memory above it is.
	if strings.Contains(got, "vm-boot") {
		t.Errorf("the index names a scope this task has nothing to do with:\n%s", got)
	}

	ungranted, err := resolveMemory(ctx, store, model.Task{ID: "t2", Target: &repo}, time.Now().UTC())
	if err != nil {
		t.Fatalf("resolveMemory (no grant): %v", err)
	}
	if strings.Contains(ungranted, "flaky-e2e") {
		t.Errorf("a run with no way to read a document was told one exists:\n%s", ungranted)
	}
}

// A memory an operator wrote by hand reaches the prompt saying so, and
// only that layer does.
//
// memorySection tells every run that memory is earlier runs'
// observations, to be replaced once they have stopped being true. That
// is right about what the memory agent wrote and wrong about what a
// person typed on the Settings pane, and the memory agent is itself one
// of the runs reading this: "replace what is no longer true" quietly
// means "replace what the operator just fixed" unless the prompt says
// which is which (model.MemoryHandNote, grain/task-394).
func TestResolveMemoryMarksTheLayerAPersonWrote(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	if err := store.PutMemory(ctx, model.Memory{
		Text: "grain worked this out", UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "a1b2"},
	}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	if err := store.PutMemory(ctx, model.Memory{
		Repo: &repo, Text: "somebody corrected this",
		UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"},
	}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	got, err := resolveMemory(ctx, store, model.Task{ID: "t1", Target: &repo}, time.Now())
	if err != nil {
		t.Fatalf("resolveMemory: %v", err)
	}
	note := model.MemoryHandNote(model.Principal{Kind: model.PrincipalHuman})
	if strings.Count(got, note) != 1 {
		t.Fatalf("the prompt says a person wrote %d of its two layers, want the one they did:\n%s",
			strings.Count(got, note), got)
	}
	if strings.Index(got, note) < strings.Index(got, "grain worked this out") {
		t.Errorf("the mark landed on the layer the memory agent wrote:\n%s", got)
	}
}

// Memory has no team layer (single-tenant reset, stage 4d): a task's
// team, which is inert metadata, changes nothing about what it is told.
func TestResolveMemoryIgnoresTheTasksTeam(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "app"}
	if err := store.PutMemory(ctx, model.Memory{Text: "deployment notes"}); err != nil {
		t.Fatalf("PutMemory deployment: %v", err)
	}
	if err := store.PutMemory(ctx, model.Memory{Repo: &repo, Text: "repo notes"}); err != nil {
		t.Fatalf("PutMemory repo: %v", err)
	}
	var prompts []string
	for _, team := range []string{"", model.DefaultTeamID, "team-b"} {
		got, err := resolveMemory(ctx, store, model.Task{ID: "t-" + team, Target: &repo}, time.Now())
		if err != nil {
			t.Fatalf("resolveMemory (team %q): %v", team, err)
		}
		if !strings.Contains(got, "deployment notes") || !strings.Contains(got, "repo notes") {
			t.Errorf("team %q: got %q, want the deployment's and the repo's notes", team, got)
		}
		if strings.Contains(got, "your team") {
			t.Errorf("team %q: got %q, want no team layer", team, got)
		}
		prompts = append(prompts, got)
	}
	if prompts[0] != prompts[1] || prompts[1] != prompts[2] {
		t.Errorf("the memory a task is told differs by its team: %q", prompts)
	}
}
