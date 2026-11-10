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

package protocol

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestLiteralCredentialsNamesTypedInCredentialsOnly(t *testing.T) {
	stdio := ServerConfig{
		Command: "srv",
		Args:    []string{"--region=us-central1", "--key=sk-fakefakefakefakefake1234"},
		Env: map[string]string{
			"REGION":       "us-central1",
			"API_TOKEN":    "$secret:cap-token",
			"DB_PASSWORD":  "hunter2",
			"LOG_LEVEL":    "debug",
			"UPSTREAM_URL": "https://ghp_abcdefghijklmnopqrstuv@example.com",
			// A path names where a credential is, not the credential.
			"GOOGLE_APPLICATION_CREDENTIALS": "/var/secrets/adc.json",
			// ...unless the path itself carries a credential's shape.
			"GH_TOKEN_FILE": "/run/ghp_abcdefghijklmnopqrstuv",
		},
	}
	if got, want := stdio.LiteralCredentials(), []string{"args[1]", "env DB_PASSWORD", "env GH_TOKEN_FILE", "env UPSTREAM_URL"}; !slices.Equal(got, want) {
		t.Errorf("stdio LiteralCredentials = %v, want %v", got, want)
	}
	http := ServerConfig{
		ServerURL: "https://cap.example.com/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer $secret:cap-token",
			"X-Api-Key":     "abc123",
			"X-Tenant":      "acme",
		},
	}
	if got, want := http.LiteralCredentials(), []string{"header X-Api-Key"}; !slices.Equal(got, want) {
		t.Errorf("http LiteralCredentials = %v, want %v", got, want)
	}
	http.Headers["Authorization"] = "Bearer abcdefghijklmnop"
	if got := http.LiteralCredentials(); !slices.Contains(got, "header Authorization") {
		t.Errorf("a literal bearer token was not named: %v", got)
	}
}

func TestWithheldLiteralsKeepsReferencesAndCutsValues(t *testing.T) {
	c := ServerConfig{
		ServerURL: "https://cap.example.com/mcp?key=AIzaFAKEFAKEFAKEFAKEFAKE123",
		Headers: map[string]string{
			"Authorization": "Bearer $secret:cap-token",
			"X-Api-Key":     "planted-literal-value",
		},
		Config: map[string]any{"token": "$secret:cfg", "nested": map[string]any{"k": "ghp_abcdefghijklmnopqrstuv"}},
	}
	got := c.WithheldLiterals()
	if got.Headers["Authorization"] != "Bearer $secret:cap-token" {
		t.Errorf("a reference was not kept: %q", got.Headers["Authorization"])
	}
	if got.Headers["X-Api-Key"] != LiteralWithheld {
		t.Errorf("a literal was not withheld: %q", got.Headers["X-Api-Key"])
	}
	if c.Headers["X-Api-Key"] != "planted-literal-value" {
		t.Errorf("WithheldLiterals changed the original")
	}
	raw, _ := json.Marshal(got)
	for _, leaked := range []string{"planted-literal-value", "AIzaFAKE", "ghp_abcdef"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("WithheldLiterals left %q in %s", leaked, raw)
		}
	}
	if !strings.Contains(string(raw), "$secret:cfg") {
		t.Errorf("a config reference was not kept: %s", raw)
	}
}

func TestNewLiteralCredentialsCarriesForwardWhatWasStored(t *testing.T) {
	prev := ServerConfig{ServerURL: "https://cap.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer old-literal-token"}}
	same := ServerConfig{ServerURL: "https://cap.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer old-literal-token", "X-Tenant": "acme"}}
	if got := same.NewLiteralCredentials(prev); len(got) != 0 {
		t.Errorf("an unchanged stored literal was reported as new: %v", got)
	}
	changed := ServerConfig{ServerURL: "https://cap.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer new-literal-token"}}
	if got := changed.NewLiteralCredentials(prev); !slices.Equal(got, []string{"header Authorization"}) {
		t.Errorf("a changed literal = %v, want it reported", got)
	}
	if got := changed.NewLiteralCredentials(ServerConfig{}); !slices.Equal(got, []string{"header Authorization"}) {
		t.Errorf("a literal on a new entry = %v, want it reported", got)
	}
}
