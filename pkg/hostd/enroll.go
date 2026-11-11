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

package hostd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// Proof-of-possession headers sent on /api/hostd/connect for key-bound hosts.
const (
	TimestampHeader = "X-Grain-Hostd-Timestamp"
	NonceHeader     = "X-Grain-Hostd-Nonce"
	SignatureHeader = "X-Grain-Hostd-Signature"
)

// ConnectProofSkew bounds clock skew and replay retention on signed host
// requests (/api/hostd/connect, /api/hostd/join*, /api/hostd/renew, OpRenew).
const ConnectProofSkew = 5 * time.Minute

const hostPrivateKeyPEMType = "GRAIN HOST PRIVATE KEY"

const pairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// PublicKeyString formats pub as stored in the host registry ("ed25519:<base64>").
func PublicKeyString(pub ed25519.PublicKey) string {
	return "ed25519:" + base64.RawStdEncoding.EncodeToString(pub)
}

// ParsePublicKey parses an "ed25519:<base64>" public key string.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	b64, ok := strings.CutPrefix(s, "ed25519:")
	if !ok || b64 == "" {
		return nil, fmt.Errorf("host public key must start with ed25519:")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid ed25519 public key")
	}
	return ed25519.PublicKey(raw), nil
}

// KeyFingerprint returns the SHA-256 fingerprint ("SHA256:<base64>") of pub.
func KeyFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// DerivePairingCode deterministically derives the short XXXX-XXXX pairing code
// from the join request ID, server nonce, Ed25519 public key, and hostname so
// both the enrolling host's terminal and Settings -> Hosts display the same
// code for the deployment owner to compare.
func DerivePairingCode(requestID, serverNonce, publicKey, hostname string) string {
	msg := strings.Join([]string{
		"grain-host-pairing-v1",
		strings.TrimSpace(requestID),
		strings.TrimSpace(serverNonce),
		strings.TrimSpace(publicKey),
		strings.ToLower(strings.TrimSpace(hostname)),
	}, "\n")
	sum := sha256.Sum256([]byte(msg))
	var bits uint64
	for i := 0; i < 5; i++ {
		bits = (bits << 8) | uint64(sum[i])
	}
	var out [9]byte
	for i := 0; i < 8; i++ {
		idx := (bits >> uint(35-5*i)) & 0x1f
		pos := i
		if i >= 4 {
			pos++
		}
		out[pos] = pairingAlphabet[idx]
	}
	out[4] = '-'
	return string(out[:])
}

// EncodePrivateKeyPEM encodes an Ed25519 private key as PEM.
func EncodePrivateKeyPEM(priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid ed25519 private key length %d", len(priv))
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ParsePrivateKeyPEM parses an Ed25519 private key from PEM bytes.
func ParsePrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not a PEM-encoded host private key")
	}
	if block.Type == hostPrivateKeyPEMType && len(block.Bytes) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(block.Bytes), nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing host private key: %w", err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("host private key is not Ed25519")
	}
	return priv, nil
}

// LoadPrivateKey reads an Ed25519 private key from path, refusing symlinks and
// group/world-accessible modes (must be 0600 or 0400 on Unix).
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("empty host private key path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("host private key %s must be a regular file, not a symlink", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("host private key %s has permissions %04o; must be 0600", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePrivateKeyPEM(data)
}

// GenerateOrLoadPrivateKey loads the host's Ed25519 private key at path if it
// already exists, or generates a fresh keypair and writes it mode 0600.
func GenerateOrLoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	if priv, err := LoadPrivateKey(path); err == nil {
		return priv, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating directory for %s: %w", path, err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating host keypair: %w", err)
	}
	data, err := EncodePrivateKeyPEM(priv)
	if err != nil {
		return nil, err
	}
	if err := WriteSecretFile0600(path, data); err != nil {
		return nil, err
	}
	return priv, nil
}

// TokenFileIssuedAt is when the credential in path was written -- issued on
// acceptance or by the last silent renewal, which rewrites the file -- for
// Server.RenewedAt; zero when path is empty or unreadable.
func TokenFileIssuedAt(path string) time.Time {
	if strings.TrimSpace(path) == "" {
		return time.Time{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// WriteSecretFile0600 atomically writes data to path with permissions 0600,
// refusing to overwrite a symlink.
func WriteSecretFile0600(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write secret through symlink %s", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".grain-secret-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func randomHexNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sha256HexString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// maxReplayEntries bounds NonceReplayCache: POST /api/hostd/join is
// served without the access code, and any self-signed request records a
// nonce before the enrollment code is checked, so an unbounded cache is
// memory an anonymous caller can fill. Past the bound (after pruning what
// has expired) new nonces are refused, which fails closed.
const maxReplayEntries = 1 << 16

// replayPruneEvery is how often CheckAndRecord sweeps expired entries
// once the cache is large, so a full cache does not cost a whole-map walk
// on every request.
const replayPruneEvery = time.Second

// NonceReplayCache rejects replayed Ed25519 request nonces within ConnectProofSkew.
type NonceReplayCache struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	lastPrune time.Time
}

// CheckAndRecord returns true when nonce has not been seen for scope within
// window, recording it until now + window. It returns false, refusing the
// request, for a replayed nonce and when the cache is full of live nonces.
func (c *NonceReplayCache) CheckAndRecord(scope, nonce string, now time.Time, window time.Duration) bool {
	if c == nil {
		return true
	}
	now = now.UTC()
	key := scope + "\x00" + nonce
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]time.Time{}
	}
	if len(c.seen) > 4096 && now.Sub(c.lastPrune) >= replayPruneEvery {
		c.lastPrune = now
		for k, exp := range c.seen {
			if !exp.After(now) {
				delete(c.seen, k)
			}
		}
	}
	if exp, exists := c.seen[key]; exists && exp.After(now) {
		return false
	}
	if len(c.seen) >= maxReplayEntries {
		return false
	}
	c.seen[key] = now.Add(window)
	return true
}

func connectCanonicalPayload(method, requestURI, token, ts, nonce string) []byte {
	return []byte(strings.Join([]string{
		"grain-hostd-connect-v1",
		strings.ToUpper(strings.TrimSpace(method)),
		requestURI,
		sha256HexString(strings.TrimSpace(token)),
		ts,
		nonce,
	}, "\n"))
}

// SetConnectHeaders signs req's /api/hostd/connect upgrade with priv, binding
// the signature to the request method, path, token digest, timestamp and nonce.
func SetConnectHeaders(req *http.Request, token string, priv ed25519.PrivateKey, now time.Time) error {
	if len(priv) != ed25519.PrivateKeySize {
		return errors.New("invalid ed25519 private key")
	}
	nonce, err := randomHexNonce()
	if err != nil {
		return err
	}
	ts := now.UTC().Format(time.RFC3339Nano)
	uri := req.URL.RequestURI()
	if uri == "" {
		uri = RemotePath
	}
	sig := ed25519.Sign(priv, connectCanonicalPayload(req.Method, uri, token, ts, nonce))
	req.Header.Set(TimestampHeader, ts)
	req.Header.Set(NonceHeader, nonce)
	req.Header.Set(SignatureHeader, base64.RawStdEncoding.EncodeToString(sig))
	return nil
}

// VerifyConnectHeaders verifies that req carries a fresh, un-replayed Ed25519
// proof-of-possession signature for publicKey over token.
func VerifyConnectHeaders(req *http.Request, token, publicKey string, now time.Time, replay *NonceReplayCache) error {
	pub, err := ParsePublicKey(publicKey)
	if err != nil {
		return err
	}
	tsStr := strings.TrimSpace(req.Header.Get(TimestampHeader))
	nonce := strings.TrimSpace(req.Header.Get(NonceHeader))
	sigStr := strings.TrimSpace(req.Header.Get(SignatureHeader))
	if tsStr == "" || nonce == "" || sigStr == "" {
		return errors.New("missing key-bound signature headers; this host credential requires its Ed25519 private key")
	}
	if len(nonce) < 16 || len(nonce) > 128 {
		return errors.New("invalid host signature nonce")
	}
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return errors.New("invalid host signature timestamp")
	}
	delta := now.UTC().Sub(ts.UTC())
	if delta < -ConnectProofSkew || delta > ConnectProofSkew {
		return errors.New("host signature timestamp outside allowed clock skew")
	}
	sig, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(sigStr, "="))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("malformed host signature")
	}
	uri := req.URL.RequestURI()
	if uri == "" {
		uri = RemotePath
	}
	if !ed25519.Verify(pub, connectCanonicalPayload(req.Method, uri, token, tsStr, nonce), sig) {
		return errors.New("host signature verification failed")
	}
	if !replay.CheckAndRecord("connect:"+publicKey, nonce, now, 2*ConnectProofSkew) {
		return errors.New("replayed host signature nonce")
	}
	return nil
}

// JoinSubmitRequest is the JSON body POSTed to /api/hostd/join.
type JoinSubmitRequest struct {
	EnrollmentCode string   `json:"enrollment_code,omitempty"`
	RekeyHostd     string   `json:"rekey_hostd,omitempty"`
	PublicKey      string   `json:"public_key"`
	Hostname       string   `json:"hostname"`
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	CLIs           []string `json:"clis,omitempty"`
	Version        string   `json:"version,omitempty"`
	LocalUser      string   `json:"local_user,omitempty"`
	GitHubToken    string   `json:"github_token,omitempty"`
	Timestamp      string   `json:"timestamp"`
	Nonce          string   `json:"nonce"`
	Signature      string   `json:"signature"`
}

func joinCanonicalPayload(r JoinSubmitRequest) []byte {
	target := strings.TrimSpace(r.EnrollmentCode)
	if r.RekeyHostd != "" {
		target = "rekey:" + strings.TrimSpace(r.RekeyHostd)
	}
	return []byte(strings.Join([]string{
		"grain-hostd-join-v1",
		target,
		strings.TrimSpace(r.PublicKey),
		strings.TrimSpace(r.Hostname),
		strings.TrimSpace(r.OS),
		strings.TrimSpace(r.Arch),
		strings.TrimSpace(r.Timestamp),
		strings.TrimSpace(r.Nonce),
	}, "\n"))
}

// SignJoinSubmit populates PublicKey, Timestamp, Nonce, and Signature on r.
func SignJoinSubmit(r *JoinSubmitRequest, priv ed25519.PrivateKey, now time.Time) error {
	nonce, err := randomHexNonce()
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	r.PublicKey = PublicKeyString(pub)
	r.Timestamp = now.UTC().Format(time.RFC3339Nano)
	r.Nonce = nonce
	sig := ed25519.Sign(priv, joinCanonicalPayload(*r))
	r.Signature = base64.RawStdEncoding.EncodeToString(sig)
	return nil
}

// VerifyJoinSubmit checks r's Ed25519 signature, timestamp skew, and replay cache.
func VerifyJoinSubmit(r JoinSubmitRequest, now time.Time, replay *NonceReplayCache) (ed25519.PublicKey, error) {
	pub, err := ParsePublicKey(r.PublicKey)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Hostname) == "" {
		return nil, errors.New("hostname is required")
	}
	if (strings.TrimSpace(r.EnrollmentCode) == "") == (strings.TrimSpace(r.RekeyHostd) == "") {
		return nil, errors.New("exactly one of enrollment_code or rekey_hostd is required")
	}
	if len(r.Nonce) < 16 || len(r.Nonce) > 128 {
		return nil, errors.New("invalid join signature nonce")
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(r.Timestamp))
	if err != nil {
		return nil, errors.New("invalid join signature timestamp")
	}
	delta := now.UTC().Sub(ts.UTC())
	if delta < -ConnectProofSkew || delta > ConnectProofSkew {
		return nil, errors.New("join signature timestamp outside allowed clock skew")
	}
	sig, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(r.Signature), "="))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, errors.New("malformed join signature")
	}
	if !ed25519.Verify(pub, joinCanonicalPayload(r), sig) {
		return nil, errors.New("join signature verification failed")
	}
	if !replay.CheckAndRecord("join:"+r.PublicKey, r.Nonce, now, 2*ConnectProofSkew) {
		return nil, errors.New("replayed join signature nonce")
	}
	return pub, nil
}

// JoinSubmitResponse is returned by POST /api/hostd/join (201 Created) or on a
// 401 github_device challenge.
type JoinSubmitResponse struct {
	ID             string    `json:"id,omitempty"`
	ServerNonce    string    `json:"server_nonce,omitempty"`
	PairingCode    string    `json:"pairing_code,omitempty"`
	KeyFingerprint string    `json:"key_fingerprint,omitempty"`
	EnrolledBy     string    `json:"enrolled_by,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	AuthMode       string    `json:"auth_mode,omitempty"`
	GitHubClientID string    `json:"github_client_id,omitempty"`
	GitHubWebURL   string    `json:"github_web_url,omitempty"`
	Error          string    `json:"error,omitempty"`
}

// JoinPollRequest is POSTed to /api/hostd/join/{id}/poll, signed by the host's
// Ed25519 private key so only the host holding that key can claim its credential.
type JoinPollRequest struct {
	Timestamp string `json:"timestamp"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
}

func joinPollCanonicalPayload(requestID, publicKey, ts, nonce string) []byte {
	return []byte(strings.Join([]string{
		"grain-hostd-join-poll-v1",
		strings.TrimSpace(requestID),
		strings.TrimSpace(publicKey),
		strings.TrimSpace(ts),
		strings.TrimSpace(nonce),
	}, "\n"))
}

// SignJoinPoll signs a poll request for requestID with priv.
func SignJoinPoll(requestID string, priv ed25519.PrivateKey, now time.Time) (JoinPollRequest, error) {
	nonce, err := randomHexNonce()
	if err != nil {
		return JoinPollRequest{}, err
	}
	pubStr := PublicKeyString(priv.Public().(ed25519.PublicKey))
	ts := now.UTC().Format(time.RFC3339Nano)
	sig := ed25519.Sign(priv, joinPollCanonicalPayload(requestID, pubStr, ts, nonce))
	return JoinPollRequest{
		Timestamp: ts,
		Nonce:     nonce,
		Signature: base64.RawStdEncoding.EncodeToString(sig),
	}, nil
}

// VerifyJoinPoll verifies a JoinPollRequest against the join request's stored publicKey.
func VerifyJoinPoll(requestID, publicKey string, p JoinPollRequest, now time.Time, replay *NonceReplayCache) error {
	pub, err := ParsePublicKey(publicKey)
	if err != nil {
		return err
	}
	if len(p.Nonce) < 16 || len(p.Nonce) > 128 {
		return errors.New("invalid poll signature nonce")
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(p.Timestamp))
	if err != nil {
		return errors.New("invalid poll signature timestamp")
	}
	delta := now.UTC().Sub(ts.UTC())
	if delta < -ConnectProofSkew || delta > ConnectProofSkew {
		return errors.New("poll signature timestamp outside allowed clock skew")
	}
	sig, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(p.Signature), "="))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("malformed poll signature")
	}
	if !ed25519.Verify(pub, joinPollCanonicalPayload(requestID, publicKey, p.Timestamp, p.Nonce), sig) {
		return errors.New("poll signature verification failed")
	}
	if !replay.CheckAndRecord("poll:"+requestID, p.Nonce, now, 2*ConnectProofSkew) {
		return errors.New("replayed poll signature nonce")
	}
	return nil
}

// JoinPollResponse is returned by POST /api/hostd/join/{id}/poll.
type JoinPollResponse struct {
	Status    string    `json:"status"` // "pending" or "accepted"
	Name      string    `json:"name,omitempty"`
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// RenewProof is sent over OpRenew or POST /api/hostd/renew to prove possession
// of the host's Ed25519 private key during silent credential renewal.
type RenewProof struct {
	CurrentToken string `json:"current_token,omitempty"`
	Timestamp    string `json:"timestamp"`
	Nonce        string `json:"nonce"`
	Signature    string `json:"signature"`
}

// RenewReply is returned on OpRenew and POST /api/hostd/renew.
type RenewReply struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func renewCanonicalPayload(publicKey, currentToken, ts, nonce string) []byte {
	return []byte(strings.Join([]string{
		"grain-hostd-renew-v1",
		strings.TrimSpace(publicKey),
		sha256HexString(strings.TrimSpace(currentToken)),
		strings.TrimSpace(ts),
		strings.TrimSpace(nonce),
	}, "\n"))
}

// SignRenewProof signs a silent renewal request with priv.
func SignRenewProof(_ string, currentToken string, priv ed25519.PrivateKey, now time.Time) (RenewProof, error) {
	nonce, err := randomHexNonce()
	if err != nil {
		return RenewProof{}, err
	}
	pubStr := PublicKeyString(priv.Public().(ed25519.PublicKey))
	ts := now.UTC().Format(time.RFC3339Nano)
	sig := ed25519.Sign(priv, renewCanonicalPayload(pubStr, currentToken, ts, nonce))
	return RenewProof{
		CurrentToken: currentToken,
		Timestamp:    ts,
		Nonce:        nonce,
		Signature:    base64.RawStdEncoding.EncodeToString(sig),
	}, nil
}

// VerifyRenewProof verifies a RenewProof against publicKey.
func VerifyRenewProof(_ string, currentToken, publicKey string, p RenewProof, now time.Time, replay *NonceReplayCache) error {
	pub, err := ParsePublicKey(publicKey)
	if err != nil {
		return err
	}
	if len(p.Nonce) < 16 || len(p.Nonce) > 128 {
		return errors.New("invalid renew signature nonce")
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(p.Timestamp))
	if err != nil {
		return errors.New("invalid renew signature timestamp")
	}
	delta := now.UTC().Sub(ts.UTC())
	if delta < -ConnectProofSkew || delta > ConnectProofSkew {
		return errors.New("renew signature timestamp outside allowed clock skew")
	}
	sig, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(p.Signature), "="))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("malformed renew signature")
	}
	if !ed25519.Verify(pub, renewCanonicalPayload(publicKey, currentToken, p.Timestamp, p.Nonce), sig) {
		return errors.New("renew signature verification failed")
	}
	if !replay.CheckAndRecord("renew:"+publicKey, p.Nonce, now, 2*ConnectProofSkew) {
		return errors.New("replayed renew signature nonce")
	}
	return nil
}

// EnrollHTTPProvider builds an HTTP client and optional bearer token source for
// enrollment requests when an edition wraps grain with its own proxy transport.
type EnrollHTTPProvider func(serverURL string) (*http.Client, func(context.Context) (string, error), error)

var (
	enrollHTTPMu       sync.RWMutex
	enrollHTTPProvider EnrollHTTPProvider
)

// DetectAgentCLIs probes PATH for known agent CLIs on this host.
func DetectAgentCLIs() []string {
	candidates := []string{"agy", "claude", "codex", "gemini", "antigravity"}
	var found []string
	for _, name := range candidates {
		if _, err := exec.LookPath(name); err == nil {
			found = append(found, name)
		}
	}
	return found
}

// CurrentLocalUser returns the local OS account name running this process.
func CurrentLocalUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return ""
}

// EnrollOptions configures a host join or rekey handshake.
type EnrollOptions struct {
	ServerURL    string
	JoinCode     string
	RekeyHostd   string
	CurrentToken string
	KeyPath      string
	TokenPath    string
	Hostname     string
	OS           string
	Arch         string
	CLIs         []string
	Version      string
	LocalUser    string
	HTTPClient   *http.Client
	IDToken      func(context.Context) (string, error)
	Out          io.Writer
	PollInterval time.Duration
	// DeviceFlowClient overrides the HTTP client used for GitHub OAuth device flow in tests.
	DeviceFlowClient *http.Client
}

// EnrollResult is returned once the deployment owner accepts the host and the
// host claims its key-bound 30-day credential.
type EnrollResult struct {
	Name           string
	Token          string
	ExpiresAt      time.Time
	PairingCode    string
	KeyFingerprint string
	PrivateKey     ed25519.PrivateKey
}

// Enroll generates (or loads) the host's 0600 Ed25519 keypair, submits a join
// request with a verified identity, prints the host and key it is enrolling,
// and waits until the deployment owner accepts or rejects the host.
func Enroll(ctx context.Context, opts EnrollOptions) (*EnrollResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.ServerURL), "/")
	if baseURL == "" {
		return nil, errors.New("server URL is required")
	}
	if u, err := url.Parse(baseURL); err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid server URL %q", opts.ServerURL)
	}
	if (strings.TrimSpace(opts.JoinCode) == "") == (strings.TrimSpace(opts.RekeyHostd) == "") {
		return nil, errors.New("either --join <code> or --rekey <host> is required")
	}
	if strings.TrimSpace(opts.KeyPath) == "" {
		return nil, errors.New("private key path is required")
	}
	priv, err := GenerateOrLoadPrivateKey(opts.KeyPath)
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	pubStr := PublicKeyString(pub)
	fp := KeyFingerprint(pub)

	hostname := strings.TrimSpace(opts.Hostname)
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	if hostname == "" {
		hostname = "host"
	}
	osName := opts.OS
	if osName == "" {
		osName = runtime.GOOS
	}
	arch := opts.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	clis := opts.CLIs
	if clis == nil {
		clis = DetectAgentCLIs()
	}
	localUser := opts.LocalUser
	if localUser == "" {
		localUser = CurrentLocalUser()
	}
	out := opts.Out
	if out == nil {
		out = os.Stderr
	}
	pollEvery := opts.PollInterval
	if pollEvery <= 0 {
		pollEvery = 2 * time.Second
	}

	httpClient := opts.HTTPClient
	idToken := opts.IDToken
	if httpClient == nil {
		enrollHTTPMu.RLock()
		prov := enrollHTTPProvider
		enrollHTTPMu.RUnlock()
		if prov != nil {
			c, tokFn, err := prov(baseURL)
			if err != nil {
				return nil, err
			}
			httpClient = c
			if idToken == nil {
				idToken = tokFn
			}
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	doJSON := func(method, endpoint string, body any) (*http.Response, []byte, error) {
		rawBody, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(rawBody))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "grain-hostd")
		if opts.CurrentToken != "" {
			req.Header.Set(TokenHeader, opts.CurrentToken)
		}
		if idToken != nil {
			tok, err := idToken(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("obtaining enrolling identity token: %w", err)
			}
			if tok = strings.TrimSpace(strings.TrimPrefix(tok, "Bearer ")); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp, respBytes, err
	}

	buildSubmit := func(ghToken string) (JoinSubmitRequest, error) {
		sub := JoinSubmitRequest{
			EnrollmentCode: strings.TrimSpace(opts.JoinCode),
			RekeyHostd:     strings.TrimSpace(opts.RekeyHostd),
			Hostname:       hostname,
			OS:             osName,
			Arch:           arch,
			CLIs:           clis,
			Version:        opts.Version,
			LocalUser:      localUser,
			GitHubToken:    ghToken,
		}
		err := SignJoinSubmit(&sub, priv, time.Now().UTC())
		return sub, err
	}

	submitReq, err := buildSubmit("")
	if err != nil {
		return nil, err
	}
	resp, respBytes, err := doJSON(http.MethodPost, baseURL+"/api/hostd/join", submitReq)
	if err != nil {
		return nil, fmt.Errorf("submitting join request to %s: %w", baseURL, err)
	}
	var joinResp JoinSubmitResponse
	_ = json.Unmarshal(respBytes, &joinResp)

	if resp.StatusCode == http.StatusUnauthorized && joinResp.AuthMode == "github_device" {
		clientID := joinResp.GitHubClientID
		if clientID == "" {
			clientID = github.DefaultOAuthClientID
		}
		webURL := joinResp.GitHubWebURL
		if webURL == "" {
			webURL = "https://github.com"
		}
		dc, err := github.RequestDeviceCode(ctx, opts.DeviceFlowClient, webURL, clientID, []string{"read:user", "read:org"})
		if err != nil {
			return nil, fmt.Errorf("starting GitHub device sign-in for host enrollment: %w", err)
		}
		fmt.Fprintf(out, "\nVerify your GitHub identity to enroll %s:\n  Open: %s\n  Code: %s\nWaiting for GitHub authorization...\n",
			hostname, dc.VerificationURI, dc.UserCode)
		ghToken, err := github.PollDeviceToken(ctx, opts.DeviceFlowClient, webURL, clientID, dc.DeviceCode, time.Duration(dc.Interval)*time.Second)
		if err != nil {
			return nil, fmt.Errorf("completing GitHub device sign-in: %w", err)
		}
		submitReq, err = buildSubmit(ghToken)
		if err != nil {
			return nil, err
		}
		resp, respBytes, err = doJSON(http.MethodPost, baseURL+"/api/hostd/join", submitReq)
		if err != nil {
			return nil, fmt.Errorf("submitting authenticated join request to %s: %w", baseURL, err)
		}
		joinResp = JoinSubmitResponse{}
		_ = json.Unmarshal(respBytes, &joinResp)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(joinResp.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(respBytes))
		}
		return nil, fmt.Errorf("join request refused (%s): %s", resp.Status, msg)
	}
	if joinResp.ID == "" || joinResp.ServerNonce == "" {
		return nil, errors.New("server returned an incomplete join handshake response")
	}

	pairingCode := DerivePairingCode(joinResp.ID, joinResp.ServerNonce, pubStr, hostname)
	if joinResp.PairingCode != "" && joinResp.PairingCode != pairingCode {
		return nil, errors.New("pairing code mismatch between host and server")
	}

	fmt.Fprintf(out, "\nHost join request pending approval in Settings → Hosts:\n")
	fmt.Fprintf(out, "  Host:         %s (%s/%s)\n", hostname, osName, arch)
	if joinResp.EnrolledBy != "" {
		fmt.Fprintf(out, "  Enrolled by:  %s\n", joinResp.EnrolledBy)
	}
	fmt.Fprintf(out, "  Key:          %s\n", fp)
	fmt.Fprintf(out, "Waiting for the deployment owner to accept it in Settings → Hosts...\n")

	pollURL := baseURL + "/api/hostd/join/" + url.PathEscape(joinResp.ID) + "/poll"
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		pollBody, err := SignJoinPoll(joinResp.ID, priv, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		pResp, pBytes, err := doJSON(http.MethodPost, pollURL, pollBody)
		if err != nil {
			continue
		}
		if pResp.StatusCode == http.StatusNotFound || pResp.StatusCode == http.StatusGone || pResp.StatusCode == http.StatusForbidden {
			var errObj struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(pBytes, &errObj)
			msg := strings.TrimSpace(errObj.Error)
			if msg == "" {
				msg = "the join request was rejected or expired"
			}
			return nil, fmt.Errorf("host join handshake ended: %s", msg)
		}
		if pResp.StatusCode != http.StatusOK {
			continue
		}
		var pr JoinPollResponse
		if err := json.Unmarshal(pBytes, &pr); err != nil {
			continue
		}
		if pr.Status == "accepted" && pr.Token != "" {
			if opts.TokenPath != "" {
				if err := WriteSecretFile0600(opts.TokenPath, []byte(pr.Token+"\n")); err != nil {
					return nil, fmt.Errorf("writing host credential to %s: %w", opts.TokenPath, err)
				}
			}
			fmt.Fprintf(out, "Accepted as host %q.\n", pr.Name)
			return &EnrollResult{
				Name:           pr.Name,
				Token:          pr.Token,
				ExpiresAt:      pr.ExpiresAt,
				PairingCode:    pairingCode,
				KeyFingerprint: fp,
				PrivateKey:     priv,
			}, nil
		}
	}
}
