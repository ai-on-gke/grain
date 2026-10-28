package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

type fakeRenamer struct {
	titles []string
	report SessionRenameReport
	err    error
}

func (f *fakeRenamer) RenameSession(_ context.Context, title string) (SessionRenameReport, error) {
	f.titles = append(f.titles, title)
	if f.err != nil {
		return SessionRenameReport{}, f.err
	}
	if f.report.Title == "" {
		return SessionRenameReport{Title: title}, nil
	}
	return f.report, nil
}

// renameSession calls the tool the way a client would: by name, off the
// constructor, with whatever arguments the model sent.
func renameSession(t *testing.T, renamer SessionRenamer, args map[string]any) Result {
	t.Helper()
	tools := NewRenameSessionTools(renamer)
	if len(tools) != 1 || tools[0].Name != "rename_session" {
		t.Fatalf("NewRenameSessionTools returned %+v, want one rename_session tool", tools)
	}
	return tools[0].Handler(context.Background(), args)
}

func TestRenameSessionSendsTheTitleAndConfirmsIt(t *testing.T) {
	renamer := &fakeRenamer{}

	res := renameSession(t, renamer, map[string]any{"title": "Nightly CI failure"})
	if res.IsError {
		t.Fatalf("rename_session reported an error: %s", res.Text)
	}
	if len(renamer.titles) != 1 || renamer.titles[0] != "Nightly CI failure" {
		t.Fatalf("renamer got %q, want the title as written", renamer.titles)
	}
	// The confirmation quotes it back, and reminds the agent this is a
	// one-time naming rather than a status line.
	if !strings.Contains(res.Text, "Nightly CI failure") {
		t.Errorf("text = %q, want the title quoted back", res.Text)
	}
	if !strings.Contains(res.Text, "do not call this again") || strings.Contains(res.Text, "unless the subject") {
		t.Errorf("text = %q, want it to say the name is final and not to call again", res.Text)
	}
}

// A title is one line in the rail, so one that arrives as several is
// flattened rather than refused.
func TestRenameSessionFlattensWhitespace(t *testing.T) {
	renamer := &fakeRenamer{}

	if res := renameSession(t, renamer, map[string]any{
		"title": "  Nightly CI\nfailure  ",
	}); res.IsError {
		t.Fatalf("rename_session reported an error: %s", res.Text)
	}
	if got := renamer.titles[0]; got != "Nightly CI failure" {
		t.Errorf("renamer got %q, want it flattened onto one line", got)
	}
}

// Both refusals happen before the renamer is reached: an empty title
// names nothing, and one too long for the rail is worth shortening in
// the same turn rather than after a round trip. The bound is the model's
// own, so the tool and the daemon can never disagree about it.
func TestRenameSessionRefusesTitlesItCannotShow(t *testing.T) {
	if MaxSessionTitleLength != model.ConsoleSessionTitleMaxLength {
		t.Fatalf("MaxSessionTitleLength = %d, want model.ConsoleSessionTitleMaxLength (%d)",
			MaxSessionTitleLength, model.ConsoleSessionTitleMaxLength)
	}
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing", nil, "required"},
		{"empty", map[string]any{"title": "   "}, "required"},
		{"too long", map[string]any{"title": strings.Repeat("x", MaxSessionTitleLength+1)}, "characters"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			renamer := &fakeRenamer{}
			res := renameSession(t, renamer, tt.args)
			if !res.IsError {
				t.Fatalf("rename_session accepted %v: %s", tt.args, res.Text)
			}
			if !strings.Contains(res.Text, tt.want) {
				t.Errorf("text = %q, want it to mention %q", res.Text, tt.want)
			}
			if len(renamer.titles) != 0 {
				t.Errorf("renamer was reached with %q; the refusal should come first", renamer.titles)
			}
		})
	}
	// Exactly the limit is fine: the bound is "at most", as the
	// description says.
	renamer := &fakeRenamer{}
	if res := renameSession(t, renamer, map[string]any{
		"title": strings.Repeat("é", MaxSessionTitleLength),
	}); res.IsError {
		t.Errorf("a title of exactly %d runes was refused: %s", MaxSessionTitleLength, res.Text)
	}
}

// The daemon's refusal -- a task that is not a session, a deployment
// whose settings are read-only -- is the agent's answer, verbatim.
func TestRenameSessionRelaysTheDaemonsError(t *testing.T) {
	renamer := &fakeRenamer{err: errors.New("task 7 is not a standing chat session")}
	res := renameSession(t, renamer, map[string]any{"title": "Nightly CI failure"})
	if !res.IsError || !strings.Contains(res.Text, "not a standing chat session") {
		t.Fatalf("res = %+v, want the daemon's error relayed", res)
	}
}

// With nobody to ask, the tool still exists -- so the frameworks'
// allowed-tool lists can name it -- and refuses without pretending.
func TestRenameSessionWithoutARenamerRefuses(t *testing.T) {
	res := renameSession(t, nil, map[string]any{"title": "Nightly CI failure"})
	if !res.IsError || !strings.Contains(res.Text, "cannot rename") {
		t.Fatalf("res = %+v, want a refusal saying the run cannot rename its session", res)
	}
}

// The description is what an agent reads before deciding whether to call
// it: a one-time naming of an auto-titled session, with no invitation to
// retitle the chat when the subject drifts.
func TestRenameSessionDescriptionIsAOneTimeNaming(t *testing.T) {
	desc := NewRenameSessionTools(nil)[0].Description
	for _, want := range []string{"once", "auto-titled", "refuses"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description = %q, want it to mention %q", desc, want)
		}
	}
	if strings.Contains(desc, "unless the subject") {
		t.Errorf("description = %q, still invites a rename when the subject changes", desc)
	}
}
