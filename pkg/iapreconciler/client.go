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
