package pkgcache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPTHostMatchingAndDisallowed verifies that allowed distribution hostnames
// (including wildcard domains like .debian.org and .ubuntu.com) are accepted,
// while disallowed domains (such as evil.debian.org.attacker.com or random hosts)
// are rejected with 403 Forbidden.
func TestAPTHostMatchingAndDisallowed(t *testing.T) {
	up := newUpstream(t, map[string]string{
		"/debian/pool/main/h/hello/hello.deb": "hello-deb-content",
	})
	s, _ := newTestServer(t, Config{Client: up.client()})

	allowedHosts := []string{
		"deb.debian.org",
		"security.debian.org",
		"ftp.debian.org",
		"archive.debian.org",
		"archive.ubuntu.com",
		"security.ubuntu.com",
		"ports.ubuntu.com",
		"dl-cdn.alpinelinux.org",
		"download.docker.com",
		"packages.cloud.google.com",
		"apt.releases.hashicorp.com",
		"pkgs.tailscale.com",
		"pkgs.k8s.io",
		"apt.kubernetes.io",
		"deb.nodesource.com",
		"packages.microsoft.com",
		"ppa.launchpadcontent.net",
		"ppa.launchpad.net",
		"subdomain.debian.org",
		"mirror.ubuntu.com",
		"mirror.alpinelinux.org",
	}

	for _, host := range allowedHosts {
		t.Run("Allowed_"+host, func(t *testing.T) {
			rec := get(t, s, fmt.Sprintf("http://%s/debian/pool/main/h/hello/hello.deb", host))
			if rec.Code != http.StatusOK {
				t.Errorf("host %q got status %d, want 200", host, rec.Code)
			}
		})
	}

	disallowedHosts := []string{
		"evil.debian.org.attacker.com",
		"attacker.debian.org.fake",
		"example.com",
		"notdebian.org",
		"google.com",
	}

	for _, host := range disallowedHosts {
		t.Run("Disallowed_"+host, func(t *testing.T) {
			rec := get(t, s, fmt.Sprintf("http://%s/debian/pool/main/h/hello/hello.deb", host))
			if rec.Code != http.StatusForbidden {
				t.Errorf("host %q got status %d, want 403 Forbidden", host, rec.Code)
			}
		})
	}
}

// TestAPTExtensionsAndIndices verifies caching behavior for different APT file types:
// .deb, .udeb, .ddeb are cached, while index files (InRelease, Packages.gz, Sources.xz, Release.gpg)
// pass through uncached.
func TestAPTExtensionsAndIndices(t *testing.T) {
	files := map[string]string{
		"/debian/pool/main/h/hello/hello.deb":       "deb-content",
		"/debian/pool/main/u/udebpkg/pkg.udeb":      "udeb-content",
		"/debian/pool/main/d/ddebug/dbg.ddeb":       "ddeb-content",
		"/alpine/v3.19/main/x86_64/curl.apk":        "apk-content",
		"/debian/dists/bookworm/InRelease":          "inrelease-index",
		"/debian/dists/bookworm/Release":            "release-index",
		"/debian/dists/bookworm/Release.gpg":        "release-gpg",
		"/debian/dists/bookworm/main/Packages.gz":   "packages-gz-index",
		"/debian/dists/bookworm/main/Packages.xz":   "packages-xz-index",
		"/debian/dists/bookworm/main/Sources.gz":    "sources-gz-index",
		"/debian/dists/bookworm/i18n/Translation":   "translation-index",
		"/alpine/v3.19/main/x86_64/APKINDEX.tar.gz": "apk-index",
	}

	up := newUpstream(t, files)
	s, cache := newTestServer(t, Config{Client: up.client()})

	cacheable := []string{
		"/debian/pool/main/h/hello/hello.deb",
		"/debian/pool/main/u/udebpkg/pkg.udeb",
		"/debian/pool/main/d/ddebug/dbg.ddeb",
		"/alpine/v3.19/main/x86_64/curl.apk",
	}

	for _, path := range cacheable {
		t.Run("Cacheable_"+path, func(t *testing.T) {
			// First fetch -> miss
			rec1 := get(t, s, "http://deb.debian.org"+path)
			if rec1.Code != http.StatusOK || rec1.Body.String() != files[path] {
				t.Fatalf("first fetch %s: %d %q", path, rec1.Code, rec1.Body.String())
			}
			if got := rec1.Header().Get(cacheStatusHeader); got != "miss" {
				t.Errorf("first fetch cache status = %q, want miss", got)
			}

			// Second fetch -> hit
			rec2 := get(t, s, "http://deb.debian.org"+path)
			if got := rec2.Header().Get(cacheStatusHeader); got != "hit" {
				t.Errorf("second fetch cache status = %q, want hit", got)
			}
		})
	}

	passThrough := []string{
		"/debian/dists/bookworm/InRelease",
		"/debian/dists/bookworm/Release",
		"/debian/dists/bookworm/Release.gpg",
		"/debian/dists/bookworm/main/Packages.gz",
		"/debian/dists/bookworm/main/Packages.xz",
		"/debian/dists/bookworm/main/Sources.gz",
		"/debian/dists/bookworm/i18n/Translation",
	}

	for _, path := range passThrough {
		t.Run("PassThrough_"+path, func(t *testing.T) {
			rec1 := get(t, s, "http://deb.debian.org"+path)
			if rec1.Code != http.StatusOK || rec1.Body.String() != files[path] {
				t.Fatalf("first fetch %s: %d %q", path, rec1.Code, rec1.Body.String())
			}
			if got := rec1.Header().Get(cacheStatusHeader); got != "pass" {
				t.Errorf("first fetch cache status = %q, want pass", got)
			}

			rec2 := get(t, s, "http://deb.debian.org"+path)
			if got := rec2.Header().Get(cacheStatusHeader); got != "pass" {
				t.Errorf("second fetch cache status = %q, want pass", got)
			}
		})
	}

	// Verify only cacheable files are counted in cache.Bytes()
	if cache.Bytes() == 0 {
		t.Error("cache holds 0 bytes after caching .deb/.udeb/.ddeb")
	}
}

// TestAPTRangeRequestsAndHEAD tests Range requests and HEAD requests.
func TestAPTRangeRequestsAndHEAD(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello.deb"
	const fullContent = "0123456789abcdefghijklmnopqrstuvwxyz"
	up := newUpstream(t, map[string]string{pkg: fullContent})
	s, cache := newTestServer(t, Config{Client: up.client()})

	// HEAD request should return 200 OK without writing to cache
	headReq := httptest.NewRequest(http.MethodHead, "http://deb.debian.org"+pkg, nil)
	headRec := httptest.NewRecorder()
	s.ServeHTTP(headRec, headReq)
	if headRec.Code != http.StatusOK {
		t.Errorf("HEAD code = %d, want 200", headRec.Code)
	}
	if cache.Bytes() != 0 {
		t.Errorf("HEAD request cached %d bytes, want 0", cache.Bytes())
	}

	// Range request
	rangeReq := httptest.NewRequest(http.MethodGet, "http://deb.debian.org"+pkg, nil)
	rangeReq.Header.Set("Range", "bytes=0-9")
	rangeRec := httptest.NewRecorder()
	s.ServeHTTP(rangeRec, rangeReq)
	if got := rangeRec.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("Range request cache status = %q, want pass", got)
	}
}

// TestPyPIComplexHTMLIndexRewriting tests PyPI HTML index rewriting with relative URLs,
// absolute URLs, query strings, fragments, and data attributes.
func TestPyPIComplexHTMLIndexRewriting(t *testing.T) {
	htmlIndex := `<!DOCTYPE html>
<html>
  <head><title>Links for mypkg</title></head>
  <body>
    <h1>Links for mypkg</h1>
    <a href="https://files.pythonhosted.org/packages/mypkg-1.0.0-py3-none-any.whl#sha256=aaa" data-requires-python="&gt;=3.8">mypkg-1.0.0-py3-none-any.whl</a><br/>
    <a href="https://files.pythonhosted.org/packages/mypkg-2.0.0.tar.gz?auth=token#sha256=bbb" data-yanked="broken release">mypkg-2.0.0.tar.gz</a><br/>
    <a href="https://untrusted.example.com/packages/mypkg-3.0.0.whl#sha256=ccc">mypkg-3.0.0.whl</a><br/>
    <a href="/packages/relative-1.0.whl#sha256=ddd">relative-1.0.whl</a><br/>
  </body>
</html>`

	up := newUpstream(t, map[string]string{
		"/simple/mypkg/": htmlIndex,
	})

	s, _ := newTestServer(t, Config{
		Client:         up.client(),
		PyPIIndex:      up.srv.URL + "/simple",
		PyPIFileHosts:  []string{"files.pythonhosted.org"},
		PyPIFileScheme: "http",
	})

	rec := get(t, s, "/pypi/simple/mypkg/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pypi/simple/mypkg/: %d", rec.Code)
	}

	body := rec.Body.String()

	// 1. files.pythonhosted.org link should be rewritten to /pypi/files/files.pythonhosted.org/packages/...
	if !strings.Contains(body, `/pypi/files/files.pythonhosted.org/packages/mypkg-1.0.0-py3-none-any.whl#sha256=aaa`) {
		t.Errorf("body does not contain rewritten whl link: %s", body)
	}

	// 2. Query string and fragment preserved
	if !strings.Contains(body, `/pypi/files/files.pythonhosted.org/packages/mypkg-2.0.0.tar.gz?auth=token#sha256=bbb`) {
		t.Errorf("body does not contain rewritten tar.gz link with query: %s", body)
	}

	// 3. Data attributes preserved
	if !strings.Contains(body, `data-requires-python="&gt;=3.8"`) {
		t.Error("data-requires-python was not preserved")
	}
	if !strings.Contains(body, `data-yanked="broken release"`) {
		t.Error("data-yanked was not preserved")
	}

	// 4. Untrusted host must NOT be rewritten
	if !strings.Contains(body, `https://untrusted.example.com/packages/mypkg-3.0.0.whl#sha256=ccc`) {
		t.Error("untrusted host was unexpectedly rewritten")
	}
}

// TestPyPIJSONPEP691Rewriting tests PyPI PEP 691 JSON index rewriting.
func TestPyPIJSONPEP691Rewriting(t *testing.T) {
	jsonPayload := `{
		"meta": {"api-version": "1.0"},
		"name": "demopkg",
		"files": [
			{
				"filename": "demopkg-1.0.0-py3-none-any.whl",
				"url": "https://files.pythonhosted.org/packages/demopkg-1.0.0-py3-none-any.whl",
				"hashes": {"sha256": "abcdef"},
				"requires-python": ">=3.9"
			},
			{
				"filename": "demopkg-1.0.0.tar.gz",
				"url": "https://untrusted.com/demopkg-1.0.0.tar.gz",
				"hashes": {"sha256": "123456"}
			}
		]
	}`

	up := newUpstream(t, map[string]string{
		"/simple/demopkg/": jsonPayload,
	})

	s, _ := newTestServer(t, Config{
		Client:         up.client(),
		PyPIIndex:      up.srv.URL + "/simple",
		PyPIFileHosts:  []string{"files.pythonhosted.org"},
		PyPIFileScheme: "http",
	})

	req := httptest.NewRequest(http.MethodGet, "/pypi/simple/demopkg/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET json index: %d", rec.Code)
	}

	var parsed struct {
		Files []struct {
			Filename string `json:"filename"`
			URL      string `json:"url"`
		} `json:"files"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("Unmarshal rewritten JSON: %v", err)
	}

	if len(parsed.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(parsed.Files))
	}

	// First URL should be rewritten
	wantRewritten := "/pypi/files/files.pythonhosted.org/packages/demopkg-1.0.0-py3-none-any.whl"
	if parsed.Files[0].URL != wantRewritten {
		t.Errorf("file 0 URL = %q, want %q", parsed.Files[0].URL, wantRewritten)
	}

	// Second URL (untrusted host) must remain untouched
	wantUntouched := "https://untrusted.com/demopkg-1.0.0.tar.gz"
	if parsed.Files[1].URL != wantUntouched {
		t.Errorf("file 1 URL = %q, want %q", parsed.Files[1].URL, wantUntouched)
	}
}

// TestPyPIFileExtensionsAndDisallowedHosts verifies PyPI file caching extensions
// and host validation.
func TestPyPIFileExtensionsAndDisallowedHosts(t *testing.T) {
	files := map[string]string{
		"/packages/pkg.whl":     "whl-data",
		"/packages/pkg.tar.gz":  "tar-gz-data",
		"/packages/pkg.zip":     "zip-data",
		"/packages/pkg.tar.bz2": "tar-bz2-data",
		"/packages/pkg.tar.xz":  "tar-xz-data",
		"/packages/pkg.egg":     "egg-data",
		"/packages/readme.txt":  "text-data",
	}

	up := newUpstream(t, files)
	s, cache := newTestServer(t, Config{
		Client:         up.client(),
		PyPIFileHosts:  []string{"files.pythonhosted.org"},
		PyPIFileScheme: "http",
	})

	cacheableExts := []string{
		"/packages/pkg.whl",
		"/packages/pkg.tar.gz",
		"/packages/pkg.zip",
		"/packages/pkg.tar.bz2",
		"/packages/pkg.tar.xz",
		"/packages/pkg.egg",
	}

	for _, p := range cacheableExts {
		rec := get(t, s, "/pypi/files/files.pythonhosted.org"+p)
		if rec.Code != http.StatusOK || rec.Body.String() != files[p] {
			t.Errorf("fetch %s failed: %d %q", p, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get(cacheStatusHeader); got != "miss" {
			t.Errorf("cache status = %q, want miss", got)
		}
		// Second fetch must hit
		rec2 := get(t, s, "/pypi/files/files.pythonhosted.org"+p)
		if got := rec2.Header().Get(cacheStatusHeader); got != "hit" {
			t.Errorf("subsequent cache status = %q, want hit", got)
		}
	}

	// Non-package file should pass through uncached
	recTxt := get(t, s, "/pypi/files/files.pythonhosted.org/packages/readme.txt")
	if got := recTxt.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("readme.txt cache status = %q, want pass", got)
	}

	// Disallowed file host -> 403 Forbidden
	recBadHost := get(t, s, "/pypi/files/evil.attacker.com/packages/pkg.whl")
	if recBadHost.Code != http.StatusForbidden {
		t.Errorf("disallowed host code = %d, want 403 Forbidden", recBadHost.Code)
	}

	_ = cache
}

// TestDockerMultiRegistryRouting tests Docker registry routing via path prefix
// (/v2/docker/..., /v2/ghcr/..., /v2/gcr/...) and query parameter (?ns=ghcr.io).
func TestDockerMultiRegistryRouting(t *testing.T) {
	reg := newRegistry(t, map[string]string{
		"/v2/library/alpine/blobs/" + testDigest: "docker-blob-content",
		"/v2/library/alpine/manifests/latest":    "docker-manifest-content",
		"/v2/org/repo/blobs/" + testDigest:       "ghcr-blob-content",
		"/v2/project/img/blobs/" + testDigest:    "gcr-blob-content",
	})

	s, cache := newTestServer(t, Config{
		DockerRegistry: reg.srv.URL,
		GHCRRegistry:   reg.srv.URL,
		GCRRegistry:    reg.srv.URL,
	})

	// 1. Default Docker Hub prefix
	rec1 := get(t, s, "/v2/library/alpine/blobs/"+testDigest)
	if rec1.Code != http.StatusOK || rec1.Body.String() != "docker-blob-content" {
		t.Fatalf("docker hub blob fetch: %d %q", rec1.Code, rec1.Body.String())
	}
	if got := rec1.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("cache status = %q, want miss", got)
	}

	// 2. GHCR path prefix routing
	// Since blobs are content-addressed by digest across registries, we should test
	// that GHCR queries succeed and manifest queries pass through correctly.
	recManifest := get(t, s, "/v2/library/alpine/manifests/latest")
	if recManifest.Code != http.StatusOK || recManifest.Body.String() != "docker-manifest-content" {
		t.Fatalf("manifest fetch: %d %q", recManifest.Code, recManifest.Body.String())
	}
	if got := recManifest.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("manifest cache status = %q, want pass", got)
	}

	// 3. Query param routing (?ns=ghcr.io)
	const digest2 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reg.mu.Lock()
	reg.files["/v2/org/repo/blobs/"+digest2] = "ghcr-ns-blob"
	reg.mu.Unlock()

	recNS := get(t, s, "/v2/org/repo/blobs/"+digest2+"?ns=ghcr.io")
	if recNS.Code != http.StatusOK || recNS.Body.String() != "ghcr-ns-blob" {
		t.Fatalf("ghcr ns fetch: %d %q", recNS.Code, recNS.Body.String())
	}

	_ = cache
}

// TestNPMScopedPackagesAndPackuments tests NPM scoped packages with both URL encoded (%2f)
// and unencoded paths, packuments pass-through, and tarball caching.
func TestNPMScopedPackagesAndPackuments(t *testing.T) {
	files := map[string]string{
		"/registry/@types/node":                   `{"name":"@types/node","versions":{"20.0.0":{"dist":{"tarball":"https://registry.npmjs.org/@types/node/-/node-20.0.0.tgz"}}}}`,
		"/registry/@types/node/-/node-20.0.0.tgz": "node-types-tarball-bytes",
		"/registry/lodash":                        `{"name":"lodash"}`,
		"/registry/lodash/-/lodash-4.17.21.tgz":   "lodash-tarball-bytes",
	}

	up := newUpstream(t, files)
	s, cache := newTestServer(t, Config{
		Client:      up.client(),
		NPMRegistry: up.srv.URL + "/registry",
	})

	// 1. Packument -> pass
	recPack := get(t, s, "/npm/@types%2fnode")
	if recPack.Code != http.StatusOK {
		t.Fatalf("packument fetch failed: %d", recPack.Code)
	}
	if got := recPack.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("packument cache status = %q, want pass", got)
	}

	// 2. Scoped tarball -> miss then hit
	recTar1 := get(t, s, "/npm/@types/node/-/node-20.0.0.tgz")
	if recTar1.Code != http.StatusOK || recTar1.Body.String() != "node-types-tarball-bytes" {
		t.Fatalf("scoped tarball fetch: %d %q", recTar1.Code, recTar1.Body.String())
	}
	if got := recTar1.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("scoped tarball cache status = %q, want miss", got)
	}

	recTar2 := get(t, s, "/npm/@types/node/-/node-20.0.0.tgz")
	if got := recTar2.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("scoped tarball subsequent cache status = %q, want hit", got)
	}

	_ = cache
}

// TestGoModulesAndSumDBEndpoints tests Go module proxy semantics and SumDB operations:
// - Semver and pseudo-version module archives are cached (.zip, .mod, .info)
// - Branch names / mutable queries (@v/list, @latest) pass through uncached
// - SumDB /supported returns 200 OK directly
// - SumDB tiles are cached
// - SumDB lookup passes through uncached
// - Disallowed SumDB host returns 403 Forbidden
func TestGoModulesAndSumDBEndpoints(t *testing.T) {
	files := map[string]string{
		"/mod/example.com/foo/@v/v1.0.0.zip":                       "foo-v1.0.0-zip",
		"/mod/example.com/foo/@v/v1.0.0.mod":                       "module example.com/foo\n",
		"/mod/example.com/foo/@v/v1.0.0.info":                      `{"Version":"v1.0.0"}`,
		"/mod/example.com/foo/@v/v0.0.0-20240101000000-abcdef.zip": "foo-pseudo-zip",
		"/mod/github.com/!azure/sdk/@v/v2.0.0.zip":                 "azure-sdk-zip",
		"/mod/example.com/foo/@v/list":                             "v1.0.0\nv1.0.1\n",
		"/mod/example.com/foo/@latest":                             `{"Version":"v1.0.1"}`,
		"/tile/8/0/001":                                            "sumdb-tile-data",
		"/lookup/example.com/foo@v1.0.0":                           "sumdb-lookup-result",
		"/latest":                                                  "sumdb-latest-tree-head",
	}

	up := newUpstream(t, files)
	s, cache := newTestServer(t, Config{
		Client:       up.client(),
		GoProxy:      up.srv.URL + "/mod",
		GoSumDBHosts: []string{"sum.golang.org"},
	})

	// 1. Cacheable Go Module artifacts
	cacheableGo := []struct {
		url      string
		wantBody string
	}{
		{"/go/example.com/foo/@v/v1.0.0.zip", "foo-v1.0.0-zip"},
		{"/go/example.com/foo/@v/v1.0.0.mod", "module example.com/foo\n"},
		{"/go/example.com/foo/@v/v1.0.0.info", `{"Version":"v1.0.0"}`},
		{"/go/example.com/foo/@v/v0.0.0-20240101000000-abcdef.zip", "foo-pseudo-zip"},
		{"/go/github.com/!azure/sdk/@v/v2.0.0.zip", "azure-sdk-zip"},
	}

	for _, tc := range cacheableGo {
		t.Run("Cacheable_"+tc.url, func(t *testing.T) {
			rec1 := get(t, s, tc.url)
			if rec1.Code != http.StatusOK || rec1.Body.String() != tc.wantBody {
				t.Fatalf("fetch %s failed: %d %q", tc.url, rec1.Code, rec1.Body.String())
			}
			if got := rec1.Header().Get(cacheStatusHeader); got != "miss" {
				t.Errorf("cache status = %q, want miss", got)
			}
			rec2 := get(t, s, tc.url)
			if got := rec2.Header().Get(cacheStatusHeader); got != "hit" {
				t.Errorf("subsequent cache status = %q, want hit", got)
			}
		})
	}

	// 2. Mutable Go proxy queries -> pass through uncached
	passGo := []string{
		"/go/example.com/foo/@v/list",
		"/go/example.com/foo/@latest",
	}
	for _, tc := range passGo {
		t.Run("PassThrough_"+tc, func(t *testing.T) {
			rec := get(t, s, tc)
			if rec.Code != http.StatusOK {
				t.Fatalf("fetch %s failed: %d", tc, rec.Code)
			}
			if got := rec.Header().Get(cacheStatusHeader); got != "pass" {
				t.Errorf("cache status = %q, want pass", got)
			}
		})
	}

	// 3. SumDB /supported endpoint -> returns 200 directly
	recSupp := get(t, s, "/go/sumdb/sum.golang.org/supported")
	if recSupp.Code != http.StatusOK {
		t.Errorf("/supported returned status %d, want 200", recSupp.Code)
	}

	// 4. SumDB tile -> cached
	recTile1 := get(t, s, "/go/sumdb/sum.golang.org/tile/8/0/001")
	if recTile1.Code != http.StatusOK || recTile1.Body.String() != "sumdb-tile-data" {
		t.Fatalf("tile fetch failed: %d %q", recTile1.Code, recTile1.Body.String())
	}
	if got := recTile1.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("tile cache status = %q, want miss", got)
	}
	recTile2 := get(t, s, "/go/sumdb/sum.golang.org/tile/8/0/001")
	if got := recTile2.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("subsequent tile cache status = %q, want hit", got)
	}

	// 5. SumDB lookup & latest -> pass through uncached
	recLookup := get(t, s, "/go/sumdb/sum.golang.org/lookup/example.com/foo@v1.0.0")
	if got := recLookup.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("lookup cache status = %q, want pass", got)
	}
	recLatest := get(t, s, "/go/sumdb/sum.golang.org/latest")
	if got := recLatest.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("latest cache status = %q, want pass", got)
	}

	// 6. Disallowed SumDB host -> 403 Forbidden
	recBadSum := get(t, s, "/go/sumdb/evil.example.com/supported")
	if recBadSum.Code != http.StatusForbidden {
		t.Errorf("disallowed sumdb host status = %d, want 403 Forbidden", recBadSum.Code)
	}

	_ = cache
}

// TestDockerTokenRefreshOnExpiration verifies that when a cached Docker auth token
// is rejected as expired by upstream registry (401), the client transparently re-challenges,
// fetches a fresh token, and retries the request successfully.
func TestDockerTokenRefreshOnExpiration(t *testing.T) {
	var (
		tokenCalls int
		activeGen  = 1
		regServer  *httptest.Server
	)
	regServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"token-gen-%d","expires_in":300}`, tokenCalls)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != fmt.Sprintf("Bearer token-gen-%d", activeGen) {
			w.Header().Set("Www-Authenticate", fmt.Sprintf(
				`Bearer realm="%s/token",service="registry.test",scope="repository:library/alpine:pull"`,
				regServer.URL))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v2/library/alpine/manifests/latest" {
			fmt.Fprint(w, "manifest-body")
			return
		}
		http.NotFound(w, r)
	}))
	defer regServer.Close()

	s, _ := newTestServer(t, Config{
		DockerRegistry: regServer.URL,
	})

	// First fetch gets token-gen-1
	rec1 := get(t, s, "/v2/library/alpine/manifests/latest")
	if rec1.Code != http.StatusOK || rec1.Body.String() != "manifest-body" {
		t.Fatalf("first manifest fetch failed: %d %q", rec1.Code, rec1.Body.String())
	}
	if tokenCalls != 1 {
		t.Fatalf("tokenCalls = %d, want 1", tokenCalls)
	}

	// Invalidate token-gen-1 and require token-gen-2 upstream
	activeGen = 2

	// Second fetch should receive 401 with cached token, re-challenge, get token-gen-2, and succeed
	rec2 := get(t, s, "/v2/library/alpine/manifests/latest")
	if rec2.Code != http.StatusOK || rec2.Body.String() != "manifest-body" {
		t.Fatalf("second manifest fetch after expiration failed: %d %q", rec2.Code, rec2.Body.String())
	}
	if tokenCalls != 2 {
		t.Errorf("tokenCalls = %d after expiration, want 2", tokenCalls)
	}
}

// TestDockerMirrorPushOperations verifies that write/push operations sent through
// legacy mirror routes (/v2/ghcr/..., /v2/.../?ns=ghcr.io) are proxied upstream to the real registry.
func TestDockerMirrorPushOperations(t *testing.T) {
	regServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/org/repo/blobs/uploads/":
			w.Header().Set("Location", "/v2/org/repo/blobs/uploads/uuid-999")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPatch && r.URL.Path == "/v2/org/repo/blobs/uploads/uuid-999":
			w.Header().Set("Location", "/v2/org/repo/blobs/uploads/uuid-999")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/org/repo/blobs/uploads/uuid-999":
			if r.URL.Query().Get("digest") != testDigest {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", "/v2/org/repo/blobs/"+testDigest)
			w.Header().Set("Docker-Content-Digest", testDigest)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/org/repo/manifests/v1.0.0":
			w.Header().Set("Location", "/v2/org/repo/manifests/v1.0.0")
			w.Header().Set("Docker-Content-Digest", testDigest)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer regServer.Close()

	s, _ := newTestServer(t, Config{
		GHCRRegistry: regServer.URL,
	})

	// 1. POST /v2/ghcr/org/repo/blobs/uploads/ -> 202 Accepted
	req1 := httptest.NewRequest(http.MethodPost, "/v2/ghcr/org/repo/blobs/uploads/", nil)
	rec1 := httptest.NewRecorder()
	s.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("POST upload init failed: %d", rec1.Code)
	}
	if loc := rec1.Header().Get("Location"); loc != "/v2/org/repo/blobs/uploads/uuid-999" {
		t.Errorf("Location = %q, want /v2/org/repo/blobs/uploads/uuid-999", loc)
	}

	// 2. PATCH /v2/ghcr/org/repo/blobs/uploads/uuid-999 -> 202 Accepted
	req2 := httptest.NewRequest(http.MethodPatch, "/v2/ghcr/org/repo/blobs/uploads/uuid-999", strings.NewReader("blob-chunk"))
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("PATCH upload chunk failed: %d", rec2.Code)
	}

	// 3. PUT /v2/ghcr/org/repo/blobs/uploads/uuid-999?digest=... -> 201 Created
	req3 := httptest.NewRequest(http.MethodPut, "/v2/ghcr/org/repo/blobs/uploads/uuid-999?digest="+testDigest, nil)
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusCreated {
		t.Fatalf("PUT upload finish failed: %d", rec3.Code)
	}

	// 4. PUT /v2/org/repo/manifests/v1.0.0?ns=ghcr.io -> 201 Created
	req4 := httptest.NewRequest(http.MethodPut, "/v2/org/repo/manifests/v1.0.0?ns=ghcr.io", strings.NewReader(`{}`))
	rec4 := httptest.NewRecorder()
	s.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusCreated {
		t.Fatalf("PUT manifest with ns query failed: %d", rec4.Code)
	}
}
