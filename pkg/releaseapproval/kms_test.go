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

package releaseapproval

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/secrets/kmsfake"
)

// kmsVersion is a made-up key version. The public-key cache is keyed by
// endpoint too, so each test's fake (its own URL) reads its own key.
func kmsVersion(t *testing.T, n int) string {
	t.Helper()
	return "projects/example-project/locations/global/keyRings/grain/cryptoKeys/release-approval/cryptoKeyVersions/" + strconv.Itoa(n)
}

func TestAKMSKeySignsWhatTheWorkflowsVerifierAccepts(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not installed")
	}
	fake := kmsfake.Start(t)
	name := kmsVersion(t, 1)
	dir, commit := fixtureRepo(t)
	signer, err := LoadKMSSigner(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := PublicKeyPEM(fake.SigningKey(name))
	if pub != want {
		t.Fatalf("the KMS signer's public key is not KMS's getPublicKey:\n%s\nwant\n%s", pub, want)
	}
	now := time.Unix(1_800_000_000, 0)
	signed, err := signer.Sign(context.Background(), Record{Repo: "example/widgets", Version: "v1.2.0", Decision: "approve",
		Commit: commit, Approver: "alice@example.com", Issued: now, Expires: now.Add(Lifetime)})
	if err != nil {
		t.Fatal(err)
	}
	if fake.Signs() != 1 {
		t.Fatalf("asymmetricSign calls = %d, want 1", fake.Signs())
	}
	env := map[string]string{
		"APPROVAL": signed.Record, "APPROVAL_SIGNATURE": signed.Signature, "APPROVAL_PUBLIC_KEY": pub,
		"EXPECT_REPO": "example/widgets", "EXPECT_VERSION": "v1.2.0", "EXPECT_DECISION": "approve",
		"EXPECT_SHORT_SOAK_OK": "false", "EXPECT_ALLOW_BREAKING": "false", "NOW": strconv.FormatInt(now.Add(time.Minute).Unix(), 10),
	}
	if by, stderr, ok := verify(t, dir, env); !ok || by != "alice@example.com" {
		t.Fatalf("a KMS-signed approval = %q, ok=%v (%s); want alice@example.com", by, ok, stderr)
	}

	// The store-held key is another key: a record it signs does not
	// verify against the KMS key's variable.
	local, err := LoadSigner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := local.Sign(context.Background(), Record{Repo: "example/widgets", Version: "v1.2.0", Decision: "approve",
		Commit: commit, Approver: "alice@example.com", Issued: now, Expires: now.Add(Lifetime)})
	env["APPROVAL"], env["APPROVAL_SIGNATURE"] = forged.Record, forged.Signature
	if _, _, ok := verify(t, dir, env); ok {
		t.Error("a record the store's key signed verified against the KMS key; want refused")
	}
}

func TestAKMSKeyThatCannotSignIsAnError(t *testing.T) {
	fake := kmsfake.Start(t)
	name := kmsVersion(t, 2)
	signer, err := LoadKMSSigner(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	fake.Deny(true)
	_, err = signer.Sign(context.Background(), Record{Repo: "example/widgets", Version: "v1.2.0", Decision: "reject",
		Commit: "0123456789abcdef0123456789abcdef01234567", Approver: "alice@example.com",
		Issued: time.Unix(1_800_000_000, 0), Expires: time.Unix(1_800_000_000, 0).Add(Lifetime)})
	if !errors.Is(err, secrets.ErrKMS) {
		t.Fatalf("signing once KMS refuses = %v, want a KMS error", err)
	}
	fake.Close()
	if _, err := LoadKMSSigner(context.Background(), kmsVersion(t, 3)); !errors.Is(err, secrets.ErrKMS) {
		t.Fatalf("loading with KMS unreachable = %v, want a KMS error", err)
	}
}

func TestAKMSKeyMustBeAKeyVersion(t *testing.T) {
	kmsfake.Start(t)
	for _, name := range []string{
		"",
		"projects/example-project/locations/global/keyRings/grain/cryptoKeys/release-approval",
		"projects/example-project/locations/global/keyRings/grain/cryptoKeys/release-approval/cryptoKeyVersions/1?x",
		"projects/../locations/global/keyRings/grain/cryptoKeys/release-approval/cryptoKeyVersions/1",
	} {
		if _, err := LoadKMSSigner(context.Background(), name); err == nil {
			t.Errorf("LoadKMSSigner(%q) = nil error, want a refusal", name)
		}
	}
}
