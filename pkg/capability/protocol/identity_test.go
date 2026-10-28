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
	"context"
	"testing"
)

func identityServer(t *testing.T, applied *[]map[string]any) *CapabilityServer {
	t.Helper()
	srv, err := NewCapabilityServer("ident")
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"project":          map[string]any{"type": "string"},
			DeploymentIDConfig: map[string]any{"type": "string"},
			InstanceIDConfig:   map[string]any{"type": "string"},
		},
	}
	if err := srv.HandleConfigure(schema, func(_ context.Context, cfg map[string]any) error {
		*applied = append(*applied, cfg)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return srv
}

// A server declaring the identity fields is sent grain's own values,
// whatever the stored config says; Settings never draws them.
func TestConfigureIsSentGrainsIdentity(t *testing.T) {
	var applied []map[string]any
	lb := &loopback{srv: identityServer(t, &applied)}
	id := Identity{DeploymentID: "049f1e09", InstanceID: "049f1e09c0ffee0123456789abcdef01"}
	s, err := AttachCallerAs(context.Background(), "capabilities.example.com/ident",
		ServerConfig{ServerURL: "https://capabilities.example.com/mcp", Config: map[string]any{"project": "p", DeploymentIDConfig: "typed-by-hand"}}, nil, lb, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0][DeploymentIDConfig] != "049f1e09" || applied[0][InstanceIDConfig] != id.InstanceID || applied[0]["project"] != "p" {
		t.Fatalf("configured with %v", applied)
	}
	op := OperatorSchema(s.ConfigSchema)
	props := op["properties"].(map[string]any)
	if _, ok := props[DeploymentIDConfig]; ok {
		t.Errorf("Settings' schema carries %s", DeploymentIDConfig)
	}
	if _, ok := props["project"]; !ok {
		t.Error("Settings' schema lost the operator's own field")
	}
	if _, ok := s.ConfigSchema["properties"].(map[string]any)[DeploymentIDConfig]; !ok {
		t.Error("OperatorSchema changed the server's own schema")
	}
}

// A server that does not declare them is not sent them: its
// additionalProperties: false would refuse them.
func TestWithIdentityOnlyFillsDeclaredFields(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	got := WithIdentity(schema, map[string]any{"a": "x"}, Identity{DeploymentID: "d", InstanceID: "i"})
	if len(got) != 1 || got["a"] != "x" {
		t.Errorf("WithIdentity = %v", got)
	}
	declared := map[string]any{"type": "object", "properties": map[string]any{DeploymentIDConfig: map[string]any{}, InstanceIDConfig: map[string]any{}}}
	got = WithIdentity(declared, map[string]any{DeploymentIDConfig: "stale"}, Identity{})
	if len(got) != 0 {
		t.Errorf("an unknown identity left %v", got)
	}
	if OperatorSchema(schema)["properties"].(map[string]any)["a"] == nil {
		t.Error("OperatorSchema of a schema without identity fields changed it")
	}
}
