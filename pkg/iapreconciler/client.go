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
	"fmt"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

// IAMClient defines the interface for interacting with GCP Compute Backend Service IAM policies.
type IAMClient interface {
	GetBackendServiceIAMPolicy(ctx context.Context, projectID, backendServiceName string) (*compute.Policy, error)
	SetBackendServiceIAMPolicy(ctx context.Context, projectID, backendServiceName string, policy *compute.Policy) (*compute.Policy, error)
}

// ComputeIAMClient implements IAMClient using the Google Cloud Compute API SDK.
type ComputeIAMClient struct {
	service *compute.Service
}

// NewComputeIAMClient creates a new ComputeIAMClient using Google Cloud default credentials or provided options.
func NewComputeIAMClient(ctx context.Context, opts ...option.ClientOption) (*ComputeIAMClient, error) {
	svc, err := compute.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating compute service: %w", err)
	}
	return &ComputeIAMClient{service: svc}, nil
}

// GetBackendServiceIAMPolicy retrieves the IAM policy for a global backend service.
func (c *ComputeIAMClient) GetBackendServiceIAMPolicy(ctx context.Context, projectID, backendServiceName string) (*compute.Policy, error) {
	return c.service.BackendServices.GetIamPolicy(projectID, backendServiceName).Context(ctx).Do()
}

// SetBackendServiceIAMPolicy sets the IAM policy for a global backend service.
func (c *ComputeIAMClient) SetBackendServiceIAMPolicy(ctx context.Context, projectID, backendServiceName string, policy *compute.Policy) (*compute.Policy, error) {
	req := &compute.GlobalSetPolicyRequest{
		Policy: policy,
	}
	return c.service.BackendServices.SetIamPolicy(projectID, backendServiceName, req).Context(ctx).Do()
}
