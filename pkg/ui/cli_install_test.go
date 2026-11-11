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

package ui_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// cliArchive is a grain-<os>-<arch>.tar.gz holding one file, grain,
// with body as its contents.
func cliArchive(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "grain", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestCLIInstallScriptEndpointPinsDaemonVersionAndVerifiesSHA256 is
// external-cli.spec.md#XC-32: a single-tenant daemon serves GET
// /cli/install.sh to a caller without its access code, the script asks
// the release mirror for the archive of the daemon's own release, and it
// installs grain 0755 into $GRAIN_INSTALL_DIR only when the archive
// matches checksums.txt.
func TestCLIInstallScriptEndpointPinsDaemonVersionAndVerifiesSHA256(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the script installs on Linux and macOS")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("the script installs amd64 and arm64")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		if _, err := exec.LookPath("wget"); err != nil {
			t.Skip("neither curl nor wget is installed")
		}
	}

	const release = "v9.8.7"
	asset := fmt.Sprintf("grain-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := cliArchive(t, "#!/bin/sh\necho grain "+release+"\n")
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"

	var mu sync.Mutex
	var asked []string
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		body := checksums
		mu.Unlock()
		switch r.URL.Path {
		case "/releases/" + release + "/" + asset:
			_, _ = w.Write(archive)
		case "/releases/" + release + "/checksums.txt":
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mirror.Close)
	t.Setenv("GRAIN_CLI_RELEASE_URL", mirror.URL+"/releases/")
	ui.SetCLIInstallReleaseForTest(t, release)

	client, _, _ := testClient(t)
	client.Config.LocalAPIToken = testAccessCode
	srv := ui.NewServerWithClient(client)

	// A stranger, off this machine and without the code, gets the script.
	req := httptest.NewRequest(http.MethodGet, "http://grain.example.com/cli/install.sh", nil)
	req.RemoteAddr = "203.0.113.9:4242"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /cli/install.sh without the access code: %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/x-shellscript; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/x-shellscript; charset=utf-8", ct)
	}
	script := rec.Body.String()
	for _, want := range []string{"GRAIN_DAEMON_RELEASE='" + release + "'", "GRAIN_DAEMON_RELEASE_URL='" + mirror.URL + "/releases'"} {
		if !strings.Contains(script, want) {
			t.Errorf("script does not hold %s", want)
		}
	}
	if strings.Contains(script, "@GRAIN_CLI_") || strings.Contains(script, testAccessCode) {
		t.Fatal("script still holds a placeholder, or the access code")
	}
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}

	install := func(dir string) (string, error) {
		cmd := exec.Command("sh", path)
		cmd.Env = append(os.Environ(), "GRAIN_INSTALL_DIR="+dir, "GRAIN_CLI_RELEASE_URL=", "GRAIN_CLI_VERSION=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// The right digest installs grain at the pinned release, mode 0755.
	dir := filepath.Join(t.TempDir(), "bin")
	out, err := install(dir)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	fi, err := os.Stat(filepath.Join(dir, "grain"))
	if err != nil {
		t.Fatalf("no grain installed: %v\n%s", err, out)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("grain is mode %v, want 0755", fi.Mode().Perm())
	}
	if got, err := exec.Command(filepath.Join(dir, "grain")).Output(); err != nil || strings.TrimSpace(string(got)) != "grain "+release {
		t.Errorf("installed grain says %q (%v), want the archive's", got, err)
	}
	mu.Lock()
	wantAsked := []string{"/releases/" + release + "/" + asset, "/releases/" + release + "/checksums.txt"}
	if strings.Join(asked, " ") != strings.Join(wantAsked, " ") {
		t.Errorf("the script asked the mirror for %v, want %v", asked, wantAsked)
	}
	// A digest that does not match refuses the archive and installs nothing.
	checksums = strings.Repeat("0", 64) + "  " + asset + "\n"
	mu.Unlock()
	bad := filepath.Join(t.TempDir(), "bin")
	out, err = install(bad)
	if err == nil {
		t.Fatalf("install with a wrong sha256 succeeded:\n%s", out)
	}
	if !strings.Contains(out, "not installing it") {
		t.Errorf("a wrong sha256 is not what the script refused for:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(bad, "grain")); !os.IsNotExist(err) {
		t.Errorf("a wrong sha256 still installed grain (stat: %v)", err)
	}
}
