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

package capability_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A registered method is listed, forwarded to the run under every spelling a
// task may hold it by, seeded as a default, and its alias is rewritten to
// its grant -- until it is unregistered.
func TestRegisterMethod(t *testing.T) {
	if model.CanonicalLegacyGrant(capabilitytest.MethodAlias) != capabilitytest.MethodAlias {
		t.Fatalf("%s is an alias before anything registers it", capabilitytest.MethodAlias)
	}
	unregister := capability.Register(capability.Method{
		Server: capabilitytest.MethodServer,
		Spec:   capabilitytest.MethodSpec("other"),
	})
	capabilitytest.RegisterMethod(t)

	var refs []string
	for _, m := range capability.Methods() {
		refs = append(refs, m.Ref())
	}
	if want := []string{capabilitytest.MethodRef, capabilitytest.MethodServer + "#other"}; !slices.Equal(refs, want) {
		t.Errorf("Methods() = %v, want %v, ordered by reference", refs, want)
	}
	if got := capability.MethodServers(); !slices.Equal(got, []string{capabilitytest.MethodServer}) {
		t.Errorf("MethodServers() = %v, want the one server both methods are on", got)
	}
	srv, ok, err := capability.MethodServer(capabilitytest.MethodServer)
	if err != nil || !ok || srv.ID != capabilitytest.MethodServer || len(srv.Methods) != 2 {
		t.Fatalf("MethodServer() = %+v, %v, %v; want the server with both methods", srv, ok, err)
	}
	if _, ok, _ := capability.MethodServer("https://grain.example/capabilities/none"); ok {
		t.Error("MethodServer() built a server nothing registered a method on")
	}

	want := []string{capabilitytest.MethodRef, capabilitytest.MethodGrant, capabilitytest.MethodAlias}
	if got := capability.RunGrants(); !slices.Equal(got, want) {
		t.Errorf("RunGrants() = %v, want %v", got, want)
	}
	if got := capability.DefaultOn(); !slices.Equal(got, []string{capabilitytest.MethodGrant}) {
		t.Errorf("DefaultOn() = %v, want only the method grant", got)
	}
	if got := capability.NewDeploymentConfig().DefaultCapabilities; !slices.Equal(got, []string{capabilitytest.MethodGrant}) {
		t.Errorf("NewDeploymentConfig().DefaultCapabilities = %v, want the method grant", got)
	}
	if got := model.DefaultConsoleSessions()[0].Capabilities; !slices.Contains(got, capabilitytest.MethodGrant) {
		t.Errorf("the director's default capabilities %v do not hold the method grant", got)
	}
	if got := model.CanonicalLegacyGrants(" " + capabilitytest.MethodAlias); !slices.Equal(got, []string{capabilitytest.MethodGrant}) {
		t.Errorf("CanonicalLegacyGrants(%q) = %v, want the method grant", capabilitytest.MethodAlias, got)
	}

	unregister()
	if got := len(capability.Methods()); got != 1 {
		t.Errorf("unregistering left %d methods, want 1", got)
	}
}

func TestRegisterMethodUndoesEverythingWhenUnregistered(t *testing.T) {
	unregister := capability.Register(capability.Method{
		Server:    capabilitytest.MethodServer,
		Spec:      capabilitytest.MethodSpec("gone"),
		RunGrant:  true,
		DefaultOn: true,
		Aliases:   []string{"zz-gone"},
	})
	unregister()
	if len(capability.Methods()) != 0 || len(capability.RunGrants()) != 0 || len(capability.DefaultOn()) != 0 {
		t.Errorf("after unregistering: Methods %v, RunGrants %v, DefaultOn %v; want none",
			capability.Methods(), capability.RunGrants(), capability.DefaultOn())
	}
	if got := model.CanonicalLegacyGrant("zz-gone"); got != "zz-gone" {
		t.Errorf("CanonicalLegacyGrant(zz-gone) = %q after unregistering, want it as it is", got)
	}
	if got := model.DefaultConsoleSessions()[0].Capabilities; slices.Contains(got, capabilitytest.MethodServer+"#gone={}") {
		t.Errorf("the director's default capabilities %v still hold the method", got)
	}
}

func TestRegisterMethodRefusesABadMethod(t *testing.T) {
	capabilitytest.RegisterMethod(t)
	for name, m := range map[string]capability.Method{
		"duplicate":        {Server: capabilitytest.MethodServer, Spec: capabilitytest.MethodSpec(capabilitytest.MethodName)},
		"core grain":       {Server: model.GrainCapabilityServerID, Spec: capabilitytest.MethodSpec("extra")},
		"no server":        {Spec: capabilitytest.MethodSpec("extra")},
		"bad method name":  {Server: capabilitytest.MethodServer, Spec: capabilitytest.MethodSpec("Not A Method")},
		"core alias taken": {Server: capabilitytest.MethodServer, Spec: capabilitytest.MethodSpec("extra"), Aliases: []string{"self-repair"}},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("Register did not panic")
				}
			}()
			capability.Register(m)
		})
	}
	if got := len(capability.Methods()); got != 1 {
		t.Errorf("a refused registration left %d methods, want only the made-up one", got)
	}

	// An alias refused after another took leaves neither behind.
	func() {
		defer func() { _ = recover() }()
		capability.Register(capability.Method{Server: capabilitytest.MethodServer, Spec: capabilitytest.MethodSpec("extra"),
			Aliases: []string{"zz-first", "self-repair"}})
	}()
	if got := model.CanonicalLegacyGrant("zz-first"); got != "zz-first" {
		t.Errorf("CanonicalLegacyGrant(zz-first) = %q after a refused registration, want it as it is", got)
	}
}
