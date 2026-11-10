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

package gcp

import (
	"bytes"
	"context"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/hub"
)

const defaultKMSBaseURL = "https://cloudkms.googleapis.com"

var (
	cryptoKeyRe        = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+$`)
	cryptoKeyVersionRe = regexp.MustCompile(`^(projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+)/cryptoKeyVersions/([^/]+)$`)
)

// KMSSignerOptions configures a Cloud KMS ES256 Signer.
type KMSSignerOptions struct {
	// KeyName is either a CryptoKey resource name
	// ("projects/.../locations/.../keyRings/.../cryptoKeys/...")
	// or a specific CryptoKeyVersion resource name
	// (".../cryptoKeys/.../cryptoKeyVersions/1").
	KeyName        string
	JWTTTL         time.Duration
	JWKSRefreshTTL time.Duration
	BaseURL        string
	HTTPClient     *http.Client
	Now            func() time.Time
}

// KMSSigner signs registry assertions via Google Cloud KMS (`EC_SIGN_P256_SHA256`)
// and publishes all enabled key versions as a JWKS document.
type KMSSigner struct {
	cryptoKeyName string
	pinnedVersion string
	ttl           time.Duration
	jwksTTL       time.Duration
	baseURL       string
	httpClient    *http.Client
	nowFn         func() time.Time

	mu          sync.Mutex
	activeVer   string
	activeKid   string
	cachedJWKS  *hub.JSONWebKeySet
	jwksExpires time.Time
}

// NewKMSSigner creates a KMSSigner for the configured Cloud KMS key resource.
func NewKMSSigner(ctx context.Context, opts KMSSignerOptions) (*KMSSigner, error) {
	keyName := strings.TrimSpace(opts.KeyName)
	if keyName == "" {
		return nil, errors.New("gcp kms: key_name is required")
	}
	var cryptoKey, pinnedVer string
	switch {
	case cryptoKeyVersionRe.MatchString(keyName):
		m := cryptoKeyVersionRe.FindStringSubmatch(keyName)
		cryptoKey = m[1]
		pinnedVer = keyName
	case cryptoKeyRe.MatchString(keyName):
		cryptoKey = keyName
	default:
		return nil, fmt.Errorf("gcp kms: invalid kms_key_name %q (expected projects/.../locations/.../keyRings/.../cryptoKeys/...)", keyName)
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		c, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloudkms")
		if err != nil {
			return nil, fmt.Errorf("gcp kms: creating default OAuth2 client: %w", err)
		}
		if c.Timeout == 0 {
			c.Timeout = 15 * time.Second
		}
		httpClient = c
	}

	ttl := opts.JWTTTL
	if ttl <= 0 {
		ttl = hub.DefaultJWTTTL
	}
	jwksTTL := opts.JWKSRefreshTTL
	if jwksTTL <= 0 {
		jwksTTL = 5 * time.Minute
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultKMSBaseURL
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	s := &KMSSigner{
		cryptoKeyName: cryptoKey,
		pinnedVersion: pinnedVer,
		ttl:           ttl,
		jwksTTL:       jwksTTL,
		baseURL:       baseURL,
		httpClient:    httpClient,
		nowFn:         nowFn,
	}
	if pinnedVer != "" {
		s.activeVer = pinnedVer
		s.activeKid = versionResourceToKid(pinnedVer)
	}
	return s, nil
}

// SignAssertion signs HubClaims using Cloud KMS asymmetricSign (EC_SIGN_P256_SHA256)
// and converts the returned ASN.1 DER signature into 64-byte JWS ES256 format (`R || S`).
func (s *KMSSigner) SignAssertion(ctx context.Context, claims hub.HubClaims) (string, error) {
	verName, kid, err := s.ensureActiveVersion(ctx)
	if err != nil {
		return "", err
	}
	now := s.nowFn()
	claims = hub.CompleteClaims(claims, now, s.ttl)
	signingInput, digest, err := hub.EncodeUnsignedJWT(kid, claims)
	if err != nil {
		return "", err
	}

	reqBody := struct {
		Digest struct {
			SHA256 string `json:"sha256"`
		} `json:"digest"`
	}{}
	reqBody.Digest.SHA256 = base64.StdEncoding.EncodeToString(digest[:])
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/v1/%s:asymmetricSign", s.baseURL, verName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gcp kms asymmetricSign: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("gcp kms asymmetricSign HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var signResp struct {
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&signResp); err != nil {
		return "", fmt.Errorf("gcp kms decode asymmetricSign response: %w", err)
	}
	derSig, err := base64.StdEncoding.DecodeString(signResp.Signature)
	if err != nil {
		return "", fmt.Errorf("gcp kms decode base64 signature: %w", err)
	}
	rawSig, err := DERTORawES256(derSig)
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(rawSig), nil
}

// DERTORawES256 converts an ASN.1 DER-encoded ECDSA P-256 signature into a
// fixed 64-byte JWS ES256 (`R || S`) signature.
func DERTORawES256(der []byte) ([]byte, error) {
	var parsed struct {
		R, S *big.Int
	}
	rest, err := asn1.Unmarshal(der, &parsed)
	if err != nil {
		return nil, fmt.Errorf("gcp kms unmarshal ASN.1 DER signature: %w", err)
	}
	if len(rest) != 0 {
		return nil, errors.New("gcp kms trailing bytes in ASN.1 DER signature")
	}
	if parsed.R == nil || parsed.S == nil || parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 {
		return nil, errors.New("gcp kms invalid R/S in ECDSA signature")
	}
	if parsed.R.BitLen() > 256 || parsed.S.BitLen() > 256 {
		return nil, errors.New("gcp kms ECDSA signature integer exceeds P-256 size")
	}
	out := make([]byte, 64)
	parsed.R.FillBytes(out[:32])
	parsed.S.FillBytes(out[32:])
	return out, nil
}

// JWKS fetches (or returns cached) public keys for all enabled versions of the KMS CryptoKey.
func (s *KMSSigner) JWKS(ctx context.Context) (*hub.JSONWebKeySet, error) {
	now := s.nowFn()
	s.mu.Lock()
	if s.cachedJWKS != nil && now.Before(s.jwksExpires) {
		jwks := s.cachedJWKS
		s.mu.Unlock()
		return jwks, nil
	}
	s.mu.Unlock()

	jwks, activeVer, err := s.refreshKeys(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cachedJWKS = jwks
	s.jwksExpires = now.Add(s.jwksTTL)
	if s.pinnedVersion == "" && activeVer != "" {
		s.activeVer = activeVer
		s.activeKid = versionResourceToKid(activeVer)
	}
	s.mu.Unlock()
	return jwks, nil
}

func (s *KMSSigner) ensureActiveVersion(ctx context.Context) (verName, kid string, err error) {
	s.mu.Lock()
	verName = s.activeVer
	kid = s.activeKid
	s.mu.Unlock()
	if verName != "" && kid != "" {
		return verName, kid, nil
	}
	if _, err := s.JWKS(ctx); err != nil {
		return "", "", err
	}
	s.mu.Lock()
	verName = s.activeVer
	kid = s.activeKid
	s.mu.Unlock()
	if verName == "" {
		return "", "", errors.New("gcp kms: no enabled CryptoKeyVersion found")
	}
	return verName, kid, nil
}

func (s *KMSSigner) refreshKeys(ctx context.Context) (*hub.JSONWebKeySet, string, error) {
	var versions []string
	if s.pinnedVersion != "" {
		versions = []string{s.pinnedVersion}
	} else {
		url := fmt.Sprintf("%s/v1/%s/cryptoKeyVersions?filter=state%%3DENABLED", s.baseURL, s.cryptoKeyName)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("gcp kms list cryptoKeyVersions: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, "", fmt.Errorf("gcp kms list cryptoKeyVersions HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
		}
		var listResp struct {
			CryptoKeyVersions []struct {
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"cryptoKeyVersions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			return nil, "", fmt.Errorf("gcp kms decode cryptoKeyVersions list: %w", err)
		}
		for _, v := range listResp.CryptoKeyVersions {
			if v.State == "" || v.State == "ENABLED" {
				versions = append(versions, v.Name)
			}
		}
	}
	if len(versions) == 0 {
		return nil, "", fmt.Errorf("gcp kms: no enabled key versions under %s", s.cryptoKeyName)
	}

	keys := make([]hub.JSONWebKey, 0, len(versions))
	var newestVer string
	for _, ver := range versions {
		jwk, err := s.fetchPublicKeyJWK(ctx, ver)
		if err != nil {
			return nil, "", err
		}
		keys = append(keys, jwk)
		newestVer = ver
	}
	return &hub.JSONWebKeySet{Keys: keys}, newestVer, nil
}

func (s *KMSSigner) fetchPublicKeyJWK(ctx context.Context, versionName string) (hub.JSONWebKey, error) {
	url := fmt.Sprintf("%s/v1/%s/publicKey", s.baseURL, versionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return hub.JSONWebKey{}, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return hub.JSONWebKey{}, fmt.Errorf("gcp kms getPublicKey %s: %w", versionName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return hub.JSONWebKey{}, fmt.Errorf("gcp kms getPublicKey HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var pubResp struct {
		Pem       string `json:"pem"`
		Algorithm string `json:"algorithm"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pubResp); err != nil {
		return hub.JSONWebKey{}, fmt.Errorf("gcp kms decode publicKey: %w", err)
	}
	_, pub, err := hub.ParseECDSAP256PEM([]byte(pubResp.Pem))
	if err != nil {
		return hub.JSONWebKey{}, fmt.Errorf("gcp kms parse publicKey PEM for %s: %w", versionName, err)
	}
	return hub.PublicKeyToJWK(versionResourceToKid(versionName), pub)
}

func versionResourceToKid(versionName string) string {
	m := cryptoKeyVersionRe.FindStringSubmatch(versionName)
	if len(m) == 3 {
		parts := strings.Split(m[1], "/")
		keyShort := parts[len(parts)-1]
		return keyShort + "-v" + m[2]
	}
	return versionName
}

func init() {
	hub.RegisterSignerProvider("kms", func(ctx context.Context, cfg hub.SigningConfig) (hub.Signer, error) {
		kmsSigner, err := NewKMSSigner(ctx, KMSSignerOptions{
			KeyName: cfg.KMSKeyName,
			JWTTTL:  cfg.JWTTTL,
		})
		if err != nil {
			return nil, err
		}
		cacheTTL := hub.CacheTTLForJWTTTL(cfg.JWTTTL)
		if cacheTTL <= 0 {
			return kmsSigner, nil
		}
		return hub.NewCachingSigner(kmsSigner, cacheTTL), nil
	})
}
