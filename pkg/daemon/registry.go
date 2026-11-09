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

package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/idtoken"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/version"
)

const (
	// RegistryURLEnv, RegistryEntryEnv, RegistryTokenEnv, and RegistryConnectEnv
	// allow container and Kubernetes deployments to configure registry
	// self-registration and the outbound tunnel via environment variables when
	// flags are omitted.
	RegistryURLEnv     = "GRAIN_REGISTRY_URL"
	RegistryEntryEnv   = "GRAIN_REGISTRY_ENTRY"
	RegistryTokenEnv   = "GRAIN_REGISTRY_TOKEN"
	RegistryConnectEnv = "GRAIN_REGISTRY_CONNECT"

	defaultRegistryRequestTimeout = 10 * time.Second
	defaultRegistryMinInterval    = 2 * time.Second
	defaultRegistryMaxInterval    = 5 * time.Minute
	defaultRegistryBaseBackoff    = 1 * time.Second
	defaultRegistryMaxBackoff     = 60 * time.Second
	defaultRegistryJitterFraction = 0.15
)

// InferRegistryHostKind returns the configured connected host_kind when set,
// or derives one from the daemon's sandbox backend / runtime environment.
func (c Config) InferRegistryHostKind() api.HostKind {
	if c.Registry.HostKind != "" {
		return c.Registry.HostKind
	}
	if c.K8sSandboxes || c.K8sGranuleSandboxes || InKubernetesPod() {
		return api.HostKindKubernetes
	}
	if c.GranuleSandboxes || c.ContainerSandboxes || c.HostSandboxes {
		return api.HostKindWorkstation
	}
	return api.HostKindOther
}

// OIDCTokenFunc mints an OIDC ID token for audience, returning the raw JWT and
// its expiration time (or zero time if the JWT's own exp claim should be read).
type OIDCTokenFunc func(ctx context.Context, audience string) (string, time.Time, error)

// RegistryTunnelConnector is the seam for the outbound multiplexed proxy tunnel
// (`/api/entries/<name>/connect` via api.ConnectMux).
type RegistryTunnelConnector interface {
	RunTunnel(ctx context.Context, cfg api.ConnectConfig) error
}

// RegistryManagerOptions configures a RegistryManager.
type RegistryManagerOptions struct {
	Store             *model.Store
	Secrets           *secrets.Store
	LiveConfig        func() Config
	StateRepo         func() model.RepoRef
	LocalRepos        []model.RepoRef
	Health            func() (api.HealthState, string)
	Version           func() version.Info
	HTTPClient        *http.Client
	AllowInsecureHTTP bool
	OIDCTokenSource   OIDCTokenFunc
	TunnelConnector   RegistryTunnelConnector

	BaseInterval   time.Duration
	MinInterval    time.Duration
	MaxInterval    time.Duration
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	RequestTimeout time.Duration
	// JitterFraction controls +/- jitter on intervals and backoffs (default 0.15).
	// Set negative in tests to disable jitter.
	JitterFraction float64
	Now            func() time.Time
	Logf           func(string, ...any)
}

// RegistryManager self-registers a grain daemon as a connected entry in a
// grain registry and keeps its entry fresh via a background heartbeat loop
// (design/design/registry.design.md, "Connected entries").
//
// It runs in its own goroutine and never blocks or slows the reconcile loop.
type RegistryManager struct {
	opts RegistryManagerOptions

	wakeCh chan struct{}
	stepMu sync.Mutex

	mu                       sync.Mutex
	registered               bool
	syncedMetaKey            string
	syncedCfg                model.RegistryConfig
	registryStatus           api.EntryStatus
	lastHeartbeatAt          *time.Time
	lastAttemptAt            *time.Time
	nextHeartbeatAt          *time.Time
	heartbeatIntervalSeconds int
	consecutiveFailures      int
	lastError                string
	scopeConflicts           []api.ScopeConflict
	lastObservedCfg          model.RegistryConfig
	lastObservedRepos        []string

	credMu                 sync.Mutex
	cachedSecretLoaded     bool
	cachedSecretCiphertext []byte
	cachedSecretToken      string
	cachedSecretErr        error
	cachedOIDCAudience     string
	cachedOIDCToken        string
	cachedOIDCExpiry       time.Time

	tunnelMu        sync.Mutex
	tunnelRunCtx    context.Context
	tunnelConnector RegistryTunnelConnector
	tunnelKey       string
	tunnelCancel    context.CancelFunc
	tunnelDone      chan struct{}
}

// NewRegistryManager builds a RegistryManager from opts.
func NewRegistryManager(opts RegistryManagerOptions) *RegistryManager {
	if opts.BaseInterval <= 0 {
		opts.BaseInterval = api.DefaultHeartbeatInterval
	}
	if opts.MinInterval <= 0 {
		if opts.BaseInterval < defaultRegistryMinInterval {
			opts.MinInterval = opts.BaseInterval / 2
			if opts.MinInterval <= 0 {
				opts.MinInterval = time.Millisecond
			}
		} else {
			opts.MinInterval = defaultRegistryMinInterval
		}
	}
	if opts.MaxInterval <= 0 {
		opts.MaxInterval = defaultRegistryMaxInterval
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = defaultRegistryBaseBackoff
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = defaultRegistryMaxBackoff
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = defaultRegistryRequestTimeout
	}
	if opts.JitterFraction == 0 {
		opts.JitterFraction = defaultRegistryJitterFraction
	}
	if opts.Version == nil {
		opts.Version = version.Get
	}
	if opts.Health == nil {
		opts.Health = defaultRegistryHealth
	}
	if opts.OIDCTokenSource == nil {
		opts.OIDCTokenSource = defaultOIDCTokenSource
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return &RegistryManager{
		opts:            opts,
		wakeCh:          make(chan struct{}, 1),
		tunnelConnector: opts.TunnelConnector,
	}
}

func defaultRegistryHealth() (api.HealthState, string) {
	if ReconcilerDown.Load() {
		return api.HealthDegraded, "reconcile loop is down"
	}
	return api.HealthOK, ""
}

// SetTunnelConnector attaches or replaces the outbound proxy tunnel connector
// seam reserved for `/api/entries/<name>/connect` (design/design/registry.design.md).
func (m *RegistryManager) SetTunnelConnector(tc RegistryTunnelConnector) {
	if m == nil {
		return
	}
	m.tunnelMu.Lock()
	m.tunnelConnector = tc
	running := m.tunnelRunCtx != nil
	m.tunnelMu.Unlock()
	if running {
		m.Trigger()
	}
}

// HasTunnelConnector reports whether a RegistryTunnelConnector is attached.
func (m *RegistryManager) HasTunnelConnector() bool {
	if m == nil {
		return false
	}
	m.tunnelMu.Lock()
	defer m.tunnelMu.Unlock()
	return m.tunnelConnector != nil
}

// InvalidateOIDCCache clears any cached OIDC ID token so the next heartbeat or
// tunnel dial mints a fresh token.
func (m *RegistryManager) InvalidateOIDCCache() {
	if m == nil {
		return
	}
	m.invalidateOIDCCache()
}

// Trigger wakes the background heartbeat loop asynchronously. Safe to call from
// any goroutine, HTTP handler, or reconcile tick.
func (m *RegistryManager) Trigger() {
	if m == nil {
		return
	}
	select {
	case m.wakeCh <- struct{}{}:
	default:
	}
}

// NotifyStoreConfig triggers an immediate sync when grain_config's registry
// settings or target repository list changed on a reconcile tick.
func (m *RegistryManager) NotifyStoreConfig(reg model.RegistryConfig, targetRepos []string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	changed := !reflect.DeepEqual(m.lastObservedCfg, reg) || !slices.Equal(m.lastObservedRepos, targetRepos)
	if changed {
		m.lastObservedCfg = reg
		m.lastObservedRepos = slices.Clone(targetRepos)
	}
	m.mu.Unlock()
	if changed {
		m.Trigger()
	}
}

// TriggerHeartbeat runs one registration/heartbeat step synchronously and
// returns the resulting snapshot (used by POST /api/registry/heartbeat and CLI).
func (m *RegistryManager) TriggerHeartbeat(ctx context.Context) (model.RegistrySnapshot, error) {
	if m == nil {
		return model.RegistrySnapshot{State: model.RegistryStateDisabled}, nil
	}
	_, stepErr := m.step(ctx)
	snap, snapErr := m.Snapshot(ctx)
	if stepErr != nil {
		return snap, redactRegistryError(stepErr)
	}
	return snap, redactRegistryError(snapErr)
}

func redactRegistryError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted, n := (*redact.Redactor)(nil).Text(msg)
	if n == 0 || redacted == msg {
		return err
	}
	return errors.New(redacted)
}

// Run executes the background self-registration and heartbeat loop until ctx is
// cancelled.
func (m *RegistryManager) Run(ctx context.Context) {
	if m == nil {
		return
	}
	m.tunnelMu.Lock()
	m.tunnelRunCtx = ctx
	m.tunnelMu.Unlock()
	defer func() {
		m.tunnelMu.Lock()
		m.tunnelRunCtx = nil
		m.tunnelMu.Unlock()
		m.stopTunnel()
	}()

	delay, _ := m.step(ctx)
	timer := time.NewTimer(delay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wakeCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}

		if ctx.Err() != nil {
			return
		}
		delay, _ = m.step(ctx)
		timer.Reset(delay)
	}
}

// Snapshot builds a point-in-time RegistrySnapshot combining the latest
// heartbeat state with current store-derived scopes and task counts.
func (m *RegistryManager) Snapshot(ctx context.Context) (model.RegistrySnapshot, error) {
	if m == nil {
		return model.RegistrySnapshot{State: model.RegistryStateDisabled}, nil
	}
	inputs, err := m.loadInputs(ctx)
	if err != nil {
		return model.RegistrySnapshot{}, err
	}

	m.mu.Lock()
	registered := m.registered
	regStatus := m.registryStatus
	lastHB := cloneTimePtr(m.lastHeartbeatAt)
	lastAttempt := cloneTimePtr(m.lastAttemptAt)
	nextHB := cloneTimePtr(m.nextHeartbeatAt)
	intervalSec := m.heartbeatIntervalSeconds
	failures := m.consecutiveFailures
	lastErr := m.lastError
	conflicts := slices.Clone(m.scopeConflicts)
	m.mu.Unlock()

	enabled := inputs.regCfg.Enabled()
	state := model.RegistryStateDisabled
	if enabled {
		switch {
		case lastErr != "" || failures > 0:
			state = model.RegistryStateError
		case lastHB == nil:
			state = model.RegistryStateConnecting
		case len(conflicts) > 0 || regStatus == api.StatusDegraded || inputs.health != api.HealthOK:
			state = model.RegistryStateDegraded
		default:
			state = model.RegistryStateConnected
		}
	}

	return model.RegistrySnapshot{
		Enabled:                  enabled,
		State:                    state,
		Config:                   inputs.regCfg,
		EffectiveHostKind:        inputs.hostKind,
		TokenConfigured:          inputs.tokenConfigured,
		CredentialSource:         inputs.credentialSource,
		RegistryStatus:           regStatus,
		Registered:               registered,
		LastHeartbeatAt:          lastHB,
		LastAttemptAt:            lastAttempt,
		NextHeartbeatAt:          nextHB,
		HeartbeatIntervalSeconds: intervalSec,
		ConsecutiveFailures:      failures,
		LastError:                lastErr,
		ScopeConflicts:           conflicts,
		GrainVersion:             inputs.grainVersion,
		GrainCommit:              inputs.grainCommit,
		Health:                   inputs.health,
		HealthDetail:             inputs.healthDetail,
		ActiveTasks:              inputs.activeTasks,
		AwaitingReplyTasks:       inputs.awaitingReplyTasks,
		AdvertisedScopes:         inputs.scopes,
	}, nil
}

type registryStepInputs struct {
	daemonCfg          Config
	regCfg             model.RegistryConfig
	hostKind           api.HostKind
	tokenConfigured    bool
	credentialSource   string
	grainVersion       string
	grainCommit        string
	health             api.HealthState
	healthDetail       string
	activeTasks        int
	awaitingReplyTasks int
	scopes             []model.RegistryAdvertisedScope
}

func (m *RegistryManager) loadInputs(ctx context.Context) (registryStepInputs, error) {
	var dcfg Config
	if m.opts.LiveConfig != nil {
		dcfg = m.opts.LiveConfig()
	}

	var stored *model.Config
	var repoConfigs []model.RepoConfig
	var active, awaiting int
	if m.opts.Store != nil {
		var err error
		stored, err = m.opts.Store.GetConfig(ctx)
		if err != nil {
			return registryStepInputs{}, fmt.Errorf("reading registry configuration: %w", err)
		}
		repoConfigs, err = m.opts.Store.ListRepoConfigs(ctx)
		if err != nil {
			return registryStepInputs{}, fmt.Errorf("listing repository configs: %w", err)
		}
		active, awaiting, err = m.opts.Store.RegistryTaskCounts(ctx)
		if err != nil {
			return registryStepInputs{}, fmt.Errorf("counting registry tasks: %w", err)
		}
	}

	regCfg := dcfg.Registry
	var targetRepos []string
	if stored != nil {
		regCfg = stored.Registry
		targetRepos = stored.TargetRepos
	}
	dcfg.Registry = regCfg
	m.mu.Lock()
	m.lastObservedCfg = regCfg
	m.lastObservedRepos = slices.Clone(targetRepos)
	m.mu.Unlock()

	var stateRepo model.RepoRef
	if m.opts.StateRepo != nil {
		stateRepo = m.opts.StateRepo()
	}
	localRepoStrings := make([]string, 0, len(m.opts.LocalRepos)+len(dcfg.LocalRepos))
	for _, r := range m.opts.LocalRepos {
		localRepoStrings = append(localRepoStrings, r.String())
	}
	for _, r := range dcfg.LocalRepos {
		localRepoStrings = append(localRepoStrings, r.String())
	}

	scopes := model.DeriveRegistryScopes(targetRepos, localRepoStrings, stateRepo, repoConfigs, regCfg.Scopes)
	vInfo := m.opts.Version()
	health, healthDetail := m.opts.Health()
	if health == "" {
		health = api.HealthOK
	}

	tokConfigured, credSource := m.inspectCredential(dcfg, regCfg)

	return registryStepInputs{
		daemonCfg:          dcfg,
		regCfg:             regCfg,
		hostKind:           dcfg.InferRegistryHostKind(),
		tokenConfigured:    tokConfigured,
		credentialSource:   credSource,
		grainVersion:       vInfo.Release,
		grainCommit:        vInfo.Revision,
		health:             health,
		healthDetail:       healthDetail,
		activeTasks:        active,
		awaitingReplyTasks: awaiting,
		scopes:             scopes,
	}, nil
}

func (m *RegistryManager) step(parentCtx context.Context) (time.Duration, error) {
	m.stepMu.Lock()
	defer m.stepMu.Unlock()

	reqCtx, cancel := context.WithTimeout(parentCtx, m.opts.RequestTimeout)
	defer cancel()

	inputs, err := m.loadInputs(reqCtx)
	if err != nil {
		return m.recordFailure(err), err
	}

	if !inputs.regCfg.Enabled() {
		m.stopTunnel()
		m.mu.Lock()
		m.registered = false
		m.syncedMetaKey = ""
		m.syncedCfg = model.RegistryConfig{}
		m.registryStatus = ""
		m.nextHeartbeatAt = nil
		m.heartbeatIntervalSeconds = 0
		m.consecutiveFailures = 0
		m.lastError = ""
		m.scopeConflicts = nil
		m.mu.Unlock()
		// Idle wait; Trigger() wakes immediately when Registry is enabled.
		return m.opts.MaxInterval, nil
	}

	attemptAt := m.opts.Now().UTC()
	m.mu.Lock()
	m.lastAttemptAt = &attemptAt
	m.mu.Unlock()

	normCfg, err := model.NormalizeRegistryConfig(inputs.regCfg, m.opts.AllowInsecureHTTP)
	if err != nil {
		return m.recordFailure(err), err
	}
	inputs.regCfg = normCfg
	inputs.daemonCfg.Registry = normCfg
	inputs.hostKind = inputs.daemonCfg.InferRegistryHostKind()

	tokenSource := func(callCtx context.Context) (string, error) {
		tok, _, resolveErr := m.resolveCredential(callCtx, inputs.daemonCfg, inputs.regCfg)
		return tok, resolveErr
	}

	// Pre-flight credential resolution so missing credentials fail cleanly with
	// an actionable message before opening any network connection.
	initialTok, _, credErr := m.resolveCredential(reqCtx, inputs.daemonCfg, inputs.regCfg)
	if credErr != nil {
		return m.recordFailure(credErr), credErr
	}

	clientCfg := api.ClientConfig{
		BaseURL:           inputs.regCfg.URL,
		HTTPClient:        m.opts.HTTPClient,
		AllowInsecureHTTP: m.opts.AllowInsecureHTTP,
		UserAgent:         registryUserAgent(inputs.grainVersion),
	}
	if strings.HasPrefix(initialTok, api.RegistrationTokenPrefix) {
		clientCfg.RegistrationToken = initialTok
	} else {
		clientCfg.IDToken = tokenSource
	}
	client, err := api.NewClient(clientCfg)
	if err != nil {
		return m.recordFailure(err), err
	}

	scopeInputs := model.ToScopeInputs(inputs.scopes)
	hbScopes := model.ToHeartbeatScopes(inputs.scopes)
	metaKey := metadataFingerprint(inputs.regCfg, inputs.hostKind)

	// In OIDC mode, ensure the connected entry exists and keep owner-managed
	// catalog metadata (display_name, purpose, host_kind, listed, owner_group,
	// tags) in sync via PATCH / POST /api/entries.
	if inputs.regCfg.EffectiveAuthMode() == model.RegistryAuthOIDC {
		m.mu.Lock()
		alreadySynced := m.syncedMetaKey == metaKey
		prevCfg := m.syncedCfg
		m.mu.Unlock()
		if !alreadySynced {
			if syncErr := m.syncConnectedEntryMetadata(reqCtx, client, inputs.regCfg, prevCfg, inputs.hostKind, scopeInputs); syncErr != nil {
				if api.IsUnauthorized(syncErr) {
					m.invalidateOIDCCache()
				}
				return m.recordFailure(syncErr), syncErr
			}
			m.mu.Lock()
			m.syncedMetaKey = metaKey
			m.syncedCfg = inputs.regCfg
			m.registered = true
			m.mu.Unlock()
		}
	}

	// Only send HostKind on heartbeats when explicitly configured on the daemon
	// (e.g. -registry-host-kind or settings.registry.host_kind). When unset, the
	// daemon's inferred kind is used only at initial entry creation so heartbeats
	// never override a host_kind declared earlier via `grain registry register`
	// or `grain registry update`.
	hbReq := api.HeartbeatRequest{
		GrainVersion:       inputs.grainVersion,
		GrainCommit:        inputs.grainCommit,
		HostKind:           inputs.regCfg.HostKind,
		Health:             inputs.health,
		HealthDetail:       inputs.healthDetail,
		ActiveTasks:        inputs.activeTasks,
		AwaitingReplyTasks: inputs.awaitingReplyTasks,
		Scopes:             hbScopes,
	}

	resp, err := client.Heartbeat(reqCtx, inputs.regCfg.Entry, hbReq)
	if err != nil && api.IsNotFound(err) && !strings.HasPrefix(initialTok, api.RegistrationTokenPrefix) {
		// Entry was missing or recreated on the registry while authenticating with
		// a creator/OIDC token: self-register as a connected entry and retry once.
		if createErr := m.createConnectedEntry(reqCtx, client, inputs.regCfg, inputs.hostKind, scopeInputs); createErr == nil {
			m.mu.Lock()
			m.syncedMetaKey = metaKey
			m.syncedCfg = inputs.regCfg
			m.registered = true
			m.mu.Unlock()
			resp, err = client.Heartbeat(reqCtx, inputs.regCfg.Entry, hbReq)
		} else {
			err = createErr
		}
	}
	if err != nil {
		if api.IsUnauthorized(err) {
			m.invalidateOIDCCache()
		}
		m.ensureTunnel(parentCtx, inputs, initialTok, tokenSource)
		return m.recordFailure(err), err
	}

	interval := m.clampInterval(time.Duration(resp.NextIntervalSeconds) * time.Second)
	delay := m.jitter(interval)
	now := m.opts.Now().UTC()
	next := now.Add(delay)

	m.mu.Lock()
	wasFailing := m.consecutiveFailures > 0
	m.registered = true
	if resp.Status != "" {
		m.registryStatus = resp.Status
	} else {
		m.registryStatus = api.StatusOnline
	}
	m.lastHeartbeatAt = &now
	m.nextHeartbeatAt = &next
	m.heartbeatIntervalSeconds = int(interval / time.Second)
	m.consecutiveFailures = 0
	m.lastError = ""
	m.scopeConflicts = slices.Clone(resp.Conflicts)
	m.mu.Unlock()

	if wasFailing {
		m.opts.Logf("grain daemon: registry heartbeat to %s (%s) recovered", inputs.regCfg.URL, inputs.regCfg.Entry)
	}

	m.ensureTunnel(parentCtx, inputs, initialTok, tokenSource)
	return delay, nil
}

func (m *RegistryManager) syncConnectedEntryMetadata(ctx context.Context, client *api.Client, cfg, prevCfg model.RegistryConfig, inferredHostKind api.HostKind, scopes []api.ScopeInput) error {
	sameTarget := prevCfg.URL == cfg.URL && strings.EqualFold(prevCfg.Entry, cfg.Entry)
	var upd api.UpdateEntryRequest
	if cfg.DisplayName != "" || (sameTarget && prevCfg.DisplayName != "") {
		dn := cfg.DisplayName
		upd.DisplayName = &dn
	}
	if cfg.Purpose != "" || (sameTarget && prevCfg.Purpose != "") {
		purpose := cfg.Purpose
		upd.Purpose = &purpose
	}
	if cfg.HostKind != "" {
		hk := cfg.HostKind
		upd.HostKind = &hk
	}
	if cfg.OwnerGroup != "" || (sameTarget && prevCfg.OwnerGroup != "") {
		ownerGroup := cfg.OwnerGroup
		upd.OwnerGroup = &ownerGroup
	}
	if cfg.Listed || (sameTarget && prevCfg.Listed) {
		listed := cfg.Listed
		upd.Listed = &listed
	}
	if len(cfg.Tags) > 0 || (sameTarget && len(prevCfg.Tags) > 0) {
		tags := slices.Clone(cfg.Tags)
		if tags == nil {
			tags = []string{}
		}
		upd.Tags = &tags
	}
	_, err := client.UpdateEntry(ctx, cfg.Entry, upd)
	if err == nil {
		return nil
	}
	if api.IsNotFound(err) {
		return m.createConnectedEntry(ctx, client, cfg, inferredHostKind, scopes)
	}
	return err
}

func (m *RegistryManager) createConnectedEntry(ctx context.Context, client *api.Client, cfg model.RegistryConfig, hostKind api.HostKind, scopes []api.ScopeInput) error {
	listed := cfg.Listed
	_, err := client.CreateEntry(ctx, api.CreateEntryRequest{
		Name:        cfg.Entry,
		Kind:        api.KindConnected,
		HostKind:    hostKind,
		DisplayName: cfg.DisplayName,
		Purpose:     cfg.Purpose,
		OwnerGroup:  cfg.OwnerGroup,
		Listed:      &listed,
		Tags:        slices.Clone(cfg.Tags),
		Scopes:      scopes,
	})
	if err != nil && api.IsConflict(err) {
		// Concurrent creation by another replica or CLI: check whether our
		// PATCH succeeds against the existing entry.
		return nil
	}
	return err
}

func (m *RegistryManager) recordFailure(err error) time.Duration {
	now := m.opts.Now().UTC()
	redactedErr, _ := (*redact.Redactor)(nil).Text(err.Error())

	m.mu.Lock()
	m.consecutiveFailures++
	failures := m.consecutiveFailures
	m.lastError = redactedErr
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Conflict != nil {
		m.scopeConflicts = []api.ScopeConflict{*apiErr.Conflict}
	}
	backoff := m.computeBackoff(failures, apiErr)
	next := now.Add(backoff)
	m.nextHeartbeatAt = &next
	m.mu.Unlock()

	if failures == 1 || failures%10 == 0 {
		m.opts.Logf("grain daemon: registry heartbeat failed (attempt %d, retrying in %s): %s",
			failures, backoff.Round(10*time.Millisecond), redactedErr)
	}
	return backoff
}

func (m *RegistryManager) computeBackoff(failures int, apiErr *api.APIError) time.Duration {
	delay := m.opts.BaseBackoff
	for i := 1; i < failures && delay < m.opts.MaxBackoff; i++ {
		delay *= 2
		if delay > m.opts.MaxBackoff {
			delay = m.opts.MaxBackoff
			break
		}
	}
	delay = m.jitter(delay)
	if apiErr != nil && apiErr.RetryAfter > delay {
		delay = apiErr.RetryAfter
		if delay > m.opts.MaxInterval {
			delay = m.opts.MaxInterval
		}
	}
	return delay
}

func (m *RegistryManager) clampInterval(serverInterval time.Duration) time.Duration {
	d := serverInterval
	if d <= 0 {
		d = m.opts.BaseInterval
	}
	if d < m.opts.MinInterval {
		d = m.opts.MinInterval
	}
	if d > m.opts.MaxInterval {
		d = m.opts.MaxInterval
	}
	return d
}

func (m *RegistryManager) jitter(d time.Duration) time.Duration {
	if d <= 0 || m.opts.JitterFraction < 0 {
		return d
	}
	f := m.opts.JitterFraction
	mult := (1 - f) + (2 * f * rand.Float64())
	out := time.Duration(float64(d) * mult)
	if out <= 0 {
		return time.Millisecond
	}
	return out
}

func (m *RegistryManager) inspectCredential(dcfg Config, regCfg model.RegistryConfig) (bool, string) {
	if regCfg.EffectiveAuthMode() == model.RegistryAuthOIDC {
		return true, "oidc"
	}
	if m.opts.Secrets != nil {
		if ok, err := m.opts.Secrets.Has(secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey); err == nil && ok {
			return true, "secret"
		}
	}
	if path := strings.TrimSpace(dcfg.RegistryTokenFile); path != "" {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
			return true, "file"
		}
	}
	if strings.TrimSpace(os.Getenv(RegistryTokenEnv)) != "" {
		return true, "env"
	}
	return false, ""
}

func (m *RegistryManager) resolveCredential(ctx context.Context, dcfg Config, regCfg model.RegistryConfig) (string, string, error) {
	if regCfg.EffectiveAuthMode() == model.RegistryAuthOIDC {
		aud := regCfg.EffectiveAudience()
		tok, err := m.resolveOIDCToken(ctx, aud)
		if err != nil {
			return "", "oidc", err
		}
		return tok, "oidc", nil
	}

	if m.opts.Secrets != nil {
		tok, err := m.resolveRegistrationSecret(ctx)
		if err == nil && tok != "" {
			return tok, "secret", nil
		}
		if err != nil && !errors.Is(err, secrets.ErrNoSecret) {
			return "", "secret", err
		}
	}

	if path := strings.TrimSpace(dcfg.RegistryTokenFile); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "file", fmt.Errorf("reading registry token file %s: %w", path, err)
		}
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, "file", nil
		}
	}

	if tok := strings.TrimSpace(os.Getenv(RegistryTokenEnv)); tok != "" {
		return tok, "env", nil
	}

	return "", "", fmt.Errorf("no hub registration credential configured: run 'grain registry register %s' or set secret %s in Settings",
		regCfg.Entry, secrets.RegistryRegistrationTokenSecret)
}

// resolveRegistrationSecret reads secrets.RegistryRegistrationTokenSecret from
// m.opts.Secrets, caching the decrypted value against the raw ciphertext bytes
// on disk so rotating or setting the secret in Settings takes effect on the
// very next heartbeat without writing a secret_audit database row every 15s.
func (m *RegistryManager) resolveRegistrationSecret(ctx context.Context) (string, error) {
	m.credMu.Lock()
	defer m.credMu.Unlock()

	secretFile := m.opts.Secrets.File()
	if secretFile != "" {
		raw, err := os.ReadFile(secretFile)
		switch {
		case os.IsNotExist(err):
			m.cachedSecretLoaded = true
			m.cachedSecretCiphertext = nil
			m.cachedSecretToken = ""
			m.cachedSecretErr = secrets.ErrNoSecret
			return "", secrets.ErrNoSecret
		case err == nil:
			if m.cachedSecretLoaded && bytes.Equal(raw, m.cachedSecretCiphertext) {
				return m.cachedSecretToken, m.cachedSecretErr
			}
			val, resolveErr := m.opts.Secrets.Resolve(
				secrets.WithPrincipal(ctx, "daemon/registry"),
				secrets.RegistryRegistrationTokenSecret+"/"+secrets.AgentCredentialKey,
			)
			if resolveErr != nil {
				// Also support single-key resolution if stored under another key name.
				if altVal, altErr := m.opts.Secrets.Resolve(
					secrets.WithPrincipal(ctx, "daemon/registry"),
					secrets.RegistryRegistrationTokenSecret,
				); altErr == nil {
					val, resolveErr = altVal, nil
				}
			}
			m.cachedSecretLoaded = true
			m.cachedSecretCiphertext = bytes.Clone(raw)
			m.cachedSecretToken = strings.TrimSpace(val)
			m.cachedSecretErr = resolveErr
			return m.cachedSecretToken, m.cachedSecretErr
		}
	}

	val, err := m.opts.Secrets.Resolve(
		secrets.WithPrincipal(ctx, "daemon/registry"),
		secrets.RegistryRegistrationTokenSecret+"/"+secrets.AgentCredentialKey,
	)
	if err != nil {
		if altVal, altErr := m.opts.Secrets.Resolve(
			secrets.WithPrincipal(ctx, "daemon/registry"),
			secrets.RegistryRegistrationTokenSecret,
		); altErr == nil {
			val, err = altVal, nil
		}
	}
	return strings.TrimSpace(val), err
}

func (m *RegistryManager) resolveOIDCToken(ctx context.Context, audience string) (string, error) {
	m.credMu.Lock()
	defer m.credMu.Unlock()

	now := m.opts.Now()
	if m.cachedOIDCAudience == audience && m.cachedOIDCToken != "" && now.Before(m.cachedOIDCExpiry) {
		return m.cachedOIDCToken, nil
	}

	tok, expiry, err := m.opts.OIDCTokenSource(ctx, audience)
	if err != nil {
		return "", fmt.Errorf("minting OIDC ID token for %s: %w", audience, err)
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", fmt.Errorf("minting OIDC ID token for %s returned an empty token", audience)
	}
	if expiry.IsZero() {
		expiry = parseJWTExpiry(tok)
	}
	if !expiry.IsZero() {
		m.cachedOIDCAudience = audience
		m.cachedOIDCToken = tok
		m.cachedOIDCExpiry = expiry.Add(-time.Minute)
	} else {
		m.cachedOIDCAudience = ""
		m.cachedOIDCToken = ""
		m.cachedOIDCExpiry = time.Time{}
	}
	return tok, nil
}

func (m *RegistryManager) invalidateOIDCCache() {
	m.credMu.Lock()
	m.cachedOIDCAudience = ""
	m.cachedOIDCToken = ""
	m.cachedOIDCExpiry = time.Time{}
	m.credMu.Unlock()
}

func defaultOIDCTokenSource(ctx context.Context, audience string) (string, time.Time, error) {
	ts, err := idtoken.NewTokenSource(ctx, audience)
	if err == nil {
		if tok, tokErr := ts.Token(); tokErr == nil && tok != nil && strings.TrimSpace(tok.AccessToken) != "" {
			return strings.TrimSpace(tok.AccessToken), tok.Expiry, nil
		} else if tokErr != nil {
			err = tokErr
		}
	}
	if gcloudPath, lookErr := exec.LookPath("gcloud"); lookErr == nil {
		cmd := exec.CommandContext(ctx, gcloudPath, "auth", "print-identity-token", "--audiences="+audience)
		var env []string
		for _, k := range []string{
			"PATH", "HOME", "USER", "TMPDIR",
			"CLOUDSDK_CONFIG", "GOOGLE_APPLICATION_CREDENTIALS",
			"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE",
		} {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
		cmd.Env = env
		if out, cmdErr := cmd.Output(); cmdErr == nil {
			if tok := strings.TrimSpace(string(out)); tok != "" {
				return tok, time.Time{}, nil
			}
		}
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return "", time.Time{}, fmt.Errorf("no Google ADC or gcloud identity token available for audience %q", audience)
}

func parseJWTExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

func (m *RegistryManager) ensureTunnel(parentCtx context.Context, inputs registryStepInputs, initialTok string, tokenSource func(context.Context) (string, error)) {
	m.tunnelMu.Lock()
	tc := m.tunnelConnector
	if tc == nil {
		m.tunnelMu.Unlock()
		m.stopTunnel()
		return
	}
	cfg := inputs.regCfg
	regTok := ""
	if strings.HasPrefix(initialTok, api.RegistrationTokenPrefix) {
		regTok = initialTok
	}
	key := fmt.Sprintf("%s|%s|%s|%s|%s", cfg.URL, cfg.Entry, cfg.EffectiveAuthMode(), cfg.EffectiveAudience(), regTok)
	if m.tunnelCancel != nil && m.tunnelKey == key {
		select {
		case <-m.tunnelDone:
			// Previous tunnel loop exited; restart it on this heartbeat.
		default:
			m.tunnelMu.Unlock()
			return
		}
	}
	prevCancel := m.tunnelCancel
	prevDone := m.tunnelDone

	baseCtx := m.tunnelRunCtx
	if baseCtx == nil {
		baseCtx = parentCtx
	}
	tunCtx, cancel := context.WithCancel(baseCtx)
	done := make(chan struct{})
	m.tunnelKey = key
	m.tunnelCancel = cancel
	m.tunnelDone = done
	m.tunnelMu.Unlock()

	if prevCancel != nil {
		prevCancel()
		<-prevDone
	}

	connCfg := api.ConnectConfig{
		BaseURL:           cfg.URL,
		Entry:             cfg.Entry,
		GrainVersion:      inputs.grainVersion,
		GrainCommit:       inputs.grainCommit,
		HostKind:          inputs.hostKind,
		AllowInsecureHTTP: m.opts.AllowInsecureHTTP,
	}
	if regTok != "" {
		connCfg.RegistrationToken = regTok
	} else {
		connCfg.IDToken = tokenSource
	}

	go func(c api.ConnectConfig) {
		defer close(done)
		if err := tc.RunTunnel(tunCtx, c); err != nil && !errors.Is(err, context.Canceled) {
			redactedErr, _ := (*redact.Redactor)(nil).Text(err.Error())
			m.opts.Logf("grain daemon: hub tunnel (%s) stopped: %s", c.Entry, redactedErr)
		}
	}(connCfg)
}

func (m *RegistryManager) stopTunnel() {
	m.tunnelMu.Lock()
	cancel := m.tunnelCancel
	done := m.tunnelDone
	m.tunnelCancel = nil
	m.tunnelDone = nil
	m.tunnelKey = ""
	m.tunnelMu.Unlock()

	if cancel != nil {
		cancel()
		<-done
	}
}

func metadataFingerprint(cfg model.RegistryConfig, hostKind api.HostKind) string {
	b, _ := json.Marshal(struct {
		URL         string       `json:"u"`
		Entry       string       `json:"e"`
		DisplayName string       `json:"dn"`
		Purpose     string       `json:"p"`
		HostKind    api.HostKind `json:"hk"`
		Listed      bool         `json:"l"`
		OwnerGroup  string       `json:"og"`
		Tags        []string     `json:"t"`
	}{
		URL:         cfg.URL,
		Entry:       cfg.Entry,
		DisplayName: cfg.DisplayName,
		Purpose:     cfg.Purpose,
		HostKind:    hostKind,
		Listed:      cfg.Listed,
		OwnerGroup:  cfg.OwnerGroup,
		Tags:        cfg.Tags,
	})
	return string(b)
}

func registryUserAgent(rel string) string {
	if s := strings.TrimSpace(rel); s != "" {
		return "grain-daemon/" + s
	}
	return "grain-daemon"
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}
