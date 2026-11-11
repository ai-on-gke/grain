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
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// CE-2's migration (docs/security/audits/2026-11-10-credential-exposure.md):
// a stored capability server holding typed-in credentials comes out of a
// start with each moved into the secrets store as
// capability-server-<id>/<field>, the entry referencing it, and the
// server resolving exactly what it did before -- and with no trace of
// any value in grain_config or in the state repository export. A second
// start changes nothing.
func TestMigrateLiteralCredentialsLeavesNoLiteralInConfigOrExport(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, closeStore := openTestStore(t, dataDir)
	defer closeStore()
	secretStore := OpenSecrets(t.TempDir())

	const (
		httpID   = "https://example.com/caps/typed-in"
		stdioID  = "stdio-tool"
		header   = "shapeless-header-value-1234"
		envValue = "shapeless-env-value-5678"
		mixed    = "mixed-literal-9012"
	)
	before := map[string]protocol.ServerConfig{
		httpID: {ServerURL: "https://typed.example.com/mcp", Headers: map[string]string{
			"X-Api-Key":     header,
			"Authorization": "Bearer " + mixed + " $secret:already-stored/value",
			"X-Plain":       "not-a-credential",
		}},
		stdioID: {Command: "/usr/local/bin/tool", Env: map[string]string{
			"API_TOKEN": envValue,
			"LOG_LEVEL": "debug",
		}},
		"https://example.com/caps/clean": {ServerURL: "https://clean.example.com/mcp", Headers: map[string]string{
			"Authorization": "Bearer $secret:clean-token",
		}},
	}
	if err := store.PutConfig(ctx, model.Config{PollInterval: 30 * time.Second, MaxWorkers: 1, MCPServers: before}); err != nil {
		t.Fatal(err)
	}

	if err := migrateLiteralCredentials(ctx, store, secretStore); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, sc := range cfg.MCPServers {
		if lits := sc.LiteralCredentials(); len(lits) > 0 {
			t.Errorf("capability server %s still holds typed-in credentials at %v", id, lits)
		}
	}
	keyName := protocol.LiteralSecretName(httpID, "header X-Api-Key")
	if got := cfg.MCPServers[httpID].Headers["X-Api-Key"]; got != protocol.SecretRef(keyName) {
		t.Errorf("X-Api-Key = %q, want %q", got, protocol.SecretRef(keyName))
	}
	if got := cfg.MCPServers[httpID].Headers["X-Plain"]; got != "not-a-credential" {
		t.Errorf("X-Plain = %q, want a value that is no credential left alone", got)
	}
	if got := cfg.MCPServers[stdioID].Env["LOG_LEVEL"]; got != "debug" {
		t.Errorf("LOG_LEVEL = %q, want it left alone", got)
	}
	if !strings.HasPrefix(keyName, "capability-server-") || strings.Count(keyName, "/") != 1 {
		t.Errorf("secret name %q is not capability-server-<id>/<field>", keyName)
	}

	// Each server resolves exactly what it did before the move.
	if err := secretStore.Set("already-stored", "value", []byte("stored-elsewhere")); err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) (string, bool) {
		v, err := secretStore.Resolve(ctx, name)
		return v, err == nil
	}
	for _, id := range []string{httpID, stdioID} {
		after, err := cfg.MCPServers[id].Resolve(lookup)
		if err != nil {
			t.Fatalf("resolving %s after the move: %v", id, err)
		}
		was, _ := before[id].Resolve(lookup)
		for k, v := range was.Headers {
			if after.Headers[k] != v {
				t.Errorf("%s header %s resolves to %q, want %q", id, k, after.Headers[k], v)
			}
		}
		for k, v := range was.Env {
			if after.Env[k] != v {
				t.Errorf("%s env %s resolves to %q, want %q", id, k, after.Env[k], v)
			}
		}
	}

	// Neither grain_config nor the export holds any value.
	var raw string
	if err := store.DB().QueryRowContext(ctx, `SELECT mcp_servers FROM grain_config`).Scan(&raw); err != nil {
		t.Fatalf("reading grain_config.mcp_servers: %v", err)
	}
	exportDir := t.TempDir()
	if err := staterepo.Export(ctx, store.DB(), exportDir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	var exported bytes.Buffer
	if err := filepath.WalkDir(exportDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		exported.Write(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exported.String(), keyName) {
		t.Fatalf("the export does not carry the reference %s; is it exporting grain_config at all?", keyName)
	}
	for _, v := range []string{header, envValue, mixed} {
		if strings.Contains(raw, v) {
			t.Errorf("grain_config.mcp_servers still holds %q", v)
		}
		if strings.Contains(exported.String(), v) {
			t.Errorf("the state repository export still holds %q", v)
		}
	}

	// A second start finds nothing to move and writes nothing.
	if err := migrateLiteralCredentials(ctx, store, secretStore); err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	again, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := mustEncode(t, cfg.MCPServers), mustEncode(t, again.MCPServers); a != b {
		t.Errorf("a second start changed mcp_servers:\n%s\nto\n%s", a, b)
	}
}

// A name that already holds a different value is never overwritten: the
// entry is left as it was, for the next start or the operator.
func TestMigrateLiteralCredentialsNeverOverwritesADifferentSecret(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	defer closeStore()
	secretStore := OpenSecrets(t.TempDir())
	const id = "stdio-tool"
	name := protocol.LiteralSecretName(id, "env API_TOKEN")
	secret, key, _ := strings.Cut(name, "/")
	if err := secretStore.Set(secret, key, []byte("somebody-elses-value")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConfig(ctx, model.Config{PollInterval: 30 * time.Second, MaxWorkers: 1, MCPServers: map[string]protocol.ServerConfig{
		id: {Command: "/usr/local/bin/tool", Env: map[string]string{"API_TOKEN": "typed-in-value"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := migrateLiteralCredentials(ctx, store, secretStore); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MCPServers[id].Env["API_TOKEN"]; got != "typed-in-value" {
		t.Errorf("API_TOKEN = %q, want the entry left as it was", got)
	}
	if v, _ := secretStore.Resolve(ctx, name); v != "somebody-elses-value" {
		t.Errorf("%s = %q, want it left alone", name, v)
	}
}

func mustEncode(t *testing.T, servers map[string]protocol.ServerConfig) string {
	t.Helper()
	s, err := model.EncodeMCPServers(servers)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
