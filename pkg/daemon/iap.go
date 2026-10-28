package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/iapreconciler"
	"github.com/bwsalmon/grain/pkg/model"
)

// iapSyncTriggers is the event trigger channel for dynamic IAP IAM reconciliation.
var IapSyncTriggers = make(chan struct{}, 1)

// triggerIAPSync requests an immediate IAP IAM reconciliation cycle.
// It is non-blocking and safe to call from any request handler or background worker.
func TriggerIAPSync() {
	select {
	case IapSyncTriggers <- struct{}{}:
	default:
	}
}

// CollectDesiredIAPMembers is the members grain itself needs on IAP
// access: the agent GCP service account, if one is configured. A
// single-tenant grain keeps no list of the people it serves -- whoever
// holds the access code is its owner (docs/access-code.md) -- so the
// people on a binding are whoever was put there, and ReconcileIAP keeps
// them (iapreconciler.WithKeepCurrentMembers).
func CollectDesiredIAPMembers(cfg Config) []string {
	var members []string
	if cfg.GcpServiceAccountEmail != "" {
		members = append(members, cfg.GcpServiceAccountEmail)
	}
	return iapreconciler.NormalizeMembers(members)
}

// IAPWebResource is the IAP web resource ReconcileIAP reconciles besides
// any backend service, and where it came from: -iap-resource (or
// grain_config.iap_resource) when that names one. An iap-resource of "none"
// turns it off.
func IAPWebResource(cfg Config) (resource, source string) {
	switch v := strings.TrimSpace(cfg.IapResource); {
	case v == "" || v == iapreconciler.ResourceNone:
		return "", ""
	default:
		return v, "iap-resource"
	}
}

// ReconcileIAP reconciles the roles/iap.httpsResourceAccessor binding of
// every IAP target the configuration names -- the backend service
// (-iap-backend-service) and the IAP web resource (IAPWebResource) --
// with the identities grain needs there (CollectDesiredIAPMembers). It
// adds rather than replaces: every member a binding already holds stays,
// except a broad principal, which is removed
// (iapreconciler.WithKeepCurrentMembers). Each target's outcome is
// recorded in IAPStatus; a failure on one does not stop the other, and
// the error returned joins them. The store is no longer read: nothing in
// it names who should have access.
func ReconcileIAP(ctx context.Context, _ *model.Store, cfg Config) error {
	if cfg.GcpProject == "" {
		iapStatus.retain(nil)
		return nil
	}
	resource, source := IAPWebResource(cfg)
	if cfg.IapBackendService == "" && resource == "" {
		iapStatus.retain(nil)
		return nil
	}

	desired := CollectDesiredIAPMembers(cfg)
	var err error
	keep := iapreconciler.WithKeepCurrentMembers()

	var targets []IAPTargetStatus
	var errs []error
	record := func(t IAPTargetStatus, terr error) {
		iapStatus.record(&t, terr)
		targets = append(targets, t)
		if terr != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", t.Kind, t.Name, terr))
		}
	}

	if cfg.IapBackendService != "" {
		t := IAPTargetStatus{Kind: IAPKindBackendService, Name: cfg.IapBackendService, Source: "iap-backend-service"}
		terr := err
		if terr == nil {
			if cfg.IapClient != nil {
				terr = iapreconciler.ReconcileIAPMembersWithClient(ctx, cfg.IapClient, cfg.GcpProject, cfg.IapBackendService, desired, keep)
			} else {
				terr = iapreconciler.ReconcileIAPMembers(ctx, cfg.GcpProject, cfg.IapBackendService, desired, keep)
			}
		}
		record(t, terr)
	}

	if resource != "" {
		t := IAPTargetStatus{Kind: IAPKindWebResource, Name: resource, Source: source}
		terr := err
		if terr == nil {
			if cfg.IapWebClient != nil {
				terr = iapreconciler.ReconcileWebResourceMembersWithClient(ctx, cfg.IapWebClient, resource, desired, keep)
			} else {
				terr = iapreconciler.ReconcileWebResourceMembers(ctx, resource, desired, keep)
			}
		}
		record(t, terr)
	}

	iapStatus.retain(targets)
	return errors.Join(errs...)
}

// IAPTargetStatus is one IAP target's reconciliation state.
type IAPTargetStatus struct {
	Kind        string     `json:"kind"`
	Name        string     `json:"name"`
	Source      string     `json:"source,omitempty"`
	SyncedAt    *time.Time `json:"synced_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
}

// The kinds of IAP target ReconcileIAP manages.
const (
	IAPKindBackendService = "backend service"
	IAPKindWebResource    = "IAP resource"
)

// iapStatus is the last outcome of each IAP target ReconcileIAP manages.
var iapStatus iapStatusBook

type iapStatusBook struct {
	mu      sync.Mutex
	targets []IAPTargetStatus
	now     func() time.Time
}

// record stamps t with err's outcome, carrying forward from the previous
// record of the same target when the last success was, and logs a
// target's recovery from a failure.
func (b *iapStatusBook) record(t *IAPTargetStatus, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().UTC()
	if b.now != nil {
		now = b.now().UTC()
	}
	var prev *IAPTargetStatus
	for i := range b.targets {
		if b.targets[i].Kind == t.Kind && b.targets[i].Name == t.Name {
			prev = &b.targets[i]
		}
	}
	if err != nil {
		t.LastError = err.Error()
		t.LastErrorAt = &now
		if prev != nil {
			t.SyncedAt = prev.SyncedAt
		}
		return
	}
	t.SyncedAt = &now
	if prev != nil && prev.LastError != "" {
		log.Printf("grain daemon: IAP IAM reconciliation of %s %s succeeded again", t.Kind, t.Name)
	}
}

// retain makes targets the whole of what is reported, dropping any
// target the configuration no longer names.
func (b *iapStatusBook) retain(targets []IAPTargetStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.targets = append([]IAPTargetStatus(nil), targets...)
}

// IAPStatus is what ReconcileIAP last found for each IAP target.
func IAPStatus() []IAPTargetStatus {
	iapStatus.mu.Lock()
	defer iapStatus.mu.Unlock()
	return append([]IAPTargetStatus(nil), iapStatus.targets...)
}
