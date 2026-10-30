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
	"encoding/json"
	"reflect"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// clearBootstrap zeroes the bootstrap fields of the deployment's row:
// the Models and Sandboxes settings that stay writable from the UI in
// internal mode (docs/design/config-repo-settings.md, section 2.2), so
// what is left is the configuration only the config repository writes.
func clearBootstrap(c *model.Config) {
	// Models.
	c.AgentBackends = nil
	c.DefaultModel = ""
	c.AgentBackendID = ""
	c.ModelSettings = nil
	c.JudgementModel = ""
	// Sandboxes.
	c.MaxWorkers = 0
	c.SandboxBackend = ""
	c.SandboxCPUs = 0
	c.SandboxMemoryMB = 0
	c.SandboxDiskGB = 0
	c.SandboxCPUOvercommit = 0
	c.SandboxMemoryOvercommit = 0
	c.SandboxDiskOvercommit = 0
	c.SandboxMemoryReserveMB = 0
	c.SandboxDiskFloorShare = 0
	c.SandboxDiskFloorMinMB = 0
	c.SandboxOvercommit = model.OvercommitSettings{}
	c.NetworkAccess = ""
	c.SandboxImage = ""
	c.SandboxImageMirror = ""
	c.CLIRun = nil
	v := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		// granule_* and the other Kubernetes sandbox settings.
		if strings.HasPrefix(v.Type().Field(i).Name, "K8s") {
			v.Field(i).SetZero()
		}
	}
}

// bootstrapOnlyChange reports whether writing after over before changes
// nothing but bootstrap fields. A nil list and an empty one are the same
// configuration here, as they are once exported.
func bootstrapOnlyChange(before, after model.Config) bool {
	clearBootstrap(&before)
	clearBootstrap(&after)
	a, errA := comparableConfig(before)
	b, errB := comparableConfig(after)
	if errA != nil || errB != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func comparableConfig(c model.Config) (any, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return pruneEmpty(v), nil
}

// pruneEmpty drops the null, empty and zero values of a decoded JSON
// document, so a value left out and a value given as its zero compare
// equal.
func pruneEmpty(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			x = pruneEmpty(x)
			if x == nil {
				delete(t, k)
				continue
			}
			t[k] = x
		}
		if len(t) == 0 {
			return nil
		}
		return t
	case []any:
		if len(t) == 0 {
			return nil
		}
		for i := range t {
			t[i] = pruneEmpty(t[i])
		}
		return t
	case string:
		if t == "" {
			return nil
		}
	case bool:
		if !t {
			return nil
		}
	case float64:
		if t == 0 {
			return nil
		}
	}
	return v
}

// cloneConfig is a copy of c that shares no slice or map with it, for
// working out what a write would change before making it.
func cloneConfig(c model.Config) model.Config {
	raw, err := json.Marshal(c)
	if err != nil {
		return c
	}
	var out model.Config
	if err := json.Unmarshal(raw, &out); err != nil {
		return c
	}
	return out
}
