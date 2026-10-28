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

package pkgcache

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitHubInfoRefsIsNotCached(t *testing.T) {
	var upstreamHits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/owner/repo.git/info/refs" {
			atomic.AddInt32(&upstreamHits, 1)
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			fmt.Fprint(w, "001e# service=git-upload-pack\n0000004895036130a102ff96004b4904d0f50d03247d0335 refs/heads/main\n0000")
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()

	s, cache := newTestServer(t, Config{
		GitHubURL: up.URL,
	})

	// First request: legacy path
	req := httptest.NewRequest(http.MethodGet, "/github/owner/repo.git/info/refs?service=git-upload-pack", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first info/refs request got status %d, want 200", rec.Code)
	}
	if rec.Header().Get(cacheStatusHeader) != "pass" {
		t.Errorf("first info/refs status header = %q, want %q", rec.Header().Get(cacheStatusHeader), "pass")
	}

	// Second request: legacy path
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second info/refs request got status %d, want 200", rec2.Code)
	}
	if rec2.Header().Get(cacheStatusHeader) != "pass" {
		t.Errorf("second info/refs status header = %q, want %q", rec2.Header().Get(cacheStatusHeader), "pass")
	}
	if hits := atomic.LoadInt32(&upstreamHits); hits != 2 {
		t.Errorf("upstream hits for info/refs = %d, want 2 (never cached)", hits)
	}

	// Check that cache has 0 entries for github
	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for _, st := range stats {
		if st.Kind == KindGitHub && st.Entries > 0 {
			t.Errorf("cache holds %d github entries, want 0 after info/refs requests", st.Entries)
		}
	}
}

func TestGitHubUploadPackIsCached(t *testing.T) {
	var upstreamHits int32
	packData := []byte("0008NAK\n0000PACKfake-packfile-bytes-1234567890")

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/owner/repo.git/git-upload-pack" && r.Method == http.MethodPost {
			atomic.AddInt32(&upstreamHits, 1)
			w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			w.Write(packData)
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()

	s, cache := newTestServer(t, Config{
		GitHubURL: up.URL,
	})

	body := []byte("0032want 95036130a102ff96004b4904d0f50d03247d0335\n00000009done\n")

	// First POST: miss, caches response
	req := httptest.NewRequest(http.MethodPost, "/github/owner/repo.git/git-upload-pack", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first upload-pack got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(cacheStatusHeader) != "miss" {
		t.Errorf("first upload-pack status header = %q, want %q", rec.Header().Get(cacheStatusHeader), "miss")
	}
	if !bytes.Equal(rec.Body.Bytes(), packData) {
		t.Errorf("first upload-pack body mismatch: got %q, want %q", rec.Body.String(), string(packData))
	}

	// Second POST with exact same body: hit, served from disk!
	req2 := httptest.NewRequest(http.MethodPost, "/github/owner/repo.git/git-upload-pack", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second upload-pack got %d: %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get(cacheStatusHeader) != "hit" {
		t.Errorf("second upload-pack status header = %q, want %q", rec2.Header().Get(cacheStatusHeader), "hit")
	}
	if !bytes.Equal(rec2.Body.Bytes(), packData) {
		t.Errorf("second upload-pack body mismatch: got %q, want %q", rec2.Body.String(), string(packData))
	}
	if hits := atomic.LoadInt32(&upstreamHits); hits != 1 {
		t.Errorf("upstream hits = %d, want 1 (second served from cache)", hits)
	}

	// Third POST with different body: miss, fetches upstream
	diffBody := []byte("0032want aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n00000009done\n")
	req3 := httptest.NewRequest(http.MethodPost, "/github/owner/repo.git/git-upload-pack", bytes.NewReader(diffBody))
	req3.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusOK {
		t.Fatalf("third upload-pack got %d", rec3.Code)
	}
	if rec3.Header().Get(cacheStatusHeader) != "miss" {
		t.Errorf("third upload-pack status header = %q, want %q", rec3.Header().Get(cacheStatusHeader), "miss")
	}
	if hits := atomic.LoadInt32(&upstreamHits); hits != 2 {
		t.Errorf("upstream hits after diff body = %d, want 2", hits)
	}

	// Verify stats
	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	var githubEntries int
	for _, st := range stats {
		if st.Kind == KindGitHub {
			githubEntries = st.Entries
		}
	}
	if githubEntries != 2 {
		t.Errorf("github cache entries = %d, want 2", githubEntries)
	}
}

func TestGitHubReleaseAndArchiveCaching(t *testing.T) {
	files := map[string]string{
		"/owner/repo/releases/download/v1.0.0/tool-linux-amd64.tar.gz":        "release-binary-tarball",
		"/owner/repo/archive/refs/tags/v1.0.0.tar.gz":                         "tag-source-tarball",
		"/owner/repo/archive/v1.0.0.zip":                                      "tag-zipball",
		"/owner/repo/archive/refs/heads/main.tar.gz":                          "mutable-main-tarball",
		"/owner/repo/archive/1111222233334444555566667777888899990000.tar.gz": "commit-sha-tarball",
	}

	up := newUpstream(t, files)
	s, _ := newTestServer(t, Config{
		GitHubURL: up.srv.URL,
		Client:    up.client(),
	})

	// 1. Release download should be cached
	rec := get(t, s, "http://github.com/owner/repo/releases/download/v1.0.0/tool-linux-amd64.tar.gz")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "miss" {
		t.Errorf("first release download got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
	rec = get(t, s, "http://github.com/owner/repo/releases/download/v1.0.0/tool-linux-amd64.tar.gz")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "hit" {
		t.Errorf("second release download got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}

	// 2. Tag archive should be cached
	rec = get(t, s, "http://github.com/owner/repo/archive/refs/tags/v1.0.0.tar.gz")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "miss" {
		t.Errorf("first tag archive got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
	rec = get(t, s, "http://github.com/owner/repo/archive/refs/tags/v1.0.0.tar.gz")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "hit" {
		t.Errorf("second tag archive got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}

	// 3. Commit SHA archive should be cached
	rec = get(t, s, "http://github.com/owner/repo/archive/1111222233334444555566667777888899990000.tar.gz")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "miss" {
		t.Errorf("first commit sha archive got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
	rec = get(t, s, "http://github.com/owner/repo/archive/1111222233334444555566667777888899990000.tar.gz")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "hit" {
		t.Errorf("second commit sha archive got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}

	// 4. Branch archive (refs/heads/main) MUST NOT be cached (mutable)
	rec = get(t, s, "http://github.com/owner/repo/archive/refs/heads/main.tar.gz")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "pass" {
		t.Errorf("first branch archive got code %d, status %q, want 'pass'", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
	rec = get(t, s, "http://github.com/owner/repo/archive/refs/heads/main.tar.gz")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "pass" {
		t.Errorf("second branch archive got code %d, status %q, want 'pass'", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
}

func TestGitHubRawCommitContentIsCached(t *testing.T) {
	files := map[string]string{
		"/owner/repo/1111222233334444555566667777888899990000/README.md": "# Hello World",
		"/owner/repo/main/README.md":                                     "# Mutable Hello World",
	}

	up := newUpstream(t, files)
	s, _ := newTestServer(t, Config{
		Client: up.client(),
	})

	// Raw file with commit SHA: cacheable
	rec := get(t, s, "http://raw.githubusercontent.com/owner/repo/1111222233334444555566667777888899990000/README.md")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "miss" {
		t.Errorf("first raw commit file got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
	rec = get(t, s, "http://raw.githubusercontent.com/owner/repo/1111222233334444555566667777888899990000/README.md")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "hit" {
		t.Errorf("second raw commit file got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}

	// Raw file with branch 'main': mutable, pass-through
	rec = get(t, s, "http://raw.githubusercontent.com/owner/repo/main/README.md")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "pass" {
		t.Errorf("branch raw file got code %d, status %q, want 'pass'", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
}

func TestGitHubRealGitCloneE2E(t *testing.T) {
	// Create a real bare git repository to act as upstream
	upstreamDir := t.TempDir()
	initCmd := exec.Command("git", "init", "--bare", upstreamDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init bare: %v: %s", err, string(out))
	}

	// Create a work repo to commit files and push to bare upstream
	workDir := t.TempDir()
	runGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, string(out))
		}
	}

	runGit(workDir, "init")
	runGit(workDir, "config", "user.name", "Test")
	runGit(workDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(workDir, "hello.txt"), []byte("hello from git repo!"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit(workDir, "add", "hello.txt")
	runGit(workDir, "commit", "-m", "Initial commit")
	runGit(workDir, "remote", "add", "origin", upstreamDir)
	runGit(workDir, "push", "origin", "HEAD:main")
	runGit(upstreamDir, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(workDir, "push", "origin", "HEAD:refs/tags/v1.0.0")

	// Set up git http backend handler for upstreamDir
	gitHTTPHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := exec.Command("git", "http-backend")
		cmd.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+filepath.Dir(upstreamDir),
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path,
			"REMOTE_USER=test",
			"REMOTE_ADDR="+r.RemoteAddr,
			"QUERY_STRING="+r.URL.RawQuery,
			"REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"),
			"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"HTTP_GIT_PROTOCOL="+r.Header.Get("Git-Protocol"),
		)
		cmd.Stdin = r.Body
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			http.Error(w, fmt.Sprintf("git backend error: %v, stderr: %s", err, stderr.String()), http.StatusInternalServerError)
			return
		}

		outBytes := stdout.Bytes()
		// Parse CGI response headers
		headerEnd := bytes.Index(outBytes, []byte("\r\n\r\n"))
		bodyStart := headerEnd + 4
		if headerEnd == -1 {
			headerEnd = bytes.Index(outBytes, []byte("\n\n"))
			bodyStart = headerEnd + 2
		}
		if headerEnd != -1 {
			headerLines := strings.Split(string(outBytes[:headerEnd]), "\n")
			for _, line := range headerLines {
				line = strings.TrimSpace(line)
				if idx := strings.Index(line, ":"); idx != -1 {
					key := strings.TrimSpace(line[:idx])
					val := strings.TrimSpace(line[idx+1:])
					w.Header().Set(key, val)
				}
			}
			w.WriteHeader(http.StatusOK)
			w.Write(outBytes[bodyStart:])
		} else {
			w.Write(outBytes)
		}
	})

	upServer := httptest.NewServer(gitHTTPHandler)
	defer upServer.Close()

	repoName := filepath.Base(upstreamDir)

	s, cache := newTestServer(t, Config{
		GitHubURL: upServer.URL,
		Client:    upServer.Client(),
	})

	proxyServer := httptest.NewServer(s)
	defer proxyServer.Close()

	// Clone 1 via package cache
	cloneDir1 := t.TempDir()
	cloneURL := fmt.Sprintf("%s/github/%s", proxyServer.URL, repoName)
	cmdClone1 := exec.Command("git", "clone", cloneURL, cloneDir1)
	if out, err := cmdClone1.CombinedOutput(); err != nil {
		t.Fatalf("first git clone failed: %v: %s", err, string(out))
	}

	content1, err := os.ReadFile(filepath.Join(cloneDir1, "hello.txt"))
	if err != nil || string(content1) != "hello from git repo!" {
		t.Fatalf("cloned content = %q, err = %v", string(content1), err)
	}

	// Verify cache has entries
	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("cache.Stats: %v", err)
	}
	var githubEntries int
	for _, st := range stats {
		if st.Kind == KindGitHub {
			githubEntries = st.Entries
		}
	}
	if githubEntries < 1 {
		t.Errorf("expected github cache entries >= 1 after first clone, got %d", githubEntries)
	}

	// Clone 2 via package cache (should hit cache)
	cloneDir2 := t.TempDir()
	cmdClone2 := exec.Command("git", "clone", cloneURL, cloneDir2)
	if out, err := cmdClone2.CombinedOutput(); err != nil {
		t.Fatalf("second git clone failed: %v: %s", err, string(out))
	}

	content2, err := os.ReadFile(filepath.Join(cloneDir2, "hello.txt"))
	if err != nil || string(content2) != "hello from git repo!" {
		t.Fatalf("second cloned content = %q, err = %v", string(content2), err)
	}
}

func TestGitHubFreshnessOnPush(t *testing.T) {
	// Verify that when a new commit is pushed upstream:
	// 1. info/refs returns the new ref advertisement
	// 2. git-upload-pack for the new commit fetches the new packfile from upstream (not stale!)
	upstreamDir := t.TempDir()
	exec.Command("git", "init", "--bare", upstreamDir).CombinedOutput()

	workDir := t.TempDir()
	runGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, string(out))
		}
	}

	runGit(workDir, "init")
	runGit(workDir, "config", "user.name", "Test")
	runGit(workDir, "config", "user.email", "test@example.com")
	os.WriteFile(filepath.Join(workDir, "version.txt"), []byte("v1"), 0644)
	runGit(workDir, "add", "version.txt")
	runGit(workDir, "commit", "-m", "v1 commit")
	runGit(workDir, "remote", "add", "origin", upstreamDir)
	runGit(workDir, "push", "origin", "HEAD:main")
	runGit(upstreamDir, "symbolic-ref", "HEAD", "refs/heads/main")

	gitHTTPHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := exec.Command("git", "http-backend")
		cmd.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+filepath.Dir(upstreamDir),
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path,
			"REMOTE_USER=test",
			"REMOTE_ADDR="+r.RemoteAddr,
			"QUERY_STRING="+r.URL.RawQuery,
			"REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"),
			"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"HTTP_GIT_PROTOCOL="+r.Header.Get("Git-Protocol"),
		)
		cmd.Stdin = r.Body
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			http.Error(w, fmt.Sprintf("git backend error: %v, stderr: %s", err, stderr.String()), http.StatusInternalServerError)
			return
		}

		outBytes := stdout.Bytes()
		headerEnd := bytes.Index(outBytes, []byte("\r\n\r\n"))
		bodyStart := headerEnd + 4
		if headerEnd == -1 {
			headerEnd = bytes.Index(outBytes, []byte("\n\n"))
			bodyStart = headerEnd + 2
		}
		if headerEnd != -1 {
			headerLines := strings.Split(string(outBytes[:headerEnd]), "\n")
			for _, line := range headerLines {
				line = strings.TrimSpace(line)
				if idx := strings.Index(line, ":"); idx != -1 {
					key := strings.TrimSpace(line[:idx])
					val := strings.TrimSpace(line[idx+1:])
					w.Header().Set(key, val)
				}
			}
			w.WriteHeader(http.StatusOK)
			w.Write(outBytes[bodyStart:])
		} else {
			w.Write(outBytes)
		}
	})

	upServer := httptest.NewServer(gitHTTPHandler)
	defer upServer.Close()

	repoName := filepath.Base(upstreamDir)

	s, cache := newTestServer(t, Config{
		GitHubURL: upServer.URL,
		Client:    upServer.Client(),
	})

	proxyServer := httptest.NewServer(s)
	defer proxyServer.Close()

	cloneURL := fmt.Sprintf("%s/github/%s", proxyServer.URL, repoName)

	// Clone 1: gets v1
	cloneDir1 := t.TempDir()
	if out, err := exec.Command("git", "clone", cloneURL, cloneDir1).CombinedOutput(); err != nil {
		t.Fatalf("clone 1 failed: %v: %s", err, string(out))
	}
	v1Content, _ := os.ReadFile(filepath.Join(cloneDir1, "version.txt"))
	if string(v1Content) != "v1" {
		t.Fatalf("clone 1 got %q, want 'v1'", string(v1Content))
	}

	// Push v2 commit to upstream repo
	os.WriteFile(filepath.Join(workDir, "version.txt"), []byte("v2-updated"), 0644)
	runGit(workDir, "add", "version.txt")
	runGit(workDir, "commit", "-m", "v2 commit")
	runGit(workDir, "push", "origin", "HEAD:main")

	// Clone 2: must get v2-updated (freshness guaranteed!)
	cloneDir2 := t.TempDir()
	if out, err := exec.Command("git", "clone", cloneURL, cloneDir2).CombinedOutput(); err != nil {
		t.Fatalf("clone 2 failed: %v: %s", err, string(out))
	}
	v2Content, _ := os.ReadFile(filepath.Join(cloneDir2, "version.txt"))
	if string(v2Content) != "v2-updated" {
		t.Fatalf("clone 2 got stale content %q, want 'v2-updated'", string(v2Content))
	}

	// Verify both v1 and v2 packfiles are stored in cache
	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("cache.Stats: %v", err)
	}
	for _, st := range stats {
		if st.Kind == KindGitHub && st.Entries < 2 {
			t.Errorf("expected at least 2 entries in github cache for both packfiles, got %d", st.Entries)
		}
	}
}

func TestGitHubCodeloadArchiveCaching(t *testing.T) {
	files := map[string]string{
		"/owner/repo/tar.gz/refs/tags/v1.0.0":                         "codeload-tag-tarball",
		"/owner/repo/zip/refs/tags/v1.0.0":                            "codeload-tag-zip",
		"/owner/repo/tar.gz/refs/heads/main":                          "codeload-mutable-main",
		"/owner/repo/tar.gz/1111222233334444555566667777888899990000": "codeload-commit-tarball",
	}

	up := newUpstream(t, files)
	s, _ := newTestServer(t, Config{
		Client: up.client(),
	})

	// Tag archive: cacheable
	rec := get(t, s, "http://codeload.github.com/owner/repo/tar.gz/refs/tags/v1.0.0")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "miss" {
		t.Errorf("first codeload tag archive got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
	rec = get(t, s, "http://codeload.github.com/owner/repo/tar.gz/refs/tags/v1.0.0")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "hit" {
		t.Errorf("second codeload tag archive got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}

	// Commit SHA archive: cacheable
	rec = get(t, s, "http://codeload.github.com/owner/repo/tar.gz/1111222233334444555566667777888899990000")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "miss" {
		t.Errorf("first codeload commit archive got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
	rec = get(t, s, "http://codeload.github.com/owner/repo/tar.gz/1111222233334444555566667777888899990000")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "hit" {
		t.Errorf("second codeload commit archive got code %d, status %q", rec.Code, rec.Header().Get(cacheStatusHeader))
	}

	// Branch archive: mutable, pass-through
	rec = get(t, s, "http://codeload.github.com/owner/repo/tar.gz/refs/heads/main")
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "pass" {
		t.Errorf("branch archive got code %d, status %q, want 'pass'", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
}

func TestGitHubPathTraversalRefused(t *testing.T) {
	s, _ := newTestServer(t, Config{})

	rec := get(t, s, "/github/../../etc/passwd")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("path traversal got code %d, want 400 Bad Request", rec.Code)
	}

	rec = get(t, s, "http://github.com/../../etc/passwd")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("transparent path traversal got code %d, want 400 Bad Request", rec.Code)
	}
}

// TestGitHubRefusesAnythingButGitAndDownloads holds the method gate
// shut: a sandbox with no network reaches this cache on purpose, so a
// GitHub route that forwarded any method would be a way to drive
// GitHub's API -- a POST to api.github.com carrying whatever the caller
// put in Authorization -- from a sandbox promised it could not. Every
// case here must be refused before upstream sees anything.
func TestGitHubRefusesAnythingButGitAndDownloads(t *testing.T) {
	var upstreamHits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer up.Close()
	s, _ := newTestServer(t, Config{GitHubURL: up.URL})

	for _, c := range []struct {
		method, target, host string
	}{
		{http.MethodPost, "/repos/acme/widgets/dispatches", "api.github.com"},
		{http.MethodPatch, "/repos/acme/widgets", "api.github.com"},
		{http.MethodDelete, "/repos/acme/widgets", "api.github.com"},
		{http.MethodPut, "/acme/widgets/contents/x", "raw.githubusercontent.com"},
		{http.MethodPost, "/acme/widgets/pulls", "github.com"},
		{http.MethodPost, "http://api.github.com/graphql", ""},
		{http.MethodPost, "/github/acme/widgets/issues", ""},
		{http.MethodDelete, "/github/acme/widgets.git/info/refs", ""},
		// A git endpoint's name on a host that is GitHub's API, not git:
		// the path alone is no gate.
		{http.MethodPost, "/repos/acme/widgets/git-upload-pack", "api.github.com"},
		{http.MethodPost, "/acme/widgets.git/git-receive-pack", "api.github.com"},
		{http.MethodPost, "/acme/widgets.git/git-receive-pack", "uploads.github.com"},
		{http.MethodPost, "/acme/widgets.git/git-upload-pack", "raw.githubusercontent.com"},
	} {
		req := httptest.NewRequest(c.method, c.target, strings.NewReader(`{"event_type":"x"}`))
		if c.host != "" {
			req.Host = c.host
		}
		req.Header.Set("Authorization", "Bearer sandbox-token")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s (Host %q) answered %d, want 405", c.method, c.target, c.host, rec.Code)
		}
	}
	if hits := atomic.LoadInt32(&upstreamHits); hits != 0 {
		t.Errorf("upstream saw %d requests, want none: a refused method must not be forwarded", hits)
	}

	// git's own POSTs still go through.
	for _, target := range []string{"/github/acme/widgets.git/git-receive-pack", "/acme/widgets.git/git-receive-pack"} {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("0000"))
		if !strings.HasPrefix(target, "/github/") {
			req.Host = "github.com"
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("POST %s was refused, want it forwarded: it is a git push", target)
		}
	}
}
