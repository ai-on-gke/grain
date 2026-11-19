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

package servers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/authfix/authfixtest"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// configCaller is a server advertising configure (§4.5) with a secret
// token field, recording each config it is sent. failWith makes configure
// fail, echoing the token it was handed into its error, as a careless
// server would.
type configCaller struct {
	fakeCaller
	mu       sync.Mutex
	configs  []map[string]any
	failWith string
}

func (c *configCaller) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	tools, err := c.fakeCaller.ListTools(ctx)
	return append(tools, protocol.Tool{Name: protocol.ConfigureTool, InputSchema: map[string]any{
		"type": "object", "required": []any{"config"},
		"properties": map[string]any{"config": map[string]any{
			"type": "object", "required": []any{"project", "token"},
			"properties": map[string]any{
				"project": map[string]any{"type": "string"},
				"token":   map[string]any{"type": "string", "writeOnly": true},
			},
		}},
	}}), err
}

func (c *configCaller) CallTool(_ context.Context, name string, args map[string]any) ([]byte, error) {
	if name != protocol.ConfigureTool {
		return nil, errors.New("not used")
	}
	cfg, _ := args["config"].(map[string]any)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configs = append(c.configs, cfg)
	if c.failWith != "" {
		return nil, fmt.Errorf("%s (token %v)", c.failWith, cfg["token"])
	}
	return []byte(`{"status":"ok"}`), nil
}

type configHarness struct {
	mu       sync.Mutex
	callers  []*configCaller
	failWith string
}

func (h *configHarness) attach(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
	c := &configCaller{fakeCaller: fakeCaller{methods: []string{"key"}}, failWith: h.failWith}
	h.mu.Lock()
	h.callers = append(h.callers, c)
	h.mu.Unlock()
	return protocol.AttachCaller(ctx, id, cfg, secrets, c)
}

func (h *configHarness) sent() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]any
	for _, c := range h.callers {
		out = append(out, c.configs...)
	}
	return out
}

const secretValue = "hunter2-very-secret"

func TestConfigureIsSentOnAttachAndAgainOnChange(t *testing.T) {
	h := &configHarness{}
	var logs strings.Builder
	r := New(Options{
		Attach:  h.attach,
		Secrets: func(name string) (string, bool) { return secretValue, name == "tok" },
		Logf:    func(f string, a ...any) { fmt.Fprintf(&logs, f+"\n", a...) },
	})
	ctx := context.Background()
	cfg := protocol.ServerConfig{Command: "grain", Config: map[string]any{"project": "p1", "token": "$secret:tok"}}
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: cfg})
	if _, err := r.Lookup(gcp); err != nil {
		t.Fatal(err)
	}
	sent := h.sent()
	if len(sent) != 1 || sent[0]["project"] != "p1" || sent[0]["token"] != secretValue {
		t.Fatalf("configure was sent %v, want one call with the secret resolved", sent)
	}
	st := r.Statuses()[0]
	if st.ConfigSchema == nil || len(protocol.SecretFields(st.ConfigSchema)) != 1 {
		t.Errorf("status carries config schema %v", st.ConfigSchema)
	}

	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: cfg})
	if n := len(h.sent()); n != 1 {
		t.Errorf("an unchanged configuration was sent again (%d calls)", n)
	}

	changed := cfg
	changed.Config = map[string]any{"project": "p2", "token": "$secret:tok"}
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: changed})
	sent = h.sent()
	if len(sent) != 2 || sent[1]["project"] != "p2" {
		t.Fatalf("after a change configure was sent %v, want the new config", sent)
	}
	if _, err := r.Lookup(gcp); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), secretValue) {
		t.Errorf("the log carries the secret:\n%s", logs.String())
	}
}

func TestAFailedConfigureMarksTheServerUnhealthyAndRefusesMints(t *testing.T) {
	h := &configHarness{failWith: "project p1 does not exist"}
	var logs strings.Builder
	r := New(Options{
		Attach:  h.attach,
		Secrets: func(name string) (string, bool) { return secretValue, true },
		Logf:    func(f string, a ...any) { fmt.Fprintf(&logs, f+"\n", a...) },
	})
	r.Sync(context.Background(), map[string]protocol.ServerConfig{gcp: {Command: "grain", Config: map[string]any{"project": "p1", "token": "$secret:tok"}}})
	st := r.Statuses()[0]
	if st.State != Failed || !strings.Contains(st.Error, "does not exist") || st.ConfigSchema == nil {
		t.Fatalf("status = %+v, want failed with configure's error and the schema kept", st)
	}
	if _, err := r.Lookup(gcp); err == nil || !strings.Contains(err.Error(), "configure") {
		t.Errorf("Lookup = %v, want mints refused with the configure error", err)
	}
	for what, s := range map[string]string{"status": st.Error, "log": logs.String()} {
		if strings.Contains(s, secretValue) {
			t.Errorf("the %s carries the secret: %s", what, s)
		}
	}
	if !h.callers[0].closed {
		t.Errorf("the refused server's session was left open")
	}

	// A secret field stored in clear is refused before anything is sent.
	h2 := &configHarness{}
	r2 := New(Options{Attach: h2.attach})
	r2.Sync(context.Background(), map[string]protocol.ServerConfig{gcp: {Command: "grain", Config: map[string]any{"project": "p1", "token": secretValue}}})
	if st := r2.Statuses()[0]; st.State != Failed || strings.Contains(st.Error, secretValue) || len(h2.sent()) != 0 {
		t.Errorf("clear secret: status %+v, sent %v", st, h2.sent())
	}
}

func TestProbeReportsWithoutAttaching(t *testing.T) {
	h := &configHarness{failWith: "bad project"}
	r := New(Options{Attach: h.attach})
	ctx := context.Background()
	cfg := protocol.ServerConfig{Command: "grain", Config: map[string]any{"project": "p1", "token": "$secret:typed"}}
	res := r.Probe(ctx, gcp, cfg, func(name string) (string, bool) { return secretValue, name == "typed" })
	if res.OK || !strings.Contains(res.Error, "bad project") || strings.Contains(res.Error, secretValue) || res.ConfigSchema == nil {
		t.Errorf("failing probe = %+v", res)
	}
	h.failWith = ""
	res = r.Probe(ctx, gcp, cfg, func(name string) (string, bool) { return secretValue, name == "typed" })
	if !res.OK || len(res.Methods) != 1 || res.ConfigSchema == nil {
		t.Errorf("probe = %+v", res)
	}
	// A method that declares no mcpDelivery keeps its tools model-only,
	// and the probe says so (design/spec/capabilities/capabilities.spec.md §5.2.6).
	if len(res.Methods) == 1 && (res.Methods[0].Delivery != protocol.DeliveryModelOnly || res.Methods[0].DeliveryDeclared) {
		t.Errorf("probe method delivery = %q (declared %v), want the model_only default", res.Methods[0].Delivery, res.Methods[0].DeliveryDeclared)
	}
	if !h.callers[1].closed {
		t.Errorf("the probe's session was left open")
	}
	if len(r.Statuses()) != 0 {
		t.Errorf("probing attached something: %+v", r.Statuses())
	}
	if _, err := r.HostCapabilityEntries(ctx); !errors.Is(err, ErrNoHostd) {
		t.Errorf("HostCapabilityEntries with no hostd = %v", err)
	}
}

// A built-in attached under one config that refuses the next is a drop
// (Options.OnDrop) -- the 2026-11-03 role-ceiling save nobody heard of --
// and attaching again is what clears it (Options.OnAttach).
func TestRefusedReconfigureIsReportedAsADrop(t *testing.T) {
	h := &configHarness{}
	var drops []Status
	var attaches []string
	r := New(Options{
		Attach:   h.attach,
		Secrets:  func(name string) (string, bool) { return secretValue, name == "tok" },
		OnDrop:   func(st Status) { drops = append(drops, st) },
		OnAttach: func(id string) { attaches = append(attaches, id) },
	})
	ctx := context.Background()
	good := protocol.ServerConfig{Builtin: "gcp", Command: "grain", Config: map[string]any{"project": "p1", "token": "$secret:tok"}}
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: good})
	if len(drops) != 0 || len(attaches) != 1 {
		t.Fatalf("first attach: drops %v, attaches %v", drops, attaches)
	}

	h.failWith = "roles/run.admin exceeds admin ambient-host's role ceiling"
	bad := good
	bad.Config = map[string]any{"project": "p2", "token": "$secret:tok"}
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: bad})
	if len(drops) != 1 {
		t.Fatalf("a refused reconfigure raised %d drops, want 1", len(drops))
	}
	d := drops[0]
	if d.ID != gcp || d.State != NeedsConfig || !d.ConfigRefused || !strings.Contains(d.Reason, "role ceiling") {
		t.Errorf("drop = %+v, want %s needs-config, refused, naming the ceiling", d, gcp)
	}
	if strings.Contains(d.Reason, secretValue) {
		t.Errorf("drop reason carries the secret: %q", d.Reason)
	}
	if st := r.Statuses()[0]; !st.ConfigRefused {
		t.Errorf("status = %+v, want ConfigRefused", st)
	}

	// Retried with the same refusal: said once, not every retry.
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: bad})
	if len(drops) != 1 {
		t.Errorf("an unchanged refusal was reported again (%d drops)", len(drops))
	}

	h.failWith = ""
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: good})
	if len(attaches) != 2 {
		t.Errorf("attaches = %v, want the fixed config to attach again", attaches)
	}
	if st := r.Statuses()[0]; st.ConfigRefused || st.State != Attached {
		t.Errorf("status after the fix = %+v", st)
	}
}

// A server refusing its saved config on its first attach -- after a
// restart, with nothing attached before it -- is reported too; a
// built-in with no config yet is only waiting, and is not.
func TestRefusedConfigOnFirstAttachIsReported(t *testing.T) {
	h := &configHarness{failWith: "bad config"}
	var drops []Status
	r := New(Options{Attach: h.attach, OnDrop: func(st Status) { drops = append(drops, st) }})
	ctx := context.Background()
	r.Sync(ctx, map[string]protocol.ServerConfig{
		gcp:            {Builtin: "gcp", Command: "grain", Config: map[string]any{"project": "p1", "token": "x"}},
		"https://e/hs": {Builtin: "host-script", Command: "grain"},
	})
	if len(drops) != 1 || drops[0].ID != gcp {
		t.Fatalf("drops = %+v, want only %s", drops, gcp)
	}
}

// A refresh failure that echoes a configured secret or managed token is
// scrubbed before reaching Statuses, logs or OnDrop, and disabling or
// removing the entry reports OnClear.
func TestRefreshDropScrubsSecretsAndDisableOrRemoveClears(t *testing.T) {
	h := &configHarness{}
	var drops []Status
	var cleared []string
	var logs strings.Builder
	now := DefaultRefreshInterval
	r := New(Options{
		Attach:  h.attach,
		Secrets: func(name string) (string, bool) { return secretValue, name == "tok" },
		Now:     func() time.Time { return time.Unix(0, 0).Add(now) },
		Logf:    func(f string, a ...any) { fmt.Fprintf(&logs, f+"\n", a...) },
		OnDrop:  func(st Status) { drops = append(drops, st) },
		OnClear: func(id string) { cleared = append(cleared, id) },
	})
	ctx := context.Background()
	cfg := protocol.ServerConfig{Command: "grain", Config: map[string]any{"project": "p1", "token": "$secret:tok"}}
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: cfg})
	h.mu.Lock()
	h.callers[0].fakeCaller.mu.Lock()
	h.callers[0].listErr = errors.New("upstream rejected bearer " + secretValue)
	h.callers[0].fakeCaller.mu.Unlock()
	h.mu.Unlock()
	now += DefaultRefreshInterval
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: cfg})
	if len(drops) != 1 {
		t.Fatalf("drops = %+v, want 1", drops)
	}
	for what, s := range map[string]string{"drop": drops[0].Error, "status": r.Statuses()[0].Error, "log": logs.String()} {
		if strings.Contains(s, secretValue) || !strings.Contains(s, "[redacted]") {
			t.Errorf("%s = %q, want secret scrubbed", what, s)
		}
	}

	disabled := cfg
	disabled.Disabled = true
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: disabled})
	if len(cleared) != 1 || cleared[0] != gcp {
		t.Errorf("cleared on disable = %v, want [%s]", cleared, gcp)
	}
	r.Sync(ctx, map[string]protocol.ServerConfig{})
	if len(cleared) != 2 || cleared[1] != gcp {
		t.Errorf("cleared on remove = %v, want [%s %s]", cleared, gcp, gcp)
	}
}

// Probe generates managed values only in memory for that probe, leaving
// the persistent ManagedSecrets store untouched when an unsaved or refused
// configuration is probed.
func TestProbeDoesNotPersistGeneratedManagedSecrets(t *testing.T) {
	persisted := 0
	managed := &countingManaged{onEnsure: func() { persisted++ }}
	attach := func(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, opts protocol.DialOptions) (*protocol.Server, error) {
		c := &refusingTokenCaller{}
		return protocol.AttachCallerManaged(ctx, id, cfg, secrets, c, opts.Identity, opts.Managed)
	}
	r := New(Options{Attach: attach, Managed: managed})
	res := r.Probe(context.Background(), gcp, protocol.ServerConfig{Command: "grain", Config: map[string]any{}}, nil)
	if !res.ConfigRefused {
		t.Fatalf("Probe = %+v, want ConfigRefused", res)
	}
	if persisted != 0 {
		t.Errorf("Probe persisted %d managed secret(s) to the store, want 0", persisted)
	}
}

type refusingTokenCaller struct{ tokenCaller }

func (c *refusingTokenCaller) CallTool(context.Context, string, map[string]any) ([]byte, error) {
	return nil, errors.New("role ceiling exceeded")
}

type countingManaged struct{ onEnsure func() }

func (m *countingManaged) Ensure(string, func() (string, error)) error {
	m.onEnsure()
	return nil
}

func (m *countingManaged) Rotate(string, func() (string, error)) error {
	m.onEnsure()
	return nil
}

// waitingCaller is a built-in whose configure takes an empty config and
// refuses it, while waiting is set, as waiting on something outside
// grain (protocol.WaitingError) -- a host credential that has expired --
// or, with refuse set, refuses it outright.
type waitingCaller struct {
	fakeCaller
	h *waitingHarness
}

type waitingHarness struct {
	mu        sync.Mutex
	waiting   string
	refuse    string
	configure int
	callers   []*waitingCaller
}

func (c *waitingCaller) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	tools, err := c.fakeCaller.ListTools(ctx)
	return append(tools, protocol.Tool{Name: protocol.ConfigureTool, InputSchema: map[string]any{
		"type": "object", "required": []any{"config"},
		"properties": map[string]any{"config": map[string]any{"type": "object", "properties": map[string]any{}}},
	}}), err
}

func (c *waitingCaller) CallTool(_ context.Context, name string, _ map[string]any) ([]byte, error) {
	if name != protocol.ConfigureTool {
		return nil, errors.New("not used")
	}
	c.h.mu.Lock()
	defer c.h.mu.Unlock()
	c.h.configure++
	if c.h.waiting != "" {
		return nil, protocol.Waiting(c.h.waiting, errors.New("the host credential expired an hour ago"))
	}
	if c.h.refuse != "" {
		return nil, errors.New(c.h.refuse)
	}
	return []byte(`{"status":"ok"}`), nil
}

func (h *waitingHarness) attach(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
	c := &waitingCaller{fakeCaller: fakeCaller{methods: []string{"key"}}, h: h}
	h.mu.Lock()
	h.callers = append(h.callers, c)
	h.mu.Unlock()
	return protocol.AttachCaller(ctx, id, cfg, secrets, c)
}

func (h *waitingHarness) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.configure
}

// A built-in with no secret refs whose configure refused at start only
// because a host credential had expired (2026-11-10: three servers sat
// "not configured" long after it was renewed) is retried every
// RetryInterval and attaches by itself once it is renewed, reporting
// what it waits for meanwhile; one refused outright still waits for an
// edit.
func TestAWaitingConfigureRefusalIsRetriedUntilItAttaches(t *testing.T) {
	h := &waitingHarness{waiting: "a renewed host credential"}
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	var drops []Status
	var attaches []string
	var logs strings.Builder
	r := New(Options{
		Attach:        h.attach,
		Now:           func() time.Time { return now },
		RetryInterval: time.Minute,
		OnDrop:        func(st Status) { drops = append(drops, st) },
		OnAttach:      func(id string) { attaches = append(attaches, id) },
		Logf:          func(f string, a ...any) { fmt.Fprintf(&logs, f+"\n", a...) },
	})
	ctx := context.Background()
	servers := map[string]protocol.ServerConfig{gcp: {Builtin: "corp-thing", Command: "grain"}}
	r.Sync(ctx, servers)
	st := r.Statuses()[0]
	if st.State != NeedsConfig || st.Waiting != "a renewed host credential" || st.ConfigRefused || !strings.Contains(st.Reason, "expired") {
		t.Fatalf("status = %+v, want needs-config waiting for the credential, its reason kept, not refused", st)
	}
	if WaitingLabel(st) != "waiting for a renewed host credential" {
		t.Errorf("WaitingLabel = %q", WaitingLabel(st))
	}
	if len(drops) != 1 || drops[0].Waiting == "" {
		t.Errorf("drops = %+v, want the wait raised once", drops)
	}
	if !strings.Contains(logs.String(), "waiting for a renewed host credential, retrying every 1m0s") {
		t.Errorf("log does not say it is waiting and retrying:\n%s", logs.String())
	}

	now = now.Add(30 * time.Second)
	r.Sync(ctx, servers)
	if n := h.calls(); n != 1 {
		t.Errorf("retried before RetryInterval: %d configures", n)
	}
	now = now.Add(31 * time.Second)
	r.Sync(ctx, servers)
	if n := h.calls(); n != 2 {
		t.Errorf("not retried once RetryInterval passed: %d configures", n)
	}
	if len(drops) != 1 {
		t.Errorf("an unchanged wait was raised again (%d drops)", len(drops))
	}

	h.mu.Lock()
	h.waiting = ""
	h.mu.Unlock()
	now = now.Add(time.Minute)
	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != Attached || st.Waiting != "" || st.Reason != "" {
		t.Fatalf("after the renewal status = %+v, want attached", st)
	}
	if len(attaches) != 1 {
		t.Errorf("attaches = %v, want the renewal to attach it", attaches)
	}

	// A configure that refuses without waiting is not retried: it needs
	// somebody's edit.
	h2 := &waitingHarness{refuse: "no project is set"}
	r2 := New(Options{Attach: h2.attach, Now: func() time.Time { return now }, RetryInterval: time.Minute})
	r2.Sync(ctx, servers)
	now = now.Add(2 * time.Minute)
	r2.Sync(ctx, servers)
	if st := r2.Statuses()[0]; st.State != NeedsConfig || st.Waiting != "" || h2.calls() != 1 {
		t.Errorf("plain refusal: status %+v after %d configures, want needs-config tried once", st, h2.calls())
	}
}

// A configured built-in whose data-plane port is occupied fails in
// configure at runtime, not because its saved configuration is wrong
// (2026-11-13 hosts-env-drain F3): the registry marks it failed (not
// needs-config with ConfigRefused), logs why it could not start rather
// than "not configured", and retries on RetryInterval so freeing the port
// attaches it without restarting the daemon.
func TestAConfigureStartFailureMarksABuiltinFailedAndRetriesOncePortIsFree(t *testing.T) {
	const gitID = "https://grain.dev/capabilities/git"
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := held.Addr().(*net.TCPAddr).Port
	listenAddr := held.Addr().String()
	t.Cleanup(func() { _ = held.Close() })

	gitBuilder, ok := intree.Lookup("git")
	if !ok {
		t.Fatal("missing built-in git capability")
	}

	var built []*intree.Server
	t.Cleanup(func() {
		for _, s := range built {
			_ = s.Close(context.Background())
		}
	})
	attach := func(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, opts protocol.DialOptions) (*protocol.Server, error) {
		buildArgs := []string{"-listen", listenAddr}
		if len(cfg.Args) > 2 {
			buildArgs = cfg.Args[2:]
		}
		srv, err := gitBuilder.Build(ctx, buildArgs, func(string) string { return "" }, nil)
		if err != nil {
			return nil, err
		}
		built = append(built, srv)
		srv.CapabilityServer.Token = strings.Repeat("t", protocol.MinHTTPTokenBytes)
		ts := httptest.NewServer(srv.CapabilityServer)
		httpCfg := protocol.ServerConfig{
			ServerURL: ts.URL,
			Headers:   map[string]string{"Authorization": "Bearer " + srv.CapabilityServer.Token},
			Config:    cfg.Config,
		}
		ps, err := protocol.Attach(ctx, id, httpCfg, secrets, opts)
		if err != nil {
			ts.Close()
			_ = srv.Close(ctx)
			return nil, err
		}
		t.Cleanup(ts.Close)
		return ps, nil
	}

	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var drops []Status
	var attaches []string
	var logs strings.Builder
	r := New(Options{
		Attach:        attach,
		Secrets:       func(name string) (string, bool) { return secretValue, name == "walk-git/token" },
		Now:           func() time.Time { return now },
		RetryInterval: time.Minute,
		OnDrop:        func(st Status) { drops = append(drops, st) },
		OnAttach:      func(id string) { attaches = append(attaches, id) },
		Logf:          func(f string, a ...any) { fmt.Fprintf(&logs, f+"\n", a...) },
	})
	t.Cleanup(r.Close)

	ctx := context.Background()
	servers := map[string]protocol.ServerConfig{
		gitID: {
			Command: "grain",
			Args:    []string{"capability", "git"},
			Builtin: "git",
			Config: map[string]any{
				"upstream": "http://127.0.0.1:9",
				"repos":    []any{"acme/widgets"},
				"token":    "$secret:walk-git/token",
			},
		},
	}

	r.Sync(ctx, servers)
	st := r.Statuses()[0]
	wantLabel := fmt.Sprintf("could not start: port %d in use", port)
	if st.State != Failed || st.ConfigRefused || st.StartError != wantLabel || st.ConfigSchema == nil {
		t.Fatalf("status with port held = %+v, want Failed, !ConfigRefused, StartError=%q, ConfigSchema kept", st, wantLabel)
	}
	if st.Reason != "" || !strings.Contains(st.Error, "address already in use") || strings.Contains(st.Error, secretValue) {
		t.Errorf("status error/reason = %q / %q, want bind error in Error, empty Reason, no secret", st.Error, st.Reason)
	}
	if _, err := r.Lookup(gitID); err == nil || strings.Contains(err.Error(), "needs configuring") {
		t.Errorf("Lookup(git) = %v, want not-attached error rather than needs configuring", err)
	}
	if len(drops) != 1 || drops[0].ConfigRefused || drops[0].StartError != wantLabel {
		t.Errorf("drops = %+v, want 1 start-failure drop without ConfigRefused", drops)
	}
	if !strings.Contains(logs.String(), wantLabel+", retrying every 1m0s") || strings.Contains(logs.String(), "not configured") {
		t.Errorf("logs = %q, want %q and never 'not configured'", logs.String(), wantLabel)
	}

	// Probe on a start failure keeps the schema without marking ConfigRefused.
	probeRes := r.Probe(ctx, gitID, servers[gitID], nil)
	if probeRes.OK || probeRes.ConfigRefused || probeRes.ConfigSchema == nil {
		t.Errorf("Probe with port held = %+v, want !OK, !ConfigRefused, ConfigSchema kept", probeRes)
	}
	// Probe on a non-loopback -listen address is still a configuration refusal,
	// so Settings refuses to save it rather than treating it as a start error.
	badListen := servers[gitID]
	badListen.Args = []string{"capability", "git", "-listen", "0.0.0.0:4254"}
	if badRes := r.Probe(ctx, gitID, badListen, nil); badRes.OK || !badRes.ConfigRefused {
		t.Errorf("Probe with non-loopback -listen = %+v, want !OK and ConfigRefused", badRes)
	}
	// A pending Update clears StartError on Statuses until Sync runs.
	r.Update(map[string]protocol.ServerConfig{gitID: badListen})
	if pst := r.Statuses()[0]; pst.State != Pending || pst.StartError != "" || pst.ConfigRefused {
		t.Errorf("Statuses while Pending = %+v, want Pending with StartError cleared", pst)
	}
	r.Update(servers)

	// Free the port: after RetryInterval the next Sync attaches with no restart.
	_ = held.Close()
	now = now.Add(time.Minute)
	r.Sync(ctx, servers)
	st = r.Statuses()[0]
	if st.State != Attached || st.StartError != "" || st.ConfigRefused || st.Error != "" {
		t.Fatalf("status once port is free = %+v, want Attached with StartError cleared", st)
	}
	if len(attaches) != 1 || attaches[0] != gitID {
		t.Errorf("attaches = %v, want [%s]", attaches, gitID)
	}

	// A built-in with no SecretRefs whose configure returns a non-port
	// runtime StartFailure is also Failed ("could not start") and retried.
	const hsID = "https://grain.dev/capabilities/host-script"
	failStart := true
	hsAttach := func(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
		return protocol.AttachCaller(ctx, id, cfg, secrets, &startFailCaller{
			fakeCaller: fakeCaller{methods: []string{"host-script"}},
			fail:       failStart,
		})
	}
	r2 := New(Options{Attach: hsAttach, Now: func() time.Time { return now }, RetryInterval: time.Minute})
	t.Cleanup(r2.Close)
	hsCfg := map[string]protocol.ServerConfig{
		hsID: {Command: "grain", Builtin: "host-script", Config: map[string]any{"scripts": []any{}}},
	}
	r2.Sync(ctx, hsCfg)
	if st := r2.Statuses()[0]; st.State != Failed || st.ConfigRefused || st.StartError != "could not start" {
		t.Fatalf("non-port start failure status = %+v, want Failed with StartError='could not start'", st)
	}
	failStart = false
	now = now.Add(time.Minute)
	r2.Sync(ctx, hsCfg)
	if st := r2.Statuses()[0]; st.State != Attached || st.StartError != "" {
		t.Fatalf("after runtime recovery status = %+v, want Attached", st)
	}

	for msg, want := range map[string]string{
		"git: listen tcp 127.0.0.1:4254: bind: address already in use": "could not start: port 4254 in use",
		"git: listen tcp6 [::1]:4254: bind: address already in use":    "could not start: port 4254 in use",
		"listen tcp :9733: address already in use":                     "could not start: port 9733 in use",
		"git: listen tcp 127.0.0.1:80: bind: permission denied":        "could not start",
		"host-script: data directory: mkdir /ro/hs: read-only":         "could not start",
	} {
		if got := startErrorLabel(msg); got != want {
			t.Errorf("startErrorLabel(%q) = %q, want %q", msg, got, want)
		}
	}
}

type startFailCaller struct {
	fakeCaller
	fail bool
}

func (c *startFailCaller) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	tools, err := c.fakeCaller.ListTools(ctx)
	return append(tools, protocol.Tool{Name: protocol.ConfigureTool, InputSchema: map[string]any{
		"type": "object", "required": []any{"config"},
		"properties": map[string]any{"config": map[string]any{"type": "object", "properties": map[string]any{}}},
	}}), err
}

func (c *startFailCaller) CallTool(_ context.Context, name string, _ map[string]any) ([]byte, error) {
	if name != protocol.ConfigureTool {
		return nil, errors.New("not used")
	}
	if c.fail {
		return nil, protocol.StartFailure(errors.New("mkdir /readonly/hs: read-only file system"))
	}
	return []byte(`{"status":"ok"}`), nil
}

// scriptedAttach answers each attach with the next of results: an error,
// or nil for a server offering methods (none when methods is empty).
type scriptedAttach struct {
	mu      sync.Mutex
	results []error
	methods []string
	calls   int
}

func (s *scriptedAttach) attach(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
	s.mu.Lock()
	var err error
	if s.calls < len(s.results) {
		err = s.results[s.calls]
	}
	s.calls++
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return protocol.AttachCaller(ctx, id, cfg, secrets, &fakeCaller{methods: s.methods})
}

// An unconfigured built-in that could not start raises an alert
// (Options.OnDrop); when a later retry comes up into the needs-config an
// unconfigured built-in rests in -- which nothing raises and Sync no
// longer retries -- that alert is cleared (Options.OnClear), not left open
// with `grain health` clean.
func TestAStartFailureAlertClearsWhenAnUnconfiguredBuiltinSettlesIntoNeedsConfig(t *testing.T) {
	const gitID = "https://grain.dev/capabilities/git"
	for name, settle := range map[string]error{
		// configure refusing an empty config (the attach error path)
		"configure refuses": &protocol.ConfigureError{ID: gitID, Err: errors.New("no repositories are configured")},
		// a server offering no methods until configured
		"no methods": nil,
	} {
		t.Run(name, func(t *testing.T) {
			sa := &scriptedAttach{results: []error{fmt.Errorf("attach: %w", protocol.ErrClosedOutput), settle}}
			now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
			var drops []Status
			var cleared, attaches []string
			r := New(Options{
				Attach:        sa.attach,
				Now:           func() time.Time { return now },
				RetryInterval: time.Minute,
				OnDrop:        func(st Status) { drops = append(drops, st) },
				OnClear:       func(id string) { cleared = append(cleared, id) },
				OnAttach:      func(id string) { attaches = append(attaches, id) },
			})
			t.Cleanup(r.Close)
			ctx := context.Background()
			servers := map[string]protocol.ServerConfig{gitID: {Command: "grain", Args: []string{"capability", "git"}, Builtin: "git"}}

			r.Sync(ctx, servers)
			if st := r.Statuses()[0]; st.State != Failed || st.StartError == "" {
				t.Fatalf("after the exit status = %+v, want failed with a start error", st)
			}
			if len(drops) != 1 || len(cleared) != 0 {
				t.Fatalf("drops = %+v, cleared = %v, want one start-failure drop", drops, cleared)
			}

			now = now.Add(time.Minute)
			r.Sync(ctx, servers)
			if st := r.Statuses()[0]; st.State != NeedsConfig || st.StartError != "" || st.ConfigRefused || st.Waiting != "" {
				t.Fatalf("after the retry status = %+v, want an unalerted needs-config", st)
			}
			if len(drops) != 1 || len(cleared) != 1 || cleared[0] != gitID || len(attaches) != 0 {
				t.Errorf("drops = %d, cleared = %v, attaches = %v, want the start-failure alert cleared once", len(drops), cleared, attaches)
			}

			// Resting there clears nothing again.
			now = now.Add(time.Hour)
			r.Sync(ctx, servers)
			if len(cleared) != 1 || sa.calls != 2 {
				t.Errorf("cleared = %v after %d attaches, want one clear and no retry", cleared, sa.calls)
			}
		})
	}
}

// A configured built-in that never attached and could not start raises
// an alert; clearing its config back to {} -- which replaces the entry
// rather than removing it -- leaves it in an unalerted needs-config, so
// the alert is cleared there too.
func TestAStartFailureAlertClearsWhenABuiltinsConfigIsCleared(t *testing.T) {
	const gitID = "https://grain.dev/capabilities/git"
	sa := &scriptedAttach{results: []error{
		&protocol.ConfigureError{ID: gitID, Err: protocol.StartFailure(errors.New("listen tcp 127.0.0.1:4254: bind: address already in use"))},
		&protocol.ConfigureError{ID: gitID, Err: errors.New("no repositories are configured")},
	}}
	var drops []Status
	var cleared []string
	r := New(Options{
		Attach:  sa.attach,
		Secrets: func(name string) (string, bool) { return secretValue, true },
		OnDrop:  func(st Status) { drops = append(drops, st) },
		OnClear: func(id string) { cleared = append(cleared, id) },
	})
	t.Cleanup(r.Close)
	ctx := context.Background()
	configured := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "git"}, Builtin: "git", Config: map[string]any{"repos": []any{"acme/widgets"}, "token": "$secret:tok"}}

	r.Sync(ctx, map[string]protocol.ServerConfig{gitID: configured})
	if st := r.Statuses()[0]; st.State != Failed || st.StartError != "could not start: port 4254 in use" {
		t.Fatalf("with the port held status = %+v, want failed on the port", st)
	}
	if len(drops) != 1 {
		t.Fatalf("drops = %+v, want one start-failure drop", drops)
	}

	unconfigured := configured
	unconfigured.Config = map[string]any{}
	r.Sync(ctx, map[string]protocol.ServerConfig{gitID: unconfigured})
	if st := r.Statuses()[0]; st.State != NeedsConfig || st.StartError != "" || st.ConfigRefused {
		t.Fatalf("after clearing its config status = %+v, want an unalerted needs-config", st)
	}
	if len(drops) != 1 || len(cleared) != 1 || cleared[0] != gitID {
		t.Errorf("drops = %d, cleared = %v, want the start-failure alert cleared", len(drops), cleared)
	}
}

// An unconfigured built-in resting in needs-config with no alert open
// clears nothing: OnClear is for an alert some drop raised.
func TestAnUnconfiguredBuiltinWithNoAlertClearsNothing(t *testing.T) {
	const gitID = "https://grain.dev/capabilities/git"
	sa := &scriptedAttach{}
	var cleared []string
	r := New(Options{Attach: sa.attach, OnDrop: func(Status) {}, OnClear: func(id string) { cleared = append(cleared, id) }})
	t.Cleanup(r.Close)
	r.Sync(context.Background(), map[string]protocol.ServerConfig{gitID: {Command: "grain", Args: []string{"capability", "git"}, Builtin: "git"}})
	if st := r.Statuses()[0]; st.State != NeedsConfig || len(cleared) != 0 {
		t.Errorf("status = %+v, cleared = %v, want needs-config and nothing cleared", st, cleared)
	}
}

// A built-in that already attached under its current ServerConfig and
// Identity (such as srcfs on hostd before its 24h process recycle) has a
// working configuration: if re-attaching after its process ends fails in
// configure without protocol.Waiting (for example /mnt/srcfs/cloud
// returning ENOKEY while host credentials are expired), it is marked
// Failed rather than parked in NeedsConfig, retried every RetryInterval,
// and re-attached automatically once the host recovers.
func TestAPreviouslyAttachedBuiltinWhoseProcessRecycleFailsConfigureIsFailedAndRetried(t *testing.T) {
	const srcfsID = "https://grain.dev/capabilities/srcfs"
	var (
		mu       sync.Mutex
		refuse   string
		last     *waitingCaller
		attaches int
	)
	h := &waitingHarness{}
	attach := func(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
		mu.Lock()
		attaches++
		h.mu.Lock()
		h.refuse = refuse
		h.mu.Unlock()
		c := &waitingCaller{fakeCaller: fakeCaller{methods: []string{"workspace", "submit", "probe"}}, h: h}
		last = c
		mu.Unlock()
		return protocol.AttachCaller(ctx, id, cfg, secrets, c)
	}

	now := time.Date(2026, 11, 13, 16, 25, 32, 0, time.UTC)
	var drops []Status
	var onAttaches []string
	r := New(Options{
		Attach:        attach,
		Now:           func() time.Time { return now },
		RetryInterval: time.Minute,
		OnDrop:        func(st Status) { drops = append(drops, st) },
		OnAttach:      func(id string) { onAttaches = append(onAttaches, id) },
	})
	ctx := context.Background()
	servers := map[string]protocol.ServerConfig{
		srcfsID: {Builtin: "srcfs", Command: "grain-srcfs", Run: protocol.RunHost},
	}

	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != Attached || st.Dropped {
		t.Fatalf("initial status = %+v, want attached", st)
	}
	if len(onAttaches) != 1 {
		t.Fatalf("onAttaches = %v, want 1", onAttaches)
	}

	// 24 hours later hostd recycles the process while /mnt/srcfs/cloud is unavailable.
	now = now.Add(24 * time.Hour)
	mu.Lock()
	refuse = "srcfs offers no method until this is fixed: the srcfs mount /mnt/srcfs/cloud is not available on this host (is srcfsd running?)"
	last.mu.Lock()
	last.ended = true
	last.mu.Unlock()
	mu.Unlock()

	r.Sync(ctx, servers)
	st := r.Statuses()[0]
	if st.State != Failed || st.ConfigRefused || !st.Dropped || st.ConfigSchema == nil || !strings.Contains(st.Error, "/mnt/srcfs/cloud") {
		t.Fatalf("status after recycle drop = %+v, want Failed with Dropped=true, ConfigRefused=false, schema and error kept", st)
	}
	if _, err := r.Lookup(srcfsID); err == nil || strings.Contains(err.Error(), "needs configuring") {
		t.Errorf("Lookup = %v, want not-attached error rather than needs-configuring", err)
	}
	if len(drops) != 1 || drops[0].State != Failed {
		t.Fatalf("drops = %+v, want one Failed drop", drops)
	}

	// Before RetryInterval, Sync does not retry; after RetryInterval, it retries without re-raising OnDrop.
	now = now.Add(30 * time.Second)
	r.Sync(ctx, servers)
	if h.calls() != 2 {
		t.Errorf("retried before RetryInterval: %d configures, want 2", h.calls())
	}
	now = now.Add(31 * time.Second)
	r.Sync(ctx, servers)
	if h.calls() != 3 {
		t.Errorf("not retried after RetryInterval: %d configures, want 3", h.calls())
	}
	if len(drops) != 1 {
		t.Errorf("unchanged failure re-raised OnDrop (%d drops)", len(drops))
	}

	// Once a credential refresh / srcfsd restores /mnt/srcfs/cloud, the next minute tick re-attaches automatically.
	mu.Lock()
	refuse = ""
	mu.Unlock()
	now = now.Add(time.Minute)
	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != Attached || st.Dropped || st.Error != "" {
		t.Fatalf("status after host recovery = %+v, want attached", st)
	}
	if len(onAttaches) != 2 {
		t.Errorf("onAttaches = %v, want OnAttach fired to clear the alert", onAttaches)
	}
}

// A HostOnly built-in that requires no operator configuration
// (NeedsConfig: false) whose configure refuses on cold daemon start because
// its host mount is not ready yet is retried every RetryInterval rather
// than waiting forever for a Settings edit.
func TestAHostOnlyZeroConfigBuiltinRefusedAtColdStartIsRetried(t *testing.T) {
	const name = "test-host-mount"
	intree.Register(intree.Builder{
		Name:        name,
		Summary:     "test host-only workspace server",
		Host:        true,
		HostOnly:    true,
		NeedsConfig: false,
		Build: func(context.Context, []string, intree.Env, *log.Logger) (*intree.Server, error) {
			return nil, errors.New("unused")
		},
	})
	t.Cleanup(func() { intree.Unregister(name) })

	id := intree.CanonicalID(name)
	h := &waitingHarness{refuse: "host mount /mnt/srcfs/cloud is not mounted yet"}
	now := time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC)
	var drops []Status
	r := New(Options{
		Attach:        h.attach,
		Now:           func() time.Time { return now },
		RetryInterval: time.Minute,
		OnDrop:        func(st Status) { drops = append(drops, st) },
	})
	ctx := context.Background()
	servers := map[string]protocol.ServerConfig{
		id: {Builtin: name, Command: intree.HostEntry(name), Run: protocol.RunHost},
	}

	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != NeedsConfig || st.ConfigRefused || st.Dropped {
		t.Fatalf("cold-start status = %+v, want NeedsConfig without ConfigRefused or Dropped", st)
	}
	if len(drops) != 0 {
		t.Errorf("cold start raised OnDrop: %+v", drops)
	}

	h.mu.Lock()
	h.refuse = ""
	h.mu.Unlock()
	now = now.Add(time.Minute)
	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != Attached {
		t.Fatalf("status after RetryInterval = %+v (calls=%d), want attached", st, h.calls())
	}
}

// Retry wakes an unattached NeedsConfig entry even when its stored
// ServerConfig has not changed, without firing duplicate OnDrop alerts if
// the refusal still holds.
func TestRetryReattachesUnchangedNeedsConfigWithoutDuplicateDrops(t *testing.T) {
	h := &configHarness{failWith: "role ceiling exceeded"}
	var drops []Status
	var attaches []string
	r := New(Options{
		Attach:   h.attach,
		Secrets:  func(name string) (string, bool) { return secretValue, name == "tok" },
		OnDrop:   func(st Status) { drops = append(drops, st) },
		OnAttach: func(id string) { attaches = append(attaches, id) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := map[string]protocol.ServerConfig{
		gcp: {Builtin: "gcp", Command: "grain", Config: map[string]any{"project": "p1", "token": "$secret:tok"}},
	}
	r.Update(cfg)
	done := make(chan struct{})
	go func() { r.Run(ctx, time.Hour); close(done) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if st := r.Statuses(); len(st) == 1 && st[0].State == NeedsConfig {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for initial NeedsConfig: %+v", r.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Note: cfg references $secret:tok; also verify Retry on a config with no secret refs.
	noSecretID := "https://grain.dev/capabilities/host-script"
	hNoSecret := &waitingHarness{refuse: "external check failed"}
	var nsDrops []Status
	rNoSecret := New(Options{
		Attach: hNoSecret.attach,
		OnDrop: func(st Status) { nsDrops = append(nsDrops, st) },
	})
	nsCfg := map[string]protocol.ServerConfig{
		noSecretID: {Builtin: "host-script", Command: "grain", Config: map[string]any{"scripts": []any{}}},
	}
	rNoSecret.Sync(context.Background(), nsCfg)
	if len(nsDrops) != 1 || hNoSecret.calls() != 1 {
		t.Fatalf("initial Sync: drops=%d calls=%d, want 1 each", len(nsDrops), hNoSecret.calls())
	}
	if !rNoSecret.Retry(noSecretID) {
		t.Fatal("Retry(NeedsConfig) = false, want true")
	}
	rNoSecret.Sync(context.Background(), nsCfg)
	if hNoSecret.calls() != 2 || len(nsDrops) != 1 {
		t.Fatalf("after Retry while still failing: calls=%d drops=%d, want 2 calls and 1 drop", hNoSecret.calls(), len(nsDrops))
	}

	hNoSecret.mu.Lock()
	hNoSecret.refuse = ""
	hNoSecret.mu.Unlock()
	if !rNoSecret.Retry(noSecretID) {
		t.Fatal("second Retry(NeedsConfig) = false, want true")
	}
	rNoSecret.Sync(context.Background(), nsCfg)
	if st := rNoSecret.Statuses()[0]; st.State != Attached {
		t.Fatalf("after Retry once fixed: status = %+v, want Attached", st)
	}
	if rNoSecret.Retry(noSecretID) {
		t.Error("Retry on an already Attached server = true, want false")
	}
	_ = attaches
}

// A built-in that already attached with empty Config and later exits while
// configure temporarily fails without WaitingError fires OnDrop once,
// retries after RetryInterval, and re-attaches once the failure clears.
func TestPreviouslyAttachedServerWhoseReattachConfigureFailsIsRetried(t *testing.T) {
	h := &waitingHarness{}
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	var drops []Status
	var attaches []string
	r := New(Options{
		Attach:        h.attach,
		Now:           func() time.Time { return now },
		RetryInterval: time.Minute,
		OnDrop:        func(st Status) { drops = append(drops, st) },
		OnAttach:      func(id string) { attaches = append(attaches, id) },
	})
	ctx := context.Background()
	servers := map[string]protocol.ServerConfig{gcp: {Builtin: "srcfs", Command: "grain"}}
	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != Attached {
		t.Fatalf("first attach status = %+v, want attached", st)
	}
	if len(attaches) != 1 || len(drops) != 0 {
		t.Fatalf("first attach: attaches=%v drops=%v", attaches, drops)
	}

	h.mu.Lock()
	first := h.callers[0]
	h.refuse = "host daemon temporarily unavailable"
	h.mu.Unlock()
	first.mu.Lock()
	first.ended = true
	first.mu.Unlock()

	now = now.Add(24 * time.Hour)
	r.Sync(ctx, servers)
	st := r.Statuses()[0]
	if st.State != Failed || st.ConfigRefused || !strings.Contains(st.Error, "temporarily unavailable") {
		t.Fatalf("after process exit with failing configure: status = %+v, want Failed with error and ConfigRefused=false", st)
	}
	if len(drops) != 1 {
		t.Fatalf("drops after first re-attach failure = %+v, want 1", drops)
	}

	now = now.Add(30 * time.Second)
	r.Sync(ctx, servers)
	if n := h.calls(); n != 2 {
		t.Fatalf("retried before RetryInterval: %d configures", n)
	}

	now = now.Add(31 * time.Second)
	r.Sync(ctx, servers)
	if n := h.calls(); n != 3 {
		t.Fatalf("not retried after RetryInterval: %d configures, want 3", n)
	}
	if len(drops) != 1 {
		t.Errorf("OnDrop fired %d times on unchanged retry, want 1", len(drops))
	}

	h.mu.Lock()
	h.refuse = ""
	h.mu.Unlock()
	now = now.Add(time.Minute)
	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != Attached || st.Error != "" {
		t.Fatalf("status after recovery = %+v, want attached", st)
	}
	if len(attaches) != 2 {
		t.Errorf("attaches = %v, want 2", attaches)
	}
}

// An attach or configure failure recognized by a registered pkg/authfix
// classifier enters Waiting (NeedsConfig with Waiting populated) both on
// cold startup and after a process recycle, and retries every RetryInterval.
func TestAuthfixClassifiedAttachFailureWaitsAndRetries(t *testing.T) {
	authfixtest.Register(t)
	h := &waitingHarness{refuse: "fork/exec /opt/bin/srcfs: " + authfixtest.Reason}
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	var drops []Status
	var attaches []string
	r := New(Options{
		Attach:        h.attach,
		Now:           func() time.Time { return now },
		RetryInterval: time.Minute,
		OnDrop:        func(st Status) { drops = append(drops, st) },
		OnAttach:      func(id string) { attaches = append(attaches, id) },
	})
	ctx := context.Background()
	servers := map[string]protocol.ServerConfig{gcp: {Builtin: "srcfs", Command: "grain-srcfs", Run: protocol.RunHost}}
	r.Sync(ctx, servers)
	st := r.Statuses()[0]
	if st.State != NeedsConfig || st.Waiting != authfixtest.Action || st.ConfigRefused || !strings.Contains(st.Reason, authfixtest.Reason) {
		t.Fatalf("cold-start status = %+v, want needs-config with authfix Waiting=%q and Reason", st, authfixtest.Action)
	}
	if len(drops) != 1 || drops[0].Waiting != authfixtest.Action {
		t.Fatalf("drops = %+v, want 1 drop carrying Waiting", drops)
	}

	now = now.Add(30 * time.Second)
	r.Sync(ctx, servers)
	if n := h.calls(); n != 1 {
		t.Fatalf("retried before RetryInterval: %d configures", n)
	}

	now = now.Add(31 * time.Second)
	r.Sync(ctx, servers)
	if n := h.calls(); n != 2 {
		t.Fatalf("not retried after RetryInterval: %d configures, want 2", n)
	}
	if len(drops) != 1 {
		t.Errorf("unchanged authfix wait raised %d drops, want 1", len(drops))
	}

	h.mu.Lock()
	h.refuse = ""
	h.mu.Unlock()
	now = now.Add(time.Minute)
	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != Attached || st.Waiting != "" || st.Reason != "" {
		t.Fatalf("status after renewing credentials = %+v, want attached", st)
	}
	if len(attaches) != 1 {
		t.Errorf("attaches = %v, want 1", attaches)
	}

	// If credentials expire again before a later 24h process recycle, re-attach
	// also enters Waiting rather than generic Failed.
	h.mu.Lock()
	attachedCaller := h.callers[len(h.callers)-1]
	h.refuse = "fork/exec /opt/bin/srcfs: " + authfixtest.Reason
	h.mu.Unlock()
	attachedCaller.mu.Lock()
	attachedCaller.ended = true
	attachedCaller.mu.Unlock()

	now = now.Add(24 * time.Hour)
	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != NeedsConfig || st.Waiting != authfixtest.Action {
		t.Fatalf("post-recycle authfix status = %+v, want needs-config with Waiting=%q", st, authfixtest.Action)
	}
}

func TestReattachRetriesUnchangedNeedsConfigEntry(t *testing.T) {
	h := &waitingHarness{refuse: "external service not ready"}
	now := time.Date(2026, 11, 16, 11, 0, 0, 0, time.UTC)
	r := New(Options{
		Attach:        h.attach,
		Now:           func() time.Time { return now },
		RetryInterval: time.Minute,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	servers := map[string]protocol.ServerConfig{gcp: {Builtin: "srcfs", Command: "grain"}}

	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != NeedsConfig || h.calls() != 1 {
		t.Fatalf("initial status = %+v (%d calls), want NeedsConfig after 1 call", st, h.calls())
	}

	h.mu.Lock()
	h.refuse = ""
	h.mu.Unlock()
	now = now.Add(2 * time.Minute)
	r.Sync(ctx, servers)
	if st := r.Statuses()[0]; st.State != NeedsConfig || h.calls() != 1 {
		t.Fatalf("unchanged Sync retried without Reattach: status=%+v calls=%d", st, h.calls())
	}

	done := make(chan struct{})
	go func() { r.Run(ctx, time.Hour); close(done) }()
	if !r.Retry(gcp) {
		t.Fatal("Retry(NeedsConfig) = false, want true")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if st := r.Statuses(); len(st) == 1 && st[0].State == Attached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Reattach did not attach the server; status = %+v", r.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := h.calls(); n != 2 {
		t.Errorf("configure calls after Reattach = %d, want 2", n)
	}
	cancel()
	<-done
}

// Remote HTTP servers must not trigger local host ExecClassifiers (which
// disclose Who() in Status.Waiting and suppress ConfigRefused), non-built-in
// servers must not populate Status.Waiting or fire cold-start OnDrop alerts,
// and resolved secrets are scrubbed from Waiting and classifier stderr input.
func TestWaitingGuardsHTTPNonBuiltinAndSecretScrubbing(t *testing.T) {
	authfixtest.Register(t)
	ctx := context.Background()

	// 1. Remote HTTP server (!cfg.Stdio()): error text matching an ExecClassifier
	// must not populate Waiting or clear ConfigRefused.
	httpHarness := &waitingHarness{refuse: authfixtest.Reason}
	rHTTP := New(Options{Attach: httpHarness.attach})
	rHTTP.Sync(ctx, map[string]protocol.ServerConfig{
		gcp: {
			Builtin:   "srcfs",
			ServerURL: "https://cap.example.test/mcp",
			Config:    map[string]any{"project": "demo"},
		},
	})
	stHTTP := rHTTP.Statuses()[0]
	if stHTTP.Waiting != "" || !stHTTP.ConfigRefused {
		t.Fatalf("remote HTTP server status = %+v, want Waiting=\"\" and ConfigRefused=true", stHTTP)
	}

	// 2. Non-built-in server (Builtin == ""): neither protocol.WaitingError nor
	// authfix populates Status.Waiting or raises a cold-start OnDrop alert.
	customWait := &waitingHarness{waiting: "spoofed wait phrase"}
	var customDrops []Status
	rCustom := New(Options{
		Attach: customWait.attach,
		OnDrop: func(st Status) { customDrops = append(customDrops, st) },
	})
	rCustom.Sync(ctx, map[string]protocol.ServerConfig{
		"https://custom.example.test/cap": {Command: "custom-cap", Run: protocol.RunHost},
	})
	stCustom := rCustom.Statuses()[0]
	if stCustom.State != Failed || stCustom.Waiting != "" {
		t.Fatalf("non-built-in WaitingError status = %+v, want Failed with Waiting=\"\"", stCustom)
	}
	if len(customDrops) != 0 {
		t.Fatalf("cold-start non-built-in WaitingError fired OnDrop: %+v", customDrops)
	}

	// 3. Resolved secret values are scrubbed both from protocol.WaitingError.Waiting
	// and before stderr reaches registered ExecClassifiers.
	const secretVal = "super-secret-cap-token-99"
	var seenStderr string
	unreg := authfix.RegisterExec("00-scrub-check", func(_ error, stderr string) (authfix.Fix, bool) {
		seenStderr = stderr
		if strings.Contains(stderr, "scrub-trigger") {
			return authfix.Fix{Action: "renew with " + secretVal}, true
		}
		return authfix.Fix{}, false
	})
	defer unreg()

	rScrub := New(Options{
		Secrets: func(name string) (string, bool) {
			if name == "tok" {
				return secretVal, true
			}
			return "", false
		},
		Attach: func(_ context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
			if _, err := cfg.Resolve(secrets); err != nil {
				return nil, err
			}
			return nil, &protocol.ConfigureError{
				ID:  id,
				Err: errors.New("scrub-trigger token=" + secretVal),
			}
		},
	})
	rScrub.Sync(ctx, map[string]protocol.ServerConfig{
		gcp: {Builtin: "srcfs", Command: "grain-srcfs", Env: map[string]string{"TOKEN": "$secret:tok"}},
	})
	stScrub := rScrub.Statuses()[0]
	if strings.Contains(seenStderr, secretVal) {
		t.Errorf("ExecClassifier received unscrubbed secret in stderr: %q", seenStderr)
	}
	if strings.Contains(stScrub.Waiting, secretVal) || strings.Contains(stScrub.Reason, secretVal) {
		t.Errorf("status leaked secret value: Waiting=%q Reason=%q", stScrub.Waiting, stScrub.Reason)
	}
	if stScrub.Waiting != "renew with [redacted]" {
		t.Errorf("Waiting = %q, want %q", stScrub.Waiting, "renew with [redacted]")
	}

	rWaitScrub := New(Options{
		Secrets: func(name string) (string, bool) {
			if name == "tok" {
				return secretVal, true
			}
			return "", false
		},
		Attach: func(_ context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
			if _, err := cfg.Resolve(secrets); err != nil {
				return nil, err
			}
			return nil, &protocol.ConfigureError{
				ID:  id,
				Err: protocol.Waiting("token "+secretVal+" expired", errors.New("bad "+secretVal)),
			}
		},
	})
	rWaitScrub.Sync(ctx, map[string]protocol.ServerConfig{
		gcp: {Builtin: "srcfs", Command: "grain-srcfs", Env: map[string]string{"TOKEN": "$secret:tok"}},
	})
	stWaitScrub := rWaitScrub.Statuses()[0]
	if strings.Contains(stWaitScrub.Waiting, secretVal) || strings.Contains(stWaitScrub.Reason, secretVal) {
		t.Errorf("WaitingError leaked secret value: Waiting=%q Reason=%q", stWaitScrub.Waiting, stWaitScrub.Reason)
	}
	if stWaitScrub.Waiting != "token [redacted] expired" {
		t.Errorf("WaitingError Waiting = %q, want %q", stWaitScrub.Waiting, "token [redacted] expired")
	}
}
