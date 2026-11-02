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
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A method contributed from outside core (capability.Register) is listed by
// GET /api/capability-methods beside the in-process grain server's, and a
// task can be granted it -- by its reference or by the bare name it retires,
// which is recorded as the method grant -- without core naming it.
func TestRegisteredMethodIsListedAndGranted(t *testing.T) {
	ts, client := grantTestServer(t)
	capabilitytest.RegisterMethod(t)

	var methods []ui.CapabilityMethod
	if code := postJSON(t, ts, "GET", "/api/capability-methods", nil, &methods); code != 200 {
		t.Fatalf("status %d", code)
	}
	i := slices.IndexFunc(methods, func(m ui.CapabilityMethod) bool { return m.Ref == capabilitytest.MethodRef })
	if i < 0 {
		t.Fatalf("methods = %+v, want %s among them", methods, capabilitytest.MethodRef)
	}
	if m := methods[i]; m.Capability != capabilitytest.MethodServer || m.Method != capabilitytest.MethodName || m.RequiresArgs {
		t.Errorf("listed %+v", m)
	}
	if !slices.ContainsFunc(methods, func(m ui.CapabilityMethod) bool { return m.Ref == model.GrainJournalRef }) {
		t.Errorf("listing the contributed server lost the grain server's methods: %+v", methods)
	}

	ctx := context.Background()
	for _, asked := range []string{capabilitytest.MethodRef, capabilitytest.MethodGrant, capabilitytest.MethodAlias} {
		task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "holds " + asked, Capabilities: &[]string{asked}})
		if err != nil {
			t.Fatalf("filing a task holding %s: %v", asked, err)
		}
		if held := task.Capabilities; !slices.Equal(held, []string{capabilitytest.MethodGrant}) {
			t.Errorf("a task filed holding %s holds %v, want %s", asked, held, capabilitytest.MethodGrant)
		}
	}

	checks, err := client.CheckCapabilityGrants(ctx, []ui.GrantRequest{{Capability: capabilitytest.MethodRef}})
	if err != nil {
		t.Fatal(err)
	}
	if c := checks[0]; c.Error != "" || c.Unchecked || c.Recorded != capabilitytest.MethodGrant {
		t.Errorf("checking %s = %+v, want it recorded as %s against its in-process server", capabilitytest.MethodRef, c, capabilitytest.MethodGrant)
	}
}
