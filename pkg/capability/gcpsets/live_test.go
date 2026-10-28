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

package gcpsets_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
)

// TestLiveMintUseRelease runs a permission set against real GCP. It is
// opt-in: set GRAIN_GCP_LIVE_PROJECT (e.g. grain-sandbox-dev) and
// GRAIN_GCP_LIVE_BUCKET (a test bucket in it), and either
// GRAIN_GCP_LIVE_ADMIN_KEY (a file holding the admin's key: configured)
// or nothing (the ambient ADC is the admin). The admin needs the
// grainGcpKeyAdmin role of docs/capabilities.md on the project, and
// storage.buckets.getIamPolicy / setIamPolicy on the bucket.
//
// It mints a key for a set holding roles/storage.objectViewer on the
// bucket, lists the bucket with gcloud as that key, releases it, and
// checks the key is gone. The set's service account is left for the next
// run to reuse; delete it with delete_orphans or by hand.
func TestLiveMintUseRelease(t *testing.T) {
	project, bucket := os.Getenv("GRAIN_GCP_LIVE_PROJECT"), os.Getenv("GRAIN_GCP_LIVE_BUCKET")
	if project == "" || bucket == "" {
		t.Skip("GRAIN_GCP_LIVE_PROJECT and GRAIN_GCP_LIVE_BUCKET are not set")
	}
	admin := gcpsets.Admin{Name: "live", CredentialSource: gcpsets.SourceAmbient, Projects: []string{project}, RoleCeiling: []string{"roles/storage.objectViewer"}}
	if f := os.Getenv("GRAIN_GCP_LIVE_ADMIN_KEY"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		admin.CredentialSource, admin.Credential = gcpsets.SourceConfigured, string(b)
	}
	cfg := &gcpsets.Config{
		// Its own deployment id, so its account never meets a real
		// deployment's: grain-livetest-live-test-1.
		Deployment: "livetest",
		Admins:     []gcpsets.Admin{admin},
		Sets: []gcpsets.PermissionSet{{
			Name: "live-test", Admin: "live", Project: project,
			Roles: []string{"roles/storage.objectViewer"}, ResourceScope: "buckets/" + bucket,
			MaxLease: time.Hour, PoolMax: 1,
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	m := gcpsets.NewManager(cfg, gcpsets.GoogleConnector{})
	for _, chk := range m.CheckAdmins(ctx) {
		t.Logf("admin %s (%s) is %q; missing %v %s", chk.Admin, chk.Source, chk.Identity, chk.Missing, chk.Error)
		if chk.Error != "" || len(chk.Missing) > 0 {
			t.Fatalf("the admin cannot run this test: grant it the grainGcpKeyAdmin role of docs/capabilities.md on %s and storage.buckets.getIamPolicy/setIamPolicy on gs://%s", project, bucket)
		}
	}
	keyJSON, lease, err := m.Mint(ctx, "live-1-0", "live-test", time.Now().Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			m.Release(context.Background(), "live-1-0")
		}
	}()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.json")
	if err := os.WriteFile(keyFile, []byte(keyJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("gcloud"); err == nil {
		env := append(os.Environ(), "CLOUDSDK_CONFIG="+filepath.Join(dir, "gcloud"))
		run := func(args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, "gcloud", args...)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			return string(out), err
		}
		if out, err := run("auth", "activate-service-account", "--key-file="+keyFile); err != nil {
			t.Fatalf("activating the key: %v", strings.ReplaceAll(out, keyJSON, "<key>"))
		}
		// A new binding takes a minute or so to be honoured.
		var out string
		for i := 0; i < 20; i++ {
			if out, err = run("storage", "ls", "gs://"+bucket, "--project="+project); err == nil {
				break
			}
			time.Sleep(15 * time.Second)
		}
		if err != nil {
			t.Fatalf("listing gs://%s as %s: %s", bucket, lease.Account, out)
		}
	} else {
		t.Log("no gcloud: skipping the use of the key")
	}
	if err := m.Release(ctx, "live-1-0"); err != nil {
		t.Fatal(err)
	}
	released = true
	c, err := gcpsets.GoogleConnector{}.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := c.ListKeys(ctx, "projects/"+project+"/serviceAccounts/"+lease.Account)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.ID == lease.KeyID {
			t.Fatalf("key %s outlived its release", k.ID)
		}
	}
}
