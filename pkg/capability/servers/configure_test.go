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
	"strings"
	"sync"
	"testing"
	"time"

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
	return protocol.AttachCaller(ctx, id, cfg, secrets, &waitingCaller{fakeCaller: fakeCaller{methods: []string{"key"}}, h: h})
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
