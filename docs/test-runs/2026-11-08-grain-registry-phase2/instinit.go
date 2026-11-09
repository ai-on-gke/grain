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

// instinit feeds reconcile.InstanceGrainConfig -- built from the walk's live
// registry config and an instance entry row read from its PostgreSQL store --
// into scripts/grain-instance-init.sh, through a local GCE metadata stub and a
// Secret Manager stub, and prints what the script wrote. compute.provider is
// none in the walk, so the resource bundle stands in for what the GCP
// provider's Provision returns.
//
//	go run docs/test-runs/2026-11-08-grain-registry-phase2/instinit.go -config /tmp/gr2/registry-a.yaml -entry alice-vmb
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/registry"
	"github.com/ai-on-gke/grain/pkg/registry/reconcile"
	"github.com/ai-on-gke/grain/pkg/registry/store"
)

func main() {
	cfgPath := flag.String("config", "/tmp/gr2/registry-a.yaml", "registry config")
	entryName := flag.String("entry", "alice-vmb", "instance entry to boot")
	flag.Parse()
	ctx := context.Background()
	cfg, err := registry.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	// The walk runs compute.provider none; give the config the project and
	// secret prefix a GCP deployment would carry.
	cfg.Compute.Project = "walk-proj"
	cfg.Compute.Region = "us-central1"
	cfg.Secrets.SecretPrefix = "grain-inst-"
	st, err := store.Open(ctx, cfg.Database.URL, store.DefaultThresholds())
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	entry, err := st.GetEntryByName(ctx, *entryName)
	if err != nil {
		log.Fatalf("entry %s: %v", *entryName, err)
	}
	n := entry.Name
	bundle := &registry.InstanceResourceBundle{
		ServiceAccountEmail: "grain-sa-" + n + "@walk-proj.iam.gserviceaccount.com",
		SQLInstance:         "grain-shared-pg",
		SQLConnectionName:   "walk-proj:us-central1:grain-shared-pg",
		SQLPrivateIP:        "10.0.0.5",
		DatabaseName:        "grain_" + strings.ReplaceAll(n, "-", "_"),
		SecretPrefix:        "grain-inst-" + n + "-",
		GCSBucket:           "grain-state",
		GCSManagedFolder:    "instances/" + n + "/",
	}
	meta := reconcile.InstanceGrainConfig(*cfg, entry, bundle, "")
	grainConfig, _ := json.Marshal(meta)
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("grain-config metadata keys (%d): %s\n", len(keys), strings.Join(keys, " "))

	var mu sync.Mutex
	secrets := map[string]string{}
	var requests []string
	smPrefix := "/v1/projects/walk-proj/secrets/" + bundle.SecretPrefix
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/computeMetadata/v1/instance/attributes/grain-config":
			if r.Header.Get("Metadata-Flavor") != "Google" {
				http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
				return
			}
			_, _ = w.Write(grainConfig)
		case r.URL.Path == "/computeMetadata/v1/instance/service-accounts/default/token":
			_, _ = w.Write([]byte(`{"access_token":"walk-oauth-token","token_type":"Bearer","expires_in": 3600}`))
		case r.URL.Path == "/computeMetadata/v1/instance/service-accounts/default/email":
			_, _ = w.Write([]byte(bundle.ServiceAccountEmail))
		case strings.HasPrefix(r.URL.Path, smPrefix) && strings.HasSuffix(r.URL.Path, "/versions/latest:access"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/versions/latest:access")
			mu.Lock()
			val, ok := secrets[name]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"payload":{"data":"` + base64.StdEncoding.EncodeToString([]byte(val)) + `"}}`))
		case strings.HasPrefix(r.URL.Path, smPrefix) && strings.HasSuffix(r.URL.Path, ":addVersion") && r.Method == http.MethodPost:
			var body struct {
				Payload struct {
					Data string `json:"data"`
				} `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			decoded, _ := base64.StdEncoding.DecodeString(body.Payload.Data)
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), ":addVersion")
			mu.Lock()
			secrets[name] = string(decoded)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"name":"` + name + `/versions/1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root, _ := os.MkdirTemp("", "instinit")
	defer os.RemoveAll(root)
	etcDir := filepath.Join(root, "etc")
	cmd := exec.Command("bash", "scripts/grain-instance-init.sh")
	cmd.Env = append(os.Environ(),
		"GRAIN_METADATA_URL="+srv.URL+"/computeMetadata/v1",
		"GRAIN_SM_API_URL="+srv.URL,
		"GRAIN_DATA_DIR="+filepath.Join(root, "data"),
		"GRAIN_ETC_DIR="+etcDir,
		"GRAIN_SECRETS_RUN_DIR="+filepath.Join(root, "run-secrets"),
		"GRAIN_CLOUDSQL_DIR="+filepath.Join(root, "cloudsql"),
		"GRAIN_SKIP_TMPFS_MOUNT=1",
		"GRAIN_SKIP_SYSTEMCTL=1",
	)
	out, err := cmd.CombinedOutput()
	fmt.Printf("grain-instance-init.sh exit: %v\n%s", err, out)
	mu.Lock()
	fmt.Printf("stub requests: %s\n", strings.Join(requests, ", "))
	for k, v := range secrets {
		fmt.Printf("secret stored: %s (%d bytes)\n", k, len(v))
	}
	mu.Unlock()
	for _, f := range []string{"daemon.env", "cloud-sql-proxy.env"} {
		b, rerr := os.ReadFile(filepath.Join(etcDir, f))
		fmt.Printf("--- %s (%v)\n%s", f, rerr, b)
	}
	if err != nil {
		os.Exit(1)
	}
}
