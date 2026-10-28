package pkgcache

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// upstream is a stand-in archive: it serves whatever paths a test gives
// it and counts how many times each was actually fetched, which is how
// every test below distinguishes a cache hit from a second download.
type upstream struct {
	t     *testing.T
	mu    sync.Mutex
	files map[string]string
	hits  map[string]int
	srv   *httptest.Server
}

func newUpstream(t *testing.T, files map[string]string) *upstream {
	t.Helper()
	u := &upstream{t: t, files: files, hits: map[string]int{}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := u.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		u.mu.Lock()
		u.hits[r.URL.Path]++
		u.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".json") || strings.Contains(r.Header.Get("Accept"), "json") {
			w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		} else if strings.HasSuffix(r.URL.Path, "/") {
			w.Header().Set("Content-Type", "text/html")
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// client is an HTTP client that sends every request to this upstream,
// whatever host the request names -- so a handler asking for
// http://deb.debian.org/... is answered here without a test needing to
// own that name.
func (u *upstream) client() *http.Client {
	base, err := url.Parse(u.srv.URL)
	if err != nil {
		u.t.Fatalf("parsing %s: %v", u.srv.URL, err)
	}
	return &http.Client{Transport: roundTripTo{base: base}}
}

type roundTripTo struct{ base *url.URL }

func (t roundTripTo) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.base.Scheme
	clone.URL.Host = t.base.Host
	clone.Host = ""
	return http.DefaultTransport.RoundTrip(clone)
}

func newTestServer(t *testing.T, cfg Config) (*Server, *Cache) {
	t.Helper()
	cache, err := NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	cfg.Cache = cache
	cfg.Log = func(format string, args ...any) { t.Logf(format, args...) }
	return NewServer(cfg), cache
}

func get(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestAPTPackageIsCachedAndServedFromDisk(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	up := newUpstream(t, map[string]string{pkg: "the package bytes"})
	s, cache := newTestServer(t, Config{Client: up.client()})

	first := get(t, s, "http://deb.debian.org"+pkg)
	if first.Code != http.StatusOK || first.Body.String() != "the package bytes" {
		t.Fatalf("first fetch: %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("first fetch cache status = %q, want miss", got)
	}

	second := get(t, s, "http://deb.debian.org"+pkg)
	if second.Body.String() != "the package bytes" {
		t.Errorf("second fetch served %q", second.Body.String())
	}
	if got := second.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second fetch cache status = %q, want hit", got)
	}
	if up.hits[pkg] != 1 {
		t.Errorf("upstream was asked for the package %d times, want 1", up.hits[pkg])
	}
	if cache.Bytes() == 0 {
		t.Error("the cache reports holding nothing after storing a package")
	}
}

// TestAPTUpstreamFetchesAnArchiveFromWhereItIsPointed pins the one
// thing AptUpstream has to do and the one thing it must not: an archive
// named in it is fetched from there, path and all, and an archive that
// is not named is still fetched from itself.
//
// The client here answers everything and records what it was asked for,
// so what is asserted is the URL this service *decided on* rather than
// where a test's transport happened to send it.
func TestAPTUpstreamFetchesAnArchiveFromWhereItIsPointed(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	asked := &recordingTransport{}
	s, cache := newTestServer(t, Config{
		Client:      &http.Client{Transport: asked},
		AptUpstream: map[string]string{"deb.debian.org": "http://127.0.0.1:9/mirror"},
	})

	// The transparent half: a sandbox that resolved deb.debian.org to
	// this service and asked it for a package.
	if rec := get(t, s, "http://deb.debian.org"+pkg); rec.Code != http.StatusOK {
		t.Fatalf("fetching the package answered %d", rec.Code)
	}
	// The proxy half: the same package asked for in absolute form.
	if rec := get(t, s, "http://deb.debian.org"+pkg+"?x=1"); rec.Code != http.StatusOK {
		t.Fatalf("fetching the package through the proxy answered %d", rec.Code)
	}
	// And an archive the override does not name.
	if rec := get(t, s, "http://security.debian.org"+pkg); rec.Code != http.StatusOK {
		t.Fatalf("fetching from an archive with no override answered %d", rec.Code)
	}

	want := []string{
		"http://127.0.0.1:9/mirror" + pkg,
		"http://127.0.0.1:9/mirror" + pkg + "?x=1",
		"http://security.debian.org" + pkg,
	}
	if got := asked.urls(); !reflect.DeepEqual(got, want) {
		t.Errorf("the cache fetched\n  %v\nwant\n  %v", got, want)
	}
	// The stored copy is filed under where it came from, so that a
	// deployment moving its mirror does not serve the old one's bytes
	// under the new one's name.
	if _, _, ok := cache.Open(KindAPT, "http://127.0.0.1:9/mirror"+pkg); !ok {
		t.Error("the package fetched from the override is not in the cache under the URL it was fetched from")
	}
}

// recordingTransport answers every request with an empty 200 and keeps
// the URL it was given.
type recordingTransport struct {
	mu   sync.Mutex
	seen []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.seen = append(rt.seen, req.URL.String())
	rt.mu.Unlock()
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"application/octet-stream"}},
		Body:          io.NopCloser(strings.NewReader("package bytes")),
		ContentLength: int64(len("package bytes")),
		Request:       req,
	}, nil
}

func (rt *recordingTransport) urls() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.seen...)
}

func TestAPTIndexesAreNeverCached(t *testing.T) {
	const index = "/debian/dists/bookworm/InRelease"
	up := newUpstream(t, map[string]string{index: "signed index"})
	s, _ := newTestServer(t, Config{Client: up.client()})

	for i := 0; i < 2; i++ {
		rec := get(t, s, "http://deb.debian.org"+index)
		if rec.Body.String() != "signed index" {
			t.Fatalf("fetch %d served %q", i, rec.Body.String())
		}
		if got := rec.Header().Get(cacheStatusHeader); got != "pass" {
			t.Errorf("fetch %d cache status = %q, want pass", i, got)
		}
	}
	if up.hits[index] != 2 {
		t.Errorf("the index was fetched %d times, want 2 -- an index must never be served from cache", up.hits[index])
	}
}

func TestAPTRefusesAHostThatIsNotAnArchive(t *testing.T) {
	up := newUpstream(t, map[string]string{"/secrets": "should never be fetched"})
	s, _ := newTestServer(t, Config{Client: up.client()})

	rec := get(t, s, "http://example.com/secrets")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 -- the cache must not be a general egress proxy", rec.Code)
	}
	if up.hits["/secrets"] != 0 {
		t.Error("the refused request was forwarded anyway")
	}
	if !strings.Contains(rec.Body.String(), "example.com") {
		t.Errorf("the refusal does not name the host it refused: %q", rec.Body.String())
	}
}

func TestAPTThirdPartyRepositoriesAreProxiedAndCached(t *testing.T) {
	cases := []struct {
		host string
		pkg  string
	}{
		{"download.docker.com", "/linux/debian/dists/bookworm/pool/stable/amd64/docker-ce_24.0.7-1~debian.12~bookworm_amd64.deb"},
		{"packages.cloud.google.com", "/apt/pool/cloud-sdk/google-cloud-cli_455.0.0-0_amd64.deb"},
		{"apt.releases.hashicorp.com", "/dists/bookworm/pool/amd64/main/terraform_1.6.4-1_amd64.deb"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.host, func(t *testing.T) {
			up := newUpstream(t, map[string]string{tc.pkg: "binary payload"})
			s, cache := newTestServer(t, Config{Client: up.client()})

			first := get(t, s, "http://"+tc.host+tc.pkg)
			if first.Code != http.StatusOK || first.Body.String() != "binary payload" {
				t.Fatalf("first fetch: %d %q", first.Code, first.Body.String())
			}
			if got := first.Header().Get(cacheStatusHeader); got != "miss" {
				t.Errorf("first fetch cache status = %q, want miss", got)
			}

			second := get(t, s, "http://"+tc.host+tc.pkg)
			if second.Body.String() != "binary payload" {
				t.Errorf("second fetch served %q", second.Body.String())
			}
			if got := second.Header().Get(cacheStatusHeader); got != "hit" {
				t.Errorf("second fetch cache status = %q, want hit", got)
			}
			if up.hits[tc.pkg] != 1 {
				t.Errorf("upstream was asked for package %d times, want 1", up.hits[tc.pkg])
			}
			if cache.Bytes() == 0 {
				t.Error("cache reports 0 bytes after storing package")
			}
		})
	}
}

// TestConnectWithoutAConnectionToTunnelOver: a CONNECT is answered by
// taking the connection over, so it needs one. Anything carrying a
// request that cannot be hijacked -- an HTTP/2 stream, a test's recorder
// -- gets a sentence saying so rather than a panic. The tunnel itself is
// tested against a real listener in connect_test.go.
func TestConnectWithoutAConnectionToTunnelOver(t *testing.T) {
	s, _ := newTestServer(t, Config{})
	rec := httptest.NewRecorder()
	// The authority form a real CONNECT uses -- "host:port" and no scheme
	// or path, which is the whole request-target.
	req := httptest.NewRequest(http.MethodConnect, "pypi.org:443", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "tunnel") {
		t.Errorf("the refusal does not say a tunnel is what could not be made:\n%s", body)
	}
}

// pypiServer stands up a cache in front of an index whose one package
// links to an artifact on a host of the test's own.
func pypiServer(t *testing.T, index string, artifact string) (*Server, *upstream) {
	t.Helper()
	up := newUpstream(t, map[string]string{
		"/simple/demo/":                       index,
		"/packages/demo-1.0-py3-none-any.whl": artifact,
	})
	s, _ := newTestServer(t, Config{
		Client:         up.client(),
		PyPIIndex:      up.srv.URL + "/simple",
		PyPIFileHosts:  []string{"files.example.test"},
		PyPIFileScheme: "http",
	})
	return s, up
}

func TestPyPIIndexLinksAreRewrittenThroughTheCache(t *testing.T) {
	const index = `<!DOCTYPE html><html><body>` +
		`<a href="https://files.example.test/packages/demo-1.0-py3-none-any.whl#sha256=abc" data-requires-python="&gt;=3.8">demo-1.0-py3-none-any.whl</a>` +
		`<a href="https://elsewhere.example/demo-1.0.tar.gz#sha256=def">demo-1.0.tar.gz</a>` +
		`</body></html>`
	s, _ := pypiServer(t, index, "wheel bytes")

	rec := get(t, s, "/pypi/simple/demo/")
	body := rec.Body.String()
	if !strings.Contains(body, `href="/pypi/files/files.example.test/packages/demo-1.0-py3-none-any.whl#sha256=abc"`) {
		t.Errorf("the download link was not rewritten through the cache: %q", body)
	}
	if !strings.Contains(body, `data-requires-python="&gt;=3.8"`) {
		t.Errorf("the rewrite did not leave the other attributes alone: %q", body)
	}
	if !strings.Contains(body, `href="https://elsewhere.example/demo-1.0.tar.gz#sha256=def"`) {
		t.Errorf("a link to a host the cache does not serve should be left alone: %q", body)
	}
}

func TestPyPIJSONIndexLinksAreRewritten(t *testing.T) {
	index := `{"meta":{"api-version":"1.0"},"name":"demo","files":[` +
		`{"filename":"demo-1.0-py3-none-any.whl","url":"https://files.example.test/packages/demo-1.0-py3-none-any.whl","hashes":{"sha256":"abc"},"yanked":false}` +
		`]}`
	s, _ := pypiServer(t, index, "wheel bytes")

	// The Accept header pip itself sends, which is what makes the index
	// answer in PEP 691's JSON rather than in HTML.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pypi/simple/demo/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	s.ServeHTTP(rec, req)

	var doc struct {
		Files []struct {
			URL    string            `json:"url"`
			Hashes map[string]string `json:"hashes"`
			Yanked bool              `json:"yanked"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the rewritten index is not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(doc.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(doc.Files))
	}
	if doc.Files[0].URL != "/pypi/files/files.example.test/packages/demo-1.0-py3-none-any.whl" {
		t.Errorf("url = %q, want it rewritten through the cache", doc.Files[0].URL)
	}
	if doc.Files[0].Hashes["sha256"] != "abc" {
		t.Errorf("the rewrite lost the hash pip verifies with: %+v", doc.Files[0])
	}
}

func TestPyPIArtifactIsCached(t *testing.T) {
	const index = `<a href="https://files.example.test/packages/demo-1.0-py3-none-any.whl#sha256=abc">demo</a>`
	s, _ := pypiServer(t, index, "wheel bytes")

	const target = "/pypi/files/files.example.test/packages/demo-1.0-py3-none-any.whl"
	first := get(t, s, target)
	if first.Code != http.StatusOK || first.Body.String() != "wheel bytes" {
		t.Fatalf("first fetch: %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("first fetch cache status = %q, want miss", got)
	}
	second := get(t, s, target)
	if got := second.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second fetch cache status = %q, want hit", got)
	}
	if second.Body.String() != "wheel bytes" {
		t.Errorf("second fetch served %q", second.Body.String())
	}
}

func TestPyPIRefusesAnUnlistedFileHost(t *testing.T) {
	s, _ := pypiServer(t, "", "")
	rec := get(t, s, "/pypi/files/example.com/anything.whl")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// registry is a stand-in Docker Hub: it answers an unauthenticated
// request with the same 401-and-challenge the real one does, hands out
// anonymous tokens, and counts what was actually fetched -- which is how
// the tests below tell a cache hit from a second pull, and one token from
// one per request.
type registry struct {
	mu     sync.Mutex
	files  map[string]string
	hits   map[string]int
	tokens int
	srv    *httptest.Server
}

// testLayer is the layer testDigest names. A blob is only stored once its
// bytes hash to the digest it was asked for by, so a stand-in registry a
// test expects to be cached from has to serve the bytes the digest names.
const testLayer = "the layer bytes"

var testDigest = digestOf(testLayer)

// digestOf is the sha256 content digest of body, as a registry names it.
func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newRegistry(t *testing.T, files map[string]string) *registry {
	t.Helper()
	reg := &registry{files: files, hits: map[string]int{}}
	reg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			reg.mu.Lock()
			reg.tokens++
			reg.mu.Unlock()
			if r.URL.Query().Get("scope") == "" {
				http.Error(w, "no scope asked for", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"anonymous-pull-token","expires_in":300}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer anonymous-pull-token" {
			repo := strings.TrimPrefix(r.URL.Path, "/v2/")
			if i := strings.Index(repo, "/blobs/"); i > 0 {
				repo = repo[:i]
			} else if i := strings.Index(repo, "/manifests/"); i > 0 {
				repo = repo[:i]
			}
			w.Header().Set("Www-Authenticate", fmt.Sprintf(
				`Bearer realm="%s/token",service="registry.test",scope="repository:%s:pull"`,
				reg.srv.URL, strings.Trim(repo, "/")))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		reg.mu.Lock()
		body, ok := reg.files[r.URL.Path]
		if ok {
			reg.hits[r.URL.Path]++
		}
		reg.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(reg.srv.Close)
	return reg
}

func dockerServer(t *testing.T, files map[string]string) (*Server, *registry) {
	t.Helper()
	reg := newRegistry(t, files)
	s, _ := newTestServer(t, Config{DockerRegistry: reg.srv.URL})
	return s, reg
}

func TestDockerPingAnswersAsARegistry(t *testing.T) {
	s, _ := dockerServer(t, nil)
	rec := get(t, s, dockerAPIPrefix)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- dockerd decides a mirror is usable by this request", rec.Code)
	}
	if got := rec.Header().Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
		t.Errorf("Docker-Distribution-Api-Version = %q", got)
	}
}

func TestDockerBlobIsCachedAndServedFromDisk(t *testing.T) {
	blob := dockerAPIPrefix + "library/demo/blobs/" + testDigest
	s, reg := dockerServer(t, map[string]string{blob: testLayer})

	first := get(t, s, blob)
	if first.Code != http.StatusOK || first.Body.String() != "the layer bytes" {
		t.Fatalf("first pull: %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("first pull cache status = %q, want miss", got)
	}
	second := get(t, s, blob)
	if got := second.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second pull cache status = %q, want hit", got)
	}
	if second.Body.String() != "the layer bytes" {
		t.Errorf("second pull served %q", second.Body.String())
	}
	if reg.hits[blob] != 1 {
		t.Errorf("the registry was asked for the layer %d times, want 1", reg.hits[blob])
	}
	if reg.tokens != 1 {
		t.Errorf("fetched %d anonymous tokens, want 1 -- a token is good for the whole repository until it expires", reg.tokens)
	}
}

func TestDockerBlobIsStoredOnceForEveryRepositorySharingIt(t *testing.T) {
	first := dockerAPIPrefix + "library/demo/blobs/" + testDigest
	s, reg := dockerServer(t, map[string]string{first: testLayer})

	if rec := get(t, s, first); rec.Code != http.StatusOK {
		t.Fatalf("first pull: %d", rec.Code)
	}
	// The same digest under another repository, which the stand-in
	// registry does not serve at all: a 200 here is the stored copy, and
	// anything else would have gone upstream for bytes it already had.
	second := get(t, s, dockerAPIPrefix+"someone/else/blobs/"+testDigest)
	if second.Code != http.StatusOK || second.Body.String() != testLayer {
		t.Fatalf("the same layer under another repository: %d %q -- a blob is its own digest and should be stored once",
			second.Code, second.Body.String())
	}
	if reg.hits[first] != 1 {
		t.Errorf("the layer was fetched %d times, want 1", reg.hits[first])
	}
}

func TestDockerManifestsAreNeverCached(t *testing.T) {
	manifest := dockerAPIPrefix + "library/demo/manifests/latest"
	s, reg := dockerServer(t, map[string]string{manifest: "the manifest"})

	for i := 0; i < 2; i++ {
		rec := get(t, s, manifest)
		if rec.Body.String() != "the manifest" {
			t.Fatalf("fetch %d served %q", i, rec.Body.String())
		}
		if got := rec.Header().Get(cacheStatusHeader); got != "pass" {
			t.Errorf("fetch %d cache status = %q, want pass -- a tag is mutable", i, got)
		}
	}
	if reg.hits[manifest] != 2 {
		t.Errorf("the manifest was fetched %d times, want 2 -- a cached tag is last month's image", reg.hits[manifest])
	}
}

func TestDockerBlobWithoutADigestIsNotStored(t *testing.T) {
	blob := dockerAPIPrefix + "library/demo/blobs/not-a-digest"
	s, reg := dockerServer(t, map[string]string{blob: "whatever this is"})

	for i := 0; i < 2; i++ {
		if got := get(t, s, blob).Header().Get(cacheStatusHeader); got != "pass" {
			t.Errorf("fetch %d cache status = %q, want pass -- only a digest names bytes that cannot change", i, got)
		}
	}
	if reg.hits[blob] != 2 {
		t.Errorf("fetched %d times, want 2", reg.hits[blob])
	}
}

func TestDockerRefusesAPathThatIsNotAPull(t *testing.T) {
	s, _ := dockerServer(t, nil)
	for _, path := range []string{dockerAPIPrefix + "library/demo/tags/list", dockerAPIPrefix + "library/demo/blobs/uploads/x/y"} {
		rec := get(t, s, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 -- dockerd falls back to Docker Hub for anything a mirror does not serve",
				path, rec.Code)
		}
	}
}

func TestDockerTokenIsNotSentToWhereverARedirectPoints(t *testing.T) {
	// A blob request the registry answers with a redirect to a CDN, which
	// is how every real layer download goes. The token belongs to the
	// registry and must not travel past it.
	var cdnAuth string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, "the layer bytes")
	}))
	t.Cleanup(cdn.Close)

	blob := dockerAPIPrefix + "library/demo/blobs/" + testDigest
	reg := newRegistry(t, nil)
	reg.files = map[string]string{}
	inner := reg.srv.Config.Handler
	reg.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == blob && r.Header.Get("Authorization") != "" {
			http.Redirect(w, r, cdn.URL+"/layers/blob", http.StatusTemporaryRedirect)
			return
		}
		inner.ServeHTTP(w, r)
	})
	s, _ := newTestServer(t, Config{DockerRegistry: reg.srv.URL})

	rec := get(t, s, blob)
	if rec.Code != http.StatusOK || rec.Body.String() != "the layer bytes" {
		t.Fatalf("pull through a redirect: %d %q", rec.Code, rec.Body.String())
	}
	if cdnAuth != "" {
		t.Errorf("the registry token was sent on to %s: %q", cdn.URL, cdnAuth)
	}
}

func ghcrServer(t *testing.T, files map[string]string) (*Server, *registry) {
	t.Helper()
	reg := newRegistry(t, files)
	s, _ := newTestServer(t, Config{GHCRRegistry: reg.srv.URL})
	return s, reg
}

func gcrServer(t *testing.T, files map[string]string) (*Server, *registry) {
	t.Helper()
	reg := newRegistry(t, files)
	s, _ := newTestServer(t, Config{GCRRegistry: reg.srv.URL})
	return s, reg
}

func TestGHCRPingAnswersAsARegistry(t *testing.T) {
	s, _ := ghcrServer(t, nil)
	for _, path := range []string{"/v2/ghcr", "/v2/ghcr/", "/v2/ghcr.io", "/v2/ghcr.io/"} {
		rec := get(t, s, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
			t.Errorf("%s: Docker-Distribution-Api-Version = %q", path, got)
		}
	}
}

func TestGCRPingAnswersAsARegistry(t *testing.T) {
	s, _ := gcrServer(t, nil)
	for _, path := range []string{"/v2/gcr", "/v2/gcr/", "/v2/gcr.io", "/v2/gcr.io/"} {
		rec := get(t, s, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
			t.Errorf("%s: Docker-Distribution-Api-Version = %q", path, got)
		}
	}
}

func TestGHCRBlobIsCachedAndServedFromDisk(t *testing.T) {
	upstreamBlob := "/v2/owner/repo/blobs/" + testDigest
	s, reg := ghcrServer(t, map[string]string{upstreamBlob: testLayer})

	first := get(t, s, "/v2/ghcr/owner/repo/blobs/"+testDigest)
	if first.Code != http.StatusOK || first.Body.String() != testLayer {
		t.Fatalf("first pull: %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("first pull cache status = %q, want miss", got)
	}
	second := get(t, s, "/v2/ghcr/owner/repo/blobs/"+testDigest)
	if got := second.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second pull cache status = %q, want hit", got)
	}
	if second.Body.String() != testLayer {
		t.Errorf("second pull served %q", second.Body.String())
	}
	if reg.hits[upstreamBlob] != 1 {
		t.Errorf("ghcr was asked for the layer %d times, want 1", reg.hits[upstreamBlob])
	}
}

func TestGCRBlobIsCachedAndServedFromDisk(t *testing.T) {
	upstreamBlob := "/v2/google-samples/node-hello/blobs/" + testDigest
	s, reg := gcrServer(t, map[string]string{upstreamBlob: testLayer})

	first := get(t, s, "/v2/gcr/google-samples/node-hello/blobs/"+testDigest)
	if first.Code != http.StatusOK || first.Body.String() != testLayer {
		t.Fatalf("first pull: %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("first pull cache status = %q, want miss", got)
	}
	second := get(t, s, "/v2/gcr/google-samples/node-hello/blobs/"+testDigest)
	if got := second.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second pull cache status = %q, want hit", got)
	}
	if second.Body.String() != testLayer {
		t.Errorf("second pull served %q", second.Body.String())
	}
	if reg.hits[upstreamBlob] != 1 {
		t.Errorf("gcr was asked for the layer %d times, want 1", reg.hits[upstreamBlob])
	}
}

func TestDockerBlobSharedAcrossRegistries(t *testing.T) {
	blob := "/v2/shared/image/blobs/" + testDigest
	ghcrReg := newRegistry(t, map[string]string{blob: testLayer})
	gcrReg := newRegistry(t, map[string]string{})
	dockerReg := newRegistry(t, map[string]string{})

	s, _ := newTestServer(t, Config{
		DockerRegistry: dockerReg.srv.URL,
		GHCRRegistry:   ghcrReg.srv.URL,
		GCRRegistry:    gcrReg.srv.URL,
	})

	// Pull from GHCR first (fetches and caches)
	rec := get(t, s, "/v2/ghcr/shared/image/blobs/"+testDigest)
	if rec.Code != http.StatusOK || rec.Body.String() != testLayer {
		t.Fatalf("ghcr pull: %d %q", rec.Code, rec.Body.String())
	}

	// Pull same blob from GCR (should hit local cache without contacting GCR registry)
	gcrRec := get(t, s, "/v2/gcr/other/image/blobs/"+testDigest)
	if gcrRec.Code != http.StatusOK || gcrRec.Body.String() != testLayer {
		t.Fatalf("gcr pull: %d %q", gcrRec.Code, gcrRec.Body.String())
	}
	if got := gcrRec.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("gcr pull cache status = %q, want hit", got)
	}
	if len(gcrReg.hits) != 0 {
		t.Errorf("gcr registry received fetches: %v", gcrReg.hits)
	}

	// Pull same blob from Docker Hub (should hit local cache without contacting Docker registry)
	hubRec := get(t, s, "/v2/library/image/blobs/"+testDigest)
	if hubRec.Code != http.StatusOK || hubRec.Body.String() != testLayer {
		t.Fatalf("docker hub pull: %d %q", hubRec.Code, hubRec.Body.String())
	}
	if got := hubRec.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("docker hub pull cache status = %q, want hit", got)
	}
	if len(dockerReg.hits) != 0 {
		t.Errorf("docker registry received fetches: %v", dockerReg.hits)
	}
}

func TestDockerNamespaceQueryRouting(t *testing.T) {
	blob := "/v2/my/image/blobs/" + testDigest
	ghcrReg := newRegistry(t, map[string]string{blob: "ghcr content"})
	gcrReg := newRegistry(t, map[string]string{blob: "gcr content"})

	s, _ := newTestServer(t, Config{
		GHCRRegistry: ghcrReg.srv.URL,
		GCRRegistry:  gcrReg.srv.URL,
	})

	ghcrRec := get(t, s, "/v2/my/image/blobs/"+testDigest+"?ns=ghcr.io")
	if ghcrRec.Code != http.StatusOK || ghcrRec.Body.String() != "ghcr content" {
		t.Fatalf("ghcr query pull: %d %q", ghcrRec.Code, ghcrRec.Body.String())
	}

	otherDigest := "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	otherBlob := "/v2/my/image/blobs/" + otherDigest
	gcrReg.files[otherBlob] = "gcr other content"

	gcrRec := get(t, s, "/v2/my/image/blobs/"+otherDigest+"?ns=gcr.io")
	if gcrRec.Code != http.StatusOK || gcrRec.Body.String() != "gcr other content" {
		t.Fatalf("gcr query pull: %d %q", gcrRec.Code, gcrRec.Body.String())
	}
}

func TestDockerDaemonJSONKeepsWhatIsAlreadyThere(t *testing.T) {
	existing := `{"bip":"172.30.255.1/24","default-address-pools":[{"base":"172.31.0.0/16","size":24}]}`
	merged, err := DockerDaemonJSON([]byte(existing), "http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("DockerDaemonJSON: %v", err)
	}
	var doc struct {
		BIP                string   `json:"bip"`
		Mirrors            []string `json:"registry-mirrors"`
		InsecureRegistries []string `json:"insecure-registries"`
		Pools              []struct {
			Base string `json:"base"`
		} `json:"default-address-pools"`
	}
	if err := json.Unmarshal([]byte(merged), &doc); err != nil {
		t.Fatalf("the merged file is not JSON: %v\n%s", err, merged)
	}
	if doc.BIP != "172.30.255.1/24" || len(doc.Pools) != 1 {
		t.Errorf("the merge lost the guest's own docker bridge configuration:\n%s", merged)
	}
	if len(doc.Mirrors) != 1 || doc.Mirrors[0] != "http://10.9.0.1:41234" {
		t.Errorf("registry-mirrors = %v, want the cache", doc.Mirrors)
	}
	if len(doc.InsecureRegistries) != 1 || doc.InsecureRegistries[0] != "10.9.0.1:41234" {
		t.Errorf("insecure-registries = %v -- dockerd refuses a plain-HTTP mirror it has not been told to trust", doc.InsecureRegistries)
	}

	// Applying it again -- which is what a recreated sandbox does -- must
	// leave the same file rather than a growing list.
	again, err := DockerDaemonJSON([]byte(merged), "http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("second DockerDaemonJSON: %v", err)
	}
	if again != merged {
		t.Errorf("applying the merge twice changed the file:\n%s\n%s", merged, again)
	}
}

func TestDockerDaemonJSONOnAnEmptyOrUnreadableFile(t *testing.T) {
	merged, err := DockerDaemonJSON(nil, "http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("a sandbox with no daemon.json at all: %v", err)
	}
	if !strings.Contains(merged, "10.9.0.1:41234") {
		t.Errorf("the mirror is not in the file:\n%s", merged)
	}
	if _, err := DockerDaemonJSON([]byte("this is not json"), "http://10.9.0.1:41234"); err == nil {
		t.Error("a daemon.json this cannot parse should be left alone rather than overwritten")
	}
	for _, bad := range []string{"", "not a url", "https://example.com", "http://"} {
		if _, err := DockerDaemonJSON(nil, bad); err == nil {
			t.Errorf("DockerDaemonJSON(_, %q) was accepted", bad)
		}
	}
}

func TestStatsAndFlush(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	up := newUpstream(t, map[string]string{pkg: "the package bytes"})
	s, cache := newTestServer(t, Config{Client: up.client()})
	get(t, s, "http://deb.debian.org"+pkg)

	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	var apt KindStats
	for _, st := range stats {
		if st.Kind == KindAPT {
			apt = st
		}
	}
	if apt.Entries != 1 || apt.Bytes == 0 {
		t.Fatalf("apt stats = %+v, want one entry of non-zero size", apt)
	}

	entries, freed, err := cache.Flush(KindAPT)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if entries != 1 || freed == 0 {
		t.Errorf("Flush removed %d entries freeing %d bytes, want 1 and non-zero", entries, freed)
	}
	if cache.Bytes() != 0 {
		t.Errorf("the cache still reports %d bytes after a flush", cache.Bytes())
	}

	// And the next fetch goes upstream again, which is the whole point
	// of a flush.
	get(t, s, "http://deb.debian.org"+pkg)
	if up.hits[pkg] != 2 {
		t.Errorf("after a flush the package was fetched %d times in total, want 2", up.hits[pkg])
	}
}

func TestFlushRefusesAnUnknownEcosystem(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	if _, _, err := cache.Flush("cargo"); err == nil {
		t.Fatal("flushing an ecosystem this cache does not have should be refused")
	}
}

func TestEvictionKeepsTheCacheUnderItsBound(t *testing.T) {
	body := strings.Repeat("x", 4096)
	files := map[string]string{}
	for i := 0; i < 8; i++ {
		files[fmt.Sprintf("/debian/pool/main/p/pkg%d/pkg%d.deb", i, i)] = body
	}
	up := newUpstream(t, files)
	cache, err := NewCache(t.TempDir(), 12*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	s := NewServer(Config{Cache: cache, Client: up.client(), Log: func(string, ...any) {}})
	for i := 0; i < 8; i++ {
		get(t, s, fmt.Sprintf("http://deb.debian.org/debian/pool/main/p/pkg%d/pkg%d.deb", i, i))
	}
	if cache.Bytes() > cache.MaxBytes() {
		t.Errorf("the cache holds %d bytes, past its %d-byte bound", cache.Bytes(), cache.MaxBytes())
	}
	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats[0].Entries == 0 || stats[0].Entries == 8 {
		t.Errorf("after eviction the cache holds %d of 8 entries, want some but not all", stats[0].Entries)
	}
}

func TestSetMaxBytesEvictsWhenOverNewBound(t *testing.T) {
	body := strings.Repeat("x", 4096)
	files := map[string]string{}
	for i := 0; i < 4; i++ {
		files[fmt.Sprintf("/debian/pool/main/p/pkg%d/pkg%d.deb", i, i)] = body
	}
	up := newUpstream(t, files)
	cache, err := NewCache(t.TempDir(), 64*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	s := NewServer(Config{Cache: cache, Client: up.client(), Log: func(string, ...any) {}})
	for i := 0; i < 4; i++ {
		get(t, s, fmt.Sprintf("http://deb.debian.org/debian/pool/main/p/pkg%d/pkg%d.deb", i, i))
	}
	if cache.Bytes() <= 8*1024 {
		t.Fatalf("expected cache bytes > 8KB before downsizing, got %d", cache.Bytes())
	}
	// Downsize bound so it triggers eviction
	cache.SetMaxBytes(8 * 1024)
	if cache.MaxBytes() != 8*1024 {
		t.Errorf("MaxBytes = %d, want %d", cache.MaxBytes(), 8*1024)
	}
	if cache.Bytes() > cache.MaxBytes() {
		t.Errorf("the cache holds %d bytes, past new %d-byte bound", cache.Bytes(), cache.MaxBytes())
	}
}

// npmServer stands a cache in front of a registry of the test's own.
func npmServer(t *testing.T, files map[string]string) (*Server, *upstream) {
	t.Helper()
	up := newUpstream(t, files)
	s, _ := newTestServer(t, Config{Client: up.client(), NPMRegistry: up.srv.URL + "/registry"})
	return s, up
}

// What `npm ci` asks for: npm maps a tarball URL on registry.npmjs.org
// onto the registry it has been configured with before fetching it, so
// what arrives here is a registry-relative path and no link anywhere had
// to be rewritten to produce it.
func TestNPMTarballIsCached(t *testing.T) {
	const tarball = "/registry/lodash/-/lodash-4.17.21.tgz"
	s, up := npmServer(t, map[string]string{tarball: "tarball bytes"})

	const target = "/npm/lodash/-/lodash-4.17.21.tgz"
	first := get(t, s, target)
	if first.Code != http.StatusOK || first.Body.String() != "tarball bytes" {
		t.Fatalf("first fetch: %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("first fetch cache status = %q, want miss", got)
	}
	if got := get(t, s, target).Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second fetch cache status = %q, want hit", got)
	}
	if up.hits[tarball] != 1 {
		t.Errorf("upstream was asked for the tarball %d times, want 1", up.hits[tarball])
	}
}

func TestNPMScopedTarballIsCached(t *testing.T) {
	const tarball = "/registry/@types/node/-/node-20.11.0.tgz"
	s, up := npmServer(t, map[string]string{tarball: "tarball bytes"})

	if rec := get(t, s, "/npm/@types/node/-/node-20.11.0.tgz"); rec.Body.String() != "tarball bytes" {
		t.Fatalf("a scoped package's tarball: %d %q", rec.Code, rec.Body.String())
	}
	if got := get(t, s, "/npm/@types/node/-/node-20.11.0.tgz").Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second fetch cache status = %q, want hit", got)
	}
	if up.hits[tarball] != 1 {
		t.Errorf("upstream was asked for the tarball %d times, want 1", up.hits[tarball])
	}
}

// A packument is forwarded untouched, both because it changes whenever
// anything is published and because the tarball URLs in it are what npm
// writes into the package-lock.json a run may well commit: a cache that
// rewrote them would put this deployment's own address in a repository.
func TestNPMPackumentIsForwardedUnchangedAndUncached(t *testing.T) {
	const packument = "/registry/lodash.json"
	const body = `{"name":"lodash","versions":{"4.17.21":{"dist":{` +
		`"tarball":"https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz","integrity":"sha512-abc"}}}}`
	s, up := npmServer(t, map[string]string{packument: body})

	for i := 0; i < 2; i++ {
		rec := get(t, s, "/npm/lodash.json")
		if rec.Body.String() != body {
			t.Errorf("fetch %d rewrote the packument:\n%s", i, rec.Body.String())
		}
		if got := rec.Header().Get(cacheStatusHeader); got != "pass" {
			t.Errorf("fetch %d cache status = %q, want pass", i, got)
		}
	}
	if up.hits[packument] != 2 {
		t.Errorf("the packument was fetched %d times, want 2 -- it changes whenever anything is published",
			up.hits[packument])
	}
}

// goProxyServer stands a cache in front of a module proxy of the test's
// own, holding one version of one module.
func goProxyServer(t *testing.T, files map[string]string) (*Server, *upstream) {
	t.Helper()
	up := newUpstream(t, files)
	s, _ := newTestServer(t, Config{Client: up.client(), GoProxy: up.srv.URL + "/mod"})
	return s, up
}

func TestGoModuleZipIsCached(t *testing.T) {
	const zip = "/mod/example.com/demo/@v/v1.2.3.zip"
	s, up := goProxyServer(t, map[string]string{zip: "module zip bytes"})

	const target = "/go/example.com/demo/@v/v1.2.3.zip"
	first := get(t, s, target)
	if first.Code != http.StatusOK || first.Body.String() != "module zip bytes" {
		t.Fatalf("first fetch: %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("first fetch cache status = %q, want miss", got)
	}
	second := get(t, s, target)
	if got := second.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second fetch cache status = %q, want hit", got)
	}
	if second.Body.String() != "module zip bytes" {
		t.Errorf("second fetch served %q", second.Body.String())
	}
	if up.hits[zip] != 1 {
		t.Errorf("upstream was asked for the module %d times, want 1", up.hits[zip])
	}
}

func TestGoVersionListIsNeverCached(t *testing.T) {
	const list = "/mod/example.com/demo/@v/list"
	s, up := goProxyServer(t, map[string]string{list: "v1.2.3\n"})

	for i := 0; i < 2; i++ {
		if rec := get(t, s, "/go/example.com/demo/@v/list"); rec.Header().Get(cacheStatusHeader) != "pass" {
			t.Errorf("fetch %d cache status = %q, want pass", i, rec.Header().Get(cacheStatusHeader))
		}
	}
	if up.hits[list] != 2 {
		t.Errorf("the version list was fetched %d times, want 2 -- it changes when a module is released", up.hits[list])
	}
}

// The go command asks a proxy whether it will serve checksum-database
// material before it uses it for any, and proxy.golang.org says no --
// which is why this service answers for itself and fetches from the
// database. Getting this wrong sends the go command to sum.golang.org
// over HTTPS, which a sandbox with no default route cannot reach, and
// turns every first build in one into a verification failure.
func TestGoChecksumDatabaseIsServedByTheCache(t *testing.T) {
	const tile = "/tile/8/0/x123/456"
	const lookup = "/lookup/example.com/demo@v1.2.3"
	s, up := goProxyServer(t, map[string]string{tile: "tile bytes", lookup: "signed lookup"})

	rec := get(t, s, "/go/sumdb/sum.golang.org/supported")
	if rec.Code != http.StatusOK {
		t.Fatalf("sumdb/supported = %d, want 200 -- the go command reads anything else as "+
			"\"fetch the checksum database yourself\"", rec.Code)
	}
	for i := 0; i < 2; i++ {
		if rec := get(t, s, "/go/sumdb/sum.golang.org"+tile); rec.Body.String() != "tile bytes" {
			t.Fatalf("tile fetch %d served %q", i, rec.Body.String())
		}
		if rec := get(t, s, "/go/sumdb/sum.golang.org"+lookup); rec.Body.String() != "signed lookup" {
			t.Fatalf("lookup fetch %d served %q", i, rec.Body.String())
		}
	}
	if up.hits[tile] != 1 {
		t.Errorf("the tile was fetched %d times, want 1 -- a log tile is the same bytes forever", up.hits[tile])
	}
	if up.hits[lookup] != 2 {
		t.Errorf("the lookup was fetched %d times, want 2 -- its reply carries a tree head that moves",
			up.hits[lookup])
	}
}

func TestGoRefusesAnUnlistedChecksumDatabase(t *testing.T) {
	s, _ := goProxyServer(t, nil)
	if rec := get(t, s, "/go/sumdb/example.com/tile/8/0/1"); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestGoCacheableIsConservative(t *testing.T) {
	cacheable := []string{
		"example.com/demo/@v/v1.2.3.zip",
		"example.com/demo/@v/v1.2.3.mod",
		"example.com/demo/@v/v1.2.3.info",
		"example.com/demo/@v/v0.0.0-20240101000000-abcdefabcdef.zip",
		"github.com/!burnt!sushi/toml/@v/v1.4.0.zip",
	}
	for _, p := range cacheable {
		if !goCacheable(p) {
			t.Errorf("goCacheable(%q) = false, want true", p)
		}
	}
	uncacheable := []string{
		"example.com/demo/@v/list",
		"example.com/demo/@latest",
		// A branch or tag name, whose answer moves as the branch does.
		"example.com/demo/@v/main.info",
		"example.com/demo/@v/master.info",
	}
	for _, p := range uncacheable {
		if goCacheable(p) {
			t.Errorf("goCacheable(%q) = true, want false -- its answer can change", p)
		}
	}
}

func TestSandboxFiles(t *testing.T) {
	files, err := SandboxFiles("http://10.9.0.1:41234", nil)
	if err != nil {
		t.Fatalf("SandboxFiles: %v", err)
	}
	if len(files) != 5 {
		t.Fatalf("got %d files, want 5", len(files))
	}
	byPath := map[string]SandboxFile{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	apt, ok := byPath[AptConfigPath]
	if !ok {
		t.Fatalf("no apt configuration in %v", files)
	}
	if !strings.Contains(apt.Content, `Acquire::http::Proxy::deb.debian.org "http://10.9.0.1:41234/";`) {
		t.Errorf("apt config does not point deb.debian.org at the cache:\n%s", apt.Content)
	}
	for _, host := range []string{"download.docker.com", "packages.cloud.google.com", "apt.releases.hashicorp.com"} {
		want := fmt.Sprintf("Acquire::http::Proxy::%s \"http://10.9.0.1:41234/\";", host)
		if !strings.Contains(apt.Content, want) {
			t.Errorf("apt config does not point %s at the cache:\n%s", host, apt.Content)
		}
	}
	// Every host proxied over http is sent direct over https, because apt
	// falls back to the http proxy for an https source: without these
	// lines a `deb https://download.docker.com` line -- which is the one
	// Docker's own install page tells people to write -- reaches this cache
	// as a CONNECT, and a CONNECT is a tunnel the cache cannot store
	// anything out of (connect.go). Direct is the better of the two here,
	// because the sandbox's DNS points that host at the cache's own HTTPS
	// listener, where the download is cached properly.
	for _, host := range DefaultAptHosts {
		if strings.HasPrefix(host, ".") {
			continue
		}
		want := fmt.Sprintf("Acquire::https::Proxy::%s \"DIRECT\";", host)
		if !strings.Contains(apt.Content, want) {
			t.Errorf("apt config does not send https for %s direct, so an https source for it would be "+
				"tunnelled through the cache uncached instead of cached:\n%s", host, apt.Content)
		}
	}
	if strings.Contains(apt.Content, "Proxy::.debian.org") {
		t.Errorf("apt config names a wildcard host apt cannot use:\n%s", apt.Content)
	}
	pip, ok := byPath[PipConfigPath]
	if !ok {
		t.Fatalf("no pip configuration in %v", files)
	}
	if !strings.Contains(pip.Content, "index-url = http://10.9.0.1:41234/pypi/simple/") {
		t.Errorf("pip config does not point at the cache index:\n%s", pip.Content)
	}
	if !strings.Contains(pip.Content, "trusted-host = 10.9.0.1:41234") {
		t.Errorf("pip config does not trust the cache host, so pip would refuse a plain-HTTP index:\n%s", pip.Content)
	}
	ghcr, ok := byPath[GHCRHostsConfigPath]
	if !ok {
		t.Fatalf("no ghcr hosts configuration in %v", files)
	}
	if !strings.Contains(ghcr.Content, `server = "https://ghcr.io"`) ||
		!strings.Contains(ghcr.Content, `[host."http://10.9.0.1:41234/v2/ghcr"]`) ||
		!strings.Contains(ghcr.Content, `override_path = true`) {
		t.Errorf("ghcr hosts config does not point at the cache:\n%s", ghcr.Content)
	}
	gcr, ok := byPath[GCRHostsConfigPath]
	if !ok {
		t.Fatalf("no gcr hosts configuration in %v", files)
	}
	if !strings.Contains(gcr.Content, `server = "https://gcr.io"`) ||
		!strings.Contains(gcr.Content, `[host."http://10.9.0.1:41234/v2/gcr"]`) ||
		!strings.Contains(gcr.Content, `override_path = true`) {
		t.Errorf("gcr hosts config does not point at the cache:\n%s", gcr.Content)
	}
	// The one file here that is not for this sandbox: what a container it
	// builds or runs can be pointed at the cache with. Its own tests are
	// below; what matters here is that a configured sandbox has it at all,
	// since nothing else puts it there.
	child, ok := byPath[ChildContainerEnvPath]
	if !ok {
		t.Fatalf("no child-container settings in %v", files)
	}
	if !strings.Contains(child.Content, "PIP_INDEX_URL=http://10.9.0.1:41234/pypi/simple/") {
		t.Errorf("the child-container settings do not point at the cache:\n%s", child.Content)
	}
}

func TestSandboxFilesRefusesAnUnusableAddress(t *testing.T) {
	for _, bad := range []string{"", "not a url", "https://example.com", "http://"} {
		if _, err := SandboxFiles(bad, nil); err == nil {
			t.Errorf("SandboxFiles(%q) was accepted", bad)
		}
		if _, err := SandboxCommands(bad); err == nil {
			t.Errorf("SandboxCommands(%q) was accepted", bad)
		}
	}
}

func TestSandboxCommandsPointGoAndNPMAtTheCache(t *testing.T) {
	commands, err := SandboxCommands("http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("SandboxCommands: %v", err)
	}
	byTool := map[string][]string{}
	for _, c := range commands {
		if len(c.Argv) == 0 {
			t.Fatalf("a command with no argv: %+v", c)
		}
		byTool[c.Argv[0]] = c.Argv
	}
	goCmd, ok := byTool["go"]
	if !ok {
		t.Fatalf("no Go toolchain command in %v", commands)
	}
	want := "GOPROXY=http://10.9.0.1:41234/go,direct"
	if goCmd[len(goCmd)-1] != want {
		t.Errorf("go command = %v, want it to end in %q", goCmd, want)
	}
	wantCacheProg := "GOCACHEPROG=/tmp/grain-gocacheprog http://10.9.0.1:41234"
	if !slices.Contains(goCmd, wantCacheProg) {
		t.Errorf("go command = %v, want it to include %q", goCmd, wantCacheProg)
	}
	if goCmd[1] != "env" || goCmd[2] != "-w" {
		t.Errorf("go command = %v, want `go env -w`, which merges rather than replacing the go env file", goCmd)
	}
	npmCmd, ok := byTool["npm"]
	if !ok {
		t.Fatalf("no npm command in %v", commands)
	}
	if strings.Join(npmCmd, " ") != "npm config set registry=http://10.9.0.1:41234/npm/ audit=false" {
		t.Errorf("npm command = %v, want it to set the registry (and to stop npm auditing against a registry "+
			"this cache does not serve)", npmCmd)
	}
}

func TestChildContainerEnv(t *testing.T) {
	env, err := ChildContainerEnv("http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("ChildContainerEnv: %v", err)
	}
	if got := env["PIP_INDEX_URL"]; got != "http://10.9.0.1:41234/pypi/simple/" {
		t.Errorf("PIP_INDEX_URL = %q", got)
	}
	if got := env["PIP_TRUSTED_HOST"]; got != "10.9.0.1:41234" {
		t.Errorf("PIP_TRUSTED_HOST = %q", got)
	}
	if got := env["NPM_CONFIG_REGISTRY"]; got != "http://10.9.0.1:41234/npm/" {
		t.Errorf("NPM_CONFIG_REGISTRY = %q", got)
	}
	if got := env["GOPROXY"]; got != "http://10.9.0.1:41234/go,direct" {
		t.Errorf("GOPROXY = %q", got)
	}
	// The whole claim of this shape: not one of these names is a setting
	// any HTTP client reads as "a proxy for everything". A container given
	// the set below can still reach an https:// archive and a plain-HTTP
	// host this cache has never heard of, which is what 2026-09-18's
	// proxies.default.httpProxy took away from every build in this
	// deployment.
	for _, name := range proxyEnvNames {
		if _, ok := env[name]; ok {
			t.Errorf("ChildContainerEnv sets %s, which is one proxy for the whole internet: %v", name, env)
		}
	}
}

// proxyEnvNames is every environment variable an HTTP client in a
// container takes as "send everything through this" -- Go's
// http.ProxyFromEnvironment, curl's, apt's http method, and the set
// BuildKit passes into a build without the Dockerfile asking for it.
var proxyEnvNames = []string{
	"HTTP_PROXY", "http_proxy",
	"HTTPS_PROXY", "https_proxy",
	"ALL_PROXY", "all_proxy",
	"FTP_PROXY", "ftp_proxy",
	"NO_PROXY", "no_proxy",
}

func TestChildContainerEnvFileIsWhatDockerCanRead(t *testing.T) {
	file, err := ChildContainerEnvFile("http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("ChildContainerEnvFile: %v", err)
	}
	if file.Path != ChildContainerEnvPath || file.Mode != "0644" {
		t.Errorf("file = %+v, want %s readable by the account the run's shells use", file, ChildContainerEnvPath)
	}
	// Parsed the way `docker run --env-file` parses one: a line is a
	// comment, blank, or KEY=value. A file docker cannot read is a file
	// that is worse than absent, since the command naming it fails.
	got := map[string]string{}
	for i, line := range strings.Split(file.Content, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("line %d is neither a comment nor an assignment, so docker would refuse the file: %q", i+1, line)
		}
		got[key] = value
	}
	want, err := ChildContainerEnv("http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("ChildContainerEnv: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("the file assigns %v, want exactly %v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("the file sets %s=%q, want %q", name, got[name], value)
		}
	}
	// Every value names this service and nothing else, which is the
	// property that makes handing all four to a Dockerfile grain has never
	// read a safe thing to do.
	for name, value := range got {
		if strings.Contains(value, "://") && !strings.Contains(value, "10.9.0.1:41234") {
			t.Errorf("%s=%q names a host that is not the cache", name, value)
		}
	}
	// The file is the only place a run is told how to use it, so the two
	// invocations are part of what it has to contain.
	for _, want := range []string{"--env-file", "--build-arg", "ARG"} {
		if !strings.Contains(file.Content, want) {
			t.Errorf("the file does not say %q, so nothing tells a run how to pass it in:\n%s", want, file.Content)
		}
	}
}

// TestChildContainerEnvFileIsTheSameTwice is about the recreate path,
// which configures a sandbox that has already been configured: two
// identical writes should leave identical bytes rather than reordering a
// map's keys under a run that had read the file.
func TestChildContainerEnvFileIsTheSameTwice(t *testing.T) {
	first, err := ChildContainerEnvFile("http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("ChildContainerEnvFile: %v", err)
	}
	for i := 0; i < 8; i++ {
		again, err := ChildContainerEnvFile("http://10.9.0.1:41234")
		if err != nil {
			t.Fatalf("ChildContainerEnvFile: %v", err)
		}
		if again.Content != first.Content {
			t.Fatalf("two calls disagree:\n%s\n---\n%s", first.Content, again.Content)
		}
	}
}

// TestChildContainerEnvLeavesTheRestOfTheInternetAlone is the claim this
// whole shape exists to keep, made against a real client and a real cache
// rather than against a list of names: a process holding exactly the
// environment a container is handed can still fetch an https:// URL and a
// plain-HTTP URL for a host this cache does not serve.
//
// Those are the two failures a4aa27e41 caused on 2026-09-18 by writing
// proxies.default.httpProxy into ~/.docker/config.json, and they are why
// there is no proxy here. The second half of this test puts that proxy
// back, in the same child process against the same two servers, and
// requires both fetches to fail -- so the first half is a claim about
// something that could have gone wrong rather than a fetch that was never
// in any danger.
//
// In a child process because the environment is the thing under test:
// http.ProxyFromEnvironment reads the environment once for the life of a
// process, and a test that set it in this one would be telling every
// other test in this package where to send its requests.
func TestChildContainerEnvLeavesTheRestOfTheInternetAlone(t *testing.T) {
	s, _ := newTestServer(t, Config{})
	cache := httptest.NewServer(s)
	defer cache.Close()

	// An ordinary plain-HTTP host that is not a distribution archive --
	// the cache's allowlist has never heard of it, and refuses it with a
	// 403 when it is asked to fetch it.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, probeBody)
	}))
	defer plain.Close()
	// And an https:// one -- a host an image's own build fetches over TLS,
	// which reaches a proxy as a CONNECT, and which this cache refuses to
	// tunnel to because it is not a host it serves.
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, probeBody)
	}))
	defer secure.Close()

	// Both are addressed by name rather than by the loopback address they
	// are really on, and the child is given the mapping to dial. Two
	// reasons, and the first is the one that decides it: every HTTP client
	// there is exempts a loopback target from its proxy settings (Go's
	// httpproxy, curl's, apt's), so a test whose targets were
	// 127.0.0.1:NNNNN would find both fetches working under a proxy and
	// would be asserting nothing at all. The second is that this is what a
	// container actually does -- it fetches deb.debian.org by name.
	// "example.com" because httptest's own certificate is issued for it,
	// which saves this test a certificate authority of its own.
	const plainHost, secureHost = "mirror.example.org", "example.com"
	plainPort := strings.TrimPrefix(plain.URL, "http://")
	securePort := strings.TrimPrefix(secure.URL, "https://")

	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: secure.Certificate().Raw,
	}), 0o644); err != nil {
		t.Fatalf("writing the https server's certificate: %v", err)
	}

	env, err := ChildContainerEnv(cache.URL)
	if err != nil {
		t.Fatalf("ChildContainerEnv: %v", err)
	}
	// A clean environment rather than this process's: a stray http_proxy
	// on the machine running the tests would otherwise decide the answer.
	childEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.Getenv("TMPDIR"),
		"GRAIN_CHILD_CONTAINER_PROBE=" +
			"http://" + plainHost + ":" + portOf(t, plainPort) + "/hello " +
			"https://" + secureHost + ":" + portOf(t, securePort) + "/hello",
		"GRAIN_CHILD_CONTAINER_PROBE_CA=" + ca,
		"GRAIN_CHILD_CONTAINER_PROBE_DIAL=" +
			plainHost + "=" + plainPort + " " + secureHost + "=" + securePort,
	}
	for name, value := range env {
		childEnv = append(childEnv, name+"="+value)
	}

	out, err := runChildProbe(t, childEnv)
	if err != nil {
		t.Errorf("a process given exactly what a container is given could not reach an ordinary host: %v\n%s", err, out)
	}

	// Now the proxy, exactly as the docker client used to pass it in.
	withProxy := append(append([]string{}, childEnv...),
		"HTTP_PROXY="+cache.URL, "http_proxy="+cache.URL,
		"HTTPS_PROXY="+cache.URL, "https_proxy="+cache.URL)
	out, err = runChildProbe(t, withProxy)
	if err == nil {
		t.Fatalf("a process proxied through the package cache reached the whole internet, so this test is "+
			"asserting nothing:\n%s", out)
	}
	// The two refusals by name, so that a future cache which answered
	// these differently could not pass this test by failing for some
	// other reason. Both are the allowlist's: neither of these hosts is
	// one the cache fetches from, over either scheme. (An archive it does
	// serve is tunnelled rather than refused now -- connect.go -- which is
	// what stops the same proxy being a hard build failure, and is not
	// what makes it the wrong thing to hand a container.)
	if !strings.Contains(out, "403") {
		t.Errorf("a plain-HTTP fetch of a host the cache does not serve did not get the allowlist's 403:\n%s", out)
	}
	// A refused CONNECT reaches the client as the status line and nothing
	// else -- there is no response to read a body from, and Go's transport
	// keeps only the reason phrase -- which is why this one is matched on
	// the text rather than on the number, and is also why the status an
	// agent sees in a broken build says so little about where it came from.
	if !strings.Contains(out, "Forbidden") {
		t.Errorf("an https:// fetch through the cache did not get the CONNECT refusal's 403:\n%s", out)
	}
}

// probeBody is what both stand-in hosts answer with, so that the child
// can tell a real answer from a proxy's explanation of why there is none.
const probeBody = "the host answered for itself"

// portOf is the port half of a "host:port" a stand-in server is
// listening on.
func portOf(t *testing.T, hostPort string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("splitting %q: %v", hostPort, err)
	}
	return port
}

// runChildProbe runs TestChildContainerEnvProbe in a process of its own,
// with env and nothing else.
func runChildProbe(t *testing.T, env []string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildContainerEnvProbe$", "-test.v")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestChildContainerEnvProbe is the child half of the test above: it
// fetches each URL it is given and fails if any of them did not answer
// for itself. It does nothing at all when run as part of an ordinary
// `go test`, which is what the environment variable distinguishes.
func TestChildContainerEnvProbe(t *testing.T) {
	targets := strings.Fields(os.Getenv("GRAIN_CHILD_CONTAINER_PROBE"))
	if len(targets) == 0 {
		t.Skip("the child half of TestChildContainerEnvLeavesTheRestOfTheInternetAlone, which runs it itself")
	}
	pool := x509.NewCertPool()
	if ca := os.Getenv("GRAIN_CHILD_CONTAINER_PROBE_CA"); ca != "" {
		bytes, err := os.ReadFile(ca)
		if err != nil {
			t.Fatalf("reading the https server's certificate: %v", err)
		}
		pool.AppendCertsFromPEM(bytes)
	}
	// Where each of the names in those URLs really listens. This is the
	// child's /etc/hosts, and nothing else about the request changes:
	// the URL, the Host header and whatever a proxy would be told to
	// fetch are all still the name.
	dial := map[string]string{}
	for _, entry := range strings.Fields(os.Getenv("GRAIN_CHILD_CONTAINER_PROBE_DIAL")) {
		if name, addr, ok := strings.Cut(entry, "="); ok {
			dial[name] = addr
		}
	}
	client := &http.Client{Transport: &http.Transport{
		// The point of the whole exercise: whatever this process's
		// environment says about proxies is what a container's curl, apt
		// and pip would each do with it too.
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, _, err := net.SplitHostPort(addr); err == nil {
				if to, ok := dial[host]; ok {
					addr = to
				}
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	for _, target := range targets {
		resp, err := client.Get(target)
		if err != nil {
			t.Errorf("GET %s: %v", target, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("GET %s -> %d %q", target, resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != probeBody {
			t.Errorf("GET %s answered %d, not the host itself", target, resp.StatusCode)
		}
	}
}

func TestChildContainerEnvRefusesAnUnusableAddress(t *testing.T) {
	for _, bad := range []string{"", "not a url", "https://example.com", "http://"} {
		if _, err := ChildContainerEnv(bad); err == nil {
			t.Errorf("ChildContainerEnv(%q) was accepted", bad)
		}
		if _, err := ChildContainerEnvFile(bad); err == nil {
			t.Errorf("ChildContainerEnvFile(%q) was accepted", bad)
		}
	}
}
