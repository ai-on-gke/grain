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

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

type fakeCaller struct {
	mu      sync.Mutex
	methods []string
	listErr error
	lists   int
	closed  bool
	ended   bool
}

func (f *fakeCaller) Ended() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended
}

func (f *fakeCaller) ListTools(context.Context) ([]protocol.Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var tools []protocol.Tool
	for _, m := range f.methods {
		tools = append(tools, protocol.Tool{Name: "mint_" + m, InputSchema: map[string]any{
			"type": "object", "required": []any{"attempt_id", "deadline", "args"},
			"properties": map[string]any{"attempt_id": map[string]any{"type": "string"}},
		}})
	}
	return tools, nil
}

func (f *fakeCaller) CallTool(context.Context, string, map[string]any) ([]byte, error) {
	return nil, errors.New("not used")
}

func (f *fakeCaller) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// harness attaches every server to a fakeCaller of its own, resolving
// secrets the way protocol.Attach does so a missing one is refused.
type harness struct {
	mu      sync.Mutex
	callers map[string]*fakeCaller
	attachs map[string]int
	dialErr map[string]string
	roots   []string
	idents  []protocol.Identity
}

func newHarness() *harness {
	return &harness{callers: map[string]*fakeCaller{}, attachs: map[string]int{}, dialErr: map[string]string{}}
}

func (h *harness) attach(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, opts protocol.DialOptions) (*protocol.Server, error) {
	h.mu.Lock()
	h.attachs[id]++
	h.roots = append(h.roots, opts.ConfigRoot)
	h.idents = append(h.idents, opts.Identity)
	dialErr := h.dialErr[id]
	h.mu.Unlock()
	if cfg.Disabled {
		return nil, errors.New("disabled entries must never be attached")
	}
	resolved, err := cfg.Resolve(secrets)
	if err != nil {
		return nil, err
	}
	if dialErr != "" {
		// A server that echoes its credential back in an error.
		return nil, errors.New(dialErr + " " + resolved.Env["TOKEN"])
	}
	c := &fakeCaller{methods: []string{"key"}}
	h.mu.Lock()
	h.callers[id] = c
	h.mu.Unlock()
	return protocol.NewServer(ctx, id, c)
}

const (
	gcp   = "https://github.com/bwsalmon/grain/capabilities/gcp"
	other = "https://example.com/caps/other"
)

func TestSyncAttachesLooksUpAndRefusesUnmappedOrDisabled(t *testing.T) {
	h := newHarness()
	r := New(Options{Attach: h.attach, ConfigRoot: "/data/state-repo"})
	ctx := context.Background()
	r.Sync(ctx, map[string]protocol.ServerConfig{
		gcp:   {Command: "grain", Args: []string{"capability", "gcp"}},
		other: {ServerURL: "https://cap.example.com/mcp", Disabled: true},
	})

	s, err := r.Lookup(gcp)
	if err != nil || s == nil || s.Methods["key"] == nil {
		t.Fatalf("Lookup(gcp) = %v, %v; want the attached server with method key", s, err)
	}
	if _, err := r.Lookup(other); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("Lookup(disabled) error = %v, want a refusal saying it is disabled", err)
	}
	if _, err := r.Lookup("https://example.com/unmapped"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("Lookup(unmapped) error = %v, want a refusal saying it is not configured", err)
	}
	if h.attachs[other] != 0 {
		t.Errorf("the disabled server was attached %d times", h.attachs[other])
	}
	if len(h.roots) != 1 || h.roots[0] != "/data/state-repo" {
		t.Errorf("attach was given config roots %q, want the grain/config checkout", h.roots)
	}
	st := r.Statuses()
	// Sorted by capability_id: example.com before github.com.
	if len(st) != 2 || st[1].ID != gcp || st[1].State != Attached || st[1].Transport != "stdio" ||
		len(st[1].Methods) != 1 || st[0].State != Disabled || st[0].Transport != "http" {
		t.Errorf("Statuses() = %+v", st)
	}
}

func TestAMissingSecretRefusesTheAttachAndARetryPicksItUp(t *testing.T) {
	h := newHarness()
	var mu sync.Mutex
	store := map[string]string{}
	now := time.Unix(1000, 0)
	r := New(Options{
		Attach: h.attach,
		Secrets: func(name string) (string, bool) {
			mu.Lock()
			defer mu.Unlock()
			v, ok := store[name]
			return v, ok
		},
		Now: func() time.Time { return now },
	})
	cfg := map[string]protocol.ServerConfig{gcp: {Command: "grain", Env: map[string]string{"TOKEN": "$secret:gcp-minter"}}}
	ctx := context.Background()
	r.Sync(ctx, cfg)
	_, err := r.Lookup(gcp)
	if err == nil || !strings.Contains(err.Error(), "gcp-minter") {
		t.Fatalf("Lookup error = %v, want the missing secret named", err)
	}
	if st := r.Statuses(); st[0].State != Failed || !strings.Contains(st[0].Error, "gcp-minter") {
		t.Errorf("status = %+v, want failed naming the secret", st[0])
	}

	mu.Lock()
	store["gcp-minter"] = "s3cret-value"
	mu.Unlock()
	r.Sync(ctx, cfg) // not yet due
	if h.attachs[gcp] != 1 {
		t.Fatalf("attached %d times before the retry was due", h.attachs[gcp])
	}
	now = now.Add(DefaultRetryInterval)
	r.Sync(ctx, cfg)
	if _, err := r.Lookup(gcp); err != nil {
		t.Fatalf("Lookup after the secret was added and the retry came due: %v", err)
	}
}

func TestAnAttachErrorNeverCarriesASecretValue(t *testing.T) {
	h := newHarness()
	h.dialErr[gcp] = "server said: bad token"
	r := New(Options{Attach: h.attach, Secrets: func(string) (string, bool) { return "hunter2-very-secret", true }})
	r.Sync(context.Background(), map[string]protocol.ServerConfig{gcp: {Command: "grain", Env: map[string]string{"TOKEN": "$secret:tok"}}})
	st := r.Statuses()[0]
	if st.State != Failed || strings.Contains(st.Error, "hunter2") || !strings.Contains(st.Error, "[redacted]") {
		t.Errorf("status = %+v, want a failure with the value redacted", st)
	}
	if _, err := r.Lookup(gcp); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Lookup error = %v", err)
	}
}

func TestRefreshOnReloadHourlyAndDetachOnRemoval(t *testing.T) {
	h := newHarness()
	now := time.Unix(1000, 0)
	r := New(Options{Attach: h.attach, Now: func() time.Time { return now }})
	ctx := context.Background()
	cfg := map[string]protocol.ServerConfig{gcp: {Command: "grain"}}
	r.Sync(ctx, cfg)
	c := h.callers[gcp]
	if c.lists != 1 {
		t.Fatalf("tools/list issued %d times on attach, want 1", c.lists)
	}
	r.Sync(ctx, cfg)
	if c.lists != 1 {
		t.Errorf("an unchanged configuration, not yet due, re-listed (%d)", c.lists)
	}
	now = now.Add(time.Hour)
	r.Sync(ctx, cfg)
	if c.lists != 2 {
		t.Errorf("tools/list issued %d times after an hour, want 2", c.lists)
	}

	// A reload -- another entry added -- refreshes the one kept.
	cfg2 := map[string]protocol.ServerConfig{gcp: {Command: "grain"}, other: {ServerURL: "https://cap.example.com/mcp"}}
	r.Sync(ctx, cfg2)
	if c.lists != 3 || h.attachs[gcp] != 1 {
		t.Errorf("after a reload: %d lists, %d attaches; want a refresh, not a re-attach", c.lists, h.attachs[gcp])
	}

	// A changed entry is detached and attached afresh.
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: {Command: "grain", Args: []string{"capability", "gcp"}}})
	if !c.closed || h.attachs[gcp] != 2 {
		t.Errorf("changed entry: closed=%v attaches=%d", c.closed, h.attachs[gcp])
	}
	if !h.callers[other].closed {
		t.Error("a removed entry's session was not closed")
	}
	if _, err := r.Lookup(other); err == nil {
		t.Error("a removed entry is still looked up")
	}

	// A refresh failure detaches it and is reported.
	c2 := h.callers[gcp]
	c2.listErr = errors.New("session gone")
	now = now.Add(time.Hour)
	r.Sync(ctx, map[string]protocol.ServerConfig{gcp: {Command: "grain", Args: []string{"capability", "gcp"}}})
	if !c2.closed {
		t.Error("a server whose refresh failed was left attached")
	}
	if st := r.Statuses()[0]; st.State != Failed || !strings.Contains(st.Error, "session gone") {
		t.Errorf("status after a failed refresh = %+v", st)
	}
}

// A server whose process ended -- a run: host server that hostd's drain
// ended for its upgrade -- is refused by Lookup at once and attached again
// on the next sync, not left looking attached until its hourly refresh.
func TestAServerWhoseProcessEndedIsAttachedAgain(t *testing.T) {
	h := newHarness()
	now := time.Unix(1000, 0)
	r := New(Options{Attach: h.attach, Now: func() time.Time { return now }})
	ctx := context.Background()
	cfg := map[string]protocol.ServerConfig{gcp: {Command: "fakecap", Run: protocol.RunHost}}
	r.Sync(ctx, cfg)
	first := h.callers[gcp]
	first.mu.Lock()
	first.ended = true
	first.mu.Unlock()

	if _, err := r.Lookup(gcp); err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Errorf("Lookup of a server whose process ended = %v, want it refused as ended", err)
	}
	now = now.Add(time.Second)
	r.Sync(ctx, cfg)
	if !first.closed || h.attachs[gcp] != 2 {
		t.Fatalf("after the process ended: closed=%v attaches=%d; want the old session closed and a fresh attach", first.closed, h.attachs[gcp])
	}
	s, err := r.Lookup(gcp)
	if err != nil || s == nil {
		t.Fatalf("Lookup after the re-attach = %v, %v", s, err)
	}
	if st := r.Statuses()[0]; st.State != Attached {
		t.Errorf("status after the re-attach = %+v", st)
	}
}

func TestRunAppliesUpdates(t *testing.T) {
	h := newHarness()
	r := New(Options{Attach: h.attach})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx, time.Hour); close(done) }()
	r.Update(map[string]protocol.ServerConfig{gcp: {Command: "grain"}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := r.Lookup(gcp); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never attached the updated configuration")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	h.mu.Lock()
	closed := h.callers[gcp].closed
	h.mu.Unlock()
	if !closed {
		t.Error("Run left a server attached after its context ended")
	}
}

func TestABuiltinWaitingForConfigurationNeedsConfigAndAliasesResolve(t *testing.T) {
	const canonical = "https://grain.dev/capabilities/gcp"
	const configMap = "https://grain.dev/capabilities/config-map"
	const legacySkill = "https://grain.dev/capabilities/skill"
	schema := map[string]any{"type": "object"}
	attachCalls := map[string]int{}
	attach := func(ctx context.Context, id string, cfg protocol.ServerConfig, _ protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
		attachCalls[id]++
		switch id {
		case canonical:
			if len(cfg.Config) == 0 {
				return nil, &protocol.ConfigureError{ID: id, Schema: schema, Err: errors.New("gcp: no permission sets are configured")}
			}
			return protocol.NewServer(ctx, id, &fakeCaller{methods: []string{"gcp-key"}})
		case configMap:
			if len(cfg.Config) == 0 {
				// Attaches, but has nothing to offer yet.
				return protocol.NewServer(ctx, id, &fakeCaller{})
			}
			return protocol.NewServer(ctx, id, &fakeCaller{methods: []string{"config-map"}})
		}
		return nil, errors.New("crashed")
	}
	now := time.Unix(1000, 0)
	var logs []string
	r := New(Options{
		Attach: attach,
		Now:    func() time.Time { return now },
		Logf:   func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	})
	ctx := context.Background()
	initialCfg := map[string]protocol.ServerConfig{
		canonical: {Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp", Aliases: []string{gcp}},
		configMap: {Command: "grain", Args: []string{"capability", "config-map"}, Builtin: "config-map"},
		other:     {Command: "broken"},
	}
	r.Sync(ctx, initialCfg)
	byID := map[string]Status{}
	for _, s := range r.Statuses() {
		byID[s.ID] = s
	}
	g := byID[canonical]
	if g.State != NeedsConfig || g.Error != "" || !strings.Contains(g.Reason, "no permission sets") || len(g.Methods) != 0 || g.ConfigSchema == nil {
		t.Errorf("unconfigured gcp = %+v, want needs-config with its reason, its schema and no methods", g)
	}
	if g.Builtin != "gcp" || len(g.Aliases) != 1 || g.Aliases[0] != gcp {
		t.Errorf("gcp status = %+v, want builtin gcp aliased from %s", g, gcp)
	}
	if s := byID[configMap]; s.State != NeedsConfig || len(s.Methods) != 0 {
		t.Errorf("a built-in offering no methods = %+v, want needs-config", s)
	} else if !strings.Contains(s.Reason, "Edit server settings") {
		// Settings shows this reason beside the server; it used to say
		// "open Configure", a control that page does not have.
		t.Errorf("needs-config reason = %q, want it to name the Edit server settings control", s.Reason)
	}
	if s := byID[other]; s.State != Failed {
		t.Errorf("an operator's own broken entry = %+v, want failed, not needs-config", s)
	}
	if _, err := r.Lookup(canonical); err == nil || !strings.Contains(err.Error(), "needs configuring") {
		t.Errorf("Lookup(unconfigured gcp) = %v, want a needs-configuring refusal", err)
	}
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "capability server "+canonical+": not configured:") || !strings.Contains(joinedLogs, "gcp: no permission sets are configured") {
		t.Errorf("logs = %q, want gcp reported as not configured", joinedLogs)
	}
	if strings.Contains(joinedLogs, "capability server "+canonical+": attach refused") {
		t.Errorf("logs = %q, unconfigured built-in should not log attach refused", joinedLogs)
	}

	// Advancing past RetryInterval does not re-attach or re-log unconfigured
	// built-ins, while a genuinely failed server is retried.
	logs = nil
	now = now.Add(DefaultRetryInterval)
	r.Sync(ctx, initialCfg)
	if attachCalls[canonical] != 1 || attachCalls[configMap] != 1 {
		t.Errorf("unconfigured built-ins were retried on the minute tick: gcp=%d config-map=%d, want 1 each", attachCalls[canonical], attachCalls[configMap])
	}
	if attachCalls[other] != 2 {
		t.Errorf("failed server was attached %d times, want 2", attachCalls[other])
	}
	for _, line := range logs {
		if strings.Contains(line, canonical) || strings.Contains(line, configMap) {
			t.Errorf("unconfigured built-in logged again on retry tick: %q", line)
		}
	}

	r.Sync(ctx, map[string]protocol.ServerConfig{
		canonical: {Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp", Aliases: []string{gcp}, Config: map[string]any{"permission_sets": []any{}}},
		configMap: {Command: "grain", Args: []string{"capability", "config-map"}, Builtin: "config-map", Config: map[string]any{"config_maps": []any{}}},
	})
	for _, id := range []string{canonical, gcp} {
		s, err := r.Lookup(id)
		if err != nil || s.ID != canonical || s.Methods["gcp-key"] == nil {
			t.Errorf("Lookup(%s) = %v, %v; want the configured gcp server", id, s, err)
		}
		grants, err := protocol.Normalize([]protocol.GrantSpec{protocol.BareGrant(id + "#gcp-key"), protocol.BareGrant(id)}, r.Lookup)
		if err != nil || len(grants) != 1 || grants[0].Ref != (protocol.Ref{CapabilityID: canonical, Method: "gcp-key"}) {
			t.Errorf("Normalize(%s) = %+v, %v; want one canonical gcp-key grant", id, grants, err)
		}
	}
	for _, id := range []string{configMap, legacySkill} {
		s, err := r.Lookup(id)
		if err != nil || s.ID != configMap || s.Methods["config-map"] == nil {
			t.Errorf("Lookup(%s) = %v, %v; want the configured config-map server", id, s, err)
		}
	}
}

func TestProbeReportsWarnings(t *testing.T) {
	h := newHarness()
	r := New(Options{Attach: h.attach, ProbeWarnings: func(id string, cfg protocol.ServerConfig) []string {
		return []string{"shared accounts in " + id}
	}})
	res := r.Probe(context.Background(), gcp, protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}}, nil)
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0] != "shared accounts in "+gcp {
		t.Errorf("Probe = %+v, want OK with the warning", res)
	}
}

func TestEnsureBuiltinsDropsRetiredSelfDebugBeforeSync(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  protocol.ServerConfig
	}{
		{
			name: "default",
			cfg:  intree.BuiltinEntry(intree.Builtin{Name: "self-debug"}),
		},
		{
			name: "with-minted-dataplane-token",
			cfg: protocol.ServerConfig{
				Command: "grain",
				Args:    []string{"capability", "self-debug"},
				Builtin: "self-debug",
				Config:  map[string]any{protocol.DataPlaneTokenConfig: "$secret:capability-server-ef7e460e/dataPlaneToken"},
			},
		},
		{
			name: "with-minted-dataplane-token-and-cleared-builtin",
			cfg: protocol.ServerConfig{
				Command: "grain",
				Args:    []string{"capability", "self-debug"},
				Config:  map[string]any{protocol.DataPlaneTokenConfig: "$secret:capability-server-ef7e460e/dataPlaneToken"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			r := New(Options{Attach: h.attach})
			ensured, _ := intree.EnsureBuiltins(map[string]protocol.ServerConfig{
				intree.CanonicalID("self-debug"): tc.cfg,
			})
			if _, ok := ensured[intree.CanonicalID("self-debug")]; ok {
				t.Fatalf("EnsureBuiltins kept retired self-debug server: %+v", ensured)
			}
			r.Sync(context.Background(), ensured)
			for _, st := range r.Statuses() {
				if st.ID == intree.CanonicalID("self-debug") || st.Builtin == "self-debug" {
					t.Errorf("Statuses includes retired self-debug server: %+v", st)
				}
			}
		})
	}
}

func TestStatusesReportsNewEntriesBeforeTheirFirstSync(t *testing.T) {
	h := newHarness()
	r := New(Options{Attach: h.attach})
	r.Update(map[string]protocol.ServerConfig{
		"https://example.com/new": {Command: "grain", Args: []string{"capability", "nothing"}},
		"https://example.com/off": {Command: "grain", Args: []string{"capability", "nothing"}, Disabled: true},
	})
	got := r.Statuses()
	if len(got) != 2 || got[0].ID != "https://example.com/new" || got[0].State != Pending ||
		got[1].ID != "https://example.com/off" || got[1].State != Disabled {
		t.Fatalf("Statuses() after Update before any Sync = %+v, want new Pending and off Disabled", got)
	}
}

func TestStatusesReportsPendingWhenUpdatedConfigAwaitsSync(t *testing.T) {
	id := intree.CanonicalID("config-map")
	h := newHarness()
	r := New(Options{Attach: h.attach})

	initial := map[string]protocol.ServerConfig{
		id: {Command: "grain", Args: []string{"capability", "config-map"}, Builtin: "config-map"},
	}
	r.Sync(context.Background(), initial)
	if got := r.Statuses(); len(got) != 1 || got[0].State != Attached {
		t.Fatalf("initial Statuses() = %+v, want Attached", got)
	}

	updated := map[string]protocol.ServerConfig{
		id: {
			Command: "grain",
			Args:    []string{"capability", "config-map"},
			Builtin: "config-map",
			Config:  map[string]any{"config_maps": []any{map[string]any{"name": "app"}}},
		},
	}
	r.Update(updated)
	if got := r.Statuses(); len(got) != 1 || got[0].State != Pending {
		t.Fatalf("Statuses() after Update before Sync = %+v, want Pending", got)
	}
}

// A run: host built-in that never started -- its hostd entry missing --
// is retried, not left waiting for a Configure it cannot offer: once the
// entry is there it attaches with no daemon restart, and meanwhile its
// status carries the built-in's static schema.
func TestARunHostBuiltinThatNeverStartedIsRetried(t *testing.T) {
	const id = "https://grain.dev/capabilities/gcp"
	entryThere := false
	calls := 0
	attach := func(ctx context.Context, id string, cfg protocol.ServerConfig, _ protocol.SecretLookup, _ protocol.DialOptions) (*protocol.Server, error) {
		calls++
		if !entryThere {
			return nil, errors.New(`hostd refused: no capabilities entry "grain-gcp"`)
		}
		return protocol.NewServer(ctx, id, &fakeCaller{methods: []string{"gcp-key"}})
	}
	now := time.Unix(1000, 0)
	r := New(Options{Attach: attach, Now: func() time.Time { return now }})
	cfg := map[string]protocol.ServerConfig{id: {Command: "grain-gcp", Run: protocol.RunHost, Builtin: "gcp"}}
	ctx := context.Background()
	r.Sync(ctx, cfg)
	st := r.Statuses()[0]
	if st.State != NeedsConfig || st.ConfigSchema == nil {
		t.Fatalf("status = %+v, want needs-config with gcp's static schema", st)
	}
	entryThere = true
	now = now.Add(DefaultRetryInterval)
	r.Sync(ctx, cfg)
	if st := r.Statuses()[0]; st.State != Attached || calls != 2 {
		t.Fatalf("after the entry appeared: %+v (%d attaches), want attached on the retry", st, calls)
	}
}
