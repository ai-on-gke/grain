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

package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/repowrite"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// These mirror pkg/orchestrator/capability_pass_internal_test.go, for a
// CI job's grant admission rather than an agent task's.

const ciPassTestCap = "https://github.com/bwsalmon/grain/capabilities/gcp"

// ciMintCaller is a capability server with one releasable method,
// gcp-key, answering every mint with reply (an ok by default) and
// recording each call.
type ciMintCaller struct {
	mu    sync.Mutex
	reply func() ([]byte, error)
	calls []string // tool name and attempt_id
}

func (c *ciMintCaller) ListTools(context.Context) ([]protocol.Tool, error) {
	return []protocol.Tool{
		{Name: "mint_gcp-key", InputSchema: map[string]any{
			"type": "object", "required": []any{"attempt_id", "deadline", "args"},
			"properties": map[string]any{
				"attempt_id": map[string]any{"type": "string"},
				"deadline":   map[string]any{"type": "string"},
				"args": map[string]any{
					"type": "object", "additionalProperties": false,
					"properties": map[string]any{"account": map[string]any{"type": "string", "default": "staging"}},
				},
			},
		}},
		{Name: "release_gcp-key", InputSchema: map[string]any{
			"type": "object", "required": []any{"attempt_id"},
			"properties": map[string]any{"attempt_id": map[string]any{"type": "string"}},
		}},
	}, nil
}

func (c *ciMintCaller) CallTool(_ context.Context, name string, args map[string]any) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name+" "+args["attempt_id"].(string))
	if strings.HasPrefix(name, "release_") || c.reply == nil {
		return []byte(`{"status":"ok"}`), nil
	}
	return c.reply()
}

func (c *ciMintCaller) Close() error { return nil }

func (c *ciMintCaller) called() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func (c *ciMintCaller) mints() int {
	n := 0
	for _, call := range c.called() {
		if strings.HasPrefix(call, "mint_") {
			n++
		}
	}
	return n
}

type ciOneServer struct{ srv *protocol.Server }

func (o ciOneServer) Lookup(id string) (*protocol.Server, error) {
	if id != o.srv.ID {
		return nil, &protocol.RefusedError{Reason: id + " is not configured"}
	}
	return o.srv, nil
}

type ciPassHarness struct {
	grants *CiGrantReservations
	caller *ciMintCaller
	config *model.Config
	now    time.Time
	slept  []time.Duration
	waits  []ci.Wait
}

func newCIPassHarness(t *testing.T) *ciPassHarness {
	t.Helper()
	h := &ciPassHarness{
		caller: &ciMintCaller{},
		config: &model.Config{DeploymentID: "dep1"},
		now:    time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
	}
	srv, err := protocol.NewServer(context.Background(), ciPassTestCap, h.caller)
	if err != nil {
		t.Fatal(err)
	}
	h.grants = &CiGrantReservations{
		Capabilities: func() *model.CapabilityRegistry { return model.NewCapabilityRegistry() },
		Servers:      func() orchestrator.CapabilityServers { return ciOneServer{srv} },
		Config:       func(context.Context) (*model.Config, error) { return h.config, nil },
		Now:          func() time.Time { return h.now },
		Sleep: func(_ context.Context, d time.Duration) error {
			h.slept = append(h.slept, d)
			h.now = h.now.Add(d)
			return nil
		},
	}
	return h
}

func (h *ciPassHarness) request(event ci.Event) ci.SandboxRequest {
	return ci.SandboxRequest{
		Name: "c0123abcd-2", Repo: "acme/widgets", Ref: "main", Event: event,
		RunID: "0123abcd", JobAttempt: 2,
		Capabilities: []string{ciPassTestCap + "#gcp-key"},
	}
}

func (h *ciPassHarness) acquire(req ci.SandboxRequest) (func(), error) {
	return h.grants.Acquire(context.Background(), req, func(w ci.Wait) { h.waits = append(h.waits, w) })
}

func TestCIAdmissionMintsHoldsAndReleasesAJobsPass(t *testing.T) {
	h := newCIPassHarness(t)
	req := h.request(ci.EventPush)
	release, err := h.acquire(req)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := h.caller.called(); len(got) != 1 || got[0] != "mint_gcp-key dep1-0123abcd-2-0" {
		t.Fatalf("calls = %q, want one mint for attempt dep1-0123abcd-2-0", got)
	}
	pass := h.grants.Pass(req.Name)
	if pass == nil || len(pass.Grants) != 1 {
		t.Fatalf("held pass = %+v, want the job's one grant", pass)
	}
	if acct := pass.Grants[0].Args["account"]; acct != "staging" {
		t.Errorf("minted args = %v, want the schema's default applied", pass.Grants[0].Args)
	}
	release()
	if got := h.caller.called(); len(got) != 2 || got[1] != "release_gcp-key dep1-0123abcd-2-0" {
		t.Errorf("calls = %q, want the pass released when the job ends", got)
	}
	if h.grants.Pass(req.Name) != nil {
		t.Error("the released pass is still held")
	}
}

func TestCIAdmissionRefusalErrorsTheJobWithTheServersWordsQuoted(t *testing.T) {
	h := newCIPassHarness(t)
	h.caller.reply = func() ([]byte, error) {
		return []byte(`{"status":"refused","reason":"account staging is closed"}`), nil
	}
	_, err := h.acquire(h.request(ci.EventPush))
	if err == nil {
		t.Fatal("a refused grant was admitted")
	}
	for _, want := range []string{"account staging is closed", "untrusted text", "capability:" + ciPassTestCap} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to carry %q", err, want)
		}
	}
	if got := h.caller.called(); len(got) != 2 || !strings.HasPrefix(got[1], "release_") {
		t.Errorf("calls = %q, want the refused grant rolled back", got)
	}
}

func TestCIAdmissionBacksOffTransientFailuresAndErrorsOnTheThird(t *testing.T) {
	h := newCIPassHarness(t)
	h.caller.reply = func() ([]byte, error) { return []byte(`not json`), nil }
	_, err := h.acquire(h.request(ci.EventPush))
	if err == nil || !strings.Contains(err.Error(), "3 attempts in a row failed") {
		t.Fatalf("error = %v, want the job errored after three transient failures", err)
	}
	if n := h.caller.mints(); n != 3 {
		t.Errorf("mints = %d, want 3", n)
	}
	if want := []time.Duration{ciPassBackoffBase, 2 * ciPassBackoffBase}; len(h.slept) != 2 || h.slept[0] != want[0] || h.slept[1] != want[1] {
		t.Errorf("backoff = %v, want %v", h.slept, want)
	}
	if len(h.waits) == 0 || h.waits[0].Cause != ci.WaitForGrant || !strings.Contains(h.waits[0].Reason, "retrying") {
		t.Errorf("waits = %+v, want the job shown waiting on its grant", h.waits)
	}
}

func TestCIAdmissionKeepsAnExhaustedGrantQueuedUntilRetryAfter(t *testing.T) {
	h := newCIPassHarness(t)
	retry := h.now.Add(10 * time.Minute)
	exhausted := true
	h.caller.reply = func() ([]byte, error) {
		if !exhausted {
			return []byte(`{"status":"ok"}`), nil
		}
		exhausted = false
		return json.Marshal(map[string]any{"status": "resource_exhausted", "retry_after": retry.Format(time.RFC3339)})
	}
	release, err := h.acquire(h.request(ci.EventPush))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	if len(h.slept) != 1 || h.slept[0] != 10*time.Minute {
		t.Errorf("waited %v, want until retry_after", h.slept)
	}
	if len(h.waits) == 0 || !strings.Contains(h.waits[0].Reason, "capacity") {
		t.Errorf("waits = %+v, want the job shown queued for capacity", h.waits)
	}
	if h.caller.mints() != 2 {
		t.Errorf("calls = %q, want a mint, its rollback, then a mint after retry_after", h.caller.called())
	}
}

func TestCIAdmissionDeniesUntrustedRunsUnlessThePolicyOptsIn(t *testing.T) {
	h := newCIPassHarness(t)
	req := h.request(ci.EventPullRequest)
	req.Untrusted = true
	if _, err := h.acquire(req); err == nil || !strings.Contains(err.Error(), "untrusted input") {
		t.Fatalf("untrusted pull_request: error = %v, want the default denial", err)
	}
	if got := h.caller.called(); len(got) != 0 {
		t.Fatalf("calls = %q, want nothing minted", got)
	}
	h.config.CapabilityPolicy = model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{{
		Capability: ciPassTestCap + "#gcp-key", RunKinds: []string{model.CapabilityRunCI},
		Events: []string{string(ci.EventPullRequest)}, AllowUntrusted: true,
	}}}
	release, err := h.acquire(req)
	if err != nil {
		t.Fatalf("an opted-in untrusted run was refused: %v", err)
	}
	release()
	// The same rule names the event it admits; a push is not it.
	if _, err := h.acquire(h.request(ci.EventPush)); err == nil || !strings.Contains(err.Error(), "capability policy") {
		t.Errorf("push: error = %v, want the policy refusal", err)
	}
}

func TestCIAdmissionRefusesAReferenceWithNoServersAttached(t *testing.T) {
	h := newCIPassHarness(t)
	h.grants.Servers = nil
	if _, err := h.acquire(h.request(ci.EventPush)); err == nil || !strings.Contains(err.Error(), "attaches no capability servers") {
		t.Fatalf("error = %v, want the refusal", err)
	}
}

func TestResolveCICapabilitiesLeavesReferencesToTheirServers(t *testing.T) {
	cc, resolved, err := ResolveCICapabilities(context.Background(), model.NewCapabilityRegistry(), nil,
		model.RepoRef{Owner: "acme", Name: "widgets"}, "run", ci.EventPush, false, []string{ciPassTestCap})
	if err != nil || len(cc.Task.Grants) != 0 || len(resolved) != 0 {
		t.Fatalf("resolution = %+v, %+v, %v; want the reference left alone", cc.Task.Grants, resolved, err)
	}
}

// ciCommandSandbox is a job's sandbox that records the commands run in it.
type ciCommandSandbox struct {
	mu    sync.Mutex
	paths []string
	fail  string // a setup path that exits 3
}

func (s *ciCommandSandbox) Name() string                              { return "c0123abcd-2" }
func (s *ciCommandSandbox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (s *ciCommandSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (s *ciCommandSandbox) Release(context.Context) error { return nil }
func (s *ciCommandSandbox) RunInSandbox(_ context.Context, argv []string, _ string) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(argv) > 4 {
		s.paths = append(s.paths, argv[4])
		if argv[4] == s.fail {
			return "fatal: no such account\n", "", 3
		}
	}
	return "", "", 0
}

func TestCIPassIsMaterializedIntoEachSandboxOfTheJob(t *testing.T) {
	h := newCIPassHarness(t)
	h.caller.reply = func() ([]byte, error) {
		return []byte(`{"status":"ok","placements":[{"path":"/home/debian/.config/gcp/key.json","content":"k"}],` +
			`"setup":"/home/debian/.config/gcp/setup"}`), nil
	}
	req := h.request(ci.EventPush)
	release, err := h.acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	first := &ciCommandSandbox{}
	if err := h.grants.materializeCIPass(context.Background(), req.Name, first); err != nil {
		t.Fatal(err)
	}
	want := "/home/debian/.config/gcp/key.json /home/debian/.config/gcp/setup"
	if got := strings.Join(first.paths, " "); got != want {
		t.Fatalf("first sandbox ran %q, want the placement then the setup", got)
	}
	// The automatic retry's fresh sandbox gets the same pass, minting
	// nothing.
	retry := &ciCommandSandbox{}
	if err := h.grants.materializeCIPass(context.Background(), req.Name, retry); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(retry.paths, " "); got != want {
		t.Fatalf("retry sandbox ran %q", got)
	}
	if h.caller.mints() != 1 {
		t.Errorf("minted %d times, want once for the whole job", h.caller.mints())
	}
	release()
	if err := h.grants.materializeCIPass(context.Background(), req.Name, &ciCommandSandbox{}); err != nil {
		t.Errorf("a job holding no pass = %v", err)
	}

	// A job with no capability references holds no pass and runs nothing.
	none := &ciCommandSandbox{}
	if err := h.grants.materializeCIPass(context.Background(), "c0123abcd-3", none); err != nil || len(none.paths) != 0 {
		t.Errorf("no pass = %v, ran %v", err, none.paths)
	}
}

func TestCIPassSetupFailureErrorsTheJobWithTheServersOutputQuoted(t *testing.T) {
	h := newCIPassHarness(t)
	h.caller.reply = func() ([]byte, error) {
		return []byte(`{"status":"ok","setup":"/home/debian/.config/gcp/setup"}`), nil
	}
	req := h.request(ci.EventPush)
	release, err := h.acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = h.grants.materializeCIPass(context.Background(), req.Name, &ciCommandSandbox{fail: "/home/debian/.config/gcp/setup"})
	if err == nil {
		t.Fatal("a failed setup command let the job run")
	}
	for _, want := range []string{"/home/debian/.config/gcp/setup", "exited with status 3", "fatal: no such account",
		"untrusted text from capability:" + ciPassTestCap, "none of this job's steps ran"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to carry %q", err, want)
		}
	}
}

// ciToolSandbox is a ciCommandSandbox that exposes a run_command tool, so
// a readopt gets as far as granting the job's repository scopes.
type ciToolSandbox struct{ ciCommandSandbox }

func (s *ciToolSandbox) Tools(context.Context) ([]mcp.Tool, error) {
	return []mcp.Tool{{Name: "run_command", Handler: func(context.Context, map[string]any) mcp.Result { return mcp.Result{} }}}, nil
}

// A readopted CI sandbox holds the repository scopes its job was first
// given and no others: a repo-write a push run declared is refused by the
// capability's event gate when the job starts, so a daemon restart under
// that job must not hand its sandbox write access the start never did.
func TestReadoptGrantsOnlyTheWriteScopesTheEventAdmits(t *testing.T) {
	target := model.RepoRef{Owner: "acme", Name: "deploy"}
	for _, tc := range []struct {
		event ci.Event
		want  int
	}{
		{ci.EventPush, 0},
		{ci.EventPullRequest, 0},
		{ci.EventRelease, 1},
	} {
		t.Run(string(tc.event), func(t *testing.T) {
			owner := CiSandboxes{
				Scopes:      gitproxy.NewReadScopes(),
				WriteScopes: gitproxy.NewWriteScopes(),
				Teams:       gitproxy.NewSandboxTeams(),
				Capabilities: func() *model.CapabilityRegistry {
					reg := model.NewCapabilityRegistry()
					reg.RegisterDynamic(repowrite.DynamicProvider)
					return reg
				},
			}
			sb := &CiSandbox{Owner: owner, Backing: &ciToolSandbox{}, SandboxName: "c0123abcd-2"}
			if err := sb.readoptJob(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"},
				tc.event, false, []string{"repo-write:" + target.String()}); err != nil {
				t.Fatalf("readopt: %v", err)
			}
			if got := owner.WriteScopes.Repos(sb.SandboxName); len(got) != tc.want {
				t.Fatalf("write scopes after readopting a %s job = %v, want %d", tc.event, got, tc.want)
			}
		})
	}
}
