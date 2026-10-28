package mcp

import (
	"context"
	"strings"
	"testing"
)

// supersedingFiler records what a live propose_task call asked to
// supersede and answers with whatever the daemon would have answered:
// the task it actually closed, which is "" for a supersede grain
// refused (model.SupersedeRefusal).
type supersedingFiler struct {
	asked      string
	superseded string
}

func (f *supersedingFiler) FileProposal(_ context.Context, p ProposedTask, _ string) (FiledProposal, error) {
	f.asked = p.Supersedes
	return FiledProposal{ID: "9", URL: "/tasks/9", Superseded: f.superseded}, nil
}

// The argument has to reach the filer as a filer can use it: an id, with
// the whitespace an agent's own formatting leaves around it gone.
func TestProposeTaskCarriesSupersedesToTheFiler(t *testing.T) {
	f := &supersedingFiler{superseded: "468"}
	got := NewLiveProposalTools(&MockSink{}, nil, "471", f)[0].
		Handler(context.Background(), map[string]any{
			"title": "revision five", "body": "the design again", "supersedes": " 468 ",
		})
	if got.IsError {
		t.Fatalf("result = %+v", got)
	}
	if f.asked != "468" {
		t.Errorf("filer was asked to supersede %q, want \"468\"", f.asked)
	}
	if !strings.Contains(got.Text, "468 is closed as superseded") {
		t.Errorf("result = %q, want it to say the task was closed", got.Text)
	}
}

// And when the daemon would not close it, the run is told so rather than
// left believing a stale task has stopped being dispatchable -- the one
// belief that makes it stop asking anybody to close it (FiledProposal's
// own doc comment on why Queued is reported the same way).
func TestProposeTaskSaysWhenTheSupersedeDidNotHappen(t *testing.T) {
	f := &supersedingFiler{}
	got := NewLiveProposalTools(&MockSink{}, nil, "471", f)[0].
		Handler(context.Background(), map[string]any{
			"title": "revision five", "body": "the design again", "supersedes": "468",
		})
	if got.IsError {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(got.Text, "468 is still open") {
		t.Errorf("result = %q, want it to say the task was left open", got.Text)
	}
}

// A proposal that supersedes nothing says nothing about superseding, so
// the ordinary call reads exactly as it always has.
func TestProposeTaskSaysNothingAboutSupersedingWhenNoneWasAsked(t *testing.T) {
	f := &supersedingFiler{}
	got := NewLiveProposalTools(&MockSink{}, nil, "471", f)[0].
		Handler(context.Background(), map[string]any{"title": "new work", "body": "unrelated"})
	if got.IsError {
		t.Fatalf("result = %+v", got)
	}
	if strings.Contains(strings.ToLower(got.Text), "supersed") {
		t.Errorf("result = %q, want no mention of a supersede nobody asked for", got.Text)
	}
}

// The bounds belong in the description, not only in the code that
// enforces them: a run that cannot see why a supersede might be refused
// will keep asking for ones that are, and will not say in its closing
// note which tasks somebody still has to close by hand.
func TestSupersedesArgumentDescribesItsBounds(t *testing.T) {
	description := argumentDescription(t,
		toolNamed(t, NewMockTools(&MockSink{}, nil), "propose_task"), "supersedes")
	for _, want := range []string{"never been dispatched", "same", "repository", "left open"} {
		if !strings.Contains(description, want) {
			t.Errorf("supersedes description = %q, want it to mention %q", description, want)
		}
	}
}

// A supersede that moved the old task's dependents onto the new one says
// so, and says which it left alone and why -- the run has just changed
// what other tasks wait on, and is the one that knows whether it meant to.
func TestProposeTaskReportsTheDependentsASupersedeMoved(t *testing.T) {
	filer := filerFunc(func(context.Context, ProposedTask, string) (FiledProposal, error) {
		return FiledProposal{ID: "9", URL: "/tasks/9", Superseded: "468",
			SupersedeMoved: []string{"480", "481"},
			SupersedeKept:  []SupersedeKept{{ID: "490", Reason: "it would wait on itself"}},
		}, nil
	})
	got := NewLiveProposalTools(&MockSink{}, nil, "471", filer)[0].
		Handler(context.Background(), map[string]any{
			"title": "revision five", "body": "the design again", "supersedes": "468",
		})
	if got.IsError {
		t.Fatalf("result = %+v", got)
	}
	for _, want := range []string{
		"#480, #481 were waiting on task 468 and now wait on #9 instead",
		"#490 was waiting on task 468 and was not moved: it would wait on itself",
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("result = %q, want it to say %q", got.Text, want)
		}
	}
}

type filerFunc func(context.Context, ProposedTask, string) (FiledProposal, error)

func (f filerFunc) FileProposal(ctx context.Context, p ProposedTask, key string) (FiledProposal, error) {
	return f(ctx, p, key)
}
