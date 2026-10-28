package cli

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

// list_test.go covers what taskQuery decides; this covers the wiring
// around it, against a real server -- that every flag reaches the field
// it names, that a tri-state flag can be asked in the negative, and that
// a value nobody can act on stops the command rather than printing an
// empty listing.
func listTestServer(t *testing.T) string {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Widget work", Repo: "acme/widgets", Approved: true, AutoMerge: true,
	}); err != nil {
		t.Fatalf("creating the queued task: %v", err)
	}
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Gadget work", NoRepo: true,
	}); err != nil {
		t.Fatalf("creating the proposed task: %v", err)
	}
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestListFlagsNarrowTheListing(t *testing.T) {
	server := listTestServer(t)
	for _, tc := range []struct {
		name  string
		args  []string
		want  []string
		avoid []string
	}{
		{
			name: "no flags lists everything",
			args: []string{"list"},
			want: []string{"Widget work", "Gadget work"},
		},
		{
			name:  "state",
			args:  []string{"list", "-state", "queued"},
			want:  []string{"Widget work"},
			avoid: []string{"Gadget work"},
		},
		{
			name:  "repo",
			args:  []string{"list", "-repo", "acme/widgets"},
			want:  []string{"Widget work"},
			avoid: []string{"Gadget work"},
		},
		{
			name:  "the tasks with no repo at all",
			args:  []string{"list", "-repo", "none"},
			want:  []string{"Gadget work"},
			avoid: []string{"Widget work"},
		},
		{
			name:  "search",
			args:  []string{"list", "-search", "gadget"},
			want:  []string{"Gadget work"},
			avoid: []string{"Widget work"},
		},
		// A tri-state flag asked in the negative: the tasks that do not
		// merge themselves, which "-auto-merge" left off could not ask
		// for.
		{
			name:  "auto-merge, in the negative",
			args:  []string{"list", "-auto-merge=false"},
			want:  []string{"Gadget work"},
			avoid: []string{"Widget work"},
		},
		{
			name:  "auto-merge",
			args:  []string{"list", "-auto-merge"},
			want:  []string{"Widget work"},
			avoid: []string{"Gadget work"},
		},
		// grain/task-65: the conditions the UI's own filter editor
		// writes, typed instead of picked.
		{
			name:  "a condition",
			args:  []string{"list", "-filter", "repo:any:acme/widgets"},
			want:  []string{"Widget work"},
			avoid: []string{"Gadget work"},
		},
		{
			name: "either of two values, as a link spells it",
			args: []string{"list", "-filter", "state:any:queued|proposed"},
			want: []string{"Widget work", "Gadget work"},
		},
		{
			name:  "the tasks with no value at all",
			args:  []string{"list", "-filter", "repo:empty"},
			want:  []string{"Gadget work"},
			avoid: []string{"Widget work"},
		},
		{
			name:  "text a title contains",
			args:  []string{"list", "-filter", "title:has:widget"},
			want:  []string{"Widget work"},
			avoid: []string{"Gadget work"},
		},
		{
			name: "a date, read in the deployment's own zone",
			args: []string{"list", "-filter", "created:within:1"},
			want: []string{"Widget work", "Gadget work"},
		},
		{
			name:  "two conditions, both of which have to hold",
			args:  []string{"list", "-filter", "state:any:queued", "-filter", "repo:any:acme/widgets"},
			want:  []string{"Widget work"},
			avoid: []string{"Gadget work"},
		},
		{
			name:  "a condition and a shorthand together",
			args:  []string{"list", "-repo", "acme/widgets", "-filter", "autoMerge:any:on"},
			want:  []string{"Widget work"},
			avoid: []string{"Gadget work"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				if err := runCLIWithAccessCode(append([]string{"-server", server}, tc.args...)); err != nil {
					t.Fatalf("grain %s: %v", strings.Join(tc.args, " "), err)
				}
			})
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("grain %s printed %q, want it to list %q", strings.Join(tc.args, " "), out, want)
				}
			}
			for _, avoid := range tc.avoid {
				if strings.Contains(out, avoid) {
					t.Errorf("grain %s printed %q, want it not to list %q", strings.Join(tc.args, " "), out, avoid)
				}
			}
		})
	}
}

func TestListSortsByTitle(t *testing.T) {
	server := listTestServer(t)
	// The named order this command has always taken, and the sort key
	// it now stands for -- both of which put Gadget first.
	for _, order := range []string{"title", "title:asc"} {
		out := captureStdout(t, func() {
			if err := runCLIWithAccessCode([]string{"-server", server, "list", "-sort", order}); err != nil {
				t.Fatalf("grain list -sort %s: %v", order, err)
			}
		})
		gadget, widget := strings.Index(out, "Gadget work"), strings.Index(out, "Widget work")
		if gadget < 0 || widget < 0 {
			t.Fatalf("grain list -sort %s printed %q, want both tasks", order, out)
		}
		if gadget > widget {
			t.Errorf("grain list -sort %s printed %q, want Gadget before Widget", order, out)
		}
	}
}

// grain/task-65: a sort is a list of keys, the first deciding and the
// rest breaking its ties -- "state, then newest first", which no single
// named order could say. The two tasks share no state here, so what this
// pins is that a second key parses and orders rather than being ignored.
func TestListSortsOnSeveralKeys(t *testing.T) {
	server := listTestServer(t)
	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "list", "-sort", "state:asc,created:desc"}); err != nil {
			t.Fatalf("grain list -sort state:asc,created:desc: %v", err)
		}
	})
	gadget, widget := strings.Index(out, "Gadget work"), strings.Index(out, "Widget work")
	if gadget < 0 || widget < 0 {
		t.Fatalf("printed %q, want both tasks", out)
	}
	// Proposed comes before queued in a task's life, and Gadget work is
	// the proposed one.
	if gadget > widget {
		t.Errorf("printed %q, want the proposed task before the queued one", out)
	}
}

// `grain defer` and `grain undefer` over the same wiring: the pair has
// to reach the daemon, move the task, and leave the approval alone --
// which is what makes `-state deferred` a listing anybody can ask for,
// and the undeferred task queued rather than proposed again.
func TestDeferAndUndeferMoveATaskAndBack(t *testing.T) {
	server := listTestServer(t)
	// "1" is the queued task listTestServer files first.
	if err := runCLIWithAccessCode([]string{"-server", server, "defer", "1"}); err != nil {
		t.Fatalf("grain defer 1: %v", err)
	}
	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "list", "-state", "deferred"}); err != nil {
			t.Fatalf("grain list -state deferred: %v", err)
		}
	})
	if !strings.Contains(out, "Widget work") || strings.Contains(out, "Gadget work") {
		t.Errorf("grain list -state deferred printed %q, want the deferred task and nothing else", out)
	}

	if err := runCLIWithAccessCode([]string{"-server", server, "undefer", "1"}); err != nil {
		t.Fatalf("grain undefer 1: %v", err)
	}
	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "list", "-state", "queued"}); err != nil {
			t.Fatalf("grain list -state queued: %v", err)
		}
	})
	if !strings.Contains(out, "Widget work") {
		t.Errorf("grain list -state queued printed %q, want the undeferred task back on the queue", out)
	}
}

func TestListRejectsAValueNobodyCanActOn(t *testing.T) {
	t.Parallel()
	server := listTestServer(t)
	for _, args := range [][]string{
		{"list", "-state", "in_progress"},
		{"list", "-origin", "robot"},
		{"list", "-sort", "newest-first"},
		// A condition the deployment could never answer stops the
		// command too, rather than being dropped the way the frontend
		// drops a clause out of a stale link.
		{"list", "-filter", "colour:any:blue"},
		{"list", "-filter", "state:any:in_progress"},
		{"list", "-filter", "repo:startswith:acme"},
		{"list", "-filter", "created:after:last tuesday"},
		{"list", "-filter", "repo"},
		{"list", "-sort", "created:sideways"},
		{"list", "-sort", "colour:asc"},
	} {
		if err := runCLIWithAccessCode(append([]string{"-server", server}, args...)); err == nil {
			t.Errorf("grain %s returned no error, want one naming the values that work", strings.Join(args, " "))
		}
	}
}
