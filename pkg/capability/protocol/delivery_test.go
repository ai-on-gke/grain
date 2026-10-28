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
	"testing"
)

func TestDiscoverReadsMethodDelivery(t *testing.T) {
	sandbox := mintTool("ping", nil)
	sandbox.Annotations = &ToolAnnotations{MCPDelivery: DeliverySandbox}
	topLevel := mintTool("pong", nil)
	topLevel.XGrainMCPDelivery = DeliveryModelOnly
	methods, err := Discover([]Tool{sandbox, topLevel, mintTool("silent", nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got := methods["ping"].MCPDelivery; got != DeliverySandbox {
		t.Errorf("ping's delivery = %q", got)
	}
	if got := methods["pong"].MCPDelivery; got != DeliveryModelOnly {
		t.Errorf("pong's delivery = %q", got)
	}
	if got := methods["silent"].MCPDelivery; got != "" {
		t.Errorf("silent's delivery = %q", got)
	}
	bad := mintTool("bad", nil)
	bad.Annotations = &ToolAnnotations{MCPDelivery: "everywhere"}
	if _, err := Discover([]Tool{bad}); err == nil {
		t.Error("Discover accepted an mcpDelivery that is neither sandbox nor model_only")
	}
}

func TestToolDeclaredModelOnlyFromEitherSpelling(t *testing.T) {
	var tools []Tool
	if err := json.Unmarshal([]byte(`[
		{"name": "a", "annotations": {"modelOnly": false}},
		{"name": "b", "x-grain-model-only": true},
		{"name": "c", "annotations": {"modelOnly": true}, "x-grain-model-only": false},
		{"name": "d"}
	]`), &tools); err != nil {
		t.Fatal(err)
	}
	want := []*bool{ptr(false), ptr(true), ptr(true), nil}
	for i, tool := range tools {
		got := tool.DeclaredModelOnly()
		if (got == nil) != (want[i] == nil) || (got != nil && *got != *want[i]) {
			t.Errorf("%s: DeclaredModelOnly = %v, want %v", tool.Name, got, want[i])
		}
	}
}

func TestResolveModelOnlyPrecedence(t *testing.T) {
	for _, tc := range []struct {
		tool     *bool
		delivery string
		want     bool
	}{
		{nil, "", true},                    // external servers default to model-only
		{nil, DeliveryModelOnly, true},     // the method says so
		{nil, DeliverySandbox, false},      // the method opts its tools in
		{ptr(true), DeliverySandbox, true}, // a tool's own word wins
		{ptr(false), "", false},            // ... either way
		{ptr(false), DeliveryModelOnly, false},
	} {
		if got := ResolveModelOnly(tc.tool, tc.delivery); got != tc.want {
			t.Errorf("ResolveModelOnly(%v, %q) = %v, want %v", tc.tool, tc.delivery, got, tc.want)
		}
	}
	if DeliveryMode(true) != DeliveryModelOnly || DeliveryMode(false) != DeliverySandbox {
		t.Error("DeliveryMode names the wrong mode")
	}
}

func ptr(b bool) *bool { return &b }
