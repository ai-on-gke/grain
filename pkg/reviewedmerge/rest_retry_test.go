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

package reviewedmerge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// flakyForge is a GitHub that fails each path's first failures[path]
// requests with status, then answers from answers.
type flakyForge struct {
	mu       sync.Mutex
	status   int
	failures map[string]int
	answers  map[string]string
	asked    map[string]int
}

func (f *flakyForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked[r.URL.Path]++
	if f.asked[r.URL.Path] <= f.failures[r.URL.Path] {
		http.Error(w, http.StatusText(f.status), f.status)
		return
	}
	body, ok := f.answers[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	fmt.Fprint(w, body)
}

func newFlakyForge(t *testing.T, status int, failures map[string]int, answers map[string]string) (*flakyForge, REST) {
	t.Helper()
	shortRESTBackoff(t)
	f := &flakyForge{status: status, failures: failures, answers: answers, asked: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, REST{BaseURL: srv.URL}
}

const (
	listPath   = "/repos/example/grain/commits/abc/pulls"
	detailPath = "/repos/example/grain/pulls/7"
	mergedList = `[{"number":7,"state":"closed","merged_at":"2026-11-10T00:00:00Z","merge_commit_sha":"abc","base":{"ref":"main"},"head":{"sha":"h7"},"user":{"login":"alice"}}]`
)

func TestAForgeThatFailsTwiceThenAnswersPasses(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f, rest := newFlakyForge(t, status,
				map[string]int{listPath: 2, detailPath: 2},
				map[string]string{listPath: mergedList, detailPath: `{"merged_by":{"login":"grain-bot[bot]"}}`})
			m, err := policy.Check(context.Background(), rest, "abc")
			if err != nil {
				t.Fatal(err)
			}
			if m.Via != "grain" || m.Pull != 7 {
				t.Fatalf("merge = %+v", m)
			}
			if f.asked[listPath] != 3 || f.asked[detailPath] != 3 {
				t.Fatalf("asked %v, want each path three times", f.asked)
			}
		})
	}
}

func TestAPushedCommitIsStillRefusedAfterRetrying(t *testing.T) {
	_, rest := newFlakyForge(t, http.StatusServiceUnavailable,
		map[string]int{listPath: 2},
		map[string]string{listPath: `[]`})
	_, err := policy.Check(context.Background(), rest, "abc")
	if err == nil || !strings.Contains(err.Error(), "pushed onto the branch") {
		t.Fatalf("err = %v, want the pushed-commit refusal", err)
	}
	if IsForgeError(err) {
		t.Fatalf("a refusal was reported as a forge error: %v", err)
	}
}

func TestAnUnreviewedMergeIsStillRefusedAfterRetrying(t *testing.T) {
	_, rest := newFlakyForge(t, http.StatusServiceUnavailable,
		map[string]int{listPath: 2, detailPath: 1, detailPath + "/reviews": 2},
		map[string]string{listPath: mergedList, detailPath: `{"merged_by":{"login":"alice"}}`, detailPath + "/reviews": `[]`})
	_, err := policy.Check(context.Background(), rest, "abc")
	if err == nil || !strings.Contains(err.Error(), "did not reach main through a reviewed merge") || IsForgeError(err) {
		t.Fatalf("err = %v, want the policy's refusal", err)
	}
}

func TestAForgeThatKeepsFailingIsAForgeErrorNotARefusal(t *testing.T) {
	f, rest := newFlakyForge(t, http.StatusServiceUnavailable,
		map[string]int{listPath: 100},
		map[string]string{listPath: mergedList})
	_, err := policy.CheckChain(context.Background(), rest, "abc", "", 10)
	if !IsForgeError(err) {
		t.Fatalf("err = %v, want a ForgeError", err)
	}
	for _, want := range []string{"could not read the pull requests for abc", "after 5 attempts", "503 Service Unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to say %q", err, want)
		}
	}
	if f.asked[listPath] != restAttempts {
		t.Fatalf("asked %d times, want %d", f.asked[listPath], restAttempts)
	}
}

func TestAClientErrorIsNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f, rest := newFlakyForge(t, status, map[string]int{listPath: 100}, map[string]string{listPath: mergedList})
			_, err := policy.Check(context.Background(), rest, "abc")
			if !IsForgeError(err) || strings.Contains(err.Error(), "attempts") {
				t.Fatalf("err = %v", err)
			}
			if f.asked[listPath] != 1 {
				t.Fatalf("asked %d times, want once", f.asked[listPath])
			}
		})
	}
}

func TestANetworkErrorIsRetried(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls <= 2 {
			return nil, errors.New("connection reset by peer")
		}
		rec := httptest.NewRecorder()
		switch r.URL.Path {
		case listPath:
			fmt.Fprint(rec, mergedList)
		case detailPath:
			fmt.Fprint(rec, `{"merged_by":{"login":"grain-bot[bot]"}}`)
		}
		return rec.Result(), nil
	})}
	shortRESTBackoff(t)
	rest := REST{BaseURL: "https://forge.example", Client: client}
	if _, err := policy.Check(context.Background(), rest, "abc"); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("%d requests, want 4 (two resets, the list, the pull request)", calls)
	}
}

func TestRetryingStopsWhenTheContextIsDone(t *testing.T) {
	f, rest := newFlakyForge(t, http.StatusServiceUnavailable, map[string]int{listPath: 100}, map[string]string{listPath: mergedList})
	restBackoff = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := policy.Check(ctx, rest, "abc")
	if !IsForgeError(err) || !strings.Contains(err.Error(), "after 1 attempt:") {
		t.Fatalf("err = %v", err)
	}
	if f.asked[listPath] != 1 {
		t.Fatalf("asked %d times, want once", f.asked[listPath])
	}
}
