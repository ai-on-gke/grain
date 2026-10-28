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
	"errors"
	"fmt"
	"strings"
	"time"
)

// GCPKey is one of the deployment's named GCP keys: the project, agent
// service account(s) and minter secret that capability gcp-key:<Name>
// mints with. It is the deployment's, not a team's (single-tenant reset
// stage 4e), and its rows are gcp_key's.
type GCPKey struct {
	Name                string        `json:"name"`
	ProjectID           string        `json:"project_id"`
	ServiceAccountEmail string        `json:"service_account_email"`
	ServiceAccountCount int           `json:"service_account_count"`
	MinterCredential    string        `json:"minter_credential"`
	MaxKeyAge           time.Duration `json:"max_key_age,omitempty"`
	Description         string        `json:"description,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
	UpdatedAt           time.Time     `json:"updated_at"`
}

// CapabilityName returns the capability ID corresponding to this key (e.g. "gcp-key:deploy").
func (k GCPKey) CapabilityName() string {
	return GCPKeyCapability(k.Name)
}

// Label returns the trigger label corresponding to this key (e.g. "grain-gcp-key-deploy").
func (k GCPKey) Label() string {
	if k.Name == "" || k.Name == "default" {
		return "grain-gcp-key"
	}
	return "grain-gcp-key-" + k.Name
}

// GCPKeyMinterCredential is the minter secret a new GCP key mints through
// when nobody names one: gcp-key-minter-<name>, the name a row with no
// minter already fell back to (EffectiveMinterCredential).
func GCPKeyMinterCredential(name string) string {
	return "gcp-key-minter-" + name
}

// EffectiveMinterCredential is the secret this key mints through: its
// MinterCredential, or for a row saved with none the name gcpkey.Provider
// falls back to (gcp-key-minter-<name>, or the deployment's own
// gcp-key-minter for the key named "default").
func (k GCPKey) EffectiveMinterCredential() string {
	if c := strings.TrimSpace(k.MinterCredential); c != "" {
		return c
	}
	if k.Name != "" && k.Name != "default" {
		return GCPKeyMinterCredential(k.Name)
	}
	return "gcp-key-minter"
}

// Validate checks that the GCP key configuration has required fields.
func (k GCPKey) Validate() error {
	if strings.TrimSpace(k.Name) == "" {
		return errors.New("key name is required")
	}
	if strings.ContainsAny(k.Name, " \t\n\r/\\:") {
		return fmt.Errorf("key name %q contains invalid characters", k.Name)
	}
	// gcp-key:default is the capability name gcp-key, which always resolves
	// to the deployment's own pool provider, so a key by that name could
	// never be granted.
	if strings.EqualFold(strings.TrimSpace(k.Name), "default") {
		return fmt.Errorf("key name %q is reserved for the deployment's own gcp-key pool; choose another name", k.Name)
	}
	if strings.TrimSpace(k.ProjectID) == "" {
		return errors.New("project ID is required")
	}
	if strings.TrimSpace(k.ServiceAccountEmail) == "" {
		return errors.New("service account email is required")
	}
	return nil
}
