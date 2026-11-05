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

//go:build linux

package intree

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/hostscriptcli"
	"github.com/ai-on-gke/grain/pkg/localexec"
)

// startHostScriptHostd serves a real grain hostd whose allowlist offers
// this test binary as the capability entry "host-script".
func startHostScriptHostd(t *testing.T, data string) hostd.Client {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	// Not t.TempDir: a unix socket's path has to fit in 108 bytes.
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	body := fmt.Sprintf("capabilities:\n  host-script:\n    command: %s\n    env: [%s, %s]\n    paths_under: [%s]\n", exe, hostScriptHelperEnv, hostScriptHelperData, data)
	if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket:          filepath.Join(dir, "hostd.sock"),
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Logf:            func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; ; i++ {
		if _, err := os.Stat(srv.Socket); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("hostd never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return hostd.Client{Socket: srv.Socket}
}

// Under run: host the server is started by grain hostd and its scripts
// run as the user hostd runs as, with the user's ambient identity; its
// pid is known, so its port can be forwarded.
func TestHostScriptRunsOnTheHostThroughHostd(t *testing.T) {
	data := t.TempDir()
	client := startHostScriptHostd(t, data)
	id := "https://github.com/bwsalmon/grain/capabilities/host-script"
	cfg := protocol.ServerConfig{
		Command: "host-script", Run: protocol.RunHost,
		Env: map[string]string{hostScriptHelperEnv: "1", hostScriptHelperData: data},
		Config: map[string]any{"scripts": []any{
			map[string]any{"name": "whoami", "description": "reports who it runs as", "script": `id -u; printf '%s\n' "$1"`, "timeout": 10},
		}},
	}
	if err := cfg.Validate(id); err != nil {
		t.Fatal(err)
	}
	ps, err := protocol.Attach(context.Background(), id, cfg, func(string) (string, bool) { return "", false }, protocol.DialOptions{Hostd: &client, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	if pid, _ := ps.ServiceProcess(); pid <= 0 || syscall.Kill(pid, 0) != nil {
		t.Errorf("ServiceProcess() pid = %d: a run: host server's ports could not be forwarded", pid)
	}
	f := &hostScriptFixture{t: t, ps: ps, data: data}
	cmd, resp := f.grant("e1-42-1-0", "whoami")
	if len(resp.Ports) != 1 {
		t.Fatalf("grant ports = %+v, want 1", resp.Ports)
	}
	svc, err := portforward.ServiceFor(ps.ServiceProcess())
	if err != nil {
		t.Fatal(err)
	}
	inode, err := portforward.ListenerInode(resp.Ports[0].HostPort)
	if err != nil {
		t.Fatalf("ListenerInode(%d): %v", resp.Ports[0].HostPort, err)
	}
	if owner, err := client.SocketOwner(context.Background(), inode); err != nil || !svc.Contains(owner) {
		t.Errorf("hostd SocketOwner(%d) = %d, %v; want in service %+v", inode, owner, err, svc)
	}
	relay := portforward.NewRelay()
	relay.InodeOwner = client.SocketOwner
	fwd, err := relay.Open(context.Background(), resp.Ports[0].HostPort, svc)
	if err != nil {
		t.Fatalf("relay.Open for run: host server port: %v", err)
	}
	defer fwd.Close()
	got := call(t, cmd, "$(id -u) literal")
	want := strconv.Itoa(os.Getuid()) + "\n$(id -u) literal\n"
	if got.code != 0 || got.stdout != want {
		t.Errorf("got %+v, want stdout %q: the script runs as the user, with its argument literal", got, want)
	}
	if _, err := ps.Release(context.Background(), "host-script", "e1-42-1-0"); err != nil {
		t.Fatal(err)
	}
	if got := call(t, cmd); got.code != 125 {
		t.Errorf("after release: %+v", got)
	}
}

// Under run: host (separate process started by grain hostd), an approve: true
// script reaches the daemon's approval endpoint via the callback URL and bearer
// token handed over at mint, forwards --stdin on approval, and refuses on denial.
func TestHostScriptApprovalCallbackThroughHostd(t *testing.T) {
	var mu sync.Mutex
	var gotAuth, gotPath string
	var gotReq localexec.Request
	var expired bool
	decisions := make(chan string, 4)

	approvalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks/42/local-script-approvals":
			var req localexec.Request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			gotPath = r.URL.Path
			gotReq = req
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "status": "pending"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wait"):
			status := <-decisions
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/expire"):
			mu.Lock()
			expired = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "expired"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer approvalSrv.Close()

	data := t.TempDir()
	client := startHostScriptHostd(t, data)
	id := "https://github.com/bwsalmon/grain/capabilities/host-script"
	cfg := protocol.ServerConfig{
		Command: "host-script", Run: protocol.RunHost,
		Env: map[string]string{hostScriptHelperEnv: "1", hostScriptHelperData: data},
		Config: map[string]any{"scripts": []any{
			map[string]any{
				"name": "deploy", "description": "deploys with approval",
				"script":  `printf 'uid=%s arg=%s in=' "$(id -u)" "$1"; cat`,
				"timeout": 10, "approve": true,
			},
		}},
	}
	ps, err := protocol.Attach(context.Background(), id, cfg, func(string) (string, bool) { return "", false }, protocol.DialOptions{Hostd: &client, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()

	mintCtx := protocol.WithApprovalCallback(context.Background(), protocol.ApprovalCallback{
		URL:   approvalSrv.URL,
		Token: "ghsa_test_token_42",
	})
	resp, err := ps.Mint(mintCtx, "host-script", "e1-42-1-0", time.Now().Add(time.Minute), map[string]any{"script": "deploy"})
	if err != nil || resp.Status != protocol.StatusOK {
		t.Fatalf("Mint = %+v, %v", resp, err)
	}
	sandbox := t.TempDir()
	for _, p := range resp.Placements {
		if err := os.WriteFile(filepath.Join(sandbox, filepath.Base(p.Path)), []byte(p.Content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := hostScriptCmd{dir: sandbox, name: "deploy"}
	_ = hostscriptcli.Dir

	decisions <- "approved"
	got := callStdin(t, cmd, true, []byte("hello-hostd\n"), "prod")
	want := fmt.Sprintf("uid=%d arg=prod in=hello-hostd\n", os.Getuid())
	if got.code != 0 || got.stdout != want {
		t.Fatalf("approved hostd call = %+v, want %q", got, want)
	}
	mu.Lock()
	if gotAuth != "Bearer ghsa_test_token_42" || gotPath != "/api/tasks/42/local-script-approvals" ||
		gotReq.Tool != "grain host-script deploy" || gotReq.StdinPreview != "hello-hostd\n" {
		t.Errorf("callback saw auth=%q path=%q req=%+v", gotAuth, gotPath, gotReq)
	}
	mu.Unlock()

	decisions <- "denied"
	denied := call(t, cmd, "prod")
	if denied.code != 125 || !strings.Contains(denied.stderr, "the call was denied") {
		t.Fatalf("denied hostd call = %+v, want exit 125 denied", denied)
	}
	_ = expired
}
