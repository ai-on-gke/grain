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

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
)

const (
	LeaseName                  = "reconciler"
	DefaultLeaseTTL            = 15 * time.Second
	DefaultLeaseRenewInterval  = 5 * time.Second
	DefaultReconcileInterval   = 5 * time.Second
	DefaultStatusPollInterval  = 15 * time.Second
	DefaultVMCheckInterval     = 60 * time.Second
	DefaultOrphanSweepInterval = 5 * time.Minute
)

// HostCacheUpdater is an optional callback notified when an entry's backend placement
// or lifecycle state changes so the data-plane host cache stays immediately warm.
type HostCacheUpdater interface {
	Put(e *store.EntryRow)
	Invalidate(name string)
}

// OrphanInstance describes an unmatched GCE VM discovered during an orphan sweep.
type OrphanInstance struct {
	InstanceID string            `json:"instance_id"`
	Zone       string            `json:"zone"`
	State      string            `json:"state"`
	InternalIP string            `json:"internal_ip,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	DetectedAt time.Time         `json:"detected_at"`
}

// Options configures a `Reconciler`.
type Options struct {
	Config             registry.Config
	Store              *store.Store
	Compute            registry.Compute
	Resources          registry.InstanceResources
	ConfigRepos        registry.ConfigRepos
	Signer             registry.Signer
	HostCache          HostCacheUpdater
	HolderID           string
	HTTPClient         *http.Client
	LeaseTTL           time.Duration
	LeaseRenewInterval time.Duration
	ReconcileInterval  time.Duration
	StatusPollInterval time.Duration
	DrainTimeout       time.Duration
	HealthTimeout      time.Duration
	StepByStepReplace  bool
	Now                func() time.Time
}

// Reconciler drives hosted VM lifecycle transitions (`creating`, `replacing`, `deleting`),
// delayed `purge_after` / `db_purge_after` cleanup, expired `replicas` cleanup, 15 s
// `/api/registry/status` polling into `entries` + `entry_scopes(source='reported')`,
// and orphan VM reporting/cleanup under the single-leader `registry.leases` lock.
type Reconciler struct {
	cfg                registry.Config
	store              *store.Store
	compute            registry.Compute
	resources          registry.InstanceResources
	configRepos        registry.ConfigRepos
	signer             registry.Signer
	hostCache          HostCacheUpdater
	holderID           string
	httpClient         *http.Client
	leaseTTL           time.Duration
	leaseRenewInterval time.Duration
	reconcileInterval  time.Duration
	statusPollInterval time.Duration
	drainTimeout       time.Duration
	healthTimeout      time.Duration
	stepByStepReplace  bool
	nowFn              func() time.Time

	isLeader atomic.Bool

	orphanMu        sync.RWMutex
	orphans         []OrphanInstance
	reportedOrphans map[string]bool
}

// New constructs a new `Reconciler`.
func New(opts Options) (*Reconciler, error) {
	if opts.Store == nil {
		return nil, errors.New("reconcile: Store is required")
	}
	holderID := strings.TrimSpace(opts.HolderID)
	if holderID == "" {
		holderID = "reconciler-default"
	}
	leaseTTL := opts.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = DefaultLeaseTTL
	}
	renewInterval := opts.LeaseRenewInterval
	if renewInterval <= 0 {
		renewInterval = DefaultLeaseRenewInterval
	}
	recInterval := opts.ReconcileInterval
	if recInterval <= 0 {
		recInterval = DefaultReconcileInterval
	}
	pollInterval := opts.StatusPollInterval
	if pollInterval <= 0 {
		pollInterval = opts.Config.Compute.StatusPollInterval
	}
	if pollInterval <= 0 {
		pollInterval = DefaultStatusPollInterval
	}
	drainTimeout := opts.DrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = opts.Config.Compute.DrainTimeout
	}
	if drainTimeout <= 0 {
		drainTimeout = registry.DefaultComputeDrainTimeout
	}
	healthTimeout := opts.HealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = opts.Config.Compute.HealthTimeout
	}
	if healthTimeout <= 0 {
		healthTimeout = registry.DefaultComputeHealthTimeout
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	return &Reconciler{
		cfg:                opts.Config,
		store:              opts.Store,
		compute:            opts.Compute,
		resources:          opts.Resources,
		configRepos:        opts.ConfigRepos,
		signer:             opts.Signer,
		hostCache:          opts.HostCache,
		holderID:           holderID,
		httpClient:         hc,
		leaseTTL:           leaseTTL,
		leaseRenewInterval: renewInterval,
		reconcileInterval:  recInterval,
		statusPollInterval: pollInterval,
		drainTimeout:       drainTimeout,
		healthTimeout:      healthTimeout,
		stepByStepReplace:  opts.StepByStepReplace,
		nowFn:              nowFn,
		reportedOrphans:    make(map[string]bool),
	}, nil
}

// SetNow overrides the clock used by the reconciler (for deterministic tests).
func (r *Reconciler) SetNow(fn func() time.Time) {
	if fn != nil {
		r.nowFn = fn
	}
}

func (r *Reconciler) now() time.Time {
	if r.nowFn != nil {
		return r.nowFn().UTC()
	}
	return time.Now().UTC()
}

// IsLeader reports whether this reconciler replica currently holds the `"reconciler"` lease.
func (r *Reconciler) IsLeader() bool {
	return r.isLeader.Load()
}

// HolderID returns this reconciler's lease holder ID.
func (r *Reconciler) HolderID() string {
	return r.holderID
}

// Orphans returns a snapshot of unmatched GCE VMs discovered during the most recent orphan sweep.
func (r *Reconciler) Orphans() []OrphanInstance {
	r.orphanMu.RLock()
	defer r.orphanMu.RUnlock()
	out := make([]OrphanInstance, len(r.orphans))
	copy(out, r.orphans)
	return out
}

// Start launches the background leader-election, reconciliation, and status-polling loops
// until `ctx` is canceled.
func (r *Reconciler) Start(ctx context.Context) {
	go r.loop(ctx)
}

func (r *Reconciler) loop(ctx context.Context) {
	recTicker := time.NewTicker(r.reconcileInterval)
	defer recTicker.Stop()
	pollTicker := time.NewTicker(r.statusPollInterval)
	defer pollTicker.Stop()

	_ = r.RunOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-recTicker.C:
			_ = r.RunOnce(ctx)
		case <-pollTicker.C:
			if r.IsLeader() {
				_ = r.PollRunningInstances(ctx)
			}
		}
	}
}

// RunOnce attempts to acquire or renew the `"reconciler"` database lease.
// If this instance is not the lease holder, `RunOnce` returns `nil` immediately without
// performing any reconciliation or cloud API calls.
// When holding the lease, `RunOnce` executes one pass of:
// 1. Expired `replicas` cleanup (`DeleteExpiredReplicas`)
// 2. `creating` entries reconciliation
// 3. `replacing` entries reconciliation (`draining` -> `deleting_vm` -> `creating_vm` -> `probing` -> `running`)
// 4. `deleting` entries reconciliation (VM delete + DB/IAM fence -> `deleted`)
// 5. Delayed `purge_after` / `db_purge_after` cleanup and expired tombstone removal
// 6. 15 s `/api/registry/status` polling into `entries` + `entry_scopes(source='reported')` and VM status check
// 7. Orphan GCE VM reporting and stale-generation cleanup.
func (r *Reconciler) RunOnce(ctx context.Context) error {
	acquired, err := r.store.AcquireOrRenewLease(ctx, LeaseName, r.holderID, r.leaseTTL)
	if err != nil {
		r.isLeader.Store(false)
		return fmt.Errorf("reconcile: acquiring lease: %w", err)
	}
	r.isLeader.Store(acquired)
	if !acquired {
		return nil
	}

	var firstErr error
	recordErr := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}

	_, err = r.store.DeleteExpiredReplicas(ctx)
	recordErr(err)

	recordErr(r.reconcileEntries(ctx))
	recordErr(r.reconcilePurge(ctx))
	recordErr(r.PollRunningInstances(ctx))
	recordErr(r.reconcileFleetUpgrades(ctx))
	recordErr(r.ReconcileOrphans(ctx))

	return firstErr
}

func (r *Reconciler) reconcileEntries(ctx context.Context) error {
	entries, err := r.store.ListEntriesFiltered(ctx, true)
	if err != nil {
		return err
	}
	var firstErr error
	for _, entry := range entries {
		if entry.Kind != api.KindInstance {
			continue
		}
		var stepErr error
		switch entry.LifecycleState {
		case api.StateCreating, "PROVISIONING":
			if entry.DeletedAt == nil {
				stepErr = r.reconcileCreatingEntry(ctx, entry)
			}
		case api.StateRunning, "ACTIVE":
			if entry.DeletedAt == nil && strings.TrimSpace(entry.TargetVersion) != "" && entry.TargetVersion != entry.GrainVersion {
				// Upgrade requested via POST /api/entries/<name>/upgrade -> transition to replacing (draining).
				upgrading, uErr := r.store.RequestEntryReplace(ctx, entry.ID, entry.Version, entry.Shape, entry.TargetVersion)
				if uErr == nil && upgrading != nil {
					stepErr = r.reconcileReplacingEntry(ctx, upgrading)
				} else {
					stepErr = uErr
				}
			}
		case api.StateReplacing:
			if entry.DeletedAt == nil {
				stepErr = r.reconcileReplacingEntry(ctx, entry)
			}
		case api.StateStopping, "STOPPING":
			if entry.DeletedAt == nil {
				stepErr = r.reconcileStoppingEntry(ctx, entry)
			}
		case api.StateDeleting, "DEPROVISIONING":
			stepErr = r.reconcileDeletingEntry(ctx, entry)
		case api.StateDeleted, "DELETED":
			// If SoftDeleteEntryWithOptions was called directly on an instance that still has a live VM,
			// tear down the VM and fence its DB/IAM resources now.
			if entry.GCEInstanceID != "" || entry.BackendURL != "" {
				stepErr = r.reconcileDeletingEntry(ctx, entry)
			}
		}
		if stepErr != nil && firstErr == nil {
			firstErr = stepErr
		}
	}
	return firstErr
}

func (r *Reconciler) reconcileCreatingEntry(ctx context.Context, entry *store.EntryRow) error {
	if r.compute == nil {
		return nil
	}
	// Phase 1: Provision GSA, Cloud SQL DB/role, GCS Managed Folder, Secret Manager IAM, Config Repo, and GCE VM.
	if entry.GCEInstanceID == "" || entry.BackendURL == "" {
		bundle, configRepoURL, err := r.provisionEntryResources(ctx, entry)
		if err != nil {
			r.failEntry(ctx, entry, fmt.Sprintf("provisioning instance resources failed: %v", err))
			return err
		}

		if configRepoURL != "" && configRepoURL != entry.ConfigRepo {
			updated, uErr := r.store.UpdateConfigRepo(ctx, entry.ID, entry.Version, configRepoURL)
			if uErr != nil {
				if errors.Is(uErr, store.ErrConflict) {
					return nil
				}
				return uErr
			}
			entry = updated
		}

		handle, err := r.createVMForEntry(ctx, entry, bundle, configRepoURL)
		if err != nil {
			r.failEntry(ctx, entry, fmt.Sprintf("creating GCE instance failed: %v", err))
			return err
		}

		backendURL := strings.TrimSpace(handle.BackendURL)
		if backendURL == "" && handle.InternalIP != "" {
			backendURL = fmt.Sprintf("http://%s:8080", handle.InternalIP)
		}
		updated, err := r.store.UpdateInstancePlacement(
			ctx,
			entry.ID,
			entry.Version,
			handle.InstanceID,
			handle.InternalIP,
			backendURL,
			bundle.ServiceAccountEmail,
			bundle.SQLInstance,
			bundle.DatabaseName,
			bundle.SecretPrefix,
		)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		entry = updated
	}

	// Phase 2: Probe `<backend_url>/healthz` and `<backend_url>/api/registry/status`.
	statusResp, probeErr := r.probeInstanceHealthAndStatus(ctx, entry.Name, entry.BackendURL)
	if probeErr != nil {
		if r.now().Sub(entry.UpdatedAt) >= r.healthTimeout {
			r.failEntry(ctx, entry, fmt.Sprintf("instance health check timed out after %s: %v", r.healthTimeout, probeErr))
		}
		return nil
	}

	// Instance is healthy: record initial heartbeat + reported scopes, then transition `creating` -> `running`.
	_, _ = r.recordStatusHeartbeat(ctx, entry.ID, statusResp)
	fresh, err := r.store.GetEntryByID(ctx, entry.ID)
	if err != nil {
		return err
	}
	if fresh.LifecycleState != api.StateCreating && fresh.LifecycleState != "PROVISIONING" {
		return nil
	}
	running, err := r.store.TransitionEntryState(ctx, fresh.ID, fresh.Version, api.StateRunning, "")
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	if r.hostCache != nil && running != nil {
		r.hostCache.Put(running)
	}
	_ = r.store.RecordAudit(ctx, "system:reconciler", "entry_running", running.ID, running.Name, map[string]any{
		"gce_instance_id": running.GCEInstanceID,
		"backend_url":     running.BackendURL,
	})
	return nil
}

func (r *Reconciler) reconcileReplacingEntry(ctx context.Context, entry *store.EntryRow) error {
	if r.compute == nil {
		return nil
	}
	step := api.ReplaceStep(entry.ReplaceStep)
	if step == "" {
		step = api.ReplaceStepDraining
	}

	switch step {
	case api.ReplaceStepDraining:
		// If the old VM is reachable and still running active tasks within drain_timeout, wait before stopping.
		if entry.BackendURL != "" && r.now().Sub(entry.UpdatedAt) < r.drainTimeout {
			if stResp, err := r.fetchInstanceStatus(ctx, entry.Name, entry.BackendURL); err == nil && stResp != nil {
				_, _ = r.recordStatusHeartbeat(ctx, entry.ID, stResp)
				if stResp.ActiveTasks > 0 {
					return nil
				}
			}
		}
		zone, vmID := r.resolveZoneAndVMName(entry)
		if err := r.compute.StopInstance(ctx, zone, vmID); err != nil {
			r.failEntry(ctx, entry, fmt.Sprintf("stopping old GCE instance during replace failed: %v", err))
			return err
		}
		updated, err := r.store.UpdateReplaceStep(ctx, entry.ID, entry.Version, api.ReplaceStepDeletingVM, "")
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		entry = updated
		if r.stepByStepReplace {
			return nil
		}
		fallthrough

	case api.ReplaceStepDeletingVM:
		zone, vmID := r.resolveZoneAndVMName(entry)
		if err := r.compute.DeleteInstance(ctx, zone, vmID); err != nil {
			r.failEntry(ctx, entry, fmt.Sprintf("deleting old GCE instance during replace failed: %v", err))
			return err
		}
		updated, err := r.store.UpdateReplaceStep(ctx, entry.ID, entry.Version, api.ReplaceStepCreatingVM, "")
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		entry = updated
		if r.stepByStepReplace {
			return nil
		}
		fallthrough

	case api.ReplaceStepCreatingVM:
		bundle, configRepoURL, err := r.provisionEntryResources(ctx, entry)
		if err != nil {
			r.failEntry(ctx, entry, fmt.Sprintf("re-provisioning resources during replace failed: %v", err))
			return err
		}
		handle, err := r.createVMForEntry(ctx, entry, bundle, configRepoURL)
		if err != nil {
			r.failEntry(ctx, entry, fmt.Sprintf("creating replacement GCE instance failed: %v", err))
			return err
		}
		backendURL := strings.TrimSpace(handle.BackendURL)
		if backendURL == "" && handle.InternalIP != "" {
			backendURL = fmt.Sprintf("http://%s:8080", handle.InternalIP)
		}
		placed, err := r.store.UpdateInstancePlacement(
			ctx,
			entry.ID,
			entry.Version,
			handle.InstanceID,
			handle.InternalIP,
			backendURL,
			bundle.ServiceAccountEmail,
			bundle.SQLInstance,
			bundle.DatabaseName,
			bundle.SecretPrefix,
		)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		updated, err := r.store.UpdateReplaceStep(ctx, placed.ID, placed.Version, api.ReplaceStepProbing, "")
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		entry = updated
		if r.stepByStepReplace {
			return nil
		}
		fallthrough

	case api.ReplaceStepProbing:
		statusResp, probeErr := r.probeInstanceHealthAndStatus(ctx, entry.Name, entry.BackendURL)
		if probeErr != nil {
			if r.now().Sub(entry.UpdatedAt) >= r.healthTimeout {
				r.failEntry(ctx, entry, fmt.Sprintf("replacement VM health check timed out after %s: %v", r.healthTimeout, probeErr))
			}
			return nil
		}
		var newVer, newCommit string
		if statusResp != nil {
			newVer = statusResp.GrainVersion
			newCommit = statusResp.GrainCommit
		}
		if newVer == "" && entry.TargetVersion != "" {
			newVer = entry.TargetVersion
		}
		_, _ = r.recordStatusHeartbeat(ctx, entry.ID, statusResp)
		fresh, err := r.store.GetEntryByID(ctx, entry.ID)
		if err != nil {
			return err
		}
		completed, err := r.store.CompleteEntryReplace(
			ctx,
			fresh.ID,
			fresh.Version,
			fresh.GCEInstanceID,
			fresh.InternalIP,
			fresh.BackendURL,
			newVer,
			newCommit,
		)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		if r.hostCache != nil && completed != nil {
			r.hostCache.Put(completed)
		}
		_ = r.store.RecordAudit(ctx, "system:reconciler", "entry_replace_complete", completed.ID, completed.Name, map[string]any{
			"shape":           completed.Shape,
			"grain_version":   completed.GrainVersion,
			"gce_instance_id": completed.GCEInstanceID,
			"backend_url":     completed.BackendURL,
		})
	}
	return nil
}

func (r *Reconciler) reconcileStoppingEntry(ctx context.Context, entry *store.EntryRow) error {
	// Wait for active tasks to drain up to drainTimeout before halting the VM.
	if entry.BackendURL != "" && r.now().Sub(entry.UpdatedAt) < r.drainTimeout {
		if stResp, err := r.fetchInstanceStatus(ctx, entry.Name, entry.BackendURL); err == nil && stResp != nil {
			_, _ = r.recordStatusHeartbeat(ctx, entry.ID, stResp)
			if stResp.ActiveTasks > 0 {
				return nil
			}
		}
	}

	zone, vmID := r.resolveZoneAndVMName(entry)
	if r.compute != nil && vmID != "" && zone != "" {
		if err := r.compute.StopInstance(ctx, zone, vmID); err != nil {
			r.failEntry(ctx, entry, fmt.Sprintf("stopping GCE instance %s failed: %v", vmID, err))
			return err
		}
		if err := r.compute.DeleteInstance(ctx, zone, vmID); err != nil {
			r.failEntry(ctx, entry, fmt.Sprintf("deleting stopped GCE instance %s failed: %v", vmID, err))
			return err
		}
	}

	stopped, err := r.store.CompleteStopEntry(ctx, entry.ID, entry.Version)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	if r.hostCache != nil && stopped != nil {
		r.hostCache.Put(stopped)
	}
	_ = r.store.RecordAudit(ctx, "system:reconciler", "entry_stopped", stopped.ID, stopped.Name, map[string]any{
		"gce_instance_id": vmID,
		"zone":            zone,
	})
	return nil
}

func (r *Reconciler) reconcileFleetUpgrades(ctx context.Context) error {
	targetVer := strings.TrimSpace(r.cfg.Compute.TargetVersion)
	if targetVer == "" {
		return nil
	}
	entries, err := r.store.ListEntriesFiltered(ctx, false)
	if err != nil {
		return err
	}

	replacingCount := 0
	for _, e := range entries {
		if e.Kind == api.KindInstance && e.DeletedAt == nil && e.LifecycleState == api.StateReplacing {
			replacingCount++
		}
	}
	maxConcurrent := r.cfg.Compute.MaxConcurrentUpgrades
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	var firstErr error
	for _, entry := range entries {
		if entry.Kind != api.KindInstance || entry.DeletedAt != nil || entry.LifecycleState != api.StateRunning {
			continue
		}
		curVer := strings.TrimSpace(entry.GrainVersion)
		if curVer == "" {
			continue
		}
		if curVer == targetVer {
			if entry.UpgradeAvailable != "" {
				_, _ = r.store.SetEntryUpgradeAvailable(ctx, entry.ID, "")
			}
			continue
		}

		policy := api.AutoUpgradePolicy(strings.ToLower(strings.TrimSpace(entry.AutoUpgrade)))
		if policy == "" {
			policy = api.AutoUpgradePolicy(strings.ToLower(strings.TrimSpace(r.cfg.Compute.AutoUpgrade)))
		}
		if policy == "" {
			policy = api.AutoUpgradeOff
		}

		switch policy {
		case api.AutoUpgradeOff:
			continue

		case api.AutoUpgradeNotify:
			if !isNewerSemver(curVer, targetVer) {
				continue
			}
			if entry.UpgradeAvailable != targetVer {
				if _, err := r.store.SetEntryUpgradeAvailable(ctx, entry.ID, targetVer); err == nil {
					_ = r.store.RecordAudit(ctx, "system:reconciler", "entry_upgrade_available", entry.ID, entry.Name, map[string]any{
						"current_version": curVer,
						"target_version":  targetVer,
						"policy":          string(policy),
					})
				}
			}

		case api.AutoUpgradeMinor:
			if !isNewerSemver(curVer, targetVer) {
				continue
			}
			if !isMinorOrPatchSemverUpgrade(curVer, targetVer) {
				// Major version bump under `minor` policy: notify without auto-replacing.
				if entry.UpgradeAvailable != targetVer {
					if _, err := r.store.SetEntryUpgradeAvailable(ctx, entry.ID, targetVer); err == nil {
						_ = r.store.RecordAudit(ctx, "system:reconciler", "entry_upgrade_available", entry.ID, entry.Name, map[string]any{
							"current_version": curVer,
							"target_version":  targetVer,
							"policy":          string(policy),
							"skipped_reason":  "major_version_change",
						})
					}
				}
				continue
			}

			// Mark upgrade_available while waiting on maintenance window, idle state, or concurrency cap.
			if !registry.InMaintenanceWindow(r.cfg.Compute.MaintenanceWindow, r.now()) ||
				entry.ActiveTasks > 0 || entry.ActiveSessions > 0 ||
				replacingCount >= maxConcurrent {
				if entry.UpgradeAvailable != targetVer {
					_, _ = r.store.SetEntryUpgradeAvailable(ctx, entry.ID, targetVer)
				}
				continue
			}

			replacing, err := r.store.RequestEntryReplace(ctx, entry.ID, entry.Version, entry.Shape, targetVer)
			if err != nil {
				if !errors.Is(err, store.ErrConflict) && firstErr == nil {
					firstErr = err
				}
				continue
			}
			replacingCount++
			if r.hostCache != nil && replacing != nil {
				r.hostCache.Put(replacing)
			}
			_ = r.store.RecordAudit(ctx, "system:reconciler", "entry_auto_upgrade", replacing.ID, replacing.Name, map[string]any{
				"from_version":   curVer,
				"target_version": targetVer,
				"policy":         string(policy),
			})
			if stepErr := r.reconcileReplacingEntry(ctx, replacing); stepErr != nil && firstErr == nil {
				firstErr = stepErr
			}
		}
	}
	return firstErr
}

func parseSemver(v string) (major, minor, patch int, ok bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, false
	}
	maj, err := strconv.Atoi(parts[0])
	if err != nil || maj < 0 {
		return 0, 0, 0, false
	}
	minVer, err := strconv.Atoi(parts[1])
	if err != nil || minVer < 0 {
		return 0, 0, 0, false
	}
	pat := 0
	if len(parts) == 3 {
		pat, err = strconv.Atoi(parts[2])
		if err != nil || pat < 0 {
			return 0, 0, 0, false
		}
	}
	return maj, minVer, pat, true
}

func isNewerSemver(current, target string) bool {
	cMaj, cMin, cPat, okC := parseSemver(current)
	tMaj, tMin, tPat, okT := parseSemver(target)
	if !okC || !okT {
		return strings.TrimSpace(current) != strings.TrimSpace(target)
	}
	if tMaj != cMaj {
		return tMaj > cMaj
	}
	if tMin != cMin {
		return tMin > cMin
	}
	return tPat > cPat
}

func isMinorOrPatchSemverUpgrade(current, target string) bool {
	cMaj, cMin, cPat, okC := parseSemver(current)
	tMaj, tMin, tPat, okT := parseSemver(target)
	if !okC || !okT {
		return false
	}
	if cMaj != tMaj {
		return false
	}
	if tMin != cMin {
		return tMin > cMin
	}
	return tPat > cPat
}

func (r *Reconciler) reconcileDeletingEntry(ctx context.Context, entry *store.EntryRow) error {
	zone, vmID := r.resolveZoneAndVMName(entry)
	if r.compute != nil && vmID != "" && zone != "" {
		if err := r.compute.DeleteInstance(ctx, zone, vmID); err != nil {
			return fmt.Errorf("reconcile deleting %s: DeleteInstance: %w", entry.Name, err)
		}
	}

	now := r.now()
	retainDB := entry.DBPurgeAfter == nil || entry.DBPurgeAfter.After(now)
	if r.resources != nil {
		resSpec := registry.InstanceResourceSpec{
			EntryID:     entry.ID,
			Name:        entry.Name,
			OwnerEmail:  entry.OwnerEmail,
			SQLInstance: entry.SQLInstance,
			ShipLogs:    entry.ShipLogs,
			AllowSSH:    entry.AllowSSH,
		}
		resBundle := registry.InstanceResourceBundle{
			ServiceAccountEmail: entry.ServiceAccount,
			SQLInstance:         entry.SQLInstance,
			DatabaseName:        entry.DBName,
			SecretPrefix:        entry.SecretPrefix,
		}
		if err := r.resources.Teardown(ctx, resSpec, resBundle, retainDB); err != nil {
			return fmt.Errorf("reconcile deleting %s: Teardown(retainDB=%v): %w", entry.Name, retainDB, err)
		}
	}
	if !retainDB && r.configRepos != nil && r.cfg.ConfigRepos.Provider != "" && r.cfg.ConfigRepos.Provider != "none" {
		if err := r.configRepos.ArchiveRepo(ctx, entry.Name); err != nil {
			return fmt.Errorf("reconcile deleting %s: ArchiveRepo: %w", entry.Name, err)
		}
	}

	if r.hostCache != nil {
		r.hostCache.Invalidate(entry.Name)
	}
	_, err := r.store.CompleteDeleteEntry(ctx, entry.ID, entry.Version, !retainDB)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	return err
}

func (r *Reconciler) reconcilePurge(ctx context.Context) error {
	due, err := r.store.ListEntriesDueForPurge(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, entry := range due {
		if r.configRepos != nil && r.cfg.ConfigRepos.Provider != "none" {
			if err := r.configRepos.ArchiveRepo(ctx, entry.Name); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if r.resources != nil {
			resSpec := registry.InstanceResourceSpec{
				EntryID:     entry.ID,
				Name:        entry.Name,
				OwnerEmail:  entry.OwnerEmail,
				SQLInstance: entry.SQLInstance,
			}
			resBundle := registry.InstanceResourceBundle{
				ServiceAccountEmail: entry.ServiceAccount,
				SQLInstance:         entry.SQLInstance,
				DatabaseName:        entry.DBName,
				SecretPrefix:        entry.SecretPrefix,
			}
			if err := r.resources.Teardown(ctx, resSpec, resBundle, false); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if err := r.store.MarkEntryResourcesPurged(ctx, entry.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		_ = r.store.RecordAudit(ctx, "system:reconciler", "entry_purge", entry.ID, entry.Name, nil)
	}

	if _, err := r.store.PurgeExpiredTombstones(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// PollRunningInstances polls `<backend_url>/api/registry/status` for every `running` hosted
// `instance` entry, updating `entries` (`grain_version`, `grain_commit`, `health_state`,
// `active_tasks`, `awaiting_reply_tasks`, `last_heartbeat_at`) and `entry_scopes(source='reported')`,
// and checks GCE VM status to transition unexpectedly terminated/missing VMs to `failed`.
func (r *Reconciler) PollRunningInstances(ctx context.Context) error {
	entries, err := r.store.ListEntriesFiltered(ctx, false)
	if err != nil {
		return err
	}
	var firstErr error
	for _, entry := range entries {
		if entry.Kind != api.KindInstance || entry.LifecycleState != api.StateRunning {
			continue
		}
		// 1. Check GCE VM status if Compute is configured.
		if r.compute != nil && entry.GCEInstanceID != "" {
			zone, vmID := r.resolveZoneAndVMName(entry)
			vmStatus, vmErr := r.compute.GetInstanceStatus(ctx, zone, vmID)
			if vmErr == nil && vmStatus != nil {
				st := strings.ToUpper(strings.TrimSpace(vmStatus.State))
				if st == "NOT_FOUND" || st == "TERMINATED" {
					r.failEntry(ctx, entry, fmt.Sprintf("GCE instance %s in zone %s is %s", vmID, zone, st))
					continue
				}
			}
		}

		// 2. Poll `<backend_url>/api/registry/status`.
		if entry.BackendURL == "" {
			continue
		}
		statusResp, err := r.fetchInstanceStatus(ctx, entry.Name, entry.BackendURL)
		if err != nil {
			continue
		}
		if _, err := r.recordStatusHeartbeat(ctx, entry.ID, statusResp); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ReconcileOrphans lists all GCE VMs matching `vm_prefix` and `grain-registry` in the configured
// zones and compares them against `registry.entries`:
//   - If a VM belongs to an entry that is now `deleted` or has moved to a newer `grain-generation`,
//     the stale VM is automatically deleted via `Compute.DeleteInstance`.
//   - If a VM has no matching entry in `registry.entries`, it is recorded in `Orphans()` and
//     `registry.audit_log` (`orphan_vm_detected`) for operator visibility.
func (r *Reconciler) ReconcileOrphans(ctx context.Context) error {
	if r.compute == nil {
		return nil
	}
	vms, err := r.compute.ListInstances(ctx)
	if err != nil {
		return err
	}
	entries, err := r.store.ListEntriesFiltered(ctx, true)
	if err != nil {
		return err
	}

	activeByName := make(map[string]*store.EntryRow)
	deletedByName := make(map[string]*store.EntryRow)
	for _, e := range entries {
		if e.Kind != api.KindInstance {
			continue
		}
		if e.DeletedAt == nil && e.LifecycleState != api.StateDeleted {
			activeByName[e.Name] = e
		} else {
			deletedByName[e.Name] = e
		}
	}

	vmPrefix := strings.TrimSpace(r.cfg.Compute.VMPrefix)
	if vmPrefix == "" {
		vmPrefix = "grain-vm-"
	}

	var unmatched []OrphanInstance
	now := r.now()

	for _, vm := range vms {
		entryName := ""
		if vm.Labels != nil {
			entryName = strings.TrimSpace(vm.Labels["grain-entry"])
		}
		if entryName == "" && strings.HasPrefix(vm.Name, vmPrefix) {
			entryName = strings.TrimPrefix(vm.Name, vmPrefix)
		}

		if active, ok := activeByName[entryName]; ok {
			if active.LifecycleState == api.StateStopped || active.LifecycleState == "STOPPED" {
				if err := r.compute.DeleteInstance(ctx, vm.Zone, vm.InstanceID); err == nil {
					_ = r.store.RecordAudit(ctx, "system:reconciler", "orphan_vm_deleted", active.ID, active.Name, map[string]any{
						"instance_id": vm.InstanceID,
						"zone":        vm.Zone,
						"reason":      "stopped_entry_straggler",
					})
				}
				continue
			}
			// Check whether the VM is from a stale generation that was already replaced.
			if vmGenStr := vm.Labels["grain-generation"]; vmGenStr != "" {
				if vmGen, pErr := strconv.ParseInt(vmGenStr, 10, 64); pErr == nil {
					if active.GCEInstanceID != "" && active.GCEInstanceID != vm.InstanceID && vmGen < active.Version {
						_ = r.compute.DeleteInstance(ctx, vm.Zone, vm.InstanceID)
						_ = r.store.RecordAudit(ctx, "system:reconciler", "orphan_vm_deleted", active.ID, active.Name, map[string]any{
							"instance_id":      vm.InstanceID,
							"zone":             vm.Zone,
							"stale_generation": vmGenStr,
						})
					}
				}
			}
			continue
		}

		if delEntry, wasDeleted := deletedByName[entryName]; wasDeleted {
			// Known deleted entry of this registry: clean up the straggler VM automatically.
			if err := r.compute.DeleteInstance(ctx, vm.Zone, vm.InstanceID); err == nil {
				_ = r.store.RecordAudit(ctx, "system:reconciler", "orphan_vm_deleted", delEntry.ID, delEntry.Name, map[string]any{
					"instance_id": vm.InstanceID,
					"zone":        vm.Zone,
					"reason":      "deleted_entry_straggler",
				})
			}
			continue
		}

		// Unknown orphan VM with no matching entry in `registry.entries`: report it without deleting.
		unmatched = append(unmatched, OrphanInstance{
			InstanceID: vm.InstanceID,
			Zone:       vm.Zone,
			State:      vm.State,
			InternalIP: vm.InternalIP,
			Labels:     vm.Labels,
			DetectedAt: now,
		})
		orphanKey := vm.Zone + "/" + vm.InstanceID
		r.orphanMu.Lock()
		alreadyReported := r.reportedOrphans[orphanKey]
		r.reportedOrphans[orphanKey] = true
		r.orphanMu.Unlock()
		if !alreadyReported {
			_ = r.store.RecordAudit(ctx, "system:reconciler", "orphan_vm_detected", "", entryName, map[string]any{
				"instance_id": vm.InstanceID,
				"zone":        vm.Zone,
				"internal_ip": vm.InternalIP,
				"labels":      vm.Labels,
			})
		}
	}

	r.orphanMu.Lock()
	r.orphans = unmatched
	r.orphanMu.Unlock()
	return nil
}

func (r *Reconciler) provisionEntryResources(ctx context.Context, entry *store.EntryRow) (*registry.InstanceResourceBundle, string, error) {
	sqlInstName := entry.SQLInstance
	dbName := entry.DBName
	if sqlInstName == "" {
		if sqRow, allocDB, err := r.store.AllocateSQLInstance(ctx, entry.ID, entry.Name); err == nil && sqRow != nil {
			sqlInstName = sqRow.Name
			dbName = allocDB
		}
	}
	bundle := &registry.InstanceResourceBundle{
		ServiceAccountEmail: entry.ServiceAccount,
		SQLInstance:         sqlInstName,
		DatabaseName:        dbName,
		SecretPrefix:        entry.SecretPrefix,
	}
	if r.resources != nil {
		provBundle, err := r.resources.Provision(ctx, registry.InstanceResourceSpec{
			EntryID:     entry.ID,
			Name:        entry.Name,
			OwnerEmail:  entry.OwnerEmail,
			SQLInstance: sqlInstName,
			ShipLogs:    entry.ShipLogs,
			AllowSSH:    entry.AllowSSH,
		})
		if err != nil {
			return nil, "", err
		}
		if provBundle != nil {
			bundle = provBundle
			if bundle.SQLInstance == "" {
				bundle.SQLInstance = sqlInstName
			}
			if bundle.DatabaseName == "" {
				bundle.DatabaseName = dbName
			}
		}
	}
	if bundle.DatabaseName == "" {
		bundle.DatabaseName = store.DatabaseNameForEntry(entry.Name)
	}
	if bundle.SecretPrefix == "" {
		secPref := strings.TrimSpace(r.cfg.Secrets.SecretPrefix)
		if secPref == "" {
			secPref = "grain-inst-"
		}
		bundle.SecretPrefix = secPref + entry.Name + "-"
	}

	configRepoURL := entry.ConfigRepo
	if r.configRepos != nil && r.cfg.ConfigRepos.Provider != "none" {
		repoBundle, err := r.configRepos.EnsureRepo(ctx, entry.Name, entry.OwnerEmail)
		if err != nil {
			return nil, "", err
		}
		if repoBundle != nil {
			configRepoURL = repoBundle.RepoURL
			if repoBundle.DeployKey != "" && r.resources != nil {
				if sw, ok := r.resources.(registry.SecretWriter); ok {
					if _, err := sw.PutSecret(ctx, entry.Name, "config-repo-key", []byte(repoBundle.DeployKey)); err != nil {
						return nil, "", err
					}
				}
			}
		}
	}
	return bundle, configRepoURL, nil
}

func (r *Reconciler) createVMForEntry(ctx context.Context, entry *store.EntryRow, bundle *registry.InstanceResourceBundle, configRepoURL string) (*registry.InstanceHandle, error) {
	shape := r.resolveShape(ctx, entry.Shape)
	zone := strings.TrimSpace(entry.Zone)
	if zone == "" && len(r.cfg.Compute.Zones) > 0 {
		zone = r.cfg.Compute.Zones[0]
	}

	regID := strings.TrimSpace(r.cfg.Compute.RegistryID)
	if regID == "" && r.cfg.Compute.Labels != nil {
		regID = strings.TrimSpace(r.cfg.Compute.Labels["grain-registry"])
	}
	if regID == "" {
		regID = "grain-registry"
	}

	genStr := strconv.FormatInt(entry.Version, 10)
	labels := map[string]string{
		"grain-registry":   regID,
		"grain-entry":      entry.Name,
		"grain-generation": genStr,
	}

	regURL := ""
	jwksURL := ""
	if r.cfg.Host != "" {
		regURL = "https://" + r.cfg.Host
		jwksURL = regURL + "/.well-known/jwks.json"
	}
	targetVer := strings.TrimSpace(entry.TargetVersion)
	if targetVer == "" {
		targetVer = strings.TrimSpace(entry.GrainVersion)
	}
	startupEnv := map[string]string{
		"GRAIN_ENTRY_NAME":          entry.Name,
		"GRAIN_REGISTRY_URL":        regURL,
		"GRAIN_JWKS_URL":            jwksURL,
		"GRAIN_SQL_INSTANCE":        bundle.SQLInstance,
		"GRAIN_SQL_CONNECTION_NAME": bundle.SQLConnectionName,
		"GRAIN_SQL_PRIVATE_IP":      bundle.SQLPrivateIP,
		"GRAIN_DB_NAME":             bundle.DatabaseName,
		"GRAIN_SECRET_PREFIX":       bundle.SecretPrefix,
		"GRAIN_GCS_BUCKET":          bundle.GCSBucket,
		"GRAIN_GCS_PREFIX":          bundle.GCSManagedFolder,
		"GRAIN_CONFIG_REPO":         configRepoURL,
		"GRAIN_VERSION":             targetVer,
	}

	return r.compute.CreateInstance(ctx, registry.InstanceSpec{
		EntryID:        entry.ID,
		Name:           entry.Name,
		Zone:           zone,
		Shape:          shape,
		ServiceAccount: bundle.ServiceAccountEmail,
		BootImage:      r.cfg.Compute.BootImage,
		StartupEnv:     startupEnv,
		Labels:         labels,
		Generation:     entry.Version,
	})
}

func (r *Reconciler) resolveShape(ctx context.Context, shapeName string) registry.Shape {
	shapeName = strings.TrimSpace(shapeName)
	if shapeName == "" {
		shapeName = strings.TrimSpace(r.cfg.Compute.DefaultShape)
	}
	if shapeName != "" && r.store != nil {
		if row, err := r.store.GetShape(ctx, shapeName); err == nil && row != nil {
			return registry.Shape{
				Name:         row.Name,
				MachineType:  row.MachineType,
				BootDiskGB:   row.BootDiskGB,
				BootDiskIOPS: row.BootDiskIOPS,
				QuotaUnits:   row.QuotaUnits,
				Description:  row.Description,
			}
		}
	}
	for _, s := range r.cfg.Compute.Shapes {
		if s.Name == shapeName || shapeName == "" {
			return s
		}
	}
	return registry.Shape{
		Name:        "default",
		MachineType: "e2-standard-4",
		BootDiskGB:  50,
		QuotaUnits:  1,
	}
}

func (r *Reconciler) resolveZoneAndVMName(entry *store.EntryRow) (string, string) {
	zone := strings.TrimSpace(entry.Zone)
	if zone == "" && len(r.cfg.Compute.Zones) > 0 {
		zone = r.cfg.Compute.Zones[0]
	}
	vmID := strings.TrimSpace(entry.GCEInstanceID)
	if vmID == "" {
		vmPrefix := strings.TrimSpace(r.cfg.Compute.VMPrefix)
		if vmPrefix == "" {
			vmPrefix = "grain-vm-"
		}
		vmID = vmPrefix + entry.Name
	}
	return zone, vmID
}

func (r *Reconciler) probeInstanceHealthAndStatus(ctx context.Context, entryName, backendURL string) (*api.InstanceStatusResponse, error) {
	backendURL = strings.TrimRight(strings.TrimSpace(backendURL), "/")
	if backendURL == "" {
		return nil, errors.New("empty backend_url")
	}
	healthReq, err := http.NewRequestWithContext(ctx, http.MethodGet, backendURL+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	r.attachRegistryAssertion(ctx, healthReq, entryName)
	resp, err := r.httpClient.Do(healthReq)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/healthz returned HTTP %d", resp.StatusCode)
	}

	statusResp, statusErr := r.fetchInstanceStatus(ctx, entryName, backendURL)
	if statusErr != nil {
		// If `/healthz` returned 200 OK and `/api/registry/status` returned 404 on a minimal stub,
		// synthesize a basic OK status response; otherwise return the error so probing continues until ready.
		if strings.Contains(statusErr.Error(), "HTTP 404") {
			return &api.InstanceStatusResponse{Health: api.HealthOK}, nil
		}
		return nil, statusErr
	}
	return statusResp, nil
}

func (r *Reconciler) fetchInstanceStatus(ctx context.Context, entryName, backendURL string) (*api.InstanceStatusResponse, error) {
	backendURL = strings.TrimRight(strings.TrimSpace(backendURL), "/")
	if backendURL == "" {
		return nil, errors.New("empty backend_url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, backendURL+"/api/registry/status", nil)
	if err != nil {
		return nil, err
	}
	r.attachRegistryAssertion(ctx, req, entryName)
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/api/registry/status HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var statusResp api.InstanceStatusResponse
	if err := json.Unmarshal(body, &statusResp); err != nil {
		return nil, fmt.Errorf("decoding /api/registry/status: %w", err)
	}
	return &statusResp, nil
}

func (r *Reconciler) attachRegistryAssertion(ctx context.Context, req *http.Request, entryName string) {
	if r.signer == nil {
		return
	}
	tok, err := r.signer.SignAssertion(ctx, registry.RegistryClaims{
		Audience: "grain-instance:" + entryName,
		Subject:  "serviceAccount:grain-registry",
		Email:    "grain-registry@system.local",
		Kind:     "service",
	})
	if err == nil && tok != "" {
		req.Header.Set(api.HeaderRegistryAssertion, tok)
	}
}

func (r *Reconciler) recordStatusHeartbeat(ctx context.Context, entryID string, st *api.InstanceStatusResponse) (*store.EntryRow, error) {
	if st == nil {
		st = &api.InstanceStatusResponse{Health: api.HealthOK}
	}
	health := st.Health
	if health == "" {
		health = api.HealthOK
	}
	scopes := make([]api.HeartbeatScope, 0, len(st.Scopes))
	for _, sc := range st.Scopes {
		role := sc.Role
		if role == "" {
			role = api.RolePrimary
		}
		scopes = append(scopes, api.HeartbeatScope{
			Repo:       sc.Repo,
			PathPrefix: sc.PathPrefix,
			Role:       role,
			Branch:     sc.Branch,
		})
	}
	return r.store.RecordHeartbeat(ctx, entryID, api.HeartbeatRequest{
		GrainVersion:       st.GrainVersion,
		GrainCommit:        st.GrainCommit,
		HostKind:           st.HostKind,
		Health:             health,
		HealthDetail:       st.HealthDetail,
		ActiveTasks:        st.ActiveTasks,
		AwaitingReplyTasks: st.AwaitingReplyTasks,
		QueueDepth:         st.QueueDepth,
		ActiveSessions:     st.ActiveSessions,
		Scopes:             scopes,
	})
}

func (r *Reconciler) failEntry(ctx context.Context, entry *store.EntryRow, reason string) {
	fresh, err := r.store.GetEntryByID(ctx, entry.ID)
	if err != nil || fresh == nil {
		return
	}
	failed, err := r.store.TransitionEntryState(ctx, fresh.ID, fresh.Version, api.StateFailed, reason)
	if err == nil && failed != nil {
		if r.hostCache != nil {
			r.hostCache.Put(failed)
		}
		_ = r.store.RecordAudit(ctx, "system:reconciler", "entry_failed", failed.ID, failed.Name, map[string]any{
			"reason": reason,
		})
	}
}
