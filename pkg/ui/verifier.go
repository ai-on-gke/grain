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

package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/model"
)

// An IdentityVerifier turns a signed header a trusted proxy adds into a verified identity.
type IdentityVerifier interface {
	Name() string      // "iap", or a wrapper's proxy; becomes the AuthMethod
	Headers() []string // the headers it reads; also treated as proxy headers (relayedByProxy)
	// Verify returns ok=false when none of its headers is present, and an
	// error when one is present but does not verify.
	Verify(r *http.Request) (id VerifiedIdentity, ok bool, err error)
}

// AccessCodePrecedence may be implemented by an IdentityVerifier whose proxy
// attaches a signed header to every proxied request, so a valid access code
// can still be used for break-glass recovery when the allowlist or group
// lookup is broken. Verifiers that do not implement it default to false:
// the verified identity wins over the access code.
type AccessCodePrecedence interface {
	AccessCodeWins() bool
}

// VerifiedIdentity is the authenticated caller an IdentityVerifier extracted
// from a signed proxy header.
type VerifiedIdentity struct {
	Email string              // the principal's ID
	Kind  model.PrincipalKind // human or service
}

type registeredVerifier struct {
	id uint64
	v  IdentityVerifier
}

var (
	verifierRegistryMu sync.RWMutex
	verifierNextID     atomic.Uint64
	verifierRegistry   []registeredVerifier

	refusedVerifiersMu     sync.Mutex
	refusedVerifiersLogged = map[string]bool{}
)

// RegisterIdentityVerifier registers v to be consulted by identifyCaller in
// registration order, and returns a function that removes it.
func RegisterIdentityVerifier(v IdentityVerifier) (unregister func()) {
	if v == nil {
		panic("ui: RegisterIdentityVerifier called with nil IdentityVerifier")
	}
	id := verifierNextID.Add(1)
	verifierRegistryMu.Lock()
	verifierRegistry = append(verifierRegistry, registeredVerifier{id: id, v: v})
	verifierRegistryMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			verifierRegistryMu.Lock()
			defer verifierRegistryMu.Unlock()
			for i, entry := range verifierRegistry {
				if entry.id == id {
					verifierRegistry = append(verifierRegistry[:i], verifierRegistry[i+1:]...)
					break
				}
			}
		})
	}
}

// registeredVerifiersSnapshot returns a copy of the explicitly registered verifiers.
func registeredVerifiersSnapshot() []IdentityVerifier {
	verifierRegistryMu.RLock()
	defer verifierRegistryMu.RUnlock()
	if len(verifierRegistry) == 0 {
		return nil
	}
	out := make([]IdentityVerifier, len(verifierRegistry))
	for i, e := range verifierRegistry {
		out[i] = e.v
	}
	return out
}

// activeVerifiers returns the verifiers active for ctx in evaluation order.
// IAP is registered first by default whenever an IAP audience is configured
// (-ui-iap-audience / $GRAIN_IAP_AUDIENCE / Config.IAPAudience), unless an
// explicit "iap" verifier has been registered via RegisterIdentityVerifier.
func activeVerifiers(ctx context.Context) []IdentityVerifier {
	explicit := registeredVerifiersSnapshot()
	for _, v := range explicit {
		if v.Name() == string(AuthMethodIAP) {
			return explicit
		}
	}
	if iapAudienceFor(ctx) == "" {
		return explicit
	}
	out := make([]IdentityVerifier, 0, 1+len(explicit))
	out = append(out, defaultIAPVerifier)
	out = append(out, explicit...)
	return out
}

// IAPVerifierOption configures an IAP IdentityVerifier created by NewIAPVerifier.
type IAPVerifierOption func(*iapVerifier)

// WithIAPValidator overrides JWT validation on an IAP IdentityVerifier.
func WithIAPValidator(v IAPValidator) IAPVerifierOption {
	return func(iv *iapVerifier) {
		iv.validator = v
	}
}

// WithIAPAudience pins an expected audience (or comma-separated audiences) on
// an IAP IdentityVerifier, taking precedence over request/package defaults.
func WithIAPAudience(audience string) IAPVerifierOption {
	return func(iv *iapVerifier) {
		iv.audience = strings.TrimSpace(audience)
	}
}

// WithIAPIssuer pins the expected JWT issuer on an IAP IdentityVerifier.
func WithIAPIssuer(issuer string) IAPVerifierOption {
	return func(iv *iapVerifier) {
		iv.issuer = strings.TrimSpace(issuer)
	}
}

// WithIAPKeysURL pins the public-key endpoint URL on an IAP IdentityVerifier.
func WithIAPKeysURL(keysURL string) IAPVerifierOption {
	return func(iv *iapVerifier) {
		iv.keysURL = strings.TrimSpace(keysURL)
	}
}

type iapVerifier struct {
	validator IAPValidator
	audience  string
	issuer    string
	keysURL   string
}

var defaultIAPVerifier IdentityVerifier = &iapVerifier{}

// NewIAPVerifier constructs an IdentityVerifier for Google Cloud IAP
// (X-Goog-IAP-JWT-Assertion).
func NewIAPVerifier(opts ...IAPVerifierOption) IdentityVerifier {
	iv := &iapVerifier{}
	for _, opt := range opts {
		if opt != nil {
			opt(iv)
		}
	}
	return iv
}

func (v *iapVerifier) Name() string {
	return string(AuthMethodIAP)
}

func (v *iapVerifier) Headers() []string {
	return []string{IAPAssertionHeader}
}

func (v *iapVerifier) Verify(r *http.Request) (VerifiedIdentity, bool, error) {
	if r == nil {
		return VerifiedIdentity{}, false, nil
	}
	tokenStr := strings.TrimSpace(r.Header.Get(IAPAssertionHeader))
	if tokenStr == "" {
		return VerifiedIdentity{}, false, nil
	}
	ctx := r.Context()
	if v.issuer != "" || v.keysURL != "" {
		cfg, _ := IAPConfigFromContext(ctx)
		if v.issuer != "" {
			cfg.Issuer = v.issuer
		}
		if v.keysURL != "" {
			cfg.KeysURL = v.keysURL
		}
		ctx = WithIAPConfig(ctx, cfg)
	}
	aud := v.audience
	if aud == "" {
		aud = iapAudienceFor(ctx)
	}

	var claims *IAPClaims
	var err error
	if v.validator != nil {
		claims, err = v.validator(ctx, tokenStr, aud)
	} else {
		claims, err = defaultIAPValidator(ctx, tokenStr, aud)
	}
	if err != nil {
		logRefusedAssertion(tokenStr, err)
		return VerifiedIdentity{}, false, err
	}
	if claims == nil {
		err = errors.New("nil IAP claims")
		logRefusedAssertion(tokenStr, err)
		return VerifiedIdentity{}, false, err
	}
	email := strings.TrimSpace(claims.Email)
	if email == "" {
		email = strings.TrimSpace(claims.Subject)
	}
	email = normalizeIdentityEmail(email)
	if email == "" {
		err = errors.New("no email or subject found in verified IAP token claims")
		logRefusedAssertion(tokenStr, err)
		return VerifiedIdentity{}, false, err
	}
	return VerifiedIdentity{
		Email: email,
		Kind:  principalKindForEmail(email),
	}, true, nil
}

// normalizeIdentityEmail strips Google's accounts prefix, trims whitespace,
// and lowercases the identity so emails are stored lowercased and in full.
func normalizeIdentityEmail(val string) string {
	return strings.ToLower(cleanIdentity(val))
}

// principalKindForEmail classifies *.gserviceaccount.com addresses as
// model.PrincipalService and everything else as model.PrincipalHuman.
func principalKindForEmail(email string) model.PrincipalKind {
	email = strings.ToLower(strings.TrimSpace(email))
	if local, domain, ok := strings.Cut(email, "@"); ok && local != "" && strings.HasSuffix(domain, ".gserviceaccount.com") && len(domain) > len(".gserviceaccount.com") {
		return model.PrincipalService
	}
	return model.PrincipalHuman
}

func verifierAccessCodeWins(v IdentityVerifier) bool {
	if p, ok := v.(AccessCodePrecedence); ok {
		return p.AccessCodeWins()
	}
	return false
}

type callerVerificationKey struct{}

type callerVerification struct {
	present        bool
	verifier       IdentityVerifier
	id             VerifiedIdentity
	method         AuthMethod
	accessCodeWins bool
	err            error
}

func withCallerVerification(ctx context.Context, cv *callerVerification) context.Context {
	return context.WithValue(ctx, callerVerificationKey{}, cv)
}

func cachedOrVerifyCaller(r *http.Request) callerVerification {
	if r == nil {
		return callerVerification{}
	}
	if cv, ok := r.Context().Value(callerVerificationKey{}).(*callerVerification); ok && cv != nil {
		return *cv
	}
	return verifyCallerNow(r)
}

func firstPresentHeader(r *http.Request, headers []string) string {
	for _, h := range headers {
		if strings.TrimSpace(r.Header.Get(h)) != "" {
			return h
		}
	}
	return ""
}

// verifyCallerNow asks each active IdentityVerifier in turn. The first one
// whose header is present decides the request; its error refuses the request
// and never falls through to another verifier.
func verifyCallerNow(r *http.Request) callerVerification {
	if r == nil {
		return callerVerification{}
	}
	for _, v := range activeVerifiers(r.Context()) {
		headers := v.Headers()
		header := firstPresentHeader(r, headers)
		if len(headers) > 0 && header == "" {
			continue
		}
		id, ok, err := v.Verify(r)
		if len(headers) == 0 && !ok && err == nil {
			continue
		}
		if err != nil {
			if v.Name() != string(AuthMethodIAP) {
				logRefusedVerifierHeader(v.Name(), header, err)
			}
			return callerVerification{
				present:        true,
				verifier:       v,
				method:         AuthMethod(v.Name()),
				accessCodeWins: verifierAccessCodeWins(v),
				err:            err,
			}
		}
		if !ok {
			err = fmt.Errorf("verifier %q did not accept present %s header", v.Name(), header)
			if v.Name() != string(AuthMethodIAP) {
				logRefusedVerifierHeader(v.Name(), header, err)
			}
			return callerVerification{
				present:        true,
				verifier:       v,
				method:         AuthMethod(v.Name()),
				accessCodeWins: verifierAccessCodeWins(v),
				err:            err,
			}
		}
		email := normalizeIdentityEmail(id.Email)
		if email == "" {
			err = fmt.Errorf("verifier %q returned an empty identity email", v.Name())
			logRefusedVerifierHeader(v.Name(), header, err)
			return callerVerification{
				present:        true,
				verifier:       v,
				method:         AuthMethod(v.Name()),
				accessCodeWins: verifierAccessCodeWins(v),
				err:            err,
			}
		}
		kind := id.Kind
		if kind == "" {
			kind = principalKindForEmail(email)
		}
		if kind != model.PrincipalHuman && kind != model.PrincipalService {
			err = fmt.Errorf("verifier %q returned unsupported principal kind %q", v.Name(), kind)
			logRefusedVerifierHeader(v.Name(), header, err)
			return callerVerification{
				present:        true,
				verifier:       v,
				method:         AuthMethod(v.Name()),
				accessCodeWins: verifierAccessCodeWins(v),
				err:            err,
			}
		}
		return callerVerification{
			present:        true,
			verifier:       v,
			id:             VerifiedIdentity{Email: email, Kind: kind},
			method:         AuthMethod(v.Name()),
			accessCodeWins: verifierAccessCodeWins(v),
		}
	}

	// No active verifier claimed the request. If an IAP assertion arrived while
	// IAP is unconfigured, log it once so an operator setting up a proxy can
	// see what audience it sent; otherwise log any unsigned plaintext header.
	if raw := strings.TrimSpace(r.Header.Get(IAPAssertionHeader)); raw != "" {
		logRefusedAssertion(raw, errors.New("no IAP audience is configured (GRAIN_IAP_AUDIENCE / -ui-iap-audience), so no assertion can be verified"))
	} else {
		logIgnoredIdentityHeader(r)
	}
	return callerVerification{}
}

func logRefusedVerifierHeader(verifierName, header string, err error) {
	key := verifierName + "\x00" + header
	refusedVerifiersMu.Lock()
	seen := refusedVerifiersLogged[key]
	if !seen && len(refusedVerifiersLogged) < 64 {
		refusedVerifiersLogged[key] = true
	}
	refusedVerifiersMu.Unlock()
	if seen {
		return
	}
	if header == "" {
		header = verifierName
	}
	log.Printf("ui: verifier %q refused %s header: %v (logged once per verifier header)", verifierName, header, err)
}
