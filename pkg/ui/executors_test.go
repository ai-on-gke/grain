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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelexec"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type fakeExecutors struct {
	status []modelexec.Status
}

func (f fakeExecutors) Connected() []modelexec.Status {
	return f.status
}

func TestExecutorTokenRejectsOverlongName(t *testing.T) {
	ctx := context.Background()
	srv, client := testServer(t)
	_ = client.Store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})

	rec := do(t, srv, "POST", "/api/executors/tokens", `{"name":"`+strings.Repeat("n", 129)+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot be longer than 128 characters") {
		t.Fatalf("POST 129-char name: code %d, body %s; want 400", rec.Code, rec.Body.String())
	}
	tokens, err := client.Store.ListExecutorTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected no tokens minted, got %+v", tokens)
	}

	// 128 characters, multi-byte ones included, is still accepted.
	rec = do(t, srv, "POST", "/api/executors/tokens", `{"name":"`+strings.Repeat("é", 128)+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST 128-char name: code %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestExecutorTokenAuthorization(t *testing.T) {
	ctx := context.Background()
	srv, client := testServer(t)

	// Configure system admin account
	cfg := model.DefaultConfig()
	if err := client.Store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("storing config: %v", err)
	}

	adminPrincipal := model.Principal{ID: "admin@example.com", Kind: model.PrincipalHuman}

	adminCtx := ui.WithPrincipal(context.Background(), adminPrincipal)

	// 4. Admin user can successfully perform operations
	req := httptest.NewRequest(http.MethodGet, "/api/executors/tokens", nil)
	req = req.WithContext(ui.WithPrincipal(req.Context(), adminPrincipal))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET /api/executors/tokens: code %d, body: %s", rec.Code, rec.Body.String())
	}

	createPayload := `{"name":"prod-cluster"}`
	req = httptest.NewRequest(http.MethodPost, "/api/executors/tokens", strings.NewReader(createPayload))
	req = req.WithContext(ui.WithPrincipal(req.Context(), adminPrincipal))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin POST /api/executors/tokens: code %d, body: %s", rec.Code, rec.Body.String())
	}
	var created ui.CreateExecutorTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshaling created token: %v", err)
	}

	// Admin deletes/revokes token via DELETE
	req = httptest.NewRequest(http.MethodDelete, "/api/executors/tokens/"+created.Record.ID, nil)
	req = req.WithContext(ui.WithPrincipal(req.Context(), adminPrincipal))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin DELETE /api/executors/tokens/%s: code %d, body: %s", created.Record.ID, rec.Code, rec.Body.String())
	}

	// Admin creates and revokes token via Client methods
	rawTok, recTok, err := client.CreateExecutorToken(adminCtx, "admin-created")
	if err != nil || rawTok == "" || recTok == nil {
		t.Fatalf("client.CreateExecutorToken(adminCtx) failed: %v", err)
	}
	tokens, err := client.ListExecutorTokens(adminCtx)
	if err != nil || len(tokens) == 0 {
		t.Fatalf("client.ListExecutorTokens(adminCtx) failed: %v", err)
	}
	revoked, err := client.RevokeExecutorToken(adminCtx, recTok.ID)
	if err != nil || revoked == nil || revoked.RevokedAt == nil {
		t.Fatalf("client.RevokeExecutorToken(adminCtx) failed: %v", err)
	}
}

func TestExecutorInstallScriptEndpoint(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)

	for _, path := range []string{"/api/model-servers/install.sh", "/api/executors/install.sh", "/install-executor.sh"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, rec.Code, http.StatusOK)
		}

		contentType := rec.Header().Get("Content-Type")
		if contentType != "text/x-shellscript; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q, want text/x-shellscript; charset=utf-8", path, contentType)
		}

		body := rec.Body.String()
		if len(body) == 0 {
			t.Errorf("GET %s returned empty body", path)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("run-model-server.sh")) {
			t.Errorf("GET %s body missing run-model-server.sh banner / identifier", path)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("run-remote-executor.sh")) {
			t.Errorf("GET %s body missing script banner / identifier", path)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("grain model-server")) {
			t.Errorf("GET %s body missing grain model-server invocation", path)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("grain executor")) {
			t.Errorf("GET %s body missing grain executor invocation", path)
		}
	}
}

func TestExecutorInstallScriptContentMatchesRepoScript(t *testing.T) {
	t.Parallel()

	scriptPath := filepath.Join("..", "..", "scripts", "run-model-server.sh")
	repoScript, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Skipf("cannot read repo script at %s: %v", scriptPath, err)
		return
	}

	srv, _ := testServer(t)
	for _, path := range []string{"/api/model-servers/install.sh", "/api/executors/install.sh"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if !bytes.Equal(rec.Body.Bytes(), repoScript) {
			t.Errorf("GET %s differs from scripts/run-model-server.sh", path)
		}
	}
}

func TestListExecutorsEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("nil executors returns empty array", func(t *testing.T) {
		srv, _ := testServer(t)

		req := httptest.NewRequest(http.MethodGet, "/api/executors", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/executors status = %d, want %d", rec.Code, http.StatusOK)
		}

		var got []modelexec.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshaling /api/executors response: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d executors, want 0", len(got))
		}
		// Confirm literal JSON serialization is [] rather than null
		if body := bytes.TrimSpace(rec.Body.Bytes()); string(body) != "[]" {
			t.Errorf("JSON body = %s, want []", body)
		}
	})

	t.Run("connected executors returned", func(t *testing.T) {
		srv, client := testServer(t)

		since := time.Date(2026, 9, 24, 7, 0, 0, 0, time.UTC)
		expected := []modelexec.Status{
			{
				Host: "worker-node-1",
				Addr: "10.0.1.5:41234",
				Binaries: map[string]string{
					"agy":    "/usr/local/bin/agy",
					"claude": "/usr/local/bin/claude",
				},
				ServerURL:   "https://grain.example.com",
				Since:       since,
				Running:     2,
				Concurrency: 4,
			},
			{
				Host: "worker-node-2",
				Addr: "10.0.1.6:41235",
				Binaries: map[string]string{
					"codex": "/usr/local/bin/codex",
				},
				ServerURL:   "https://grain.example.com",
				Since:       since.Add(-1 * time.Hour),
				Running:     0,
				Concurrency: 2,
			},
		}

		client.Config.Executors = fakeExecutors{status: expected}

		req := httptest.NewRequest(http.MethodGet, "/api/executors", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/executors status = %d, want %d", rec.Code, http.StatusOK)
		}

		var got []modelexec.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshaling /api/executors response: %v", err)
		}

		if len(got) != len(expected) {
			t.Fatalf("got %d executors, want %d", len(got), len(expected))
		}

		if got[0].Host != "worker-node-1" || got[0].Running != 2 || got[0].Concurrency != 4 {
			t.Errorf("got[0] = %+v, want worker-node-1 info", got[0])
		}
		if got[0].Binaries["agy"] != "/usr/local/bin/agy" || got[0].Binaries["claude"] != "/usr/local/bin/claude" {
			t.Errorf("got[0].Binaries = %+v", got[0].Binaries)
		}
		if got[1].Host != "worker-node-2" || got[1].Running != 0 || got[1].Binaries["codex"] != "/usr/local/bin/codex" {
			t.Errorf("got[1] = %+v, want worker-node-2 info", got[1])
		}
	})

	t.Run("http client list executors", func(t *testing.T) {
		srv, client := testServer(t)

		expected := []modelexec.Status{
			{
				Host: "executor-host",
				Addr: "127.0.0.1:5000",
				Binaries: map[string]string{
					"agy": "/bin/agy",
				},
				Since:       time.Now().UTC(),
				Running:     1,
				Concurrency: 2,
			},
		}
		client.Config.Executors = fakeExecutors{status: expected}

		ts := httptest.NewServer(srv)
		defer ts.Close()

		httpClient := serverAccessClient(srv, ts.URL)
		list, err := httpClient.ListExecutors(context.Background())
		if err != nil {
			t.Fatalf("ListExecutors failed: %v", err)
		}
		if len(list) != 1 || list[0].Host != "executor-host" {
			t.Fatalf("ListExecutors = %+v, want 1 executor with host executor-host", list)
		}
	})
}

func TestExecutorConnectEndpoint(t *testing.T) {
	t.Parallel()

	// 1. Without ExecutorServer configured -> 503 Service Unavailable
	srv, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/executors/connect", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "grain-executor")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/executors/connect without ExecutorServer status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	// 2. With ExecutorServer, but non-upgrade request -> 400 Bad Request
	execSrv, err := modelexec.NewServer(modelexec.Config{
		Token: "test-token",
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	client, _, _ := testClient(t)
	client.Config.ExecutorServer = execSrv
	srvWithExec := ui.NewServerWithClient(client)

	nonUpgradeReq := httptest.NewRequest(http.MethodGet, "/api/executors/connect", nil)
	nonUpgradeRec := httptest.NewRecorder()
	srvWithExec.ServeHTTP(nonUpgradeRec, nonUpgradeReq)
	if nonUpgradeRec.Code != http.StatusBadRequest {
		t.Fatalf("GET /api/executors/connect non-upgrade status = %d, want %d", nonUpgradeRec.Code, http.StatusBadRequest)
	}

	// 3. End-to-end HTTP Upgrade via httptest.Server
	ts := httptest.NewServer(srvWithExec)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := &modelexec.Daemon{
		Addr:  ts.URL,
		Token: "test-token",
		Name:  "worker-1",
		Binaries: map[string]string{
			"echo": "/bin/echo",
			"sh":   "/bin/sh",
		},
	}

	go func() { _ = d.Run(ctx) }()

	host, err := execSrv.Host(ctx, "echo")
	if err != nil {
		t.Fatalf("execSrv.Host: %v", err)
	}
	if host == nil {
		t.Fatal("execSrv.Host returned nil host")
	}

	out, err := host.Run(ctx, agent.Command{
		Path: "/bin/echo",
		Args: []string{"hello-remote-ui"},
	}, nil)
	if err != nil {
		t.Fatalf("host.Run: %v, out = %q", err, out)
	}
	if !strings.Contains(out, "hello-remote-ui") {
		t.Fatalf("unexpected command output: %q", out)
	}
}

func TestExecutorBinaryEndpointsHost(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)

	// The endpoints serve whatever findGrainBinary resolves for this
	// host: the running executable, unless a grain binary sits earlier
	// on its search path (a sandbox image's /usr/local/bin/grain, say).
	// The endpoints serve whatever findGrainBinary resolves for this
	// host: a grain in one of the fixed install directories (a sandbox
	// image's /usr/local/bin/grain host-script shim, say) ahead of the
	// running executable.
	want, err := ui.FindGrainBinary("grain", "", "")
	if err != nil {
		t.Fatalf("FindGrainBinary: %v", err)
	}
	expectedBytes, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("reading %s: %v", want, err)
	}

	paths := []struct {
		path         string
		wantFilename string
	}{
		{
			path:         "/api/executors/bin/grain",
			wantFilename: "grain",
		},
		{
			path:         fmt.Sprintf("/api/executors/bin/grain-%s-%s", runtime.GOOS, runtime.GOARCH),
			wantFilename: fmt.Sprintf("grain-%s-%s", runtime.GOOS, runtime.GOARCH),
		},
		{
			path:         fmt.Sprintf("/install/%s/%s", runtime.GOOS, runtime.GOARCH),
			wantFilename: fmt.Sprintf("grain-%s-%s", runtime.GOOS, runtime.GOARCH),
		},
	}

	for _, tc := range paths {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d (body: %s)", tc.path, rec.Code, http.StatusOK, rec.Body.String())
		}

		contentType := rec.Header().Get("Content-Type")
		if contentType != "application/octet-stream" {
			t.Errorf("GET %s Content-Type = %q, want application/octet-stream", tc.path, contentType)
		}

		contentDisposition := rec.Header().Get("Content-Disposition")
		wantDisposition := fmt.Sprintf("attachment; filename=%q", tc.wantFilename)
		if contentDisposition != wantDisposition {
			t.Errorf("GET %s Content-Disposition = %q, want %q", tc.path, contentDisposition, wantDisposition)
		}

		if !bytes.Equal(rec.Body.Bytes(), expectedBytes) {
			t.Errorf("GET %s body does not match %s (%d bytes vs %d bytes)",
				tc.path, want, rec.Body.Len(), len(expectedBytes))
		}
	}
}

func TestExecutorBinaryEndpointsPrebuiltBinaries(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GRAIN_BIN_DIR", tmpDir)

	srv, _ := testServer(t)

	darwinArm64Content := []byte("mock-grain-darwin-arm64-binary-content")
	if err := os.WriteFile(filepath.Join(tmpDir, "grain-darwin-arm64"), darwinArm64Content, 0o755); err != nil {
		t.Fatalf("writing mock binary: %v", err)
	}

	linuxArm64Content := []byte("mock-grain-linux-arm64-binary-content")
	if err := os.WriteFile(filepath.Join(tmpDir, "grain-linux-arm64"), linuxArm64Content, 0o755); err != nil {
		t.Fatalf("writing mock binary: %v", err)
	}

	cases := []struct {
		path         string
		wantContent  []byte
		wantFilename string
	}{
		{
			path:         "/api/executors/bin/grain-darwin-arm64",
			wantContent:  darwinArm64Content,
			wantFilename: "grain-darwin-arm64",
		},
		{
			path:         "/install/darwin/arm64",
			wantContent:  darwinArm64Content,
			wantFilename: "grain-darwin-arm64",
		},
		{
			path:         "/api/executors/bin/grain-linux-arm64",
			wantContent:  linuxArm64Content,
			wantFilename: "grain-linux-arm64",
		},
		{
			path:         "/install/linux/arm64",
			wantContent:  linuxArm64Content,
			wantFilename: "grain-linux-arm64",
		},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d (body: %s)", tc.path, rec.Code, http.StatusOK, rec.Body.String())
		}

		if !bytes.Equal(rec.Body.Bytes(), tc.wantContent) {
			t.Errorf("GET %s body = %q, want %q", tc.path, rec.Body.String(), string(tc.wantContent))
		}

		contentDisposition := rec.Header().Get("Content-Disposition")
		wantDisposition := fmt.Sprintf("attachment; filename=%q", tc.wantFilename)
		if contentDisposition != wantDisposition {
			t.Errorf("GET %s Content-Disposition = %q, want %q", tc.path, contentDisposition, wantDisposition)
		}
	}
}

func TestExecutorBinaryChecksumEndpoints(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GRAIN_BIN_DIR", tmpDir)

	srv, _ := testServer(t)

	content := []byte("mock-grain-linux-arm64-binary-content")
	if err := os.WriteFile(filepath.Join(tmpDir, "grain-linux-arm64"), content, 0o755); err != nil {
		t.Fatalf("writing mock binary: %v", err)
	}
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:]) + "  grain-linux-arm64\n"

	for _, path := range []string{
		"/api/executors/bin/grain-linux-arm64.sha256",
		"/install/linux/arm64.sha256",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d (body: %s)", path, rec.Code, http.StatusOK, rec.Body.String())
		}
		if got := rec.Body.String(); got != want {
			t.Errorf("GET %s body = %q, want %q", path, got, want)
		}
	}

	for _, path := range []string{
		"/api/executors/bin/grain-linux-riscv.sha256",
		"/install/linux/riscv.sha256",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestExecutorBinaryNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)

	for _, path := range []string{
		"/api/executors/bin/grain-nonexistent-arch",
		"/install/nonexistent/arch",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

// TestExecutorBinaryRejectsUnexpectedNames guards against the endpoint being
// used to read arbitrary files out of its search directories (issue #2422).
func TestExecutorBinaryRejectsUnexpectedNames(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GRAIN_BIN_DIR", tmpDir)
	if err := os.WriteFile(filepath.Join(tmpDir, "grain.db"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("writing decoy file: %v", err)
	}

	srv, _ := testServer(t)

	for _, path := range []string{
		"/api/executors/bin/grain.db",
		"/api/executors/bin/config.yaml",
		"/api/executors/bin/.env",
		"/api/executors/bin/grain-linux",
		"/api/executors/bin/grain-linux-amd64-extra",
		"/install/linux/amd64.db",
		"/install/lin.ux/amd64",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want %d", path, rec.Code, http.StatusBadRequest)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("GET %s leaked file contents", path)
		}
	}
}

// TestExecutorBinaryIgnoresWorkingDirectory checks that a grain-named file
// in the daemon's cwd is not served.
func TestExecutorBinaryIgnoresWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(cwd, "grain-plan9-mips"), []byte("x"), 0o755); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	srv, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/executors/bin/grain-plan9-mips", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET grain-plan9-mips from cwd status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
