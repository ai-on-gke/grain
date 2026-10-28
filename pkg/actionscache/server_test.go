package actionscache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// client is a job talking to the cache the way actions/cache and
// buildkit's `gha` backend do: an ACTIONS_CACHE_URL with the token
// already in it, and paths joined onto the end.
type client struct {
	t    *testing.T
	base string
}

func newServer(t *testing.T, repos map[string]string) (*client, *Cache, *Scopes) {
	t.Helper()
	cache, err := NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("opening a cache: %v", err)
	}
	scopes := NewScopes()
	for token, repo := range repos {
		scopes.Grant(token, branch(repo))
	}
	srv := httptest.NewServer(NewServer(Config{Cache: cache, Scopes: scopes}))
	t.Cleanup(srv.Close)
	return &client{t: t, base: srv.URL}, cache, scopes
}

func (c *client) url(token, path string) string {
	return SandboxURL(c.base, token) + strings.TrimPrefix(path, "/")
}

func (c *client) do(method, url, body string, header map[string]string) (*http.Response, string) {
	c.t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		c.t.Fatalf("building %s %s: %v", method, url, err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

// The API as a client actually drives it: reserve, upload in two
// ranges, commit, look up, and fetch the archive from the absolute URL
// the lookup answered with.
func TestServerRoundTrip(t *testing.T) {
	t.Parallel()
	c, _, _ := newServer(t, map[string]string{"tok": "acme/widgets"})

	resp, body := c.do("POST", c.url("tok", "_apis/artifactcache/caches"),
		`{"key":"Image-Layers-abc","version":"sha256:v1","cacheSize":10}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reserve = %d %s", resp.StatusCode, body)
	}
	var reserved struct {
		CacheID uint64 `json:"cacheId"`
	}
	if err := json.Unmarshal([]byte(body), &reserved); err != nil {
		t.Fatalf("reserve answered %q: %v", body, err)
	}

	upload := fmt.Sprintf("_apis/artifactcache/caches/%d", reserved.CacheID)
	if resp, body := c.do("PATCH", c.url("tok", upload), "01234",
		map[string]string{"Content-Range": "bytes 0-4/*", "Content-Type": "application/octet-stream"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("first range = %d %s", resp.StatusCode, body)
	}
	if resp, body := c.do("PATCH", c.url("tok", upload), "56789",
		map[string]string{"Content-Range": "bytes 5-9/*"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("second range = %d %s", resp.StatusCode, body)
	}
	if resp, body := c.do("POST", c.url("tok", upload), `{"size":10}`, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("commit = %d %s", resp.StatusCode, body)
	}

	resp, body = c.do("GET", c.url("tok", "_apis/artifactcache/cache?keys=image-layers-abc&version=sha256:v1"), "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lookup = %d %s", resp.StatusCode, body)
	}
	var hit struct {
		Result          string `json:"result"`
		CacheKey        string `json:"cacheKey"`
		ArchiveLocation string `json:"archiveLocation"`
	}
	if err := json.Unmarshal([]byte(body), &hit); err != nil {
		t.Fatalf("lookup answered %q: %v", body, err)
	}
	if hit.Result != "hit" || hit.CacheKey != "image-layers-abc" {
		t.Fatalf("lookup = %+v", hit)
	}
	if !strings.HasPrefix(hit.ArchiveLocation, c.base) {
		t.Fatalf("archiveLocation = %q, want an absolute URL on the address the job reached", hit.ArchiveLocation)
	}
	resp, body = c.do("GET", hit.ArchiveLocation, "", nil)
	if resp.StatusCode != http.StatusOK || body != "0123456789" {
		t.Fatalf("download = %d %q", resp.StatusCode, body)
	}
	// buildkit fetches an archive in ranges rather than whole, so the
	// download has to be a range server and not a file dump.
	resp, body = c.do("GET", hit.ArchiveLocation, "", map[string]string{"Range": "bytes=2-4"})
	if resp.StatusCode != http.StatusPartialContent || body != "234" {
		t.Fatalf("ranged download = %d %q", resp.StatusCode, body)
	}
}

// A lookup that misses is a 204, which is what tells a client to build
// the thing itself.
func TestServerMissIsNoContent(t *testing.T) {
	t.Parallel()
	c, _, _ := newServer(t, map[string]string{"tok": "acme/widgets"})
	resp, body := c.do("GET", c.url("tok", "_apis/artifactcache/cache?keys=nothing&version=v1"), "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("miss = %d %s, want 204", resp.StatusCode, body)
	}
}

// The token is the whole of the boundary between one repository's cache
// and another's, so every route is refused without a live one.
func TestServerRefusesAnUnknownToken(t *testing.T) {
	t.Parallel()
	c, _, scopes := newServer(t, map[string]string{"tok": "acme/widgets"})

	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "_apis/artifactcache/cache?keys=k&version=v", ""},
		{"POST", "_apis/artifactcache/caches", `{"key":"k","version":"v"}`},
		{"PATCH", "_apis/artifactcache/caches/1", "x"},
		{"POST", "_apis/artifactcache/caches/1", `{"size":1}`},
		{"GET", "_apis/artifactcache/artifacts/1", ""},
	} {
		resp, body := c.do(tc.method, c.url("nobody", tc.path), tc.body, map[string]string{"Content-Range": "bytes 0-0/*"})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with an unknown token = %d %s, want 403", tc.method, tc.path, resp.StatusCode, body)
		}
	}

	// And a token is worth nothing the moment its job's sandbox is
	// released, which is what Revoke is for.
	scopes.Revoke("tok")
	resp, _ := c.do("GET", c.url("tok", "_apis/artifactcache/cache?keys=k&version=v"), "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a revoked token = %d, want 403", resp.StatusCode)
	}
}

// Two jobs of two repositories, one service: neither can see the
// other's entries even knowing the id.
func TestServerIsolatesRepositories(t *testing.T) {
	t.Parallel()
	c, cache, _ := newServer(t, map[string]string{"mine": "acme/widgets", "theirs": "evil/fork"})
	id, err := cache.Reserve(branch("acme/widgets"), "secret", "v1", 5)
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}
	if err := cache.Write(branch("acme/widgets"), id, 0, bytes.NewReader([]byte("12345"))); err != nil {
		t.Fatalf("uploading: %v", err)
	}
	if _, err := cache.Commit(branch("acme/widgets"), id); err != nil {
		t.Fatalf("committing: %v", err)
	}

	resp, _ := c.do("GET", c.url("theirs", fmt.Sprintf("_apis/artifactcache/artifacts/%d", id)), "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("another repository's download = %d, want 404", resp.StatusCode)
	}
	resp, body := c.do("GET", c.url("theirs", "_apis/artifactcache/cache?keys=secret&version=v1"), "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("another repository's lookup = %d %s, want a miss", resp.StatusCode, body)
	}
	resp, _ = c.do("GET", c.url("mine", fmt.Sprintf("_apis/artifactcache/artifacts/%d", id)), "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the owning repository's download = %d, want 200", resp.StatusCode)
	}
}

// A deployment that keeps no cache still answers, and says so, rather
// than leaving a job's client hanging on a closed port.
func TestServerWithNoCache(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewServer(Config{}))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/tok/_apis/artifactcache/cache?keys=k&version=v")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestSandboxURL(t *testing.T) {
	t.Parallel()
	// The trailing slash matters: every client joins its own path onto
	// the end of ACTIONS_CACHE_URL, and act's own server sets one too.
	for _, base := range []string{"http://10.0.0.1:9000", "http://10.0.0.1:9000/"} {
		if got := SandboxURL(base, "tok"); got != "http://10.0.0.1:9000/tok/" {
			t.Errorf("SandboxURL(%q) = %q", base, got)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		header string
		want   int64
		fails  bool
	}{
		{header: "bytes 0-4/*", want: 0},
		{header: "bytes 1048576-2097151/*", want: 1048576},
		{header: "bytes 5-9/10", want: 5},
		{header: "", fails: true},
		{header: "bytes */10", fails: true},
		{header: "bytes -5-9/*", fails: true},
	} {
		got, err := parseContentRange(tc.header)
		if tc.fails {
			if err == nil {
				t.Errorf("parseContentRange(%q) = %d, want a refusal", tc.header, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseContentRange(%q) = %d, %v, want %d", tc.header, got, err, tc.want)
		}
	}
}

func TestScopes(t *testing.T) {
	t.Parallel()
	var nilScopes *Scopes
	if _, ok := nilScopes.Repo("tok"); ok {
		t.Error("a nil registry granted something")
	}
	scopes := NewScopes()
	scopes.Grant("tok", branch("acme/widgets"))
	// One token, one repository: granting again replaces rather than adds.
	scopes.Grant("tok", branch("acme/gadgets"))
	if repo, ok := scopes.Repo("tok"); !ok || repo != "acme/gadgets" {
		t.Errorf("Repo = %q, %v", repo, ok)
	}
	if scopes.Granted() != 1 {
		t.Errorf("registry holds %d tokens, want 1", scopes.Granted())
	}
	scopes.Revoke("tok")
	if _, ok := scopes.Repo("tok"); ok {
		t.Error("a revoked token still names a repository")
	}
}

func TestMintTokenIsUnique(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 100 {
		token, err := MintToken()
		if err != nil {
			t.Fatalf("minting: %v", err)
		}
		if len(token) != 32 || seen[token] {
			t.Fatalf("minted %q, which is short or a repeat", token)
		}
		seen[token] = true
	}
}

func TestServerCleanEndpoint(t *testing.T) {
	t.Parallel()
	c, cache, _ := newServer(t, map[string]string{"tok": "acme/widgets"})
	store(t, cache, "acme/widgets", "key1", "v1", []byte("val1"))

	resp, _ := c.do("POST", c.url("tok", "_apis/artifactcache/clean"), "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("clean endpoint status = %d, want 200", resp.StatusCode)
	}
	// clean is a no-op for clients (eviction is managed by the cache), so entry remains
	if _, ok := cache.Find(branch("acme/widgets"), []string{"key1"}, "v1"); !ok {
		t.Error("entry was unexpectedly removed by clean endpoint")
	}

	// Clean with unknown token is 403
	resp, _ = c.do("POST", c.url("unknown-tok", "_apis/artifactcache/clean"), "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("clean with unknown token = %d, want 403", resp.StatusCode)
	}
}

func TestServerMalformedRequests(t *testing.T) {
	t.Parallel()
	c, _, _ := newServer(t, map[string]string{"tok": "acme/widgets"})

	// Malformed JSON on reserve
	resp, _ := c.do("POST", c.url("tok", "_apis/artifactcache/caches"), `not-valid-json`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("reserve with bad json = %d, want 400", resp.StatusCode)
	}

	// Non-numeric cache ID
	for _, tc := range []struct {
		method, path string
	}{
		{"PATCH", "_apis/artifactcache/caches/not-a-number"},
		{"POST", "_apis/artifactcache/caches/not-a-number"},
		{"GET", "_apis/artifactcache/artifacts/not-a-number"},
	} {
		resp, _ := c.do(tc.method, c.url("tok", tc.path), "", map[string]string{"Content-Range": "bytes 0-0/*"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, resp.StatusCode)
		}
	}

	// Invalid Content-Range header on PATCH
	resp, _ = c.do("PATCH", c.url("tok", "_apis/artifactcache/caches/1"), "data", map[string]string{"Content-Range": "invalid-range"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("patch with invalid content range = %d, want 400", resp.StatusCode)
	}

	// Unreserved entry upload/commit
	resp, _ = c.do("PATCH", c.url("tok", "_apis/artifactcache/caches/99999"), "data", map[string]string{"Content-Range": "bytes 0-3/*"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("patch on unreserved entry = %d, want 400", resp.StatusCode)
	}
	resp, _ = c.do("POST", c.url("tok", "_apis/artifactcache/caches/99999"), `{"size":4}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("commit on unreserved entry = %d, want 400", resp.StatusCode)
	}
}
