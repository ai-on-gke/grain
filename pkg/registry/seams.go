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

package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/webauth"
)

// Shape describes an instance hardware shape configured for hosted VMs.
type Shape struct {
	Name         string `yaml:"name" json:"name"`
	MachineType  string `yaml:"machine_type" json:"machine_type"`
	BootDiskGB   int    `yaml:"boot_disk_gb" json:"boot_disk_gb"`
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
	ServiceAccount string
	BootImage      string
	StartupEnv     map[string]string
	Labels         map[string]string
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
	State      string
	InternalIP string
	BackendURL string
}

// Compute provisions and manages hosted VM instances.
type Compute interface {
	CreateInstance(ctx context.Context, spec InstanceSpec) (*InstanceHandle, error)
	DeleteInstance(ctx context.Context, zone, instanceID string) error
	GetInstanceStatus(ctx context.Context, zone, instanceID string) (*InstanceStatus, error)
}

// InstanceResourceSpec holds parameters for provisioning per-instance cloud resources.
type InstanceResourceSpec struct {
	EntryID     string
	Name        string
	OwnerEmail  string
	SQLInstance string
}

// InstanceResourceBundle describes the provisioned per-instance identity, database, and secret scope.
type InstanceResourceBundle struct {
	ServiceAccountEmail string
	DatabaseName        string
	SecretPrefix        string
}

// InstanceResources provisions per-instance IAM identities, databases, and secret prefixes.
type InstanceResources interface {
	Provision(ctx context.Context, spec InstanceResourceSpec) (*InstanceResourceBundle, error)
	Teardown(ctx context.Context, spec InstanceResourceSpec, bundle InstanceResourceBundle, retainDB bool) error
	GrantSSH(ctx context.Context, zone, instanceID, userEmail string) error
	RevokeSSH(ctx context.Context, zone, instanceID, userEmail string) error
}

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
	Sub string `json:"sub"`
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
	return nil, fmt.Errorf("%w: principal %q cannot act on behalf of %q for %q", ErrDelegationDenied, servicePrincipal, targetUserEmail, targetEntryName)
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
)

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

func (noopComputeProvider) DeleteInstance(context.Context, string, string) error {
	return errors.New("registry: compute provider is disabled (compute.provider = \"none\")")
}

func (noopComputeProvider) GetInstanceStatus(context.Context, string, string) (*InstanceStatus, error) {
	return nil, errors.New("registry: compute provider is disabled (compute.provider = \"none\")")
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
