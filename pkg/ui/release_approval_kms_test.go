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

package ui_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/releaseapproval"
	"github.com/ai-on-gke/grain/pkg/secrets/kmsfake"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A deployment that signs with a Cloud KMS key reports that key's public
// half as the GRAIN_RELEASE_APPROVAL_KEY value, and keeps no key of its
// own in its data directory.
func TestTheReleaseApprovalKeyIsTheKMSKeysWhenOneIsConfigured(t *testing.T) {
	fake := kmsfake.Start(t)
	const name = "projects/example-project/locations/global/keyRings/grain/cryptoKeys/release-approval/cryptoKeyVersions/7"
	c, _, _ := testClient(t)
	c.Config.DataDir = t.TempDir()
	c.Config.ReleaseApprovalKMSKey = name
	srv := ui.NewServerWithClient(c)

	got := decode[map[string]string](t, do(t, srv, http.MethodGet, "/api/release-approval-key", ""))
	want, _ := releaseapproval.PublicKeyPEM(fake.SigningKey(name))
	if got["publicKey"] != want {
		t.Fatalf("publicKey = %q, want KMS's key %q", got["publicKey"], want)
	}
	if got["variable"] != "GRAIN_RELEASE_APPROVAL_KEY" || got["note"] == "" {
		t.Fatalf("variable = %q, note = %q; want GRAIN_RELEASE_APPROVAL_KEY and a note on changing the key", got["variable"], got["note"])
	}
	if _, err := os.Stat(filepath.Join(c.Config.DataDir, releaseapproval.KeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s = %v; want no local key made beside a KMS one", releaseapproval.KeyFile, err)
	}
}
