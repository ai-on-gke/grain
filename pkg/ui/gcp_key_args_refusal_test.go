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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type inProcessGCPCaller struct {
	srv *intree.Server
}

func (c inProcessGCPCaller) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	var resp struct {
		Tools []protocol.Tool `json:"tools"`
	}
	if err := c.roundTrip(ctx, "tools/list", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp.Tools, nil
}

func (c inProcessGCPCaller) CallTool(context.Context, string, map[string]any) ([]byte, error) {
	return nil, fmt.Errorf("not minted in this test")
}

func (c inProcessGCPCaller) Close() error { return nil }

func (c inProcessGCPCaller) roundTrip(ctx context.Context, method string, params any, out any) error {
	var in bytes.Buffer
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	if err := json.NewEncoder(&in).Encode(req); err != nil {
		return err
	}
	var respBuf bytes.Buffer
	if err := c.srv.Serve(ctx, &in, &respBuf); err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBuf.Bytes(), &envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s", envelope.Error.Message)
	}
	return json.Unmarshal(envelope.Result, out)
}

type attachedGCPServer struct{ server *protocol.Server }

func (g attachedGCPServer) Statuses() []servers.Status {
	m := g.server.Methods["gcp-key"]
	return []servers.Status{{
		ID:      model.GCPCapabilityServerID,
		State:   servers.Attached,
		Methods: []string{"gcp-key"},
		MethodDetails: []servers.MethodDetail{{
			Name:        "gcp-key",
			Description: m.Description,
			Args:        m.ArgsSchema,
			Releasable:  m.Releasable,
		}},
	}}
}

func (g attachedGCPServer) Lookup(id string) (*protocol.Server, error) {
	if id != model.GCPCapabilityServerID {
		return nil, fmt.Errorf("capability %s is not configured in mcpServers", id)
	}
	return g.server, nil
}

func TestGCPKeyRemovedArgsRefusedAcrossAPIAndCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	setsCfg, err := gcpsets.Parse(map[string]any{
		protocol.DeploymentIDConfig: "testdeploy",
		protocol.InstanceIDConfig:   "1234567890abcdef",
		"admins": []any{map[string]any{
			"name": "default", "credential_source": "ambient",
			"projects": []any{"acme-ai"}, "role_ceiling": []any{"roles/viewer", "roles/iap.admin"},
		}},
		"permission_sets": []any{
			map[string]any{"name": "sandbox-agent", "admin": "default", "project": "acme-ai", "roles": []any{"roles/viewer"}},
			map[string]any{"name": "iap-test", "admin": "default", "project": "acme-ai", "roles": []any{"roles/iap.admin"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mcpSrv, err := intree.NewGCP(intree.GCPConfig{Fallback: setsCfg, Connector: gcpfake.New()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mcpSrv.Close(ctx) })

	protoSrv, err := protocol.NewServer(ctx, model.GCPCapabilityServerID, inProcessGCPCaller{srv: mcpSrv})
	if err != nil {
		t.Fatal(err)
	}

	// Both with an attached gcp capability server and when unattached (offline),
	// creating a task or checking grants with account or activate is refused
	// with a message naming permission_set.
	for _, attached := range []bool{true, false} {
		name := "unattached"
		if attached {
			name = "attached"
		}
		t.Run(name, func(t *testing.T) {
			srv, client := testServer(t)
			if attached {
				client.Config.CapabilityServers = attachedGCPServer{server: protoSrv}
			}
			ts := httptest.NewServer(srv)
			t.Cleanup(ts.Close)
			ui.SetAccessCodeForTest(srv, testAccessCode)

			if attached {
				var methods []ui.CapabilityMethod
				if code := postJSON(t, ts, "GET", "/api/capability-methods", nil, &methods); code != 200 {
					t.Fatalf("GET /api/capability-methods status = %d", code)
				}
				idx := slices.IndexFunc(methods, func(m ui.CapabilityMethod) bool {
					return m.Ref == model.GCPCapabilityServerID+"#gcp-key"
				})
				if idx < 0 {
					t.Fatalf("gcp-key missing from /api/capability-methods: %+v", methods)
				}
				props, _ := methods[idx].Args["properties"].(map[string]any)
				if props["permission_set"] == nil || props["primary"] == nil {
					t.Errorf("gcp-key args properties = %v, want permission_set and primary", props)
				}
				if props["account"] != nil || props["activate"] != nil {
					t.Errorf("gcp-key args properties still exposes removed fields: %v", props)
				}
			}

			badGrants := []struct {
				grant string
				field string
			}{
				{`https://grain.dev/capabilities/gcp#gcp-key={"account":"iap-test"}`, "account"},
				{`https://grain.dev/capabilities/gcp#gcp-key={"account":"default"}`, "account"},
				{`https://grain.dev/capabilities/gcp#gcp-key={"activate":true}`, "activate"},
				{`https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test","activate":true}`, "activate"},
			}

			for _, tc := range badGrants {
				caps := []string{tc.grant}
				_, err := client.CreateTask(ctx, ui.CreateTaskRequest{
					NoRepo:       true,
					Title:        "Bad grant task",
					Capabilities: &caps,
				})
				if err == nil {
					t.Fatalf("CreateTask(%s) succeeded, want refusal", tc.grant)
				}
				if !strings.Contains(err.Error(), "permission_set") {
					t.Errorf("CreateTask(%s) error = %q, want mention of permission_set", tc.grant, err)
				}

				res, err := client.CheckCapabilityGrants(ctx, []ui.GrantRequest{{Grant: tc.grant}})
				if err != nil {
					t.Fatalf("CheckCapabilityGrants(%s): %v", tc.grant, err)
				}
				if len(res) != 1 || res[0].Recorded != "" || res[0].Error == "" {
					t.Fatalf("CheckCapabilityGrants(%s) = %+v, want invalid", tc.grant, res)
				}
				msg := res[0].Error + " " + res[0].Fields[tc.field]
				if !strings.Contains(msg, "permission_set") {
					t.Errorf("CheckCapabilityGrants(%s) = %+v, want error mentioning permission_set", tc.grant, res[0])
				}
			}

			if attached {
				// Valid permission_set grant and legacy bare gcp-key alias still succeed.
				validCaps := []string{
					`https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test"}`,
					model.GCPKeySandboxAgentGrant,
				}
				created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
					NoRepo:       true,
					Title:        "Valid permission_set grant",
					Capabilities: &validCaps,
				})
				if err != nil {
					t.Fatalf("CreateTask with two permission sets failed: %v", err)
				}
				want := []string{
					`https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test","primary":true}`,
					`https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}`,
				}
				for _, w := range want {
					if !slices.Contains(created.Capabilities, w) {
						t.Errorf("created.Capabilities = %v, missing %s", created.Capabilities, w)
					}
				}
			}
		})
	}
}
