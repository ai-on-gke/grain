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

package webauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// Caller represents a verified front-door identity (from IAP or another FrontIdentity provider).
type Caller struct {
	Email        string        `json:"email"`
	Subject      string        `json:"sub"`
	HostedDomain string        `json:"hd,omitempty"`
	Kind         PrincipalKind `json:"kind,omitempty"`
}

// IdentityVerifier abstracts the verification of the inbound request's
// caller identity (e.g. Cloud IAP JWT verification).
type IdentityVerifier interface {
	Authenticate(r *http.Request) (*Caller, error)
}

// IAPAuthenticator verifies `X-Goog-IAP-JWT-Assertion` headers using IAPConfig.
type IAPAuthenticator struct {
	cfg IAPConfig
}

// NewIAPAuthenticator constructs an IAPAuthenticator for the given IAPConfig.
func NewIAPAuthenticator(cfg IAPConfig) (*IAPAuthenticator, error) {
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, errors.New("webauth: IAPConfig.Audience is required")
	}
	return &IAPAuthenticator{cfg: cfg}, nil
}

// Authenticate verifies the request's `X-Goog-IAP-JWT-Assertion` JWT.
func (a *IAPAuthenticator) Authenticate(r *http.Request) (*Caller, error) {
	tok := strings.TrimSpace(r.Header.Get(IAPAssertionHeader))
	if tok == "" {
		return nil, errors.New("missing X-Goog-IAP-JWT-Assertion header")
	}
	ctx := WithIAPConfig(r.Context(), a.cfg)
	claims, err := ValidateIAPToken(ctx, tok, a.cfg.Audience)
	if err != nil {
		return nil, err
	}
	vi, err := IdentityFromIAPClaims(claims)
	if err != nil {
		return nil, err
	}
	return &Caller{
		Email:        vi.Email,
		Subject:      claims.Subject,
		HostedDomain: claims.HD,
		Kind:         vi.Kind,
	}, nil
}

// StaticGroupChecker is an in-memory GroupMemberChecker for tests and static deployments.
type StaticGroupChecker struct {
	members map[string]map[string]bool
}

// NewStaticGroups constructs a StaticGroupChecker from a map of group identifiers
// ("group:team@example.com" or "team@example.com") to member emails.
func NewStaticGroups(groups map[string][]string) *StaticGroupChecker {
	m := make(map[string]map[string]bool, len(groups))
	for g, list := range groups {
		gKey := normalizeGroupEmail(g)
		set := make(map[string]bool, len(list))
		for _, u := range list {
			u = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(u), "user:")))
			if u != "" {
				set[u] = true
			}
		}
		m[gKey] = set
	}
	return &StaticGroupChecker{members: m}
}

// IsMember reports whether memberEmail is in groupEmail.
func (s *StaticGroupChecker) IsMember(_ context.Context, groupEmail, memberEmail string) (bool, error) {
	if s == nil {
		return false, nil
	}
	gKey := normalizeGroupEmail(groupEmail)
	uKey := strings.ToLower(strings.TrimSpace(memberEmail))
	return s.members[gKey][uKey], nil
}

func normalizeGroupEmail(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return strings.TrimSpace(strings.TrimPrefix(raw, "group:"))
}

// DisabledChecker reports whether a human owner email has been disabled by an admin.
type DisabledChecker func(ctx context.Context, email string) (bool, error)

// ServicePrincipalChecker reports whether a service principal email is authorized
// to call read-only catalog endpoints (e.g. via DelegationPolicy.IsServicePrincipalReader).
type ServicePrincipalChecker func(ctx context.Context, email string) (bool, error)

// AdmittedCaller holds the verified identity and authorization flags for an
// admitted HTTP request.
type AdmittedCaller struct {
	Email        string        `json:"email"`
	Subject      string        `json:"sub"`
	HostedDomain string        `json:"hd,omitempty"`
	Kind         PrincipalKind `json:"kind"`
	IsAdmin      bool          `json:"is_admin"`
	CanCreate    bool          `json:"can_create"`
}

type admittedCallerContextKey struct{}

// AdmittedFromContext returns the AdmittedCaller attached to ctx, or nil.
func AdmittedFromContext(ctx context.Context) *AdmittedCaller {
	c, _ := ctx.Value(admittedCallerContextKey{}).(*AdmittedCaller)
	return c
}

// WithAdmitted attaches an AdmittedCaller to ctx.
func WithAdmitted(ctx context.Context, c *AdmittedCaller) context.Context {
	return context.WithValue(ctx, admittedCallerContextKey{}, c)
}

// AdmissionConfig configures IAP + service-principal admission for the registry.
type AdmissionConfig struct {
	Verifier                IdentityVerifier
	Groups                  GroupMemberChecker
	AllowedDomains          []string
	Admins                  []string
	Creators                []string
	ServicePrincipals       []string
	ServicePrincipalChecker ServicePrincipalChecker
	IsDisabled              DisabledChecker
}

// Admitter enforces caller authentication, domain admission, service-principal
// catalog-read admission, disabled-user checks, and admin/creator role resolution.
type Admitter struct {
	verifier                IdentityVerifier
	groups                  GroupMemberChecker
	allowedDomains          map[string]bool
	adminUsers              map[string]bool
	adminGroups             []string
	creatorUsers            map[string]bool
	creatorGroups           []string
	hasCreatorsCfg          bool
	serviceAccounts         map[string]bool
	servicePrincipalChecker ServicePrincipalChecker
	isDisabled              DisabledChecker
}

// NewAdmitter validates the AdmissionConfig and constructs an Admitter.
func NewAdmitter(cfg AdmissionConfig) (*Admitter, error) {
	if cfg.Verifier == nil {
		return nil, errors.New("webauth: IdentityVerifier is required")
	}
	a := &Admitter{
		verifier:                cfg.Verifier,
		groups:                  cfg.Groups,
		allowedDomains:          make(map[string]bool, len(cfg.AllowedDomains)),
		adminUsers:              make(map[string]bool),
		creatorUsers:            make(map[string]bool),
		hasCreatorsCfg:          len(cfg.Creators) > 0,
		serviceAccounts:         make(map[string]bool, len(cfg.ServicePrincipals)),
		servicePrincipalChecker: cfg.ServicePrincipalChecker,
		isDisabled:              cfg.IsDisabled,
	}
	for _, d := range cfg.AllowedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			a.allowedDomains[d] = true
		}
	}
	for _, entry := range cfg.Admins {
		kind, email, err := parsePrincipalSpec(entry)
		if err != nil {
			return nil, fmt.Errorf("webauth: invalid admins entry %q: %w", entry, err)
		}
		switch kind {
		case "user":
			a.adminUsers[email] = true
		case "group":
			a.adminGroups = append(a.adminGroups, email)
		default:
			return nil, fmt.Errorf("webauth: unsupported admins principal kind %q in %q", kind, entry)
		}
	}
	for _, entry := range cfg.Creators {
		kind, email, err := parsePrincipalSpec(entry)
		if err != nil {
			return nil, fmt.Errorf("webauth: invalid creators entry %q: %w", entry, err)
		}
		switch kind {
		case "user":
			a.creatorUsers[email] = true
		case "group":
			a.creatorGroups = append(a.creatorGroups, email)
		default:
			return nil, fmt.Errorf("webauth: unsupported creators principal kind %q in %q", kind, entry)
		}
	}
	for _, entry := range cfg.ServicePrincipals {
		kind, email, err := parsePrincipalSpec(entry)
		if err != nil {
			return nil, fmt.Errorf("webauth: invalid service_principals entry %q: %w", entry, err)
		}
		switch kind {
		case "serviceAccount", "user":
			a.serviceAccounts[email] = true
		default:
			return nil, fmt.Errorf("webauth: unsupported service_principals kind %q in %q", kind, entry)
		}
	}
	return a, nil
}

func parsePrincipalSpec(raw string) (kind, email string, err error) {
	raw = strings.TrimSpace(raw)
	idx := strings.IndexByte(raw, ':')
	if idx <= 0 {
		return "", "", errors.New("expected <kind>:<email>")
	}
	kind = raw[:idx]
	email = strings.ToLower(strings.TrimSpace(raw[idx+1:]))
	if email == "" || !strings.Contains(email, "@") {
		return "", "", errors.New("expected valid email after prefix")
	}
	return kind, email, nil
}

// ErrGroupLookupUnavailable indicates that the backing group membership source
// failed and the request must fail closed with 503 Service Unavailable.
var ErrGroupLookupUnavailable = errors.New("webauth: group membership lookup unavailable")

// ErrCallerUnauthenticated indicates that the request has no valid front-door identity.
var ErrCallerUnauthenticated = errors.New("webauth: caller unauthenticated")

// ErrCallerForbidden indicates that the authenticated caller is not admitted.
var ErrCallerForbidden = errors.New("webauth: caller forbidden")

// AuthenticateIdentity verifies the caller's front-door identity without requiring
// human domain admission or static catalog-reader membership (used when evaluating
// per-entry `connected_principal` or `DelegationPolicy.AuthorizeOnBehalfOf`).
func (a *Admitter) AuthenticateIdentity(r *http.Request) (*Caller, error) {
	caller, err := a.verifier.Authenticate(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCallerUnauthenticated, err)
	}
	email := NormalizeIdentityEmail(caller.Email)
	if email == "" {
		return nil, fmt.Errorf("%w: empty caller email", ErrCallerUnauthenticated)
	}
	kind := caller.Kind
	if kind == "" {
		kind = PrincipalKindForEmail(email)
	}
	return &Caller{
		Email:        email,
		Subject:      caller.Subject,
		HostedDomain: caller.HostedDomain,
		Kind:         kind,
	}, nil
}

// Admit verifies the request caller and computes their AdmittedCaller record.
func (a *Admitter) Admit(r *http.Request) (*AdmittedCaller, error) {
	caller, err := a.verifier.Authenticate(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCallerUnauthenticated, err)
	}
	email := NormalizeIdentityEmail(caller.Email)
	if email == "" {
		return nil, fmt.Errorf("%w: empty caller email", ErrCallerUnauthenticated)
	}

	ctx := r.Context()
	kind := caller.Kind
	if kind == "" {
		kind = PrincipalKindForEmail(email)
	}

	// Check whether this caller is an authorized service principal reader.
	isServiceReader := a.serviceAccounts[email]
	if !isServiceReader && a.servicePrincipalChecker != nil {
		ok, err := a.servicePrincipalChecker(ctx, email)
		if err != nil {
			return nil, fmt.Errorf("%w: checking service principal admission: %v", ErrGroupLookupUnavailable, err)
		}
		isServiceReader = ok
	}
	if isServiceReader {
		return &AdmittedCaller{
			Email:        email,
			Subject:      caller.Subject,
			HostedDomain: caller.HostedDomain,
			Kind:         PrincipalService,
			IsAdmin:      false,
			CanCreate:    false,
		}, nil
	}

	// Machine / service identities must never fall through to human domain admission.
	if kind == PrincipalService {
		return nil, fmt.Errorf("%w: service principal %q is not authorized", ErrCallerForbidden, email)
	}

	// Human caller: verify email domain against allowed_domains.
	domain := emailDomain(email)
	if domain == "" || !a.allowedDomains[domain] {
		return nil, fmt.Errorf("%w: domain %q is not in allowed_domains", ErrCallerForbidden, domain)
	}

	// Check if human account is disabled in the store.
	if a.isDisabled != nil {
		disabled, err := a.isDisabled(ctx, email)
		if err != nil {
			return nil, fmt.Errorf("%w: checking disabled status: %v", ErrGroupLookupUnavailable, err)
		}
		if disabled {
			return nil, fmt.Errorf("%w: account %q is disabled", ErrCallerForbidden, email)
		}
	}

	isAdmin, err := a.checkRole(ctx, email, a.adminUsers, a.adminGroups)
	if err != nil {
		return nil, err
	}

	canCreate := true
	if a.hasCreatorsCfg {
		if isAdmin {
			canCreate = true
		} else {
			canCreate, err = a.checkRole(ctx, email, a.creatorUsers, a.creatorGroups)
			if err != nil {
				return nil, err
			}
		}
	}

	return &AdmittedCaller{
		Email:        email,
		Subject:      caller.Subject,
		HostedDomain: caller.HostedDomain,
		Kind:         PrincipalHuman,
		IsAdmin:      isAdmin,
		CanCreate:    canCreate,
	}, nil
}

func (a *Admitter) checkRole(ctx context.Context, email string, users map[string]bool, groups []string) (bool, error) {
	if users[email] {
		return true, nil
	}
	if len(groups) == 0 {
		return false, nil
	}
	if a.groups == nil {
		return false, fmt.Errorf("%w: group resolver not configured", ErrGroupLookupUnavailable)
	}
	for _, gEmail := range groups {
		ok, err := a.groups.IsMember(ctx, gEmail, email)
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrGroupLookupUnavailable, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func emailDomain(email string) string {
	idx := strings.LastIndexByte(email, '@')
	if idx < 0 || idx+1 >= len(email) {
		return ""
	}
	return strings.ToLower(email[idx+1:])
}

// RequireAdmitted wraps an http.Handler and requires any admitted caller
// (either human or service principal).
func (a *Admitter) RequireAdmitted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, ok := a.admitOrWriteError(w, r)
		if !ok {
			return
		}
		next.ServeHTTP(w, r.WithContext(WithAdmitted(r.Context(), caller)))
	})
}

// RequireHuman wraps an http.Handler and requires an admitted human caller
// (rejecting service principals with 403 Forbidden).
func (a *Admitter) RequireHuman(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, ok := a.admitOrWriteError(w, r)
		if !ok {
			return
		}
		if caller.Kind != PrincipalHuman {
			writeAdmissionError(w, http.StatusForbidden, "forbidden", "service principals may only call read-only catalog endpoints")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithAdmitted(r.Context(), caller)))
	})
}

// RequireAdmin wraps an http.Handler and requires an admitted human admin caller.
func (a *Admitter) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, ok := a.admitOrWriteError(w, r)
		if !ok {
			return
		}
		if caller.Kind != PrincipalHuman || !caller.IsAdmin {
			writeAdmissionError(w, http.StatusForbidden, "forbidden", "admin privileges required")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithAdmitted(r.Context(), caller)))
	})
}

func (a *Admitter) admitOrWriteError(w http.ResponseWriter, r *http.Request) (*AdmittedCaller, bool) {
	if existing := AdmittedFromContext(r.Context()); existing != nil {
		return existing, true
	}
	caller, err := a.Admit(r)
	if err == nil {
		return caller, true
	}
	switch {
	case errors.Is(err, ErrGroupLookupUnavailable):
		w.Header().Set("Retry-After", "5")
		writeAdmissionError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
	case errors.Is(err, ErrCallerForbidden):
		writeAdmissionError(w, http.StatusForbidden, "forbidden", err.Error())
	default:
		writeAdmissionError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
	}
	return nil, false
}

func writeAdmissionError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apierror.Response{
		Error: msg,
		Code:  code,
	})
}
