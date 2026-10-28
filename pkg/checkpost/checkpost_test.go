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

package checkpost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSecret = "shhh"

// sha is a full object name of the shape Validate insists on, varied by
// a single character so a test can talk about two different commits.
func sha(last byte) string { return strings.Repeat("a", 39) + string(last) }

func post(t *testing.T, srv *Server, v Verdict, at time.Time) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/verdicts", bytes.NewReader(body))
	req.Header.Set(TimestampHeader, fmt.Sprint(at.Unix()))
	req.Header.Set(SignatureHeader, Sign(testSecret, at, http.MethodPost, "/v1/verdicts", body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func get(t *testing.T, srv *Server, repo, commit string, at time.Time) []Verdict {
	t.Helper()
	path := "/v1/verdicts/" + repo + "/" + commit
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(TimestampHeader, fmt.Sprint(at.Unix()))
	req.Header.Set(SignatureHeader, Sign(testSecret, at, http.MethodGet, path, nil))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	var out []Verdict
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v", w.Body, err)
	}
	return out
}

// testClock is the instant these tests freeze, and it is today's rather
// than a date typed into this file.
//
// A fixed calendar date is what wedged this package once already: the
// tests stamped verdicts a week before the day the suite happened to
// run, reopened the Store, and found load had dropped the lot as past
// Retention -- failing every day thereafter, and pointing at durability
// bugs that were not there. Two things keep that from coming back, and
// either would do: the instant is taken from the real clock, so no date
// here can go stale; and every reopen goes through openStore, so the
// frozen clock reaches load as well and the age of a line is measured
// against the instant it was written at rather than the real one.
//
// Truncated to the second because the stamp a verdict carries is
// compared against this one, and rounded off in JSON.
func testClock() time.Time {
	return time.Now().UTC().Truncate(time.Second)
}

func newTestServer(t *testing.T, path string) (*Server, *Store, *time.Time) {
	t.Helper()
	clock := testClock()
	// Opened through the frozen clock rather than adopting it afterwards,
	// so the load this store does on the way up is measured from the same
	// instant its verdicts are stamped at.
	st, err := openStore(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := NewServer(st, testSecret)
	srv.now = func() time.Time { return clock }
	return srv, st, &clock
}

// The whole point, end to end: a build reports once, and the thing that
// gates on it reads the answer back.
func TestAVerdictReportedIsAVerdictReadBack(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	v := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "success",
		URL: "https://github.com/acme/widgets/actions/runs/1"}

	if w := post(t, srv, v, *clock); w.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", w.Code, w.Body)
	}
	got := get(t, srv, "acme/widgets", sha('1'), *clock)
	if len(got) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(got))
	}
	if got[0].Name != "go" || got[0].Conclusion != "success" || got[0].URL != v.URL {
		t.Errorf("read back %+v", got[0])
	}
	if !got[0].ReportedAt.Equal(*clock) {
		t.Errorf("ReportedAt = %v, want the server's clock %v", got[0].ReportedAt, *clock)
	}
}

// A commit nobody has reported on is silence, not an error: the reader
// has to be able to tell "nothing yet" from "something went wrong", and
// a 404 would invite it to retry the former.
func TestACommitNobodyHasReportedOnIsAnEmptyList(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	if got := get(t, srv, "acme/widgets", sha('9'), *clock); len(got) != 0 {
		t.Errorf("got %d verdicts for a commit nobody reported on", len(got))
	}
}

// A re-run reports the same name about the same commit again, from a
// later attempt, and that answer is the true one -- the same rule GitHub
// applies to a re-run check.
func TestAReRunReplacesItsOwnEarlierAnswer(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	red := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "failure", Attempt: 1}
	post(t, srv, red, *clock)
	green := red
	green.Conclusion = "success"
	green.Attempt = 2
	if w := post(t, srv, green, *clock); w.Code != http.StatusNoContent {
		t.Fatalf("re-run report: %d %s", w.Code, w.Body)
	}

	got := get(t, srv, "acme/widgets", sha('1'), *clock)
	if len(got) != 1 {
		t.Fatalf("got %d verdicts, want the re-run to have replaced the first", len(got))
	}
	if got[0].Conclusion != "success" || got[0].Attempt != 2 {
		t.Errorf("held %+v, want the re-run's answer", got[0])
	}
}

// #2401: a success for the same commit and check, from no later attempt
// than the failure it contradicts, does not erase that failure. It is
// refused with 409, so the reporter's job fails visibly.
func TestALaterSuccessDoesNotOverwriteAFailureFromTheSameAttempt(t *testing.T) {
	for _, attempt := range []int{0, 1} {
		t.Run(fmt.Sprint("attempt ", attempt), func(t *testing.T) {
			srv, _, clock := newTestServer(t, "")
			red := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "failure", Attempt: attempt}
			post(t, srv, red, *clock)
			green := red
			green.Conclusion = "success"
			w := post(t, srv, green, *clock)
			if w.Code != http.StatusConflict {
				t.Fatalf("success over a failure: %d %s, want 409", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), "later run attempt") {
				t.Errorf("refusal %q does not say what would clear it", w.Body)
			}
			got := get(t, srv, "acme/widgets", sha('1'), *clock)
			if len(got) != 1 || got[0].Conclusion != "failure" {
				t.Errorf("held %+v, want the failure to stand", got)
			}
		})
	}
}

// supersedes, the whole table: a later attempt always replaces, an
// earlier one never does, and within one attempt a failure outranks a
// pass.
func TestSupersedesOrdersByAttemptThenSeverity(t *testing.T) {
	v := func(conclusion string, attempt int) Verdict {
		return Verdict{Conclusion: conclusion, Attempt: attempt}
	}
	for _, c := range []struct {
		next, held Verdict
		want       bool
	}{
		{v("success", 2), v("failure", 1), true},
		{v("failure", 2), v("success", 1), true},
		{v("success", 1), v("failure", 0), true},
		{v("success", 1), v("failure", 2), false},
		{v("failure", 1), v("success", 2), false},
		{v("success", 1), v("failure", 1), false},
		{v("skipped", 0), v("timed_out", 0), false},
		{v("neutral", 0), v("cancelled", 0), false},
		{v("failure", 1), v("success", 1), true},
		{v("cancelled", 1), v("failure", 1), true},
		{v("success", 1), v("success", 1), true},
		{v("success", 1), v("skipped", 1), true},
	} {
		if got := supersedes(c.next, c.held); got != c.want {
			t.Errorf("supersedes(%s#%d over %s#%d) = %v, want %v",
				c.next.Conclusion, c.next.Attempt, c.held.Conclusion, c.held.Attempt, got, c.want)
		}
	}
}

// A refused report is recorded nowhere, so a restart reloads the failure
// rather than the success that was turned away.
func TestARefusedReportIsNotWrittenToTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	_, st, clock := newTestServer(t, path)
	red := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "failure"}
	if err := st.Put(red); err != nil {
		t.Fatal(err)
	}
	green := red
	green.Conclusion = "success"
	var superseded *Superseded
	if err := st.Put(green); !errors.As(err, &superseded) {
		t.Fatalf("Put(success over failure) = %v, want *Superseded", err)
	}
	st.Close()

	reopened, err := openStore(path, func() time.Time { return *clock })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got := reopened.For("acme/widgets", sha('1'))
	if len(got) != 1 || got[0].Conclusion != "failure" {
		t.Errorf("after a restart held %+v, want the failure", got)
	}
}

// Two different checks on one commit are two verdicts; two commits do
// not see each other's.
func TestVerdictsAreKeptPerCheckAndPerCommit(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	for _, v := range []Verdict{
		{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "success"},
		{Repo: "acme/widgets", Commit: sha('1'), Name: "frontend", Conclusion: "failure"},
		{Repo: "acme/widgets", Commit: sha('2'), Name: "go", Conclusion: "success"},
		{Repo: "other/repo", Commit: sha('1'), Name: "go", Conclusion: "failure"},
	} {
		if w := post(t, srv, v, *clock); w.Code != http.StatusNoContent {
			t.Fatalf("%+v: %d %s", v, w.Code, w.Body)
		}
	}
	if got := get(t, srv, "acme/widgets", sha('1'), *clock); len(got) != 2 {
		t.Errorf("got %d verdicts for the first commit, want 2", len(got))
	}
	if got := get(t, srv, "acme/widgets", sha('2'), *clock); len(got) != 1 {
		t.Errorf("got %d verdicts for the second commit, want 1", len(got))
	}
	if got := get(t, srv, "other/repo", sha('1'), *clock); len(got) != 1 {
		t.Errorf("a second repository saw %d verdicts, want its own 1", len(got))
	}
}

// --- authentication. This is the one endpoint in the deployment an
// untrusted network can reach, so each way in is asserted shut.

func TestAnUnsignedRequestIsRefused(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	req := httptest.NewRequest(http.MethodPost, "/v1/verdicts", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("an unsigned report got %d, want 401", w.Code)
	}
}

func TestASignatureUnderTheWrongSecretIsRefused(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	body := []byte(`{"repo":"acme/widgets"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/verdicts", bytes.NewReader(body))
	req.Header.Set(TimestampHeader, fmt.Sprint(clock.Unix()))
	req.Header.Set(SignatureHeader, Sign("not the secret", *clock, http.MethodPost, "/v1/verdicts", body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", w.Code)
	}
}

// The signature covers the body, so a body swapped after signing is a
// signature that no longer matches -- which is the property that makes
// this worth more than a bearer token.
func TestATamperedBodyIsRefused(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	signed := []byte(`{"repo":"acme/widgets","commit":"` + sha('1') + `","name":"go","conclusion":"failure"}`)
	sent := bytes.Replace(signed, []byte("failure"), []byte("success"), 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/verdicts", bytes.NewReader(sent))
	req.Header.Set(TimestampHeader, fmt.Sprint(clock.Unix()))
	req.Header.Set(SignatureHeader, Sign(testSecret, *clock, http.MethodPost, "/v1/verdicts", signed))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a body changed after signing got %d, want 401", w.Code)
	}
}

// Without the timestamp in the signature, anybody who captured one
// request could repost the verdict it carried for as long as the secret
// lived -- and the one most worth reposting is a success.
func TestACapturedRequestStopsWorkingOnceItIsStale(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	v := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "success"}
	captured := *clock

	if w := post(t, srv, v, captured); w.Code != http.StatusNoContent {
		t.Fatalf("the request did not work when fresh: %d %s", w.Code, w.Body)
	}
	// The same request, replayed after the window.
	*clock = clock.Add(MaxClockSkew + time.Minute)
	if w := post(t, srv, v, captured); w.Code != http.StatusUnauthorized {
		t.Errorf("a replayed request got %d, want 401", w.Code)
	}
}

// A read has no body, so it is the path in the signature that ties it to
// one repository and commit. Without it every read in the same second
// carried the same signature, and one captured from a proxy log read any
// repository's verdicts until its timestamp aged out.
func TestAReadSignatureDoesNotReadAnotherRepository(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	if w := post(t, srv, Verdict{Repo: "acme/secret", Commit: sha('2'), Name: "go", Conclusion: "success"}, *clock); w.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", w.Code, w.Body)
	}
	captured := "/v1/verdicts/acme/widgets/" + sha('1')
	sig := Sign(testSecret, *clock, http.MethodGet, captured, nil)

	for _, target := range []string{"/v1/verdicts/acme/secret/" + sha('2'), "/v1/verdicts/acme/widgets/" + sha('2')} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set(TimestampHeader, fmt.Sprint(clock.Unix()))
		req.Header.Set(SignatureHeader, sig)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("a read signed for %s answered for %s: %d %s", captured, target, w.Code, w.Body)
		}
	}
}

// The old scheme, over the timestamp and body alone, is refused for a
// read whatever the transition window says: its read signature pinned
// nothing, which is the hole the method and path close.
func TestAReadSignedTheOldWayIsRefused(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/verdicts/acme/widgets/"+sha('1'), nil)
	req.Header.Set(TimestampHeader, fmt.Sprint(clock.Unix()))
	req.Header.Set(SignatureHeader, signLegacy(testSecret, *clock, nil))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a read signed the old way got %d, want 401", w.Code)
	}
}

// A report signed the old way still lands while workflows move over, and
// stops landing once the window is closed -- which is what
// legacyReports is for.
func TestAReportSignedTheOldWayIsAcceptedOnlyInTheTransitionWindow(t *testing.T) {
	srv, st, clock := newTestServer(t, "")
	v := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "success"}
	body, _ := json.Marshal(v)
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/verdicts", bytes.NewReader(body))
		req.Header.Set(TimestampHeader, fmt.Sprint(clock.Unix()))
		req.Header.Set(SignatureHeader, signLegacy(testSecret, *clock, body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	if code := send(); code != http.StatusNoContent {
		t.Fatalf("an old-style report got %d during the transition window, want 204", code)
	}
	if got := st.For("acme/widgets", sha('1')); len(got) != 1 {
		t.Fatalf("got %d verdicts, want the old-style report recorded", len(got))
	}
	srv.legacyReports = false
	if code := send(); code != http.StatusUnauthorized {
		t.Errorf("an old-style report got %d with the window closed, want 401", code)
	}
}

// A signature is for one method on one path: a report's signature does
// not verify against another route, nor as a read.
func TestASignatureIsBoundToItsMethodAndPath(t *testing.T) {
	now := testClock()
	body := []byte(`{"repo":"acme/widgets"}`)
	sig := Sign(testSecret, now, http.MethodPost, "/v1/verdicts", body)
	ts := fmt.Sprint(now.Unix())
	if err := Verify(testSecret, sig, ts, http.MethodPost, "/v1/verdicts", body, now); err != nil {
		t.Fatalf("the signature did not verify where it was made: %v", err)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/verdicts"},
		{http.MethodPost, "/v1/verdicts/acme/widgets"},
	} {
		if err := Verify(testSecret, sig, ts, c.method, c.path, body, now); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s %s: got %v, want ErrBadSignature", c.method, c.path, err)
		}
	}
	// Verify itself is strict; only the server's transition window takes
	// the old scheme.
	if err := Verify(testSecret, signLegacy(testSecret, now, body), ts, http.MethodPost, "/v1/verdicts", body, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("Verify accepted the old scheme: %v", err)
	}
}

// A service started with no secret can authenticate nothing, so it
// refuses rather than accepting: the failure mode of a forgotten
// setting must not be an open endpoint.
func TestAServerWithNoSecretAcceptsNothing(t *testing.T) {
	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(st, "")
	req := httptest.NewRequest(http.MethodPost, "/v1/verdicts", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503 -- a server with no secret must refuse, not accept", w.Code)
	}
}

// healthz is the one route with no signature, and says nothing but that
// the process is up.
func TestHealthNeedsNoSignatureAndLeaksNothing(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Errorf("healthz = %d %q", w.Code, w.Body)
	}
}

// --- validation. The reporter is a shell script in somebody's workflow
// file, and what this says back is the only debugging it has.

func TestValidateRefusesWhatAGateCouldNotActOn(t *testing.T) {
	ok := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "success"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a good verdict was refused: %v", err)
	}
	cases := []struct {
		name  string
		edit  func(*Verdict)
		wants string
	}{
		{"no repo", func(v *Verdict) { v.Repo = "" }, "repo is required"},
		{"a repo that is not owner/name", func(v *Verdict) { v.Repo = "widgets" }, "must be owner/name"},
		{"a repo with too many slashes", func(v *Verdict) { v.Repo = "a/b/c" }, "must be owner/name"},
		{"no commit", func(v *Verdict) { v.Commit = "" }, "commit is required"},
		{"an abbreviated commit", func(v *Verdict) { v.Commit = "aaaaaaa" }, "full 40-character sha"},
		{"a commit that is not hex", func(v *Verdict) { v.Commit = strings.Repeat("z", 40) }, "full 40-character sha"},
		{"no name", func(v *Verdict) { v.Name = "" }, "name is required"},
		{"no conclusion", func(v *Verdict) { v.Conclusion = "" }, "conclusion is required"},
		{"a conclusion GitHub does not use", func(v *Verdict) { v.Conclusion = "green" }, "not one GitHub uses"},
		{"an unbounded field", func(v *Verdict) { v.Name = strings.Repeat("x", maxFieldLen+1) }, "longer than"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ok
			c.edit(&v)
			err := v.Validate()
			if err == nil {
				t.Fatalf("%+v was accepted", v)
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("error %q does not say %q", err, c.wants)
			}
		})
	}
}

// The 400 names the field to fix but does not echo back what was sent.
func TestARefusedVerdictNamesTheFieldWithoutEchoingIt(t *testing.T) {
	srv, _, clock := newTestServer(t, "")
	v := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "<script>green</script>"}
	w := post(t, srv, v, *clock)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "checkpost: conclusion is not one GitHub uses" {
		t.Errorf("body = %q", body)
	}
}

// A verdict still running is not a verdict: there is no Status field to
// send one in, and "in_progress" is not a conclusion.
func TestAnUnfinishedCheckHasNoWayToBeReported(t *testing.T) {
	v := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "in_progress"}
	if err := v.Validate(); err == nil {
		t.Error("an in-progress check was accepted as a verdict")
	}
}

// --- durability.

func TestVerdictsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	srv, st, clock := newTestServer(t, path)
	post(t, srv, Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "success"}, *clock)
	post(t, srv, Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "frontend", Conclusion: "failure"}, *clock)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openStore(path, func() time.Time { return *clock })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.For("acme/widgets", sha('1')); len(got) != 2 {
		t.Fatalf("after a restart the store holds %d verdicts, want 2", len(got))
	}
}

// A torn write -- what a power cut during an append leaves -- must not
// stop the service starting, because the alternative is a deployment
// that will not come back over one damaged line.
func TestATornLineDoesNotStopTheStoreLoading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	good, _ := json.Marshal(Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go",
		Conclusion: "success", ReportedAt: time.Now()})
	torn := append(append(good, '\n'), []byte(`{"repo":"acme/wid`)...)
	if err := os.WriteFile(path, torn, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("a torn last line stopped the store loading: %v", err)
	}
	defer st.Close()
	if got := st.For("acme/widgets", sha('1')); len(got) != 1 {
		t.Errorf("got %d verdicts, want the intact line to have survived", len(got))
	}
}

// A line over the ceiling is damage, and like a torn one it is skipped:
// a Scanner stopped on it with ErrTooLong, and OpenStore failing made
// one over-long line the thing that kept the service from starting.
func TestAnOverLongLineDoesNotStopTheStoreLoading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	first, _ := json.Marshal(Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go",
		Conclusion: "success", ReportedAt: time.Now()})
	second, _ := json.Marshal(Verdict{Repo: "acme/widgets", Commit: sha('2'), Name: "go",
		Conclusion: "failure", ReportedAt: time.Now()})
	long := `{"repo":"acme/widgets","name":"` + strings.Repeat("x", 70<<10) + `"}`
	var file []byte
	for _, line := range [][]byte{first, []byte(long), second} {
		file = append(append(file, line...), '\n')
	}
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("a 70 KiB line stopped the store loading: %v", err)
	}
	defer st.Close()
	for _, c := range []byte{'1', '2'} {
		if got := st.For("acme/widgets", sha(c)); len(got) != 1 {
			t.Errorf("commit %c: got %d verdicts, want the line on each side of the long one kept", c, len(got))
		}
	}
}

// The same, with the over-long line last and no newline after it -- the
// shape a write cut off partway through leaves.
func TestAnOverLongLastLineDoesNotStopTheStoreLoading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	good, _ := json.Marshal(Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go",
		Conclusion: "success", ReportedAt: time.Now()})
	file := append(append(good, '\n'), strings.Repeat("y", 200<<10)...)
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("an over-long last line stopped the store loading: %v", err)
	}
	defer st.Close()
	if got := st.For("acme/widgets", sha('1')); len(got) != 1 {
		t.Errorf("got %d verdicts, want the intact line to have survived", len(got))
	}
}

func TestExpiredVerdictsAreDroppedOnLoadAndOnPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	now := testClock()
	st, err := openStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: "go", Conclusion: "success"}); err != nil {
		t.Fatal(err)
	}

	// Past retention, and pruned.
	now = now.Add(Retention + time.Hour)
	if err := st.Prune(); err != nil {
		t.Fatal(err)
	}
	if got := st.For("acme/widgets", sha('1')); len(got) != 0 {
		t.Errorf("a verdict past retention survived pruning")
	}
	st.Close()

	// And the compaction pruning triggered means it does not come back.
	reopened, err := openStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.For("acme/widgets", sha('1')); len(got) != 0 {
		t.Errorf("a pruned verdict came back after a restart")
	}
}

// The other half of the same rule, and the half a reopened Store applies
// on its own: a file that outlived the service holds lines past
// retention, and load drops those and keeps the rest. Asserted against a
// frozen clock, so what "past retention" means here is the age of the
// line and not the day the suite runs.
func TestLoadKeepsWhatIsInsideRetentionAndDropsWhatIsNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	now := testClock()
	var file []byte
	for _, v := range []Verdict{
		{Repo: "acme/widgets", Commit: sha('1'), Name: "fresh", Conclusion: "success",
			ReportedAt: now.Add(-time.Hour)},
		{Repo: "acme/widgets", Commit: sha('1'), Name: "just inside", Conclusion: "success",
			ReportedAt: now.Add(-Retention + time.Minute)},
		{Repo: "acme/widgets", Commit: sha('1'), Name: "just outside", Conclusion: "success",
			ReportedAt: now.Add(-Retention - time.Minute)},
		{Repo: "acme/widgets", Commit: sha('1'), Name: "ancient", Conclusion: "failure",
			ReportedAt: now.Add(-30 * 24 * time.Hour)},
	} {
		line, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		file = append(file, append(line, '\n')...)
	}
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := openStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got := st.For("acme/widgets", sha('1'))
	var names []string
	for _, v := range got {
		names = append(names, v.Name)
	}
	if len(got) != 2 {
		t.Fatalf("load kept %v, want the two inside retention", names)
	}
	for _, name := range names {
		if name != "fresh" && name != "just inside" {
			t.Errorf("load kept %q, which is past retention", name)
		}
	}
}

// Compaction rewrites the file through a rename, and the append handle
// has to follow it -- otherwise every later write goes to an unlinked
// inode and is lost at the next restart, silently.
func TestAppendsAfterCompactionAreNotLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	now := testClock()
	st, err := openStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	// Enough appends to trip compaction at least once.
	for i := 0; i < compactEvery+5; i++ {
		v := Verdict{Repo: "acme/widgets", Commit: sha('1'), Name: fmt.Sprintf("job-%d", i),
			Conclusion: "success"}
		if err := st.Put(v); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	reopened, err := openStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.For("acme/widgets", sha('1')); len(got) != compactEvery+5 {
		t.Errorf("after compaction the file holds %d verdicts, want %d -- appends went to an "+
			"unlinked file", len(got), compactEvery+5)
	}
}

func TestRepoSplitsOwnerFromName(t *testing.T) {
	if o, n, ok := Repo("acme/widgets"); !ok || o != "acme" || n != "widgets" {
		t.Errorf("Repo() = %q %q %v", o, n, ok)
	}
	for _, bad := range []string{"", "widgets", "a/b/c", "/widgets", "acme/"} {
		if _, _, ok := Repo(bad); ok {
			t.Errorf("Repo(%q) was accepted", bad)
		}
	}
}
