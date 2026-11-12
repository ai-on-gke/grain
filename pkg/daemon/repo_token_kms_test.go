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

package daemon

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets/kmsfake"
)

// TestRepoTokenUnwrapsAKMSWrappedKeyOnce: RepoToken is asked for on every
// state- and secrets-repository sync, so with a KMS-wrapped key each ask
// that opened its own store was a Cloud KMS decrypt -- several a minute
// from an idle daemon (docs/test-runs/2026-11-11-secrets-store.md).
func TestRepoTokenUnwrapsAKMSWrappedKeyOnce(t *testing.T) {
	fake := kmsfake.Start(t)
	dataDir := t.TempDir()
	store := OpenSecrets(dataDir)
	ctx := context.Background()
	path, err := writeSecretsRepoToken(dataDir, "ghp_secrets_repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WrapKeyWithKMS(ctx, "projects/example-project/locations/us/keyRings/grain/cryptoKeys/secrets-key", nil); err != nil {
		t.Fatalf("wrap: %v", err)
	}
	before := fake.Decrypts()
	for i := 0; i < 5; i++ {
		if tok, err := RepoToken(dataDir, "", path)(ctx); err != nil || tok != "ghp_secrets_repo" {
			t.Fatalf("RepoToken %d = %q, %v", i, tok, err)
		}
	}
	if got := fake.Decrypts() - before; got > 1 {
		t.Fatalf("5 RepoToken reads made %d KMS decrypts, want at most 1", got)
	}
}
