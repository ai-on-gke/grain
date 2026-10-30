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

package ui_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const configCap = "https://capabilities.example.com/cloud"

var cloudConfigSchema = map[string]any{
	"type": "object", "additionalProperties": false, "required": []any{"project", "token"},
	"properties": map[string]any{
		"project": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]+$"},
		"region":  map[string]any{"type": "string", "enum": []any{"eu", "us"}},
		"token":   map[string]any{"type": "string", "writeOnly": true},
	},
}

// fakeProber is a registry whose one server advertises cloudConfigSchema,
// recording what it is asked to probe, with hostd offering hostEntries
// (or, when noHostd, absent).
type fakeProber struct {
	probed      []protocol.ServerConfig
	resolved    []string
	hostEntries []string
	noHostd     bool
}

func (f *fakeProber) Statuses() []servers.Status {
	return []servers.Status{{ID: configCap, State: servers.Attached, Transport: "stdio", ConfigSchema: cloudConfigSchema}}
}

func (f *fakeProber) Probe(_ context.Context, id string, cfg protocol.ServerConfig, extra protocol.SecretLookup) servers.ProbeResult {
	f.probed = append(f.probed, cfg)
	if ref, ok := cfg.Config["token"].(string); ok {
		if v, ok := extra(strings.TrimPrefix(ref, "$secret:")); ok {
			f.resolved = append(f.resolved, v)
		}
	}
	return servers.ProbeResult{ID: id, OK: true, ConfigSchema: cloudConfigSchema, Methods: []servers.MethodDetail{{Name: "key", Releasable: true}}}
}

func (f *fakeProber) HostCapabilityEntries(context.Context) ([]string, error) {
	if f.noHostd {
		return nil, servers.ErrNoHostd
	}
	return f.hostEntries, nil
}

func configTestServer(t *testing.T, p *fakeProber) (*httptest.Server, *ui.HTTPClient, *ui.Client, *secrets.Store) {
	t.Helper()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	store := secrets.New(t.TempDir())
	client.Config.Secrets = store
	client.Config.CapabilityServers = p
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, serverAccessClient(srv, ts.URL), client, store
}

func TestCapabilityServerConfigIsValidatedAndItsSecretsStoredApart(t *testing.T) {
	ts, hc, client, store := configTestServer(t, &fakeProber{})
	ctx := context.Background()
	const secretValue = "tok-super-secret-value"
	base := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "cloud"}}

	// A secret typed into the form is stored, and the field saved as a
	// reference to it.
	withConfig := base
	withConfig.Config = map[string]any{"project": "p1", "region": "eu"}
	list, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: withConfig, ConfigSecrets: map[string]string{"token": secretValue}})
	if err != nil {
		t.Fatalf("SetCapabilityServer: %v", err)
	}
	ref, _ := own(list)[0].Config.Config["token"].(string)
	if !strings.HasPrefix(ref, "$secret:capability-server-") || !strings.HasSuffix(ref, "/token") {
		t.Fatalf("token saved as %q, want a $secret: reference", ref)
	}
	if v, err := store.Resolve(ctx, strings.TrimPrefix(ref, "$secret:")); err != nil || v != secretValue {
		t.Errorf("stored secret = %q, %v", v, err)
	}
	if len(own(list)[0].Secrets) != 1 || !own(list)[0].Secrets[0].Present {
		t.Errorf("secrets = %+v, want the token's present", own(list)[0].Secrets)
	}
	// Neither the stored row (what is exported to grain/config) nor the
	// API's answer carries the value.
	cfg, _ := client.Store.GetConfig(ctx)
	row, _ := json.Marshal(cfg)
	answer, _ := json.Marshal(list)
	for what, data := range map[string][]byte{"grain_config": row, "the API answer": answer} {
		if strings.Contains(string(data), secretValue) {
			t.Errorf("%s carries the secret value", what)
		}
	}

	// Edited in place: new args, the config and its reference kept, and
	// a new value for the same field goes to the same secret.
	edited := own(list)[0].Config
	edited.Args = []string{"capability", "cloud", "-v"}
	list, err = hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: edited, ConfigSecrets: map[string]string{"token": "rotated-value"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(own(list)) != 1 || len(own(list)[0].Config.Args) != 3 || own(list)[0].Config.Config["token"] != ref {
		t.Errorf("after the edit: %+v", list)
	}
	if v, _ := store.Resolve(ctx, strings.TrimPrefix(ref, "$secret:")); v != "rotated-value" {
		t.Errorf("rotated secret = %q", v)
	}

	// Refusals come back keyed by the field, for the form to show inline.
	for name, tc := range map[string]struct {
		body  map[string]any
		field string
	}{
		"pattern":            {map[string]any{"id": configCap, "config": map[string]any{"command": "grain", "config": map[string]any{"project": "Bad!", "token": ref}}}, "project"},
		"a secret in clear":  {map[string]any{"id": configCap, "config": map[string]any{"command": "grain", "config": map[string]any{"project": "p1", "token": secretValue}}}, "token"},
		"not a secret field": {map[string]any{"id": configCap, "config": map[string]any{"command": "grain", "config": map[string]any{"project": "p1", "token": ref}}, "configSecrets": map[string]string{"project": "x"}}, "project"},
	} {
		t.Run(name, func(t *testing.T) {
			var out map[string]any
			code := postJSON(t, ts, "PUT", "/api/capability-servers", tc.body, &out)
			fields, _ := out["fields"].(map[string]any)
			if code != 400 || fields[tc.field] == nil {
				t.Fatalf("%d %v, want a 400 naming %s", code, out, tc.field)
			}
			if msg, _ := out["error"].(string); strings.Contains(msg, secretValue) {
				t.Errorf("the refusal quotes the secret: %s", msg)
			}
		})
	}

	// A server grain knows no schema for cannot be given a config.
	_, err = hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: "https://capabilities.example.com/unknown", Config: protocol.ServerConfig{Command: "grain", Config: map[string]any{"a": "b"}}})
	if err == nil || !strings.Contains(err.Error(), "test it first") {
		t.Errorf("config for an unknown schema: %v", err)
	}
}

func TestCapabilityServerTestProbesWithoutSaving(t *testing.T) {
	p := &fakeProber{}
	_, hc, client, _ := configTestServer(t, p)
	ctx := context.Background()
	res, err := hc.TestCapabilityServer(ctx, ui.TestCapabilityServerRequest{
		ID:            configCap,
		Config:        &protocol.ServerConfig{Command: "grain", Config: map[string]any{"project": "p1"}},
		ConfigSecrets: map[string]string{"token": "typed-not-saved"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || len(res.Methods) != 1 || !res.Methods[0].Releasable || res.ConfigSchema == nil {
		t.Errorf("result = %+v", res)
	}
	if len(p.resolved) != 1 || p.resolved[0] != "typed-not-saved" {
		t.Errorf("the typed secret reached the probe as %v", p.resolved)
	}
	if cfg, _ := client.Store.GetConfig(ctx); cfg != nil && len(cfg.MCPServers) != 0 {
		t.Errorf("testing saved the entry: %v", cfg.MCPServers)
	}
	if _, err := hc.TestCapabilityServer(ctx, ui.TestCapabilityServerRequest{ID: configCap}); err == nil || !strings.Contains(err.Error(), "no capability server") {
		t.Errorf("testing an unsaved entry by id: %v", err)
	}
}

func TestRunHostIsCheckedAgainstTheHostdAllowlist(t *testing.T) {
	ctx := context.Background()
	host := protocol.ServerConfig{Command: "cloud-cap", Run: protocol.RunHost}

	_, hc, _, _ := configTestServer(t, &fakeProber{noHostd: true})
	entries, err := hc.CapabilityServerHostEntries(ctx)
	if err != nil || entries.Available || !strings.Contains(entries.Reason, "hostd") {
		t.Errorf("no hostd: entries = %+v, %v", entries, err)
	}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: host}); err == nil || !strings.Contains(err.Error(), "Kubernetes") {
		t.Errorf("run: host with no hostd: %v", err)
	}

	_, hc, _, _ = configTestServer(t, &fakeProber{hostEntries: []string{"other-cap"}})
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: host}); err == nil || !strings.Contains(err.Error(), `no capabilities entry "cloud-cap"`) || !strings.Contains(err.Error(), "other-cap") {
		t.Errorf("run: host without an allowlist entry: %v", err)
	}
	if _, err := hc.TestCapabilityServer(ctx, ui.TestCapabilityServerRequest{ID: configCap, Config: &host}); err == nil {
		t.Errorf("a run: host entry hostd does not offer was probed")
	}

	_, hc, _, _ = configTestServer(t, &fakeProber{hostEntries: []string{"cloud-cap", "other-cap"}})
	if entries, _ := hc.CapabilityServerHostEntries(ctx); !entries.Available || len(entries.Entries) != 2 {
		t.Errorf("entries = %+v", entries)
	}
	list, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: host})
	if err != nil || own(list)[0].Config.Run != protocol.RunHost {
		t.Errorf("run: host with an allowlist entry: %v, %+v", err, list)
	}
	// Back to the container is always allowed.
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: protocol.ServerConfig{Command: "grain"}}); err != nil {
		t.Errorf("run in the container: %v", err)
	}
}

// Only config-map's secret_values is merged into what is stored: another
// server's secret that happens to be a JSON object is stored as typed, so
// a rotated key never keeps a field of the one it replaced.
func TestOnlyConfigMapSecretValuesAreMergedIntoTheStoredSecret(t *testing.T) {
	_, hc, _, store := configTestServer(t, &fakeProber{})
	ctx := context.Background()

	cloud := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "cloud"}, Config: map[string]any{"project": "p1"}}
	list, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: cloud, ConfigSecrets: map[string]string{"token": `{"key_id":"old","private_key":"old-key"}`}})
	if err != nil {
		t.Fatal(err)
	}
	var cloudRow ui.CapabilityServer
	for _, row := range list {
		if row.ID == configCap {
			cloudRow = row
		}
	}
	ref, _ := cloudRow.Config.Config["token"].(string)
	const rotated = `{"key_id":"new"}`
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: cloudRow.Config, ConfigSecrets: map[string]string{"token": rotated}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := store.Resolve(ctx, strings.TrimPrefix(ref, "$secret:")); v != rotated {
		t.Errorf("rotated JSON token stored as %q, want %q exactly", v, rotated)
	}

	const cm = "https://grain.dev/capabilities/config-map"
	secretMap := func(name string, keys ...string) map[string]any {
		km := map[string]any{}
		for _, k := range keys {
			km[k] = map[string]any{}
		}
		return map[string]any{"name": name, "keys": km}
	}
	entry := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "config-map"}, Builtin: "config-map", Config: map[string]any{
		"secrets": []any{secretMap("creds", "a", "b"), secretMap("other", "x")},
	}}
	list, err = hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: cm, Config: entry, ConfigSecrets: map[string]string{"secret_values": `{"creds/a":"1","creds/b":"2","other/x":"9"}`}})
	if err != nil {
		t.Fatal(err)
	}
	row := func(list []ui.CapabilityServer) ui.CapabilityServer {
		for _, r := range list {
			if r.ID == cm {
				return r
			}
		}
		t.Fatalf("no %s row in %+v", cm, list)
		return ui.CapabilityServer{}
	}
	cmRow := row(list)
	cmRef, _ := cmRow.Config.Config["secret_values"].(string)
	if !strings.HasPrefix(cmRef, "$secret:") {
		t.Fatalf("secret_values saved as %q, want a $secret: reference", cmRef)
	}
	if got := strings.Join(cmRow.ConfiguredSecretKeys, ","); got != "creds/a,creds/b,other/x" {
		t.Errorf("configured secret keys = %q, want creds/a,creds/b,other/x", got)
	}
	stored := func() map[string]string {
		t.Helper()
		v, _ := store.Resolve(ctx, strings.TrimPrefix(cmRef, "$secret:"))
		var got map[string]string
		if err := json.Unmarshal([]byte(v), &got); err != nil {
			t.Fatalf("stored secret_values %q: %v", v, err)
		}
		return got
	}
	// Rotating one key keeps the others; removing a key drops its value;
	// adding a key sets only it.
	edited := cmRow.Config
	edited.Config = map[string]any{"secret_values": cmRef, "secrets": []any{secretMap("creds", "a", "c"), secretMap("other", "x")}}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: cm, Config: edited, ConfigSecrets: map[string]string{"secret_values": `{"creds/a":"1-rotated","creds/c":"3"}`}}); err != nil {
		t.Fatal(err)
	}
	if got := stored(); !reflect.DeepEqual(got, map[string]string{"creds/a": "1-rotated", "creds/c": "3", "other/x": "9"}) {
		t.Errorf("after rotate/add/remove, secret_values = %v", got)
	}
	// A save that types no value still drops the values of removed keys.
	edited.Config = map[string]any{"secret_values": cmRef, "secrets": []any{secretMap("creds", "a", "c")}}
	list, err = hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: cm, Config: edited})
	if err != nil {
		t.Fatal(err)
	}
	if got := stored(); !reflect.DeepEqual(got, map[string]string{"creds/a": "1-rotated", "creds/c": "3"}) {
		t.Errorf("after removing map other, secret_values = %v", got)
	}
	if got := strings.Join(row(list).ConfiguredSecretKeys, ","); got != "creds/a,creds/c" {
		t.Errorf("configured secret keys = %q, want creds/a,creds/c", got)
	}
}

// A built-in's own rules beyond its schema -- a config map's name and
// mode, a host script's unique name -- refuse the save, rather than
// storing what the server then refuses on its next start, which would
// take an attached server out of use.
func TestABuiltinsOwnConfigRulesRefuseTheSave(t *testing.T) {
	_, hc, _, _ := configTestServer(t, &fakeProber{})
	ctx := context.Background()

	const cm = "https://grain.dev/capabilities/config-map"
	configMap := func(name, mode string) protocol.ServerConfig {
		return protocol.ServerConfig{Command: "grain", Args: []string{"capability", "config-map"}, Builtin: "config-map", Config: map[string]any{
			"config_maps": []any{map[string]any{"name": name, "data": map[string]any{"walk.toml": map[string]any{"content": "a = 1\n", "mode": mode}}}},
		}}
	}
	for _, tc := range []struct{ name, mode, want string }{
		{"Walk Settings", "", `name "Walk Settings" does not match`},
		{"walk-settings", "999", `mode "999" must be a four-digit octal mode`},
	} {
		_, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: cm, Config: configMap(tc.name, tc.mode)})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("saving config map %q mode %q: err = %v, want one containing %q", tc.name, tc.mode, err, tc.want)
		}
	}
	list, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: cm, Config: configMap("walk-settings", "0640")})
	if err != nil {
		t.Fatalf("saving a valid config map: %v", err)
	}
	for _, row := range list {
		if row.ID == cm && len(row.Config.Config) == 0 {
			t.Errorf("valid config map not saved: %+v", row.Config)
		}
	}

	const hs = "https://grain.dev/capabilities/host-script"
	script := map[string]any{"name": "walk-echo", "description": "d", "script": "echo hi", "timeout": 10}
	twice := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "host-script"}, Builtin: "host-script", Config: map[string]any{
		"scripts": []any{script, script},
	}}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: hs, Config: twice}); err == nil || !strings.Contains(err.Error(), "configured twice") {
		t.Errorf("saving two host scripts named walk-echo: err = %v, want configured twice", err)
	}
}
