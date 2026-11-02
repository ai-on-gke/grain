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

package capability

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Method is one contributed method of an in-process capability server
// (docs/design/unified-grants.md §4): a capability a build outside core
// answers inside grain itself, as core's https://grain.dev/capabilities/grain
// server answers grain#journal, rather than through an MCP server
// subprocess. Register adds it to every place that resolves, lists or
// forwards the in-process grain server's methods -- grainserver.Lookup,
// grainserver.IsInProcessGrant, GET /api/capability-methods, the run's
// forwarded grants -- so a build can retire one of its own in-process
// providers onto a method without core naming it.
type Method struct {
	// Server is the capability_id of the in-process server the method is
	// on, such as https://grain.dev/capabilities/corp. Every method
	// registered with one Server is served by the one in-process server.
	// It may not be core's own model.GrainCapabilityServerID.
	Server string
	// Spec is the method: its name, arguments and Mint, as
	// protocol.NewMethod takes them.
	Spec protocol.MethodSpec
	// Name is the short display name Settings and the per-task picker
	// show, and Description the one sentence beside it; Description
	// defaults to Spec.Description.
	Name, Description string
	// RunGrant says the method's effect is on the run itself -- tools an
	// agent framework or `grain mcpserver` gives it -- so the
	// orchestrator forwards the grant (and any alias of it a task holds)
	// to the run in agent.RunConfig.Grants, as Registration.RunGrant does
	// for a provider.
	RunGrant bool
	// DefaultOn seeds Grant into a brand-new deployment's default
	// capabilities and the shipped director session's
	// (Registration.DefaultOn).
	DefaultOn bool
	// Aliases are the retired bare capability names the method replaces,
	// such as the provider it retires: model.CanonicalLegacyGrant rewrites
	// each to Grant wherever a grant is recorded, and the bare name keeps
	// granting what Grant does wherever nothing rewrote it (a repository's
	// .grain, an older grain/config, a capability policy entry). The store
	// migration rewriting rows already stored is core's (pkg/model).
	Aliases []string
}

// Ref is the method's capability reference: <Server>#<method>.
func (m Method) Ref() string { return m.Server + "#" + m.Spec.Name }

// Grant is the recorded grant of the method with no arguments:
// <Server>#<method>={}. It is what DefaultOn seeds, what Aliases are
// rewritten to and what RunGrant forwards.
func (m Method) Grant() string { return m.Ref() + "={}" }

var registeredMethods = map[string]Method{}

// Register adds the in-process capability server method m to every
// listing of grain's capabilities until the returned func is called. It
// is meant to be called from init(), and panics on a method whose
// reference is not valid, whose server is core's own grain server, or
// which is already registered, since each is a programming error no
// deployment could work around.
func Register(m Method) (unregister func()) {
	if m.Server == model.GrainCapabilityServerID {
		panic(fmt.Sprintf("capability: Register(%q): core's own grain server takes no contributed methods", m.Ref()))
	}
	if _, err := protocol.ParseRef(m.Ref()); err != nil {
		panic(fmt.Sprintf("capability: Register(%q): %v", m.Ref(), err))
	}
	if _, err := protocol.NewMethod(m.Spec); err != nil {
		panic(fmt.Sprintf("capability: Register(%q): %v", m.Ref(), err))
	}
	if m.Description == "" {
		m.Description = m.Spec.Description
	}
	m.Aliases = slices.Clone(m.Aliases)
	mu.Lock()
	defer mu.Unlock()
	ref := m.Ref()
	if _, ok := registeredMethods[ref]; ok {
		panic(fmt.Sprintf("capability: Register(%q): already registered", ref))
	}
	var undo []func()
	registered := false
	defer func() {
		// A refused alias panics after the ones before it took; undo them.
		if !registered {
			for _, f := range undo {
				f()
			}
		}
	}()
	for _, alias := range m.Aliases {
		undo = append(undo, model.RegisterLegacyGrantAlias(alias, m.Grant()))
	}
	registeredMethods[ref] = m
	registered = true
	if m.DefaultOn {
		undo = append(undo, model.RegisterDefaultConsoleSessionCapability(m.Grant()))
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		delete(registeredMethods, ref)
		for _, f := range undo {
			f()
		}
	}
}

// Methods returns every registered method, ordered by reference.
func Methods() []Method {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Method, 0, len(registeredMethods))
	for _, m := range registeredMethods {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out
}

// MethodServers returns the capability_id of every in-process server a
// registered method is on, sorted.
func MethodServers() []string {
	var ids []string
	for _, m := range Methods() {
		if !slices.Contains(ids, m.Server) {
			ids = append(ids, m.Server)
		}
	}
	return ids
}

// IsMethodServer reports whether id is the capability_id of an in-process
// server a registered method is on.
func IsMethodServer(id string) bool {
	return slices.Contains(MethodServers(), id)
}

// MethodServer builds the in-process server id names from the methods
// registered on it, or reports false when none is. Like
// grainserver.Lookup's, the server is built afresh on each call, so it
// always serves what is registered now.
func MethodServer(id string) (*protocol.Server, bool, error) {
	var methods []*protocol.Method
	for _, m := range Methods() {
		if m.Server != id {
			continue
		}
		pm, err := protocol.NewMethod(m.Spec)
		if err != nil {
			return nil, true, fmt.Errorf("capability %s: building #%s: %w", id, m.Spec.Name, err)
		}
		methods = append(methods, pm)
	}
	if len(methods) == 0 {
		return nil, false, nil
	}
	cs, err := protocol.NewCapabilityServer(path.Base(strings.TrimRight(id, "/")), methods...)
	if err != nil {
		return nil, true, fmt.Errorf("capability %s: building capability server: %w", id, err)
	}
	srv, err := protocol.NewServer(context.Background(), id, cs.InProcessCaller())
	if err != nil {
		return nil, true, fmt.Errorf("capability %s: attaching in-process server: %w", id, err)
	}
	return srv, true, nil
}

// methodRunGrants is the grant names of the registered methods with
// RunGrant set: each one's reference, its recorded grant and its
// aliases, which are every spelling a task may hold it by.
func methodRunGrants() []string {
	var out []string
	for _, m := range Methods() {
		if m.RunGrant {
			out = append(out, m.Ref(), m.Grant())
			out = append(out, m.Aliases...)
		}
	}
	return out
}

// methodDefaultOn is the recorded grants of the registered methods with
// DefaultOn set.
func methodDefaultOn() []string {
	var out []string
	for _, m := range Methods() {
		if m.DefaultOn {
			out = append(out, m.Grant())
		}
	}
	return out
}
