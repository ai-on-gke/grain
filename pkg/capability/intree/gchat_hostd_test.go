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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// gchatHelperEnv makes this test binary serve the gchat server over
// stdio, the way `grain capability gchat` is started in the container or
// by grain hostd for a run: host entry. Its ambient credentials are the
// process's own Application Default Credentials --
// GOOGLE_APPLICATION_CREDENTIALS, as a container's or a user's are -- and
// gchatHelperChat is the fake Chat it may post to.
const (
	gchatHelperEnv  = "GRAIN_INTREE_TEST_GCHAT"
	gchatHelperChat = "GRAIN_INTREE_TEST_GCHAT_CHAT"
)

func init() { testMainHelpers[gchatHelperEnv] = serveGChatHelper }

func serveGChatHelper() {
	listen := "127.0.0.1:0"
	for i, a := range os.Args {
		if a == "-listen" && i+1 < len(os.Args) {
			listen = os.Args[i+1]
		}
	}
	chat := os.Getenv(gchatHelperChat)
	s, err := NewGChat(GChatServerConfig{
		Listen: listen, Token: os.Getenv(protocol.DataPlaneTokenEnv), ChatBase: chat,
		WebhookHosts: []string{strings.TrimPrefix(chat, "http://")}, Log: log.New(os.Stderr, "gchat: ", 0),
	})
	if err == nil {
		err = s.Serve(context.Background(), os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func freeLoopback(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// adcFile writes a service-account key whose token endpoint is the fake
// Chat's, for GOOGLE_APPLICATION_CREDENTIALS.
func adcFile(t *testing.T, f *fakeChat) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, []byte(serviceAccountKey(t, f)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// gchatAttachConfig is an entry's stored configuration -- an ambient
// app space and a webhook space -- and the secrets it references.
func gchatAttachConfig(f *fakeChat) (map[string]any, protocol.SecretLookup) {
	hooks, _ := json.Marshal(map[string]string{"room": f.webhookURL("HOOK")})
	config := map[string]any{
		"grain_url": "https://grain.example.com",
		"apps":      []any{map[string]any{"name": "me", "credential_source": "ambient"}},
		"spaces": []any{
			map[string]any{"name": "room", "transport": "webhook"},
			map[string]any{"name": "corp", "transport": "app", "space": "spaces/CORP", "app": "me"},
		},
		"webhook_urls":                "$secret:hooks",
		protocol.DataPlaneTokenConfig: "$secret:token",
	}
	secrets := map[string]string{"hooks": string(hooks), "token": gchatTestToken}
	return config, func(n string) (string, bool) { v, ok := secrets[n]; return v, ok }
}

// exerciseAttachedGChat grants each space of an attached, configured
// gchat server, posts through grain's own proxy of each grant's
// mcp_endpoint, and releases.
func exerciseAttachedGChat(t *testing.T, ps *protocol.Server, f *fakeChat) {
	t.Helper()
	ctx := context.Background()
	desc := ps.Methods[GChatMethod].Description
	if !strings.Contains(desc, "app me (ambient): posts as grain-bot@proj.iam.gserviceaccount.com") || !strings.Contains(desc, "can post to spaces/CORP") {
		t.Errorf("Test report = %q, want the ambient identity and its reach", desc)
	}
	dp, err := ps.DataPlane()
	if err != nil {
		t.Fatal(err)
	}
	for i, space := range []string{"room", "corp"} {
		attempt := fmt.Sprintf("e1-77-1-%d", i)
		resp, err := ps.Mint(ctx, GChatMethod, attempt, time.Now().Add(time.Hour), map[string]any{"space": space})
		if err != nil || resp.Status != protocol.StatusOK {
			t.Fatalf("mint %s: %+v, %v", space, resp, err)
		}
		proxy, err := protocol.NewProxy(dp, attempt, nil)
		if err != nil {
			t.Fatal(err)
		}
		front := httptest.NewServer(proxy)
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"post_message","arguments":{"text":"hello from ` + space + `"}}}`
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		front.Close()
		if r.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte("threads/grain-task-77")) {
			t.Errorf("post_message to %s through grain's proxy: %d %s", space, r.StatusCode, raw)
		}
		rel, err := ps.Release(ctx, GChatMethod, attempt)
		if err != nil {
			t.Fatal(err)
		}
		if len(rel.Audit) != 1 {
			t.Errorf("release audit = %+v, want the one post", rel.Audit)
		}
	}
	posts := f.all()
	if len(posts) != 2 || !posts[0].Webhook || posts[1].Bearer != "configured-access-token" {
		t.Errorf("posts = %+v, want one by webhook and one as the ambient service account", posts)
	}
}

// In the container the server is `grain capability gchat` in the
// daemon's own environment, posting as the container's ADC.
func TestGChatServesInTheContainerWithAmbientADC(t *testing.T) {
	f := newFakeChat(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id := CanonicalID(GChatName)
	cfg := protocol.ServerConfig{
		Command: exe, Args: []string{"-listen", freeLoopback(t)},
		Env: map[string]string{gchatHelperEnv: "1", gchatHelperChat: f.srv.URL, "GOOGLE_APPLICATION_CREDENTIALS": adcFile(t, f)},
	}
	var secrets protocol.SecretLookup
	cfg.Config, secrets = gchatAttachConfig(f)
	if err := cfg.Validate(id); err != nil {
		t.Fatal(err)
	}
	ps, err := protocol.Attach(context.Background(), id, cfg, secrets, protocol.DialOptions{Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	exerciseAttachedGChat(t, ps, f)
}

// On the host the same server is started by grain hostd (run: host) and
// posts as the user's own ambient identity.
func TestGChatServesOnTheHostThroughHostd(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	f := newFakeChat(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// The user's ambient identity: their ADC file, which hostd passes
	// from its own environment.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adcFile(t, f))
	allow := filepath.Join(dir, "hostd.yaml")
	body := fmt.Sprintf("capabilities:\n  grain-gchat:\n    command: %s\n    extra_args: true\n    env: [%s, %s, %s]\n    pass_env: [GOOGLE_APPLICATION_CREDENTIALS]\n",
		exe, gchatHelperEnv, gchatHelperChat, protocol.ProbeEnv)
	if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket: filepath.Join(dir, "hostd.sock"), Allowlist: allow, AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir: filepath.Join(dir, "state"), Logf: func(string, ...any) {},
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
	client := hostd.Client{Socket: srv.Socket}
	id := CanonicalID(GChatName)
	cfg := protocol.ServerConfig{
		Command: HostEntry(GChatName), Run: protocol.RunHost, Args: []string{"-listen", freeLoopback(t)},
		Env: map[string]string{gchatHelperEnv: "1", gchatHelperChat: f.srv.URL},
	}
	if InTreeName(cfg) != GChatName {
		t.Errorf("a run: host grain-gchat entry is not taken for the built-in")
	}
	var secrets protocol.SecretLookup
	cfg.Config, secrets = gchatAttachConfig(f)
	if err := cfg.Validate(id); err != nil {
		t.Fatal(err)
	}
	ps, err := protocol.Attach(context.Background(), id, cfg, secrets, protocol.DialOptions{Hostd: &client, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	exerciseAttachedGChat(t, ps, f)
}
