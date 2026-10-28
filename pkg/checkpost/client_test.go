package checkpost

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/forge"
)

// clientAgainst wires a real Client to a real Server, because everything
// worth asserting here is about the two agreeing -- the signature over a
// bodyless GET, the path, the JSON shape.
func clientAgainst(t *testing.T, expect map[string][]string) (*Client, *Store, func(Verdict)) {
	t.Helper()
	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := NewServer(st, testSecret)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	c := &Client{BaseURL: ts.URL, Secret: testSecret, Expect: expect}
	report := func(v Verdict) {
		t.Helper()
		if err := st.Put(v); err != nil {
			t.Fatal(err)
		}
	}
	return c, st, report
}

func ref() forge.Ref { return forge.Ref{Owner: "acme", Repo: "widgets", Number: 7} }

// The whole answer, and grain may act on it.
func TestACompleteAnswerIsComplete(t *testing.T) {
	c, _, report := clientAgainst(t, map[string][]string{"acme/widgets": {"go", "frontend"}})
	commit := sha('1')
	report(Verdict{Repo: "acme/widgets", Commit: commit, Name: "go", Conclusion: "success"})
	report(Verdict{Repo: "acme/widgets", Commit: commit, Name: "frontend", Conclusion: "failure",
		URL: "https://example.invalid/frontend"})

	checks, complete, err := c.For(ref(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("every expected check had reported and the answer was still called incomplete")
	}
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(checks))
	}
	byName := map[string]string{}
	for _, ch := range checks {
		if ch.Status != "completed" {
			t.Errorf("%s has status %q -- a verdict is by definition finished", ch.Name, ch.Status)
		}
		if ch.Conclusion == nil {
			t.Fatalf("%s came back with no conclusion", ch.Name)
		}
		byName[ch.Name] = *ch.Conclusion
	}
	if byName["go"] != "success" || byName["frontend"] != "failure" {
		t.Errorf("conclusions = %v", byName)
	}
	for _, ch := range checks {
		if ch.Name == "frontend" && ch.HTMLURL != "https://example.invalid/frontend" {
			t.Errorf("the url did not survive: %q", ch.HTMLURL)
		}
	}
}

// The one that matters. Three jobs into a twelve-job build the service
// holds three, all green, and nothing about that answer distinguishes it
// from a finished build with three checks. If this ever returns
// complete, grain merges a change nine checks have not looked at.
func TestAPartialAnswerIsNeverComplete(t *testing.T) {
	c, _, report := clientAgainst(t, map[string][]string{
		"acme/widgets": {"go", "frontend", "ui e2e"},
	})
	commit := sha('1')
	report(Verdict{Repo: "acme/widgets", Commit: commit, Name: "go", Conclusion: "success"})
	report(Verdict{Repo: "acme/widgets", Commit: commit, Name: "frontend", Conclusion: "success"})

	checks, complete, err := c.For(ref(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("a build with one check still to report was called complete -- grain would " +
			"merge on two of three")
	}
	if checks != nil {
		t.Errorf("an incomplete answer handed back %d checks; the caller must fall through to "+
			"the forge with nothing", len(checks))
	}
}

// A commit nothing has reported on is the same "ask the forge", not an
// error and not an empty repository. This is every commit for the first
// nine minutes of its life.
func TestACommitNobodyHasReportedOnIsIncomplete(t *testing.T) {
	c, _, _ := clientAgainst(t, map[string][]string{"acme/widgets": {"go"}})
	checks, complete, err := c.For(ref(), sha('9'))
	if err != nil {
		t.Fatal(err)
	}
	if complete || checks != nil {
		t.Errorf("a commit nobody reported on was answered: complete=%v checks=%d", complete, len(checks))
	}
}

// A repository the deployment does not expect reports from is never
// answered from here, and costs no request at all: that is what makes
// this opt-in per repository rather than a switch that silently changes
// how every repo is gated.
func TestARepositoryNotExpectedToReportIsNeverAnswered(t *testing.T) {
	c, _, report := clientAgainst(t, map[string][]string{"other/repo": {"go"}})
	commit := sha('1')
	report(Verdict{Repo: "acme/widgets", Commit: commit, Name: "go", Conclusion: "success"})

	_, complete, err := c.For(ref(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Error("a repository with no Expect entry was answered from the service")
	}
}

// A service that is down, wrong or unreachable must not stop the queue:
// the caller treats an error exactly as it treats incomplete, and goes
// and asks GitHub.
func TestAnUnreachableServiceIsAnErrorAndNotAnAnswer(t *testing.T) {
	c := &Client{
		BaseURL: "http://127.0.0.1:1",
		Secret:  testSecret,
		Expect:  map[string][]string{"acme/widgets": {"go"}},
		HTTP:    &http.Client{Timeout: 50 * time.Millisecond},
	}
	checks, complete, err := c.For(ref(), sha('1'))
	if err == nil {
		t.Fatal("an unreachable service answered without error")
	}
	if complete || checks != nil {
		t.Error("an unreachable service produced an answer")
	}
}

// The client and the server have to agree about signing a request with
// no body, which is the one shape the reporting side never exercises.
func TestTheClientsSignatureIsAcceptedByTheServer(t *testing.T) {
	c, _, report := clientAgainst(t, map[string][]string{"acme/widgets": {"go"}})
	commit := sha('1')
	report(Verdict{Repo: "acme/widgets", Commit: commit, Name: "go", Conclusion: "success"})
	if _, complete, err := c.For(ref(), commit); err != nil || !complete {
		t.Fatalf("the server did not accept the client's signature: complete=%v err=%v", complete, err)
	}
}

func TestTheClientIsRefusedUnderTheWrongSecret(t *testing.T) {
	c, _, report := clientAgainst(t, map[string][]string{"acme/widgets": {"go"}})
	c.Secret = "not the secret"
	commit := sha('1')
	report(Verdict{Repo: "acme/widgets", Commit: commit, Name: "go", Conclusion: "success"})
	if _, complete, err := c.For(ref(), commit); err == nil || complete {
		t.Errorf("a client signing with the wrong secret was answered: complete=%v err=%v", complete, err)
	}
}

// --- ParseExpect

func TestParseExpectReadsWhatAUnitFileWouldCarry(t *testing.T) {
	got, err := ParseExpect(" acme/widgets = go , frontend ; other/repo=ci ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d repositories, want 2: %v", len(got), got)
	}
	if len(got["acme/widgets"]) != 2 || got["acme/widgets"][0] != "go" || got["acme/widgets"][1] != "frontend" {
		t.Errorf("acme/widgets = %v", got["acme/widgets"])
	}
	if len(got["other/repo"]) != 1 || got["other/repo"][0] != "ci" {
		t.Errorf("other/repo = %v", got["other/repo"])
	}
}

func TestParseExpectOfNothingIsNoRepositories(t *testing.T) {
	got, err := ParseExpect("")
	if err != nil || len(got) != 0 {
		t.Errorf("ParseExpect(\"\") = %v, %v", got, err)
	}
}

// A repository naming no checks would be complete the moment a single
// verdict arrived -- the exact failure Expect exists to prevent -- so it
// is refused rather than read as "expect nothing".
func TestParseExpectRefusesARepositoryWithNoChecks(t *testing.T) {
	for _, bad := range []string{"acme/widgets=", "acme/widgets= , ", "acme/widgets"} {
		if _, err := ParseExpect(bad); err == nil {
			t.Errorf("ParseExpect(%q) was accepted", bad)
		}
	}
}

func TestParseExpectRefusesAThingThatIsNotARepository(t *testing.T) {
	for _, bad := range []string{"widgets=go", "=go", "a/b/c=go"} {
		if _, err := ParseExpect(bad); err == nil {
			t.Errorf("ParseExpect(%q) was accepted", bad)
		}
	}
}
