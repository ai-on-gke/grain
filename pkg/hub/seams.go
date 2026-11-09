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

package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// Shape describes an instance hardware shape configured for hosted VMs.
type Shape struct {
	Name         string `yaml:"name" json:"name"`
	MachineType  string `yaml:"machine_type" json:"machine_type"`
	BootDiskGB   int    `yaml:"boot_disk_gb" json:"boot_disk_gb"`
	BootDiskType string `yaml:"boot_disk_type,omitempty" json:"boot_disk_type,omitempty"`
	BootDiskIOPS int    `yaml:"boot_disk_iops,omitempty" json:"boot_disk_iops,omitempty"`
	QuotaUnits   int    `yaml:"quota_units" json:"quota_units"`
	Description  string `yaml:"description,omitempty" json:"description,omitempty"`
}

// InstanceSpec holds parameters for provisioning a hosted VM instance.
type InstanceSpec struct {
	EntryID        string
	Name           string
	Zone           string
	Shape          Shape
	BootDiskGB     int
	ServiceAccount string
	BootImage      string
	StartupEnv     map[string]string
	Labels         map[string]string
	Generation     int64
}

// InstanceHandle describes the created or inspected VM instance.
type InstanceHandle struct {
	InstanceID string
	Zone       string
	InternalIP string
	BackendURL string
}

// InstanceStatus describes the live status of a hosted VM instance.
type InstanceStatus struct {
	InstanceID string
	Name       string
	Zone       string
	State      string
	InternalIP string
	BackendURL string
	Labels     map[string]string
}

// ErrServiceAccountNotReady is wrapped by a Compute provider's CreateInstance error when the
// instance's own service account is not yet usable by the compute service: not yet visible
// (GCE's EXTERNAL_RESOURCE_NOT_FOUND on a just-created account), or not yet attachable because
// the registry's actAs grant on it has not applied (GCE's SERVICE_ACCOUNT_ACCESS_DENIED).
// Newly created service accounts and IAM bindings are eventually consistent -- GCP documents up
// to about 60s before other services see them -- so the reconciler retries the creation with
// backoff instead of failing the entry outright.
var ErrServiceAccountNotReady = errors.New("instance service account is not yet visible to the compute provider")

// Compute provisions and manages hosted VM instances.
type Compute interface {
	CreateInstance(ctx context.Context, spec InstanceSpec) (*InstanceHandle, error)
	StopInstance(ctx context.Context, zone, instanceID string) error
	DeleteInstance(ctx context.Context, zone, instanceID string) error
	GetInstanceStatus(ctx context.Context, zone, instanceID string) (*InstanceStatus, error)
	ListInstances(ctx context.Context) ([]InstanceStatus, error)
}

// InstanceResourceSpec holds parameters for provisioning per-instance cloud resources.
type InstanceResourceSpec struct {
	EntryID     string
	Name        string
	OwnerEmail  string
	SQLInstance string
	ShipLogs    bool
	AllowSSH    bool
}

// InstanceResourceBundle describes the provisioned per-instance identity, database, and secret scope.
type InstanceResourceBundle struct {
	ServiceAccountEmail string
	SQLInstance         string
	SQLConnectionName   string
	SQLPrivateIP        string
	DatabaseName        string
	DatabaseUser        string
	// DatabasePassword is a generated password for DatabaseUser when the
	// provider could not store it itself. It never reaches the VM: GCE
	// instance metadata is readable by anyone with compute.instances.get,
	// so the reconciler moves it into Secret Manager (SecretWriter, suffix
	// DatabasePasswordSecretSuffix) and clears it before the VM's
	// grain-config is built. Providers should prefer setting
	// DatabasePasswordSecret directly.
	DatabasePassword string
	// DatabasePasswordSecret is the Secret Manager resource name
	// (`projects/<p>/secrets/<prefix>db-password`) whose latest version
	// holds DatabaseUser's password. The VM reads it at boot with its own
	// service account; it is the only form of the password grain-config
	// carries.
	DatabasePasswordSecret string
	SecretPrefix           string
	GCSBucket              string
	GCSManagedFolder       string
}

// InstanceResources provisions per-instance IAM identities, databases, and secret prefixes.
type InstanceResources interface {
	Provision(ctx context.Context, spec InstanceResourceSpec) (*InstanceResourceBundle, error)
	Teardown(ctx context.Context, spec InstanceResourceSpec, bundle InstanceResourceBundle, retainDB bool) error
	GrantSSH(ctx context.Context, zone, instanceID, userEmail string) error
	RevokeSSH(ctx context.Context, zone, instanceID, userEmail string) error
}

// SecretWriter is an optional interface implemented by InstanceResources providers
// that can store per-instance secrets in Secret Manager under `<secret_prefix><entryName>-<suffix>`.
type SecretWriter interface {
	PutSecret(ctx context.Context, entryName, secretSuffix string, payload []byte) (string, error)
}

// DatabasePasswordSecretSuffix is the SecretWriter suffix a generated
// per-instance database password is stored under
// (`<secret_prefix><entryName>-db-password`).
const DatabasePasswordSecretSuffix = "db-password"

// ConfigRepoBundle describes a provisioned backing Git repository for an instance's state.
type ConfigRepoBundle struct {
	RepoURL   string
	DeployKey string
}

// ConfigRepos creates and archives per-instance config repositories.
type ConfigRepos interface {
	EnsureRepo(ctx context.Context, entryName, ownerEmail string) (*ConfigRepoBundle, error)
	ArchiveRepo(ctx context.Context, entryName string) error
}

// FrontIdentity verifies the identity of an inbound request at the registry's front door.
type FrontIdentity interface {
	Authenticate(r *http.Request) (*webauth.Caller, error)
}

// GroupResolver resolves group membership for authorization checks and
// canonicalizes group identifiers before config validation.
//
// Core follow-up C-1: NormalizeGroup canonicalizes raw group identifiers
// (such as wrapper shorthand "group:team") into canonical "group:<local>@<domain>"
// form before validation.
type GroupResolver interface {
	IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error)
	NormalizeGroup(raw string) (string, error)
}

// GroupNormalizer is an optional interface for group resolvers that normalize
// shorthand group identifiers into canonical "group:<local>@<domain>" form.
type GroupNormalizer interface {
	NormalizeGroup(raw string) (string, error)
}

// NormalizeGroup canonicalizes a group identifier using resolver if it implements
// NormalizeGroup, or applies the default canonical "group:<local>@<domain>" validation.
func NormalizeGroup(resolver any, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if resolver != nil {
		if norm, ok := resolver.(GroupNormalizer); ok {
			return norm.NormalizeGroup(raw)
		}
	}
	return defaultNormalizeGroup(raw)
}

func defaultNormalizeGroup(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "group:") {
		return "", fmt.Errorf("group identifier %q must start with \"group:\"", raw)
	}
	addr := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "group:")))
	at := strings.IndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 || strings.Contains(addr[at+1:], "@") || !strings.Contains(addr[at+1:], ".") {
		return "", fmt.Errorf("group identifier %q must have canonical form \"group:<local>@<domain>\"", raw)
	}
	return "group:" + addr, nil
}

// DelegationActor identifies the acting service principal when an on-behalf-of
// delegation assertion is issued.
type DelegationActor struct {
	Sub    string `json:"sub"`
	Kind   string `json:"kind,omitempty"`
	App    string `json:"app,omitempty"`
	Grant  string `json:"grant,omitempty"`
	Access string `json:"access,omitempty"`
}

// DelegationPolicy governs machine service-principal admission and on-behalf-of
// delegation requests.
type DelegationPolicy interface {
	// IsServicePrincipalReader reports whether the given service principal email
	// is admitted for read-only catalog endpoints.
	IsServicePrincipalReader(ctx context.Context, email string) (bool, error)

	// AuthorizeOnBehalfOf checks whether servicePrincipal is permitted to act on
	// behalf of targetUserEmail for targetEntryName. In v1, the default policy
	// refuses all on-behalf-of requests.
	AuthorizeOnBehalfOf(ctx context.Context, servicePrincipal, targetUserEmail, targetEntryName string) (*DelegationActor, error)
}

// ErrDelegationDenied is returned when an on-behalf-of request is refused by policy.
var ErrDelegationDenied = errors.New("registry: on-behalf-of delegation denied by policy")

// DelegationRefusedError wraps ErrDelegationDenied with a machine-readable
// refusal reason (such as "app_not_registered", "app_disabled",
// "person_not_admitted", "no_grant", "grant_expired", "entry_out_of_reach",
// "read_only_grant", "not_delegable", or "delegation_disabled").
type DelegationRefusedError struct {
	Reason  string
	Message string
}

func (e *DelegationRefusedError) Error() string {
	if e == nil {
		return ErrDelegationDenied.Error()
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", ErrDelegationDenied.Error(), e.Message)
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s (%s)", ErrDelegationDenied.Error(), e.Reason)
	}
	return ErrDelegationDenied.Error()
}

func (e *DelegationRefusedError) Unwrap() error {
	return ErrDelegationDenied
}

// DelegationRefusalReason returns the machine-readable reason code for HTTP 403 responses.
func (e *DelegationRefusedError) DelegationRefusalReason() string {
	if e == nil {
		return ""
	}
	return e.Reason
}

// StaticDelegationPolicy implements the v1 static service-principal reader list
// and a refuse-all AuthorizeOnBehalfOf stub.
type StaticDelegationPolicy struct {
	readers map[string]bool
}

// NewStaticDelegationPolicy constructs a StaticDelegationPolicy from configured
// service_principals entries (e.g. "serviceAccount:scanner@proj.iam.gserviceaccount.com").
func NewStaticDelegationPolicy(servicePrincipals []string) (*StaticDelegationPolicy, error) {
	readers := make(map[string]bool, len(servicePrincipals))
	for _, raw := range servicePrincipals {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		email := raw
		if idx := strings.IndexByte(raw, ':'); idx > 0 {
			prefix := raw[:idx]
			if prefix != "serviceAccount" && prefix != "user" {
				return nil, fmt.Errorf("unsupported service_principals prefix %q in %q", prefix, raw)
			}
			email = raw[idx+1:]
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || !strings.Contains(email, "@") {
			return nil, fmt.Errorf("invalid service_principals email in %q", raw)
		}
		readers[email] = true
	}
	return &StaticDelegationPolicy{readers: readers}, nil
}

// IsServicePrincipalReader reports whether email is in the static service_principals set.
func (p *StaticDelegationPolicy) IsServicePrincipalReader(_ context.Context, email string) (bool, error) {
	if p == nil {
		return false, nil
	}
	return p.readers[strings.ToLower(strings.TrimSpace(email))], nil
}

// AuthorizeOnBehalfOf unconditionally denies all delegation requests in v1.
func (p *StaticDelegationPolicy) AuthorizeOnBehalfOf(_ context.Context, servicePrincipal, targetUserEmail, targetEntryName string) (*DelegationActor, error) {
	return nil, &DelegationRefusedError{
		Reason:  "delegation_disabled",
		Message: fmt.Sprintf("principal %q cannot act on behalf of %q for %q", servicePrincipal, targetUserEmail, targetEntryName),
	}
}

// ConsentDelegationPolicy implements the "consent" DelegationPolicy backed by
// registered delegating apps (`delegating_apps`) and per-person consent grants
// (`delegation_grants`) in `*store.Store` (or in-memory registrations for unit tests).
type ConsentDelegationPolicy struct {
	static         *StaticDelegationPolicy
	allowedDomains map[string]bool

	mu     sync.RWMutex
	st     *store.Store
	nowFn  func() time.Time
	apps   map[string]store.DelegatingAppRow
	grants map[string]store.DelegationGrantRow
}

// NewConsentDelegationPolicy constructs a ConsentDelegationPolicy from AuthConfig
// and an optional `*store.Store`.
func NewConsentDelegationPolicy(cfg AuthConfig, st *store.Store) (*ConsentDelegationPolicy, error) {
	staticPol, err := NewStaticDelegationPolicy(cfg.ServicePrincipals)
	if err != nil {
		return nil, err
	}
	domains := make(map[string]bool, len(cfg.AllowedDomains))
	for _, d := range cfg.AllowedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			domains[d] = true
		}
	}
	return &ConsentDelegationPolicy{
		static:         staticPol,
		allowedDomains: domains,
		st:             st,
		nowFn:          time.Now,
		apps:           make(map[string]store.DelegatingAppRow),
		grants:         make(map[string]store.DelegationGrantRow),
	}, nil
}

// SetStore attaches a backing `*store.Store` to p.
func (p *ConsentDelegationPolicy) SetStore(st *store.Store) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.st = st
	p.mu.Unlock()
}

// SetNow overrides the clock used for grant expiration checks.
func (p *ConsentDelegationPolicy) SetNow(fn func() time.Time) {
	if p == nil || fn == nil {
		return
	}
	p.mu.Lock()
	p.nowFn = fn
	p.mu.Unlock()
}

// RegisterApp registers or updates an in-memory delegating app on p.
func (p *ConsentDelegationPolicy) RegisterApp(app store.DelegatingAppRow) {
	if p == nil {
		return
	}
	app.Name = strings.ToLower(strings.TrimSpace(app.Name))
	app.Principal = store.NormalizeAppPrincipal(app.Principal)
	if app.MaxAccess == "" {
		app.MaxAccess = "write"
	}
	p.mu.Lock()
	p.apps[app.Name] = app
	p.mu.Unlock()
}

// UpsertGrant registers or updates an in-memory consent grant on p.
func (p *ConsentDelegationPolicy) UpsertGrant(grant store.DelegationGrantRow) {
	if p == nil {
		return
	}
	grant.App = strings.ToLower(strings.TrimSpace(grant.App))
	grant.Person = strings.ToLower(strings.TrimSpace(grant.Person))
	if grant.Access == "" {
		grant.Access = "write"
	}
	p.mu.Lock()
	p.grants[grant.App+"|"+grant.Person] = grant
	p.mu.Unlock()
}

// IsServicePrincipalReader reports whether email is in `service_principals` or is
// an enabled registered delegating app.
func (p *ConsentDelegationPolicy) IsServicePrincipalReader(ctx context.Context, email string) (bool, error) {
	if p == nil {
		return false, nil
	}
	if ok, err := p.static.IsServicePrincipalReader(ctx, email); err != nil || ok {
		return ok, err
	}
	app, err := p.lookupAppByPrincipal(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return app != nil && !app.Disabled(), nil
}

func (p *ConsentDelegationPolicy) lookupAppByPrincipal(ctx context.Context, principal string) (*store.DelegatingAppRow, error) {
	norm := store.NormalizeAppPrincipal(principal)

	p.mu.RLock()
	st := p.st
	for _, a := range p.apps {
		if strings.EqualFold(a.Principal, norm) {
			cp := a
			p.mu.RUnlock()
			return &cp, nil
		}
	}
	p.mu.RUnlock()

	if st != nil {
		return st.GetDelegatingAppByPrincipal(ctx, principal)
	}
	return nil, store.ErrNotFound
}

func (p *ConsentDelegationPolicy) lookupGrant(ctx context.Context, appName, personEmail string) (*store.DelegationGrantRow, error) {
	key := strings.ToLower(strings.TrimSpace(appName)) + "|" + strings.ToLower(strings.TrimSpace(personEmail))
	p.mu.RLock()
	st := p.st
	if g, ok := p.grants[key]; ok && g.RevokedAt == nil {
		cp := g
		p.mu.RUnlock()
		return &cp, nil
	}
	p.mu.RUnlock()

	if st != nil {
		return st.GetLiveDelegationGrant(ctx, appName, personEmail)
	}
	return nil, store.ErrNotFound
}

// AuthorizeOnBehalfOf verifies that servicePrincipal is an enabled delegating app,
// targetUserEmail is an admitted human, and a live consent grant from targetUserEmail
// to that app covers targetEntryName.
func (p *ConsentDelegationPolicy) AuthorizeOnBehalfOf(ctx context.Context, servicePrincipal, targetUserEmail, targetEntryName string) (*DelegationActor, error) {
	if p == nil {
		return nil, &DelegationRefusedError{Reason: "delegation_disabled"}
	}
	spEmail := strings.TrimSpace(servicePrincipal)
	if strings.HasPrefix(strings.ToLower(spEmail), "serviceaccount:") {
		spEmail = spEmail[len("serviceAccount:"):]
	}
	spEmail = strings.ToLower(strings.TrimSpace(spEmail))
	personEmail := strings.ToLower(strings.TrimSpace(targetUserEmail))
	entryName := strings.ToLower(strings.TrimSpace(targetEntryName))

	app, err := p.lookupAppByPrincipal(ctx, spEmail)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &DelegationRefusedError{
				Reason:  "app_not_registered",
				Message: fmt.Sprintf("service principal %q is not a registered delegating app", spEmail),
			}
		}
		return nil, err
	}
	if app.Disabled() {
		return nil, &DelegationRefusedError{
			Reason:  "app_disabled",
			Message: fmt.Sprintf("delegating app %q is disabled", app.Name),
		}
	}

	if personEmail == "" || !strings.Contains(personEmail, "@") || webauth.PrincipalKindForEmail(personEmail) != webauth.PrincipalHuman {
		return nil, &DelegationRefusedError{
			Reason:  "person_not_admitted",
			Message: fmt.Sprintf("principal %q is not an admitted human user", personEmail),
		}
	}
	if len(p.allowedDomains) > 0 {
		at := strings.LastIndexByte(personEmail, '@')
		domain := ""
		if at > 0 && at < len(personEmail)-1 {
			domain = strings.ToLower(personEmail[at+1:])
		}
		if !p.allowedDomains[domain] {
			return nil, &DelegationRefusedError{
				Reason:  "person_not_admitted",
				Message: fmt.Sprintf("principal %q is not in an allowed domain", personEmail),
			}
		}
	}
	p.mu.RLock()
	st := p.st
	nowFn := p.nowFn
	p.mu.RUnlock()
	if st != nil {
		if disabled, err := st.IsOwnerDisabled(ctx, personEmail); err == nil && disabled {
			return nil, &DelegationRefusedError{
				Reason:  "person_not_admitted",
				Message: fmt.Sprintf("principal %q is disabled", personEmail),
			}
		}
	}

	grant, err := p.lookupGrant(ctx, app.Name, personEmail)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &DelegationRefusedError{
				Reason:  "no_grant",
				Message: fmt.Sprintf("no active delegation grant from %q to %q", personEmail, app.Name),
			}
		}
		return nil, err
	}
	now := time.Now().UTC()
	if nowFn != nil {
		now = nowFn().UTC()
	}
	if !grant.ExpiresAt.IsZero() && !now.Before(grant.ExpiresAt) {
		return nil, &DelegationRefusedError{
			Reason:  "grant_expired",
			Message: fmt.Sprintf("delegation grant %q from %q to %q has expired", grant.ID, personEmail, app.Name),
		}
	}
	if entryName != "" && !grant.CoversEntry(entryName) {
		return nil, &DelegationRefusedError{
			Reason:  "entry_out_of_reach",
			Message: fmt.Sprintf("delegation grant %q does not cover entry %q", grant.ID, entryName),
		}
	}

	access := strings.ToLower(strings.TrimSpace(grant.Access))
	if access == "" {
		access = "write"
	}
	if strings.EqualFold(app.MaxAccess, "read") {
		access = "read"
	}
	return &DelegationActor{
		Sub:    spEmail,
		Kind:   "service",
		App:    app.Name,
		Grant:  grant.ID,
		Access: access,
	}, nil
}

// Signer signs short-lived ES256 registry assertions (`X-Grain-Registry-Assertion`)
// and exposes the public verification keys as a RFC 7517 JWKS document.
type Signer interface {
	SignAssertion(ctx context.Context, claims RegistryClaims) (string, error)
	JWKS(ctx context.Context) (*JSONWebKeySet, error)
}

// ConnectSession represents an established reverse-tunnel multiplexing session
// from a `grain connect` client to a registry replica.
type ConnectSession interface {
	RoundTrip(r *http.Request) (*http.Response, error)
	Ping(ctx context.Context) (time.Duration, error)
	Done() <-chan struct{}
	Close() error
}

// ConnectTransport upgrades an inbound HTTP connection from `grain connect` into
// a multiplexed ConnectSession.
type ConnectTransport interface {
	Name() string
	Accept(w http.ResponseWriter, r *http.Request) (ConnectSession, error)
}

// OrphanProvider reports and deletes unmatched GCE VMs discovered during orphan sweeps.
type OrphanProvider interface {
	ListOrphans() []api.OrphanVMInfo
	DeleteOrphan(ctx context.Context, zone, instanceID string) error
}

// ReconcilerHostCache is notified when an entry's placement or lifecycle state changes.
type ReconcilerHostCache interface {
	Put(e *store.EntryRow)
	Invalidate(name string)
}

// ReconcilerBuildOptions holds dependencies for constructing the background Reconciler.
type ReconcilerBuildOptions struct {
	Config      Config
	Store       *store.Store
	Compute     Compute
	Resources   InstanceResources
	ConfigRepos ConfigRepos
	Signer      Signer
	HostCache   ReconcilerHostCache
	HolderID    string
}

// BackgroundReconciler drives hosted VM reconciliation and orphan sweeps in the background.
type BackgroundReconciler interface {
	OrphanProvider
	Start(ctx context.Context)
}

// Factory types for extension hooks.
type (
	FrontIdentityFactory       func(ctx context.Context, cfg AuthConfig) (FrontIdentity, error)
	GroupResolverFactory       func(ctx context.Context, cfg GroupsConfig) (GroupResolver, error)
	DelegationPolicyFactory    func(ctx context.Context, cfg AuthConfig) (DelegationPolicy, error)
	ConnectTransportFactory    func(cfg TunnelConfig) (ConnectTransport, error)
	ComputeProviderFactory     func(ctx context.Context, cfg ComputeConfig) (Compute, error)
	InstanceResourcesFactory   func(ctx context.Context, cfg Config) (InstanceResources, error)
	ConfigReposProviderFactory func(ctx context.Context, cfg ConfigReposConfig) (ConfigRepos, error)
	SignerProviderFactory      func(ctx context.Context, cfg SigningConfig) (Signer, error)
	ReconcilerFactory          func(opts ReconcilerBuildOptions) (BackgroundReconciler, error)
)

var (
	regMu                  sync.RWMutex
	frontIdentityFactories = map[string]FrontIdentityFactory{}
	groupResolverFactories = map[string]GroupResolverFactory{}
	delegationFactories    = map[string]DelegationPolicyFactory{}
	connectTransports      = map[string]ConnectTransportFactory{}
	computeFactories       = map[string]ComputeProviderFactory{}
	instanceResFactories   = map[string]InstanceResourcesFactory{}
	configReposFactories   = map[string]ConfigReposProviderFactory{}
	signerFactories        = map[string]SignerProviderFactory{}
	reconcilerFactory      ReconcilerFactory
)

// RegisterReconcilerFactory registers the background reconciler factory (registered by pkg/hub/reconcile).
func RegisterReconcilerFactory(fn ReconcilerFactory) {
	if fn == nil {
		panic("registry: RegisterReconcilerFactory requires non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	reconcilerFactory = fn
}

// BuildReconciler constructs a BackgroundReconciler if a ReconcilerFactory has been registered.
func BuildReconciler(opts ReconcilerBuildOptions) (BackgroundReconciler, error) {
	regMu.RLock()
	fn := reconcilerFactory
	regMu.RUnlock()
	if fn == nil {
		return nil, nil
	}
	return fn(opts)
}

// RegisterFrontIdentity registers a named FrontIdentity provider factory (e.g. "iap").
// Wrapper binaries call this from init() or before registry.Main().
func RegisterFrontIdentity(name string, fn FrontIdentityFactory) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		panic("registry: RegisterFrontIdentity requires non-empty name and non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	frontIdentityFactories[name] = fn
}

// RegisterGroupResolver registers a named GroupResolver provider factory (e.g. "cloud-identity", "static").
func RegisterGroupResolver(name string, fn GroupResolverFactory) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		panic("registry: RegisterGroupResolver requires non-empty name and non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	groupResolverFactories[name] = fn
}

// RegisterDelegationPolicy registers a named DelegationPolicy factory (e.g. "static").
func RegisterDelegationPolicy(name string, fn DelegationPolicyFactory) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		panic("registry: RegisterDelegationPolicy requires non-empty name and non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	delegationFactories[name] = fn
}

// RegisterConnectTransport registers a named ConnectTransport factory (default "ws-mux").
func RegisterConnectTransport(name string, fn ConnectTransportFactory) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		panic("registry: RegisterConnectTransport requires non-empty name and non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	connectTransports[name] = fn
}

// RegisterComputeProvider registers a named Compute provider factory (e.g. "gce", "none").
func RegisterComputeProvider(name string, fn ComputeProviderFactory) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		panic("registry: RegisterComputeProvider requires non-empty name and non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	computeFactories[name] = fn
}

// RegisterInstanceResourcesProvider registers a named InstanceResources provider factory.
func RegisterInstanceResourcesProvider(name string, fn InstanceResourcesFactory) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		panic("registry: RegisterInstanceResourcesProvider requires non-empty name and non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	instanceResFactories[name] = fn
}

// RegisterConfigReposProvider registers a named ConfigRepos provider factory (e.g. "github", "none").
func RegisterConfigReposProvider(name string, fn ConfigReposProviderFactory) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		panic("registry: RegisterConfigReposProvider requires non-empty name and non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	configReposFactories[name] = fn
}

// RegisterSignerProvider registers a named Signer provider factory (e.g. "kms", "local").
func RegisterSignerProvider(name string, fn SignerProviderFactory) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		panic("registry: RegisterSignerProvider requires non-empty name and non-nil factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	signerFactories[name] = fn
}

func BuildFrontIdentity(ctx context.Context, cfg AuthConfig) (FrontIdentity, error) {
	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		provider = "iap"
	}
	regMu.RLock()
	fn, ok := frontIdentityFactories[provider]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown auth.provider %q (registered: %s)", provider, strings.Join(RegisteredFrontIdentities(), ", "))
	}
	return fn(ctx, cfg)
}

func BuildGroupResolver(ctx context.Context, cfg GroupsConfig) (GroupResolver, error) {
	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		provider = "cloud-identity"
	}
	regMu.RLock()
	fn, ok := groupResolverFactories[provider]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown groups.provider %q (registered: %s)", provider, strings.Join(RegisteredGroupResolvers(), ", "))
	}
	return fn(ctx, cfg)
}

func BuildDelegationPolicy(ctx context.Context, cfg AuthConfig) (DelegationPolicy, error) {
	policyName := strings.TrimSpace(cfg.DelegationPolicy)
	if policyName == "" {
		policyName = "static"
	}
	regMu.RLock()
	fn, ok := delegationFactories[policyName]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown delegation policy %q (registered: %s)", policyName, strings.Join(RegisteredDelegationPolicies(), ", "))
	}
	return fn(ctx, cfg)
}

func BuildConnectTransport(cfg TunnelConfig) (ConnectTransport, error) {
	name := strings.TrimSpace(cfg.Transport)
	if name == "" {
		name = DefaultConnectTransportName
	}
	regMu.RLock()
	fn, ok := connectTransports[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown tunnel.transport %q (registered: %s)", name, strings.Join(RegisteredConnectTransports(), ", "))
	}
	return fn(cfg)
}

func BuildSigner(ctx context.Context, cfg SigningConfig) (Signer, error) {
	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		provider = "kms"
	}
	regMu.RLock()
	fn, ok := signerFactories[provider]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown signing.provider %q (registered: %s)", provider, strings.Join(RegisteredSignerProviders(), ", "))
	}
	return fn(ctx, cfg)
}

func BuildCompute(ctx context.Context, cfg ComputeConfig) (Compute, error) {
	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		provider = "gce"
	}
	regMu.RLock()
	fn, ok := computeFactories[provider]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown compute.provider %q (registered: %s)", provider, strings.Join(RegisteredComputeProviders(), ", "))
	}
	return fn(ctx, cfg)
}

func BuildInstanceResources(ctx context.Context, cfg Config) (InstanceResources, error) {
	provider := strings.TrimSpace(cfg.Compute.Provider)
	if provider == "" {
		provider = "gce"
	}
	regMu.RLock()
	fn, ok := instanceResFactories[provider]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown instance resources provider %q (registered: %s)", provider, strings.Join(RegisteredInstanceResourcesProviders(), ", "))
	}
	return fn(ctx, cfg)
}

func BuildConfigRepos(ctx context.Context, cfg ConfigReposConfig) (ConfigRepos, error) {
	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		provider = "none"
	}
	regMu.RLock()
	fn, ok := configReposFactories[provider]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown config_repos.provider %q (registered: %s)", provider, strings.Join(RegisteredConfigReposProviders(), ", "))
	}
	return fn(ctx, cfg)
}

func RegisteredFrontIdentities() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return sortedKeys(frontIdentityFactories)
}

func RegisteredGroupResolvers() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return sortedKeys(groupResolverFactories)
}

func RegisteredDelegationPolicies() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return sortedKeys(delegationFactories)
}

func RegisteredConnectTransports() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return sortedKeys(connectTransports)
}

func RegisteredSignerProviders() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return sortedKeys(signerFactories)
}

func RegisteredComputeProviders() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return sortedKeys(computeFactories)
}

func RegisteredInstanceResourcesProviders() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return sortedKeys(instanceResFactories)
}

func RegisteredConfigReposProviders() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return sortedKeys(configReposFactories)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// webauthGroupResolverAdapter adapts webauth.GroupMemberChecker to registry.GroupResolver
// by supplying the default NormalizeGroup implementation when the underlying
// resolver does not implement GroupNormalizer.
type webauthGroupResolverAdapter struct {
	inner webauth.GroupMemberChecker
}

func (a webauthGroupResolverAdapter) IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
	return a.inner.IsMember(ctx, groupEmail, memberEmail)
}

func (a webauthGroupResolverAdapter) NormalizeGroup(raw string) (string, error) {
	if norm, ok := a.inner.(GroupNormalizer); ok {
		return norm.NormalizeGroup(raw)
	}
	return defaultNormalizeGroup(raw)
}

type noopComputeProvider struct{}

func (noopComputeProvider) CreateInstance(context.Context, InstanceSpec) (*InstanceHandle, error) {
	return nil, errors.New("registry: compute provider is disabled (compute.provider = \"none\")")
}

func (noopComputeProvider) StopInstance(context.Context, string, string) error {
	return errors.New("registry: compute provider is disabled (compute.provider = \"none\")")
}

func (noopComputeProvider) DeleteInstance(context.Context, string, string) error {
	return errors.New("registry: compute provider is disabled (compute.provider = \"none\")")
}

func (noopComputeProvider) GetInstanceStatus(context.Context, string, string) (*InstanceStatus, error) {
	return nil, errors.New("registry: compute provider is disabled (compute.provider = \"none\")")
}

func (noopComputeProvider) ListInstances(context.Context) ([]InstanceStatus, error) {
	return nil, nil
}

type noopInstanceResources struct{}

func (noopInstanceResources) Provision(context.Context, InstanceResourceSpec) (*InstanceResourceBundle, error) {
	return nil, errors.New("registry: instance resources provider is disabled")
}

func (noopInstanceResources) Teardown(context.Context, InstanceResourceSpec, InstanceResourceBundle, bool) error {
	return nil
}

func (noopInstanceResources) GrantSSH(context.Context, string, string, string) error {
	return errors.New("registry: instance resources provider is disabled")
}

func (noopInstanceResources) RevokeSSH(context.Context, string, string, string) error {
	return errors.New("registry: instance resources provider is disabled")
}

type noopConfigRepos struct{}

func (noopConfigRepos) EnsureRepo(context.Context, string, string) (*ConfigRepoBundle, error) {
	return nil, errors.New("registry: config_repos provider is disabled")
}

func (noopConfigRepos) ArchiveRepo(context.Context, string) error {
	return nil
}

func init() {
	RegisterFrontIdentity("iap", func(_ context.Context, cfg AuthConfig) (FrontIdentity, error) {
		aud := strings.TrimSpace(cfg.IAPAudience)
		if aud == "" {
			return nil, errors.New("auth.iap_audience is required when auth.provider is \"iap\"")
		}
		return webauth.NewIAPAuthenticator(webauth.IAPConfig{
			Audience: aud,
			KeysURL:  strings.TrimSpace(cfg.IAPCertsURL),
		})
	})

	RegisterGroupResolver("cloud-identity", func(_ context.Context, cfg GroupsConfig) (GroupResolver, error) {
		ci := webauth.NewCloudIdentityGroupChecker(webauth.WithCloudIdentityCacheTTL(cfg.CacheTTL))
		return webauthGroupResolverAdapter{inner: webauth.NewDefaultGroupMemberChecker(ci)}, nil
	})

	RegisterGroupResolver("static", func(_ context.Context, cfg GroupsConfig) (GroupResolver, error) {
		return webauthGroupResolverAdapter{inner: webauth.NewStaticGroups(cfg.StaticMembers)}, nil
	})

	RegisterDelegationPolicy("static", func(_ context.Context, cfg AuthConfig) (DelegationPolicy, error) {
		return NewStaticDelegationPolicy(cfg.ServicePrincipals)
	})

	RegisterDelegationPolicy("consent", func(_ context.Context, cfg AuthConfig) (DelegationPolicy, error) {
		return NewConsentDelegationPolicy(cfg, nil)
	})

	RegisterComputeProvider("none", func(_ context.Context, _ ComputeConfig) (Compute, error) {
		return noopComputeProvider{}, nil
	})

	RegisterInstanceResourcesProvider("none", func(_ context.Context, _ Config) (InstanceResources, error) {
		return noopInstanceResources{}, nil
	})

	RegisterConfigReposProvider("none", func(_ context.Context, _ ConfigReposConfig) (ConfigRepos, error) {
		return noopConfigRepos{}, nil
	})
}
