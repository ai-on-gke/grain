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

// Package grainserver is the in-process https://grain.dev/capabilities/grain
// capability server (docs/design/unified-grants.md §1, §4): the host- and
// controller-side diagnostic and database grants answered by grain itself
// rather than by an external MCP server subprocess.
package grainserver

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// ID is the canonical capability_id of the in-process grain server.
	ID = model.GrainCapabilityServerID

	// JournalMethod lets a task read grain's redacted host journal (formerly grain-log).
	JournalMethod = "journal"
	JournalRef    = model.GrainJournalRef
	JournalGrant  = model.GrainJournalGrant

	// LegacyGrainLog is the retired in-process capability name for JournalGrant.
	LegacyGrainLog = "grain-log"
)

var (
	serverOnce sync.Once
	serverInst *protocol.Server
)

func buildServer() *protocol.Server {
	journal, err := protocol.NewMethod(protocol.MethodSpec{
		Name: JournalMethod,
		Description: "Let this task read a bounded slice of grain's own systemd journal on this " +
			"host -- the daemon's log and the kernel's -- with known credentials redacted before " +
			"it leaves. Separate from self-debug on purpose: the journal carries whatever anything " +
			"here has ever logged, which is far more than grain's source code.",
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", JournalMethod, err))
	}
	cs, err := protocol.NewCapabilityServer("grain", journal)
	if err != nil {
		panic(fmt.Sprintf("grainserver: building capability server: %v", err))
	}
	srv, err := protocol.NewServer(context.Background(), ID, cs.InProcessCaller())
	if err != nil {
		panic(fmt.Sprintf("grainserver: attaching in-process server: %v", err))
	}
	return srv
}

// Server returns the singleton in-process https://grain.dev/capabilities/grain server.
func Server() *protocol.Server {
	serverOnce.Do(func() {
		serverInst = buildServer()
	})
	return serverInst
}

// Status reports the in-process server's method details in the same shape
// servers.Registry.Statuses returns for external servers, so GET /api/capability-methods
// lists its methods without registering a subprocess in grain_config.mcp_servers.
func Status() servers.Status {
	srv := Server()
	names := make([]string, 0, len(srv.Methods))
	for name := range srv.Methods {
		names = append(names, name)
	}
	sort.Strings(names)
	details := make([]servers.MethodDetail, 0, len(names))
	for _, name := range names {
		m := srv.Methods[name]
		details = append(details, servers.MethodDetail{
			Name:        name,
			Description: m.Description,
			Args:        m.ArgsSchema,
			Releasable:  m.Releasable,
			Delivery:    m.MCPDelivery,
		})
	}
	return servers.Status{
		ID:            ID,
		State:         servers.Attached,
		Transport:     "in-process",
		Methods:       names,
		MethodDetails: details,
	}
}

// Lookup wraps fallback so ID always resolves to the in-process Server(), even
// when fallback is nil or comes from a registry that only tracks mcp_servers.
func Lookup(fallback protocol.Lookup) protocol.Lookup {
	return func(capabilityID string) (*protocol.Server, error) {
		if capabilityID == ID {
			return Server(), nil
		}
		if fallback != nil {
			return fallback(capabilityID)
		}
		return nil, fmt.Errorf("capability %s is not configured in mcpServers", capabilityID)
	}
}

// IsInProcessGrant reports whether grant is a capability reference targeting
// the in-process grain server (which opens no external control- or data-plane
// network listener).
func IsInProcessGrant(grant string) bool {
	if !model.IsCapabilityRef(grant) {
		return false
	}
	ref, err := protocol.ParseRef(model.CapabilityGrantRef(grant))
	return err == nil && ref.CapabilityID == ID
}

// IsJournalGrant reports whether grant names grain#journal (recorded or bare)
// or the retired bare grain-log alias.
func IsJournalGrant(grant string) bool { return model.IsGrainJournalGrant(grant) }

// HasJournal reports whether any grant in grants opens grain#journal.
func HasJournal(grants []string) bool { return model.HasGrainJournal(grants) }
