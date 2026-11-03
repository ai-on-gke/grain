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

package packagepublish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/ciapi"
	"github.com/ai-on-gke/grain/pkg/model"
)

// rotating is a CredentialSource whose token changes on every call, the
// way an hour-long token does between a job's start and its last push.
type rotating struct {
	mu    sync.Mutex
	repo  string
	calls []string
	fail  error
}

func (r *rotating) PackageCredential(registry, owner, repo string) (Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, registry+" "+owner+"/"+repo)
	if r.fail != nil {
		return Credential{}, r.fail
	}
	if owner+"/"+repo != r.repo {
		return Credential{}, errors.New("no GitHub credential in this deployment covers " + owner + "/" + repo)
	}
	if registry == "" {
		registry = "ghcr.io"
	}
	return Credential{Registry: registry, Username: "x-access-token",
		Token: fmt.Sprintf("token-%d", len(r.calls)), Name: "default"}, nil
}

// asked is what the ladder has been asked so far. The refresh route
// calls PackageCredential on the listener's goroutine, so the test reads
// calls under the same lock.
func (r *rotating) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// listener is grain's CI API listener with the refresh route mounted,
// as the daemon serves it.
func listener(t *testing.T, refresh *Refresher) *httptest.Server {
	t.Helper()
	endpoint := ciapi.NewEndpoint()
	endpoint.Log = func(string, ...any) {}
	endpoint.Mount(RefreshRoute, refresh)
	srv := httptest.NewServer(endpoint)
	t.Cleanup(srv.Close)
	return srv
}

func placed(m model.Materialization, path string) (model.Placement, bool) {
	for _, p := range m.Placements {
		if p.Path == path {
			return p, true
		}
	}
	return model.Placement{}, false
}

func refreshJob(runID string) model.CapabilityContext {
	cc := job("acme", "widgets")
	cc.CI = true
	cc.Run.ID = runID
	return cc
}

// A job runs its sandbox's copy of the helper, with its paths moved
// into a scratch directory standing in for /home/debian.
func installHelper(t *testing.T, m model.Materialization) (helper string, home string) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("the helper speaks to grain with curl, which this host does not have")
	}
	home = t.TempDir()
	move := func(s string) string {
		return strings.ReplaceAll(s, "/home/debian/", home+"/")
	}
	for _, p := range m.Placements {
		full := move(p.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		content := p.Content
		if p.Path == HelperPath {
			content = move(content)
		}
		mode := os.FileMode(0o600)
		if p.Mode == "0755" {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	return move(HelperPath), home
}

func runHelper(t *testing.T, helper, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(helper, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// The point of the route: a push an hour into a job asks again and is
// handed the token the ladder answers *now*, not the one placed when the
// sandbox was made.
func TestTheHelperHandsEveryPushTheLaddersCurrentToken(t *testing.T) {
	source := &rotating{repo: "acme/widgets"}
	refresh := NewRefresher()
	var (
		logMu sync.Mutex
		lines []string
	)
	refresh.Log = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	srv := listener(t, refresh)
	p := New(Config{Credentials: source, Refresh: refresh, RefreshURL: func() string { return srv.URL }})

	m, err := p.Materialize(context.Background(), refreshJob("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if tok, _ := placed(m, TokenPath); tok.Content != "token-1" {
		t.Fatalf("static token = %q, want the one the ladder answered at the start", tok.Content)
	}
	cfg, ok := placed(m, DockerConfigPath)
	if !ok || !strings.Contains(cfg.Content, `"ghcr.io": "grain"`) {
		t.Fatalf("docker config = %+v, want credHelpers naming grain for ghcr.io", cfg)
	}
	if h, ok := placed(m, HelperPath); !ok || h.Mode != "0755" {
		t.Fatalf("helper placement = %+v", h)
	}
	if m.Lease == nil || m.Lease.Resource == "" {
		t.Fatalf("no lease to revoke the refresh token with: %+v", m.Lease)
	}

	helper, home := installHelper(t, m)
	for _, want := range []string{"token-2", "token-3"} {
		out, stderr, err := runHelper(t, helper, "ghcr.io\n", "get")
		if err != nil {
			t.Fatalf("get: %v\n%s%s", err, out, stderr)
		}
		var got helperCredential
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("get printed %q: %v", out, err)
		}
		if got != (helperCredential{ServerURL: "ghcr.io", Username: "x-access-token", Secret: want}) {
			t.Errorf("get = %+v, want secret %q", got, want)
		}
	}
	// The ladder is asked for the job's own repository, every time.
	for _, call := range source.asked() {
		if call != " acme/widgets" {
			t.Errorf("ladder asked %q, want the job's own repository", call)
		}
	}

	// refresh rewrites the static file for a script that reads it.
	if out, stderr, err := runHelper(t, helper, "", "refresh"); err != nil {
		t.Fatalf("refresh: %v\n%s%s", err, out, stderr)
	}
	data, err := os.ReadFile(filepath.Join(home, strings.TrimPrefix(TokenPath, "/home/debian/")))
	if err != nil || strings.TrimSpace(string(data)) != "token-4" {
		t.Errorf("token file after refresh = %q, %v; want token-4", data, err)
	}

	// store and erase are what `docker login` calls; they keep nothing.
	if _, _, err := runHelper(t, helper, `{"ServerURL":"ghcr.io","Username":"u","Secret":"s"}`, "store"); err != nil {
		t.Errorf("store: %v", err)
	}
	if asked := source.asked(); len(asked) != 4 {
		t.Errorf("ladder asked %d times, want 4", len(asked))
	}

	// The log says who asked and which credential answered, and holds
	// no material: no token, and not the job's refresh token.
	refreshToken, _ := placed(m, RefreshTokenPath)
	logMu.Lock()
	logged := slices.Clone(lines)
	logMu.Unlock()
	for _, line := range logged {
		if strings.Contains(line, "token-") || strings.Contains(line, strings.TrimSpace(refreshToken.Content)) {
			t.Errorf("log line holds material: %q", line)
		}
	}

	// The job ends; its refresh token ends with it.
	if err := p.Revoke(context.Background(), refreshJob("run-1"), *m.Lease); err != nil {
		t.Fatal(err)
	}
	out, _, err := runHelper(t, helper, "ghcr.io\n", "get")
	if err == nil || !strings.Contains(out, "401") {
		t.Errorf("get after the job ended = %q, %v; want a 401", out, err)
	}
}

// A retry of the same job in a fresh sandbox is handed the token its
// first attempt was, so the first attempt's revoke covers both.
func TestARetryOfTheSameJobGetsTheSameRefreshToken(t *testing.T) {
	refresh := NewRefresher()
	p := New(Config{Credentials: &rotating{repo: "acme/widgets"}, Refresh: refresh,
		RefreshURL: func() string { return "http://10.0.0.1:9" }})
	first, err := p.Materialize(context.Background(), refreshJob("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := p.Materialize(context.Background(), refreshJob("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := p.Materialize(context.Background(), refreshJob("run-2"))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := placed(first, RefreshTokenPath)
	b, _ := placed(again, RefreshTokenPath)
	c, _ := placed(other, RefreshTokenPath)
	if a.Content != b.Content || a.Content == c.Content {
		t.Errorf("refresh tokens: first %q, retry %q, another job %q", a.Content, b.Content, c.Content)
	}
	if u, _ := placed(first, RefreshURLPath); u.Content != "http://10.0.0.1:9\n" {
		t.Errorf("refresh URL placed as %q", u.Content)
	}
}

// With no listener to reach -- a daemon that could not serve it, or a
// caller that wires none -- the job gets the static login it always did,
// and docker is not pointed at a helper that has nothing to ask.
func TestWithNoListenerOnlyTheStaticLoginIsPlaced(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no refresher": {RefreshURL: func() string { return "http://10.0.0.1:9" }},
		"no address":   {Refresh: NewRefresher(), RefreshURL: func() string { return "" }},
	} {
		cfg.Credentials = &rotating{repo: "acme/widgets"}
		m, err := New(cfg).Materialize(context.Background(), refreshJob("run-1"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(m.Placements) != 3 || m.Lease != nil {
			t.Errorf("%s: placements %+v, lease %+v; want the three static files alone", name, m.Placements, m.Lease)
		}
	}
}

func TestTheRefreshRouteRefusesWhatItShould(t *testing.T) {
	source := &rotating{repo: "acme/widgets"}
	refresh := NewRefresher()
	refresh.Log = func(string, ...any) {}
	clock := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	refresh.now = func() time.Time { return clock }
	srv := listener(t, refresh)
	p := New(Config{Credentials: source, Refresh: refresh, RefreshURL: func() string { return srv.URL }})
	cc := refreshJob("run-1")
	cc.Now = clock
	cc.Deadline = clock.Add(2 * time.Hour)
	m, err := p.Materialize(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	if m.Lease.ExpiresAt == nil || !m.Lease.ExpiresAt.Equal(cc.Deadline) {
		t.Errorf("lease expires %v, want the job's deadline %v", m.Lease.ExpiresAt, cc.Deadline)
	}
	tok, _ := placed(m, RefreshTokenPath)
	token := strings.TrimSpace(tok.Content)

	get := func(method, path, bearer string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}

	if code, _ := get(http.MethodGet, CredentialRoute, ""); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code, _ := get(http.MethodGet, CredentialRoute, token+"x"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := get(http.MethodPost, CredentialRoute, token); code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", code)
	}
	if code, _ := get(http.MethodGet, RefreshRoute+"other", token); code != http.StatusNotFound {
		t.Errorf("another path: %d", code)
	}
	if code, body := get(http.MethodGet, TokenRoute, token); code != http.StatusOK || strings.TrimSpace(body) == "" {
		t.Errorf("token route: %d %q", code, body)
	}

	// The ladder's refusal reaches the job as its reason, not a 200.
	source.mu.Lock()
	source.fail = errors.New("the App is no longer installed on acme")
	source.mu.Unlock()
	if code, body := get(http.MethodGet, CredentialRoute, token); code != http.StatusBadGateway ||
		!strings.Contains(body, "no longer installed") {
		t.Errorf("ladder refusal: %d %q", code, body)
	}

	// Past the job's deadline the token is dead, revoked or not.
	clock = cc.Deadline
	if code, _ := get(http.MethodGet, CredentialRoute, token); code != http.StatusUnauthorized {
		t.Errorf("past the deadline: %d", code)
	}
	// A CI API token is not a refresh token, nor the other way round.
	if code, _ := get(http.MethodGet, "/api/tasks", token); code != http.StatusUnauthorized {
		t.Errorf("refresh token on grain's API: %d", code)
	}
}
