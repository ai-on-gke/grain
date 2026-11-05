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
	"net/http"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/version"
)

// RegistryHealth summarizes the deployment's connected-entry registry
// heartbeat state on GET /api/sandboxes and `grain health`. Absent (nil)
// when registry self-registration is not configured.
type RegistryHealth struct {
	URL                 string                        `json:"url"`
	Entry               string                        `json:"entry"`
	State               model.RegistryConnectionState `json:"state"`
	LastHeartbeatAt     *time.Time                    `json:"lastHeartbeatAt,omitempty"`
	ConsecutiveFailures int                           `json:"consecutiveFailures,omitempty"`
	LastError           string                        `json:"lastError,omitempty"`
	ScopeConflicts      []api.ScopeConflict           `json:"scopeConflicts,omitempty"`
}

func (c *Client) registryTokenSet() bool {
	if c == nil || c.Config.Secrets == nil {
		return false
	}
	ok, err := c.Config.Secrets.Has(secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey)
	return err == nil && ok
}

func (c *Client) triggerRegistrySync() {
	if c != nil && c.Config.TriggerRegistrySync != nil {
		c.Config.TriggerRegistrySync()
	}
}

func (c *Client) redactRegistryError(line string) string {
	if line == "" || c == nil || c.Config.LogRedactor == nil {
		return line
	}
	redactor, err := c.Config.LogRedactor()
	if err != nil || redactor == nil {
		return "(withheld: the redaction list could not be read)"
	}
	redacted, _ := redactor.Line(line)
	return redacted
}

// GetRegistrySnapshot returns the deployment's current connected-entry
// registry state, heartbeat timings, queue counts, and advertised scopes.
func (c *Client) GetRegistrySnapshot(ctx context.Context) (model.RegistrySnapshot, error) {
	if c.Config.RegistryStatus != nil {
		snap, err := c.Config.RegistryStatus(ctx)
		if err != nil {
			return model.RegistrySnapshot{}, err
		}
		snap.LastError = c.redactRegistryError(snap.LastError)
		if snap.AdvertisedScopes == nil {
			snap.AdvertisedScopes = []model.RegistryAdvertisedScope{}
		}
		return snap, nil
	}
	return c.fallbackRegistrySnapshot(ctx)
}

func (c *Client) fallbackRegistrySnapshot(ctx context.Context) (model.RegistrySnapshot, error) {
	var (
		regCfg      model.RegistryConfig
		targetRepos []string
		repoConfigs []model.RepoConfig
		active      int
		awaiting    int
	)
	if c.Store != nil {
		cfg, err := c.Store.GetConfig(ctx)
		if err != nil {
			return model.RegistrySnapshot{}, err
		}
		if cfg != nil {
			regCfg = cfg.Registry
			targetRepos = cfg.TargetRepos
		}
		repoConfigs, err = c.Store.ListRepoConfigs(ctx)
		if err != nil {
			return model.RegistrySnapshot{}, err
		}
		active, awaiting, err = c.Store.RegistryTaskCounts(ctx)
		if err != nil {
			return model.RegistrySnapshot{}, err
		}
	}
	var stateRepo model.RepoRef
	if c.Config.StateRepoRef != nil {
		stateRepo = c.Config.StateRepoRef()
	}
	scopes := model.DeriveRegistryScopes(targetRepos, nil, stateRepo, repoConfigs, regCfg.Scopes)
	if scopes == nil {
		scopes = []model.RegistryAdvertisedScope{}
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
	tokenSet := c.registryTokenSet()
	authMode, _ := model.NormalizeRegistryAuthMode(regCfg.AuthMode)
	var credSource string
	switch authMode {
	case model.RegistryAuthOIDC:
		credSource = "oidc"
	default:
		if tokenSet {
			credSource = "secret:" + secrets.RegistryRegistrationTokenSecret
		}
	}
	state := model.RegistryStateDisabled
	var lastErr string
	if regCfg.Enabled() {
		if authMode == model.RegistryAuthToken && !tokenSet {
			state = model.RegistryStateError
			lastErr = "registration token secret " + secrets.RegistryRegistrationTokenSecret + " is not set"
		} else {
			state = model.RegistryStateConnecting
		}
	}
	hostKind := regCfg.HostKind
	if hostKind == "" {
		hostKind = api.HostKindWorkstation
	}
	return model.RegistrySnapshot{
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

// TriggerRegistryHeartbeat runs one immediate registration/heartbeat step
// when wired to a live RegistryManager and returns the resulting snapshot.
func (c *Client) TriggerRegistryHeartbeat(ctx context.Context) (model.RegistrySnapshot, error) {
	if c.Config.TriggerRegistryHeartbeat != nil {
		snap, err := c.Config.TriggerRegistryHeartbeat(ctx)
		if err != nil {
			return model.RegistrySnapshot{}, err
		}
		snap.LastError = c.redactRegistryError(snap.LastError)
		if snap.AdvertisedScopes == nil {
			snap.AdvertisedScopes = []model.RegistryAdvertisedScope{}
		}
		return snap, nil
	}
	return c.GetRegistrySnapshot(ctx)
}

// GetRegistryInstanceStatus answers GET /api/registry/status with the wire
// api.InstanceStatus payload (design/design/registry.design.md).
func (c *Client) GetRegistryInstanceStatus(ctx context.Context) (api.InstanceStatus, error) {
	snap, err := c.GetRegistrySnapshot(ctx)
	if err != nil {
		return api.InstanceStatus{}, err
	}
	scopes := model.ToAPIScopes(snap.AdvertisedScopes)
	if scopes == nil {
		scopes = []api.Scope{}
	}
	health := snap.Health
	if health == "" {
		health = api.HealthOK
	}
	return api.InstanceStatus{
		GrainVersion:       snap.GrainVersion,
		GrainCommit:        snap.GrainCommit,
		Health:             health,
		HealthDetail:       snap.HealthDetail,
		ActiveTasks:        snap.ActiveTasks,
		AwaitingReplyTasks: snap.AwaitingReplyTasks,
		Scopes:             scopes,
	}, nil
}

func (c *Client) registryHealth(ctx context.Context) *RegistryHealth {
	if c == nil {
		return nil
	}
	if c.Config.RegistryStatus == nil && c.Store == nil {
		return nil
	}
	snap, err := c.GetRegistrySnapshot(ctx)
	if err != nil || !snap.Enabled {
		return nil
	}
	return &RegistryHealth{
		URL:                 snap.Config.URL,
		Entry:               snap.Config.Entry,
		State:               snap.State,
		LastHeartbeatAt:     snap.LastHeartbeatAt,
		ConsecutiveFailures: snap.ConsecutiveFailures,
		LastError:           snap.LastError,
		ScopeConflicts:      snap.ScopeConflicts,
	}
}

func (s *Server) handleGetRegistry(w http.ResponseWriter, r *http.Request) {
	snap, err := s.tasks.GetRegistrySnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleTriggerRegistryHeartbeat(w http.ResponseWriter, r *http.Request) {
	snap, err := s.tasks.TriggerRegistryHeartbeat(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleGetRegistryInstanceStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.tasks.GetRegistryInstanceStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
