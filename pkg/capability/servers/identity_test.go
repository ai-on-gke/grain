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

package servers

import (
	"context"
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// Every attach is given the deployment's identity, and a server attached
// under one that has since changed -- a deployment id renamed in
// Settings -- is attached again.
func TestARenamedDeploymentReattachesItsServers(t *testing.T) {
	h := newHarness()
	id := protocol.Identity{DeploymentID: "049f1e09", InstanceID: "049f1e09c0ffee01"}
	r := New(Options{Attach: h.attach, Identity: func() protocol.Identity { return id }})
	ctx := context.Background()
	servers := map[string]protocol.ServerConfig{gcp: {Command: "grain", Args: []string{"capability", "gcp"}}}
	r.Sync(ctx, servers)
	r.Sync(ctx, servers)
	if h.attachs[gcp] != 1 || h.idents[0] != id {
		t.Fatalf("attachs %d, identities %v", h.attachs[gcp], h.idents)
	}
	first := h.callers[gcp]
	id.DeploymentID = "bsalmon"
	r.Sync(ctx, servers)
	if !first.closed {
		t.Error("the server attached under the old id was not closed")
	}
	if h.attachs[gcp] != 2 || h.idents[1].DeploymentID != "bsalmon" {
		t.Fatalf("after the rename: attachs %d, identities %v", h.attachs[gcp], h.idents)
	}
	if _, err := r.Lookup(gcp); err != nil {
		t.Errorf("Lookup after the rename: %v", err)
	}
}

type identitySource struct {
	dep, inst string
	err       error
}

func (s *identitySource) DeploymentID(context.Context) (string, error) { return s.dep, s.err }
func (s *identitySource) InstanceID(context.Context) (string, error)   { return s.inst, s.err }

func TestStoreIdentityKeepsTheLastOnAFailedRead(t *testing.T) {
	src := &identitySource{dep: "049f1e09", inst: "abc"}
	read := StoreIdentity(src, nil)
	if got := read(); got != (protocol.Identity{DeploymentID: "049f1e09", InstanceID: "abc"}) {
		t.Fatalf("identity = %+v", got)
	}
	src.err = errors.New("database is locked")
	if got := read(); got.DeploymentID != "049f1e09" {
		t.Errorf("a failed read gave %+v", got)
	}
}
