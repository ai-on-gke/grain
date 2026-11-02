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

// Package grainserver holds grain's in-process capability servers
// (docs/design/unified-grants.md §1, §4), answered by grain itself rather
// than by an external MCP server subprocess:
//
//   - https://grain.dev/capabilities/grain: the host- and controller-side
//     diagnostic and database grants.
//   - https://grain.dev/capabilities/repo: repository access. Its #write
//     method so far carries one value, GitPushAnyRefGrant (formerly
//     git-push-any-ref), which the git proxy reads off the live task
//     (model.Store.GitPushAnyRef).
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

	// HealthMethod lets a task read host/sandbox probes and local state snapshot (formerly deployment-health).
	HealthMethod = "health"
	HealthRef    = model.GrainHealthRef
	HealthGrant  = model.GrainHealthGrant

	// LegacyDeploymentHealth is the retired in-process capability name for api:deployment:read + HealthGrant.
	LegacyDeploymentHealth = "deployment-health"

	// SQLMethod lets a task run SQL against Grain's database (formerly db-read / db-write).
	SQLMethod     = model.GrainSQLMethod
	SQLRef        = model.GrainSQLRef
	SQLReadGrant  = model.GrainSQLReadGrant
	SQLWriteGrant = model.GrainSQLWriteGrant

	// HostExecMethod lets a task run shell commands on grain's own host,
	// each confirmed by a live reply in its chat (formerly self-repair).
	// Only a person may grant it (model's RequirePersonGrant).
	HostExecMethod = model.GrainHostExecMethod
	HostExecRef    = model.GrainHostExecRef
	HostExecGrant  = model.GrainHostExecGrant

	// LegacySelfRepair is the retired in-process capability name for HostExecGrant.
	LegacySelfRepair = "self-repair"

	// RepoID is the canonical capability_id of the in-process repo server.
	RepoID = model.RepoCapabilityServerID

	// RepoWriteMethod widens which refs of the repositories a task may
	// write its run may push (formerly git-push-any-ref).
	RepoWriteMethod    = model.RepoWriteMethod
	RepoWriteRef       = model.RepoWriteRef
	GitPushAnyRefGrant = model.GitPushAnyRefGrant

	// LegacyGitPushAnyRef is the retired in-process capability name for GitPushAnyRefGrant.
	LegacyGitPushAnyRef = model.GitPushAnyRefCapability
)

var (
	serverOnce sync.Once
	serverInst *protocol.Server

	repoOnce sync.Once
	repoInst *protocol.Server
)

// callerClock is the in-process methods' clock. An external server
// holds a pass's deadline to its own clock; this one runs inside grain,
// whose orchestrator set that deadline from the clock it runs on (a test
// world's, say), so there is no second clock to hold it to.
func callerClock() time.Time { return time.Time{} }

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
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", JournalMethod, err))
	}
	health, err := protocol.NewMethod(protocol.MethodSpec{
		Name: HealthMethod,
		Description: "Read bounded controller-side host RAM, swap, disks and load, live sandbox/VM " +
			"counts and per-sandbox resource readings, and the local synchronized deployment-state " +
			"snapshot, without host shell or sandbox network access.",
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", HealthMethod, err))
	}
	sqlMethod, err := protocol.NewMethod(protocol.MethodSpec{
		Name: SQLMethod,
		Description: "Execute SQL against Grain's database: read-only queries by default " +
			"(write: false), or read and write statements when write: true (dangerous).",
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"write": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Allow write statements (INSERT, UPDATE, DELETE, DDL) as well as read-only SQL queries.",
				},
			},
		},
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", SQLMethod, err))
	}
	hostExec, err := protocol.NewMethod(protocol.MethodSpec{
		Name: HostExecMethod,
		Description: "Let this task run commands on grain's own host -- restart services, edit config, " +
			"call the grain CLI -- each one needing a live reply in the task's chat before it runs " +
			"(dangerous: only a person may grant it, and only an interactive task gets the tool).",
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building #%s: %v", HostExecMethod, err))
	}
	cs, err := protocol.NewCapabilityServer("grain", journal, health, sqlMethod, hostExec)
	if err != nil {
		panic(fmt.Sprintf("grainserver: building capability server: %v", err))
	}
	srv, err := protocol.NewServer(context.Background(), ID, cs.InProcessCaller())
	if err != nil {
		panic(fmt.Sprintf("grainserver: attaching in-process server: %v", err))
	}
	return srv
}

func buildRepoServer() *protocol.Server {
	write, err := protocol.NewMethod(protocol.MethodSpec{
		Name: RepoWriteMethod,
		Description: "Let this task's run push any branch or tag of the repositories it may write -- the " +
			"default branch, force-pushes and deletions included -- rather than only its own task branch, " +
			"with the team's own git credential. Standing jobs declare the refs they push under `push:` in " +
			"their .grain template instead.",
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"refs", "force"},
			"properties": map[string]any{
				"refs": map[string]any{
					"type":        "array",
					"description": "Ref patterns the run may update beyond its own task branch. Only every branch and every tag together is honoured so far.",
					"items":       map[string]any{"type": "string", "enum": []any{"refs/heads/*", "refs/tags/*"}},
					"minItems":    2,
					"maxItems":    2,
					"uniqueItems": true,
				},
				"force": map[string]any{
					"type":        "boolean",
					"const":       true,
					"description": "Allow force-pushes and deletions of those refs as well as fast-forwards.",
				},
			},
		},
		// Nothing is placed in the sandbox: the git proxy reads the grant
		// off the live task when a push names another ref.
		Mint: func(_ context.Context, _ string, _ time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK}, nil
		},
		Now: callerClock,
	})
	if err != nil {
		panic(fmt.Sprintf("grainserver: building repo#%s: %v", RepoWriteMethod, err))
	}
	cs, err := protocol.NewCapabilityServer("repo", write)
	if err != nil {
		panic(fmt.Sprintf("grainserver: building repo capability server: %v", err))
	}
	srv, err := protocol.NewServer(context.Background(), RepoID, cs.InProcessCaller())
	if err != nil {
		panic(fmt.Sprintf("grainserver: attaching in-process repo server: %v", err))
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

// RepoServer returns the singleton in-process https://grain.dev/capabilities/repo server.
func RepoServer() *protocol.Server {
	repoOnce.Do(func() {
		repoInst = buildRepoServer()
	})
	return repoInst
}

// Status reports the in-process grain server's method details in the same shape
// servers.Registry.Statuses returns for external servers, so GET /api/capability-methods
// lists its methods without registering a subprocess in grain_config.mcp_servers.
func Status() servers.Status { return statusOf(ID, Server()) }

// RepoStatus is Status for the in-process repo server.
func RepoStatus() servers.Status { return statusOf(RepoID, RepoServer()) }

// Statuses is every in-process server's Status, by ID.
func Statuses() []servers.Status { return []servers.Status{Status(), RepoStatus()} }

// IsInProcessID reports whether capabilityID is one of the in-process servers.
func IsInProcessID(capabilityID string) bool { return capabilityID == ID || capabilityID == RepoID }

func statusOf(id string, srv *protocol.Server) servers.Status {
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
		ID:            id,
		State:         servers.Attached,
		Transport:     "in-process",
		Methods:       names,
		MethodDetails: details,
	}
}

// Lookup wraps fallback so ID and RepoID always resolve to the in-process
// servers, even when fallback is nil or comes from a registry that only
// tracks mcp_servers.
func Lookup(fallback protocol.Lookup) protocol.Lookup {
	return func(capabilityID string) (*protocol.Server, error) {
		switch capabilityID {
		case ID:
			return buildServer(), nil
		case RepoID:
			return buildRepoServer(), nil
		}
		if fallback != nil {
			return fallback(capabilityID)
		}
		return nil, fmt.Errorf("capability %s is not configured in mcpServers", capabilityID)
	}
}

// IsInProcessGrant reports whether grant is a capability reference targeting
// an in-process server (which opens no external control- or data-plane
// network listener).
func IsInProcessGrant(grant string) bool {
	if !model.IsCapabilityRef(grant) {
		return false
	}
	ref, err := protocol.ParseRef(model.CapabilityGrantRef(grant))
	return err == nil && IsInProcessID(ref.CapabilityID)
}

// IsJournalGrant reports whether grant names grain#journal (recorded or bare)
// or the retired bare grain-log alias.
func IsJournalGrant(grant string) bool { return model.IsGrainJournalGrant(grant) }

// HasJournal reports whether any grant in grants opens grain#journal.
func HasJournal(grants []string) bool { return model.HasGrainJournal(grants) }

// IsHealthGrant reports whether grant names grain#health (recorded or bare)
// or the retired bare deployment-health alias.
func IsHealthGrant(grant string) bool { return model.IsGrainHealthGrant(grant) }

// HasHealth reports whether any grant in grants opens grain#health.
func HasHealth(grants []string) bool { return model.HasGrainHealth(grants) }

// IsSQLGrant reports whether grant names grain#sql (read or write) or a retired db-read/db-write alias.
func IsSQLGrant(grant string) bool { return model.IsGrainSQLGrant(grant) }

// IsSQLWriteGrant reports whether grant names grain#sql with write: true or the retired db-write alias.
func IsSQLWriteGrant(grant string) bool { return model.IsGrainSQLWriteGrant(grant) }

// HasSQL reports whether any grant in grants opens grain#sql (read or write).
func HasSQL(grants []string) bool { return model.HasGrainSQL(grants) }

// HasSQLWrite reports whether any grant in grants opens grain#sql with write: true.
func HasSQLWrite(grants []string) bool { return model.HasGrainSQLWrite(grants) }

// IsHostExecGrant reports whether grant names grain#host-exec or the retired self-repair alias.
func IsHostExecGrant(grant string) bool { return model.IsGrainHostExecGrant(grant) }

// HasHostExec reports whether any grant in grants opens grain#host-exec.
func HasHostExec(grants []string) bool { return model.HasGrainHostExec(grants) }

// IsGitPushAnyRefGrant reports whether grant is GitPushAnyRefGrant or the
// retired bare git-push-any-ref alias.
func IsGitPushAnyRefGrant(grant string) bool { return model.IsGitPushAnyRefGrant(grant) }
