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

package gcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub"
)

func TestGCEComputeNestedVirtualizationN2C4AndCustomDisk(t *testing.T) {
	srv, state := newFakeGCEServer(t)
	ctx := context.Background()

	startupPath := filepath.Join(t.TempDir(), "hosted-vm-startup.sh")
	const startupBody = "#!/usr/bin/env bash\necho booted\n"
	if err := os.WriteFile(startupPath, []byte(startupBody), 0o600); err != nil {
		t.Fatalf("WriteFile startup script: %v", err)
	}

	shapes := []hub.Shape{
		{Name: "n2-standard-2", MachineType: "n2-standard-2", BootDiskGB: 50, QuotaUnits: 1},
		{Name: "c4-standard-2", MachineType: "c4-standard-2", BootDiskGB: 50, QuotaUnits: 1},
		{Name: "n2-standard-4", MachineType: "n2-standard-4", BootDiskGB: 100, QuotaUnits: 2},
		{Name: "c4-standard-4", MachineType: "c4-standard-4", BootDiskGB: 100, QuotaUnits: 2},
		{Name: "n2-standard-8", MachineType: "n2-standard-8", BootDiskGB: 200, QuotaUnits: 4},
		{Name: "c4-standard-8", MachineType: "c4-standard-8", BootDiskGB: 200, QuotaUnits: 4},
		{Name: "n2-standard-16", MachineType: "n2-standard-16", BootDiskGB: 400, QuotaUnits: 8},
		{Name: "c4-standard-16", MachineType: "c4-standard-16", BootDiskGB: 400, QuotaUnits: 8},
		{Name: "n2-standard-64", MachineType: "n2-standard-64", BootDiskGB: 1000, QuotaUnits: 32},
	}

	cfg := hub.ComputeConfig{
		Provider:          "gce",
		Project:           "test-proj",
		Region:            "us-central1",
		Zones:             []string{"us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"},
		Network:           "grain-reg-net",
		Subnetwork:        "grain-reg-subnet",
		VMPrefix:          "grain-vm-",
		SAPrefix:          "grain-sa-",
		StartupScriptFile: startupPath,
		DefaultShape:      "n2-standard-4",
		Shapes:            shapes,
		RegistryID:        "grain.example.com",
	}

	comp, err := NewGCECompute(ctx, cfg,
		WithComputeBaseURL(srv.URL+"/compute/v1"),
		WithComputeHTTPClient(srv.Client()),
	)
	if err != nil {
		t.Fatalf("NewGCECompute: %v", err)
	}

	cases := []struct {
		entryName    string
		shape        hub.Shape
		customDiskGB int
		wantDiskGB   float64
		wantDiskType string
	}{
		{
			entryName:    "n2-default-vm",
			shape:        shapes[2], // n2-standard-4 (100 GB default)
			customDiskGB: 0,
			wantDiskGB:   100,
			wantDiskType: "zones/us-central1-a/diskTypes/pd-balanced",
		},
		{
			entryName:    "n2-custom-disk",
			shape:        shapes[2], // n2-standard-4 overridden to 150 GB
			customDiskGB: 150,
			wantDiskGB:   150,
			wantDiskType: "zones/us-central1-a/diskTypes/pd-balanced",
		},
		{
			entryName:    "c4-vm",
			shape:        shapes[5], // c4-standard-8 (200 GB default -> hyperdisk-balanced)
			customDiskGB: 0,
			wantDiskGB:   200,
			wantDiskType: "zones/us-central1-a/diskTypes/hyperdisk-balanced",
		},
		{
			entryName:    "n2-fleet-64",
			shape:        shapes[8], // n2-standard-64 (1000 GB default)
			customDiskGB: 0,
			wantDiskGB:   1000,
			wantDiskType: "zones/us-central1-a/diskTypes/pd-balanced",
		},
	}

	for _, tc := range cases {
		t.Run(tc.entryName, func(t *testing.T) {
			handle, err := comp.CreateInstance(ctx, hub.InstanceSpec{
				Name:           tc.entryName,
				Zone:           "us-central1-a",
				Shape:          tc.shape,
				BootDiskGB:     tc.customDiskGB,
				ServiceAccount: "grain-sa-" + tc.entryName + "@test-proj.iam.gserviceaccount.com",
				Generation:     1,
				StartupEnv: map[string]string{
					"GRAIN_ENTRY_NAME": tc.entryName,
				},
			})
			if err != nil {
				t.Fatalf("CreateInstance(%s): %v", tc.entryName, err)
			}
			if handle.InstanceID != "grain-vm-"+tc.entryName {
				t.Fatalf("InstanceID = %q, want %q", handle.InstanceID, "grain-vm-"+tc.entryName)
			}

			state.mu.Lock()
			raw := state.instances["test-proj/us-central1-a/grain-vm-"+tc.entryName]
			state.mu.Unlock()
			if raw == nil {
				t.Fatalf("expected GCE instance payload to be recorded for %s", tc.entryName)
			}

			adv, _ := raw["advancedMachineFeatures"].(map[string]any)
			if adv == nil || adv["enableNestedVirtualization"] != true {
				t.Fatalf("expected advancedMachineFeatures.enableNestedVirtualization=true, got %+v", adv)
			}

			wantMT := "zones/us-central1-a/machineTypes/" + tc.shape.MachineType
			if raw["machineType"] != wantMT {
				t.Fatalf("machineType = %v, want %q", raw["machineType"], wantMT)
			}

			disks, _ := raw["disks"].([]any)
			if len(disks) != 1 {
				t.Fatalf("disks len = %d, want 1", len(disks))
			}
			disk0, _ := disks[0].(map[string]any)
			initParams, _ := disk0["initializeParams"].(map[string]any)
			if initParams["diskSizeGb"] != tc.wantDiskGB {
				t.Fatalf("diskSizeGb = %v, want %v", initParams["diskSizeGb"], tc.wantDiskGB)
			}
			if initParams["diskType"] != tc.wantDiskType {
				t.Fatalf("diskType = %v, want %q", initParams["diskType"], tc.wantDiskType)
			}

			nics, _ := raw["networkInterfaces"].([]any)
			nic0, _ := nics[0].(map[string]any)
			if nic0["network"] != "projects/test-proj/global/networks/grain-reg-net" {
				t.Fatalf("network = %v, want expanded network URL", nic0["network"])
			}
			if nic0["subnetwork"] != "projects/test-proj/regions/us-central1/subnetworks/grain-reg-subnet" {
				t.Fatalf("subnetwork = %v, want expanded subnetwork URL", nic0["subnetwork"])
			}
			if tc.shape.MachineType == "c4-standard-8" && nic0["nicType"] != "GVNIC" {
				t.Fatalf("nicType = %v, want GVNIC for c4 machine family", nic0["nicType"])
			}

			meta, _ := raw["metadata"].(map[string]any)
			items, _ := meta["items"].([]any)
			foundStartup := false
			for _, it := range items {
				m, _ := it.(map[string]any)
				if m["key"] == "startup-script" && m["value"] == startupBody {
					foundStartup = true
				}
			}
			if !foundStartup {
				t.Fatalf("expected startup-script metadata item on instance %s, got %+v", tc.entryName, items)
			}
		})
	}
}

func TestValidateMachineType(t *testing.T) {
	valid := []string{
		"n2-standard-2",
		"n2-standard-4",
		"n2-standard-8",
		"n2-standard-16",
		"n2-standard-64",
		"c4-standard-2",
		"c4-standard-4",
		"c4-standard-8",
		"c4-standard-16",
		"c4-standard-48",
		"c4-standard-96",
		"zones/us-central1-a/machineTypes/n2-standard-8",
	}
	for _, mt := range valid {
		if err := ValidateMachineType(mt); err != nil {
			t.Errorf("ValidateMachineType(%q) unexpected error: %v", mt, err)
		}
	}

	invalid := []string{
		"",
		"../bad",
		"unknown_type",
		"n2-standard-",
		"c4-invalid-4",
	}
	for _, mt := range invalid {
		err := ValidateMachineType(mt)
		if err == nil || !strings.Contains(err.Error(), "machine_type") {
			t.Errorf("ValidateMachineType(%q) = %v, want machine_type error", mt, err)
		}
	}

	// E2 machine types cannot run nested virtualization, which every hosted VM
	// is created with, so they are refused with an error naming N2 / C4.
	noNestedVirt := []string{
		"e2-standard-2",
		"e2-standard-4",
		"e2-standard-8",
		"e2-highmem-4",
		"e2-micro",
		"e2-small",
		"e2-medium",
		"zones/us-central1-a/machineTypes/e2-standard-4",
	}
	for _, mt := range noNestedVirt {
		err := ValidateMachineType(mt)
		if !errors.Is(err, ErrNoNestedVirtualization) {
			t.Errorf("ValidateMachineType(%q) = %v, want ErrNoNestedVirtualization", mt, err)
			continue
		}
		if !strings.Contains(err.Error(), "N2 or C4") {
			t.Errorf("ValidateMachineType(%q) error %q does not point at N2 / C4 machine types", mt, err)
		}
	}
}

func TestGCEComputeRejectsE2Shapes(t *testing.T) {
	srv, state := newFakeGCEServer(t)
	ctx := context.Background()
	base := hub.ComputeConfig{
		Provider:   "gce",
		Project:    "test-proj",
		Region:     "us-central1",
		Zones:      []string{"us-central1-a"},
		VMPrefix:   "grain-vm-",
		RegistryID: "grain.example.com",
	}

	withE2 := base
	withE2.Shapes = []hub.Shape{{Name: "small", MachineType: "e2-standard-4", BootDiskGB: 50, QuotaUnits: 1}}
	if _, err := NewGCECompute(ctx, withE2, WithComputeBaseURL(srv.URL+"/compute/v1"), WithComputeHTTPClient(srv.Client())); !errors.Is(err, ErrNoNestedVirtualization) {
		t.Fatalf("NewGCECompute with an e2 shape = %v, want ErrNoNestedVirtualization", err)
	}

	comp, err := NewGCECompute(ctx, base, WithComputeBaseURL(srv.URL+"/compute/v1"), WithComputeHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewGCECompute: %v", err)
	}
	_, err = comp.CreateInstance(ctx, hub.InstanceSpec{
		Name:           "legacy-e2",
		Zone:           "us-central1-a",
		Shape:          hub.Shape{Name: "legacy", MachineType: "e2-standard-4", BootDiskGB: 50},
		ServiceAccount: "grain-sa-legacy-e2@test-proj.iam.gserviceaccount.com",
		Generation:     1,
	})
	if !errors.Is(err, ErrNoNestedVirtualization) {
		t.Fatalf("CreateInstance on e2-standard-4 = %v, want ErrNoNestedVirtualization", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if raw := state.instances["test-proj/us-central1-a/grain-vm-legacy-e2"]; raw != nil {
		t.Fatalf("an e2 instance reached the compute API: %+v", raw)
	}
}

func TestGCEComputeInsertWithUnpropagatedServiceAccountIsRetryable(t *testing.T) {
	srv, state := newFakeGCEServer(t)
	ctx := context.Background()

	comp, err := NewGCECompute(ctx, hub.ComputeConfig{
		Provider:   "gce",
		Project:    "test-proj",
		Region:     "us-central1",
		Zones:      []string{"us-central1-a"},
		VMPrefix:   "grain-vm-",
		RegistryID: "grain.example.com",
	},
		WithComputeBaseURL(srv.URL+"/compute/v1"),
		WithComputeHTTPClient(srv.Client()),
	)
	if err != nil {
		t.Fatalf("NewGCECompute: %v", err)
	}

	const saEmail = "gi-hub-e2e-test1@test-proj.iam.gserviceaccount.com"
	spec := hub.InstanceSpec{
		Name:           "hub-e2e-test1",
		Zone:           "us-central1-a",
		Shape:          hub.Shape{Name: "n2-standard-2", MachineType: "n2-standard-2", BootDiskGB: 50},
		ServiceAccount: saEmail,
		Generation:     1,
	}

	// Compute has not seen the just-created account yet: the insert operation fails once.
	state.mu.Lock()
	state.insertOpErrors = []fakeGCEOpError{{
		Code:    "EXTERNAL_RESOURCE_NOT_FOUND",
		Message: "The resource '" + saEmail + "' of type 'serviceAccount' was not found.",
	}}
	state.mu.Unlock()

	_, err = comp.CreateInstance(ctx, spec)
	if err == nil {
		t.Fatal("CreateInstance succeeded, want the EXTERNAL_RESOURCE_NOT_FOUND failure")
	}
	if !errors.Is(err, hub.ErrServiceAccountNotReady) {
		t.Fatalf("CreateInstance error = %v, want it to wrap registry.ErrServiceAccountNotReady", err)
	}
	if !strings.Contains(err.Error(), "EXTERNAL_RESOURCE_NOT_FOUND") || !strings.Contains(err.Error(), saEmail) {
		t.Fatalf("CreateInstance error = %v, want it to keep GCE's code and the account name", err)
	}
	state.mu.Lock()
	_, leftBehind := state.instances["test-proj/us-central1-a/grain-vm-hub-e2e-test1"]
	state.mu.Unlock()
	if leftBehind {
		t.Fatal("failed insert left an instance behind")
	}

	// Once the account has propagated, the retried insert goes through.
	handle, err := comp.CreateInstance(ctx, spec)
	if err != nil {
		t.Fatalf("retried CreateInstance: %v", err)
	}
	if handle.InstanceID != "grain-vm-hub-e2e-test1" || handle.InternalIP == "" {
		t.Fatalf("retried CreateInstance handle = %+v", handle)
	}
	state.mu.Lock()
	attempts := state.insertAttempts
	state.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("insert attempts = %d, want 2", attempts)
	}
}

func TestGCEComputeInsertFailuresThatAreNotServiceAccountPropagationAreNotRetryable(t *testing.T) {
	srv, state := newFakeGCEServer(t)
	ctx := context.Background()

	comp, err := NewGCECompute(ctx, hub.ComputeConfig{
		Provider:   "gce",
		Project:    "test-proj",
		Region:     "us-central1",
		Zones:      []string{"us-central1-a"},
		RegistryID: "grain.example.com",
	},
		WithComputeBaseURL(srv.URL+"/compute/v1"),
		WithComputeHTTPClient(srv.Client()),
	)
	if err != nil {
		t.Fatalf("NewGCECompute: %v", err)
	}

	state.mu.Lock()
	state.insertOpErrors = []fakeGCEOpError{{
		Code:    "ZONE_RESOURCE_POOL_EXHAUSTED",
		Message: "The zone 'projects/test-proj/zones/us-central1-a' does not have enough resources available to fulfill the request.",
	}}
	state.mu.Unlock()

	_, err = comp.CreateInstance(ctx, hub.InstanceSpec{
		Name:           "stockout-vm",
		Zone:           "us-central1-a",
		Shape:          hub.Shape{Name: "n2-standard-2", MachineType: "n2-standard-2"},
		ServiceAccount: "gi-stockout-vm@test-proj.iam.gserviceaccount.com",
		Generation:     1,
	})
	if err == nil {
		t.Fatal("CreateInstance succeeded, want the stockout failure")
	}
	if errors.Is(err, hub.ErrServiceAccountNotReady) {
		t.Fatalf("CreateInstance error = %v, must not be classified as service account propagation", err)
	}
}

func TestIsServiceAccountNotFound(t *testing.T) {
	const sa = "gi-alice@test-proj.iam.gserviceaccount.com"
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"operation error", "GCE operation op-1 failed (EXTERNAL_RESOURCE_NOT_FOUND): The resource '" + sa + "' of type 'serviceAccount' was not found.", true},
		{"synchronous 404 naming the account", `HTTP 400: {"error":{"code":400,"message":"The resource 'projects/test-proj/serviceAccounts/` + sa + `' was not found","errors":[{"reason":"notFound"}]}}`, true},
		{"another account", "GCE operation op-1 failed (EXTERNAL_RESOURCE_NOT_FOUND): The resource 'other@test-proj.iam.gserviceaccount.com' of type 'serviceAccount' was not found.", true},
		{"missing image", "GCE operation op-1 failed (RESOURCE_NOT_FOUND): The resource 'projects/debian-cloud/global/images/nope' was not found", false},
		{"quota", "GCE operation op-1 failed (QUOTA_EXCEEDED): Quota 'CPUS' exceeded.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isServiceAccountNotFound(errors.New(tc.msg), sa); got != tc.want {
				t.Fatalf("isServiceAccountNotFound(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}
