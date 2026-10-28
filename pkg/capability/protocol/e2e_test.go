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

package protocol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// helperEnv makes this test binary act as a stdio capability server, the
// way Grain spawns `grain capability <name>` or a grain/config plugin.
const helperEnv = "GRAIN_PROTOCOL_TEST_CAPABILITY"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		s, err := newTestCapability(os.Getenv("TEST_MINTER_KEY"), nil)
		if err == nil {
			err = s.ServeStdio(context.Background(), os.Stdin, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if mode := os.Getenv(dropCapsHelperEnv); mode != "" {
		dropCapsHelper(mode)
	}
	os.Exit(m.Run())
}

// newTestCapability serves #gcp-key (stateful, leased) and #skill
// (stateless). The minted key embeds minterKey so a test can see the
// server received its resolved secret.
func newTestCapability(minterKey string, released *atomic.Int64) (*CapabilityServer, error) {
	var mints atomic.Int64
	gcp, err := NewMethod(MethodSpec{
		Name:        "gcp-key",
		Description: "Mint a short-lived key.",
		Args: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "default": "default", "pattern": "^[a-z0-9][a-z0-9_-]{0,62}$"},
			},
		},
		Mint: func(_ context.Context, attemptID string, _ time.Time, args map[string]any) (*MintResponse, error) {
			if args["account"] == "full" {
				return &MintResponse{Status: StatusResourceExhausted}, nil
			}
			n := mints.Add(1)
			path := fmt.Sprintf("/home/debian/.gcp-service-account-%s.json", args["account"])
			return &MintResponse{
				Status:        StatusOK,
				Placements:    []Placement{{Path: path, Content: fmt.Sprintf(`{"key":%d,"minter":%q}`, n, minterKey)}},
				MCPEndpoint:   "/runs/" + attemptID + "/mcp",
				PromptSection: "A key is at `" + path + "`.",
			}, nil
		},
		Release: func(context.Context, string) error {
			if released != nil {
				released.Add(1)
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	skill, err := NewMethod(MethodSpec{
		Name: "skill",
		Mint: func(context.Context, string, time.Time, map[string]any) (*MintResponse, error) {
			return &MintResponse{Status: StatusOK, Placements: []Placement{{Path: "/home/debian/.skills/x/SKILL.md", Mode: "0644", Content: strings.Repeat("s", 100<<10)}}}, nil
		},
	})
	if err != nil {
		return nil, err
	}
	return NewCapabilityServer("test-capability", gcp, skill)
}

func exerciseServer(t *testing.T, srv *Server) {
	t.Helper()
	ctx := context.Background()
	if !srv.Methods["gcp-key"].Releasable || srv.Methods["skill"].Releasable {
		t.Fatalf("discovered methods = %+v", srv.Methods)
	}
	lookup := func(id string) (*Server, error) {
		if id != srv.ID {
			return nil, fmt.Errorf("%s is not configured", id)
		}
		return srv, nil
	}
	grants, err := Normalize([]GrantSpec{
		{Capability: srv.ID + "#gcp-key", With: map[string]any{"account": "staging"}},
		BareGrant(srv.ID + "#skill"),
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	minters := func(string) (Minter, error) { return srv, nil }
	opts := PassOptions{Deployment: "e1010c47", Task: "175", Attempt: 2, Deadline: time.Now().Add(time.Hour)}
	pass, err := MintPass(ctx, grants, minters, opts)
	if err != nil {
		t.Fatal(err)
	}
	key := pass.Grants[0].Response.Placements[0]
	if key.Path != "/home/debian/.gcp-service-account-staging.json" || !strings.Contains(key.Content, `"minter":"root-key"`) {
		t.Errorf("minted placement = %+v", key)
	}
	if pass.Grants[0].Response.MCPEndpoint != "/runs/e1010c47-175-2-0/mcp" {
		t.Errorf("mcp_endpoint = %q", pass.Grants[0].Response.MCPEndpoint)
	}
	if n := len(pass.Grants[1].Response.Placements[0].Content); n != 100<<10 {
		t.Errorf("a 100 KiB placement arrived as %d bytes", n)
	}

	// A repeat mint is idempotent, and with a later deadline renews.
	again, err := srv.Mint(ctx, "gcp-key", "e1010c47-175-2-0", opts.Deadline.Add(time.Hour), grants[0].Args)
	if err != nil || again.Placements[0].Content != key.Content {
		t.Errorf("repeat mint allocated again: %+v, %v", again, err)
	}
	if err := pass.Renew(ctx, opts.Deadline.Add(2*time.Hour)); err != nil {
		t.Errorf("Renew: %v", err)
	}
	diff, err := srv.Mint(ctx, "gcp-key", "e1010c47-175-2-0", opts.Deadline, map[string]any{"account": "prod"})
	if err != nil || diff.Status != StatusRefused {
		t.Errorf("a repeat mint with different args must be refused: %+v, %v", diff, err)
	}

	// The server re-validates args whatever Grain sent.
	bad, err := srv.Mint(ctx, "gcp-key", "e1010c47-175-3-0", opts.Deadline, map[string]any{"account": "../etc"})
	if err != nil || bad.Status != StatusRefused {
		t.Errorf("invalid args reached the mint: %+v, %v", bad, err)
	}

	if _, err := pass.Release(ctx); err != nil {
		t.Errorf("Release: %v", err)
	}
	// Releasing again, or releasing what was never minted, is ok.
	if _, err := srv.Release(ctx, "gcp-key", "e1010c47-175-2-0"); err != nil {
		t.Errorf("second release: %v", err)
	}
	if _, err := srv.Release(ctx, "gcp-key", "never-minted"); err != nil {
		t.Errorf("unknown release: %v", err)
	}

	full, err := Normalize([]GrantSpec{{Capability: srv.ID + "#gcp-key", With: map[string]any{"account": "full"}}}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	opts.Attempt = 9
	var ex *ExhaustedError
	if _, err := MintPass(ctx, full, minters, opts); !errors.As(err, &ex) {
		t.Errorf("resource_exhausted came back as %v", err)
	}
}

func TestStdioCapabilityServerEndToEnd(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id := "https://github.com/bwsalmon/grain/capabilities/test"
	cfg := ServerConfig{Command: exe, Env: map[string]string{helperEnv: "1", "TEST_MINTER_KEY": "$secret:minter"}}
	secrets := func(name string) (string, bool) {
		return map[string]string{"minter": "root-key"}[name], name == "minter"
	}
	srv, err := Attach(context.Background(), id, cfg, secrets, DialOptions{Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	exerciseServer(t, srv)

	if _, err := Attach(context.Background(), id, cfg, func(string) (string, bool) { return "", false }, DialOptions{}); err == nil || !strings.Contains(err.Error(), "minter") {
		t.Errorf("a missing secret must refuse the attach, got %v", err)
	}
	cfg.Disabled = true
	if _, err := Attach(context.Background(), id, cfg, secrets, DialOptions{}); err == nil {
		t.Errorf("a disabled server was attached")
	}
}

func TestHTTPCapabilityServerEndToEnd(t *testing.T) {
	var released atomic.Int64
	cs, err := newTestCapability("root-key", &released)
	if err != nil {
		t.Fatal(err)
	}
	cs.Token = "0123456789abcdef0123456789abcdef"
	ts := httptest.NewServer(cs)
	defer ts.Close()

	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unauthenticated request was answered %d, want 401", resp.StatusCode)
	}

	id := "https://github.com/my-org/capabilities/external-cap"
	cfg := ServerConfig{ServerURL: ts.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer $secret:tok"}}
	srv, err := Attach(context.Background(), id, cfg, func(string) (string, bool) { return cs.Token, true }, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	exerciseServer(t, srv)
	if released.Load() != 1 {
		t.Errorf("release_gcp-key revoked %d leases, want 1", released.Load())
	}
}

// An HTTP capability server nobody gave a token to must refuse every
// request, not serve mints to whoever reaches its port (§1.3.2, §3.1.3).
func TestHTTPCapabilityServerWithoutTokenRefusesEverything(t *testing.T) {
	cs, err := newTestCapability("root-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "too-short"} {
		cs.Token = token
		ts := httptest.NewServer(cs)
		for _, body := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mint_skill","arguments":{"attempt_id":"e1010c47-175-1-0","deadline":"2099-01-01T00:00:00Z","args":{}}}}`,
		} {
			req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("token %q: %s was answered %d, want 401", token, body, resp.StatusCode)
			}
		}
		ts.Close()
	}
}

// A capability server's redirect must not carry the secret headers Grain
// resolved for it, nor the request body, to another host.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Int64
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"elsewhere"}}}`)
	}))
	defer elsewhere.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	cfg := ServerConfig{ServerURL: redirector.URL, Headers: map[string]string{"X-Api-Key": "s3cret", "Authorization": "Bearer s3cret"}}
	for _, opts := range []DialOptions{{}, {HTTPClient: &http.Client{}}} {
		c, err := Dial(context.Background(), cfg, opts)
		if err == nil {
			c.Close()
			t.Errorf("a redirected initialize succeeded")
		} else if !strings.Contains(err.Error(), "HTTP 307") {
			t.Errorf("err = %v, want the 307 surfaced", err)
		}
	}
	if leaked.Load() != 0 {
		t.Errorf("secret headers followed the redirect %d time(s)", leaked.Load())
	}
}

func TestSSEAnswer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Mcp-Session-Id", "s1")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\n")
		fmt.Fprint(w, "data: \"result\":{\"serverInfo\":{\"name\":\"sse\"}}}\n\n")
	}))
	defer ts.Close()
	c, err := Dial(context.Background(), ServerConfig{ServerURL: ts.URL}, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerName != "sse" {
		t.Errorf("ServerName = %q", c.ServerName)
	}
}

func TestMethodExpiresLeasesAfterGrace(t *testing.T) {
	now := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	var revoked []string
	m, err := NewMethod(MethodSpec{
		Name: "lease",
		Now:  func() time.Time { return now },
		Mint: func(context.Context, string, time.Time, map[string]any) (*MintResponse, error) {
			return &MintResponse{Status: StatusOK}, nil
		},
		Release: func(_ context.Context, id string) error { revoked = append(revoked, id); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	mint := m.tools()[0].handle
	deadline := now.Add(time.Hour).Format(time.RFC3339)
	if _, err := mint(context.Background(), map[string]any{"attempt_id": "a-1-1-0", "deadline": deadline, "args": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	out, _ := mint(context.Background(), map[string]any{"attempt_id": "a-1-1-1", "deadline": now.Add(-time.Minute).Format(time.RFC3339), "args": map[string]any{}})
	if !strings.Contains(string(out), StatusRefused) {
		t.Errorf("a deadline in the past was minted: %s", out)
	}
	now = now.Add(time.Hour + DefaultGrace - time.Second)
	if got := m.Expire(context.Background()); len(got) != 0 {
		t.Errorf("expired %v inside the grace period", got)
	}
	now = now.Add(2 * time.Second)
	if got := m.Expire(context.Background()); len(got) != 1 || len(revoked) != 1 || m.Leases() != 0 {
		t.Errorf("Expire = %v, revoked %v, %d leases left", got, revoked, m.Leases())
	}
}
