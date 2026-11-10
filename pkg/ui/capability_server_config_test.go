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
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
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
	// hostd, when set, is asked for hostEntries as the registry asks it.
	hostd *hostd.Client
	// placedEntries, when non-nil, simulates CapabilityServerPlacedProber:
	// nil slice for a key means registered-but-disconnected.
	placedEntries map[string][]string
	// agentsOnly names the placed hosts whose hello says they run agent
	// CLIs only (CapabilityServerAgentsOnlyProber).
	agentsOnly map[string]bool
	// refuse makes the server's configure refuse every config with it;
	// unreachable makes the probe fail before configure is reached.
	refuse, unreachable string
	// schema, when set, is the server's config schema in place of
	// cloudConfigSchema.
	schema map[string]any
}

func (f *fakeProber) Statuses() []servers.Status {
	schema := cloudConfigSchema
	if f.schema != nil {
		schema = f.schema
	}
	return []servers.Status{{ID: configCap, State: servers.Attached, Transport: "stdio", ConfigSchema: schema}}
}

func (f *fakeProber) Probe(_ context.Context, id string, cfg protocol.ServerConfig, extra protocol.SecretLookup) servers.ProbeResult {
	f.probed = append(f.probed, cfg)
	for _, field := range []string{"token", "secret_values"} {
		if ref, ok := cfg.Config[field].(string); ok {
			if v, ok := extra(strings.TrimPrefix(ref, "$secret:")); ok {
				f.resolved = append(f.resolved, v)
			}
		}
	}
	if f.refuse != "" {
		return servers.ProbeResult{ID: id, ConfigRefused: true, ConfigSchema: cloudConfigSchema, Error: "capability server " + id + ": configure: " + f.refuse}
	}
	if f.unreachable != "" {
		return servers.ProbeResult{ID: id, Error: f.unreachable}
	}
	return servers.ProbeResult{ID: id, OK: true, ConfigSchema: cloudConfigSchema, Methods: []servers.MethodDetail{{Name: "key", Releasable: true}}}
}

func (f *fakeProber) HostCapabilityEntries(ctx context.Context) ([]string, error) {
	if f.noHostd {
		return nil, servers.ErrNoHostd
	}
	if f.hostd != nil {
		if _, err := f.hostd.Offers(ctx); err != nil {
			return nil, err
		}
	}
	return f.hostEntries, nil
}

func (f *fakeProber) HostCapabilityEntriesFor(_ context.Context, selector string) ([]string, bool, error) {
	if f.placedEntries == nil {
		return f.hostEntries, !f.noHostd, nil
	}
	entries, ok := f.placedEntries[selector]
	if !ok {
		return nil, false, fmt.Errorf("hostd %s is not connected", selector)
	}
	return entries, true, nil
}

func (f *fakeProber) HostAgentsOnly(_ context.Context, selector string) bool {
	return f.agentsOnly[selector]
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
	if err != nil || entries.Available || !strings.Contains(entries.Reason, "host agent") {
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

// A server placed on an agents-only host (grain-agentd) is refused with
// advice that can work there: that host runs agent CLIs only, so the
// server needs a `grain host`, not an entry in /etc/grain/hostd.yaml.
func TestPlacedCapabilityServerOnAnAgentsOnlyHostSaysItNeedsAGrainHost(t *testing.T) {
	ctx := context.Background()
	_, hc, client, _ := configTestServer(t, &fakeProber{
		noHostd: true,
		placedEntries: map[string][]string{
			"host-d":   {},
			"host-old": {},
			"host-e":   {"other-cap"},
		},
		agentsOnly: map[string]bool{"host-d": true},
	})
	registerHostds(t, client.Store, model.Hostd{Name: "host-d"}, model.Hostd{Name: "host-old"}, model.Hostd{Name: "host-e"})

	cfg := protocol.ServerConfig{Command: "walk-echo", Run: protocol.RunHostd, Hostd: "host-d"}
	_, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: cfg})
	if err == nil {
		t.Fatal("placing a run: host server on an agents-only host succeeded")
	}
	for _, want := range []string{"host-d runs agent CLIs only", "grain host", `"walk-echo"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "under capabilities: in") {
		t.Errorf("refusal %q tells the operator to add an allowlist entry an agents-only host refuses", err)
	}

	// A host that offers no capabilities entries and does not say it is
	// agents-only (an older grain-agentd) gets both pieces of advice.
	cfg.Hostd = "host-old"
	_, err = hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "it offers no capabilities entries") ||
		!strings.Contains(err.Error(), "if this host runs grain-agentd, it runs agent CLIs only") {
		t.Errorf("refusal on a host offering nothing = %v", err)
	}

	// A full host offering other entries is told to add one, as before.
	cfg.Hostd = "host-e"
	_, err = hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: cfg})
	if err == nil || !strings.Contains(err.Error(), `add an entry "walk-echo" under capabilities:`) || strings.Contains(err.Error(), "grain-agentd") {
		t.Errorf("refusal on a full host = %v", err)
	}
}

func TestPlacedCapabilityServerChecksRegistryAndTargetHostdAllowlist(t *testing.T) {
	ctx := context.Background()
	// Even with no default hostd (as on Kubernetes), a server placed on a
	// registered hostd checks that hostd's registry status and allowlist.
	_, hc, client, _ := configTestServer(t, &fakeProber{
		noHostd: true,
		placedEntries: map[string][]string{
			"ws-a":            {"placed-cap"},
			"gpu=yes,zone=us": {"placed-cap"},
		},
	})
	registerHostds(t, client.Store,
		model.Hostd{Name: "ws-a"},
		model.Hostd{Name: "ws-off"},
		model.Hostd{Name: "gpu-box", Labels: map[string]string{"gpu": "yes", "zone": "us"}},
	)

	// Unknown, malformed, empty-valued and revoked hostds are refused before saving or probing.
	for _, bad := range []protocol.HostdTarget{"no-such-hostd", "gone", "gpu=nope", "gpu="} {
		cfg := protocol.ServerConfig{Command: "placed-cap", Run: protocol.RunHostd, Hostd: bad}
		if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: cfg}); err == nil {
			t.Errorf("SetCapabilityServer(hostd=%q) succeeded; want registry refusal", bad)
		}
		if _, err := hc.TestCapabilityServer(ctx, ui.TestCapabilityServerRequest{ID: configCap, Config: &cfg}); err == nil {
			t.Errorf("TestCapabilityServer(hostd=%q) succeeded; want registry refusal", bad)
		}
	}

	// A connected placed hostd lacking the allowlist entry is refused.
	missing := protocol.ServerConfig{Command: "not-offered", Run: protocol.RunHostd, Hostd: "ws-a"}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: missing}); err == nil || !strings.Contains(err.Error(), "on host ws-a") {
		t.Errorf("missing allowlist entry on ws-a: err = %v", err)
	}

	// A connected placed hostd offering the entry succeeds and normalizes labels.
	byLabel := protocol.ServerConfig{Command: "placed-cap", Run: protocol.RunHostd, Hostd: "zone=us,gpu=yes"}
	list, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: byLabel})
	if err != nil || own(list)[0].Config.Hostd != "gpu=yes,zone=us" {
		t.Errorf("placed by labels: err=%v, list=%+v", err, own(list))
	}

	// A registered but currently disconnected hostd can be saved (it parks
	// in needs-hostd), while Test refuses immediately.
	offline := protocol.ServerConfig{Command: "placed-cap", Run: protocol.RunHostd, Hostd: "ws-off"}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: offline}); err != nil {
		t.Errorf("saving server on disconnected registered hostd ws-off: %v", err)
	}
	if _, err := hc.TestCapabilityServer(ctx, ui.TestCapabilityServerRequest{ID: configCap, Config: &offline}); err == nil || !strings.Contains(err.Error(), "ws-off") {
		t.Errorf("testing server on disconnected hostd ws-off: err = %v, want immediate refusal", err)
	}

	// If ws-off is later revoked, editing another field of the saved server
	// still works without forcing the operator to change hostd first.
	if _, err := client.Store.RevokeHostd(ctx, "ws-off"); err != nil {
		t.Fatal(err)
	}
	offline.Disabled = true
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: offline}); err != nil {
		t.Errorf("editing disabled on server whose hostd was later revoked: %v", err)
	}

	// If CapabilityServers does not implement CapabilityServerPlacedProber,
	// saving or testing a placed server refuses immediately rather than
	// falling back to probing on the default hostd with typed-in secrets.
	inner := &fakeProber{hostEntries: []string{"placed-cap"}}
	client.Config.CapabilityServers = unplacedOnlyProber{inner}
	placedWithSecret := protocol.ServerConfig{
		Command: "placed-cap", Run: protocol.RunHostd, Hostd: "ws-a",
		Config: map[string]any{"project": "p1"},
	}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{
		ID: configCap, Config: placedWithSecret, ConfigSecrets: map[string]string{"token": "must-not-probe-default"},
	}); err == nil {
		t.Error("SetCapabilityServer succeeded without CapabilityServerPlacedProber")
	}
	if _, err := hc.TestCapabilityServer(ctx, ui.TestCapabilityServerRequest{
		ID: configCap, Config: &placedWithSecret, ConfigSecrets: map[string]string{"token": "must-not-probe-default"},
	}); err == nil {
		t.Error("TestCapabilityServer succeeded without CapabilityServerPlacedProber")
	}
	if len(inner.probed) > 0 || len(inner.resolved) > 0 {
		t.Errorf("placed server touched default hostd prober without PlacedProber: probed=%+v resolved=%+v", inner.probed, inner.resolved)
	}
}

type unplacedOnlyProber struct{ p *fakeProber }

func (u unplacedOnlyProber) Statuses() []servers.Status { return u.p.Statuses() }
func (u unplacedOnlyProber) Probe(ctx context.Context, id string, cfg protocol.ServerConfig, extra protocol.SecretLookup) servers.ProbeResult {
	return u.p.Probe(ctx, id, cfg, extra)
}
func (u unplacedOnlyProber) HostCapabilityEntries(ctx context.Context) ([]string, error) {
	return u.p.HostCapabilityEntries(ctx)
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

// With no dialing-in hostd connected, a run: host server's test says so
// at once, rather than after the 30 s a run waits for one to dial in
// (hostd.ForDisplay).
func TestTestingAHostServerWithNoHostdConnectedAnswersAtOnce(t *testing.T) {
	c := hostd.Client{Dial: (&hostd.Remote{Token: "x"}).Dial}
	_, hc, _, _ := configTestServer(t, &fakeProber{hostd: &c})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := hc.TestCapabilityServer(ctx, ui.TestCapabilityServerRequest{ID: configCap, Config: &protocol.ServerConfig{Command: "cloud", Run: protocol.RunHost}})
	if took := time.Since(start); took > time.Second {
		t.Errorf("test took %s with no hostd connected, want it at once", took)
	}
	if err == nil || !strings.Contains(err.Error(), hostd.ErrRemoteNotConnected.Error()) {
		t.Fatalf("err = %v, want it to say no hostd is connected", err)
	}
}

// A config the server's own configure refuses is refused at save, with
// the server's reason on the form and on the field it names, and nothing
// is stored; a server that cannot be reached at all is still saved.
func TestCapabilityServerSaveIsRefusedWhenTheServerRefusesItsConfig(t *testing.T) {
	setsSchema := map[string]any{
		"type": "object", "required": []any{"project", "token"},
		"properties": map[string]any{
			"project": map[string]any{"type": "string"},
			"token":   map[string]any{"type": "string", "writeOnly": true},
			"permission_sets": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"name":  map[string]any{"type": "string"},
					"roles": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
			}},
		},
	}
	p := &fakeProber{schema: setsSchema, refuse: `gcp: permission set "iap-test": roles/run.admin exceeds admin ambient-host's role ceiling: add it to the admin's role_ceiling too`}
	ts, _, client, store := configTestServer(t, p)
	ctx := context.Background()
	body := map[string]any{"id": configCap, "config": map[string]any{"command": "grain", "config": map[string]any{
		"project": "p1", "token": "$secret:capability-server-x/token",
		"permission_sets": []any{map[string]any{"name": "other"}, map[string]any{"name": "iap-test", "roles": []any{"roles/run.admin"}}},
	}}, "configSecrets": map[string]string{"token": "typed-secret"}}
	var out map[string]any
	code := postJSON(t, ts, "PUT", "/api/capability-servers", body, &out)
	msg, _ := out["error"].(string)
	fields, _ := out["fields"].(map[string]any)
	if code != 400 || !strings.Contains(msg, "add it to the admin's role_ceiling too") || !strings.Contains(msg, "not saved") {
		t.Fatalf("%d %v, want a 400 with the server's reason", code, out)
	}
	if fields[""] == nil || fields["permission_sets[1].roles"] == nil {
		t.Errorf("fields = %v, want the form and permission_sets[1].roles", fields)
	}
	if strings.Contains(msg, "configure:") {
		t.Errorf("error %q keeps the probe's prefix", msg)
	}
	// The probe was handed the typed secret, and nothing was kept.
	if len(p.resolved) != 1 || p.resolved[0] != "typed-secret" {
		t.Errorf("probe resolved %v, want the typed secret", p.resolved)
	}
	cfg, _ := client.Store.GetConfig(ctx)
	if _, ok := cfg.MCPServers[configCap]; ok {
		t.Errorf("a refused config was stored: %+v", cfg.MCPServers[configCap])
	}
	if list, _ := store.List(); len(list) != 0 {
		t.Errorf("a refused save stored secrets %v", list)
	}

	// Unreachable: saved, since the config was never judged.
	p.refuse, p.unreachable = "", "dial tcp: connection refused"
	var saved []any
	if code := postJSON(t, ts, "PUT", "/api/capability-servers", body, &saved); code != 200 {
		t.Fatalf("an unreachable server's save = %d %v, want it saved", code, saved)
	}
}

// External-config mode refuses a save carrying typed secrets before Probe
// runs, and a non-builtin server claiming Builtin: "config-map" cannot
// merge or read config-map's stored secret_values.
func TestCapabilityServerSaveGuardsProbeAndBuiltinSpoofing(t *testing.T) {
	mapSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"secret_values": map[string]any{"type": "string", "writeOnly": true},
			"secrets":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		},
	}
	p := &fakeProber{schema: mapSchema}
	ts, _, client, store := configTestServer(t, p)
	if err := store.Set("existing", "secret_values", []byte(`{"creds/a":"keep-secret"}`)); err != nil {
		t.Fatal(err)
	}

	// Spoofed Builtin: "config-map" on a non-builtin id does not merge
	// existing/secret_values into what Probe receives.
	spoof := map[string]any{
		"id": configCap,
		"config": map[string]any{
			"command": "grain",
			"builtin": "config-map",
			"config": map[string]any{
				"secret_values": "$secret:existing/secret_values",
				"secrets":       []any{map[string]any{"name": "creds", "keys": map[string]any{"a": "A", "b": "B"}}},
			},
		},
		"configSecrets": map[string]string{"secret_values": `{"creds/b":"typed-only"}`},
	}
	p.refuse = "no"
	var refused map[string]any
	if code := postJSON(t, ts, "PUT", "/api/capability-servers", spoof, &refused); code != 400 {
		t.Fatalf("spoof status = %d %v, want 400", code, refused)
	}
	if len(p.resolved) != 1 || strings.Contains(p.resolved[0], "keep-secret") {
		t.Fatalf("spoofed Builtin merged stored secret into Probe: %v", p.resolved)
	}

	// External-config mode refuses before calling Probe at all.
	p.probed, p.resolved = nil, nil
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Remote: "https://github.com/acme/grain-config.git", Branch: "main"}}
	var conflict map[string]any
	if code := postJSON(t, ts, "PUT", "/api/capability-servers", spoof, &conflict); code != 409 {
		t.Fatalf("external mode status = %d %v, want 409", code, conflict)
	}
	if len(p.probed) != 0 || len(p.resolved) != 0 {
		t.Errorf("external mode still ran Probe: probed=%v resolved=%v", p.probed, p.resolved)
	}
}

func TestGCPPermissionSetRefusalMapsToField(t *testing.T) {
	p := &fakeProber{schema: gcpsetsSchemaForTest()}
	ts, _, _, _ := configTestServer(t, p)

	for _, tc := range []struct {
		name      string
		refuse    string
		wantField string
	}{
		{
			name:      "pool_max exclusive with token mode",
			refuse:    `permission set "ar-writer": credential: token and pool_max are exclusive: in token mode grain creates one service account`,
			wantField: "permission_sets[0].pool_max",
		},
		{
			name:      "invalid resource_scope",
			refuse:    `permission set "ar-writer": resource_scope "topics/x" must be buckets/<name> or repositories/<location>/<repo>`,
			wantField: "permission_sets[0].resource_scope",
		},
		{
			name:      "grants no roles",
			refuse:    `permission set "ar-writer" grants no roles: add roles for grain to bind`,
			wantField: "permission_sets[0].roles",
		},
		{
			name:      "missing project",
			refuse:    `permission set "ar-writer": "" is not a project id: project is required when service_account is omitted`,
			wantField: "permission_sets[0].project",
		},
		{
			name:      "service_account exclusive",
			refuse:    `permission set "ar-writer": service_account "bad" is not a service account email`,
			wantField: "permission_sets[0].service_account",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.refuse = tc.refuse
			body := map[string]any{
				"id": configCap,
				"config": map[string]any{
					"command": "grain",
					"config": map[string]any{
						"permission_sets": []any{
							map[string]any{"name": "ar-writer", "project": "my-project", "credential": "token", "roles": []any{"roles/artifactregistry.writer"}},
						},
					},
				},
			}
			var refused struct {
				Fields map[string]string `json:"fields"`
			}
			if code := postJSON(t, ts, "PUT", "/api/capability-servers", body, &refused); code != 400 {
				t.Fatalf("status = %d, want 400", code)
			}
			if got := refused.Fields[tc.wantField]; got == "" {
				t.Fatalf("fields = %v, want non-empty message under %q", refused.Fields, tc.wantField)
			}
		})
	}
}

func gcpsetsSchemaForTest() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"permission_sets": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"name"},
					"properties": map[string]any{
						"name":            map[string]any{"type": "string"},
						"project":         map[string]any{"type": "string"},
						"credential":      map[string]any{"type": "string", "enum": []any{"token", "key"}},
						"roles":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"pool_max":        map[string]any{"type": "integer"},
						"resource_scope":  map[string]any{"type": "string"},
						"service_account": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

// A credential typed into an entry's env, headers, args or URL would be
// stored in grain_config and exported with it, so the save is refused,
// naming where the credential is and what to write instead, and nothing
// is stored (docs/security/audits/2026-11-10-credential-exposure.md).
func TestCapabilityServerSaveRefusesATypedInCredential(t *testing.T) {
	_, hc, client, _ := configTestServer(t, &fakeProber{})
	ctx := context.Background()
	for name, cfg := range map[string]protocol.ServerConfig{
		"env":    {Command: "grain", Args: []string{"capability", "cloud"}, Env: map[string]string{"API_TOKEN": "typed-in-token-value"}},
		"args":   {Command: "grain", Args: []string{"capability", "cloud", "--key=sk-typedinkeytypedinkey0000"}},
		"header": {ServerURL: "https://cap.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer typed-in-token-value"}},
	} {
		_, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: "typed-in-" + name, Config: cfg})
		if err == nil || !strings.Contains(err.Error(), "$secret:<name>") {
			t.Errorf("%s: SetCapabilityServer = %v, want a refusal pointing at $secret:<name>", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "typed-in-token-value") {
			t.Errorf("%s: the refusal echoes the credential: %v", name, err)
		}
	}
	stored, _ := client.Store.GetConfig(ctx)
	row, _ := json.Marshal(stored)
	if strings.Contains(string(row), "typed-in-token-value") {
		t.Errorf("a refused save was stored: %s", row)
	}
	// An entry saved before the rule keeps its literal through an edit of
	// something else, and is refused a new one.
	const legacyID = "legacy-literal"
	if err := client.Store.UpdateConfig(ctx, func(c *model.Config) error {
		if c.MCPServers == nil {
			c.MCPServers = map[string]protocol.ServerConfig{}
		}
		c.MCPServers[legacyID] = protocol.ServerConfig{Command: "grain", Args: []string{"capability", "cloud"}, Env: map[string]string{"API_TOKEN": "legacy-token-value"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	carried := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "cloud"}, Env: map[string]string{"API_TOKEN": "legacy-token-value", "REGION": "eu"}}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: legacyID, Config: carried}); err != nil && strings.Contains(err.Error(), "typed in") {
		t.Errorf("a stored literal carried forward was refused: %v", err)
	}
	carried.Env["API_TOKEN"] = "replaced-token-value"
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: legacyID, Config: carried}); err == nil || !strings.Contains(err.Error(), "typed in") {
		t.Errorf("a replaced literal was not refused: %v", err)
	}

	// A reference, with its scheme word, is still accepted.
	ok := protocol.ServerConfig{Command: "grain", Args: []string{"capability", "cloud"}, Env: map[string]string{"API_TOKEN": "$secret:cap-token", "REGION": "eu"}}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: configCap, Config: ok}); err != nil &&
		strings.Contains(err.Error(), "typed in") {
		t.Errorf("a $secret: reference was refused as typed in: %v", err)
	}
}
