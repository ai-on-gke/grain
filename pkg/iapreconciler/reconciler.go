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

package iapreconciler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

const (
	// RoleIAPHTTPSResourceAccessor is the GCP IAM role required to access IAP-secured web backend services.
	RoleIAPHTTPSResourceAccessor = "roles/iap.httpsResourceAccessor"

	// Default retry parameters for concurrency conflicts.
	DefaultMaxRetries     = 5
	DefaultInitialBackoff = 100 * time.Millisecond
	DefaultMaxBackoff     = 2 * time.Second
	DefaultBackoffFactor  = 2.0
)

// Option configures a Reconciler.
type Option func(*Reconciler)

// WithMaxRetries sets the maximum number of retries on concurrency conflicts.
func WithMaxRetries(maxRetries int) Option {
	return func(r *Reconciler) {
		if maxRetries >= 0 {
			r.maxRetries = maxRetries
		}
	}
}

// WithKeepCurrentMembers makes a reconcile add the desired members to
// the accessor binding rather than replace it with them: every member
// the binding already holds stays, except a broad one (IsBroadMember),
// which is still removed. It is how a caller that no longer knows every
// person who should have access -- a single-tenant grain, which has no
// accounts (docs/access-code.md) -- grants its own identities without
// revoking anybody else's.
func WithKeepCurrentMembers() Option {
	return func(r *Reconciler) {
		r.keepCurrent = true
	}
}

// withCurrent is desired with current's members added, when r keeps
// them (WithKeepCurrentMembers), and desired alone otherwise.
func (r *Reconciler) withCurrent(desired, current []string) []string {
	if !r.keepCurrent {
		return desired
	}
	return append(append([]string(nil), desired...), current...)
}

// accessorMembers is the members of the unconditional
// roles/iap.httpsResourceAccessor binding among bindings, each read
// through unconditional and membersOf, so one helper serves both policy
// types.
func accessorMembers[B any](bindings []*B, unconditional func(*B) bool, membersOf func(*B) []string) []string {
	var out []string
	for _, b := range bindings {
		if b != nil && unconditional(b) {
			out = append(out, membersOf(b)...)
		}
	}
	return out
}

// WithSleep sets a custom sleep function, useful for tests.
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(r *Reconciler) {
		r.sleep = sleep
	}
}

// Reconciler synchronizes desired members with the GCP IAP IAM policy on
// a backend service (Reconcile, through an IAMClient) or on an IAP web
// resource such as a Cloud Run service (ReconcileWebResource, through an
// IAPWebClient).
type Reconciler struct {
	client         IAMClient
	web            IAPWebClient
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	backoffFactor  float64
	sleep          func(context.Context, time.Duration) error
	keepCurrent    bool
}

// NewReconciler creates a new Reconciler with the specified IAMClient and optional configuration.
func NewReconciler(client IAMClient, opts ...Option) *Reconciler {
	return newReconciler(client, nil, opts)
}

// NewWebResourceReconciler creates a Reconciler for IAP web resources
// (ReconcileWebResource) with the specified IAPWebClient.
func NewWebResourceReconciler(client IAPWebClient, opts ...Option) *Reconciler {
	return newReconciler(nil, client, opts)
}

func newReconciler(client IAMClient, web IAPWebClient, opts []Option) *Reconciler {
	r := &Reconciler{
		client:         client,
		web:            web,
		maxRetries:     DefaultMaxRetries,
		initialBackoff: DefaultInitialBackoff,
		maxBackoff:     DefaultMaxBackoff,
		backoffFactor:  DefaultBackoffFactor,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// NormalizeMember ensures a member string is properly formatted for GCP IAM.
// It handles user:, serviceAccount:, group:, domain:, and raw email strings.
func NormalizeMember(member string) string {
	member = strings.TrimSpace(member)
	if member == "" {
		return ""
	}
	prefixes := []string{
		"user:",
		"serviceAccount:",
		"group:",
		"domain:",
		"principal:",
		"principalSet:",
		"deleted:user:",
		"deleted:serviceAccount:",
		"deleted:group:",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(member, p) {
			val := strings.TrimSpace(strings.TrimPrefix(member, p))
			if val == "" {
				return ""
			}
			return p + val
		}
	}
	if member == "allUsers" || member == "allAuthenticatedUsers" {
		return member
	}
	if strings.HasSuffix(member, ".gserviceaccount.com") {
		return "serviceAccount:" + member
	}
	if strings.Contains(member, "@") {
		return "user:" + member
	}
	return "user:" + member
}

// NormalizeMembers normalizes, filters empty values, deduplicates, and sorts a list of IAM members.
func NormalizeMembers(members []string) []string {
	seen := make(map[string]bool, len(members))
	var result []string
	for _, m := range members {
		norm := NormalizeMember(m)
		if norm != "" && !seen[norm] {
			seen[norm] = true
			result = append(result, norm)
		}
	}
	slices.Sort(result)
	return result
}

// IsBroadMember reports whether a normalized IAM member names a population
// rather than an identity grain manages: everybody (allUsers), every Google
// account (allAuthenticatedUsers), every account in a domain (domain:), or a
// whole workload or workforce identity pool (principalSet:). Granting any of
// them roles/iap.httpsResourceAccessor opens the UI far beyond the people
// grain knows about, so the reconciler never puts one into a policy.
func IsBroadMember(member string) bool {
	switch member {
	case "allUsers", "allAuthenticatedUsers":
		return true
	}
	return strings.HasPrefix(member, "domain:") || strings.HasPrefix(member, "principalSet:")
}

// FilterDesiredMembers normalizes members as NormalizeMembers does and
// splits off the ones IsBroadMember refuses. kept is what may be granted;
// refused is what was dropped, for the caller to report.
func FilterDesiredMembers(members []string) (kept, refused []string) {
	for _, m := range NormalizeMembers(members) {
		if IsBroadMember(m) {
			refused = append(refused, m)
			continue
		}
		kept = append(kept, m)
	}
	return kept, refused
}

// ApplyDesiredMembers computes whether the current policy needs updating and produces the updated policy.
// It preserves bindings for all other roles as well as conditional bindings on roles/iap.httpsResourceAccessor.
// Broad members (see IsBroadMember) are dropped from the desired set, while the
// current binding is read as-is, so an existing public grant is removed.
func ApplyDesiredMembers(currentPolicy *compute.Policy, desiredMembers []string) (bool, *compute.Policy) {
	desired, _ := FilterDesiredMembers(desiredMembers)
	if currentPolicy == nil {
		currentPolicy = &compute.Policy{}
	}

	var currentMembers []string
	var retainedBindings []*compute.Binding

	for _, b := range currentPolicy.Bindings {
		if b == nil {
			continue
		}
		if b.Role == RoleIAPHTTPSResourceAccessor && b.Condition == nil {
			for _, m := range b.Members {
				norm := NormalizeMember(m)
				if norm != "" {
					currentMembers = append(currentMembers, norm)
				}
			}
		} else {
			retainedBindings = append(retainedBindings, b)
		}
	}

	currentMembers = NormalizeMembers(currentMembers)

	// Check if already in desired state
	if slices.Equal(currentMembers, desired) {
		return false, currentPolicy
	}

	// Construct updated bindings
	var newBindings []*compute.Binding
	newBindings = append(newBindings, retainedBindings...)
	if len(desired) > 0 {
		newBindings = append(newBindings, &compute.Binding{
			Role:    RoleIAPHTTPSResourceAccessor,
			Members: desired,
		})
	}

	updatedPolicy := &compute.Policy{
		AuditConfigs: currentPolicy.AuditConfigs,
		Bindings:     newBindings,
		Etag:         currentPolicy.Etag,
		Version:      currentPolicy.Version,
	}

	return true, updatedPolicy
}

// isConcurrencyConflict checks if an error from GCP API indicates an IAM concurrency conflict or ETag mismatch.
func isConcurrencyConflict(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		if gerr.Code == http.StatusConflict || gerr.Code == http.StatusPreconditionFailed {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "conflict") ||
		strings.Contains(msg, "etag") ||
		strings.Contains(msg, "precondition") ||
		strings.Contains(msg, "concurrent")
}

// Reconcile synchronizes desired members with the GCP backend service's IAP IAM policy.
func (r *Reconciler) Reconcile(ctx context.Context, projectID, backendServiceName string, desiredMembers []string) error {
	if projectID == "" {
		return errors.New("iapreconciler: projectID is required")
	}
	if backendServiceName == "" {
		return errors.New("iapreconciler: backendServiceName is required")
	}
	if r.client == nil {
		return errors.New("iapreconciler: client is nil")
	}

	desiredMembers, refused := FilterDesiredMembers(desiredMembers)
	if len(refused) > 0 {
		log.Printf("iapreconciler: refusing to grant %s on backend service %q to broad principals %v", RoleIAPHTTPSResourceAccessor, backendServiceName, refused)
	}

	return r.converge(ctx, func(ctx context.Context) (getErr, setErr error) {
		policy, err := r.client.GetBackendServiceIAMPolicy(ctx, projectID, backendServiceName)
		if err != nil {
			return fmt.Errorf("iapreconciler: getting IAM policy for backend service %q: %w", backendServiceName, err), nil
		}
		var current []string
		if policy != nil {
			current = accessorMembers(policy.Bindings,
				func(b *compute.Binding) bool { return b.Role == RoleIAPHTTPSResourceAccessor && b.Condition == nil },
				func(b *compute.Binding) []string { return b.Members })
		}
		changed, newPolicy := ApplyDesiredMembers(policy, r.withCurrent(desiredMembers, current))
		if !changed {
			// Idempotent: no update needed
			return nil, nil
		}
		if _, err := r.client.SetBackendServiceIAMPolicy(ctx, projectID, backendServiceName, newPolicy); err != nil {
			return nil, fmt.Errorf("iapreconciler: setting IAM policy for backend service %q: %w", backendServiceName, err)
		}
		return nil, nil
	})
}

// converge runs one read-modify-write of a policy, step, until it lands:
// a getErr ends it at once, a setErr that is a concurrency conflict (the
// policy changed between the read and the write, so its etag no longer
// matches) is retried with backoff from a fresh read, up to maxRetries,
// and any other setErr ends it.
func (r *Reconciler) converge(ctx context.Context, step func(context.Context) (getErr, setErr error)) error {
	backoff := r.initialBackoff
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		getErr, setErr := step(ctx)
		if getErr != nil {
			return getErr
		}
		if setErr == nil {
			return nil
		}
		if !isConcurrencyConflict(setErr) || attempt == r.maxRetries {
			return setErr
		}

		if err := r.sleep(ctx, backoff); err != nil {
			return fmt.Errorf("iapreconciler: waiting for retry: %w", err)
		}

		backoff = time.Duration(float64(backoff) * r.backoffFactor)
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}
	return nil
}

// ReconcileIAPMembers creates a default Compute client and reconciles IAP members for the given backend service.
func ReconcileIAPMembers(ctx context.Context, projectID, backendServiceName string, desiredMembers []string, opts ...Option) error {
	client, err := NewComputeIAMClient(ctx)
	if err != nil {
		return err
	}
	return NewReconciler(client, opts...).Reconcile(ctx, projectID, backendServiceName, desiredMembers)
}

// ReconcileIAPMembersWithClient reconciles IAP members using the provided IAMClient.
func ReconcileIAPMembersWithClient(ctx context.Context, client IAMClient, projectID, backendServiceName string, desiredMembers []string, opts ...Option) error {
	return NewReconciler(client, opts...).Reconcile(ctx, projectID, backendServiceName, desiredMembers)
}
