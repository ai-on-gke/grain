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

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/server"
)

func runDemo(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("grain-registry demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:0", "listen address for demo server")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", *addr, err)
	}
	defer ln.Close()

	demo := newDemoState()
	srv := &http.Server{
		Handler:           demo.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	fmt.Fprintf(stdout, "grain-registry demo listening on http://%s\n", ln.Addr().String())

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

type demoState struct {
	mu             sync.RWMutex
	now            time.Time
	callerEmail    string
	instanceDomain string
	registryHost   string
	shapes         []api.Shape
	entries        map[string]*api.Entry
	order          []string
	replicas       []api.ReplicaStatus
	tunnels        []api.TunnelSessionInfo
	auditLog       []api.AuditLogEntry
	nextAuditID    int64
}

func newDemoState() *demoState {
	now := time.Date(2026, 11, 6, 23, 30, 0, 0, time.UTC)
	boolPtr := func(b bool) *bool { return &b }
	intPtr := func(i int) *int { return &i }
	timePtr := func(t time.Time) *time.Time { return &t }

	d := &demoState{
		now:            now,
		callerEmail:    "alice@example.com",
		instanceDomain: "grain.example.com",
		registryHost:   "registry.grain.example.com",
		shapes: []api.Shape{
			{
				Name:         "small",
				DisplayName:  "Small (2 vCPU, 8 GB)",
				Description:  "Lightweight hosted grain VM for single-repo workflows",
				MachineType:  "e2-standard-2",
				VCPU:         2,
				MemoryGB:     8,
				BootDiskGB:   50,
				BootDiskIOPS: 3000,
				QuotaUnits:   1,
			},
			{
				Name:         "medium",
				DisplayName:  "Medium (4 vCPU, 16 GB)",
				Description:  "Standard hosted grain VM for multi-agent builds and tests",
				MachineType:  "e2-standard-4",
				VCPU:         4,
				MemoryGB:     16,
				BootDiskGB:   100,
				BootDiskIOPS: 3000,
				QuotaUnits:   2,
				Default:      true,
			},
			{
				Name:         "large",
				DisplayName:  "Large (8 vCPU, 32 GB)",
				Description:  "High-capacity hosted grain VM for heavy monorepo builds",
				MachineType:  "e2-standard-8",
				VCPU:         8,
				MemoryGB:     32,
				BootDiskGB:   200,
				BootDiskIOPS: 6000,
				QuotaUnits:   4,
			},
		},
		entries: make(map[string]*api.Entry),
		replicas: []api.ReplicaStatus{
			{
				ReplicaID:     "registry-us-central1-a-01",
				PeerAddr:      "10.128.0.14:8082",
				StartedAt:     now.Add(-48 * time.Hour),
				HeartbeatAt:   now.Add(-3 * time.Second),
				Healthy:       true,
				ActiveTunnels: 1,
			},
			{
				ReplicaID:     "registry-us-central1-b-02",
				PeerAddr:      "10.128.0.19:8082",
				StartedAt:     now.Add(-24 * time.Hour),
				HeartbeatAt:   now.Add(-5 * time.Second),
				Healthy:       true,
				ActiveTunnels: 1,
			},
		},
		tunnels: []api.TunnelSessionInfo{
			{
				EntryName:    "alice-workstation",
				ReplicaID:    "registry-us-central1-a-01",
				PeerAddr:     "10.128.0.14:8082",
				SessionEpoch: 7,
				RemoteAddr:   "203.0.113.42:54318",
				ConnectedAt:  now.Add(-6 * time.Hour),
				LastSeenAt:   now.Add(-8 * time.Second),
				RTTMs:        14,
				CredID:       "cred-alice-ws-01",
			},
			{
				EntryName:    "ml-trainer-k8s",
				ReplicaID:    "registry-us-central1-b-02",
				PeerAddr:     "10.128.0.19:8082",
				SessionEpoch: 12,
				RemoteAddr:   "198.51.100.88:49120",
				ConnectedAt:  now.Add(-18 * time.Hour),
				LastSeenAt:   now.Add(-52 * time.Second),
				RTTMs:        38,
				CredID:       "cred-ml-k8s-01",
			},
		},
		auditLog: []api.AuditLogEntry{
			{
				ID:         104,
				At:         now.Add(-12 * time.Minute),
				ActorEmail: "bob@example.com",
				Action:     "entry_transfer_offer",
				EntryName:  "ml-trainer-k8s",
				Detail:     map[string]any{"to": "alice@example.com"},
			},
			{
				ID:         103,
				At:         now.Add(-25 * time.Minute),
				ActorEmail: "alice@example.com",
				Action:     "entry_replace",
				EntryName:  "payments-staging",
				Detail:     map[string]any{"shape": "large", "target_version": "v0.14.2"},
			},
			{
				ID:         102,
				At:         now.Add(-2 * time.Hour),
				ActorEmail: "alice@example.com",
				Action:     "credential_rotate",
				EntryName:  "alice-workstation",
				Detail:     map[string]any{"cred_id": "cred-alice-ws-01"},
			},
			{
				ID:         101,
				At:         now.Add(-6 * time.Hour),
				ActorEmail: "alice@example.com",
				Action:     "entry_scopes_update",
				EntryName:  "alice-workstation",
				Detail:     map[string]any{"repo": "acme/platform"},
			},
		},
		nextAuditID: 105,
	}

	seedEntries := []*api.Entry{
		{
			Name:                "platform-vm",
			URL:                 "https://platform-vm.grain.example.com",
			Kind:                api.KindInstance,
			State:               api.StateRunning,
			Status:              api.StatusOnline,
			Health:              api.HealthOK,
			HostKind:            api.HostKindRegistryVM,
			DisplayName:         "Platform Core VM",
			Purpose:             "Primary hosted grain VM for acme/platform and production GKE cluster manifests.",
			Owner:               "alice@example.com",
			OwnerGroup:          "group:platform-eng@example.com",
			Listed:              true,
			Tags:                []string{"platform", "production", "gke"},
			Shape:               "medium",
			Zone:                "us-central1-a",
			MachineType:         "e2-standard-4",
			BootDiskGB:          100,
			AllowSSH:            true,
			ShipLogs:            false,
			BackendURL:          "http://10.128.15.22:8080",
			GrainVersion:        "v0.14.2",
			GrainCommit:         "9f3c1e8b4a12",
			ActiveTasks:         3,
			AwaitingReplyTasks:  1,
			QueueDepth:          1,
			ActiveSessions:      2,
			LastHeartbeatAt:     timePtr(now.Add(-6 * time.Second)),
			LastSeenAt:          timePtr(now.Add(-6 * time.Second)),
			HeartbeatAgeSeconds: intPtr(6),
			Scopes: []api.Scope{
				{
					Repo:          "acme/platform",
					PathPrefix:    "",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceReported,
					Branch:        "main",
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-72 * time.Hour)),
					UpdatedAt:     now.Add(-6 * time.Second),
				},
				{
					Repo:          "acme/infra",
					PathPrefix:    "clusters/prod",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceDeclared,
					Branch:        "main",
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-48 * time.Hour)),
					UpdatedAt:     now.Add(-6 * time.Second),
				},
			},
			ReportedScopes: []api.Scope{
				{
					Repo:          "acme/platform",
					PathPrefix:    "",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceReported,
					Branch:        "main",
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-72 * time.Hour)),
					UpdatedAt:     now.Add(-6 * time.Second),
				},
				{
					Repo:           "acme/infra",
					PathPrefix:     "",
					Role:           api.RolePrimary,
					EffectiveRole:  api.RolePrimary,
					Source:         api.SourceReported,
					Branch:         "main",
					Active:         false,
					InactiveReason: api.InactiveOverriddenByNarrower,
					ClaimedAt:      timePtr(now.Add(-72 * time.Hour)),
					UpdatedAt:      now.Add(-6 * time.Second),
				},
			},
			DeclaredScopes: []api.Scope{
				{
					Repo:          "acme/infra",
					PathPrefix:    "clusters/prod",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceDeclared,
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-48 * time.Hour)),
					UpdatedAt:     now.Add(-6 * time.Second),
				},
			},
			CreatedAt:  now.Add(-14 * 24 * time.Hour),
			UpdatedAt:  now.Add(-6 * time.Second),
			Generation: 4,
		},
		{
			Name:                "alice-workstation",
			URL:                 "https://alice-workstation.grain.example.com",
			Kind:                api.KindConnected,
			State:               api.StateRunning,
			Status:              api.StatusOnline,
			Health:              api.HealthOK,
			HostKind:            api.HostKindWorkstation,
			DisplayName:         "Alice Dev Workstation",
			Purpose:             "Connected workstation daemon for ui-registry and control-plane development over outbound tunnel.",
			Owner:               "alice@example.com",
			OwnerGroup:          "group:platform-eng@example.com",
			ConnectedPrincipal:  "alice@example.com",
			Listed:              true,
			Tags:                []string{"workstation", "frontend", "registry"},
			GrainVersion:        "v0.14.2",
			GrainCommit:         "9f3c1e8b4a12",
			ActiveTasks:         1,
			AwaitingReplyTasks:  0,
			QueueDepth:          0,
			ActiveSessions:      1,
			TunnelConnected:     boolPtr(true),
			ConnectedSince:      timePtr(now.Add(-6 * time.Hour)),
			LastHeartbeatAt:     timePtr(now.Add(-8 * time.Second)),
			LastSeenAt:          timePtr(now.Add(-8 * time.Second)),
			HeartbeatAgeSeconds: intPtr(8),
			Scopes: []api.Scope{
				{
					Repo:          "acme/platform",
					PathPrefix:    "ui-registry",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceDeclared,
					Branch:        "feat/registry-ui",
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-12 * time.Hour)),
					UpdatedAt:     now.Add(-8 * time.Second),
				},
				{
					Repo:          "acme/platform",
					PathPrefix:    "pkg/registry",
					Role:          api.RoleSecondary,
					EffectiveRole: api.RoleSecondary,
					Source:        api.SourceReported,
					Branch:        "feat/registry-ui",
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-12 * time.Hour)),
					UpdatedAt:     now.Add(-8 * time.Second),
				},
				{
					Repo:           "acme/infra",
					PathPrefix:     "clusters/prod",
					Role:           api.RolePrimary,
					EffectiveRole:  api.RolePrimary,
					Source:         api.SourceReported,
					Branch:         "main",
					Active:         false,
					InactiveReason: api.InactivePrimaryConflict,
					ConflictWith:   "platform-vm",
					ClaimedAt:      timePtr(now.Add(-2 * time.Hour)),
					UpdatedAt:      now.Add(-8 * time.Second),
				},
			},
			ReportedScopes: []api.Scope{
				{
					Repo:           "acme/platform",
					PathPrefix:     "",
					Role:           api.RolePrimary,
					EffectiveRole:  api.RolePrimary,
					Source:         api.SourceReported,
					Branch:         "feat/registry-ui",
					Active:         false,
					InactiveReason: api.InactiveOverriddenByNarrower,
					ClaimedAt:      timePtr(now.Add(-12 * time.Hour)),
					UpdatedAt:      now.Add(-8 * time.Second),
				},
				{
					Repo:          "acme/platform",
					PathPrefix:    "pkg/registry",
					Role:          api.RoleSecondary,
					EffectiveRole: api.RoleSecondary,
					Source:        api.SourceReported,
					Branch:        "feat/registry-ui",
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-12 * time.Hour)),
					UpdatedAt:     now.Add(-8 * time.Second),
				},
				{
					Repo:           "acme/infra",
					PathPrefix:     "clusters/prod",
					Role:           api.RolePrimary,
					EffectiveRole:  api.RolePrimary,
					Source:         api.SourceReported,
					Branch:         "main",
					Active:         false,
					InactiveReason: api.InactivePrimaryConflict,
					ConflictWith:   "platform-vm",
					ClaimedAt:      timePtr(now.Add(-2 * time.Hour)),
					UpdatedAt:      now.Add(-8 * time.Second),
				},
			},
			DeclaredScopes: []api.Scope{
				{
					Repo:          "acme/platform",
					PathPrefix:    "ui-registry",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceDeclared,
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-6 * time.Hour)),
					UpdatedAt:     now.Add(-6 * time.Hour),
				},
			},
			ScopeConflicts: []api.ScopeConflict{
				{
					Repo:                  "acme/infra",
					PathPrefix:            "clusters/prod",
					ConflictingEntry:      "platform-vm",
					ConflictingOwner:      "alice@example.com",
					ConflictingOwnerGroup: "group:platform-eng@example.com",
					ConflictingPrefix:     "clusters/prod",
					ConflictingListed:     true,
				},
			},
			Credentials: []api.CredentialMeta{
				{
					CredentialID: "cred-alice-ws-01",
					Label:        "workstation-systemd",
					CreatedBy:    "alice@example.com",
					CreatedAt:    now.Add(-2 * time.Hour),
					LastUsedAt:   timePtr(now.Add(-8 * time.Second)),
				},
				{
					CredentialID: "cred-alice-ws-00",
					Label:        "initial-bootstrap",
					CreatedBy:    "alice@example.com",
					CreatedAt:    now.Add(-7 * 24 * time.Hour),
					LastUsedAt:   timePtr(now.Add(-2 * time.Hour)),
					RevokedAt:    timePtr(now.Add(-2 * time.Hour)),
				},
			},
			CreatedAt:  now.Add(-7 * 24 * time.Hour),
			UpdatedAt:  now.Add(-8 * time.Second),
			Generation: 6,
		},
		{
			Name:                "payments-staging",
			URL:                 "https://payments-staging.grain.example.com",
			Kind:                api.KindInstance,
			State:               api.StateReplacing,
			ReplaceStep:         api.ReplaceStepCreatingVM,
			Status:              api.StatusOffline,
			StatusDetail:        "Replacing VM (creating_vm): upgrading to v0.14.2 on shape large",
			HostKind:            api.HostKindRegistryVM,
			DisplayName:         "Payments Staging VM",
			Purpose:             "Hosted grain VM for acme/payments integration verification and ledger migration runs.",
			Owner:               "alice@example.com",
			OwnerGroup:          "group:payments-eng@example.com",
			Listed:              true,
			Tags:                []string{"payments", "staging"},
			Shape:               "large",
			Zone:                "us-central1-b",
			MachineType:         "e2-standard-8",
			BootDiskGB:          200,
			AllowSSH:            true,
			ShipLogs:            true,
			GrainVersion:        "v0.14.1",
			GrainCommit:         "7b2d4a910c3e",
			TargetVersion:       "v0.14.2",
			LastHeartbeatAt:     timePtr(now.Add(-3 * time.Minute)),
			LastSeenAt:          timePtr(now.Add(-3 * time.Minute)),
			HeartbeatAgeSeconds: intPtr(180),
			Scopes: []api.Scope{
				{
					Repo:          "acme/payments",
					PathPrefix:    "",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceDeclared,
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-10 * 24 * time.Hour)),
					UpdatedAt:     now.Add(-25 * time.Minute),
				},
			},
			DeclaredScopes: []api.Scope{
				{
					Repo:          "acme/payments",
					PathPrefix:    "",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceDeclared,
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-10 * 24 * time.Hour)),
					UpdatedAt:     now.Add(-25 * time.Minute),
				},
			},
			CreatedAt:  now.Add(-10 * 24 * time.Hour),
			UpdatedAt:  now.Add(-25 * time.Minute),
			Generation: 3,
		},
		{
			Name:                "ml-trainer-k8s",
			URL:                 "https://ml-trainer-k8s.grain.example.com",
			Kind:                api.KindConnected,
			State:               api.StateRunning,
			Status:              api.StatusDegraded,
			StatusDetail:        "GPU worker pool queue latency 4.2s (> 2.0s threshold)",
			Health:              api.HealthDegraded,
			HealthDetail:        "GPU worker pool queue latency 4.2s (> 2.0s threshold)",
			HostKind:            api.HostKindKubernetes,
			DisplayName:         "ML Trainer Pod (GKE)",
			Purpose:             "Kubernetes-hosted grain daemon for acme/models evaluation and checkpoint validation.",
			Owner:               "bob@example.com",
			OwnerGroup:          "group:ml-infra@example.com",
			PendingOwner:        "alice@example.com",
			ConnectedPrincipal:  "ml-trainer-sa@acme-prod.iam.gserviceaccount.com",
			Listed:              true,
			Tags:                []string{"ml", "kubernetes", "gpu"},
			GrainVersion:        "v0.14.0",
			GrainCommit:         "4e11a809c2b1",
			ActiveTasks:         4,
			AwaitingReplyTasks:  2,
			QueueDepth:          3,
			ActiveSessions:      3,
			TunnelConnected:     boolPtr(true),
			ConnectedSince:      timePtr(now.Add(-18 * time.Hour)),
			LastHeartbeatAt:     timePtr(now.Add(-52 * time.Second)),
			LastSeenAt:          timePtr(now.Add(-52 * time.Second)),
			HeartbeatAgeSeconds: intPtr(52),
			Scopes: []api.Scope{
				{
					Repo:          "acme/models",
					PathPrefix:    "training",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceReported,
					Branch:        "main",
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-5 * 24 * time.Hour)),
					UpdatedAt:     now.Add(-52 * time.Second),
				},
			},
			ReportedScopes: []api.Scope{
				{
					Repo:          "acme/models",
					PathPrefix:    "training",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceReported,
					Branch:        "main",
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-5 * 24 * time.Hour)),
					UpdatedAt:     now.Add(-52 * time.Second),
				},
			},
			Credentials: []api.CredentialMeta{
				{
					CredentialID: "cred-ml-k8s-01",
					Label:        "gke-workload-secret",
					CreatedBy:    "bob@example.com",
					CreatedAt:    now.Add(-5 * 24 * time.Hour),
					LastUsedAt:   timePtr(now.Add(-52 * time.Second)),
				},
			},
			CreatedAt:  now.Add(-5 * 24 * time.Hour),
			UpdatedAt:  now.Add(-12 * time.Minute),
			Generation: 5,
		},
		{
			Name:                "sec-scanner-ci",
			URL:                 "https://sec-scanner-ci.grain.example.com",
			Kind:                api.KindConnected,
			State:               api.StateRunning,
			Status:              api.StatusOffline,
			StatusDetail:        "tunnel disconnected 2h ago",
			HostKind:            api.HostKindOther,
			DisplayName:         "Security Scanner Runner",
			Purpose:             "Dedicated security audit daemon for supply-chain and policy verification.",
			Owner:               "carol@example.com",
			OwnerGroup:          "group:security@example.com",
			Listed:              true,
			Tags:                []string{"security", "audit"},
			GrainVersion:        "v0.13.9",
			GrainCommit:         "1a8c09f2d3b4",
			TunnelConnected:     boolPtr(false),
			LastHeartbeatAt:     timePtr(now.Add(-2 * time.Hour)),
			LastSeenAt:          timePtr(now.Add(-2 * time.Hour)),
			HeartbeatAgeSeconds: intPtr(7200),
			Scopes: []api.Scope{
				{
					Repo:          "acme/security",
					PathPrefix:    "",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceDeclared,
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-20 * 24 * time.Hour)),
					UpdatedAt:     now.Add(-2 * time.Hour),
				},
			},
			DeclaredScopes: []api.Scope{
				{
					Repo:          "acme/security",
					PathPrefix:    "",
					Role:          api.RolePrimary,
					EffectiveRole: api.RolePrimary,
					Source:        api.SourceDeclared,
					Active:        true,
					ClaimedAt:     timePtr(now.Add(-20 * 24 * time.Hour)),
					UpdatedAt:     now.Add(-2 * time.Hour),
				},
			},
			CreatedAt:  now.Add(-20 * 24 * time.Hour),
			UpdatedAt:  now.Add(-2 * time.Hour),
			Generation: 2,
		},
		{
			Name:          "alice-sandbox-old",
			URL:           "https://alice-sandbox-old.grain.example.com",
			Kind:          api.KindInstance,
			State:         api.StateDeleted,
			Status:        api.StatusOffline,
			StatusDetail:  "Soft-deleted; name reserved for 28d",
			HostKind:      api.HostKindRegistryVM,
			DisplayName:   "Alice Legacy Sandbox",
			Purpose:       "Previous experimental VM held in 30-day reservation window.",
			Owner:         "alice@example.com",
			Listed:        false,
			Tags:          []string{"experimental"},
			Shape:         "small",
			Zone:          "us-central1-a",
			MachineType:   "e2-standard-2",
			BootDiskGB:    50,
			GrainVersion:  "v0.13.8",
			CreatedAt:     now.Add(-40 * 24 * time.Hour),
			UpdatedAt:     now.Add(-48 * time.Hour),
			DeletedAt:     timePtr(now.Add(-48 * time.Hour)),
			ReservedUntil: timePtr(now.Add(28 * 24 * time.Hour)),
			PurgeAfter:    timePtr(now.Add(28 * 24 * time.Hour)),
			Generation:    2,
		},
	}

	for _, e := range seedEntries {
		d.entries[e.Name] = e
		d.order = append(d.order, e.Name)
	}
	return d
}

func (d *demoState) handler() http.Handler {
	mux := http.NewServeMux()
	staticHandler := server.NewStaticHandler()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "ok"})
	})
	mux.HandleFunc("GET /api/me", d.handleMe)
	mux.HandleFunc("GET /api/shapes", d.handleShapes)
	mux.HandleFunc("GET /api/status", d.handleStatus)
	mux.HandleFunc("GET /api/admin/summary", d.handleAdminSummary)
	mux.HandleFunc("GET /api/directory", d.handleDirectory)
	mux.HandleFunc("GET /api/route", d.handleRoute)
	mux.HandleFunc("GET /api/entries/{name}/availability", d.handleAvailability)
	mux.HandleFunc("GET /api/names/{name}", d.handleAvailability)
	mux.HandleFunc("GET /api/entries", d.handleListEntries)
	mux.HandleFunc("POST /api/entries", d.handleCreateEntry)
	mux.HandleFunc("GET /api/entries/{name}", d.handleGetEntry)
	mux.HandleFunc("PATCH /api/entries/{name}", d.handleUpdateEntry)
	mux.HandleFunc("PUT /api/entries/{name}/scopes", d.handleSetScopes)
	mux.HandleFunc("POST /api/entries/{name}/credentials/rotate", d.handleRotateCredential)
	mux.HandleFunc("DELETE /api/entries/{name}/credentials/{id}", d.handleRevokeCredential)
	mux.HandleFunc("POST /api/entries/{name}/upgrade", d.handleUpgradeEntry)
	mux.HandleFunc("POST /api/entries/{name}/replace", d.handleReplaceEntry)
	mux.HandleFunc("POST /api/entries/{name}/restore", d.handleRestoreEntry)
	mux.HandleFunc("DELETE /api/entries/{name}", d.handleDeleteEntry)
	mux.HandleFunc("POST /api/entries/{name}/transfer", d.handleOfferTransfer)
	mux.HandleFunc("POST /api/entries/{name}/transfer/accept", d.handleAcceptTransfer)
	mux.Handle("/", staticHandler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", strictCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (d *demoState) handleMe(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	usedUnits := 0
	usedConn := 0
	for _, name := range d.order {
		e := d.entries[name]
		if e.DeletedAt != nil || !strings.EqualFold(e.Owner, d.callerEmail) {
			continue
		}
		if e.Kind == api.KindInstance {
			for _, sh := range d.shapes {
				if sh.Name == e.Shape {
					usedUnits += sh.QuotaUnits
				}
			}
		} else if e.Kind == api.KindConnected {
			usedConn++
		}
	}
	writeJSON(w, http.StatusOK, api.MeResponse{
		Email:           d.callerEmail,
		Kind:            "human",
		IsRegistryAdmin: true,
		CanCreate:       true,
		Groups:          []string{"group:platform-eng@example.com", "group:payments-eng@example.com"},
		InstanceDomain:  d.instanceDomain,
		RegistryHost:    d.registryHost,
		Quota: api.QuotaUsage{
			QuotaUnitsUsed:  usedUnits,
			QuotaUnitsLimit: 10,
			ConnectedUsed:   usedConn,
			ConnectedLimit:  20,
		},
	})
}

func (d *demoState) handleShapes(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	writeJSON(w, http.StatusOK, api.ShapesResponse{
		DefaultShape: "medium",
		Shapes:       d.shapes,
	})
}

func (d *demoState) handleStatus(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	resp := api.StatusResponse{
		OK:             true,
		Status:         "ok",
		Host:           d.registryHost,
		InstanceDomain: d.instanceDomain,
		Release:        "v0.14.2",
		Revision:       "9f3c1e8b4a12",
		DefaultShape:   "medium",
		ActiveTunnels:  len(d.tunnels),
		Replicas:       d.replicas,
		VersionCounts:  map[string]int{},
		Shapes:         d.shapes,
	}
	for _, name := range d.order {
		e := d.entries[name]
		if e.DeletedAt != nil {
			continue
		}
		resp.TotalEntries++
		switch e.Status {
		case api.StatusOnline:
			resp.OnlineEntries++
		case api.StatusDegraded:
			resp.DegradedEntries++
		default:
			resp.OfflineEntries++
		}
		if e.Kind == api.KindInstance {
			resp.InstanceEntries++
		} else {
			resp.ConnectedCount++
		}
		ver := e.GrainVersion
		if ver == "" {
			ver = "unreported"
		}
		resp.VersionCounts[ver]++
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d *demoState) handleAdminSummary(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	writeJSON(w, http.StatusOK, api.AdminSummaryResponse{
		Replicas:       d.replicas,
		TunnelSessions: d.tunnels,
		AuditLog:       d.auditLog,
	})
}

func (d *demoState) handleDirectory(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	qv := r.URL.Query()
	q := strings.ToLower(strings.TrimSpace(qv.Get("q")))
	repo := strings.ToLower(strings.TrimSpace(qv.Get("repo")))
	tag := strings.ToLower(strings.TrimSpace(qv.Get("tag")))
	status := strings.ToLower(strings.TrimSpace(qv.Get("status")))
	hostKind := strings.ToLower(strings.TrimSpace(qv.Get("host_kind")))

	var out []api.DirectoryEntry
	for _, name := range d.order {
		e := d.entries[name]
		if e.DeletedAt != nil || !e.Listed {
			continue
		}
		if status != "" && !strings.EqualFold(string(e.Status), status) {
			continue
		}
		if hostKind != "" && !strings.EqualFold(string(e.HostKind), hostKind) {
			continue
		}
		if tag != "" {
			foundTag := false
			for _, t := range e.Tags {
				if strings.EqualFold(t, tag) {
					foundTag = true
					break
				}
			}
			if !foundTag {
				continue
			}
		}
		if repo != "" {
			foundRepo := false
			for _, sc := range e.Scopes {
				if sc.Active && strings.Contains(strings.ToLower(sc.Repo), repo) {
					foundRepo = true
					break
				}
			}
			if !foundRepo {
				continue
			}
		}
		if q != "" {
			hay := strings.ToLower(e.Name + " " + e.DisplayName + " " + e.Purpose + " " + e.Owner + " " + e.OwnerGroup + " " + strings.Join(e.Tags, " "))
			for _, sc := range e.Scopes {
				hay += " " + strings.ToLower(sc.Repo+" "+sc.PathPrefix)
			}
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, api.DirectoryEntry{
			Name:                e.Name,
			URL:                 e.URL,
			Kind:                e.Kind,
			State:               e.State,
			Status:              e.Status,
			HeartbeatAgeSeconds: e.HeartbeatAgeSeconds,
			DisplayName:         e.DisplayName,
			Purpose:             e.Purpose,
			Owner:               e.Owner,
			OwnerGroup:          e.OwnerGroup,
			Tags:                e.Tags,
			GrainVersion:        e.GrainVersion,
			GrainCommit:         e.GrainCommit,
			HostKind:            e.HostKind,
			Scopes:              e.Scopes,
			ActiveTasks:         e.ActiveTasks,
			AwaitingReplyTasks:  e.AwaitingReplyTasks,
			QueueDepth:          e.QueueDepth,
			UpdatedAt:           e.UpdatedAt,
		})
	}
	if out == nil {
		out = []api.DirectoryEntry{}
	}
	writeJSON(w, http.StatusOK, api.DirectoryResponse{Entries: out})
}

func (d *demoState) handleRoute(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	qv := r.URL.Query()
	repo := strings.TrimSpace(qv.Get("repo"))
	filePath := strings.Trim(strings.TrimSpace(qv.Get("path")), "/")
	includeOffline := strings.EqualFold(qv.Get("include_offline"), "true") || qv.Get("include_offline") == "1"

	var candidates []api.RouteCandidate
	for _, name := range d.order {
		e := d.entries[name]
		if e.DeletedAt != nil || !e.Listed {
			continue
		}
		if !includeOffline && e.Status == api.StatusOffline {
			continue
		}
		bestSpec := -1
		var bestScope *api.Scope
		for i := range e.Scopes {
			sc := &e.Scopes[i]
			if !sc.Active || !strings.EqualFold(sc.Repo, repo) {
				continue
			}
			if ok, spec := api.PathMatchesPrefix(filePath, sc.PathPrefix); ok && spec > bestSpec {
				bestSpec = spec
				bestScope = sc
			}
		}
		if bestScope != nil {
			role := bestScope.EffectiveRole
			if role == "" {
				role = bestScope.Role
			}
			candidates = append(candidates, api.RouteCandidate{
				Name:                e.Name,
				URL:                 e.URL,
				Kind:                e.Kind,
				State:               e.State,
				Status:              e.Status,
				HeartbeatAgeSeconds: e.HeartbeatAgeSeconds,
				DisplayName:         e.DisplayName,
				Purpose:             e.Purpose,
				Owner:               e.Owner,
				OwnerGroup:          e.OwnerGroup,
				ActiveTasks:         e.ActiveTasks,
				AwaitingReplyTasks:  e.AwaitingReplyTasks,
				MatchedScope: api.MatchedScope{
					Repo:        bestScope.Repo,
					PathPrefix:  bestScope.PathPrefix,
					Role:        role,
					Source:      bestScope.Source,
					Specificity: bestSpec,
				},
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		ip := candidates[i].MatchedScope.Role == api.RolePrimary
		jp := candidates[j].MatchedScope.Role == api.RolePrimary
		if ip != jp {
			return ip
		}
		return candidates[i].MatchedScope.Specificity > candidates[j].MatchedScope.Specificity
	})
	if candidates == nil {
		candidates = []api.RouteCandidate{}
	}
	var primary *api.RouteCandidate
	if len(candidates) > 0 {
		c := candidates[0]
		primary = &c
	}
	writeJSON(w, http.StatusOK, api.RouteResponse{
		Repo:       repo,
		Path:       filePath,
		Primary:    primary,
		Candidates: candidates,
	})
}

func (d *demoState) handleAvailability(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if err := api.ValidateEntryName(name); err != nil {
		if api.IsReservedEntryName(name) {
			writeJSON(w, http.StatusOK, api.NameAvailabilityResponse{
				Name:   name,
				Status: api.NameReserved,
				Reason: err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, api.NameAvailabilityResponse{
			Name:   name,
			Status: api.NameInvalid,
			Reason: err.Error(),
		})
		return
	}
	if existing, ok := d.entries[name]; ok {
		if existing.DeletedAt != nil {
			writeJSON(w, http.StatusOK, api.NameAvailabilityResponse{
				Name:          name,
				Status:        api.NameReserved,
				Reason:        "entry was recently deleted and is in its 30-day reservation window",
				ReservedUntil: existing.ReservedUntil,
			})
			return
		}
		writeJSON(w, http.StatusOK, api.NameAvailabilityResponse{
			Name:   name,
			Status: api.NameTaken,
			Reason: "entry name is already in use",
		})
		return
	}
	writeJSON(w, http.StatusOK, api.NameAvailabilityResponse{
		Name:   name,
		Status: api.NameFree,
	})
}

func (d *demoState) handleListEntries(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	qv := r.URL.Query()
	includeDeleted := strings.EqualFold(qv.Get("include_deleted"), "true") || qv.Get("include_deleted") == "1"
	mineOnly := strings.EqualFold(qv.Get("mine"), "true") || qv.Get("mine") == "1"
	var out []api.Entry
	for _, name := range d.order {
		e := d.entries[name]
		if e.DeletedAt != nil && !includeDeleted {
			continue
		}
		if mineOnly {
			if !strings.EqualFold(e.Owner, d.callerEmail) &&
				!strings.EqualFold(e.PendingOwner, d.callerEmail) &&
				e.OwnerGroup != "group:platform-eng@example.com" &&
				e.OwnerGroup != "group:payments-eng@example.com" {
				continue
			}
		}
		out = append(out, *e)
	}
	if out == nil {
		out = []api.Entry{}
	}
	writeJSON(w, http.StatusOK, api.ListEntriesResponse{Entries: out})
}

func (d *demoState) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var req api.CreateEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON")
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if err := api.ValidateEntryName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	if _, exists := d.entries[name]; exists {
		writeJSONError(w, http.StatusConflict, "conflict", fmt.Sprintf("entry %q already exists", name))
		return
	}
	listed := false
	if req.Listed != nil {
		listed = *req.Listed
	}
	state := api.StateRunning
	status := api.StatusOnline
	hostKind := req.HostKind
	if req.Kind == api.KindInstance {
		state = api.StateCreating
		status = api.StatusOffline
		hostKind = api.HostKindRegistryVM
		if req.Shape == "" {
			req.Shape = "medium"
		}
	} else if hostKind == "" {
		hostKind = api.HostKindWorkstation
	}
	var scopes []api.Scope
	for _, sc := range req.Scopes {
		role := sc.Role
		if role == "" {
			role = api.RolePrimary
		}
		scopes = append(scopes, api.Scope{
			Repo:          sc.Repo,
			PathPrefix:    strings.Trim(sc.PathPrefix, "/"),
			Role:          role,
			EffectiveRole: role,
			Source:        api.SourceDeclared,
			Active:        true,
			UpdatedAt:     d.now,
		})
	}
	entry := &api.Entry{
		Name:               name,
		URL:                fmt.Sprintf("https://%s.%s", name, d.instanceDomain),
		Kind:               req.Kind,
		State:              state,
		Status:             status,
		HostKind:           hostKind,
		DisplayName:        req.DisplayName,
		Purpose:            req.Purpose,
		Owner:              d.callerEmail,
		OwnerGroup:         req.OwnerGroup,
		ConnectedPrincipal: req.ConnectedPrincipal,
		Listed:             listed,
		Tags:               req.Tags,
		Shape:              req.Shape,
		AllowSSH:           true,
		Scopes:             scopes,
		DeclaredScopes:     scopes,
		CreatedAt:          d.now,
		UpdatedAt:          d.now,
		Generation:         1,
	}
	var tok string
	var cred *api.CredentialMeta
	if req.Kind == api.KindConnected {
		tc := false
		entry.TunnelConnected = &tc
		tok = "grc_demo_" + strings.ReplaceAll(name, "-", "_") + "_9f8e7d6c5b4a3210"
		c := api.CredentialMeta{
			CredentialID: "cred-" + name + "-01",
			Label:        "default",
			CreatedBy:    d.callerEmail,
			CreatedAt:    d.now,
		}
		cred = &c
		entry.Credentials = []api.CredentialMeta{c}
	}
	d.entries[name] = entry
	d.order = append(d.order, name)
	writeJSON(w, http.StatusCreated, api.CreateEntryResponse{
		Entry:             *entry,
		RegistrationToken: tok,
		Credential:        cred,
	})
}

func (d *demoState) handleGetEntry(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	writeJSON(w, http.StatusOK, *e)
}

func (d *demoState) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	var req api.UpdateEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON")
		return
	}
	if req.DisplayName != nil {
		e.DisplayName = *req.DisplayName
	}
	if req.Purpose != nil {
		e.Purpose = *req.Purpose
	}
	if req.HostKind != nil {
		e.HostKind = *req.HostKind
	}
	if req.ConnectedPrincipal != nil {
		e.ConnectedPrincipal = *req.ConnectedPrincipal
	}
	if req.OwnerGroup != nil {
		e.OwnerGroup = *req.OwnerGroup
	}
	if req.Listed != nil {
		e.Listed = *req.Listed
	}
	if req.Tags != nil {
		e.Tags = *req.Tags
	}
	if req.Shape != nil && *req.Shape != e.Shape {
		e.Shape = *req.Shape
		e.State = api.StateReplacing
		e.ReplaceStep = api.ReplaceStepDraining
	}
	if req.AllowSSH != nil {
		e.AllowSSH = *req.AllowSSH
	}
	if req.ShipLogs != nil {
		e.ShipLogs = *req.ShipLogs
	}
	e.Generation++
	e.UpdatedAt = d.now
	writeJSON(w, http.StatusOK, *e)
}

func (d *demoState) handleSetScopes(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	var req api.SetScopesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON")
		return
	}
	var declared []api.Scope
	for _, sc := range req.Scopes {
		role := sc.Role
		if role == "" {
			role = api.RolePrimary
		}
		declared = append(declared, api.Scope{
			Repo:          sc.Repo,
			PathPrefix:    strings.Trim(sc.PathPrefix, "/"),
			Role:          role,
			EffectiveRole: role,
			Source:        api.SourceDeclared,
			Active:        true,
			UpdatedAt:     d.now,
		})
	}
	e.DeclaredScopes = declared
	e.Scopes = append([]api.Scope{}, declared...)
	for _, rep := range e.ReportedScopes {
		e.Scopes = append(e.Scopes, rep)
	}
	writeJSON(w, http.StatusOK, api.SetScopesResponse{
		Scopes:         e.Scopes,
		ReportedScopes: e.ReportedScopes,
		DeclaredScopes: e.DeclaredScopes,
		Conflicts:      e.ScopeConflicts,
	})
}

func (d *demoState) handleRotateCredential(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	var req api.RotateCredentialRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "rotated"
	}
	if req.RevokePrevious {
		for i := range e.Credentials {
			if e.Credentials[i].RevokedAt == nil {
				t := d.now
				e.Credentials[i].RevokedAt = &t
			}
		}
	}
	meta := api.CredentialMeta{
		CredentialID: fmt.Sprintf("cred-%s-%02d", name, len(e.Credentials)+1),
		Label:        label,
		CreatedBy:    d.callerEmail,
		CreatedAt:    d.now,
	}
	e.Credentials = append([]api.CredentialMeta{meta}, e.Credentials...)
	writeJSON(w, http.StatusOK, api.RotateCredentialResponse{
		RegistrationToken: "grc_rotated_" + strings.ReplaceAll(name, "-", "_") + "_a1b2c3d4e5f60718",
		Credential:        meta,
	})
}

func (d *demoState) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	credID := strings.TrimSpace(r.PathValue("id"))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	for i := range e.Credentials {
		if e.Credentials[i].CredentialID == credID {
			t := d.now
			e.Credentials[i].RevokedAt = &t
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "cred_id": credID})
}

func (d *demoState) handleUpgradeEntry(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	var req api.UpgradeEntryRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.TargetVersion != "" {
		e.TargetVersion = req.TargetVersion
	} else {
		e.TargetVersion = "v0.14.2"
	}
	writeJSON(w, http.StatusAccepted, *e)
}

func (d *demoState) handleReplaceEntry(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	var req api.ReplaceEntryRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Shape != "" {
		e.Shape = req.Shape
	}
	if req.TargetVersion != "" {
		e.TargetVersion = req.TargetVersion
	}
	e.State = api.StateReplacing
	e.ReplaceStep = api.ReplaceStepDraining
	writeJSON(w, http.StatusAccepted, *e)
}

func (d *demoState) handleRestoreEntry(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	e.State = api.StateRunning
	e.Status = api.StatusOnline
	e.DeletedAt = nil
	e.ReservedUntil = nil
	e.PurgeAfter = nil
	writeJSON(w, http.StatusOK, *e)
}

func (d *demoState) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	t := d.now
	resUntil := d.now.Add(30 * 24 * time.Hour)
	e.State = api.StateDeleted
	e.Status = api.StatusOffline
	e.DeletedAt = &t
	e.ReservedUntil = &resUntil
	e.PurgeAfter = &resUntil
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "name": name})
}

func (d *demoState) handleOfferTransfer(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	var req api.TransferRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	to := strings.TrimSpace(req.To)
	if to == "" {
		to = strings.TrimSpace(req.NewOwner)
	}
	e.PendingOwner = to
	if req.OwnerGroup != "" {
		e.OwnerGroup = req.OwnerGroup
	}
	writeJSON(w, http.StatusOK, *e)
}

func (d *demoState) handleAcceptTransfer(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	e, ok := d.entries[name]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	e.Owner = d.callerEmail
	e.PendingOwner = ""
	writeJSON(w, http.StatusOK, *e)
}
