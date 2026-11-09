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

//go:build ignore

// registry-fakegcp is cmd/grain-registry with the real `gce` compute and
// instance-resources providers re-registered against gcpstub.go
// (GCPSTUB_URL, default http://127.0.0.1:4271) instead of Google's APIs. Only
// the base URLs and the HTTP client change; every provider code path,
// including the reconciler's service-account propagation retry, is the real one.
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry"
	"github.com/ai-on-gke/grain/pkg/registry/providers/gcp"
	_ "github.com/ai-on-gke/grain/pkg/registry/providers/github"
	_ "github.com/ai-on-gke/grain/pkg/registry/reconcile"
)

func main() {
	stub := os.Getenv("GCPSTUB_URL")
	if stub == "" {
		stub = "http://127.0.0.1:4271"
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	compute := func(ctx context.Context, cfg registry.ComputeConfig) (registry.Compute, error) {
		return gcp.NewGCECompute(ctx, cfg,
			gcp.WithComputeHTTPClient(hc),
			gcp.WithComputeBaseURL(stub+"/compute/v1"),
			gcp.WithOperationPollInterval(200*time.Millisecond))
	}
	resources := func(ctx context.Context, cfg registry.Config) (registry.InstanceResources, error) {
		return gcp.NewGCPInstanceResources(ctx, cfg,
			gcp.WithResourcesHTTPClient(hc),
			gcp.WithIAMBaseURL(stub+"/iam"),
			gcp.WithResourceManagerBaseURL(stub+"/crm"),
			gcp.WithSecretManagerBaseURL(stub+"/sm"),
			gcp.WithSQLAdminBaseURL(stub+"/sqladmin"),
			gcp.WithStorageBaseURL(stub+"/storage"),
			gcp.WithResourcesComputeBaseURL(stub+"/compute/v1"))
	}
	registry.RegisterComputeProvider("gce", compute)
	registry.RegisterInstanceResourcesProvider("gce", resources)
	registry.RegisterInstanceResourcesProvider("gcp", resources)
	registry.Main()
}
