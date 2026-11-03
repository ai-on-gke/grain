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

package hostd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A hostd refused for its token is logged on the daemon's side too, at
// most once a minute however often it retries, with how many went
// unlogged in between.
func TestRemoteLogsRefusedTokensRateLimited(t *testing.T) {
	var lines []string
	clock := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	r := &Remote{
		Token: "right",
		Logf:  func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
		now:   func() time.Time { return clock },
	}
	refuse := func(token string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, RemotePath, nil)
		req.RemoteAddr = "10.0.0.7:5555"
		req.Header.Set(HostHeader, "workstation")
		if token != "" {
			req.Header.Set(TokenHeader, token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("ServeHTTP with token %q: got %d, want 401", token, w.Code)
		}
	}

	refuse("wrong")
	if len(lines) != 1 {
		t.Fatalf("after one refusal, logged %q; want one line", lines)
	}
	want := "grain daemon: refused a grain hostd on workstation connecting from 10.0.0.7:5555: it presented a token that is not a hostd token here"
	if !strings.HasPrefix(lines[0], want) {
		t.Errorf("logged %q; want it to start %q", lines[0], want)
	}

	for range 5 {
		clock = clock.Add(5 * time.Second)
		refuse("wrong")
	}
	if len(lines) != 1 {
		t.Fatalf("retries inside a minute logged %q; want only the first line", lines)
	}

	clock = clock.Add(time.Minute)
	refuse("")
	if len(lines) != 2 {
		t.Fatalf("a refusal a minute on logged %q; want a second line", lines)
	}
	if want := "it presented no hostd token (5 more refused since the last line like this)"; !strings.HasSuffix(lines[1], want) {
		t.Errorf("logged %q; want it to end %q", lines[1], want)
	}
}

// A hostd retrying a stale token every few seconds does not hide a token
// presented under another hostd's name: each kind of refusal has its own
// line a minute (walkthrough 2026-11-03-hostd-registry, F2).
func TestRemoteLogsANameMismatchBesideARetryingStaleToken(t *testing.T) {
	var lines []string
	clock := time.Date(2026, 11, 3, 8, 47, 0, 0, time.UTC)
	r := &Remote{
		Resolve: func(_ context.Context, token string) (string, string, bool, error) {
			return "ws-a", "", token == "ws-a-token", nil
		},
		Logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
		now:  func() time.Time { return clock },
	}
	dial := func(token, name string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, RemotePath, nil)
		req.RemoteAddr = "127.0.0.1:5555"
		req.Header.Set(TokenHeader, token)
		req.Header.Set(NameHeader, name)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("ServeHTTP with token %q as %q: got %d, want %d", token, name, w.Code, want)
		}
	}
	dial("rotated-away", "ws-a", http.StatusUnauthorized)
	clock = clock.Add(2 * time.Second)
	dial("rotated-away", "ws-a", http.StatusUnauthorized)
	clock = clock.Add(2 * time.Second)
	dial("ws-a-token", "ws-b", http.StatusForbidden)
	if len(lines) != 2 {
		t.Fatalf("logged %q; want the stale token's first line and the name mismatch's", lines)
	}
	if want := `it calls itself hostd "ws-b", but its token is hostd "ws-a"'s`; !strings.HasSuffix(lines[1], want) {
		t.Errorf("logged %q; want it to end %q", lines[1], want)
	}
	clock = clock.Add(2 * time.Second)
	dial("ws-a-token", "ws-b", http.StatusForbidden)
	if len(lines) != 2 {
		t.Fatalf("the same mismatch again inside a minute logged %q; want no new line", lines)
	}
}

// A registry hostd named as the shared token's hostd is would share its
// session slot, and work pinned to that name skips the registry's
// revocation check: its token is refused instead.
func TestRegistryHostdMayNotTakeTheSharedTokensName(t *testing.T) {
	r := &Remote{Token: "shared", SharedName: "edge",
		Resolve: func(_ context.Context, token string) (string, string, bool, error) {
			return map[string]string{"t-edge": "edge", "t-other": "other"}[token], "", token == "t-edge" || token == "t-other", nil
		}}
	if name, _, ok, err := r.Identify(context.Background(), "shared"); !ok || err != nil || name != "edge" {
		t.Fatalf("shared token = %q %v %v, want edge", name, ok, err)
	}
	if name, _, ok, err := r.Identify(context.Background(), "t-edge"); ok || err == nil {
		t.Fatalf("registry token for the shared name = %q %v %v, want refused", name, ok, err)
	}
	if name, _, ok, err := r.Identify(context.Background(), "t-other"); !ok || err != nil || name != "other" {
		t.Fatalf("other registry token = %q %v %v, want other", name, ok, err)
	}
	if got := r.SharedTokenName(); got != "edge" {
		t.Fatalf("SharedTokenName = %q, want edge", got)
	}
	if got := (&Remote{}).SharedTokenName(); got != "" {
		t.Fatalf("SharedTokenName with no shared token = %q, want empty", got)
	}
}
