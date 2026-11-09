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

package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCodexDockerfileReleaseAPIFallback runs the actual `RUN if [ "$INSTALL_CODEX_CLI" = "1" ]`
// shell block out of both Dockerfile and Dockerfile.k8s-sandbox against a local HTTP server,
// verifying:
//  1. Healthy web `/releases/download/...` builds make zero calls to `api.github.com`.
//  2. When `/releases/download/...` returns HTTP 504, the script queries
//     `/repos/openai/codex/releases/tags/${CODEX_CLI_VERSION}` once, extracts each
//     matching asset URL (`/repos/openai/codex/releases/assets/<id>`), downloads it
//     with `Accept: application/octet-stream` across a 302 redirect to a release-assets
//     origin, verifies `CODEX_CLI_SHA256_*` and `CODEX_CODE_MODE_HOST_SHA256_*` before
//     unpacking, and removes `/tmp/codex-release.json`.
//  3. Corrupt bytes returned by the asset download fail SHA-256 verification before
//     unpacking while still cleaning up `/tmp/codex-release.json`.
//  4. When both `/releases/download/...` and the GitHub Releases API fail, `/tmp` is
//     cleaned up and the digest-pinned `@openai/codex` npm archive fallback succeeds.
func TestCodexDockerfileReleaseAPIFallback(t *testing.T) {
	for _, bin := range []string{"sh", "curl", "tar", "sha256sum", "grep", "awk", "find"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH: %v", bin, err)
		}
	}

	var target string
	switch runtime.GOARCH {
	case "amd64":
		target = "x86_64-unknown-linux-musl"
	case "arm64":
		target = "aarch64-unknown-linux-musl"
	default:
		t.Skipf("unsupported test arch %s", runtime.GOARCH)
	}

	codexScript := "#!/bin/sh\necho 'codex-cli 0.166.0'\n"
	hostScript := "#!/bin/sh\necho 'codex-code-mode-host help'\n"

	codexTar, codexSHA := makeTarGz(t, map[string]string{
		"codex-" + target: codexScript,
	})
	hostTar, hostSHA := makeTarGz(t, map[string]string{
		"codex-code-mode-host-" + target: hostScript,
	})
	npmTar, npmSHA := makeTarGz(t, map[string]string{
		"package/vendor/" + target + "/bin/codex":                codexScript,
		"package/vendor/" + target + "/bin/codex-code-mode-host": hostScript,
	})

	for _, dfName := range []string{"Dockerfile", "Dockerfile.k8s-sandbox"} {
		dfText := read(t, dfName)
		rawScript := extractCodexInstallRunBlock(t, dfName, dfText)

		t.Run(dfName+"/web-download-consumes-zero-api-quota", func(t *testing.T) {
			var webHits, apiTagHits, npmHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/web/openai/codex/releases/download/rust-v0.166.0/codex-" + target + ".tar.gz":
					webHits.Add(1)
					_, _ = w.Write(codexTar)
				case "/web/openai/codex/releases/download/rust-v0.166.0/codex-code-mode-host-" + target + ".tar.gz":
					webHits.Add(1)
					_, _ = w.Write(hostTar)
				case "/api/repos/openai/codex/releases/tags/rust-v0.166.0":
					apiTagHits.Add(1)
					http.Error(w, "unexpected API call", http.StatusInternalServerError)
				default:
					if strings.HasPrefix(r.URL.Path, "/npm/") {
						npmHits.Add(1)
					}
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			tmpDir, binDir := runCodexInstallScript(t, rawScript, srv.URL, codexSHA, hostSHA, npmSHA, true)
			if webHits.Load() != 2 {
				t.Fatalf("web download hits = %d, want 2", webHits.Load())
			}
			if apiTagHits.Load() != 0 || npmHits.Load() != 0 {
				t.Fatalf("apiTagHits=%d npmHits=%d, want 0 and 0", apiTagHits.Load(), npmHits.Load())
			}
			assertTempCleanAndBinariesWork(t, tmpDir, binDir)
		})

		t.Run(dfName+"/504-falls-back-to-releases-api-octet-stream", func(t *testing.T) {
			var webHits, apiTagHits, assetHits, cdnHits, npmHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				baseURL := "http://" + r.Host
				switch r.URL.Path {
				case "/web/openai/codex/releases/download/rust-v0.166.0/codex-" + target + ".tar.gz",
					"/web/openai/codex/releases/download/rust-v0.166.0/codex-code-mode-host-" + target + ".tar.gz":
					webHits.Add(1)
					http.Error(w, "504 Gateway Time-out", http.StatusGatewayTimeout)
				case "/api/repos/openai/codex/releases/tags/rust-v0.166.0":
					apiTagHits.Add(1)
					release := map[string]any{
						"url":      baseURL + "/api/repos/openai/codex/releases/9001",
						"tag_name": "rust-v0.166.0",
						"body":     `Release notes with decoy JSON: {"url":"` + baseURL + `/api/repos/openai/codex/releases/assets/999","name":"codex-` + target + `.tar.gz"} and {{{ quotes \"`,
						"author": map[string]any{
							"url":  baseURL + "/api/users/bot",
							"name": "codex-" + target + ".tar.gz",
						},
						"assets": []map[string]any{
							{
								"url":   baseURL + "/api/repos/openai/codex/releases/assets/100",
								"id":    100,
								"name":  "codex-" + target + ".tar.gz.sha256",
								"label": nil,
							},
							{
								"url":   baseURL + "/api/repos/openai/codex/releases/assets/101",
								"id":    101,
								"label": nil,
								"name":  "codex-" + target + ".tar.gz",
								"uploader": map[string]any{
									"url":  baseURL + "/api/users/bot",
									"name": "codex-" + target + ".tar.gz",
								},
							},
							{
								"name":  "codex-code-mode-host-" + target + ".tar.gz",
								"id":    102,
								"label": nil,
								"url":   baseURL + "/api/repos/openai/codex/releases/assets/102",
							},
						},
					}
					w.Header().Set("Content-Type", "application/json")
					// Serve minified single-line JSON like api.github.com.
					_ = json.NewEncoder(w).Encode(release)
				case "/api/repos/openai/codex/releases/assets/101":
					if r.Header.Get("Accept") != "application/octet-stream" {
						http.Error(w, "missing Accept: application/octet-stream", http.StatusBadRequest)
						return
					}
					assetHits.Add(1)
					http.Redirect(w, r, baseURL+"/cdn/codex.tar.gz", http.StatusFound)
				case "/api/repos/openai/codex/releases/assets/102":
					if r.Header.Get("Accept") != "application/octet-stream" {
						http.Error(w, "missing Accept: application/octet-stream", http.StatusBadRequest)
						return
					}
					assetHits.Add(1)
					http.Redirect(w, r, baseURL+"/cdn/codex-host.tar.gz", http.StatusFound)
				case "/cdn/codex.tar.gz":
					cdnHits.Add(1)
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write(codexTar)
				case "/cdn/codex-host.tar.gz":
					cdnHits.Add(1)
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write(hostTar)
				default:
					if strings.HasPrefix(r.URL.Path, "/npm/") {
						npmHits.Add(1)
					}
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			tmpDir, binDir := runCodexInstallScript(t, rawScript, srv.URL, codexSHA, hostSHA, npmSHA, true)
			if webHits.Load() == 0 {
				t.Fatal("expected primary web download to be tried first")
			}
			if apiTagHits.Load() != 1 {
				t.Fatalf("apiTagHits = %d, want 1 (release metadata reused across both assets)", apiTagHits.Load())
			}
			if assetHits.Load() != 2 || cdnHits.Load() != 2 {
				t.Fatalf("assetHits=%d cdnHits=%d, want 2 and 2", assetHits.Load(), cdnHits.Load())
			}
			if npmHits.Load() != 0 {
				t.Fatalf("npmHits = %d, want 0 when GitHub Releases API succeeds", npmHits.Load())
			}
			assertTempCleanAndBinariesWork(t, tmpDir, binDir)
		})

		t.Run(dfName+"/api-digest-mismatch-rejected-before-unpack", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				baseURL := "http://" + r.Host
				switch r.URL.Path {
				case "/web/openai/codex/releases/download/rust-v0.166.0/codex-" + target + ".tar.gz",
					"/web/openai/codex/releases/download/rust-v0.166.0/codex-code-mode-host-" + target + ".tar.gz":
					http.Error(w, "504 Gateway Time-out", http.StatusGatewayTimeout)
				case "/api/repos/openai/codex/releases/tags/rust-v0.166.0":
					release := map[string]any{
						"assets": []map[string]any{
							{"url": baseURL + "/api/repos/openai/codex/releases/assets/101", "name": "codex-" + target + ".tar.gz"},
							{"url": baseURL + "/api/repos/openai/codex/releases/assets/102", "name": "codex-code-mode-host-" + target + ".tar.gz"},
						},
					}
					_ = json.NewEncoder(w).Encode(release)
				case "/api/repos/openai/codex/releases/assets/101":
					_, _ = w.Write([]byte("tampered-bytes"))
				case "/api/repos/openai/codex/releases/assets/102":
					_, _ = w.Write(hostTar)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			tmpDir, binDir := runCodexInstallScript(t, rawScript, srv.URL, codexSHA, hostSHA, npmSHA, false)
			if _, err := os.Stat(filepath.Join(tmpDir, "codex-release.json")); !os.IsNotExist(err) {
				t.Fatalf("codex-release.json left behind after digest failure: err=%v", err)
			}
			if _, err := os.Lstat(filepath.Join(binDir, "codex")); !os.IsNotExist(err) {
				t.Fatalf("codex symlink created despite digest mismatch: err=%v", err)
			}
		})

		t.Run(dfName+"/both-github-paths-fail-falls-back-to-npm", func(t *testing.T) {
			var npmHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/web/"):
					http.Error(w, "504 Gateway Time-out", http.StatusGatewayTimeout)
				case strings.HasPrefix(r.URL.Path, "/api/"):
					http.Error(w, "403 API rate limit exceeded", http.StatusForbidden)
				case strings.HasPrefix(r.URL.Path, "/npm/@openai/codex/-/codex-0.166.0-linux-"):
					npmHits.Add(1)
					_, _ = w.Write(npmTar)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			tmpDir, binDir := runCodexInstallScript(t, rawScript, srv.URL, codexSHA, hostSHA, npmSHA, true)
			if npmHits.Load() != 1 {
				t.Fatalf("npmHits = %d, want 1", npmHits.Load())
			}
			assertTempCleanAndBinariesWork(t, tmpDir, binDir)
		})
	}
}

var retryFlagRe = regexp.MustCompile(`--retry\s+\d+\s+--retry-max-time\s+\d+`)

func extractCodexInstallRunBlock(t *testing.T, name, dockerfile string) string {
	t.Helper()
	const marker = `RUN if [ "$INSTALL_CODEX_CLI" = "1" ]; then \`
	idx := strings.Index(dockerfile, marker)
	if idx < 0 {
		t.Fatalf("%s has no %q block", name, marker)
	}
	lines := strings.Split(dockerfile[idx:], "\n")
	var joined strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.HasSuffix(trimmed, `\`) {
			joined.WriteString(strings.TrimSuffix(trimmed, `\`))
			continue
		}
		joined.WriteString(trimmed)
		break
	}
	out := strings.TrimPrefix(joined.String(), "RUN ")
	if !strings.HasSuffix(strings.TrimSpace(out), "fi") {
		t.Fatalf("%s codex RUN block did not end with fi:\n%s", name, out)
	}
	return out
}

func runCodexInstallScript(t *testing.T, rawScript, srvURL, codexSHA, hostSHA, npmSHA string, wantOK bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	optDir := filepath.Join(root, "opt-codex")
	binDir := filepath.Join(root, "bin")
	tmpDir := filepath.Join(root, "tmp")
	homeDir := filepath.Join(root, "home")
	for _, d := range []string{binDir, tmpDir, homeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	escapedAPI := strings.ReplaceAll(srvURL+"/api", "/", `\/`)
	escapedAPI = strings.ReplaceAll(escapedAPI, ".", `\.`)

	script := rawScript
	script = strings.ReplaceAll(script, `https:\/\/api\.github\.com`, escapedAPI)
	script = strings.ReplaceAll(script, "https://github.com", srvURL+"/web")
	script = strings.ReplaceAll(script, "https://api.github.com", srvURL+"/api")
	script = strings.ReplaceAll(script, "https://registry.npmjs.org", srvURL+"/npm")
	script = strings.ReplaceAll(script, "/tmp/codex", filepath.Join(tmpDir, "codex"))
	script = strings.ReplaceAll(script, "/opt/codex", optDir)
	script = strings.ReplaceAll(script, "/usr/local/bin", binDir)
	script = retryFlagRe.ReplaceAllString(script, "--retry 0 --retry-max-time 5")

	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{
		"PATH=" + binDir + ":" + os.Getenv("PATH"),
		"HOME=" + homeDir,
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
		"INSTALL_CODEX_CLI=1",
		"CODEX_CLI_VERSION=rust-v0.166.0",
		"CODEX_CLI_SHA256_AMD64=" + codexSHA,
		"CODEX_CLI_SHA256_ARM64=" + codexSHA,
		"CODEX_CODE_MODE_HOST_SHA256_AMD64=" + hostSHA,
		"CODEX_CODE_MODE_HOST_SHA256_ARM64=" + hostSHA,
		"CODEX_CLI_NPM_VERSION=0.166.0",
		"CODEX_CLI_NPM_SHA256_AMD64=" + npmSHA,
		"CODEX_CLI_NPM_SHA256_ARM64=" + npmSHA,
	}
	out, err := cmd.CombinedOutput()
	if wantOK && err != nil {
		t.Fatalf("codex install script failed (%v):\n%s", err, string(out))
	}
	if !wantOK && err == nil {
		t.Fatalf("codex install script unexpectedly succeeded:\n%s", string(out))
	}
	return tmpDir, binDir
}

func assertTempCleanAndBinariesWork(t *testing.T, tmpDir, binDir string) {
	t.Helper()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("/tmp left temporary files behind: %v", names)
	}
	for _, check := range []struct {
		bin, arg, want string
	}{
		{"codex", "--version", "codex-cli 0.166.0"},
		{"codex-code-mode-host", "--help", "codex-code-mode-host help"},
	} {
		out, err := exec.Command(filepath.Join(binDir, check.bin), check.arg).CombinedOutput()
		if err != nil {
			t.Fatalf("running %s %s: %v (%s)", check.bin, check.arg, err, string(out))
		}
		if !strings.Contains(string(out), check.want) {
			t.Fatalf("%s %s = %q, want %q", check.bin, check.arg, string(out), check.want)
		}
	}
}

func makeTarGz(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, fmt.Sprintf("%s", hex.EncodeToString(sum[:]))
}
