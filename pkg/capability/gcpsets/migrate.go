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

package gcpsets

import "github.com/ai-on-gke/grain/pkg/capability/gcpkey"

// LegacyAccount is one -account of the gcp server before permission sets:
// a fixed pool of Count agent accounts named after Email.
type LegacyAccount struct {
	Name  string
	Email string
	Count int
}

// LegacyAdmin is the admin migration makes of the old minter key: a
// configured admin, since the old key was a secret in grain's store.
const LegacyAdmin = "minter"

// Migrate turns the old flags-and-environment configuration -- a project,
// fixed pools of agent accounts and the minter key -- into permission
// sets. Each pool becomes a set over exactly those accounts (accounts:),
// which the server mints keys for and never changes. The pool called
// "default" (or nothing) becomes DefaultSet; the others keep their names.
// The first set is what a grant with no arguments gets, as it got the
// first pool before. The minter key becomes the configured admin LegacyAdmin, whose
// role ceiling is empty because a migrated set binds nothing.
//
// The result is a Config, not a configure document: the minter key is
// still the secret it was, and nothing about it is written anywhere.
func Migrate(project string, accounts []LegacyAccount, minterKey string) (*Config, error) {
	c := &Config{Admins: []Admin{{
		Name:             LegacyAdmin,
		CredentialSource: SourceConfigured,
		Credential:       minterKey,
		Projects:         []string{project},
	}}}
	for _, a := range accounts {
		name := a.Name
		if name == "" || name == "default" {
			name = DefaultSet
		}
		c.Sets = append(c.Sets, PermissionSet{
			Name:     name,
			Admin:    LegacyAdmin,
			Project:  project,
			Accounts: gcpkey.PoolEmails(a.Email, a.Count, project),
			MaxLease: gcpkey.DefaultMaxKeyAge,
			// The pool's accounts are its own whatever they are called;
			// adopting is what a migrated set does.
			AdoptLegacy: true,
		})
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}
