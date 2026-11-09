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
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"gopkg.in/yaml.v3"
)

const (
	DefaultListenAddr                 = ":8080"
	DefaultJWKSListenAddr             = ":8081"
	DefaultPeerListenAddr             = ":8082"
	DefaultConnectTransportName       = "ws-mux"
	DefaultJWTTTL                     = 60 * time.Second
	DefaultJWTCacheTTL                = 30 * time.Second
	DefaultCatalogHeartbeatInterval   = 15 * time.Second
	DefaultCatalogDegradedAfter       = 45 * time.Second
	DefaultCatalogOfflineAfter        = 90 * time.Second
	DefaultCatalogCacheTTL            = 3 * time.Second
	DefaultTunnelReplicaHeartbeat     = 5 * time.Second
	DefaultTunnelReplicaTTL           = 15 * time.Second
	DefaultTunnelMaxConcurrentStreams = 256
	DefaultTunnelMaxBufferedBytes     = 16 * 1024 * 1024
	DefaultDBRetentionPeriod          = 168 * time.Hour // 7 days
	DefaultQuotaReservationPeriod     = 720 * time.Hour // 30 days
	DefaultInstanceQuotaUnits         = 2
	DefaultConnectedQuota             = 20
	DefaultGroupsCacheTTL             = 5 * time.Minute
	DefaultComputeDrainTimeout        = 5 * time.Minute
	DefaultComputeHealthTimeout       = 5 * time.Minute
	DefaultComputeStatusPollInterval  = 15 * time.Second
)

var (
	prefixRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,14}-$`)
	hostnameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	shapeNameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	sqlInstNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// Config represents the complete configuration for `grain-registry`.
type Config struct {
	Listen         string              `yaml:"listen"`
	JWKSListen     string              `yaml:"jwks_listen"`
	Host           string              `yaml:"host"`
	InstanceDomain string              `yaml:"instance_domain"`
	Auth           AuthConfig          `yaml:"auth"`
	Groups         GroupsConfig        `yaml:"groups"`
	Signing        SigningConfig       `yaml:"signing"`
	Catalog        CatalogConfig       `yaml:"catalog"`
	Tunnel         TunnelConfig        `yaml:"tunnel"`
	Database       DatabaseConfig      `yaml:"database"`
	Compute        ComputeConfig       `yaml:"compute"`
	InstanceSQL    InstanceSQLConfig   `yaml:"instance_sql"`
	InstanceState  InstanceStateConfig `yaml:"instance_state"`
	Secrets        SecretsConfig       `yaml:"secrets"`
	Observability  ObservabilityConfig `yaml:"observability"`
	ConfigRepos    ConfigReposConfig   `yaml:"config_repos"`
	Quotas         QuotasConfig        `yaml:"quotas"`
}

// AuthConfig configures caller authentication, admission, and role bindings.
type AuthConfig struct {
	Provider          string   `yaml:"provider"`
	IAPAudience       string   `yaml:"iap_audience"`
	IAPCertsURL       string   `yaml:"iap_certs_url,omitempty"`
	AllowedDomains    []string `yaml:"allowed_domains"`
	Admins            []string `yaml:"admins"`
	Creators          []string `yaml:"creators"`
	ServicePrincipals []string `yaml:"service_principals"`
	DelegationPolicy  string   `yaml:"delegation_policy,omitempty"`
}

// GroupsConfig configures group membership resolution.
type GroupsConfig struct {
	Provider      string              `yaml:"provider"`
	CacheTTL      time.Duration       `yaml:"cache_ttl"`
	StaticMembers map[string][]string `yaml:"static_members,omitempty"`
}

// SigningConfig configures the registry's ES256 assertion signer and JWKS publisher.
type SigningConfig struct {
	Provider   string        `yaml:"provider"`
	KMSKeyName string        `yaml:"kms_key_name"`
	KeyFile    string        `yaml:"key_file"`
	KeyDir     string        `yaml:"key_dir"`
	KeyID      string        `yaml:"key_id"`
	JWTTTL     time.Duration `yaml:"jwt_ttl"`
}

// ScopeOwnerRule restricts which entry owners or co-owning groups may hold an
// active primary scope on a repository (`<owner>/<repo>`) or org/host prefix
// (`<prefix>/*`, such as `alpha/*` or `github.com/alpha/*`).
//
// When `catalog.scope_owners` is empty or no rule matches a repository, any
// admitted entry manager may claim non-overlapping primary scopes on that
// repository (first-registered wins across listed entries).
type ScopeOwnerRule struct {
	Repo       string   `yaml:"repo"`
	Principals []string `yaml:"principals"`
}

// CatalogConfig configures entry heartbeat, status thresholds, and optional
// primary-scope repository ownership rules.
type CatalogConfig struct {
	HeartbeatInterval time.Duration    `yaml:"heartbeat_interval"`
	DegradedAfter     time.Duration    `yaml:"degraded_after"`
	OfflineAfter      time.Duration    `yaml:"offline_after"`
	CacheTTL          time.Duration    `yaml:"cache_ttl"`
	ScopeOwners       []ScopeOwnerRule `yaml:"scope_owners,omitempty"`
}

// TunnelConfig configures the outbound `grain connect` tunnel listener and replica routing.
type TunnelConfig struct {
	Transport            string        `yaml:"transport"`
	PeerListen           string        `yaml:"peer_listen"`
	SelfAddr             string        `yaml:"self_addr"`
	ReplicaHeartbeat     time.Duration `yaml:"replica_heartbeat"`
	ReplicaTTL           time.Duration `yaml:"replica_ttl"`
	ReconnectGrace       time.Duration `yaml:"reconnect_grace"`
	DrainTimeout         time.Duration `yaml:"drain_timeout"`
	MaxConcurrentStreams int           `yaml:"max_concurrent_streams"`
	MaxBufferedBytes     int           `yaml:"max_buffered_bytes"`
}

// DatabaseConfig configures the registry's own PostgreSQL catalog database.
type DatabaseConfig struct {
	URL string `yaml:"url"`
}

// ComputeConfig configures hosted VM provisioning.
type ComputeConfig struct {
	Provider          string   `yaml:"provider"`
	Project           string   `yaml:"project"`
	Region            string   `yaml:"region"`
	Zones             []string `yaml:"zones"`
	Network           string   `yaml:"network"`
	Subnetwork        string   `yaml:"subnetwork"`
	BootImage         string   `yaml:"boot_image"`
	StartupScript     string   `yaml:"startup_script,omitempty"`
	StartupScriptFile string   `yaml:"startup_script_file,omitempty"`
	VMPrefix          string   `yaml:"vm_prefix"`
	SAPrefix          string   `yaml:"sa_prefix"`
	RegistryID        string   `yaml:"registry_id,omitempty"`
	SSHBreakGlass     *bool    `yaml:"ssh_break_glass"`
	BindProjectIAM    *bool    `yaml:"bind_project_iam,omitempty"`
	// GrantSelfActAs has the gce provider grant the registry's own principal
	// roles/iam.serviceAccountUser on each per-instance service account it
	// creates, so it can attach that account to the instance's VM. Defaults
	// to true; set false when actAs on the instance accounts is granted out
	// of band.
	GrantSelfActAs *bool `yaml:"grant_self_act_as,omitempty"`
	// SelfPrincipal overrides the IAM principal GrantSelfActAs grants
	// ("serviceAccount:<email>", "principal://...", "user:<email>"). Empty
	// means detect it from the registry's Application Default Credentials.
	SelfPrincipal         string            `yaml:"self_principal,omitempty"`
	Labels                map[string]string `yaml:"labels"`
	DefaultShape          string            `yaml:"default_shape"`
	Shapes                []Shape           `yaml:"shapes"`
	DrainTimeout          time.Duration     `yaml:"drain_timeout,omitempty"`
	HealthTimeout         time.Duration     `yaml:"health_timeout,omitempty"`
	StatusPollInterval    time.Duration     `yaml:"status_poll_interval,omitempty"`
	AutoUpgrade           string            `yaml:"auto_upgrade,omitempty"`
	TargetVersion         string            `yaml:"target_version,omitempty"`
	MaintenanceWindow     string            `yaml:"maintenance_window,omitempty"`
	MaxConcurrentUpgrades int               `yaml:"max_concurrent_upgrades,omitempty"`
}

// SQLInstanceConfig describes a shared Cloud SQL instance in the registry's pool.
type SQLInstanceConfig struct {
	Name           string `yaml:"name" json:"name"`
	ConnectionName string `yaml:"connection_name" json:"connection_name"`
	PrivateIP      string `yaml:"private_ip" json:"private_ip"`
	MaxDatabases   int    `yaml:"max_databases" json:"max_databases"`
}

// InstanceSQLConfig configures the pool of Cloud SQL instances for hosted VMs.
type InstanceSQLConfig struct {
	SQLInstances     []SQLInstanceConfig `yaml:"sql_instances"`
	AdminDSNTemplate string              `yaml:"admin_dsn_template,omitempty"`
	CloudSQLAdmin    *bool               `yaml:"cloud_sql_admin,omitempty"`
}

// InstanceStateConfig configures retention and GCS storage for hosted VMs.
type InstanceStateConfig struct {
	DBRetentionPeriod time.Duration `yaml:"db_retention_period"`
	GCSBucket         string        `yaml:"gcs_bucket,omitempty"`
}

// SecretsConfig configures Secret Manager prefix isolation for hosted VMs.
type SecretsConfig struct {
	Project      string `yaml:"project"`
	SecretPrefix string `yaml:"secret_prefix"`
}

// ObservabilityConfig configures optional fleet logs and metrics.
type ObservabilityConfig struct {
	FleetLogs bool `yaml:"fleet_logs"`
}

// ConfigReposConfig configures optional per-instance Git config repositories.
type ConfigReposConfig struct {
	Provider       string `yaml:"provider"`
	GitHubOrg      string `yaml:"github_org"`
	RepoPrefix     string `yaml:"repo_prefix"`
	AppID          int64  `yaml:"app_id"`
	Installation   int64  `yaml:"installation_id"`
	PrivateKeyFile string `yaml:"private_key_file,omitempty"`
	PrivateKeyPEM  string `yaml:"private_key_pem,omitempty"`
	KeySecret      string `yaml:"key_secret,omitempty"`
	APIBaseURL     string `yaml:"api_base_url,omitempty"`
}

// QuotaOverride specifies custom quota limits for a specific user or group.
type QuotaOverride struct {
	Principal        string `yaml:"principal"`
	MaxInstanceUnits int    `yaml:"max_instance_units"`
	MaxConnected     int    `yaml:"max_connected"`
}

// QuotasConfig configures per-owner quota limits and tombstone reservation.
type QuotasConfig struct {
	DefaultInstanceUnits int             `yaml:"default_instance_units"`
	DefaultConnected     int             `yaml:"default_connected"`
	ReservationPeriod    time.Duration   `yaml:"reservation_period"`
	Overrides            []QuotaOverride `yaml:"overrides"`
}

// LoadConfig reads, parses, applies defaults to, and validates a YAML registry config file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading hub config %s: %w", path, err)
	}
	return ParseConfig(data)
}

// ParseConfig unmarshals YAML bytes into a Config, applies defaults, and validates it.
func ParseConfig(data []byte) (*Config, error) {
	expanded := os.ExpandEnv(string(data))
	var cfg Config
	dec := yamlStrictDecoder(expanded)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing registry config YAML: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func yamlStrictDecoder(s string) *yaml.Decoder {
	dec := yaml.NewDecoder(strings.NewReader(s))
	dec.KnownFields(true)
	return dec
}

// ApplyDefaults populates default values for omitted configuration fields.
func (c *Config) ApplyDefaults() {
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = DefaultListenAddr
	}
	if strings.TrimSpace(c.JWKSListen) == "" {
		c.JWKSListen = DefaultJWKSListenAddr
	}
	if strings.TrimSpace(c.Auth.Provider) == "" {
		c.Auth.Provider = "iap"
	}
	if strings.TrimSpace(c.Groups.Provider) == "" {
		c.Groups.Provider = "cloud-identity"
	}
	if c.Groups.CacheTTL == 0 {
		c.Groups.CacheTTL = DefaultGroupsCacheTTL
	}
	if strings.TrimSpace(c.Signing.Provider) == "" {
		c.Signing.Provider = "kms"
	}
	if c.Signing.JWTTTL == 0 {
		c.Signing.JWTTTL = DefaultJWTTTL
	}
	if c.Catalog.HeartbeatInterval == 0 {
		c.Catalog.HeartbeatInterval = DefaultCatalogHeartbeatInterval
	}
	if c.Catalog.DegradedAfter == 0 {
		c.Catalog.DegradedAfter = DefaultCatalogDegradedAfter
	}
	if c.Catalog.OfflineAfter == 0 {
		c.Catalog.OfflineAfter = DefaultCatalogOfflineAfter
	}
	if c.Catalog.CacheTTL == 0 {
		c.Catalog.CacheTTL = DefaultCatalogCacheTTL
	}
	if strings.TrimSpace(c.Tunnel.Transport) == "" {
		c.Tunnel.Transport = DefaultConnectTransportName
	}
	if strings.TrimSpace(c.Tunnel.PeerListen) == "" {
		c.Tunnel.PeerListen = DefaultPeerListenAddr
	}
	if c.Tunnel.ReplicaHeartbeat == 0 {
		c.Tunnel.ReplicaHeartbeat = DefaultTunnelReplicaHeartbeat
	}
	if c.Tunnel.ReplicaTTL == 0 {
		c.Tunnel.ReplicaTTL = DefaultTunnelReplicaTTL
	}
	if c.Tunnel.ReconnectGrace == 0 {
		c.Tunnel.ReconnectGrace = 2 * time.Second
	}
	if c.Tunnel.DrainTimeout == 0 {
		c.Tunnel.DrainTimeout = 30 * time.Second
	}
	if c.Tunnel.MaxConcurrentStreams == 0 {
		c.Tunnel.MaxConcurrentStreams = DefaultTunnelMaxConcurrentStreams
	}
	if c.Tunnel.MaxBufferedBytes == 0 {
		c.Tunnel.MaxBufferedBytes = DefaultTunnelMaxBufferedBytes
	}
	if strings.TrimSpace(c.Compute.Provider) == "" {
		c.Compute.Provider = "gce"
	}
	if strings.TrimSpace(c.Compute.VMPrefix) == "" {
		c.Compute.VMPrefix = "grain-vm-"
	}
	if strings.TrimSpace(c.Compute.SAPrefix) == "" {
		c.Compute.SAPrefix = "grain-sa-"
	}
	if c.Compute.SSHBreakGlass == nil {
		v := true
		c.Compute.SSHBreakGlass = &v
	}
	if c.Compute.BindProjectIAM == nil {
		v := true
		c.Compute.BindProjectIAM = &v
	}
	if c.Compute.GrantSelfActAs == nil {
		v := true
		c.Compute.GrantSelfActAs = &v
	}
	if c.InstanceSQL.CloudSQLAdmin == nil {
		v := true
		c.InstanceSQL.CloudSQLAdmin = &v
	}
	if c.Compute.DrainTimeout == 0 {
		c.Compute.DrainTimeout = DefaultComputeDrainTimeout
	}
	if c.Compute.HealthTimeout == 0 {
		c.Compute.HealthTimeout = DefaultComputeHealthTimeout
	}
	if c.Compute.StatusPollInterval == 0 {
		c.Compute.StatusPollInterval = DefaultComputeStatusPollInterval
	}
	if strings.TrimSpace(c.Compute.AutoUpgrade) == "" {
		c.Compute.AutoUpgrade = string(api.AutoUpgradeOff)
	}
	if c.Compute.MaxConcurrentUpgrades == 0 {
		c.Compute.MaxConcurrentUpgrades = 1
	}
	if c.InstanceState.DBRetentionPeriod == 0 {
		c.InstanceState.DBRetentionPeriod = DefaultDBRetentionPeriod
	}
	if strings.TrimSpace(c.Secrets.SecretPrefix) == "" {
		c.Secrets.SecretPrefix = "grain-inst-"
	}
	if strings.TrimSpace(c.ConfigRepos.Provider) == "" {
		c.ConfigRepos.Provider = "none"
	}
	if strings.TrimSpace(c.ConfigRepos.RepoPrefix) == "" {
		c.ConfigRepos.RepoPrefix = "grain-cfg-"
	}
	if c.Quotas.DefaultInstanceUnits == 0 {
		c.Quotas.DefaultInstanceUnits = DefaultInstanceQuotaUnits
	}
	if c.Quotas.DefaultConnected == 0 {
		c.Quotas.DefaultConnected = DefaultConnectedQuota
	}
	if c.Quotas.ReservationPeriod == 0 {
		c.Quotas.ReservationPeriod = DefaultQuotaReservationPeriod
	}
}

// Validate checks all configuration invariants using the default group normalizer.
func (c *Config) Validate() error {
	return c.ValidateWithResolver(nil)
}

// ValidateWithResolver checks all configuration invariants and normalizes any
// `group:` entries in `auth.admins`, `auth.creators`, and `quotas.overrides`
// using resolver if provided (Core follow-up C-1).
func (c *Config) ValidateWithResolver(resolver any) error {
	c.ApplyDefaults()

	host := strings.ToLower(strings.TrimSpace(c.Host))
	instDomain := strings.ToLower(strings.TrimSpace(c.InstanceDomain))
	if host == "" {
		return errors.New("config: host is required")
	}
	if !hostnameRe.MatchString(host) {
		return fmt.Errorf("config: host %q must be a valid DNS hostname", c.Host)
	}
	if instDomain == "" {
		return errors.New("config: instance_domain is required")
	}
	if !hostnameRe.MatchString(instDomain) {
		return fmt.Errorf("config: instance_domain %q must be a valid DNS domain", c.InstanceDomain)
	}
	if host == instDomain {
		return fmt.Errorf("config: host (%q) and instance_domain (%q) must not be equal", host, instDomain)
	}
	if strings.HasSuffix(host, "."+instDomain) {
		label := strings.TrimSuffix(host, "."+instDomain)
		if !strings.Contains(label, ".") {
			return fmt.Errorf("config: host (%q) is a single-label child of instance_domain (%q) and would collide with wildcard entry routing; use a sibling domain or reserved name", host, instDomain)
		}
	}
	c.Host = host
	c.InstanceDomain = instDomain

	// Auth validation
	if len(c.Auth.AllowedDomains) == 0 {
		return errors.New("config: auth.allowed_domains must be non-empty")
	}
	for i, d := range c.Auth.AllowedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || !hostnameRe.MatchString(d) {
			return fmt.Errorf("config: auth.allowed_domains[%d] %q is not a valid domain", i, c.Auth.AllowedDomains[i])
		}
		c.Auth.AllowedDomains[i] = d
	}
	if c.Auth.Provider == "iap" && strings.TrimSpace(c.Auth.IAPAudience) == "" {
		return errors.New("config: auth.iap_audience is required when auth.provider is \"iap\"")
	}

	normalizedAdmins, err := normalizeUserOrGroupList(resolver, "auth.admins", c.Auth.Admins)
	if err != nil {
		return err
	}
	c.Auth.Admins = normalizedAdmins

	normalizedCreators, err := normalizeUserOrGroupList(resolver, "auth.creators", c.Auth.Creators)
	if err != nil {
		return err
	}
	c.Auth.Creators = normalizedCreators

	for i, sp := range c.Auth.ServicePrincipals {
		norm, err := normalizeServicePrincipal(sp)
		if err != nil {
			return fmt.Errorf("config: auth.service_principals[%d]: %w", i, err)
		}
		c.Auth.ServicePrincipals[i] = norm
	}

	// Signing validation
	switch c.Signing.Provider {
	case "kms":
		if strings.TrimSpace(c.Signing.KMSKeyName) == "" {
			return errors.New("config: signing.kms_key_name is required when signing.provider is \"kms\"")
		}
	case "local":
		// local allows key_file, key_dir, or ephemeral in-memory key for tests/dev
	default:
		regMu.RLock()
		_, ok := signerFactories[c.Signing.Provider]
		regMu.RUnlock()
		if !ok {
			return fmt.Errorf("config: unsupported signing.provider %q", c.Signing.Provider)
		}
	}
	if c.Signing.JWTTTL <= 0 {
		return errors.New("config: signing.jwt_ttl must be positive")
	}

	// Catalog thresholds: offline_after > degraded_after > heartbeat_interval > 0
	if c.Catalog.HeartbeatInterval <= 0 {
		return errors.New("config: catalog.heartbeat_interval must be positive")
	}
	if c.Catalog.DegradedAfter <= c.Catalog.HeartbeatInterval {
		return fmt.Errorf("config: catalog.degraded_after (%s) must be greater than catalog.heartbeat_interval (%s)", c.Catalog.DegradedAfter, c.Catalog.HeartbeatInterval)
	}
	if c.Catalog.OfflineAfter <= c.Catalog.DegradedAfter {
		return fmt.Errorf("config: catalog.offline_after (%s) must be greater than catalog.degraded_after (%s)", c.Catalog.OfflineAfter, c.Catalog.DegradedAfter)
	}
	if c.Catalog.CacheTTL < 0 {
		return errors.New("config: catalog.cache_ttl must not be negative")
	}
	seenScopeOwnerRepos := make(map[string]bool, len(c.Catalog.ScopeOwners))
	for i, rule := range c.Catalog.ScopeOwners {
		pattern, err := normalizeScopeOwnerRepoPattern(rule.Repo)
		if err != nil {
			return fmt.Errorf("config: catalog.scope_owners[%d].repo: %w", i, err)
		}
		if seenScopeOwnerRepos[pattern] {
			return fmt.Errorf("config: duplicate catalog.scope_owners repo pattern %q", pattern)
		}
		seenScopeOwnerRepos[pattern] = true
		if len(rule.Principals) == 0 {
			return fmt.Errorf("config: catalog.scope_owners[%d].principals (%s) must be non-empty", i, pattern)
		}
		normPrincipals, err := normalizeUserOrGroupList(resolver, fmt.Sprintf("catalog.scope_owners[%d].principals", i), rule.Principals)
		if err != nil {
			return err
		}
		c.Catalog.ScopeOwners[i].Repo = pattern
		c.Catalog.ScopeOwners[i].Principals = normPrincipals
	}

	// Tunnel validation
	if c.Tunnel.ReplicaHeartbeat <= 0 {
		return errors.New("config: tunnel.replica_heartbeat must be positive")
	}
	if c.Tunnel.ReplicaTTL <= c.Tunnel.ReplicaHeartbeat {
		return fmt.Errorf("config: tunnel.replica_ttl (%s) must be greater than tunnel.replica_heartbeat (%s)", c.Tunnel.ReplicaTTL, c.Tunnel.ReplicaHeartbeat)
	}
	if c.Tunnel.MaxConcurrentStreams <= 0 {
		return errors.New("config: tunnel.max_concurrent_streams must be > 0")
	}
	if c.Tunnel.MaxBufferedBytes <= 0 {
		return errors.New("config: tunnel.max_buffered_bytes must be > 0")
	}
	regMu.RLock()
	_, hasTransport := connectTransports[c.Tunnel.Transport]
	regMu.RUnlock()
	if !hasTransport {
		return fmt.Errorf("config: unknown tunnel.transport %q", c.Tunnel.Transport)
	}

	// Compute & SQL pool validation
	if !prefixRe.MatchString(c.Compute.VMPrefix) {
		return fmt.Errorf("config: compute.vm_prefix %q must match %s", c.Compute.VMPrefix, prefixRe.String())
	}
	if !prefixRe.MatchString(c.Compute.SAPrefix) {
		return fmt.Errorf("config: compute.sa_prefix %q must match %s", c.Compute.SAPrefix, prefixRe.String())
	}
	policy := api.AutoUpgradePolicy(strings.ToLower(strings.TrimSpace(c.Compute.AutoUpgrade)))
	if !policy.Valid() {
		return fmt.Errorf("config: compute.auto_upgrade %q must be one of off, notify, or minor", c.Compute.AutoUpgrade)
	}
	c.Compute.AutoUpgrade = string(policy)
	if c.Compute.MaxConcurrentUpgrades <= 0 {
		return errors.New("config: compute.max_concurrent_upgrades must be > 0")
	}
	if strings.TrimSpace(c.Compute.MaintenanceWindow) != "" {
		if _, _, err := ParseMaintenanceWindow(c.Compute.MaintenanceWindow); err != nil {
			return fmt.Errorf("config: compute.maintenance_window: %w", err)
		}
	}
	if c.Compute.Provider != "none" {
		if strings.TrimSpace(c.Compute.Project) == "" {
			return errors.New("config: compute.project is required when compute.provider != \"none\"")
		}
		if strings.TrimSpace(c.Compute.Region) == "" {
			return errors.New("config: compute.region is required when compute.provider != \"none\"")
		}
		if len(c.Compute.Zones) == 0 {
			return errors.New("config: compute.zones must be non-empty when compute.provider != \"none\"")
		}
		regionPrefix := strings.TrimSpace(c.Compute.Region) + "-"
		for _, z := range c.Compute.Zones {
			if !strings.HasPrefix(strings.TrimSpace(z), regionPrefix) {
				return fmt.Errorf("config: compute.zone %q does not belong to compute.region %q", z, c.Compute.Region)
			}
		}
		if len(c.Compute.Shapes) == 0 {
			return errors.New("config: compute.shapes must be non-empty when compute.provider != \"none\"")
		}
		shapeSet := make(map[string]bool, len(c.Compute.Shapes))
		for i, s := range c.Compute.Shapes {
			if !shapeNameRe.MatchString(s.Name) {
				return fmt.Errorf("config: compute.shapes[%d].name %q is invalid", i, s.Name)
			}
			if shapeSet[s.Name] {
				return fmt.Errorf("config: duplicate compute shape %q", s.Name)
			}
			if strings.TrimSpace(s.MachineType) == "" {
				return fmt.Errorf("config: compute.shapes[%d] (%s) machine_type is required", i, s.Name)
			}
			if c.Compute.Provider == "gce" && isE2MachineType(s.MachineType) {
				return fmt.Errorf("config: compute.shapes[%d] (%s) machine_type %q does not support nested virtualization, which hosted VMs need; use an N2 or C4 machine type (for example n2-standard-4 or c4-standard-4)", i, s.Name, s.MachineType)
			}
			if s.BootDiskGB <= 0 {
				return fmt.Errorf("config: compute.shapes[%d] (%s) boot_disk_gb must be > 0", i, s.Name)
			}
			if s.QuotaUnits <= 0 {
				return fmt.Errorf("config: compute.shapes[%d] (%s) quota_units must be > 0", i, s.Name)
			}
			shapeSet[s.Name] = true
		}
		if c.Compute.DefaultShape != "" && !shapeSet[c.Compute.DefaultShape] {
			return fmt.Errorf("config: compute.default_shape %q is not defined in compute.shapes", c.Compute.DefaultShape)
		}

		if len(c.InstanceSQL.SQLInstances) == 0 {
			return errors.New("config: instance_sql.sql_instances must be non-empty when compute.provider != \"none\"")
		}
		sqlNames := make(map[string]bool, len(c.InstanceSQL.SQLInstances))
		for i, inst := range c.InstanceSQL.SQLInstances {
			if !sqlInstNameRe.MatchString(inst.Name) {
				return fmt.Errorf("config: instance_sql.sql_instances[%d].name %q is invalid", i, inst.Name)
			}
			if sqlNames[inst.Name] {
				return fmt.Errorf("config: duplicate sql_instance name %q", inst.Name)
			}
			if strings.TrimSpace(inst.ConnectionName) == "" {
				return fmt.Errorf("config: instance_sql.sql_instances[%d] (%s) connection_name is required", i, inst.Name)
			}
			if inst.MaxDatabases <= 0 {
				return fmt.Errorf("config: instance_sql.sql_instances[%d] (%s) max_databases must be > 0", i, inst.Name)
			}
			sqlNames[inst.Name] = true
		}
	}

	// Quotas & retention validation
	if c.Quotas.DefaultInstanceUnits < 0 {
		return errors.New("config: quotas.default_instance_units must not be negative")
	}
	if c.Quotas.DefaultConnected < 0 {
		return errors.New("config: quotas.default_connected must not be negative")
	}
	if c.Quotas.ReservationPeriod < c.InstanceState.DBRetentionPeriod {
		return fmt.Errorf("config: quotas.reservation_period (%s) must be >= instance_state.db_retention_period (%s)", c.Quotas.ReservationPeriod, c.InstanceState.DBRetentionPeriod)
	}
	for i, ov := range c.Quotas.Overrides {
		norm, err := normalizeUserOrGroup(resolver, ov.Principal)
		if err != nil {
			return fmt.Errorf("config: quotas.overrides[%d].principal: %w", i, err)
		}
		c.Quotas.Overrides[i].Principal = norm
	}

	return nil
}

// isE2MachineType reports whether a GCE machine type (bare, or a
// "zones/<z>/machineTypes/<type>" path) is in the E2 series, which cannot run
// nested virtualization.
func isE2MachineType(machineType string) bool {
	mt := strings.TrimSpace(machineType)
	if idx := strings.LastIndex(mt, "/"); idx >= 0 {
		mt = mt[idx+1:]
	}
	return strings.HasPrefix(mt, "e2-")
}

func normalizeUserOrGroupList(resolver any, field string, items []string) ([]string, error) {
	if len(items) == 0 {
		return items, nil
	}
	out := make([]string, len(items))
	for i, raw := range items {
		norm, err := normalizeUserOrGroup(resolver, raw)
		if err != nil {
			return nil, fmt.Errorf("config: %s[%d]: %w", field, i, err)
		}
		out[i] = norm
	}
	return out, nil
}

func normalizeUserOrGroup(resolver any, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "user:"):
		email := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "user:")))
		if !isValidEmail(email) {
			return "", fmt.Errorf("invalid user principal %q (expected user:<email>)", raw)
		}
		return "user:" + email, nil
	case strings.HasPrefix(raw, "group:"):
		return NormalizeGroup(resolver, raw)
	default:
		return "", fmt.Errorf("principal %q must start with \"user:\" or \"group:\"", raw)
	}
}

func normalizeServicePrincipal(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "serviceAccount:"):
		email := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "serviceAccount:")))
		if !isValidEmail(email) {
			return "", fmt.Errorf("invalid serviceAccount principal %q", raw)
		}
		return "serviceAccount:" + email, nil
	case strings.HasPrefix(raw, "user:"):
		email := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "user:")))
		if !isValidEmail(email) {
			return "", fmt.Errorf("invalid user principal %q", raw)
		}
		return "user:" + email, nil
	default:
		return "", fmt.Errorf("service principal %q must start with \"serviceAccount:\" or \"user:\"", raw)
	}
}

func isValidEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return false
	}
	domain := email[at+1:]
	return !strings.Contains(domain, "@") && strings.Contains(domain, ".")
}

func normalizeScopeOwnerRepoPattern(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", errors.New("repo pattern is required")
	}
	if strings.HasSuffix(s, "/*") {
		prefix := strings.TrimSuffix(s, "/*")
		if prefix == "" || strings.ContainsAny(prefix, " \t\r\n*?#\\") {
			return "", fmt.Errorf("invalid repo wildcard pattern %q (expected <owner>/* or <host>/<owner>/*)", raw)
		}
		for _, seg := range strings.Split(prefix, "/") {
			if seg == "" || seg == "." || seg == ".." {
				return "", fmt.Errorf("invalid repo wildcard pattern %q: invalid segment %q", raw, seg)
			}
		}
		return prefix + "/*", nil
	}
	if strings.Contains(s, "*") {
		return "", fmt.Errorf("invalid repo pattern %q (wildcards are only supported as trailing \"/*\")", raw)
	}
	return api.NormalizeRepo(s)
}

// MatchScopeOwnerRule returns the most-specific ScopeOwnerRule matching repo:
// an exact `<owner>/<repo>` rule wins over `<prefix>/*` rules, and longer
// `<prefix>/*` rules win over shorter ones.
//
// Repository identifiers are free-form, so the same repository can be claimed
// as `acme/billing` or host-qualified as `github.com/acme/billing`, and
// /api/route matches whichever form a caller queries. A rule written in the
// bare `<owner>/...` form therefore also governs host-qualified claims of the
// same path (`<host>/<owner>/...`, where the first segment contains a dot);
// otherwise `acme/*` could be sidestepped by claiming `github.com/acme/billing`.
// Rules naming the host-qualified form are consulted first, so an operator can
// still give one host's copy of a repository its own principals.
func MatchScopeOwnerRule(rules []ScopeOwnerRule, repo string) (*ScopeOwnerRule, bool) {
	if len(rules) == 0 {
		return nil, false
	}
	normRepo, err := api.NormalizeRepo(repo)
	if err != nil {
		normRepo = strings.ToLower(strings.TrimSpace(repo))
	}
	forms := []string{normRepo}
	if host, rest, ok := strings.Cut(normRepo, "/"); ok && strings.Contains(host, ".") && strings.Contains(rest, "/") {
		forms = append(forms, rest)
	}
	for _, form := range forms {
		for i := range rules {
			if rules[i].Repo == form {
				return &rules[i], true
			}
		}
	}
	for _, form := range forms {
		var bestPrefix *ScopeOwnerRule
		bestPrefixLen := -1
		for i := range rules {
			pat := rules[i].Repo
			if !strings.HasSuffix(pat, "/*") {
				continue
			}
			prefix := strings.TrimSuffix(pat, "/*")
			if strings.HasPrefix(form, prefix+"/") && len(prefix) > bestPrefixLen {
				bestPrefix = &rules[i]
				bestPrefixLen = len(prefix)
			}
		}
		if bestPrefix != nil {
			return bestPrefix, true
		}
	}
	return nil, false
}

// ParseMaintenanceWindow parses a UTC maintenance window in "HH:MM-HH:MM" format
// into start and end minutes since 00:00 UTC. End may be <= start for windows
// wrapping across midnight (or equal to 24:00 as 1440).
func ParseMaintenanceWindow(spec string) (startMinute, endMinute int, err error) {
	spec = strings.TrimSpace(spec)
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid maintenance window %q: expected HH:MM-HH:MM", spec)
	}
	startMinute, err = parseClockHHMM(parts[0], false)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid maintenance window start %q: %w", parts[0], err)
	}
	endMinute, err = parseClockHHMM(parts[1], true)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid maintenance window end %q: %w", parts[1], err)
	}
	return startMinute, endMinute, nil
}

func parseClockHHMM(s string, allow2400 bool) (int, error) {
	s = strings.TrimSpace(s)
	if allow2400 && s == "24:00" {
		return 24 * 60, nil
	}
	hm := strings.Split(s, ":")
	if len(hm) != 2 || len(hm[0]) != 2 || len(hm[1]) != 2 {
		return 0, fmt.Errorf("expected HH:MM")
	}
	h, err := strconv.Atoi(hm[0])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("hour out of range 00..23")
	}
	m, err := strconv.Atoi(hm[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("minute out of range 00..59")
	}
	return h*60 + m, nil
}

// InMaintenanceWindow reports whether UTC timestamp t falls inside the
// "HH:MM-HH:MM" window spec. An empty spec matches all times.
func InMaintenanceWindow(spec string, t time.Time) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true
	}
	startMin, endMin, err := ParseMaintenanceWindow(spec)
	if err != nil {
		return false
	}
	if startMin == endMin {
		return true
	}
	u := t.UTC()
	cur := u.Hour()*60 + u.Minute()
	if startMin < endMin {
		return cur >= startMin && cur < endMin
	}
	// Overnight window wrapping past 00:00 UTC (for example "22:00-04:00").
	return cur >= startMin || cur < endMin
}
