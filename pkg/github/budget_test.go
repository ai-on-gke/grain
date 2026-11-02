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

package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Two labelled clients over one transport: each read is charged to the
// caller that made it and to the kind of read it was, a 304 is counted
// as free, and the label never reaches GitHub -- nor splits the ETag
// cache, so the second caller's read of the same resource revalidates
// against the first one's answer.
func TestEachCallerIsChargedForItsOwnReads(t *testing.T) {
	var (
		mu       sync.Mutex
		leaked   []string
		served   int
		notMod   int
		etag     = `W/"pr"`
		bodyJSON = `{"number":7,"head":{"sha":"abc"},"base":{"ref":"main"}}`
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if v := r.Header.Get(CallerHeader); v != "" {
			leaked = append(leaked, v)
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(4900-served))
		served++
		if r.Header.Get("If-None-Match") == etag {
			notMod++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write([]byte(bodyJSON))
	}))
	t.Cleanup(server.Close)
	l := &callerLedger{logf: func(string, ...any) {}}
	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), Conditional: true, ledger: l}
	base := NewClient(transport, nil)

	syncClient := WithCaller(base, "sync")
	uiClient := WithCaller(base, "ui")
	for _, c := range []Client{syncClient, syncClient, uiClient} {
		if _, err := c.GetPullRequest("acme", "widgets", 7); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := base.GetPullRequest("acme", "widgets", 7); err != nil {
		t.Fatal(err)
	}

	if len(leaked) != 0 {
		t.Errorf("the caller label reached GitHub: %v", leaked)
	}
	if notMod != 3 {
		t.Errorf("304s = %d, want 3 -- the label must not split the ETag cache", notMod)
	}
	got := l.callerStats()
	want := map[string]CallerStats{
		"sync":       {Sent: 2, Revalidated: 1, Kinds: map[string]KindStats{"PR read": {Sent: 2, Revalidated: 1}}},
		"ui":         {Sent: 1, Revalidated: 1, Kinds: map[string]KindStats{"PR read": {Sent: 1, Revalidated: 1}}},
		Unattributed: {Sent: 1, Revalidated: 1, Kinds: map[string]KindStats{"PR read": {Sent: 1, Revalidated: 1}}},
	}
	for name, w := range want {
		g := got[name]
		if g.Sent != w.Sent || g.Revalidated != w.Revalidated || fmt.Sprint(g.Kinds) != fmt.Sprint(w.Kinds) {
			t.Errorf("caller %q = %+v, want %+v", name, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("callers = %v, want exactly %v", got, want)
	}
	// And the transport's own Stats carry the budget it heard.
	if b := l.budget(time.Now()); b.Limit != 5000 || b.Remaining != 4897 {
		t.Errorf("budget = %+v, want 4897 of 5000", b)
	}
}

// A wrapper client passes the label through without losing its own type.
type labelledWrapper struct{ Client }

func (w labelledWrapper) WithCaller(caller string) Client {
	return labelledWrapper{WithCaller(w.Client, caller)}
}

func TestWithCallerKeepsAWrappersType(t *testing.T) {
	inner := NewClient(NewFakeTransport(), nil)
	got := WithCaller(labelledWrapper{inner}, "sync")
	w, ok := got.(labelledWrapper)
	if !ok {
		t.Fatalf("WithCaller returned %T, want the wrapper", got)
	}
	if rc := w.Client.(*RESTClient); rc.Caller != "sync" || inner.Caller != "" {
		t.Errorf("inner caller = %q, original = %q; want sync and untouched", rc.Caller, inner.Caller)
	}
	fake := &FakeTransport{}
	var plain Client = fakeOnly{}
	if WithCaller(plain, "x") != plain {
		t.Error("a client with no transport to charge should come back unchanged")
	}
	_ = fake
}

type fakeOnly struct{ Client }

// The projection: 1,000 calls spent over ten minutes is 6,000 an hour,
// so 4,000 left runs out in forty minutes -- before a reset fifty
// minutes away, so the snapshot says when.
func TestBudgetProjectsWhenTheHourRunsOut(t *testing.T) {
	l := &callerLedger{logf: func(string, ...any) {}}
	start := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)
	reset := start.Add(time.Hour)
	headers := func(remaining int) map[string]string {
		return map[string]string{"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": strconv.Itoa(remaining),
			"X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)}
	}
	for i := 0; i <= 10; i++ {
		l.noteLimit("token a", headers(5000-100*i), start.Add(time.Duration(i)*time.Minute))
	}
	now := start.Add(10 * time.Minute)
	b := l.budget(now)
	if b.Remaining != 4000 || b.Limit != 5000 {
		t.Fatalf("budget = %+v", b)
	}
	if b.SpendPerHour < 5999 || b.SpendPerHour > 6001 {
		t.Errorf("spend = %.1f/h, want 6000", b.SpendPerHour)
	}
	if want := now.Add(40 * time.Minute); !b.ExhaustedAt.Equal(want) {
		t.Errorf("exhausted at %v, want %v", b.ExhaustedAt, want)
	}

	// The same spend with the reset sooner than that lasts the hour.
	l2 := &callerLedger{logf: func(string, ...any) {}}
	reset = start.Add(20 * time.Minute)
	for i := 0; i <= 10; i++ {
		l2.noteLimit("token a", headers(5000-100*i), start.Add(time.Duration(i)*time.Minute))
	}
	if b := l2.budget(now); !b.ExhaustedAt.IsZero() || b.SpendPerHour == 0 {
		t.Errorf("budget = %+v, want a spend rate and no exhaustion before the reset", b)
	}
}

// The tightest credential is the one reported, and a new hour (a reset
// that moved) is not read as negative spend.
func TestBudgetReportsTheTightestCredentialAndStartsAfreshEachHour(t *testing.T) {
	l := &callerLedger{logf: func(string, ...any) {}}
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)
	h := func(remaining int, reset time.Time) map[string]string {
		return map[string]string{"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": strconv.Itoa(remaining),
			"X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)}
	}
	l.noteLimit("token a", h(4500, now.Add(time.Hour)), now)
	l.noteLimit("token b", h(1200, now.Add(time.Hour)), now)
	if b := l.budget(now); b.Remaining != 1200 || b.Credentials != 2 {
		t.Errorf("budget = %+v, want token b's 1200 of two credentials", b)
	}
	l.noteLimit("token b", h(100, now.Add(time.Hour)), now.Add(time.Minute))
	l.noteLimit("token b", h(5000, now.Add(2*time.Hour)), now.Add(2*time.Minute))
	if b := l.budget(now.Add(2 * time.Minute)); b.SpendPerHour != 0 || b.Remaining != 4500 {
		t.Errorf("budget after the reset = %+v, want token a's 4500 and no spend rate yet", b)
	}
}

// Low is under 15%, said once when it starts and once when it ends.
func TestLowBudgetIsLoggedOncePerChange(t *testing.T) {
	var said []string
	l := &callerLedger{logf: func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) }}
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)
	note := func(remaining int) {
		now = now.Add(10 * time.Second)
		l.noteLimit("token a", map[string]string{"X-RateLimit-Limit": "5000",
			"X-RateLimit-Remaining": strconv.Itoa(remaining)}, now)
	}
	for _, r := range []int{900, 800, 749, 700, 600, 1000, 1100} {
		note(r)
	}
	if len(said) != 2 || !strings.Contains(said[0], "749 of 5000") || !strings.Contains(said[1], "back to normal") {
		t.Errorf("said %q, want one line going low and one recovering", said)
	}
	if l.low(now) {
		t.Error("still low after recovering")
	}
	note(10)
	if !l.low(now) {
		t.Error("not low at 10 of 5000")
	}
	if l.low(now.Add(budgetStale)) {
		t.Error("a credential nothing has heard from in a quarter of an hour is still holding polls back")
	}
	// GraphQL's budget is its own and does not move the core one.
	l.noteLimit("token a", map[string]string{"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": "4999",
		"X-RateLimit-Resource": "graphql"}, now)
	if !l.low(now) {
		t.Error("a GraphQL remainder lifted the core budget's low state")
	}
}

func TestKindOfNamesTheReadsTheIncidentNamed(t *testing.T) {
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/repos/acme/widgets/branches/main", "head read"},
		{"GET", "/repos/acme/widgets/pulls/12", "PR read"},
		{"GET", "/repos/acme/widgets/pulls/12/comments?per_page=100", "review comments"},
		{"GET", "/repos/acme/widgets/commits/deadbeef/check-runs?per_page=100", "checks"},
		{"GET", "/repos/acme/widgets/actions/runs?head_sha=abc", "checks"},
		{"GET", "/repos/acme/widgets/actions/runs/42/jobs?per_page=100", "checks"},
		{"GET", "/repos/acme/widgets/actions/jobs/77/logs", "job logs"},
		{"GET", "/repos/acme/widgets/actions/runs/42/artifacts?per_page=100", "artifacts"},
		{"GET", "/repos/acme/widgets/compare/abc1234...def5678", "compare"},
		{"GET", "/repos/acme/widgets/issues/12/comments", "comments"},
		{"GET", "/repos/acme/widgets", "repo read"},
		{"PUT", "/repos/acme/widgets/pulls/12/merge", "write"},
		{"POST", "/graphql", "write"},
	} {
		if got := kindOf(c.method, endpointOf(c.path)); got != c.want {
			t.Errorf("kindOf(%s %s) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}
