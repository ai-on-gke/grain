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

package hostd

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStageRootDefaultsToItsOwnDirectory: with no StageRoot, staging is
// confined to a directory of its own under ~/.grain, never ~/.grain
// itself, which on a workstation is the local daemon's data directory.
func TestStageRootDefaultsToItsOwnDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".grain"), 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(home, ".grain", "grain.db")
	if err := os.WriteFile(db, []byte("secrets"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	root := s.stageRoot()
	if want := filepath.Join(home, ".grain", "hostd-stage"); root != want {
		t.Fatalf("stageRoot() = %q, want %q", root, want)
	}
	for _, op := range []StageOp{
		{Op: StageReadTail, Path: db},
		{Op: StageReadTail, Path: "~/.grain/grain.db"},
		{Op: StageWriteFile, Path: db, Data: []byte("x")},
		{Op: StageRemoveAll, Path: filepath.Join(home, ".grain")},
	} {
		if r := s.stage(&op); r.Error == "" {
			t.Errorf("stage(%s %s) succeeded outside the staging root", op.Op, op.Path)
		}
	}
	if data, err := os.ReadFile(db); err != nil || string(data) != "secrets" {
		t.Fatalf("the daemon's data directory was touched: %q, %v", data, err)
	}
}

// TestStageRefusesToFollowASymlinkOutOfTheRoot: a link inside the root
// cannot be named as the directory a create or a prune works in.
func TestStageRefusesToFollowASymlinkOutOfTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	s := &Server{StageRoot: root}
	for _, op := range []StageOp{
		{Op: StagePrune, Path: link, MaxAge: 1},
		{Op: StageMkdirAll, Path: link, Perm: 0o700},
		{Op: StageCreateTemp, Dir: link, Pattern: "x-"},
		{Op: StageWriteFile, Path: filepath.Join(link, "f"), Data: []byte("x")},
		{Op: StageReadTail, Path: link},
	} {
		if r := s.stage(&op); r.Error == "" {
			t.Errorf("stage(%s) through %s succeeded", op.Op, link)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("prune followed the link: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatalf("something was created outside the root: %v", entries)
	}
	// The temp area itself planted as a link out of the root.
	if err := os.Symlink(outside, filepath.Join(root, "tmp")); err != nil {
		t.Fatal(err)
	}
	for _, op := range []StageOp{
		{Op: StageMkdirTemp, Pattern: "x-"},
		{Op: StageCreateTemp, Pattern: "x-"},
	} {
		if r := s.stage(&op); r.Error == "" {
			t.Errorf("stage(%s) through the linked temp area succeeded: %s", op.Op, r.Path)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatalf("a temp directory was created outside the root: %v", entries)
	}
}

// TestStageSymlinkTargetsConfiguredLogins: StageSymlink allows regular
// files from the built-in table when logins: is absent, or from the
// allowlist's logins: map when present, and refuses everything else.
func TestStageSymlinkTakesOnlyTheCodexLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	root := t.TempDir()
	s := &Server{StageRoot: root}
	op := StageOp{Op: StageSymlink, Path: filepath.Join(root, "auth.json"), Target: filepath.Join(home, ".codex", "auth.json")}
	if r := s.stage(&op); r.Error == "" {
		t.Fatal("linked a codex login this machine does not have")
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(op.Target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := s.stage(&op); r.Error != "" {
		t.Fatalf("linking the codex login: %s", r.Error)
	}
	for _, target := range []string{"/etc/passwd", filepath.Join(home, ".ssh"), "~/.ssh/id_ed5519"} {
		other := StageOp{Op: StageSymlink, Path: filepath.Join(root, "other"), Target: target}
		if r := s.stage(&other); r.Error == "" || !strings.Contains(r.Error, "configured login file") {
			t.Errorf("stage(symlink -> %s) = %+v, want a refusal", target, r)
		}
	}

	// An explicit logins: map replaces the built-in table, resolves ~/ and
	// symlinks to regular files, and refuses directories, FIFOs and unmapped
	// logins.
	realCred := filepath.Join(home, "dotfiles", "mycli-auth.json")
	if err := os.MkdirAll(filepath.Dir(realCred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realCred, []byte(`{"token":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	credLink := filepath.Join(home, ".mycli-link.json")
	if err := os.Symlink(realCred, credLink); err != nil {
		t.Fatal(err)
	}
	dirTarget := filepath.Join(home, ".dir-login")
	if err := os.MkdirAll(dirTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	fifoTarget := filepath.Join(home, ".fifo-login")
	if err := syscall.Mkfifo(fifoTarget, 0o600); err != nil {
		t.Fatal(err)
	}
	allowPath := filepath.Join(t.TempDir(), "agentd.yaml")
	allowYAML := "logins:\n" +
		"  mycli: ~/.mycli-link.json\n" +
		"  dircli: ~/.dir-login\n" +
		"  fifocli: ~/.fifo-login\n" +
		"  missing: ~/.no-such-login.json\n"
	if err := os.WriteFile(allowPath, []byte(allowYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	mapped := &Server{StageRoot: root, Allowlist: allowPath, AllowlistOwners: []uint32{uint32(os.Getuid())}}

	// Unmapped ~/.codex/auth.json is refused when logins: omits codex.
	if r := mapped.stage(&StageOp{Op: StageSymlink, Path: filepath.Join(root, "codex-denied"), Target: op.Target}); r.Error == "" {
		t.Error("StageSymlink allowed built-in codex login despite explicit logins: map omitting it")
	}
	// Directories, FIFOs and missing files in logins: are refused.
	for _, bad := range []string{dirTarget, fifoTarget, filepath.Join(home, ".no-such-login.json")} {
		if r := mapped.stage(&StageOp{Op: StageSymlink, Path: filepath.Join(root, "bad"), Target: bad}); r.Error == "" {
			t.Errorf("StageSymlink(%s) succeeded for non-regular/missing target", bad)
		}
	}
	// Mapped symlinked login resolves to its underlying regular file.
	stagedLink := filepath.Join(root, "mycli.json")
	if r := mapped.stage(&StageOp{Op: StageSymlink, Path: stagedLink, Target: realCred}); r.Error != "" {
		t.Fatalf("StageSymlink(resolved target): %s", r.Error)
	}
	if dest, err := os.Readlink(stagedLink); err != nil || dest != realCred {
		t.Fatalf("Readlink(%s) = %q, %v; want %q", stagedLink, dest, err, realCred)
	}
}

// TestStageWriteFileRefusesFIFOsAndTightensPermissions: a FIFO planted in
// a persistent session home neither blocks StageWriteFile nor receives the
// staged bytes, and staged files and directories are owner-only with no
// execute bit even when looser modes were requested or left behind.
func TestStageWriteFileRefusesFIFOsAndTightensPermissions(t *testing.T) {
	root := t.TempDir()
	s := &Server{StageRoot: root}

	fifo := filepath.Join(root, "planted.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Unread FIFO: must fail immediately rather than blocking on a reader.
	if r := s.stage(&StageOp{Op: StageWriteFile, Path: fifo, Data: []byte("secret"), Perm: 0o600}); r.Error == "" {
		t.Fatal("StageWriteFile succeeded on an unread FIFO")
	}
	// Read FIFO: must still be refused without writing staged credentials into the pipe.
	pr, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	if r := s.stage(&StageOp{Op: StageWriteFile, Path: fifo, Data: []byte("secret"), Perm: 0o600}); r.Error == "" {
		t.Fatal("StageWriteFile succeeded on a FIFO with an open reader")
	}
	if buf := make([]byte, 16); func() int { n, _ := pr.Read(buf); return n }() != 0 {
		t.Fatal("StageWriteFile wrote data into the FIFO")
	}

	dir := filepath.Join(root, "sub")
	if r := s.stage(&StageOp{Op: StageMkdirAll, Path: dir, Perm: 0o777}); r.Error != "" {
		t.Fatalf("StageMkdirAll: %s", r.Error)
	}
	if info, err := os.Lstat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("staged dir mode = %v, %v; want 0700", info.Mode().Perm(), err)
	}

	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if r := s.stage(&StageOp{Op: StageWriteFile, Path: cfg, Data: []byte("new"), Perm: 0o777}); r.Error != "" {
		t.Fatalf("StageWriteFile: %s", r.Error)
	}
	info, err := os.Lstat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("staged file mode = %04o, want 0600 (executable and group/other bits stripped)", info.Mode().Perm())
	}
	if got, err := os.ReadFile(cfg); err != nil || string(got) != "new" {
		t.Errorf("staged file content = %q, %v; want %q", got, err, "new")
	}

	tmp := s.stage(&StageOp{Op: StageCreateTemp, Pattern: "claude-*.json"})
	if tmp.Error != "" {
		t.Fatalf("StageCreateTemp with empty Dir: %s", tmp.Error)
	}
	if filepath.Dir(tmp.Path) != filepath.Join(root, "tmp") {
		t.Errorf("StageCreateTemp Path = %q, want directly under %s", tmp.Path, filepath.Join(root, "tmp"))
	}
}

// TestStageAndListenAreRefusedOnTheLocalSocket: OpStage and OpListen are
// served only over a dialed-out remote session, never on the unix socket.
func TestStageAndListenAreRefusedOnTheLocalSocket(t *testing.T) {
	ts := startServer(t, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ts.client.Stage(ctx, StageOp{Op: StageMkdirTemp, Pattern: "x-"}); err == nil || !strings.Contains(err.Error(), "session") {
		t.Errorf("socket Stage = %v, want refusal naming the session", err)
	}
	conn, err := net.Dial("unix", ts.srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeJSON(conn, frameRequest, Request{Op: OpListen, ID: "id-1"}); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := readFrame(conn)
	if err != nil || typ != frameRefused || !strings.Contains(string(payload), "session") {
		t.Errorf("socket OpListen = (%q, %s, %v), want frameRefused naming the session", typ, payload, err)
	}
}

// TestCarryHTTPGuardsLoopbackAndScopesListenersToTheSession: CarryHTTP
// requires the secret path segment, rejects cross-origin and rebound-Host
// requests with 403, and drops listener IDs when a session reconnects.
func TestCarryHTTPGuardsLoopbackAndScopesListenersToTheSession(t *testing.T) {
	ctl := startController(t, "s3cret")
	startRemoteServer(t, ctl, "s3cret", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var seenPath string
	baseURL, release, err := ctl.remote.CarryHTTP(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		_, _ = io.WriteString(w, "ok:"+r.URL.Path)
	}))
	if err != nil {
		t.Fatalf("CarryHTTP: %v", err)
	}
	defer release()

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimPrefix(u.Path, "/")
	if len(secret) != 32 {
		t.Fatalf("secret = %q, want 32 hex chars", secret)
	}

	// Valid request reaches the handler with the secret stripped.
	resp, err := http.Get(baseURL + "/mcp/v1")
	if err != nil {
		t.Fatalf("GET carried URL: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok:/mcp/v1" || seenPath != "/mcp/v1" {
		t.Fatalf("GET = %d %q (seen %q), want 200 ok:/mcp/v1", resp.StatusCode, body, seenPath)
	}

	// Missing or partial/extended secret segment is 404 without reaching h.
	seenPath = ""
	for _, bad := range []string{
		"http://" + u.Host + "/mcp/v1",
		"http://" + u.Host + "/" + secret + "extra/mcp",
		"http://" + u.Host + "/wrongsecret/mcp",
	} {
		r, err := http.Get(bad)
		if err != nil {
			t.Fatalf("GET %s: %v", bad, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", bad, r.StatusCode)
		}
	}
	if seenPath != "" {
		t.Errorf("handler reached on unauthenticated request: %q", seenPath)
	}

	// Browser cross-origin (Origin header) and DNS-rebound Host are 403.
	for _, tc := range []struct {
		name   string
		host   string
		origin string
	}{
		{"origin header", u.Host, "https://evil.example"},
		{"rebound host", "evil.example", ""},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", tc.name, r.StatusCode)
		}
	}
	if seenPath != "" {
		t.Errorf("handler reached on cross-origin/rebound request: %q", seenPath)
	}

	// Reconnect: the old session's carry registry does not carry over to
	// the new session even before release() is called.
	ctl.remote.mu.Lock()
	oldSess := ctl.remote.cur[ctl.remote.DefaultName()]
	ctl.remote.mu.Unlock()
	var oldID string
	oldSess.carries.mu.Lock()
	for id := range oldSess.carries.handles {
		oldID = id
	}
	oldSess.carries.mu.Unlock()
	if oldID == "" {
		t.Fatal("expected active carry ID on session 1")
	}

	ctl.drop()
	waitConnected(t, ctl.remote, 2)
	ctl.remote.mu.Lock()
	newSess := ctl.remote.cur[ctl.remote.DefaultName()]
	ctl.remote.mu.Unlock()
	if newSess == oldSess || newSess.carries.lookup(oldID) != nil {
		t.Fatal("session 1's carry listener ID remained reachable on session 2")
	}
}
