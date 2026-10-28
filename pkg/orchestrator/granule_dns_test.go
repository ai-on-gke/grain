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

package orchestrator

// GranuleConfig.DNS: a granule VM's "-dns" is asked for at the same
// moment, and through the same function, as the post-boot package-cache
// rewrite (cmd/grain's packageCache.sandboxDNS) -- so the two can never
// tell a guest two different things, and a cache that is off or not yet
// serving leaves a create exactly as it was before this field existed.

import (
	"context"
	"slices"
	"testing"
)

// A deployment with no DNS resolver at all -- the default, same as
// before this field existed -- creates a VM with no "-dns" flag.
func TestNoDNSResolverLeavesTheCreateArgsWithoutDNS(t *testing.T) {
	cfg := GranuleConfig{CreateArgs: []string{"-disk-mode=overlay"}}

	args, err := cfg.createArgsWithImage(context.Background(), Shape{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "-dns") {
		t.Errorf("args = %q, want no -dns with no resolver configured", args)
	}
}

// A resolver that answers "" -- the cache switched off, or its resolver
// not actually serving (sandboxDNS's own doc comment) -- gets the same
// treatment as no resolver at all: never pass a nameserver nobody is
// listening on.
func TestADNSResolverAnsweringEmptyLeavesTheCreateArgsWithoutDNS(t *testing.T) {
	asked := false
	cfg := GranuleConfig{DNS: func(context.Context) string { asked = true; return "" }}

	args, err := cfg.createArgsWithImage(context.Background(), Shape{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Error("the resolver was never asked")
	}
	if slices.Contains(args, "-dns") {
		t.Errorf("args = %q, want no -dns when the resolver answered empty", args)
	}
}

// A resolver that is actually serving reaches the VM's own kernel command
// line, so a guest resolves through this deployment's package cache from
// its first boot rather than only after the post-boot rewrite reaches it.
func TestADNSResolverThatIsServingReachesTheCreateArgs(t *testing.T) {
	cfg := GranuleConfig{
		CreateArgs: []string{"-disk-mode=overlay"},
		DNS:        func(context.Context) string { return "172.17.0.1" },
	}

	args, err := cfg.createArgsWithImage(context.Background(), Shape{}, "")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(args, "-dns")
	if i < 0 || i+1 >= len(args) || args[i+1] != "172.17.0.1" {
		t.Errorf("args = %q, want -dns 172.17.0.1", args)
	}
}
