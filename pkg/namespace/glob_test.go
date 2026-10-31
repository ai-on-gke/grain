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

package namespace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// manyTasksServer serves task:// with tasks 1..n, more of them than
// maxWalkFiles when n is.
func manyTasksServer(n int) *Server {
	tasks := make(map[string]*TaskView, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprint(i)
		tasks[id] = &TaskView{ID: id, Record: "Task " + id + "\n"}
	}
	return &Server{Tasks: stubTaskProvider{tasks: tasks}}
}

func lines(body string) []string {
	body = strings.TrimSuffix(body, "\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

func TestGlobWithoutSlashMatchesDirectChildrenPastTheWalkCap(t *testing.T) {
	t.Parallel()

	const n = maxWalkFiles + 100
	srv := manyTasksServer(n)
	scope := Scope{MayReadAllTasks: true}

	// 19, 190..199 and 1900..1999, in numeric order, none of them lost to
	// a walk that never reached them.
	var want []string
	want = append(want, "19/")
	for i := 190; i <= 199; i++ {
		want = append(want, fmt.Sprintf("%d/", i))
	}
	for i := 1900; i <= 1999; i++ {
		want = append(want, fmt.Sprintf("%d/", i))
	}
	for _, pattern := range []string{"19*", "19*/"} {
		rec := serveRequest(srv, scope, http.MethodGet, "/fs/task/?glob="+pattern, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("glob %q: status=%d body=%q", pattern, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Grain-Truncated"); got != "" {
			t.Errorf("glob %q: X-Grain-Truncated = %q, want empty", pattern, got)
		}
		got := lines(rec.Body.String())
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("glob %q = %d lines %v..., want %d lines %v...", pattern, len(got), head(got), len(want), head(want))
		}
	}
}

func head(s []string) []string {
	if len(s) > 5 {
		return s[:5]
	}
	return s
}

func TestTaskRootListsIDsNumerically(t *testing.T) {
	t.Parallel()

	srv := manyTasksServer(1000)
	srv.Tasks.(stubTaskProvider).tasks["draft"] = &TaskView{ID: "draft"}
	rec := serveRequest(srv, Scope{MayReadAllTasks: true}, http.MethodGet, "/fs/task/?list", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%q", rec.Code, rec.Body.String())
	}
	got := lines(rec.Body.String())
	if len(got) != 1001 {
		t.Fatalf("listed %d entries, want 1001", len(got))
	}
	if got[0] != "1/" || got[1] != "2/" || got[9] != "10/" || got[999] != "1000/" || got[1000] != "draft/" {
		t.Errorf("listing order = %v ... %v, want 1/ 2/ ... 1000/ draft/", got[:10], got[995:])
	}
}

func TestRecursiveGlobSaysWhenTheWalkCapCutItShort(t *testing.T) {
	t.Parallel()

	srv := manyTasksServer(maxWalkFiles + 100)
	scope := Scope{MayReadAllTasks: true}

	rec := serveRequest(srv, scope, http.MethodGet, "/fs/task/?glob=**/no-such-file", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Grain-Truncated"); got != "true" {
		t.Errorf("X-Grain-Truncated = %q, want true", got)
	}
	want := fmt.Sprintf("[truncated: walked %d entries; narrow the path]\n", maxWalkFiles)
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	rec = serveRequest(srv, scope, http.MethodGet, "/fs/task/?glob=**/no-such-file", map[string]string{"Accept": "application/json"})
	var payload struct {
		Matches   []string `json:"matches"`
		Truncated bool     `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	if !payload.Truncated || len(payload.Matches) != 0 {
		t.Errorf("JSON = %+v, want truncated with no matches", payload)
	}
}

func TestGlobSlashSuffixAndRecursion(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	for _, f := range []string{"pkg/other.go", "pkg/sub/many.txt", "pkg/sub/deeper/x.txt", "top.txt"} {
		full := filepath.Join(srcDir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := newTestServer(t, ci.NewRunStore(5), srcDir, nil)
	scope := Scope{MayReadSrc: true}

	for _, tc := range []struct {
		uri, pattern string
		want         []string
	}{
		// No "/" and no "**": the directory's own entries only.
		{"pkg", "*", []string{"other.go", "sub/"}},
		{"", "*.txt", []string{"top.txt"}},
		// A trailing "/" is directories only, at any depth it reaches.
		{"pkg", "*/", []string{"sub/"}},
		{"pkg", "s*/", []string{"sub/"}},
		{"pkg", "o*/", nil},
		{"pkg", "**/", []string{"sub/", "sub/deeper/"}},
		// "/" or "**" walks the subtree.
		{"", "**/*.txt", []string{"pkg/sub/deeper/x.txt", "pkg/sub/many.txt", "top.txt"}},
		{"pkg", "sub/*.txt", []string{"sub/many.txt"}},
	} {
		rec := serveRequest(srv, scope, http.MethodGet, "/fs/src/"+tc.uri+"?glob="+tc.pattern, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("src://%s glob %q: status=%d body=%q", tc.uri, tc.pattern, rec.Code, rec.Body.String())
		}
		if got := lines(rec.Body.String()); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("src://%s glob %q = %q, want %q", tc.uri, tc.pattern, got, tc.want)
		}
	}
}

func TestCIRepoListingSaysWhenRunsAreCapped(t *testing.T) {
	t.Parallel()

	const runs = maxCIRunsListed + 10
	store := ci.NewRunStore(runs + 10)
	base := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	for i := 0; i < runs; i++ {
		started := base.Add(time.Duration(i) * time.Minute)
		store.Put(ci.Run{
			ID:        fmt.Sprintf("run-%04d", i),
			Repo:      "bwsalmon/grain",
			Ref:       "main",
			StartedAt: &started,
		})
	}
	srv := newTestServer(t, store, "", nil)
	scope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true}

	rec := serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain?list", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	got := lines(rec.Body.String())
	if len(got) != maxCIRunsListed+1 {
		t.Fatalf("listed %d lines, want %d runs and a truncation line", len(got), maxCIRunsListed)
	}
	wantLast := fmt.Sprintf("[truncated: listed the newest %d of %d runs; use grain ci list to filter the rest]", maxCIRunsListed, runs)
	if last := got[len(got)-1]; last != wantLast {
		t.Errorf("last line = %q, want %q", last, wantLast)
	}
	if h := rec.Header().Get("X-Grain-Truncated"); h != "true" {
		t.Errorf("X-Grain-Truncated = %q, want true", h)
	}

	rec = serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain?list", map[string]string{"Accept": "application/json"})
	var payload struct {
		Entries   []Entry `json:"entries"`
		Truncated bool    `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !payload.Truncated || len(payload.Entries) != maxCIRunsListed {
		t.Errorf("JSON truncated=%v entries=%d, want true and %d", payload.Truncated, len(payload.Entries), maxCIRunsListed)
	}

	// A glob over the same directory carries the listing's cap with it.
	rec = serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain?glob=run-*", nil)
	if got := lines(rec.Body.String()); len(got) != maxCIRunsListed+1 || got[len(got)-1] != wantLast {
		t.Errorf("glob listed %d lines ending %q, want %d ending %q", len(got), got[len(got)-1], maxCIRunsListed+1, wantLast)
	}
}

func TestGrepSaysWhenTheWalkCapCutItShort(t *testing.T) {
	t.Parallel()

	srv := manyTasksServer(maxWalkFiles + 100)
	rec := serveRequest(srv, Scope{MayReadAllTasks: true}, http.MethodGet, "/fs/task/?grep=no-such-text", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Grain-Truncated"); got != "true" {
		t.Errorf("X-Grain-Truncated = %q, want true", got)
	}
	want := fmt.Sprintf("[truncated: walked %d entries; narrow the path]\n", maxWalkFiles)
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
