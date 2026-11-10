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
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

type principalContextKey struct{}
type authMethodContextKey struct{}
type iapConfigContextKey struct{}

// IAPAssertionHeader is the HTTP request header holding the Google Cloud IAP JWT assertion.
const IAPAssertionHeader = webauth.IAPAssertionHeader

// RegistryAssertionHeader is the HTTP request header holding the grain-registry JWT assertion.
const RegistryAssertionHeader = webauth.RegistryAssertionHeader

// HubAssertionHeader is RegistryAssertionHeader's name since the Grain
// Registry became the Grain Hub; either is read (webauth.HubAssertion).
const HubAssertionHeader = webauth.HubAssertionHeader

// GoogleIAPCertURL is the Google endpoint serving public keys in PEM format.
const GoogleIAPCertURL = webauth.GoogleIAPCertURL

// GoogleIAPIssuer is the expected issuer claim in Google IAP JWT assertions.
const GoogleIAPIssuer = webauth.GoogleIAPIssuer

// PlaintextIdentityHeaders are the unsigned identity headers a proxy or
// grain's own older clients send. Anything able to reach the daemon can
// write them, so ExtractPrincipal believes none of them; a request
// carrying one is logged (logIgnoredIdentityHeader).
var PlaintextIdentityHeaders = []string{
	"X-Goog-Authenticated-User-Email",
	"X-User-LDAP",
	"X-Forwarded-User",
	"X-Goog-Authenticated-User-Id",
	"X-Grain-User",
	"Grain-User",
}

// HubConfig holds request-level or server-level registry JWT verification
// configuration (-ui-hub-issuer, -ui-hub-audience, -ui-hub-keys-url).
type HubConfig = webauth.HubConfig

// WithHubConfig attaches a HubConfig to ctx.
func WithHubConfig(ctx context.Context, cfg HubConfig) context.Context {
	return webauth.WithHubConfig(ctx, cfg)
}

// HubConfigFromContext extracts HubConfig from ctx if present.
func HubConfigFromContext(ctx context.Context) (HubConfig, bool) {
	return webauth.HubConfigFromContext(ctx)
}

// IAPConfig holds request-level or server-level IAP configuration options.
type IAPConfig struct {
	// Audience is the audience a signed assertion must carry, or several
	// separated by commas. Empty falls back to $GRAIN_IAP_AUDIENCE; with
	// neither, no assertion verifies at all.
	Audience string
	// Issuer is the issuer a signed assertion must carry. Empty means
	// $GRAIN_IAP_ISSUER, and then GoogleIAPIssuer.
	Issuer string
	// KeysURL is where the assertion's signing keys are published, as
	// IAP's PEM map or a JWK set. Empty means $GRAIN_IAP_KEYS_URL, and
	// then GoogleIAPCertURL.
	KeysURL string
	// HubIssuer is Config.HubIssuer (-ui-hub-issuer): the
	// exact iss claim a signed X-Grain-Registry-Assertion must carry.
	HubIssuer string
	// HubAudience is Config.HubAudience (-ui-hub-audience):
	// the single aud URL a signed X-Grain-Registry-Assertion must carry.
	HubAudience string
	// HubKeysURL is Config.HubKeysURL (-ui-hub-keys-url):
	// the JWKS URL publishing the registry's ES256 public keys.
	HubKeysURL string
	// RequireVerifier is Config.RequireVerifier (-ui-require-verifier):
	// when non-empty, names the IdentityVerifier network callers must sign
	// in through, so the access code counts only where accessCodeAccepted
	// says so.
	RequireVerifier string
	// RequireIAP is Config.RequireIAP (-ui-require-iap), an alias for
	// RequireVerifier = "iap".
	RequireIAP bool
	// LocalAPIToken is Config.LocalAPIToken: the secret this host's own
	// CLI presents (presentsLocalAPIToken).
	LocalAPIToken string
	// AccessSessionLifetime is Config.AccessSessionLifetime: how long a
	// single-tenant daemon's access session lasts unused.
	AccessSessionLifetime time.Duration
}

// WithIAPConfig attaches an IAPConfig to the context.
func WithIAPConfig(ctx context.Context, cfg IAPConfig) context.Context {
	ctx = webauth.WithIAPConfig(ctx, webauth.IAPConfig{
		Audience: cfg.Audience,
		Issuer:   cfg.Issuer,
		KeysURL:  cfg.KeysURL,
	})
	if cfg.HubIssuer != "" || cfg.HubAudience != "" || cfg.HubKeysURL != "" {
		rc, _ := webauth.HubConfigFromContext(ctx)
		if cfg.HubIssuer != "" {
			rc.Issuer = cfg.HubIssuer
		}
		if cfg.HubAudience != "" {
			rc.Audience = cfg.HubAudience
		}
		if cfg.HubKeysURL != "" {
			rc.KeysURL = cfg.HubKeysURL
		}
		ctx = webauth.WithHubConfig(ctx, rc)
	}
	return context.WithValue(ctx, iapConfigContextKey{}, cfg)
}

// IAPConfigFromContext extracts IAPConfig from context if present.
func IAPConfigFromContext(ctx context.Context) (IAPConfig, bool) {
	if ctx == nil {
		return IAPConfig{}, false
	}
	cfg, ok := ctx.Value(iapConfigContextKey{}).(IAPConfig)
	return cfg, ok
}

// IAPClaims represents the JWT claims contained in a Google Cloud IAP JWT assertion.
type IAPClaims = webauth.IAPClaims

// IAPValidator validates a Google Cloud IAP JWT assertion token string for a target audience.
type IAPValidator = webauth.IAPValidator

// SetIAPAudience sets the expected audience for Google Cloud IAP JWT verification.
func SetIAPAudience(aud string) {
	webauth.SetIAPAudience(aud)
}

// GetIAPAudience returns the configured audience for Google Cloud IAP JWT verification.
func GetIAPAudience() string {
	return webauth.GetIAPAudience()
}

// SetIAPIssuer sets the expected issuer for Google Cloud IAP JWT verification.
func SetIAPIssuer(issuer string) {
	webauth.SetIAPIssuer(issuer)
}

// GetIAPIssuer returns the issuer a signed assertion must carry.
func GetIAPIssuer() string {
	return webauth.GetIAPIssuer()
}

// SetIAPHTTPClient sets a custom http.Client for fetching IAP public keys.
func SetIAPHTTPClient(client *http.Client) {
	webauth.SetIAPHTTPClient(client)
}

// SetIAPCertURL overrides the URL used to fetch Google IAP public keys
// (an operator's -ui-iap-keys-url, or a test's own server).
func SetIAPCertURL(u string) {
	webauth.SetIAPCertURL(u)
}

// iapAudienceFor is the audience (or comma-separated audiences) a request's
// IAP assertions must carry.
func iapAudienceFor(ctx context.Context) string {
	return webauth.IAPAudienceFor(ctx)
}

// ResetIAPConfig resets all IAP package-level configurations to defaults.
func ResetIAPConfig() {
	webauth.ResetIAPConfig()
	ignoredIdentityHeadersMu.Lock()
	clear(ignoredIdentityHeadersLogged)
	ignoredIdentityHeadersMu.Unlock()
	refusedAssertionsMu.Lock()
	clear(refusedAssertionsLogged)
	refusedAssertionsMu.Unlock()
	refusedVerifiersMu.Lock()
	clear(refusedVerifiersLogged)
	refusedVerifiersMu.Unlock()
	// The once-per-principal refusal log (logRefusedPrincipal,
	// logRefusedDelegate) is package state too: without clearing it, a
	// second run of a test (-count=2) or an earlier test under -shuffle has
	// already spent the "once" and the refusal is never logged.
	refusedPrincipalsMu.Lock()
	clear(refusedPrincipalsLogged)
	refusedPrincipalsMu.Unlock()
}

// VerifyIAPToken validates an IAP JWT assertion and returns the authenticated user email/identity.
func VerifyIAPToken(ctx context.Context, tokenStr string) (string, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return "", errors.New("empty IAP assertion token")
	}

	var claims *IAPClaims
	var err error
	handled := false
	for _, v := range registeredVerifiersSnapshot() {
		if iv, ok := v.(*iapVerifier); ok {
			claims, _, err = iv.inner.VerifyToken(ctx, tokenStr)
			handled = true
			break
		}
	}
	if !handled {
		claims, err = webauth.ValidateIAPToken(ctx, tokenStr, iapAudienceFor(ctx))
	}
	if err != nil {
		return "", err
	}
	id, err := webauth.IdentityFromIAPClaims(claims)
	if err != nil {
		return "", err
	}
	return id.Email, nil
}

// HubClaims represents the JWT claims contained in a grain-registry JWT assertion.
type HubClaims = webauth.HubClaims

// HubValidator validates a grain-registry JWT assertion token string for a target audience.
type HubValidator = webauth.HubValidator

// SetHubAudience sets the expected audience for grain-registry JWT verification.
func SetHubAudience(aud string) {
	webauth.SetHubAudience(aud)
}

// GetHubAudience returns the configured audience for grain-registry JWT verification.
func GetHubAudience() string {
	return webauth.GetHubAudience()
}

// SetHubIssuer sets the expected issuer for grain-registry JWT verification.
func SetHubIssuer(issuer string) {
	webauth.SetHubIssuer(issuer)
}

// GetHubIssuer returns the configured issuer for grain-registry JWT verification.
func GetHubIssuer() string {
	return webauth.GetHubIssuer()
}

// SetHubKeysURL sets the JWKS endpoint URL for grain-registry JWT verification.
func SetHubKeysURL(u string) {
	webauth.SetHubKeysURL(u)
}

// GetHubKeysURL returns the configured JWKS endpoint URL for grain-registry JWT verification.
func GetHubKeysURL() string {
	return webauth.GetHubKeysURL()
}

// SetHubHTTPClient sets a custom http.Client for fetching grain-registry JWKS keys.
func SetHubHTTPClient(client *http.Client) {
	webauth.SetHubHTTPClient(client)
}

// SetHubRefetchFloor overrides the minimum interval between registry JWKS
// refetches triggered by an unknown key ID.
func SetHubRefetchFloor(d time.Duration) {
	webauth.SetHubRefetchFloor(d)
}

// ResetHubConfig resets all registry package-level configurations to defaults.
func ResetHubConfig() {
	webauth.ResetHubConfig()
	refusedVerifiersMu.Lock()
	clear(refusedVerifiersLogged)
	refusedVerifiersMu.Unlock()
}

// VerifyHubToken validates a grain-registry JWT assertion and returns the
// authenticated user email/identity.
func VerifyHubToken(ctx context.Context, tokenStr string) (string, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return "", errors.New("empty registry assertion token")
	}

	var claims *HubClaims
	var err error
	handled := false
	for _, v := range registeredVerifiersSnapshot() {
		if rv, ok := v.(*hubVerifier); ok {
			claims, _, err = rv.inner.VerifyToken(ctx, tokenStr)
			handled = true
			break
		}
	}
	if !handled {
		claims, err = webauth.ValidateHubToken(ctx, tokenStr, webauth.HubAudienceFor(ctx))
	}
	if err != nil {
		return "", err
	}
	id, err := webauth.IdentityFromHubClaims(claims)
	if err != nil {
		return "", err
	}
	return id.Email, nil
}

// testSourceNets are networks singleTenantTrusted trusts as it does an
// in-process request in this package's own tests and nowhere else: export_test.go
// is all that sets it, to the TEST-NET-1 range httptest.NewRequest gives
// every request it builds. Production code used to trust that range
// outright for the tests' sake (grain#2370).
var testSourceNets []*net.IPNet

// proxyIdentityHeaders are headers a signing or identity-aware proxy
// adds to the requests it relays; forwardingHeaders are the ones any
// reverse proxy adds. Grain's own CLI and frontend send neither.
var (
	proxyIdentityHeaders = []string{
		IAPAssertionHeader,
		HubAssertionHeader,
		RegistryAssertionHeader,
		"X-Goog-Authenticated-User-Email",
		"X-Goog-Authenticated-User-Id",
	}
	forwardingHeaders = []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"}
)

// relayedByProxy reports whether r shows signs of having been relayed
// on somebody else's behalf, which makes an in-process-looking request no
// longer the daemon's own (singleTenantTrusted). Every registered
// IdentityVerifier's Headers() also count as proxy headers.
func relayedByProxy(r *http.Request) bool {
	for _, h := range proxyIdentityHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	for _, h := range forwardingHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	for _, v := range registeredVerifiersSnapshot() {
		for _, h := range v.Headers() {
			if r.Header.Get(h) != "" {
				return true
			}
		}
	}
	return false
}

// WithPrincipal returns a copy of parent context with the authenticated principal attached.
func WithPrincipal(ctx context.Context, p model.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext extracts the authenticated principal from ctx, if any.
func PrincipalFromContext(ctx context.Context) (model.Principal, bool) {
	if ctx == nil {
		return model.Principal{}, false
	}
	p, ok := ctx.Value(principalContextKey{}).(model.Principal)
	if !ok || p == (model.Principal{}) {
		return model.Principal{}, false
	}
	return p, true
}

// AuthMethod is how a request's principal was established, which the
// frontend reads to decide what it offers a signed-in person
// (grain/task-2142).
type AuthMethod string

const (
	// AuthMethodNone is a request ExtractPrincipal identified nobody in.
	AuthMethodNone AuthMethod = ""
	// AuthMethodIAP is a verified IAP (or other signing proxy's)
	// assertion.
	AuthMethodIAP AuthMethod = "iap"
	// AuthMethodHub is a verified grain-registry assertion.
	AuthMethodHub AuthMethod = "registry"
	// AuthMethodTrusted is nobody signed in, from a source this daemon
	// trusts as itself: the access code, or an in-process request.
	AuthMethodTrusted AuthMethod = "trusted"
)

// ExtractPrincipal returns who r is authenticated as through a registered
// IdentityVerifier (such as Google Cloud IAP). Plaintext identity headers
// never authenticate anybody. If the deciding verifier declares
// AccessCodeWins() == true and r also carries a valid access code, the
// request is judged by the access code alone and ExtractPrincipal returns
// false.
func ExtractPrincipal(r *http.Request) (model.Principal, bool) {
	p, _, ok := extractPrincipal(r)
	return p, ok
}

// extractPrincipal is ExtractPrincipal, also saying which source
// identified the caller.
func extractPrincipal(r *http.Request) (model.Principal, AuthMethod, bool) {
	if r == nil {
		return model.Principal{}, AuthMethodNone, false
	}
	cv := cachedOrVerifyCaller(r)
	if !cv.present || cv.err != nil {
		return model.Principal{}, AuthMethodNone, false
	}
	if cv.accessCodeWins {
		cfg, _ := IAPConfigFromContext(r.Context())
		if presentsAccessCode(r, cfg) {
			return model.Principal{}, AuthMethodNone, false
		}
		if _, ok := accessSessionIssued(r, cfg); ok {
			return model.Principal{}, AuthMethodNone, false
		}
	}
	return model.Principal{Kind: cv.id.Kind, ID: cv.id.Email, Source: model.AuthSource(cv.method)}, cv.method, true
}

// identifyCaller asks each registered IdentityVerifier in turn (with IAP
// registered first by default when its audience is set). The first verifier
// whose header is present decides the request; its error refuses the request
// and never falls through to another verifier. Plaintext identity headers
// name nobody and are only logged (logIgnoredIdentityHeader).
func identifyCaller(r *http.Request) (model.Principal, AuthMethod, bool) {
	if r == nil {
		return model.Principal{}, AuthMethodNone, false
	}
	cv := cachedOrVerifyCaller(r)
	if !cv.present || cv.err != nil {
		return model.Principal{}, AuthMethodNone, false
	}
	return model.Principal{Kind: cv.id.Kind, ID: cv.id.Email, Source: model.AuthSource(cv.method)}, cv.method, true
}

// withAuthMethod records how the request's principal was established
// (extractPrincipal), for authMethodFromContext to read back.
func withAuthMethod(ctx context.Context, m AuthMethod) context.Context {
	return context.WithValue(ctx, authMethodContextKey{}, m)
}

// authMethodFromContext is the AuthMethod withAuthMethod recorded, and
// AuthMethodNone where nothing did.
func authMethodFromContext(ctx context.Context) AuthMethod {
	if ctx == nil {
		return AuthMethodNone
	}
	m, _ := ctx.Value(authMethodContextKey{}).(AuthMethod)
	return m
}

// unverifiedIdentity describes the identity r claims that ExtractPrincipal
// did not accept -- a refused assertion, or a plaintext header nobody
// opted in to -- for the refusal a caller sees, or "" when it claims
// none. It names what an operator has to configure for that identity to
// count.
//
// With proxyOnly it leaves out the headers grain's own clients send
// (ownClientIdentityHeaders): a signed-out browser names its remembered
// user in X-Grain-User on every request, and that is no proxy for an
// operator to configure.
func unverifiedIdentity(r *http.Request, proxyOnly bool) string {
	for _, v := range activeVerifiers(r.Context()) {
		if h := firstPresentHeader(r, v.Headers()); h != "" {
			if v.Name() == string(AuthMethodIAP) {
				return fmt.Sprintf("its %s did not verify against this deployment's IAP audience, issuer and keys "+
					"(-ui-iap-audience, -ui-iap-issuer, -ui-iap-keys-url)", h)
			}
			if v.Name() == string(AuthMethodHub) {
				return fmt.Sprintf("its %s did not verify against this deployment's registry audience, issuer and keys "+
					"(-ui-hub-audience, -ui-hub-issuer, -ui-hub-keys-url)", h)
			}
			return fmt.Sprintf("its %s header did not verify (%s)", h, v.Name())
		}
	}
	if strings.TrimSpace(r.Header.Get(IAPAssertionHeader)) != "" {
		return fmt.Sprintf("its %s did not verify against this deployment's IAP audience, issuer and keys "+
			"(-ui-iap-audience, -ui-iap-issuer, -ui-iap-keys-url)", IAPAssertionHeader)
	}
	if webauth.HubAssertion(r.Header) != "" {
		return fmt.Sprintf("its %s did not verify against this deployment's hub audience, issuer and keys "+
			"(-ui-hub-audience, -ui-hub-issuer, -ui-hub-keys-url)", HubAssertionHeader)
	}
	if h, id, why, _ := ignoredIdentityHeader(r, proxyOnly); h != "" {
		return fmt.Sprintf("it names %q only in the unsigned %s header, %s", id, h, why)
	}
	return ""
}

// ignoredIdentityHeader is the first plaintext identity header r carries
// (ExtractPrincipal believes none), the identity it names, and why it
// was not believed, phrased to follow "which ...", and what the operator
// would do about it, for the log -- or four empty strings when r carries
// none.
// It is what both the refusal a caller sees and the daemon's log line
// (logIgnoredIdentityHeader) say; proxyOnly skips
// ownClientIdentityHeaders.
func ignoredIdentityHeader(r *http.Request, proxyOnly bool) (header, id, why, fix string) {
	for _, h := range PlaintextIdentityHeaders {
		if proxyOnly && slices.Contains(ownClientIdentityHeaders, h) {
			continue
		}
		if v := cleanIdentity(r.Header.Get(h)); v != "" {
			header, id = h, v
			break
		}
	}
	if header == "" {
		return "", "", "", ""
	}
	why = "which a single-tenant daemon never takes as anybody: it keeps no accounts"
	fix = "present the access code (`grain access-code` prints it) instead"
	return header, id, why, fix
}

// ownClientIdentityHeaders are the PlaintextIdentityHeaders grain's own
// clients send -- the frontend X-Grain-User (ui/src/api.js), the Go
// client X-Forwarded-User (HTTPClient.User) -- rather than a proxy in
// front of the daemon.
var ownClientIdentityHeaders = []string{"X-Grain-User", "Grain-User", "X-Forwarded-User"}

// ignoredIdentityHeadersLogged is which plaintext identity headers have
// had an ignored request logged already. Anybody can send one, so it is
// logged once per header name for the life of the process, and
// PlaintextIdentityHeaders bounds how many lines that can ever be.
var (
	ignoredIdentityHeadersMu     sync.Mutex
	ignoredIdentityHeadersLogged = map[string]bool{}
)

// logIgnoredIdentityHeader logs, once per header name, a request whose
// plaintext identity header ExtractPrincipal ignored, naming the header,
// the identity and what the operator would set for it to count. It is
// how a deployment whose BeyondCorp tunnel sends only that header finds
// out why everybody is anonymous (the identity-aware proxy's own docs), as
// logRefusedAssertion is for a signed assertion.
func logIgnoredIdentityHeader(r *http.Request) {
	h, id, why, fix := ignoredIdentityHeader(r, true)
	if h == "" {
		return
	}
	ignoredIdentityHeadersMu.Lock()
	seen := ignoredIdentityHeadersLogged[h]
	ignoredIdentityHeadersLogged[h] = true
	ignoredIdentityHeadersMu.Unlock()
	if seen {
		return
	}
	log.Printf("ui: ignored the unsigned %s header naming %q on a request from %q, %s -- %s "+
		"(logged once per header; later ones are ignored silently)",
		h, id, r.RemoteAddr, why, fix)
}

func cleanIdentity(val string) string {
	return webauth.CleanIdentity(val)
}

// withAgentRequester is requestPrincipal for a write an agent makes on
// its own behalf through the daemon's hop rather than the agent API (whose
// gate makes the acting task the actor itself) -- returning ctx with
// that agent as the actor, so the pause row and the task's conversation
// name {agent, <task id>} rather than this deployment's own operator.
// Only an agent may be named: a request naming a person or an automation
// would be a caller claiming to be somebody it is not, where naming the
// task a run serves is only being specific about which agent it is.
// Naming nobody leaves ctx as it was.
func withAgentRequester(ctx context.Context, what, id, kind string) (context.Context, error) {
	by, err := requestPrincipal(what, id, kind)
	if err != nil {
		return ctx, err
	}
	if by == (model.Principal{}) {
		return ctx, nil
	}
	if by.Kind != model.PrincipalAgent {
		return ctx, validationErrorf("%s may only name an agent, got %s %q", what, by.Kind, by.ID)
	}
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
		if !by.Same(p) {
			return ctx, forbiddenErrorf("agent %q may not act as %s %q", p.ID, by.Kind, by.ID)
		}
		return ctx, nil
	}
	by.Source = model.SourceSandbox
	return WithPrincipal(ctx, by), nil
}

// bodyAuthor is who a write whose body may name its author (a memory, a
// release note) is recorded as (operator-attribution spec §2.3, §6):
//
//   - the request's own principal when it has one. A person's body is
//     ignored; an agent's that names anybody but itself is refused, since
//     an agent never claims somebody else.
//   - otherwise only an agent the body names, as withAgentRequester
//     allows -- a body naming a human or a service is refused, since
//     nobody verified it;
//   - and the zero Principal for a body naming nobody, which its caller
//     records as RequestActor.
func bodyAuthor(ctx context.Context, what, id, kind string) (model.Principal, error) {
	named, err := requestPrincipal(what, id, kind)
	if err != nil {
		return model.Principal{}, err
	}
	if p, ok := PrincipalFromContext(ctx); ok {
		if p.Kind == model.PrincipalAgent && named != (model.Principal{}) && !named.Same(p) {
			return model.Principal{}, forbiddenErrorf("agent %q may not record %s as %s %q", p.ID, what, named.Kind, named.ID)
		}
		return p, nil
	}
	if named == (model.Principal{}) {
		return named, nil
	}
	if named.Kind != model.PrincipalAgent {
		return model.Principal{}, validationErrorf("%s may only name an agent, got %s %q: "+
			"a person is recorded as themselves only when a verifier says who they are", what, named.Kind, named.ID)
	}
	named.Source = model.SourceSandbox
	return named, nil
}

// requestPrincipal is the principal a request names, and the zero
// Principal for one that names nobody -- which every caller of this
// reads as "record this deployment's own actor".
//
// It is a claim, not a proof: the API is unauthenticated, so a caller
// that says it is somebody is believed. Worth stating plainly rather
// than assuming otherwise, and it is the footing every other attribution
// in this store already stands on -- a caller that can reach this port
// at all can already do the thing it is attributing.
//
// A kind and an id are given together or not at all: a kind grain does
// not know would land an unreadable word in a column every reader
// switches on, and an id with no kind would be an author nothing could
// say the nature of. Both are refused with a sentence rather than
// quietly defaulted, since a caller that went to the trouble of naming a
// principal has an opinion worth failing over.
//
// what names the thing being attributed ("a memory's author"), so the
// sentence a 400 carries says which field of which request was wrong.
func requestPrincipal(what, id, kind string) (model.Principal, error) {
	id, kind = strings.TrimSpace(id), strings.TrimSpace(kind)
	if id == "" && kind == "" {
		return model.Principal{}, nil
	}
	if id == "" || kind == "" {
		return model.Principal{}, validationErrorf(
			"%s is a kind and an id together, got kind %q and id %q", what, kind, id)
	}
	if !model.PrincipalKind(kind).Valid() {
		return model.Principal{}, validationErrorf(
			"%q is not a kind of principal grain knows (%s, %s, %s or %s)",
			kind, model.PrincipalHuman, model.PrincipalService, model.PrincipalAgent, model.PrincipalAutomation)
	}
	return model.Principal{Kind: model.PrincipalKind(kind), ID: id}, nil
}
