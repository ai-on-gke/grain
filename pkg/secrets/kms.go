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

package secrets

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A key file wrapped by Cloud KMS (design/design/secrets-key-protection.design.md
// §3a, Phase 2). The X25519 private key is 32 bytes, so KMS encrypts the
// key itself; there is no data key in between. The file is two lines:
//
//	grain-secret-key-kms-v1:<projects/P/locations/L/keyRings/R/cryptoKeys/K>:<base64 KMS ciphertext>
//	grain-secret-pub-v1:<base64 public key>
//
// The first is the design's format. The second is the public half the
// AAD binds the ciphertext to, carried in the clear so that the AAD can
// be rebuilt before the key is unwrapped -- and so that `grain state
// status`, the retired-key list and the State pane can say which key a
// wrapped file holds without a KMS call. It is the same public key
// `grain state key show` prints, so it tells nobody anything new.
const (
	kmsKeyPrefix = "grain-secret-key-kms-v1:"
	// kmsAADPrefix is the AAD's domain separator; the public key's 32
	// raw bytes follow it. Part of the file format: a blob wrapped for
	// one deployment's key does not decrypt as another's.
	kmsAADPrefix = "grain secrets key v1\x00"

	// DefaultKMSEndpoint is Cloud KMS's v1 REST root.
	DefaultKMSEndpoint = "https://cloudkms.googleapis.com/v1/"
	// KMSEndpointEnv overrides the endpoint, for tests against a fake
	// (as SecretManagerEndpointEnv does for Secret Manager).
	KMSEndpointEnv = "GRAIN_KMS_ENDPOINT"
	// KMSTokenEnv supplies a static Bearer token instead of the
	// metadata server's, for the same tests.
	KMSTokenEnv = "GRAIN_KMS_TOKEN"
)

// Key protection modes, as KeyFileInfo and Store.KeyProtection report
// them.
const (
	ProtectionFile = "file"
	ProtectionKMS  = "kms"
)

// kmsKeyName is the shape of a Cloud KMS CryptoKey resource name. A
// version is not accepted: decrypt picks the version from the
// ciphertext, and encrypt always uses the primary. Each segment is held
// to the characters Cloud KMS and project IDs use, since the name is
// spliced into the URL the daemon sends its Bearer token to: a "?", "#",
// "%" or ".." in one would change what that request asks for.
var kmsKeyName = regexp.MustCompile(`^projects/[A-Za-z0-9_-]+/locations/[A-Za-z0-9_-]+/keyRings/[A-Za-z0-9_-]+/cryptoKeys/[A-Za-z0-9_-]+$`)

// ValidateKMSKeyName reports whether name is a CryptoKey resource name.
func ValidateKMSKeyName(name string) error {
	if !kmsKeyName.MatchString(name) {
		return fmt.Errorf("secrets: %q is not a Cloud KMS key name (projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>)", name)
	}
	return nil
}

// kmsKeyVersionName is a CryptoKeyVersion resource name: what an
// asymmetric signing key is named by, since each version is its own key
// pair. Its segments are held to the same characters as kmsKeyName's.
var kmsKeyVersionName = regexp.MustCompile(`^projects/[A-Za-z0-9_-]+/locations/[A-Za-z0-9_-]+/keyRings/[A-Za-z0-9_-]+/cryptoKeys/[A-Za-z0-9_-]+/cryptoKeyVersions/[0-9]+$`)

// ValidateKMSKeyVersionName reports whether name is a CryptoKeyVersion
// resource name.
func ValidateKMSKeyVersionName(name string) error {
	if !kmsKeyVersionName.MatchString(name) {
		return fmt.Errorf("secrets: %q is not a Cloud KMS key version name (projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>/cryptoKeyVersions/<n>)", name)
	}
	return nil
}

// ErrKMS is what every KMSError Is: the store's key could not be
// unwrapped because KMS did not answer, or refused. The store treats it
// as transient -- it stays open and tries again with backoff -- because
// an outage or an IAM change that is put right should unlock a running
// daemon without a restart.
var ErrKMS = errors.New("secrets: Cloud KMS")

// KMSError is a failed call to Cloud KMS, phrased for an operator:
// "KMS unreachable for <key>" or "KMS permission denied for <key>".
type KMSError struct {
	Key string
	// Reason is "unreachable", "permission denied" or "refused".
	Reason string
	Err    error
}

func (e *KMSError) Error() string {
	if e.Reason == "refused" {
		return fmt.Sprintf("secrets: KMS refused the request for %s: %v", e.Key, e.Err)
	}
	return fmt.Sprintf("secrets: KMS %s for %s: %v", e.Reason, e.Key, e.Err)
}

func (e *KMSError) Unwrap() error        { return e.Err }
func (e *KMSError) Is(target error) bool { return target == ErrKMS }

// WrappedKey is a key file's KMS-wrapped form.
type WrappedKey struct {
	// KMSKey is the CryptoKey resource name that wrapped it.
	KMSKey string
	// Ciphertext is KMS's ciphertext of the 32-byte private key.
	Ciphertext []byte
	// Public is the key's public half, in the form Key.Public renders.
	Public string
}

// String renders the file form.
func (w WrappedKey) String() string {
	return kmsKeyPrefix + w.KMSKey + ":" + base64.StdEncoding.EncodeToString(w.Ciphertext) + "\n" + w.Public
}

// IsWrappedKey reports whether a key file's contents are the KMS form.
func IsWrappedKey(data string) bool {
	return strings.HasPrefix(strings.TrimSpace(data), kmsKeyPrefix)
}

// ParseWrappedKey reads the form WrappedKey.String renders.
func ParseWrappedKey(s string) (WrappedKey, error) {
	lines := strings.Fields(strings.TrimSpace(s))
	if len(lines) != 2 || !strings.HasPrefix(lines[0], kmsKeyPrefix) {
		return WrappedKey{}, fmt.Errorf("secrets: a wrapped key is %q<key name>:<ciphertext> and then its public key", kmsKeyPrefix)
	}
	rest := strings.TrimPrefix(lines[0], kmsKeyPrefix)
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return WrappedKey{}, errors.New("secrets: a wrapped key needs a KMS key name and a ciphertext")
	}
	name, b64 := rest[:i], rest[i+1:]
	if err := ValidateKMSKeyName(name); err != nil {
		return WrappedKey{}, err
	}
	ct, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(ct) == 0 {
		return WrappedKey{}, errors.New("secrets: a wrapped key's ciphertext is not valid base64")
	}
	if _, err := publicKeyBytes(lines[1]); err != nil {
		return WrappedKey{}, err
	}
	return WrappedKey{KMSKey: name, Ciphertext: ct, Public: lines[1]}, nil
}

func publicKeyBytes(public string) ([]byte, error) {
	if !strings.HasPrefix(public, pubPrefix) {
		return nil, fmt.Errorf("secrets: a public key must start with %q", pubPrefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(public, pubPrefix))
	if err != nil {
		return nil, fmt.Errorf("secrets: the public key is not valid base64: %w", err)
	}
	if _, err := ecdh.X25519().NewPublicKey(raw); err != nil {
		return nil, fmt.Errorf("secrets: the public key is not a valid X25519 key: %w", err)
	}
	return raw, nil
}

// kmsAAD is the additional authenticated data a key is wrapped under.
func kmsAAD(public string) ([]byte, error) {
	raw, err := publicKeyBytes(public)
	if err != nil {
		return nil, err
	}
	return append([]byte(kmsAADPrefix), raw...), nil
}

// KeyFileInfo is what a key file says about itself without being
// unwrapped: how it is protected, by which KMS key, and its public half.
type KeyFileInfo struct {
	Path       string `json:"path"`
	Protection string `json:"protection"`
	KMSKey     string `json:"kmsKey,omitempty"`
	Public     string `json:"public,omitempty"`
}

// ReadKeyFileInfo describes the key file at path, reporting ErrNoKey
// when there is none. It never calls KMS.
func ReadKeyFileInfo(path string) (KeyFileInfo, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return KeyFileInfo{}, fmt.Errorf("%w at %s", ErrNoKey, path)
	}
	if err != nil {
		return KeyFileInfo{}, fmt.Errorf("secrets: reading %s: %w", path, err)
	}
	if IsWrappedKey(string(data)) {
		w, err := ParseWrappedKey(string(data))
		if err != nil {
			return KeyFileInfo{}, fmt.Errorf("secrets: %s: %w", path, err)
		}
		return KeyFileInfo{Path: path, Protection: ProtectionKMS, KMSKey: w.KMSKey, Public: w.Public}, nil
	}
	k, err := ParseKey(string(data))
	if err != nil {
		return KeyFileInfo{}, err
	}
	return KeyFileInfo{Path: path, Protection: ProtectionFile, Public: k.Public()}, nil
}

// KMSClient calls Cloud KMS's encrypt and decrypt over REST, with a
// token from the metadata server -- the VM's service account on GCE,
// the Workload Identity-bound account on GKE.
type KMSClient struct {
	// Endpoint is the REST root; $GRAIN_KMS_ENDPOINT, then
	// DefaultKMSEndpoint, when empty.
	Endpoint string
	HTTP     *http.Client
	// Token overrides how the Bearer token is got.
	Token func(ctx context.Context) (string, error)

	tokMu      sync.Mutex
	tokCached  string
	tokExpires time.Time
}

var (
	defaultKMSOnce   sync.Once
	defaultKMSClient *KMSClient
)

// DefaultKMSClient is the process-wide client, so one token serves every
// call a process makes.
func DefaultKMSClient() *KMSClient {
	defaultKMSOnce.Do(func() { defaultKMSClient = &KMSClient{} })
	return defaultKMSClient
}

func (c *KMSClient) endpoint() string {
	ep := c.Endpoint
	if ep == "" {
		ep = strings.TrimSpace(os.Getenv(KMSEndpointEnv))
	}
	if ep == "" {
		ep = DefaultKMSEndpoint
	}
	return strings.TrimRight(ep, "/") + "/"
}

func (c *KMSClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *KMSClient) token(ctx context.Context) (string, error) {
	if c.Token != nil {
		return c.Token(ctx)
	}
	if tok := strings.TrimSpace(os.Getenv(KMSTokenEnv)); tok != "" {
		return tok, nil
	}
	c.tokMu.Lock()
	defer c.tokMu.Unlock()
	if c.tokCached != "" && time.Now().Before(c.tokExpires) {
		return c.tokCached, nil
	}
	tok, ttl, err := fetchMetadataToken(ctx, c.httpClient())
	if err != nil {
		return "", err
	}
	c.tokCached, c.tokExpires = tok, time.Now().Add(ttl)
	return tok, nil
}

// call POSTs one KMS method on name and decodes its answer into out,
// classifying a failure as a KMSError. A nil in is a GET.
func (c *KMSClient) call(ctx context.Context, name, method string, in, out any) error {
	tok, err := c.token(ctx)
	if err != nil {
		return &KMSError{Key: name, Reason: "unreachable", Err: err}
	}
	verb, body := http.MethodGet, io.Reader(nil)
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		verb, body = http.MethodPost, bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, verb, c.endpoint()+name+":"+method, body)
	if err != nil {
		return &KMSError{Key: name, Reason: "unreachable", Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return &KMSError{Key: name, Reason: "unreachable", Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &KMSError{Key: name, Reason: "unreachable", Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, kmsErrorMessage(data))
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return &KMSError{Key: name, Reason: "permission denied", Err: msg}
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			return &KMSError{Key: name, Reason: "unreachable", Err: msg}
		default:
			return &KMSError{Key: name, Reason: "refused", Err: msg}
		}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &KMSError{Key: name, Reason: "refused", Err: fmt.Errorf("decoding the %s response: %w", method, err)}
	}
	return nil
}

// kmsErrorMessage pulls error.message out of a Google API error body,
// falling back to the body itself.
func kmsErrorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// Encrypt wraps plaintext under name's primary version.
func (c *KMSClient) Encrypt(ctx context.Context, name string, plaintext, aad []byte) ([]byte, error) {
	var out struct {
		Ciphertext string `json:"ciphertext"`
	}
	err := c.call(ctx, name, "encrypt", map[string]string{
		"plaintext":                   base64.StdEncoding.EncodeToString(plaintext),
		"additionalAuthenticatedData": base64.StdEncoding.EncodeToString(aad),
	}, &out)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(out.Ciphertext)
	if err != nil || len(ct) == 0 {
		return nil, &KMSError{Key: name, Reason: "refused", Err: errors.New("encrypt returned no ciphertext")}
	}
	return ct, nil
}

// Decrypt unwraps ciphertext. usedPrimary is false when it was wrapped
// under a version that is no longer the primary -- KMS rotated since --
// which is the caller's cue to rewrap.
func (c *KMSClient) Decrypt(ctx context.Context, name string, ciphertext, aad []byte) (plaintext []byte, usedPrimary bool, err error) {
	var out struct {
		Plaintext   string `json:"plaintext"`
		UsedPrimary bool   `json:"usedPrimary"`
	}
	err = c.call(ctx, name, "decrypt", map[string]string{
		"ciphertext":                  base64.StdEncoding.EncodeToString(ciphertext),
		"additionalAuthenticatedData": base64.StdEncoding.EncodeToString(aad),
	}, &out)
	if err != nil {
		return nil, false, err
	}
	pt, err := base64.StdEncoding.DecodeString(out.Plaintext)
	if err != nil {
		return nil, false, &KMSError{Key: name, Reason: "refused", Err: errors.New("decrypt returned a plaintext that is not base64")}
	}
	return pt, out.UsedPrimary, nil
}

// AsymmetricSign signs a SHA-256 digest with the key version name, and
// returns the ASN.1 DER signature an EC_SIGN_P256_SHA256 key answers in.
func (c *KMSClient) AsymmetricSign(ctx context.Context, name string, digest [sha256.Size]byte) ([]byte, error) {
	if err := ValidateKMSKeyVersionName(name); err != nil {
		return nil, err
	}
	var out struct {
		Signature string `json:"signature"`
	}
	in := map[string]any{"digest": map[string]string{"sha256": base64.StdEncoding.EncodeToString(digest[:])}}
	if err := c.call(ctx, name, "asymmetricSign", in, &out); err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(out.Signature)
	if err != nil || len(sig) == 0 {
		return nil, &KMSError{Key: name, Reason: "refused", Err: errors.New("asymmetricSign returned no signature")}
	}
	return sig, nil
}

// GetPublicKey is the public half of the key version name, which must
// be an EC_SIGN_P256_SHA256 key.
func (c *KMSClient) GetPublicKey(ctx context.Context, name string) (*ecdsa.PublicKey, error) {
	if err := ValidateKMSKeyVersionName(name); err != nil {
		return nil, err
	}
	var out struct {
		PEM       string `json:"pem"`
		Algorithm string `json:"algorithm"`
	}
	if err := c.call(ctx, name, "getPublicKey", nil, &out); err != nil {
		return nil, err
	}
	if out.Algorithm != "EC_SIGN_P256_SHA256" {
		return nil, &KMSError{Key: name, Reason: "refused", Err: fmt.Errorf("the key's algorithm is %q, not EC_SIGN_P256_SHA256", out.Algorithm)}
	}
	block, _ := pem.Decode([]byte(out.PEM))
	if block == nil {
		return nil, &KMSError{Key: name, Reason: "refused", Err: errors.New("getPublicKey returned no PEM block")}
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, &KMSError{Key: name, Reason: "refused", Err: fmt.Errorf("getPublicKey: %w", err)}
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, &KMSError{Key: name, Reason: "refused", Err: errors.New("getPublicKey returned a key that is not ECDSA P-256")}
	}
	return ec, nil
}

// WrapKey wraps k under the KMS key name, and unwraps the result to
// check it is k before returning it: nothing is written by anyone on the
// strength of a wrap that has not been read back.
func WrapKey(ctx context.Context, c *KMSClient, name string, k Key) (WrappedKey, error) {
	if err := ValidateKMSKeyName(name); err != nil {
		return WrappedKey{}, err
	}
	if k.priv == nil {
		return WrappedKey{}, errors.New("secrets: no key to wrap")
	}
	aad, err := kmsAAD(k.Public())
	if err != nil {
		return WrappedKey{}, err
	}
	ct, err := c.Encrypt(ctx, name, k.priv.Bytes(), aad)
	if err != nil {
		return WrappedKey{}, err
	}
	w := WrappedKey{KMSKey: name, Ciphertext: ct, Public: k.Public()}
	back, _, err := UnwrapKey(ctx, c, w)
	if err != nil {
		return WrappedKey{}, fmt.Errorf("secrets: the key wrapped, but unwrapping it again to check failed: %w", err)
	}
	if back.String() != k.String() {
		return WrappedKey{}, fmt.Errorf("secrets: %s unwrapped the key it had just wrapped to a different one; nothing was written", name)
	}
	return w, nil
}

// UnwrapKey asks KMS for the key w holds.
func UnwrapKey(ctx context.Context, c *KMSClient, w WrappedKey) (Key, bool, error) {
	aad, err := kmsAAD(w.Public)
	if err != nil {
		return Key{}, false, err
	}
	raw, usedPrimary, err := c.Decrypt(ctx, w.KMSKey, w.Ciphertext, aad)
	if err != nil {
		return Key{}, false, err
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return Key{}, false, fmt.Errorf("secrets: %s unwrapped to something that is not an X25519 key: %w", w.KMSKey, err)
	}
	k := Key{priv: priv}
	if k.Public() != w.Public {
		return Key{}, false, fmt.Errorf("secrets: %s unwrapped to %s, not the %s the file names", w.KMSKey, k.Public(), w.Public)
	}
	return k, usedPrimary, nil
}

// KMSKeySource loads a key file in the KMS-wrapped form: one decrypt
// call. When KMS says the blob is under a version that is no longer the
// primary, it rewraps the key under the primary and replaces the file,
// so that disabling an old version after a rotation never strands this
// deployment. A failed rewrap is logged and the key still loads: the
// old version still decrypts, and the next start tries again.
//
// FileKeySource hands a wrapped file here on its own, by its prefix, so
// nothing has to configure it.
type KMSKeySource struct {
	Path string
	// Client is the KMS client; DefaultKMSClient when nil.
	Client *KMSClient
}

func (s KMSKeySource) client() *KMSClient {
	if s.Client != nil {
		return s.Client
	}
	return DefaultKMSClient()
}

// Load unwraps the file.
func (s KMSKeySource) Load(ctx context.Context) (Key, error) {
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return Key{}, fmt.Errorf("%w at %s", ErrNoKey, s.Path)
	}
	if err != nil {
		return Key{}, fmt.Errorf("secrets: reading %s: %w", s.Path, err)
	}
	w, err := ParseWrappedKey(string(data))
	if err != nil {
		return Key{}, fmt.Errorf("secrets: %s: %w", s.Path, err)
	}
	k, usedPrimary, err := UnwrapKey(ctx, s.client(), w)
	if err != nil {
		return Key{}, err
	}
	if !usedPrimary {
		if err := s.rewrap(ctx, w.KMSKey, k); err != nil {
			log.Printf("grain: %s is wrapped under an older version of %s and rewrapping it under the primary failed (it still unwraps; the next start tries again): %v",
				s.Path, w.KMSKey, err)
		} else {
			log.Printf("grain: rewrapped %s under the primary version of %s", s.Path, w.KMSKey)
		}
	}
	return k, nil
}

func (s KMSKeySource) rewrap(ctx context.Context, name string, k Key) error {
	w, err := WrapKey(ctx, s.client(), name, k)
	if err != nil {
		return err
	}
	return writeKeyFileData(s.Path, w.String())
}

// Describe names the KMS key and the file.
func (s KMSKeySource) Describe() string {
	if info, err := ReadKeyFileInfo(s.Path); err == nil && info.KMSKey != "" {
		return s.Path + " (wrapped by Cloud KMS key " + info.KMSKey + ")"
	}
	return s.Path
}

// writeKeyFileData writes a key file's contents 0600 through a
// temporary file and a rename, as WriteKeyFile does.
func writeKeyFileData(path, contents string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secrets: preparing %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".secrets-key-*")
	if err != nil {
		return fmt.Errorf("secrets: creating a temporary file next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secrets: securing %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.WriteString(strings.TrimRight(contents, "\n") + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("secrets: writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
