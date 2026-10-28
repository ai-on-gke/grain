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

package attest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Envelope is a DSSE envelope. Payload marshals as standard base64, which
// is what DSSE's JSON encoding says.
type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     []byte      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

// Signature is one DSSE signature: an ASN.1 DER ECDSA signature over
// PAE(payloadType, payload), by the key KeyID names.
type Signature struct {
	KeyID string `json:"keyid"`
	Sig   []byte `json:"sig"`
}

// PAE is DSSE's pre-authentication encoding, the bytes actually signed:
// "DSSEv1" SP LEN(type) SP type SP LEN(body) SP body.
func PAE(payloadType string, payload []byte) []byte {
	b := []byte("DSSEv1 ")
	b = strconv.AppendInt(b, int64(len(payloadType)), 10)
	b = append(b, ' ')
	b = append(b, payloadType...)
	b = append(b, ' ')
	b = strconv.AppendInt(b, int64(len(payload)), 10)
	b = append(b, ' ')
	return append(b, payload...)
}

// Signer signs a SHA-256 digest with ECDSA P-256 and returns the ASN.1
// DER signature: the shape Cloud KMS's EC_SIGN_P256_SHA256 keys answer
// in, so the KMS signer of GS-3 and the local file key here are
// interchangeable.
type Signer interface {
	KeyID() string
	SignDigest(ctx context.Context, digest [sha256.Size]byte) ([]byte, error)
}

// Sign marshals s and signs it with signer.
func Sign(ctx context.Context, signer Signer, s Statement) (*Envelope, error) {
	payload, err := s.marshal()
	if err != nil {
		return nil, err
	}
	sig, err := signer.SignDigest(ctx, sha256.Sum256(PAE(PayloadType, payload)))
	if err != nil {
		return nil, fmt.Errorf("attest: sign: %w", err)
	}
	return &Envelope{PayloadType: PayloadType, Payload: payload, Signatures: []Signature{{KeyID: signer.KeyID(), Sig: sig}}}, nil
}

// Digest is "sha256:<hex>" of the envelope's JSON: how a cached job's
// CachedRef names the envelope it reuses.
func Digest(env *Envelope) (string, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// KeyStatus is a published key's state ("Key management").
type KeyStatus string

const (
	KeyActive  KeyStatus = "active"
	KeyRetired KeyStatus = "retired"
	KeyRevoked KeyStatus = "revoked"
)

// Key is one verification key and its state. A retired key still
// verifies everything it signed; a revoked one verifies only envelopes
// issued before RevokedAt.
type Key struct {
	Public    *ecdsa.PublicKey
	Status    KeyStatus
	RevokedAt time.Time
}

// KeySet is the verification keys a verifier trusts, by key id.
type KeySet map[string]Key

// NewKeySet trusts every P-256 key in keys (pkg/jwks.Fetch's answer) as
// active, and refuses any key of another kind.
func NewKeySet(keys map[string]crypto.PublicKey) (KeySet, error) {
	set := KeySet{}
	for kid, k := range keys {
		ec, ok := k.(*ecdsa.PublicKey)
		if !ok || ec.Curve != elliptic.P256() {
			return nil, fmt.Errorf("attest: key %q is not an ECDSA P-256 key", kid)
		}
		set[kid] = Key{Public: ec, Status: KeyActive}
	}
	return set, nil
}

// Verify checks env's signature against keys and returns the statement
// it carries. It checks the envelope, not what it says: the caller still
// checks the subject (Statement.CheckSubject) and the request it meant.
func Verify(env *Envelope, keys KeySet) (*Statement, error) {
	if env == nil {
		return nil, errors.New("attest: no envelope")
	}
	if env.PayloadType != PayloadType {
		return nil, fmt.Errorf("attest: payload type %q, want %q", env.PayloadType, PayloadType)
	}
	var s Statement
	if err := json.Unmarshal(env.Payload, &s); err != nil {
		return nil, fmt.Errorf("attest: payload: %w", err)
	}
	if s.Type != StatementType || s.PredicateType != RunPredicateType {
		return nil, fmt.Errorf("attest: statement %q with predicate %q is not a grit run", s.Type, s.PredicateType)
	}
	digest := sha256.Sum256(PAE(env.PayloadType, env.Payload))
	var why []error
	for _, sig := range env.Signatures {
		key, ok := keys[sig.KeyID]
		if !ok {
			why = append(why, fmt.Errorf("key %q is not trusted", sig.KeyID))
			continue
		}
		if key.Status == KeyRevoked && !s.Predicate.IssuedAt.Before(key.RevokedAt) {
			why = append(why, fmt.Errorf("key %q was revoked at %s, before this was issued", sig.KeyID, key.RevokedAt.Format(time.RFC3339)))
			continue
		}
		if !ecdsa.VerifyASN1(key.Public, digest[:], sig.Sig) {
			why = append(why, fmt.Errorf("signature by %q does not verify", sig.KeyID))
			continue
		}
		return &s, nil
	}
	if len(why) == 0 {
		return nil, errors.New("attest: envelope carries no signature")
	}
	return nil, fmt.Errorf("attest: %w", errors.Join(why...))
}

// FileSigner is a local ECDSA P-256 key: embedded mode's signer, kept in
// the state directory. GS-3 puts a KMS key behind the same interface.
type FileSigner struct {
	key *ecdsa.PrivateKey
	kid string
}

// NewFileSigner signs with key.
func NewFileSigner(key *ecdsa.PrivateKey) (*FileSigner, error) {
	if key.Curve != elliptic.P256() {
		return nil, errors.New("attest: signing key is not P-256")
	}
	kid, err := KeyID(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &FileSigner{key: key, kid: kid}, nil
}

// LoadOrCreateFileSigner reads the PEM private key at path, or makes a
// new one there (mode 0600) when there is none.
func LoadOrCreateFileSigner(path string) (*FileSigner, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		if err := errors.Join(pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), f.Close()); err != nil {
			return nil, err
		}
		return NewFileSigner(key)
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("attest: %s holds no PEM block", path)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("attest: %s: %w", path, err)
	}
	return NewFileSigner(key)
}

func (f *FileSigner) KeyID() string { return f.kid }

// Public is the key verifiers need.
func (f *FileSigner) Public() *ecdsa.PublicKey { return &f.key.PublicKey }

func (f *FileSigner) SignDigest(_ context.Context, digest [sha256.Size]byte) ([]byte, error) {
	return ecdsa.SignASN1(rand.Reader, f.key, digest[:])
}

// KeyID is the id a local key goes by: the first 16 bytes of the SHA-256
// of its SubjectPublicKeyInfo, in hex.
func KeyID(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:16]), nil
}

// MarshalPublicKey is pub as a PEM "PUBLIC KEY" block.
func MarshalPublicKey(pub *ecdsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// ParsePublicKey reads a PEM "PUBLIC KEY" block holding a P-256 key, and
// returns it with its KeyID.
func ParsePublicKey(b []byte) (string, *ecdsa.PublicKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return "", nil, errors.New("attest: no PEM block")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", nil, err
	}
	ec, ok := k.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return "", nil, errors.New("attest: public key is not ECDSA P-256")
	}
	kid, err := KeyID(ec)
	return kid, ec, err
}
