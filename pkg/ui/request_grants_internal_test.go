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

package ui

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestCreateTaskRequestGrants is unified-grants §3's API half of slice 2:
// grants: [{capability, with}] beside capabilities, reads, writes and
// repo, sorted into those fields in today's spelling.
func TestCreateTaskRequestGrants(t *testing.T) {
	var req CreateTaskRequest
	err := json.Unmarshal([]byte(`{"title":"x","grants":[
		{"capability":"https://grain.dev/capabilities/api#ci:write","with":{"repo":"acme/widgets"}},
		"https://grain.dev/capabilities/api#task-admin",
		"self-debug",
		{"capability":"https://grain.dev/capabilities/repo#read","with":{"repo":"acme/docs"}},
		{"capability":"https://grain.dev/capabilities/repo#write","with":{"repo":"acme/site","base":"release"}},
		{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/widgets","base":"main"}}
	]}`), &req)
	if err != nil {
		t.Fatal(err)
	}
	if req.Capabilities == nil || !reflect.DeepEqual(*req.Capabilities, []string{"api:ci:write@acme/widgets", "api:task-admin", "self-debug"}) {
		t.Errorf("Capabilities = %v", req.Capabilities)
	}
	if !reflect.DeepEqual(req.Reads, []string{"acme/docs"}) {
		t.Errorf("Reads = %v", req.Reads)
	}
	if !reflect.DeepEqual(req.Writes, []WriteTarget{{Repo: "acme/site", Base: "release"}}) {
		t.Errorf("Writes = %v", req.Writes)
	}
	if req.Repo != "acme/widgets" || req.Base != "main" {
		t.Errorf("Repo, Base = %q, %q", req.Repo, req.Base)
	}

	// Repository grants alone leave capabilities to the defaults.
	req = CreateTaskRequest{}
	if err := json.Unmarshal([]byte(`{"grants":[{"capability":"https://grain.dev/capabilities/repo#read","with":{"repo":"acme/docs"}}]}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Capabilities != nil {
		t.Errorf("Capabilities = %v, want nil (the deployment's defaults)", *req.Capabilities)
	}

	for _, body := range []string{
		`{"capabilities":["self-debug"],"grants":["api:ci:read"]}`,
		`{"capabilities":[],"grants":[{"capability":"https://grain.dev/capabilities/api#ci:read"}]}`,
		`{"capabilities":["api:ci:read"],"grants":[]}`,
		`{"reads":["acme/docs"],"grants":[{"capability":"https://grain.dev/capabilities/repo#read","with":{"repo":"acme/x"}}]}`,
		`{"reads":[],"grants":[{"capability":"https://grain.dev/capabilities/repo#read","with":{"repo":"acme/x"}}]}`,
		`{"writes":[{"repo":"acme/site"}],"grants":[{"capability":"https://grain.dev/capabilities/repo#write","with":{"repo":"acme/x","base":""}}]}`,
		`{"writes":[],"grants":[{"capability":"https://grain.dev/capabilities/repo#write","with":{"repo":"acme/x","base":""}}]}`,
		`{"repo":"acme/widgets","grants":[{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/x","base":""}}]}`,
		`{"base":"develop","grants":[{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/x","base":"main"}}]}`,
		`{"noRepo":true,"grants":[{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/x","base":"main"}}]}`,
		`{"grants":[{"capability":"https://grain.dev/capabilities/api#ci:read","with":{"repo":"acme/x","extra":1}}]}`,
	} {
		var req CreateTaskRequest
		if err := json.Unmarshal([]byte(body), &req); err == nil {
			t.Errorf("CreateTaskRequest accepted %s", body)
		}
	}

	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"repo":"acme/widgets","grants":[{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/x","base":""}}]}`, "the request's repo field"},
		{`{"base":"develop","grants":[{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/x","base":"main"}}]}`, "the request's base field"},
		{`{"noRepo":true,"grants":[{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/x","base":"main"}}]}`, "the request's noRepo field"},
	} {
		var req CreateTaskRequest
		err := json.Unmarshal([]byte(tc.body), &req)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Unmarshal(%s) = %v, want substring %q", tc.body, err, tc.want)
		}
	}
}

func TestUpdateTaskRequestGrants(t *testing.T) {
	var req UpdateTaskRequest
	if err := json.Unmarshal([]byte(`{"grants":[
		{"capability":"https://grain.dev/capabilities/api#ci:read"},
		{"capability":"https://grain.dev/capabilities/repo#write","with":{"repo":"acme/site","base":""}}
	]}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Capabilities == nil || !reflect.DeepEqual(*req.Capabilities, []string{"api:ci:read"}) {
		t.Errorf("Capabilities = %v", req.Capabilities)
	}
	if req.Writes == nil || !reflect.DeepEqual(*req.Writes, []WriteTarget{{Repo: "acme/site"}}) {
		t.Errorf("Writes = %v", req.Writes)
	}
	if req.Reads != nil {
		t.Errorf("Reads = %v, want left alone", *req.Reads)
	}
	for _, body := range []string{
		`{"capabilities":["self-debug"],"grants":["api:ci:read"]}`,
		`{"capabilities":["api:ci:read"],"grants":[]}`,
		`{"reads":[],"grants":[{"capability":"https://grain.dev/capabilities/repo#read","with":{"repo":"acme/x"}}]}`,
		`{"writes":[],"grants":[{"capability":"https://grain.dev/capabilities/repo#write","with":{"repo":"acme/x","base":""}}]}`,
		`{"grants":[{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/x","base":""}}]}`,
	} {
		var req UpdateTaskRequest
		if err := json.Unmarshal([]byte(body), &req); err == nil {
			t.Errorf("UpdateTaskRequest accepted %s", body)
		}
	}
}

// TestCreateTaskRequestCapabilitiesReadAsProposeTaskReadsThem is
// docs/test-runs/2026-11-09-access-forms-api-grants.md F1:
// capabilities: [{capability: read-issues}] on POST /api/tasks was
// refused with Go's own "json: cannot unmarshal object into Go struct
// field .Alias.capabilities of type string", where the identical
// propose_task call says a bare name is written as a plain string. Both
// now read the list with model.CapabilityEntries.
func TestCreateTaskRequestCapabilitiesReadAsProposeTaskReadsThem(t *testing.T) {
	var req CreateTaskRequest
	err := json.Unmarshal([]byte(`{"title":"x","capabilities":[{"capability":"read-issues"}]}`), &req)
	if err == nil {
		t.Fatalf("a bare name in a {capability} object was accepted: %v", req.Capabilities)
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Errorf("err = %v (%T), want a validation error", err, err)
	}
	want := `capabilities: capability grant "read-issues": a name that is not a capability reference (https://...) is written as a plain string, "read-issues", not as a {capability, with} object`
	if err.Error() != want {
		t.Errorf("err = %q\nwant %q", err, want)
	}

	req = CreateTaskRequest{}
	if err := json.Unmarshal([]byte(`{"capabilities":[
		"self-debug",
		{"capability":"https://grain.dev/capabilities/api#ci-read","with":{"repo":"acme/widgets"}}
	]}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Capabilities == nil || !reflect.DeepEqual(*req.Capabilities, []string{"self-debug", `https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"}`}) {
		t.Errorf("Capabilities = %v", req.Capabilities)
	}

	// An empty list is still an opinion (no capabilities), and an absent
	// or null one still none (the deployment's defaults).
	for body, wantNil := range map[string]bool{`{"capabilities":[]}`: false, `{"capabilities":null}`: true, `{}`: true} {
		req = CreateTaskRequest{}
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if (req.Capabilities == nil) != wantNil || (!wantNil && len(*req.Capabilities) != 0) {
			t.Errorf("%s: Capabilities = %v, want nil=%v and empty", body, req.Capabilities, wantNil)
		}
	}
	if err := json.Unmarshal([]byte(`{"capabilities":"self-debug"}`), &req); err == nil || !errors.As(err, &validation) {
		t.Errorf("a capabilities string: err = %v, want a validation error", err)
	}
}
