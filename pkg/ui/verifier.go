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
	"github.com/ai-on-gke/grain/pkg/webauth"
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

// VerifierUnavailableError may be implemented by an error an
// IdentityVerifier's Verify returns (anywhere in its wrapped chain) when
// it could not check the header at all rather than found it bad: a
// backend it consults that is not reachable yet, say just after a
// restart. Unavailable reporting true makes the refusal a 503 with
// Retry-After and a "starting up" page rather than the 401 a bad
// signature gets. It is still a refusal: the request is not admitted
// and neither the access code nor another verifier is consulted.
type VerifierUnavailableError interface {
	error
	Unavailable() bool
}

// verifierUnavailable is whether err says its verifier could not check
// the header yet (VerifierUnavailableError).
func verifierUnavailable(err error) bool {
	var u interface{ Unavailable() bool }
	return errors.As(err, &u) && u.Unavailable()
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
type IAPVerifierOption = webauth.IAPVerifierOption

// WithIAPValidator overrides JWT validation on an IAP IdentityVerifier.
func WithIAPValidator(v IAPValidator) IAPVerifierOption {
	return webauth.WithIAPValidator(v)
}

// WithIAPAudience pins an expected audience (or comma-separated audiences) on
// an IAP IdentityVerifier, taking precedence over request/package defaults.
func WithIAPAudience(audience string) IAPVerifierOption {
	return webauth.WithIAPAudience(audience)
}

// WithIAPIssuer pins the expected JWT issuer on an IAP IdentityVerifier.
func WithIAPIssuer(issuer string) IAPVerifierOption {
	return webauth.WithIAPIssuer(issuer)
}

// WithIAPKeysURL pins the public-key endpoint URL on an IAP IdentityVerifier.
func WithIAPKeysURL(keysURL string) IAPVerifierOption {
	return webauth.WithIAPKeysURL(keysURL)
}

type iapVerifier struct {
	inner *webauth.IAPVerifier
}

var defaultIAPVerifier IdentityVerifier = &iapVerifier{inner: webauth.NewIAPVerifier()}

// NewIAPVerifier constructs an IdentityVerifier for Google Cloud IAP
// (X-Goog-IAP-JWT-Assertion).
func NewIAPVerifier(opts ...IAPVerifierOption) IdentityVerifier {
	return &iapVerifier{inner: webauth.NewIAPVerifier(opts...)}
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
	claims, aud, err := v.inner.VerifyToken(r.Context(), tokenStr)
	if err != nil {
		logRefusedAssertion(tokenStr, aud, err)
		return VerifiedIdentity{}, false, err
	}
	id, err := webauth.IdentityFromIAPClaims(claims)
	if err != nil {
		logRefusedAssertion(tokenStr, aud, err)
		return VerifiedIdentity{}, false, err
	}
	return VerifiedIdentity{
		Email: id.Email,
		Kind:  model.PrincipalKind(id.Kind),
	}, true, nil
}

// normalizeIdentityEmail strips Google's accounts prefix, trims whitespace,
// and lowercases the identity so emails are stored lowercased and in full.
func normalizeIdentityEmail(val string) string {
	return webauth.NormalizeIdentityEmail(val)
}

// principalKindForEmail classifies *.gserviceaccount.com addresses as
// model.PrincipalService and everything else as model.PrincipalHuman.
func principalKindForEmail(email string) model.PrincipalKind {
	return model.PrincipalKind(webauth.PrincipalKindForEmail(email))
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
		logRefusedAssertion(raw, "", errors.New("no IAP audience is configured (GRAIN_IAP_AUDIENCE / -ui-iap-audience), so no assertion can be verified"))
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
