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

package model_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestConnectionCredentials(t *testing.T) {
	t.Parallel()
	all := []model.AgentBackend{
		{ID: "agy-login", Framework: model.AgentFrameworkAntigravity, AuthMode: model.AntigravityAuthAmbient},
		{ID: "agy-key", Framework: model.AgentFrameworkAntigravity, AuthMode: model.CodexAuthAPIKey},
		{ID: "codex-sub", Framework: model.AgentFrameworkCodex, AuthMode: model.CodexAuthSubscription},
		{ID: "claude-login", Framework: model.AgentFrameworkClaude, AuthMode: model.AntigravityAuthAmbient},
		{ID: "reuses-login", Framework: model.AgentFrameworkAntigravity, CredentialBackendID: "agy-login"},
		{ID: "reuses-key", Framework: model.AgentFrameworkAntigravity, CredentialBackendID: "agy-key"},
		{ID: "legacy", Framework: model.AgentFrameworkClaude},
	}
	want := map[string]string{
		"agy-login":    model.CredentialsAmbient,
		"agy-key":      model.CredentialsStored,
		"codex-sub":    model.CredentialsStored, // grain holds its auth.json
		"claude-login": model.CredentialsAmbient,
		"reuses-login": model.CredentialsAmbient,
		"reuses-key":   model.CredentialsStored,
		"legacy":       model.CredentialsStored,
	}
	for _, b := range all {
		if got := b.Credentials(all); got != want[b.ID] {
			t.Errorf("%s: Credentials = %q, want %q", b.ID, got, want[b.ID])
		}
	}
}

func TestCheckAmbientPlacementRefusesOnlyOnChange(t *testing.T) {
	t.Parallel()
	ambient := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity, AuthMode: model.AntigravityAuthAmbient}
	placed := ambient
	placed.Hostd = "ws-a"
	stored := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity, AuthMode: model.CodexAuthAPIKey}

	for _, tc := range []struct {
		name    string
		next    []model.AgentBackend
		before  []model.AgentBackend
		refused bool
	}{
		{"new ambient connection with no host", []model.AgentBackend{ambient}, nil, true},
		{"new ambient connection placed on a host", []model.AgentBackend{placed}, nil, false},
		{"stored credentials turned ambient with no host", []model.AgentBackend{ambient}, []model.AgentBackend{stored}, true},
		{"host cleared from an ambient connection", []model.AgentBackend{ambient}, []model.AgentBackend{placed}, true},
		{"ambient connection saved before the rule, unchanged", []model.AgentBackend{ambient}, []model.AgentBackend{ambient}, false},
		{"ambient connection saved before the rule, renamed", []model.AgentBackend{{ID: "agy", Name: "Renamed",
			Framework: model.AgentFrameworkAntigravity, AuthMode: model.AntigravityAuthAmbient}}, []model.AgentBackend{ambient}, false},
		{"stored credentials with no host", []model.AgentBackend{stored}, nil, false},
		{"reusing an ambient connection, with no host", []model.AgentBackend{placed,
			{ID: "reuse", Framework: model.AgentFrameworkAntigravity, CredentialBackendID: "agy"}}, nil, true},
	} {
		err := model.CheckAmbientPlacement(tc.next, tc.before, func(string) bool { return true })
		if (err != nil) != tc.refused {
			t.Errorf("%s: err = %v, want refused=%v", tc.name, err, tc.refused)
		}
		if err != nil && !strings.Contains(err.Error(), model.AmbientCredentialsNeedHost) {
			t.Errorf("%s: err = %v, want it to say %q", tc.name, err, model.AmbientCredentialsNeedHost)
		}
	}
}

// Ambient on a CLI the daemon starts as its own child means the daemon's
// own login, and a caller that cannot say where a CLI runs refuses
// nothing.
func TestCheckAmbientPlacementOnlyWhereTheCLIGoesThroughHostd(t *testing.T) {
	t.Parallel()
	next := []model.AgentBackend{
		{ID: "agy", Framework: model.AgentFrameworkAntigravity, AuthMode: model.AntigravityAuthAmbient},
		{ID: "claude", Framework: model.AgentFrameworkClaude, AuthMode: model.AntigravityAuthAmbient},
	}
	onlyAgy := func(fw string) bool { return fw == model.AgentFrameworkAntigravity }
	if err := model.CheckAmbientPlacement(next[1:], nil, onlyAgy); err != nil {
		t.Errorf("claude in the container: %v, want accepted", err)
	}
	if err := model.CheckAmbientPlacement(next, nil, onlyAgy); err == nil || !strings.Contains(err.Error(), "(agy)") {
		t.Errorf("agy through hostd: %v, want agy refused", err)
	}
	if err := model.CheckAmbientPlacement(next, nil, nil); err != nil {
		t.Errorf("no way to tell: %v, want nothing refused", err)
	}
}

func TestSoleHostd(t *testing.T) {
	t.Parallel()
	now := time.Now()
	local := model.Hostd{Name: model.LocalHostd}
	wsA := model.Hostd{Name: "ws-a"}
	wsB := model.Hostd{Name: "ws-b"}
	revoked := model.Hostd{Name: "ws-old", RevokedAt: &now}
	for _, tc := range []struct {
		name string
		rows []model.Hostd
		p    model.ImplicitHostPlacement
		want string
	}{
		{"co-located hostd alone", []model.Hostd{local}, model.ImplicitHostPlacement{Default: model.LocalHostd, LocalUsable: true}, model.LocalHostd},
		{"no co-located hostd, one remote host", []model.Hostd{local, wsA}, model.ImplicitHostPlacement{}, "ws-a"},
		{"a revoked host is not a host", []model.Hostd{local, wsA, revoked}, model.ImplicitHostPlacement{}, "ws-a"},
		{"two hosts", []model.Hostd{local, wsA, wsB}, model.ImplicitHostPlacement{}, ""},
		{"co-located and remote", []model.Hostd{local, wsA}, model.ImplicitHostPlacement{Default: model.LocalHostd, LocalUsable: true}, ""},
		{"a default outside the registry is a second host", []model.Hostd{local, wsA}, model.ImplicitHostPlacement{Default: "shared"}, ""},
		{"bare-machine daemon: no hostd at all", []model.Hostd{local}, model.ImplicitHostPlacement{Default: model.LocalHostd}, ""},
	} {
		if got := model.SoleHostd(tc.rows, tc.p); got != tc.want {
			t.Errorf("%s: SoleHostd = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPlaceImplicitHostWork(t *testing.T) {
	t.Parallel()
	backends := []model.AgentBackend{
		{ID: "claude-host", Framework: model.AgentFrameworkClaude},                                              // Settings says Host
		{ID: "codex-flag", Framework: model.AgentFrameworkCodex},                                                // GRAIN_CODEX_RUN=host
		{ID: "agy-container", Framework: model.AgentFrameworkAntigravity},                                       // container: left alone
		{ID: "agy-ambient", Framework: model.AgentFrameworkAntigravity, AuthMode: model.AntigravityAuthAmbient}, // container too
		{ID: "claude-placed", Framework: model.AgentFrameworkClaude, Hostd: "pool=gpu"},                         // already placed
		{ID: "retired", Framework: model.RemovedAgentFrameworkCustom, Disabled: true,
			DisabledReason: model.RemovedCustomConnectionReason},
	}
	cliRun := map[string]model.CLIRunSetting{
		model.AgentFrameworkClaude:      {Location: model.CLIRunHost},
		model.AgentFrameworkAntigravity: {Location: model.CLIRunContainer},
	}
	servers := map[string]protocol.ServerConfig{
		"https://example.com/caps/scripts": {Command: "scripts", Run: protocol.RunHost},
		"https://example.com/caps/placed":  {Command: "placed", Run: protocol.RunHostd, Hostd: "ws-b"},
		"https://example.com/caps/inproc":  {Command: "inproc"},
	}
	p := model.ImplicitHostPlacement{DaemonHost: map[string]bool{model.AgentFrameworkCodex: true, model.AgentFrameworkAntigravity: true}}
	changes := model.PlaceImplicitHostWork(backends, cliRun, servers, "ws-a", p)
	if len(changes) != 3 {
		t.Fatalf("changes = %q, want three", changes)
	}
	wantHostd := map[string]string{
		"claude-host": "ws-a", "codex-flag": "ws-a", "agy-container": "", "agy-ambient": "",
		"claude-placed": "pool=gpu", "retired": "",
	}
	for _, b := range backends {
		if b.Hostd != wantHostd[b.ID] {
			t.Errorf("%s: hostd = %q, want %q", b.ID, b.Hostd, wantHostd[b.ID])
		}
	}
	if s := servers["https://example.com/caps/scripts"]; s.Run != protocol.RunHostd || s.Hostd != "ws-a" {
		t.Errorf("run: host server = %+v, want run: hostd, hostd: ws-a", s)
	}
	if s := servers["https://example.com/caps/placed"]; s.Hostd != "ws-b" {
		t.Errorf("placed server moved: %+v", s)
	}
	if s := servers["https://example.com/caps/inproc"]; s.Run != "" || s.Hostd != "" {
		t.Errorf("in-process server placed: %+v", s)
	}
	// Idempotent: nothing is left to place.
	if again := model.PlaceImplicitHostWork(backends, cliRun, servers, "ws-a", p); len(again) != 0 {
		t.Errorf("second pass changed %q", again)
	}
	if none := model.PlaceImplicitHostWork(backends, cliRun, servers, "", p); none != nil {
		t.Errorf("no sole host changed %q", none)
	}
}

func TestStoreMigrateImplicitHostPlacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)
	if err := st.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateConfig(ctx, func(c *model.Config) error {
		c.AgentBackends = []model.AgentBackend{
			{ID: "agy", Framework: model.AgentFrameworkAntigravity, AuthMode: model.AntigravityAuthAmbient},
		}
		c.CLIRun = map[string]model.CLIRunSetting{model.AgentFrameworkAntigravity: {Location: model.CLIRunHost}}
		c.MCPServers = map[string]protocol.ServerConfig{
			"https://example.com/caps/scripts": {Command: "scripts", Run: protocol.RunHost},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := model.ImplicitHostPlacement{Default: model.LocalHostd, LocalUsable: true}

	// Two hosts: nothing is written down.
	if _, _, err := st.CreateHostd(ctx, model.Hostd{Name: "ws-a"}); err != nil {
		t.Fatal(err)
	}
	if changes, err := st.MigrateImplicitHostPlacement(ctx, p); err != nil || len(changes) != 0 {
		t.Fatalf("with two hosts: changes %q, err %v; want none", changes, err)
	}
	// One host left once ws-a is revoked: placed on local.
	if _, err := st.RevokeHostd(ctx, "ws-a"); err != nil {
		t.Fatal(err)
	}
	changes, err := st.MigrateImplicitHostPlacement(ctx, p)
	if err != nil || len(changes) != 2 {
		t.Fatalf("with one host: changes %q, err %v; want two", changes, err)
	}
	cfg, err := st.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.AgentBackends[0].Hostd; got != model.LocalHostd {
		t.Errorf("connection hostd = %q, want %q", got, model.LocalHostd)
	}
	if s := cfg.MCPServers["https://example.com/caps/scripts"]; s.Run != protocol.RunHostd || string(s.Hostd) != model.LocalHostd {
		t.Errorf("server = %+v, want run: hostd on %s", s, model.LocalHostd)
	}
	if again, err := st.MigrateImplicitHostPlacement(ctx, p); err != nil || len(again) != 0 {
		t.Errorf("second run: changes %q, err %v; want none", again, err)
	}
}
