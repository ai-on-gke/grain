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
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/version"
)

// HubHealth summarizes the deployment's connected-entry registry
// heartbeat state on GET /api/sandboxes and `grain health`. Absent (nil)
// when registry self-registration is not configured.
type HubHealth struct {
	URL                 string                   `json:"url"`
	Entry               string                   `json:"entry"`
	State               model.HubConnectionState `json:"state"`
	LastHeartbeatAt     *time.Time               `json:"lastHeartbeatAt,omitempty"`
	ConsecutiveFailures int                      `json:"consecutiveFailures,omitempty"`
	LastError           string                   `json:"lastError,omitempty"`
	ScopeConflicts      []api.ScopeConflict      `json:"scopeConflicts,omitempty"`
}

func (c *Client) hubTokenSet() bool {
	if c == nil || c.Config.Secrets == nil {
		return false
	}
	ok, err := c.Config.Secrets.Has(secrets.HubRegistrationTokenSecret, secrets.AgentCredentialKey)
	return err == nil && ok
}

func (c *Client) triggerHubSync() {
	if c != nil && c.Config.TriggerHubSync != nil {
		c.Config.TriggerHubSync()
	}
}

func (c *Client) redactHubText(text string) string {
	if text == "" {
		return ""
	}
	redactor := grainlog.NewRedactor(nil)
	if c != nil && c.Config.LogRedactor != nil {
		var err error
		redactor, err = c.Config.LogRedactor()
		if err != nil || redactor == nil {
			return "(withheld: the redaction list could not be read)"
		}
	}
	redacted, _ := redactor.Text(text)
	return redacted
}

// hubTarget is the part of a HubConfig that decides where the
// daemon's registry credential goes and what it acts on there: the registry
// URL, the auth mode and OIDC audience it presents (and mints host identity
// tokens for), the entry name the credential is presented for -- in OIDC
// mode the host identity PATCHes or creates whatever entry is named -- and the
// owner_group it pushes onto that entry, whose members the registry lets
// manage the entry like its owner (rotate its grc_ credential, take over its
// tunnel). Catalog-only fields (displayName, purpose, listed, tags, hostKind,
// scopes) are not part of it.
type hubTarget struct {
	URL          string
	Entry        string
	AuthMode     model.HubAuthMode
	OIDCAudience string
	OwnerGroup   string
}

func hubAuthTarget(cfg model.HubConfig) hubTarget {
	mode, _ := model.NormalizeHubAuthMode(cfg.AuthMode)
	return hubTarget{
		URL:          strings.TrimSpace(cfg.URL),
		Entry:        strings.ToLower(strings.TrimSpace(cfg.Entry)),
		AuthMode:     mode,
		OIDCAudience: strings.TrimSpace(cfg.OIDCAudience),
		OwnerGroup:   strings.TrimSpace(cfg.OwnerGroup),
	}
}

func hubTargets(before *model.Config, after model.Config) (hubTarget, hubTarget) {
	var prev model.HubConfig
	if before != nil {
		prev = before.Hub
	}
	return hubAuthTarget(prev), hubAuthTarget(after.Hub)
}

func hubAuthTargetChanged(before *model.Config, after model.Config) bool {
	b, a := hubTargets(before, after)
	return b != a
}

// checkHubAuthTargetWrite holds changes to where the daemon sends its
// registry registration token or mints host OIDC identity tokens for (URL,
// authMode, oidcAudience), which entry it presents them for (entry), and who
// it hands management of that entry to (ownerGroup) to a human principal with
// secrets:write.
func (c *Client) checkHubAuthTargetWrite(ctx context.Context, before *model.Config, after model.Config) error {
	if !hubAuthTargetChanged(before, after) {
		return nil
	}
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
		return forbiddenErrorf("%q is not a person, so it may not change registry URL, entry, owner group or authentication settings", p.ID)
	}
	if role := accessRoleFromContext(ctx); role != nil && !role.allows("secrets:write", "") {
		return forbiddenErrorf("%s holds no scope secrets:write, which changing registry URL, entry, owner group or authentication settings needs", role.label())
	}
	return nil
}

// auditHubChanges records changes to registry URL, entry, authMode,
// oidcAudience or ownerGroup in the credential-access audit log.
func (c *Client) auditHubChanges(ctx context.Context, before *model.Config, after model.Config) {
	if c == nil || c.Store == nil || !hubAuthTargetChanged(before, after) {
		return
	}
	b, a := hubTargets(before, after)
	action := "set registry target"
	if a.URL == "" {
		action = "clear registry target"
	}
	outcome := fmt.Sprintf("url %q -> %q, entry %q -> %q, authMode %q -> %q, oidcAudience %q -> %q, ownerGroup %q -> %q",
		b.URL, a.URL, b.Entry, a.Entry, b.AuthMode, a.AuthMode, b.OIDCAudience, a.OIDCAudience, b.OwnerGroup, a.OwnerGroup)
	if user := c.requestActor(ctx).Actor.ID; user != "" {
		outcome += " by " + user
	}
	log.Printf("grain ui: registry: %s %s", action, outcome)
	if err := c.Store.RecordCredentialAccess(ctx, model.CredentialAccessEntry{
		Kind:       model.CredentialAccessConnection,
		Resource:   "registry",
		Action:     action,
		Credential: secrets.HubRegistrationTokenSecret,
		Outcome:    outcome,
	}); err != nil {
		log.Printf("grain ui: auditing registry target change: %v", err)
	}
}

// GetHubSnapshot returns the deployment's current connected-entry
// registry state, heartbeat timings, queue counts, and advertised scopes.
func (c *Client) GetHubSnapshot(ctx context.Context) (model.HubSnapshot, error) {
	if c.Config.HubStatus != nil {
		snap, err := c.Config.HubStatus(ctx)
		if err != nil {
			return model.HubSnapshot{}, err
		}
		snap.LastError = c.redactHubText(snap.LastError)
		snap.HealthDetail = c.redactHubText(snap.HealthDetail)
		if snap.AdvertisedScopes == nil {
			snap.AdvertisedScopes = []model.HubAdvertisedScope{}
		}
		return snap, nil
	}
	return c.fallbackHubSnapshot(ctx)
}

func (c *Client) fallbackHubSnapshot(ctx context.Context) (model.HubSnapshot, error) {
	var (
		regCfg      model.HubConfig
		targetRepos []string
		repoConfigs []model.RepoConfig
		active      int
		awaiting    int
	)
	if c.Store != nil {
		cfg, err := c.Store.GetConfig(ctx)
		if err != nil {
			return model.HubSnapshot{}, err
		}
		if cfg != nil {
			regCfg = cfg.Hub
			targetRepos = cfg.TargetRepos
		}
		repoConfigs, err = c.Store.ListRepoConfigs(ctx)
		if err != nil {
			return model.HubSnapshot{}, err
		}
		active, awaiting, err = c.Store.HubTaskCounts(ctx)
		if err != nil {
			return model.HubSnapshot{}, err
		}
	}
	var stateRepo model.RepoRef
	if c.Config.StateRepoRef != nil {
		stateRepo = c.Config.StateRepoRef()
	}
	scopes := model.DeriveHubScopes(targetRepos, nil, stateRepo, repoConfigs, regCfg.Scopes)
	if scopes == nil {
		scopes = []model.HubAdvertisedScope{}
	}
	ver := version.Get()
	if c.Config.DaemonVersion != nil {
		ver = c.Config.DaemonVersion()
	}
	health := api.HealthOK
	var healthDetail string
	if c.Config.ReconcilerDown != nil && c.Config.ReconcilerDown() {
		health = api.HealthDegraded
		healthDetail = "reconcile loop is down"
	} else if c.Config.StateRepoSyncFailure != nil {
		if sf := c.Config.StateRepoSyncFailure(); sf != nil {
			health = api.HealthDegraded
			healthDetail = "state repository sync failing"
		}
	}
	tokenSet := c.hubTokenSet()
	authMode, _ := model.NormalizeHubAuthMode(regCfg.AuthMode)
	var credSource string
	switch authMode {
	case model.HubAuthOIDC:
		credSource = "oidc"
	default:
		if tokenSet {
			credSource = "secret:" + secrets.HubRegistrationTokenSecret
		}
	}
	state := model.HubStateDisabled
	var lastErr string
	if regCfg.Enabled() {
		if authMode == model.HubAuthToken && !tokenSet {
			state = model.HubStateError
			lastErr = "registration token secret " + secrets.HubRegistrationTokenSecret + " is not set"
		} else {
			state = model.HubStateConnecting
		}
	}
	hostKind := regCfg.HostKind
	if hostKind == "" {
		hostKind = api.HostKindWorkstation
	}
	return model.HubSnapshot{
		Enabled:            regCfg.Enabled(),
		State:              state,
		Config:             regCfg,
		EffectiveHostKind:  hostKind,
		TokenConfigured:    tokenSet,
		CredentialSource:   credSource,
		LastError:          lastErr,
		GrainVersion:       ver.Release,
		GrainCommit:        ver.Revision,
		Health:             health,
		HealthDetail:       healthDetail,
		ActiveTasks:        active,
		AwaitingReplyTasks: awaiting,
		AdvertisedScopes:   scopes,
	}, nil
}

// TriggerHubHeartbeat runs one immediate registration/heartbeat step
// when wired to a live HubManager and returns the resulting snapshot.
func (c *Client) TriggerHubHeartbeat(ctx context.Context) (model.HubSnapshot, error) {
	if c.Config.TriggerHubHeartbeat != nil {
		snap, err := c.Config.TriggerHubHeartbeat(ctx)
		if err != nil {
			if msg := err.Error(); msg != "" {
				if redacted := c.redactHubText(msg); redacted != msg {
					err = errors.New(redacted)
				}
			}
			return model.HubSnapshot{}, err
		}
		snap.LastError = c.redactHubText(snap.LastError)
		snap.HealthDetail = c.redactHubText(snap.HealthDetail)
		if snap.AdvertisedScopes == nil {
			snap.AdvertisedScopes = []model.HubAdvertisedScope{}
		}
		return snap, nil
	}
	return c.GetHubSnapshot(ctx)
}

// GetHubInstanceStatus answers GET /api/registry/status with the wire
// api.InstanceStatusResponse payload (design/design/hub.design.md).
func (c *Client) GetHubInstanceStatus(ctx context.Context) (api.InstanceStatusResponse, error) {
	snap, err := c.GetHubSnapshot(ctx)
	if err != nil {
		return api.InstanceStatusResponse{}, err
	}
	scopes := model.ToHeartbeatScopes(snap.AdvertisedScopes)
	if scopes == nil {
		scopes = []api.HeartbeatScope{}
	}
	health := snap.Health
	if health == "" {
		health = api.HealthOK
	}
	return api.InstanceStatusResponse{
		GrainVersion:       snap.GrainVersion,
		GrainCommit:        snap.GrainCommit,
		HostKind:           snap.EffectiveHostKind,
		Health:             health,
		HealthDetail:       snap.HealthDetail,
		ActiveTasks:        snap.ActiveTasks,
		AwaitingReplyTasks: snap.AwaitingReplyTasks,
		Scopes:             scopes,
	}, nil
}

func (c *Client) hubHealth(ctx context.Context) *HubHealth {
	if c == nil {
		return nil
	}
	if c.Config.HubStatus == nil && c.Store == nil {
		return nil
	}
	snap, err := c.GetHubSnapshot(ctx)
	if err != nil || !snap.Enabled {
		return nil
	}
	return &HubHealth{
		URL:                 snap.Config.URL,
		Entry:               snap.Config.Entry,
		State:               snap.State,
		LastHeartbeatAt:     snap.LastHeartbeatAt,
		ConsecutiveFailures: snap.ConsecutiveFailures,
		LastError:           snap.LastError,
		ScopeConflicts:      snap.ScopeConflicts,
	}
}

func (s *Server) handleGetHub(w http.ResponseWriter, r *http.Request) {
	snap, err := s.tasks.GetHubSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleTriggerHubHeartbeat(w http.ResponseWriter, r *http.Request) {
	snap, err := s.tasks.TriggerHubHeartbeat(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleGetHubInstanceStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.tasks.GetHubInstanceStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
