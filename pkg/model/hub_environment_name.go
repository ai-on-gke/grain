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

package model

import (
	"context"
	"strings"

	"github.com/ai-on-gke/grain/pkg/hub/api"
)

// AdoptHubEnvironmentName applies name, an environment name a Grain Hub
// supplied as this deployment's default, to c, and reports whether c
// changed.
//
// It is a default only. EnvironmentName takes name when it is empty or
// still the hub's own previous name (HubEnvironmentName), so renaming the
// hub entry renames the instance; a name the operator set -- in Settings,
// or in the state repository's grain_config -- is anything else, and is
// kept. name is sanitized to what Settings would accept rather than
// refused, and an empty name changes nothing.
func (c *Config) AdoptHubEnvironmentName(name string) bool {
	name = api.SanitizeEnvironmentName(name)
	if name == "" {
		return false
	}
	current := strings.TrimSpace(c.EnvironmentName)
	if current != "" && current != strings.TrimSpace(c.HubEnvironmentName) {
		// The operator's own name: leave it be.
		return false
	}
	if current == name && c.HubEnvironmentName == name {
		return false
	}
	c.EnvironmentName = name
	c.HubEnvironmentName = name
	return true
}

// AdoptHubEnvironmentName applies a hub-supplied default environment name
// to the stored config (Config.AdoptHubEnvironmentName), writing only when
// it changes something, and reports whether it did.
func (s *Store) AdoptHubEnvironmentName(ctx context.Context, name string) (bool, error) {
	if api.SanitizeEnvironmentName(name) == "" {
		return false, nil
	}
	cur, err := s.GetConfig(ctx)
	if err != nil {
		return false, err
	}
	if cur == nil {
		cur = &Config{}
	}
	if probe := *cur; !probe.AdoptHubEnvironmentName(name) {
		return false, nil
	}
	changed := false
	err = s.UpdateConfig(ctx, func(c *Config) error {
		changed = c.AdoptHubEnvironmentName(name)
		return nil
	})
	return changed, err
}
