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

// list_hidden_test.go covers what a listing leaves out (list.go's
// hideable/hiddenStates): closed and deferred tasks, which the frontend
// has always hidden and this command used to print.
//
// The rule is the frontend's, so the tests are written against the same
// three ways of overriding it: naming the state is asking to see it, the
// -closed/-deferred flags are the toolbar's checkboxes, and with neither
// the deployment's own showClosedByDefault decides.
package cli

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// hiddenTestServer files one task in each of the four states this is
// about -- queued, proposed, closed, deferred -- and serves them.
func hiddenTestServer(t *testing.T) (string, *ui.Client) {
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
	for _, title := range []string{"Live work", "Proposed work", "Closed work", "Deferred work"} {
		if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: title, Repo: "acme/widgets", Approved: title == "Live work",
		}); err != nil {
			t.Fatalf("creating %q: %v", title, err)
		}
	}
	if err := client.Close(ctx, "3", ui.CloseOptions{}); err != nil {
		t.Fatalf("closing task 3: %v", err)
	}
	if err := client.Defer(ctx, "4", ui.DeferOptions{}); err != nil {
		t.Fatalf("deferring task 4: %v", err)
	}
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)
	return srv.URL, client
}

func TestListHidesClosedAndDeferredTasksUnlessAsked(t *testing.T) {
	server, _ := hiddenTestServer(t)
	for _, tc := range []struct {
		name  string
		args  []string
		want  []string
		avoid []string
	}{
		{
			name:  "a plain listing is what is in play",
			args:  []string{"list"},
			want:  []string{"Live work", "Proposed work"},
			avoid: []string{"Closed work", "Deferred work"},
		},
		{
			name:  "-closed asks for the closed ones as well",
			args:  []string{"list", "-closed"},
			want:  []string{"Live work", "Closed work"},
			avoid: []string{"Deferred work"},
		},
		{
			name:  "-deferred asks for the deferred ones as well",
			args:  []string{"list", "-deferred"},
			want:  []string{"Live work", "Deferred work"},
			avoid: []string{"Closed work"},
		},
		{
			name: "both switches show both",
			args: []string{"list", "-closed", "-deferred"},
			want: []string{"Live work", "Closed work", "Deferred work"},
		},
		{
			name:  "naming a state is asking to see it",
			args:  []string{"list", "-state", "closed"},
			want:  []string{"Closed work"},
			avoid: []string{"Live work", "Deferred work"},
		},
		{
			name:  "and so is the condition that shorthand stands for",
			args:  []string{"list", "-filter", "state:any:deferred"},
			want:  []string{"Deferred work"},
			avoid: []string{"Live work", "Closed work"},
		},
		{
			name: "either of two hidden states, in one condition",
			args: []string{"list", "-filter", "state:any:closed|deferred"},
			want: []string{"Closed work", "Deferred work"},
		},
		// An explicit condition is the whole query, as it is in a URL.
		// Deployment defaults are not silently intersected underneath it.
		{
			name:  "an explicit exclusion replaces deployment defaults",
			args:  []string{"list", "-filter", "state:none:proposed"},
			want:  []string{"Live work", "Closed work", "Deferred work"},
			avoid: []string{"Proposed work"},
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

// The deployment's own answer, which is the setting behind the
// frontend's checkbox: with it on, a plain listing carries closed tasks
// here too -- and -closed=false is how one invocation says otherwise,
// which is the whole reason that flag is tri-state.
func TestListFollowsTheDeploymentOnClosedTasks(t *testing.T) {
	server, client := hiddenTestServer(t)
	// A first save has to carry the fields a deployment cannot run
	// without, which a daemon's own start would have seeded (loadConfig).
	on, poll, workers := true, "30s", 1
	host := "github.com"
	if _, err := client.UpdateSettings(context.Background(), ui.UpdateSettingsRequest{
		ShowClosedByDefault: &on,
		PollInterval:        &poll,
		MaxMergesPerRepo:    &workers,
		GitHubHost:          &host,
	}); err != nil {
		t.Fatalf("turning showClosedByDefault on: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "list"}); err != nil {
			t.Fatalf("grain list: %v", err)
		}
	})
	if !strings.Contains(out, "Closed work") {
		t.Errorf("grain list printed %q, want the closed task on a deployment that shows them", out)
	}
	// Deferred has no such setting -- on either side -- so it stays out.
	if strings.Contains(out, "Deferred work") {
		t.Errorf("grain list printed %q, want the deferred task still hidden", out)
	}

	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "list", "-closed=false"}); err != nil {
			t.Fatalf("grain list -closed=false: %v", err)
		}
	})
	if strings.Contains(out, "Closed work") {
		t.Errorf("grain list -closed=false printed %q, want the closed task left out", out)
	}
}

// What was hidden is said out loud, and said somewhere that does not
// spoil the listing: a note on stderr, so `grain list | wc -l` counts
// tasks and `-json` emits JSON and nothing else.
func TestListSaysWhatItLeftOutWithoutSpoilingTheListing(t *testing.T) {
	server, _ := hiddenTestServer(t)
	var out string
	note := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := runCLIWithAccessCode([]string{"-server", server, "list"}); err != nil {
				t.Fatalf("grain list: %v", err)
			}
		})
	})
	for _, want := range []string{"1 deferred", "1 closed", "-deferred", "-closed"} {
		if !strings.Contains(note, want) {
			t.Errorf("grain list said %q on stderr, want it to mention %q", note, want)
		}
	}
	if strings.Contains(out, "not shown") {
		t.Errorf("grain list printed %q, want the note kept off the listing itself", out)
	}

	note = captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := runCLIWithAccessCode([]string{"-server", server, "-json", "list"}); err != nil {
				t.Fatalf("grain -json list: %v", err)
			}
		})
	})
	if strings.TrimSpace(note) != "" {
		t.Errorf("grain -json list said %q on stderr, want a machine-readable answer to say nothing else", note)
	}
	if strings.Contains(out, "Closed work") || strings.Contains(out, "Deferred work") {
		t.Errorf("grain -json list printed %q, want the same tasks the table shows", out)
	}

	// Nothing hidden, nothing to say: a note about no tasks helps
	// nobody, and a deployment whose every task is in play should read
	// exactly as it did before this rule existed.
	note = captureStderr(t, func() {
		captureStdout(t, func() {
			if err := runCLIWithAccessCode([]string{"-server", server, "list", "-closed", "-deferred"}); err != nil {
				t.Fatalf("grain list -closed -deferred: %v", err)
			}
		})
	})
	if strings.TrimSpace(note) != "" {
		t.Errorf("grain list -closed -deferred said %q on stderr, want nothing left to report", note)
	}
}

func TestListOrdersClosedTasksNewestFirstByDefault(t *testing.T) {
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
	for _, title := range []string{"Open task 1", "Closed task 1", "Closed task 2", "Closed task 3"} {
		if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: title, Repo: "acme/widgets", Approved: true,
		}); err != nil {
			t.Fatalf("creating %q: %v", title, err)
		}
	}
	t1 := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	if err := store.Observe(ctx, model.Observation{TaskID: "2", ClosedAt: &t1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "3", ClosedAt: &t2}); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "4", ClosedAt: &t3}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)

	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", srv.URL, "list", "-state", "closed"}); err != nil {
			t.Fatalf("grain list -state closed: %v", err)
		}
	})
	i2 := strings.Index(out, "Closed task 2")
	i3 := strings.Index(out, "Closed task 3")
	i1 := strings.Index(out, "Closed task 1")
	if i2 == -1 || i3 == -1 || i1 == -1 || !(i2 < i3 && i3 < i1) {
		t.Errorf("grain list -state closed printed:\n%s\nwant order: Closed task 2, Closed task 3, Closed task 1", out)
	}
}

var captureStderrMu sync.Mutex

// captureStderr is captureStdout's twin, for the one thing this command
// writes there.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	captureStderrMu.Lock()
	defer captureStderrMu.Unlock()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()

	fn()

	w.Close()
	os.Stderr = old
	return <-done
}
