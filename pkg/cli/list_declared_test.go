// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain/task-99: list_custom_test.go covers what a custom attribute
// means; this covers the wiring around it against a real server -- that
// `grain list` asks the deployment which fields it declares when the
// command line names one, that the values reach the conditions, and that
// a name nothing declares stops the command rather than printing a
// listing narrowed by a question nobody answered.

// declaredFieldsServer is listTestServer with a deployment's own fields
// declared and filled in: a priority whose options are deliberately not
// in alphabetical order, and a customer.
//
// The declarations are written straight to the store, because that is
// the only way there is -- the set of fields is decided in the state
// repository and no endpoint writes one (model.CustomField).
func declaredFieldsServer(t *testing.T) string {
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
	for _, f := range []model.CustomField{
		{Name: "priority", Label: "Priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high", "critical"}},
		{Name: "customer", Label: "Customer", Type: model.CustomFieldText, Position: 1},
	} {
		if err := store.PutCustomField(ctx, f); err != nil {
			t.Fatalf("declaring %s: %v", f.Name, err)
		}
	}
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)
	for _, req := range []ui.CreateTaskRequest{
		{Title: "Widget work", Repo: "acme/widgets", Approved: true,
			CustomFields: map[string]ui.CustomFieldValue{"priority": {"low"}, "customer": {"Acme"}}},
		{Title: "Gadget work", Repo: "acme/gadgets", Approved: true,
			CustomFields: map[string]ui.CustomFieldValue{"priority": {"critical"}, "customer": {"Globex"}}},
		{Title: "Says nothing", Repo: "acme/widgets", Approved: true},
	} {
		if _, err := client.CreateTask(ctx, req); err != nil {
			t.Fatalf("filing %q: %v", req.Title, err)
		}
	}
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestListNarrowsByADeclaredField(t *testing.T) {
	server := declaredFieldsServer(t)
	for _, tc := range []struct {
		name  string
		args  []string
		want  []string
		avoid []string
	}{
		{
			name:  "one value",
			args:  []string{"list", "-filter", "custom.priority:any:critical"},
			want:  []string{"Gadget work"},
			avoid: []string{"Widget work", "Says nothing"},
		},
		// The clause the UI writes into a link, pasted after -filter
		// unaltered: several values joined by "|".
		{
			name:  "either of two, as a link spells it",
			args:  []string{"list", "-filter", "custom.priority:any:low|critical"},
			want:  []string{"Widget work", "Gadget work"},
			avoid: []string{"Says nothing"},
		},
		{
			name:  "nobody filled it in",
			args:  []string{"list", "-filter", "custom.priority:empty"},
			want:  []string{"Says nothing"},
			avoid: []string{"Widget work", "Gadget work"},
		},
		{
			name:  "text a value contains",
			args:  []string{"list", "-filter", "custom.customer:has:glob"},
			want:  []string{"Gadget work"},
			avoid: []string{"Widget work"},
		},
		{
			name:  "a declared field beside a built-in attribute",
			args:  []string{"list", "-repo", "acme/widgets", "-filter", "custom.priority:filled"},
			want:  []string{"Widget work"},
			avoid: []string{"Gadget work", "Says nothing"},
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

// An enum's options are its order, so "most urgent first" is the order
// the deployment declared rather than the alphabet -- which is the half
// of this a filter cannot stand in for.
func TestListSortsByADeclaredField(t *testing.T) {
	server := declaredFieldsServer(t)
	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "list", "-sort", "custom.priority:desc"}); err != nil {
			t.Fatalf("grain list -sort custom.priority:desc: %v", err)
		}
	})
	critical, low := strings.Index(out, "Gadget work"), strings.Index(out, "Widget work")
	nothing := strings.Index(out, "Says nothing")
	if critical < 0 || low < 0 || nothing < 0 {
		t.Fatalf("printed %q, want all three tasks", out)
	}
	if critical > low {
		t.Errorf("printed %q, want the critical task before the low one", out)
	}
	// The task nobody filled the field in on goes last, whichever way
	// the order points.
	if nothing < low {
		t.Errorf("printed %q, want the task with no priority last", out)
	}
}

// A name nothing declares is refused, and refused with the attributes
// this deployment actually has -- which is the one thing that tells
// somebody the field is called custom.priority.
func TestListRefusesAFieldNobodyDeclared(t *testing.T) {
	t.Parallel()
	server := declaredFieldsServer(t)
	err := runCLIWithAccessCode([]string{"-server", server, "list", "-filter", "custom.prio:any:low"})
	if err == nil {
		t.Fatal("grain list -filter custom.prio:any:low returned no error, want one naming the attributes that work")
	}
	if !strings.Contains(err.Error(), "custom.priority") {
		t.Errorf("grain list -filter custom.prio:any:low = %v, want it to name custom.priority", err)
	}
	// And a value the field does not declare is refused with the ones it
	// does, the same way a misspelt -state is.
	err = runCLIWithAccessCode([]string{"-server", server, "list", "-filter", "custom.priority:any:urgent"})
	if err == nil || !strings.Contains(err.Error(), "low, high, critical") {
		t.Errorf("grain list -filter custom.priority:any:urgent = %v, want an error naming the declared options", err)
	}
}

// `grain list -h` is where somebody finds out this deployment has a
// custom.priority at all: the built-in vocabulary is printed from the
// table, and the declared half is asked for.
func TestListHelpNamesTheDeclaredFields(t *testing.T) {
	server := declaredFieldsServer(t)
	out := captureStderr(t, func() {
		// -h is a usage request, which flag.FlagSet reports as an error;
		// what matters is what was printed.
		_ = runCLIWithAccessCode([]string{"-server", server, "list", "-h"})
	})
	for _, want := range []string{"custom.priority", "Priority", "custom.customer", "repo"} {
		if !strings.Contains(out, want) {
			t.Errorf("grain list -h printed %q, want it to mention %q", out, want)
		}
	}
}
