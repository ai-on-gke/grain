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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
)

type fakeGCEState struct {
	mu        sync.Mutex
	instances map[string]map[string]any // key: project/zone/name
	disks     map[string]map[string]any // key: project/zone/name
	// insertOpErrors queues failed-operation responses for instances.insert: each POST pops
	// the first one and answers with a DONE operation carrying that error instead of creating
	// the instance.
	insertOpErrors []fakeGCEOpError
	insertAttempts int
	// canActAs, when set, is GCE's iam.serviceAccounts.actAs check: an insert whose
	// service account it rejects fails with SERVICE_ACCOUNT_ACCESS_DENIED, as GCE does
	// when the caller lacks roles/iam.serviceAccountUser on that account.
	canActAs func(saEmail string) bool
}

type fakeGCEOpError struct {
	Code    string
	Message string
}

func newFakeGCEServer(t *testing.T) (*httptest.Server, *fakeGCEState) {
	t.Helper()
	st := &fakeGCEState{
		instances: make(map[string]map[string]any),
		disks:     make(map[string]map[string]any),
	}
	doneOp := map[string]any{
		"kind":   "compute#operation",
		"name":   "op-1",
		"status": "DONE",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		// Expected prefix: compute / v1 / projects / {proj} / zones / {zone} / ...
		if len(parts) < 7 || parts[0] != "compute" || parts[2] != "projects" || parts[4] != "zones" {
			http.NotFound(w, r)
			return
		}
		proj := parts[3]
		zone := parts[5]
		resource := parts[6]

		switch resource {
		case "operations":
			_ = json.NewEncoder(w).Encode(doneOp)
			return

		case "instances":
			if len(parts) == 7 {
				switch r.Method {
				case http.MethodPost:
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					name, _ := body["name"].(string)
					key := proj + "/" + zone + "/" + name
					st.insertAttempts++
					if len(st.insertOpErrors) > 0 {
						opErr := st.insertOpErrors[0]
						st.insertOpErrors = st.insertOpErrors[1:]
						_ = json.NewEncoder(w).Encode(map[string]any{
							"kind":   "compute#operation",
							"name":   "op-failed-insert",
							"status": "DONE",
							"error": map[string]any{
								"errors": []any{map[string]any{"code": opErr.Code, "message": opErr.Message}},
							},
						})
						return
					}
					if st.canActAs != nil {
						saEmail := ""
						if sas, ok := body["serviceAccounts"].([]any); ok && len(sas) > 0 {
							if sa, ok := sas[0].(map[string]any); ok {
								saEmail, _ = sa["email"].(string)
							}
						}
						if !st.canActAs(saEmail) {
							_ = json.NewEncoder(w).Encode(map[string]any{
								"kind":   "compute#operation",
								"name":   "op-denied-insert",
								"status": "DONE",
								"error": map[string]any{
									"errors": []any{map[string]any{
										"code":    "SERVICE_ACCOUNT_ACCESS_DENIED",
										"message": "The user does not have access to service account '" + saEmail + "'. User: 'grain-hub/grain-registry (test-proj.svc.id.goog)'. Ask a project owner to grant you the iam.serviceAccountUser role on the service account.",
									}},
								},
							})
							return
						}
					}
					if _, exists := st.instances[key]; exists {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(map[string]any{
							"error": map[string]any{"code": 409, "message": "already exists"},
						})
						return
					}
					body["status"] = "RUNNING"
					if nics, ok := body["networkInterfaces"].([]any); ok && len(nics) > 0 {
						if nic0, ok := nics[0].(map[string]any); ok {
							nic0["networkIP"] = "10.128.0.42"
						}
					}
					st.instances[key] = body
					_ = json.NewEncoder(w).Encode(doneOp)
					return

				case http.MethodGet:
					var items []any
					prefix := proj + "/" + zone + "/"
					for k, inst := range st.instances {
						if strings.HasPrefix(k, prefix) {
							items = append(items, inst)
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
					return
				}
			}

			if len(parts) >= 8 {
				name := parts[7]
				key := proj + "/" + zone + "/" + name
				if len(parts) == 9 && parts[8] == "stop" && r.Method == http.MethodPost {
					inst, ok := st.instances[key]
					if !ok {
						w.WriteHeader(http.StatusNotFound)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 404}})
						return
					}
					inst["status"] = "TERMINATED"
					_ = json.NewEncoder(w).Encode(doneOp)
					return
				}
				switch r.Method {
				case http.MethodGet:
					inst, ok := st.instances[key]
					if !ok {
						w.WriteHeader(http.StatusNotFound)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 404}})
						return
					}
					_ = json.NewEncoder(w).Encode(inst)
					return
				case http.MethodDelete:
					if _, ok := st.instances[key]; !ok {
						w.WriteHeader(http.StatusNotFound)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 404}})
						return
					}
					delete(st.instances, key)
					_ = json.NewEncoder(w).Encode(doneOp)
					return
				}
			}

		case "disks":
			if len(parts) == 7 && r.Method == http.MethodGet {
				var items []any
				prefix := proj + "/" + zone + "/"
				for k, d := range st.disks {
					if strings.HasPrefix(k, prefix) {
						items = append(items, d)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
				return
			}
			if len(parts) == 8 && r.Method == http.MethodDelete {
				key := proj + "/" + zone + "/" + parts[7]
				delete(st.disks, key)
				_ = json.NewEncoder(w).Encode(doneOp)
				return
			}
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func TestGCEComputeProvisionScopeAndLifecycle(t *testing.T) {
	srv, state := newFakeGCEServer(t)
	ctx := context.Background()

	cfg := hub.ComputeConfig{
		Provider:   "gce",
		Project:    "test-proj",
		Region:     "us-central1",
		Zones:      []string{"us-central1-a", "us-central1-b"},
		Network:    "grain-vpc",
		Subnetwork: "grain-subnet",
		VMPrefix:   "grain-vm-",
		SAPrefix:   "grain-sa-",
		BootImage:  "projects/test-proj/global/images/family/grain-base",
		RegistryID: "grain.example.com",
	}

	comp, err := NewGCECompute(ctx, cfg,
		WithComputeBaseURL(srv.URL+"/compute/v1"),
		WithComputeHTTPClient(srv.Client()),
	)
	if err != nil {
		t.Fatalf("NewGCECompute: %v", err)
	}

	// 1. Out-of-scope checks: wrong zone, invalid entry name.
	_, err = comp.CreateInstance(ctx, hub.InstanceSpec{
		Name: "alice-vm",
		Zone: "europe-west1-b",
		Shape: hub.Shape{
			Name:        "medium",
			MachineType: "n2-standard-8",
			BootDiskGB:  100,
		},
		Generation: 1,
	})
	if !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("expected ErrOutOfScope for wrong zone, got %v", err)
	}

	// 2. CreateInstance creates a Shielded VM with no external IP, OS Login, grain-instance tag, and HTTP backend URL.
	spec := hub.InstanceSpec{
		Name: "alice-vm",
		Zone: "us-central1-a",
		Shape: hub.Shape{
			Name:         "medium",
			MachineType:  "n2-standard-8",
			BootDiskGB:   100,
			BootDiskIOPS: 3000,
		},
		ServiceAccount: "grain-sa-alice-vm@test-proj.iam.gserviceaccount.com",
		Generation:     1,
		StartupEnv: map[string]string{
			"GRAIN_ENTRY_NAME": "alice-vm",
		},
	}
	handle, err := comp.CreateInstance(ctx, spec)
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if handle.InstanceID != "grain-vm-alice-vm" || handle.InternalIP != "10.128.0.42" || handle.BackendURL != "http://10.128.0.42:8080" {
		t.Fatalf("unexpected InstanceHandle: %+v", handle)
	}

	// Inspect raw recorded GCE request body.
	state.mu.Lock()
	rawInst := state.instances["test-proj/us-central1-a/grain-vm-alice-vm"]
	state.mu.Unlock()
	if rawInst == nil {
		t.Fatal("expected instance to be stored in fake GCE")
	}
	lbls, _ := rawInst["labels"].(map[string]any)
	if lbls["grain-registry"] != "grain.example.com" || lbls["grain-entry"] != "alice-vm" || lbls["grain-generation"] != "1" {
		t.Fatalf("unexpected instance labels: %+v", lbls)
	}
	nics, _ := rawInst["networkInterfaces"].([]any)
	nic0, _ := nics[0].(map[string]any)
	if _, hasAccessConfigs := nic0["accessConfigs"]; hasAccessConfigs {
		t.Fatalf("expected no accessConfigs (no external IP), got %+v", nic0)
	}
	shielded, _ := rawInst["shieldedInstanceConfig"].(map[string]any)
	if shielded["enableSecureBoot"] != true || shielded["enableVtpm"] != true || shielded["enableIntegrityMonitoring"] != true {
		t.Fatalf("expected Shielded VM flags enabled, got %+v", shielded)
	}

	// 3. Re-running CreateInstance with the same generation adopts the existing VM idempotently.
	handleAdopt, err := comp.CreateInstance(ctx, spec)
	if err != nil {
		t.Fatalf("CreateInstance adopt: %v", err)
	}
	if handleAdopt.InternalIP != "10.128.0.42" || handleAdopt.InstanceID != "grain-vm-alice-vm" {
		t.Fatalf("unexpected adopted handle: %+v", handleAdopt)
	}

	// 4. Re-running CreateInstance with a newer generation (Generation=2) deletes the stale generation 1 VM and creates generation 2.
	specGen2 := spec
	specGen2.Generation = 2
	if _, err := comp.CreateInstance(ctx, specGen2); err != nil {
		t.Fatalf("CreateInstance generation 2: %v", err)
	}
	stGen2, err := comp.GetInstanceStatus(ctx, "us-central1-a", "grain-vm-alice-vm")
	if err != nil {
		t.Fatalf("GetInstanceStatus generation 2: %v", err)
	}
	if stGen2.Labels["grain-generation"] != "2" {
		t.Fatalf("expected grain-generation=2 label after stale replacement, got %+v", stGen2.Labels)
	}

	// 5. ListInstances returns our VM and ignores VMs belonging to another registry.
	state.mu.Lock()
	state.instances["test-proj/us-central1-a/grain-vm-foreign"] = map[string]any{
		"name":   "grain-vm-foreign",
		"status": "RUNNING",
		"labels": map[string]any{
			"grain-registry": "other-registry.example.com",
			"grain-entry":    "foreign",
		},
	}
	state.mu.Unlock()

	listed, err := comp.ListInstances(ctx)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(listed) != 1 || listed[0].InstanceID != "grain-vm-alice-vm" {
		t.Fatalf("expected ListInstances to return only our registry's VM, got %+v", listed)
	}

	// Attempting to Stop or Delete the foreign VM or a non-prefixed VM must fail with ErrOutOfScope.
	if err := comp.StopInstance(ctx, "us-central1-a", "other-prefix-vm"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("expected ErrOutOfScope stopping non-prefixed VM, got %v", err)
	}
	if err := comp.StopInstance(ctx, "us-central1-a", "grain-vm-foreign"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("expected ErrOutOfScope stopping foreign VM, got %v", err)
	}
	if err := comp.DeleteInstance(ctx, "us-central1-a", "grain-vm-foreign"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("expected ErrOutOfScope deleting foreign VM, got %v", err)
	}

	// 6. StopInstance transitions VM to TERMINATED.
	if err := comp.StopInstance(ctx, "us-central1-a", "grain-vm-alice-vm"); err != nil {
		t.Fatalf("StopInstance: %v", err)
	}
	gotSt, err := comp.GetInstanceStatus(ctx, "us-central1-a", "grain-vm-alice-vm")
	if err != nil {
		t.Fatalf("GetInstanceStatus: %v", err)
	}
	if gotSt.State != "TERMINATED" {
		t.Fatalf("expected TERMINATED after StopInstance, got %s", gotSt.State)
	}

	// 7. DeleteInstance deletes the VM and sweeps any straggler disk labelled grain-entry=alice-vm.
	state.mu.Lock()
	state.disks["test-proj/us-central1-a/grain-vm-alice-vm-straggler"] = map[string]any{
		"name": "grain-vm-alice-vm-straggler",
		"labels": map[string]any{
			"grain-registry": "grain.example.com",
			"grain-entry":    "alice-vm",
		},
	}
	state.mu.Unlock()

	if err := comp.DeleteInstance(ctx, "us-central1-a", "grain-vm-alice-vm"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	state.mu.Lock()
	_, diskStillExists := state.disks["test-proj/us-central1-a/grain-vm-alice-vm-straggler"]
	state.mu.Unlock()
	if diskStillExists {
		t.Fatal("expected straggler disk labelled grain-entry=alice-vm to be swept on DeleteInstance")
	}

	// Deleting again when already gone is idempotent.
	if err := comp.DeleteInstance(ctx, "us-central1-a", "grain-vm-alice-vm"); err != nil {
		t.Fatalf("idempotent second DeleteInstance: %v", err)
	}
}

func TestServiceAccountIDForEntryBounds(t *testing.T) {
	shortID, err := ServiceAccountIDForEntry("grain-sa-", "alice")
	if err != nil {
		t.Fatalf("ServiceAccountIDForEntry short: %v", err)
	}
	if shortID != "grain-sa-alice" {
		t.Fatalf("expected grain-sa-alice, got %q", shortID)
	}

	longName1 := "twenty-five-char-name-001"
	longID1, err := ServiceAccountIDForEntry("grain-sa-", longName1)
	if err != nil {
		t.Fatalf("ServiceAccountIDForEntry long: %v", err)
	}
	if len(longID1) > 30 || len(longID1) < 6 {
		t.Fatalf("expected GSA accountId length in 6..30, got %d (%q)", len(longID1), longID1)
	}
	if !strings.HasPrefix(longID1, "grain-sa-") {
		t.Fatalf("expected GSA accountId to start with grain-sa-, got %q", longID1)
	}
	// Distinct long names with same prefix must hash to different GSA account IDs.
	longName2 := "twenty-five-char-name-002"
	longID2, err := ServiceAccountIDForEntry("grain-sa-", longName2)
	if err != nil {
		t.Fatalf("ServiceAccountIDForEntry long 2: %v", err)
	}
	if longID1 == longID2 {
		t.Fatalf("expected distinct GSA accountIds for distinct long names, both got %q", longID1)
	}
}

type fakeGCPControlPlane struct {
	mu              sync.Mutex
	serviceAccounts map[string]bool
	projectPolicy   iamPolicy
	instancePolicy  map[string]iamPolicy       // zone/instance -> policy
	sqlUsers        map[string]map[string]bool // sqlInstance -> userName -> exists
	secrets         map[string][][]byte        // secretID -> versions
	secretLabels    map[string]map[string]string
	secretPolicies  map[string]iamPolicy // secretID -> secret-level IAM policy
	managedFolders  map[string]iamPolicy
	objects         map[string][]byte

	// Service accounts' own IAM policies (email -> policy) and the knobs the
	// self-actAs tests turn: etags are "etag-<n>" and a setIamPolicy carrying a
	// stale one is answered 409, as IAM does.
	saPolicies       map[string]iamPolicy
	saPolicySerial   int
	saGetIAMCalls    int
	saSetIAMCalls    int
	saRefuseGetIAM   bool // answer serviceAccounts.getIamPolicy 403
	saRefuseSetIAM   bool // answer serviceAccounts.setIamPolicy 403
	saGetIAMNotFound int  // answer this many getIamPolicy calls 404 (account not visible yet)
	// saConcurrentWrites makes this many setIamPolicy calls lose a race: just before
	// each, another writer adds its own binding and bumps the etag.
	saConcurrentWrites int
}

// canActAs reports whether principal holds roles/iam.serviceAccountUser on saEmail
// through that account's own IAM policy.
func (cp *fakeGCPControlPlane) canActAs(principal, saEmail string) bool {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if !cp.serviceAccounts[saEmail] {
		return false
	}
	pol := cp.saPolicies[saEmail]
	return pol.hasUnconditionalMember(roleServiceAccountUser, principal)
}

func (cp *fakeGCPControlPlane) nextSAEtag() string {
	cp.saPolicySerial++
	return fmt.Sprintf("etag-%d", cp.saPolicySerial)
}

func newFakeGCPControlPlaneServer(t *testing.T) (*httptest.Server, *fakeGCPControlPlane) {
	t.Helper()
	cp := &fakeGCPControlPlane{
		serviceAccounts: make(map[string]bool),
		instancePolicy:  make(map[string]iamPolicy),
		sqlUsers:        make(map[string]map[string]bool),
		secrets:         make(map[string][][]byte),
		secretLabels:    make(map[string]map[string]string),
		secretPolicies:  make(map[string]iamPolicy),
		managedFolders:  make(map[string]iamPolicy),
		objects:         make(map[string][]byte),
		saPolicies:      make(map[string]iamPolicy),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path

		// 0. A service account's own IAM policy: /v1/projects/{p}/serviceAccounts/{email}:getIamPolicy | :setIamPolicy
		if strings.HasPrefix(path, "/v1/projects/") && strings.Contains(path, "/serviceAccounts/") &&
			(strings.HasSuffix(path, ":getIamPolicy") || strings.HasSuffix(path, ":setIamPolicy")) && r.Method == http.MethodPost {
			colon := strings.LastIndex(path, ":")
			email, method := path[strings.LastIndex(path, "/")+1:colon], path[colon+1:]
			writeErr := func(code int, status, msg string) {
				w.WriteHeader(code)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "status": status, "message": msg}})
			}
			if method == "getIamPolicy" {
				cp.saGetIAMCalls++
				if cp.saRefuseGetIAM {
					writeErr(http.StatusForbidden, "PERMISSION_DENIED", "Permission 'iam.serviceAccounts.getIamPolicy' denied on resource (or it may not exist).")
					return
				}
				if cp.saGetIAMNotFound > 0 || !cp.serviceAccounts[email] {
					if cp.saGetIAMNotFound > 0 {
						cp.saGetIAMNotFound--
					}
					writeErr(http.StatusNotFound, "NOT_FOUND", "Unknown service account")
					return
				}
				pol, ok := cp.saPolicies[email]
				if !ok {
					pol = iamPolicy{Etag: cp.nextSAEtag()}
					cp.saPolicies[email] = pol
				}
				_ = json.NewEncoder(w).Encode(pol)
				return
			}
			cp.saSetIAMCalls++
			if cp.saRefuseSetIAM {
				writeErr(http.StatusForbidden, "PERMISSION_DENIED", "Permission 'iam.serviceAccounts.setIamPolicy' denied on resource (or it may not exist).")
				return
			}
			if !cp.serviceAccounts[email] {
				writeErr(http.StatusNotFound, "NOT_FOUND", "Unknown service account")
				return
			}
			if cp.saConcurrentWrites > 0 {
				cp.saConcurrentWrites--
				cur := cp.saPolicies[email]
				cur.Bindings = append(cur.Bindings, iamBinding{
					Role:    "roles/iam.serviceAccountTokenCreator",
					Members: []string{fmt.Sprintf("user:concurrent-%d@example.com", cp.saPolicySerial)},
				})
				cur.Etag = cp.nextSAEtag()
				cp.saPolicies[email] = cur
			}
			var req struct {
				Policy iamPolicy `json:"policy"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Policy.Etag != cp.saPolicies[email].Etag {
				writeErr(http.StatusConflict, "ABORTED", "There were concurrent policy changes. Please retry the whole read-modify-write with exponential backoff.")
				return
			}
			req.Policy.Etag = cp.nextSAEtag()
			cp.saPolicies[email] = req.Policy
			_ = json.NewEncoder(w).Encode(req.Policy)
			return
		}

		// 1. IAM Service Accounts: /v1/projects/{p}/serviceAccounts...
		if strings.HasPrefix(path, "/v1/projects/") && strings.Contains(path, "/serviceAccounts") {
			if strings.HasSuffix(path, "/serviceAccounts") && r.Method == http.MethodPost {
				var req struct {
					AccountID string `json:"accountId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				proj := strings.Split(strings.TrimPrefix(path, "/v1/projects/"), "/")[0]
				email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", req.AccountID, proj)
				cp.serviceAccounts[email] = true
				_ = json.NewEncoder(w).Encode(map[string]any{"email": email})
				return
			}
			email := path[strings.LastIndex(path, "/")+1:]
			switch r.Method {
			case http.MethodGet:
				if !cp.serviceAccounts[email] {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"email": email})
				return
			case http.MethodDelete:
				delete(cp.serviceAccounts, email)
				// The account's own policy, and so any actAs binding on it, goes with it.
				delete(cp.saPolicies, email)
				_ = json.NewEncoder(w).Encode(map[string]any{})
				return
			}
		}

		// 1b. Secret Manager secret-level IAM policy: /v1/projects/{p}/secrets/{s}:setIamPolicy
		if strings.HasPrefix(path, "/v1/projects/") && strings.Contains(path, "/secrets/") && strings.HasSuffix(path, ":setIamPolicy") {
			trimmed := strings.TrimSuffix(path, ":setIamPolicy")
			secretID := trimmed[strings.LastIndex(trimmed, "/")+1:]
			if _, ok := cp.secrets[secretID]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var req struct {
				Policy iamPolicy `json:"policy"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			cp.secretPolicies[secretID] = req.Policy
			_ = json.NewEncoder(w).Encode(req.Policy)
			return
		}

		// 2. Cloud Resource Manager project IAM policy: /v1/projects/{p}:getIamPolicy | :setIamPolicy
		if strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, ":getIamPolicy") {
			_ = json.NewEncoder(w).Encode(cp.projectPolicy)
			return
		}
		if strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, ":setIamPolicy") {
			var req struct {
				Policy iamPolicy `json:"policy"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			cp.projectPolicy = req.Policy
			_ = json.NewEncoder(w).Encode(cp.projectPolicy)
			return
		}

		// 2b. Compute Instance IAM policy: /compute/v1/projects/{p}/zones/{z}/instances/{i}/getIamPolicy | setIamPolicy
		if strings.HasPrefix(path, "/compute/v1/projects/") && strings.HasSuffix(path, "/getIamPolicy") {
			_ = json.NewEncoder(w).Encode(cp.instancePolicy[path])
			return
		}
		if strings.HasPrefix(path, "/compute/v1/projects/") && strings.HasSuffix(path, "/setIamPolicy") {
			var pol iamPolicy
			_ = json.NewDecoder(r.Body).Decode(&pol)
			getKey := strings.TrimSuffix(path, "/setIamPolicy") + "/getIamPolicy"
			cp.instancePolicy[getKey] = pol
			_ = json.NewEncoder(w).Encode(pol)
			return
		}

		// 3. Cloud SQL Admin API: /sql/v1beta4/projects/{p}/instances/{inst}/users | /databases
		if strings.HasPrefix(path, "/sql/v1beta4/projects/") && strings.Contains(path, "/users") {
			parts := strings.Split(strings.TrimPrefix(path, "/sql/v1beta4/projects/"), "/")
			inst := parts[2]
			if cp.sqlUsers[inst] == nil {
				cp.sqlUsers[inst] = make(map[string]bool)
			}
			switch r.Method {
			case http.MethodPost:
				var req struct {
					Name string `json:"name"`
					Type string `json:"type"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				cp.sqlUsers[inst][req.Name] = true
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "DONE"})
				return
			case http.MethodDelete:
				name := r.URL.Query().Get("name")
				delete(cp.sqlUsers[inst], name)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "DONE"})
				return
			}
		}
		if strings.HasPrefix(path, "/sql/v1beta4/projects/") && strings.Contains(path, "/databases") {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "DONE"})
			return
		}

		// 4. Secret Manager: /v1/projects/{p}/secrets...
		if strings.HasPrefix(path, "/v1/projects/") && strings.Contains(path, "/secrets") {
			if strings.HasSuffix(path, "/secrets") {
				switch r.Method {
				case http.MethodPost:
					secretID := r.URL.Query().Get("secretId")
					var createReq struct {
						Labels map[string]string `json:"labels"`
					}
					_ = json.NewDecoder(r.Body).Decode(&createReq)
					if _, exists := cp.secrets[secretID]; exists {
						w.WriteHeader(http.StatusConflict)
						return
					}
					cp.secrets[secretID] = nil
					cp.secretLabels[secretID] = createReq.Labels
					_ = json.NewEncoder(w).Encode(map[string]any{"name": path + "/" + secretID, "labels": createReq.Labels})
					return
				case http.MethodGet:
					proj := strings.Split(strings.TrimPrefix(path, "/v1/projects/"), "/")[0]
					var list []map[string]any
					for id := range cp.secrets {
						list = append(list, map[string]any{
							"name":   fmt.Sprintf("projects/%s/secrets/%s", proj, id),
							"labels": cp.secretLabels[id],
						})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"secrets": list})
					return
				}
			}
			if strings.HasSuffix(path, ":addVersion") && r.Method == http.MethodPost {
				trimmed := strings.TrimSuffix(path, ":addVersion")
				secretID := trimmed[strings.LastIndex(trimmed, "/")+1:]
				var req struct {
					Payload struct {
						Data []byte `json:"data"`
					} `json:"payload"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				cp.secrets[secretID] = append(cp.secrets[secretID], req.Payload.Data)
				ver := len(cp.secrets[secretID])
				_ = json.NewEncoder(w).Encode(map[string]any{
					"name": fmt.Sprintf("%s/versions/%d", trimmed, ver),
				})
				return
			}
			if r.Method == http.MethodDelete {
				secretID := path[strings.LastIndex(path, "/")+1:]
				delete(cp.secrets, secretID)
				_ = json.NewEncoder(w).Encode(map[string]any{})
				return
			}
		}

		// 5. GCS Managed Folders and Objects: /storage/v1/b/{bucket}/...
		if strings.HasPrefix(path, "/storage/v1/b/") {
			rest := strings.TrimPrefix(path, "/storage/v1/b/")
			parts := strings.SplitN(rest, "/", 2)
			bucket := parts[0]
			sub := ""
			if len(parts) > 1 {
				sub = parts[1]
			}
			if sub == "managedFolders" && r.Method == http.MethodPost {
				var req struct {
					Name string `json:"name"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				key := bucket + "/" + req.Name
				if _, exists := cp.managedFolders[key]; exists {
					w.WriteHeader(http.StatusConflict)
					return
				}
				cp.managedFolders[key] = iamPolicy{}
				_ = json.NewEncoder(w).Encode(map[string]any{"name": req.Name})
				return
			}
			if strings.HasPrefix(sub, "managedFolders/") && strings.HasSuffix(sub, "/iam") {
				folder := strings.TrimSuffix(strings.TrimPrefix(sub, "managedFolders/"), "/iam")
				folder, _ = url.PathUnescape(folder)
				key := bucket + "/" + folder
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(cp.managedFolders[key])
					return
				}
				if r.Method == http.MethodPut {
					var pol iamPolicy
					_ = json.NewDecoder(r.Body).Decode(&pol)
					cp.managedFolders[key] = pol
					_ = json.NewEncoder(w).Encode(pol)
					return
				}
			}
			if strings.HasPrefix(sub, "managedFolders/") && r.Method == http.MethodDelete {
				folder := strings.TrimPrefix(sub, "managedFolders/")
				folder, _ = url.PathUnescape(folder)
				delete(cp.managedFolders, bucket+"/"+folder)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if sub == "o" && r.Method == http.MethodGet {
				prefix := r.URL.Query().Get("prefix")
				var items []map[string]any
				for k := range cp.objects {
					if strings.HasPrefix(k, bucket+"/"+prefix) {
						objName := strings.TrimPrefix(k, bucket+"/")
						items = append(items, map[string]any{"name": objName})
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
				return
			}
			if strings.HasPrefix(sub, "o/") && r.Method == http.MethodDelete {
				objName, _ := url.PathUnescape(strings.TrimPrefix(sub, "o/"))
				delete(cp.objects, bucket+"/"+objName)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		body, _ := io.ReadAll(r.Body)
		t.Logf("unhandled fake GCP request: %s %s (%s)", r.Method, path, string(body))
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, cp
}

func startLocalPostgresForGCPTest(t *testing.T) (dsnTemplate string, sockDir string, adminDB *sql.DB) {
	t.Helper()
	findBin := func(name string) string {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		matches, _ := filepath.Glob(filepath.Join("/usr/lib/postgresql", "*", "bin", name))
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[len(matches)-1]
		}
		return ""
	}
	initdbBin := findBin("initdb")
	pgCtlBin := findBin("pg_ctl")
	if initdbBin == "" || pgCtlBin == "" {
		if os.Getenv("GRAIN_REQUIRE_POSTGRES") != "" {
			t.Fatal("postgresql binaries (initdb/pg_ctl) not found")
		}
		t.Skip("skipping postgres test: initdb/pg_ctl not found")
	}

	dir, err := os.MkdirTemp("/tmp", "grain-gcp-pg-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	dataDir := filepath.Join(dir, "data")
	sockDir = filepath.Join(dir, "sock")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	asPostgres := os.Geteuid() == 0
	if asPostgres {
		if pgUser, err := user.Lookup("postgres"); err == nil {
			_ = exec.Command("chown", "-R", pgUser.Username, dir).Run()
		} else {
			asPostgres = false
		}
	}
	runCmd := func(bin string, args ...string) error {
		var cmd *exec.Cmd
		if asPostgres {
			cmd = exec.Command("runuser", append([]string{"-u", "postgres", "--", bin}, args...)...)
		} else {
			cmd = exec.Command(bin, args...)
		}
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %v: %w (%s)", bin, args, err, strings.TrimSpace(out.String()))
		}
		return nil
	}

	if err := runCmd(initdbBin, "-D", dataDir, "-U", "postgres", "-A", "trust", "--no-instructions", "--encoding=UTF8", "--locale=C"); err != nil {
		t.Fatalf("initdb: %v", err)
	}
	pgOpts := fmt.Sprintf("-F -c listen_addresses= -c unix_socket_directories=%s -c max_connections=100", sockDir)
	if err := runCmd(pgCtlBin, "-D", dataDir, "-l", filepath.Join(dir, "postgres.log"), "-o", pgOpts, "-w", "start"); err != nil {
		t.Fatalf("pg_ctl start: %v", err)
	}

	t.Cleanup(func() {
		if adminDB != nil {
			_ = adminDB.Close()
		}
		_ = runCmd(pgCtlBin, "-D", dataDir, "-m", "immediate", "-w", "stop")
		_ = os.RemoveAll(dir)
	})

	dsnTemplate = fmt.Sprintf("postgres://postgres@localhost/{database}?host=%s&sslmode=disable", url.QueryEscape(sockDir))
	adminDSN := strings.ReplaceAll(dsnTemplate, "{database}", "postgres")
	adminDB, err = sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("sql.Open admin: %v", err)
	}
	return dsnTemplate, sockDir, adminDB
}

func TestGCPInstanceResourcesAndPostgresIsolation(t *testing.T) {
	dsnTemplate, sockDir, adminDB := startLocalPostgresForGCPTest(t)
	ctx := context.Background()

	// Create registry store database on the same Postgres server.
	if _, err := adminDB.ExecContext(ctx, `CREATE DATABASE registry_meta`); err != nil {
		t.Fatalf("CREATE DATABASE registry_meta: %v", err)
	}
	regDSN := strings.ReplaceAll(dsnTemplate, "{database}", "registry_meta")
	st, err := store.Open(ctx, regDSN, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	// Seed two SQL instances in the pool to verify least-loaded placement and draining behavior.
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 2},
		{Name: "sql-pool-02", ConnectionName: "test-proj:us-central1:sql-pool-02", PrivateIP: "10.8.0.6", MaxDatabases: 2},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}
	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "medium", MachineType: "n2-standard-8", BootDiskGB: 100, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}

	srv, cp := newFakeGCPControlPlaneServer(t)

	sshAllow := true
	cfg := hub.Config{
		Compute: hub.ComputeConfig{
			Provider:      "gce",
			Project:       "test-proj",
			Region:        "us-central1",
			Zones:         []string{"us-central1-a"},
			VMPrefix:      "grain-vm-",
			SAPrefix:      "grain-sa-",
			SSHBreakGlass: &sshAllow,
			RegistryID:    "grain.example.com",
			SelfPrincipal: testSelfPrincipal,
		},
		Secrets: hub.SecretsConfig{
			Project:      "test-proj",
			SecretPrefix: "grain-inst-",
		},
		InstanceSQL: hub.InstanceSQLConfig{
			AdminDSNTemplate: dsnTemplate,
		},
		InstanceState: hub.InstanceStateConfig{
			GCSBucket: "grain-state-bucket",
		},
		Observability: hub.ObservabilityConfig{
			FleetLogs: false,
		},
	}

	res, err := NewGCPInstanceResources(ctx, cfg,
		WithResourcesHTTPClient(srv.Client()),
		WithSQLPoolAllocator(st),
		WithIAMBaseURL(srv.URL),
		WithResourceManagerBaseURL(srv.URL),
		WithSQLAdminBaseURL(srv.URL),
		WithSecretManagerBaseURL(srv.URL),
		WithStorageBaseURL(srv.URL),
		WithResourcesComputeBaseURL(srv.URL+"/compute/v1"),
	)
	if err != nil {
		t.Fatalf("NewGCPInstanceResources: %v", err)
	}

	// Create entries alice-vm and bob-vm in store and allocate SQL instances.
	aliceEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "alice-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "medium",
		Zone:       "us-central1-a",
	})
	if err != nil {
		t.Fatalf("CreateEntry alice-vm: %v", err)
	}
	bobEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "bob-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "bob@example.com",
		Shape:      "medium",
		Zone:       "us-central1-a",
	})
	if err != nil {
		t.Fatalf("CreateEntry bob-vm: %v", err)
	}

	// Provision resources for alice-vm and bob-vm.
	aliceSpec := hub.InstanceResourceSpec{
		EntryID:    aliceEntry.ID,
		Name:       "alice-vm",
		OwnerEmail: "alice@example.com",
	}
	aliceBundle, err := res.Provision(ctx, aliceSpec)
	if err != nil {
		t.Fatalf("Provision alice-vm: %v", err)
	}
	if !cp.canActAs(testSelfPrincipal, aliceBundle.ServiceAccountEmail) {
		t.Fatalf("Provision did not grant the registry %s on %s", roleServiceAccountUser, aliceBundle.ServiceAccountEmail)
	}
	if _, err := st.UpdateInstancePlacement(ctx, aliceEntry.ID, aliceEntry.Version, "grain-vm-alice-vm", "10.128.0.10", "http://10.128.0.10:8080", aliceBundle.ServiceAccountEmail, aliceBundle.SQLInstance, aliceBundle.DatabaseName, aliceBundle.SecretPrefix); err != nil {
		t.Fatalf("UpdateInstancePlacement alice-vm: %v", err)
	}

	bobSpec := hub.InstanceResourceSpec{
		EntryID:    bobEntry.ID,
		Name:       "bob-vm",
		OwnerEmail: "bob@example.com",
	}
	bobBundle, err := res.Provision(ctx, bobSpec)
	if err != nil {
		t.Fatalf("Provision bob-vm: %v", err)
	}
	if _, err := st.UpdateInstancePlacement(ctx, bobEntry.ID, bobEntry.Version, "grain-vm-bob-vm", "10.128.0.11", "http://10.128.0.11:8080", bobBundle.ServiceAccountEmail, bobBundle.SQLInstance, bobBundle.DatabaseName, bobBundle.SecretPrefix); err != nil {
		t.Fatalf("UpdateInstancePlacement bob-vm: %v", err)
	}

	// Verify least-loaded placement across sql-pool-01 and sql-pool-02:
	// alice got sql-pool-01 (0->1 DB), so bob must get sql-pool-02 (0 DBs < 1 DB).
	if aliceBundle.SQLInstance != "sql-pool-01" || bobBundle.SQLInstance != "sql-pool-02" {
		t.Fatalf("expected least-loaded placement (sql-pool-01, sql-pool-02), got (%s, %s)", aliceBundle.SQLInstance, bobBundle.SQLInstance)
	}
	if aliceBundle.DatabaseName != "grain_alice_vm" || bobBundle.DatabaseName != "grain_bob_vm" {
		t.Fatalf("unexpected DB names: (%s, %s)", aliceBundle.DatabaseName, bobBundle.DatabaseName)
	}

	// Verify PostgreSQL database isolation (`REVOKE ALL ON DATABASE ... FROM PUBLIC` and `CONNECTION LIMIT 20`):
	// 1. Alice's IAM role (`grain-sa-alice-vm@test-proj.iam`) can connect to `grain_alice_vm` and create tables.
	aliceIAMUser := strings.TrimSuffix(aliceBundle.ServiceAccountEmail, ".gserviceaccount.com")
	bobIAMUser := strings.TrimSuffix(bobBundle.ServiceAccountEmail, ".gserviceaccount.com")

	openAsRole := func(role, dbName string) (*sql.DB, error) {
		q := make(url.Values)
		q.Set("host", sockDir)
		q.Set("sslmode", "disable")
		u := &url.URL{
			Scheme:   "postgres",
			User:     url.User(role),
			Host:     "localhost",
			Path:     "/" + dbName,
			RawQuery: q.Encode(),
		}
		return sql.Open("pgx", u.String())
	}

	aliceDB, err := openAsRole(aliceIAMUser, "grain_alice_vm")
	if err != nil {
		t.Fatalf("openAsRole alice: %v", err)
	}
	defer aliceDB.Close()
	if _, err := aliceDB.ExecContext(ctx, `CREATE TABLE tasks (id INT PRIMARY KEY, title TEXT); INSERT INTO tasks VALUES (1, 'keep across restore');`); err != nil {
		t.Fatalf("expected alice IAM role to create table and insert row in grain_alice_vm, got: %v", err)
	}

	// 2. Check role attributes in pg_roles: rolconnlimit = 20, NOSUPERUSER, NOCREATEDB, NOCREATEROLE.
	var connLimit int
	var rolSuper, rolCreateDB, rolCreateRole bool
	if err := adminDB.QueryRowContext(ctx, `SELECT rolconnlimit, rolsuper, rolcreatedb, rolcreaterole FROM pg_roles WHERE rolname = $1`, aliceIAMUser).Scan(&connLimit, &rolSuper, &rolCreateDB, &rolCreateRole); err != nil {
		t.Fatalf("querying pg_roles for %s: %v", aliceIAMUser, err)
	}
	if connLimit != 20 || rolSuper || rolCreateDB || rolCreateRole {
		t.Fatalf("unexpected role flags for %s: connLimit=%d super=%v createdb=%v createrole=%v", aliceIAMUser, connLimit, rolSuper, rolCreateDB, rolCreateRole)
	}

	// 3. Bob's IAM role MUST NOT be able to connect to Alice's database `grain_alice_vm` (`REVOKE ALL ON DATABASE ... FROM PUBLIC`).
	bobToAliceDB, err := openAsRole(bobIAMUser, "grain_alice_vm")
	if err != nil {
		t.Fatalf("openAsRole bob->alice: %v", err)
	}
	defer bobToAliceDB.Close()
	if err := bobToAliceDB.PingContext(ctx); err == nil {
		t.Fatal("SECURITY VIOLATION: bob's IAM user was able to connect to grain_alice_vm!")
	}

	// 4. Alice's IAM role MUST NOT be able to connect to `postgres` (`REVOKE CONNECT ON DATABASE postgres FROM PUBLIC`).
	aliceToPostgresDB, err := openAsRole(aliceIAMUser, "postgres")
	if err != nil {
		t.Fatalf("openAsRole alice->postgres: %v", err)
	}
	defer aliceToPostgresDB.Close()
	if err := aliceToPostgresDB.PingContext(ctx); err == nil {
		t.Fatal("SECURITY VIOLATION: alice's IAM user was able to connect to postgres database!")
	}

	// Verify Secret Manager conditional IAM binding has trailing hyphen (`grain-inst-alice-vm-`)
	// and fleet_logs roles are NOT granted when fleet_logs: false.
	cp.mu.Lock()
	var foundSecretAccessor, foundLogging bool
	wantSecretPrefixExpr := `resource.name.startsWith("projects/test-proj/secrets/grain-inst-alice-vm-")`
	for _, b := range cp.projectPolicy.Bindings {
		for _, m := range b.Members {
			if m == "serviceAccount:"+aliceBundle.ServiceAccountEmail {
				if b.Role == "roles/secretmanager.secretAccessor" && b.Condition != nil && b.Condition.Expression == wantSecretPrefixExpr {
					foundSecretAccessor = true
				}
				if b.Role == "roles/logging.logWriter" || b.Role == "roles/monitoring.metricWriter" {
					foundLogging = true
				}
			}
		}
	}
	folderPol := cp.managedFolders["grain-state-bucket/instances/alice-vm/"]
	cp.mu.Unlock()
	if !foundSecretAccessor {
		t.Fatalf("expected conditional roles/secretmanager.secretAccessor binding with expression %q", wantSecretPrefixExpr)
	}
	if foundLogging {
		t.Fatal("expected no logging/monitoring roles when fleet_logs is false")
	}
	if len(folderPol.Bindings) != 1 || folderPol.Bindings[0].Role != "roles/storage.objectUser" {
		t.Fatalf("expected GCS managed folder roles/storage.objectUser binding, got %+v", folderPol)
	}

	// Verify break-glass SSH grant and revoke.
	if err := res.GrantSSH(ctx, "us-central1-a", "grain-vm-alice-vm", "alice@example.com"); err != nil {
		t.Fatalf("GrantSSH: %v", err)
	}
	if err := res.RevokeSSH(ctx, "us-central1-a", "grain-vm-alice-vm", "alice@example.com"); err != nil {
		t.Fatalf("RevokeSSH: %v", err)
	}

	// Store secrets for alice-vm and a prefix-sibling `alice-vm-dev` to verify prefix-scoped cleanup.
	if _, err := res.PutSecret(ctx, "alice-vm", "config-repo-key", []byte("alice-key")); err != nil {
		t.Fatalf("PutSecret alice: %v", err)
	}
	if _, err := res.PutSecret(ctx, "alice-vm-dev", "config-repo-key", []byte("alice-dev-key")); err != nil {
		t.Fatalf("PutSecret alice-dev: %v", err)
	}
	cp.mu.Lock()
	cp.objects["grain-state-bucket/instances/alice-vm/checkpoint.tar"] = []byte("tar-bytes")
	cp.objects["grain-state-bucket/instances/alice-vm-dev/checkpoint.tar"] = []byte("dev-tar-bytes")
	cp.mu.Unlock()

	// 5. Fence on delete (`Teardown(..., retainDB=true)`):
	// Database `grain_alice_vm` must disallow connections and role must be NOLOGIN, while data and GCS objects are retained.
	if err := res.Teardown(ctx, aliceSpec, *aliceBundle, true); err != nil {
		t.Fatalf("Teardown(retainDB=true): %v", err)
	}
	_ = aliceDB.Close()

	fencedConn, err := openAsRole(aliceIAMUser, "grain_alice_vm")
	if err != nil {
		t.Fatalf("openAsRole fenced: %v", err)
	}
	defer fencedConn.Close()
	if err := fencedConn.PingContext(ctx); err == nil {
		t.Fatal("expected fenced database/role to reject new connections after Teardown(retainDB=true)")
	}

	// 6. Restore within retention window: re-running Provision un-fences `grain_alice_vm` with data intact!
	if _, err := res.Provision(ctx, aliceSpec); err != nil {
		t.Fatalf("Provision on restore: %v", err)
	}
	restoredConn, err := openAsRole(aliceIAMUser, "grain_alice_vm")
	if err != nil {
		t.Fatalf("openAsRole restored: %v", err)
	}
	defer restoredConn.Close()
	var title string
	if err := restoredConn.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id = 1`).Scan(&title); err != nil {
		t.Fatalf("expected restored database to retain table row, got: %v", err)
	}
	if title != "keep across restore" {
		t.Fatalf("unexpected row title after restore: %q", title)
	}
	_ = restoredConn.Close()

	// 7. Final purge (`Teardown(..., retainDB=false)`):
	// Drops `grain_alice_vm`, drops role, deletes `grain-inst-alice-vm-*` secrets and `instances/alice-vm/*` objects,
	// while leaving `alice-vm-dev` secrets and objects untouched!
	if err := res.Teardown(ctx, aliceSpec, *aliceBundle, false); err != nil {
		t.Fatalf("Teardown(retainDB=false): %v", err)
	}

	var dbCount int
	if err := adminDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_database WHERE datname = 'grain_alice_vm'`).Scan(&dbCount); err != nil {
		t.Fatalf("checking dropped database: %v", err)
	}
	if dbCount != 0 {
		t.Fatal("expected grain_alice_vm database to be dropped on Teardown(retainDB=false)")
	}

	cp.mu.Lock()
	_, aliceSAPolicyExists := cp.saPolicies[aliceBundle.ServiceAccountEmail]
	cp.mu.Unlock()
	if aliceSAPolicyExists {
		t.Fatal("expected the registry's actAs binding on alice-vm's service account to go with the account on purge")
	}

	cp.mu.Lock()
	_, aliceSecretExists := cp.secrets["grain-inst-alice-vm-config-repo-key"]
	_, aliceDevSecretExists := cp.secrets["grain-inst-alice-vm-dev-config-repo-key"]
	_, aliceObjExists := cp.objects["grain-state-bucket/instances/alice-vm/checkpoint.tar"]
	_, aliceDevObjExists := cp.objects["grain-state-bucket/instances/alice-vm-dev/checkpoint.tar"]
	cp.mu.Unlock()
	if aliceSecretExists {
		t.Fatal("expected grain-inst-alice-vm-config-repo-key secret to be deleted on purge")
	}
	if !aliceDevSecretExists {
		t.Fatal("expected sibling grain-inst-alice-vm-dev-config-repo-key secret to NOT be deleted")
	}
	if aliceObjExists {
		t.Fatal("expected instances/alice-vm/checkpoint.tar object to be deleted on purge")
	}
	if !aliceDevObjExists {
		t.Fatal("expected sibling instances/alice-vm-dev/checkpoint.tar object to NOT be deleted")
	}
}
