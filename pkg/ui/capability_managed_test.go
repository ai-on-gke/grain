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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

const tokenCap = "https://capabilities.example.com/tokened"

// rotatingRegistry is a registry whose one server declares the
// data-plane token, rotating it in the store as servers.Registry does.
type rotatingRegistry struct {
	fakeProber
	store   *secrets.Store
	rotated []string
}

func (r *rotatingRegistry) Statuses() []servers.Status {
	return []servers.Status{{ID: tokenCap, State: servers.Attached, Transport: "stdio", ConfigSchema: map[string]any{
		"type": "object", "properties": map[string]any{
			protocol.DataPlaneTokenConfig: map[string]any{"type": "string", "writeOnly": true, "title": "Data-plane token"},
		},
	}}}
}

func (r *rotatingRegistry) RotateManaged(id string) ([]string, error) {
	r.rotated = append(r.rotated, id)
	err := r.store.Rotate(protocol.ManagedSecretName(id, protocol.DataPlaneTokenConfig), protocol.GenerateManagedValue)
	return []string{protocol.DataPlaneTokenConfig}, err
}

// Settings lists a grain-managed field by the secret it is kept under,
// never its value -- in the list and in the exported grain_config alike
// -- and its Rotate action reaches the registry.
func TestManagedSecretsAreListedNotShownAndRotate(t *testing.T) {
	reg := &rotatingRegistry{}
	_, hc, client, store := configTestServer(t, &reg.fakeProber)
	reg.store = store
	client.Config.CapabilityServers = reg
	ctx := context.Background()
	if err := client.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.MCPServers = map[string]protocol.ServerConfig{tokenCap: {Command: "grain", Args: []string{"capability", "tokened"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	name := protocol.ManagedSecretName(tokenCap, protocol.DataPlaneTokenConfig)
	if err := store.Ensure(name, protocol.GenerateManagedValue); err != nil {
		t.Fatal(err)
	}
	value, _ := store.Resolve(ctx, name)

	list, err := hc.ListCapabilityServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range list {
		if s.ID != tokenCap {
			continue
		}
		found = true
		if len(s.Managed) != 1 || s.Managed[0].Field != protocol.DataPlaneTokenConfig || s.Managed[0].Secret != name || !s.Managed[0].Present || s.Managed[0].Overridden {
			t.Errorf("managed = %+v", s.Managed)
		}
	}
	if !found {
		t.Fatalf("%s not listed", tokenCap)
	}
	raw, _ := json.Marshal(list)
	stored, _ := client.Store.GetConfig(ctx)
	exported, _ := json.Marshal(stored)
	if strings.Contains(string(raw), value) || strings.Contains(string(exported), value) {
		t.Errorf("the managed token appears in the list or in grain_config")
	}

	if _, err := hc.CapabilityServerAction(ctx, "rotate-managed", tokenCap); err != nil {
		t.Fatal(err)
	}
	if next, _ := store.Resolve(ctx, name); len(reg.rotated) != 1 || next == value {
		t.Errorf("rotate: registry called %v, value changed %v", reg.rotated, next != value)
	}
}
