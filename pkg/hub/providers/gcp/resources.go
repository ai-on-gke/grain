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
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
)

const (
	defaultIAMBaseURL             = "https://iam.googleapis.com"
	defaultResourceManagerBaseURL = "https://cloudresourcemanager.googleapis.com"
	defaultSQLAdminBaseURL        = "https://sqladmin.googleapis.com"
	defaultStorageBaseURL         = "https://storage.googleapis.com"
	defaultSecretManagerBaseURL   = "https://secretmanager.googleapis.com"

	DefaultRoleConnectionLimit = 20
	DefaultSSHGrantTTL         = 2 * time.Hour
)

var (
	// ErrSSHBreakGlassDisabled is returned when GrantSSH is called while compute.ssh_break_glass is false.
	ErrSSHBreakGlassDisabled = errors.New("gcp resources: break-glass SSH is disabled by configuration")

	dbIdentRe = regexp.MustCompile(`^grain_[a-z0-9_]{1,57}$`)
)

// SQLPoolAllocator selects or looks up shared Cloud SQL instance placement from the registry store.
type SQLPoolAllocator interface {
	AllocateSQLInstance(ctx context.Context, entryID, entryName string) (*store.SQLInstanceRow, string, error)
	GetSQLInstance(ctx context.Context, name string) (*store.SQLInstanceRow, error)
}

// SQLDBOpener opens a `*sql.DB` connection to `dbName` (e.g. `"postgres"` or `"grain_alice"`)
// on the named shared `sqlInstance` so the provisioner can execute PostgreSQL role and database ACL statements.
type SQLDBOpener func(ctx context.Context, sqlInstance, dbName string) (*sql.DB, error)

// ResourcesOption configures a GCPInstanceResources provider.
type ResourcesOption func(*GCPInstanceResources)

// WithResourcesHTTPClient overrides the HTTP client used for GCP REST API calls.
func WithResourcesHTTPClient(hc *http.Client) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if hc != nil {
			r.httpClient = hc
		}
	}
}

// WithIAMBaseURL overrides the IAM API base URL.
func WithIAMBaseURL(u string) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if v := strings.TrimSpace(u); v != "" {
			r.iamBaseURL = strings.TrimRight(v, "/")
		}
	}
}

// WithResourceManagerBaseURL overrides the Cloud Resource Manager API base URL.
func WithResourceManagerBaseURL(u string) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if v := strings.TrimSpace(u); v != "" {
			r.crmBaseURL = strings.TrimRight(v, "/")
		}
	}
}

// WithSQLAdminBaseURL overrides the Cloud SQL Admin API base URL.
func WithSQLAdminBaseURL(u string) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if v := strings.TrimSpace(u); v != "" {
			r.sqlAdminBaseURL = strings.TrimRight(v, "/")
		}
	}
}

// WithStorageBaseURL overrides the Cloud Storage JSON API base URL.
func WithStorageBaseURL(u string) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if v := strings.TrimSpace(u); v != "" {
			r.storageBaseURL = strings.TrimRight(v, "/")
		}
	}
}

// WithSecretManagerBaseURL overrides the Secret Manager API base URL.
func WithSecretManagerBaseURL(u string) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if v := strings.TrimSpace(u); v != "" {
			r.secretBaseURL = strings.TrimRight(v, "/")
		}
	}
}

// WithResourcesComputeBaseURL overrides the Compute Engine v1 base URL used by GrantSSH/RevokeSSH.
func WithResourcesComputeBaseURL(u string) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if v := strings.TrimSpace(u); v != "" {
			r.computeBaseURL = strings.TrimRight(v, "/")
		}
	}
}

// WithSQLPoolAllocator wires a store-backed `SQLPoolAllocator` for least-loaded `sql_instances` placement.
func WithSQLPoolAllocator(alloc SQLPoolAllocator) ResourcesOption {
	return func(r *GCPInstanceResources) {
		r.sqlAllocator = alloc
	}
}

// GCPInstanceResources implements `registry.InstanceResources` and `registry.SecretWriter`,
// provisioning per-instance GCP Service Accounts, shared Cloud SQL databases + IAM roles
// (`REVOKE ALL ON DATABASE ... FROM PUBLIC`, `CONNECTION LIMIT 20`), GCS Managed Folders,
// and Secret Manager conditional IAM bindings.
type GCPInstanceResources struct {
	cfg             hub.Config
	project         string
	secretsProject  string
	vmPrefix        string
	saPrefix        string
	secretPrefix    string
	gcsBucket       string
	sshBreakGlass   bool
	bindProjectIAM  bool
	grantSelfActAs  bool
	cloudSQLAdmin   bool
	fleetLogs       bool
	zoneSet         map[string]bool
	sqlConfigs      map[string]hub.SQLInstanceConfig
	sqlAllocator    SQLPoolAllocator
	sqlOpener       SQLDBOpener
	httpClient      *http.Client
	iamBaseURL      string
	crmBaseURL      string
	sqlAdminBaseURL string
	storageBaseURL  string
	secretBaseURL   string
	computeBaseURL  string
	nowFn           func() time.Time
	// selfPrincipal is the registry's own IAM principal, granted
	// roles/iam.serviceAccountUser on each instance account when
	// grantSelfActAs is set: compute.self_principal, or detected on first
	// use by principalResolver and cached.
	selfPrincipal     string
	selfPrincipalMu   sync.Mutex
	principalResolver func(ctx context.Context) (string, error)
	sleep             func(ctx context.Context, d time.Duration) error
}

// SetStore wires the registry catalog store as the SQL pool allocator when none was explicitly configured.
func (r *GCPInstanceResources) SetStore(st *store.Store) {
	if r != nil && st != nil && r.sqlAllocator == nil {
		r.sqlAllocator = st
	}
}

// NewGCPInstanceResources constructs a GCPInstanceResources provider from `cfg`.
func NewGCPInstanceResources(ctx context.Context, cfg hub.Config, opts ...ResourcesOption) (*GCPInstanceResources, error) {
	project := strings.TrimSpace(cfg.Compute.Project)
	if project == "" {
		return nil, errors.New("gcp resources: compute.project is required")
	}
	secretsProject := strings.TrimSpace(cfg.Secrets.Project)
	if secretsProject == "" {
		secretsProject = project
	}
	vmPrefix := strings.TrimSpace(cfg.Compute.VMPrefix)
	if vmPrefix == "" {
		vmPrefix = "grain-vm-"
	}
	saPrefix := strings.TrimSpace(cfg.Compute.SAPrefix)
	if saPrefix == "" {
		saPrefix = "grain-sa-"
	}
	secretPrefix := strings.TrimSpace(cfg.Secrets.SecretPrefix)
	if secretPrefix == "" {
		secretPrefix = "grain-inst-"
	}
	sshBreakGlass := true
	if cfg.Compute.SSHBreakGlass != nil {
		sshBreakGlass = *cfg.Compute.SSHBreakGlass
	}
	bindProjectIAM := true
	if cfg.Compute.BindProjectIAM != nil {
		bindProjectIAM = *cfg.Compute.BindProjectIAM
	}
	grantSelfActAs := true
	if cfg.Compute.GrantSelfActAs != nil {
		grantSelfActAs = *cfg.Compute.GrantSelfActAs
	}
	selfPrincipal, err := normalizeSelfPrincipal(cfg.Compute.SelfPrincipal)
	if err != nil {
		return nil, fmt.Errorf("gcp resources: compute.self_principal: %w", err)
	}
	cloudSQLAdmin := true
	if cfg.InstanceSQL.CloudSQLAdmin != nil {
		cloudSQLAdmin = *cfg.InstanceSQL.CloudSQLAdmin
	}
	zoneSet := make(map[string]bool, len(cfg.Compute.Zones))
	for _, z := range cfg.Compute.Zones {
		if z = strings.TrimSpace(z); z != "" {
			zoneSet[z] = true
		}
	}
	sqlConfigs := make(map[string]hub.SQLInstanceConfig, len(cfg.InstanceSQL.SQLInstances))
	for _, sq := range cfg.InstanceSQL.SQLInstances {
		sqlConfigs[sq.Name] = sq
	}

	r := &GCPInstanceResources{
		cfg:             cfg,
		project:         project,
		secretsProject:  secretsProject,
		vmPrefix:        vmPrefix,
		saPrefix:        saPrefix,
		secretPrefix:    secretPrefix,
		gcsBucket:       strings.TrimSpace(cfg.InstanceState.GCSBucket),
		sshBreakGlass:   sshBreakGlass,
		bindProjectIAM:  bindProjectIAM,
		grantSelfActAs:  grantSelfActAs,
		selfPrincipal:   selfPrincipal,
		sleep:           sleepContext,
		cloudSQLAdmin:   cloudSQLAdmin,
		fleetLogs:       cfg.Observability.FleetLogs,
		zoneSet:         zoneSet,
		sqlConfigs:      sqlConfigs,
		iamBaseURL:      defaultIAMBaseURL,
		crmBaseURL:      defaultResourceManagerBaseURL,
		sqlAdminBaseURL: defaultSQLAdminBaseURL,
		storageBaseURL:  defaultStorageBaseURL,
		secretBaseURL:   defaultSecretManagerBaseURL,
		computeBaseURL:  defaultComputeBaseURL,
		nowFn:           time.Now,
	}
	if strings.TrimSpace(cfg.InstanceSQL.AdminDSNTemplate) != "" {
		tmpl := strings.TrimSpace(cfg.InstanceSQL.AdminDSNTemplate)
		r.sqlOpener = func(_ context.Context, sqlInstance, dbName string) (*sql.DB, error) {
			dsn := strings.ReplaceAll(tmpl, "{instance}", sqlInstance)
			dsn = strings.ReplaceAll(dsn, "{database}", dbName)
			return sql.Open("pgx", dsn)
		}
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.httpClient == nil {
		ts, err := google.DefaultTokenSource(ctx, cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("gcp resources: resolving Application Default Credentials: %w", err)
		}
		r.httpClient = oauth2.NewClient(ctx, ts)
		r.httpClient.Timeout = 30 * time.Second
	}
	if r.principalResolver == nil {
		r.principalResolver = newADCPrincipalDetector().detect
	}
	return r, nil
}

// ServiceAccountIDForEntry computes the 6..30 character GCP service account ID for `(saPrefix, entryName)`.
// When `len(saPrefix + entryName) > 30`, it deterministically truncates and appends `-<6-hex-sha256>`
// so the resulting ID is exactly 30 characters, starts with `saPrefix`, and is collision-resistant.
func ServiceAccountIDForEntry(saPrefix, entryName string) (string, error) {
	entryName = strings.ToLower(strings.TrimSpace(entryName))
	if err := api.ValidateEntryName(entryName); err != nil {
		return "", fmt.Errorf("%w: invalid entry name %q: %v", ErrOutOfScope, entryName, err)
	}
	saPrefix = strings.ToLower(strings.TrimSpace(saPrefix))
	if saPrefix == "" {
		saPrefix = "gi-"
	}
	if !strings.HasSuffix(saPrefix, "-") {
		saPrefix += "-"
	}
	raw := saPrefix + entryName
	if len(raw) > 30 {
		sum := sha256.Sum256([]byte(entryName))
		hashSuffix := hex.EncodeToString(sum[:])[:6]
		// 30 total chars = 23 prefix+slug chars + '-' + 6 hex chars.
		head := strings.TrimRight(raw[:23], "-")
		for len(head) < 23 {
			head += "x"
		}
		raw = head + "-" + hashSuffix
	}
	for len(raw) < 6 {
		raw += "0"
	}
	return raw, nil
}

// ServiceAccountEmailForEntry returns `<accountId>@<project>.iam.gserviceaccount.com`.
func (r *GCPInstanceResources) ServiceAccountEmailForEntry(entryName string) (string, string, error) {
	accountID, err := ServiceAccountIDForEntry(r.saPrefix, entryName)
	if err != nil {
		return "", "", err
	}
	email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, r.project)
	return accountID, email, nil
}

// DatabaseNameForEntry returns the validated PostgreSQL database name `grain_<name with '-' as '_'>`.
func DatabaseNameForEntry(entryName string) (string, error) {
	entryName = strings.ToLower(strings.TrimSpace(entryName))
	if err := api.ValidateEntryName(entryName); err != nil {
		return "", fmt.Errorf("%w: invalid entry name %q: %v", ErrOutOfScope, entryName, err)
	}
	dbName := "grain_" + strings.ReplaceAll(entryName, "-", "_")
	if !dbIdentRe.MatchString(dbName) {
		return "", fmt.Errorf("%w: invalid database identifier %q", ErrOutOfScope, dbName)
	}
	return dbName, nil
}

// IAMUserForServiceAccount returns the Cloud SQL PostgreSQL `CLOUD_IAM_SERVICE_ACCOUNT`
// role name (`<sa-email-without-.gserviceaccount.com>`).
func IAMUserForServiceAccount(saEmail string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(saEmail)), ".gserviceaccount.com")
}

// SecretPrefixForEntry returns the trailing-hyphen-terminated Secret Manager prefix
// `<secret_prefix><entryName>-` (e.g. `grain-inst-alice-`), ensuring prefix conditions
// for `alice` never match `alice-dev`.
func (r *GCPInstanceResources) SecretPrefixForEntry(entryName string) (string, error) {
	entryName = strings.ToLower(strings.TrimSpace(entryName))
	if err := api.ValidateEntryName(entryName); err != nil {
		return "", fmt.Errorf("%w: invalid entry name %q: %v", ErrOutOfScope, entryName, err)
	}
	return r.secretPrefix + entryName + "-", nil
}

// Provision idempotently creates and configures:
//  1. Per-instance GCP Service Account (`<sa_prefix><slug>@<project>.iam.gserviceaccount.com`)
//  2. Shared Cloud SQL placement, database (`grain_<slug>`), and `CLOUD_IAM_SERVICE_ACCOUNT` role
//     with `NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS CONNECTION LIMIT 20` and `REVOKE ALL ON DATABASE ... FROM PUBLIC`.
//     With `instance_sql.cloud_sql_admin: false` the role is a built-in password user instead: a fresh random
//     password is written to Secret Manager as `<secret_prefix><name>-db-password` (returned as
//     DatabasePasswordSecret) and never placed in the returned bundle, so it cannot reach GCE metadata.
//  3. GCS Managed Folder `instances/<name>/` with `roles/storage.objectUser`
//  4. Conditional project IAM bindings for Cloud SQL, Secret Manager (`<secret_prefix><name>-*`), and optional fleet logs.
func (r *GCPInstanceResources) Provision(ctx context.Context, spec hub.InstanceResourceSpec) (*hub.InstanceResourceBundle, error) {
	accountID, saEmail, err := r.ServiceAccountEmailForEntry(spec.Name)
	if err != nil {
		return nil, err
	}
	dbName, err := DatabaseNameForEntry(spec.Name)
	if err != nil {
		return nil, err
	}
	secPrefix, err := r.SecretPrefixForEntry(spec.Name)
	if err != nil {
		return nil, err
	}

	sqlInstName, sqlConnName, sqlPrivateIP, allocatedDB, err := r.resolveSQLInstance(ctx, spec)
	if err != nil {
		return nil, err
	}
	if allocatedDB != "" {
		dbName = allocatedDB
	}

	// 1. Ensure per-instance GCP Service Account.
	if err := r.ensureServiceAccount(ctx, accountID, saEmail, spec.Name); err != nil {
		return nil, err
	}
	// 1b. Let the registry attach that account to the VM it is about to create
	// (GCE requires iam.serviceAccounts.actAs on it). The binding lives on the
	// account itself, so it is deleted along with the account at final purge.
	if r.grantSelfActAs {
		if err := r.ensureSelfActAs(ctx, saEmail); err != nil {
			return nil, err
		}
	}

	// 2. Provision shared SQL user + database + PostgreSQL ACLs (`REVOKE ... FROM PUBLIC`, `CONNECTION LIMIT 20`).
	iamUser := IAMUserForServiceAccount(saEmail)
	var dbUser, dbPassword, dbPasswordSecret string
	if r.cloudSQLAdmin {
		if err := r.ensureCloudSQLUserAndDB(ctx, sqlInstName, dbName, iamUser); err != nil {
			return nil, err
		}
	} else {
		var pwBytes [24]byte
		if _, err := rand.Read(pwBytes[:]); err != nil {
			return nil, fmt.Errorf("gcp resources: generating database password: %w", err)
		}
		dbUser = iamUser
		dbPassword = hex.EncodeToString(pwBytes[:])
		// The password goes to Secret Manager under the instance's prefix,
		// never into the bundle the VM's grain-config metadata is built
		// from: instance metadata is readable by anyone holding
		// compute.instances.get. Store it before the role's password is
		// changed so a failed write leaves the running role untouched.
		dbPasswordSecret, err = r.PutSecret(ctx, spec.Name, hub.DatabasePasswordSecretSuffix, []byte(dbPassword))
		if err != nil {
			return nil, fmt.Errorf("gcp resources: storing database password: %w", err)
		}
		if !r.bindProjectIAM {
			// Without the project-level prefix bindings the VM's service
			// account could not read the secret; grant it on the secret.
			if err := r.grantSecretAccessor(ctx, dbPasswordSecret, saEmail); err != nil {
				return nil, err
			}
		}
	}
	if err := r.provisionPostgresACLs(ctx, sqlInstName, dbName, iamUser, dbPassword); err != nil {
		return nil, err
	}

	// 3. Provision GCS Managed Folder `instances/<name>/` (when `gcs_bucket` is configured).
	var managedFolder string
	if r.gcsBucket != "" {
		managedFolder = fmt.Sprintf("instances/%s/", spec.Name)
		if err := r.ensureGCSManagedFolder(ctx, r.gcsBucket, managedFolder, saEmail); err != nil {
			return nil, err
		}
	}

	// 4. Configure conditional IAM bindings on the project (Secret Manager prefix, Cloud SQL instance, optional fleet logs).
	if r.bindProjectIAM {
		enableLogs := r.fleetLogs || spec.ShipLogs
		if err := r.ensureProjectIAMBindings(ctx, spec.Name, saEmail, sqlInstName, secPrefix, enableLogs); err != nil {
			return nil, err
		}
	}

	return &hub.InstanceResourceBundle{
		ServiceAccountEmail:    saEmail,
		SQLInstance:            sqlInstName,
		SQLConnectionName:      sqlConnName,
		SQLPrivateIP:           sqlPrivateIP,
		DatabaseName:           dbName,
		DatabaseUser:           dbUser,
		DatabasePasswordSecret: dbPasswordSecret,
		SecretPrefix:           secPrefix,
		GCSBucket:              r.gcsBucket,
		GCSManagedFolder:       managedFolder,
	}, nil
}

// Teardown revokes access or permanently purges per-instance cloud resources:
//   - When `retainDB == true` (Fence on Delete during the 7-day retention window):
//     revokes project and GCS Managed Folder IAM bindings for the GSA, sets
//     `ALTER DATABASE "<db>" WITH ALLOW_CONNECTIONS false`, sets `ALTER ROLE "<iam_user>" WITH NOLOGIN CONNECTION LIMIT 0`,
//     and terminates all active PostgreSQL backends via `pg_terminate_backend`.
//   - When `retainDB == false` (Final Purge after `db_purge_after` or `delete_disk: true`):
//     drops the PostgreSQL database and IAM role, deletes the Cloud SQL user/DB via Admin API,
//     deletes all `grain-inst-<name>-*` secrets from Secret Manager, deletes all objects and
//     the Managed Folder under `gs://<bucket>/instances/<name>/`, and deletes the GSA.
func (r *GCPInstanceResources) Teardown(ctx context.Context, spec hub.InstanceResourceSpec, bundle hub.InstanceResourceBundle, retainDB bool) error {
	_, expectedSA, err := r.ServiceAccountEmailForEntry(spec.Name)
	if err != nil {
		return err
	}
	saEmail := strings.TrimSpace(bundle.ServiceAccountEmail)
	if saEmail == "" {
		saEmail = expectedSA
	}
	if !strings.HasPrefix(strings.ToLower(saEmail), strings.ToLower(r.saPrefix)) {
		return fmt.Errorf("%w: service account %q does not match sa_prefix %q", ErrOutOfScope, saEmail, r.saPrefix)
	}

	dbName := strings.TrimSpace(bundle.DatabaseName)
	if dbName == "" {
		dbName, err = DatabaseNameForEntry(spec.Name)
		if err != nil {
			return err
		}
	}
	if !dbIdentRe.MatchString(dbName) {
		return fmt.Errorf("%w: database %q does not match grain_* scope", ErrOutOfScope, dbName)
	}

	secPrefix := strings.TrimSpace(bundle.SecretPrefix)
	if secPrefix == "" {
		secPrefix, err = r.SecretPrefixForEntry(spec.Name)
		if err != nil {
			return err
		}
	}
	expectedSecPrefix, err := r.SecretPrefixForEntry(spec.Name)
	if err != nil {
		return err
	}
	if secPrefix != expectedSecPrefix {
		return fmt.Errorf("%w: secret prefix %q does not match expected %q", ErrOutOfScope, secPrefix, expectedSecPrefix)
	}

	sqlInstName := strings.TrimSpace(bundle.SQLInstance)
	if sqlInstName == "" {
		sqlInstName = strings.TrimSpace(spec.SQLInstance)
	}
	iamUser := IAMUserForServiceAccount(saEmail)

	// Remove project-level IAM bindings for the GSA first when configured.
	if r.bindProjectIAM {
		if err := r.removeProjectIAMBindings(ctx, saEmail); err != nil {
			return err
		}
	}

	if retainDB {
		// Fence on Delete: keep database, GCS objects, secrets, and GSA intact for restore,
		// but revoke access and terminate all active database connections immediately.
		if r.gcsBucket != "" {
			folder := fmt.Sprintf("instances/%s/", spec.Name)
			if err := r.revokeGCSManagedFolderIAM(ctx, r.gcsBucket, folder, saEmail); err != nil {
				return err
			}
		}
		if sqlInstName != "" {
			if err := r.fencePostgresDatabase(ctx, sqlInstName, dbName, iamUser); err != nil {
				return err
			}
		}
		return nil
	}

	// Final Purge (`retainDB == false`): drop database + role, delete secrets, delete GCS folder, delete GSA.
	if sqlInstName != "" {
		if err := r.purgePostgresDatabase(ctx, sqlInstName, dbName, iamUser); err != nil {
			return err
		}
		if r.cloudSQLAdmin {
			if err := r.deleteCloudSQLUserAndDB(ctx, sqlInstName, dbName, iamUser); err != nil {
				return err
			}
		}
	}
	if err := r.purgePrefixedSecrets(ctx, spec.Name, secPrefix); err != nil {
		return err
	}
	if r.gcsBucket != "" {
		folder := fmt.Sprintf("instances/%s/", spec.Name)
		if err := r.purgeGCSManagedFolder(ctx, r.gcsBucket, folder); err != nil {
			return err
		}
	}
	if err := r.deleteServiceAccount(ctx, saEmail); err != nil {
		return err
	}
	return nil
}

// PutSecret creates or updates a Secret Manager secret named `<secret_prefix><entryName>-<secretSuffix>`
// (for example `grain-inst-alice-config-repo-key`) and adds a new secret version with `payload`.
func (r *GCPInstanceResources) PutSecret(ctx context.Context, entryName, secretSuffix string, payload []byte) (string, error) {
	secPrefix, err := r.SecretPrefixForEntry(entryName)
	if err != nil {
		return "", err
	}
	secretSuffix = strings.Trim(strings.ToLower(strings.TrimSpace(secretSuffix)), "-")
	if secretSuffix == "" {
		return "", errors.New("gcp resources: secretSuffix is required")
	}
	secretID := secPrefix + secretSuffix
	fullSecretName := fmt.Sprintf("projects/%s/secrets/%s", r.secretsProject, secretID)

	createURL := fmt.Sprintf("%s/v1/projects/%s/secrets?secretId=%s",
		r.secretBaseURL, url.PathEscape(r.secretsProject), url.QueryEscape(secretID))
	createBody := map[string]any{
		"replication": map[string]any{
			"automatic": map[string]any{},
		},
		"labels": map[string]string{
			"grain-entry": entryName,
		},
	}
	if err := r.doJSONRequest(ctx, http.MethodPost, createURL, createBody, nil, http.StatusConflict); err != nil {
		return "", fmt.Errorf("gcp secretmanager: creating secret %s: %w", secretID, err)
	}

	verURL := fmt.Sprintf("%s/v1/projects/%s/secrets/%s:addVersion",
		r.secretBaseURL, url.PathEscape(r.secretsProject), url.PathEscape(secretID))
	verBody := map[string]any{
		"payload": map[string]string{
			"data": base64.StdEncoding.EncodeToString(payload),
		},
	}
	if err := r.doJSONRequest(ctx, http.MethodPost, verURL, verBody, nil); err != nil {
		return "", fmt.Errorf("gcp secretmanager: adding version to %s: %w", secretID, err)
	}
	return fullSecretName, nil
}

// grantSecretAccessor sets `secretName`'s own IAM policy so `saEmail` holds
// roles/secretmanager.secretAccessor on it. It is used when
// `compute.bind_project_iam: false` leaves the instance's service account
// without the project-level Secret Manager prefix bindings. The secret is
// registry-owned and per-instance, so its policy names only that account.
func (r *GCPInstanceResources) grantSecretAccessor(ctx context.Context, secretName, saEmail string) error {
	wantPrefix := fmt.Sprintf("projects/%s/secrets/%s", r.secretsProject, r.secretPrefix)
	if !strings.HasPrefix(secretName, wantPrefix) {
		return fmt.Errorf("%w: secret %q is outside %q", ErrOutOfScope, secretName, wantPrefix)
	}
	secretID := strings.TrimPrefix(secretName, fmt.Sprintf("projects/%s/secrets/", r.secretsProject))
	setURL := fmt.Sprintf("%s/v1/projects/%s/secrets/%s:setIamPolicy",
		r.secretBaseURL, url.PathEscape(r.secretsProject), url.PathEscape(secretID))
	body := map[string]any{
		"policy": map[string]any{
			"bindings": []map[string]any{{
				"role":    "roles/secretmanager.secretAccessor",
				"members": []string{"serviceAccount:" + saEmail},
			}},
		},
	}
	if err := r.doJSONRequest(ctx, http.MethodPost, setURL, body, nil); err != nil {
		return fmt.Errorf("gcp secretmanager: granting %s access to %s: %w", saEmail, secretID, err)
	}
	return nil
}

// GrantSSH grants time-bounded break-glass OS Login and IAP tunnel access (`roles/compute.osAdminLogin`,
// `roles/iap.tunnelResourceAccessor`) on `instanceID` in `zone` to `user:<userEmail>`.
func (r *GCPInstanceResources) GrantSSH(ctx context.Context, zone, instanceID, userEmail string) error {
	if !r.sshBreakGlass {
		return ErrSSHBreakGlassDisabled
	}
	if err := r.checkSSHScope(zone, instanceID); err != nil {
		return err
	}
	userEmail = strings.ToLower(strings.TrimSpace(userEmail))
	if userEmail == "" || !strings.Contains(userEmail, "@") {
		return errors.New("gcp resources: valid userEmail is required for GrantSSH")
	}
	member := "user:" + userEmail
	expiresAt := r.nowFn().UTC().Add(DefaultSSHGrantTTL).Format(time.RFC3339)
	cond := &iamCondition{
		Title:       "grain-break-glass-ssh",
		Description: fmt.Sprintf("Time-bounded break-glass SSH for %s on %s", userEmail, instanceID),
		Expression:  fmt.Sprintf("request.time < timestamp(%q)", expiresAt),
	}

	policy, err := r.getInstanceIAMPolicy(ctx, zone, instanceID)
	if err != nil {
		return err
	}
	policy.upsertConditionalBinding("roles/compute.osAdminLogin", member, cond)
	policy.upsertConditionalBinding("roles/iap.tunnelResourceAccessor", member, cond)
	return r.setInstanceIAMPolicy(ctx, zone, instanceID, policy)
}

// RevokeSSH removes break-glass OS Login and IAP tunnel bindings for `user:<userEmail>` on `instanceID`.
func (r *GCPInstanceResources) RevokeSSH(ctx context.Context, zone, instanceID, userEmail string) error {
	if err := r.checkSSHScope(zone, instanceID); err != nil {
		return err
	}
	userEmail = strings.ToLower(strings.TrimSpace(userEmail))
	member := "user:" + userEmail
	policy, err := r.getInstanceIAMPolicy(ctx, zone, instanceID)
	if err != nil {
		return err
	}
	policy.removeMemberBindings(member)
	return r.setInstanceIAMPolicy(ctx, zone, instanceID, policy)
}

func (r *GCPInstanceResources) checkSSHScope(zone, instanceID string) error {
	zone = strings.TrimSpace(zone)
	if len(r.zoneSet) > 0 && !r.zoneSet[zone] {
		return fmt.Errorf("%w: zone %q is not in configured zones", ErrOutOfScope, zone)
	}
	instanceID = strings.TrimSpace(instanceID)
	if !strings.HasPrefix(instanceID, r.vmPrefix) {
		return fmt.Errorf("%w: instance %q does not have required vm_prefix %q", ErrOutOfScope, instanceID, r.vmPrefix)
	}
	return nil
}

func (r *GCPInstanceResources) resolveSQLInstance(ctx context.Context, spec hub.InstanceResourceSpec) (name, connName, privateIP, dbName string, err error) {
	if r.sqlAllocator != nil {
		row, allocatedDB, allocErr := r.sqlAllocator.AllocateSQLInstance(ctx, spec.EntryID, spec.Name)
		if allocErr != nil {
			return "", "", "", "", allocErr
		}
		return row.Name, row.ConnectionName, row.PrivateIP, allocatedDB, nil
	}
	target := strings.TrimSpace(spec.SQLInstance)
	if target != "" {
		if sq, ok := r.sqlConfigs[target]; ok {
			return sq.Name, sq.ConnectionName, sq.PrivateIP, "", nil
		}
		return target, "", "", "", nil
	}
	if len(r.cfg.InstanceSQL.SQLInstances) > 0 {
		sq := r.cfg.InstanceSQL.SQLInstances[0]
		return sq.Name, sq.ConnectionName, sq.PrivateIP, "", nil
	}
	return "", "", "", "", errors.New("gcp resources: no sql_instances configured")
}

func (r *GCPInstanceResources) ensureServiceAccount(ctx context.Context, accountID, saEmail, entryName string) error {
	if !strings.HasPrefix(accountID, r.saPrefix) {
		return fmt.Errorf("%w: service account ID %q does not start with %q", ErrOutOfScope, accountID, r.saPrefix)
	}
	getURL := fmt.Sprintf("%s/v1/projects/%s/serviceAccounts/%s",
		r.iamBaseURL, url.PathEscape(r.project), url.PathEscape(saEmail))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return err
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gcp iam: GET serviceAccount %s: %w", saEmail, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("gcp iam: GET serviceAccount %s returned HTTP %d", saEmail, resp.StatusCode)
	}

	createURL := fmt.Sprintf("%s/v1/projects/%s/serviceAccounts",
		r.iamBaseURL, url.PathEscape(r.project))
	payload := map[string]any{
		"accountId": accountID,
		"serviceAccount": map[string]string{
			"displayName": "grain instance " + entryName,
			"description": "grain-registry managed service account for entry:" + entryName,
		},
	}
	if err := r.doJSONRequest(ctx, http.MethodPost, createURL, payload, nil, http.StatusConflict); err != nil {
		return fmt.Errorf("gcp iam: creating serviceAccount %s: %w", saEmail, err)
	}
	return nil
}

func (r *GCPInstanceResources) deleteServiceAccount(ctx context.Context, saEmail string) error {
	if !strings.HasPrefix(strings.ToLower(saEmail), strings.ToLower(r.saPrefix)) {
		return fmt.Errorf("%w: refusing to delete service account %q outside sa_prefix %q", ErrOutOfScope, saEmail, r.saPrefix)
	}
	delURL := fmt.Sprintf("%s/v1/projects/%s/serviceAccounts/%s",
		r.iamBaseURL, url.PathEscape(r.project), url.PathEscape(saEmail))
	return r.doJSONRequest(ctx, http.MethodDelete, delURL, nil, nil, http.StatusNotFound)
}

func (r *GCPInstanceResources) ensureCloudSQLUserAndDB(ctx context.Context, sqlInstance, dbName, iamUser string) error {
	if sqlInstance == "" {
		return nil
	}
	if !dbIdentRe.MatchString(dbName) {
		return fmt.Errorf("%w: invalid database name %q", ErrOutOfScope, dbName)
	}
	usersURL := fmt.Sprintf("%s/sql/v1beta4/projects/%s/instances/%s/users",
		r.sqlAdminBaseURL, url.PathEscape(r.project), url.PathEscape(sqlInstance))
	userBody := map[string]any{
		"project":  r.project,
		"instance": sqlInstance,
		"name":     iamUser,
		"type":     "CLOUD_IAM_SERVICE_ACCOUNT",
	}
	if err := r.doJSONRequest(ctx, http.MethodPost, usersURL, userBody, nil, http.StatusConflict); err != nil {
		return fmt.Errorf("gcp sqladmin: creating IAM user %s on %s: %w", iamUser, sqlInstance, err)
	}

	dbsURL := fmt.Sprintf("%s/sql/v1beta4/projects/%s/instances/%s/databases",
		r.sqlAdminBaseURL, url.PathEscape(r.project), url.PathEscape(sqlInstance))
	dbBody := map[string]any{
		"project":   r.project,
		"instance":  sqlInstance,
		"name":      dbName,
		"charset":   "UTF8",
		"collation": "en_US.UTF8",
	}
	if err := r.doJSONRequest(ctx, http.MethodPost, dbsURL, dbBody, nil, http.StatusConflict); err != nil {
		return fmt.Errorf("gcp sqladmin: creating database %s on %s: %w", dbName, sqlInstance, err)
	}
	return nil
}

func (r *GCPInstanceResources) deleteCloudSQLUserAndDB(ctx context.Context, sqlInstance, dbName, iamUser string) error {
	if sqlInstance == "" {
		return nil
	}
	if !dbIdentRe.MatchString(dbName) {
		return fmt.Errorf("%w: refusing to delete non-grain database %q", ErrOutOfScope, dbName)
	}
	delDBURL := fmt.Sprintf("%s/sql/v1beta4/projects/%s/instances/%s/databases/%s",
		r.sqlAdminBaseURL, url.PathEscape(r.project), url.PathEscape(sqlInstance), url.PathEscape(dbName))
	if err := r.doJSONRequest(ctx, http.MethodDelete, delDBURL, nil, nil, http.StatusNotFound); err != nil {
		return fmt.Errorf("gcp sqladmin: deleting database %s on %s: %w", dbName, sqlInstance, err)
	}

	delUserURL := fmt.Sprintf("%s/sql/v1beta4/projects/%s/instances/%s/users?name=%s",
		r.sqlAdminBaseURL, url.PathEscape(r.project), url.PathEscape(sqlInstance), url.QueryEscape(iamUser))
	if err := r.doJSONRequest(ctx, http.MethodDelete, delUserURL, nil, nil, http.StatusNotFound); err != nil {
		return fmt.Errorf("gcp sqladmin: deleting IAM user %s on %s: %w", iamUser, sqlInstance, err)
	}
	return nil
}

// provisionPostgresACLs connects to the target Cloud SQL PostgreSQL instance (when `sqlOpener` is configured)
// and enforces strict database-level isolation:
// - `CREATE ROLE "<iam_user>" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS CONNECTION LIMIT 20`
// - `CREATE DATABASE "<db_name>"`
// - `ALTER DATABASE "<db_name>" WITH ALLOW_CONNECTIONS true CONNECTION LIMIT 20`
// - `REVOKE ALL ON DATABASE "<db_name>" FROM PUBLIC`
// - `REVOKE CONNECT ON DATABASE postgres FROM PUBLIC`
// - `GRANT CONNECT, TEMPORARY ON DATABASE "<db_name>" TO "<iam_user>"`
// - Schema `public` ownership and privilege isolation inside `<db_name>`.
func (r *GCPInstanceResources) provisionPostgresACLs(ctx context.Context, sqlInstance, dbName, iamUser, dbPassword string) error {
	if r.sqlOpener == nil || sqlInstance == "" {
		return nil
	}
	if !dbIdentRe.MatchString(dbName) {
		return fmt.Errorf("%w: invalid database name %q", ErrOutOfScope, dbName)
	}
	adminDB, err := r.sqlOpener(ctx, sqlInstance, "postgres")
	if err != nil {
		return fmt.Errorf("gcp sql: opening postgres admin connection on %s: %w", sqlInstance, err)
	}
	defer adminDB.Close()

	quotedRole := quotePGIdent(iamUser)
	quotedDB := quotePGIdent(dbName)
	pwClause := ""
	if dbPassword != "" {
		pwClause = fmt.Sprintf(" PASSWORD '%s'", strings.ReplaceAll(dbPassword, "'", "''"))
	}

	var roleExists bool
	if err := adminDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, iamUser).Scan(&roleExists); err != nil {
		return fmt.Errorf("gcp sql: checking role %s: %w", iamUser, err)
	}
	if !roleExists {
		createRoleSQL := fmt.Sprintf(
			`CREATE ROLE %s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT %d%s`,
			quotedRole, DefaultRoleConnectionLimit, pwClause)
		if _, err := adminDB.ExecContext(ctx, createRoleSQL); err != nil {
			return fmt.Errorf("gcp sql: creating role %s: %w", iamUser, err)
		}
	} else {
		// A re-provision (a retried create, a replace) finds the role already
		// there. The admin is normally not a superuser -- Cloud SQL never grants
		// one -- and PostgreSQL refuses to let a non-superuser name SUPERUSER,
		// BYPASSRLS or REPLICATION in ALTER ROLE at all, even to turn them off.
		// So check those attributes instead of restating them: a role carrying
		// any of them was not made by this provisioner and is left alone.
		var rolSuper, rolBypassRLS, rolReplication bool
		if err := adminDB.QueryRowContext(ctx,
			`SELECT rolsuper, rolbypassrls, rolreplication FROM pg_roles WHERE rolname = $1`, iamUser,
		).Scan(&rolSuper, &rolBypassRLS, &rolReplication); err != nil {
			return fmt.Errorf("gcp sql: checking attributes of role %s: %w", iamUser, err)
		}
		if rolSuper || rolBypassRLS || rolReplication {
			return fmt.Errorf("%w: existing role %s has SUPERUSER, BYPASSRLS or REPLICATION; refusing to reuse it", ErrOutOfScope, iamUser)
		}
		alterRoleSQL := fmt.Sprintf(
			`ALTER ROLE %s WITH LOGIN NOCREATEDB NOCREATEROLE CONNECTION LIMIT %d%s`,
			quotedRole, DefaultRoleConnectionLimit, pwClause)
		if _, err := adminDB.ExecContext(ctx, alterRoleSQL); err != nil {
			return fmt.Errorf("gcp sql: altering role %s: %w", iamUser, err)
		}
	}

	var dbExists bool
	if err := adminDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&dbExists); err != nil {
		return fmt.Errorf("gcp sql: checking database %s: %w", dbName, err)
	}
	if !dbExists {
		// CREATE DATABASE cannot run inside a transaction block.
		if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s ENCODING 'UTF8'`, quotedDB)); err != nil {
			return fmt.Errorf("gcp sql: creating database %s: %w", dbName, err)
		}
	}

	aclStatements := []string{
		fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS true CONNECTION LIMIT %d`, quotedDB, DefaultRoleConnectionLimit),
		fmt.Sprintf(`REVOKE ALL ON DATABASE %s FROM PUBLIC`, quotedDB),
		`REVOKE CONNECT ON DATABASE postgres FROM PUBLIC`,
		fmt.Sprintf(`GRANT CONNECT, TEMPORARY ON DATABASE %s TO %s`, quotedDB, quotedRole),
	}
	for _, stmt := range aclStatements {
		if _, err := adminDB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("gcp sql: executing %q: %w", stmt, err)
		}
	}

	// Connect to `<db_name>` to lock down `public` schema and grant privileges to `<iam_user>`.
	instDB, err := r.sqlOpener(ctx, sqlInstance, dbName)
	if err != nil {
		return fmt.Errorf("gcp sql: opening instance database %s on %s: %w", dbName, sqlInstance, err)
	}
	defer instDB.Close()

	schemaStatements := []string{
		`REVOKE ALL ON SCHEMA public FROM PUBLIC`,
		fmt.Sprintf(`GRANT USAGE, CREATE ON SCHEMA public TO %s`, quotedRole),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO %s`, quotedRole),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO %s`, quotedRole),
	}
	for _, stmt := range schemaStatements {
		if _, err := instDB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("gcp sql: executing schema ACL on %s (%q): %w", dbName, stmt, err)
		}
	}

	// Grant on the existing tables and sequences someone other than the
	// instance role owns (a restored dump, anything the admin created). This
	// replaces GRANT ... ON ALL TABLES/SEQUENCES IN SCHEMA public: on a
	// re-provision the schema also holds everything the instance's daemon
	// created, which the role owns and needs no grant on, and a non-superuser
	// admin (Cloud SQL's) may not grant on another role's objects at all, so the
	// blanket form failed every re-provision once the daemon had run.
	rows, err := instDB.QueryContext(ctx, `
SELECT c.relname, c.relkind = 'S'
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
  AND pg_get_userbyid(c.relowner) <> $1
ORDER BY c.relname`, iamUser)
	if err != nil {
		return fmt.Errorf("gcp sql: listing objects to grant on in %s: %w", dbName, err)
	}
	var grants []string
	for rows.Next() {
		var name string
		var isSeq bool
		if err := rows.Scan(&name, &isSeq); err != nil {
			rows.Close()
			return fmt.Errorf("gcp sql: listing objects to grant on in %s: %w", dbName, err)
		}
		kind := "TABLE"
		if isSeq {
			kind = "SEQUENCE"
		}
		grants = append(grants, fmt.Sprintf(`GRANT ALL PRIVILEGES ON %s public.%s TO %s`, kind, quotePGIdent(name), quotedRole))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("gcp sql: listing objects to grant on in %s: %w", dbName, err)
	}
	rows.Close()
	for _, stmt := range grants {
		if _, err := instDB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("gcp sql: executing schema ACL on %s (%q): %w", dbName, stmt, err)
		}
	}
	return nil
}

// fencePostgresDatabase disables connections to `dbName`, sets `NOLOGIN CONNECTION LIMIT 0`
// on `iamUser`, revokes `CONNECT` privilege, and terminates all active backends connected to `dbName`.
func (r *GCPInstanceResources) fencePostgresDatabase(ctx context.Context, sqlInstance, dbName, iamUser string) error {
	if r.sqlOpener == nil || sqlInstance == "" {
		return nil
	}
	if !dbIdentRe.MatchString(dbName) {
		return fmt.Errorf("%w: invalid database name %q", ErrOutOfScope, dbName)
	}
	adminDB, err := r.sqlOpener(ctx, sqlInstance, "postgres")
	if err != nil {
		return fmt.Errorf("gcp sql: opening postgres admin connection for fence on %s: %w", sqlInstance, err)
	}
	defer adminDB.Close()

	quotedRole := quotePGIdent(iamUser)
	quotedDB := quotePGIdent(dbName)

	var roleExists bool
	if err := adminDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, iamUser).Scan(&roleExists); err == nil && roleExists {
		if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`ALTER ROLE %s WITH NOLOGIN CONNECTION LIMIT 0`, quotedRole)); err != nil {
			return fmt.Errorf("gcp sql: fencing role %s: %w", iamUser, err)
		}
	}

	var dbExists bool
	if err := adminDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&dbExists); err == nil && dbExists {
		if roleExists {
			_, _ = adminDB.ExecContext(ctx, fmt.Sprintf(`REVOKE CONNECT, TEMPORARY ON DATABASE %s FROM %s`, quotedDB, quotedRole))
		}
		if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS false`, quotedDB)); err != nil {
			return fmt.Errorf("gcp sql: setting ALLOW_CONNECTIONS false on %s: %w", dbName, err)
		}
		_, _ = adminDB.ExecContext(ctx, `
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
	}
	return nil
}

// purgePostgresDatabase terminates active backends and drops `dbName` and `iamUser`.
func (r *GCPInstanceResources) purgePostgresDatabase(ctx context.Context, sqlInstance, dbName, iamUser string) error {
	if r.sqlOpener == nil || sqlInstance == "" {
		return nil
	}
	if !dbIdentRe.MatchString(dbName) {
		return fmt.Errorf("%w: refusing to drop non-grain database %q", ErrOutOfScope, dbName)
	}
	adminDB, err := r.sqlOpener(ctx, sqlInstance, "postgres")
	if err != nil {
		return fmt.Errorf("gcp sql: opening postgres admin connection for purge on %s: %w", sqlInstance, err)
	}
	defer adminDB.Close()

	quotedRole := quotePGIdent(iamUser)
	quotedDB := quotePGIdent(dbName)

	var dbExists bool
	if err := adminDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&dbExists); err == nil && dbExists {
		_, _ = adminDB.ExecContext(ctx, fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS false`, quotedDB))
		_, _ = adminDB.ExecContext(ctx, `
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
		if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, quotedDB)); err != nil {
			return fmt.Errorf("gcp sql: dropping database %s: %w", dbName, err)
		}
	}

	var roleExists bool
	if err := adminDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, iamUser).Scan(&roleExists); err == nil && roleExists {
		_, _ = adminDB.ExecContext(ctx, fmt.Sprintf(`REVOKE ALL ON DATABASE postgres FROM %s`, quotedRole))
		if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, quotedRole)); err != nil {
			return fmt.Errorf("gcp sql: dropping role %s: %w", iamUser, err)
		}
	}
	return nil
}

func quotePGIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func (r *GCPInstanceResources) ensureGCSManagedFolder(ctx context.Context, bucket, folder, saEmail string) error {
	if !strings.HasPrefix(folder, "instances/") || !strings.HasSuffix(folder, "/") {
		return fmt.Errorf("%w: GCS managed folder %q must match instances/<name>/", ErrOutOfScope, folder)
	}
	createURL := fmt.Sprintf("%s/storage/v1/b/%s/managedFolders",
		r.storageBaseURL, url.PathEscape(bucket))
	if err := r.doJSONRequest(ctx, http.MethodPost, createURL, map[string]string{"name": folder}, nil, http.StatusConflict); err != nil {
		return fmt.Errorf("gcp storage: creating managedFolder %s in %s: %w", folder, bucket, err)
	}

	iamURL := fmt.Sprintf("%s/storage/v1/b/%s/managedFolders/%s/iam",
		r.storageBaseURL, url.PathEscape(bucket), url.PathEscape(folder))
	var policy iamPolicy
	if err := r.doJSONRequest(ctx, http.MethodGet, iamURL, nil, &policy, http.StatusNotFound); err != nil {
		return fmt.Errorf("gcp storage: getting IAM policy on managedFolder %s: %w", folder, err)
	}
	policy.Bindings = []iamBinding{
		{
			Role:    "roles/storage.objectUser",
			Members: []string{"serviceAccount:" + saEmail},
		},
	}
	if err := r.doJSONRequest(ctx, http.MethodPut, iamURL, &policy, nil); err != nil {
		return fmt.Errorf("gcp storage: setting IAM policy on managedFolder %s: %w", folder, err)
	}
	return nil
}

func (r *GCPInstanceResources) revokeGCSManagedFolderIAM(ctx context.Context, bucket, folder, saEmail string) error {
	if !strings.HasPrefix(folder, "instances/") {
		return fmt.Errorf("%w: GCS managed folder %q must match instances/<name>/", ErrOutOfScope, folder)
	}
	iamURL := fmt.Sprintf("%s/storage/v1/b/%s/managedFolders/%s/iam",
		r.storageBaseURL, url.PathEscape(bucket), url.PathEscape(folder))
	var policy iamPolicy
	if err := r.doJSONRequest(ctx, http.MethodGet, iamURL, nil, &policy, http.StatusNotFound); err != nil {
		return err
	}
	policy.removeMemberBindings("serviceAccount:" + saEmail)
	return r.doJSONRequest(ctx, http.MethodPut, iamURL, &policy, nil, http.StatusNotFound)
}

func (r *GCPInstanceResources) purgeGCSManagedFolder(ctx context.Context, bucket, folder string) error {
	if !strings.HasPrefix(folder, "instances/") || !strings.HasSuffix(folder, "/") {
		return fmt.Errorf("%w: refusing to delete GCS folder %q outside instances/<name>/", ErrOutOfScope, folder)
	}
	listURL := fmt.Sprintf("%s/storage/v1/b/%s/o?prefix=%s",
		r.storageBaseURL, url.PathEscape(bucket), url.QueryEscape(folder))
	var objList struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := r.doJSONRequest(ctx, http.MethodGet, listURL, nil, &objList, http.StatusNotFound); err != nil {
		return fmt.Errorf("gcp storage: listing objects under %s: %w", folder, err)
	}
	for _, obj := range objList.Items {
		if !strings.HasPrefix(obj.Name, folder) {
			continue
		}
		delObjURL := fmt.Sprintf("%s/storage/v1/b/%s/o/%s",
			r.storageBaseURL, url.PathEscape(bucket), url.PathEscape(obj.Name))
		if err := r.doJSONRequest(ctx, http.MethodDelete, delObjURL, nil, nil, http.StatusNotFound); err != nil {
			return fmt.Errorf("gcp storage: deleting object %s: %w", obj.Name, err)
		}
	}

	delFolderURL := fmt.Sprintf("%s/storage/v1/b/%s/managedFolders/%s?allowNonEmpty=true",
		r.storageBaseURL, url.PathEscape(bucket), url.PathEscape(folder))
	if err := r.doJSONRequest(ctx, http.MethodDelete, delFolderURL, nil, nil, http.StatusNotFound); err != nil {
		return fmt.Errorf("gcp storage: deleting managedFolder %s: %w", folder, err)
	}
	return nil
}

func (r *GCPInstanceResources) purgePrefixedSecrets(ctx context.Context, entryName, secPrefix string) error {
	if !strings.HasPrefix(secPrefix, r.secretPrefix) || !strings.HasSuffix(secPrefix, "-") {
		return fmt.Errorf("%w: secret prefix %q must start with %q and end with '-'", ErrOutOfScope, secPrefix, r.secretPrefix)
	}
	listURL := fmt.Sprintf("%s/v1/projects/%s/secrets?filter=%s",
		r.secretBaseURL, url.PathEscape(r.secretsProject), url.QueryEscape("name:"+secPrefix))
	var listResp struct {
		Secrets []struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"secrets"`
	}
	if err := r.doJSONRequest(ctx, http.MethodGet, listURL, nil, &listResp, http.StatusNotFound); err != nil {
		return fmt.Errorf("gcp secretmanager: listing secrets with prefix %s: %w", secPrefix, err)
	}
	requiredPrefix := fmt.Sprintf("projects/%s/secrets/%s", r.secretsProject, secPrefix)
	for _, sec := range listResp.Secrets {
		shortID := sec.Name
		if idx := strings.LastIndex(sec.Name, "/secrets/"); idx >= 0 {
			shortID = sec.Name[idx+len("/secrets/"):]
		}
		if !strings.HasPrefix(sec.Name, requiredPrefix) && !strings.HasPrefix(shortID, secPrefix) {
			continue
		}
		if entryName != "" && sec.Labels != nil && sec.Labels["grain-entry"] != "" && sec.Labels["grain-entry"] != entryName {
			continue
		}
		delURL := fmt.Sprintf("%s/v1/projects/%s/secrets/%s",
			r.secretBaseURL, url.PathEscape(r.secretsProject), url.PathEscape(shortID))
		if err := r.doJSONRequest(ctx, http.MethodDelete, delURL, nil, nil, http.StatusNotFound); err != nil {
			return fmt.Errorf("gcp secretmanager: deleting secret %s: %w", shortID, err)
		}
	}
	return nil
}

type iamCondition struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Expression  string `json:"expression"`
}

type iamBinding struct {
	Role      string        `json:"role"`
	Members   []string      `json:"members"`
	Condition *iamCondition `json:"condition,omitempty"`
}

type iamPolicy struct {
	Version  int          `json:"version,omitempty"`
	Bindings []iamBinding `json:"bindings,omitempty"`
	Etag     string       `json:"etag,omitempty"`
}

func (p *iamPolicy) removeMemberBindings(member string) {
	if p == nil {
		return
	}
	filtered := make([]iamBinding, 0, len(p.Bindings))
	for _, b := range p.Bindings {
		var keep []string
		for _, m := range b.Members {
			if !strings.EqualFold(m, member) {
				keep = append(keep, m)
			}
		}
		if len(keep) > 0 {
			b.Members = keep
			filtered = append(filtered, b)
		}
	}
	p.Bindings = filtered
}

func (p *iamPolicy) upsertConditionalBinding(role, member string, cond *iamCondition) {
	if p.Version < 3 {
		p.Version = 3
	}
	for i := range p.Bindings {
		b := &p.Bindings[i]
		if b.Role != role {
			continue
		}
		if !conditionsEqual(b.Condition, cond) {
			continue
		}
		for _, m := range b.Members {
			if strings.EqualFold(m, member) {
				return
			}
		}
		b.Members = append(b.Members, member)
		return
	}
	p.Bindings = append(p.Bindings, iamBinding{
		Role:      role,
		Members:   []string{member},
		Condition: cond,
	})
}

func conditionsEqual(a, b *iamCondition) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Title == b.Title && a.Expression == b.Expression
}

func (r *GCPInstanceResources) ensureProjectIAMBindings(ctx context.Context, entryName, saEmail, sqlInstance, secPrefix string, enableFleetLogs bool) error {
	policy, err := r.getProjectIAMPolicy(ctx)
	if err != nil {
		return err
	}
	member := "serviceAccount:" + saEmail
	// Clean any prior bindings for this SA before applying current conditional bindings.
	policy.removeMemberBindings(member)

	// 1. Secret Manager prefix-scoped conditional bindings (`resource.name.startsWith("projects/<project>/secrets/<secret_prefix><name>-")`).
	secTargetPrefix := fmt.Sprintf("projects/%s/secrets/%s", r.secretsProject, secPrefix)
	secCond := &iamCondition{
		Title:       "grain-secret-prefix-" + entryName,
		Description: fmt.Sprintf("Restricts %s to Secret Manager prefix %s", saEmail, secPrefix),
		Expression:  fmt.Sprintf("resource.name.startsWith(%q)", secTargetPrefix),
	}
	policy.upsertConditionalBinding("roles/secretmanager.secretAccessor", member, secCond)
	policy.upsertConditionalBinding("roles/secretmanager.secretVersionManager", member, secCond)

	// 2. Cloud SQL instance-scoped conditional bindings.
	if sqlInstance != "" {
		sqlResource := fmt.Sprintf("projects/%s/instances/%s", r.project, sqlInstance)
		sqlCond := &iamCondition{
			Title:       "grain-sql-instance-" + entryName,
			Description: fmt.Sprintf("Restricts %s to Cloud SQL instance %s", saEmail, sqlInstance),
			Expression:  fmt.Sprintf("resource.name == %q", sqlResource),
		}
		policy.upsertConditionalBinding("roles/cloudsql.client", member, sqlCond)
		policy.upsertConditionalBinding("roles/cloudsql.instanceUser", member, sqlCond)
	}

	// 3. Optional fleet logs (`roles/logging.logWriter` and `roles/monitoring.metricWriter` only when `fleet_logs: true`).
	if enableFleetLogs {
		policy.upsertConditionalBinding("roles/logging.logWriter", member, nil)
		policy.upsertConditionalBinding("roles/monitoring.metricWriter", member, nil)
	}

	return r.setProjectIAMPolicy(ctx, policy)
}

func (r *GCPInstanceResources) removeProjectIAMBindings(ctx context.Context, saEmail string) error {
	policy, err := r.getProjectIAMPolicy(ctx)
	if err != nil {
		return err
	}
	member := "serviceAccount:" + saEmail
	policy.removeMemberBindings(member)
	return r.setProjectIAMPolicy(ctx, policy)
}

func (r *GCPInstanceResources) getProjectIAMPolicy(ctx context.Context) (*iamPolicy, error) {
	getURL := fmt.Sprintf("%s/v1/projects/%s:getIamPolicy",
		r.crmBaseURL, url.PathEscape(r.project))
	reqBody := map[string]any{
		"options": map[string]int{"requestedPolicyVersion": 3},
	}
	var policy iamPolicy
	if err := r.doJSONRequest(ctx, http.MethodPost, getURL, reqBody, &policy); err != nil {
		return nil, fmt.Errorf("gcp crm: getIamPolicy on project %s: %w", r.project, err)
	}
	if policy.Version < 3 {
		policy.Version = 3
	}
	return &policy, nil
}

func (r *GCPInstanceResources) setProjectIAMPolicy(ctx context.Context, policy *iamPolicy) error {
	setURL := fmt.Sprintf("%s/v1/projects/%s:setIamPolicy",
		r.crmBaseURL, url.PathEscape(r.project))
	reqBody := map[string]any{
		"policy": policy,
	}
	if err := r.doJSONRequest(ctx, http.MethodPost, setURL, reqBody, nil); err != nil {
		return fmt.Errorf("gcp crm: setIamPolicy on project %s: %w", r.project, err)
	}
	return nil
}

func (r *GCPInstanceResources) getInstanceIAMPolicy(ctx context.Context, zone, instanceID string) (*iamPolicy, error) {
	getURL := fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s/getIamPolicy?optionsRequestedPolicyVersion=3",
		r.computeBaseURL, url.PathEscape(r.project), url.PathEscape(zone), url.PathEscape(instanceID))
	var policy iamPolicy
	if err := r.doJSONRequest(ctx, http.MethodGet, getURL, nil, &policy); err != nil {
		return nil, fmt.Errorf("gcp compute: getIamPolicy on %s: %w", instanceID, err)
	}
	if policy.Version < 3 {
		policy.Version = 3
	}
	return &policy, nil
}

func (r *GCPInstanceResources) setInstanceIAMPolicy(ctx context.Context, zone, instanceID string, policy *iamPolicy) error {
	setURL := fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s/setIamPolicy",
		r.computeBaseURL, url.PathEscape(r.project), url.PathEscape(zone), url.PathEscape(instanceID))
	reqBody := map[string]any{
		"Bindings": policy.Bindings,
		"bindings": policy.Bindings,
		"etag":     policy.Etag,
		"version":  3,
	}
	if err := r.doJSONRequest(ctx, http.MethodPost, setURL, reqBody, nil); err != nil {
		return fmt.Errorf("gcp compute: setIamPolicy on %s: %w", instanceID, err)
	}
	return nil
}

// httpStatusError is a non-2xx answer from a GCP REST API, kept typed so
// callers can tell a refusal (403) or a lost etag race (409) from other failures.
type httpStatusError struct {
	StatusCode int
	Body       string
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body) }

// httpStatusOf returns the HTTP status of an httpStatusError in err's chain, or 0.
func httpStatusOf(err error) int {
	var he *httpStatusError
	if errors.As(err, &he) {
		return he.StatusCode
	}
	return 0
}

func (r *GCPInstanceResources) doJSONRequest(ctx context.Context, method, reqURL string, reqPayload any, respOut any, allowedStatuses ...int) error {
	var bodyReader io.Reader
	if reqPayload != nil {
		b, err := json.Marshal(reqPayload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return err
	}
	if reqPayload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	for _, code := range allowedStatuses {
		if resp.StatusCode == code {
			return nil
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(respBytes))}
	}
	if respOut != nil && len(bytes.TrimSpace(respBytes)) > 0 {
		if err := json.Unmarshal(respBytes, respOut); err != nil {
			return fmt.Errorf("decoding response JSON: %w", err)
		}
	}
	return nil
}

func init() {
	factory := func(ctx context.Context, cfg hub.Config) (hub.InstanceResources, error) {
		return NewGCPInstanceResources(ctx, cfg)
	}
	hub.RegisterInstanceResourcesProvider("gce", factory)
	hub.RegisterInstanceResourcesProvider("gcp", factory)
}
