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
	// and the probe says so (docs/capabilities-spec.md §5.2.6).
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
