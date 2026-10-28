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

package intree

import (
	"context"
	"log"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// Every in-tree server but an internal one is a built-in with its
// canonical id, registered on a fresh configuration.
func TestEveryInTreeServerIsRegisteredUnderItsCanonicalID(t *testing.T) {
	ensured, changed := EnsureBuiltins(nil)
	if !changed {
		t.Error("registering the built-ins on an empty configuration reported no change")
	}
	registered := 0
	for _, name := range Names() {
		b, _ := Lookup(name)
		if b.Internal {
			if _, ok := ensured[CanonicalID(name)]; ok {
				t.Errorf("internal server %s was registered", name)
			}
			continue
		}
		registered++
		if b.ID != CanonicalID(name) || !strings.HasPrefix(b.ID, CanonicalIDPrefix) {
			t.Errorf("in-tree server %s has id %q, want %q", name, b.ID, CanonicalID(name))
		}
		cfg, ok := ensured[b.ID]
		if !ok {
			t.Errorf("in-tree server %s is missing from the registered set", name)
			continue
		}
		if cfg.Builtin != name || cfg.Disabled || cfg.Command != BuiltinCommand || !reflect.DeepEqual(cfg.Args, append([]string{"capability", name}, b.Args...)) || cfg.Run != "" {
			t.Errorf("%s registered as %+v, want an enabled `grain capability %s` in the container", name, cfg, name)
		}
		if err := cfg.Validate(b.ID); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(ensured) != registered {
		t.Errorf("registered %d entries for %d built-ins", len(ensured), registered)
	}
	for _, name := range []string{"gcp", "git", "model-proxy", "config-map", "host-script"} {
		if _, ok := BuiltinNamed(name); !ok {
			t.Errorf("%s is not a built-in", name)
		}
	}
	if _, ok := BuiltinNamed("self-debug"); ok {
		t.Error("self-debug is still registered as a built-in capability server")
	}
	if b, _ := BuiltinNamed("config-map"); !b.NeedsConfig {
		t.Errorf("config-map = %+v, want needs-config", b)
	}
	if _, ok := Lookup("skill"); !ok {
		t.Error("legacy skill CLI builder is not registered")
	}
	for _, name := range []string{"gcp", "host-script"} {
		if b, _ := BuiltinNamed(name); !b.NeedsConfig || !b.Host {
			t.Errorf("%s = %+v, want needs-config and host-capable", name, b)
		}
	}
}

func TestLegacySkillEntryMigratesToConfigMapAndAliasResolves(t *testing.T) {
	const legacy = "https://grain.dev/capabilities/skill"
	const target = "https://grain.dev/capabilities/config-map"
	in := map[string]protocol.ServerConfig{
		legacy: {Command: "grain", Args: []string{"capability", "skill"}, Builtin: "skill"},
	}
	out, changed := EnsureBuiltins(in)
	if !changed {
		t.Fatal("migrating legacy skill reported no change")
	}
	if _, stillThere := out[legacy]; stillThere {
		t.Errorf("legacy %s entry remained in %+v", legacy, out)
	}
	cm, ok := out[target]
	if !ok || cm.Builtin != "config-map" || !reflect.DeepEqual(cm.Args, []string{"capability", "config-map"}) {
		t.Fatalf("migrated config-map = %+v", cm)
	}
	if resolved, ok := ResolveAlias(out, legacy); !ok || resolved != target {
		t.Errorf("ResolveAlias(%s) = %q, %v; want %q, true", legacy, resolved, ok, target)
	}
	fresh, _ := EnsureBuiltins(nil)
	if resolved, ok := ResolveAlias(fresh, legacy); !ok || resolved != target {
		t.Errorf("ResolveAlias(%s on fresh builtins) = %q, %v; want %q, true", legacy, resolved, ok, target)
	}
}

// A server added to the table is registered with nothing else changed.
func TestANewInTreeServerIsRegisteredWithNoOtherChange(t *testing.T) {
	register(Builder{Name: "fake-test", Summary: "a test server", Build: func(context.Context, []string, Env, *log.Logger) (*Server, error) { return nil, nil }})
	t.Cleanup(func() { delete(builders, "fake-test") })
	ensured, _ := EnsureBuiltins(nil)
	if cfg, ok := ensured[CanonicalIDPrefix+"fake-test"]; !ok || cfg.Builtin != "fake-test" {
		t.Errorf("the new server was not registered: %+v", ensured)
	}
}

func TestEnsureBuiltinsIsIdempotentAndKeepsTheOperatorsSettings(t *testing.T) {
	first, _ := EnsureBuiltins(map[string]protocol.ServerConfig{
		"https://example.com/caps/own": {ServerURL: "https://cap.example.com/mcp"},
	})
	gcp := first[CanonicalID("gcp")]
	gcp.Disabled = true
	gcp.Config = map[string]any{"permission_sets": []any{}}
	first[CanonicalID("gcp")] = gcp
	second, changed := EnsureBuiltins(first)
	if changed || !reflect.DeepEqual(first, second) {
		t.Errorf("a second registration changed %+v into %+v", first, second)
	}
	if !second[CanonicalID("gcp")].Disabled {
		t.Error("a disabled built-in was enabled again")
	}
	if _, ok := second["https://example.com/caps/own"]; !ok {
		t.Error("the operator's own entry was dropped")
	}
}

func TestEnsureBuiltinsDropsRetiredSelfDebugEntry(t *testing.T) {
	retiredID := CanonicalID("self-debug")
	withDefault, changed := EnsureBuiltins(map[string]protocol.ServerConfig{
		retiredID: BuiltinEntry(Builtin{Name: "self-debug"}),
	})
	if !changed {
		t.Error("dropping retired self-debug reported no change")
	}
	if _, ok := withDefault[retiredID]; ok {
		t.Errorf("unconfigured default self-debug entry was kept: %+v", withDefault[retiredID])
	}

	for _, builtin := range []string{"self-debug", ""} {
		withToken := BuiltinEntry(Builtin{Name: "self-debug"})
		withToken.Builtin = builtin
		withToken.Config = map[string]any{
			protocol.DataPlaneTokenConfig: "$secret:capability-server-ef7e460e/dataPlaneToken",
		}
		dropped, changed := EnsureBuiltins(map[string]protocol.ServerConfig{
			retiredID: withToken,
		})
		if !changed {
			t.Errorf("dropping retired self-debug (builtin=%q) with auto-minted dataPlaneToken reported no change", builtin)
		}
		if _, ok := dropped[retiredID]; ok {
			t.Errorf("retired self-debug (builtin=%q) with only dataPlaneToken was kept: %+v", builtin, dropped[retiredID])
		}
	}

	custom := BuiltinEntry(Builtin{Name: "self-debug"})
	custom.Config = map[string]any{"src": "/custom/src"}
	withCustom, _ := EnsureBuiltins(map[string]protocol.ServerConfig{
		retiredID: custom,
	})
	got, ok := withCustom[retiredID]
	if !ok {
		t.Fatal("configured self-debug entry was dropped")
	}
	if got.Builtin != "" {
		t.Errorf("configured self-debug entry kept Builtin = %q, want empty", got.Builtin)
	}
}

func TestAHandAddedEntryIsAdoptedAndItsOldIDStillResolves(t *testing.T) {
	const old = "https://github.com/bwsalmon/grain/capabilities/gcp"
	const dup = "https://example.com/caps/gcp-copy"
	const other = "https://example.com/caps/gcp-dev"
	in := map[string]protocol.ServerConfig{
		old: {Command: "grain", Args: []string{"capability", "gcp", "-project", "p"}, Env: map[string]string{"GCP_MINTER_KEY": "$secret:gcp-minter"}, Config: map[string]any{"permission_sets": []any{}}},
		dup: {Command: "grain", Args: []string{"capability", "gcp", "-project", "p"}, Env: map[string]string{"GCP_MINTER_KEY": "$secret:gcp-minter"}, Config: map[string]any{"permission_sets": []any{}}},
		// Settings of its own: merging it would hand its grants, and the
		// policy rules allowing it, the adopted entry's settings.
		other: {Command: "/usr/local/bin/grain", Args: []string{"capability", "gcp"}, Disabled: true},
	}
	out, changed := EnsureBuiltins(in)
	if !changed {
		t.Fatal("adopting reported no change")
	}
	if got, ok := out[other]; !ok || got.Builtin != "" || !got.Disabled || !reflect.DeepEqual(got.Args, []string{"capability", "gcp"}) {
		t.Errorf("an entry with settings of its own = %+v, %v; want it left as it was", got, ok)
	}
	if got, _ := ResolveAlias(out, other); got != other {
		t.Errorf("ResolveAlias(%s) = %q; want the entry itself", other, got)
	}
	for _, id := range []string{old, dup} {
		if _, ok := out[id]; ok {
			t.Errorf("%s was kept beside the built-in", id)
		}
		if got, ok := ResolveAlias(out, id); !ok || got != CanonicalID("gcp") {
			t.Errorf("ResolveAlias(%s) = %q, %v; want the canonical gcp id", id, got, ok)
		}
	}
	cfg := out[CanonicalID("gcp")]
	if cfg.Builtin != "gcp" || cfg.Disabled || cfg.Env["GCP_MINTER_KEY"] != "$secret:gcp-minter" || len(cfg.Args) != 4 {
		t.Errorf("adopted entry = %+v, want the configured hand-added entry's settings", cfg)
	}
	if !reflect.DeepEqual(cfg.Aliases, []string{dup, old}) {
		t.Errorf("aliases = %v", cfg.Aliases)
	}
	if !strings.Contains(cfg.Notice, old) || !strings.Contains(cfg.Notice, CanonicalID("gcp")) {
		t.Errorf("notice = %q", cfg.Notice)
	}
	if again, changed := EnsureBuiltins(out); changed || !reflect.DeepEqual(again, out) {
		t.Error("adoption is not idempotent")
	}
	// Once registered, another entry running the same server is a
	// second instance of the operator's, not adopted.
	const second = "https://example.com/caps/gcp-trial"
	out[second] = protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}}
	if again, _ := EnsureBuiltins(out); again[second].Builtin != "" || again[other].Builtin != "" || len(again[CanonicalID("gcp")].Aliases) != 2 {
		t.Errorf("a second instance added after registration was adopted: %+v", again[CanonicalID("gcp")])
	}
	delete(out, second)
	// Dismissing the notice sticks.
	cfg.Notice = ""
	out[CanonicalID("gcp")] = cfg
	if again, _ := EnsureBuiltins(out); again[CanonicalID("gcp")].Notice != "" {
		t.Error("a dismissed notice came back")
	}
}

func TestGCPOverlapWarnsOnAccountsTheInProcessProviderMintsFrom(t *testing.T) {
	pool := []string{"grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com", "grain-sb-agent-1@grain-sandbox-dev.iam.gserviceaccount.com"}
	shared := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}, Config: map[string]any{"permission_sets": []any{
		map[string]any{"name": "sandbox-agent", "accounts": []any{"grain-sb-agent-1@grain-sandbox-dev.iam.gserviceaccount.com"}},
		map[string]any{"name": "own", "accounts": []any{"trial-0@grain-sandbox-dev.iam.gserviceaccount.com"}},
		map[string]any{"name": "created"},
	}}}
	w := GCPOverlapWarnings(shared, pool)
	if len(w) != 1 || !strings.Contains(w[0], `"sandbox-agent"`) || !strings.Contains(w[0], "grain-sb-agent-1@") {
		t.Errorf("warnings = %q, want one for sandbox-agent", w)
	}
	// The old flags migrate to a set over the same pool.
	flags := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp", "-project", "grain-sandbox-dev", "-account", "grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com*10"}}
	if w := GCPOverlapWarnings(flags, pool); len(w) != 1 {
		t.Errorf("flag-configured warnings = %q, want one", w)
	}
	separate := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}, Config: map[string]any{"permission_sets": []any{
		map[string]any{"name": "trial", "accounts": []any{"trial-0@grain-sandbox-dev.iam.gserviceaccount.com"}},
	}}}
	if w := GCPOverlapWarnings(separate, pool); len(w) != 0 {
		t.Errorf("separate accounts warned: %q", w)
	}
	if w := GCPOverlapWarnings(shared, nil); len(w) != 0 {
		t.Errorf("no in-process pool warned: %q", w)
	}
}

// scripts/setup.sh writes a hostd allowlist entry for every built-in
// worth running on the host, and for no other.
func TestSetupWritesAHostdEntryForEveryHostBuiltin(t *testing.T) {
	data, err := os.ReadFile("../../../scripts/setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, b := range Builtins() {
		if b.Host {
			want = append(want, b.Name)
		}
	}
	// The global ensure_hostd_builtin_capabilities reads, and the copy
	// hostd_allowlist_content keeps to stand alone.
	for _, list := range []string{`^HOSTD_BUILTIN_CAPABILITIES`, `^  local builtin_caps`} {
		m := regexp.MustCompile(`(?m)` + list + `=\(([^)]*)\)`).FindSubmatch(data)
		if m == nil {
			t.Fatalf("scripts/setup.sh has no %s list", list)
		}
		if listed := strings.Fields(string(m[1])); !reflect.DeepEqual(listed, want) {
			t.Errorf("setup.sh's %s = %v, want the Host built-ins %v", list, listed, want)
		}
	}
	for _, name := range want {
		host := protocol.ServerConfig{Command: HostEntry(name), Run: protocol.RunHost}
		if InTreeName(host) != name {
			t.Errorf("a run: host entry naming %s is not recognised as the %s built-in", HostEntry(name), name)
		}
	}
	if InTreeName(protocol.ServerConfig{Command: "host-script", Run: protocol.RunHost}) != "" {
		t.Error("an operator's own host-script hostd entry was taken for the built-in")
	}
}

// HostdBuiltins is host-git and grain-<name> for every Host built-in, and
// in the core build no edition's: an edition's own host entries are its own.
func TestHostdBuiltinsAreCoresOwnInTheCoreBuild(t *testing.T) {
	var names []string
	for _, b := range HostdBuiltins() {
		names = append(names, b.Name)
		if b.Name == "grain-gchat" && !b.ExtraArgs {
			t.Error("grain-gchat takes its data-plane -listen, so its entry needs extra_args")
		}
	}
	want := []string{"host-git"}
	for _, b := range Builtins() {
		if b.Host {
			want = append(want, HostEntry(b.Name))
		}
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("HostdBuiltins = %v, want %v", names, want)
	}
}
