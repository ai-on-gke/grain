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
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// KMSSigner signs with a Cloud KMS EC_SIGN_P256_SHA256 key version
// (cloudkms.googleapis.com/v1/<version>:asymmetricSign), so the private
// key never leaves KMS (design/design/secrets-key-protection.design.md
// §5). It answers in the same ASN.1 DER form as attest.FileSigner, so
// scripts/verify-release-approval.sh checks either alike.
type KMSSigner struct {
	client *secrets.KMSClient
	name   string
	pub    *ecdsa.PublicKey
	kid    string
}

// kmsPublic caches each key version's public key: a version's key pair
// never changes, and Approve should not cost a getPublicKey every time.
// It is keyed by the endpoint as well as the name, so a key read from
// one KMS (a test's fake, say) is never taken for another's.
var kmsPublic sync.Map // endpoint + version name -> *ecdsa.PublicKey

// NewKMSSigner is the signer for the key version name, reading its
// public key through client (secrets.DefaultKMSClient when nil: the
// metadata server's token, $GRAIN_KMS_ENDPOINT for a fake).
func NewKMSSigner(ctx context.Context, client *secrets.KMSClient, name string) (*KMSSigner, error) {
	name = strings.TrimSpace(name)
	if err := secrets.ValidateKMSKeyVersionName(name); err != nil {
		return nil, err
	}
	if client == nil {
		client = secrets.DefaultKMSClient()
	}
	var pub *ecdsa.PublicKey
	cacheKey := client.ResolvedEndpoint() + name
	if v, ok := kmsPublic.Load(cacheKey); ok {
		pub = v.(*ecdsa.PublicKey)
	} else {
		var err error
		if pub, err = client.GetPublicKey(ctx, name); err != nil {
			return nil, err
		}
		kmsPublic.Store(cacheKey, pub)
	}
	kid, err := attest.KeyID(pub)
	if err != nil {
		return nil, err
	}
	return &KMSSigner{client: client, name: name, pub: pub, kid: kid}, nil
}

func (k *KMSSigner) KeyID() string { return k.kid }

// Public is the key verifiers need: KMS's getPublicKey for the version.
func (k *KMSSigner) Public() *ecdsa.PublicKey { return k.pub }

// SignDigest asks KMS to sign digest, and checks the answer against the
// version's public key before handing it out: a signature the workflow
// would refuse is an error here, not a failed dispatch later.
func (k *KMSSigner) SignDigest(ctx context.Context, digest [sha256.Size]byte) ([]byte, error) {
	sig, err := k.client.AsymmetricSign(ctx, k.name, digest)
	if err != nil {
		return nil, err
	}
	if !ecdsa.VerifyASN1(k.pub, digest[:], sig) {
		return nil, errors.New("KMS's signature does not verify against its public key")
	}
	return sig, nil
}

// LoadKMSSigner is LoadSigner for a deployment whose key is the Cloud
// KMS key version name (the daemon's -release-approval-kms-key).
func LoadKMSSigner(ctx context.Context, name string) (*Signer, error) {
	key, err := NewKMSSigner(ctx, nil, name)
	if err != nil {
		return nil, fmt.Errorf("release approval: signing key %s: %w", name, err)
	}
	return &Signer{key: key}, nil
}
