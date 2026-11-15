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

// Package release defines the signed per-platform release manifest, pinned
// Ed25519 release public key, and key-rotation chain for grain-agentd
// (design/design/agentd-stable-contract.design.md, "The safety valve").
//
// The release signing private key is held outside the controller. Every
// grain-agentd binary embeds the release public key and writes it on install
// to ~/.config/grain/agentd-release.pub. When rotating to a new release key,
// the old private key signs a KeyRotation record naming the new public key;
// hosts pinned to the old key walk that signed delegation forward, while hosts
// already pinned to the new key verify the new key directly and never accept
// the retired old key.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

const (
	// ManifestSchema is the current grain-agentd release manifest schema.
	ManifestSchema = 1

	// ManifestSignatureDomain is the first line of the canonical text signed
	// for a release manifest.
	ManifestSignatureDomain = "grain-agentd-release-manifest/v1"

	// RotationSignatureDomain is the first line of the canonical text signed
	// by an outgoing release key to delegate to its replacement.
	RotationSignatureDomain = "grain-agentd-key-rotation/v1"

	// MaxRotations caps how many key-rotation hops a manifest may carry.
	MaxRotations = 8

	// DefaultConfigFileName is the filename written under ~/.config/grain/.
	DefaultConfigFileName = "agentd-release.pub"
)

// Platforms are the OS/architecture pairs published with each release.
// grain-agentd manages a systemd --user unit and Linux process groups, so
// release binaries target Linux amd64 and arm64.
var Platforms = []string{
	"linux/amd64",
	"linux/arm64",
}

//go:embed agentd-release.pub
var embeddedReleasePub string

// ReleasePublicKey overrides embeddedReleasePub when stamped at link time via
// -ldflags "-X github.com/ai-on-gke/grain/pkg/agentd/release.ReleasePublicKey=...".
// Accepts either SubjectPublicKeyInfo PEM or "ed25519:<base64>".
var ReleasePublicKey string

var (
	versionTokenRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,127}$`)
	platformTokenRE = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)
	assetTokenRE    = regexp.MustCompile(`^grain-agentd-[a-z0-9]+-[a-z0-9]+$`)
	sha256HexRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keyIDHexRE      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	featureTokenRE  = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// AssetName returns the release asset filename for platform (e.g.
// "grain-agentd-linux-amd64").
func AssetName(platform string) (string, error) {
	if !slices.Contains(Platforms, platform) {
		return "", fmt.Errorf("unsupported grain-agentd platform %q (supported: %s)", platform, strings.Join(Platforms, ", "))
	}
	osName, arch, _ := strings.Cut(platform, "/")
	return "grain-agentd-" + osName + "-" + arch, nil
}

// ManifestAssetName returns the manifest filename for platform (e.g.
// "grain-agentd-linux-amd64.manifest.json").
func ManifestAssetName(platform string) (string, error) {
	bin, err := AssetName(platform)
	if err != nil {
		return "", err
	}
	return bin + ".manifest.json", nil
}

// DefaultMinContractFeatures returns the minimum host agent contract v1
// features a peer must support for this build of grain-agentd.
func DefaultMinContractFeatures() []string {
	var out []string
	if hostd.MinCompatible >= 1 {
		out = append(out, string(hostd.FeatureStart))
	}
	if hostd.MinCompatible >= 2 {
		out = append(out, string(hostd.FeatureDetach), string(hostd.FeatureDrain))
	}
	if hostd.MinCompatible >= 3 {
		out = append(out, string(hostd.FeatureSocketOwner))
	}
	if hostd.MinCompatible >= 4 {
		out = append(out, string(hostd.FeatureStage), string(hostd.FeatureListen))
	}
	if len(out) == 0 {
		out = []string{string(hostd.FeatureStart)}
	}
	return out
}

// CurrentContractFeatures returns every host agent contract feature this tree
// implements, in declaration order.
func CurrentContractFeatures() []string {
	out := make([]string, 0, len(hostd.Features))
	for _, f := range hostd.Features {
		out = append(out, string(f))
	}
	return out
}

// KeyID returns the 16-hex-character identifier of pub: the first 8 bytes of
// SHA-256(pub) over the raw 32-byte Ed25519 public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// PublicKeyString formats pub as "ed25519:<raw-std-base64>".
func PublicKeyString(pub ed25519.PublicKey) string {
	return "ed25519:" + base64.RawStdEncoding.EncodeToString(pub)
}

// FormatPublicKeyPEM encodes pub as a standard RFC 8410 SubjectPublicKeyInfo
// PEM block preceded by a human-readable key_id comment. Both Go's pem.Decode
// and `openssl pkeyutl -pubin` read this file directly.
func FormatPublicKeyPEM(pub ed25519.PublicKey) ([]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid ed25519 public key length %d", len(pub))
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshaling ed25519 public key: %w", err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# grain-agentd release public key (key_id: %s)\n", KeyID(pub))
	if err := pem.Encode(&buf, &pem.Block{Type: "PUBLIC KEY", Bytes: der}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FormatPrivateKeyPEM encodes priv as a standard RFC 8410 PKCS#8 PEM block
// (`-----BEGIN PRIVATE KEY-----`), interoperable with OpenSSL 3.
func FormatPrivateKeyPEM(priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid ed25519 private key length %d", len(priv))
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshaling ed25519 private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// GenerateKeyPair generates a fresh Ed25519 release signing keypair.
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// ParsePublicKey parses an Ed25519 public key in either SubjectPublicKeyInfo
// PEM format (with optional '#' comment lines) or "ed25519:<base64>" format.
func ParsePublicKey(raw string) (ed25519.PublicKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("empty release public key")
	}
	if block, _ := pem.Decode([]byte(trimmed)); block != nil {
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("unexpected PEM block %q; want PUBLIC KEY", block.Type)
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing PUBLIC KEY PEM: %w", err)
		}
		pub, ok := parsed.(ed25519.PublicKey)
		if !ok || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("PUBLIC KEY PEM is %T, want ed25519.PublicKey", parsed)
		}
		return pub, nil
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b64, ok := strings.CutPrefix(line, "ed25519:")
		if !ok || b64 == "" {
			return nil, errors.New("release public key must be PUBLIC KEY PEM or ed25519:<base64>")
		}
		decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, errors.New("invalid ed25519:<base64> release public key")
		}
		return ed25519.PublicKey(decoded), nil
	}
	return nil, errors.New("no release public key found")
}

// ParsePrivateKey parses an Ed25519 private key from PKCS#8 PEM
// (`-----BEGIN PRIVATE KEY-----`) or "ed25519-seed:<base64>".
func ParsePrivateKey(raw string) (ed25519.PrivateKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("empty release private key")
	}
	if block, _ := pem.Decode([]byte(trimmed)); block != nil {
		if block.Type != "PRIVATE KEY" {
			if strings.Contains(block.Type, "PUBLIC") {
				return nil, fmt.Errorf("got %q PEM, want PRIVATE KEY", block.Type)
			}
			return nil, fmt.Errorf("unexpected PEM block %q; want unencrypted PKCS#8 PRIVATE KEY", block.Type)
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing PRIVATE KEY PEM: %w", err)
		}
		priv, ok := parsed.(ed25519.PrivateKey)
		if !ok || len(priv) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("PRIVATE KEY PEM is %T, want ed25519.PrivateKey", parsed)
		}
		return priv, nil
	}
	if b64, ok := strings.CutPrefix(trimmed, "ed25519-seed:"); ok && b64 != "" {
		seed, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, errors.New("invalid ed25519-seed:<base64> private key")
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	return nil, errors.New("release private key must be PKCS#8 PRIVATE KEY PEM or ed25519-seed:<base64>")
}

// EmbeddedPublicKey returns the Ed25519 release public key compiled into this
// binary (ReleasePublicKey when stamped via -ldflags -X, else agentd-release.pub).
func EmbeddedPublicKey() (ed25519.PublicKey, error) {
	src := strings.TrimSpace(ReleasePublicKey)
	if src == "" {
		src = embeddedReleasePub
	}
	return ParsePublicKey(src)
}

// EmbeddedPublicKeyPEM returns the canonical PEM file bytes for EmbeddedPublicKey(),
// suitable for writing to ~/.config/grain/agentd-release.pub.
func EmbeddedPublicKeyPEM() ([]byte, error) {
	pub, err := EmbeddedPublicKey()
	if err != nil {
		return nil, err
	}
	return FormatPublicKeyPEM(pub)
}

// KeyRotation is a signed statement by an outgoing release key delegating
// authority to its replacement ("the old key signs the new").
type KeyRotation struct {
	OldPublicKey string `json:"old_public_key"`
	NewPublicKey string `json:"new_public_key"`
	IssuedAt     string `json:"issued_at"`
	Signature    string `json:"signature"`
}

// CanonicalText returns the exact domain-separated text signed by OldPublicKey.
func (r KeyRotation) CanonicalText() (string, error) {
	oldPub, err := ParsePublicKey(r.OldPublicKey)
	if err != nil {
		return "", fmt.Errorf("rotation old_public_key: %w", err)
	}
	newPub, err := ParsePublicKey(r.NewPublicKey)
	if err != nil {
		return "", fmt.Errorf("rotation new_public_key: %w", err)
	}
	if bytes.Equal(oldPub, newPub) {
		return "", errors.New("rotation old_public_key and new_public_key are identical")
	}
	t, err := time.Parse(time.RFC3339, r.IssuedAt)
	if err != nil {
		return "", fmt.Errorf("rotation issued_at %q is not RFC3339: %w", r.IssuedAt, err)
	}
	var b strings.Builder
	b.WriteString(RotationSignatureDomain + "\n")
	b.WriteString("old_public_key: " + PublicKeyString(oldPub) + "\n")
	b.WriteString("new_public_key: " + PublicKeyString(newPub) + "\n")
	b.WriteString("issued_at: " + t.UTC().Format(time.RFC3339) + "\n")
	return b.String(), nil
}

// SignKeyRotation creates a KeyRotation signed by oldPriv delegating to newPub.
func SignKeyRotation(oldPriv ed25519.PrivateKey, newPub ed25519.PublicKey, issuedAt time.Time) (KeyRotation, error) {
	if len(oldPriv) != ed25519.PrivateKeySize {
		return KeyRotation{}, errors.New("invalid old ed25519 private key")
	}
	if len(newPub) != ed25519.PublicKeySize {
		return KeyRotation{}, errors.New("invalid new ed25519 public key")
	}
	oldPub := oldPriv.Public().(ed25519.PublicKey)
	rot := KeyRotation{
		OldPublicKey: PublicKeyString(oldPub),
		NewPublicKey: PublicKeyString(newPub),
		IssuedAt:     issuedAt.UTC().Truncate(time.Second).Format(time.RFC3339),
	}
	text, err := rot.CanonicalText()
	if err != nil {
		return KeyRotation{}, err
	}
	sig := ed25519.Sign(oldPriv, []byte(text))
	rot.Signature = formatSignature(sig)
	return rot, nil
}

// Verify checks r's fields and verifies its signature against r.OldPublicKey.
func (r KeyRotation) Verify() (oldPub, newPub ed25519.PublicKey, issuedAt time.Time, err error) {
	text, err := r.CanonicalText()
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	oldPub, _ = ParsePublicKey(r.OldPublicKey)
	newPub, _ = ParsePublicKey(r.NewPublicKey)
	issuedAt, _ = time.Parse(time.RFC3339, r.IssuedAt)
	sig, err := parseSignature(r.Signature)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("rotation signature: %w", err)
	}
	if !ed25519.Verify(oldPub, []byte(text), sig) {
		return nil, nil, time.Time{}, fmt.Errorf("rotation from %s to %s failed signature verification", KeyID(oldPub), KeyID(newPub))
	}
	return oldPub, newPub, issuedAt.UTC(), nil
}

// Manifest describes one per-platform grain-agentd release binary and carries
// its Ed25519 release signature (and optional key-rotation chain).
type Manifest struct {
	Schema              int           `json:"schema"`
	Version             string        `json:"version"`
	Platform            string        `json:"platform"`
	Asset               string        `json:"asset"`
	SHA256              string        `json:"sha256"`
	Size                int64         `json:"size"`
	MinContractFeatures []string      `json:"min_contract_features"`
	Features            []string      `json:"features,omitempty"`
	KeyID               string        `json:"key_id,omitempty"`
	Signature           string        `json:"signature,omitempty"`
	Rotations           []KeyRotation `json:"rotations,omitempty"`
}

// NewManifestForBinary constructs an unsigned Manifest for binary on platform.
func NewManifestForBinary(version, platform string, binary []byte, minFeatures []string) (Manifest, error) {
	asset, err := AssetName(platform)
	if err != nil {
		return Manifest{}, err
	}
	if len(binary) == 0 {
		return Manifest{}, fmt.Errorf("%s binary is empty", asset)
	}
	if strings.TrimSpace(version) == "" {
		version = "unversioned"
	}
	if len(minFeatures) == 0 {
		minFeatures = DefaultMinContractFeatures()
	}
	sum := sha256.Sum256(binary)
	m := Manifest{
		Schema:              ManifestSchema,
		Version:             version,
		Platform:            platform,
		Asset:               asset,
		SHA256:              hex.EncodeToString(sum[:]),
		Size:                int64(len(binary)),
		MinContractFeatures: slices.Clone(minFeatures),
		Features:            CurrentContractFeatures(),
	}
	if err := m.ValidateStructural(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ParseManifest decodes a JSON Manifest, rejecting unknown or trailing fields,
// and validates its structural invariants.
func ParseManifest(data []byte) (Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("decoding grain-agentd manifest: %w", err)
	}
	if dec.More() {
		return Manifest{}, errors.New("decoding grain-agentd manifest: unexpected trailing JSON data")
	}
	if err := m.ValidateStructural(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ParseRotations decodes a JSON array of KeyRotation entries and verifies each
// individual hop's signature and linkage.
func ParseRotations(data []byte) ([]KeyRotation, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rots []KeyRotation
	if err := dec.Decode(&rots); err != nil {
		return nil, fmt.Errorf("decoding key rotations: %w", err)
	}
	if len(rots) > MaxRotations {
		return nil, fmt.Errorf("too many key rotations (%d > %d)", len(rots), MaxRotations)
	}
	var prevNew ed25519.PublicKey
	var prevTime time.Time
	seen := map[string]bool{}
	for i, r := range rots {
		oldPub, newPub, issued, err := r.Verify()
		if err != nil {
			return nil, fmt.Errorf("rotation[%d]: %w", i, err)
		}
		oldID, newID := KeyID(oldPub), KeyID(newPub)
		if i == 0 {
			seen[oldID] = true
		} else {
			if !bytes.Equal(oldPub, prevNew) {
				return nil, fmt.Errorf("rotation[%d] old_public_key %s does not chain from previous new_public_key %s", i, oldID, KeyID(prevNew))
			}
			if issued.Before(prevTime) {
				return nil, fmt.Errorf("rotation[%d] issued_at %s precedes previous rotation %s", i, r.IssuedAt, prevTime.Format(time.RFC3339))
			}
		}
		if seen[newID] {
			return nil, fmt.Errorf("rotation[%d] introduces a key cycle at %s", i, newID)
		}
		seen[newID] = true
		prevNew = newPub
		prevTime = issued
	}
	return rots, nil
}

// ValidateStructural checks every non-cryptographic invariant of m.
func (m Manifest) ValidateStructural() error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("unsupported manifest schema %d (want %d)", m.Schema, ManifestSchema)
	}
	if !versionTokenRE.MatchString(m.Version) {
		return fmt.Errorf("invalid manifest version %q", m.Version)
	}
	if !platformTokenRE.MatchString(m.Platform) || !slices.Contains(Platforms, m.Platform) {
		return fmt.Errorf("unsupported manifest platform %q", m.Platform)
	}
	wantAsset, err := AssetName(m.Platform)
	if err != nil || !assetTokenRE.MatchString(m.Asset) || m.Asset != wantAsset {
		return fmt.Errorf("manifest asset %q does not match platform %s (want %s)", m.Asset, m.Platform, wantAsset)
	}
	if !sha256HexRE.MatchString(m.SHA256) {
		return fmt.Errorf("invalid manifest sha256 %q", m.SHA256)
	}
	if m.Size <= 0 {
		return fmt.Errorf("invalid manifest size %d", m.Size)
	}
	if len(m.MinContractFeatures) == 0 {
		return errors.New("manifest min_contract_features must not be empty")
	}
	for _, f := range m.MinContractFeatures {
		if !featureTokenRE.MatchString(f) {
			return fmt.Errorf("invalid min_contract_features entry %q", f)
		}
	}
	for _, f := range m.Features {
		if !featureTokenRE.MatchString(f) {
			return fmt.Errorf("invalid features entry %q", f)
		}
	}
	if (m.Signature == "") != (m.KeyID == "") {
		return errors.New("manifest key_id and signature must either both be set or both be empty")
	}
	if m.KeyID != "" && !keyIDHexRE.MatchString(m.KeyID) {
		return fmt.Errorf("invalid manifest key_id %q", m.KeyID)
	}
	if m.Signature != "" {
		if _, err := parseSignature(m.Signature); err != nil {
			return fmt.Errorf("invalid manifest signature: %w", err)
		}
	}
	if len(m.Rotations) > MaxRotations {
		return fmt.Errorf("manifest carries %d rotations (max %d)", len(m.Rotations), MaxRotations)
	}
	return nil
}

// CanonicalText returns the deterministic, domain-separated payload that
// SignManifest signs and VerifyManifest verifies.
//
// keyID is passed explicitly (or taken from m.KeyID when already set) so the
// signing key's identifier is bound inside the signed message.
func (m Manifest) CanonicalText() (string, error) {
	if err := m.ValidateStructural(); err != nil {
		return "", err
	}
	if m.KeyID == "" {
		return "", errors.New("manifest key_id must be set before computing canonical signed text")
	}
	var b strings.Builder
	b.WriteString(ManifestSignatureDomain + "\n")
	b.WriteString("schema: " + strconv.Itoa(m.Schema) + "\n")
	b.WriteString("version: " + m.Version + "\n")
	b.WriteString("platform: " + m.Platform + "\n")
	b.WriteString("asset: " + m.Asset + "\n")
	b.WriteString("sha256: " + m.SHA256 + "\n")
	b.WriteString("size: " + strconv.FormatInt(m.Size, 10) + "\n")
	b.WriteString("min_contract_features: " + strings.Join(m.MinContractFeatures, ",") + "\n")
	if len(m.Features) > 0 {
		b.WriteString("features: " + strings.Join(m.Features, ",") + "\n")
	}
	b.WriteString("key_id: " + m.KeyID + "\n")
	return b.String(), nil
}

// SignManifest signs m with priv and attaches any verified key-rotation chain.
func SignManifest(m Manifest, priv ed25519.PrivateKey, rotations []KeyRotation) (Manifest, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return Manifest{}, errors.New("invalid ed25519 private key")
	}
	pub := priv.Public().(ed25519.PublicKey)
	if len(rotations) > 0 {
		raw, err := json.Marshal(rotations)
		if err != nil {
			return Manifest{}, err
		}
		checked, err := ParseRotations(raw)
		if err != nil {
			return Manifest{}, err
		}
		lastNew, _ := ParsePublicKey(checked[len(checked)-1].NewPublicKey)
		if !bytes.Equal(lastNew, pub) {
			return Manifest{}, fmt.Errorf("rotation chain ends at %s, not signing key %s", KeyID(lastNew), KeyID(pub))
		}
		m.Rotations = checked
	} else {
		m.Rotations = nil
	}
	m.KeyID = KeyID(pub)
	m.Signature = formatSignature(make([]byte, ed25519.SignatureSize))
	text, err := m.CanonicalText()
	if err != nil {
		return Manifest{}, err
	}
	sig := ed25519.Sign(priv, []byte(text))
	m.Signature = formatSignature(sig)
	return m, nil
}

// VerifyManifest verifies m's Ed25519 signature starting strictly from
// pinnedPub (never trusting an unpinned key from m itself).
//
// If pinnedPub rotated forward via m.Rotations, VerifyManifest verifies every
// hop from pinnedPub forward and returns the active signing public key so the
// caller can advance its pinned key file once the update succeeds. A host
// already pinned to a newer key never walks backward to a retired older key.
func VerifyManifest(m Manifest, pinnedPub ed25519.PublicKey) (ed25519.PublicKey, error) {
	if len(pinnedPub) != ed25519.PublicKeySize {
		return nil, errors.New("invalid pinned release public key")
	}
	if err := m.ValidateStructural(); err != nil {
		return nil, err
	}
	if m.Signature == "" || m.KeyID == "" {
		return nil, errors.New("manifest is unsigned")
	}
	activePub, err := resolveSigningKey(pinnedPub, m.KeyID, m.Rotations)
	if err != nil {
		return nil, err
	}
	text, err := m.CanonicalText()
	if err != nil {
		return nil, err
	}
	sig, err := parseSignature(m.Signature)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(activePub, []byte(text), sig) {
		return nil, fmt.Errorf("manifest signature verification failed for key %s", KeyID(activePub))
	}
	return activePub, nil
}

// VerifyBinary verifies both m's signature (against pinnedPub) and that binary
// has the exact size and SHA-256 digest m signs.
func VerifyBinary(m Manifest, binary []byte, pinnedPub ed25519.PublicKey) (ed25519.PublicKey, error) {
	signerPub, err := VerifyManifest(m, pinnedPub)
	if err != nil {
		return nil, err
	}
	if err := VerifyBinaryHash(m, binary); err != nil {
		return nil, err
	}
	return signerPub, nil
}

// VerifyBinaryHash checks m's structural validity and verifies that binary
// matches m.Size and m.SHA256 (without checking m.Signature).
func VerifyBinaryHash(m Manifest, binary []byte) error {
	if err := m.ValidateStructural(); err != nil {
		return err
	}
	if int64(len(binary)) != m.Size {
		return fmt.Errorf("%s has size %d, manifest says %d", m.Asset, len(binary), m.Size)
	}
	sum := sha256.Sum256(binary)
	got := hex.EncodeToString(sum[:])
	if got != m.SHA256 {
		return fmt.Errorf("%s has sha256 %s, manifest says %s", m.Asset, got, m.SHA256)
	}
	return nil
}

func resolveSigningKey(pinnedPub ed25519.PublicKey, targetKeyID string, rotations []KeyRotation) (ed25519.PublicKey, error) {
	if len(rotations) == 0 {
		if KeyID(pinnedPub) != targetKeyID {
			return nil, fmt.Errorf("manifest signed by key %s, not pinned key %s (and carries no key rotation)", targetKeyID, KeyID(pinnedPub))
		}
		return pinnedPub, nil
	}
	raw, err := json.Marshal(rotations)
	if err != nil {
		return nil, err
	}
	checked, err := ParseRotations(raw)
	if err != nil {
		return nil, err
	}
	// If pinnedPub is already the final key at the end of the chain, accept
	// it directly without needing any rotation step.
	if KeyID(pinnedPub) == targetKeyID {
		lastNew, _ := ParsePublicKey(checked[len(checked)-1].NewPublicKey)
		if bytes.Equal(lastNew, pinnedPub) {
			return pinnedPub, nil
		}
		return nil, fmt.Errorf("manifest carries rotations ending at %s while claiming key_id %s", KeyID(lastNew), targetKeyID)
	}
	// Otherwise find pinnedPub as an OldPublicKey in the verified chain and
	// walk strictly forward to the end of the chain.
	startIdx := -1
	for i, r := range checked {
		oldPub, _ := ParsePublicKey(r.OldPublicKey)
		if bytes.Equal(oldPub, pinnedPub) {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return nil, fmt.Errorf("manifest signed by key %s does not chain from pinned key %s", targetKeyID, KeyID(pinnedPub))
	}
	finalPub, _ := ParsePublicKey(checked[len(checked)-1].NewPublicKey)
	if KeyID(finalPub) != targetKeyID {
		return nil, fmt.Errorf("rotation chain from %s ends at %s, not manifest key_id %s", KeyID(pinnedPub), KeyID(finalPub), targetKeyID)
	}
	return finalPub, nil
}

func formatSignature(sig []byte) string {
	return "ed25519:" + base64.StdEncoding.EncodeToString(sig)
}

func parseSignature(s string) ([]byte, error) {
	b64, ok := strings.CutPrefix(strings.TrimSpace(s), "ed25519:")
	if !ok || b64 == "" {
		return nil, errors.New("signature must start with ed25519:")
	}
	sig, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Also accept unpadded base64.
		sig, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	}
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, errors.New("invalid 64-byte ed25519 signature")
	}
	return sig, nil
}
