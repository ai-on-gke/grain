package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// conditionalServer is a GitHub that behaves the way GitHub does: it
// stamps an ETag on every answer and returns 304 with no body when the
// caller sends that ETag back.
//
// etag is what it is currently serving; changing it between calls is how
// a test says "the resource moved". requests records what it was asked,
// so a test can assert on the validator actually sent rather than on the
// answer alone.
type conditionalServer struct {
	etag     string
	body     string
	link     string
	requests []string
	notMod   int
}

func (s *conditionalServer) start(t *testing.T) *RealTransport {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests = append(s.requests, r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", s.etag)
		if s.link != "" {
			w.Header().Set("Link", s.link)
		}
		if r.Header.Get("If-None-Match") == s.etag {
			s.notMod++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(s.body))
	}))
	t.Cleanup(server.Close)
	return &RealTransport{
		Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false, Conditional: true,
	}
}

// The whole point: the second poll of an unchanged resource sends the
// validator, GitHub answers 304, and the caller gets the same bytes it
// got the first time -- for free, since a 304 costs nothing against the
// primary rate limit.
func TestConditionalGetAnswersAnUnchangedResourceFromTheCache(t *testing.T) {
	server := &conditionalServer{etag: `W/"abc"`, body: `[{"name":"go"}]`}
	transport := server.start(t)

	first, err := transport.Request("GET", "/repos/acme/widgets/commits/deadbeef/check-runs", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := transport.Request("GET", "/repos/acme/widgets/commits/deadbeef/check-runs", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if server.requests[0] != "" {
		t.Errorf("the first call sent If-None-Match: %q -- there was nothing to revalidate against",
			server.requests[0])
	}
	if server.requests[1] != `W/"abc"` {
		t.Errorf("the second call sent If-None-Match: %q, want the ETag back", server.requests[1])
	}
	if server.notMod != 1 {
		t.Errorf("the server answered %d not-modifieds, want 1", server.notMod)
	}
	if second.Status != 200 {
		t.Errorf("the caller saw status %d, want the 200 it would have seen unconditionally",
			second.Status)
	}
	if string(second.Body) != string(first.Body) {
		t.Errorf("the cached answer was %q, want %q", second.Body, first.Body)
	}
}

// A 304 carries no Link header of its own, and every paginated read in
// this package walks that header -- so a cached answer that dropped it
// would silently truncate a listing to its first page.
func TestConditionalGetKeepsTheHeadersThePagerWalks(t *testing.T) {
	link := `<https://api.github.com/repos/acme/widgets/pulls?page=2>; rel="next"`
	server := &conditionalServer{etag: `"one"`, body: "[]", link: link}
	transport := server.start(t)

	if _, err := transport.Request("GET", "/repos/acme/widgets/pulls", nil, nil); err != nil {
		t.Fatal(err)
	}
	second, err := transport.Request("GET", "/repos/acme/widgets/pulls", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextPagePath(second.Headers["Link"]); got != "/repos/acme/widgets/pulls?page=2" {
		t.Errorf("next page from the cached answer = %q, want the second page", got)
	}
}

// A resource that moved answers 200 with the new bytes, and the cache
// takes up the new validator rather than pinning the old answer.
func TestConditionalGetFollowsTheResourceWhenItChanges(t *testing.T) {
	server := &conditionalServer{etag: `"one"`, body: `{"state":"open"}`}
	transport := server.start(t)

	if _, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil); err != nil {
		t.Fatal(err)
	}
	server.etag, server.body = `"two"`, `{"state":"closed"}`
	second, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(second.Body) != `{"state":"closed"}` {
		t.Errorf("body = %q, want the new answer", second.Body)
	}
	third, err := transport.Request("GET", "/repos/acme/widgets/pulls/7", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if server.requests[2] != `"two"` {
		t.Errorf("the third call sent %q, want the new ETag", server.requests[2])
	}
	if string(third.Body) != `{"state":"closed"}` {
		t.Errorf("body = %q, want the new answer still", third.Body)
	}
}

// The credential is part of the key: the same path is a 200 for a token
// that may see the repository and a 404 for one that may not, and a
// deployment whose ladder holds several routes different repositories
// through different ones.
func TestConditionalGetDoesNotShareAnswersBetweenCredentials(t *testing.T) {
	server := &conditionalServer{etag: `"one"`, body: "{}"}
	transport := server.start(t)

	alice := map[string]string{"Authorization": "token alice"}
	bob := map[string]string{"Authorization": "token bob"}
	if _, err := transport.Request("GET", "/repos/acme/widgets", alice, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Request("GET", "/repos/acme/widgets", bob, nil); err != nil {
		t.Fatal(err)
	}
	if server.requests[1] != "" {
		t.Errorf("bob's call sent alice's validator (%q), so one credential could be served the "+
			"other's answer", server.requests[1])
	}
}

// Writes have nothing to revalidate, and a cached POST would be a
// disaster of a different kind: nothing but GETs is ever keyed.
func TestConditionalGetLeavesWritesAlone(t *testing.T) {
	server := &conditionalServer{etag: `"one"`, body: "{}"}
	transport := server.start(t)

	for i := 0; i < 2; i++ {
		if _, err := transport.Request("POST", "/repos/acme/widgets/issues/1/comments",
			nil, []byte(`{"body":"hi"}`)); err != nil {
			t.Fatal(err)
		}
	}
	for i, sent := range server.requests {
		if sent != "" {
			t.Errorf("POST %d sent If-None-Match: %q", i, sent)
		}
	}
}

// The zero-value transport is the mocked-GitHub test seam, and a seam
// whose job is to observe the calls a client makes has to see all of
// them.
func TestTheZeroValueTransportSendsUnconditionalRequests(t *testing.T) {
	server := &conditionalServer{etag: `"one"`, body: "{}"}
	transport := server.start(t)
	transport.Conditional = false

	for i := 0; i < 2; i++ {
		if _, err := transport.Request("GET", "/repos/acme/widgets", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if server.notMod != 0 {
		t.Errorf("the server answered %d not-modifieds with conditional requests off", server.notMod)
	}
}

func TestNewRealTransportRevalidates(t *testing.T) {
	if !NewRealTransport("github.com").Conditional {
		t.Error("the transport every real deployment uses is not sending conditional requests, so " +
			"every poll of an unchanged resource costs a call against the rate limit")
	}
}

// A caller that set its own validator has said what it wants, and is not
// to be second-guessed.
func TestConditionalGetLeavesACallersOwnValidatorAlone(t *testing.T) {
	server := &conditionalServer{etag: `"one"`, body: "{}"}
	transport := server.start(t)

	headers := map[string]string{"If-None-Match": `"somebody else's"`}
	if _, err := transport.Request("GET", "/repos/acme/widgets", headers, nil); err != nil {
		t.Fatal(err)
	}
	if server.requests[0] != `"somebody else's"` {
		t.Errorf("If-None-Match = %q, want the caller's own", server.requests[0])
	}
}

func TestResponseCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	cache := newResponseCache()
	for i := 0; i < maxCachedEntries+10; i++ {
		cache.store(fmt.Sprintf("key-%d", i), fmt.Sprintf(`"etag-%d"`, i),
			ApiResponse{Status: 200, Body: []byte("x")})
	}
	if got := len(cache.items); got != maxCachedEntries {
		t.Errorf("cache holds %d entries, want it bounded at %d", got, maxCachedEntries)
	}
	if _, ok := cache.lookup("key-0"); ok {
		t.Error("the oldest entry survived eviction")
	}
	if _, ok := cache.lookup(fmt.Sprintf("key-%d", maxCachedEntries+9)); !ok {
		t.Error("the newest entry was evicted")
	}
}

// The one resource that is polled constantly is exactly the one that
// must not age out from under its own poll, and a 304 is a use.
func TestResponseCacheKeepsWhatIsBeingPolled(t *testing.T) {
	cache := newResponseCache()
	cache.store("polled", `"etag"`, ApiResponse{Status: 200, Body: []byte("x")})
	for i := 0; i < maxCachedEntries; i++ {
		cache.touch("polled")
		cache.store(fmt.Sprintf("other-%d", i), `"e"`, ApiResponse{Status: 200, Body: []byte("x")})
	}
	if _, ok := cache.lookup("polled"); !ok {
		t.Error("the entry being revalidated on every tick was evicted by one-off reads")
	}
}

// One very large listing must not stand for the whole cache.
func TestResponseCacheDeclinesAnOversizedAnswer(t *testing.T) {
	cache := newResponseCache()
	cache.store("huge", `"etag"`, ApiResponse{Status: 200, Body: make([]byte, maxCachedEntryBytes+1)})
	if _, ok := cache.lookup("huge"); ok {
		t.Error("an answer past the per-entry ceiling was cached")
	}
}

// An answer with no validator drops whatever the key held: sending an
// If-None-Match GitHub can only ever answer 200 to is a wasted header,
// and holding an entry nothing will revalidate is a wasted entry.
func TestResponseCacheForgetsAnAnswerWithNoValidator(t *testing.T) {
	cache := newResponseCache()
	cache.store("key", `"etag"`, ApiResponse{Status: 200, Body: []byte("old")})
	cache.store("key", "", ApiResponse{Status: 200, Body: []byte("new")})
	if _, ok := cache.lookup("key"); ok {
		t.Error("the stale entry survived an answer that carried no ETag")
	}
}

// Nothing handed to a caller shares a map or a slice with the cache: a
// caller that appends to a body, or writes to a header map, must not be
// writing into the next call's answer.
func TestCachedAnswersAreCopies(t *testing.T) {
	cache := newResponseCache()
	cache.store("key", `"etag"`, ApiResponse{
		Status:  200,
		Headers: map[string]string{"Link": "first"},
		Body:    []byte("body"),
	})
	entry, ok := cache.lookup("key")
	if !ok {
		t.Fatal("nothing cached")
	}
	handed := copyResponse(entry.resp)
	handed.Headers["Link"] = "meddled"
	handed.Body[0] = 'X'

	again, _ := cache.lookup("key")
	if again.resp.Headers["Link"] != "first" || string(again.resp.Body) != "body" {
		t.Errorf("the cached entry was written through: %+v", again.resp)
	}
}
