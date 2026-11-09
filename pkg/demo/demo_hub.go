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

package demo

import (
	"context"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

func configureRegistryDemo(storedCfg *model.Config, store *model.Store, cfg *ui.Config) {
	storedCfg.Registry = model.RegistryConfig{
		URL:         "https://registry.grain.example.com",
		Entry:       "acme-widgets",
		AuthMode:    model.RegistryAuthToken,
		DisplayName: "Acme Widgets Grain",
		Purpose:     "Primary grain controller for acme/widgets engineering tasks and CI",
		HostKind:    api.HostKindWorkstation,
		Listed:      true,
	}

	var mu sync.Mutex
	lastHeartbeat := time.Now().UTC().Add(-4 * time.Second)

	snapshot := func(ctx context.Context) (model.RegistrySnapshot, error) {
		mc, err := store.GetConfig(ctx)
		if err != nil || mc == nil || !mc.Registry.Enabled() {
			return model.RegistrySnapshot{State: model.RegistryStateDisabled}, err
		}
		repoCfgs, _ := store.ListRepoConfigs(ctx)
		scopes := model.DeriveRegistryScopes(mc.TargetRepos, nil, model.RepoRef{}, repoCfgs, mc.Registry.Scopes)
		active, awaiting, _ := store.RegistryTaskCounts(ctx)
		v := version.Get()

		mu.Lock()
		hb := lastHeartbeat
		mu.Unlock()
		next := hb.Add(15 * time.Second)
		hk := mc.Registry.HostKind
		if hk == "" {
			hk = api.HostKindWorkstation
		}

		return model.RegistrySnapshot{
			Enabled:                  true,
			State:                    model.RegistryStateConnected,
			Config:                   mc.Registry,
			EffectiveHostKind:        hk,
			TokenConfigured:          true,
			CredentialSource:         "secret",
			RegistryStatus:           api.StatusOnline,
			Registered:               true,
			LastHeartbeatAt:          &hb,
			LastAttemptAt:            &hb,
			NextHeartbeatAt:          &next,
			HeartbeatIntervalSeconds: 15,
			GrainVersion:             v.Release,
			GrainCommit:              v.Revision,
			Health:                   api.HealthOK,
			ActiveTasks:              active,
			AwaitingReplyTasks:       awaiting,
			AdvertisedScopes:         scopes,
		}, nil
	}

	cfg.RegistryStatus = snapshot
	cfg.TriggerRegistryHeartbeat = func(ctx context.Context) (model.RegistrySnapshot, error) {
		mu.Lock()
		lastHeartbeat = time.Now().UTC()
		mu.Unlock()
		return snapshot(ctx)
	}
}
