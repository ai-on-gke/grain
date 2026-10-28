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
	"regexp"
	"slices"
	"strings"

	"google.golang.org/api/iap/v1"
	"google.golang.org/api/option"
)

// IAPWebClient defines the interface for reading and writing the IAM policy
// of an IAP web resource through the IAP API (iap.googleapis.com v1), the
// way `gcloud iap web get-iam-policy` does. The resource is a full IAP
// resource name such as
// projects/<NUMBER>/iap_web/cloud_run-<REGION>/services/<SERVICE>, which
// is where Cloud Run's built-in IAP (`gcloud run deploy --iap`) keeps who
// may pass it.
type IAPWebClient interface {
	GetIAPWebIAMPolicy(ctx context.Context, resource string) (*iap.Policy, error)
	SetIAPWebIAMPolicy(ctx context.Context, resource string, policy *iap.Policy) (*iap.Policy, error)
}

// APIIAPWebClient implements IAPWebClient using the Google Cloud IAP API SDK.
type APIIAPWebClient struct {
	service *iap.Service
}

// NewAPIIAPWebClient creates a new APIIAPWebClient using Google Cloud default credentials or provided options.
func NewAPIIAPWebClient(ctx context.Context, opts ...option.ClientOption) (*APIIAPWebClient, error) {
	svc, err := iap.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating IAP service: %w", err)
	}
	return &APIIAPWebClient{service: svc}, nil
}

// conditionalPolicyVersion is the policy version that carries conditional
// bindings. It is asked for on every read so a condition someone set on
// the resource comes back intact and is written back as it was, rather
// than being read as version 1 and lost.
const conditionalPolicyVersion = 3

// GetIAPWebIAMPolicy retrieves the IAM policy for an IAP web resource.
func (c *APIIAPWebClient) GetIAPWebIAMPolicy(ctx context.Context, resource string) (*iap.Policy, error) {
	req := &iap.GetIamPolicyRequest{Options: &iap.GetPolicyOptions{RequestedPolicyVersion: conditionalPolicyVersion}}
	return c.service.V1.GetIamPolicy(resource, req).Context(ctx).Do()
}

// SetIAPWebIAMPolicy sets the IAM policy for an IAP web resource. The
// policy's etag makes the write conditional on nobody having changed it
// since it was read.
func (c *APIIAPWebClient) SetIAPWebIAMPolicy(ctx context.Context, resource string, policy *iap.Policy) (*iap.Policy, error) {
	return c.service.V1.SetIamPolicy(resource, &iap.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do()
}

// ResourceNone is the IAP resource setting that turns off deriving the
// resource from a Cloud Run relay, so only an explicitly named backend
// service (if any) is reconciled.
const ResourceNone = "none"

// webResourcePattern is the shape of an IAP web resource name naming one
// application: projects/<project>/iap_web/<type>/services/<service>[/...].
// The project (projects/<project>/iap_web) and the type
// (.../iap_web/compute) are resources too, but a grant there admits its
// members to every IAP application under them, so they are refused.
var webResourcePattern = regexp.MustCompile(`^projects/[^/]+/iap_web/[^/]+(/[^/]+/[^/]+)+$`)

// ValidateWebResource checks name is an IAP web resource name, such as
// projects/123/iap_web/cloud_run-europe-west4/services/grain-relay.
func ValidateWebResource(name string) error {
	if !webResourcePattern.MatchString(name) {
		return fmt.Errorf("iapreconciler: %q is not an IAP web resource name (want projects/<NUMBER>/iap_web/cloud_run-<REGION>/services/<SERVICE>)", name)
	}
	return nil
}

// CloudRunWebResource is the IAP web resource name of a Cloud Run
// service with IAP enabled on it.
func CloudRunWebResource(projectNumber, region, service string) string {
	return "projects/" + projectNumber + "/iap_web/cloud_run-" + region + "/services/" + service
}

// cloudRunAudiencePattern is the audience IAP signs assertions for in
// front of a Cloud Run service: /projects/<NUMBER>/locations/<REGION>/services/<SERVICE>.
var cloudRunAudiencePattern = regexp.MustCompile(`^/projects/([0-9]+)/locations/([a-z0-9-]+)/services/([A-Za-z0-9_-]+)$`)

// CloudRunWebResourceFromAudience derives the IAP web resource of a Cloud
// Run service from the audience IAP puts on the assertions it forwards
// to it (terraform's relay_iap_audience output, which the daemon is
// given as its user IAP audience). audiences may hold several, comma
// separated; the first in the Cloud Run form wins. ok is false when none
// is -- a load balancer's /projects/<N>/global/backendServices/<ID>
// audience names a backend service, which is not this kind of resource.
func CloudRunWebResourceFromAudience(audiences string) (resource string, ok bool) {
	for _, aud := range strings.Split(audiences, ",") {
		m := cloudRunAudiencePattern.FindStringSubmatch(strings.TrimSpace(aud))
		if m != nil {
			return CloudRunWebResource(m[1], m[2], m[3]), true
		}
	}
	return "", false
}

// ApplyDesiredMembersToWebPolicy is ApplyDesiredMembers for an IAP API
// policy: it computes whether the current policy needs updating and
// produces the updated policy, preserving bindings for all other roles,
// conditional bindings on roles/iap.httpsResourceAccessor, the etag and
// the version. Broad members are dropped from the desired set.
func ApplyDesiredMembersToWebPolicy(currentPolicy *iap.Policy, desiredMembers []string) (bool, *iap.Policy) {
	desired, _ := FilterDesiredMembers(desiredMembers)
	if currentPolicy == nil {
		currentPolicy = &iap.Policy{}
	}

	var currentMembers []string
	var retainedBindings []*iap.Binding
	for _, b := range currentPolicy.Bindings {
		if b == nil {
			continue
		}
		if b.Role == RoleIAPHTTPSResourceAccessor && b.Condition == nil {
			for _, m := range b.Members {
				if norm := NormalizeMember(m); norm != "" {
					currentMembers = append(currentMembers, norm)
				}
			}
		} else {
			retainedBindings = append(retainedBindings, b)
		}
	}

	if slices.Equal(NormalizeMembers(currentMembers), desired) {
		return false, currentPolicy
	}

	newBindings := append([]*iap.Binding(nil), retainedBindings...)
	if len(desired) > 0 {
		newBindings = append(newBindings, &iap.Binding{
			Role:    RoleIAPHTTPSResourceAccessor,
			Members: desired,
		})
	}
	return true, &iap.Policy{
		Bindings: newBindings,
		Etag:     currentPolicy.Etag,
		Version:  currentPolicy.Version,
	}
}

// ReconcileWebResource synchronizes desired members with the
// roles/iap.httpsResourceAccessor binding of an IAP web resource, with
// the same guarantees as Reconcile: broad members are refused, a write
// that loses an etag race is retried from a fresh read, and nothing but
// the unconditional accessor binding is changed.
func (r *Reconciler) ReconcileWebResource(ctx context.Context, resource string, desiredMembers []string) error {
	if resource == "" {
		return errors.New("iapreconciler: IAP web resource is required")
	}
	if err := ValidateWebResource(resource); err != nil {
		return err
	}
	if r.web == nil {
		return errors.New("iapreconciler: IAP web client is nil")
	}

	desiredMembers, refused := FilterDesiredMembers(desiredMembers)
	if len(refused) > 0 {
		log.Printf("iapreconciler: refusing to grant %s on IAP resource %q to broad principals %v", RoleIAPHTTPSResourceAccessor, resource, refused)
	}

	return r.converge(ctx, func(ctx context.Context) (getErr, setErr error) {
		policy, err := r.web.GetIAPWebIAMPolicy(ctx, resource)
		if err != nil {
			return fmt.Errorf("iapreconciler: getting IAM policy for IAP resource %q: %w", resource, err), nil
		}
		var current []string
		if policy != nil {
			current = accessorMembers(policy.Bindings,
				func(b *iap.Binding) bool { return b.Role == RoleIAPHTTPSResourceAccessor && b.Condition == nil },
				func(b *iap.Binding) []string { return b.Members })
		}
		changed, newPolicy := ApplyDesiredMembersToWebPolicy(policy, r.withCurrent(desiredMembers, current))
		if !changed {
			return nil, nil
		}
		if _, err := r.web.SetIAPWebIAMPolicy(ctx, resource, newPolicy); err != nil {
			return nil, fmt.Errorf("iapreconciler: setting IAM policy for IAP resource %q: %w", resource, err)
		}
		return nil, nil
	})
}

// ReconcileWebResourceMembers creates a default IAP API client and
// reconciles IAP members for the given IAP web resource.
func ReconcileWebResourceMembers(ctx context.Context, resource string, desiredMembers []string, opts ...Option) error {
	client, err := NewAPIIAPWebClient(ctx)
	if err != nil {
		return err
	}
	return NewWebResourceReconciler(client, opts...).ReconcileWebResource(ctx, resource, desiredMembers)
}

// ReconcileWebResourceMembersWithClient reconciles IAP members for an IAP
// web resource using the provided IAPWebClient.
func ReconcileWebResourceMembersWithClient(ctx context.Context, client IAPWebClient, resource string, desiredMembers []string, opts ...Option) error {
	return NewWebResourceReconciler(client, opts...).ReconcileWebResource(ctx, resource, desiredMembers)
}
